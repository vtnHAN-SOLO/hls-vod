package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/vtnHAN-SOLO/hls-vod/internal/service"
	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
)

// Google is sign in with Google. *service.Google satisfies it.
type Google interface {
	StartLogin(ctx context.Context) (authURL, state string, err error)
	StartLink(ctx context.Context, p service.Principal, currentPassword string) (authURL, state string, err error)
	Callback(ctx context.Context, in service.CallbackInput) (service.CallbackResult, error)
}

const (
	// GoogleStateCookie carries the state between start and callback.
	// SameSite=Lax: Google's redirect back is a cross-site top-level
	// navigation, which a Strict cookie would not be sent on.
	GoogleStateCookie = "google_state"
	googlePath        = "/api/v1/auth/google"
)

// Where the callback sends the browser: SPA pages, relative so they stay on
// the origin the browser used.
const (
	googleDonePage    = "/auth/google/done"
	googleLoginPage   = "/login"
	googleAccountPage = "/account"
)

func setGoogleState(w http.ResponseWriter, state string, opts Options) {
	http.SetCookie(w, &http.Cookie{ // #nosec G124 -- Secure except for plain-http dev on loopback; HttpOnly always; Lax is required (see GoogleStateCookie)
		Name: GoogleStateCookie, Value: state, Path: googlePath,
		MaxAge: int(service.OAuthStateTTL.Seconds()), HttpOnly: true, Secure: opts.SecureCookies, SameSite: http.SameSiteLaxMode,
	})
}

func clearGoogleState(w http.ResponseWriter, opts Options) {
	http.SetCookie(w, &http.Cookie{ // #nosec G124 -- as in setGoogleState
		Name: GoogleStateCookie, Value: "", Path: googlePath, MaxAge: -1,
		HttpOnly: true, Secure: opts.SecureCookies, SameSite: http.SameSiteLaxMode,
	})
}

// redirect answers 302 to target with no body.
func redirect(w http.ResponseWriter, target string) {
	w.Header().Set("Location", target)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusFound)
}

func googleErrorPage(page, code string) string {
	return page + "?" + url.Values{"google_error": {code}}.Encode()
}

// googleStart is the "Continue with Google" link: a new state in the cookie,
// then off to Google.
func googleStart(logger *slog.Logger, opts Options) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if opts.Google == nil {
			redirect(w, googleErrorPage(googleLoginPage, "disabled"))
			return
		}
		authURL, state, err := opts.Google.StartLogin(r.Context())
		if err != nil {
			logger.ErrorContext(r.Context(), "Google sign-in could not start", "client_ip", clientIP(r), "err", err)
			redirect(w, googleErrorPage(googleLoginPage, service.GoogleErrUnavailable))
			return
		}
		setGoogleState(w, state, opts)
		redirect(w, authURL)
	}
}

type googleLinkRequest struct {
	CurrentPassword string `json:"current_password"`
}

type googleLinkJSON struct {
	URL string `json:"url"`
}

// googleLink starts linking: the SPA navigates to the returned url.
func googleLink(logger *slog.Logger, opts Options) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if opts.Google == nil {
			writeError(w, http.StatusNotFound, codeNotFound, "sign in with Google is not enabled")
			return
		}
		var req googleLinkRequest
		if !decodeJSONMax(w, r, &req, `body must be {"current_password": …}`, maxPasswordBody) {
			return
		}
		authURL, state, err := opts.Google.StartLink(r.Context(), principal(r), req.CurrentPassword)
		if err != nil {
			writeAuthError(w, r, logger, err)
			return
		}
		setGoogleState(w, state, opts)
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, googleLinkJSON{URL: authURL})
	}
}

// googleCallback is where Google sends the browser back. A login sets the
// refresh cookie (never a token in a URL) and goes to the done page, where
// the SPA refreshes; a link goes back to the account page. A failure goes
// to the login or account page with a short code, never more.
func googleCallback(logger *slog.Logger, opts Options) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		clearGoogleState(w, opts)
		if opts.Google == nil {
			redirect(w, googleErrorPage(googleLoginPage, "disabled"))
			return
		}
		in := service.CallbackInput{
			State: r.URL.Query().Get("state"), Code: r.URL.Query().Get("code"),
			ProviderError: r.URL.Query().Get("error"), UserAgent: r.UserAgent(),
		}
		if c, err := r.Cookie(GoogleStateCookie); err == nil {
			in.CookieState = c.Value
		}
		res, err := opts.Google.Callback(r.Context(), in)
		if err != nil {
			code, page := service.GoogleErrServer, googleLoginPage
			var ge *service.GoogleError
			if errors.As(err, &ge) {
				code = ge.Code
				if ge.Intent == store.IntentLink {
					page = googleAccountPage
				}
			}
			// The error never carries the code or a token.
			level := slog.LevelInfo
			if code == service.GoogleErrServer || code == service.GoogleErrUnavailable {
				level = slog.LevelError
			}
			logger.Log(r.Context(), level, "Google callback failed", "code", code, "err", err)
			redirect(w, googleErrorPage(page, code))
			return
		}
		if res.Intent == store.IntentLink {
			redirect(w, googleAccountPage+"?google=linked")
			return
		}
		setRefreshCookie(w, res.Session, opts)
		redirect(w, googleDonePage)
	}
}

func googleUnlink(logger *slog.Logger, a Auth) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := a.UnlinkGoogle(r.Context(), principal(r)); err != nil {
			writeAuthError(w, r, logger, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// passwordSetLink is "Set password" for a Google-only account: 202, the
// link goes to the account's own email.
func passwordSetLink(logger *slog.Logger, reg Registration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := reg.PasswordSetLink(r.Context(), principal(r)); err != nil {
			writeAuthError(w, r, logger, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusAccepted, acceptedJSON{
			Message: "Check your inbox for the link. Setting the password signs you out everywhere; then sign in again.",
		})
	}
}
