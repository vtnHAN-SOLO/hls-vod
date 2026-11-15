package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"errors"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func randomKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, 48)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return k
}

func TestPasswordHash(t *testing.T) {
	h := NewHasher(2)
	ctx := t.Context()
	enc, err := h.Hash(ctx, "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	// The standard encoding with the OWASP minimum parameters.
	if !regexp.MustCompile(`^\$argon2id\$v=19\$m=19456,t=2,p=1\$[A-Za-z0-9+/]{22}\$[A-Za-z0-9+/]{43}$`).MatchString(enc) {
		t.Fatalf("encoded hash = %q", enc)
	}
	if ok, err := h.Verify(ctx, enc, "correct horse battery staple"); err != nil || !ok {
		t.Fatalf("Verify(right) = %v, %v", ok, err)
	}
	if ok, err := h.Verify(ctx, enc, "correct horse battery stapl"); err != nil || ok {
		t.Fatalf("Verify(wrong) = %v, %v", ok, err)
	}
	other, _ := h.Hash(ctx, "correct horse battery staple")
	if other == enc {
		t.Fatal("two hashes of one password are equal: the salt is not random")
	}

	for name, bad := range map[string]string{
		"empty":         "",
		"bcrypt":        "$2a$10$abcdefghijklmnopqrstuuAbCdEfGhIjKlMnOpQrStUvWxYz012",
		"argon2i":       strings.Replace(enc, "argon2id", "argon2i", 1),
		"other version": strings.Replace(enc, "v=19", "v=16", 1),
		"huge memory":   strings.Replace(enc, "m=19456", "m=4194304", 1),
		"huge time":     strings.Replace(enc, "t=2", "t=1000", 1),
		"bad base64":    enc[:len(enc)-5] + "!!!!!",
		"missing part":  enc[:strings.LastIndex(enc, "$")],
	} {
		if _, err := h.Verify(ctx, bad, "x"); !errors.Is(err, ErrMalformedHash) {
			t.Errorf("%s: Verify = %v, want ErrMalformedHash", name, err)
		}
	}

	// The dummy check runs a real verification and matches nothing.
	start := time.Now()
	if err := h.VerifyDummy(ctx, "anything at all"); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < time.Millisecond {
		t.Fatal("VerifyDummy returned too fast to have hashed")
	}
}

func TestHasherCapsConcurrency(t *testing.T) {
	h := NewHasher(2)
	var mu sync.Mutex
	peak := 0
	h.observe = func(n int) {
		mu.Lock()
		peak = max(peak, n)
		mu.Unlock()
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := h.Hash(t.Context(), "a password long enough"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if peak != 2 {
		t.Fatalf("peak concurrent hashes = %d, want exactly 2", peak)
	}

	// Waiting for a slot respects the request's context.
	full := NewHasher(1)
	full.sem <- struct{}{}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if _, err := full.Hash(ctx, "a password long enough"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Hash with no free slot = %v, want the context's deadline", err)
	}
}

func TestCheckPassword(t *testing.T) {
	for _, tt := range []struct {
		p  string
		ok bool
	}{
		{"elevenchars", false},
		{"twelve chars", true},
		{strings.Repeat("é", 12), true},
		{strings.Repeat("a", 1024), true},
		{strings.Repeat("a", 1025), false},
		{"bad utf-8 \xff\xfe pass", false},
	} {
		if err := CheckPassword(tt.p); (err == nil) != tt.ok {
			t.Errorf("CheckPassword(%q…) = %v, want ok=%v", tt.p[:min(len(tt.p), 16)], err, tt.ok)
		}
	}
}

func TestAccessTokens(t *testing.T) {
	key := randomKey(t)
	s, err := NewSigner(key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewSigner(key[:31]); err == nil {
		t.Fatal("a 31-byte key was accepted")
	}
	const uid = "0b9d6b52-2d2c-4a7b-9e0e-6a4f39a0c1d2"
	const sid = "5d0c2e8a-7f1b-4c3e-9a6d-2b8f4e1c7a90"

	if _, _, err := s.Issue(uid, "admin", ""); err == nil {
		t.Fatal("Issue without a session id succeeded")
	}
	tok, exp, err := s.Issue(uid, "admin", sid)
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Until(exp); d < 14*time.Minute || d > 15*time.Minute {
		t.Fatalf("expires in %v, want 15 min", d)
	}
	c, err := s.Verify(tok)
	if err != nil || c != (Claims{UserID: uid, Role: "admin", SessionID: sid}) {
		t.Fatalf("Verify = %+v, %v", c, err)
	}
	header, _ := base64.RawURLEncoding.DecodeString(strings.Split(tok, ".")[0])
	if string(header) != `{"alg":"HS256","typ":"JWT"}` {
		t.Fatalf("header = %s", header)
	}

	now := time.Now()
	claims := func(role string, iat, exp time.Time) jwt.MapClaims {
		return jwt.MapClaims{"sub": uid, "role": role, "sid": sid, "iat": iat.Unix(), "exp": exp.Unix()}
	}
	sign := func(m jwt.SigningMethod, c jwt.Claims, k any) string {
		t.Helper()
		out, err := jwt.NewWithClaims(m, c).SignedString(k)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	valid := claims("user", now, now.Add(time.Minute))
	parts := strings.Split(tok, ".")
	sig := []byte(parts[2])
	sig[3] ^= 1
	unsigned := sign(jwt.SigningMethodNone, valid, jwt.UnsafeAllowNoneSignatureType)

	refused := map[string]string{
		"alg none":                unsigned,
		"alg none with signature": unsigned + parts[2],
		"HS512 with the same key": sign(jwt.SigningMethodHS512, valid, key),
		"RS256":                   sign(jwt.SigningMethodRS256, valid, rsaKey),
		"another HS256 key":       sign(jwt.SigningMethodHS256, valid, randomKey(t)),
		"tampered signature":      parts[0] + "." + parts[1] + "." + string(sig),
		"tampered payload":        parts[0] + "." + base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"x","role":"admin","exp":9999999999,"iat":1}`)) + "." + parts[2],
		"expired":                 sign(jwt.SigningMethodHS256, claims("user", now.Add(-time.Hour), now.Add(-time.Second)), key),
		"issued in the future":    sign(jwt.SigningMethodHS256, claims("user", now.Add(time.Hour), now.Add(2*time.Hour)), key),
		"no exp":                  sign(jwt.SigningMethodHS256, jwt.MapClaims{"sub": uid, "role": "user", "sid": sid, "iat": now.Unix()}, key),
		"no iat":                  sign(jwt.SigningMethodHS256, jwt.MapClaims{"sub": uid, "role": "user", "sid": sid, "exp": now.Add(time.Minute).Unix()}, key),
		// Issued before sessions were tracked: refused, so the holder logs in once more.
		"no sid":                   sign(jwt.SigningMethodHS256, jwt.MapClaims{"sub": uid, "role": "user", "iat": now.Unix(), "exp": now.Add(time.Minute).Unix()}, key),
		"empty sid":                sign(jwt.SigningMethodHS256, jwt.MapClaims{"sub": uid, "role": "user", "sid": "", "iat": now.Unix(), "exp": now.Add(time.Minute).Unix()}, key),
		"unknown role":             sign(jwt.SigningMethodHS256, claims("root", now, now.Add(time.Minute)), key),
		"no subject":               sign(jwt.SigningMethodHS256, jwt.MapClaims{"role": "user", "sid": sid, "iat": now.Unix(), "exp": now.Add(time.Minute).Unix()}, key),
		"empty":                    "",
		"garbage":                  "not.a.jwt",
		"bearer prefix left on it": "Bearer " + tok,
	}
	for name, tok := range refused {
		if _, err := s.Verify(tok); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("%s: Verify = %v, want ErrInvalidToken", name, err)
		}
	}
}

func TestRefreshTokens(t *testing.T) {
	tok, hash, err := NewRefreshToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(tok) != 43 || len(hash) != 32 {
		t.Fatalf("token %d chars, hash %d bytes", len(tok), len(hash))
	}
	got, ok := HashRefreshToken(tok)
	if !ok || string(got) != string(hash) {
		t.Fatal("HashRefreshToken does not match NewRefreshToken's hash")
	}
	if strings.Contains(string(hash), tok) {
		t.Fatal("the stored form contains the token")
	}
	other, _, _ := NewRefreshToken()
	if other == tok {
		t.Fatal("two refresh tokens are equal")
	}
	for _, bad := range []string{"", "short", tok + "A", tok[:42] + "!", strings.Repeat("A", 44)} {
		if _, ok := HashRefreshToken(bad); ok {
			t.Errorf("HashRefreshToken(%q) accepted", bad)
		}
	}
}

// failOnce reserves an attempt for key and records it as a failure.
func failOnce(t *testing.T, th *Throttle, key string) {
	t.Helper()
	a, _, ok := th.Allow(key)
	if !ok {
		t.Fatalf("Allow(%q) refused", key)
	}
	a.Fail()
}

func allowed(th *Throttle, key string) bool {
	a, _, ok := th.Allow(key)
	a.Release()
	return ok
}

func TestThrottle(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	th := NewThrottle()
	th.now = func() time.Time { return now }
	const key = "alice|203.0.113.7"

	for i := range MaxLoginFailures - 1 {
		a, _, ok := th.Allow(key)
		if !ok {
			t.Fatalf("blocked after %d failures", i)
		}
		if a.Prior != i {
			t.Fatalf("Prior = %d, want %d", a.Prior, i)
		}
		if n := a.Fail(); n != i+1 {
			t.Fatalf("Fail = %d, want %d", n, i+1)
		}
	}
	if !allowed(th, key) {
		t.Fatal("blocked after 4 failures, want the 5th to block")
	}
	failOnce(t, th, key)
	_, retry, ok := th.Allow(key)
	if ok || retry != LoginBackoff {
		t.Fatalf("after 5 failures: ok=%v retry=%v, want blocked for %v", ok, retry, LoginBackoff)
	}
	if !allowed(th, "alice|198.51.100.1") {
		t.Fatal("the same user from another IP is blocked: the key is username + IP")
	}
	if !allowed(th, "bob|203.0.113.7") {
		t.Fatal("another user from the same IP is blocked")
	}

	now = now.Add(LoginBackoff - time.Second)
	if _, retry, ok := th.Allow(key); ok || retry != time.Second {
		t.Fatalf("1s before the end: ok=%v retry=%v", ok, retry)
	}
	now = now.Add(time.Second)
	if !allowed(th, key) {
		t.Fatal("still blocked after the backoff")
	}
	failOnce(t, th, key) // a fresh window: one failure is not a block
	if !allowed(th, key) {
		t.Fatal("one failure after the backoff blocked again")
	}

	// Failures spread over more than the window do not add up.
	slow := "carol|192.0.2.1"
	for range MaxLoginFailures + 2 {
		failOnce(t, th, slow)
		now = now.Add(LoginBackoff/4 + time.Second)
	}
	if !allowed(th, slow) {
		t.Fatal("failures spread over hours blocked the key")
	}

	// A successful login forgets the failures.
	const dave = "dave|192.0.2.2"
	for range MaxLoginFailures - 1 {
		failOnce(t, th, dave)
	}
	a, _, _ := th.Allow(dave)
	a.Succeed()
	failOnce(t, th, dave)
	if !allowed(th, dave) || th.Failures(dave) != 1 {
		t.Fatalf("Succeed did not forget earlier failures: %d", th.Failures(dave))
	}
}

// The reservation: parallel attempts count while they run, so a burst
// cannot all pass Allow before the first failure is recorded.
func TestThrottleInFlight(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	th := NewThrottle()
	th.now = func() time.Time { return now }
	const key = "erin|203.0.113.9"
	failOnce(t, th, key)

	var held []*Attempt
	for i := range MaxLoginFailures - 1 {
		a, _, ok := th.Allow(key)
		if !ok {
			t.Fatalf("attempt %d refused with %d in flight", i, len(held))
		}
		if a.Prior != 1+i {
			t.Fatalf("Prior = %d, want %d (1 failure + %d in flight)", a.Prior, 1+i, i)
		}
		held = append(held, a)
	}
	if _, retry, ok := th.Allow(key); ok || retry != InFlightRetry {
		t.Fatalf("a 6th concurrent attempt: ok=%v retry=%v, want refused for %v", ok, retry, InFlightRetry)
	}

	// Release frees the slot and records nothing.
	held[0].Release()
	held[0].Fail() // no-op after Release
	if th.Failures(key) != 1 {
		t.Fatalf("Failures = %d after a release", th.Failures(key))
	}
	a, _, ok := th.Allow(key)
	if !ok {
		t.Fatal("the released slot is not free")
	}
	held[0] = a

	// Every held attempt failing blocks the key for the backoff.
	for _, a := range held {
		a.Fail()
	}
	if _, retry, ok := th.Allow(key); ok || retry != LoginBackoff {
		t.Fatalf("after the burst failed: ok=%v retry=%v", ok, retry)
	}

	// A success among in-flight attempts frees only its own slot.
	const frank = "frank|203.0.113.10"
	a1, _, _ := th.Allow(frank)
	a2, _, _ := th.Allow(frank)
	a1.Succeed()
	if a3, _, ok := th.Allow(frank); !ok || a3.Prior != 1 {
		t.Fatalf("after one success: ok=%v prior=%d, want the other still counted", ok, a3.Prior)
	}
	a2.Release()

	// Idle keys leave the map.
	th2 := NewThrottle()
	b, _, _ := th2.Allow("x")
	b.Release()
	if len(th2.entries) != 0 {
		t.Fatalf("an idle key stayed: %v", th2.entries)
	}
}

func TestLimiterAndFailures(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l := NewLimiter(2, time.Hour)
	l.now = func() time.Time { return now }
	if n := l.Failures("ip"); n != 0 {
		t.Fatalf("Failures of a new key = %d", n)
	}
	failOnce(t, l, "ip")
	if n := l.Failures("ip"); n != 1 {
		t.Fatalf("Failures = %d, want 1", n)
	}
	failOnce(t, l, "ip")
	if _, retry, ok := l.Allow("ip"); ok || retry != time.Hour {
		t.Fatalf("after 2 of 2: Allow = %v, %v; want blocked for the window", retry, ok)
	}
	if !allowed(l, "other") {
		t.Fatal("another key is blocked")
	}
	now = now.Add(time.Hour)
	if !allowed(l, "ip") || l.Failures("ip") != 0 {
		t.Fatalf("after the window: Failures = %d", l.Failures("ip"))
	}
}

func TestCheckUsername(t *testing.T) {
	for _, tt := range []struct {
		name string
		ok   bool
	}{
		{"alice", true}, {"a", true}, {"bob.smith_2-x", true}, {strings.Repeat("a", 64), true},
		{"", false}, {"Alice", false}, {".alice", false}, {"-a", false}, {"al ice", false},
		{"al@ice", false}, {strings.Repeat("a", 65), false}, {"ålice", false},
	} {
		if err := CheckUsername(tt.name); (err == nil) != tt.ok {
			t.Errorf("CheckUsername(%q) = %v, want ok=%v", tt.name, err, tt.ok)
		}
	}
}

func TestOpaqueTokens(t *testing.T) {
	tok, hash, err := NewOpaqueToken()
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := HashOpaqueToken(tok); !ok || string(got) != string(hash) {
		t.Fatal("the token does not hash to its stored form")
	}
	for _, bad := range []string{"", "short", tok + "A", "!" + tok[1:]} {
		if _, ok := HashOpaqueToken(bad); ok {
			t.Errorf("HashOpaqueToken(%q) accepted", bad)
		}
	}
}
