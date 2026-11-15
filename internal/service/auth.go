package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/vtnHAN-SOLO/hls-vod/internal/auth"
	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
	"github.com/vtnHAN-SOLO/hls-vod/internal/turnstile"
)

var (
	// ErrInvalidCredentials is the one answer for an unknown username and
	// for a wrong password, so a client cannot tell which.
	ErrInvalidCredentials = errors.New("invalid username or password")
	// ErrInvalidRefreshToken covers unknown, expired, revoked and reused
	// refresh tokens alike.
	ErrInvalidRefreshToken = errors.New("invalid refresh token")
	// ErrUnauthenticated means no valid access token was presented.
	ErrUnauthenticated = errors.New("authentication required")
	// ErrForbidden means the caller is authenticated but not allowed.
	ErrForbidden = errors.New("not allowed")
	// ErrEmailNotVerified means the username and password are right but the
	// account's email is not confirmed yet. Returned only after the password
	// checked out, so it tells nothing to someone guessing.
	ErrEmailNotVerified = errors.New("the email address is not verified yet")
	// ErrAccountDisabled means an admin has disabled the account. Like
	// ErrEmailNotVerified, a login gets it only after the password checked
	// out.
	ErrAccountDisabled = errors.New("the account is disabled")
	// ErrCaptchaRequired means this username + address has failed
	// CaptchaAfterFailures times: a login needs a solved challenge, and the
	// one sent (if any) was missing or rejected.
	ErrCaptchaRequired = errors.New("complete the challenge to log in")
	// ErrCaptchaFailed means a register or resend challenge was missing or
	// rejected.
	ErrCaptchaFailed = errors.New("the challenge was not solved")
	// ErrCaptchaUnavailable means the challenge could not be checked
	// (Cloudflare unreachable, or Turnstile not configured): the request is
	// refused, never let through.
	ErrCaptchaUnavailable = errors.New("the challenge cannot be checked right now")
)

// CaptchaAfterFailures is the number of failed logins for one username +
// address from which a login needs a solved challenge.
const CaptchaAfterFailures = 3

// Password spraying: one address trying a few passwords on many usernames
// stays under the per-username throttle, so failed logins are also counted
// per client IP across every username, within SprayWindow. From
// SprayCaptchaAfter of them every login from that address needs a solved
// challenge; from SprayBlockAfter the address gets 429 for SprayWindow.
// Many real users can share one address (CGNAT, an office, a campus), so
// the challenge comes first and the block only far later. Like the other
// throttles it is per api process.
const (
	SprayCaptchaAfter = 10
	SprayBlockAfter   = 100
	SprayWindow       = 15 * time.Minute
)

// Captcha checks a challenge token. *turnstile.Verifier satisfies it; its
// errors wrap turnstile.ErrRejected or turnstile.ErrUnavailable.
type Captcha interface {
	Verify(ctx context.Context, token, remoteIP string) error
}

// checkCaptcha maps a challenge check to ErrCaptchaUnavailable (fail
// closed: no captcha configured, or it cannot answer) or rejected.
func checkCaptcha(ctx context.Context, c Captcha, token, clientIP string, rejected error) error {
	if c == nil {
		return ErrCaptchaUnavailable
	}
	err := c.Verify(ctx, token, clientIP)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, turnstile.ErrUnavailable):
		return fmt.Errorf("%w: %w", ErrCaptchaUnavailable, err)
	default:
		return rejected
	}
}

// ThrottledError means a password check (login, or the current password
// of a password change) is blocked for RetryAfter.
type ThrottledError struct{ RetryAfter time.Duration }

func (e *ThrottledError) Error() string {
	return fmt.Sprintf("too many failed password attempts; retry in %s", e.RetryAfter.Round(time.Second))
}

// Principal is the authenticated caller, from a verified access token.
type Principal struct {
	UserID string
	Role   model.Role
	// SessionID is the login (refresh-token family) the access token was
	// issued for.
	SessionID string
}

// Admin reports whether the caller may see every movie and filter by status.
func (p Principal) Admin() bool { return p.Role == model.RoleAdmin }

// AuthStore is the slice of the store authentication needs.
type AuthStore interface {
	UserByUsername(ctx context.Context, username string) (model.User, error)
	UserByID(ctx context.Context, id string) (model.User, error)
	CreateRefreshToken(ctx context.Context, userID string, refresh store.NewRefresh) (string, error)
	RotateRefreshToken(ctx context.Context, oldHash []byte, next store.NewRefresh) (userID, familyID string, err error)
	RevokeRefreshFamily(ctx context.Context, hash []byte) error

	ChangePassword(ctx context.Context, userID, passwordHash string, refresh store.NewRefresh) (string, error)
	GoogleIdentity(ctx context.Context, userID string) (model.Identity, bool, error)
	UnlinkGoogle(ctx context.Context, userID string) error
	ListSessions(ctx context.Context, userID string) ([]model.LoginSession, error)
	SessionLive(ctx context.Context, userID, familyID string) (bool, error)
	RevokeSession(ctx context.Context, userID, familyID string) error
	RevokeOtherSessions(ctx context.Context, userID, keepFamily string) (int, error)
}

// Passwords hashes and verifies passwords. *auth.Hasher satisfies it.
type Passwords interface {
	Hash(ctx context.Context, password string) (string, error)
	Verify(ctx context.Context, encoded, password string) (bool, error)
	VerifyDummy(ctx context.Context, password string) error
}

// Session is what a login or a refresh hands out.
type Session struct {
	AccessToken    string
	AccessExpires  time.Time
	RefreshToken   string // goes into an HttpOnly cookie, never into JSON
	RefreshExpires time.Time
	User           model.User
}

// Auth logs users in and out, rotates refresh tokens, checks access tokens,
// and runs the account page: password change and sessions (account.go).
type Auth struct {
	store  AuthStore
	pw     Passwords
	signer *auth.Signer
	// throttle counts failed logins per username + client IP; ipThrottle
	// per client IP across every username (password spraying); pwThrottle
	// failed current passwords of a password change or Google link per user
	// id. Separate instances: one must not lock the other out.
	throttle   *auth.Throttle
	ipThrottle *auth.Throttle
	pwThrottle *auth.Throttle
	// captcha is nil when Turnstile is not configured: logins then never
	// ask for a challenge (the throttle still applies).
	captcha Captcha
	log     *slog.Logger
	now     func() time.Time
}

// WithCaptcha makes logins with CaptchaAfterFailures failures behind them
// need a solved challenge. Returns a.
func (a *Auth) WithCaptcha(c Captcha) *Auth {
	a.captcha = c
	return a
}

// NewAuth returns an Auth service. loginThrottle and passwordThrottle must
// be distinct instances.
func NewAuth(st AuthStore, pw Passwords, signer *auth.Signer, loginThrottle, passwordThrottle *auth.Throttle, log *slog.Logger) *Auth {
	return &Auth{
		store: st, pw: pw, signer: signer,
		throttle: loginThrottle, pwThrottle: passwordThrottle,
		ipThrottle: auth.NewLimiter(SprayBlockAfter, SprayWindow),
		log:        log, now: time.Now,
	}
}

// maxCredentialBytes bounds what is ever hashed: a 1 MiB "password" is not
// worth a hash slot.
const maxCredentialBytes = 4 * auth.MaxPasswordLen

// LoginInput is one login attempt.
type LoginInput struct {
	Username, Password string
	ClientIP           string
	UserAgent          string
	// CaptchaToken is the solved challenge, needed only once the username
	// + address has CaptchaAfterFailures failures.
	CaptchaToken string
}

// Login checks the username and password and starts a new session (a new
// refresh-token family), remembering the client's User-Agent for the
// sessions list. Usernames are case-insensitive: they are stored lowercase.
//
// In order: a blocked username + address, or a blocked address
// (SprayBlockAfter), gets a *ThrottledError; CaptchaAfterFailures failures
// of the username + address, or SprayCaptchaAfter of the address across
// every username (in-flight attempts counted), need a solved challenge
// (ErrCaptchaRequired, or ErrCaptchaUnavailable if it cannot be checked);
// then the password. A failure that brings the key to the threshold answers
// ErrCaptchaRequired too, so the client shows the challenge at once. A right
// password on a disabled account is ErrAccountDisabled, on one whose email
// is not confirmed ErrEmailNotVerified. A login under the threshold never
// calls Cloudflare.
func (a *Auth) Login(ctx context.Context, in LoginInput) (Session, error) {
	username := strings.ToLower(strings.TrimSpace(in.Username))
	password := in.Password
	if username == "" || password == "" {
		return Session{}, &ValidationError{"credentials", "username and password are required"}
	}
	// Both reservations are held until the password is decided (see
	// auth.Throttle): a parallel burst counts while it runs.
	userAttempt, retry, ok := a.throttle.Allow(username + "|" + in.ClientIP)
	if !ok {
		return Session{}, &ThrottledError{RetryAfter: retry}
	}
	defer userAttempt.Release()
	ipAttempt, retry, ok := a.ipThrottle.Allow(in.ClientIP)
	if !ok {
		return Session{}, &ThrottledError{RetryAfter: retry}
	}
	defer ipAttempt.Release()
	captchaOn := a.captcha != nil
	if captchaOn && (userAttempt.Prior >= CaptchaAfterFailures || ipAttempt.Prior >= SprayCaptchaAfter) {
		if err := checkCaptcha(ctx, a.captcha, in.CaptchaToken, in.ClientIP, ErrCaptchaRequired); err != nil {
			return Session{}, err
		}
	}
	fail := func() (Session, error) {
		userFailures, ipFailures := userAttempt.Fail(), ipAttempt.Fail()
		if captchaOn && (userFailures >= CaptchaAfterFailures || ipFailures >= SprayCaptchaAfter) {
			return Session{}, fmt.Errorf("%w: %w", ErrInvalidCredentials, ErrCaptchaRequired)
		}
		return Session{}, ErrInvalidCredentials
	}
	if len(username) > 256 || len(password) > maxCredentialBytes {
		return fail()
	}

	u, err := a.store.UserByUsername(ctx, username)
	if errors.Is(err, store.ErrNotFound) {
		// Same work as a wrong password, so timing does not reveal
		// which usernames exist.
		if err := a.pw.VerifyDummy(ctx, password); err != nil {
			return Session{}, err
		}
		return fail()
	}
	if err != nil {
		return Session{}, err
	}
	if !u.HasPassword() {
		// A Google-only account: the same work and answer as a wrong
		// password, so it does not show which accounts have one.
		if err := a.pw.VerifyDummy(ctx, password); err != nil {
			return Session{}, err
		}
		return fail()
	}
	match, err := a.pw.Verify(ctx, u.PasswordHash, password)
	if err != nil {
		return Session{}, fmt.Errorf("verifying password of user %s: %w", u.ID, err)
	}
	if !match {
		return fail()
	}
	// A right password clears this username + address, but not the
	// address's spraying count: a sprayer's own account must not reset it.
	userAttempt.Succeed()
	if u.Disabled() {
		return Session{}, ErrAccountDisabled
	}
	if u.Unverified() {
		return Session{}, ErrEmailNotVerified
	}

	refresh, hash, err := auth.NewRefreshToken()
	if err != nil {
		return Session{}, err
	}
	family, err := a.store.CreateRefreshToken(ctx, u.ID,
		store.NewRefresh{Hash: hash, TTL: auth.RefreshTTL, UserAgent: cleanUserAgent(in.UserAgent)})
	if err != nil {
		return Session{}, err
	}
	return a.session(u, family, refresh)
}

// Refresh rotates a refresh token: the presented one dies, a new one (same
// family) and a new access token are returned. Presenting a token that was
// already rotated revokes the whole family; the caller sees
// ErrInvalidRefreshToken either way.
func (a *Auth) Refresh(ctx context.Context, token string) (Session, error) {
	oldHash, ok := auth.HashRefreshToken(token)
	if !ok {
		return Session{}, ErrInvalidRefreshToken
	}
	refresh, newHash, err := auth.NewRefreshToken()
	if err != nil {
		return Session{}, err
	}
	userID, family, err := a.store.RotateRefreshToken(ctx, oldHash, store.NewRefresh{Hash: newHash, TTL: auth.RefreshTTL})
	switch {
	case errors.Is(err, store.ErrRefreshReused):
		a.log.WarnContext(ctx, "refresh token reused: the whole session family is revoked")
		return Session{}, ErrInvalidRefreshToken
	case errors.Is(err, store.ErrRefreshInvalid):
		return Session{}, ErrInvalidRefreshToken
	case err != nil:
		return Session{}, err
	}
	// Re-read the user: a role change takes effect at the next refresh.
	// The store already refuses a disabled user's token; this is the same
	// rule once more, in case one was disabled in between.
	u, err := a.store.UserByID(ctx, userID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && u.Disabled()) {
		return Session{}, ErrInvalidRefreshToken
	}
	if err != nil {
		return Session{}, err
	}
	return a.session(u, family, refresh)
}

// Logout revokes the session (refresh-token family) the token belongs to.
// No token, or one that is not ours, is not an error.
func (a *Auth) Logout(ctx context.Context, token string) error {
	hash, ok := auth.HashRefreshToken(token)
	if !ok {
		return nil
	}
	return a.store.RevokeRefreshFamily(ctx, hash)
}

// Authenticate verifies an access token.
func (a *Auth) Authenticate(token string) (Principal, error) {
	c, err := a.signer.Verify(token)
	if err != nil {
		return Principal{}, ErrUnauthenticated
	}
	return Principal{UserID: c.UserID, Role: model.Role(c.Role), SessionID: c.SessionID}, nil
}

// session issues the access token for login family and packs it with the
// refresh token.
func (a *Auth) session(u model.User, family, refresh string) (Session, error) {
	access, exp, err := a.signer.Issue(u.ID, string(u.Role), family)
	if err != nil {
		return Session{}, err
	}
	return Session{
		AccessToken: access, AccessExpires: exp,
		RefreshToken: refresh, RefreshExpires: a.now().Add(auth.RefreshTTL),
		User: u,
	}, nil
}
