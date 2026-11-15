package main

import (
	"context"
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

// loginAs logs in from a browser with userAgent and returns its access
// token and refresh cookie.
func loginAs(t *testing.T, base, username, password, userAgent string) (string, *http.Cookie) {
	t.Helper()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, base+"/api/v1/auth/login",
		strings.NewReader(`{"username":"`+username+`","password":"`+password+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)
	r := do(t, req)
	if r.status != http.StatusOK {
		t.Fatalf("login = %d: %s", r.status, r.body)
	}
	return r.json["access_token"].(string), refreshCookie(t, r)
}

func do(t *testing.T, req *http.Request) httpResult {
	t.Helper()
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

// The PR's verify step, automated against real Postgres: Firefox and
// Chrome logged in as one user; Firefox sees both sessions and changes the
// password; Firefox keeps working, Chrome's session is dead.
func TestPasswordChangeEndToEnd(t *testing.T) {
	db := pgtest.Start(t)
	if _, err := db.Migrator(t).Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.API{DatabaseURL: db.DSN, ShutdownTimeout: 5 * time.Second, JWTSigningKey: testSigningKey(),
		MediaSigningKey: testSigningKey(), HTTPAddr: "127.0.0.1:0"}
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

	st, err := store.New(t.Context(), db.DSN)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	password, next := randomSecret(18), randomSecret(18)
	hash, err := auth.NewHasher(1).Hash(t.Context(), password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUser(t.Context(), "viewer", hash, model.RoleUser); err != nil {
		t.Fatal(err)
	}

	firefox, _ := loginAs(t, base, "viewer", password,
		"Mozilla/5.0 (X11; Linux x86_64; rv:140.0) Gecko/20100101 Firefox/140.0")
	_, chromeCookie := loginAs(t, base, "viewer", password,
		"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36")

	sessions := func(token string) []map[string]any {
		t.Helper()
		status, body := getJSON(t, base+"/api/v1/me/sessions", token)
		if status != http.StatusOK {
			t.Fatalf("sessions = %d %v", status, body)
		}
		var out []map[string]any
		for _, s := range body["items"].([]any) {
			out = append(out, s.(map[string]any))
		}
		return out
	}
	ss := sessions(firefox)
	if len(ss) != 2 || ss[0]["user_agent"] != "Chrome on Linux" || ss[0]["current"] != false ||
		ss[1]["user_agent"] != "Firefox on Linux" || ss[1]["current"] != true {
		t.Fatalf("sessions in Firefox = %v; want Chrome, and Firefox marked current", ss)
	}

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, base+"/api/v1/me/password",
		strings.NewReader(`{"current_password":"`+password+`","new_password":"`+next+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+firefox)
	changed := do(t, req)
	if changed.status != http.StatusOK {
		t.Fatalf("change password = %d: %s", changed.status, changed.body)
	}
	firefox = changed.json["access_token"].(string)
	if status, _ := getJSON(t, base+"/api/v1/me", firefox); status != http.StatusOK {
		t.Fatalf("Firefox after the change = %d", status)
	}
	if r := post(t, base+"/api/v1/auth/refresh", "", []*http.Cookie{refreshCookie(t, changed)}); r.status != http.StatusOK {
		t.Fatalf("Firefox's new refresh cookie = %d", r.status)
	} else {
		firefox = r.json["access_token"].(string)
	}
	if r := post(t, base+"/api/v1/auth/refresh", "", []*http.Cookie{chromeCookie}); r.status != http.StatusUnauthorized {
		t.Fatalf("Chrome's refresh after the change = %d, want 401", r.status)
	}
	if ss := sessions(firefox); len(ss) != 1 || ss[0]["current"] != true {
		t.Fatalf("sessions after the change = %v, want only Firefox", ss)
	}
	if r := post(t, base+"/api/v1/auth/login", `{"username":"viewer","password":"`+password+`"}`, nil); r.status != http.StatusUnauthorized {
		t.Fatalf("the old password = %d", r.status)
	}
}
