package handler

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/vtnHAN-SOLO/hls-vod/internal/poster"
	"github.com/vtnHAN-SOLO/hls-vod/internal/service"
)

// Poster and publishing error codes.
const (
	codeInvalidImage   = "invalid_image"
	codeTooLarge       = "too_large"
	codeNotPublished   = "not_published"
	codeMoviePublished = "movie_published"
	codeExpired        = "expired"
)

// posterJSON is a movie's uploaded portrait poster; null when it has none.
type posterJSON struct {
	Hash     string              `json:"hash"`
	Variants []posterVariantJSON `json:"variants"`
}

type posterVariantJSON struct {
	Width  int    `json:"width"`
	Height int    `json:"height"`
	URL    string `json:"url"`
}

func toPosterJSON(p *service.PosterView) *posterJSON {
	if p == nil {
		return nil
	}
	out := &posterJSON{Hash: p.Hash, Variants: make([]posterVariantJSON, 0, len(p.Variants))}
	for _, v := range p.Variants {
		out.Variants = append(out.Variants, posterVariantJSON{Width: v.Width, Height: v.Height, URL: v.URL})
	}
	return out
}

// servePoster answers a signed poster URL from the database. No access
// token: an <img> cannot send one, so the URL's signature is the
// permission, as for /media/. 403 for a bad signature, 410 once expired,
// 404 for a poster that is not there (any longer). A new upload changes the
// hash and so the URL, which is why the image may be cached as immutable
// until the URL expires.
func servePoster(logger *slog.Logger, c Catalog) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		img, err := c.PosterImage(r.Context(), r.PathValue("id"), r.PathValue("width"), r.PathValue("file"),
			q.Get("exp"), q.Get("sig"))
		switch {
		case errors.Is(err, service.ErrBadSignature):
			writeError(w, http.StatusForbidden, codeForbidden, "missing or invalid signature")
			return
		case errors.Is(err, service.ErrExpired):
			writeError(w, http.StatusGone, codeExpired, "this poster URL has expired")
			return
		case errors.Is(err, service.ErrNotFound):
			writeError(w, http.StatusNotFound, codeNotFound, "poster not found")
			return
		case err != nil:
			writeServiceError(w, r, logger, err)
			return
		}
		etag := `"` + img.Hash + `"`
		maxAge := max(int64(time.Until(img.Expires).Seconds()), 0)
		h := w.Header()
		// private: the URL is a permission for one viewer, never kept by a
		// shared cache.
		h.Set("Cache-Control", "private, max-age="+strconv.FormatInt(maxAge, 10)+", immutable")
		h.Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		h.Set("Content-Type", "image/jpeg")
		h.Set("Content-Length", strconv.Itoa(len(img.Bytes)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(img.Bytes)
	}
}

// adminUploadPoster takes the raw image as the body (any Content-Type: the
// type is read from the bytes), at most poster.MaxUploadBytes.
func adminUploadPoster(logger *slog.Logger, a Admin, c Catalog) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// A non-admin is refused before 10 MB are read.
		if err := a.RequireAdmin(r.Context(), principal(r)); err != nil {
			writeAdminError(w, r, logger, err)
			return
		}
		if r.ContentLength > poster.MaxUploadBytes {
			writeError(w, http.StatusRequestEntityTooLarge, codeTooLarge, poster.ErrTooLarge.Error())
			return
		}
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, poster.MaxUploadBytes))
		var tooBig *http.MaxBytesError
		switch {
		case errors.As(err, &tooBig):
			writeError(w, http.StatusRequestEntityTooLarge, codeTooLarge, poster.ErrTooLarge.Error())
			return
		case err != nil:
			writeError(w, http.StatusBadRequest, codeInvalidRequest, "could not read the upload")
			return
		}
		m, err := a.UploadPoster(r.Context(), principal(r), r.PathValue("id"), data)
		switch {
		case errors.Is(err, poster.ErrTooLarge):
			writeError(w, http.StatusRequestEntityTooLarge, codeTooLarge, err.Error())
		case errors.Is(err, poster.ErrUnsupported), errors.Is(err, poster.ErrTooSmall),
			errors.Is(err, poster.ErrTooManyPx), errors.Is(err, poster.ErrCorrupt):
			writeError(w, http.StatusBadRequest, codeInvalidImage, err.Error())
		case err != nil:
			writeAdminError(w, r, logger, err)
		default:
			writeAdminJSON(w, toAdminMovieJSON(c, m))
		}
	}
}

// adminNoContent answers 204 after act.
func adminNoContent(logger *slog.Logger, act adminAction) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := act(r.Context(), principal(r), r.PathValue("id")); err != nil {
			writeAdminError(w, r, logger, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusNoContent)
	}
}
