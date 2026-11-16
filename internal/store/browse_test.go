package store_test

import (
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
	"github.com/vtnHAN-SOLO/hls-vod/internal/pgtest"
	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
)

// Popular ranking, featured movies, metadata edits and top_height.
func TestBrowse(t *testing.T) {
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

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// Not a real hash: the store never inspects it.
	const hash = "$argon2id$v=19$m=19456,t=2,p=1$c2FsdHNhbHRzYWx0$a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2V5a2U"
	users := make([]string, 4)
	for i := range users {
		u, err := st.CreateUser(ctx, fmt.Sprintf("viewer%d", i), hash, model.RoleUser)
		if err != nil {
			t.Fatal(err)
		}
		users[i] = u.ID
	}
	ids := map[string]string{}
	for i, s := range []struct {
		title  string
		status model.MovieStatus
	}{
		{"recent hit", model.StatusReady}, {"old classic", model.StatusReady},
		{"tie x", model.StatusReady}, {"tie y", model.StatusReady},
		{"unwatched", model.StatusReady}, {"hidden failed", model.StatusFailed},
	} {
		ids[s.title] = insertMovie(t, db.Pool, s.title, fmt.Sprintf("sources/b%d.mkv", i), s.status, base.Add(time.Duration(i)*time.Hour))
		if s.status == model.StatusReady {
			db.Publish(ctx, t, ids[s.title])
		}
	}
	now := time.Now()
	day := 24 * time.Hour
	// watched inserts a progress row saved at `at` (the trigger only moves
	// updated_at on UPDATE, so an INSERT keeps it).
	watched := func(user, movie string, at time.Time) {
		t.Helper()
		if _, err := db.Pool.Exec(ctx, `
			INSERT INTO watch_progress (user_id, movie_id, position_sec, created_at, updated_at)
			VALUES ($1, $2, 60, $3, $3)`, user, ids[movie], at); err != nil {
			t.Fatal(err)
		}
	}
	// recent hit: 2 viewers this week. old classic: 3 viewers, 2 months ago.
	// tie x, tie y: 1 viewer each, 10 days ago. hidden failed: 4 viewers,
	// never ranked. unwatched: never ranked.
	watched(users[0], "recent hit", now.Add(-1*day))
	watched(users[1], "recent hit", now.Add(-2*day))
	for _, u := range users[:3] {
		watched(u, "old classic", now.Add(-60*day))
	}
	watched(users[3], "tie x", now.Add(-10*day))
	watched(users[3], "tie y", now.Add(-10*day))
	for _, u := range users {
		watched(u, "hidden failed", now.Add(-1*day))
	}

	since := func(d time.Duration) *time.Time { t := now.Add(-d); return &t }
	tieFirst, tieSecond := "tie x", "tie y"
	if ids["tie y"] < ids["tie x"] {
		tieFirst, tieSecond = tieSecond, tieFirst
	}
	for _, tc := range []struct {
		name  string
		since *time.Time
		limit int
		want  []string
		views []int
	}{
		{"week", since(7 * day), 10, []string{"recent hit"}, []int{2}},
		{"month: ties by id", since(30 * day), 10, []string{"recent hit", tieFirst, tieSecond}, []int{2, 1, 1}},
		{"all", nil, 10, []string{"old classic", "recent hit", tieFirst, tieSecond}, []int{3, 2, 1, 1}},
		{"limit", nil, 2, []string{"old classic", "recent hit"}, []int{3, 2}},
	} {
		t.Run("popular "+tc.name, func(t *testing.T) {
			ps, err := st.PopularMovies(ctx, tc.since, tc.limit)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			var views []int
			for _, p := range ps {
				got = append(got, p.Movie.Title)
				views = append(views, p.Viewers)
			}
			if !slices.Equal(got, tc.want) || !slices.Equal(views, tc.views) {
				t.Fatalf("got %v %v, want %v %v", got, views, tc.want, tc.views)
			}
		})
	}

	t.Run("a second save by the same user counts once", func(t *testing.T) {
		if err := st.SaveProgress(ctx, users[0], ids["recent hit"], 120); err != nil {
			t.Fatal(err)
		}
		ps, err := st.PopularMovies(ctx, since(7*day), 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(ps) != 1 || ps[0].Viewers != 2 {
			t.Fatalf("got %+v, want recent hit with 2 viewers", ps)
		}
	})

	t.Run("featured: ready only, newest featured first", func(t *testing.T) {
		if ms, err := st.FeaturedMovies(ctx, 10); err != nil || len(ms) != 0 {
			t.Fatalf("before any feature: %v, %v", ms, err)
		}
		if err := st.SetFeatured(ctx, ids["hidden failed"], true); !errors.Is(err, store.ErrMovieNotReady) {
			t.Fatalf("featuring a failed movie: %v, want ErrMovieNotReady", err)
		}
		if err := st.SetFeatured(ctx, model.NewID(), true); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("featuring an unknown movie: %v, want ErrNotFound", err)
		}
		for _, title := range []string{"old classic", "unwatched", "recent hit"} {
			if err := st.SetFeatured(ctx, ids[title], true); err != nil {
				t.Fatal(err)
			}
		}
		// Featuring again moves it to the front.
		if err := st.SetFeatured(ctx, ids["old classic"], true); err != nil {
			t.Fatal(err)
		}
		if err := st.SetFeatured(ctx, ids["unwatched"], false); err != nil {
			t.Fatal(err)
		}
		ms, err := st.FeaturedMovies(ctx, 10)
		if err != nil {
			t.Fatal(err)
		}
		if got := titles(ms); !slices.Equal(got, []string{"old classic", "recent hit"}) {
			t.Fatalf("featured = %v", got)
		}
		if ms[0].FeaturedAt == nil {
			t.Fatal("featured_at not read back")
		}
		// A featured movie that is deleted leaves the spotlight (deleting
		// unpublishes it in the same statement).
		if err := st.DeleteMovie(ctx, ids["recent hit"]); err != nil {
			t.Fatal(err)
		}
		ms, err = st.FeaturedMovies(ctx, 10)
		if err != nil {
			t.Fatal(err)
		}
		if got := titles(ms); !slices.Equal(got, []string{"old classic"}) {
			t.Fatalf("featured after delete = %v", got)
		}
	})

	t.Run("metadata: partial update, clear, unknown", func(t *testing.T) {
		id := ids["unwatched"]
		title, year, overview := "Renamed", 1999, "A new overview."
		m, err := st.UpdateMovieMetadata(ctx, id, model.MovieMetadata{Title: &title, Year: &year, Overview: &overview})
		if err != nil {
			t.Fatal(err)
		}
		if m.Title != title || m.Year == nil || *m.Year != year || m.Overview == nil || *m.Overview != overview {
			t.Fatalf("after edit: %+v", m.Movie)
		}
		// Only the year cleared: title and overview stay.
		m, err = st.UpdateMovieMetadata(ctx, id, model.MovieMetadata{ClearYear: true})
		if err != nil {
			t.Fatal(err)
		}
		if m.Title != title || m.Year != nil || m.Overview == nil {
			t.Fatalf("after clearing the year: %+v", m.Movie)
		}
		// A published movie keeps its overview: unpublish it first.
		if _, err := st.UpdateMovieMetadata(ctx, id, model.MovieMetadata{ClearOverview: true}); !errors.Is(err, store.ErrMoviePublished) {
			t.Fatalf("clearing a published movie's overview: %v, want ErrMoviePublished", err)
		}
		if err := st.UnpublishMovie(ctx, id); err != nil {
			t.Fatal(err)
		}
		m, err = st.UpdateMovieMetadata(ctx, id, model.MovieMetadata{ClearOverview: true})
		if err != nil {
			t.Fatal(err)
		}
		if m.Overview != nil || m.Title != title {
			t.Fatalf("after clearing the overview: %+v", m.Movie)
		}
		if _, err := st.UpdateMovieMetadata(ctx, model.NewID(), model.MovieMetadata{Title: &title}); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("unknown movie: %v, want ErrNotFound", err)
		}
	})

	t.Run("top_height is the tallest rendition", func(t *testing.T) {
		id := ids["tie x"]
		m, err := st.GetMovie(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if m.TopHeight != nil {
			t.Fatalf("no renditions: top height %d, want nil", *m.TopHeight)
		}
		for _, h := range []int{480, 1080, 720} {
			if _, err := db.Pool.Exec(ctx, `
				INSERT INTO renditions (movie_id, height, playlist_path) VALUES ($1, $2, $3)`,
				id, h, fmt.Sprintf("v/%d/index.m3u8", h)); err != nil {
				t.Fatal(err)
			}
		}
		m, err = st.GetMovie(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if m.TopHeight == nil || *m.TopHeight != 1080 {
			t.Fatalf("top height = %v, want 1080", m.TopHeight)
		}
		ps, err := st.PopularMovies(ctx, nil, 10)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range ps {
			if p.Movie.ID == id && (p.Movie.TopHeight == nil || *p.Movie.TopHeight != 1080) {
				t.Fatalf("popular: top height = %v, want 1080", p.Movie.TopHeight)
			}
		}
	})
}
