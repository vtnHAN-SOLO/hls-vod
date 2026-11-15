package store_test

import (
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
	"github.com/vtnHAN-SOLO/hls-vod/internal/pgtest"
	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
)

func randomHash(t *testing.T) []byte {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestUsersAndRefreshTokens(t *testing.T) {
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
	// Not a real hash: the store never inspects it.
	const hash = "$argon2id$v=19$m=19456,t=2,p=1$c2FsdHNhbHRzYWx0$a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2U"

	alice, err := st.CreateUser(ctx, "alice", hash, model.RoleAdmin)
	if err != nil || alice.ID == "" || alice.Role != model.RoleAdmin {
		t.Fatalf("CreateUser = %+v, %v", alice, err)
	}
	bob, err := st.CreateUser(ctx, "bob", hash, model.RoleUser)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("users", func(t *testing.T) {
		if _, err := st.CreateUser(ctx, "alice", hash, model.RoleUser); !errors.Is(err, store.ErrUserExists) {
			t.Fatalf("duplicate = %v, want ErrUserExists", err)
		}
		// The database itself refuses uppercase, not only application code.
		if _, err := st.CreateUser(ctx, "Carol", hash, model.RoleUser); err == nil || errors.Is(err, store.ErrUserExists) {
			t.Fatalf("uppercase username = %v, want the CHECK constraint", err)
		}
		if u, err := st.UserByUsername(ctx, "alice"); err != nil || u.ID != alice.ID || u.PasswordHash != hash {
			t.Fatalf("UserByUsername = %+v, %v", u, err)
		}
		if u, err := st.UserByID(ctx, bob.ID); err != nil || u.Username != "bob" {
			t.Fatalf("UserByID = %+v, %v", u, err)
		}
		if _, err := st.UserByUsername(ctx, "nobody"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("unknown user = %v, want ErrNotFound", err)
		}
		if err := st.SetRole(ctx, "bob", model.RoleAdmin); err != nil {
			t.Fatal(err)
		}
		if u, _ := st.UserByUsername(ctx, "bob"); u.Role != model.RoleAdmin {
			t.Fatalf("role = %s, want admin", u.Role)
		}
		if err := st.SetRole(ctx, "bob", model.RoleUser); err != nil {
			t.Fatal(err)
		}
		if err := st.SetRole(ctx, "nobody", model.RoleUser); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("SetRole(unknown) = %v", err)
		}
		if err := st.SetPasswordHash(ctx, "nobody", hash); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("SetPasswordHash(unknown) = %v", err)
		}
		users, err := st.ListUsers(ctx)
		if err != nil || len(users) != 2 || users[0].Username != "alice" || users[1].Username != "bob" {
			t.Fatalf("ListUsers = %+v, %v", users, err)
		}
	})

	t.Run("rotation, then reuse of the old token kills the whole family", func(t *testing.T) {
		t1 := randomHash(t)
		if _, err := st.CreateRefreshToken(ctx, alice.ID, store.NewRefresh{Hash: t1, TTL: time.Hour}); err != nil {
			t.Fatal(err)
		}
		t2 := randomHash(t)
		uid, _, err := st.RotateRefreshToken(ctx, t1, store.NewRefresh{Hash: t2, TTL: time.Hour})
		if err != nil || uid != alice.ID {
			t.Fatalf("rotate = %q, %v", uid, err)
		}
		var replacedBy *string
		var revoked bool
		if err := db.Pool.QueryRow(ctx, `SELECT revoked_at IS NOT NULL, replaced_by::text FROM refresh_tokens WHERE token_hash = $1`, t1).
			Scan(&revoked, &replacedBy); err != nil || !revoked || replacedBy == nil {
			t.Fatalf("old token: revoked=%v replaced_by=%v, %v", revoked, replacedBy, err)
		}

		// The old token again: reuse. Both are dead now, and that is committed.
		if _, _, err := st.RotateRefreshToken(ctx, t1, store.NewRefresh{Hash: randomHash(t), TTL: time.Hour}); !errors.Is(err, store.ErrRefreshReused) {
			t.Fatalf("reuse = %v, want ErrRefreshReused", err)
		}
		if _, _, err := st.RotateRefreshToken(ctx, t2, store.NewRefresh{Hash: randomHash(t), TTL: time.Hour}); !errors.Is(err, store.ErrRefreshReused) {
			t.Fatalf("the newer token after reuse = %v, want it revoked too", err)
		}
		var live int
		if err := db.Pool.QueryRow(ctx, `
			SELECT count(*) FROM refresh_tokens WHERE family_id = (SELECT family_id FROM refresh_tokens WHERE token_hash = $1)
			AND revoked_at IS NULL`, t1).Scan(&live); err != nil || live != 0 {
			t.Fatalf("%d live tokens left in the family, %v", live, err)
		}
	})

	t.Run("reuse revokes only that family", func(t *testing.T) {
		a1, b1 := randomHash(t), randomHash(t)
		for _, h := range [][]byte{a1, b1} {
			if _, err := st.CreateRefreshToken(ctx, alice.ID, store.NewRefresh{Hash: h, TTL: time.Hour}); err != nil {
				t.Fatal(err)
			}
		}
		if _, _, err := st.RotateRefreshToken(ctx, a1, store.NewRefresh{Hash: randomHash(t), TTL: time.Hour}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := st.RotateRefreshToken(ctx, a1, store.NewRefresh{Hash: randomHash(t), TTL: time.Hour}); !errors.Is(err, store.ErrRefreshReused) {
			t.Fatal(err)
		}
		b2 := randomHash(t)
		if _, _, err := st.RotateRefreshToken(ctx, b1, store.NewRefresh{Hash: b2, TTL: time.Hour}); err != nil {
			t.Fatalf("another login of the same user was revoked: %v", err)
		}
	})

	t.Run("unknown and expired tokens", func(t *testing.T) {
		if _, _, err := st.RotateRefreshToken(ctx, randomHash(t), store.NewRefresh{Hash: randomHash(t), TTL: time.Hour}); !errors.Is(err, store.ErrRefreshInvalid) {
			t.Fatalf("unknown = %v", err)
		}
		old := randomHash(t)
		if _, err := st.CreateRefreshToken(ctx, bob.ID, store.NewRefresh{Hash: old, TTL: time.Hour}); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Pool.Exec(ctx, `UPDATE refresh_tokens SET expires_at = now() - interval '1 second' WHERE token_hash = $1`, old); err != nil {
			t.Fatal(err)
		}
		if _, _, err := st.RotateRefreshToken(ctx, old, store.NewRefresh{Hash: randomHash(t), TTL: time.Hour}); !errors.Is(err, store.ErrRefreshInvalid) {
			t.Fatalf("expired = %v, want ErrRefreshInvalid", err)
		}
	})

	t.Run("logout revokes the family; a second logout is fine", func(t *testing.T) {
		t1, t2 := randomHash(t), randomHash(t)
		if _, err := st.CreateRefreshToken(ctx, bob.ID, store.NewRefresh{Hash: t1, TTL: time.Hour}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := st.RotateRefreshToken(ctx, t1, store.NewRefresh{Hash: t2, TTL: time.Hour}); err != nil {
			t.Fatal(err)
		}
		if err := st.RevokeRefreshFamily(ctx, t2); err != nil {
			t.Fatal(err)
		}
		if _, _, err := st.RotateRefreshToken(ctx, t2, store.NewRefresh{Hash: randomHash(t), TTL: time.Hour}); err == nil {
			t.Fatal("token still works after logout")
		}
		if err := st.RevokeRefreshFamily(ctx, randomHash(t)); err != nil {
			t.Fatalf("logout with an unknown token = %v", err)
		}
	})

	t.Run("a password change ends every session of that user", func(t *testing.T) {
		mine, theirs := randomHash(t), randomHash(t)
		if _, err := st.CreateRefreshToken(ctx, bob.ID, store.NewRefresh{Hash: mine, TTL: time.Hour}); err != nil {
			t.Fatal(err)
		}
		if _, err := st.CreateRefreshToken(ctx, alice.ID, store.NewRefresh{Hash: theirs, TTL: time.Hour}); err != nil {
			t.Fatal(err)
		}
		if err := st.SetPasswordHash(ctx, "bob", hash); err != nil {
			t.Fatal(err)
		}
		if _, _, err := st.RotateRefreshToken(ctx, mine, store.NewRefresh{Hash: randomHash(t), TTL: time.Hour}); err == nil {
			t.Fatal("bob's session survived the password change")
		}
		if _, _, err := st.RotateRefreshToken(ctx, theirs, store.NewRefresh{Hash: randomHash(t), TTL: time.Hour}); err != nil {
			t.Fatalf("alice's session was revoked by bob's password change: %v", err)
		}
	})
}
