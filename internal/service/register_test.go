package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/vtnHAN-SOLO/hls-vod/internal/auth"
	"github.com/vtnHAN-SOLO/hls-vod/internal/mail"
	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
	"github.com/vtnHAN-SOLO/hls-vod/internal/turnstile"
)

const baseURL = "https://media.example"

// fakeCaptcha passes "good", is unreachable when down, rejects the rest.
type fakeCaptcha struct {
	down  bool
	calls int
}

func (c *fakeCaptcha) Verify(_ context.Context, token, _ string) error {
	c.calls++
	switch {
	case c.down:
		return fmt.Errorf("%w: connection refused", turnstile.ErrUnavailable)
	case token == "good":
		return nil
	default:
		return fmt.Errorf("%w: invalid-input-response", turnstile.ErrRejected)
	}
}

type fakeMailer struct{ sent []mail.Message }

func (m *fakeMailer) Enqueue(msg mail.Message) { m.sent = append(m.sent, msg) }

// fakeRegStore: the addresses in existing have an account, the usernames
// in taken are used; everything else registers.
type fakeRegStore struct {
	existing map[string]model.User
	taken    map[string]bool
	resend   store.ResendOutcome
	verifyOK []byte // the one token hash VerifyEmail accepts
	created  []store.NewAccount

	// recovery.go
	resetOutcome store.ResetOutcome
	resetOK      []byte // the one reset token hash that is usable
	resetHashes  []string
}

func (s *fakeRegStore) IssueResetToken(_ context.Context, email string, _ []byte, ttl time.Duration) (model.User, store.ResetOutcome, error) {
	if ttl != auth.ResetTTL {
		return model.User{}, 0, errors.New("wrong ttl")
	}
	return model.User{ID: testUserID, Username: "dana", Email: &email}, s.resetOutcome, nil
}

func (s *fakeRegStore) EmailTokenUsable(_ context.Context, hash []byte, purpose string) (bool, error) {
	return purpose == store.PurposeReset && string(hash) == string(s.resetOK), nil
}

func (s *fakeRegStore) ResetPassword(_ context.Context, hash []byte, passwordHash string) (model.User, error) {
	if string(hash) != string(s.resetOK) {
		return model.User{}, store.ErrEmailTokenInvalid
	}
	s.resetHashes = append(s.resetHashes, passwordHash)
	return model.User{ID: testUserID}, nil
}

func (s *fakeRegStore) Register(_ context.Context, a store.NewAccount) (store.Registration, error) {
	if s.taken[a.Username] {
		return store.Registration{}, store.ErrUserExists
	}
	if u, ok := s.existing[a.Email]; ok {
		return store.Registration{Existing: &u}, nil
	}
	s.created = append(s.created, a)
	return store.Registration{User: model.User{ID: testUserID, Username: a.Username, Email: &a.Email}}, nil
}

func (s *fakeRegStore) ResendVerification(_ context.Context, email string, _ []byte, ttl time.Duration) (model.User, store.ResendOutcome, error) {
	if ttl != auth.VerifyTTL {
		return model.User{}, 0, errors.New("wrong ttl")
	}
	return model.User{ID: testUserID, Username: "dana", Email: &email}, s.resend, nil
}

func (s *fakeRegStore) VerifyEmail(_ context.Context, hash []byte, _ store.NewRefresh) (model.User, string, error) {
	if string(hash) != string(s.verifyOK) {
		return model.User{}, "", store.ErrEmailTokenInvalid
	}
	now := time.Now()
	return model.User{ID: testUserID, Username: "dana", Role: model.RoleUser, EmailVerifiedAt: &now}, newFamily, nil
}

func (s *fakeRegStore) IssueResetTokenForUser(_ context.Context, _ string, hash []byte, ttl time.Duration) error {
	if ttl != auth.ResetTTL {
		return errors.New("wrong ttl")
	}
	s.resetOK = hash
	return nil
}

func strongPassword() string {
	b := make([]byte, 15)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

type regFixture struct {
	reg     *Registration
	st      *fakeRegStore
	pw      *countingPasswords
	captcha *fakeCaptcha
	mailer  *fakeMailer
	auth    *Auth
}

func newReg(t *testing.T, open bool) *regFixture {
	t.Helper()
	f := &regFixture{
		st: &fakeRegStore{
			existing: map[string]model.User{"taken@example.com": {ID: "e0000000-0000-4000-8000-000000000001", Username: "olduser"}},
			taken:    map[string]bool{"admin": true},
		},
		pw: &countingPasswords{}, captcha: &fakeCaptcha{}, mailer: &fakeMailer{},
	}
	f.auth = newTestAuth(t, f.pw)
	f.reg = NewRegistration(f.st, f.pw, f.captcha, f.mailer, f.auth,
		RegistrationConfig{Open: open, BaseURL: baseURL, TurnstileSiteKey: "site-key"}, slog.New(slog.DiscardHandler))
	return f
}

func signup(email, username string) RegisterInput {
	return RegisterInput{Email: email, Username: username, Password: strongPassword(), CaptchaToken: "good", ClientIP: "192.0.2.7"}
}

// The enumeration rule: a new address and a registered one answer the same,
// both hash the password and pass the challenge; only the mail differs.
func TestRegisterDoesNotRevealAddresses(t *testing.T) {
	type outcome struct {
		err             error
		hashes, checks  int
		kind, to, inURL string
	}
	run := func(email string) outcome {
		f := newReg(t, true)
		err := f.reg.Register(t.Context(), signup(email, "newbie"))
		o := outcome{err: err, hashes: f.pw.hash, checks: f.captcha.calls}
		if len(f.mailer.sent) == 1 {
			m := f.mailer.sent[0]
			o.kind, o.to = m.Kind, m.To
			if i := strings.Index(m.Text, baseURL); i >= 0 {
				o.inURL = strings.Fields(m.Text[i:])[0]
			}
		}
		return o
	}
	fresh, taken := run("New.Person@Example.com"), run("taken@example.com")
	if fresh.err != nil || taken.err != nil {
		t.Fatalf("errors: new %v, taken %v; want nil for both", fresh.err, taken.err)
	}
	if fresh.hashes != 1 || taken.hashes != 1 || fresh.checks != 1 || taken.checks != 1 {
		t.Fatalf("work: new %+v, taken %+v; want one hash and one challenge each", fresh, taken)
	}
	if fresh.kind != mail.KindVerify || fresh.to != "new.person@example.com" || !strings.HasPrefix(fresh.inURL, baseURL+"/verify-email#token=") {
		t.Fatalf("new address mail = %+v", fresh)
	}
	tok := strings.TrimPrefix(fresh.inURL, baseURL+"/verify-email#token=")
	if _, ok := auth.HashOpaqueToken(tok); !ok {
		t.Fatalf("link token %q is not one of ours", tok)
	}
	if taken.kind != mail.KindAccountExists || taken.to != "taken@example.com" || taken.inURL != baseURL+"/login" {
		t.Fatalf("taken address mail = %+v", taken)
	}
}

func TestRegisterRules(t *testing.T) {
	t.Run("closed: 403 before any work", func(t *testing.T) {
		f := newReg(t, false)
		if err := f.reg.Register(t.Context(), signup("a@example.com", "anna")); !errors.Is(err, ErrRegistrationClosed) {
			t.Fatalf("err = %v", err)
		}
		if f.pw.hash+f.captcha.calls+len(f.mailer.sent) != 0 || f.reg.Config().RegistrationOpen {
			t.Fatal("work was done, or the config says open")
		}
	})

	t.Run("validation", func(t *testing.T) {
		f := newReg(t, true)
		for _, tt := range []struct {
			name  string
			in    RegisterInput
			field string
		}{
			{"display name", signup("Bob <bob@example.com>", "bob"), "email"},
			{"angle brackets", signup("<bob@example.com>", "bob"), "email"},
			{"no at", signup("bob.example.com", "bob"), "email"},
			{"empty", signup("", "bob"), "email"},
			{"too long", signup(strings.Repeat("a", 243)+"@example.com", "bob"), "email"},
			{"two addresses", signup("a@example.com, b@example.com", "bob"), "email"},
			{"bad username", signup("bob@example.com", "-bob"), "username"},
			{"short password", RegisterInput{Email: "bob@example.com", Username: "bob", Password: "short", CaptchaToken: "good"}, "password"},
		} {
			var verr *ValidationError
			if err := f.reg.Register(t.Context(), tt.in); !errors.As(err, &verr) || verr.Field != tt.field {
				t.Errorf("%s: %v; want a %s ValidationError", tt.name, err, tt.field)
			}
		}
		if f.pw.hash+f.captcha.calls != 0 {
			t.Fatal("invalid input reached the challenge or the hash")
		}
	})

	t.Run("an address of 254 characters, mixed case, is stored lowercase", func(t *testing.T) {
		f := newReg(t, true)
		addr := strings.Repeat("A", 242) + "@example.com"
		if err := f.reg.Register(t.Context(), signup(addr, "Long.Name")); err != nil {
			t.Fatal(err)
		}
		if a := f.st.created[0]; a.Email != strings.ToLower(addr) || a.Username != "long.name" || a.PasswordHash != "hashed" {
			t.Fatalf("stored %+v", a)
		}
	})

	t.Run("username taken: 409, whatever the address", func(t *testing.T) {
		f := newReg(t, true)
		for _, email := range []string{"new@example.com", "taken@example.com"} {
			if err := f.reg.Register(t.Context(), signup(email, "Admin")); !errors.Is(err, ErrUsernameTaken) {
				t.Errorf("%s: %v; want ErrUsernameTaken", email, err)
			}
		}
		if len(f.mailer.sent) != 0 {
			t.Fatal("a mail went out")
		}
	})

	t.Run("challenge rejected, unreachable or not configured: refused, nothing hashed", func(t *testing.T) {
		f := newReg(t, true)
		in := signup("x@example.com", "xavier")
		in.CaptchaToken = "forged"
		if err := f.reg.Register(t.Context(), in); !errors.Is(err, ErrCaptchaFailed) {
			t.Fatalf("rejected = %v", err)
		}
		f.captcha.down = true
		if err := f.reg.Register(t.Context(), signup("x@example.com", "xavier")); !errors.Is(err, ErrCaptchaUnavailable) {
			t.Fatalf("down = %v; want fail closed", err)
		}
		f.reg.captcha = nil
		if err := f.reg.Register(t.Context(), signup("x@example.com", "xavier")); !errors.Is(err, ErrCaptchaUnavailable) {
			t.Fatalf("not configured = %v; want fail closed", err)
		}
		if f.pw.hash != 0 || len(f.st.created) != 0 {
			t.Fatal("work done without a solved challenge")
		}
	})

	t.Run("per-IP limit", func(t *testing.T) {
		f := newReg(t, true)
		for i := range RegisterPerIPPerHour {
			if err := f.reg.Register(t.Context(), signup(fmt.Sprintf("u%d@example.com", i), fmt.Sprintf("user%d", i))); err != nil {
				t.Fatal(err)
			}
		}
		var throttled *ThrottledError
		if err := f.reg.Register(t.Context(), signup("late@example.com", "late")); !errors.As(err, &throttled) || throttled.RetryAfter <= 0 {
			t.Fatalf("over the limit = %v", err)
		}
		other := signup("late@example.com", "late")
		other.ClientIP = "198.51.100.1"
		if err := f.reg.Register(t.Context(), other); err != nil {
			t.Fatalf("another address = %v", err)
		}
	})

	t.Run("no mail configured: still 202, nothing queued", func(t *testing.T) {
		f := newReg(t, true)
		f.reg.mailer = nil
		if err := f.reg.Register(t.Context(), signup("quiet@example.com", "quiet")); err != nil {
			t.Fatal(err)
		}
	})
}

func TestResendVerification(t *testing.T) {
	for _, tt := range []struct {
		outcome store.ResendOutcome
		mails   int
	}{
		{store.ResendIssued, 1}, {store.ResendNoAccount, 0}, {store.ResendAlreadyVerified, 0}, {store.ResendExpired, 0},
	} {
		t.Run(tt.outcome.String(), func(t *testing.T) {
			f := newReg(t, false) // closed registration: resend still works
			f.st.resend = tt.outcome
			if err := f.reg.ResendVerification(t.Context(), " Dana@Example.com ", "good", "192.0.2.7"); err != nil {
				t.Fatalf("err = %v; want nil whatever the outcome", err)
			}
			if len(f.mailer.sent) != tt.mails {
				t.Fatalf("mails = %d, want %d", len(f.mailer.sent), tt.mails)
			}
			if tt.mails == 1 {
				if m := f.mailer.sent[0]; m.Kind != mail.KindVerify || m.To != "dana@example.com" || !strings.Contains(m.Text, "/verify-email#token=") {
					t.Fatalf("mail = %+v", m)
				}
			}
		})
	}
	t.Run("challenge down: fail closed", func(t *testing.T) {
		f := newReg(t, true)
		f.captcha.down = true
		if err := f.reg.ResendVerification(t.Context(), "dana@example.com", "good", "192.0.2.7"); !errors.Is(err, ErrCaptchaUnavailable) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("malformed address is a 400", func(t *testing.T) {
		f := newReg(t, true)
		var verr *ValidationError
		if err := f.reg.ResendVerification(t.Context(), "nope", "good", "192.0.2.7"); !errors.As(err, &verr) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestVerifyEmail(t *testing.T) {
	f := newReg(t, true)
	tok, hash, err := auth.NewOpaqueToken()
	if err != nil {
		t.Fatal(err)
	}
	f.st.verifyOK = hash
	s, err := f.reg.VerifyEmail(t.Context(), tok, "Firefox")
	if err != nil {
		t.Fatal(err)
	}
	p, err := f.auth.Authenticate(s.AccessToken)
	if err != nil || p.UserID != testUserID || p.SessionID != newFamily || s.RefreshToken == "" {
		t.Fatalf("session = %+v, principal %+v, %v", s, p, err)
	}
	other, _, _ := auth.NewOpaqueToken()
	for _, bad := range []string{"", "not-a-token", other} {
		if _, err := f.reg.VerifyEmail(t.Context(), bad, ""); !errors.Is(err, ErrInvalidEmailToken) {
			t.Errorf("VerifyEmail(%q) = %v", bad, err)
		}
	}
}

// Login: an unverified account is refused only after a right password; a
// username + address with 3 failures needs the challenge; under that,
// Cloudflare is never asked.
func TestLoginVerificationAndCaptcha(t *testing.T) {
	key := make([]byte, 48)
	_, _ = rand.Read(key)
	signer, err := auth.NewSigner(key)
	if err != nil {
		t.Fatal(err)
	}
	email := "dana@example.com"
	newAuth := func(u model.User, pw *countingPasswords, c Captcha) *Auth {
		a := NewAuth(oneUserStore{user: u, got: &storeCalls{}}, pw, signer, auth.NewThrottle(), auth.NewThrottle(), slog.New(slog.DiscardHandler))
		if c != nil {
			a.WithCaptcha(c)
		}
		return a
	}
	login := func(a *Auth, token string) error {
		_, err := a.Login(t.Context(), LoginInput{Username: "dana", Password: strongPassword(), ClientIP: "192.0.2.1", CaptchaToken: token})
		return err
	}

	t.Run("unverified: right password → ErrEmailNotVerified, wrong → invalid credentials", func(t *testing.T) {
		u := model.User{ID: testUserID, Username: "dana", PasswordHash: fakePasswordHash, Role: model.RoleUser, Email: &email}
		if err := login(newAuth(u, &countingPasswords{match: false}, nil), ""); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("wrong password = %v", err)
		}
		if err := login(newAuth(u, &countingPasswords{match: true}, nil), ""); !errors.Is(err, ErrEmailNotVerified) {
			t.Fatalf("right password = %v", err)
		}
		now := time.Now()
		u.EmailVerifiedAt = &now
		if err := login(newAuth(u, &countingPasswords{match: true}, nil), ""); err != nil {
			t.Fatalf("verified = %v", err)
		}
		// useradmin accounts: no email, log in as always.
		if err := login(newAuth(model.User{ID: testUserID, Username: "dana", PasswordHash: fakePasswordHash, Role: model.RoleUser}, &countingPasswords{match: true}, nil), ""); err != nil {
			t.Fatalf("no email = %v", err)
		}
	})

	t.Run("challenge after 3 failures", func(t *testing.T) {
		u := model.User{ID: testUserID, Username: "dana", PasswordHash: fakePasswordHash, Role: model.RoleUser}
		pw := &countingPasswords{}
		c := &fakeCaptcha{}
		a := newAuth(u, pw, c)
		for i := 1; i <= 2; i++ {
			if err := login(a, ""); !errors.Is(err, ErrInvalidCredentials) || errors.Is(err, ErrCaptchaRequired) {
				t.Fatalf("failure %d = %v", i, err)
			}
		}
		if err := login(a, ""); !errors.Is(err, ErrCaptchaRequired) || !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("3rd failure = %v; want invalid credentials + captcha required", err)
		}
		if c.calls != 0 {
			t.Fatalf("Cloudflare asked %d times under the threshold", c.calls)
		}
		verifies := pw.verify
		if err := login(a, ""); !errors.Is(err, ErrCaptchaRequired) || errors.Is(err, ErrInvalidCredentials) || pw.verify != verifies {
			t.Fatalf("no token = %v (password checked: %v)", err, pw.verify != verifies)
		}
		c.down = true
		if err := login(a, "good"); !errors.Is(err, ErrCaptchaUnavailable) {
			t.Fatalf("challenge down = %v; want fail closed", err)
		}
		c.down = false
		pw.match = true
		if err := login(a, "good"); err != nil {
			t.Fatalf("solved + right password = %v", err)
		}
		if err := login(a, ""); err != nil {
			t.Fatalf("after a success the counter resets: %v", err)
		}
	})

	t.Run("Turnstile not configured: never asks, the throttle still blocks", func(t *testing.T) {
		a := newAuth(model.User{ID: testUserID, Username: "dana", PasswordHash: fakePasswordHash, Role: model.RoleUser}, &countingPasswords{}, nil)
		for i := 1; i < auth.MaxLoginFailures; i++ {
			if err := login(a, ""); errors.Is(err, ErrCaptchaRequired) || !errors.Is(err, ErrInvalidCredentials) {
				t.Fatalf("failure %d = %v", i, err)
			}
		}
		_ = login(a, "")
		var throttled *ThrottledError
		if err := login(a, ""); !errors.As(err, &throttled) {
			t.Fatalf("after %d failures = %v", auth.MaxLoginFailures, err)
		}
	})
}
