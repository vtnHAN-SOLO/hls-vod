// Package handler holds the HTTP layer. Handlers stay thin: decode, call a
// service, encode. Business logic belongs in internal/service.
package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// ReadyTimeout bounds each dependency check behind /readyz.
const ReadyTimeout = 2 * time.Second

// Pinger is a dependency /readyz checks. *store.Store satisfies it.
type Pinger interface {
	Ping(ctx context.Context) error
}

// NewRouter builds the API's routes using Go 1.22+ ServeMux method patterns.
// Every /api/ response, errors included, matches docs/openapi.yaml.
//
// Every /api/v1 endpoint needs a valid access token except the /auth/ ones
// (login, refresh, logout, config, register, verify-email,
// resend-verification, forgot-password, reset-password, google/start,
// google/callback) and the signed poster images; the probes and /metrics
// stay open.
func NewRouter(logger *slog.Logger, reg *prometheus.Registry, db Pinger, catalog Catalog, a Auth, signup Registration, opts Options) http.Handler {
	mux := http.NewServeMux()
	protect := requireAuth(a)

	mux.Handle("GET /api/v1/movies", protect(listMovies(logger, catalog)))
	// More specific than /movies/{id}, so ServeMux picks these for the two
	// literal names (never valid UUIDs anyway).
	mux.Handle("GET /api/v1/movies/popular", protect(popularMovies(logger, catalog)))
	mux.Handle("GET /api/v1/movies/featured", protect(featuredMovies(logger, catalog)))
	mux.Handle("GET /api/v1/movies/{id}", protect(getMovie(logger, catalog)))
	mux.Handle("GET /api/v1/movies/{id}/playback", protect(playback(logger, catalog)))
	mux.Handle("POST /api/v1/progress", protect(saveProgress(logger, catalog)))
	mux.Handle("GET /api/v1/continue-watching", protect(continueWatching(logger, catalog)))
	// No access token: the signed URL is the permission (an <img> sends no
	// Authorization header), as for media-delivery's /media/.
	mux.Handle("GET /api/v1/posters/{id}/{width}/{file}", servePoster(logger, catalog))

	// The account page: always the access token's own user.
	mux.Handle("GET /api/v1/me", protect(getMe(logger, a)))
	mux.Handle("POST /api/v1/me/password", protect(changePassword(logger, a, opts)))
	mux.Handle("GET /api/v1/me/sessions", protect(listSessions(logger, a)))
	mux.Handle("DELETE /api/v1/me/sessions/{id}", protect(revokeSession(logger, a)))
	mux.Handle("POST /api/v1/me/sessions/revoke-others", protect(revokeOtherSessions(logger, a)))

	mux.Handle("POST /api/v1/auth/login", login(logger, a, opts))
	mux.Handle("POST /api/v1/auth/refresh", refresh(logger, a, opts))
	mux.Handle("POST /api/v1/auth/logout", logout(logger, a, opts))

	// Sign-up. Registration has its own kill switch (403); the rest works
	// either way.
	mux.Handle("GET /api/v1/auth/config", authConfig(signup))
	mux.Handle("POST /api/v1/auth/register", register(logger, signup))
	mux.Handle("POST /api/v1/auth/verify-email", verifyEmail(logger, signup, opts))
	mux.Handle("POST /api/v1/auth/resend-verification", resendVerification(logger, signup))

	// Sign in with Google (opts.Google nil: off). start and callback are
	// browser navigations, not fetches; they answer with redirects.
	mux.Handle("GET /api/v1/auth/google/start", googleStart(logger, opts))
	mux.Handle("GET /api/v1/auth/google/callback", googleCallback(logger, opts))
	mux.Handle("POST /api/v1/me/google/link", protect(googleLink(logger, opts)))
	mux.Handle("DELETE /api/v1/me/google", protect(googleUnlink(logger, a)))
	mux.Handle("POST /api/v1/me/password/set-link", protect(passwordSetLink(logger, signup)))

	// Forgot / reset password. An account's email never changes.
	mux.Handle("POST /api/v1/auth/forgot-password", forgotPassword(logger, signup))
	mux.Handle("POST /api/v1/auth/reset-password", resetPassword(logger, signup))

	// The admin area: each endpoint checks the role in the database.
	if opts.Admin != nil {
		registerAdmin(mux, logger, protect, opts.Admin, catalog)
	}

	RegisterProbes(mux, logger, reg, db)

	return withClientIP(opts.ClientIP, apiFallback(mux))
}

// RegisterProbes adds /healthz (liveness), /readyz (readiness: db answers)
// and /metrics to mux. Every long-running Go service uses these, so the
// probe semantics are the same everywhere.
func RegisterProbes(mux *http.ServeMux, logger *slog.Logger, reg *prometheus.Registry, db Pinger) {
	mux.HandleFunc("GET /healthz", healthz)
	mux.Handle("GET /readyz", readyz(logger, db))
	mux.Handle("GET /metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{
		ErrorLog: slog.NewLogLogger(logger.Handler(), slog.LevelError),
		Registry: reg,
	}))
}

// NewProbeRouter serves only the probes, for services with no API of their
// own (the dispatcher).
func NewProbeRouter(logger *slog.Logger, reg *prometheus.Registry, db Pinger) http.Handler {
	mux := http.NewServeMux()
	RegisterProbes(mux, logger, reg, db)
	return mux
}

// healthz is liveness: process only. It must never check Postgres or any
// other dependency, or a DB blip would make Kubernetes restart every pod.
func healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

type readyResponse struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
}

// readyz is readiness: 200 when Postgres answers within ReadyTimeout, else
// 503. Failing it takes the pod out of the Service without restarting it.
func readyz(logger *slog.Logger, db Pinger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), ReadyTimeout)
		defer cancel()

		resp := readyResponse{Status: "ready", Checks: map[string]string{"postgres": "ok"}}
		code := http.StatusOK
		if err := db.Ping(ctx); err != nil {
			// Details go to the log only; the body may be reachable from outside.
			logger.WarnContext(r.Context(), "readiness check failed", "check", "postgres", "err", err)
			resp = readyResponse{Status: "unavailable", Checks: map[string]string{"postgres": "unreachable"}}
			code = http.StatusServiceUnavailable
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(resp)
	}
}
