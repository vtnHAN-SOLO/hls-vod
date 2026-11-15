package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/vtnHAN-SOLO/hls-vod/internal/service"
)

// Error codes: stable, machine-readable, documented in docs/openapi.yaml.
const (
	codeInvalidRequest   = "invalid_request"
	codeNotFound         = "not_found"
	codeNotReady         = "not_ready"
	codeMethodNotAllowed = "method_not_allowed"
	codeInternal         = "internal"

	codeUnauthorized         = "unauthorized"
	codeInvalidCredentials   = "invalid_credentials" // #nosec G101 -- an error code name, not a credential
	codeForbidden            = "forbidden"
	codeRateLimited          = "rate_limited"
	codeUnsupportedMediaType = "unsupported_media_type"

	codeCaptchaRequired    = "captcha_required"
	codeCaptchaFailed      = "captcha_failed"
	codeCaptchaUnavailable = "captcha_unavailable"
	codeEmailNotVerified   = "email_not_verified"
	codeAccountDisabled    = "account_disabled"
	codeRegistrationClosed = "registration_closed"
	codeUsernameTaken      = "username_taken"
	codeLastLoginMethod    = "last_login_method"
	codeInvalidToken       = "invalid_token" // #nosec G101 -- an error code name, not a credential
)

// errorBody is the one error shape every /api/ response uses.
type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, errorBody{errorDetail{Code: code, Message: msg}})
}

// writeServiceError maps service errors to status codes. Anything unknown is
// a 500 whose detail goes to the log only, never to the client.
func writeServiceError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error) {
	var verr *service.ValidationError
	var notReady *service.NotReadyError
	switch {
	case errors.As(err, &verr):
		writeError(w, http.StatusBadRequest, codeInvalidRequest, verr.Error())
	case errors.Is(err, service.ErrUnauthenticated):
		writeUnauthorized(w, "an access token is required")
	case errors.Is(err, service.ErrForbidden):
		writeError(w, http.StatusForbidden, codeForbidden, "this needs the admin role")
	case errors.Is(err, service.ErrNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, "movie not found")
	case errors.Is(err, service.ErrSessionNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, "session not found")
	case errors.As(err, &notReady):
		writeError(w, http.StatusConflict, codeNotReady,
			"movie is not ready for playback (status: "+string(notReady.Status)+")")
	default:
		logger.ErrorContext(r.Context(), "request failed",
			"method", r.Method, "path", r.URL.Path, "client_ip", clientIP(r), "err", err)
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
	}
}

// apiFallback serves mux, except that for /api/ paths no route matches it
// replaces ServeMux's plain-text 404 / 405 with the JSON error shape (keeping
// the Allow header on 405).
func apiFallback(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			mux.ServeHTTP(w, r)
			return
		}
		h, pattern := mux.Handler(r)
		if pattern != "" {
			mux.ServeHTTP(w, r)
			return
		}
		rec := &statusRecorder{header: http.Header{}}
		h.ServeHTTP(rec, r)
		switch rec.status {
		case http.StatusMethodNotAllowed:
			w.Header().Set("Allow", rec.header.Get("Allow"))
			writeError(w, http.StatusMethodNotAllowed, codeMethodNotAllowed, r.Method+" is not allowed here")
		case http.StatusNotFound:
			writeError(w, http.StatusNotFound, codeNotFound, "no such endpoint")
		default: // e.g. a redirect to the cleaned path: pass it through
			mux.ServeHTTP(w, r)
		}
	})
}

// statusRecorder captures the status and headers ServeMux's internal
// 404/405 handlers write, and discards their text body.
type statusRecorder struct {
	header http.Header
	status int
}

func (s *statusRecorder) Header() http.Header         { return s.header }
func (s *statusRecorder) Write(b []byte) (int, error) { return len(b), nil }
func (s *statusRecorder) WriteHeader(code int)        { s.status = code }
