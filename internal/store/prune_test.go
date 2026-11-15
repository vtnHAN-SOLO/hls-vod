package store_test

import (
	"slices"
	"testing"

	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
	"github.com/vtnHAN-SOLO/hls-vod/internal/pgtest"
	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
)

func TestPruneAuthRows(t *testing.T) {
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
	u, err := st.CreateUser(ctx, "pruned", "not-a-real-hash", model.RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	exec := func(t *testing.T, sql string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	// refresh rows: family label → (expires, revoked) as SQL expressions.
	families := map[string]string{}
	token := func(t *testing.T, family, expires, revoked string) {
		t.Helper()
		if families[family] == "" {
			families[family] = model.NewID()
		}
		exec(t, `INSERT INTO refresh_tokens (user_id, family_id, token_hash, expires_at, revoked_at)
			VALUES ($1, $2, $3, `+expires+`, `+revoked+`)`, u.ID, families[family], randomHash(t))
	}
	const (
		future   = "now() + interval '20 days'"
		longAgo  = "now() - interval '8 days'"
		recently = "now() - interval '2 days'"
	)
	token(t, "revoked long ago", future, longAgo)
	token(t, "revoked long ago", future, longAgo)
	token(t, "expired long ago", longAgo, "NULL")
	token(t, "one live row", future, longAgo)
	token(t, "one live row", future, "NULL")
	token(t, "dead, but recently", future, recently)
	token(t, "dead, but recently", longAgo, longAgo)
	token(t, "expired recently", recently, "NULL")

	email := func(t *testing.T, label, expires, used string) {
		t.Helper()
		exec(t, `INSERT INTO email_tokens (user_id, purpose, token_hash, expires_at, used_at)
			VALUES ($1, 'reset', $2, `+expires+`, `+used+`)`, u.ID, []byte(label + "-------------------------------")[:32])
	}
	email(t, "used long ago", future, longAgo)
	email(t, "expired long ago", longAgo, "NULL")
	email(t, "used recently", future, recently)
	email(t, "live", future, "NULL")

	state := func(t *testing.T, expires string) {
		t.Helper()
		exec(t, `INSERT INTO oauth_states (state_hash, nonce, pkce_verifier, intent, expires_at)
			VALUES ($1, 'n', 'v', 'login', `+expires+`)`, randomHash(t))
	}
	state(t, "now() - interval '1 minute'")
	state(t, "now() + interval '5 minutes'")

	familiesLeft := func(t *testing.T) []string {
		t.Helper()
		var left []string
		for label, id := range families {
			var n int
			if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM refresh_tokens WHERE family_id = $1`, id).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n > 0 {
				left = append(left, label)
			}
		}
		slices.Sort(left)
		return left
	}
	count := func(t *testing.T, table string) int {
		t.Helper()
		var n int
		if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Batches of one, one batch a table: a bounded run.
	p, err := st.PruneAuthRows(ctx, 1, 1)
	if err != nil || p != (store.Pruned{RefreshTokens: 1, EmailTokens: 1, OAuthStates: 1}) {
		t.Fatalf("bounded run: %+v, %v", p, err)
	}
	p, err = st.PruneAuthRows(ctx, 100, 10)
	if err != nil || p != (store.Pruned{RefreshTokens: 2, EmailTokens: 1, OAuthStates: 0}) {
		t.Fatalf("second run: %+v, %v", p, err)
	}
	// A live family keeps every row, revoked ones included (reuse detection
	// and the sessions list need them); so does one dead for under 7 days.
	if got, want := familiesLeft(t), []string{"dead, but recently", "expired recently", "one live row"}; !slices.Equal(got, want) {
		t.Fatalf("families left = %v, want %v", got, want)
	}
	if n := count(t, "refresh_tokens"); n != 5 {
		t.Fatalf("%d refresh rows left, want 5", n)
	}
	if n := count(t, "email_tokens"); n != 2 {
		t.Fatalf("%d email tokens left, want the recent and the live one", n)
	}
	if n := count(t, "oauth_states"); n != 1 {
		t.Fatalf("%d sign-in states left, want the live one", n)
	}
	if p, err := st.PruneAuthRows(ctx, 100, 10); err != nil || p != (store.Pruned{}) {
		t.Fatalf("third run: %+v, %v; want nothing", p, err)
	}
}
