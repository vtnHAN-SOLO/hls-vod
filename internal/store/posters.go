package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/vtnHAN-SOLO/hls-vod/internal/poster"
)

// Posters keeps movie posters in Postgres (movie_posters, one row per
// width) and implements poster.Store. See ADR 0010 for why the bytes live in
// the database and not on the media volume.
type Posters struct{ s *Store }

var _ poster.Store = Posters{}

// Posters returns the Postgres poster store over s's pool.
func (s *Store) Posters() Posters { return Posters{s: s} }

// Put replaces the movie's poster rows and sets poster_hash, in one
// transaction under the movie's row lock, so a list never sees a hash
// without its variants. poster.ErrNotFound for an unknown movie.
func (p Posters) Put(ctx context.Context, movieID string, ps poster.Poster) error {
	if len(ps.Variants) == 0 {
		return errors.New("storing poster: no variants")
	}
	err := pgx.BeginFunc(ctx, p.s.pool, func(tx pgx.Tx) error {
		if _, err := lockMovie(ctx, tx, movieID); err != nil {
			return err
		}
		return putPoster(ctx, tx, movieID, ps)
	})
	if errors.Is(err, ErrNotFound) {
		return poster.ErrNotFound
	}
	return err
}

func putPoster(ctx context.Context, tx pgx.Tx, movieID string, ps poster.Poster) error {
	if _, err := tx.Exec(ctx, `DELETE FROM movie_posters WHERE movie_id = $1`, movieID); err != nil {
		return fmt.Errorf("removing old poster: %w", err)
	}
	for _, v := range ps.Variants {
		if _, err := tx.Exec(ctx,
			`INSERT INTO movie_posters (movie_id, width, height, bytes) VALUES ($1, $2, $3, $4)`,
			movieID, v.Width, v.Height, v.Bytes); err != nil {
			return fmt.Errorf("storing poster variant %d: %w", v.Width, err)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE movies SET poster_hash = $2 WHERE id = $1`, movieID, ps.Hash[:]); err != nil {
		return fmt.Errorf("setting poster hash: %w", err)
	}
	return nil
}

// Get returns one variant's JPEG bytes while hash is still the movie's
// poster hash; poster.ErrNotFound otherwise (an old URL after a new
// upload, an unknown movie or width).
func (p Posters) Get(ctx context.Context, movieID string, width int, hash []byte) ([]byte, error) {
	var b []byte
	err := p.s.pool.QueryRow(ctx, `
		SELECT p.bytes FROM movie_posters p
		JOIN movies m ON m.id = p.movie_id
		WHERE p.movie_id = $1 AND p.width = $2 AND m.poster_hash = $3`,
		movieID, width, hash).Scan(&b)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, poster.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("reading poster: %w", err)
	}
	return b, nil
}

// Delete removes the movie's poster and clears poster_hash in one
// transaction; poster.ErrPublished while the movie is published (it must
// keep one), poster.ErrNotFound for an unknown movie. No poster is fine.
func (p Posters) Delete(ctx context.Context, movieID string) error {
	err := pgx.BeginFunc(ctx, p.s.pool, func(tx pgx.Tx) error {
		st, err := lockMovieState(ctx, tx, movieID)
		if err != nil {
			return err
		}
		if st.published {
			return poster.ErrPublished
		}
		if _, err := tx.Exec(ctx, `DELETE FROM movie_posters WHERE movie_id = $1`, movieID); err != nil {
			return fmt.Errorf("removing poster: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE movies SET poster_hash = NULL WHERE id = $1`, movieID); err != nil {
			return fmt.Errorf("clearing poster hash: %w", err)
		}
		return nil
	})
	if errors.Is(err, ErrNotFound) {
		return poster.ErrNotFound
	}
	return err
}
