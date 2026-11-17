// Command migrate applies the database migrations embedded in the binary
// (migrations.FS, the same files the integration tests apply). In the
// cluster it runs before the api starts; locally `make migrate` still uses
// the goose CLI.
//
//	migrate up       apply every pending migration
//	migrate status   list every migration and whether it is applied
//
// DATABASE_URL comes from the environment, with no default. goose's Postgres
// session locker (an advisory lock) makes a second `migrate up` wait for the
// first instead of racing it. Logs are JSON on stderr; the exit code is 0 on
// success and 1 on any failure, bad usage included.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	_ "github.com/jackc/pgx/v5/stdlib" // the "pgx" database/sql driver
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"

	"github.com/vtnHAN-SOLO/hls-vod/internal/buildinfo"
	"github.com/vtnHAN-SOLO/hls-vod/internal/config"
	"github.com/vtnHAN-SOLO/hls-vod/migrations"
)

const usage = "usage: migrate up|status"

var errUsage = errors.New(usage)

func main() {
	os.Exit(realMain(os.Args[1:]))
}

func realMain(args []string) int {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	buildinfo.LogStart(logger, "migrate")

	if len(args) != 1 || (args[0] != "up" && args[0] != "status") {
		logger.Error("bad usage", "err", errUsage)
		return 1
	}
	cfg, err := config.Load[config.Migrate]()
	if err != nil {
		logger.Error("startup failed", "err", err)
		return 1
	}
	logger = slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		logger.Error("startup failed", "err", fmt.Errorf("opening database: %w", err))
		return 1
	}
	defer func() { _ = db.Close() }()

	if err := run(ctx, logger, db, args[0]); err != nil {
		logger.Error("migrate failed", "command", args[0], "err", err)
		return 1
	}
	return 0
}

// run executes one command against db.
func run(ctx context.Context, logger *slog.Logger, db *sql.DB, command string) error {
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return fmt.Errorf("creating the session locker: %w", err)
	}
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS, goose.WithSessionLocker(locker))
	if err != nil {
		return fmt.Errorf("creating the goose provider: %w", err)
	}
	// No p.Close(): it closes db, which belongs to the caller.

	switch command {
	case "up":
		results, err := p.Up(ctx)
		for _, r := range results {
			logger.Info("migration applied", "version", r.Source.Version, "file", r.Source.Path,
				"duration", r.Duration.String())
		}
		if err != nil {
			return fmt.Errorf("applying migrations: %w", err)
		}
		v, err := p.GetDBVersion(ctx)
		if err != nil {
			return fmt.Errorf("reading the schema version: %w", err)
		}
		logger.Info("migrations up to date", "applied_now", len(results), "version", v)
		return nil
	case "status":
		statuses, err := p.Status(ctx)
		if err != nil {
			return fmt.Errorf("reading migration status: %w", err)
		}
		pending := 0
		for _, s := range statuses {
			attrs := []any{"version", s.Source.Version, "file", s.Source.Path, "state", string(s.State)}
			if s.State == goose.StateApplied {
				attrs = append(attrs, "applied_at", s.AppliedAt)
			} else {
				pending++
			}
			logger.Info("migration", attrs...)
		}
		logger.Info("migration status", "total", len(statuses), "pending", pending)
		return nil
	default:
		return errUsage
	}
}
