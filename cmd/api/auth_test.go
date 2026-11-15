package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/vtnHAN-SOLO/hls-vod/internal/auth"
	"github.com/vtnHAN-SOLO/hls-vod/internal/config"
	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
	"github.com/vtnHAN-SOLO/hls-vod/internal/pgtest"
	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
)

func randomSecret(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// testSigningKey is generated at test time: never a literal secret.
func testSigningKey() string { return randomSecret(48) }

// createAndLogin creates an admin user directly in the database (as
// cmd/useradmin does) and logs in over HTTP. It returns the access token.
func createAndLogin(t *testing.T, dsn, base, username string) string {
	t.Helper()
	st, err := store.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	password := randomSecret(18)
	hash, err := auth.NewHasher(1).Hash(t.Context(), password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUser(t.Context(), username, hash, model.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	r := post(t, base+"/api/v1/auth/login", `{"username":"`+username+`","password":"`+password+`"}`, nil)
	if r.status != http.StatusOK {
		t.Fatalf("login = %d: %s", r.status, r.body)
	}
	return r.json["access_token"].(string)
}

type httpResult struct {
	status  int
	body    string
	json    map[string]any
	cookies []*http.Cookie
}

// post sends a JSON body (login) or, with body "", an empty POST carrying
// the CSRF header and the given cookies (refresh, logout).
func post(t *testing.T, url, body string, cookies []*http.Cookie) httpResult {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	} else {
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	r := httpResult{status: resp.StatusCode, body: string(raw), cookies: resp.Cookies()}
	_ = json.Unmarshal(raw, &r.json)
	return r
}

func refreshCookie(t *testing.T, r httpResult) *http.Cookie {
	t.Helper()
	for _, c := range r.cookies {
		if c.Name == "refresh_token" && c.Value != "" {
			return &http.Cookie{Name: c.Name, Value: c.Value} // #nosec G124 -- a request cookie the test client sends back
		}
	}
	t.Fatalf("no refresh_token cookie (status %d): %s", r.status, r.body)
	return nil
}

// The PR's verify step, automated against real Postgres and the real
// wiring: login, use the token, refresh, reuse the OLD refresh token, and
// see the whole session revoked (both refresh tokens dead).
func TestLoginRefreshReuseEndToEnd(t *testing.T) {
	db := pgtest.Start(t)
	if _, err := db.Migrator(t).Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(),
		`INSERT INTO movies (title, source_path, status) VALUES ('Ready', 'sources/r.mkv', 'ready'), ('Queued', 'sources/q.mkv', 'pending')`); err != nil {
		t.Fatal(err)
	}
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.API{DatabaseURL: db.DSN, ShutdownTimeout: 5 * time.Second, JWTSigningKey: testSigningKey(), MediaSigningKey: testSigningKey(),
		HTTPAddr: "127.0.0.1:0"}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- run(ctx, cfg, slog.New(slog.DiscardHandler), ln) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("run: %v", err)
		}
	})
	base := "http://" + ln.Addr().String()

	// Without a token: 401.
	if status, _ := getJSON(t, base+"/api/v1/movies", ""); status != http.StatusUnauthorized {
		t.Fatalf("movies without a token = %d", status)
	}

	// A plain user, as `useradmin add` would create it.
	st, err := store.New(t.Context(), db.DSN)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	password := randomSecret(18)
	hash, err := auth.NewHasher(1).Hash(t.Context(), password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUser(t.Context(), "viewer", hash, model.RoleUser); err != nil {
		t.Fatal(err)
	}

	login := post(t, base+"/api/v1/auth/login", `{"username":"viewer","password":"`+password+`"}`, nil)
	if login.status != http.StatusOK {
		t.Fatalf("login = %d: %s", login.status, login.body)
	}
	token := login.json["access_token"].(string)
	old := refreshCookie(t, login)
	for _, c := range login.cookies {
		// The api in this test listens on loopback: no Secure flag, but
		// still HttpOnly, SameSite=Strict, scoped to /api/v1/auth.
		if c.Name == "refresh_token" && (!c.HttpOnly || c.Secure || c.SameSite != http.SameSiteStrictMode || c.Path != "/api/v1/auth") {
			t.Fatalf("cookie = %+v", c)
		}
	}

	status, body := getJSON(t, base+"/api/v1/movies", token)
	if status != http.StatusOK || body["total"] != float64(1) {
		t.Fatalf("movies as a user = %d %v, want only the ready one", status, body)
	}

	// Watch and resume: save a position, see it in continue-watching and
	// as playback's resume_sec.
	readyID := body["items"].([]any)[0].(map[string]any)["id"].(string)
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, base+"/api/v1/progress",
		strings.NewReader(`{"movie_id":"`+readyID+`","position_sec":30}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("save progress = %d", resp.StatusCode)
	}
	if status, cw := getJSON(t, base+"/api/v1/continue-watching", token); status != http.StatusOK ||
		len(cw["items"].([]any)) != 1 || cw["items"].([]any)[0].(map[string]any)["position_sec"] != float64(30) {
		t.Fatalf("continue-watching = %d %v", status, cw)
	}
	if status, pb := getJSON(t, base+"/api/v1/movies/"+readyID+"/playback", token); status != http.StatusOK || pb["resume_sec"] != float64(30) {
		t.Fatalf("playback = %d %v, want resume_sec 30", status, pb)
	}

	refreshed := post(t, base+"/api/v1/auth/refresh", "", []*http.Cookie{old})
	if refreshed.status != http.StatusOK {
		t.Fatalf("refresh = %d: %s", refreshed.status, refreshed.body)
	}
	newer := refreshCookie(t, refreshed)
	if status, _ := getJSON(t, base+"/api/v1/movies", refreshed.json["access_token"].(string)); status != http.StatusOK {
		t.Fatalf("the refreshed access token = %d", status)
	}

	// Reuse the OLD refresh token: refused, and the whole family dies.
	if r := post(t, base+"/api/v1/auth/refresh", "", []*http.Cookie{old}); r.status != http.StatusUnauthorized {
		t.Fatalf("reused old token = %d: %s", r.status, r.body)
	}
	if r := post(t, base+"/api/v1/auth/refresh", "", []*http.Cookie{newer}); r.status != http.StatusUnauthorized {
		t.Fatalf("the newer token after reuse = %d, want it revoked too", r.status)
	}
	var live int
	if err := db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM refresh_tokens WHERE revoked_at IS NULL`).Scan(&live); err != nil || live != 0 {
		t.Fatalf("%d live refresh tokens, want 0", live)
	}
}
