package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/vtnHAN-SOLO/hls-vod/internal/auth"
)

// gatedPasswords holds every verification until release is closed, and
// counts how many started: a parallel burst sits inside the hash at once.
type gatedPasswords struct {
	mu      sync.Mutex
	started int
	release chan struct{}
}

func (g *gatedPasswords) enter() {
	g.mu.Lock()
	g.started++
	g.mu.Unlock()
	<-g.release
}

func (g *gatedPasswords) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.started
}

func (*gatedPasswords) Hash(context.Context, string) (string, error) { return "hashed", nil }
func (g *gatedPasswords) Verify(context.Context, string, string) (bool, error) {
	g.enter()
	return false, nil
}
func (g *gatedPasswords) VerifyDummy(context.Context, string) error {
	g.enter()
	return nil
}

// burst runs n attempts at once against a gated hash. While the first ones
// sit in the hash, the rest must already be refused; then the gate opens.
// It returns how many were throttled and how many reached the hash.
func burst(t *testing.T, pw *gatedPasswords, n int, try func() error) (throttled, hashed int) {
	t.Helper()
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() { errs <- try() })
	}
	// The refused ones return without touching the gate.
	deadline := time.Now().Add(5 * time.Second)
	for len(errs)+pw.count() < n {
		if time.Now().After(deadline) {
			t.Fatalf("stuck: %d returned, %d hashing", len(errs), pw.count())
		}
		time.Sleep(time.Millisecond)
	}
	hashed = pw.count()
	close(pw.release)
	wg.Wait()
	close(errs)
	for err := range errs {
		var te *ThrottledError
		if errors.As(err, &te) {
			throttled++
		}
	}
	return throttled, hashed
}

// A burst of parallel wrong passwords cannot all pass the check before the
// first failure is recorded: only MaxLoginFailures reach the hash, and the
// key is blocked once they fail.
func TestLoginParallelBurst(t *testing.T) {
	pw := &gatedPasswords{release: make(chan struct{})}
	a := newTestAuth(t, pw)
	const n = 20
	wrong := randomPassword(t)
	throttled, hashed := burst(t, pw, n, func() error {
		_, err := a.Login(t.Context(), LoginInput{Username: "alice", Password: wrong, ClientIP: "192.0.2.50"})
		return err
	})
	if hashed != auth.MaxLoginFailures || throttled != n-auth.MaxLoginFailures {
		t.Fatalf("hashed %d, throttled %d; want %d and %d", hashed, throttled, auth.MaxLoginFailures, n-auth.MaxLoginFailures)
	}
	var te *ThrottledError
	if _, err := a.Login(t.Context(), LoginInput{Username: "alice", Password: wrong, ClientIP: "192.0.2.50"}); !errors.As(err, &te) ||
		te.RetryAfter < auth.LoginBackoff-time.Minute {
		t.Fatalf("after the burst: %v, want blocked for the backoff", err)
	}
}

// The per-user password throttle of /me/password and the Google link
// reserves the same way.
func TestChangePasswordParallelBurst(t *testing.T) {
	pw := &gatedPasswords{release: make(chan struct{})}
	a := newTestAuth(t, pw)
	me := Principal{UserID: testUserID, SessionID: testFamily}
	const n = 12
	current, next := randomPassword(t), randomPassword(t)
	throttled, hashed := burst(t, pw, n, func() error {
		_, err := a.ChangePassword(t.Context(), me, current, next, "")
		return err
	})
	if hashed != auth.MaxLoginFailures || throttled != n-auth.MaxLoginFailures {
		t.Fatalf("hashed %d, throttled %d; want %d and %d", hashed, throttled, auth.MaxLoginFailures, n-auth.MaxLoginFailures)
	}
}

// Password spraying: one address, many usernames. From SprayCaptchaAfter
// failures every login from it needs the challenge, from SprayBlockAfter it
// is blocked; other addresses are untouched, and a right password does not
// clear the address.
func TestLoginSpraying(t *testing.T) {
	pw := &countingPasswords{}
	a := newTestAuth(t, pw)
	captcha := &fakeCaptcha{}
	a.WithCaptcha(captcha)
	const sprayer, other = "203.0.113.66", "203.0.113.67"
	guess := randomPassword(t)
	login := func(user, ip, token string) error {
		_, err := a.Login(t.Context(), LoginInput{Username: user, Password: guess, ClientIP: ip, CaptchaToken: token})
		return err
	}

	for i := range SprayCaptchaAfter - 1 {
		if err := login(fmt.Sprintf("user%d", i), sprayer, ""); !errors.Is(err, ErrInvalidCredentials) || errors.Is(err, ErrCaptchaRequired) {
			t.Fatalf("failure %d: %v, want a plain invalid_credentials", i+1, err)
		}
	}
	if captcha.calls != 0 {
		t.Fatalf("Cloudflare called %d times under the threshold", captcha.calls)
	}
	// The failure that reaches the threshold says so at once.
	if err := login("user-last", sprayer, ""); !errors.Is(err, ErrInvalidCredentials) || !errors.Is(err, ErrCaptchaRequired) {
		t.Fatalf("10th failure: %v, want invalid_credentials + captcha_required", err)
	}
	// Now even a fresh username from that address needs it, before any hash.
	hashes := pw.verify + pw.dummy
	if err := login("alice", sprayer, ""); !errors.Is(err, ErrCaptchaRequired) || errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("fresh username, no token: %v", err)
	}
	if pw.verify+pw.dummy != hashes {
		t.Fatal("hashed without the challenge")
	}
	// Another address is untouched.
	if err := login("alice", other, ""); !errors.Is(err, ErrInvalidCredentials) || errors.Is(err, ErrCaptchaRequired) {
		t.Fatalf("other address: %v", err)
	}
	// A solved challenge lets a login through; a right password does not
	// clear the address's count.
	pw.match = true
	if err := login("alice", sprayer, "good"); err != nil {
		t.Fatalf("right password with the challenge: %v", err)
	}
	pw.match = false
	if err := login("alice", sprayer, ""); !errors.Is(err, ErrCaptchaRequired) {
		t.Fatalf("after a success: %v, want the challenge still required", err)
	}

	// Up to the block: 100 failures in all, each username under its own limit.
	for i := SprayCaptchaAfter; i < SprayBlockAfter; i++ {
		if err := login(fmt.Sprintf("user%d", i), sprayer, "good"); errors.As(err, new(*ThrottledError)) {
			t.Fatalf("blocked after %d failures", i)
		}
	}
	var te *ThrottledError
	if err := login("someone-new", sprayer, "good"); !errors.As(err, &te) || te.RetryAfter < SprayWindow-time.Minute {
		t.Fatalf("after %d failures: %v, want blocked for %v", SprayBlockAfter, err, SprayWindow)
	}
	if err := login("someone-new", other, ""); errors.As(err, new(*ThrottledError)) {
		t.Fatalf("the block reached another address: %v", err)
	}
}
