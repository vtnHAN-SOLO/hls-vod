package handler

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
	"github.com/vtnHAN-SOLO/hls-vod/internal/service"
)

// Catalog is the service behind the movie endpoints. *service.Catalog
// satisfies it.
type Catalog interface {
	ListMovies(ctx context.Context, v service.Principal, p service.ListParams) (service.MoviePage, error)
	GetMovie(ctx context.Context, v service.Principal, id string) (service.MovieDetail, error)
	Playback(ctx context.Context, v service.Principal, id string) (service.Playback, error)
	SaveProgress(ctx context.Context, v service.Principal, movieID string, positionSec int64) error
	ContinueWatching(ctx context.Context, v service.Principal) ([]model.Progress, error)
	Popular(ctx context.Context, v service.Principal, window string, limit int64) ([]model.Popular, error)
	Featured(ctx context.Context, v service.Principal) ([]model.Movie, bool, error)
	BackdropURL(m model.Movie) *string
	Poster(m model.Movie) *service.PosterView
	PosterImage(ctx context.Context, movieID, width, file, exp, sig string) (service.PosterImage, error)
}

// movieJSON is the public view of a movie. Filesystem paths (source_path,
// library_path) are internal and never leave the api. Nullable columns are
// always present, as null.
type movieJSON struct {
	ID         string  `json:"id"`
	Title      string  `json:"title"`
	Year       *int    `json:"year"`
	TMDBID     *int    `json:"tmdb_id"`
	Overview   *string `json:"overview"`
	RuntimeSec *int    `json:"runtime_sec"`
	Status     string  `json:"status"`
	// BackdropURL is the landscape video frame; Poster the uploaded
	// portrait poster.
	BackdropURL *string     `json:"backdrop_url"`
	Poster      *posterJSON `json:"poster"`
	TopHeight   *int        `json:"top_height"` // the tallest rendition, null with none
	CreatedAt   time.Time   `json:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at"`
}

type popularJSON struct {
	movieJSON
	Viewers int `json:"viewers"`
}

type popularListJSON struct {
	Window string        `json:"window"`
	Items  []popularJSON `json:"items"`
}

type featuredListJSON struct {
	// Featured is false when no movie is featured and the items are the
	// newest ready movies instead.
	Featured bool        `json:"featured"`
	Items    []movieJSON `json:"items"`
}

type movieListJSON struct {
	Items  []movieJSON `json:"items"`
	Total  int64       `json:"total"`
	Limit  int64       `json:"limit"`
	Offset int64       `json:"offset"`
}

type movieDetailJSON struct {
	movieJSON
	Renditions        []renditionJSON `json:"renditions"`
	SubtitleLanguages []string        `json:"subtitle_languages"`
}

type subtitleJSON struct {
	URL      string  `json:"url"`
	Language string  `json:"language"`
	Label    *string `json:"label"`
}

type renditionJSON struct {
	Height      int    `json:"height"`
	BitrateKbps *int   `json:"bitrate_kbps"`
	SizeBytes   *int64 `json:"size_bytes"`
}

type playbackJSON struct {
	MovieID       string         `json:"movie_id"`
	PlaylistURL   string         `json:"playlist_url"`
	ThumbnailsURL string         `json:"thumbnails_url"`
	ResumeSec     *int           `json:"resume_sec"`
	Subtitles     []subtitleJSON `json:"subtitles"`
}

func listMovies(logger *slog.Logger, c Catalog) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		limit, err := intParam(q, "limit", service.DefaultLimit)
		if err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidRequest, err.Error())
			return
		}
		offset, err := intParam(q, "offset", 0)
		if err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidRequest, err.Error())
			return
		}
		search, err := stringParam(q, "q")
		if err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidRequest, err.Error())
			return
		}
		sort, err := stringParam(q, "sort")
		if err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidRequest, err.Error())
			return
		}
		yearFrom, err := optionalIntParam(q, "year_from")
		if err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidRequest, err.Error())
			return
		}
		yearTo, err := optionalIntParam(q, "year_to")
		if err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidRequest, err.Error())
			return
		}
		page, err := c.ListMovies(r.Context(), principal(r), service.ListParams{
			Status: q.Get("status"), Query: search, Sort: sort, YearFrom: yearFrom, YearTo: yearTo,
			Limit: limit, Offset: offset,
		})
		if err != nil {
			writeServiceError(w, r, logger, err)
			return
		}

		out := movieListJSON{Items: make([]movieJSON, 0, len(page.Movies)),
			Total: page.Total, Limit: page.Limit, Offset: page.Offset}
		for _, m := range page.Movies {
			out.Items = append(out.Items, toMovieJSON(c, m))
		}
		// Signed poster URLs: never stored by a shared cache.
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, out)
	}
}

func getMovie(logger *slog.Logger, c Catalog) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		d, err := c.GetMovie(r.Context(), principal(r), r.PathValue("id"))
		if err != nil {
			writeServiceError(w, r, logger, err)
			return
		}
		out := movieDetailJSON{movieJSON: toMovieJSON(c, d.Movie), Renditions: make([]renditionJSON, 0, len(d.Renditions)),
			SubtitleLanguages: d.SubtitleLanguages}
		if out.SubtitleLanguages == nil {
			out.SubtitleLanguages = []string{}
		}
		for _, rd := range d.Renditions {
			out.Renditions = append(out.Renditions, renditionJSON{
				Height: rd.Height, BitrateKbps: rd.BitrateKbps, SizeBytes: rd.SizeBytes,
			})
		}
		w.Header().Set("Cache-Control", "no-store") // a signed poster URL
		writeJSON(w, http.StatusOK, out)
	}
}

func playback(logger *slog.Logger, c Catalog) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, err := c.Playback(r.Context(), principal(r), r.PathValue("id"))
		if err != nil {
			writeServiceError(w, r, logger, err)
			return
		}
		// The URLs carry a signed token: never stored by a shared cache.
		w.Header().Set("Cache-Control", "no-store")
		subs := make([]subtitleJSON, 0, len(p.Subtitles))
		for _, s := range p.Subtitles {
			subs = append(subs, subtitleJSON{URL: s.URL, Language: s.Language, Label: s.Label})
		}
		writeJSON(w, http.StatusOK, playbackJSON{
			MovieID: p.MovieID, PlaylistURL: p.PlaylistURL, ThumbnailsURL: p.ThumbnailsURL,
			ResumeSec: p.ResumeSec, Subtitles: subs,
		})
	}
}

func popularMovies(logger *slog.Logger, c Catalog) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		window, err := stringParam(q, "window")
		if err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidRequest, err.Error())
			return
		}
		limit, err := intParam(q, "limit", service.DefaultPopularLimit)
		if err != nil {
			writeError(w, http.StatusBadRequest, codeInvalidRequest, err.Error())
			return
		}
		ps, err := c.Popular(r.Context(), principal(r), window, limit)
		if err != nil {
			writeServiceError(w, r, logger, err)
			return
		}
		if window == "" {
			window = "week"
		}
		out := popularListJSON{Window: window, Items: make([]popularJSON, 0, len(ps))}
		for _, p := range ps {
			out.Items = append(out.Items, popularJSON{movieJSON: toMovieJSON(c, p.Movie), Viewers: p.Viewers})
		}
		w.Header().Set("Cache-Control", "no-store") // signed poster URLs
		writeJSON(w, http.StatusOK, out)
	}
}

func featuredMovies(logger *slog.Logger, c Catalog) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ms, featured, err := c.Featured(r.Context(), principal(r))
		if err != nil {
			writeServiceError(w, r, logger, err)
			return
		}
		out := featuredListJSON{Featured: featured, Items: make([]movieJSON, 0, len(ms))}
		for _, m := range ms {
			out.Items = append(out.Items, toMovieJSON(c, m))
		}
		w.Header().Set("Cache-Control", "no-store") // signed poster URLs
		writeJSON(w, http.StatusOK, out)
	}
}

func toMovieJSON(c Catalog, m model.Movie) movieJSON {
	out := movieJSON{
		ID: m.ID, Title: m.Title, Year: m.Year, TMDBID: m.TMDBID, Overview: m.Overview,
		RuntimeSec: m.RuntimeSec, Status: string(m.Status), TopHeight: m.TopHeight,
		// UTC, so the output does not depend on the server's timezone.
		CreatedAt: m.CreatedAt.UTC(), UpdatedAt: m.UpdatedAt.UTC(),
		BackdropURL: c.BackdropURL(m), Poster: toPosterJSON(c.Poster(m)),
	}
	return out
}

// stringParam returns an optional query parameter, "" when absent. It rejects
// a parameter given more than once; trimming and length rules belong to the
// service.
func stringParam(q url.Values, name string) (string, error) {
	s, ok := q[name]
	if !ok {
		return "", nil
	}
	if len(s) != 1 {
		return "", &service.ValidationError{Field: name, Message: "must be given once"}
	}
	return s[0], nil
}

// optionalIntParam is intParam without a default: nil when absent or empty
// (a form submits an empty year field as "year_from=").
func optionalIntParam(q url.Values, name string) (*int64, error) {
	if s, ok := q[name]; !ok || (len(s) == 1 && s[0] == "") {
		return nil, nil
	}
	v, err := intParam(q, name, 0)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// intParam parses an optional integer query parameter, def when absent. It
// rejects a parameter given more than once or text that is not an integer;
// range rules belong to the service.
func intParam(q url.Values, name string, def int64) (int64, error) {
	s, ok := q[name]
	if !ok {
		return def, nil
	}
	if len(s) != 1 {
		return 0, &service.ValidationError{Field: name, Message: "must be given once"}
	}
	v, err := strconv.ParseInt(s[0], 10, 64)
	if err != nil {
		return 0, &service.ValidationError{Field: name, Message: "must be an integer"}
	}
	return v, nil
}
