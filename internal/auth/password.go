// Package auth holds the security primitives behind login: argon2id password
// hashes, HS256 access tokens, opaque refresh tokens and the login throttle.
// It knows nothing about HTTP or SQL.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters: the OWASP minimum (m=19 MiB, t=2, p=1). Stored in
// every encoded hash, so they can be raised later without breaking old ones.
const (
	argonMemoryKiB = 19 * 1024
	argonTime      = 2
	argonThreads   = 1
	argonSaltLen   = 16
	argonKeyLen    = 32
)

// Bounds on parameters read back from a stored hash, so a corrupted or
// crafted row cannot make one verification allocate gigabytes.
const (
	maxMemoryKiB = 256 * 1024
	maxTime      = 10
	maxThreads   = 4
)

// Password length rules, in characters.
const (
	MinPasswordLen = 12
	MaxPasswordLen = 1024
)

var (
	// ErrPasswordPolicy means a new password is too short or too long.
	ErrPasswordPolicy = fmt.Errorf("password must be %d to %d characters", MinPasswordLen, MaxPasswordLen)
	// ErrMalformedHash means a stored hash is not a valid argon2id string.
	ErrMalformedHash = errors.New("malformed argon2id hash")
)

// ErrUsernamePolicy means a username breaks the username rule.
var ErrUsernamePolicy = errors.New("username must be 1 to 64 characters: lowercase letters, digits, '.', '_' or '-', starting with a letter or digit")

// usernamePattern: lowercase, as the database enforces, and nothing that
// needs escaping anywhere. cmd/useradmin and registration share it.
var usernamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// CheckUsername applies the username rule to a new username, already
// lowercased by the caller.
func CheckUsername(name string) error {
	if !usernamePattern.MatchString(name) {
		return ErrUsernamePolicy
	}
	return nil
}

// CheckPassword applies the length policy to a new password.
func CheckPassword(p string) error {
	n := utf8.RuneCountInString(p)
	if !utf8.ValidString(p) || n < MinPasswordLen || n > MaxPasswordLen {
		return ErrPasswordPolicy
	}
	return nil
}

// Hasher hashes and verifies passwords. At most maxConcurrent hashes run at
// once: each one takes 19 MiB, beside a poster being processed in the api
// pod's 512Mi (internal/poster).
type Hasher struct {
	sem chan struct{}

	dummyOnce sync.Once
	dummy     string

	// observe, if set, is called with the number of hashes in flight each
	// time one starts. Tests use it to check the cap.
	observe func(inFlight int)
	mu      sync.Mutex
	active  int
}

// NewHasher returns a Hasher that runs at most maxConcurrent hashes at once.
func NewHasher(maxConcurrent int) *Hasher {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	return &Hasher{sem: make(chan struct{}, maxConcurrent)}
}

// Hash returns the standard encoded argon2id string for password:
// $argon2id$v=19$m=19456,t=2,p=1$<salt>$<key>, base64 without padding.
func (h *Hasher) Hash(ctx context.Context, password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generating salt: %w", err)
	}
	key, err := h.derive(ctx, password, salt, argonTime, argonMemoryKiB, argonThreads, argonKeyLen)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemoryKiB, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// Verify reports whether password matches the encoded hash, comparing in
// constant time.
func (h *Hasher) Verify(ctx context.Context, encoded, password string) (bool, error) {
	p, err := parseHash(encoded)
	if err != nil {
		return false, err
	}
	key, err := h.derive(ctx, password, p.salt, p.time, p.memory, p.threads, uint32(len(p.key))) // #nosec G115 -- key length is bounded by parseHash
	if err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare(key, p.key) == 1, nil
}

// VerifyDummy runs one full verification against a fixed hash that no
// password matches. Login calls it for an unknown username, so the response
// takes as long as for a wrong password and does not reveal which usernames
// exist.
func (h *Hasher) VerifyDummy(ctx context.Context, password string) error {
	h.dummyOnce.Do(func() {
		secret := make([]byte, 32)
		_, _ = rand.Read(secret)
		// WithoutCancel: the dummy is computed once and shared, so one
		// request's cancellation must not leave it empty for everyone.
		h.dummy, _ = h.Hash(context.WithoutCancel(ctx), base64.RawStdEncoding.EncodeToString(secret))
	})
	if h.dummy == "" {
		return errors.New("dummy hash unavailable")
	}
	_, err := h.Verify(ctx, h.dummy, password)
	return err
}

func (h *Hasher) derive(ctx context.Context, password string, salt []byte, t, m uint32, p uint8, keyLen uint32) ([]byte, error) {
	select {
	case h.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-h.sem }()
	if h.observe != nil {
		h.mu.Lock()
		h.active++
		h.observe(h.active)
		h.mu.Unlock()
		defer func() {
			h.mu.Lock()
			h.active--
			h.mu.Unlock()
		}()
	}
	return argon2.IDKey([]byte(password), salt, t, m, p, keyLen), nil
}

type hashParams struct {
	memory, time uint32
	threads      uint8
	salt, key    []byte
}

func parseHash(encoded string) (hashParams, error) {
	parts := strings.Split(encoded, "$")
	// "", "argon2id", "v=19", "m=…,t=…,p=…", salt, key
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return hashParams{}, ErrMalformedHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return hashParams{}, ErrMalformedHash
	}
	var p hashParams
	var threads uint32
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.memory, &p.time, &threads); err != nil {
		return hashParams{}, ErrMalformedHash
	}
	if p.memory < 8 || p.memory > maxMemoryKiB || p.time < 1 || p.time > maxTime || threads < 1 || threads > maxThreads {
		return hashParams{}, ErrMalformedHash
	}
	p.threads = uint8(threads)
	var err error
	if p.salt, err = base64.RawStdEncoding.DecodeString(parts[4]); err != nil || len(p.salt) < 8 {
		return hashParams{}, ErrMalformedHash
	}
	if p.key, err = base64.RawStdEncoding.DecodeString(parts[5]); err != nil || len(p.key) < 16 || len(p.key) > 64 {
		return hashParams{}, ErrMalformedHash
	}
	return p, nil
}
