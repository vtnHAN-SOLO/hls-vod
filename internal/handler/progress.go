package handler

import (
	"encoding/json"
	"log/slog"
	"mime"
	"net/http"
	"time"
)

const maxProgressBody = 1 << 10

// progressRequest has no user field: the user is always the access
// token's subject, so nobody can write someone else's progress.
type progressRequest struct {
	MovieID     string `json:"movie_id"`
	PositionSec *int64 `json:"position_sec"`
}

type progressJSON struct {
	Movie       movieJSON `json:"movie"`
	PositionSec int       `json:"position_sec"`
	RuntimeSec  *int      `json:"runtime_sec"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type continueWatchingJSON struct {
	Items []progressJSON `json:"items"`
}

func saveProgress(logger *slog.Logger, c Catalog) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
			writeError(w, http.StatusUnsupportedMediaType, codeUnsupportedMediaType, "Content-Type must be application/json")
			return
		}
		var req progressRequest
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxProgressBody))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil || req.PositionSec == nil {
			writeError(w, http.StatusBadRequest, codeInvalidRequest,
				`body must be {"movie_id": "<uuid>", "position_sec": <whole seconds>}`)
			return
		}
		if err := c.SaveProgress(r.Context(), principal(r), req.MovieID, *req.PositionSec); err != nil {
			writeServiceError(w, r, logger, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func continueWatching(logger *slog.Logger, c Catalog) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ps, err := c.ContinueWatching(r.Context(), principal(r))
		if err != nil {
			writeServiceError(w, r, logger, err)
			return
		}
		out := continueWatchingJSON{Items: make([]progressJSON, 0, len(ps))}
		for _, p := range ps {
			out.Items = append(out.Items, progressJSON{
				Movie: toMovieJSON(c, p.Movie), PositionSec: p.PositionSec,
				RuntimeSec: p.Movie.RuntimeSec, UpdatedAt: p.UpdatedAt.UTC(),
			})
		}
		// Per user: never cached by anything between the api and the browser.
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, out)
	}
}
