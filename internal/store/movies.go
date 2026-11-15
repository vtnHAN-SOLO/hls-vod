package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
)

// ErrNotFound means no row matched.
var ErrNotFound = errors.New("not found")

// MovieSort is the order of a movie list.
type MovieSort string

// The movie list orders. Each one ends in an id tiebreak, so the order is
// total and pages never overlap or skip rows that share the sort key.
const (
	SortAddedDesc MovieSort = "added_desc" // newest first; the zero value means this
	SortTitleAsc  MovieSort = "title_asc"  // case-insensitive
	SortYearDesc  MovieSort = "year_desc"  // movies without a year last
	SortYearAsc   MovieSort = "year_asc"   // movies without a year last
)

// movieOrder maps each sort to a fixed ORDER BY. Only these literals ever
// reach the SQL: the sort never comes from the request as text.
var movieOrder = map[MovieSort]string{
	SortAddedDesc: `created_at DESC, id DESC`,
	// lower() first, so "alpha" sorts before "Beta" in any collation; the
	// raw title next, so "Alpha" and "alpha" keep one order too.
	SortTitleAsc: `lower(title) ASC, title ASC, id ASC`,
	SortYearDesc: `year DESC NULLS LAST, lower(title) ASC, id ASC`,
	SortYearAsc:  `year ASC NULLS LAST, lower(title) ASC, id ASC`,
}

// MovieFilter selects a page of movies. The service validates it; the store
// trusts Limit and Offset are in range and Sort is one of the constants.
type MovieFilter struct {
	Status   *model.MovieStatus // nil: every status
	Title    *string            // nil: any title; else a case-insensitive substring, matched literally
	YearFrom *int               // nil: no lower bound; else year >= YearFrom (no year never matches)
	YearTo   *int               // nil: no upper bound; else year <= YearTo
	Sort     MovieSort          // "": SortAddedDesc
	Limit    int64
	Offset   int64
}

// containsPattern turns user text into an ILIKE pattern that matches it as
// a literal substring: the LIKE metacharacters %, _ and the escape character
// itself are escaped, so "50%" finds "50%" and not "500".
func containsPattern(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(s) + "%"
}

// movieWhere is shared by the page and the count, so the total always
// describes the same filter as the rows.
const movieWhere = `WHERE ($1::text IS NULL OR status = $1)
	  AND ($2::text IS NULL OR title ILIKE $2 ESCAPE '\')
	  AND ($3::int IS NULL OR year >= $3)
	  AND ($4::int IS NULL OR year <= $4)`

const movieColumns = `id::text, title, year, tmdb_id, overview, runtime_sec,
	source_path, library_path, poster_path, status, library_cleanup_after, created_at, updated_at`

// ListMovies returns one page of movies in f.Sort order (newest first by
// default) and the total number of movies matching the filter (for
// pagination). A year bound leaves out movies with no year.
func (s *Store) ListMovies(ctx context.Context, f MovieFilter) ([]model.Movie, int64, error) {
	sort := f.Sort
	if sort == "" {
		sort = SortAddedDesc
	}
	order, ok := movieOrder[sort]
	if !ok {
		return nil, 0, fmt.Errorf("listing movies: unknown sort %q", sort)
	}
	var status, title *string
	if f.Status != nil {
		v := string(*f.Status)
		status = &v
	}
	if f.Title != nil {
		v := containsPattern(*f.Title)
		title = &v
	}

	rows, err := s.pool.Query(ctx, `
		SELECT `+movieColumns+`
		FROM movies
		`+movieWhere+`
		ORDER BY `+order+`
		LIMIT $5 OFFSET $6`,
		status, title, f.YearFrom, f.YearTo, f.Limit, f.Offset)
	if err != nil {
		return nil, 0, fmt.Errorf("listing movies: %w", err)
	}
	movies, err := pgx.CollectRows(rows, scanMovie)
	if err != nil {
		return nil, 0, fmt.Errorf("listing movies: %w", err)
	}

	// A separate count, not count(*) OVER (): the window total is lost when
	// the offset is past the last row, and clients need it to stop paging.
	var total int64
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM movies `+movieWhere, status, title, f.YearFrom, f.YearTo,
	).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("counting movies: %w", err)
	}
	return movies, total, nil
}

// GetMovie returns one movie, or ErrNotFound. id must already be a valid UUID.
func (s *Store) GetMovie(ctx context.Context, id string) (model.Movie, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+movieColumns+` FROM movies WHERE id = $1`, id)
	if err != nil {
		return model.Movie{}, fmt.Errorf("getting movie: %w", err)
	}
	m, err := pgx.CollectExactlyOneRow(rows, scanMovie)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Movie{}, ErrNotFound
	}
	if err != nil {
		return model.Movie{}, fmt.Errorf("getting movie: %w", err)
	}
	return m, nil
}

// ListRenditions returns a movie's rungs, tallest first.
func (s *Store) ListRenditions(ctx context.Context, movieID string) ([]model.Rendition, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id::text, movie_id::text, height, bitrate_kbps, playlist_path,
		       size_bytes, created_at, updated_at
		FROM renditions
		WHERE movie_id = $1
		ORDER BY height DESC`, movieID)
	if err != nil {
		return nil, fmt.Errorf("listing renditions: %w", err)
	}
	rs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (model.Rendition, error) {
		var r model.Rendition
		err := row.Scan(&r.ID, &r.MovieID, &r.Height, &r.BitrateKbps, &r.PlaylistPath,
			&r.SizeBytes, &r.CreatedAt, &r.UpdatedAt)
		return r, err
	})
	if err != nil {
		return nil, fmt.Errorf("listing renditions: %w", err)
	}
	return rs, nil
}

func scanMovie(row pgx.CollectableRow) (model.Movie, error) {
	var m model.Movie
	var status string
	err := row.Scan(&m.ID, &m.Title, &m.Year, &m.TMDBID, &m.Overview, &m.RuntimeSec,
		&m.SourcePath, &m.LibraryPath, &m.PosterPath, &status, &m.LibraryCleanupAfter, &m.CreatedAt, &m.UpdatedAt)
	m.Status = model.MovieStatus(status)
	return m, err
}
