package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/vtnHAN-SOLO/hls-vod/internal/ingest"
	"github.com/vtnHAN-SOLO/hls-vod/internal/medialayout"
	"github.com/vtnHAN-SOLO/hls-vod/internal/mediapath"
	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
	"github.com/vtnHAN-SOLO/hls-vod/internal/poster"
	"github.com/vtnHAN-SOLO/hls-vod/internal/service"
	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
)

// errUsage is a bad argument: main prints the usage line.
var errUsage = errors.New("usage")

// catalog is the slice of the store devingest needs.
type catalog interface {
	CreateMovie(ctx context.Context, m store.NewMovie, rs []store.NewRendition) error
}

type request struct {
	MediaRoot string
	Input     string
	Title     string
	Year      int    // 0: unknown; else service.MinYear..service.MaxYear
	Overview  string // "": none; at most service.MaxOverviewLength characters
	// Poster is an image file processed as an admin upload would be (2:3
	// crop, JPEG variants); "": none. Publish publishes the movie, which
	// needs a poster and an overview.
	Poster  string
	Publish bool

	// transcode is the pipeline options for (input, output); nil means
	// transcode.DefaultOptions. Tests shrink the poster and thumbnails.
	transcode ingest.OptionsFunc
}

// ingestMovie runs one movie end to end and returns its id, with the same layout
// the watcher uses: the original as sources/{id}.{ext} (the file name is
// never rewritten; TITLE is the title) and the HLS output in library/{id}/.
// On any error it removes what it wrote, and no rows are written unless the
// transcode succeeded.
//
// It transcodes straight from INPUT and copies into sources/ only once the
// transcode is done, so the copy waits seconds, not minutes, for its row: a
// running watcher returns a sources/ file with no row to the inbox once it
// is 10 minutes old.
func ingestMovie(ctx context.Context, log *slog.Logger, cat catalog, req request) (id string, err error) {
	title := strings.TrimSpace(req.Title)
	if req.Input == "" || title == "" {
		return "", fmt.Errorf("%w: INPUT and TITLE are required", errUsage)
	}
	var year *int
	if req.Year != 0 {
		if req.Year < service.MinYear || req.Year > service.MaxYear {
			return "", fmt.Errorf("%w: YEAR must be between %d and %d", errUsage, service.MinYear, service.MaxYear)
		}
		year = &req.Year
	}
	var overview *string
	if o := strings.TrimSpace(req.Overview); o != "" {
		if utf8.RuneCountInString(o) > service.MaxOverviewLength {
			return "", fmt.Errorf("%w: OVERVIEW must be at most %d characters", errUsage, service.MaxOverviewLength)
		}
		overview = &o
	}
	if req.Publish && (req.Poster == "" || overview == nil) {
		return "", fmt.Errorf("%w: PUBLISH needs POSTER and OVERVIEW: a published movie has both", errUsage)
	}
	// The poster first: a refused image fails in a second, not after the
	// transcode.
	var pst *poster.Poster
	if req.Poster != "" {
		if pst, err = readPoster(ctx, req.Poster); err != nil {
			return "", err
		}
	}
	input, err := expandHome(req.Input)
	if err != nil {
		return "", err
	}
	fi, err := os.Stat(input)
	if err != nil {
		return "", fmt.Errorf("input: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("input %s is not a regular file", input)
	}
	ext, ok := medialayout.VideoExt(filepath.Base(input))
	if !ok {
		return "", fmt.Errorf("input %s: not a video file the watcher would take (mkv, mp4, m4v, mov, avi, webm, ts, m2ts)", input)
	}

	root, err := filepath.Abs(req.MediaRoot)
	if err != nil {
		return "", fmt.Errorf("resolving MEDIA_ROOT: %w", err)
	}
	sourcesDir, err := mediapath.Join(root, medialayout.Sources)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(sourcesDir, 0o755); err != nil { // #nosec G301 -- the transcoder (another UID) must traverse the media tree
		return "", fmt.Errorf("creating %s: %w", sourcesDir, err)
	}

	id = model.NewID()
	source := medialayout.SourcePath(id, ext)
	if _, err := mediapath.Rel(source); err != nil {
		return "", err
	}
	sourcePath, err := mediapath.Join(root, source)
	if err != nil {
		return "", err
	}

	// No job: a fresh id names the version.
	pub, removeLibrary, err := ingest.Library(ctx, log, root, id, model.NewID(), input, req.transcode)
	if err != nil {
		return "", err
	}
	defer func() {
		if err != nil {
			removeLibrary()
		}
	}()

	if err := copyFile(input, sourcePath); err != nil {
		return "", err
	}
	defer func() {
		if err != nil {
			removeLogged(log, sourcePath)
		}
	}()
	log.Info("copied the original", "source_path", source)

	movie := store.NewMovie{
		ID: id, Title: title, SourcePath: source, LibraryPath: pub.LibraryPath,
		BackdropPath: pub.BackdropPath, RuntimeSec: pub.RuntimeSec, Status: model.StatusReady,
		Year: year, Overview: overview, Subtitles: pub.Subtitles, Poster: pst, Publish: req.Publish,
	}
	rungs := pub.Renditions
	if err := cat.CreateMovie(ctx, movie, rungs); err != nil {
		return "", err
	}
	log.Info("movie ready", "id", id, "title", title, "source_path", source, "library_path", pub.LibraryPath,
		"renditions", len(rungs), "poster", pst != nil, "published", req.Publish)
	return id, nil
}

// readPoster processes an image file with internal/poster, the same rules
// as an admin upload.
func readPoster(ctx context.Context, path string) (*poster.Poster, error) {
	path, err := expandHome(path)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("poster: %w", err)
	}
	if fi.Size() > poster.MaxUploadBytes {
		return nil, fmt.Errorf("poster %s: %w", path, poster.ErrTooLarge)
	}
	data, err := os.ReadFile(path) // #nosec G304 -- a path the developer typed on the command line
	if err != nil {
		return nil, fmt.Errorf("poster: %w", err)
	}
	p, err := poster.NewProcessor().Process(ctx, data)
	if err != nil {
		return nil, fmt.Errorf("poster %s: %w", path, err)
	}
	return &p, nil
}

func expandHome(p string) (string, error) {
	rest, ok := strings.CutPrefix(p, "~/")
	if !ok {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("expanding ~: %w", err)
	}
	return filepath.Join(home, rest), nil
}

// copyFile copies src to dst via a hidden temp file in dst's dir, so dst
// appears complete or not at all.
func copyFile(src, dst string) (err error) {
	in, err := os.Open(src) // #nosec G304 -- the operator's own --input file
	if err != nil {
		return fmt.Errorf("opening input: %w", err)
	}
	defer func() { _ = in.Close() }()

	tmp, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".tmp-*")
	if err != nil {
		return fmt.Errorf("copying into sources: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err = io.Copy(tmp, in); err != nil {
		return fmt.Errorf("copying into sources: %w", err)
	}
	if err = tmp.Sync(); err != nil {
		return fmt.Errorf("copying into sources: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("copying into sources: %w", err)
	}
	if err = os.Chmod(tmp.Name(), 0o644); err != nil { // #nosec G302 -- the transcoder may run as another UID
		return fmt.Errorf("copying into sources: %w", err)
	}
	if err = os.Rename(tmp.Name(), dst); err != nil {
		return fmt.Errorf("copying into sources: %w", err)
	}
	return nil
}

func removeLogged(log *slog.Logger, path string) {
	if err := os.RemoveAll(path); err != nil {
		log.Error("cleanup failed; remove it by hand", "path", path, "err", err)
		return
	}
	log.Info("removed after failure", "path", path)
}
