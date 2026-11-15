package store_test

import (
	"errors"
	"testing"
	"time"

	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
	"github.com/vtnHAN-SOLO/hls-vod/internal/pgtest"
	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
)

func TestRegistration(t *testing.T) {
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
	const fakeHash = "$argon2id$v=19$m=19456,t=2,p=1$c2FsdHNhbHRzYWx0$a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2U"
	const ttl = 24 * time.Hour

	register := func(t *testing.T, username, email string) (store.Registration, []byte, error) {
		t.Helper()
		h := randomHash(t)
		r, err := st.Register(ctx, store.NewAccount{Username: username, Email: email, PasswordHash: fakeHash, VerifyHash: h, VerifyTTL: ttl})
		return r, h, err
	}
	mustCreate := func(t *testing.T, username, email string) (model.User, []byte) {
		t.Helper()
		r, h, err := register(t, username, email)
		if err != nil || r.Existing != nil || !model.ValidID(r.User.ID) {
			t.Fatalf("Register(%s, %s) = %+v, %v; want a new user", username, email, r, err)
		}
		return r.User, h
	}
	exists := func(t *testing.T, id string) bool {
		t.Helper()
		var n int
		if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE id = $1`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n == 1
	}
	expireTokens := func(t *testing.T, userID string) {
		t.Helper()
		if _, err := db.Pool.Exec(ctx, `UPDATE email_tokens SET expires_at = now() - interval '1 second' WHERE user_id = $1`, userID); err != nil {
			t.Fatal(err)
		}
	}
	verify := func(t *testing.T, h []byte) (model.User, string, error) {
		t.Helper()
		return st.VerifyEmail(ctx, h, store.NewRefresh{Hash: randomHash(t), TTL: time.Hour, UserAgent: firefoxUA})
	}

	// An account from cmd/useradmin: no email, and never touched by any of this.
	admin, err := st.CreateUser(ctx, "root", fakeHash, model.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("a new account is an unverified user with one verify token", func(t *testing.T) {
		u, h := mustCreate(t, "dana", "dana@example.com")
		if u.Role != model.RoleUser || u.Email == nil || *u.Email != "dana@example.com" || !u.Unverified() {
			t.Fatalf("user = %+v", u)
		}
		var purpose string
		var stored []byte
		var expires time.Time
		if err := db.Pool.QueryRow(ctx, `SELECT purpose, token_hash, expires_at FROM email_tokens WHERE user_id = $1`, u.ID).
			Scan(&purpose, &stored, &expires); err != nil {
			t.Fatal(err)
		}
		if purpose != "verify" || string(stored) != string(h) || time.Until(expires) < ttl-time.Minute {
			t.Fatalf("token = %s, %x, expires %v", purpose, stored, expires)
		}
		if a, _ := st.UserByUsername(ctx, "root"); a.Email != nil || a.Unverified() {
			t.Fatalf("useradmin account = %+v; want no email and not unverified", a)
		}
	})

	t.Run("a taken username is ErrUserExists, whatever the email", func(t *testing.T) {
		for _, email := range []string{"new@example.com", "dana@example.com"} {
			if _, _, err := register(t, "dana", email); !errors.Is(err, store.ErrUserExists) {
				t.Errorf("username dana, email %s = %v; want ErrUserExists", email, err)
			}
			if _, _, err := register(t, "root", email); !errors.Is(err, store.ErrUserExists) {
				t.Errorf("useradmin username, email %s = %v; want ErrUserExists", email, err)
			}
		}
	})

	t.Run("a taken email returns its account and creates nothing", func(t *testing.T) {
		var before int
		_ = db.Pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&before)
		r, _, err := register(t, "eve", "dana@example.com")
		if err != nil || r.Existing == nil || r.Existing.Username != "dana" || r.User.ID != "" {
			t.Fatalf("Register = %+v, %v; want Existing = dana", r, err)
		}
		var after int
		_ = db.Pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&after)
		if after != before {
			t.Fatalf("users %d → %d", before, after)
		}
	})

	t.Run("verify: marks the email verified, starts a session, single use", func(t *testing.T) {
		u, h := mustCreate(t, "fred", "fred@example.com")
		got, family, err := verify(t, h)
		if err != nil || got.ID != u.ID || got.Unverified() || got.EmailVerifiedAt == nil {
			t.Fatalf("VerifyEmail = %+v, %q, %v", got, family, err)
		}
		if live, err := st.SessionLive(ctx, u.ID, family); err != nil || !live {
			t.Fatalf("the new session is not live: %v, %v", live, err)
		}
		if _, _, err := verify(t, h); !errors.Is(err, store.ErrEmailTokenInvalid) {
			t.Fatalf("second use = %v; want ErrEmailTokenInvalid", err)
		}
		if _, _, err := verify(t, randomHash(t)); !errors.Is(err, store.ErrEmailTokenInvalid) {
			t.Fatalf("unknown token = %v; want ErrEmailTokenInvalid", err)
		}
		// A verified account keeps its address: a new registration gets Existing.
		if r, _, err := register(t, "fred2", "fred@example.com"); err != nil || r.Existing == nil || r.Existing.ID != u.ID {
			t.Fatalf("registering a verified address = %+v, %v", r, err)
		}
	})

	t.Run("an expired verify token cannot be used", func(t *testing.T) {
		u, h := mustCreate(t, "gina", "gina@example.com")
		expireTokens(t, u.ID)
		if _, _, err := verify(t, h); !errors.Is(err, store.ErrEmailTokenInvalid) {
			t.Fatalf("expired token = %v; want ErrEmailTokenInvalid", err)
		}
		if again, _ := st.UserByID(ctx, u.ID); !again.Unverified() {
			t.Fatal("an expired token verified the account")
		}
	})

	t.Run("squatting: an expired unverified account is deleted by a registration that wants its email or username", func(t *testing.T) {
		byEmail, _ := mustCreate(t, "squat1", "victim@example.com")
		expireTokens(t, byEmail.ID)
		owner, _ := mustCreate(t, "owner", "victim@example.com")
		if exists(t, byEmail.ID) || !exists(t, owner.ID) {
			t.Fatal("the squatter holding the email was not replaced")
		}

		byName, _ := mustCreate(t, "wanted", "squat2@example.com")
		expireTokens(t, byName.ID)
		if _, _ = mustCreate(t, "wanted", "real@example.com"); exists(t, byName.ID) {
			t.Fatal("the squatter holding the username was not deleted")
		}

		// Still inside its window: kept, so the address answers Existing
		// and the username ErrUserExists.
		live, _ := mustCreate(t, "pending", "pending@example.com")
		if r, _, err := register(t, "other", "pending@example.com"); err != nil || r.Existing == nil || r.Existing.ID != live.ID {
			t.Fatalf("live unverified account by email = %+v, %v", r, err)
		}
		if _, _, err := register(t, "pending", "other@example.com"); !errors.Is(err, store.ErrUserExists) {
			t.Fatalf("live unverified account by username = %v", err)
		}

		// Verified and useradmin accounts are never deleted, expired tokens or not.
		verified, h := mustCreate(t, "kept", "kept@example.com")
		if _, _, err := verify(t, h); err != nil {
			t.Fatal(err)
		}
		expireTokens(t, verified.ID)
		if _, _, err := register(t, "kept", "x@example.com"); !errors.Is(err, store.ErrUserExists) || !exists(t, verified.ID) {
			t.Fatalf("verified account: %v, exists %v", err, exists(t, verified.ID))
		}
		if _, _, err := register(t, "root", "y@example.com"); !errors.Is(err, store.ErrUserExists) || !exists(t, admin.ID) {
			t.Fatalf("useradmin account: %v", err)
		}
	})

	t.Run("resend: a new token replaces the old one but keeps the account's deadline", func(t *testing.T) {
		u, old := mustCreate(t, "hana", "hana@example.com")
		h := randomHash(t)
		got, outcome, err := st.ResendVerification(ctx, "hana@example.com", h, ttl)
		if err != nil || outcome != store.ResendIssued || got.ID != u.ID {
			t.Fatalf("ResendVerification = %+v, %v, %v", got, outcome, err)
		}
		var expires time.Time
		if err := db.Pool.QueryRow(ctx, `SELECT expires_at FROM email_tokens WHERE token_hash = $1`, h).Scan(&expires); err != nil {
			t.Fatal(err)
		}
		if want := u.CreatedAt.Add(ttl); !expires.Equal(want) {
			t.Fatalf("new token expires %v, want the account's deadline %v", expires, want)
		}
		if _, _, err := verify(t, old); !errors.Is(err, store.ErrEmailTokenInvalid) {
			t.Fatalf("the replaced token = %v; want ErrEmailTokenInvalid", err)
		}
		if _, _, err := verify(t, h); err != nil {
			t.Fatalf("the new token: %v", err)
		}

		for _, tt := range []struct {
			name, email string
			want        store.ResendOutcome
		}{
			{"unknown address", "nobody@example.com", store.ResendNoAccount},
			{"verified", "hana@example.com", store.ResendAlreadyVerified},
		} {
			if _, outcome, err := st.ResendVerification(ctx, tt.email, randomHash(t), ttl); err != nil || outcome != tt.want {
				t.Errorf("%s: %v, %v; want %v", tt.name, outcome, err, tt.want)
			}
		}

		late, _ := mustCreate(t, "ivan", "ivan@example.com")
		if _, err := db.Pool.Exec(ctx, `UPDATE users SET created_at = now() - interval '25 hours' WHERE id = $1`, late.ID); err != nil {
			t.Fatal(err)
		}
		if _, outcome, err := st.ResendVerification(ctx, "ivan@example.com", randomHash(t), ttl); err != nil || outcome != store.ResendExpired {
			t.Fatalf("past the window = %v, %v; want ResendExpired", outcome, err)
		}
		// Nothing live is left, so the next registration replaces it.
		if r, _, err := register(t, "ivan", "ivan@example.com"); err != nil || r.Existing != nil || exists(t, late.ID) {
			t.Fatalf("re-registering after the window = %+v, %v", r, err)
		}
	})

	t.Run("ReserveMail: 3 per address per hour, then the daily cap", func(t *testing.T) {
		a, b := randomHash(t), randomHash(t)
		reserve := func(to []byte, perHour, capacity int) store.MailVerdict {
			t.Helper()
			v, err := st.ReserveMail(ctx, to, "verify", perHour, capacity)
			if err != nil {
				t.Fatal(err)
			}
			return v
		}
		for i := range 3 {
			if v := reserve(a, 3, 90); v != store.MailAllowed {
				t.Fatalf("mail %d to a = %v", i+1, v)
			}
		}
		if v := reserve(a, 3, 90); v != store.MailAddressLimit {
			t.Fatalf("4th mail to a = %v; want the per-address limit", v)
		}
		if v := reserve(b, 3, 90); v != store.MailAllowed {
			t.Fatalf("another address = %v", v)
		}
		// Four recorded so far today (the refused one is not recorded).
		if v := reserve(randomHash(t), 3, 4); v != store.MailDailyCap {
			t.Fatalf("over the cap = %v; want MailDailyCap", v)
		}
		// An hour later the address may mail again; the day still counts it.
		if _, err := db.Pool.Exec(ctx, `UPDATE outgoing_mail SET created_at = created_at - interval '61 minutes' WHERE to_hash = $1`, a); err != nil {
			t.Fatal(err)
		}
		if v := reserve(a, 3, 90); v != store.MailAllowed {
			t.Fatalf("after an hour = %v", v)
		}
		if v := reserve(randomHash(t), 3, 5); v != store.MailDailyCap {
			t.Fatalf("the day counts mails older than an hour: %v", v)
		}
		// Older than a day: out of the cap, and pruned after two.
		if _, err := db.Pool.Exec(ctx, `UPDATE outgoing_mail SET created_at = now() - interval '3 days'`); err != nil {
			t.Fatal(err)
		}
		if v := reserve(a, 3, 1); v != store.MailAllowed {
			t.Fatalf("after three days = %v", v)
		}
		var n int
		_ = db.Pool.QueryRow(ctx, `SELECT count(*) FROM outgoing_mail`).Scan(&n)
		if n != 1 {
			t.Fatalf("rows left = %d; want old rows pruned", n)
		}
	})
}
