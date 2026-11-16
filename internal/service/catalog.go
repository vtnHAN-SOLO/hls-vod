// Package service holds business logic. It knows nothing about HTTP or SQL:
// handlers call it, and it calls the store through small interfaces.
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/vtnHAN-SOLO/hls-vod/internal/medialayout"
	"github.com/vtnHAN-SOLO/hls-vod/internal/mediapath"
	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
	"github.com/vtnHAN-SOLO/hls-vod/internal/poster"
	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
)

// Pagination and search rules for movie lists.
const (
	DefaultLimit   = 20
	MaxLimit       = 100
	MaxQueryLength = 100 // characters in a title search
	// MinYear and MaxYear bound year_from and year_to.
	MinYear = 1880
	MaxYear = 2100
)

// Sorts accepted by ListMovies, in the spelling of the API.
var movieSorts = map[string]store.MovieSort{
	"added_desc": store.SortAddedDesc,
	"title_asc":  store.SortTitleAsc,
	"year_desc":  store.SortYearDesc,
	"year_asc":   store.SortYearAsc,
}

// MediaPrefix is where media-delivery serves library/{id}/ (a separate
// Ingress path; the api never serves segments). Every URL under it is
// signed: see MediaSigner.
const MediaPrefix = "/media/"

var (
	// ErrNotFound means the requested movie does not exist.
	ErrNotFound = errors.New("not found")
	// ErrNotReady means the movie exists but cannot be played yet.
	ErrNotReady = errors.New("not ready")
)

// ValidationError is a bad input the client can fix.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Message }

// NotReadyError carries the status of a movie that cannot be played.
type NotReadyError struct{ Status model.MovieStatus }

func (e *NotReadyError) Error() string { return fmt.Sprintf("movie is %s, not ready", e.Status) }

// Is makes errors.Is(err, ErrNotReady) true.
func (e *NotReadyError) Is(target error) bool { return target == ErrNotReady }

// CatalogStore is the slice of the store the catalog needs.
type CatalogStore interface {
	ListMovies(ctx context.Context, f store.MovieFilter) ([]model.Movie, int64, error)
	GetMovie(ctx context.Context, id string) (model.Movie, error)
	ListRenditions(ctx context.Context, movieID string) ([]model.Rendition, error)
	ListSubtitles(ctx context.Context, movieID string) ([]model.SubtitleTrack, error)
	SaveProgress(ctx context.Context, userID, movieID string, positionSec int) error
	GetProgress(ctx context.Context, userID, movieID string) (positionSec int, ok bool, err error)
	ContinueWatching(ctx context.Context, userID string, finishedPercent, limit int) ([]model.Progress, error)
	PopularMovies(ctx context.Context, since *time.Time, limit int) ([]model.Popular, error)
	FeaturedMovies(ctx context.Context, limit int) ([]model.Movie, error)
}

// Catalog serves the movie endpoints, each user's watch progress and the
// poster images.
type Catalog struct {
	store   CatalogStore
	posters poster.Store
	media   *MediaSigner
	now     func() time.Time
}

// NewCatalog returns a Catalog over st and posters whose media and poster
// URLs media signs.
func NewCatalog(st CatalogStore, posters poster.Store, media *MediaSigner) *Catalog {
	return &Catalog{store: st, posters: posters, media: media, now: time.Now}
}

// ListParams selects a page. Status "" means every status; Query "" means
// any title; Sort "" means added_desc; a nil year means no bound.
type ListParams struct {
	Status   string
	Query    string // case-insensitive title substring, matched literally
	Sort     string // added_desc | title_asc | year_desc | year_asc
	YearFrom *int64 // inclusive; excludes movies with no year
	YearTo   *int64 // inclusive; excludes movies with no year
	Limit    int64
	Offset   int64
}

// MoviePage is one page of the catalog.
type MoviePage struct {
	Movies []model.Movie
	Total  int64
	Limit  int64
	Offset int64
}

// MovieDetail is a movie and its renditions, tallest first, and the
// languages of its subtitles (BCP 47, in stream order, each once).
type MovieDetail struct {
	Movie             model.Movie
	Renditions        []model.Rendition
	SubtitleLanguages []string
}

// PlaybackSubtitle is one subtitle track the player can load.
type PlaybackSubtitle struct {
	URL      string  // signed WebVTT URL, valid as long as the playlist URL
	Language string  // BCP 47 ("en", "vi", "und"), for <track srclang> and display names
	Label    *string // the stream's own title ("SDH", "Commentary"); nil when none
}

// Playback says where the player loads the movie from. Every URL comes
// from here, so the player never hard-codes the library layout. The URLs
// are signed for PlaybackURLTTL.
type Playback struct {
	MovieID       string
	PlaylistURL   string
	ThumbnailsURL string // WebVTT index of the seek-preview sprites
	// ResumeSec is where the caller stopped last time; nil when there is
	// nothing to resume (no progress, or the movie was watched to the end).
	ResumeSec *int
	// Subtitles in source order; a track whose path makes no safe URL is
	// left out.
	Subtitles []PlaybackSubtitle
}

// ListMovies validates p and returns one page. An admin sees every status
// and may filter by it; anyone else sees published (so ready) movies only,
// and asking for a status filter is ErrForbidden.
func (c *Catalog) ListMovies(ctx context.Context, v Principal, p ListParams) (MoviePage, error) {
	if p.Status != "" && !v.Admin() {
		return MoviePage{}, fmt.Errorf("%w: filtering by status is for admins", ErrForbidden)
	}
	if p.Limit < 1 || p.Limit > MaxLimit {
		return MoviePage{}, &ValidationError{"limit", fmt.Sprintf("must be between 1 and %d", MaxLimit)}
	}
	if p.Offset < 0 {
		return MoviePage{}, &ValidationError{"offset", "must not be negative"}
	}
	f := store.MovieFilter{Limit: p.Limit, Offset: p.Offset}
	if p.Sort != "" {
		sort, ok := movieSorts[p.Sort]
		if !ok {
			return MoviePage{}, &ValidationError{"sort", "must be one of added_desc, title_asc, year_desc, year_asc"}
		}
		f.Sort = sort
	}
	var err error
	if f.YearFrom, err = yearBound("year_from", p.YearFrom); err != nil {
		return MoviePage{}, err
	}
	if f.YearTo, err = yearBound("year_to", p.YearTo); err != nil {
		return MoviePage{}, err
	}
	if f.YearFrom != nil && f.YearTo != nil && *f.YearFrom > *f.YearTo {
		return MoviePage{}, &ValidationError{"year_from", "must not be after year_to"}
	}
	if p.Status != "" {
		s := model.MovieStatus(p.Status)
		if !s.Valid() {
			return MoviePage{}, &ValidationError{"status", "must be one of pending, processing, ready, failed, deleting"}
		}
		f.Status = &s
	}
	if !v.Admin() {
		ready := model.StatusReady
		f.Status = &ready
		f.PublishedOnly = true
	}
	if q := strings.TrimSpace(p.Query); q != "" {
		// Postgres rejects NUL and invalid UTF-8 in text: a 400, not a 500.
		if !utf8.ValidString(q) || strings.ContainsRune(q, 0) {
			return MoviePage{}, &ValidationError{"q", "must be valid UTF-8 text"}
		}
		if utf8.RuneCountInString(q) > MaxQueryLength {
			return MoviePage{}, &ValidationError{"q", fmt.Sprintf("must be at most %d characters", MaxQueryLength)}
		}
		f.Title = &q
	}

	movies, total, err := c.store.ListMovies(ctx, f)
	if err != nil {
		return MoviePage{}, err
	}
	return MoviePage{Movies: movies, Total: total, Limit: p.Limit, Offset: p.Offset}, nil
}

// yearBound checks one year bound: nil stays nil, anything else must lie in
// MinYear..MaxYear.
func yearBound(field string, y *int64) (*int, error) {
	if y == nil {
		return nil, nil
	}
	if *y < MinYear || *y > MaxYear {
		return nil, &ValidationError{field, fmt.Sprintf("must be between %d and %d", MinYear, MaxYear)}
	}
	v := int(*y)
	return &v, nil
}

// GetMovie returns a movie with its renditions. A movie that is not
// published does not exist for a non-admin (ErrNotFound).
func (c *Catalog) GetMovie(ctx context.Context, v Principal, id string) (MovieDetail, error) {
	m, err := c.visibleMovie(ctx, v, id)
	if err != nil {
		return MovieDetail{}, err
	}
	rs, err := c.store.ListRenditions(ctx, m.ID)
	if err != nil {
		return MovieDetail{}, err
	}
	subs, err := c.store.ListSubtitles(ctx, m.ID)
	if err != nil {
		return MovieDetail{}, err
	}
	langs := []string{}
	for _, s := range subs {
		if _, err := mediapath.Rel(s.Path); err != nil {
			continue // playback leaves it out too
		}
		if l := model.SubtitleLanguage(s.Language); !slices.Contains(langs, l) {
			langs = append(langs, l)
		}
	}
	return MovieDetail{Movie: m, Renditions: rs, SubtitleLanguages: langs}, nil
}

// Playback returns the signed master playlist URL of a ready movie:
// ErrNotFound for an unknown id, a *NotReadyError (errors.Is ErrNotReady)
// otherwise.
//
// For a non-admin a movie that is not published is ErrNotFound, as in
// GetMovie; an admin may play an unpublished ready movie (to check it before
// publishing) and gets the *NotReadyError for one that is not ready.
func (c *Catalog) Playback(ctx context.Context, v Principal, id string) (Playback, error) {
	m, err := c.visibleMovie(ctx, v, id)
	if err != nil {
		return Playback{}, err
	}
	if m.Status != model.StatusReady {
		return Playback{}, &NotReadyError{Status: m.Status}
	}
	version, err := currentVersion(m)
	if err != nil {
		return Playback{}, err
	}
	resume, err := c.resumeAt(ctx, v, m)
	if err != nil {
		return Playback{}, err
	}
	p := Playback{
		MovieID:       m.ID,
		PlaylistURL:   c.media.URL(m.ID, medialayout.InVersion(version, medialayout.MasterPlaylist), PlaybackURLTTL),
		ThumbnailsURL: c.media.URL(m.ID, medialayout.InVersion(version, medialayout.ThumbnailsVTT), PlaybackURLTTL),
		ResumeSec:     resume,
	}
	if p.PlaylistURL == "" || p.ThumbnailsURL == "" {
		return Playback{}, fmt.Errorf("movie %s: version %q makes no safe media URL", m.ID, version)
	}
	subs, err := c.store.ListSubtitles(ctx, m.ID)
	if err != nil {
		return Playback{}, err
	}
	p.Subtitles = make([]PlaybackSubtitle, 0, len(subs))
	for _, s := range subs {
		// The same token as the playlist: it covers library/{id}/.
		u := c.media.URL(m.ID, s.Path, PlaybackURLTTL)
		if u == "" {
			continue
		}
		p.Subtitles = append(p.Subtitles, PlaybackSubtitle{URL: u, Language: model.SubtitleLanguage(s.Language), Label: s.Label})
	}
	return p, nil
}

// currentVersion is the version folder the movie plays from, read from its
// stored library_path ("library/{id}/{version}"), never assumed. "" is the
// old layout from before versioned folders, files directly in library/{id}/ (also a
// library_path that is not set). Anything else is refused, not guessed.
func currentVersion(m model.Movie) (string, error) {
	if m.LibraryPath == nil {
		return "", nil
	}
	v, ok := medialayout.Version(m.ID, *m.LibraryPath)
	if ok && v != "" {
		_, err := mediapath.Rel(v)
		ok = err == nil
	}
	if !ok {
		return "", fmt.Errorf("movie %s: library_path %q is not library/{id}[/{version}]", m.ID, *m.LibraryPath)
	}
	return v, nil
}

// BackdropURL is the signed URL of the movie's backdrop (the landscape
// video frame the transcoder writes), valid for PosterURLTTL; nil when it
// has none or its backdrop_path is unsafe.
func (c *Catalog) BackdropURL(m model.Movie) *string {
	if m.BackdropPath == nil {
		return nil
	}
	u := c.media.URL(m.ID, *m.BackdropPath, PosterURLTTL)
	if u == "" {
		return nil
	}
	return &u
}

// PosterVariant is one signed poster size.
type PosterVariant struct {
	Width  int
	Height int
	URL    string
}

// PosterView is a movie's uploaded portrait poster: its hash (lowercase hex)
// and the signed URL of every variant, smallest first.
type PosterView struct {
	Hash     string
	Variants []PosterVariant
}

// Poster is the movie's poster with signed URLs, nil when it has none. Only
// movies the caller may see reach a response, so only those get URLs.
func (c *Catalog) Poster(m model.Movie) *PosterView {
	if len(m.PosterHash) != sha256.Size || len(m.PosterWidths) == 0 {
		return nil
	}
	h := hex.EncodeToString(m.PosterHash)
	pv := &PosterView{Hash: h, Variants: make([]PosterVariant, 0, len(m.PosterWidths))}
	for _, w := range m.PosterWidths {
		pv.Variants = append(pv.Variants, PosterVariant{Width: w, Height: poster.HeightFor(w), URL: c.media.PosterURL(m.ID, w, h)})
	}
	return pv
}

// PosterImage is one poster variant's JPEG, for a URL Poster signed.
type PosterImage struct {
	Bytes   []byte
	Hash    string // lowercase hex: the ETag
	Expires time.Time
}

// PosterImage checks a poster URL and returns its image: ErrNotFound for a
// path no URL could have (not a UUID, an unknown width, a file that is not
// {hash}.jpg) or for a poster that is not there (any longer: a new upload
// changes the hash); ErrBadSignature or ErrExpired from the signature.
func (c *Catalog) PosterImage(ctx context.Context, movieID, width, file, exp, sig string) (PosterImage, error) {
	w, err := strconv.Atoi(width)
	hash, isJPG := strings.CutSuffix(file, ".jpg")
	if err != nil || !model.ValidID(movieID) || !poster.ValidWidth(w) || strconv.Itoa(w) != width ||
		!isJPG || !validHexHash(hash) {
		return PosterImage{}, ErrNotFound
	}
	expires, err := c.media.CheckPoster(movieID, w, hash, exp, sig)
	if err != nil {
		return PosterImage{}, err
	}
	raw, _ := hex.DecodeString(hash) // validHexHash checked it
	b, err := c.posters.Get(ctx, movieID, w, raw)
	if errors.Is(err, poster.ErrNotFound) {
		return PosterImage{}, ErrNotFound
	}
	if err != nil {
		return PosterImage{}, err
	}
	return PosterImage{Bytes: b, Hash: hash, Expires: expires}, nil
}

// validHexHash: a SHA-256 in lowercase hex, the only spelling URLs use.
func validHexHash(s string) bool {
	if len(s) != 2*sha256.Size {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// visibleMovie is movie, with unpublished movies hidden from non-admins.
func (c *Catalog) visibleMovie(ctx context.Context, v Principal, id string) (model.Movie, error) {
	m, err := c.movie(ctx, id)
	if err == nil && !v.Admin() && (m.Status != model.StatusReady || !m.Published()) {
		return model.Movie{}, ErrNotFound
	}
	return m, err
}

func (c *Catalog) movie(ctx context.Context, id string) (model.Movie, error) {
	id = strings.ToLower(id)
	if !model.ValidID(id) {
		return model.Movie{}, &ValidationError{"id", "must be a UUID"}
	}
	m, err := c.store.GetMovie(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return model.Movie{}, ErrNotFound
	}
	return m, err
}
