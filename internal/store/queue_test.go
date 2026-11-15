package store_test

import (
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
	"github.com/vtnHAN-SOLO/hls-vod/internal/pgtest"
	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
)

func TestQueue(t *testing.T) {
	db := pgtest.Start(t)
	if _, err := db.Migrator(t).Up(t.Context()); err != nil {
		t.Fatalf("migrating: %v", err)
	}
	st, err := store.New(t.Context(), db.DSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)

	pending := func() store.PendingMovie {
		id := model.NewID()
		return store.PendingMovie{ID: id, Title: "My Movie (2020)", SourcePath: "sources/" + id + ".mkv"}
	}
	jobs := func(movieID string) []string {
		t.Helper()
		var statuses []string
		rows, err := db.Pool.Query(t.Context(), `SELECT status FROM transcode_jobs WHERE movie_id = $1`, movieID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			statuses = append(statuses, s)
		}
		return statuses
	}
	ok := func() error { return nil }

	t.Run("writes a pending movie and a queued job", func(t *testing.T) {
		m := pending()
		if err := st.EnqueueMovie(t.Context(), m, ok); err != nil {
			t.Fatal(err)
		}
		got, err := st.GetMovie(t.Context(), m.ID)
		if err != nil || got.Title != m.Title || got.SourcePath != m.SourcePath || got.Status != model.StatusPending {
			t.Fatalf("movie = %+v, %v", got, err)
		}
		if j := jobs(m.ID); !slices.Equal(j, []string{"queued"}) {
			t.Fatalf("jobs = %v, want one queued", j)
		}
	})

	t.Run("a failed claim writes nothing", func(t *testing.T) {
		m := pending()
		lost := errors.New("taken by another run")
		if err := st.EnqueueMovie(t.Context(), m, func() error { return lost }); !errors.Is(err, lost) {
			t.Fatalf("err = %v, want the claim's error", err)
		}
		if _, err := st.GetMovie(t.Context(), m.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("movie written despite the failed claim: %v", err)
		}
	})

	t.Run("a duplicate source writes neither row", func(t *testing.T) {
		first := pending()
		if err := st.EnqueueMovie(t.Context(), first, ok); err != nil {
			t.Fatal(err)
		}
		dup := pending()
		dup.SourcePath = first.SourcePath
		if err := st.EnqueueMovie(t.Context(), dup, ok); !errors.Is(err, store.ErrSourceExists) {
			t.Fatalf("err = %v, want ErrSourceExists", err)
		}
		if j := jobs(dup.ID); len(j) != 0 {
			t.Fatalf("job written for the duplicate: %v", j)
		}
	})

	t.Run("releases only sources without a movie row", func(t *testing.T) {
		known := pending()
		if err := st.EnqueueMovie(t.Context(), known, ok); err != nil {
			t.Fatal(err)
		}
		orphan := "sources/" + model.NewID() + ".mp4"
		var released []string
		ran, err := st.ReleaseOrphans(t.Context(), []string{known.SourcePath, orphan}, func(p string) error {
			released = append(released, p)
			return nil
		})
		if err != nil || !ran || !slices.Equal(released, []string{orphan}) {
			t.Fatalf("ran=%v err=%v released=%v, want only %s", ran, err, released, orphan)
		}
	})

	t.Run("release errors are reported, the rest still run", func(t *testing.T) {
		a, b := "sources/"+model.NewID()+".mp4", "sources/"+model.NewID()+".mp4"
		var released []string
		_, err := st.ReleaseOrphans(t.Context(), []string{a, b}, func(p string) error {
			released = append(released, p)
			return errors.New("cannot move " + p)
		})
		if err == nil || len(released) != 2 {
			t.Fatalf("err=%v released=%v, want both tried and an error", err, released)
		}
	})

	// The lock: while a claim sits between its rename and its insert, the
	// file has no row yet and must not be mistaken for a crash leftover.
	t.Run("does not run while a claim is in progress", func(t *testing.T) {
		m := pending()
		inClaim, finish := make(chan struct{}), make(chan struct{})
		var once sync.Once
		release := func() { once.Do(func() { close(finish) }) }
		// On any failure below, let the claim finish: a goroutine stuck
		// in its transaction would hang st.Close in cleanup.
		defer release()
		done := make(chan error, 1)
		go func() {
			done <- st.EnqueueMovie(t.Context(), m, func() error {
				close(inClaim)
				<-finish
				return nil
			})
		}()
		<-inClaim

		ran, err := st.ReleaseOrphans(t.Context(), []string{m.SourcePath}, func(p string) error {
			t.Errorf("released %s while its claim was in progress", p)
			return nil
		})
		if err != nil || ran {
			t.Fatalf("ran=%v err=%v, want it to skip", ran, err)
		}

		release()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("claim did not finish")
		}
		// Committed: it is a known source now, not an orphan.
		ran, err = st.ReleaseOrphans(t.Context(), []string{m.SourcePath}, func(p string) error {
			t.Errorf("released %s, which has a row", p)
			return nil
		})
		if err != nil || !ran {
			t.Fatalf("ran=%v err=%v after the claim committed", ran, err)
		}
	})
}
