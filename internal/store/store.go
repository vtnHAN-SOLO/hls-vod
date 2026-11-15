// Package store owns all Postgres access. SQL lives here and nowhere else.
package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Store wraps a pgx connection pool.
type Store struct {
	pool *pgxpool.Pool
}

// New builds a pool from dsn. It does not connect: pgxpool dials lazily, so
// the api can start (and report not-ready) while Postgres is still down.
func New(ctx context.Context, dsn string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		// pgx error text can echo the DSN; don't wrap it, it may carry the password.
		return nil, fmt.Errorf("parsing DATABASE_URL: invalid connection string")
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("creating postgres pool: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Ping checks that Postgres answers. The caller sets the deadline.
func (s *Store) Ping(ctx context.Context) error {
	if err := s.pool.Ping(ctx); err != nil {
		return fmt.Errorf("pinging postgres: %w", err)
	}
	return nil
}

// Close releases every pooled connection.
func (s *Store) Close() { s.pool.Close() }
