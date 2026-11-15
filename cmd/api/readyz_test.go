package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/vtnHAN-SOLO/hls-vod/internal/config"
	"github.com/vtnHAN-SOLO/hls-vod/internal/pgtest"
)

// End-to-end through run(): real Postgres container, real wiring. Stopping
// the container must flip /readyz to 503 while /healthz stays 200.
func TestReadinessFollowsPostgres(t *testing.T) {
	db := pgtest.Start(t)

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.API{DatabaseURL: db.DSN, ShutdownTimeout: 5 * time.Second, JWTSigningKey: testSigningKey(), MediaSigningKey: testSigningKey()}

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
	expect := func(t *testing.T, path string, want int) {
		t.Helper()
		if got := status(t, base+path); got != want {
			t.Fatalf("GET %s = %d, want %d", path, got, want)
		}
	}

	t.Run("postgres up", func(t *testing.T) {
		expect(t, "/readyz", http.StatusOK)
		expect(t, "/healthz", http.StatusOK)
	})

	stopTimeout := 10 * time.Second
	if err := db.Container.Stop(context.Background(), &stopTimeout); err != nil {
		t.Fatalf("stopping postgres container: %v", err)
	}

	t.Run("postgres stopped", func(t *testing.T) {
		expect(t, "/readyz", http.StatusServiceUnavailable)
		expect(t, "/healthz", http.StatusOK)
	})
}

func status(t *testing.T, url string) int {
	t.Helper()
	// Longer than handler.ReadyTimeout, so a hung ping shows up as 503, not a client timeout.
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}
