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

	"github.com/vtnHAN-SOLO/hls-vod/internal/ingest"
	"github.com/vtnHAN-SOLO/hls-vod/internal/medialayout"
	"github.com/vtnHAN-SOLO/hls-vod/internal/mediapath"
	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
)

var errUsage = errors.New("INPUT and TITLE are required")

// catalog is the slice of the store devingest needs.
type catalog interface {
	CreateMovie(ctx context.Context, m store.NewMovie, rs []store.NewRendition) error
}

type request struct {
	MediaRoot string
	Input     string
	Title     string

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
		return "", errUsage
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
		return "", fmt.Errorf("input %s: not a video file the watcher would take (mkv, mp4, m4v, mov, avi, webm)", input)
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
		PosterPath: pub.PosterPath, RuntimeSec: pub.RuntimeSec, Status: model.StatusReady,
	}
	rungs := pub.Renditions
	if err := cat.CreateMovie(ctx, movie, rungs); err != nil {
		return "", err
	}
	log.Info("movie ready", "id", id, "title", title, "source_path", source, "library_path", pub.LibraryPath, "renditions", len(rungs))
	return id, nil
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
