package service

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

const testMovieID = "0b9d6b52-2d2c-4a7b-9e0e-6a4f39a0c1d2"

// randomKey is generated at test time: never a literal secret.
func randomKey(t *testing.T) string {
	t.Helper()
	b := make([]byte, 36)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b) // 48 bytes, like `openssl rand -base64 36`
}

func newSigner(t *testing.T, key string, now time.Time) *MediaSigner {
	t.Helper()
	s, err := NewMediaSigner(key, 0)
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return now }
	return s
}

// The token is checked against OpenSSL, the way nginx's docs build a
// secure_link_md5 token in a shell, so a mistake in our own MD5 or base64
// handling cannot hide behind a vector we computed with the same code.
func TestTokenMatchesOpenSSL(t *testing.T) {
	key := randomKey(t)
	s := newSigner(t, key, time.Now())
	for _, exp := range []int64{1, 1_700_000_000, 4_102_444_800} {
		msg := strconv.FormatInt(exp, 10) + "/" + testMovieID + " " + key
		cmd := exec.CommandContext(t.Context(), "sh", "-c",
			"openssl md5 -binary | openssl base64 -A | tr '+/' '-_' | tr -d '='")
		cmd.Stdin = strings.NewReader(msg)
		var out, errOut bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errOut
		if err := cmd.Run(); err != nil {
			t.Fatalf("openssl (needed for this test): %v: %s", err, errOut.String())
		}
		want := strings.TrimSpace(out.String())
		if len(want) != 22 {
			t.Fatalf("openssl printed %q, want 22 base64url characters", want)
		}
		if got := s.Token(testMovieID, exp); got != want {
			t.Errorf("Token(%d) = %q, openssl says %q", exp, got, want)
		}
	}
}

func TestTokenCoversMovieExpiryAndKey(t *testing.T) {
	key := randomKey(t)
	s := newSigner(t, key, time.Now())
	base := s.Token(testMovieID, 1_700_000_000)
	others := map[string]string{
		"another movie":  s.Token("9f8e7d6c-5b4a-4392-8170-6f5e4d3c2b1a", 1_700_000_000),
		"another expiry": s.Token(testMovieID, 1_700_000_001),
		"another key":    newSigner(t, randomKey(t), time.Now()).Token(testMovieID, 1_700_000_000),
	}
	for name, tok := range others {
		if tok == base {
			t.Errorf("%s gives the same token %q", name, tok)
		}
	}
	if again := s.Token(testMovieID, 1_700_000_000); again != base {
		t.Errorf("Token is not deterministic: %q then %q", base, again)
	}
}

func TestExpires(t *testing.T) {
	now := time.Unix(1_700_000_123, 0)
	s := newSigner(t, randomKey(t), now)
	tests := []struct {
		ttl      time.Duration
		min, max time.Duration
		step     int64
	}{
		{PlaybackURLTTL, PlaybackURLTTL, PlaybackURLTTL + 15*time.Minute, 15 * 60},
		{PosterURLTTL, PosterURLTTL, PosterURLTTL + time.Hour, 3600},
	}
	for _, tt := range tests {
		exp := s.Expires(tt.ttl)
		if d := time.Unix(exp, 0).Sub(now); d < tt.min || d > tt.max {
			t.Errorf("ttl %v: valid for %v, want between %v and %v", tt.ttl, d, tt.min, tt.max)
		}
		if exp%tt.step != 0 {
			t.Errorf("ttl %v: expiry %d not rounded to %ds", tt.ttl, exp, tt.step)
		}
	}

	// Within one step every response carries the same URL (browser cache);
	// after it, a new one.
	a := s.URL(testMovieID, "poster.jpg", PosterURLTTL)
	s.now = func() time.Time { return now.Add(time.Minute) }
	if b := s.URL(testMovieID, "poster.jpg", PosterURLTTL); b != a {
		t.Errorf("URL changed within one rounding step: %q then %q", a, b)
	}
	s.now = func() time.Time { return now.Add(2 * time.Hour) }
	if b := s.URL(testMovieID, "poster.jpg", PosterURLTTL); b == a {
		t.Errorf("URL did not change after two hours: %q", b)
	}
}

func TestDevTTLReplacesEveryLifetime(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s, err := NewMediaSigner(randomKey(t), 90*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return now }
	for _, ttl := range []time.Duration{PlaybackURLTTL, PosterURLTTL} {
		if d := time.Unix(s.Expires(ttl), 0).Sub(now); d < 90*time.Second || d > 95*time.Second {
			t.Errorf("ttl %v with DEV_MEDIA_URL_TTL=90s: valid for %v", ttl, d)
		}
	}
}

func TestNewMediaSignerRejects(t *testing.T) {
	if _, err := NewMediaSigner("", 0); err == nil {
		t.Error("empty key accepted")
	}
	if _, err := NewMediaSigner(randomKey(t), -time.Second); err == nil {
		t.Error("negative TTL accepted")
	}
}

func TestURL(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	s := newSigner(t, randomKey(t), now)
	exp := s.Expires(PlaybackURLTTL)
	prefix := "/media/" + s.Token(testMovieID, exp) + "/" + strconv.FormatInt(exp, 10) + "/" + testMovieID + "/"
	tests := []struct{ rel, want string }{
		{"master.m3u8", prefix + "master.m3u8"},
		{"poster.jpg", prefix + "poster.jpg"},
		{"subs/0_eng.vtt", prefix + "subs/0_eng.vtt"},
		// Never a URL that climbs out of the signed folder.
		{"", ""},
		{"../other/master.m3u8", ""},
		{"..", ""},
		{"a/../../b", ""},
		{"/etc/passwd", ""},
		{"./poster.jpg", ""},
		{`..\poster.jpg`, ""},
		{"poster.jpg?x=1", ""},
		{"poster.jpg#frag", ""},
		{"%2e%2e%2fother/master.m3u8", ""},
		{"..%2fother/master.m3u8", ""},
		{"poster.jpg\x00.png", ""},
		{"http://evil.example/x.jpg", ""},
	}
	for _, tt := range tests {
		if got := s.URL(testMovieID, tt.rel, PlaybackURLTTL); got != tt.want {
			t.Errorf("URL(%q) = %q, want %q", tt.rel, got, tt.want)
		}
	}
}

func TestPosterURL(t *testing.T) {
	now := time.Unix(1_700_000_123, 0)
	s := newSigner(t, randomKey(t), now)
	hash := strings.Repeat("ab", 32)
	u := s.PosterURL(testMovieID, 600, hash)
	prefix := "/api/v1/posters/" + testMovieID + "/600/" + hash + ".jpg?exp="
	if !strings.HasPrefix(u, prefix) {
		t.Fatalf("url = %s", u)
	}
	q := strings.TrimPrefix(u, prefix)
	exp, sig, ok := strings.Cut(q, "&sig=")
	if !ok {
		t.Fatalf("no sig in %s", u)
	}
	e, _ := strconv.ParseInt(exp, 10, 64)
	if d := time.Unix(e, 0).Sub(now); d < 24*time.Hour || d > 25*time.Hour {
		t.Errorf("lifetime %v, want 24 h rounded up to the hour", d)
	}
	// Rounded: the same URL for a while, so the browser cache works.
	s.now = func() time.Time { return now.Add(time.Minute) }
	if again := s.PosterURL(testMovieID, 600, hash); again != u {
		t.Errorf("a minute later the URL changed: %s", again)
	}

	tests := []struct {
		name           string
		id             string
		width          int
		hash, exp, sig string
		at             time.Time
		wantErr        error
	}{
		{"genuine", testMovieID, 600, hash, exp, sig, now, nil},
		{"other width", testMovieID, 300, hash, exp, sig, now, ErrBadSignature},
		{"other movie", "1b9d6b52-2d2c-4a7b-9e0e-6a4f39a0c1d2", 600, hash, exp, sig, now, ErrBadSignature},
		{"other hash", testMovieID, 600, strings.Repeat("cd", 32), exp, sig, now, ErrBadSignature},
		{"exp extended", testMovieID, 600, hash, strconv.FormatInt(e+3600, 10), sig, now, ErrBadSignature},
		{"exp with a leading zero", testMovieID, 600, hash, "0" + exp, sig, now, ErrBadSignature},
		{"exp not a number", testMovieID, 600, hash, "soon", sig, now, ErrBadSignature},
		{"empty sig", testMovieID, 600, hash, exp, "", now, ErrBadSignature},
		{"at the expiry", testMovieID, 600, hash, exp, sig, time.Unix(e, 0), ErrExpired},
		{"after the expiry", testMovieID, 600, hash, exp, sig, time.Unix(e, 0).Add(time.Hour), ErrExpired},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s.now = func() time.Time { return tt.at }
			if _, err := s.CheckPoster(tt.id, tt.width, tt.hash, tt.exp, tt.sig); !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
		})
	}

	// The key is derived for this purpose: another key never verifies, and
	// the media token key alone is not the poster key.
	other := newSigner(t, randomKey(t), now)
	if _, err := other.CheckPoster(testMovieID, 600, hash, exp, sig); !errors.Is(err, ErrBadSignature) {
		t.Errorf("another key verified: %v", err)
	}
	if bytes.Equal(s.posterKey, s.key) {
		t.Error("the poster key is the media signing key itself")
	}
}
