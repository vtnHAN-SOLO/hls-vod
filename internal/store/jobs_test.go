package store_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
	"github.com/vtnHAN-SOLO/hls-vod/internal/pgtest"
	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
)

func TestJobs(t *testing.T) {
	db := pgtest.Start(t)
	if _, err := db.Migrator(t).Up(t.Context()); err != nil {
		t.Fatalf("migrating: %v", err)
	}
	st, err := store.New(t.Context(), db.DSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)

	reset := func(t *testing.T) {
		t.Helper()
		if _, err := db.Pool.Exec(t.Context(), `TRUNCATE movies CASCADE`); err != nil {
			t.Fatal(err)
		}
	}
	// queue inserts a pending movie with a queued job, as the watcher does.
	queue := func(t *testing.T) (movieID string) {
		t.Helper()
		id := model.NewID()
		m := store.PendingMovie{ID: id, Title: "T " + id[:8], SourcePath: "sources/" + id + ".mkv"}
		if err := st.EnqueueMovie(t.Context(), m, func() error { return nil }); err != nil {
			t.Fatal(err)
		}
		return id
	}
	type state struct{ job, movie, errMsg string }
	stateOf := func(t *testing.T, movieID string) state {
		t.Helper()
		var s state
		var msg *string
		if err := db.Pool.QueryRow(t.Context(), `
			SELECT j.status, m.status, j.error_msg FROM transcode_jobs j JOIN movies m ON m.id = j.movie_id
			WHERE j.movie_id = $1 ORDER BY j.created_at DESC LIMIT 1`, movieID).Scan(&s.job, &s.movie, &msg); err != nil {
			t.Fatal(err)
		}
		if msg != nil {
			s.errMsg = *msg
		}
		return s
	}
	name := func(id string) string { return "transcode-" + id }

	t.Run("claims the oldest queued job and records name and start", func(t *testing.T) {
		reset(t)
		first, _ := queue(t), queue(t)
		j, ok, err := st.ClaimJob(t.Context(), 5, name)
		if err != nil || !ok || j.MovieID != first || j.StartedAt.IsZero() || *j.K8sJobName != "transcode-"+j.ID {
			t.Fatalf("ClaimJob = %+v, %v, %v", j, ok, err)
		}
		if s := stateOf(t, first); s.job != "running" || s.movie != "pending" {
			t.Fatalf("state = %+v", s)
		}
		// Local backend: no name, pid recorded afterwards.
		j2, ok, err := st.ClaimJob(t.Context(), 5, nil)
		if err != nil || !ok || j2.K8sJobName != nil {
			t.Fatalf("ClaimJob(local) = %+v, %v, %v", j2, ok, err)
		}
		if err := st.SetJobPID(t.Context(), j2.ID, 4242); err != nil {
			t.Fatal(err)
		}
		rs, err := st.RunningJobs(t.Context())
		if err != nil || len(rs) != 2 || rs[1].ID != j2.ID || rs[1].LocalPID == nil || *rs[1].LocalPID != 4242 {
			t.Fatalf("RunningJobs = %+v, %v", rs, err)
		}
		if _, ok, _ := st.ClaimJob(t.Context(), 5, nil); ok {
			t.Fatal("claimed a job with nothing queued")
		}
	})

	t.Run("never more than maxRunning, even from many replicas at once", func(t *testing.T) {
		reset(t)
		for range 6 {
			queue(t)
		}
		var mu sync.Mutex
		var claimed []string
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				j, ok, err := st.ClaimJob(t.Context(), 2, name)
				if err != nil {
					t.Error(err)
					return
				}
				if ok {
					mu.Lock()
					claimed = append(claimed, j.ID)
					mu.Unlock()
				}
			})
		}
		wg.Wait()
		slices.Sort(claimed)
		if len(claimed) != 2 || claimed[0] == claimed[1] {
			t.Fatalf("claimed %v, want 2 distinct jobs", claimed)
		}
	})

	t.Run("a row another transaction holds is skipped, not waited for", func(t *testing.T) {
		reset(t)
		held, next := queue(t), queue(t)
		tx, err := db.Pool.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(t.Context()) }()
		if _, err := tx.Exec(t.Context(), `SELECT 1 FROM transcode_jobs WHERE movie_id = $1 FOR UPDATE`, held); err != nil {
			t.Fatal(err)
		}
		// Bounded: waiting for the held row instead of skipping it must fail, not hang.
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		j, ok, err := st.ClaimJob(ctx, 5, name)
		if err != nil || !ok || j.MovieID != next {
			t.Fatalf("ClaimJob = %+v, %v, %v; want the unlocked job", j, ok, err)
		}
	})

	t.Run("the database refuses a second active job for a movie", func(t *testing.T) {
		reset(t)
		m := queue(t)
		_, err := db.Pool.Exec(t.Context(), `INSERT INTO transcode_jobs (movie_id) VALUES ($1)`, m)
		if !store.IsActiveJobConflict(err) {
			t.Fatalf("second queued job: %v, want the one-active-job conflict", err)
		}
		if _, _, err := st.ClaimJob(t.Context(), 5, name); err != nil {
			t.Fatal(err)
		}
		_, err = db.Pool.Exec(t.Context(), `INSERT INTO transcode_jobs (movie_id) VALUES ($1)`, m)
		if !store.IsActiveJobConflict(err) {
			t.Fatalf("queued job beside a running one: %v, want the conflict", err)
		}
		if store.IsActiveJobConflict(errors.New("other")) || store.IsActiveJobConflict(nil) {
			t.Fatal("IsActiveJobConflict matches unrelated errors")
		}
	})

	t.Run("FailJob fails job and movie once; the first report wins", func(t *testing.T) {
		reset(t)
		m := queue(t)
		j, _, _ := st.ClaimJob(t.Context(), 5, nil)
		ok, err := st.FailJob(t.Context(), j.ID, "transcoder exited with status 1")
		if err != nil || !ok {
			t.Fatalf("FailJob = %v, %v", ok, err)
		}
		if s := stateOf(t, m); s != (state{"failed", "failed", "transcoder exited with status 1"}) {
			t.Fatalf("state = %+v", s)
		}
		if ok, err := st.FailJob(t.Context(), j.ID, "again"); err != nil || ok {
			t.Fatalf("second FailJob = %v, %v; want a no-op", ok, err)
		}
		if s := stateOf(t, m); s.errMsg != "transcoder exited with status 1" {
			t.Fatalf("error overwritten: %+v", s)
		}
	})

	t.Run("transcoder start and finish", func(t *testing.T) {
		reset(t)
		m := queue(t)
		if _, err := st.StartTranscode(t.Context(), m); !errors.Is(err, store.ErrNoRunningJob) {
			t.Fatalf("StartTranscode before the dispatcher = %v, want ErrNoRunningJob", err)
		}
		if _, err := st.StartTranscode(t.Context(), model.NewID()); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("unknown movie = %v, want ErrNotFound", err)
		}
		j, _, _ := st.ClaimJob(t.Context(), 5, nil)
		tj, err := st.StartTranscode(t.Context(), m)
		if err != nil || tj.JobID != j.ID || tj.SourcePath != "sources/"+m+".mkv" {
			t.Fatalf("StartTranscode = %+v, %v", tj, err)
		}
		if s := stateOf(t, m); s.movie != "processing" || s.job != "running" {
			t.Fatalf("state = %+v", s)
		}
		pub := store.Published{LibraryPath: "library/" + m, BackdropPath: "poster.jpg", RuntimeSec: 125,
			Renditions: []store.NewRendition{{Height: 720, BitrateKbps: 2800, PlaylistPath: "720p/index.m3u8", SizeBytes: 9}}}
		if err := st.FinishTranscode(t.Context(), tj, pub); err != nil {
			t.Fatal(err)
		}
		if s := stateOf(t, m); s.movie != "ready" || s.job != "succeeded" {
			t.Fatalf("state = %+v", s)
		}
		got, err := st.GetMovie(t.Context(), m)
		if err != nil || *got.LibraryPath != "library/"+m || *got.BackdropPath != "poster.jpg" || *got.RuntimeSec != 125 {
			t.Fatalf("movie = %+v, %v", got, err)
		}
		var finished bool
		if err := db.Pool.QueryRow(t.Context(),
			`SELECT finished_at IS NOT NULL FROM transcode_jobs WHERE id = $1`, j.ID).Scan(&finished); err != nil || !finished {
			t.Fatalf("finished_at not set: %v", err)
		}
		if rs, _ := st.ListRenditions(t.Context(), m); len(rs) != 1 || rs[0].Height != 720 {
			t.Fatalf("renditions = %+v", rs)
		}
		// Ready: a second transcode is not allowed yet.
		if _, err := db.Pool.Exec(t.Context(),
			`INSERT INTO transcode_jobs (movie_id, status, started_at) VALUES ($1, 'running', now())`, m); err != nil {
			t.Fatal(err)
		}
		if _, err := st.StartTranscode(t.Context(), m); !errors.Is(err, store.ErrNotTranscodable) {
			t.Fatalf("StartTranscode on a ready movie = %v, want ErrNotTranscodable", err)
		}
	})

	t.Run("a job failed meanwhile is not turned into a success", func(t *testing.T) {
		reset(t)
		m := queue(t)
		j, _, _ := st.ClaimJob(t.Context(), 5, nil)
		tj, err := st.StartTranscode(t.Context(), m)
		if err != nil {
			t.Fatal(err)
		}
		if ok, err := st.FailJob(t.Context(), j.ID, "deadline exceeded"); err != nil || !ok {
			t.Fatal(err)
		}
		pub := store.Published{LibraryPath: "library/" + m, BackdropPath: "poster.jpg",
			Renditions: []store.NewRendition{{Height: 720, PlaylistPath: "720p/index.m3u8"}}}
		if err := st.FinishTranscode(t.Context(), tj, pub); !errors.Is(err, store.ErrJobNotRunning) {
			t.Fatalf("FinishTranscode = %v, want ErrJobNotRunning", err)
		}
		if s := stateOf(t, m); s.movie != "failed" || s.job != "failed" {
			t.Fatalf("state = %+v", s)
		}
		if rs, _ := st.ListRenditions(t.Context(), m); len(rs) != 0 {
			t.Fatalf("renditions written: %+v", rs)
		}
	})

	t.Run("KnownJobNames", func(t *testing.T) {
		reset(t)
		queue(t)
		j, _, _ := st.ClaimJob(t.Context(), 5, name)
		known, err := st.KnownJobNames(t.Context(), []string{*j.K8sJobName, "transcode-orphan"})
		if err != nil || !known[*j.K8sJobName] || known["transcode-orphan"] {
			t.Fatalf("KnownJobNames = %v, %v", known, err)
		}
	})
}
