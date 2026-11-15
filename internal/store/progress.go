package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
)

// ErrProgressRefused means a progress save matched no ready movie whose
// runtime (when known) covers the position: the service has already checked
// both, so this is a movie that changed in between.
var ErrProgressRefused = errors.New("progress refused")

// SaveProgress upserts userID's position in movieID. The movie must still be
// ready and the position within its runtime when the row is written: the
// INSERT … SELECT checks both in the same statement, so a movie that stopped
// being ready after the service looked is refused, not saved.
func (s *Store) SaveProgress(ctx context.Context, userID, movieID string, positionSec int) error {
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO watch_progress (user_id, movie_id, position_sec)
		SELECT $1, id, $3 FROM movies
		WHERE id = $2 AND status = 'ready' AND (runtime_sec IS NULL OR $3 <= runtime_sec)
		ON CONFLICT (user_id, movie_id) DO UPDATE SET position_sec = EXCLUDED.position_sec`,
		userID, movieID, positionSec)
	if err != nil {
		return fmt.Errorf("saving progress: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrProgressRefused
	}
	return nil
}

// GetProgress returns userID's saved position in movieID; ok is false when
// there is none.
func (s *Store) GetProgress(ctx context.Context, userID, movieID string) (positionSec int, ok bool, err error) {
	err = s.pool.QueryRow(ctx,
		`SELECT position_sec FROM watch_progress WHERE user_id = $1 AND movie_id = $2`,
		userID, movieID).Scan(&positionSec)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("getting progress: %w", err)
	}
	return positionSec, true, nil
}

// ContinueWatching returns userID's ready movies with saved progress, most
// recently saved first, at most limit. A movie watched to at least
// finishedPercent of its runtime is left out; one with an unknown runtime
// is kept.
func (s *Store) ContinueWatching(ctx context.Context, userID string, finishedPercent, limit int) ([]model.Progress, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT m.id::text, m.title, m.year, m.tmdb_id, m.overview, m.runtime_sec,
		       m.source_path, m.library_path, m.poster_path, m.status, m.library_cleanup_after, m.created_at, m.updated_at,
		       p.position_sec, p.updated_at
		FROM watch_progress p
		JOIN movies m ON m.id = p.movie_id
		WHERE p.user_id = $1
		  AND m.status = 'ready'
		  AND (m.runtime_sec IS NULL OR p.position_sec::bigint * 100 < m.runtime_sec::bigint * $2)
		ORDER BY p.updated_at DESC, m.id DESC
		LIMIT $3`, userID, finishedPercent, limit)
	if err != nil {
		return nil, fmt.Errorf("listing continue-watching: %w", err)
	}
	ps, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (model.Progress, error) {
		var p model.Progress
		var status string
		m := &p.Movie
		err := row.Scan(&m.ID, &m.Title, &m.Year, &m.TMDBID, &m.Overview, &m.RuntimeSec,
			&m.SourcePath, &m.LibraryPath, &m.PosterPath, &status, &m.LibraryCleanupAfter, &m.CreatedAt, &m.UpdatedAt,
			&p.PositionSec, &p.UpdatedAt)
		m.Status = model.MovieStatus(status)
		return p, err
	})
	if err != nil {
		return nil, fmt.Errorf("listing continue-watching: %w", err)
	}
	return ps, nil
}
