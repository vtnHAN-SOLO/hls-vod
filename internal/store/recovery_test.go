package store_test

import (
	"errors"
	"testing"
	"time"

	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
	"github.com/vtnHAN-SOLO/hls-vod/internal/pgtest"
	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
)

func TestRecovery(t *testing.T) {
	db := pgtest.Start(t)
	if _, err := db.Migrator(t).Up(t.Context()); err != nil {
		t.Fatalf("migrating: %v", err)
	}
	st, err := store.New(t.Context(), db.DSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	ctx := t.Context()
	// Not real hashes: the store never inspects them.
	const oldHash = "$argon2id$v=19$m=19456,t=2,p=1$c2FsdHNhbHRzYWx0$b2xkb2xkb2xkb2xkb2xkb2xkb2xkb2xkb2xkb2w"
	const newHash = "$argon2id$v=19$m=19456,t=2,p=1$c2FsdHNhbHRzYWx0$bmV3bmV3bmV3bmV3bmV3bmV3bmV3bmV3bmV3bmU"

	// verified registers and verifies an account; it returns the user and the
	// family of the session the verification started.
	verified := func(t *testing.T, username, email string) (model.User, string) {
		t.Helper()
		h := randomHash(t)
		r, err := st.Register(ctx, store.NewAccount{Username: username, Email: email, PasswordHash: oldHash, VerifyHash: h, VerifyTTL: time.Hour})
		if err != nil || r.Existing != nil {
			t.Fatalf("Register = %+v, %v", r, err)
		}
		u, family, err := st.VerifyEmail(ctx, h, store.NewRefresh{Hash: randomHash(t), TTL: time.Hour})
		if err != nil {
			t.Fatal(err)
		}
		return u, family
	}
	live := func(t *testing.T, userID, family string) bool {
		t.Helper()
		ok, err := st.SessionLive(ctx, userID, family)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}

	admin, err := st.CreateUser(ctx, "root", oldHash, model.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("reset tokens: verified accounts only, only the newest works, single use", func(t *testing.T) {
		u, family := verified(t, "rita", "rita@example.com")
		if _, err := st.Register(ctx, store.NewAccount{Username: "una", Email: "una@example.com", PasswordHash: oldHash,
			VerifyHash: randomHash(t), VerifyTTL: time.Hour}); err != nil {
			t.Fatal(err)
		}
		for email, want := range map[string]store.ResetOutcome{
			"nobody@example.com": store.ResetNoAccount,
			"una@example.com":    store.ResetUnverified,
		} {
			if _, got, err := st.IssueResetToken(ctx, email, randomHash(t), 30*time.Minute); err != nil || got != want {
				t.Errorf("IssueResetToken(%s) = %v, %v; want %v", email, got, err, want)
			}
		}

		first, second := randomHash(t), randomHash(t)
		for _, h := range [][]byte{first, second} {
			got, outcome, err := st.IssueResetToken(ctx, "rita@example.com", h, 30*time.Minute)
			if err != nil || outcome != store.ResetIssued || got.ID != u.ID {
				t.Fatalf("IssueResetToken = %+v, %v, %v", got, outcome, err)
			}
		}
		if ok, err := st.EmailTokenUsable(ctx, first, store.PurposeReset); err != nil || ok {
			t.Fatalf("replaced token usable = %v, %v", ok, err)
		}
		if ok, err := st.EmailTokenUsable(ctx, second, store.PurposeVerify); err != nil || ok {
			t.Fatalf("reset token usable as a verify token = %v, %v", ok, err)
		}
		if _, err := st.ResetPassword(ctx, first, newHash); !errors.Is(err, store.ErrEmailTokenInvalid) {
			t.Fatalf("replaced token = %v", err)
		}
		got, err := st.ResetPassword(ctx, second, newHash)
		if err != nil || got.PasswordHash != newHash {
			t.Fatalf("ResetPassword = %+v, %v", got, err)
		}
		if live(t, u.ID, family) {
			t.Fatal("a session survived the reset")
		}
		if _, err := st.ResetPassword(ctx, second, oldHash); !errors.Is(err, store.ErrEmailTokenInvalid) {
			t.Fatalf("reused token = %v", err)
		}

		expired := randomHash(t)
		if _, _, err := st.IssueResetToken(ctx, "rita@example.com", expired, time.Millisecond); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
		if _, err := st.ResetPassword(ctx, expired, oldHash); !errors.Is(err, store.ErrEmailTokenInvalid) {
			t.Fatalf("expired token = %v", err)
		}
		if _, err := st.ResetPassword(ctx, randomHash(t), oldHash); !errors.Is(err, store.ErrEmailTokenInvalid) {
			t.Fatalf("unknown token = %v", err)
		}
	})

	t.Run("a useradmin account has no email, so no reset by mail", func(t *testing.T) {
		if admin.Email != nil {
			t.Fatalf("useradmin account email = %v", *admin.Email)
		}
		if _, got, err := st.IssueResetToken(ctx, "root@example.com", randomHash(t), time.Hour); err != nil || got != store.ResetNoAccount {
			t.Fatalf("IssueResetToken = %v, %v", got, err)
		}
	})
}
