package handler

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/vtnHAN-SOLO/hls-vod/internal/auth"
	"github.com/vtnHAN-SOLO/hls-vod/internal/mail"
	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
	"github.com/vtnHAN-SOLO/hls-vod/internal/service"
	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
	"github.com/vtnHAN-SOLO/hls-vod/internal/turnstile"
)

const (
	testBaseURL = "https://media.example"
	testSiteKey = "test-site-key"
)

// fakeCaptcha passes "good", is unreachable when down, rejects the rest.
type fakeCaptcha struct {
	mu   sync.Mutex
	down bool
}

func (c *fakeCaptcha) setDown(v bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.down = v
}

func (c *fakeCaptcha) Verify(_ context.Context, token, _ string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case c.down:
		return fmt.Errorf("%w: connection refused", turnstile.ErrUnavailable)
	case token == "good":
		return nil
	default:
		return turnstile.ErrRejected
	}
}

type fakeMailer struct {
	mu   sync.Mutex
	sent []mail.Message
}

func (m *fakeMailer) Enqueue(msg mail.Message) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, msg)
}

func (m *fakeMailer) take() []mail.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := m.sent
	m.sent = nil
	return out
}

// fakeRegStore keeps its accounts in the fakeAuthStore's users (under its
// lock), so a registered account can log in through the real Auth.
type fakeRegStore struct {
	as     *fakeAuthStore
	tokens map[string]*fakeEmailToken // by hex hash
	err    error
}

type fakeEmailToken struct {
	userID  string
	purpose string
	used    bool
	expires time.Time
}

func newFakeRegStore(as *fakeAuthStore) *fakeRegStore {
	return &fakeRegStore{as: as, tokens: map[string]*fakeEmailToken{}}
}

func (s *fakeRegStore) byEmail(email string) (model.User, bool) {
	for _, u := range s.as.users {
		if u.Email != nil && *u.Email == email {
			return u, true
		}
	}
	return model.User{}, false
}

func (s *fakeRegStore) Register(_ context.Context, a store.NewAccount) (store.Registration, error) {
	s.as.mu.Lock()
	defer s.as.mu.Unlock()
	if s.err != nil {
		return store.Registration{}, s.err
	}
	if _, ok := s.as.users[a.Username]; ok {
		return store.Registration{}, store.ErrUserExists
	}
	if u, ok := s.byEmail(a.Email); ok {
		return store.Registration{Existing: &u}, nil
	}
	email := a.Email
	u := model.User{ID: model.NewID(), Username: a.Username, PasswordHash: a.PasswordHash, Role: model.RoleUser,
		Email: &email, CreatedAt: time.Now()}
	s.as.users[a.Username] = u
	s.tokens[hex.EncodeToString(a.VerifyHash)] = &fakeEmailToken{userID: u.ID, purpose: store.PurposeVerify, expires: time.Now().Add(a.VerifyTTL)}
	return store.Registration{User: u}, nil
}

func (s *fakeRegStore) ResendVerification(_ context.Context, email string, hash []byte, ttl time.Duration) (model.User, store.ResendOutcome, error) {
	s.as.mu.Lock()
	defer s.as.mu.Unlock()
	if s.err != nil {
		return model.User{}, 0, s.err
	}
	u, ok := s.byEmail(email)
	switch {
	case !ok:
		return model.User{}, store.ResendNoAccount, nil
	case !u.Unverified():
		return u, store.ResendAlreadyVerified, nil
	case u.Disabled():
		return u, store.ResendDisabled, nil
	}
	s.dropOpen(u.ID, store.PurposeVerify)
	s.tokens[hex.EncodeToString(hash)] = &fakeEmailToken{userID: u.ID, purpose: store.PurposeVerify, expires: u.CreatedAt.Add(ttl)}
	return u, store.ResendIssued, nil
}

func (s *fakeRegStore) VerifyEmail(_ context.Context, hash []byte, r store.NewRefresh) (model.User, string, error) {
	s.as.mu.Lock()
	defer s.as.mu.Unlock()
	if s.err != nil {
		return model.User{}, "", s.err
	}
	tok := s.usable(hash, store.PurposeVerify)
	if tok == nil {
		return model.User{}, "", store.ErrEmailTokenInvalid
	}
	for _, u := range s.as.users {
		if u.ID == tok.userID && u.Disabled() {
			return model.User{}, "", store.ErrAccountDisabled
		}
	}
	s.markUsed(tok.userID, store.PurposeVerify)
	for name, u := range s.as.users {
		if u.ID == tok.userID {
			now := time.Now()
			u.EmailVerifiedAt = &now
			s.as.users[name] = u
			family := model.NewID()
			s.as.insert(u.ID, family, r, r.UserAgent)
			return u, family, nil
		}
	}
	return model.User{}, "", store.ErrEmailTokenInvalid
}

// The caller of these helpers holds s.as.mu.

func (s *fakeRegStore) usable(hash []byte, purpose string) *fakeEmailToken {
	tok := s.tokens[hex.EncodeToString(hash)]
	if tok == nil || tok.purpose != purpose || tok.used || !time.Now().Before(tok.expires) {
		return nil
	}
	return tok
}

func (s *fakeRegStore) dropOpen(userID, purpose string) {
	for k, tok := range s.tokens {
		if tok.userID == userID && tok.purpose == purpose && !tok.used {
			delete(s.tokens, k)
		}
	}
}

func (s *fakeRegStore) markUsed(userID, purpose string) {
	for _, tok := range s.tokens {
		if tok.userID == userID && tok.purpose == purpose {
			tok.used = true
		}
	}
}

func (s *fakeRegStore) IssueResetToken(_ context.Context, email string, hash []byte, ttl time.Duration) (model.User, store.ResetOutcome, error) {
	s.as.mu.Lock()
	defer s.as.mu.Unlock()
	if s.err != nil {
		return model.User{}, 0, s.err
	}
	u, ok := s.byEmail(email)
	switch {
	case !ok:
		return model.User{}, store.ResetNoAccount, nil
	case u.EmailVerifiedAt == nil:
		return u, store.ResetUnverified, nil
	case u.Disabled():
		return u, store.ResetDisabled, nil
	}
	s.dropOpen(u.ID, store.PurposeReset)
	s.tokens[hex.EncodeToString(hash)] = &fakeEmailToken{userID: u.ID, purpose: store.PurposeReset, expires: time.Now().Add(ttl)}
	return u, store.ResetIssued, nil
}

func (s *fakeRegStore) EmailTokenUsable(_ context.Context, hash []byte, purpose string) (bool, error) {
	s.as.mu.Lock()
	defer s.as.mu.Unlock()
	if s.err != nil {
		return false, s.err
	}
	return s.usable(hash, purpose) != nil, nil
}

func (s *fakeRegStore) ResetPassword(_ context.Context, hash []byte, passwordHash string) (model.User, error) {
	s.as.mu.Lock()
	defer s.as.mu.Unlock()
	if s.err != nil {
		return model.User{}, s.err
	}
	tok := s.usable(hash, store.PurposeReset)
	if tok == nil {
		return model.User{}, store.ErrEmailTokenInvalid
	}
	s.markUsed(tok.userID, store.PurposeReset)
	for name, u := range s.as.users {
		if u.ID == tok.userID {
			u.PasswordHash = passwordHash
			s.as.users[name] = u
			for _, rt := range s.as.tokens {
				if rt.userID == u.ID {
					rt.revoked = true
				}
			}
			return u, nil
		}
	}
	return model.User{}, store.ErrEmailTokenInvalid
}

func (s *fakeRegStore) IssueResetTokenForUser(_ context.Context, userID string, hash []byte, ttl time.Duration) error {
	s.as.mu.Lock()
	defer s.as.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.dropOpen(userID, store.PurposeReset)
	s.tokens[hex.EncodeToString(hash)] = &fakeEmailToken{userID: userID, purpose: store.PurposeReset, expires: time.Now().Add(ttl)}
	return nil
}

// regEnv is a server with registration wired to fakes the test can steer.
type regEnv struct {
	srv     *httptest.Server
	as      *fakeAuthStore
	rs      *fakeRegStore
	captcha *fakeCaptcha
	mailer  *fakeMailer
}

type regSetup struct {
	closed         bool
	noTurnstile    bool
	captchaOnLogin bool
	google         Google // nil: Google off
}

func newRegEnv(t *testing.T, cfg regSetup) *regEnv {
	t.Helper()
	e := &regEnv{as: newFakeAuthStore(t), captcha: &fakeCaptcha{}, mailer: &fakeMailer{}}
	e.rs = newFakeRegStore(e.as)
	f := fixtures(t)
	logger := slog.New(slog.DiscardHandler)
	a := service.NewAuth(e.as, f.hasher, f.signer, auth.NewThrottle(), auth.NewThrottle(), logger)
	var captcha service.Captcha = e.captcha
	siteKey := testSiteKey
	if cfg.noTurnstile {
		captcha, siteKey = nil, ""
	}
	if cfg.captchaOnLogin {
		a.WithCaptcha(captcha)
	}
	signup := service.NewRegistration(e.rs, f.hasher, captcha, e.mailer, a,
		service.RegistrationConfig{Open: !cfg.closed, BaseURL: testBaseURL, TurnstileSiteKey: siteKey}, logger)
	e.srv = httptest.NewServer(NewRouter(logger, prometheus.NewRegistry(), &fakePinger{}, service.NewCatalog(&fakeStore{}, f.media), a, signup,
		Options{SecureCookies: true, Google: cfg.google}))
	t.Cleanup(e.srv.Close)
	return e
}

func jsonBody(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func (e *regEnv) post(t *testing.T, path string, v any) response {
	t.Helper()
	return postJSON(t, e.srv, path, "application/json", jsonBody(t, v), false)
}

func (e *regEnv) register(t *testing.T, email, username, password, captcha string) response {
	t.Helper()
	return e.post(t, "/api/v1/auth/register",
		map[string]string{"email": email, "username": username, "password": password, "turnstile_token": captcha})
}

func (e *regEnv) login(t *testing.T, username, password, captcha string) response {
	t.Helper()
	body := map[string]string{"username": username, "password": password}
	if captcha != "" {
		body["turnstile_token"] = captcha
	}
	return e.post(t, "/api/v1/auth/login", body)
}

// tokenFrom is the token of the one verify link in mails.
func tokenFrom(t *testing.T, mails []mail.Message) string {
	t.Helper()
	if len(mails) != 1 || mails[0].Kind != mail.KindVerify {
		t.Fatalf("mails = %+v; want one verify mail", mails)
	}
	const prefix = testBaseURL + "/verify-email#token="
	i := strings.Index(mails[0].Text, prefix)
	if i < 0 {
		t.Fatalf("no link in %q", mails[0].Text)
	}
	return strings.Fields(mails[0].Text[i+len(prefix):])[0]
}

func errorCode(t *testing.T, r response) string {
	t.Helper()
	var e errorBody
	if err := json.Unmarshal(r.body, &e); err != nil {
		t.Fatalf("not an error body: %s", r.body)
	}
	return e.Error.Code
}

func TestAuthConfig(t *testing.T) {
	for _, tt := range []struct {
		name string
		cfg  regSetup
		want string
	}{
		{"open", regSetup{}, `{"registration_open":true,"turnstile_site_key":"` + testSiteKey + `","google_enabled":false}`},
		{"closed", regSetup{closed: true}, `{"registration_open":false,"turnstile_site_key":"` + testSiteKey + `","google_enabled":false}`},
		{"no Turnstile", regSetup{closed: true, noTurnstile: true}, `{"registration_open":false,"turnstile_site_key":null,"google_enabled":false}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := newRegEnv(t, tt.cfg)
			r := doFull(t, e.srv, http.MethodGet, "/api/v1/auth/config")
			if r.status != http.StatusOK || strings.TrimSpace(string(r.body)) != tt.want {
				t.Fatalf("config = %d %s, want %s", r.status, r.body, tt.want)
			}
		})
	}
	e := newRegEnv(t, regSetup{})
	if r := doFull(t, e.srv, http.MethodPost, "/api/v1/auth/config"); r.status != http.StatusMethodNotAllowed {
		t.Fatalf("POST config = %d", r.status)
	}
}

// The whole flow over HTTP: register → mail → login refused → verify → session.
func TestRegisterAndVerify(t *testing.T) {
	e := newRegEnv(t, regSetup{})
	password := randomString(18)

	r := e.register(t, "Dana@Example.com", "Dana", password, "good")
	if r.status != http.StatusAccepted || r.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("register = %d %s", r.status, r.body)
	}
	token := tokenFrom(t, e.mailer.take())

	// Not verified yet: the right password says so, a wrong one does not.
	if r := e.login(t, "dana", password, ""); r.status != http.StatusForbidden || errorCode(t, r) != "email_not_verified" {
		t.Fatalf("login before verifying = %d %s", r.status, r.body)
	}
	if r := e.login(t, "dana", password+"x", ""); r.status != http.StatusUnauthorized || errorCode(t, r) != "invalid_credentials" {
		t.Fatalf("wrong password before verifying = %d %s", r.status, r.body)
	}

	v := e.post(t, "/api/v1/auth/verify-email", map[string]string{"token": token})
	if v.status != http.StatusOK {
		t.Fatalf("verify = %d %s", v.status, v.body)
	}
	_ = refreshCookieOf(t, v)
	var s tokenJSON
	if err := json.Unmarshal(v.body, &s); err != nil || s.User.Username != "dana" || s.User.Role != "user" {
		t.Fatalf("verify body = %s", v.body)
	}
	if me := doAs(t, e.srv, http.MethodGet, "/api/v1/me", s.AccessToken); me.status != http.StatusOK {
		t.Fatalf("GET /me with the verify session = %d %s", me.status, me.body)
	}
	if r := e.login(t, "dana", password, ""); r.status != http.StatusOK {
		t.Fatalf("login after verifying = %d %s", r.status, r.body)
	}

	// Single use; anything else is the same 400.
	for _, bad := range []string{token, "", "not-a-token", randomString(32)} {
		if r := e.post(t, "/api/v1/auth/verify-email", map[string]string{"token": bad}); r.status != http.StatusBadRequest || errorCode(t, r) != "invalid_token" {
			t.Errorf("verify(%.8q) = %d %s", bad, r.status, r.body)
		}
	}
}

func TestVerifyEmailErrors(t *testing.T) {
	e := newRegEnv(t, regSetup{})
	_ = e.register(t, "erin@example.com", "erin", randomString(18), "good")
	token := tokenFrom(t, e.mailer.take())

	e.as.mu.Lock()
	for _, tok := range e.rs.tokens {
		tok.expires = time.Now().Add(-time.Second)
	}
	e.as.mu.Unlock()
	if r := e.post(t, "/api/v1/auth/verify-email", map[string]string{"token": token}); r.status != http.StatusBadRequest || errorCode(t, r) != "invalid_token" {
		t.Fatalf("expired token = %d %s", r.status, r.body)
	}
	if r := postJSON(t, e.srv, "/api/v1/auth/verify-email", "application/json", `{"token":"x","extra":1}`, false); r.status != http.StatusBadRequest || errorCode(t, r) != "invalid_request" {
		t.Fatalf("unknown field = %d %s", r.status, r.body)
	}
	if r := postJSON(t, e.srv, "/api/v1/auth/verify-email", "text/plain", `{"token":"x"}`, false); r.status != http.StatusUnsupportedMediaType {
		t.Fatalf("text/plain = %d", r.status)
	}
	if r := doFull(t, e.srv, http.MethodGet, "/api/v1/auth/verify-email"); r.status != http.StatusMethodNotAllowed {
		t.Fatalf("GET = %d", r.status)
	}
	e.rs.err = errors.New("db down")
	tok, _, _ := auth.NewOpaqueToken()
	if r := e.post(t, "/api/v1/auth/verify-email", map[string]string{"token": tok}); r.status != http.StatusInternalServerError {
		t.Fatalf("store down = %d", r.status)
	}
}

// No enumeration: a new address and a taken one get byte-identical 202s.
func TestRegisterSameAnswerForTakenAddress(t *testing.T) {
	e := newRegEnv(t, regSetup{})
	first := e.register(t, "owner@example.com", "owner", randomString(18), "good")
	_ = tokenFrom(t, e.mailer.take())
	again := e.register(t, "OWNER@example.com", "someoneelse", randomString(18), "good")
	if first.status != http.StatusAccepted || again.status != first.status || string(again.body) != string(first.body) {
		t.Fatalf("new: %d %s\ntaken: %d %s", first.status, first.body, again.status, again.body)
	}
	mails := e.mailer.take()
	if len(mails) != 1 || mails[0].Kind != mail.KindAccountExists || mails[0].To != "owner@example.com" {
		t.Fatalf("taken address mails = %+v; want one account-exists notice", mails)
	}
	if _, ok := e.as.users["someoneelse"]; ok {
		t.Fatal("an account was created for a taken address")
	}
}

func TestRegisterErrors(t *testing.T) {
	pw := randomString(18)
	t.Run("username taken: 409 whatever the address", func(t *testing.T) {
		e := newRegEnv(t, regSetup{})
		for _, email := range []string{"fresh@example.com", "other@example.com"} {
			if r := e.register(t, email, "viewer", pw, "good"); r.status != http.StatusConflict || errorCode(t, r) != "username_taken" {
				t.Fatalf("%s = %d %s", email, r.status, r.body)
			}
		}
	})
	t.Run("closed: 403, while login, verify and resend keep working", func(t *testing.T) {
		e := newRegEnv(t, regSetup{closed: true})
		if r := e.register(t, "x@example.com", "xavier", pw, "good"); r.status != http.StatusForbidden || errorCode(t, r) != "registration_closed" {
			t.Fatalf("register = %d %s", r.status, r.body)
		}
		if r := loginAs(t, e.srv, "viewer", fixtures(t).viewerPassword); r.status != http.StatusOK {
			t.Fatalf("login = %d", r.status)
		}
		if r := e.post(t, "/api/v1/auth/resend-verification", map[string]string{"email": "x@example.com", "turnstile_token": "good"}); r.status != http.StatusAccepted {
			t.Fatalf("resend = %d", r.status)
		}
	})
	t.Run("challenge", func(t *testing.T) {
		e := newRegEnv(t, regSetup{})
		if r := e.register(t, "x@example.com", "xavier", pw, "forged"); r.status != http.StatusBadRequest || errorCode(t, r) != "captcha_failed" {
			t.Fatalf("rejected = %d %s", r.status, r.body)
		}
		e.captcha.setDown(true)
		if r := e.register(t, "x@example.com", "xavier", pw, "good"); r.status != http.StatusServiceUnavailable || errorCode(t, r) != "captcha_unavailable" {
			t.Fatalf("siteverify down = %d %s", r.status, r.body)
		}
		// Fail closed for sign-up, but a normal login does not need Cloudflare.
		if r := loginAs(t, e.srv, "viewer", fixtures(t).viewerPassword); r.status != http.StatusOK {
			t.Fatalf("login while siteverify is down = %d %s", r.status, r.body)
		}
		if len(e.mailer.take()) != 0 {
			t.Fatal("mail sent without a solved challenge")
		}
	})
	t.Run("Turnstile not configured: 503", func(t *testing.T) {
		e := newRegEnv(t, regSetup{noTurnstile: true})
		if r := e.register(t, "x@example.com", "xavier", pw, "good"); r.status != http.StatusServiceUnavailable {
			t.Fatalf("register = %d", r.status)
		}
	})
	t.Run("validation, body and method", func(t *testing.T) {
		e := newRegEnv(t, regSetup{})
		for _, tt := range []struct{ email, username, password string }{
			{"Name <x@example.com>", "xavier", pw}, {"x@example.com", "x y", pw}, {"x@example.com", "xavier", "short"},
		} {
			if r := e.register(t, tt.email, tt.username, tt.password, "good"); r.status != http.StatusBadRequest || errorCode(t, r) != "invalid_request" {
				t.Errorf("%+v = %d %s", tt, r.status, r.body)
			}
		}
		if r := postJSON(t, e.srv, "/api/v1/auth/register", "application/json", `{"email":"x@example.com","admin":true}`, false); r.status != http.StatusBadRequest {
			t.Fatalf("unknown field = %d", r.status)
		}
		if r := postJSON(t, e.srv, "/api/v1/auth/register", "application/x-www-form-urlencoded", "email=x", false); r.status != http.StatusUnsupportedMediaType {
			t.Fatalf("form = %d", r.status)
		}
		if r := doFull(t, e.srv, http.MethodGet, "/api/v1/auth/register"); r.status != http.StatusMethodNotAllowed {
			t.Fatalf("GET = %d", r.status)
		}
	})
	t.Run("per-IP limit", func(t *testing.T) {
		e := newRegEnv(t, regSetup{})
		for i := range service.RegisterPerIPPerHour {
			if r := e.register(t, fmt.Sprintf("u%d@example.com", i), fmt.Sprintf("user%d", i), pw, "good"); r.status != http.StatusAccepted {
				t.Fatalf("attempt %d = %d", i+1, r.status)
			}
		}
		r := e.register(t, "late@example.com", "late", pw, "good")
		if r.status != http.StatusTooManyRequests || r.header.Get("Retry-After") == "" {
			t.Fatalf("over the limit = %d %v", r.status, r.header)
		}
	})
	t.Run("store down: 500", func(t *testing.T) {
		e := newRegEnv(t, regSetup{})
		e.rs.err = errors.New("db down")
		if r := e.register(t, "x@example.com", "xavier", pw, "good"); r.status != http.StatusInternalServerError {
			t.Fatalf("register = %d", r.status)
		}
	})
}

func TestResendVerificationEndpoint(t *testing.T) {
	e := newRegEnv(t, regSetup{})
	_ = e.register(t, "dana@example.com", "dana", randomString(18), "good")
	old := tokenFrom(t, e.mailer.take())
	resend := func(email, captcha string) response {
		return e.post(t, "/api/v1/auth/resend-verification", map[string]string{"email": email, "turnstile_token": captcha})
	}

	known, unknown := resend("dana@example.com", "good"), resend("nobody@example.com", "good")
	if known.status != http.StatusAccepted || unknown.status != known.status || string(unknown.body) != string(known.body) {
		t.Fatalf("known %d %s / unknown %d %s; want the same 202", known.status, known.body, unknown.status, unknown.body)
	}
	fresh := tokenFrom(t, e.mailer.take()) // only the known, unverified address got one
	if r := e.post(t, "/api/v1/auth/verify-email", map[string]string{"token": old}); r.status != http.StatusBadRequest {
		t.Fatalf("the replaced link = %d", r.status)
	}
	if r := e.post(t, "/api/v1/auth/verify-email", map[string]string{"token": fresh}); r.status != http.StatusOK {
		t.Fatalf("the new link = %d %s", r.status, r.body)
	}
	if r := resend("dana@example.com", "good"); r.status != http.StatusAccepted || len(e.mailer.take()) != 0 {
		t.Fatalf("verified address = %d, or a mail went out", r.status)
	}

	if r := resend("not an address", "good"); r.status != http.StatusBadRequest || errorCode(t, r) != "invalid_request" {
		t.Fatalf("malformed = %d %s", r.status, r.body)
	}
	if r := resend("dana@example.com", ""); r.status != http.StatusBadRequest || errorCode(t, r) != "captcha_failed" {
		t.Fatalf("no challenge = %d %s", r.status, r.body)
	}
	e.captcha.setDown(true)
	if r := resend("dana@example.com", "good"); r.status != http.StatusServiceUnavailable {
		t.Fatalf("siteverify down = %d", r.status)
	}
	e.captcha.setDown(false)
	if r := postJSON(t, e.srv, "/api/v1/auth/resend-verification", "text/plain", "{}", false); r.status != http.StatusUnsupportedMediaType {
		t.Fatalf("text/plain = %d", r.status)
	}
	if r := doFull(t, e.srv, http.MethodGet, "/api/v1/auth/resend-verification"); r.status != http.StatusMethodNotAllowed {
		t.Fatalf("GET = %d", r.status)
	}
	e.rs.err = errors.New("db down")
	if r := resend("dana@example.com", "good"); r.status != http.StatusInternalServerError {
		t.Fatalf("store down = %d", r.status)
	}
	e.rs.err = nil
	for range service.RegisterPerIPPerHour {
		_ = resend("dana@example.com", "good")
	}
	if r := resend("dana@example.com", "good"); r.status != http.StatusTooManyRequests {
		t.Fatalf("over the limit = %d", r.status)
	}
}

// Login asks for the challenge after 3 failures, and only then.
func TestLoginChallenge(t *testing.T) {
	e := newRegEnv(t, regSetup{captchaOnLogin: true})
	pw := fixtures(t).viewerPassword
	for i := 1; i <= 2; i++ {
		if r := e.login(t, "viewer", "wrong-"+pw, ""); r.status != http.StatusUnauthorized || errorCode(t, r) != "invalid_credentials" {
			t.Fatalf("failure %d = %d %s", i, r.status, r.body)
		}
	}
	if r := e.login(t, "viewer", "wrong-"+pw, ""); r.status != http.StatusUnauthorized || errorCode(t, r) != "captcha_required" {
		t.Fatalf("3rd failure = %d %s; want captcha_required", r.status, r.body)
	}
	if r := e.login(t, "viewer", pw, ""); r.status != http.StatusUnauthorized || errorCode(t, r) != "captcha_required" {
		t.Fatalf("right password without the challenge = %d %s", r.status, r.body)
	}
	e.captcha.setDown(true)
	if r := e.login(t, "viewer", pw, "good"); r.status != http.StatusServiceUnavailable || errorCode(t, r) != "captcha_unavailable" {
		t.Fatalf("challenge down = %d %s", r.status, r.body)
	}
	e.captcha.setDown(false)
	if r := e.login(t, "viewer", pw, "good"); r.status != http.StatusOK {
		t.Fatalf("solved + right password = %d %s", r.status, r.body)
	}
	// Another address, or another user, is not affected: and the admin
	// (a useradmin account, no email) logs in as always.
	if r := loginAs(t, e.srv, "admin", fixtures(t).adminPassword); r.status != http.StatusOK {
		t.Fatalf("admin = %d", r.status)
	}
}
