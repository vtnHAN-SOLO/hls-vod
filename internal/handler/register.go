package handler

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/vtnHAN-SOLO/hls-vod/internal/service"
)

// Registration is the service behind sign-up, email verification and
// password reset.
// *service.Registration satisfies it.
type Registration interface {
	Config() service.AuthConfig
	Register(ctx context.Context, in service.RegisterInput) error
	ResendVerification(ctx context.Context, email, captchaToken, clientIP string) error
	VerifyEmail(ctx context.Context, token, userAgent string) (service.Session, error)

	ForgotPassword(ctx context.Context, email, captchaToken, clientIP string) error
	ResetPassword(ctx context.Context, token, newPassword string) error
	PasswordSetLink(ctx context.Context, p service.Principal) error
}

type authConfigJSON struct {
	RegistrationOpen bool    `json:"registration_open"`
	TurnstileSiteKey *string `json:"turnstile_site_key"`
	GoogleEnabled    bool    `json:"google_enabled"`
}

func authConfig(reg Registration) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		c := reg.Config()
		out := authConfigJSON{RegistrationOpen: c.RegistrationOpen, GoogleEnabled: c.GoogleEnabled}
		if c.TurnstileSiteKey != "" {
			out.TurnstileSiteKey = &c.TurnstileSiteKey
		}
		w.Header().Set("Cache-Control", "no-cache")
		writeJSON(w, http.StatusOK, out)
	}
}

// acceptedJSON is the one answer register, resend and forgot-password give,
// whatever happened to the address: it must not tell whether an account
// exists.
type acceptedJSON struct {
	Message string `json:"message"`
}

func writeAccepted(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusAccepted, acceptedJSON{
		Message: "If this address can be used, a mail is on its way. Check your inbox (and the spam folder).",
	})
}

type registerRequest struct {
	Email          string `json:"email"`
	Username       string `json:"username"`
	Password       string `json:"password"`
	TurnstileToken string `json:"turnstile_token"`
}

func register(logger *slog.Logger, reg Registration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req registerRequest
		if !decodeJSON(w, r, &req, `body must be {"email": …, "username": …, "password": …, "turnstile_token": …}`) {
			return
		}
		if err := reg.Register(r.Context(), service.RegisterInput{
			Email: req.Email, Username: req.Username, Password: req.Password,
			CaptchaToken: req.TurnstileToken, ClientIP: clientIP(r),
		}); err != nil {
			writeAuthError(w, r, logger, err)
			return
		}
		writeAccepted(w)
	}
}

type resendRequest struct {
	Email          string `json:"email"`
	TurnstileToken string `json:"turnstile_token"`
}

func resendVerification(logger *slog.Logger, reg Registration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req resendRequest
		if !decodeJSON(w, r, &req, `body must be {"email": …, "turnstile_token": …}`) {
			return
		}
		if err := reg.ResendVerification(r.Context(), req.Email, req.TurnstileToken, clientIP(r)); err != nil {
			writeAuthError(w, r, logger, err)
			return
		}
		writeAccepted(w)
	}
}

type verifyEmailRequest struct {
	Token string `json:"token"`
}

func verifyEmail(logger *slog.Logger, reg Registration, opts Options) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req verifyEmailRequest
		if !decodeJSON(w, r, &req, `body must be {"token": …}`) {
			return
		}
		s, err := reg.VerifyEmail(r.Context(), req.Token, r.UserAgent())
		if err != nil {
			writeAuthError(w, r, logger, err)
			return
		}
		writeSession(w, s, opts)
	}
}
