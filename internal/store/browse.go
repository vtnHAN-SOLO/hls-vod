package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
)

// PopularMovies ranks published ready movies by the number of distinct users whose
// watch progress in them was saved at or after since (nil: ever), most
// first, ties by id; at most limit. Movies nobody watched in the window are
// left out: a rank is never made up.
//
// watch_progress has one row per (user, movie), so counting rows counts
// distinct users. Every save upserts the row and its trigger moves
// updated_at, so "saved in the window" is updated_at >= since.
func (s *Store) PopularMovies(ctx context.Context, since *time.Time, limit int) ([]model.Popular, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+movieColumns+`, v.viewers
		FROM (
			SELECT movie_id, count(*) AS viewers
			FROM watch_progress
			WHERE $1::timestamptz IS NULL OR updated_at >= $1
			GROUP BY movie_id
		) v
		JOIN movies ON movies.id = v.movie_id
		WHERE movies.status = 'ready' AND movies.published_at IS NOT NULL
		ORDER BY v.viewers DESC, movies.id ASC
		LIMIT $2`, since, limit)
	if err != nil {
		return nil, fmt.Errorf("listing popular movies: %w", err)
	}
	ps, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (model.Popular, error) {
		var p model.Popular
		var status string
		err := row.Scan(append(movieDest(&p.Movie, &status), &p.Viewers)...)
		p.Movie.Status = model.MovieStatus(status)
		return p, err
	})
	if err != nil {
		return nil, fmt.Errorf("listing popular movies: %w", err)
	}
	return ps, nil
}

// FeaturedMovies returns the published ready movies an admin featured, newest
// featured first (ties by id), at most limit.
func (s *Store) FeaturedMovies(ctx context.Context, limit int) ([]model.Movie, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+movieColumns+`
		FROM movies
		WHERE featured_at IS NOT NULL AND status = 'ready' AND published_at IS NOT NULL
		ORDER BY featured_at DESC, id DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("listing featured movies: %w", err)
	}
	ms, err := pgx.CollectRows(rows, scanMovie)
	if err != nil {
		return nil, fmt.Errorf("listing featured movies: %w", err)
	}
	return ms, nil
}

// ErrMovieNotPublished means featuring a ready movie that is not
// published: viewers could not see it.
var ErrMovieNotPublished = errors.New("the movie is not published")

// SetFeatured features (featured_at = now, so it moves to the front) or
// unfeatures a movie. Only a published movie can be featured:
// ErrMovieNotReady for one that is not ready, ErrMovieNotPublished for a
// ready one that is not published. Unfeaturing works in any status.
// ErrNotFound for an unknown movie.
func (s *Store) SetFeatured(ctx context.Context, id string, featured bool) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		lk, err := lockMovieState(ctx, tx, id)
		if err != nil {
			return err
		}
		if featured && lk.status != model.StatusReady {
			return ErrMovieNotReady
		}
		if featured && !lk.published {
			return ErrMovieNotPublished
		}
		q := `UPDATE movies SET featured_at = NULL WHERE id = $1`
		if featured {
			q = `UPDATE movies SET featured_at = now() WHERE id = $1`
		}
		if _, err := tx.Exec(ctx, q, id); err != nil {
			return fmt.Errorf("setting featured: %w", err)
		}
		return nil
	})
}

// PublishRefusal is why a movie cannot be published, in the order checked.
type PublishRefusal string

// The reasons, as the API spells them.
const (
	RefusedNotReady        PublishRefusal = "not_ready"
	RefusedMissingPoster   PublishRefusal = "missing_poster"
	RefusedMissingOverview PublishRefusal = "missing_overview"
)

// PublishRefusedError is a publish the rule refuses.
type PublishRefusedError struct{ Reason PublishRefusal }

func (e *PublishRefusedError) Error() string { return "cannot publish: " + string(e.Reason) }

// ErrMoviePublished means a change a published movie cannot take (its
// poster removed, its overview cleared): unpublish it first.
var ErrMoviePublished = errors.New("the movie is published")

// movieState is the row lock's view of a movie: what publishing needs.
type movieState struct {
	status      model.MovieStatus
	published   bool
	hasPoster   bool
	hasOverview bool
}

// lockMovieState locks the movie's row (as lockMovie) and reads what the
// publishing rule looks at. ErrNotFound for an unknown movie.
func lockMovieState(ctx context.Context, tx pgx.Tx, id string) (movieState, error) {
	var st movieState
	var status string
	err := tx.QueryRow(ctx, `
		SELECT status, published_at IS NOT NULL, poster_hash IS NOT NULL,
		       overview IS NOT NULL AND length(trim(overview)) > 0
		FROM movies WHERE id = $1 FOR UPDATE`, id).Scan(&status, &st.published, &st.hasPoster, &st.hasOverview)
	if errors.Is(err, pgx.ErrNoRows) {
		return movieState{}, ErrNotFound
	}
	if err != nil {
		return movieState{}, fmt.Errorf("loading movie: %w", err)
	}
	st.status = model.MovieStatus(status)
	return st, nil
}

// PublishMovie makes a movie visible to viewers. A *PublishRefusedError
// says why it cannot be: not ready, no poster, no overview (checked in that
// order; the movies_published_check constraint enforces the same rule).
// Publishing a published movie keeps its published_at. ErrNotFound for an
// unknown movie.
func (s *Store) PublishMovie(ctx context.Context, id string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		st, err := lockMovieState(ctx, tx, id)
		if err != nil {
			return err
		}
		switch {
		case st.status != model.StatusReady:
			return &PublishRefusedError{RefusedNotReady}
		case !st.hasPoster:
			return &PublishRefusedError{RefusedMissingPoster}
		case !st.hasOverview:
			return &PublishRefusedError{RefusedMissingOverview}
		}
		if _, err := tx.Exec(ctx,
			`UPDATE movies SET published_at = COALESCE(published_at, now()) WHERE id = $1`, id); err != nil {
			return fmt.Errorf("publishing movie: %w", err)
		}
		return nil
	})
}

// UnpublishMovie hides a movie from viewers and takes it out of the
// spotlight (featuring needs a published movie), any status. ErrNotFound
// for an unknown movie.
func (s *Store) UnpublishMovie(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE movies SET published_at = NULL, featured_at = NULL WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("unpublishing movie: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateMovieMetadata applies md to the movie (a nil field stays as it is)
// and returns it as the admin area lists it; ErrNotFound for an unknown
// movie, ErrMoviePublished when md clears the overview of a published
// movie. The service has validated md.
func (s *Store) UpdateMovieMetadata(ctx context.Context, id string, md model.MovieMetadata) (model.AdminMovie, error) {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		st, err := lockMovieState(ctx, tx, id)
		if err != nil {
			return err
		}
		if md.ClearOverview && st.published {
			return ErrMoviePublished
		}
		if _, err := tx.Exec(ctx, `
			UPDATE movies SET
				title    = COALESCE($2, title),
				year     = CASE WHEN $3 THEN NULL ELSE COALESCE($4, year) END,
				overview = CASE WHEN $5 THEN NULL ELSE COALESCE($6, overview) END
			WHERE id = $1`,
			id, md.Title, md.ClearYear, md.Year, md.ClearOverview, md.Overview); err != nil {
			return fmt.Errorf("updating movie: %w", err)
		}
		return nil
	})
	if err != nil {
		return model.AdminMovie{}, err
	}
	return s.GetAdminMovie(ctx, id)
}
