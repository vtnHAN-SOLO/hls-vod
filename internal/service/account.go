package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/vtnHAN-SOLO/hls-vod/internal/auth"
	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
)

// The account page acts on the access token's own user only: every method
// takes the Principal, never a user id from the request.
//
// Revoking a session (here, or by a password change) stops its refresh
// token at once, but access tokens already issued to it stay valid until
// they expire, at most auth.AccessTTL (15 min): Authenticate checks the
// signature only, never the database, so a request costs no query. The
// other device is therefore logged out at its next refresh, or within 15
// minutes.
//
// The exception: the endpoints that manage sessions, the password and the
// login methods (ChangePassword, Sessions, RevokeSession,
// RevokeOtherSessions, UnlinkGoogle, Registration.PasswordSetLink,
// Google.StartLink) first
// check that the caller's own session is still live (requireLiveSession,
// one query). Otherwise a revoked device could, inside that window, revoke
// the session that revoked it, or change the password. They are rare, so
// the query costs nothing that matters.

var (
	// ErrWrongPassword means the current password of a password change is
	// not the user's. A 400, not a 401: the access token is fine, and a 401
	// would make the client refresh and retry.
	ErrWrongPassword = errors.New("the current password is incorrect")
	// ErrSessionNotFound means the caller has no live session with that id:
	// unknown, already ended or another user's, indistinguishable on purpose.
	ErrSessionNotFound = errors.New("session not found")
	// ErrLastLoginMethod means the caller has no password: Google is their
	// only way to log in and cannot be removed.
	ErrLastLoginMethod = errors.New("set a password before removing Google: it is your only way to log in")
	// ErrNoGoogle means the caller has no Google account linked.
	ErrNoGoogle = errors.New("no Google account is linked")
)

// Account is the caller's account as the account page shows it.
type Account struct {
	User   model.User
	Google *model.Identity // nil when none is linked
}

// Account returns the caller's account and its Google identity.
func (a *Auth) Account(ctx context.Context, p Principal) (Account, error) {
	u, err := a.Me(ctx, p)
	if err != nil {
		return Account{}, err
	}
	out := Account{User: u}
	id, ok, err := a.store.GoogleIdentity(ctx, u.ID)
	if err != nil {
		return Account{}, err
	}
	if ok {
		out.Google = &id
	}
	return out, nil
}

// UnlinkGoogle removes the caller's Google account. Refused for an account
// without a password (ErrLastLoginMethod): it would lock the user out.
func (a *Auth) UnlinkGoogle(ctx context.Context, p Principal) error {
	if err := a.requireLiveSession(ctx, p); err != nil {
		return err
	}
	switch err := a.store.UnlinkGoogle(ctx, p.UserID); {
	case errors.Is(err, store.ErrLastLoginMethod):
		return ErrLastLoginMethod
	case errors.Is(err, store.ErrNotFound):
		return ErrNoGoogle
	case err != nil:
		return err
	}
	a.log.InfoContext(ctx, "Google account unlinked", "user_id", p.UserID)
	return nil
}

// MaxUserAgentLen is how much of a User-Agent is kept, in characters; the
// database refuses more.
const MaxUserAgentLen = 200

// SessionInfo is one login of the caller as the account page shows it.
type SessionInfo struct {
	ID         string // the refresh-token family id
	Device     string // "Firefox on Linux"
	CreatedAt  time.Time
	LastUsedAt time.Time
	Current    bool // the session of the access token that asked
}

// Me returns the caller's account. A user deleted since the token was
// issued is ErrUnauthenticated.
func (a *Auth) Me(ctx context.Context, p Principal) (model.User, error) {
	u, err := a.store.UserByID(ctx, p.UserID)
	if errors.Is(err, store.ErrNotFound) {
		return model.User{}, ErrUnauthenticated
	}
	if err != nil {
		return model.User{}, fmt.Errorf("loading user %s: %w", p.UserID, err)
	}
	return u, nil
}

// ChangePassword replaces the caller's password after checking the current
// one, ends every session of the user, and starts a new one for the caller
// (returned, like a login), so the tab that asked stays logged in.
//
// Wrong current passwords are throttled per user id, like logins: a stolen
// access token must not be a way to guess the password at speed. A blocked
// caller gets a *ThrottledError before anything is checked.
func (a *Auth) ChangePassword(ctx context.Context, p Principal, current, next, userAgent string) (Session, error) {
	if err := a.requireLiveSession(ctx, p); err != nil {
		return Session{}, err
	}
	attempt, retry, ok := a.pwThrottle.Allow(p.UserID)
	if !ok {
		return Session{}, &ThrottledError{RetryAfter: retry}
	}
	defer attempt.Release()
	// The rules on the new password first: they need no hash and say
	// nothing about the current one.
	switch {
	case current == "":
		return Session{}, &ValidationError{"current_password", "is required"}
	case auth.CheckPassword(next) != nil:
		return Session{}, &ValidationError{"new_password", auth.ErrPasswordPolicy.Error()}
	case next == current:
		return Session{}, &ValidationError{"new_password", "must differ from the current password"}
	}

	u, err := a.Me(ctx, p)
	if err != nil {
		return Session{}, err
	}
	if err := a.checkCurrentPassword(ctx, u, current, attempt); err != nil {
		return Session{}, err
	}

	hash, err := a.pw.Hash(ctx, next)
	if err != nil {
		return Session{}, fmt.Errorf("hashing new password: %w", err)
	}
	refresh, refreshHash, err := auth.NewRefreshToken()
	if err != nil {
		return Session{}, err
	}
	family, err := a.store.ChangePassword(ctx, u.ID, hash,
		store.NewRefresh{Hash: refreshHash, TTL: auth.RefreshTTL, UserAgent: cleanUserAgent(userAgent)})
	if errors.Is(err, store.ErrNotFound) {
		return Session{}, ErrUnauthenticated
	}
	if err != nil {
		return Session{}, err
	}
	a.log.InfoContext(ctx, "password changed; every other session of the user revoked", "user_id", u.ID)
	return a.session(u, family, refresh)
}

// checkCurrentPassword is ErrWrongPassword unless current is u's password.
// attempt is the caller's pwThrottle reservation (per user id): a wrong
// password fails it, a right one succeeds it (the failures are forgotten).
func (a *Auth) checkCurrentPassword(ctx context.Context, u model.User, current string, attempt *auth.Attempt) error {
	ok := false
	if !u.HasPassword() {
		// A Google-only account has no password to match: a wrong one,
		// after the same work.
		if err := a.pw.VerifyDummy(ctx, current); err != nil {
			return err
		}
	} else if len(current) <= maxCredentialBytes {
		var err error
		if ok, err = a.pw.Verify(ctx, u.PasswordHash, current); err != nil {
			return fmt.Errorf("verifying password of user %s: %w", u.ID, err)
		}
	}
	if !ok {
		attempt.Fail()
		return ErrWrongPassword
	}
	attempt.Succeed()
	return nil
}

// Sessions lists the caller's live sessions, most recently used first.
// Never a token or a hash: only ids, times and a device summary.
func (a *Auth) Sessions(ctx context.Context, p Principal) ([]SessionInfo, error) {
	if err := a.requireLiveSession(ctx, p); err != nil {
		return nil, err
	}
	ss, err := a.store.ListSessions(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	out := make([]SessionInfo, 0, len(ss))
	for _, s := range ss {
		ua := ""
		if s.UserAgent != nil {
			ua = *s.UserAgent
		}
		out = append(out, SessionInfo{
			ID: s.ID, Device: DescribeUserAgent(ua),
			CreatedAt: s.CreatedAt, LastUsedAt: s.LastUsedAt,
			Current: s.ID == p.SessionID,
		})
	}
	return out, nil
}

// RevokeSession ends one of the caller's sessions, their current one
// included. ErrSessionNotFound for anything that is not a live session of
// theirs.
func (a *Auth) RevokeSession(ctx context.Context, p Principal, id string) error {
	if err := a.requireLiveSession(ctx, p); err != nil {
		return err
	}
	id = strings.ToLower(id)
	if !model.ValidID(id) {
		return ErrSessionNotFound
	}
	err := a.store.RevokeSession(ctx, p.UserID, id)
	if errors.Is(err, store.ErrNotFound) {
		return ErrSessionNotFound
	}
	return err
}

// RevokeOtherSessions ends every session of the caller except the current
// one.
func (a *Auth) RevokeOtherSessions(ctx context.Context, p Principal) error {
	if err := a.requireLiveSession(ctx, p); err != nil {
		return err
	}
	n, err := a.store.RevokeOtherSessions(ctx, p.UserID, p.SessionID)
	if err != nil {
		return err
	}
	a.log.InfoContext(ctx, "other sessions revoked", "user_id", p.UserID, "count", n)
	return nil
}

// requireLiveSession is ErrUnauthenticated unless the caller's session can
// still refresh. A 401, so the client refreshes, is refused, and lands on
// the login page.
func (a *Auth) requireLiveSession(ctx context.Context, p Principal) error {
	if !model.ValidID(p.SessionID) {
		return ErrUnauthenticated
	}
	live, err := a.store.SessionLive(ctx, p.UserID, p.SessionID)
	if err != nil {
		return err
	}
	if !live {
		return ErrUnauthenticated
	}
	return nil
}

// cleanUserAgent is what is stored of a User-Agent header: valid UTF-8, no
// control characters, at most MaxUserAgentLen characters.
func cleanUserAgent(ua string) string {
	ua = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(ua, ""))
	ua = strings.TrimSpace(ua)
	if r := []rune(ua); len(r) > MaxUserAgentLen {
		ua = strings.TrimSpace(string(r[:MaxUserAgentLen]))
	}
	return ua
}

// Browser and OS tokens, most specific first: Edge and Opera say "Chrome",
// Chrome says "Safari", and Android says "Linux".
var (
	uaBrowsers = []struct{ token, name string }{
		{"Edg/", "Edge"}, {"EdgA/", "Edge"}, {"EdgiOS/", "Edge"},
		{"OPR/", "Opera"},
		{"Firefox/", "Firefox"}, {"FxiOS/", "Firefox"},
		{"Chrome/", "Chrome"}, {"CriOS/", "Chrome"}, {"Chromium/", "Chromium"},
		{"Safari/", "Safari"},
	}
	uaSystems = []struct{ token, name string }{
		{"Android", "Android"},
		{"iPhone", "iOS"}, {"iPad", "iPadOS"},
		{"CrOS", "ChromeOS"},
		{"Windows", "Windows"},
		{"Macintosh", "macOS"}, {"Mac OS X", "macOS"},
		{"Linux", "Linux"},
	}
)

// DescribeUserAgent sums a User-Agent up for people: "Firefox on Linux",
// "Chrome", "Unknown browser on Windows", or "Unknown device" for none.
// A label, never a security decision: any client can send any header.
func DescribeUserAgent(ua string) string {
	browser, system := "", ""
	for _, b := range uaBrowsers {
		if strings.Contains(ua, b.token) {
			browser = b.name
			break
		}
	}
	for _, s := range uaSystems {
		if strings.Contains(ua, s.token) {
			system = s.name
			break
		}
	}
	switch {
	case browser != "" && system != "":
		return browser + " on " + system
	case browser != "":
		return browser
	case system != "":
		return "Unknown browser on " + system
	default:
		return "Unknown device"
	}
}
