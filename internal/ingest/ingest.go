// Package ingest turns one source file into a published movie version: the
// HLS output in MEDIA_ROOT/library/{id}/{version}/ and the catalog values
// that describe it. The transcoder's job mode and tools/devingest both use it, so there is
// one way from "transcode finished" to "rows to write".
package ingest

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math"
	"os"
	"path/filepath"

	"github.com/vtnHAN-SOLO/hls-vod/internal/medialayout"
	"github.com/vtnHAN-SOLO/hls-vod/internal/mediapath"
	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
	"github.com/vtnHAN-SOLO/hls-vod/internal/transcode"
)

// OptionsFunc builds the pipeline options for (input, output).
// transcode.DefaultOptions in production; tests shrink posters and sprites.
type OptionsFunc func(input, output string) transcode.Options

// Library transcodes input into mediaRoot/library/{id}/{version}/ and
// returns what the catalog rows need: library_path "library/{id}/{version}",
// poster and playlist paths relative to library/{id}/ (so they start with
// "{version}/"). version is the job id (devingest: a fresh id); it must be a
// UUID. It refuses an existing version dir (transcode.Run never
// overwrites), and leaves nothing behind on error; other versions in
// library/{id}/ are never touched.
//
// remove deletes the published version again, for a caller whose next step
// (the database write) fails; it is a no-op after an error.
func Library(ctx context.Context, log *slog.Logger, mediaRoot, id, version, input string, opts OptionsFunc) (pub store.Published, remove func(), err error) {
	noop := func() {}
	root, err := filepath.Abs(mediaRoot)
	if err != nil {
		return store.Published{}, noop, fmt.Errorf("resolving MEDIA_ROOT: %w", err)
	}
	if !model.ValidID(id) || !model.ValidID(version) {
		return store.Published{}, noop, fmt.Errorf("movie id %q and version %q must be UUIDs", id, version)
	}
	movieDir, err := mediapath.Join(root, medialayout.LibraryPath(id))
	if err != nil {
		return store.Published{}, noop, err
	}
	_, statErr := os.Stat(movieDir)
	if err := os.MkdirAll(movieDir, 0o755); err != nil { // #nosec G301 -- nginx (another UID) must traverse the media tree
		return store.Published{}, noop, fmt.Errorf("creating %s: %w", movieDir, err)
	}
	// A first ingest that fails leaves no empty library/{id}/ behind. Remove
	// refuses a folder that is not empty, so another version is never at risk.
	dropMovieDir := func() {
		if errors.Is(statErr, fs.ErrNotExist) {
			_ = os.Remove(movieDir)
		}
	}
	libRel := medialayout.VersionPath(id, version)
	outDir, err := mediapath.Join(root, libRel)
	if err != nil {
		return store.Published{}, noop, err
	}
	if opts == nil {
		opts = transcode.DefaultOptions
	}

	// Run removes its own partial output on failure.
	res, err := transcode.Run(ctx, log, opts(input, outDir))
	if err != nil {
		dropMovieDir()
		return store.Published{}, noop, err
	}
	remove = func() {
		if err := os.RemoveAll(outDir); err != nil {
			log.Error("removing published output failed; remove it by hand", "dir", outDir, "err", err)
			return
		}
		log.Info("removed published output after a failure", "dir", outDir)
		dropMovieDir()
	}

	pub = store.Published{
		LibraryPath: libRel,
		PosterPath:  medialayout.InVersion(version, res.Poster),
		RuntimeSec:  int(math.Round(res.Duration.Seconds())),
		Renditions:  make([]store.NewRendition, len(res.Rungs)),
	}
	for i, r := range res.Rungs {
		pub.Renditions[i] = store.NewRendition{Height: r.Height, BitrateKbps: r.BitrateKbps,
			PlaylistPath: medialayout.InVersion(version, r.Playlist), SizeBytes: r.SizeBytes}
	}
	// Everything stored here ends up in a /media/ URL; check it the same way.
	rels := []string{pub.LibraryPath, pub.PosterPath}
	for _, r := range pub.Renditions {
		rels = append(rels, r.PlaylistPath)
	}
	for _, rel := range rels {
		if _, err := mediapath.Rel(rel); err != nil {
			remove()
			return store.Published{}, noop, err
		}
	}
	return pub, remove, nil
}
