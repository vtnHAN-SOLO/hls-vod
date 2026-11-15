package store_test

import (
	"errors"
	"testing"
	"time"

	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
	"github.com/vtnHAN-SOLO/hls-vod/internal/pgtest"
	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
)

func TestGoogle(t *testing.T) {
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
	const fakeHash = "$argon2id$v=19$m=19456,t=2,p=1$c2FsdHNhbHRzYWx0$Z29vZ2xlZ29vZ2xlZ29vZ2xlZ29vZ2xlZ29vZ2w"

	names := func(ns ...string) func() string {
		i := 0
		return func() string {
			n := ns[min(i, len(ns)-1)]
			i++
			return n
		}
	}
	signIn := func(t *testing.T, sub, email string, allow bool, next func() string) (store.GoogleResult, error) {
		t.Helper()
		return st.GoogleSignIn(ctx, store.GoogleSignIn{Subject: sub, Email: email, AllowCreate: allow, NextUsername: next,
			Refresh: store.NewRefresh{Hash: randomHash(t), TTL: time.Hour}})
	}
	verifiedUser := func(t *testing.T, username, email string) model.User {
		t.Helper()
		h := randomHash(t)
		if _, err := st.Register(ctx, store.NewAccount{Username: username, Email: email, PasswordHash: fakeHash, VerifyHash: h, VerifyTTL: time.Hour}); err != nil {
			t.Fatal(err)
		}
		u, _, err := st.VerifyEmail(ctx, h, store.NewRefresh{Hash: randomHash(t), TTL: time.Hour})
		if err != nil {
			t.Fatal(err)
		}
		return u
	}

	t.Run("d then a: a new Google-only account, then the same one again", func(t *testing.T) {
		r, err := signIn(t, "sub-new", "gina@example.com", true, names("gina-ab12"))
		if err != nil || r.Outcome != store.GoogleCreated || r.User.Username != "gina-ab12" || r.User.HasPassword() ||
			r.User.EmailVerifiedAt == nil || r.User.Role != model.RoleUser || !model.ValidID(r.Family) {
			t.Fatalf("first sign-in = %+v, %v", r, err)
		}
		again, err := signIn(t, "sub-new", "gina.changed@example.com", true, names("never"))
		if err != nil || again.Outcome != store.GoogleKnown || again.User.ID != r.User.ID || *again.User.Email != "gina@example.com" {
			t.Fatalf("second sign-in = %+v, %v (the local email must stay as it was)", again, err)
		}
		id, ok, err := st.GoogleIdentity(ctx, r.User.ID)
		if err != nil || !ok || id.Subject != "sub-new" || id.EmailAtLink == nil || *id.EmailAtLink != "gina@example.com" {
			t.Fatalf("identity = %+v, %v, %v", id, ok, err)
		}
	})

	t.Run("b: a verified account with the email is linked", func(t *testing.T) {
		u := verifiedUser(t, "tess", "tess@example.com")
		r, err := signIn(t, "sub-tess", "tess@example.com", false, names("never"))
		if err != nil || r.Outcome != store.GoogleLinkedByEmail || r.User.ID != u.ID {
			t.Fatalf("sign-in = %+v, %v", r, err)
		}
		if again, err := signIn(t, "sub-other", "tess@example.com", true, names("never")); !errors.Is(err, store.ErrGoogleAlreadyLinked) {
			t.Fatalf("a second Google account with the same email = %+v, %v", again, err)
		}
	})

	t.Run("c: an unverified account with the email is deleted, never linked", func(t *testing.T) {
		r0, err := st.Register(ctx, store.NewAccount{Username: "squat", Email: "uma@example.com", PasswordHash: fakeHash,
			VerifyHash: randomHash(t), VerifyTTL: time.Hour})
		if err != nil {
			t.Fatal(err)
		}
		r, err := signIn(t, "sub-uma", "uma@example.com", true, names("uma-cd34"))
		if err != nil || r.Outcome != store.GoogleCreated || r.User.ID == r0.User.ID || !r.DeletedUnverified || r.User.HasPassword() {
			t.Fatalf("sign-in = %+v, %v", r, err)
		}
		if _, err := st.UserByUsername(ctx, "squat"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("the unverified account = %v; want deleted", err)
		}
	})

	t.Run("registration closed blocks d only", func(t *testing.T) {
		if _, err := signIn(t, "sub-closed", "nobody@example.com", false, names("x")); !errors.Is(err, store.ErrSignupClosed) {
			t.Fatalf("closed = %v", err)
		}
		r0, err := st.Register(ctx, store.NewAccount{Username: "kept", Email: "kept@example.com", PasswordHash: fakeHash,
			VerifyHash: randomHash(t), VerifyTTL: time.Hour})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := signIn(t, "sub-kept", "kept@example.com", false, names("x")); !errors.Is(err, store.ErrSignupClosed) {
			t.Fatalf("closed, unverified holder = %v", err)
		}
		if _, err := st.UserByID(ctx, r0.User.ID); err != nil {
			t.Fatalf("a refused sign-in deleted the unverified account: %v", err)
		}
		if r, err := signIn(t, "sub-new", "x@example.com", false, names("x")); err != nil || r.Outcome != store.GoogleKnown {
			t.Fatalf("a while closed = %+v, %v", r, err)
		}
	})

	t.Run("a username collision takes the next proposal", func(t *testing.T) {
		r, err := signIn(t, "sub-collide", "collide@example.com", true, names("tess", "gina-ab12", "collide-ef56"))
		if err != nil || r.User.Username != "collide-ef56" {
			t.Fatalf("sign-in = %+v, %v", r, err)
		}
	})

	t.Run("link and unlink", func(t *testing.T) {
		u := verifiedUser(t, "lars", "lars@example.com")
		if err := st.LinkGoogle(ctx, u.ID, "sub-new", "x@example.com"); !errors.Is(err, store.ErrGoogleTaken) {
			t.Fatalf("a subject linked elsewhere = %v", err)
		}
		if err := st.LinkGoogle(ctx, u.ID, "sub-lars", "lars@gmail.example"); err != nil {
			t.Fatal(err)
		}
		if err := st.LinkGoogle(ctx, u.ID, "sub-lars-2", "lars2@gmail.example"); !errors.Is(err, store.ErrGoogleAlreadyLinked) {
			t.Fatalf("a second Google account = %v", err)
		}
		if err := st.UnlinkGoogle(ctx, u.ID); err != nil {
			t.Fatal(err)
		}
		if err := st.UnlinkGoogle(ctx, u.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("unlink twice = %v", err)
		}
		gina, err := st.UserByUsername(ctx, "gina-ab12")
		if err != nil {
			t.Fatal(err)
		}
		if err := st.UnlinkGoogle(ctx, gina.ID); !errors.Is(err, store.ErrLastLoginMethod) {
			t.Fatalf("unlink for a Google-only account = %v", err)
		}
	})

	t.Run("a reset link gives a Google-only account a password", func(t *testing.T) {
		gina, err := st.UserByUsername(ctx, "gina-ab12")
		if err != nil {
			t.Fatal(err)
		}
		h := randomHash(t)
		if err := st.IssueResetTokenForUser(ctx, gina.ID, h, time.Hour); err != nil {
			t.Fatal(err)
		}
		u, err := st.ResetPassword(ctx, h, fakeHash)
		if err != nil || !u.HasPassword() {
			t.Fatalf("ResetPassword = %+v, %v", u, err)
		}
		if err := st.UnlinkGoogle(ctx, gina.ID); err != nil {
			t.Fatalf("unlink once it has a password = %v", err)
		}
	})

	t.Run("states are single use and expire", func(t *testing.T) {
		live, expired := randomHash(t), randomHash(t)
		if err := st.CreateOAuthState(ctx, live, store.OAuthState{Nonce: "n", Verifier: "v", Intent: store.IntentLogin}, time.Minute); err != nil {
			t.Fatal(err)
		}
		if err := st.CreateOAuthState(ctx, expired, store.OAuthState{Nonce: "n", Verifier: "v", Intent: store.IntentLogin}, time.Millisecond); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
		got, err := st.ConsumeOAuthState(ctx, live)
		if err != nil || got.Nonce != "n" || got.Verifier != "v" || got.Intent != store.IntentLogin || got.UserID != nil {
			t.Fatalf("consume = %+v, %v", got, err)
		}
		for name, h := range map[string][]byte{"reused": live, "expired": expired, "unknown": randomHash(t)} {
			if _, err := st.ConsumeOAuthState(ctx, h); !errors.Is(err, store.ErrOAuthStateInvalid) {
				t.Errorf("%s = %v", name, err)
			}
		}
		// The next start deletes expired rows.
		old := randomHash(t)
		if err := st.CreateOAuthState(ctx, old, store.OAuthState{Nonce: "n", Verifier: "v", Intent: store.IntentLogin}, time.Millisecond); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
		lars, err := st.UserByUsername(ctx, "lars")
		if err != nil {
			t.Fatal(err)
		}
		if err := st.CreateOAuthState(ctx, randomHash(t), store.OAuthState{Nonce: "n", Verifier: "v", Intent: store.IntentLink, UserID: &lars.ID}, time.Minute); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM oauth_states WHERE state_hash = $1`, old).Scan(&n); err != nil || n != 0 {
			t.Fatalf("expired state still there (%d, %v)", n, err)
		}
	})

	t.Run("a Google-only account has no password to check", func(t *testing.T) {
		r, err := signIn(t, "sub-nopw", "nopw@example.com", true, names("nopw-1234"))
		if err != nil {
			t.Fatal(err)
		}
		u, err := st.UserByUsername(ctx, r.User.Username)
		if err != nil || u.HasPassword() || u.PasswordHash != "" {
			t.Fatalf("user = %+v, %v", u, err)
		}
	})
}
