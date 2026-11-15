package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/vtnHAN-SOLO/hls-vod/internal/auth"
	"github.com/vtnHAN-SOLO/hls-vod/internal/mail"
	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
)

func linkToken(t *testing.T, m mail.Message, page string) string {
	t.Helper()
	prefix := baseURL + page + "#token="
	i := strings.Index(m.Text, prefix)
	if i < 0 {
		t.Fatalf("no %s link in %q", page, m.Text)
	}
	return strings.Fields(m.Text[i+len(prefix):])[0]
}

// Forgot password answers nil for every address; only a verified account
// gets a mail, and it carries the username and a reset link.
func TestForgotPassword(t *testing.T) {
	for _, tt := range []struct {
		outcome store.ResetOutcome
		mails   int
	}{
		{store.ResetIssued, 1},
		{store.ResetUnverified, 0},
		{store.ResetNoAccount, 0},
	} {
		t.Run(tt.outcome.String(), func(t *testing.T) {
			f := newReg(t, true)
			f.st.resetOutcome = tt.outcome
			if err := f.reg.ForgotPassword(t.Context(), " Dana@Example.com ", "good", "192.0.2.7"); err != nil {
				t.Fatalf("ForgotPassword = %v", err)
			}
			if len(f.mailer.sent) != tt.mails {
				t.Fatalf("mails = %+v", f.mailer.sent)
			}
			if tt.mails == 1 {
				m := f.mailer.sent[0]
				if m.Kind != mail.KindReset || m.To != "dana@example.com" || !strings.Contains(m.Text, "dana") {
					t.Fatalf("mail = %+v", m)
				}
				if tok := linkToken(t, m, "/reset-password"); tok == "" {
					t.Fatal("empty token")
				}
			}
		})
	}

	t.Run("challenge, fail closed, validation, per-IP limit", func(t *testing.T) {
		f := newReg(t, true)
		if err := f.reg.ForgotPassword(t.Context(), "dana@example.com", "forged", "192.0.2.7"); !errors.Is(err, ErrCaptchaFailed) {
			t.Fatalf("rejected challenge = %v", err)
		}
		f.captcha.down = true
		if err := f.reg.ForgotPassword(t.Context(), "dana@example.com", "good", "192.0.2.7"); !errors.Is(err, ErrCaptchaUnavailable) {
			t.Fatalf("siteverify down = %v", err)
		}
		f.captcha.down = false
		var verr *ValidationError
		if err := f.reg.ForgotPassword(t.Context(), "nope", "good", "192.0.2.7"); !errors.As(err, &verr) {
			t.Fatalf("malformed = %v", err)
		}
		for range RegisterPerIPPerHour - 2 {
			_ = f.reg.ForgotPassword(t.Context(), "dana@example.com", "good", "192.0.2.7")
		}
		var throttled *ThrottledError
		if err := f.reg.ForgotPassword(t.Context(), "dana@example.com", "good", "192.0.2.7"); !errors.As(err, &throttled) {
			t.Fatalf("over the limit = %v", err)
		}
		// Its own key: resend and register from the same address still work.
		if err := f.reg.ResendVerification(t.Context(), "dana@example.com", "good", "192.0.2.7"); err != nil {
			t.Fatalf("resend after the forgot limit = %v", err)
		}
	})
}

func TestResetPassword(t *testing.T) {
	f := newReg(t, true)
	token, hash, err := auth.NewOpaqueToken()
	if err != nil {
		t.Fatal(err)
	}
	f.st.resetOK = hash

	var verr *ValidationError
	if err := f.reg.ResetPassword(t.Context(), token, "short"); !errors.As(err, &verr) || f.pw.hash != 0 {
		t.Fatalf("weak password = %v, hashes = %d; want a ValidationError and no hash", err, f.pw.hash)
	}
	other, _, _ := auth.NewOpaqueToken()
	for _, bad := range []string{"", "not-a-token", other} {
		if err := f.reg.ResetPassword(t.Context(), bad, strongPassword()); !errors.Is(err, ErrInvalidEmailToken) {
			t.Errorf("ResetPassword(%.8q) = %v", bad, err)
		}
	}
	if f.pw.hash != 0 {
		t.Fatalf("an unusable token cost %d hashes", f.pw.hash)
	}
	if err := f.reg.ResetPassword(t.Context(), token, strongPassword()); err != nil || len(f.st.resetHashes) != 1 {
		t.Fatalf("ResetPassword = %v, stored %v", err, f.st.resetHashes)
	}
}
