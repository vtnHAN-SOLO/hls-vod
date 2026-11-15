package handler

import (
	"log/slog"
	"net/http"
)

type forgotRequest struct {
	Email          string `json:"email"`
	TurnstileToken string `json:"turnstile_token"`
}

func forgotPassword(logger *slog.Logger, reg Registration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req forgotRequest
		if !decodeJSON(w, r, &req, `body must be {"email": …, "turnstile_token": …}`) {
			return
		}
		if err := reg.ForgotPassword(r.Context(), req.Email, req.TurnstileToken, clientIP(r)); err != nil {
			writeAuthError(w, r, logger, err)
			return
		}
		writeAccepted(w)
	}
}

type resetRequest struct {
	Token       string `json:"token"`
	NewPassword string `json:"new_password"`
}

// resetPassword answers 204: no session starts, the user logs in with the
// new password.
func resetPassword(logger *slog.Logger, reg Registration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req resetRequest
		if !decodeJSONMax(w, r, &req, `body must be {"token": …, "new_password": …}`, maxPasswordBody) {
			return
		}
		if err := reg.ResetPassword(r.Context(), req.Token, req.NewPassword); err != nil {
			writeAuthError(w, r, logger, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
