package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vtnHAN-SOLO/hls-vod/internal/auth"
	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
	"github.com/vtnHAN-SOLO/hls-vod/internal/service"
	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
)

// fakeGoogle stands for the Google service: the real flow, against a fake
// OpenID provider and Postgres, is tested in cmd/api. Here: the HTTP side.
type fakeGoogle struct {
	mu       sync.Mutex
	startErr error
	linkErr  error
	result   service.CallbackResult
	err      error
	got      service.CallbackInput
}

func (g *fakeGoogle) StartLogin(context.Context) (string, string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return "https://accounts.example/auth?state=s", "state-value", g.startErr
}

func (g *fakeGoogle) StartLink(_ context.Context, _ service.Principal, pw string) (string, string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if pw == "" {
		return "", "", &service.ValidationError{Field: "current_password", Message: "is required"}
	}
	return "https://accounts.example/auth?state=l", "link-state", g.linkErr
}

func (g *fakeGoogle) Callback(_ context.Context, in service.CallbackInput) (service.CallbackResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.got = in
	return g.result, g.err
}

// noFollow sends req without following redirects; checked against the spec.
func noFollow(t *testing.T, srv *httptest.Server, req *http.Request) response {
	t.Helper()
	c := *srv.Client()
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	checkSpec(t, req, resp, nil)
	return response{status: resp.StatusCode, header: resp.Header}
}

func navigate(t *testing.T, srv *httptest.Server, path string, cookies ...*http.Cookie) response {
	t.Helper()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+path, nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	return noFollow(t, srv, req)
}

func cookieOf(r response, name string) *http.Cookie {
	for _, c := range (&http.Response{Header: r.header}).Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func TestGoogleStartAndCallback(t *testing.T) {
	g := &fakeGoogle{}
	e := newRegEnv(t, regSetup{google: g})

	r := navigate(t, e.srv, "/api/v1/auth/google/start")
	c := cookieOf(r, GoogleStateCookie)
	if r.status != http.StatusFound || r.header.Get("Location") != "https://accounts.example/auth?state=s" || c == nil ||
		c.Value != "state-value" || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/api/v1/auth/google" ||
		c.MaxAge != 600 || !c.Secure {
		t.Fatalf("start = %d %v, cookie %+v", r.status, r.header, c)
	}

	// A login: the refresh cookie (never a token in the URL), then the done page.
	g.result = service.CallbackResult{Intent: store.IntentLogin, Session: service.Session{
		RefreshToken: "refresh-value", RefreshExpires: time.Now().Add(time.Hour),
		User: model.User{ID: viewerID, Username: "viewer", Role: model.RoleUser}}}
	r = navigate(t, e.srv, "/api/v1/auth/google/callback?state=state-value&code=the-code", &http.Cookie{Name: GoogleStateCookie, Value: "state-value"}) // #nosec G124 -- a request cookie the test client sends
	if r.status != http.StatusFound || r.header.Get("Location") != "/auth/google/done" {
		t.Fatalf("callback = %d %v", r.status, r.header)
	}
	if rc := cookieOf(r, RefreshCookie); rc == nil || rc.Value != "refresh-value" || rc.SameSite != http.SameSiteStrictMode {
		t.Fatalf("refresh cookie = %+v", rc)
	}
	if sc := cookieOf(r, GoogleStateCookie); sc == nil || sc.MaxAge >= 0 {
		t.Fatalf("the state cookie was not cleared: %+v", sc)
	}
	if g.got.State != "state-value" || g.got.CookieState != "state-value" || g.got.Code != "the-code" {
		t.Fatalf("callback input = %+v", g.got)
	}

	// A link goes back to the account page and changes no cookie.
	g.result = service.CallbackResult{Intent: store.IntentLink}
	r = navigate(t, e.srv, "/api/v1/auth/google/callback?state=x&code=y")
	if r.status != http.StatusFound || r.header.Get("Location") != "/account?google=linked" || cookieOf(r, RefreshCookie) != nil {
		t.Fatalf("link callback = %d %v", r.status, r.header)
	}

	// Failures: the page of the intent, a short code, nothing else.
	for _, tt := range []struct {
		err  error
		want string
	}{
		{&service.GoogleError{Code: service.GoogleErrState, Intent: store.IntentLogin}, "/login?google_error=state"},
		{&service.GoogleError{Code: service.GoogleErrLinkedElsewhere, Intent: store.IntentLink}, "/account?google_error=linked_elsewhere"},
		{errors.New("boom"), "/login?google_error=server"},
	} {
		g.err = tt.err
		r = navigate(t, e.srv, "/api/v1/auth/google/callback?error=access_denied&state=x")
		if r.status != http.StatusFound || r.header.Get("Location") != tt.want || cookieOf(r, RefreshCookie) != nil {
			t.Errorf("%v: %d %s, want %s", tt.err, r.status, r.header.Get("Location"), tt.want)
		}
	}
	if g.got.ProviderError != "access_denied" {
		t.Fatalf("Google's error parameter was not passed on: %+v", g.got)
	}

	g.startErr = errors.New("discovery failed")
	if r := navigate(t, e.srv, "/api/v1/auth/google/start"); r.header.Get("Location") != "/login?google_error=unavailable" || cookieOf(r, GoogleStateCookie) != nil {
		t.Fatalf("start, Google unreachable = %v", r.header)
	}
	for _, path := range []string{"/api/v1/auth/google/start", "/api/v1/auth/google/callback"} {
		if r := doFull(t, e.srv, http.MethodPost, path); r.status != http.StatusMethodNotAllowed {
			t.Errorf("POST %s = %d", path, r.status)
		}
	}
}

func TestGoogleDisabled(t *testing.T) {
	e := newRegEnv(t, regSetup{})
	for _, path := range []string{"/api/v1/auth/google/start", "/api/v1/auth/google/callback?state=x"} {
		if r := navigate(t, e.srv, path); r.status != http.StatusFound || r.header.Get("Location") != "/login?google_error=disabled" {
			t.Errorf("%s = %d %v", path, r.status, r.header)
		}
	}
	viewer := loginFrom(t, e.srv, "viewer", fixtures(t).viewerPassword, "")
	if r := call(t, e.srv, http.MethodPost, "/api/v1/me/google/link", viewer.access, `{"current_password":"x"}`); r.status != http.StatusNotFound {
		t.Fatalf("link while disabled = %d", r.status)
	}
}

func TestGoogleLinkEndpoint(t *testing.T) {
	g := &fakeGoogle{}
	e := newRegEnv(t, regSetup{google: g})
	viewer := loginFrom(t, e.srv, "viewer", fixtures(t).viewerPassword, "")
	link := func(body string) response {
		return call(t, e.srv, http.MethodPost, "/api/v1/me/google/link", viewer.access, body)
	}

	r := link(`{"current_password":"pw"}`)
	if r.status != http.StatusOK || !strings.Contains(string(r.body), `"url":"https://accounts.example/auth?state=l"`) ||
		cookieOf(r, GoogleStateCookie) == nil || cookieOf(r, GoogleStateCookie).Value != "link-state" {
		t.Fatalf("link = %d %s %v", r.status, r.body, r.header)
	}
	for _, tt := range []struct {
		err    error
		status int
		code   string
	}{
		{service.ErrWrongPassword, http.StatusBadRequest, "invalid_credentials"},
		{service.ErrUnauthenticated, http.StatusUnauthorized, "unauthorized"},
		{&service.ThrottledError{RetryAfter: time.Minute}, http.StatusTooManyRequests, "rate_limited"},
		{errors.New("db down"), http.StatusInternalServerError, "internal"},
	} {
		g.linkErr = tt.err
		if r := link(`{"current_password":"pw"}`); r.status != tt.status || errorCode(t, r) != tt.code {
			t.Errorf("%v = %d %s", tt.err, r.status, r.body)
		}
	}
	g.linkErr = nil
	if r := link(`{"current_password":""}`); r.status != http.StatusBadRequest || errorCode(t, r) != "invalid_request" {
		t.Fatalf("no password = %d %s", r.status, r.body)
	}
	if r := link(`{"password":"x"}`); r.status != http.StatusBadRequest {
		t.Fatalf("unknown field = %d", r.status)
	}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, e.srv.URL+"/api/v1/me/google/link", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("Authorization", "Bearer "+viewer.access)
	if r := send(t, e.srv, req); r.status != http.StatusUnsupportedMediaType {
		t.Fatalf("text/plain = %d", r.status)
	}
	if r := doAs(t, e.srv, http.MethodGet, "/api/v1/me/google/link", viewer.access); r.status != http.StatusMethodNotAllowed {
		t.Fatalf("GET = %d", r.status)
	}
	if r := postJSON(t, e.srv, "/api/v1/me/google/link", "application/json", `{"current_password":"pw"}`, false); r.status != http.StatusUnauthorized {
		t.Fatalf("no token = %d", r.status)
	}
}

func TestGoogleUnlinkAndMe(t *testing.T) {
	e := newRegEnv(t, regSetup{})
	viewer := loginFrom(t, e.srv, "viewer", fixtures(t).viewerPassword, "")
	unlink := func(access string) response {
		return call(t, e.srv, http.MethodDelete, "/api/v1/me/google", access, "")
	}
	if r := unlink(viewer.access); r.status != http.StatusNotFound || errorCode(t, r) != "not_found" {
		t.Fatalf("nothing linked = %d %s", r.status, r.body)
	}

	gmail := "viewer@gmail.example"
	e.as.mu.Lock()
	e.as.google[viewerID] = model.Identity{Provider: "google", Subject: "sub-v", EmailAtLink: &gmail, CreatedAt: time.Now()}
	e.as.mu.Unlock()
	var me accountJSON
	decode(t, call(t, e.srv, http.MethodGet, "/api/v1/me", viewer.access, "").body, &me)
	if !me.HasPassword || me.Google == nil || me.Google.EmailAtLink == nil || *me.Google.EmailAtLink != gmail {
		t.Fatalf("me = %+v", me)
	}
	if r := unlink(viewer.access); r.status != http.StatusNoContent {
		t.Fatalf("unlink = %d %s", r.status, r.body)
	}
	decode(t, call(t, e.srv, http.MethodGet, "/api/v1/me", viewer.access, "").body, &me)
	if me.Google != nil {
		t.Fatalf("me after unlink = %+v", me)
	}

	// A Google-only account cannot remove its only way in.
	gina := googleOnly(t, e, "gina")
	e.as.mu.Lock()
	e.as.google[gina.userID] = model.Identity{Provider: "google", Subject: "sub-g", CreatedAt: time.Now()}
	e.as.mu.Unlock()
	if r := unlink(gina.access); r.status != http.StatusConflict || errorCode(t, r) != "last_login_method" {
		t.Fatalf("unlink, no password = %d %s", r.status, r.body)
	}
	decode(t, call(t, e.srv, http.MethodGet, "/api/v1/me", gina.access, "").body, &me)
	if me.HasPassword || me.Google == nil || me.Google.EmailAtLink != nil {
		t.Fatalf("Google-only me = %+v", me)
	}

	if r := unlink(""); r.status != http.StatusUnauthorized {
		t.Fatalf("no token = %d", r.status)
	}
	if r := doAs(t, e.srv, http.MethodGet, "/api/v1/me/google", viewer.access); r.status != http.StatusMethodNotAllowed {
		t.Fatalf("GET = %d", r.status)
	}
	e.as.err = errors.New("db down")
	if r := unlink(viewer.access); r.status != http.StatusInternalServerError {
		t.Fatalf("store down = %d", r.status)
	}
}

// googleOnlyUser is a user without a password, with a live session.
type googleOnlyUser struct{ userID, access string }

func googleOnly(t *testing.T, e *regEnv, name string) googleOnlyUser {
	t.Helper()
	email, now := name+"@example.com", time.Now()
	u := model.User{ID: model.NewID(), Username: name, Role: model.RoleUser, Email: &email, EmailVerifiedAt: &now, CreatedAt: now}
	e.as.mu.Lock()
	e.as.users[name] = u
	family := model.NewID()
	_, hash, _ := auth.NewRefreshToken()
	e.as.insert(u.ID, family, store.NewRefresh{Hash: hash, TTL: time.Hour}, "")
	e.as.mu.Unlock()
	access, _, err := fixtures(t).signer.Issue(u.ID, "user", family)
	if err != nil {
		t.Fatal(err)
	}
	return googleOnlyUser{userID: u.ID, access: access}
}

func TestPasswordSetLink(t *testing.T) {
	e := newRegEnv(t, regSetup{})
	gina := googleOnly(t, e, "gina")
	setLink := func(access string) response {
		return call(t, e.srv, http.MethodPost, "/api/v1/me/password/set-link", access, "")
	}

	r := setLink(gina.access)
	if r.status != http.StatusAccepted || r.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("set-link = %d %s", r.status, r.body)
	}
	mails := e.mailer.take()
	if len(mails) != 1 || mails[0].To != "gina@example.com" {
		t.Fatalf("mails = %+v; want one, to the account's own address", mails)
	}
	token := resetToken(t, mails)
	newPassword := randomString(18)
	if r := e.reset(t, token, newPassword); r.status != http.StatusNoContent {
		t.Fatalf("reset = %d %s", r.status, r.body)
	}
	if r := e.login(t, "gina", newPassword, ""); r.status != http.StatusOK {
		t.Fatalf("password login after set = %d %s", r.status, r.body)
	}

	viewer := loginFrom(t, e.srv, "viewer", fixtures(t).viewerPassword, "")
	if r := setLink(viewer.access); r.status != http.StatusBadRequest || errorCode(t, r) != "invalid_request" {
		t.Fatalf("account with a password = %d %s", r.status, r.body)
	}
	if len(e.mailer.take()) != 0 {
		t.Fatal("a refused set-link mailed")
	}
	if r := setLink(gina.access); r.status != http.StatusUnauthorized {
		t.Fatalf("after the reset ended the session = %d", r.status)
	}
	if r := doAs(t, e.srv, http.MethodGet, "/api/v1/me/password/set-link", viewer.access); r.status != http.StatusMethodNotAllowed {
		t.Fatalf("GET = %d", r.status)
	}
	e.as.err = errors.New("db down")
	if r := setLink(viewer.access); r.status != http.StatusInternalServerError {
		t.Fatalf("store down = %d", r.status)
	}
}
