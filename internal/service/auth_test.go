package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"

	"github.com/vtnHAN-SOLO/hls-vod/internal/auth"
	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
)

// countingPasswords records which verification ran.
type countingPasswords struct {
	verify, dummy, hash int
	match               bool
}

func (c *countingPasswords) Hash(context.Context, string) (string, error) {
	c.hash++
	return "hashed", nil
}

func (c *countingPasswords) Verify(context.Context, string, string) (bool, error) {
	c.verify++
	return c.match, nil
}

func (c *countingPasswords) VerifyDummy(context.Context, string) error {
	c.dummy++
	return nil
}

// oneUserStore is an AuthStore with one user. Its password change and
// sessions methods record what they were asked.
type oneUserStore struct {
	user model.User
	got  *storeCalls
}

type storeCalls struct {
	changedHash string
	changedUA   string
	revokedKeep string
	liveChecks  atomic.Int32 // parallel tests call SessionLive at once
}

func (s oneUserStore) UserByUsername(_ context.Context, name string) (model.User, error) {
	if name == s.user.Username {
		return s.user, nil
	}
	return model.User{}, store.ErrNotFound
}
func (s oneUserStore) UserByID(context.Context, string) (model.User, error) { return s.user, nil }
func (oneUserStore) CreateRefreshToken(context.Context, string, store.NewRefresh) (string, error) {
	return testFamily, nil
}
func (oneUserStore) RotateRefreshToken(context.Context, []byte, store.NewRefresh) (string, string, error) {
	return "", "", store.ErrRefreshInvalid
}
func (oneUserStore) RevokeRefreshFamily(context.Context, []byte) error { return nil }
func (s oneUserStore) ChangePassword(_ context.Context, _, hash string, r store.NewRefresh) (string, error) {
	s.got.changedHash, s.got.changedUA = hash, r.UserAgent
	return newFamily, nil
}
func (oneUserStore) ListSessions(context.Context, string) ([]model.LoginSession, error) {
	ua := "Mozilla/5.0 (X11; Linux x86_64; rv:140.0) Gecko/20100101 Firefox/140.0"
	return []model.LoginSession{{ID: otherFamily, UserAgent: nil}, {ID: testFamily, UserAgent: &ua}}, nil
}
func (oneUserStore) RevokeSession(_ context.Context, _, family string) error {
	if family != otherFamily {
		return store.ErrNotFound
	}
	return nil
}

// SessionLive: deadFamily is a revoked session, every other one is live.
func (s oneUserStore) SessionLive(_ context.Context, _, family string) (bool, error) {
	s.got.liveChecks.Add(1)
	return family != deadFamily, nil
}
func (oneUserStore) GoogleIdentity(context.Context, string) (model.Identity, bool, error) {
	return model.Identity{}, false, nil
}
func (oneUserStore) UnlinkGoogle(context.Context, string) error { return store.ErrNotFound }
func (s oneUserStore) RevokeOtherSessions(_ context.Context, _, keep string) (int, error) {
	s.got.revokedKeep = keep
	return 1, nil
}

// fakePasswordHash stands for a stored hash: countingPasswords never reads
// it, but an empty one would mean an account without a password.
const fakePasswordHash = "stored-hash"

const (
	testUserID  = "a0000000-0000-4000-8000-000000000001"
	testFamily  = "f0000000-0000-4000-8000-000000000001"
	otherFamily = "f0000000-0000-4000-8000-000000000002"
	newFamily   = "f0000000-0000-4000-8000-000000000003"
	deadFamily  = "f0000000-0000-4000-8000-000000000004"
)

func newTestAuth(t *testing.T, pw Passwords) *Auth {
	t.Helper()
	a, _ := newTestAuthStore(t, pw)
	return a
}

func newTestAuthStore(t *testing.T, pw Passwords) (*Auth, *storeCalls) {
	t.Helper()
	key := make([]byte, 48)
	_, _ = rand.Read(key)
	signer, err := auth.NewSigner(key)
	if err != nil {
		t.Fatal(err)
	}
	st := oneUserStore{user: model.User{ID: testUserID, Username: "alice", PasswordHash: fakePasswordHash, Role: model.RoleUser}, got: &storeCalls{}}
	return NewAuth(st, pw, signer, auth.NewThrottle(), auth.NewThrottle(), slog.New(slog.DiscardHandler)), st.got
}

// An unknown username costs a full (dummy) verification, so timing does not
// reveal which usernames exist, and fails with the same error.
func TestLoginUnknownUserStillHashes(t *testing.T) {
	pw := &countingPasswords{}
	a := newTestAuth(t, pw)
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	password := base64.RawURLEncoding.EncodeToString(b)

	_, errUnknown := a.Login(t.Context(), LoginInput{Username: "nobody", Password: password, ClientIP: "192.0.2.1"})
	if pw.dummy != 1 || pw.verify != 0 {
		t.Fatalf("unknown user: dummy=%d verify=%d, want one dummy verification", pw.dummy, pw.verify)
	}
	_, errWrong := a.Login(t.Context(), LoginInput{Username: "alice", Password: password, ClientIP: "192.0.2.1"})
	if pw.verify != 1 {
		t.Fatalf("wrong password: verify=%d", pw.verify)
	}
	if !errors.Is(errUnknown, ErrInvalidCredentials) || !errors.Is(errWrong, ErrInvalidCredentials) || errUnknown.Error() != errWrong.Error() {
		t.Fatalf("errors differ: %v vs %v", errUnknown, errWrong)
	}

	pw.match = true
	s, err := a.Login(t.Context(), LoginInput{Username: "  ALICE ", Password: password, ClientIP: "192.0.2.1"})
	if err != nil || s.User.Username != "alice" || s.AccessToken == "" || s.RefreshToken == "" {
		t.Fatalf("Login = %+v, %v", s, err)
	}
	// A blocked key is refused before any hashing.
	for range auth.MaxLoginFailures {
		pw.match = false
		_, _ = a.Login(t.Context(), LoginInput{Username: "alice", Password: password, ClientIP: "198.51.100.9"})
	}
	before := pw.verify + pw.dummy
	var throttled *ThrottledError
	if _, err := a.Login(t.Context(), LoginInput{Username: "alice", Password: password, ClientIP: "198.51.100.9"}); !errors.As(err, &throttled) || throttled.RetryAfter <= 0 {
		t.Fatalf("blocked login = %v", err)
	}
	if pw.verify+pw.dummy != before {
		t.Fatal("a blocked login still ran a hash")
	}
}
