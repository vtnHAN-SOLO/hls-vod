package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/vtnHAN-SOLO/hls-vod/internal/clientip"
	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
	"github.com/vtnHAN-SOLO/hls-vod/internal/service"
)

// Auth is the service behind login, the access-token check and the account
// page. *service.Auth satisfies it.
type Auth interface {
	Login(ctx context.Context, in service.LoginInput) (service.Session, error)
	Refresh(ctx context.Context, token string) (service.Session, error)
	Logout(ctx context.Context, token string) error
	Authenticate(token string) (service.Principal, error)

	Me(ctx context.Context, p service.Principal) (model.User, error)
	Account(ctx context.Context, p service.Principal) (service.Account, error)
	UnlinkGoogle(ctx context.Context, p service.Principal) error
	ChangePassword(ctx context.Context, p service.Principal, current, next, userAgent string) (service.Session, error)
	Sessions(ctx context.Context, p service.Principal) ([]service.SessionInfo, error)
	RevokeSession(ctx context.Context, p service.Principal, id string) error
	RevokeOtherSessions(ctx context.Context, p service.Principal) error
}

// Options are the router settings that depend on how the api is deployed.
type Options struct {
	// SecureCookies sets Secure on the refresh cookie. True everywhere except
	// an api listening on loopback (local development over plain http).
	SecureCookies bool
	// Google is sign in with Google; nil when it is not configured.
	Google Google
	// Admin serves /api/v1/admin; nil leaves those routes out (404).
	Admin Admin
	// ClientIP finds the client behind the trusted proxies (TRUSTED_PROXIES);
	// nil trusts none: the client is the TCP peer.
	ClientIP *clientip.Resolver
}

const (
	// RefreshCookie holds the refresh token. HttpOnly, so page scripts never
	// see it; scoped to the auth endpoints, so it is not sent anywhere else.
	RefreshCookie = "refresh_token"
	refreshPath   = "/api/v1/auth"
	// CSRFHeader must be present on refresh and logout. A cross-site form
	// cannot set it, and a cross-site fetch that does triggers a CORS
	// preflight the api never answers. SameSite=Strict is the other half.
	CSRFHeader   = "X-Requested-With"
	maxLoginBody = 4 << 10
)

type principalKey struct{}

// principal is the caller requireAuth verified.
func principal(r *http.Request) service.Principal {
	p, _ := r.Context().Value(principalKey{}).(service.Principal)
	return p
}

// requireAuth admits a request only with a valid "Authorization: Bearer"
// access token, and makes its principal available to the handler.
func requireAuth(a Auth) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok || token == "" {
				writeUnauthorized(w, "an access token is required")
				return
			}
			p, err := a.Authenticate(token)
			if err != nil {
				writeUnauthorized(w, "the access token is invalid or expired")
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
		})
	}
}

func writeUnauthorized(w http.ResponseWriter, msg string) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="hls-vod"`)
	writeError(w, http.StatusUnauthorized, codeUnauthorized, msg)
}

type loginRequest struct {
	Username       string `json:"username"`
	Password       string `json:"password"`
	TurnstileToken string `json:"turnstile_token"`
}

type userJSON struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

type tokenJSON struct {
	AccessToken string   `json:"access_token"`
	TokenType   string   `json:"token_type"`
	ExpiresIn   int64    `json:"expires_in"`
	User        userJSON `json:"user"`
}

func login(logger *slog.Logger, a Auth, opts Options) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req loginRequest
		if !decodeJSON(w, r, &req, `body must be {"username": …, "password": …}`) {
			return
		}
		s, err := a.Login(r.Context(), service.LoginInput{
			Username: req.Username, Password: req.Password,
			ClientIP: clientIP(r), UserAgent: r.UserAgent(), CaptchaToken: req.TurnstileToken,
		})
		if err != nil {
			writeAuthError(w, r, logger, err)
			return
		}
		writeSession(w, s, opts)
	}
}

func refresh(logger *slog.Logger, a Auth, opts Options) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(CSRFHeader) == "" {
			writeError(w, http.StatusForbidden, codeForbidden, CSRFHeader+" header is required")
			return
		}
		c, err := r.Cookie(RefreshCookie)
		if err != nil || c.Value == "" {
			clearRefreshCookie(w, opts)
			writeUnauthorized(w, "no refresh token")
			return
		}
		s, err := a.Refresh(r.Context(), c.Value)
		if err != nil {
			if errors.Is(err, service.ErrInvalidRefreshToken) {
				clearRefreshCookie(w, opts)
			}
			writeAuthError(w, r, logger, err)
			return
		}
		writeSession(w, s, opts)
	}
}

func logout(logger *slog.Logger, a Auth, opts Options) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(CSRFHeader) == "" {
			writeError(w, http.StatusForbidden, codeForbidden, CSRFHeader+" header is required")
			return
		}
		if c, err := r.Cookie(RefreshCookie); err == nil && c.Value != "" {
			if err := a.Logout(r.Context(), c.Value); err != nil {
				writeServiceError(w, r, logger, err)
				return
			}
		}
		clearRefreshCookie(w, opts)
		w.WriteHeader(http.StatusNoContent)
	}
}

// decodeJSON reads a small JSON body with no unknown fields into v, or
// answers 415 / 400 and returns false. JSON only: a cross-site HTML form
// can only send form encodings or text/plain, so it cannot post to these
// endpoints as the browser's user (for login: log a browser in as someone
// else).
func decodeJSON(w http.ResponseWriter, r *http.Request, v any, shape string) bool {
	return decodeJSONMax(w, r, v, shape, maxLoginBody)
}

// decodeJSONMax is decodeJSON with a body limit of its own: maxPasswordBody
// for bodies that carry a password of up to 1024 characters.
func decodeJSONMax(w http.ResponseWriter, r *http.Request, v any, shape string, limit int64) bool {
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, codeUnsupportedMediaType, "Content-Type must be application/json")
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidRequest, shape)
		return false
	}
	return true
}

func writeSession(w http.ResponseWriter, s service.Session, opts Options) {
	setRefreshCookie(w, s, opts)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, tokenJSON{
		AccessToken: s.AccessToken, TokenType: "Bearer",
		// Ceil: the token's exp is in whole seconds, so what remains is just
		// under the full 900 s.
		ExpiresIn: int64(math.Ceil(time.Until(s.AccessExpires).Seconds())),
		User:      userJSON{ID: s.User.ID, Username: s.User.Username, Role: string(s.User.Role)},
	})
}

func setRefreshCookie(w http.ResponseWriter, s service.Session, opts Options) {
	http.SetCookie(w, &http.Cookie{ // #nosec G124 -- Secure is on except for an api bound to loopback (plain-http dev); HttpOnly and SameSite=Strict always
		Name: RefreshCookie, Value: s.RefreshToken, Path: refreshPath,
		Expires: s.RefreshExpires, MaxAge: int(time.Until(s.RefreshExpires).Seconds()),
		HttpOnly: true, Secure: opts.SecureCookies, SameSite: http.SameSiteStrictMode,
	})
}

func clearRefreshCookie(w http.ResponseWriter, opts Options) {
	http.SetCookie(w, &http.Cookie{ // #nosec G124 -- as in writeSession: Secure except for plain-http dev on loopback
		Name: RefreshCookie, Value: "", Path: refreshPath, MaxAge: -1,
		HttpOnly: true, Secure: opts.SecureCookies, SameSite: http.SameSiteStrictMode,
	})
}

func writeAuthError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error) {
	var throttled *service.ThrottledError
	switch {
	case errors.As(err, &throttled):
		logger.InfoContext(r.Context(), "throttled", "path", r.URL.Path, "client_ip", clientIP(r),
			"retry_after", throttled.RetryAfter.Round(time.Second).String())
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(throttled.RetryAfter.Seconds()))))
		writeError(w, http.StatusTooManyRequests, codeRateLimited, "too many attempts; try again later")
	case errors.Is(err, service.ErrCaptchaRequired):
		// Before invalid credentials: the failure that reaches the threshold
		// is both, and the client must learn to show the challenge.
		msg := "solve the challenge to log in"
		if errors.Is(err, service.ErrInvalidCredentials) {
			msg = "invalid username or password; solve the challenge and try again"
		}
		writeError(w, http.StatusUnauthorized, codeCaptchaRequired, msg)
	case errors.Is(err, service.ErrCaptchaUnavailable):
		logger.WarnContext(r.Context(), "challenge check unavailable: request refused", "path", r.URL.Path,
			"client_ip", clientIP(r), "err", err)
		writeError(w, http.StatusServiceUnavailable, codeCaptchaUnavailable, "the challenge cannot be checked right now; try again later")
	case errors.Is(err, service.ErrCaptchaFailed):
		writeError(w, http.StatusBadRequest, codeCaptchaFailed, "the challenge was not solved; solve it again")
	case errors.Is(err, service.ErrAccountDisabled):
		writeError(w, http.StatusForbidden, codeAccountDisabled, "this account is disabled")
	case errors.Is(err, service.ErrEmailNotVerified):
		writeError(w, http.StatusForbidden, codeEmailNotVerified, "confirm your email address first: use the link we mailed you")
	case errors.Is(err, service.ErrRegistrationClosed):
		writeError(w, http.StatusForbidden, codeRegistrationClosed, "registration is closed")
	case errors.Is(err, service.ErrUsernameTaken):
		writeError(w, http.StatusConflict, codeUsernameTaken, "that username is taken")
	case errors.Is(err, service.ErrLastLoginMethod):
		writeError(w, http.StatusConflict, codeLastLoginMethod, "set a password before removing Google: it is your only way to log in")
	case errors.Is(err, service.ErrNoGoogle):
		writeError(w, http.StatusNotFound, codeNotFound, "no Google account is linked")
	case errors.Is(err, service.ErrInvalidEmailToken):
		writeError(w, http.StatusBadRequest, codeInvalidToken, "the link is invalid, expired or already used")
	case errors.Is(err, service.ErrInvalidCredentials):
		writeError(w, http.StatusUnauthorized, codeInvalidCredentials, "invalid username or password")
	case errors.Is(err, service.ErrWrongPassword):
		// 400, not 401: the access token is fine, and a 401 would make the
		// client refresh and retry.
		writeError(w, http.StatusBadRequest, codeInvalidCredentials, "the current password is incorrect")
	case errors.Is(err, service.ErrInvalidRefreshToken):
		writeUnauthorized(w, "the refresh token is invalid, expired or revoked")
	default:
		writeServiceError(w, r, logger, err)
	}
}
