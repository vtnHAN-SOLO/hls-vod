package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vtnHAN-SOLO/hls-vod/internal/config"
	"github.com/vtnHAN-SOLO/hls-vod/internal/pgtest"
	"github.com/vtnHAN-SOLO/hls-vod/internal/service"
)

// End-to-end through run(): migrated Postgres, real store, service, router.
func TestCatalogEndToEnd(t *testing.T) {
	db := pgtest.Start(t)
	if _, err := db.Migrator(t).Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	var ready, pending string
	for _, m := range []struct {
		title, status string
		id            *string
	}{{"Ready Movie", "ready", &ready}, {"Pending Movie", "pending", &pending}} {
		if err := db.Pool.QueryRow(t.Context(),
			`INSERT INTO movies (title, source_path, status) VALUES ($1, $1, $2) RETURNING id::text`,
			m.title, m.status).Scan(m.id); err != nil {
			t.Fatal(err)
		}
	}
	db.Publish(t.Context(), t, ready)

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	cfg := config.API{DatabaseURL: db.DSN, ShutdownTimeout: 5 * time.Second, JWTSigningKey: testSigningKey(), MediaSigningKey: testSigningKey()}
	// Verifies the tokens the api hands out with the same key, as
	// media-delivery does.
	media, err := service.NewMediaSigner(cfg.MediaSigningKey, 0)
	if err != nil {
		t.Fatal(err)
	}
	go func() { done <- run(ctx, cfg, slog.New(slog.DiscardHandler), ln) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("run: %v", err)
		}
	})
	base := "http://" + ln.Addr().String()
	token := createAndLogin(t, db.DSN, base, "admin")

	tests := []struct {
		path   string
		status int
		check  func(t *testing.T, body map[string]any)
	}{
		{"/api/v1/movies?status=ready", 200, func(t *testing.T, b map[string]any) {
			if b["total"] != float64(1) || len(b["items"].([]any)) != 1 {
				t.Fatalf("body = %v", b)
			}
		}},
		{"/api/v1/movies/" + ready, 200, func(t *testing.T, b map[string]any) {
			if b["title"] != "Ready Movie" {
				t.Fatalf("body = %v", b)
			}
		}},
		{"/api/v1/movies/" + ready + "/playback", 200, func(t *testing.T, b map[string]any) {
			if signedFile(t, media, b["playlist_url"]) != ready+"/master.m3u8" ||
				signedFile(t, media, b["thumbnails_url"]) != ready+"/thumbnails.vtt" {
				t.Fatalf("body = %v", b)
			}
		}},
		// Uppercase in the request; Postgres returns the canonical lowercase id,
		// and that is what the URL must carry.
		{"/api/v1/movies/" + strings.ToUpper(ready) + "/playback", 200, func(t *testing.T, b map[string]any) {
			if b["movie_id"] != ready || signedFile(t, media, b["playlist_url"]) != ready+"/master.m3u8" {
				t.Fatalf("body = %v, want the lowercase id %s", b, ready)
			}
		}},
		{"/api/v1/movies/" + pending + "/playback", 409, nil},
		{"/api/v1/movies/9f8e7d6c-5b4a-4392-8170-6f5e4d3c2b1a", 404, nil},
		{"/api/v1/movies?limit=101", 400, nil},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			status, body := getJSON(t, base+tt.path, token)
			if status != tt.status {
				t.Fatalf("status = %d, want %d: %v", status, tt.status, body)
			}
			if tt.check != nil {
				tt.check(t, body)
			}
		})
	}
}

// signedFile checks that v is a media URL whose token media accepts and
// which lives about PlaybackURLTTL, and returns "{movie_id}/{file}".
func signedFile(t *testing.T, media *service.MediaSigner, v any) string {
	t.Helper()
	u, _ := v.(string)
	parts := strings.SplitN(strings.TrimPrefix(u, service.MediaPrefix), "/", 4)
	if len(parts) != 4 {
		t.Fatalf("%q is not /media/{token}/{expires}/{id}/{file}", u)
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		t.Fatalf("%q: expires %q", u, parts[1])
	}
	if left := time.Until(time.Unix(exp, 0)); left < service.PlaybackURLTTL-time.Minute || left > service.PlaybackURLTTL+16*time.Minute {
		t.Errorf("%q: valid for %v, want about %v", u, left, service.PlaybackURLTTL)
	}
	if parts[0] != media.Token(parts[2], exp) {
		t.Errorf("%q: token does not verify", u)
	}
	return parts[2] + "/" + parts[3]
}

func getJSON(t *testing.T, url, token string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("body is not JSON: %q", raw)
	}
	return resp.StatusCode, body
}
