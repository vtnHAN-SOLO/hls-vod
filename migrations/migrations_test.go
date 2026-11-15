package migrations_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vtnHAN-SOLO/hls-vod/internal/pgtest"
)

var wantTables = []string{
	"email_tokens", "movies", "oauth_states", "outgoing_mail", "refresh_tokens", "renditions", "transcode_jobs",
	"user_identities", "users", "watch_progress",
}

const (
	sqlstateUniqueViolation = "23505"
	sqlstateCheckViolation  = "23514"
)

// One container for the package; subtests run in order and share the schema.
func TestMigrations(t *testing.T) {
	db := pgtest.Start(t)
	ctx := t.Context()
	migrator := db.Migrator(t)

	t.Run("up creates every table", func(t *testing.T) {
		if _, err := migrator.Up(ctx); err != nil {
			t.Fatalf("up: %v", err)
		}
		if got := tables(t, db.Pool); !slices.Equal(got, wantTables) {
			t.Fatalf("tables = %v, want %v", got, wantTables)
		}
	})

	t.Run("down all then up all", func(t *testing.T) {
		if _, err := migrator.DownTo(ctx, 0); err != nil {
			t.Fatalf("down to 0: %v", err)
		}
		if got := tables(t, db.Pool); len(got) != 0 {
			t.Fatalf("tables left after down: %v", got)
		}
		var fn int
		if err := db.Pool.QueryRow(ctx,
			`SELECT count(*) FROM pg_proc WHERE proname = 'set_updated_at'`).Scan(&fn); err != nil {
			t.Fatal(err)
		}
		if fn != 0 {
			t.Fatal("set_updated_at() still exists after down")
		}
		if _, err := migrator.Up(ctx); err != nil {
			t.Fatalf("up again: %v", err)
		}
		if got := tables(t, db.Pool); !slices.Equal(got, wantTables) {
			t.Fatalf("tables after re-up = %v, want %v", got, wantTables)
		}
	})

	t.Run("database rejects invalid rows", func(t *testing.T) {
		movieID := insertMovie(t, db.Pool, "/inbox/a.mkv")
		_, err := db.Pool.Exec(ctx, `INSERT INTO transcode_jobs (movie_id) VALUES ($1)`, movieID)
		if err != nil {
			t.Fatalf("first queued job: %v", err)
		}

		tests := []struct {
			name      string
			sql       string
			args      []any
			wantState string
		}{
			{
				name:      "invalid movie status",
				sql:       `INSERT INTO movies (title, source_path, status) VALUES ('x', '/inbox/b.mkv', 'bogus')`,
				wantState: sqlstateCheckViolation,
			},
			{
				name:      "invalid job status",
				sql:       `UPDATE transcode_jobs SET status = 'paused' WHERE movie_id = $1`,
				args:      []any{movieID},
				wantState: sqlstateCheckViolation,
			},
			{
				name:      "duplicate source_path",
				sql:       `INSERT INTO movies (title, source_path) VALUES ('dup', '/inbox/a.mkv')`,
				wantState: sqlstateUniqueViolation,
			},
			{
				name:      "second queued job for same movie",
				sql:       `INSERT INTO transcode_jobs (movie_id) VALUES ($1)`,
				args:      []any{movieID},
				wantState: sqlstateUniqueViolation,
			},
			{
				name:      "running job while one is queued",
				sql:       `INSERT INTO transcode_jobs (movie_id, status) VALUES ($1, 'running')`,
				args:      []any{movieID},
				wantState: sqlstateUniqueViolation,
			},
			{
				name:      "uppercase username on insert",
				sql:       `INSERT INTO users (username, password_hash) VALUES ('Alice', 'not-a-real-hash')`,
				wantState: sqlstateCheckViolation,
			},
			{
				name:      "uppercase username on update",
				sql:       `UPDATE users SET username = 'BOB' WHERE username = 'bob'`,
				wantState: sqlstateCheckViolation,
			},
			{
				name:      "uppercase email",
				sql:       `UPDATE users SET email = 'Bob@example.com' WHERE username = 'bob'`,
				wantState: sqlstateCheckViolation,
			},
			{
				name:      "duplicate email",
				sql:       `INSERT INTO users (username, password_hash, email) VALUES ('carol', 'not-a-real-hash', 'bob@example.com')`,
				wantState: sqlstateUniqueViolation,
			},
			{
				name:      "unknown email token purpose",
				sql:       `INSERT INTO email_tokens (user_id, purpose, token_hash, expires_at) SELECT id, 'login', '\x01', now() FROM users WHERE username = 'bob'`,
				wantState: sqlstateCheckViolation,
			},
			{
				name:      "change_email token (removed in 00011)",
				sql:       `INSERT INTO email_tokens (user_id, purpose, token_hash, expires_at) SELECT id, 'change_email', '\x02', now() FROM users WHERE username = 'bob'`,
				wantState: sqlstateCheckViolation,
			},
			{
				name:      "change_email mail kind (removed in 00011)",
				sql:       `INSERT INTO outgoing_mail (to_hash, kind) VALUES ('\x03', 'change_email')`,
				wantState: sqlstateCheckViolation,
			},
			{
				name:      "identity of another provider",
				sql:       `INSERT INTO user_identities (user_id, provider, subject) SELECT id, 'github', 's1' FROM users WHERE username = 'bob'`,
				wantState: sqlstateCheckViolation,
			},
			{
				name:      "second Google account for one user",
				sql:       `INSERT INTO user_identities (user_id, provider, subject) SELECT id, 'google', 's2' FROM users WHERE username = 'bob'`,
				wantState: sqlstateUniqueViolation,
			},
			{
				name:      "one Google account for two users",
				sql:       `INSERT INTO user_identities (user_id, provider, subject) SELECT id, 'google', 's1' FROM users WHERE username = 'carl'`,
				wantState: sqlstateUniqueViolation,
			},
			{
				name:      "link state without a user",
				sql:       `INSERT INTO oauth_states (state_hash, nonce, pkce_verifier, intent, expires_at) VALUES ('\x20', 'n', 'v', 'link', now())`,
				wantState: sqlstateCheckViolation,
			},
			{
				name:      "login state with a user",
				sql:       `INSERT INTO oauth_states (state_hash, nonce, pkce_verifier, intent, user_id, expires_at) SELECT '\x21', 'n', 'v', 'login', id, now() FROM users WHERE username = 'bob'`,
				wantState: sqlstateCheckViolation,
			},
			{
				name:      "unknown state intent",
				sql:       `INSERT INTO oauth_states (state_hash, nonce, pkce_verifier, intent, expires_at) VALUES ('\x22', 'n', 'v', 'signup', now())`,
				wantState: sqlstateCheckViolation,
			},
			{
				name:      "unknown job purpose (00014)",
				sql:       `UPDATE transcode_jobs SET purpose = 'resize' WHERE movie_id = $1`,
				args:      []any{movieID},
				wantState: sqlstateCheckViolation,
			},
			{
				name:      "unknown outgoing mail kind",
				sql:       `INSERT INTO outgoing_mail (to_hash, kind) VALUES ('\x04', 'newsletter')`,
				wantState: sqlstateCheckViolation,
			},
		}

		// Control row: lowercase is accepted, so the rejections above are the CHECK, not the table.
		if _, err := db.Pool.Exec(ctx,
			`INSERT INTO users (username, password_hash, email) VALUES ('bob', 'not-a-real-hash', 'bob@example.com')`); err != nil {
			t.Fatalf("lowercase username should be accepted: %v", err)
		}
		// A Google-only account: no password. bob's Google identity is the
		// control row for the identity rules.
		if _, err := db.Pool.Exec(ctx, `INSERT INTO users (username, email) VALUES ('carl', 'carl@example.com')`); err != nil {
			t.Fatalf("a user without a password should be accepted: %v", err)
		}
		if _, err := db.Pool.Exec(ctx,
			`INSERT INTO user_identities (user_id, provider, subject) SELECT id, 'google', 's1' FROM users WHERE username = 'bob'`); err != nil {
			t.Fatalf("first Google identity: %v", err)
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				_, err := db.Pool.Exec(ctx, tt.sql, tt.args...)
				var pgErr *pgconn.PgError
				if !errors.As(err, &pgErr) {
					t.Fatalf("expected a postgres error with SQLSTATE %s, got %v", tt.wantState, err)
				}
				if pgErr.Code != tt.wantState {
					t.Fatalf("SQLSTATE = %s (%s), want %s", pgErr.Code, pgErr.Message, tt.wantState)
				}
			})
		}

		t.Run("the remaining purposes and mail kinds are accepted", func(t *testing.T) {
			for i, purpose := range []string{"verify", "reset"} {
				if _, err := db.Pool.Exec(ctx, `INSERT INTO email_tokens (user_id, purpose, token_hash, expires_at)
					SELECT id, $1, $2, now() FROM users WHERE username = 'bob'`, purpose, []byte{0x10, byte(i)}); err != nil {
					t.Errorf("purpose %s: %v", purpose, err)
				}
			}
			var cols int
			if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
				WHERE table_name = 'email_tokens' AND column_name = 'new_email'`).Scan(&cols); err != nil || cols != 0 {
				t.Errorf("email_tokens.new_email still exists (%d, %v)", cols, err)
			}
			for _, kind := range []string{"verify", "account_exists", "reset", "google_linked"} {
				if _, err := db.Pool.Exec(ctx, `INSERT INTO outgoing_mail (to_hash, kind) VALUES ('\x05', $1)`, kind); err != nil {
					t.Errorf("kind %s: %v", kind, err)
				}
			}
		})

		t.Run("deleting movies and disabled users (00013)", func(t *testing.T) {
			for i, status := range []string{"pending", "processing", "ready", "failed", "deleting"} {
				if _, err := db.Pool.Exec(ctx, `INSERT INTO movies (title, source_path, status) VALUES ('s', $1, $2)`,
					"/inbox/status-"+string(rune('a'+i))+".mkv", status); err != nil {
					t.Errorf("status %s: %v", status, err)
				}
			}
			var purpose string
			if err := db.Pool.QueryRow(ctx, `SELECT purpose FROM transcode_jobs WHERE movie_id = $1`, movieID).Scan(&purpose); err != nil ||
				purpose != "ingest" {
				t.Errorf("default purpose = %q, %v; want ingest", purpose, err)
			}
			if _, err := db.Pool.Exec(ctx, `UPDATE transcode_jobs SET purpose = 'retranscode' WHERE movie_id = $1`, movieID); err != nil {
				t.Errorf("purpose retranscode: %v", err)
			}
			if _, err := db.Pool.Exec(ctx, `UPDATE movies SET library_cleanup_after = now() WHERE id = $1`, movieID); err != nil {
				t.Errorf("library_cleanup_after: %v", err)
			}
			var disabled bool
			if err := db.Pool.QueryRow(ctx,
				`UPDATE users SET disabled_at = now() WHERE username = 'carl' RETURNING disabled_at IS NOT NULL`).Scan(&disabled); err != nil || !disabled {
				t.Errorf("disabling a user: %v", err)
			}
		})

		t.Run("new job allowed once the active one finishes", func(t *testing.T) {
			if _, err := db.Pool.Exec(ctx,
				`UPDATE transcode_jobs SET status = 'failed' WHERE movie_id = $1`, movieID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Pool.Exec(ctx,
				`INSERT INTO transcode_jobs (movie_id) VALUES ($1)`, movieID); err != nil {
				t.Fatalf("retry job after failure should be allowed: %v", err)
			}
		})
	})

	t.Run("updated_at trigger fires", func(t *testing.T) {
		id := insertMovie(t, db.Pool, "/inbox/trigger.mkv")
		var changed bool
		err := db.Pool.QueryRow(ctx, `
			UPDATE movies SET title = 'renamed' WHERE id = $1
			RETURNING updated_at > created_at`, id).Scan(&changed)
		if err != nil {
			t.Fatal(err)
		}
		if !changed {
			t.Fatal("updated_at was not advanced by the trigger")
		}
	})

	t.Run("plain indexes exist", func(t *testing.T) {
		tests := []struct{ table, index, wantDef string }{
			{"watch_progress", "watch_progress_movie_id_idx", "(movie_id)"},
			{"transcode_jobs", "transcode_jobs_movie_id_idx", "(movie_id)"},
			{"email_tokens", "email_tokens_user_id_idx", "(user_id)"},
			{"outgoing_mail", "outgoing_mail_to_hash_created_at_idx", "(to_hash, created_at)"},
			{"outgoing_mail", "outgoing_mail_created_at_idx", "(created_at)"},
			{"oauth_states", "oauth_states_expires_at_idx", "(expires_at)"},
		}
		for _, tt := range tests {
			t.Run(tt.index, func(t *testing.T) {
				var def string
				err := db.Pool.QueryRow(ctx, `
					SELECT indexdef FROM pg_indexes
					WHERE schemaname = 'public' AND tablename = $1 AND indexname = $2`,
					tt.table, tt.index).Scan(&def)
				if err != nil {
					t.Fatalf("index %s on %s not found: %v", tt.index, tt.table, err)
				}
				// A plain index: full column, not UNIQUE, not partial.
				if !strings.HasSuffix(def, tt.wantDef) || strings.Contains(def, "UNIQUE") || strings.Contains(def, "WHERE") {
					t.Fatalf("unexpected definition: %s", def)
				}
			})
		}
	})
}

func tables(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(t.Context(), `
		SELECT table_name FROM information_schema.tables
		WHERE table_schema = 'public' AND table_name <> 'goose_db_version'
		ORDER BY table_name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func insertMovie(t *testing.T, pool *pgxpool.Pool, sourcePath string) string {
	t.Helper()
	var id string
	err := pool.QueryRow(t.Context(),
		`INSERT INTO movies (title, source_path) VALUES ('t', $1) RETURNING id::text`,
		sourcePath).Scan(&id)
	if err != nil {
		t.Fatalf("inserting movie: %v", err)
	}
	return id
}
