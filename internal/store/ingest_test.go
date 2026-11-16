package store_test

import (
	"errors"
	"testing"

	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
	"github.com/vtnHAN-SOLO/hls-vod/internal/pgtest"
	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
)

func TestCreateMovie(t *testing.T) {
	db := pgtest.Start(t)
	if _, err := db.Migrator(t).Up(t.Context()); err != nil {
		t.Fatalf("migrating: %v", err)
	}
	st, err := store.New(t.Context(), db.DSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)

	newMovie := func(source string) store.NewMovie {
		id := model.NewID()
		return store.NewMovie{ID: id, Title: "T " + source, SourcePath: source, LibraryPath: "library/" + id,
			BackdropPath: "poster.jpg", RuntimeSec: 14, Status: model.StatusReady}
	}
	rungs := []store.NewRendition{
		{Height: 720, BitrateKbps: 2500, PlaylistPath: "720p/index.m3u8", SizeBytes: 5_000_000},
		{Height: 480, BitrateKbps: 1000, PlaylistPath: "480p/index.m3u8", SizeBytes: 2_000_000},
	}
	count := func(table string) int {
		var n int
		if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	t.Run("writes the movie and every rendition", func(t *testing.T) {
		m := newMovie("sources/a.mp4")
		if err := st.CreateMovie(t.Context(), m, rungs); err != nil {
			t.Fatal(err)
		}
		got, err := st.GetMovie(t.Context(), m.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Title != m.Title || got.Status != model.StatusReady || got.SourcePath != "sources/a.mp4" ||
			*got.LibraryPath != m.LibraryPath || *got.BackdropPath != "poster.jpg" || *got.RuntimeSec != 14 {
			t.Fatalf("movie = %+v", got)
		}
		rs, err := st.ListRenditions(t.Context(), m.ID)
		if err != nil || len(rs) != 2 || rs[0].Height != 720 || *rs[0].SizeBytes != 5_000_000 ||
			rs[1].PlaylistPath != "480p/index.m3u8" || *rs[1].BitrateKbps != 1000 {
			t.Fatalf("renditions = %+v, %v", rs, err)
		}
	})

	t.Run("duplicate source path is ErrSourceExists", func(t *testing.T) {
		before := count("movies")
		err := st.CreateMovie(t.Context(), newMovie("sources/a.mp4"), rungs)
		if !errors.Is(err, store.ErrSourceExists) {
			t.Fatalf("err = %v, want ErrSourceExists", err)
		}
		if count("movies") != before {
			t.Fatal("a row was written")
		}
	})

	t.Run("a failing rendition rolls the movie back", func(t *testing.T) {
		movies, renditions := count("movies"), count("renditions")
		m := newMovie("inbox/b.mp4")
		dup := append(rungs[:1:1], rungs[0]) // UNIQUE (movie_id, height) rejects the second
		if err := st.CreateMovie(t.Context(), m, dup); err == nil {
			t.Fatal("expected an error")
		}
		if count("movies") != movies || count("renditions") != renditions {
			t.Fatal("partial rows left behind: the insert was not one transaction")
		}
		if _, err := st.GetMovie(t.Context(), m.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("movie row survived the rollback: %v", err)
		}
	})
}
