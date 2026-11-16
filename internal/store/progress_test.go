package store_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
	"github.com/vtnHAN-SOLO/hls-vod/internal/pgtest"
	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
)

func TestWatchProgress(t *testing.T) {
	db := pgtest.Start(t)
	if _, err := db.Migrator(t).Up(t.Context()); err != nil {
		t.Fatalf("migrating: %v", err)
	}
	st, err := store.New(t.Context(), db.DSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	ctx := t.Context()
	// Not a real hash: the store never inspects it.
	const hash = "$argon2id$v=19$m=19456,t=2,p=1$c2FsdHNhbHRzYWx0$a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2U"
	alice, err := st.CreateUser(ctx, "alice", hash, model.RoleUser)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := st.CreateUser(ctx, "bob", hash, model.RoleUser)
	if err != nil {
		t.Fatal(err)
	}

	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	movie := func(title string, status model.MovieStatus, runtime *int) string {
		var id string
		if err := db.Pool.QueryRow(ctx, `
			INSERT INTO movies (title, source_path, status, runtime_sec, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $5) RETURNING id::text`,
			title, "sources/"+title+".mkv", string(status), runtime, at).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if status == model.StatusReady {
			db.Publish(ctx, t, id)
		}
		return id
	}
	runtime := 1000
	first := movie("first", model.StatusReady, &runtime)
	second := movie("second", model.StatusReady, &runtime)
	finished := movie("finished", model.StatusReady, &runtime)
	noRuntime := movie("no-runtime", model.StatusReady, nil)
	pending := movie("pending", model.StatusPending, &runtime)

	cw := func(t *testing.T, userID string) []string {
		t.Helper()
		ps, err := st.ContinueWatching(ctx, userID, 95, 20)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, p := range ps {
			out = append(out, fmt.Sprintf("%s@%d", p.Movie.Title, p.PositionSec))
		}
		return out
	}

	t.Run("save, refuse, upsert", func(t *testing.T) {
		for name, tt := range map[string]struct {
			movie string
			pos   int
			err   error
		}{
			"ready":                {first, 30, nil},
			"at the very end":      {second, 1000, nil},
			"past the runtime":     {second, 1001, store.ErrProgressRefused},
			"not ready":            {pending, 10, store.ErrProgressRefused},
			"unknown movie":        {"9f8e7d6c-5b4a-4392-8170-6f5e4d3c2b1a", 10, store.ErrProgressRefused},
			"unknown runtime, any": {noRuntime, 99999, nil},
		} {
			if err := st.SaveProgress(ctx, alice.ID, tt.movie, tt.pos); !errors.Is(err, tt.err) {
				t.Errorf("%s: SaveProgress = %v, want %v", name, err, tt.err)
			}
		}
		if err := st.SaveProgress(ctx, alice.ID, first, -1); err == nil {
			t.Error("a negative position was saved: the CHECK constraint is missing")
		}
		// Second save of the same movie replaces the position: one row per (user, movie).
		if err := st.SaveProgress(ctx, alice.ID, first, 40); err != nil {
			t.Fatal(err)
		}
		if pos, ok, err := st.GetProgress(ctx, alice.ID, first); err != nil || !ok || pos != 40 {
			t.Fatalf("GetProgress = %d %v %v, want 40", pos, ok, err)
		}
		if _, ok, err := st.GetProgress(ctx, alice.ID, pending); err != nil || ok {
			t.Fatalf("GetProgress(no row) = %v %v", ok, err)
		}
	})

	t.Run("continue watching: newest first, finished and non-ready left out", func(t *testing.T) {
		// 950 of 1000 is 95%: finished. 949 is not.
		if err := st.SaveProgress(ctx, alice.ID, finished, 949); err != nil {
			t.Fatal(err)
		}
		if got := cw(t, alice.ID); fmt.Sprint(got) != "[finished@949 first@40 no-runtime@99999]" {
			t.Fatalf("continue watching = %v", got)
		}
		if err := st.SaveProgress(ctx, alice.ID, finished, 950); err != nil {
			t.Fatal(err)
		}
		// second is at 1000 of 1000: also finished.
		if got := cw(t, alice.ID); fmt.Sprint(got) != "[first@40 no-runtime@99999]" {
			t.Fatalf("continue watching after finishing = %v", got)
		}
		// Saving again moves it to the front.
		if err := st.SaveProgress(ctx, alice.ID, first, 41); err != nil {
			t.Fatal(err)
		}
		if got := cw(t, alice.ID); fmt.Sprint(got) != "[first@41 no-runtime@99999]" {
			t.Fatalf("continue watching after a save = %v", got)
		}
		// A movie that stops being ready (retranscode) leaves the row.
		if _, err := db.Pool.Exec(ctx, `UPDATE movies SET status = 'processing', published_at = NULL WHERE id = $1`, first); err != nil {
			t.Fatal(err)
		}
		if got := cw(t, alice.ID); fmt.Sprint(got) != "[no-runtime@99999]" {
			t.Fatalf("continue watching with first processing = %v", got)
		}
		if _, err := db.Pool.Exec(ctx, `UPDATE movies SET status = 'ready' WHERE id = $1`, first); err != nil {
			t.Fatal(err)
		}
		db.Publish(ctx, t, first)
		if ps, err := st.ContinueWatching(ctx, alice.ID, 95, 1); err != nil || len(ps) != 1 {
			t.Fatalf("limit 1 = %d rows, %v", len(ps), err)
		}
	})

	t.Run("one user never sees or changes another's", func(t *testing.T) {
		if got := cw(t, bob.ID); len(got) != 0 {
			t.Fatalf("bob sees %v", got)
		}
		if _, ok, _ := st.GetProgress(ctx, bob.ID, first); ok {
			t.Fatal("bob reads alice's position")
		}
		if err := st.SaveProgress(ctx, bob.ID, first, 500); err != nil {
			t.Fatal(err)
		}
		if pos, _, _ := st.GetProgress(ctx, alice.ID, first); pos != 41 {
			t.Fatalf("bob's save changed alice's position to %d", pos)
		}
		if got := cw(t, bob.ID); fmt.Sprint(got) != "[first@500]" {
			t.Fatalf("bob's continue watching = %v", got)
		}
	})

	t.Run("cascade", func(t *testing.T) {
		if _, err := db.Pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, bob.ID); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM watch_progress WHERE user_id = $1`, bob.ID).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%d rows of a deleted user", n)
		}
	})
}
