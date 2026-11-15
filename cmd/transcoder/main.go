// Command transcoder converts one source file into an HLS ladder plus poster,
// seek thumbnails and subtitles. Two modes:
//
//	transcoder --movie-id <id>                  job mode, started by the dispatcher
//	transcoder --input movie.mkv --output /tmp/out   standalone, no database
//
// Job mode reads the movie and its running job from Postgres (MEDIA_ROOT and
// DATABASE_URL required), transcodes sources/{id}.{ext} into library/{id}/
// and records the result: movie ready and job succeeded, or both failed with
// the error.
//
// Logs are JSON on stderr; the exit code is 0 on success, 1 on failure, 2 on
// bad usage. SIGTERM stops ffmpeg and removes the partial output before
// exiting (and, in job mode, marks the job failed).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/vtnHAN-SOLO/hls-vod/internal/config"
	"github.com/vtnHAN-SOLO/hls-vod/internal/mediattl"
	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
	"github.com/vtnHAN-SOLO/hls-vod/internal/transcode"
)

func main() {
	os.Exit(realMain(os.Args[1:]))
}

func realMain(args []string) int {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	fs := flag.NewFlagSet("transcoder", flag.ContinueOnError)
	movieID := fs.String("movie-id", "", "job mode: transcode this movie's sources/ file into library/{id}/")
	input := fs.String("input", "", "standalone mode: path to the source media file")
	output := fs.String("output", "", "standalone mode: directory to create; must not exist, its parent must")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *movieID != "" && (*input != "" || *output != "") {
		logger.Error("bad usage", "err", "--movie-id cannot be combined with --input/--output")
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	if *movieID != "" {
		return jobMain(ctx, logger, *movieID)
	}

	cfg, err := config.Load[config.Common]()
	if err != nil {
		logger.Error("startup failed", "err", err)
		return 1
	}
	logger = slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)

	if _, err := transcode.Run(ctx, logger, transcode.DefaultOptions(*input, *output)); err != nil {
		logger.Error("transcode failed", "input", *input, "output", *output, "err", err)
		if errors.Is(err, transcode.ErrUsage) {
			fmt.Fprintln(fs.Output(), "usage: transcoder --movie-id <id>  |  transcoder --input movie.mkv --output /path/to/new/dir")
			return 2
		}
		return 1
	}
	return 0
}

func jobMain(ctx context.Context, logger *slog.Logger, movieID string) int {
	if !model.ValidID(strings.ToLower(movieID)) {
		logger.Error("bad usage", "err", errBadMovieID)
		return 2
	}
	cfg, err := config.Load[config.TranscoderJob]()
	if err == nil {
		err = cfg.Validate()
	}
	if err != nil {
		logger.Error("startup failed", "err", err)
		return 1
	}
	logger = slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)

	st, err := store.New(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("startup failed", "err", err)
		return 1
	}
	defer st.Close()

	if cfg.DevMediaURLTTL != 0 {
		logger.Warn("DEV_MEDIA_URL_TTL is set: a retranscode's old version is removed early (local development only)",
			"grace", mediattl.OldVersionGrace(cfg.DevMediaURLTTL).String())
	}
	env := jobEnv{mediaRoot: cfg.MediaRoot, grace: mediattl.OldVersionGrace(cfg.DevMediaURLTTL)}
	if err := runJob(ctx, logger, st, env, movieID); err != nil {
		logger.Error("transcode failed", "movie_id", movieID, "err", err)
		if errors.Is(err, errBadMovieID) {
			return 2
		}
		return 1
	}
	return 0
}
