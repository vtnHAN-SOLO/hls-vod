// Package turnstile checks Cloudflare Turnstile tokens server-side
// (siteverify). The browser widget produces a token; only this check makes
// it mean anything.
package turnstile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// SiteverifyURL is Cloudflare's verification endpoint.
const SiteverifyURL = "https://challenges.cloudflare.com/turnstile/v0/siteverify"

// Timeout bounds one siteverify call.
const Timeout = 5 * time.Second

// MaxTokenLen is the longest token Cloudflare issues; anything longer is
// refused without a call.
const MaxTokenLen = 2048

var (
	// ErrRejected means the token is missing, malformed, expired, already
	// used or failed the challenge: the client should solve a new one.
	ErrRejected = errors.New("captcha rejected")
	// ErrUnavailable means siteverify could not give an answer (unreachable,
	// timeout, non-200, internal error), or refused our secret key. Callers
	// fail closed on it.
	ErrUnavailable = errors.New("captcha verification unavailable")
)

// secretErrorCodes are the siteverify error codes that blame our
// configuration (TURNSTILE_SECRET_KEY), not the visitor.
var secretErrorCodes = []string{"missing-input-secret", "invalid-input-secret"}

// Verifier calls siteverify with the secret key.
type Verifier struct {
	secret   string
	endpoint string
	client   *http.Client
	log      *slog.Logger
}

// New returns a Verifier for secret. endpoint "" means SiteverifyURL; tests
// pass an httptest server. log receives an ERROR each time siteverify
// refuses the secret key.
func New(secret, endpoint string, log *slog.Logger) *Verifier {
	if endpoint == "" {
		endpoint = SiteverifyURL
	}
	return &Verifier{secret: secret, endpoint: endpoint, client: &http.Client{Timeout: Timeout}, log: log}
}

type siteverifyResponse struct {
	Success    bool     `json:"success"`
	ErrorCodes []string `json:"error-codes"`
}

// Verify checks token. remoteIP is the visitor's address (clientip), sent
// as siteverify's remoteip so Cloudflare can match it to the solver; ""
// leaves it out. nil means a human solved the challenge just now; otherwise
// the error wraps ErrRejected or ErrUnavailable.
func (v *Verifier) Verify(ctx context.Context, token, remoteIP string) error {
	if token == "" || len(token) > MaxTokenLen {
		return ErrRejected
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	form := url.Values{"secret": {v.secret}, "response": {token}}
	if remoteIP != "" {
		form.Set("remoteip", remoteIP)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("%w: building request: %w", ErrUnavailable, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := v.client.Do(req)
	if err != nil {
		// The error names the endpoint only: the secret is in the body.
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: siteverify answered %d", ErrUnavailable, resp.StatusCode)
	}
	var r siteverifyResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&r); err != nil {
		return fmt.Errorf("%w: decoding siteverify answer: %w", ErrUnavailable, err)
	}
	if r.Success {
		return nil
	}
	for _, code := range secretErrorCodes {
		if slices.Contains(r.ErrorCodes, code) {
			// Our misconfiguration, not the visitor's: fail closed and say so
			// loudly. Never log the key itself.
			v.log.ErrorContext(ctx, "Turnstile secret key is invalid: siteverify refused TURNSTILE_SECRET_KEY",
				"error_code", code)
			return fmt.Errorf("%w: siteverify refused the secret key (%s)", ErrUnavailable, code)
		}
	}
	if slices.Contains(r.ErrorCodes, "internal-error") {
		return fmt.Errorf("%w: siteverify internal error", ErrUnavailable)
	}
	return fmt.Errorf("%w: %s", ErrRejected, strings.Join(r.ErrorCodes, ","))
}
