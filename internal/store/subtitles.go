package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
)

// ListSubtitles returns a movie's subtitle tracks in source order.
func (s *Store) ListSubtitles(ctx context.Context, movieID string) ([]model.SubtitleTrack, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT path, language, label, position FROM subtitle_tracks
		WHERE movie_id = $1 ORDER BY position, path`, movieID)
	if err != nil {
		return nil, fmt.Errorf("listing subtitles: %w", err)
	}
	ts, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (model.SubtitleTrack, error) {
		var t model.SubtitleTrack
		err := row.Scan(&t.Path, &t.Language, &t.Label, &t.Position)
		return t, err
	})
	if err != nil {
		return nil, fmt.Errorf("listing subtitles: %w", err)
	}
	return ts, nil
}

func insertSubtitles(ctx context.Context, tx pgx.Tx, movieID string, ts []model.SubtitleTrack) error {
	for _, t := range ts {
		if _, err := tx.Exec(ctx, `
			INSERT INTO subtitle_tracks (movie_id, path, language, label, position)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (movie_id, path) DO NOTHING`,
			movieID, t.Path, t.Language, t.Label, t.Position); err != nil {
			return fmt.Errorf("inserting subtitle %s: %w", t.Path, err)
		}
	}
	return nil
}

// replaceSubtitles makes ts the movie's only subtitle rows: the tracks of
// the version being published.
func replaceSubtitles(ctx context.Context, tx pgx.Tx, movieID string, ts []model.SubtitleTrack) error {
	if _, err := tx.Exec(ctx, `DELETE FROM subtitle_tracks WHERE movie_id = $1`, movieID); err != nil {
		return fmt.Errorf("removing the old subtitles: %w", err)
	}
	return insertSubtitles(ctx, tx, movieID, ts)
}

// SubtitleCandidate is a ready movie with no subtitle rows: maybe published
// before subtitle_tracks existed.
type SubtitleCandidate struct {
	ID          string
	LibraryPath string
}

// SubtitleBackfillCandidates lists the ready movies that have no subtitle
// rows, for the watcher's backfill. Movies whose current version has no
// subs/ folder stay on this list (one stat each per run); every other one
// leaves it for good once its rows are written.
func (s *Store) SubtitleBackfillCandidates(ctx context.Context) ([]SubtitleCandidate, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT m.id::text, m.library_path FROM movies m
		WHERE m.status = 'ready' AND m.library_path IS NOT NULL
		  AND NOT EXISTS (SELECT 1 FROM subtitle_tracks t WHERE t.movie_id = m.id)
		ORDER BY m.id`)
	if err != nil {
		return nil, fmt.Errorf("listing subtitle backfill candidates: %w", err)
	}
	cs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (SubtitleCandidate, error) {
		var c SubtitleCandidate
		err := row.Scan(&c.ID, &c.LibraryPath)
		return c, err
	})
	if err != nil {
		return nil, fmt.Errorf("listing subtitle backfill candidates: %w", err)
	}
	return cs, nil
}

// BackfillSubtitles writes ts as the movie's subtitle rows, but only if the
// movie still plays from libraryPath, is ready and has no rows: under the
// movie's row lock, so a retranscode that publishes meanwhile (it updates
// that row) is never overwritten with the old version's tracks. written is
// false when nothing was done. Running it twice is harmless.
func (s *Store) BackfillSubtitles(ctx context.Context, movieID, libraryPath string, ts []model.SubtitleTrack) (written bool, err error) {
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var ok bool
		err := tx.QueryRow(ctx, `
			SELECT status = 'ready' AND library_path = $2
			   AND NOT EXISTS (SELECT 1 FROM subtitle_tracks WHERE movie_id = $1)
			FROM movies WHERE id = $1 FOR UPDATE`, movieID, libraryPath).Scan(&ok)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && !ok) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("locking movie: %w", err)
		}
		if err := insertSubtitles(ctx, tx, movieID, ts); err != nil {
			return err
		}
		written = len(ts) > 0
		return nil
	})
	return written, err
}
