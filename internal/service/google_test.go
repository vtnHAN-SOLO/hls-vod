package service

import (
	"crypto/rand"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/vtnHAN-SOLO/hls-vod/internal/auth"
	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
)

// Generated usernames follow the username rules, use the local part only,
// and differ on every call.
func TestGoogleUsername(t *testing.T) {
	for _, tt := range []struct{ email, prefix string }{
		{"Gina.Rossi@gmail.example", "gina.rossi-"},
		{"first+tag@example.com", "firsttag-"},
		{"._-x@example.com", "x-"},
		{"!!!@example.com", "user-"},
		{"Ünïcødé@example.com", "ncd-"},
		{strings.Repeat("a", 64) + "@example.com", strings.Repeat("a", 50) + "-"},
	} {
		got := GoogleUsername(tt.email)
		if !strings.HasPrefix(got, tt.prefix) || len(got) != len(tt.prefix)+4 || auth.CheckUsername(got) != nil ||
			strings.Contains(got, "example") {
			t.Errorf("GoogleUsername(%q) = %q, want %s + 4 characters, a valid username", tt.email, got, tt.prefix)
		}
	}
	if a, b := GoogleUsername("same@example.com"), GoogleUsername("same@example.com"); a == b {
		t.Error("two proposals are identical: the suffix is not random")
	}
}

// A Google-only account (no password) answers a password login exactly as
// a wrong password does, after the same dummy hash.
func TestPasswordLoginWithoutPassword(t *testing.T) {
	key := make([]byte, 48)
	_, _ = rand.Read(key)
	signer, err := auth.NewSigner(key)
	if err != nil {
		t.Fatal(err)
	}
	pw := &countingPasswords{match: true}
	u := model.User{ID: testUserID, Username: "gina", Role: model.RoleUser}
	a := NewAuth(oneUserStore{user: u, got: &storeCalls{}}, pw, signer, auth.NewThrottle(), auth.NewThrottle(), slog.New(slog.DiscardHandler))
	_, err = a.Login(t.Context(), LoginInput{Username: "gina", Password: strongPassword(), ClientIP: "192.0.2.1"})
	if !errors.Is(err, ErrInvalidCredentials) || pw.dummy != 1 || pw.verify != 0 {
		t.Fatalf("Login = %v, dummy=%d verify=%d; want invalid credentials after one dummy hash", err, pw.dummy, pw.verify)
	}
	// Nor can a current password be "right" for it.
	me := Principal{UserID: testUserID, SessionID: testFamily}
	if _, err := a.ChangePassword(t.Context(), me, strongPassword(), strongPassword(), ""); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("ChangePassword = %v", err)
	}
}
