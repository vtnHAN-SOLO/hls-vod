package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// claimLock is the advisory lock key shared by EnqueueMovie (shared mode) and
// ReleaseOrphans (exclusive). Arbitrary, but fixed: every watcher must agree.
const claimLock int64 = 0x6d65_6469_6100_0001

// PendingMovie is a source file the watcher has just claimed. SourcePath is
// relative to MEDIA_ROOT ("sources/{id}.mkv").
type PendingMovie struct {
	ID         string
	Title      string
	SourcePath string
}

// EnqueueMovie runs claim, then inserts the movie (status pending) and its
// transcode job (status queued) in one transaction. If claim fails nothing is
// written and its error is returned unchanged.
//
// Everything runs under claimLock in shared mode: any number of claims run at
// once, but ReleaseOrphans cannot run while one is between claim and commit.
// That is what lets ReleaseOrphans treat "file but no row" as a crash
// leftover instead of a claim in progress.
//
// If claim succeeded but the insert or the commit fails, the error is
// returned and the caller must undo the claim.
func (s *Store) EnqueueMovie(ctx context.Context, m PendingMovie, claim func() error) error {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared($1)`, claimLock); err != nil {
			return fmt.Errorf("taking the claim lock: %w", err)
		}
		if err := claim(); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO movies (id, title, source_path) VALUES ($1, $2, $3)`,
			m.ID, m.Title, m.SourcePath,
		); err != nil {
			return fmt.Errorf("inserting movie: %w", err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO transcode_jobs (movie_id) VALUES ($1)`, m.ID); err != nil {
			return fmt.Errorf("inserting transcode job: %w", err)
		}
		return nil
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "movies_source_path_key" {
		return ErrSourceExists
	}
	return err
}

// ReleaseOrphans calls release for each of sourcePaths that no movie row
// refers to, while holding claimLock exclusively. If any claim is in
// progress it does nothing and returns ran=false: the next run will check
// again. Errors from release are joined and returned; they do not stop the
// remaining paths.
func (s *Store) ReleaseOrphans(ctx context.Context, sourcePaths []string, release func(sourcePath string) error) (ran bool, err error) {
	if len(sourcePaths) == 0 {
		return true, nil
	}
	var releaseErrs []error
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1)`, claimLock).Scan(&ran); err != nil {
			return fmt.Errorf("taking the claim lock: %w", err)
		}
		if !ran {
			return nil
		}
		rows, err := tx.Query(ctx, `
			SELECT p FROM unnest($1::text[]) AS p
			WHERE NOT EXISTS (SELECT 1 FROM movies WHERE source_path = p)`, sourcePaths)
		if err != nil {
			return fmt.Errorf("finding orphaned sources: %w", err)
		}
		orphans, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return fmt.Errorf("finding orphaned sources: %w", err)
		}
		// Released while the lock is still held: a claim that starts now
		// waits for this commit, so none is between rename and insert.
		for _, p := range orphans {
			if err := release(p); err != nil {
				releaseErrs = append(releaseErrs, err)
			}
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return ran, errors.Join(releaseErrs...)
}
