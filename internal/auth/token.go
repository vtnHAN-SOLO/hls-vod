package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Token lifetimes.
const (
	AccessTTL  = 15 * time.Minute
	RefreshTTL = 30 * 24 * time.Hour
)

// MinSigningKeyLen is the shortest JWT_SIGNING_KEY accepted, in bytes:
// HS256 wants a key at least as long as its 256-bit output.
const MinSigningKeyLen = 32

// ErrInvalidToken covers every reason an access token is refused: bad
// signature, wrong algorithm, expired, malformed claims. Callers never learn
// which, and neither does the client.
var ErrInvalidToken = errors.New("invalid access token")

// Claims is what an access token asserts.
type Claims struct {
	UserID string
	Role   string
	// SessionID is the refresh-token family (one login) the token was
	// issued for: the account page marks that session "This device", and
	// "sign out everywhere else" keeps it.
	SessionID string
}

type accessClaims struct {
	Role string `json:"role"`
	Sid  string `json:"sid"`
	jwt.RegisteredClaims
}

// Signer issues and verifies HS256 access tokens. One service signs and
// verifies, so a shared secret is enough; no key pair to distribute.
type Signer struct {
	key []byte
	now func() time.Time
}

// NewSigner returns a Signer for key, which must be at least
// MinSigningKeyLen bytes.
func NewSigner(key []byte) (*Signer, error) {
	if len(key) < MinSigningKeyLen {
		return nil, fmt.Errorf("JWT signing key must be at least %d bytes, got %d", MinSigningKeyLen, len(key))
	}
	return &Signer{key: key, now: time.Now}, nil
}

// Issue returns a signed access token for the user's session (refresh-token
// family) sessionID, valid for AccessTTL. Claims: sub (user id), role, sid,
// iat, exp.
func (s *Signer) Issue(userID, role, sessionID string) (string, time.Time, error) {
	if sessionID == "" {
		return "", time.Time{}, errors.New("signing access token: no session id")
	}
	now := s.now().Truncate(time.Second)
	exp := now.Add(AccessTTL)
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims{
		Role: role,
		Sid:  sessionID,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(exp),
		},
	})
	signed, err := tok.SignedString(s.key)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("signing access token: %w", err)
	}
	return signed, exp, nil
}

// Verify checks token and returns its claims. The algorithm is pinned to
// HS256: "none", HS512, RS256 and anything else are refused, so a token
// cannot choose how it is checked. A token without a sid (issued before
// sessions were tracked) is refused too: its holder logs in once more.
//
// Verify does not ask the database: a token stays valid until it expires
// even after its session is revoked (at most AccessTTL).
func (s *Signer) Verify(token string) (Claims, error) {
	var c accessClaims
	_, err := jwt.ParseWithClaims(token, &c, func(*jwt.Token) (any, error) { return s.key, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithTimeFunc(s.now),
	)
	if err != nil {
		return Claims{}, ErrInvalidToken
	}
	if c.Subject == "" || c.Sid == "" || (c.Role != "user" && c.Role != "admin") || c.IssuedAt == nil {
		return Claims{}, ErrInvalidToken
	}
	return Claims{UserID: c.Subject, Role: c.Role, SessionID: c.Sid}, nil
}

// opaqueTokenBytes is the entropy of a refresh token and of a mailed link's
// token.
const opaqueTokenBytes = 32

// VerifyTTL is how long an email verification link lives, and so how long
// an unverified account can wait (see store.ResendVerification).
const VerifyTTL = 24 * time.Hour

// ResetTTL is how long a password-reset link lives.
const ResetTTL = 30 * time.Minute

// NewOpaqueToken returns a random token (32 bytes, base64url) and the
// SHA-256 that is stored instead of it. SHA-256, not argon2id: the token is
// random and high-entropy, so a fast hash cannot be brute-forced. Refresh
// tokens and the tokens in mailed links are both made here.
func NewOpaqueToken() (token string, hash []byte, err error) {
	b := make([]byte, opaqueTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", nil, fmt.Errorf("generating token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(token))
	return token, sum[:], nil
}

// HashOpaqueToken returns the stored form of a presented token, or false if
// it cannot be one of ours (so the database is not even asked).
func HashOpaqueToken(token string) ([]byte, bool) {
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(b) != opaqueTokenBytes {
		return nil, false
	}
	sum := sha256.Sum256([]byte(token))
	return sum[:], true
}

// NewRefreshToken is NewOpaqueToken, for a refresh token.
func NewRefreshToken() (token string, hash []byte, err error) { return NewOpaqueToken() }

// HashRefreshToken is HashOpaqueToken, for a refresh token.
func HashRefreshToken(token string) ([]byte, bool) { return HashOpaqueToken(token) }
