package store_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
	"github.com/vtnHAN-SOLO/hls-vod/internal/pgtest"
	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
)

const firefoxUA = "Mozilla/5.0 (X11; Linux x86_64; rv:140.0) Gecko/20100101 Firefox/140.0"

func TestSessions(t *testing.T) {
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
	const oldHash = "$argon2id$v=19$m=19456,t=2,p=1$c2FsdHNhbHRzYWx0$a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2U"
	const newHash = "$argon2id$v=19$m=19456,t=2,p=1$dGxhc3RsYXN0bGFz$eWVreWVreWVreWVreWVreWVreWVreWVreWVreWU"

	alice, err := st.CreateUser(ctx, "alice", oldHash, model.RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := st.CreateUser(ctx, "bob", oldHash, model.RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	login := func(t *testing.T, userID, ua string) (family string, hash []byte) {
		t.Helper()
		hash = randomHash(t)
		family, err := st.CreateRefreshToken(ctx, userID, store.NewRefresh{Hash: hash, TTL: time.Hour, UserAgent: ua})
		if err != nil || !model.ValidID(family) {
			t.Fatalf("CreateRefreshToken = %q, %v", family, err)
		}
		return family, hash
	}
	rotate := func(t *testing.T, hash []byte) ([]byte, error) {
		t.Helper()
		next := randomHash(t)
		_, _, err := st.RotateRefreshToken(ctx, hash, store.NewRefresh{Hash: next, TTL: time.Hour})
		return next, err
	}
	ids := func(t *testing.T, userID string) []string {
		t.Helper()
		ss, err := st.ListSessions(ctx, userID)
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, 0, len(ss))
		for _, s := range ss {
			out = append(out, s.ID)
		}
		return out
	}

	// Alice: one login in Firefox, refreshed twice; one with no User-Agent.
	firefox, h := login(t, alice.ID, firefoxUA)
	h, err = rotate(t, h)
	if err != nil {
		t.Fatal(err)
	}
	if userID, family, err := st.RotateRefreshToken(ctx, h, store.NewRefresh{Hash: randomHash(t), TTL: time.Hour}); err != nil ||
		userID != alice.ID || family != firefox {
		t.Fatalf("rotate = %q, %q, %v; want alice and the login's family", userID, family, err)
	}
	bare, bareHash := login(t, alice.ID, "")
	bobs, bobHash := login(t, bob.ID, firefoxUA)

	t.Run("one entry per live family, newest use first, user agent copied on rotation", func(t *testing.T) {
		ss, err := st.ListSessions(ctx, alice.ID)
		if err != nil || len(ss) != 2 {
			t.Fatalf("ListSessions = %+v, %v; want 2", ss, err)
		}
		if ss[0].ID != bare || ss[1].ID != firefox {
			t.Fatalf("order = %v, want the newest login first", ids(t, alice.ID))
		}
		if ss[0].UserAgent != nil {
			t.Errorf("no User-Agent at login: got %q, want NULL", *ss[0].UserAgent)
		}
		ff := ss[1]
		if ff.UserAgent == nil || *ff.UserAgent != firefoxUA {
			t.Fatalf("user agent = %v; rotation must copy it from the login", ff.UserAgent)
		}
		var first, last time.Time
		if err := db.Pool.QueryRow(ctx, `SELECT min(created_at), max(created_at) FROM refresh_tokens WHERE family_id = $1`,
			firefox).Scan(&first, &last); err != nil {
			t.Fatal(err)
		}
		if !ff.CreatedAt.Equal(first) || !ff.LastUsedAt.Equal(last) || !ff.LastUsedAt.After(ff.CreatedAt) {
			t.Fatalf("created %v / last used %v, want the oldest %v and newest %v row", ff.CreatedAt, ff.LastUsedAt, first, last)
		}
	})

	t.Run("revoked and expired families are not sessions", func(t *testing.T) {
		_, gone := login(t, alice.ID, firefoxUA)
		if err := st.RevokeRefreshFamily(ctx, gone); err != nil {
			t.Fatal(err)
		}
		expired, _ := login(t, alice.ID, firefoxUA)
		if _, err := db.Pool.Exec(ctx, `UPDATE refresh_tokens SET expires_at = now() - interval '1 second' WHERE family_id = $1`,
			expired); err != nil {
			t.Fatal(err)
		}
		if got := ids(t, alice.ID); !slices.Equal(got, []string{bare, firefox}) {
			t.Fatalf("sessions = %v, want only the two live ones", got)
		}
		if got := ids(t, bob.ID); !slices.Equal(got, []string{bobs}) {
			t.Fatalf("bob's sessions = %v", got)
		}
	})

	t.Run("a user agent over 200 characters is refused by the database", func(t *testing.T) {
		if _, err := st.CreateRefreshToken(ctx, alice.ID,
			store.NewRefresh{Hash: randomHash(t), TTL: time.Hour, UserAgent: strings.Repeat("x", 201)}); err == nil {
			t.Fatal("a 201-character user agent was stored")
		}
	})

	t.Run("revoking one session: only the caller's own, only live ones", func(t *testing.T) {
		if err := st.RevokeSession(ctx, alice.ID, bobs); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("revoking bob's session as alice = %v, want ErrNotFound", err)
		}
		if _, err := rotate(t, bobHash); err != nil {
			t.Fatalf("bob's session died: %v", err)
		}
		if err := st.RevokeSession(ctx, alice.ID, model.NewID()); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("unknown id = %v, want ErrNotFound", err)
		}
		if err := st.RevokeSession(ctx, alice.ID, bare); err != nil {
			t.Fatal(err)
		}
		if _, err := rotate(t, bareHash); err == nil {
			t.Fatal("the revoked session can still refresh")
		}
		if err := st.RevokeSession(ctx, alice.ID, bare); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("revoking it again = %v, want ErrNotFound", err)
		}
	})

	t.Run("revoke-others keeps the caller's own session", func(t *testing.T) {
		mine, myHash := login(t, alice.ID, firefoxUA)
		login(t, alice.ID, "")
		login(t, alice.ID, "")
		n, err := st.RevokeOtherSessions(ctx, alice.ID, mine)
		if err != nil || n != 3 { // the two new ones and the Firefox login from the start
			t.Fatalf("RevokeOtherSessions = %d, %v; want 3", n, err)
		}
		if got := ids(t, alice.ID); !slices.Equal(got, []string{mine}) {
			t.Fatalf("sessions = %v, want only %s", got, mine)
		}
		if _, err := rotate(t, myHash); err != nil {
			t.Fatalf("the caller's session died: %v", err)
		}
		if got := ids(t, bob.ID); !slices.Equal(got, []string{bobs}) {
			t.Fatalf("bob's sessions = %v; another user's revoke-others must not touch them", got)
		}
	})

	t.Run("a password change ends every session and starts the caller's new one", func(t *testing.T) {
		_, before := login(t, alice.ID, "")
		family, err := st.ChangePassword(ctx, alice.ID, newHash,
			store.NewRefresh{Hash: randomHash(t), TTL: time.Hour, UserAgent: firefoxUA})
		if err != nil || !model.ValidID(family) {
			t.Fatalf("ChangePassword = %q, %v", family, err)
		}
		if u, _ := st.UserByID(ctx, alice.ID); u.PasswordHash != newHash {
			t.Fatal("the password hash did not change")
		}
		if _, err := rotate(t, before); err == nil {
			t.Fatal("a session from before the change can still refresh")
		}
		ss, err := st.ListSessions(ctx, alice.ID)
		if err != nil || len(ss) != 1 || ss[0].ID != family || ss[0].UserAgent == nil || *ss[0].UserAgent != firefoxUA {
			t.Fatalf("sessions after the change = %+v, %v; want only the new one", ss, err)
		}
		if got := ids(t, bob.ID); !slices.Equal(got, []string{bobs}) {
			t.Fatalf("bob's sessions = %v; alice's password change must not touch them", got)
		}
		if _, err := st.ChangePassword(ctx, model.NewID(), newHash, store.NewRefresh{Hash: randomHash(t), TTL: time.Hour}); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("unknown user = %v, want ErrNotFound", err)
		}
	})

	t.Run("SessionLive: only an unrevoked, unexpired family of that user", func(t *testing.T) {
		live, _ := login(t, alice.ID, "")
		revoked, revokedHash := login(t, alice.ID, "")
		if err := st.RevokeRefreshFamily(ctx, revokedHash); err != nil {
			t.Fatal(err)
		}
		expired, _ := login(t, alice.ID, "")
		if _, err := db.Pool.Exec(ctx, `UPDATE refresh_tokens SET expires_at = now() - interval '1 second' WHERE family_id = $1`,
			expired); err != nil {
			t.Fatal(err)
		}
		for _, tt := range []struct {
			name, user, family string
			want               bool
		}{
			{"live", alice.ID, live, true},
			{"revoked", alice.ID, revoked, false},
			{"expired", alice.ID, expired, false},
			{"another user's", alice.ID, bobs, false},
			{"unknown", alice.ID, model.NewID(), false},
		} {
			if got, err := st.SessionLive(ctx, tt.user, tt.family); err != nil || got != tt.want {
				t.Errorf("%s: SessionLive = %v, %v; want %v", tt.name, got, err, tt.want)
			}
		}
	})
}
