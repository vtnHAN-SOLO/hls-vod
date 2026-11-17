package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"database/sql"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/vtnHAN-SOLO/hls-vod/internal/pgtest"
	"github.com/vtnHAN-SOLO/hls-vod/migrations"
)

// lines returns the JSON log lines whose msg is msg.
func lines(t *testing.T, buf *bytes.Buffer, msg string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("not JSON: %q", l)
		}
		if m["msg"] == msg {
			out = append(out, m)
		}
	}
	return out
}

func migrationCount(t *testing.T) int {
	t.Helper()
	entries, err := migrations.FS.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

func TestUpStatusAndConcurrentUp(t *testing.T) {
	pg := pgtest.Start(t)
	open := func() *sql.DB {
		db, err := sql.Open("pgx", pg.DSN)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return db
	}
	db := open()
	ctx := context.Background()
	n := migrationCount(t)

	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	if err := run(ctx, log, db, "status"); err != nil {
		t.Fatal(err)
	}
	if got := lines(t, &buf, "migration status"); len(got) != 1 || got[0]["pending"] != float64(n) {
		t.Fatalf("status before up = %v, want %d pending", got, n)
	}

	// Two at once: the session locker makes one wait, so both succeed and
	// every migration is applied exactly once between them.
	var wg sync.WaitGroup
	bufs := []*bytes.Buffer{{}, {}}
	errs := make([]error, 2)
	for i := range 2 {
		wg.Go(func() {
			// Each its own pool, as two processes would have.
			errs[i] = run(ctx, slog.New(slog.NewJSONHandler(bufs[i], nil)), open(), "up")
		})
	}
	wg.Wait()
	applied := 0
	for i := range 2 {
		if errs[i] != nil {
			t.Fatalf("up %d: %v", i, errs[i])
		}
		applied += len(lines(t, bufs[i], "migration applied"))
	}
	if applied != n {
		t.Errorf("two concurrent ups applied %d migrations, want %d", applied, n)
	}

	buf.Reset()
	if err := run(ctx, log, db, "up"); err != nil {
		t.Fatal(err)
	}
	if got := lines(t, &buf, "migrations up to date"); len(got) != 1 || got[0]["applied_now"] != float64(0) {
		t.Errorf("second up = %v, want nothing applied", got)
	}
	buf.Reset()
	if err := run(ctx, log, db, "status"); err != nil {
		t.Fatal(err)
	}
	if got := lines(t, &buf, "migration status"); len(got) != 1 || got[0]["pending"] != float64(0) {
		t.Errorf("status after up = %v, want 0 pending", got)
	}
}

func TestBadUsage(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	for _, args := range [][]string{nil, {"down"}, {"up", "extra"}} {
		if got := realMain(args); got != 1 {
			t.Errorf("realMain(%v) = %d, want 1", args, got)
		}
	}
	// A missing DATABASE_URL fails before any connection is tried.
	if got := realMain([]string{"up"}); got != 1 {
		t.Errorf("without DATABASE_URL = %d, want 1", got)
	}
}
