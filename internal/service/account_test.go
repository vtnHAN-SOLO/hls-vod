package service

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/vtnHAN-SOLO/hls-vod/internal/auth"
)

func randomPassword(t *testing.T) string {
	t.Helper()
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func TestChangePassword(t *testing.T) {
	me := Principal{UserID: testUserID, SessionID: testFamily}
	current, next := randomPassword(t), randomPassword(t)

	t.Run("refused before any hash: missing, too short, same as current", func(t *testing.T) {
		pw := &countingPasswords{match: true}
		a := newTestAuth(t, pw)
		for name, tt := range map[string]struct{ current, next, field string }{
			"no current password":  {"", next, "current_password"},
			"new one too short":    {current, "short", "new_password"},
			"new one is too long":  {current, strings.Repeat("a", auth.MaxPasswordLen+1), "new_password"},
			"new one is unchanged": {current, current, "new_password"},
		} {
			_, err := a.ChangePassword(t.Context(), me, tt.current, tt.next, "")
			var verr *ValidationError
			if !errors.As(err, &verr) || verr.Field != tt.field {
				t.Errorf("%s: %v, want a ValidationError on %s", name, err, tt.field)
			}
		}
		if pw.verify != 0 {
			t.Fatalf("%d password verifications for requests that were refused anyway", pw.verify)
		}
	})

	t.Run("success: new hash, new session for the caller, user agent kept", func(t *testing.T) {
		a, got := newTestAuthStore(t, &countingPasswords{match: true})
		s, err := a.ChangePassword(t.Context(), me, current, next, "Firefox/140.0\x00\n")
		if err != nil {
			t.Fatal(err)
		}
		if got.changedHash != "hashed" || got.changedUA != "Firefox/140.0" {
			t.Fatalf("store got hash %q, user agent %q", got.changedHash, got.changedUA)
		}
		p, err := a.Authenticate(s.AccessToken)
		if err != nil || p.SessionID != newFamily || p.UserID != testUserID || s.RefreshToken == "" {
			t.Fatalf("new session = %+v (%+v, %v); want an access token for the new family", s, p, err)
		}
	})

	t.Run("a wrong current password is throttled per user, then nothing is checked", func(t *testing.T) {
		pw := &countingPasswords{match: false}
		a := newTestAuth(t, pw)
		for range auth.MaxLoginFailures {
			if _, err := a.ChangePassword(t.Context(), me, current, next, ""); !errors.Is(err, ErrWrongPassword) {
				t.Fatalf("wrong password = %v, want ErrWrongPassword", err)
			}
		}
		pw.match = true
		before := pw.verify
		var throttled *ThrottledError
		if _, err := a.ChangePassword(t.Context(), me, current, next, ""); !errors.As(err, &throttled) || throttled.RetryAfter <= 0 {
			t.Fatalf("blocked = %v, want a ThrottledError", err)
		}
		if pw.verify != before {
			t.Fatal("a blocked password change still ran a hash")
		}
		// Another user, and logins, are throttled separately.
		other := Principal{UserID: "b0000000-0000-4000-8000-000000000002", SessionID: testFamily}
		if _, err := a.ChangePassword(t.Context(), other, current, next, ""); errors.As(err, &throttled) {
			t.Fatal("another user is blocked by this one's failures")
		}
		if _, err := a.Login(t.Context(), LoginInput{Username: "alice", Password: current, ClientIP: "192.0.2.1"}); err != nil {
			t.Fatalf("login after blocked password changes = %v; the throttles must be separate", err)
		}
	})
}

func TestSessionsAndRevoke(t *testing.T) {
	a, got := newTestAuthStore(t, &countingPasswords{})
	me := Principal{UserID: testUserID, SessionID: testFamily}
	ss, err := a.Sessions(t.Context(), me)
	if err != nil || len(ss) != 2 {
		t.Fatalf("Sessions = %+v, %v", ss, err)
	}
	if ss[0].Current || ss[0].Device != "Unknown device" || !ss[1].Current || ss[1].Device != "Firefox on Linux" {
		t.Fatalf("sessions = %+v; want the token's family marked current", ss)
	}
	for _, id := range []string{"not-a-uuid", "../x", newFamily} {
		if err := a.RevokeSession(t.Context(), me, id); !errors.Is(err, ErrSessionNotFound) {
			t.Errorf("revoke %q = %v, want ErrSessionNotFound", id, err)
		}
	}
	if err := a.RevokeSession(t.Context(), me, strings.ToUpper(otherFamily)); err != nil {
		t.Fatalf("revoke = %v", err)
	}
	if err := a.RevokeOtherSessions(t.Context(), me); err != nil || got.revokedKeep != testFamily {
		t.Fatalf("revoke others kept %q, %v; want the caller's own session", got.revokedKeep, err)
	}
}

func TestDescribeUserAgent(t *testing.T) {
	for _, tt := range []struct{ ua, want string }{
		{"Mozilla/5.0 (X11; Linux x86_64; rv:140.0) Gecko/20100101 Firefox/140.0", "Firefox on Linux"},
		{"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36", "Chrome on Linux"},
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36 Edg/141.0.0.0", "Edge on Windows"},
		{"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Safari/605.1.15", "Safari on macOS"},
		{"Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1", "Safari on iOS"},
		{"Mozilla/5.0 (Linux; Android 15; Pixel 9) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Mobile Safari/537.36", "Chrome on Android"},
		{"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36 OPR/120.0.0.0", "Opera on Linux"},
		{"Mozilla/5.0 (X11; CrOS x86_64 14541.0.0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36", "Chrome on ChromeOS"},
		{"curl/8.5.0", "Unknown device"},
		{"SomeBot (Windows)", "Unknown browser on Windows"},
		{"", "Unknown device"},
	} {
		if got := DescribeUserAgent(tt.ua); got != tt.want {
			t.Errorf("DescribeUserAgent(%q) = %q, want %q", tt.ua, got, tt.want)
		}
	}
}

func TestCleanUserAgent(t *testing.T) {
	for _, tt := range []struct{ in, want string }{
		{"  Firefox/140.0 ", "Firefox/140.0"},
		{"bad\xffutf8\r\n\x00", "badutf8"},
		{strings.Repeat("é", 250), strings.Repeat("é", MaxUserAgentLen)}, // characters, not bytes
		{"", ""},
	} {
		if got := cleanUserAgent(tt.in); got != tt.want {
			t.Errorf("cleanUserAgent(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// A revoked session's access token still verifies for up to 15 minutes, but
// the account endpoints refuse it before doing anything.
func TestRevokedSessionCannotManageTheAccount(t *testing.T) {
	pw := &countingPasswords{match: true}
	a, got := newTestAuthStore(t, pw)
	dead := Principal{UserID: testUserID, SessionID: deadFamily}
	current, next := randomPassword(t), randomPassword(t)

	_, errChange := a.ChangePassword(t.Context(), dead, current, next, "")
	_, errList := a.Sessions(t.Context(), dead)
	errRevoke := a.RevokeSession(t.Context(), dead, otherFamily)
	errOthers := a.RevokeOtherSessions(t.Context(), dead)
	for name, err := range map[string]error{"change password": errChange, "sessions": errList, "revoke": errRevoke, "revoke others": errOthers} {
		if !errors.Is(err, ErrUnauthenticated) {
			t.Errorf("%s from a revoked session = %v, want ErrUnauthenticated", name, err)
		}
	}
	if got.liveChecks.Load() != 4 || pw.verify != 0 || got.changedHash != "" || got.revokedKeep != "" {
		t.Fatalf("live checks %d, verifications %d, store got %+v: a revoked session must be refused before anything runs",
			got.liveChecks.Load(), pw.verify, got)
	}
	// A token whose sid is not even a UUID never reaches the store.
	if err := a.RevokeOtherSessions(t.Context(), Principal{UserID: testUserID, SessionID: "x"}); !errors.Is(err, ErrUnauthenticated) || got.liveChecks.Load() != 4 {
		t.Fatalf("malformed sid = %v (checks %d)", err, got.liveChecks.Load())
	}
}
