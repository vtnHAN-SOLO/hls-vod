package handler

import (
	"encoding/json"
	"log/slog"
	"mime"
	"net/http"
	"time"
)

// maxPasswordBody bounds a password change: two passwords of up to 1024
// characters, JSON-escaped.
const maxPasswordBody = 16 << 10

type accountJSON struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
	// Email is null for accounts made with useradmin.
	Email           *string    `json:"email"`
	EmailVerifiedAt *time.Time `json:"email_verified_at"`
	CreatedAt       time.Time  `json:"created_at"`
	// HasPassword is false for a Google-only account.
	HasPassword bool `json:"has_password"`
	// Google is null when no Google account is linked.
	Google *googleJSON `json:"google"`
}

type googleJSON struct {
	EmailAtLink *string   `json:"email_at_link"`
	LinkedAt    time.Time `json:"linked_at"`
}

type passwordChangeRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// sessionJSON is one login of the caller. Never a token or a hash.
type sessionJSON struct {
	ID         string    `json:"id"`
	UserAgent  string    `json:"user_agent"`
	CreatedAt  time.Time `json:"created_at"`
	LastUsedAt time.Time `json:"last_used_at"`
	Current    bool      `json:"current"`
}

type sessionListJSON struct {
	Items []sessionJSON `json:"items"`
}

func getMe(logger *slog.Logger, a Auth) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		acc, err := a.Account(r.Context(), principal(r))
		if err != nil {
			writeServiceError(w, r, logger, err)
			return
		}
		u := acc.User
		out := accountJSON{ID: u.ID, Username: u.Username, Role: string(u.Role),
			Email: u.Email, EmailVerifiedAt: u.EmailVerifiedAt, CreatedAt: u.CreatedAt, HasPassword: u.HasPassword()}
		if acc.Google != nil {
			out.Google = &googleJSON{EmailAtLink: acc.Google.EmailAtLink, LinkedAt: acc.Google.CreatedAt}
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, out)
	}
}

// changePassword answers like a login: a new access token in the body and a
// new refresh cookie, so the tab that changed the password keeps working.
// Bearer authentication is the CSRF defence here: a cross-site request
// cannot set the Authorization header.
func changePassword(logger *slog.Logger, a Auth, opts Options) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
			writeError(w, http.StatusUnsupportedMediaType, codeUnsupportedMediaType, "Content-Type must be application/json")
			return
		}
		var req passwordChangeRequest
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPasswordBody))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidRequest,
				"body must be {\"current_password\": …, \"new_password\": …}")
			return
		}
		s, err := a.ChangePassword(r.Context(), principal(r), req.CurrentPassword, req.NewPassword, r.UserAgent())
		if err != nil {
			writeAuthError(w, r, logger, err)
			return
		}
		writeSession(w, s, opts)
	}
}

func listSessions(logger *slog.Logger, a Auth) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ss, err := a.Sessions(r.Context(), principal(r))
		if err != nil {
			writeServiceError(w, r, logger, err)
			return
		}
		out := sessionListJSON{Items: make([]sessionJSON, 0, len(ss))}
		for _, s := range ss {
			out.Items = append(out.Items, sessionJSON{
				ID: s.ID, UserAgent: s.Device, CreatedAt: s.CreatedAt, LastUsedAt: s.LastUsedAt, Current: s.Current,
			})
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, out)
	}
}

func revokeSession(logger *slog.Logger, a Auth) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := a.RevokeSession(r.Context(), principal(r), r.PathValue("id")); err != nil {
			writeServiceError(w, r, logger, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func revokeOtherSessions(logger *slog.Logger, a Auth) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := a.RevokeOtherSessions(r.Context(), principal(r)); err != nil {
			writeServiceError(w, r, logger, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
