// Command devingest puts one local movie into the local catalog as ready, for
// development before the dispatcher and transcoder Job exist:
//
//	make dev-ingest INPUT=~/Videos/movie.mp4 TITLE="My test" [YEAR=2020] [OVERVIEW="…"] [POSTER=poster.png] [PUBLISH=1]
//
// It transcodes INPUT into $MEDIA_ROOT/library/{new id}/ with the real
// pipeline, copies the original to $MEDIA_ROOT/sources/{id}.{ext} (the layout
// the watcher uses), inserts the movie and its renditions in one transaction,
// and prints the new movie id on stdout. POSTER is processed as an admin
// upload is (internal/poster) and stored with the movie; PUBLISH=1
// publishes it in the same transaction (it needs POSTER and OVERVIEW). A failure leaves no rows and no
// files behind.
//
// Dev tooling only: it lives under tools/, not cmd/, so `make build` and the
// container images never include it.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/vtnHAN-SOLO/hls-vod/internal/config"
	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
)

func main() {
	os.Exit(realMain(os.Args[1:]))
}

func realMain(args []string) int {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	fs := flag.NewFlagSet("devingest", flag.ContinueOnError)
	input := fs.String("input", "", "movie file to ingest")
	title := fs.String("title", "", "catalog title")
	year := fs.Int("year", 0, "release year (optional)")
	overview := fs.String("overview", "", "overview text (optional)")
	posterFile := fs.String("poster", "", "portrait poster image, JPEG, PNG or WebP (optional; cropped to 2:3)")
	publish := fs.Bool("publish", false, "publish the movie (needs -poster and -overview)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cfg, err := config.Load[config.DevIngest]()
	if err != nil {
		logger.Error("startup failed", "err", err)
		return 1
	}
	logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	st, err := store.New(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("startup failed", "err", err)
		return 1
	}
	defer st.Close()

	id, err := ingestMovie(ctx, logger, st, request{MediaRoot: cfg.MediaRoot, Input: *input, Title: *title,
		Year: *year, Overview: *overview, Poster: *posterFile, Publish: *publish})
	if err != nil {
		logger.Error("ingest failed", "input", *input, "err", err)
		if errors.Is(err, errUsage) {
			_, _ = fmt.Fprintln(fs.Output(), `usage: make dev-ingest INPUT=path/to/movie.mp4 TITLE="Title" [YEAR=2020] [OVERVIEW="..."] [POSTER=poster.png] [PUBLISH=1]`)
			return 2
		}
		return 1
	}
	// stdout carries only the id, so `id=$(make -s dev-ingest ...)` works.
	_, _ = fmt.Fprintln(os.Stdout, id)
	return 0
}
