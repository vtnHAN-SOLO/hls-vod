package service

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base32"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/vtnHAN-SOLO/hls-vod/internal/auth"
	pmail "github.com/vtnHAN-SOLO/hls-vod/internal/mail"
	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
)

// Sign in with Google: OpenID Connect, authorization code flow with PKCE,
// scopes openid and email.
//
// A Google account is identified only by its subject ("sub"), never by its
// email; local emails stay as registered. The browser carries the state in
// an HttpOnly cookie and in the URL: the callback needs both to match, and
// a stored, unexpired, unused row, so a callback cannot be replayed or
// started from another browser (login CSRF). Nonce and PKCE verifier never
// leave the api. No code or token is ever logged.

// GoogleIssuer is Google's OpenID issuer.
const GoogleIssuer = "https://accounts.google.com"

const (
	// OAuthStateTTL is how long a started sign-in may take.
	OAuthStateTTL = 10 * time.Minute
	// GoogleTimeout bounds each call to Google (discovery, keys, token).
	GoogleTimeout = 10 * time.Second
)

// Codes of a failed Google sign-in or link: what the SPA is told, in the
// redirect's google_error parameter. Never more detail than this.
const (
	GoogleErrState              = "state"               // missing, mismatched, expired or used state
	GoogleErrDenied             = "denied"              // the user cancelled at Google
	GoogleErrUnavailable        = "unavailable"         // Google could not be reached
	GoogleErrExchange           = "exchange"            // the code was refused
	GoogleErrToken              = "token"               // the ID token did not verify
	GoogleErrEmailUnverified    = "email_unverified"    // Google has not verified the address
	GoogleErrRegistrationClosed = "registration_closed" // a new account would be needed
	GoogleErrLinkedElsewhere    = "linked_elsewhere"    // that Google account belongs to another user
	GoogleErrAlreadyLinked      = "already_linked"      // the account has another Google account
	GoogleErrAccountDisabled    = "account_disabled"    // an admin disabled the account
	GoogleErrServer             = "server"              // anything else (logged)
)

// GoogleError is a failed callback: Code for the SPA, Intent to know which
// page to send it to.
type GoogleError struct {
	Code   string
	Intent string // store.IntentLogin or store.IntentLink
	err    error
}

func (e *GoogleError) Error() string {
	if e.err != nil {
		return fmt.Sprintf("google sign-in (%s): %v", e.Code, e.err)
	}
	return "google sign-in: " + e.Code
}

func (e *GoogleError) Unwrap() error { return e.err }

// GoogleStore is the slice of the store Google sign-in needs.
type GoogleStore interface {
	CreateOAuthState(ctx context.Context, stateHash []byte, st store.OAuthState, ttl time.Duration) error
	ConsumeOAuthState(ctx context.Context, stateHash []byte) (store.OAuthState, error)
	GoogleSignIn(ctx context.Context, in store.GoogleSignIn) (store.GoogleResult, error)
	LinkGoogle(ctx context.Context, userID, subject, email string) error
}

// GoogleConfig is how Google sign-in is deployed.
type GoogleConfig struct {
	ClientID, ClientSecret string
	// Issuer is GoogleIssuer; tests pass a fake provider.
	Issuer string
	// RedirectURL is {PUBLIC_BASE_URL}/api/v1/auth/google/callback.
	RedirectURL string
	// BaseURL is the SPA's origin, for the link in the notice mail.
	BaseURL string
	// RegistrationOpen: a sign-in that needs a new account is refused when false.
	RegistrationOpen bool
}

// Google runs sign in with Google and linking.
type Google struct {
	store  GoogleStore
	auth   *Auth
	mailer Mailer
	cfg    GoogleConfig
	log    *slog.Logger

	client *http.Client
	base   context.Context // for discovery and key fetches, never cancelled

	mu       sync.Mutex
	provider *oidc.Provider // nil until the first successful discovery
}

// NewGoogle returns the Google sign-in service. Nothing is fetched yet:
// discovery runs on first use, so a Google outage never stops the api from
// starting. ctx carries values only; its cancellation is ignored.
func NewGoogle(ctx context.Context, st GoogleStore, a *Auth, mailer Mailer, cfg GoogleConfig, log *slog.Logger) *Google {
	if cfg.Issuer == "" {
		cfg.Issuer = GoogleIssuer
	}
	client := &http.Client{Timeout: GoogleTimeout}
	return &Google{
		store: st, auth: a, mailer: mailer, cfg: cfg, log: log,
		client: client, base: oidc.ClientContext(context.WithoutCancel(ctx), client),
	}
}

// discover returns the provider, fetching Google's discovery document the
// first time (a failure is not cached).
func (g *Google) discover(ctx context.Context) (*oidc.Provider, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.provider != nil {
		return g.provider, nil
	}
	ctx, cancel := context.WithTimeout(ctx, GoogleTimeout)
	defer cancel()
	// The provider keeps g.base for its later key fetches; this call is
	// bounded by ctx.
	p, err := oidc.NewProvider(oidc.ClientContext(ctx, g.client), g.cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("discovering %s: %w", g.cfg.Issuer, err)
	}
	g.provider = p
	return p, nil
}

func (g *Google) oauth(p *oidc.Provider) *oauth2.Config {
	return &oauth2.Config{
		ClientID: g.cfg.ClientID, ClientSecret: g.cfg.ClientSecret,
		Endpoint: p.Endpoint(), RedirectURL: g.cfg.RedirectURL,
		Scopes: []string{oidc.ScopeOpenID, "email"},
	}
}

// StartLogin begins a sign-in: the URL to send the browser to, and the state
// to put in its cookie.
func (g *Google) StartLogin(ctx context.Context) (authURL, state string, err error) {
	return g.start(ctx, store.IntentLogin, nil)
}

// StartLink begins linking Google to the caller's account. It needs the
// current password: otherwise a stolen session could link the attacker's
// Google account as a permanent way in. In order: the session must be live,
// the per-user wrong-password throttle (shared with ChangePassword), then
// the password.
func (g *Google) StartLink(ctx context.Context, p Principal, currentPassword string) (authURL, state string, err error) {
	if err := g.auth.requireLiveSession(ctx, p); err != nil {
		return "", "", err
	}
	attempt, retry, ok := g.auth.pwThrottle.Allow(p.UserID)
	if !ok {
		return "", "", &ThrottledError{RetryAfter: retry}
	}
	defer attempt.Release()
	if currentPassword == "" {
		return "", "", &ValidationError{"current_password", "is required"}
	}
	u, err := g.auth.Me(ctx, p)
	if err != nil {
		return "", "", err
	}
	if err := g.auth.checkCurrentPassword(ctx, u, currentPassword, attempt); err != nil {
		return "", "", err
	}
	return g.start(ctx, store.IntentLink, &u.ID)
}

func (g *Google) start(ctx context.Context, intent string, userID *string) (string, string, error) {
	p, err := g.discover(ctx)
	if err != nil {
		return "", "", err
	}
	state, stateHash, err := auth.NewOpaqueToken()
	if err != nil {
		return "", "", err
	}
	nonce, _, err := auth.NewOpaqueToken()
	if err != nil {
		return "", "", err
	}
	verifier := oauth2.GenerateVerifier()
	if err := g.store.CreateOAuthState(ctx, stateHash,
		store.OAuthState{Nonce: nonce, Verifier: verifier, Intent: intent, UserID: userID}, OAuthStateTTL); err != nil {
		return "", "", err
	}
	return g.oauth(p).AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), state, nil
}

// CallbackInput is what Google's redirect back brought.
type CallbackInput struct {
	State       string // the state query parameter
	CookieState string // the state cookie, "" when missing
	Code        string
	// ProviderError is Google's error parameter (access_denied when the
	// user cancelled).
	ProviderError string
	UserAgent     string
}

// CallbackResult is a successful callback: for a login, the new session.
type CallbackResult struct {
	Intent  string
	Session Session // login only
}

// Callback finishes a sign-in or link. Every failure is a *GoogleError.
func (g *Google) Callback(ctx context.Context, in CallbackInput) (CallbackResult, error) {
	fail := func(code, intent string, err error) (CallbackResult, error) {
		return CallbackResult{}, &GoogleError{Code: code, Intent: intent, err: err}
	}
	if in.State == "" || in.CookieState == "" || subtle.ConstantTimeCompare([]byte(in.State), []byte(in.CookieState)) != 1 {
		return fail(GoogleErrState, store.IntentLogin, errors.New("state parameter and cookie differ or are missing"))
	}
	stateHash, ok := auth.HashOpaqueToken(in.State)
	if !ok {
		return fail(GoogleErrState, store.IntentLogin, errors.New("malformed state"))
	}
	st, err := g.store.ConsumeOAuthState(ctx, stateHash)
	if errors.Is(err, store.ErrOAuthStateInvalid) {
		return fail(GoogleErrState, store.IntentLogin, err)
	}
	if err != nil {
		return fail(GoogleErrServer, store.IntentLogin, err)
	}
	if in.ProviderError != "" || in.Code == "" {
		return fail(GoogleErrDenied, st.Intent, fmt.Errorf("google answered error %q", in.ProviderError))
	}
	id, err := g.verify(ctx, in.Code, st)
	if err != nil {
		var ge *GoogleError
		if errors.As(err, &ge) {
			ge.Intent = st.Intent
		}
		return CallbackResult{}, err
	}
	if st.Intent == store.IntentLink && st.UserID != nil {
		return g.link(ctx, *st.UserID, id)
	}
	return g.login(ctx, id, in.UserAgent)
}

// googleID is a verified Google identity.
type googleID struct{ subject, email string }

// verify exchanges the code (with the PKCE verifier) and verifies the ID
// token: signature against Google's keys, iss, aud = our client id, exp,
// nonce, and a verified email.
func (g *Google) verify(ctx context.Context, code string, st store.OAuthState) (googleID, error) {
	fail := func(c string, err error) (googleID, error) { return googleID{}, &GoogleError{Code: c, err: err} }
	p, err := g.discover(ctx)
	if err != nil {
		return fail(GoogleErrUnavailable, err)
	}
	ctx, cancel := context.WithTimeout(oidc.ClientContext(ctx, g.client), GoogleTimeout)
	defer cancel()
	tok, err := g.oauth(p).Exchange(ctx, code, oauth2.VerifierOption(st.Verifier))
	if err != nil {
		var re *oauth2.RetrieveError
		if errors.As(err, &re) {
			// Only the status and error code: the body may echo the code.
			return fail(GoogleErrExchange, fmt.Errorf("token endpoint answered %d %s", re.Response.StatusCode, re.ErrorCode))
		}
		return fail(GoogleErrUnavailable, errors.New("token endpoint unreachable"))
	}
	raw, _ := tok.Extra("id_token").(string)
	if raw == "" {
		return fail(GoogleErrToken, errors.New("no id_token in the token response"))
	}
	idt, err := p.Verifier(&oidc.Config{ClientID: g.cfg.ClientID}).Verify(ctx, raw)
	if err != nil {
		return fail(GoogleErrToken, err)
	}
	if subtle.ConstantTimeCompare([]byte(idt.Nonce), []byte(st.Nonce)) != 1 {
		return fail(GoogleErrToken, errors.New("nonce mismatch"))
	}
	var claims struct {
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
	}
	if err := idt.Claims(&claims); err != nil {
		return fail(GoogleErrToken, err)
	}
	if !claims.EmailVerified {
		return fail(GoogleErrEmailUnverified, errors.New("email_verified is false"))
	}
	email, err := NormalizeEmail(claims.Email)
	if err != nil || idt.Subject == "" {
		return fail(GoogleErrToken, errors.New("no usable email or subject in the ID token"))
	}
	return googleID{subject: idt.Subject, email: email}, nil
}

func (g *Google) login(ctx context.Context, id googleID, userAgent string) (CallbackResult, error) {
	fail := func(code string, err error) (CallbackResult, error) {
		return CallbackResult{}, &GoogleError{Code: code, Intent: store.IntentLogin, err: err}
	}
	refresh, refreshHash, err := auth.NewRefreshToken()
	if err != nil {
		return fail(GoogleErrServer, err)
	}
	r, err := g.store.GoogleSignIn(ctx, store.GoogleSignIn{
		Subject: id.subject, Email: id.email, AllowCreate: g.cfg.RegistrationOpen,
		NextUsername: func() string { return GoogleUsername(id.email) },
		Refresh:      store.NewRefresh{Hash: refreshHash, TTL: auth.RefreshTTL, UserAgent: cleanUserAgent(userAgent)},
	})
	switch {
	case errors.Is(err, store.ErrSignupClosed):
		return fail(GoogleErrRegistrationClosed, err)
	case errors.Is(err, store.ErrGoogleAlreadyLinked):
		return fail(GoogleErrAlreadyLinked, err)
	case errors.Is(err, store.ErrAccountDisabled):
		return fail(GoogleErrAccountDisabled, err)
	case err != nil:
		return fail(GoogleErrServer, err)
	}
	if r.DeletedUnverified {
		g.log.InfoContext(ctx, "Google sign-in: an unverified account held the address and was deleted")
	}
	g.log.InfoContext(ctx, "Google sign-in", "user_id", r.User.ID, "outcome", r.Outcome.String())
	if r.Outcome == store.GoogleLinkedByEmail && r.User.Email != nil {
		sendMail(ctx, g.mailer, g.log, pmail.GoogleLinkedMessage(*r.User.Email, r.User.Username, g.cfg.BaseURL+"/account"))
	}
	s, err := g.auth.session(r.User, r.Family, refresh)
	if err != nil {
		return fail(GoogleErrServer, err)
	}
	return CallbackResult{Intent: store.IntentLogin, Session: s}, nil
}

func (g *Google) link(ctx context.Context, userID string, id googleID) (CallbackResult, error) {
	fail := func(code string, err error) (CallbackResult, error) {
		return CallbackResult{}, &GoogleError{Code: code, Intent: store.IntentLink, err: err}
	}
	switch err := g.store.LinkGoogle(ctx, userID, id.subject, id.email); {
	case errors.Is(err, store.ErrGoogleTaken):
		return fail(GoogleErrLinkedElsewhere, err)
	case errors.Is(err, store.ErrGoogleAlreadyLinked):
		return fail(GoogleErrAlreadyLinked, err)
	case errors.Is(err, store.ErrAccountDisabled):
		return fail(GoogleErrAccountDisabled, err)
	case err != nil:
		return fail(GoogleErrServer, err)
	}
	g.log.InfoContext(ctx, "Google account linked", "user_id", userID)
	if u, err := g.auth.store.UserByID(ctx, userID); err == nil && u.Email != nil {
		sendMail(ctx, g.mailer, g.log, pmail.GoogleLinkedMessage(*u.Email, u.Username, g.cfg.BaseURL+"/account"))
	}
	return CallbackResult{Intent: store.IntentLink}, nil
}

// usernameSuffix is the random part of a generated username: 4 characters
// of lowercase base32 (a-z, 2-7), so a collision is rare and retried.
var usernameSuffix = base32.StdEncoding.WithPadding(base32.NoPadding)

// GoogleUsername proposes a username for a new Google account: the email's
// local part reduced to the username rules (a-z, 0-9, '.', '_', '-',
// starting with a letter or digit, at most 50 characters here; "user" when
// nothing is left), a '-' and a random suffix. Usernames are public, so
// only the local part is used, never the domain.
func GoogleUsername(email string) string {
	local, _, _ := strings.Cut(strings.ToLower(email), "@")
	var b strings.Builder
	for _, r := range local {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
		}
	}
	base := strings.TrimLeft(b.String(), "._-")
	if len(base) > 50 {
		base = base[:50]
	}
	if base == "" {
		base = "user"
	}
	raw := make([]byte, 3)
	_, _ = rand.Read(raw) // crypto/rand never fails on supported platforms
	return base + "-" + strings.ToLower(usernameSuffix.EncodeToString(raw)[:4])
}
