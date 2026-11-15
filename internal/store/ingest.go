package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
)

// ErrSourceExists means a movie with this source_path is already catalogued.
var ErrSourceExists = errors.New("source_path already in the catalog")

// NewMovie is a movie whose files are already published under the library.
// Paths are relative: SourcePath and LibraryPath to the media root,
// PosterPath to the movie's library dir.
type NewMovie struct {
	ID          string
	Title       string
	SourcePath  string
	LibraryPath string
	PosterPath  string
	RuntimeSec  int
	Status      model.MovieStatus
}

// Published is what a finished transcode adds to a movie, as built by
// internal/ingest. LibraryPath ("library/{id}/{version}") is relative to the
// media root; PosterPath and every PlaylistPath to library/{id}/, so they
// start with the version ("{version}/poster.jpg").
type Published struct {
	LibraryPath string
	PosterPath  string
	RuntimeSec  int
	Renditions  []NewRendition
	// OldVersionGrace: a retranscode's previous version is removed this
	// long after the switch (mediattl.OldVersionGrace). Ingest ignores it.
	OldVersionGrace time.Duration
}

// NewRendition is one written rung. PlaylistPath is relative to
// library/{id}/ and starts with the version.
type NewRendition struct {
	Height       int
	BitrateKbps  int
	PlaylistPath string
	SizeBytes    int64
}

// CreateMovie inserts the movie and all its renditions in one transaction:
// either every row is written or none is. A duplicate source_path is
// ErrSourceExists.
func (s *Store) CreateMovie(ctx context.Context, m NewMovie, rs []NewRendition) error {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO movies (id, title, source_path, library_path, poster_path, runtime_sec, status)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			m.ID, m.Title, m.SourcePath, m.LibraryPath, m.PosterPath, m.RuntimeSec, string(m.Status),
		); err != nil {
			return fmt.Errorf("inserting movie: %w", err)
		}
		return insertRenditions(ctx, tx, m.ID, rs)
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "movies_source_path_key" {
		return ErrSourceExists
	}
	return err
}
