package store_test

import (
	"slices"
	"testing"

	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
	"github.com/vtnHAN-SOLO/hls-vod/internal/pgtest"
	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
)

func paths(ts []model.SubtitleTrack) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.Path
	}
	return out
}

// Subtitle rows follow the version that is published: written with the
// movie, replaced by a retranscode's publish in the same transaction, and
// backfilled only for a movie that has none, at the version it still plays.
func TestSubtitleTracks(t *testing.T) {
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

	id, v1 := model.NewID(), model.NewID()
	sdh := "SDH"
	first := []model.SubtitleTrack{
		{Path: v1 + "/subs/1_vie.vtt", Language: "vie", Position: 1},
		{Path: v1 + "/subs/0_eng.vtt", Language: "eng", Label: &sdh, Position: 0},
	}
	if err := st.CreateMovie(ctx, store.NewMovie{
		ID: id, Title: "Subbed", SourcePath: "sources/" + id + ".mkv", LibraryPath: "library/" + id + "/" + v1,
		BackdropPath: v1 + "/poster.jpg", RuntimeSec: 60, Status: model.StatusReady, Subtitles: first,
	}, []store.NewRendition{{Height: 720, PlaylistPath: v1 + "/720p/index.m3u8"}}); err != nil {
		t.Fatal(err)
	}

	t.Run("written with the movie, listed in stream order", func(t *testing.T) {
		got, err := st.ListSubtitles(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{v1 + "/subs/0_eng.vtt", v1 + "/subs/1_vie.vtt"}; !slices.Equal(paths(got), want) {
			t.Fatalf("paths = %v, want %v", paths(got), want)
		}
		if got[0].Label == nil || *got[0].Label != "SDH" || got[1].Label != nil || got[1].Language != "vie" {
			t.Fatalf("tracks = %+v", got)
		}
	})

	t.Run("a candidate for the backfill only without rows", func(t *testing.T) {
		cs, err := st.SubtitleBackfillCandidates(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range cs {
			if c.ID == id {
				t.Fatal("a movie with rows is a backfill candidate")
			}
		}
	})

	t.Run("a retranscode replaces them with the new version's", func(t *testing.T) {
		if err := st.RetranscodeMovie(ctx, id); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := st.ClaimJob(ctx, 5, nil); !ok || err != nil {
			t.Fatalf("claim: %v %v", ok, err)
		}
		job, err := st.StartTranscode(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		v2 := job.JobID
		second := []model.SubtitleTrack{{Path: v2 + "/subs/0_eng.vtt", Language: "eng", Position: 0}}
		if err := st.FinishTranscode(ctx, job, store.Published{
			LibraryPath: "library/" + id + "/" + v2, BackdropPath: v2 + "/poster.jpg", RuntimeSec: 60,
			Renditions: []store.NewRendition{{Height: 720, PlaylistPath: v2 + "/720p/index.m3u8"}},
			Subtitles:  second,
		}); err != nil {
			t.Fatal(err)
		}
		got, err := st.ListSubtitles(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(paths(got), paths(second)) {
			t.Fatalf("after the retranscode: %v, want %v", paths(got), paths(second))
		}

		// The backfill never writes for a movie that has rows, nor for a
		// version the movie no longer plays.
		if wrote, err := st.BackfillSubtitles(ctx, id, "library/"+id+"/"+v2, first); wrote || err != nil {
			t.Fatalf("backfill over existing rows: %v %v", wrote, err)
		}
		if _, err := db.Pool.Exec(ctx, `DELETE FROM subtitle_tracks WHERE movie_id = $1`, id); err != nil {
			t.Fatal(err)
		}
		if wrote, err := st.BackfillSubtitles(ctx, id, "library/"+id+"/"+v1, first); wrote || err != nil {
			t.Fatalf("backfill for the replaced version: %v %v", wrote, err)
		}
		if got, _ := st.ListSubtitles(ctx, id); len(got) != 0 {
			t.Fatalf("rows written for an old version: %v", paths(got))
		}
	})

	t.Run("the backfill writes once, then is a no-op", func(t *testing.T) {
		cs, err := st.SubtitleBackfillCandidates(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var c *store.SubtitleCandidate
		for i := range cs {
			if cs[i].ID == id {
				c = &cs[i]
			}
		}
		if c == nil {
			t.Fatal("a ready movie without rows is not a candidate")
		}
		ts := []model.SubtitleTrack{{Path: "x/subs/0_eng.vtt", Language: "eng"}}
		for run, want := range []bool{true, false} {
			wrote, err := st.BackfillSubtitles(ctx, id, c.LibraryPath, ts)
			if err != nil || wrote != want {
				t.Fatalf("run %d: wrote %v, %v; want %v", run, wrote, err, want)
			}
		}
		if got, _ := st.ListSubtitles(ctx, id); len(got) != 1 {
			t.Fatalf("rows = %v, want exactly one", paths(got))
		}
	})

	t.Run("deleting the movie removes its rows", func(t *testing.T) {
		if _, err := db.Pool.Exec(ctx, `DELETE FROM movies WHERE id = $1`, id); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM subtitle_tracks WHERE movie_id = $1`, id).Scan(&n); err != nil || n != 0 {
			t.Fatalf("rows left: %d, %v", n, err)
		}
	})
}
