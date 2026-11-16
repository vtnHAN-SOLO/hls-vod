// Package pgtest starts a throwaway Postgres container for integration tests.
//
// Tests never touch the docker-compose database: each caller gets its own
// container with a password generated at test time, removed on cleanup.
// Only _test.go files may import this package.
package pgtest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/vtnHAN-SOLO/hls-vod/migrations"
)

// Image must match the tag in docker-compose.yml.
const Image = "postgres:16.15-alpine3.24"

// DB is a running test database.
type DB struct {
	Container *postgres.PostgresContainer
	DSN       string
	Pool      *pgxpool.Pool
}

// Start runs a fresh Postgres container and returns a pool connected to it.
// Migrations are not applied; use Migrator for that.
func Start(t *testing.T) *DB {
	t.Helper()
	ctx := context.Background()

	ctr, err := postgres.Run(ctx, Image,
		postgres.WithDatabase("media_test"),
		postgres.WithUsername("media_test"),
		postgres.WithPassword(randomHex(t, 24)),
		postgres.BasicWaitStrategies(),
	)
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatalf("starting postgres container (is Docker running?): %v", err)
	}

	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("container connection string: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connecting to test postgres: %v", err)
	}
	t.Cleanup(pool.Close)

	return &DB{Container: ctr, DSN: dsn, Pool: pool}
}

// Migrator returns a goose provider over the embedded production migrations.
func (d *DB) Migrator(t *testing.T) *goose.Provider {
	t.Helper()
	db := stdlib.OpenDBFromPool(d.Pool)
	t.Cleanup(func() { _ = db.Close() })
	return newProvider(t, db)
}

func newProvider(t *testing.T, db *sql.DB) *goose.Provider {
	t.Helper()
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		t.Fatalf("creating goose provider: %v", err)
	}
	return p
}

func randomHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("generating random password: %v", err)
	}
	return hex.EncodeToString(b)
}

// Publish makes a ready movie visible to viewers, the way an admin would
// after uploading a poster and writing an overview: a stand-in poster hash
// (no poster rows), an overview when it has none, published_at now. The
// movies_published_check constraint still applies: a movie that is not
// ready fails the test.
func (d *DB) Publish(ctx context.Context, t *testing.T, movieID string) {
	t.Helper()
	if _, err := d.Pool.Exec(ctx, `
		UPDATE movies SET poster_hash = sha256(id::text::bytea),
		       overview = COALESCE(NULLIF(trim(overview), ''), 'An overview.'),
		       published_at = now()
		WHERE id = $1`, movieID); err != nil {
		t.Fatalf("publishing movie %s: %v", movieID, err)
	}
}
