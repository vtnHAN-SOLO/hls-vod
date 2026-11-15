package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/vtnHAN-SOLO/hls-vod/internal/auth"
	"github.com/vtnHAN-SOLO/hls-vod/internal/config"
	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
	"github.com/vtnHAN-SOLO/hls-vod/internal/oidctest"
	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
)

// browser follows redirects by hand and keeps cookies, as a browser does.
// The SPA's origin (PUBLIC_BASE_URL, where the vite proxy would forward
// /api) is mapped to the api.
type browser struct {
	t    *testing.T
	c    *http.Client
	base string
	spa  string
}

func newBrowser(t *testing.T, e *e2e) *browser {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &browser{t: t, base: e.base, spa: strings.TrimRight(e.cfg.PublicBaseURL, "/"), c: &http.Client{
		Jar: jar, Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// reply is what a request got back: no body is needed.
type reply struct {
	StatusCode int
	Header     http.Header
}

func (b *browser) do(method, rawURL, token string) reply {
	b.t.Helper()
	if strings.HasPrefix(rawURL, b.spa+"/api/") {
		rawURL = b.base + strings.TrimPrefix(rawURL, b.spa)
	}
	if strings.HasPrefix(rawURL, "/") {
		rawURL = b.base + rawURL
	}
	req, err := http.NewRequestWithContext(b.t.Context(), method, rawURL, nil)
	if err != nil {
		b.t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if method == http.MethodPost {
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
	}
	resp, err := b.c.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return reply{StatusCode: resp.StatusCode, Header: resp.Header}
}

// through follows redirects from rawURL until a page of the SPA (a path
// that is not /api/ and not the provider), and returns it.
func (b *browser) through(rawURL string) string {
	b.t.Helper()
	for range 5 {
		resp := b.do(http.MethodGet, rawURL, "")
		if resp.StatusCode != http.StatusFound {
			b.t.Fatalf("GET %s = %d, want a redirect", rawURL, resp.StatusCode)
		}
		loc := resp.Header.Get("Location")
		if strings.HasPrefix(loc, "/") && !strings.HasPrefix(loc, "/api/") {
			return loc
		}
		rawURL = loc
	}
	b.t.Fatal("too many redirects")
	return ""
}

// signIn is "Continue with Google": it returns the SPA page it ends on.
func (b *browser) signIn() string {
	b.t.Helper()
	return b.through("/api/v1/auth/google/start")
}

// access is what the done page does: refresh with the cookie.
func (b *browser) access() (string, bool) {
	b.t.Helper()
	req, _ := http.NewRequestWithContext(b.t.Context(), http.MethodPost, b.base+"/api/v1/auth/refresh", nil)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	resp, err := b.c.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var s struct {
		AccessToken string `json:"access_token"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&s)
	return s.AccessToken, resp.StatusCode == http.StatusOK
}

func (b *browser) me(token string) map[string]any {
	b.t.Helper()
	status, me := getJSON(b.t, b.base+"/api/v1/me", token)
	if status != http.StatusOK {
		b.t.Fatalf("me = %d %v", status, me)
	}
	return me
}

// link is Connect Google on the account page: the api's url, then Google.
func (b *browser) link(token, password string) (int, string) {
	b.t.Helper()
	req, _ := http.NewRequestWithContext(b.t.Context(), http.MethodPost, b.base+"/api/v1/me/google/link",
		strings.NewReader(`{"current_password":"`+password+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := b.c.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		URL string `json:"url"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, ""
	}
	return resp.StatusCode, b.through(out.URL)
}

func googleE2E(t *testing.T, open bool) (*e2e, *oidctest.Provider, *store.Store) {
	t.Helper()
	p := oidctest.New(t)
	e := startE2E(t, func(c *config.API) {
		c.GoogleClientID, c.GoogleClientSecret, c.GoogleIssuer = p.ClientID, p.ClientSecret, p.URL
		c.RegistrationOpen = open
	})
	st, err := store.New(t.Context(), e.db.DSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	return e, p, st
}

// account creates a user with a password; verified when email is not "".
func account(t *testing.T, st *store.Store, username, email, password string, verify bool) model.User {
	t.Helper()
	hash, err := auth.NewHasher(1).Hash(t.Context(), password)
	if err != nil {
		t.Fatal(err)
	}
	vh := make([]byte, 32)
	copy(vh, username)
	r, err := st.Register(t.Context(), store.NewAccount{Username: username, Email: email, PasswordHash: hash, VerifyHash: vh, VerifyTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if !verify {
		return r.User
	}
	u, _, err := st.VerifyEmail(t.Context(), vh, store.NewRefresh{Hash: vh, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func login(t *testing.T, e *e2e, username, password string) (string, int) {
	t.Helper()
	r := post(t, e.base+"/api/v1/auth/login", `{"username":"`+username+`","password":"`+password+`"}`, nil)
	token, _ := r.json["access_token"].(string)
	return token, r.status
}

func TestGoogleSignInEndToEnd(t *testing.T) {
	e, p, st := googleE2E(t, true)
	if status, c := getJSON(t, e.base+"/api/v1/auth/config", ""); status != http.StatusOK || c["google_enabled"] != true {
		t.Fatalf("config = %v", c)
	}

	// d, then a: a new Google-only account, then the same one.
	p.Next(oidctest.Identity{Subject: "sub-gina", Email: "Gina.Rossi@Example.com", EmailVerified: true}, oidctest.SpoilNone)
	b := newBrowser(t, e)
	if page := b.signIn(); page != "/auth/google/done" {
		t.Fatalf("first sign-in ends on %s", page)
	}
	token, ok := b.access()
	if !ok {
		t.Fatal("no session after the sign-in")
	}
	me := b.me(token)
	gina, _ := me["username"].(string)
	if !strings.HasPrefix(gina, "gina.rossi-") || me["has_password"] != false || me["email"] != "gina.rossi@example.com" || me["google"] == nil {
		t.Fatalf("new account = %v", me)
	}
	b2 := newBrowser(t, e)
	p.Next(oidctest.Identity{Subject: "sub-gina", Email: "gina.new@example.com", EmailVerified: true}, oidctest.SpoilNone)
	if page := b2.signIn(); page != "/auth/google/done" {
		t.Fatalf("second sign-in ends on %s", page)
	}
	token2, _ := b2.access()
	if again := b2.me(token2); again["id"] != me["id"] || again["email"] != "gina.rossi@example.com" {
		t.Fatalf("second sign-in = %v; want the same account, its email unchanged", again)
	}

	// A Google-only account has no password: password login is the usual
	// invalid credentials, and Google cannot be removed.
	if _, status := login(t, e, gina, randomSecret(18)); status != http.StatusUnauthorized {
		t.Fatalf("password login for a Google-only account = %d", status)
	}
	if r := b.do(http.MethodDelete, "/api/v1/me/google", token); r.StatusCode != http.StatusConflict {
		t.Fatalf("unlink without a password = %d", r.StatusCode)
	}

	// Set password: a reset link to the account's own address; then the
	// password works, every session ended, and Google still works.
	if r := b.do(http.MethodPost, "/api/v1/me/password/set-link", token); r.StatusCode != http.StatusAccepted {
		t.Fatalf("set-link = %d", r.StatusCode)
	}
	m := e.resend.next(t)
	if to(m) != "gina.rossi@example.com" {
		t.Fatalf("set-link mailed %v", m)
	}
	password := randomSecret(18)
	if r := post(t, e.base+"/api/v1/auth/reset-password",
		`{"token":"`+linkToken(t, m, "/reset-password")+`","new_password":"`+password+`"}`, nil); r.status != http.StatusNoContent {
		t.Fatalf("set the password = %d %s", r.status, r.body)
	}
	if _, ok := b.access(); ok {
		t.Fatal("a session survived setting the password")
	}
	if _, status := login(t, e, gina, password); status != http.StatusOK {
		t.Fatalf("password login after set = %d", status)
	}
	p.Next(oidctest.Identity{Subject: "sub-gina", Email: "gina.rossi@example.com", EmailVerified: true}, oidctest.SpoilNone)
	b3 := newBrowser(t, e)
	if page := b3.signIn(); page != "/auth/google/done" {
		t.Fatalf("Google after set = %s", page)
	}
	token3, _ := b3.access()
	if r := b3.do(http.MethodPost, "/api/v1/me/password/set-link", token3); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("set-link with a password = %d", r.StatusCode)
	}

	// b: a verified account with the email is linked, and told so.
	tester := account(t, st, "tester1", "tester1@example.com", randomSecret(18), true)
	p.Next(oidctest.Identity{Subject: "sub-tester1", Email: "tester1@example.com", EmailVerified: true}, oidctest.SpoilNone)
	bt := newBrowser(t, e)
	if page := bt.signIn(); page != "/auth/google/done" {
		t.Fatalf("b = %s", page)
	}
	tt, _ := bt.access()
	if me := bt.me(tt); me["id"] != tester.ID || me["has_password"] != true {
		t.Fatalf("b signed in as %v", me)
	}
	if n := e.resend.next(t); to(n) != "tester1@example.com" || !strings.Contains(n["subject"].(string), "Google") {
		t.Fatalf("notice = %v", n)
	}

	// c: an unverified account with the email is deleted, never linked.
	squat := account(t, st, "squatter", "victim@example.com", randomSecret(18), false)
	p.Next(oidctest.Identity{Subject: "sub-victim", Email: "victim@example.com", EmailVerified: true}, oidctest.SpoilNone)
	bv := newBrowser(t, e)
	if page := bv.signIn(); page != "/auth/google/done" {
		t.Fatalf("c = %s", page)
	}
	tv, _ := bv.access()
	if me := bv.me(tv); me["id"] == squat.ID || me["username"] == "squatter" || me["has_password"] != false {
		t.Fatalf("c signed in as %v; want a new account", me)
	}
	if _, err := st.UserByUsername(t.Context(), "squatter"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the unverified account = %v; want deleted", err)
	}
}

func TestGoogleCallbackRefusals(t *testing.T) {
	e, p, _ := googleE2E(t, true)
	ok := oidctest.Identity{Subject: "sub-x", Email: "x@example.com", EmailVerified: true}

	t.Run("ID token", func(t *testing.T) {
		for spoil, want := range map[oidctest.Spoil]string{
			oidctest.SpoilSignature: "token", oidctest.SpoilAudience: "token", oidctest.SpoilIssuer: "token",
			oidctest.SpoilExpired: "token", oidctest.SpoilNonce: "token",
		} {
			p.Next(ok, spoil)
			if page := newBrowser(t, e).signIn(); page != "/login?google_error="+want {
				t.Errorf("spoil %d = %s", spoil, page)
			}
		}
		p.Next(oidctest.Identity{Subject: "sub-y", Email: "y@example.com", EmailVerified: false}, oidctest.SpoilNone)
		if page := newBrowser(t, e).signIn(); page != "/login?google_error=email_unverified" {
			t.Errorf("email_verified=false = %s", page)
		}
	})

	// toCallback starts a sign-in and stops at the callback URL Google
	// would send the browser to.
	toCallback := func(b *browser) string {
		t.Helper()
		p.Next(ok, oidctest.SpoilNone)
		start := b.do(http.MethodGet, "/api/v1/auth/google/start", "")
		back := b.do(http.MethodGet, start.Header.Get("Location"), "")
		return back.Header.Get("Location")
	}
	t.Run("state", func(t *testing.T) {
		b := newBrowser(t, e)
		cb := toCallback(b)
		if page := newBrowser(t, e).through(cb); page != "/login?google_error=state" {
			t.Errorf("missing cookie = %s", page)
		}
		u, _ := url.Parse(cb)
		q := u.Query()
		// Change the first character: always to a different one (a state
		// that already starts with "x" would otherwise stay the same).
		st := q.Get("state")
		first := "x"
		if st[0] == 'x' {
			first = "y"
		}
		q.Set("state", first+st[1:])
		u.RawQuery = q.Encode()
		if page := b.through(u.String()); page != "/login?google_error=state" {
			t.Errorf("cookie and parameter differ = %s", page)
		}

		b = newBrowser(t, e)
		cb = toCallback(b)
		if _, err := e.db.Pool.Exec(t.Context(), `UPDATE oauth_states SET expires_at = now() - interval '1 second'`); err != nil {
			t.Fatal(err)
		}
		if page := b.through(cb); page != "/login?google_error=state" {
			t.Errorf("expired = %s", page)
		}

		b = newBrowser(t, e)
		cb = toCallback(b)
		state := strings.Split(strings.Split(cb, "state=")[1], "&")[0]
		if page := b.through(cb); page != "/auth/google/done" {
			t.Fatalf("first use = %s", page)
		}
		replay := newBrowser(t, e)
		ru, _ := url.Parse(e.base)
		replay.c.Jar.SetCookies(ru, []*http.Cookie{{Name: "google_state", Value: state, Path: "/api/v1/auth/google"}}) // #nosec G124 -- a request cookie the test client sends back
		if page := replay.through(cb); page != "/login?google_error=state" {
			t.Errorf("reused = %s", page)
		}
	})

	t.Run("cancelled at Google", func(t *testing.T) {
		b := newBrowser(t, e)
		cb := toCallback(b)
		u, _ := url.Parse(cb)
		q := u.Query()
		q.Del("code")
		q.Set("error", "access_denied")
		u.RawQuery = q.Encode()
		if page := b.through(u.String()); page != "/login?google_error=denied" {
			t.Errorf("denied = %s", page)
		}
	})
}

// Registration closed blocks a new account (d) only.
func TestGoogleRegistrationClosed(t *testing.T) {
	e, p, st := googleE2E(t, false)
	p.Next(oidctest.Identity{Subject: "sub-new", Email: "new@example.com", EmailVerified: true}, oidctest.SpoilNone)
	if page := newBrowser(t, e).signIn(); page != "/login?google_error=registration_closed" {
		t.Fatalf("new account while closed = %s", page)
	}
	u := account(t, st, "kate", "kate@example.com", randomSecret(18), true)
	for range 2 { // b, then a
		p.Next(oidctest.Identity{Subject: "sub-kate", Email: "kate@example.com", EmailVerified: true}, oidctest.SpoilNone)
		b := newBrowser(t, e)
		if page := b.signIn(); page != "/auth/google/done" {
			t.Fatalf("existing account while closed = %s", page)
		}
		token, _ := b.access()
		if me := b.me(token); me["id"] != u.ID {
			t.Fatalf("signed in as %v", me)
		}
	}
}

func TestGoogleLinkEndToEnd(t *testing.T) {
	e, p, st := googleE2E(t, true)
	pw := randomSecret(18)
	lars := account(t, st, "lars", "lars@example.com", pw, true)
	b := newBrowser(t, e)
	token, status := login(t, e, "lars", pw)
	if status != http.StatusOK {
		t.Fatal(status)
	}

	p.Next(oidctest.Identity{Subject: "sub-lars", Email: "lars@gmail.example", EmailVerified: true}, oidctest.SpoilNone)
	if status, page := b.link(token, pw); status != http.StatusOK || page != "/account?google=linked" {
		t.Fatalf("link = %d %s", status, page)
	}
	if n := e.resend.next(t); to(n) != "lars@example.com" {
		t.Fatalf("notice = %v", n)
	}
	me := b.me(token)
	if g, _ := me["google"].(map[string]any); g == nil || g["email_at_link"] != "lars@gmail.example" {
		t.Fatalf("me = %v", me)
	}
	// Google now lands on lars, though the Google address differs.
	p.Next(oidctest.Identity{Subject: "sub-lars", Email: "lars@gmail.example", EmailVerified: true}, oidctest.SpoilNone)
	bg := newBrowser(t, e)
	if page := bg.signIn(); page != "/auth/google/done" {
		t.Fatal(page)
	}
	tg, _ := bg.access()
	if me := bg.me(tg); me["id"] != lars.ID {
		t.Fatalf("Google sign-in after link = %v", me)
	}

	// A second Google account for lars; lars's Google account for another user.
	p.Next(oidctest.Identity{Subject: "sub-lars-2", Email: "lars2@gmail.example", EmailVerified: true}, oidctest.SpoilNone)
	if _, page := b.link(token, pw); page != "/account?google_error=already_linked" {
		t.Fatalf("second Google = %s", page)
	}
	pw2 := randomSecret(18)
	_ = account(t, st, "mia", "mia@example.com", pw2, true)
	tm, _ := login(t, e, "mia", pw2)
	p.Next(oidctest.Identity{Subject: "sub-lars", Email: "lars@gmail.example", EmailVerified: true}, oidctest.SpoilNone)
	if _, page := newBrowser(t, e).link(tm, pw2); page != "/account?google_error=linked_elsewhere" {
		t.Fatalf("linked elsewhere = %s", page)
	}

	// Unlink, with a password: fine.
	if r := b.do(http.MethodDelete, "/api/v1/me/google", token); r.StatusCode != http.StatusNoContent {
		t.Fatalf("unlink = %d", r.StatusCode)
	}

	// Wrong passwords count toward the shared throttle: 4 here + 1 on the
	// password change = blocked on both.
	for i := range auth.MaxLoginFailures - 1 {
		if status, _ := b.link(tm, "wrong"); status != http.StatusBadRequest {
			t.Fatalf("wrong %d = %d", i+1, status)
		}
	}
	if r := postAuthed(t, e.base+"/api/v1/me/password", tm, `{"current_password":"wrong","new_password":"`+randomSecret(18)+`"}`); r != http.StatusBadRequest {
		t.Fatalf("wrong on /me/password = %d", r)
	}
	if status, _ := b.link(tm, pw2); status != http.StatusTooManyRequests {
		t.Fatalf("link after 5 wrong = %d", status)
	}
}

func postAuthed(t *testing.T, rawURL, token, body string) int {
	t.Helper()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, rawURL, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}
