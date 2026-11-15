// Command watcher claims new files from MEDIA_ROOT/inbox and queues them for
// transcoding.
//
// It runs as a CronJob (every 5 minutes), scans once and exits: 0 when every
// file was handled, 1 when anything failed (logged per file). For each
// eligible file it renames inbox/<name> to sources/{id}.<ext> and inserts the
// movie (status pending) and its transcode job (status queued) in one
// transaction. It never calls the api: the database is the queue, and the
// dispatcher turns queued jobs into Kubernetes Jobs.
//
// Before scanning, it removes the movies an admin deleted (status
// deleting): library/{id}/, the source file, then the row; and, for movies
// whose library_cleanup_after has passed, every version but the current one.
//
// Overlapping runs are safe: the rename is the lock, so each file is
// claimed exactly once, and removing a deleted movie twice is harmless.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/vtnHAN-SOLO/hls-vod/internal/config"
	"github.com/vtnHAN-SOLO/hls-vod/internal/mediapath"
	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
)

func main() {
	os.Exit(realMain())
}

func realMain() int {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	cfg, err := config.Load[config.Watcher]()
	if err != nil {
		logger.Error("startup failed", "err", err)
		return 1
	}
	logger = slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	st, err := store.New(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("startup failed", "err", err)
		return 1
	}
	defer st.Close()

	root, err := mediapath.Open(cfg.MediaRoot)
	if err != nil {
		logger.Error("startup failed", "media_root", cfg.MediaRoot, "err", err)
		return 1
	}
	defer func() { _ = root.Close() }()

	s := &scanner{root: root, q: st, log: logger, now: time.Now, newID: model.NewID}
	start := time.Now()
	r, err := s.run(ctx)
	attrs := []any{"deleted", r.Deleted, "cleaned", r.Cleaned, "claimed", r.Claimed, "skipped", r.Skipped, "released", r.Released, "failed", r.Failed,
		"pruned_refresh_tokens", r.Pruned.RefreshTokens, "pruned_email_tokens", r.Pruned.EmailTokens,
		"pruned_oauth_states", r.Pruned.OAuthStates,
		"duration", time.Since(start).Round(time.Millisecond).String()}
	if err != nil {
		logger.Error("scan finished with errors", append(attrs, "err", err)...)
		return 1
	}
	logger.Info("scan finished", attrs...)
	return 0
}
