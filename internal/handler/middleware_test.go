package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vtnHAN-SOLO/hls-vod/internal/clientip"
)

// Every handler sees the one resolved address, and every response says
// nosniff.
func TestWithClientIP(t *testing.T) {
	trusting, err := clientip.Parse("10.244.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		resolver *clientip.Resolver
		peer     string
		want     string
	}{
		{"no proxies: a spoofed header is ignored", nil, "192.0.2.10:4000", "192.0.2.10"},
		{"trusted peer: the forwarded client", trusting, "10.244.0.5:4000", "1.2.3.4"},
		{"untrusted peer: the peer", trusting, "192.0.2.10:4000", "192.0.2.10"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got string
			h := withClientIP(tt.resolver, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = clientIP(r) }))
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/auth/login", nil)
			req.RemoteAddr = tt.peer
			req.Header.Set("X-Forwarded-For", "1.2.3.4")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if got != tt.want {
				t.Fatalf("clientIP = %q, want %q", got, tt.want)
			}
			if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatal("no X-Content-Type-Options: nosniff")
			}
		})
	}
}
