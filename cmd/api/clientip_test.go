package main

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/vtnHAN-SOLO/hls-vod/internal/auth"
	"github.com/vtnHAN-SOLO/hls-vod/internal/config"
)

// The PR's first verify step, automated: with TRUSTED_PROXIES empty, a
// client that writes its own X-Forwarded-For is still throttled on its real
// address. Trusting the loopback peer, each forwarded address is its own
// client.
func TestLoginThrottleKeysOnTheRealClient(t *testing.T) {
	for _, tt := range []struct {
		name      string
		trusted   string
		throttled bool
	}{
		{"no trusted proxies: the spoofed header is ignored", "", true},
		{"loopback trusted: the forwarded address is the client", "127.0.0.1/32", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := startE2E(t, func(c *config.API) {
				// No Turnstile: the throttle answers instead of the challenge.
				c.RegistrationOpen, c.TurnstileSiteKey, c.TurnstileSecretKey = false, "", ""
				c.TrustedProxies = tt.trusted
			})
			body := `{"username":"nobody","password":"` + randomSecret(18) + `"}`
			loginFrom := func(xff string) int {
				req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, e.base+"/api/v1/auth/login",
					strings.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("X-Forwarded-For", xff)
				resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
					t.Fatal("no nosniff on the api's answer")
				}
				return resp.StatusCode
			}
			for i := range auth.MaxLoginFailures {
				if s := loginFrom(fmt.Sprintf("1.2.3.%d", i)); s != http.StatusUnauthorized {
					t.Fatalf("attempt %d = %d", i+1, s)
				}
			}
			s := loginFrom("1.2.3.99")
			if tt.throttled && s != http.StatusTooManyRequests {
				t.Fatalf("6th attempt = %d, want 429: the throttle keyed on the header", s)
			}
			if !tt.throttled && s != http.StatusUnauthorized {
				t.Fatalf("6th attempt from a new forwarded address = %d, want 401", s)
			}
		})
	}
}
