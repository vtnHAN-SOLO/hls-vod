package handler

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/vtnHAN-SOLO/hls-vod/internal/auth"
	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
	"github.com/vtnHAN-SOLO/hls-vod/internal/service"
	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
)

const (
	adminID  = "a0000000-0000-4000-8000-000000000001"
	viewerID = "b0000000-0000-4000-8000-000000000002"
)

// testFixtures are generated once per test binary: argon2id is deliberately
// slow, and every secret is random (never a literal credential).
type testFixtures struct {
	key            []byte
	signer         *auth.Signer
	media          *service.MediaSigner
	mediaKey       string
	hasher         *auth.Hasher
	adminPassword  string
	viewerPassword string
	adminHash      string
	viewerHash     string
	adminToken     string
	viewerToken    string
}

var (
	fixturesOnce sync.Once
	fixturesVal  testFixtures
	fixturesErr  error
)

func randomString(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func fixtures(t *testing.T) *testFixtures {
	t.Helper()
	fixturesOnce.Do(func() {
		f := &fixturesVal
		f.key = []byte(randomString(48))
		if f.signer, fixturesErr = auth.NewSigner(f.key); fixturesErr != nil {
			return
		}
		f.mediaKey = randomString(48)
		if f.media, fixturesErr = service.NewMediaSigner(f.mediaKey, 0); fixturesErr != nil {
			return
		}
		f.hasher = auth.NewHasher(2)
		f.adminPassword, f.viewerPassword = randomString(18), randomString(18)
		ctx := context.Background()
		if f.adminHash, fixturesErr = f.hasher.Hash(ctx, f.adminPassword); fixturesErr != nil {
			return
		}
		if f.viewerHash, fixturesErr = f.hasher.Hash(ctx, f.viewerPassword); fixturesErr != nil {
			return
		}
		// Sessions that exist in no store: enough for every endpoint that
		// does not look the session up.
		if f.adminToken, _, fixturesErr = f.signer.Issue(adminID, "admin", model.NewID()); fixturesErr != nil {
			return
		}
		f.viewerToken, _, fixturesErr = f.signer.Issue(viewerID, "user", model.NewID())
	})
	if fixturesErr != nil {
		t.Fatal(fixturesErr)
	}
	return &fixturesVal
}

// fakeAuthStore is an in-memory service.AuthStore with the same rotation,
// reuse and session semantics as the Postgres store (tested there for real).
type fakeAuthStore struct {
	mu     sync.Mutex
	users  map[string]model.User // by username
	tokens map[string]*fakeRefresh
	seq    int // orders rows like created_at does
	err    error
	google map[string]model.Identity // by user id
}

type fakeRefresh struct {
	userID, family, userAgent string
	revoked                   bool
	expires                   time.Time
	seq                       int
}

func (r *fakeRefresh) live() bool { return !r.revoked && time.Now().Before(r.expires) }

func newFakeAuthStore(t *testing.T) *fakeAuthStore {
	f := fixtures(t)
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	return &fakeAuthStore{
		users: map[string]model.User{
			"admin":  {ID: adminID, Username: "admin", PasswordHash: f.adminHash, Role: model.RoleAdmin, CreatedAt: created},
			"viewer": {ID: viewerID, Username: "viewer", PasswordHash: f.viewerHash, Role: model.RoleUser, CreatedAt: created},
		},
		tokens: map[string]*fakeRefresh{},
		google: map[string]model.Identity{},
	}
}

func (s *fakeAuthStore) UserByUsername(_ context.Context, username string) (model.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return model.User{}, s.err
	}
	u, ok := s.users[username]
	if !ok {
		return model.User{}, store.ErrNotFound
	}
	return u, nil
}

func (s *fakeAuthStore) UserByID(_ context.Context, id string) (model.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return model.User{}, s.err
	}
	for _, u := range s.users {
		if u.ID == id {
			return u, nil
		}
	}
	return model.User{}, store.ErrNotFound
}

// insert stores a token; the caller holds mu.
func (s *fakeAuthStore) insert(userID, family string, r store.NewRefresh, userAgent string) {
	s.seq++
	s.tokens[hex.EncodeToString(r.Hash)] = &fakeRefresh{
		userID: userID, family: family, userAgent: userAgent, expires: time.Now().Add(r.TTL), seq: s.seq,
	}
}

func (s *fakeAuthStore) CreateRefreshToken(_ context.Context, userID string, r store.NewRefresh) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return "", s.err
	}
	family := model.NewID()
	s.insert(userID, family, r, r.UserAgent)
	return family, nil
}

func (s *fakeAuthStore) RotateRefreshToken(_ context.Context, oldHash []byte, next store.NewRefresh) (string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return "", "", s.err
	}
	old, ok := s.tokens[hex.EncodeToString(oldHash)]
	switch {
	case !ok:
		return "", "", store.ErrRefreshInvalid
	case old.revoked:
		for _, tk := range s.tokens {
			if tk.family == old.family {
				tk.revoked = true
			}
		}
		return "", "", store.ErrRefreshReused
	case !time.Now().Before(old.expires):
		return "", "", store.ErrRefreshInvalid
	}
	old.revoked = true
	s.insert(old.userID, old.family, next, old.userAgent)
	return old.userID, old.family, nil
}

func (s *fakeAuthStore) RevokeRefreshFamily(_ context.Context, hash []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	if tk, ok := s.tokens[hex.EncodeToString(hash)]; ok {
		for _, other := range s.tokens {
			if other.family == tk.family {
				other.revoked = true
			}
		}
	}
	return nil
}

func (s *fakeAuthStore) ChangePassword(_ context.Context, userID, hash string, r store.NewRefresh) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return "", s.err
	}
	for name, u := range s.users {
		if u.ID != userID {
			continue
		}
		u.PasswordHash = hash
		s.users[name] = u
		for _, tk := range s.tokens {
			if tk.userID == userID {
				tk.revoked = true
			}
		}
		family := model.NewID()
		s.insert(userID, family, r, r.UserAgent)
		return family, nil
	}
	return "", store.ErrNotFound
}

func (s *fakeAuthStore) ListSessions(_ context.Context, userID string) ([]model.LoginSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	type agg struct {
		first, last int
		live        bool
		ua          string
	}
	byFamily := map[string]*agg{}
	for _, tk := range s.tokens {
		if tk.userID != userID {
			continue
		}
		a := byFamily[tk.family]
		if a == nil {
			a = &agg{first: tk.seq}
			byFamily[tk.family] = a
		}
		a.first = min(a.first, tk.seq)
		if tk.seq >= a.last {
			a.last, a.ua = tk.seq, tk.userAgent
		}
		a.live = a.live || tk.live()
	}
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	var out []model.LoginSession
	for family, a := range byFamily {
		if !a.live {
			continue
		}
		ls := model.LoginSession{ID: family, CreatedAt: base.Add(time.Duration(a.first) * time.Minute),
			LastUsedAt: base.Add(time.Duration(a.last) * time.Minute)}
		if a.ua != "" {
			ls.UserAgent = &a.ua
		}
		out = append(out, ls)
	}
	slices.SortFunc(out, func(x, y model.LoginSession) int { return y.LastUsedAt.Compare(x.LastUsedAt) })
	return out, nil
}

func (s *fakeAuthStore) GoogleIdentity(_ context.Context, userID string) (model.Identity, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return model.Identity{}, false, s.err
	}
	id, ok := s.google[userID]
	return id, ok, nil
}

func (s *fakeAuthStore) UnlinkGoogle(_ context.Context, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	for _, u := range s.users {
		if u.ID == userID && !u.HasPassword() {
			return store.ErrLastLoginMethod
		}
	}
	if _, ok := s.google[userID]; !ok {
		return store.ErrNotFound
	}
	delete(s.google, userID)
	return nil
}

func (s *fakeAuthStore) SessionLive(_ context.Context, userID, family string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return false, s.err
	}
	for _, tk := range s.tokens {
		if tk.userID == userID && tk.family == family && tk.live() {
			return true, nil
		}
	}
	return false, nil
}

func (s *fakeAuthStore) RevokeSession(_ context.Context, userID, family string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	found := false
	for _, tk := range s.tokens {
		if tk.userID == userID && tk.family == family && tk.live() {
			tk.revoked, found = true, true
		}
	}
	if !found {
		return store.ErrNotFound
	}
	return nil
}

func (s *fakeAuthStore) RevokeOtherSessions(_ context.Context, userID, keep string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return 0, s.err
	}
	ended := map[string]bool{}
	for _, tk := range s.tokens {
		if tk.userID == userID && tk.family != keep && tk.live() {
			tk.revoked, ended[tk.family] = true, true
		}
	}
	return len(ended), nil
}

// postJSON posts body to path with the given content type and cookies.
func postJSON(t *testing.T, srv *httptest.Server, path, contentType, body string, csrf bool, cookies ...*http.Cookie) response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if csrf {
		req.Header.Set(CSRFHeader, "XMLHttpRequest")
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	return send(t, srv, req)
}

func loginAs(t *testing.T, srv *httptest.Server, username, password string) response {
	t.Helper()
	return postJSON(t, srv, "/api/v1/auth/login", "application/json",
		`{"username":"`+username+`","password":"`+password+`"}`, false)
}

func refreshCookieOf(t *testing.T, r response) *http.Cookie {
	t.Helper()
	for _, c := range (&http.Response{Header: r.header}).Cookies() {
		if c.Name == RefreshCookie {
			return c
		}
	}
	t.Fatalf("no %s cookie in %v", RefreshCookie, r.header.Values("Set-Cookie"))
	return nil
}

func TestLogin(t *testing.T) {
	f := fixtures(t)

	t.Run("success: access token in the body, refresh token in a locked-down cookie", func(t *testing.T) {
		srv := newServerWith(t, &fakePinger{}, catalogStore())
		r := loginAs(t, srv, "ADMIN", f.adminPassword) // usernames are case-insensitive
		if r.status != http.StatusOK {
			t.Fatalf("status = %d: %s", r.status, r.body)
		}
		var tok tokenJSON
		decode(t, r.body, &tok)
		if tok.TokenType != "Bearer" || tok.ExpiresIn != 900 || tok.User != (userJSON{adminID, "admin", "admin"}) {
			t.Fatalf("body = %+v", tok)
		}
		c, err := f.signer.Verify(tok.AccessToken)
		if err != nil || c.UserID != adminID || c.Role != "admin" {
			t.Fatalf("access token claims = %+v, %v", c, err)
		}
		ck := refreshCookieOf(t, r)
		if !ck.HttpOnly || !ck.Secure || ck.SameSite != http.SameSiteStrictMode || ck.Path != "/api/v1/auth" ||
			ck.MaxAge < 29*24*3600 || len(ck.Value) != 43 {
			t.Fatalf("cookie = %+v", ck)
		}
		if strings.Contains(string(r.body), ck.Value) {
			t.Fatal("the refresh token is in the JSON body; it must live only in the HttpOnly cookie")
		}
		if r.header.Get("Cache-Control") != "no-store" {
			t.Errorf("Cache-Control = %q", r.header.Get("Cache-Control"))
		}
	})

	t.Run("plain http on loopback: no Secure flag", func(t *testing.T) {
		srv, _ := newServerFull(t, &fakePinger{}, catalogStore(), newFakeAuthStore(t), Options{SecureCookies: false})
		if ck := refreshCookieOf(t, loginAs(t, srv, "viewer", f.viewerPassword)); ck.Secure || !ck.HttpOnly {
			t.Fatalf("cookie = %+v", ck)
		}
	})

	t.Run("unknown user and wrong password look the same", func(t *testing.T) {
		srv := newServerWith(t, &fakePinger{}, catalogStore())
		wrong := loginAs(t, srv, "admin", f.viewerPassword)
		unknown := loginAs(t, srv, "nobody", f.adminPassword)
		for _, r := range []response{wrong, unknown} {
			if r.status != http.StatusUnauthorized {
				t.Fatalf("status = %d: %s", r.status, r.body)
			}
			assertErrorCode(t, r.body, "invalid_credentials")
		}
		if string(wrong.body) != string(unknown.body) {
			t.Fatalf("bodies differ: %s vs %s", wrong.body, unknown.body)
		}
		if len(wrong.header.Values("Set-Cookie")) != 0 {
			t.Fatal("a failed login set a cookie")
		}
	})

	t.Run("bad requests", func(t *testing.T) {
		srv := newServerWith(t, &fakePinger{}, catalogStore())
		for name, tt := range map[string]struct {
			contentType, body string
			status            int
			code              string
		}{
			"form encoded":      {"application/x-www-form-urlencoded", "username=a&password=b", 415, "unsupported_media_type"},
			"text/plain":        {"text/plain", `{"username":"admin","password":"x"}`, 415, "unsupported_media_type"},
			"no content type":   {"", `{"username":"admin","password":"x"}`, 415, "unsupported_media_type"},
			"json with charset": {"application/json; charset=utf-8", `{"username":"","password":"x"}`, 400, "invalid_request"},
			"missing password":  {"application/json", `{"username":"admin"}`, 400, "invalid_request"},
			"not json":          {"application/json", `username=admin`, 400, "invalid_request"},
			"unknown field":     {"application/json", `{"username":"admin","password":"x","role":"admin"}`, 400, "invalid_request"},
			"over 4 KiB":        {"application/json", `{"username":"admin","password":"` + strings.Repeat("a", 5000) + `"}`, 400, "invalid_request"},
		} {
			r := postJSON(t, srv, "/api/v1/auth/login", tt.contentType, tt.body, false)
			if r.status != tt.status {
				t.Errorf("%s: status = %d, want %d: %s", name, r.status, tt.status, r.body)
				continue
			}
			assertErrorCode(t, r.body, tt.code)
		}
		if r := doAs(t, srv, http.MethodGet, "/api/v1/auth/login", ""); r.status != http.StatusMethodNotAllowed {
			t.Errorf("GET login = %d", r.status)
		}
	})

	t.Run("five failures block the username from this address, with Retry-After", func(t *testing.T) {
		srv := newServerWith(t, &fakePinger{}, catalogStore())
		for range auth.MaxLoginFailures {
			if r := loginAs(t, srv, "viewer", "wrong password here"); r.status != http.StatusUnauthorized {
				t.Fatalf("status = %d", r.status)
			}
		}
		// Blocked even with the right password: nothing is checked.
		r := loginAs(t, srv, "viewer", f.viewerPassword)
		if r.status != http.StatusTooManyRequests || r.header.Get("Retry-After") != "900" {
			t.Fatalf("status = %d, Retry-After = %q: %s", r.status, r.header.Get("Retry-After"), r.body)
		}
		assertErrorCode(t, r.body, "rate_limited")
		if r := loginAs(t, srv, "admin", f.adminPassword); r.status != http.StatusOK {
			t.Fatalf("another username from the same address was blocked: %d", r.status)
		}
	})

	t.Run("store failure", func(t *testing.T) {
		as := newFakeAuthStore(t)
		as.err = errors.New("connection refused (password=" + randomString(6) + ")")
		srv, _ := newServerFull(t, &fakePinger{}, catalogStore(), as, Options{SecureCookies: true})
		r := loginAs(t, srv, "admin", f.adminPassword)
		if r.status != http.StatusInternalServerError || strings.Contains(string(r.body), "password=") {
			t.Fatalf("status = %d: %s", r.status, r.body)
		}
	})
}

func TestRefreshAndReuse(t *testing.T) {
	f := fixtures(t)
	srv, as := newServerFull(t, &fakePinger{}, catalogStore(), newFakeAuthStore(t), Options{SecureCookies: true})
	refresh := func(t *testing.T, c *http.Cookie, csrf bool) response {
		t.Helper()
		var cookies []*http.Cookie
		if c != nil {
			cookies = append(cookies, &http.Cookie{Name: c.Name, Value: c.Value}) // #nosec G124 -- a request cookie the test client sends; attributes are the server's job
		}
		return postJSON(t, srv, "/api/v1/auth/refresh", "", "", csrf, cookies...)
	}

	first := refreshCookieOf(t, loginAs(t, srv, "viewer", f.viewerPassword))

	if r := refresh(t, first, false); r.status != http.StatusForbidden {
		t.Fatalf("without %s: %d, want 403", CSRFHeader, r.status)
	} else {
		assertErrorCode(t, r.body, "forbidden")
	}
	if r := refresh(t, nil, true); r.status != http.StatusUnauthorized {
		t.Fatalf("without a cookie: %d", r.status)
	}
	forged := &http.Cookie{Name: RefreshCookie, Value: "forged"} // #nosec G124 -- a request cookie the test client sends
	if r := refresh(t, forged, true); r.status != http.StatusUnauthorized {
		t.Fatalf("forged cookie: %d", r.status)
	} else if ck := refreshCookieOf(t, r); ck.MaxAge >= 0 {
		t.Fatalf("a refused refresh must clear the cookie, got %+v", ck)
	}

	r := refresh(t, first, true)
	if r.status != http.StatusOK {
		t.Fatalf("refresh = %d: %s", r.status, r.body)
	}
	var tok tokenJSON
	decode(t, r.body, &tok)
	if c, err := f.signer.Verify(tok.AccessToken); err != nil || c.UserID != viewerID || tok.User.Role != "user" {
		t.Fatalf("refreshed session = %+v, %v", tok, err)
	}
	second := refreshCookieOf(t, r)
	if second.Value == first.Value {
		t.Fatal("refresh did not rotate the token")
	}

	// The old token again: reuse. It fails, and so does the newer one.
	if r := refresh(t, first, true); r.status != http.StatusUnauthorized {
		t.Fatalf("reused token: %d", r.status)
	} else {
		assertErrorCode(t, r.body, "unauthorized")
	}
	if r := refresh(t, second, true); r.status != http.StatusUnauthorized {
		t.Fatalf("the newer token after reuse: %d, want the family revoked", r.status)
	}

	as.err = errors.New("db down")
	fresh := refreshCookieOf(t, loginAs(t, newServerWith(t, &fakePinger{}, catalogStore()), "viewer", f.viewerPassword))
	if r := refresh(t, fresh, true); r.status != http.StatusInternalServerError {
		t.Fatalf("store failure: %d", r.status)
	}
}

func TestLogout(t *testing.T) {
	f := fixtures(t)
	srv, as := newServerFull(t, &fakePinger{}, catalogStore(), newFakeAuthStore(t), Options{SecureCookies: true})
	ck := refreshCookieOf(t, loginAs(t, srv, "admin", f.adminPassword))
	sent := &http.Cookie{Name: ck.Name, Value: ck.Value} // #nosec G124 -- a request cookie the test client sends

	if r := postJSON(t, srv, "/api/v1/auth/logout", "", "", false, sent); r.status != http.StatusForbidden {
		t.Fatalf("without %s: %d", CSRFHeader, r.status)
	}
	r := postJSON(t, srv, "/api/v1/auth/logout", "", "", true, sent)
	if r.status != http.StatusNoContent || len(r.body) != 0 {
		t.Fatalf("logout = %d %q", r.status, r.body)
	}
	if c := refreshCookieOf(t, r); c.MaxAge >= 0 || c.Value != "" {
		t.Fatalf("logout did not clear the cookie: %+v", c)
	}
	if r := postJSON(t, srv, "/api/v1/auth/refresh", "", "", true, sent); r.status != http.StatusUnauthorized {
		t.Fatalf("refresh after logout = %d", r.status)
	}
	if r := postJSON(t, srv, "/api/v1/auth/logout", "", "", true); r.status != http.StatusNoContent {
		t.Fatalf("logout without a cookie = %d", r.status)
	}
	as.err = errors.New("db down")
	if r := postJSON(t, srv, "/api/v1/auth/logout", "", "", true, sent); r.status != http.StatusInternalServerError {
		t.Fatalf("store failure = %d", r.status)
	}
}

func TestAccessTokenRequired(t *testing.T) {
	f := fixtures(t)
	srv := newServerWith(t, &fakePinger{}, catalogStore())
	now := time.Now()
	sign := func(m jwt.SigningMethod, c jwt.MapClaims, key any) string {
		s, err := jwt.NewWithClaims(m, c).SignedString(key)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	sid := model.NewID()
	valid := jwt.MapClaims{"sub": adminID, "role": "admin", "sid": sid, "iat": now.Unix(), "exp": now.Add(time.Minute).Unix()}
	expired := jwt.MapClaims{"sub": adminID, "role": "admin", "sid": sid, "iat": now.Add(-time.Hour).Unix(), "exp": now.Add(-time.Minute).Unix()}
	// Issued before sessions were tracked (no sid): every such token is
	// refused after the deploy, and its holder logs in once more.
	noSid := jwt.MapClaims{"sub": adminID, "role": "admin", "iat": now.Unix(), "exp": now.Add(time.Minute).Unix()}

	headers := map[string]string{
		"no header":       "",
		"empty bearer":    "Bearer ",
		"wrong scheme":    "Token " + f.adminToken,
		"basic auth":      "Basic YWRtaW46YWRtaW4=",
		"expired":         "Bearer " + sign(jwt.SigningMethodHS256, expired, f.key),
		"alg none":        "Bearer " + sign(jwt.SigningMethodNone, valid, jwt.UnsafeAllowNoneSignatureType),
		"HS512, same key": "Bearer " + sign(jwt.SigningMethodHS512, valid, f.key),
		"another key":     "Bearer " + sign(jwt.SigningMethodHS256, valid, []byte(randomString(48))),
		"tampered":        "Bearer " + f.viewerToken[:len(f.viewerToken)-2] + "xx",
		"no sid claim":    "Bearer " + sign(jwt.SigningMethodHS256, noSid, f.key),
	}
	if r := doAs(t, srv, http.MethodGet, "/api/v1/movies", sign(jwt.SigningMethodHS256, valid, f.key)); r.status != http.StatusOK {
		t.Fatalf("the valid token itself = %d, so the cases below prove nothing", r.status)
	}
	for _, path := range []string{"/api/v1/movies", "/api/v1/movies/" + readyID, "/api/v1/movies/" + readyID + "/playback",
		"/api/v1/me", "/api/v1/me/sessions"} {
		for name, h := range headers {
			req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+path, nil)
			if h != "" {
				req.Header.Set("Authorization", h)
			}
			r := send(t, srv, req)
			if r.status != http.StatusUnauthorized {
				t.Errorf("%s %s: %d, want 401", path, name, r.status)
				continue
			}
			assertErrorCode(t, r.body, "unauthorized")
			if !strings.HasPrefix(r.header.Get("WWW-Authenticate"), "Bearer") {
				t.Errorf("%s %s: WWW-Authenticate = %q", path, name, r.header.Get("WWW-Authenticate"))
			}
		}
	}
	// The probes and metrics stay open.
	for _, p := range []string{"/healthz", "/readyz", "/metrics"} {
		if r := doAs(t, srv, http.MethodGet, p, ""); r.status != http.StatusOK {
			t.Errorf("%s without a token = %d", p, r.status)
		}
	}
}

func TestUserRoleSeesReadyMoviesOnly(t *testing.T) {
	f := fixtures(t)
	st := catalogStore()
	srv := newServerWith(t, &fakePinger{}, st)

	r := doAs(t, srv, http.MethodGet, "/api/v1/movies", f.viewerToken)
	if r.status != http.StatusOK || st.gotFilter.Status == nil || *st.gotFilter.Status != model.StatusReady {
		t.Fatalf("user list: %d, filter %+v; want status forced to ready", r.status, st.gotFilter)
	}
	for _, q := range []string{"?status=ready", "?status=pending"} {
		r := doAs(t, srv, http.MethodGet, "/api/v1/movies"+q, f.viewerToken)
		if r.status != http.StatusForbidden {
			t.Fatalf("user %s: %d, want 403", q, r.status)
		}
		assertErrorCode(t, r.body, "forbidden")
	}
	for _, id := range []string{pendingID, processingID, failedID} {
		for _, suffix := range []string{"", "/playback"} {
			r := doAs(t, srv, http.MethodGet, "/api/v1/movies/"+id+suffix, f.viewerToken)
			if r.status != http.StatusNotFound {
				t.Fatalf("user GET %s%s: %d, want 404 (not ready = not visible)", id, suffix, r.status)
			}
		}
	}
	if r := doAs(t, srv, http.MethodGet, "/api/v1/movies/"+readyID+"/playback", f.viewerToken); r.status != http.StatusOK {
		t.Fatalf("user playback of a ready movie: %d", r.status)
	}
	// The admin still sees everything, and gets 409 for a movie not ready.
	if r := doFull(t, srv, http.MethodGet, "/api/v1/movies/"+pendingID+"/playback"); r.status != http.StatusConflict {
		t.Fatalf("admin playback of pending: %d", r.status)
	}
}
