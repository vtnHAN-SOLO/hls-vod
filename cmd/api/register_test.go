package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vtnHAN-SOLO/hls-vod/internal/auth"
	"github.com/vtnHAN-SOLO/hls-vod/internal/config"
	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
	"github.com/vtnHAN-SOLO/hls-vod/internal/pgtest"
	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
)

// fakeResend records the mails the api sends; it never talks to Resend.
type fakeResend struct {
	mu   sync.Mutex
	key  string
	sent []map[string]any
	got  chan struct{}
}

func (f *fakeResend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+f.key {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var m map[string]any
	_ = json.NewDecoder(r.Body).Decode(&m)
	f.mu.Lock()
	f.sent = append(f.sent, m)
	f.mu.Unlock()
	_, _ = w.Write([]byte(`{"id":"fake"}`))
	f.got <- struct{}{}
}

func (f *fakeResend) next(t *testing.T) map[string]any {
	t.Helper()
	select {
	case <-f.got:
	case <-time.After(10 * time.Second):
		t.Fatal("no mail reached Resend")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sent[len(f.sent)-1]
}

// fakeSiteverify passes "good" unless down.
type fakeSiteverify struct {
	mu     sync.Mutex
	secret string
	down   bool
}

func (f *fakeSiteverify) setDown(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.down = v
}

func (f *fakeSiteverify) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	down := f.down
	f.mu.Unlock()
	if down {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	ok := r.FormValue("secret") == f.secret && r.FormValue("response") == "good"
	_ = json.NewEncoder(w).Encode(map[string]any{"success": ok, "error-codes": []string{}})
}

// e2e is the api running against real Postgres, with Resend and siteverify
// faked.
type e2e struct {
	db         *pgtest.DB
	base       string
	cfg        config.API
	resend     *fakeResend
	siteverify *fakeSiteverify
}

// startE2E starts the api; each tweak may change the config first.
func startE2E(t *testing.T, tweaks ...func(*config.API)) *e2e {
	t.Helper()
	db := pgtest.Start(t)
	if _, err := db.Migrator(t).Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	resend := &fakeResend{key: randomSecret(24), got: make(chan struct{}, 10)}
	resendSrv := httptest.NewServer(resend)
	t.Cleanup(resendSrv.Close)
	siteverify := &fakeSiteverify{secret: randomSecret(24)}
	siteverifySrv := httptest.NewServer(siteverify)
	t.Cleanup(siteverifySrv.Close)

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.API{DatabaseURL: db.DSN, ShutdownTimeout: 5 * time.Second, JWTSigningKey: testSigningKey(), MediaSigningKey: testSigningKey(),
		HTTPAddr: "127.0.0.1:0", RegistrationOpen: true, MailBackend: config.MailBackendResend, ResendAPIKey: resend.key,
		MailFrom: "HLS VOD <noreply@media.example>", PublicBaseURL: "http://127.0.0.1:5173/", MailDailyCap: 90,
		TurnstileSiteKey: "site-key", TurnstileSecretKey: siteverify.secret,
		ResendEndpoint: resendSrv.URL, SiteverifyEndpoint: siteverifySrv.URL}
	for _, tweak := range tweaks {
		tweak(&cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- run(ctx, cfg, slog.New(slog.DiscardHandler), ln) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("run: %v", err)
		}
	})
	return &e2e{db: db, base: "http://" + ln.Addr().String(), cfg: cfg, resend: resend, siteverify: siteverify}
}

// linkToken is the token of the link to page in a mail's text.
func linkToken(t *testing.T, m map[string]any, page string) string {
	t.Helper()
	text, _ := m["text"].(string)
	prefix := "http://127.0.0.1:5173" + page + "#token="
	i := strings.Index(text, prefix)
	if i < 0 {
		t.Fatalf("no %s link in %v", page, m)
	}
	return strings.Fields(text[i+len(prefix):])[0]
}

// The PR's verify step, automated against real Postgres and the real
// wiring, with Resend and siteverify faked: register → the mail → open the
// link → logged in → can play a movie. Then the guards around it.
func TestRegistrationEndToEnd(t *testing.T) {
	e := startE2E(t)
	db, cfg, resend, siteverify, base := e.db, e.cfg, e.resend, e.siteverify, e.base
	var movieID string
	if err := db.Pool.QueryRow(t.Context(),
		`INSERT INTO movies (title, source_path, library_path, status) VALUES ('Ready', 'sources/r.mkv', 'library/r', 'ready') RETURNING id::text`).
		Scan(&movieID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `UPDATE movies SET library_path = 'library/' || id::text WHERE id = $1`, movieID); err != nil {
		t.Fatal(err)
	}
	password := randomSecret(18)

	if status, c := getJSON(t, base+"/api/v1/auth/config", ""); status != http.StatusOK || c["registration_open"] != true || c["turnstile_site_key"] != "site-key" {
		t.Fatalf("config = %d %v", status, c)
	}

	r := post(t, base+"/api/v1/auth/register",
		`{"email":"Dana@Example.com","username":"dana","password":"`+password+`","turnstile_token":"good"}`, nil)
	if r.status != http.StatusAccepted {
		t.Fatalf("register = %d %s", r.status, r.body)
	}
	m := resend.next(t)
	text, _ := m["text"].(string)
	const prefix = "http://127.0.0.1:5173/verify-email#token="
	i := strings.Index(text, prefix)
	if to, _ := m["to"].([]any); len(to) != 1 || to[0] != "dana@example.com" || m["from"] != cfg.MailFrom || i < 0 {
		t.Fatalf("mail = %v", m)
	}
	token := strings.Fields(text[i+len(prefix):])[0]

	// The ledger keeps a hash, never the address.
	var rows int
	if err := db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM outgoing_mail WHERE kind = 'verify'`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("outgoing_mail rows = %d, %v", rows, err)
	}

	login := post(t, base+"/api/v1/auth/login", `{"username":"dana","password":"`+password+`"}`, nil)
	if login.status != http.StatusForbidden || !strings.Contains(login.body, "email_not_verified") {
		t.Fatalf("login before verifying = %d %s", login.status, login.body)
	}

	v := post(t, base+"/api/v1/auth/verify-email", `{"token":"`+token+`"}`, nil)
	if v.status != http.StatusOK {
		t.Fatalf("verify = %d %s", v.status, v.body)
	}
	_ = refreshCookie(t, v)
	access := v.json["access_token"].(string)
	if status, body := getJSON(t, base+"/api/v1/movies/"+movieID+"/playback", access); status != http.StatusOK || body["playlist_url"] == nil {
		t.Fatalf("playback with the new account = %d %v", status, body)
	}
	if again := post(t, base+"/api/v1/auth/verify-email", `{"token":"`+token+`"}`, nil); again.status != http.StatusBadRequest {
		t.Fatalf("reused link = %d", again.status)
	}

	// The same address again: same 202, a notice instead of a link, no account.
	r = post(t, base+"/api/v1/auth/register",
		`{"email":"dana@example.com","username":"dana2","password":"`+password+`","turnstile_token":"good"}`, nil)
	if r.status != http.StatusAccepted {
		t.Fatalf("register a taken address = %d %s", r.status, r.body)
	}
	if notice := resend.next(t); !strings.Contains(notice["subject"].(string), "Someone tried to sign up") {
		t.Fatalf("notice = %v", notice)
	}

	// useradmin accounts (no email) log in as before.
	st, err := store.New(t.Context(), db.DSN)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	adminPassword := randomSecret(18)
	hash, err := auth.NewHasher(1).Hash(t.Context(), adminPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUser(t.Context(), "root", hash, model.RoleAdmin); err != nil {
		t.Fatal(err)
	}

	// Fail closed: siteverify down → register 503; login still works.
	siteverify.setDown(true)
	r = post(t, base+"/api/v1/auth/register",
		`{"email":"x@example.com","username":"xavier","password":"`+password+`","turnstile_token":"good"}`, nil)
	if r.status != http.StatusServiceUnavailable {
		t.Fatalf("register with siteverify down = %d %s", r.status, r.body)
	}
	for _, who := range [][2]string{{"root", adminPassword}, {"dana", password}} {
		if l := post(t, base+"/api/v1/auth/login", `{"username":"`+who[0]+`","password":"`+who[1]+`"}`, nil); l.status != http.StatusOK {
			t.Fatalf("login as %s with siteverify down = %d %s", who[0], l.status, l.body)
		}
	}
}
