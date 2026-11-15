package handler

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vtnHAN-SOLO/hls-vod/internal/auth"
	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
)

const (
	firefoxLinux = "Mozilla/5.0 (X11; Linux x86_64; rv:140.0) Gecko/20100101 Firefox/140.0"
	chromeLinux  = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36"
)

// device is one logged-in browser: its access token and refresh cookie.
type device struct {
	access  string
	refresh *http.Cookie
	sid     string
}

func loginFrom(t *testing.T, srv *httptest.Server, username, password, userAgent string) device {
	t.Helper()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+"/api/v1/auth/login",
		strings.NewReader(`{"username":"`+username+`","password":"`+password+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)
	r := send(t, srv, req)
	if r.status != http.StatusOK {
		t.Fatalf("login = %d: %s", r.status, r.body)
	}
	return deviceOf(t, r)
}

// deviceOf reads the session a login or password change handed out.
func deviceOf(t *testing.T, r response) device {
	t.Helper()
	var tok tokenJSON
	decode(t, r.body, &tok)
	c, err := fixtures(t).signer.Verify(tok.AccessToken)
	if err != nil || !model.ValidID(c.SessionID) {
		t.Fatalf("access token claims = %+v, %v; want a sid", c, err)
	}
	ck := refreshCookieOf(t, r)
	return device{
		access:  tok.AccessToken,
		refresh: &http.Cookie{Name: ck.Name, Value: ck.Value}, // #nosec G124 -- a request cookie the test client sends back
		sid:     c.SessionID,
	}
}

// refreshes reports whether the device's refresh cookie still works, and
// keeps the rotated cookie.
func (d *device) refreshes(t *testing.T, srv *httptest.Server) bool {
	t.Helper()
	r := postJSON(t, srv, "/api/v1/auth/refresh", "", "", true, d.refresh)
	if r.status != http.StatusOK {
		return false
	}
	ck := refreshCookieOf(t, r)
	d.refresh = &http.Cookie{Name: ck.Name, Value: ck.Value} // #nosec G124 -- a request cookie the test client sends back
	return true
}

// call sends method path as token, with a JSON body when body is not "".
func call(t *testing.T, srv *httptest.Server, method, path, token, body string) response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("User-Agent", firefoxLinux)
	return send(t, srv, req)
}

func sessionsOf(t *testing.T, srv *httptest.Server, token string) []sessionJSON {
	t.Helper()
	r := call(t, srv, http.MethodGet, "/api/v1/me/sessions", token, "")
	if r.status != http.StatusOK {
		t.Fatalf("sessions = %d: %s", r.status, r.body)
	}
	var out sessionListJSON
	decode(t, r.body, &out)
	return out.Items
}

func passwordBody(current, next string) string {
	return `{"current_password":"` + current + `","new_password":"` + next + `"}`
}

func TestMe(t *testing.T) {
	f := fixtures(t)
	srv, as := newServerFull(t, &fakePinger{}, catalogStore(), newFakeAuthStore(t), Options{SecureCookies: true})

	r := call(t, srv, http.MethodGet, "/api/v1/me", f.viewerToken, "")
	if r.status != http.StatusOK || r.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("me = %d %q: %s", r.status, r.header.Get("Cache-Control"), r.body)
	}
	var me accountJSON
	decode(t, r.body, &me)
	if me.ID != viewerID || me.Username != "viewer" || me.Role != "user" || me.CreatedAt.IsZero() {
		t.Fatalf("me = %+v", me)
	}
	if strings.Contains(string(r.body), "argon2id") {
		t.Fatal("the password hash left the api")
	}

	// A user deleted since the token was issued.
	gone, _, err := f.signer.Issue(model.NewID(), "user", model.NewID())
	if err != nil {
		t.Fatal(err)
	}
	if r := call(t, srv, http.MethodGet, "/api/v1/me", gone, ""); r.status != http.StatusUnauthorized {
		t.Fatalf("deleted user = %d", r.status)
	}
	if r := call(t, srv, http.MethodPost, "/api/v1/me", f.viewerToken, ""); r.status != http.StatusMethodNotAllowed {
		t.Fatalf("POST me = %d", r.status)
	}
	as.err = errors.New("db down")
	if r := call(t, srv, http.MethodGet, "/api/v1/me", f.viewerToken, ""); r.status != http.StatusInternalServerError {
		t.Fatalf("store failure = %d", r.status)
	}
}

func TestSessionsEndpoints(t *testing.T) {
	f := fixtures(t)
	srv, as := newServerFull(t, &fakePinger{}, catalogStore(), newFakeAuthStore(t), Options{SecureCookies: true})
	firefox := loginFrom(t, srv, "viewer", f.viewerPassword, firefoxLinux)
	chrome := loginFrom(t, srv, "viewer", f.viewerPassword, chromeLinux)
	admin := loginFrom(t, srv, "admin", f.adminPassword, firefoxLinux)

	t.Run("list: one per login, the caller's marked, never a token", func(t *testing.T) {
		r := call(t, srv, http.MethodGet, "/api/v1/me/sessions", firefox.access, "")
		if r.status != http.StatusOK || r.header.Get("Cache-Control") != "no-store" {
			t.Fatalf("sessions = %d: %s", r.status, r.body)
		}
		for _, secret := range []string{firefox.refresh.Value, chrome.refresh.Value, firefox.access, "hash"} {
			if strings.Contains(string(r.body), secret) {
				t.Fatalf("the sessions list exposes %q: %s", secret, r.body)
			}
		}
		var list sessionListJSON
		decode(t, r.body, &list)
		got := map[string]sessionJSON{}
		for _, s := range list.Items {
			got[s.ID] = s
		}
		if len(list.Items) != 2 || !got[firefox.sid].Current || got[chrome.sid].Current {
			t.Fatalf("sessions = %+v; want firefox (current) and chrome", list.Items)
		}
		if got[firefox.sid].UserAgent != "Firefox on Linux" || got[chrome.sid].UserAgent != "Chrome on Linux" {
			t.Fatalf("devices = %q, %q", got[firefox.sid].UserAgent, got[chrome.sid].UserAgent)
		}
		// A refresh keeps the session (same family) and bumps last_used_at.
		before := got[chrome.sid]
		if !chrome.refreshes(t, srv) {
			t.Fatal("chrome could not refresh")
		}
		after := sessionsOf(t, srv, chrome.access)
		if len(after) != 2 || after[0].ID != chrome.sid || !after[0].LastUsedAt.After(before.LastUsedAt) ||
			!after[0].CreatedAt.Equal(before.CreatedAt) {
			t.Fatalf("after a refresh: %+v; want chrome first, last used later, created unchanged", after)
		}
	})

	t.Run("another user's session, an unknown one and a malformed id are 404", func(t *testing.T) {
		for _, id := range []string{admin.sid, model.NewID(), "not-a-uuid"} {
			r := call(t, srv, http.MethodDelete, "/api/v1/me/sessions/"+id, firefox.access, "")
			if r.status != http.StatusNotFound {
				t.Fatalf("DELETE %s = %d, want 404", id, r.status)
			}
			assertErrorCode(t, r.body, "not_found")
		}
		if !admin.refreshes(t, srv) {
			t.Fatal("the admin's session died")
		}
		if r := call(t, srv, http.MethodGet, "/api/v1/me/sessions/"+chrome.sid, firefox.access, ""); r.status != http.StatusMethodNotAllowed {
			t.Fatalf("GET one session = %d", r.status)
		}
	})

	t.Run("revoke-others keeps the caller's own", func(t *testing.T) {
		third := loginFrom(t, srv, "viewer", f.viewerPassword, "")
		r := call(t, srv, http.MethodPost, "/api/v1/me/sessions/revoke-others", firefox.access, "")
		if r.status != http.StatusNoContent {
			t.Fatalf("revoke-others = %d: %s", r.status, r.body)
		}
		if !firefox.refreshes(t, srv) {
			t.Fatal("the caller's own session was revoked")
		}
		if chrome.refreshes(t, srv) || third.refreshes(t, srv) {
			t.Fatal("another session survived revoke-others")
		}
		if ss := sessionsOf(t, srv, firefox.access); len(ss) != 1 || ss[0].ID != firefox.sid || !ss[0].Current {
			t.Fatalf("sessions = %+v, want only the caller's", ss)
		}
		if !admin.refreshes(t, srv) {
			t.Fatal("another user's session was revoked")
		}
		if r := call(t, srv, http.MethodGet, "/api/v1/me/sessions/revoke-others", firefox.access, ""); r.status != http.StatusMethodNotAllowed {
			t.Fatalf("GET revoke-others = %d", r.status)
		}
		if r := call(t, srv, http.MethodDelete, "/api/v1/me/sessions", firefox.access, ""); r.status != http.StatusMethodNotAllowed {
			t.Fatalf("DELETE the whole list = %d", r.status)
		}
	})

	t.Run("revoking one session, the caller's own included", func(t *testing.T) {
		other := loginFrom(t, srv, "viewer", f.viewerPassword, chromeLinux)
		if r := call(t, srv, http.MethodDelete, "/api/v1/me/sessions/"+other.sid, firefox.access, ""); r.status != http.StatusNoContent {
			t.Fatalf("DELETE other = %d: %s", r.status, r.body)
		}
		if other.refreshes(t, srv) {
			t.Fatal("the revoked session can still refresh")
		}
		if r := call(t, srv, http.MethodDelete, "/api/v1/me/sessions/"+strings.ToUpper(firefox.sid), firefox.access, ""); r.status != http.StatusNoContent {
			t.Fatalf("DELETE own = %d", r.status)
		}
		if firefox.refreshes(t, srv) {
			t.Fatal("the caller's revoked session can still refresh")
		}
		// Its access token still verifies (no per-request check), but the
		// account endpoints check the session first: 401.
		if r := call(t, srv, http.MethodGet, "/api/v1/me/sessions", firefox.access, ""); r.status != http.StatusUnauthorized {
			t.Fatalf("sessions from the revoked session = %d, want 401", r.status)
		}
	})

	t.Run("without a token, and when the store fails", func(t *testing.T) {
		for _, c := range []struct{ method, path string }{
			{http.MethodGet, "/api/v1/me"},
			{http.MethodGet, "/api/v1/me/sessions"},
			{http.MethodDelete, "/api/v1/me/sessions/" + chrome.sid},
			{http.MethodPost, "/api/v1/me/sessions/revoke-others"},
			{http.MethodPost, "/api/v1/me/password"},
		} {
			if r := call(t, srv, c.method, c.path, "", ""); r.status != http.StatusUnauthorized {
				t.Errorf("%s %s without a token = %d", c.method, c.path, r.status)
			}
		}
		as.err = errors.New("db down")
		defer func() { as.err = nil }()
		for _, c := range []struct{ method, path string }{
			{http.MethodGet, "/api/v1/me/sessions"},
			{http.MethodDelete, "/api/v1/me/sessions/" + chrome.sid},
			{http.MethodPost, "/api/v1/me/sessions/revoke-others"},
		} {
			if r := call(t, srv, c.method, c.path, admin.access, ""); r.status != http.StatusInternalServerError {
				t.Errorf("%s %s, store down = %d", c.method, c.path, r.status)
			}
		}
	})
}

func TestChangePassword(t *testing.T) {
	f := fixtures(t)
	next := randomString(18)

	t.Run("success: this tab keeps working, every other session is logged out", func(t *testing.T) {
		srv, _ := newServerFull(t, &fakePinger{}, catalogStore(), newFakeAuthStore(t), Options{SecureCookies: true})
		firefox := loginFrom(t, srv, "viewer", f.viewerPassword, firefoxLinux)
		chrome := loginFrom(t, srv, "viewer", f.viewerPassword, chromeLinux)
		admin := loginFrom(t, srv, "admin", f.adminPassword, firefoxLinux)

		r := call(t, srv, http.MethodPost, "/api/v1/me/password", firefox.access, passwordBody(f.viewerPassword, next))
		if r.status != http.StatusOK || r.header.Get("Cache-Control") != "no-store" {
			t.Fatalf("change = %d: %s", r.status, r.body)
		}
		ck := refreshCookieOf(t, r)
		if !ck.HttpOnly || !ck.Secure || ck.SameSite != http.SameSiteStrictMode || ck.Path != "/api/v1/auth" || ck.MaxAge < 29*24*3600 {
			t.Fatalf("cookie = %+v; want the login cookie's attributes", ck)
		}
		fresh := deviceOf(t, r)
		if fresh.sid == firefox.sid {
			t.Fatal("the password change did not start a new session")
		}
		if !fresh.refreshes(t, srv) {
			t.Fatal("the new session cannot refresh")
		}
		if firefox.refreshes(t, srv) || chrome.refreshes(t, srv) {
			t.Fatal("a session from before the change can still refresh")
		}
		if ss := sessionsOf(t, srv, fresh.access); len(ss) != 1 || ss[0].ID != fresh.sid || !ss[0].Current || ss[0].UserAgent != "Firefox on Linux" {
			t.Fatalf("sessions = %+v, want only the new one", ss)
		}
		if !admin.refreshes(t, srv) {
			t.Fatal("another user's session was revoked")
		}
		if r := loginAs(t, srv, "viewer", f.viewerPassword); r.status != http.StatusUnauthorized {
			t.Fatalf("login with the old password = %d", r.status)
		}
		if r := loginAs(t, srv, "viewer", next); r.status != http.StatusOK {
			t.Fatalf("login with the new password = %d", r.status)
		}
	})

	t.Run("refused requests", func(t *testing.T) {
		srv, _ := newServerFull(t, &fakePinger{}, catalogStore(), newFakeAuthStore(t), Options{SecureCookies: true})
		viewer := loginFrom(t, srv, "viewer", f.viewerPassword, firefoxLinux)
		for name, tt := range map[string]struct {
			contentType, body string
			status            int
			code              string
		}{
			"new == current":  {"application/json", passwordBody(f.viewerPassword, f.viewerPassword), 400, "invalid_request"},
			"new too short":   {"application/json", passwordBody(f.viewerPassword, "short"), 400, "invalid_request"},
			"no current":      {"application/json", passwordBody("", next), 400, "invalid_request"},
			"unknown field":   {"application/json", `{"current_password":"a","new_password":"b","user_id":"x"}`, 400, "invalid_request"},
			"not json":        {"application/json", "current=a", 400, "invalid_request"},
			"form encoded":    {"application/x-www-form-urlencoded", "current_password=a", 415, "unsupported_media_type"},
			"no content type": {"", passwordBody(f.viewerPassword, next), 415, "unsupported_media_type"},
		} {
			req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+"/api/v1/me/password", strings.NewReader(tt.body))
			if tt.contentType != "" {
				req.Header.Set("Content-Type", tt.contentType)
			}
			req.Header.Set("Authorization", "Bearer "+viewer.access)
			r := send(t, srv, req)
			if r.status != tt.status {
				t.Errorf("%s: %d, want %d: %s", name, r.status, tt.status, r.body)
				continue
			}
			assertErrorCode(t, r.body, tt.code)
			if len(r.header.Values("Set-Cookie")) != 0 {
				t.Errorf("%s: a refused change set a cookie", name)
			}
		}
		if r := call(t, srv, http.MethodGet, "/api/v1/me/password", f.viewerToken, ""); r.status != http.StatusMethodNotAllowed {
			t.Fatalf("GET password = %d", r.status)
		}
		// The password did not change.
		if r := loginAs(t, srv, "viewer", f.viewerPassword); r.status != http.StatusOK {
			t.Fatalf("login after refused changes = %d", r.status)
		}
	})

	t.Run("a wrong current password is a 400, five of them a 429 even with the right one", func(t *testing.T) {
		srv, _ := newServerFull(t, &fakePinger{}, catalogStore(), newFakeAuthStore(t), Options{SecureCookies: true})
		viewer := loginFrom(t, srv, "viewer", f.viewerPassword, firefoxLinux)
		admin := loginFrom(t, srv, "admin", f.adminPassword, firefoxLinux)
		for range auth.MaxLoginFailures {
			r := call(t, srv, http.MethodPost, "/api/v1/me/password", viewer.access, passwordBody(randomString(18), next))
			if r.status != http.StatusBadRequest {
				t.Fatalf("wrong current password = %d, want 400 (a 401 would make the client refresh and retry)", r.status)
			}
			assertErrorCode(t, r.body, "invalid_credentials")
		}
		r := call(t, srv, http.MethodPost, "/api/v1/me/password", viewer.access, passwordBody(f.viewerPassword, next))
		if r.status != http.StatusTooManyRequests || r.header.Get("Retry-After") != "900" {
			t.Fatalf("blocked = %d, Retry-After %q: %s", r.status, r.header.Get("Retry-After"), r.body)
		}
		assertErrorCode(t, r.body, "rate_limited")
		// Per user: the admin can still change theirs, and the viewer can
		// still log in (a separate throttle).
		if r := call(t, srv, http.MethodPost, "/api/v1/me/password", admin.access, passwordBody(f.adminPassword, next)); r.status != http.StatusOK {
			t.Fatalf("another user = %d", r.status)
		}
		if r := loginAs(t, srv, "viewer", f.viewerPassword); r.status != http.StatusOK {
			t.Fatalf("login while password changes are blocked = %d", r.status)
		}
	})

	t.Run("store failure", func(t *testing.T) {
		as := newFakeAuthStore(t)
		srv, _ := newServerFull(t, &fakePinger{}, catalogStore(), as, Options{SecureCookies: true})
		as.err = errors.New("db down")
		if r := call(t, srv, http.MethodPost, "/api/v1/me/password", f.viewerToken, passwordBody(f.viewerPassword, next)); r.status != http.StatusInternalServerError {
			t.Fatalf("store failure = %d", r.status)
		}
	})
}

// A revoked session's access token still verifies for up to 15 minutes; the
// account endpoints check the session itself first, so in that window it
// can neither manage sessions nor change the password.
func TestRevokedSessionLosesTheAccountEndpoints(t *testing.T) {
	f := fixtures(t)
	srv, _ := newServerFull(t, &fakePinger{}, catalogStore(), newFakeAuthStore(t), Options{SecureCookies: true})

	t.Run("revoked from another session: 401 on all four, nothing touched", func(t *testing.T) {
		a := loginFrom(t, srv, "viewer", f.viewerPassword, firefoxLinux)
		b := loginFrom(t, srv, "viewer", f.viewerPassword, chromeLinux)
		if r := call(t, srv, http.MethodDelete, "/api/v1/me/sessions/"+a.sid, b.access, ""); r.status != http.StatusNoContent {
			t.Fatalf("B revokes A = %d", r.status)
		}
		for _, c := range []struct{ method, path, body string }{
			{http.MethodPost, "/api/v1/me/password", passwordBody(f.viewerPassword, randomString(18))},
			{http.MethodGet, "/api/v1/me/sessions", ""},
			{http.MethodDelete, "/api/v1/me/sessions/" + b.sid, ""},
			{http.MethodPost, "/api/v1/me/sessions/revoke-others", ""},
		} {
			r := call(t, srv, c.method, c.path, a.access, c.body)
			if r.status != http.StatusUnauthorized {
				t.Errorf("%s %s with A's still-valid access token = %d, want 401", c.method, c.path, r.status)
				continue
			}
			assertErrorCode(t, r.body, "unauthorized")
			if len(r.header.Values("Set-Cookie")) != 0 {
				t.Errorf("%s %s: a refused request set a cookie", c.method, c.path)
			}
		}
		// B is untouched: still its session, still refreshes, and the
		// password did not change.
		if ss := sessionsOf(t, srv, b.access); len(ss) != 1 || ss[0].ID != b.sid || !ss[0].Current {
			t.Fatalf("B's sessions = %+v", ss)
		}
		if !b.refreshes(t, srv) {
			t.Fatal("B's session died")
		}
		if r := loginAs(t, srv, "viewer", f.viewerPassword); r.status != http.StatusOK {
			t.Fatalf("the password changed: login = %d", r.status)
		}
		// Endpoints outside the account set keep the plain token check.
		if r := call(t, srv, http.MethodGet, "/api/v1/me", a.access, ""); r.status != http.StatusOK {
			t.Fatalf("GET /me with A's token = %d; only the four session/password endpoints check the session", r.status)
		}
	})

	t.Run("after a password change from B, A's old token cannot revoke B's new session", func(t *testing.T) {
		a := loginFrom(t, srv, "viewer", f.viewerPassword, firefoxLinux)
		b := loginFrom(t, srv, "viewer", f.viewerPassword, chromeLinux)
		r := call(t, srv, http.MethodPost, "/api/v1/me/password", b.access, passwordBody(f.viewerPassword, randomString(18)))
		if r.status != http.StatusOK {
			t.Fatalf("B changes the password = %d: %s", r.status, r.body)
		}
		fresh := deviceOf(t, r)
		for _, c := range []struct{ method, path string }{
			{http.MethodDelete, "/api/v1/me/sessions/" + fresh.sid},
			{http.MethodPost, "/api/v1/me/sessions/revoke-others"},
		} {
			if r := call(t, srv, c.method, c.path, a.access, ""); r.status != http.StatusUnauthorized {
				t.Fatalf("%s %s with A's old token = %d, want 401", c.method, c.path, r.status)
			}
		}
		if !fresh.refreshes(t, srv) {
			t.Fatal("B's new session was revoked by A's old token")
		}
	})
}
