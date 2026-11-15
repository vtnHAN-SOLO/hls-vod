package handler

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/vtnHAN-SOLO/hls-vod/internal/auth"
	"github.com/vtnHAN-SOLO/hls-vod/internal/mail"
)

// verified registers and verifies an account and returns the session the
// verification started.
func (e *regEnv) verified(t *testing.T, email, username, password string) device {
	t.Helper()
	if r := e.register(t, email, username, password, "good"); r.status != http.StatusAccepted {
		t.Fatalf("register = %d %s", r.status, r.body)
	}
	v := e.post(t, "/api/v1/auth/verify-email", map[string]string{"token": tokenFrom(t, e.mailer.take())})
	if v.status != http.StatusOK {
		t.Fatalf("verify = %d %s", v.status, v.body)
	}
	return deviceOf(t, v)
}

// resetToken is the token of the reset link in the one reset mail.
func resetToken(t *testing.T, mails []mail.Message) string {
	t.Helper()
	const prefix = testBaseURL + "/reset-password#token="
	for _, m := range mails {
		if m.Kind != mail.KindReset {
			continue
		}
		i := strings.Index(m.Text, prefix)
		if i < 0 {
			t.Fatalf("no reset link in %q", m.Text)
		}
		return strings.Fields(m.Text[i+len(prefix):])[0]
	}
	t.Fatalf("no reset mail in %+v", mails)
	return ""
}

func (e *regEnv) forgot(t *testing.T, email, captcha string) response {
	t.Helper()
	return e.post(t, "/api/v1/auth/forgot-password", map[string]string{"email": email, "turnstile_token": captcha})
}

func (e *regEnv) reset(t *testing.T, token, password string) response {
	t.Helper()
	return e.post(t, "/api/v1/auth/reset-password", map[string]string{"token": token, "new_password": password})
}

func TestForgotAndResetPassword(t *testing.T) {
	e := newRegEnv(t, regSetup{})
	oldPassword, newPassword := randomString(18), randomString(18)
	laptop := e.verified(t, "dana@example.com", "dana", oldPassword)
	if r := e.register(t, "una@example.com", "una", randomString(18), "good"); r.status != http.StatusAccepted {
		t.Fatal(r.status)
	}
	_ = e.mailer.take()

	// The same answer for a verified, an unverified, an unknown address, and
	// one a useradmin account (no email) might be thought to have.
	want := e.forgot(t, "dana@example.com", "good")
	if want.status != http.StatusAccepted || want.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("forgot = %d %s", want.status, want.body)
	}
	for _, addr := range []string{"una@example.com", "nobody@example.com", "viewer@example.com"} {
		if r := e.forgot(t, addr, "good"); r.status != want.status || string(r.body) != string(want.body) {
			t.Fatalf("%s: %d %s; want %d %s", addr, r.status, r.body, want.status, want.body)
		}
	}
	mails := e.mailer.take()
	if len(mails) != 1 || mails[0].To != "dana@example.com" || !strings.Contains(mails[0].Text, "dana") {
		t.Fatalf("mails = %+v; want one reset mail to dana with the username", mails)
	}
	first := resetToken(t, mails)

	// Only the newest link works.
	_ = e.forgot(t, "DANA@example.com", "good")
	newest := resetToken(t, e.mailer.take())
	if r := e.reset(t, first, newPassword); r.status != http.StatusBadRequest || errorCode(t, r) != "invalid_token" {
		t.Fatalf("older link = %d %s", r.status, r.body)
	}
	if r := e.reset(t, newest, "short"); r.status != http.StatusBadRequest || errorCode(t, r) != "invalid_request" {
		t.Fatalf("weak password = %d %s", r.status, r.body)
	}
	if r := e.reset(t, newest, newPassword); r.status != http.StatusNoContent {
		t.Fatalf("reset = %d %s", r.status, r.body)
	}
	if laptop.refreshes(t, e.srv) {
		t.Fatal("a session survived the reset")
	}
	if r := e.login(t, "dana", oldPassword, ""); r.status != http.StatusUnauthorized {
		t.Fatalf("old password = %d", r.status)
	}
	login := e.login(t, "dana", newPassword, "")
	if login.status != http.StatusOK {
		t.Fatalf("new password = %d %s", login.status, login.body)
	}
	// /account shows the address, read-only, and that it is confirmed.
	var me accountJSON
	decode(t, call(t, e.srv, http.MethodGet, "/api/v1/me", deviceOf(t, login).access, "").body, &me)
	if me.Email == nil || *me.Email != "dana@example.com" || me.EmailVerifiedAt == nil {
		t.Fatalf("me = %+v", me)
	}
	if r := e.reset(t, newest, randomString(18)); r.status != http.StatusBadRequest || errorCode(t, r) != "invalid_token" {
		t.Fatalf("reused link = %d %s", r.status, r.body)
	}

	// Expired.
	_ = e.forgot(t, "dana@example.com", "good")
	expired := resetToken(t, e.mailer.take())
	e.as.mu.Lock()
	for _, tok := range e.rs.tokens {
		tok.expires = time.Now().Add(-time.Second)
	}
	e.as.mu.Unlock()
	if r := e.reset(t, expired, randomString(18)); r.status != http.StatusBadRequest || errorCode(t, r) != "invalid_token" {
		t.Fatalf("expired link = %d %s", r.status, r.body)
	}
	unknown, _, _ := auth.NewOpaqueToken()
	if r := e.reset(t, unknown, randomString(18)); r.status != http.StatusBadRequest || errorCode(t, r) != "invalid_token" {
		t.Fatalf("unknown link = %d %s", r.status, r.body)
	}
}

func TestForgotPasswordErrors(t *testing.T) {
	e := newRegEnv(t, regSetup{})
	if r := e.forgot(t, "dana@example.com", "forged"); r.status != http.StatusBadRequest || errorCode(t, r) != "captcha_failed" {
		t.Fatalf("rejected challenge = %d %s", r.status, r.body)
	}
	if r := e.forgot(t, "not an address", "good"); r.status != http.StatusBadRequest || errorCode(t, r) != "invalid_request" {
		t.Fatalf("malformed = %d %s", r.status, r.body)
	}
	e.captcha.setDown(true)
	if r := e.forgot(t, "dana@example.com", "good"); r.status != http.StatusServiceUnavailable || errorCode(t, r) != "captcha_unavailable" {
		t.Fatalf("siteverify down = %d %s", r.status, r.body)
	}
	e.captcha.setDown(false)
	for _, path := range []string{"/api/v1/auth/forgot-password", "/api/v1/auth/reset-password"} {
		if r := postJSON(t, e.srv, path, "text/plain", "{}", false); r.status != http.StatusUnsupportedMediaType {
			t.Errorf("%s text/plain = %d", path, r.status)
		}
		if r := doFull(t, e.srv, http.MethodGet, path); r.status != http.StatusMethodNotAllowed {
			t.Errorf("GET %s = %d", path, r.status)
		}
	}
	if r := postJSON(t, e.srv, "/api/v1/auth/reset-password", "application/json", `{"token":"x","password":"y"}`, false); r.status != http.StatusBadRequest {
		t.Fatalf("unknown field = %d", r.status)
	}
	if len(e.mailer.take()) != 0 {
		t.Fatal("a refused request mailed")
	}
	e.rs.err = errors.New("db down")
	if r := e.forgot(t, "dana@example.com", "good"); r.status != http.StatusInternalServerError {
		t.Fatalf("forgot, store down = %d", r.status)
	}
	tok, _, _ := auth.NewOpaqueToken()
	if r := e.reset(t, tok, randomString(18)); r.status != http.StatusInternalServerError {
		t.Fatalf("reset, store down = %d", r.status)
	}
	e.rs.err = nil
	for range 10 {
		_ = e.forgot(t, "dana@example.com", "good")
	}
	if r := e.forgot(t, "dana@example.com", "good"); r.status != http.StatusTooManyRequests || r.header.Get("Retry-After") == "" {
		t.Fatalf("over the limit = %d %v", r.status, r.header)
	}
}
