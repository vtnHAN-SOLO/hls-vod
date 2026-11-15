package store

import (
	"context"
	"fmt"
)

// A COMMIT that returns an error may still have succeeded: the server can
// commit and the connection break before the answer arrives. Before a
// caller undoes the file work that went with a transaction, it asks again
// with one of these. They run as a new query on the pool, never on the
// failed transaction's connection (pgx closes a connection that broke, and
// the pool dials a fresh one), so they see what actually committed.

// JobPublished reports whether job jobID is succeeded with its movie's
// library_path at libraryPath: FinishTranscode committed. The transcoder
// then keeps the version folder instead of removing what the catalog
// points at.
func (s *Store) JobPublished(ctx context.Context, jobID, libraryPath string) (bool, error) {
	var ok bool
	if err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (
		    SELECT 1 FROM transcode_jobs j JOIN movies m ON m.id = j.movie_id
		    WHERE j.id = $1 AND j.status = 'succeeded' AND m.library_path = $2)`,
		jobID, libraryPath).Scan(&ok); err != nil {
		return false, fmt.Errorf("re-reading job %s: %w", jobID, err)
	}
	return ok, nil
}

// SourceClaimed reports whether a movie row refers to sourcePath:
// EnqueueMovie committed. The watcher then keeps the file in sources/
// instead of returning it to the inbox, where it would be ingested twice.
func (s *Store) SourceClaimed(ctx context.Context, sourcePath string) (bool, error) {
	var ok bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM movies WHERE source_path = $1)`,
		sourcePath).Scan(&ok); err != nil {
		return false, fmt.Errorf("re-reading the movie for %s: %w", sourcePath, err)
	}
	return ok, nil
}
