package handler

import (
	"context"
	"net"
	"net/http"

	"github.com/vtnHAN-SOLO/hls-vod/internal/clientip"
)

type clientIPKey struct{}

// withClientIP resolves the client's address once per request (see
// clientip.Resolver.IP: X-Forwarded-For only through trusted proxies) for
// clientIP, and sets the headers every api response carries.
func withClientIP(resolver *clientip.Resolver, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// JSON and metrics only: nothing here is meant to be sniffed into
		// something a browser would run.
		w.Header().Set("X-Content-Type-Options", "nosniff")
		ip := resolver.IP(r)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), clientIPKey{}, ip)))
	})
}

// clientIP is the client's address as withClientIP resolved it: the one
// value the login throttles, the register / resend / forgot limits,
// Turnstile's remoteip and logs all use. Outside the router (a handler
// called directly in a test) it is the TCP peer.
func clientIP(r *http.Request) string {
	if ip, ok := r.Context().Value(clientIPKey{}).(string); ok {
		return ip
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
