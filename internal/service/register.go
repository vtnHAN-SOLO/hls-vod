package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"strings"
	"time"

	"github.com/vtnHAN-SOLO/hls-vod/internal/auth"
	pmail "github.com/vtnHAN-SOLO/hls-vod/internal/mail"
	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
)

// Open registration with email verification.
//
// No email enumeration: register and resend give the same answer (nil, so
// a 202) whether or not the address has an account. Register does the same
// work either way (validation, the challenge, an argon2id hash, a token, one
// transaction), and every mail goes out in the background, so neither the
// answer nor its timing tells. An address that already has an account gets
// a "someone tried to sign up with your email" mail instead of a link.
//
// Usernames are public (they are shown to other users later, and taken
// ones must be refused somehow): a taken username is ErrUsernameTaken, a
// 409. It is checked before the email, so the 409 never depends on
// whether the address is registered.

var (
	// ErrRegistrationClosed means REGISTRATION_OPEN=false.
	ErrRegistrationClosed = errors.New("registration is closed")
	// ErrUsernameTaken means another account has the username.
	ErrUsernameTaken = errors.New("username is taken")
	// ErrInvalidEmailToken means a mailed link's token is unknown, expired
	// or already used.
	ErrInvalidEmailToken = errors.New("the link is invalid, expired or already used")
)

// MaxEmailLen is the longest address accepted (RFC 5321's path limit).
const MaxEmailLen = 254

// Per-IP limit on register, on resend and on forgot-password (each its
// own key): this many attempts that pass validation per hour.
const (
	RegisterPerIPPerHour = 10
	registerWindow       = time.Hour
)

// RegistrationStore is the slice of the store registration needs.
type RegistrationStore interface {
	Register(ctx context.Context, a store.NewAccount) (store.Registration, error)
	ResendVerification(ctx context.Context, email string, hash []byte, verifyTTL time.Duration) (model.User, store.ResendOutcome, error)
	VerifyEmail(ctx context.Context, hash []byte, refresh store.NewRefresh) (model.User, string, error)

	// Forgot / reset password (recovery.go).
	IssueResetToken(ctx context.Context, email string, hash []byte, ttl time.Duration) (model.User, store.ResetOutcome, error)
	EmailTokenUsable(ctx context.Context, hash []byte, purpose string) (bool, error)
	ResetPassword(ctx context.Context, hash []byte, passwordHash string) (model.User, error)
	IssueResetTokenForUser(ctx context.Context, userID string, hash []byte, ttl time.Duration) error
}

// Mailer queues a mail for background delivery. *mail.Outbox satisfies it.
type Mailer interface {
	Enqueue(m pmail.Message)
}

// RegistrationConfig is how registration is deployed.
type RegistrationConfig struct {
	Open bool
	// BaseURL is the SPA's origin, without a trailing slash, for the links
	// in mails.
	BaseURL string
	// TurnstileSiteKey is public: the browser widget needs it. "" when
	// Turnstile is not configured.
	TurnstileSiteKey string
	// GoogleEnabled: sign in with Google is configured.
	GoogleEnabled bool
	// SiteName names the product in mails (SITE_NAME); "" is the default name.
	SiteName string
}

// AuthConfig is what the login and register pages need to know.
type AuthConfig struct {
	RegistrationOpen bool
	TurnstileSiteKey string // "" when not configured
	GoogleEnabled    bool
}

// Registration runs register, verify-email and resend-verification, and
// forgot / reset password (recovery.go), the other flow that mails a link.
type Registration struct {
	store   RegistrationStore
	pw      Passwords
	captcha Captcha // nil: not configured, so register and resend are refused
	mailer  Mailer  // nil: mail not configured, mails are skipped (logged)
	auth    *Auth
	ipLimit *auth.Throttle
	cfg     RegistrationConfig
	log     *slog.Logger
}

// NewRegistration returns the registration service. captcha and mailer may
// be nil (see Registration); a issues the session a verified link starts.
func NewRegistration(st RegistrationStore, pw Passwords, captcha Captcha, mailer Mailer, a *Auth,
	cfg RegistrationConfig, log *slog.Logger) *Registration {
	return &Registration{
		store: st, pw: pw, captcha: captcha, mailer: mailer, auth: a,
		ipLimit: auth.NewLimiter(RegisterPerIPPerHour, registerWindow),
		cfg:     cfg, log: log,
	}
}

// Config is the public part of the configuration.
func (r *Registration) Config() AuthConfig {
	return AuthConfig{RegistrationOpen: r.cfg.Open, TurnstileSiteKey: r.cfg.TurnstileSiteKey, GoogleEnabled: r.cfg.GoogleEnabled}
}

// RegisterInput is one sign-up.
type RegisterInput struct {
	Email, Username, Password string
	CaptchaToken              string
	ClientIP                  string
}

// Register creates an unverified account and mails its verify link, or, if
// the address already has an account, mails that account's owner a notice.
// nil in both cases (see the package comment on enumeration).
func (r *Registration) Register(ctx context.Context, in RegisterInput) error {
	if !r.cfg.Open {
		return ErrRegistrationClosed
	}
	limitKey := "register|" + in.ClientIP
	attempt, retry, ok := r.ipLimit.Allow(limitKey)
	if !ok {
		return &ThrottledError{RetryAfter: retry}
	}
	defer attempt.Release()
	email, err := NormalizeEmail(in.Email)
	if err != nil {
		return err
	}
	username := strings.ToLower(strings.TrimSpace(in.Username))
	if err := auth.CheckUsername(username); err != nil {
		return &ValidationError{"username", err.Error()}
	}
	if err := auth.CheckPassword(in.Password); err != nil {
		return &ValidationError{"password", err.Error()}
	}
	attempt.Fail() // every attempt that gets this far counts
	if err := checkCaptcha(ctx, r.captcha, in.CaptchaToken, in.ClientIP, ErrCaptchaFailed); err != nil {
		return err
	}

	// From here both paths do the same work: the hash and the token are
	// made before anyone knows whether the address is taken.
	hash, err := r.pw.Hash(ctx, in.Password)
	if err != nil {
		return fmt.Errorf("hashing password: %w", err)
	}
	token, tokenHash, err := auth.NewOpaqueToken()
	if err != nil {
		return err
	}
	reg, err := r.store.Register(ctx, store.NewAccount{
		Username: username, Email: email, PasswordHash: hash, VerifyHash: tokenHash, VerifyTTL: auth.VerifyTTL,
	})
	if errors.Is(err, store.ErrUserExists) {
		return ErrUsernameTaken
	}
	if err != nil {
		return err
	}
	if reg.Existing != nil {
		r.log.InfoContext(ctx, "registration for an address that has an account: notice mailed", "user_id", reg.Existing.ID)
		r.send(ctx, pmail.AccountExistsMessage(r.cfg.SiteName, email, r.cfg.BaseURL+"/login"))
		return nil
	}
	r.log.InfoContext(ctx, "account registered, waiting for email verification", "user_id", reg.User.ID)
	r.send(ctx, pmail.VerifyMessage(r.cfg.SiteName, email, username, r.verifyLink(token)))
	return nil
}

// ResendVerification mails a new verify link to an unverified account with
// that address. nil whether or not there is one. The new link expires when
// the first one did (store.ResendVerification).
func (r *Registration) ResendVerification(ctx context.Context, emailAddr, captchaToken, clientIP string) error {
	limitKey := "resend|" + clientIP
	attempt, retry, ok := r.ipLimit.Allow(limitKey)
	if !ok {
		return &ThrottledError{RetryAfter: retry}
	}
	defer attempt.Release()
	email, err := NormalizeEmail(emailAddr)
	if err != nil {
		return err
	}
	attempt.Fail()
	if err := checkCaptcha(ctx, r.captcha, captchaToken, clientIP, ErrCaptchaFailed); err != nil {
		return err
	}
	token, tokenHash, err := auth.NewOpaqueToken()
	if err != nil {
		return err
	}
	u, outcome, err := r.store.ResendVerification(ctx, email, tokenHash, auth.VerifyTTL)
	if err != nil {
		return err
	}
	if outcome != store.ResendIssued {
		r.log.InfoContext(ctx, "verification resend: nothing sent", "reason", outcome.String())
		return nil
	}
	r.send(ctx, pmail.VerifyMessage(r.cfg.SiteName, email, u.Username, r.verifyLink(token)))
	return nil
}

// VerifyEmail applies a verify link's token: the address is confirmed and
// a session starts, as a login would.
func (r *Registration) VerifyEmail(ctx context.Context, token, userAgent string) (Session, error) {
	hash, ok := auth.HashOpaqueToken(token)
	if !ok {
		return Session{}, ErrInvalidEmailToken
	}
	refresh, refreshHash, err := auth.NewRefreshToken()
	if err != nil {
		return Session{}, err
	}
	u, family, err := r.store.VerifyEmail(ctx, hash,
		store.NewRefresh{Hash: refreshHash, TTL: auth.RefreshTTL, UserAgent: cleanUserAgent(userAgent)})
	if errors.Is(err, store.ErrEmailTokenInvalid) {
		return Session{}, ErrInvalidEmailToken
	}
	if errors.Is(err, store.ErrAccountDisabled) {
		return Session{}, ErrAccountDisabled
	}
	if err != nil {
		return Session{}, err
	}
	r.log.InfoContext(ctx, "email verified", "user_id", u.ID)
	return r.auth.session(u, family, refresh)
}

// verifyLink puts the token in the fragment: browsers never send it to a
// server, so it stays out of access logs and Referer headers. The SPA reads
// it and posts it.
func (r *Registration) verifyLink(token string) string {
	return r.link("/verify-email", token)
}

// link is an SPA page with token in the fragment.
func (r *Registration) link(page, token string) string {
	return r.cfg.BaseURL + page + "#token=" + token
}

func (r *Registration) send(ctx context.Context, m pmail.Message) {
	sendMail(ctx, r.mailer, r.log, m)
}

// sendMail queues m, or logs that mail is not configured (mailer nil).
func sendMail(ctx context.Context, mailer Mailer, log *slog.Logger, m pmail.Message) {
	if mailer == nil {
		log.WarnContext(ctx, "mail skipped: mail is not configured (MAIL_FROM / PUBLIC_BASE_URL)", "kind", m.Kind)
		return
	}
	mailer.Enqueue(m)
}

// NormalizeEmail checks a new address: a bare address (no display name, no
// angle brackets), at most MaxEmailLen characters, returned lowercase.
func NormalizeEmail(s string) (string, error) {
	s = strings.TrimSpace(s)
	invalid := &ValidationError{"email", "must be a plain email address such as name@example.com"}
	if s == "" || len(s) > MaxEmailLen {
		return "", invalid
	}
	a, err := mail.ParseAddress(s)
	if err != nil || a.Name != "" || a.Address != s || !strings.Contains(a.Address, "@") {
		return "", invalid
	}
	return strings.ToLower(a.Address), nil
}
