package dispatch

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
	"github.com/vtnHAN-SOLO/hls-vod/internal/pgtest"
	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
)

// fakeEnv turns this test binary into a fake transcoder, so the local
// backend is tested with real processes, pids and signals:
//
//	sleep   wait for SIGTERM, then exit 1 without reporting
//	fail    exit 3 at once without reporting
//	report  record success through the real store, exit 0
const fakeEnv = "FAKE_TRANSCODER"

func TestMain(m *testing.M) {
	if mode := os.Getenv(fakeEnv); mode != "" {
		os.Exit(fakeTranscoder(mode))
	}
	os.Exit(m.Run())
}

func fakeTranscoder(mode string) int {
	var movieID string
	for i, a := range os.Args {
		if a == "--movie-id" && i+1 < len(os.Args) {
			movieID = os.Args[i+1]
		}
	}
	switch mode {
	case "sleep":
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGTERM)
		select {
		case <-ch:
			return 1
		case <-time.After(2 * time.Minute): // never outlive the test run
			return 1
		}
	case "fail":
		return 3
	case "report":
		ctx := context.Background()
		st, err := store.New(ctx, os.Getenv("FAKE_DSN"))
		if err != nil {
			return 10
		}
		defer st.Close()
		t, err := st.StartTranscode(ctx, movieID)
		if err != nil {
			return 11
		}
		pub := store.Published{LibraryPath: "library/" + movieID, PosterPath: "poster.jpg", RuntimeSec: 60,
			Renditions: []store.NewRendition{{Height: 480, BitrateKbps: 1000, PlaylistPath: "480p/index.m3u8", SizeBytes: 1}}}
		if err := st.FinishTranscode(ctx, t, pub); err != nil {
			return 12
		}
		return 0
	}
	return 99
}

func TestDispatcher(t *testing.T) {
	db := pgtest.Start(t)
	if _, err := db.Migrator(t).Up(t.Context()); err != nil {
		t.Fatalf("migrating: %v", err)
	}
	st, err := store.New(t.Context(), db.DSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	quiet := slog.New(slog.DiscardHandler)

	reset := func(t *testing.T) {
		t.Helper()
		if _, err := db.Pool.Exec(t.Context(), `TRUNCATE movies CASCADE`); err != nil {
			t.Fatal(err)
		}
	}
	queue := func(t *testing.T) string {
		t.Helper()
		id := model.NewID()
		if err := st.EnqueueMovie(t.Context(), store.PendingMovie{ID: id, Title: "T", SourcePath: "sources/" + id + ".mkv"},
			func() error { return nil }); err != nil {
			t.Fatal(err)
		}
		return id
	}
	// newLocal is a local backend whose transcoder is this binary in mode;
	// every process it starts is killed when the test ends.
	newLocal := func(t *testing.T, mode string) *Local {
		t.Helper()
		l := &Local{Bin: os.Args[0], Env: []string{fakeEnv + "=" + mode, "FAKE_DSN=" + db.DSN},
			Out: io.Discard, Log: quiet, StopGrace: 2 * time.Second}
		t.Cleanup(func() {
			for _, pid := range l.Active() {
				_ = syscall.Kill(-pid, syscall.SIGKILL)
			}
		})
		return l
	}
	newDispatcher := func(b Backend) *Dispatcher {
		return &Dispatcher{Store: st, Backend: b, Log: quiet, MaxConcurrent: 1,
			Deadline: time.Hour, StartGrace: time.Minute}
	}
	tick := func(t *testing.T, d *Dispatcher) {
		t.Helper()
		if err := d.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	type state struct {
		job, movie, errMsg string
		pid                *int
	}
	stateOf := func(t *testing.T, movieID string) state {
		t.Helper()
		var s state
		var msg *string
		if err := db.Pool.QueryRow(t.Context(), `
			SELECT j.status, m.status, j.error_msg, j.local_pid FROM transcode_jobs j JOIN movies m ON m.id = j.movie_id
			WHERE j.movie_id = $1`, movieID).Scan(&s.job, &s.movie, &msg, &s.pid); err != nil {
			t.Fatal(err)
		}
		if msg != nil {
			s.errMsg = *msg
		}
		return s
	}
	waitFor := func(t *testing.T, what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	countRunning := func(t *testing.T) int {
		t.Helper()
		var n int
		if err := db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM transcode_jobs WHERE status = 'running'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	t.Run("queued to running, with the pid recorded", func(t *testing.T) {
		reset(t)
		m := queue(t)
		l := newLocal(t, "sleep")
		tick(t, newDispatcher(l))
		s := stateOf(t, m)
		if s.job != "running" || s.pid == nil {
			t.Fatalf("state = %+v, want running with a pid", s)
		}
		if !l.isTranscoder(*s.pid, m) {
			t.Fatalf("pid %d is not a transcoder for %s", *s.pid, m)
		}
	})

	t.Run("at most MaxConcurrent at once", func(t *testing.T) {
		reset(t)
		a, b, c := queue(t), queue(t), queue(t)
		d := newDispatcher(newLocal(t, "sleep"))
		tick(t, d)
		tick(t, d)
		if n := countRunning(t); n != 1 {
			t.Fatalf("%d running, want 1", n)
		}
		if stateOf(t, a).job != "running" || stateOf(t, b).job != "queued" || stateOf(t, c).job != "queued" {
			t.Fatal("not oldest first")
		}
	})

	t.Run("two dispatchers share the limit and never the same job", func(t *testing.T) {
		reset(t)
		for range 4 {
			queue(t)
		}
		var wg sync.WaitGroup
		for range 2 {
			d := newDispatcher(newLocal(t, "sleep"))
			d.MaxConcurrent = 2
			wg.Go(func() {
				if err := d.Tick(t.Context()); err != nil {
					t.Error(err)
				}
			})
		}
		wg.Wait()
		var n, distinctPIDs int
		if err := db.Pool.QueryRow(t.Context(), `
			SELECT count(*), count(DISTINCT local_pid) FROM transcode_jobs WHERE status = 'running'`).Scan(&n, &distinctPIDs); err != nil {
			t.Fatal(err)
		}
		if n != 2 || distinctPIDs != 2 {
			t.Fatalf("%d running with %d distinct pids, want 2 and 2", n, distinctPIDs)
		}
	})

	t.Run("a second active job is refused by the database; the dispatcher carries on", func(t *testing.T) {
		reset(t)
		m := queue(t)
		_, err := db.Pool.Exec(t.Context(), `INSERT INTO transcode_jobs (movie_id) VALUES ($1)`, m)
		if !store.IsActiveJobConflict(err) {
			t.Fatalf("second job: %v, want the one-active-job conflict", err)
		}
		l := newLocal(t, "sleep")
		d := newDispatcher(l)
		d.MaxConcurrent = 5
		tick(t, d)
		tick(t, d)
		if got := len(l.Active()); got != 1 {
			t.Fatalf("%d transcoders for the movie, want exactly 1", got)
		}
	})

	t.Run("a transcoder that records success is left alone", func(t *testing.T) {
		reset(t)
		m := queue(t)
		l := newLocal(t, "report")
		d := newDispatcher(l)
		tick(t, d)
		waitFor(t, "the transcoder to exit", func() bool { return len(l.Active()) == 0 })
		tick(t, d)
		if s := stateOf(t, m); s.job != "succeeded" || s.movie != "ready" {
			t.Fatalf("state = %+v", s)
		}
	})

	t.Run("failure: exited without a result, job and movie failed with the error", func(t *testing.T) {
		reset(t)
		m := queue(t)
		l := newLocal(t, "fail")
		d := newDispatcher(l)
		tick(t, d)
		waitFor(t, "the transcoder to exit", func() bool { return len(l.Active()) == 0 })
		tick(t, d)
		s := stateOf(t, m)
		if s.job != "failed" || s.movie != "failed" || !strings.Contains(s.errMsg, "exit status 3") {
			t.Fatalf("state = %+v", s)
		}
	})

	t.Run("a start that fails marks the job failed", func(t *testing.T) {
		reset(t)
		m := queue(t)
		l := newLocal(t, "sleep")
		l.Bin = "/nonexistent/transcoder"
		tick(t, newDispatcher(l))
		if s := stateOf(t, m); s.job != "failed" || !strings.Contains(s.errMsg, "starting the transcoder") {
			t.Fatalf("state = %+v", s)
		}
	})

	t.Run("stuck: past the deadline it is failed and killed", func(t *testing.T) {
		reset(t)
		m := queue(t)
		l := newLocal(t, "sleep")
		d := newDispatcher(l)
		d.Deadline = 300 * time.Millisecond
		tick(t, d)
		time.Sleep(400 * time.Millisecond)
		tick(t, d)
		if s := stateOf(t, m); s.job != "failed" || s.movie != "failed" || !strings.HasPrefix(s.errMsg, "deadline exceeded") {
			t.Fatalf("state = %+v", s)
		}
		waitFor(t, "the transcoder to be killed", func() bool { return len(l.Active()) == 0 })
	})

	t.Run("restart: a running transcoder is adopted, not started twice", func(t *testing.T) {
		reset(t)
		m := queue(t)
		first := newLocal(t, "sleep")
		tick(t, newDispatcher(first))
		pid := *stateOf(t, m).pid

		// A new dispatcher process: knows nothing but the database.
		again := newLocal(t, "sleep")
		d := newDispatcher(again)
		tick(t, d)
		tick(t, d)
		if s := stateOf(t, m); s.job != "running" || *s.pid != pid {
			t.Fatalf("state = %+v, want still running as pid %d", s, pid)
		}
		if len(again.Active()) != 0 {
			t.Fatal("the restarted dispatcher started a duplicate")
		}

		// The adopted process dies: the next tick notices.
		if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "the process to be gone", func() bool { return !again.isTranscoder(pid, m) })
		tick(t, d)
		s := stateOf(t, m)
		if s.job != "failed" || !strings.Contains(s.errMsg, fmt.Sprintf("process %d is no longer running", pid)) {
			t.Fatalf("state = %+v", s)
		}
	})

	t.Run("restart: a deadline also stops an adopted transcoder", func(t *testing.T) {
		reset(t)
		m := queue(t)
		tick(t, newDispatcher(newLocal(t, "sleep")))
		pid := *stateOf(t, m).pid
		again := newLocal(t, "sleep")
		d := newDispatcher(again)
		d.Deadline = time.Nanosecond
		tick(t, d)
		if s := stateOf(t, m); s.job != "failed" || !strings.HasPrefix(s.errMsg, "deadline exceeded") {
			t.Fatalf("state = %+v", s)
		}
		waitFor(t, "the adopted process to be stopped", func() bool { return !again.isTranscoder(pid, m) })
	})

	t.Run("restart: a recorded pid that is gone, or that is now another program", func(t *testing.T) {
		reset(t)
		m := queue(t)
		if _, ok, err := st.ClaimJob(t.Context(), 5, nil); !ok || err != nil {
			t.Fatal(ok, err)
		}
		// A real pid of something that is not our transcoder: this test's
		// own process. It must not be adopted.
		if err := st.SetJobPID(t.Context(), mustJobID(t, st), os.Getpid()); err != nil {
			t.Fatal(err)
		}
		tick(t, newDispatcher(newLocal(t, "sleep")))
		if s := stateOf(t, m); s.job != "failed" || !strings.Contains(s.errMsg, "no longer running") {
			t.Fatalf("state = %+v, want failed: pid %d is not a transcoder", s, os.Getpid())
		}

		reset(t)
		m = queue(t)
		if _, ok, err := st.ClaimJob(t.Context(), 5, nil); !ok || err != nil {
			t.Fatal(ok, err)
		}
		dead := exec.CommandContext(t.Context(), "true")
		if err := dead.Run(); err != nil {
			t.Fatal(err)
		}
		if err := st.SetJobPID(t.Context(), mustJobID(t, st), dead.Process.Pid); err != nil {
			t.Fatal(err)
		}
		tick(t, newDispatcher(newLocal(t, "sleep")))
		if s := stateOf(t, m); s.job != "failed" {
			t.Fatalf("state = %+v, want failed", s)
		}
	})

	t.Run("no transcoder found: given StartGrace, then failed", func(t *testing.T) {
		reset(t)
		m := queue(t)
		// Claimed (committed) but the process never started: a crash, or
		// another replica that is about to start it.
		if _, ok, err := st.ClaimJob(t.Context(), 5, nil); !ok || err != nil {
			t.Fatal(ok, err)
		}
		d := newDispatcher(newLocal(t, "sleep"))
		tick(t, d)
		if s := stateOf(t, m); s.job != "running" {
			t.Fatalf("failed inside the grace period: %+v", s)
		}
		d.Now = func() time.Time { return time.Now().Add(2 * time.Minute) }
		tick(t, d)
		if s := stateOf(t, m); s.job != "failed" || !strings.Contains(s.errMsg, "no pid was recorded") {
			t.Fatalf("state = %+v", s)
		}
	})
}

func mustJobID(t *testing.T, st *store.Store) string {
	t.Helper()
	jobs, err := st.RunningJobs(t.Context())
	if err != nil || len(jobs) != 1 {
		t.Fatalf("RunningJobs = %v, %v", jobs, err)
	}
	return jobs[0].ID
}
