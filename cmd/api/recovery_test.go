package main

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// none checks that no mail reaches Resend for a while.
func (f *fakeResend) none(t *testing.T) {
	t.Helper()
	select {
	case <-f.got:
		t.Fatal("a mail was sent")
	case <-time.After(300 * time.Millisecond):
	}
}

func to(m map[string]any) string {
	if l, _ := m["to"].([]any); len(l) == 1 {
		s, _ := l[0].(string)
		return s
	}
	return ""
}

// The PR's verify steps, against real Postgres: forgot → reset (old
// password dead, new one works, the other browser logged out); an unknown
// address answers the same and sends nothing.
func TestRecoveryEndToEnd(t *testing.T) {
	e := startE2E(t)
	oldPassword, newPassword := randomSecret(18), randomSecret(18)

	if r := post(t, e.base+"/api/v1/auth/register",
		`{"email":"rita@example.com","username":"rita","password":"`+oldPassword+`","turnstile_token":"good"}`, nil); r.status != http.StatusAccepted {
		t.Fatalf("register = %d %s", r.status, r.body)
	}
	v := post(t, e.base+"/api/v1/auth/verify-email", `{"token":"`+linkToken(t, e.resend.next(t), "/verify-email")+`"}`, nil)
	if v.status != http.StatusOK {
		t.Fatalf("verify = %d %s", v.status, v.body)
	}
	otherBrowser := refreshCookie(t, v)

	// 1. Forgot → reset.
	known := post(t, e.base+"/api/v1/auth/forgot-password", `{"email":"rita@example.com","turnstile_token":"good"}`, nil)
	m := e.resend.next(t)
	if known.status != http.StatusAccepted || to(m) != "rita@example.com" || !strings.Contains(m["text"].(string), "rita") {
		t.Fatalf("forgot = %d, mail %v", known.status, m)
	}
	if r := post(t, e.base+"/api/v1/auth/reset-password",
		`{"token":"`+linkToken(t, m, "/reset-password")+`","new_password":"`+newPassword+`"}`, nil); r.status != http.StatusNoContent {
		t.Fatalf("reset = %d %s", r.status, r.body)
	}
	if r := post(t, e.base+"/api/v1/auth/login", `{"username":"rita","password":"`+oldPassword+`"}`, nil); r.status != http.StatusUnauthorized {
		t.Fatalf("old password = %d", r.status)
	}
	login := post(t, e.base+"/api/v1/auth/login", `{"username":"rita","password":"`+newPassword+`"}`, nil)
	if login.status != http.StatusOK {
		t.Fatalf("new password = %d %s", login.status, login.body)
	}
	if r := post(t, e.base+"/api/v1/auth/refresh", "", []*http.Cookie{otherBrowser}); r.status != http.StatusUnauthorized {
		t.Fatalf("the other browser after the reset = %d", r.status)
	}

	// 2. Unknown address: the same answer, nothing sent.
	unknown := post(t, e.base+"/api/v1/auth/forgot-password", `{"email":"nobody@example.com","turnstile_token":"good"}`, nil)
	if unknown.status != known.status || unknown.body != known.body {
		t.Fatalf("unknown = %d %s; known = %d %s", unknown.status, unknown.body, known.status, known.body)
	}
	e.resend.none(t)

	// /account shows the address, fixed since registration.
	if status, me := getJSON(t, e.base+"/api/v1/me", login.json["access_token"].(string)); status != http.StatusOK ||
		me["email"] != "rita@example.com" || me["email_verified_at"] == nil {
		t.Fatalf("me = %d %v", status, me)
	}
}
