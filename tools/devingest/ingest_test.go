package main

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/vtnHAN-SOLO/hls-vod/internal/ffmpeg"
	"github.com/vtnHAN-SOLO/hls-vod/internal/medialayout"
	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
	"github.com/vtnHAN-SOLO/hls-vod/internal/pgtest"
	"github.com/vtnHAN-SOLO/hls-vod/internal/poster"
	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
	"github.com/vtnHAN-SOLO/hls-vod/internal/transcode"
)

// clip is a generated 6s 640x360 H.264/AAC mp4: remux top rung only.
var clip string

func TestMain(m *testing.M) {
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			fmt.Fprintf(os.Stderr, "FAIL: %s not found in PATH; these tests need it.\n"+
				"Install with: sudo apt install ffmpeg\n", bin)
			os.Exit(1)
		}
	}
	dir, err := os.MkdirTemp("", "devingest-fixtures-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: %v\n", err)
		os.Exit(1)
	}
	clip = filepath.Join(dir, "clip.mp4")
	// #nosec G204 -- fixed ffmpeg command; the only variable is a path under os.MkdirTemp.
	cmd := exec.CommandContext(context.Background(), "ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=24:duration=6",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=6",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-g", "48",
		"-c:a", "aac", "-ac", "2", "-y", clip)
	if b, err := cmd.CombinedOutput(); err != nil {
		_ = os.RemoveAll(dir)
		fmt.Fprintf(os.Stderr, "FAIL: generating clip: %v: %s\n", err, b)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// recorder wraps the real store to count calls and optionally fail the insert.
type recorder struct {
	*store.Store
	creates   int
	failWith  error
	transcode int
}

func (r *recorder) CreateMovie(ctx context.Context, m store.NewMovie, rs []store.NewRendition) error {
	r.creates++
	if r.failWith != nil {
		return r.failWith
	}
	return r.Store.CreateMovie(ctx, m, rs)
}

func (r *recorder) request(root, input, title string) request {
	return request{MediaRoot: root, Input: input, Title: title,
		transcode: func(in, out string) transcode.Options {
			r.transcode++
			o := transcode.DefaultOptions(in, out)
			o.PosterHeight = 180
			return o
		}}
}

func TestIngest(t *testing.T) {
	db := pgtest.Start(t)
	if _, err := db.Migrator(t).Up(t.Context()); err != nil {
		t.Fatalf("migrating: %v", err)
	}
	st, err := store.New(t.Context(), db.DSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	log := slog.New(slog.DiscardHandler)

	rows := func(t *testing.T) (movies, renditions int) {
		t.Helper()
		if err := db.Pool.QueryRow(t.Context(),
			`SELECT (SELECT count(*) FROM movies), (SELECT count(*) FROM renditions)`).Scan(&movies, &renditions); err != nil {
			t.Fatal(err)
		}
		return movies, renditions
	}

	t.Run("a poster and publish: stored and published with the movie", func(t *testing.T) {
		root := t.TempDir()
		rec := &recorder{Store: st}
		posterFile := filepath.Join(t.TempDir(), "poster.jpg") // a PNG, whatever the name says
		writePNG(t, posterFile, 800, 1000)
		req := rec.request(root, clip, "Published")
		req.Overview, req.Poster, req.Publish = "With a poster.", posterFile, true
		id, err := ingestMovie(t.Context(), log, rec, req)
		if err != nil {
			t.Fatal(err)
		}
		m, err := st.GetMovie(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		// 800x1000 crops to 666x999: the 1000 width would be an upscale.
		if m.PublishedAt == nil || !slices.Equal(m.PosterWidths, []int{300, 600}) {
			t.Fatalf("published %v widths %v", m.PublishedAt, m.PosterWidths)
		}
	})

	t.Run("publish without a poster or an overview is a usage error, before any work", func(t *testing.T) {
		posterFile := filepath.Join(t.TempDir(), "poster.png")
		writePNG(t, posterFile, 300, 450)
		for name, req := range map[string]request{
			"no poster":   {Overview: "Text", Publish: true},
			"no overview": {Poster: posterFile, Publish: true},
		} {
			rec := &recorder{Store: st}
			r := rec.request(t.TempDir(), clip, "x")
			r.Overview, r.Poster, r.Publish = req.Overview, req.Poster, req.Publish
			if _, err := ingestMovie(t.Context(), log, rec, r); !errors.Is(err, errUsage) || rec.transcode != 0 {
				t.Errorf("%s: err %v, %d transcodes", name, err, rec.transcode)
			}
		}
		// A refused image fails before the transcode too.
		text := filepath.Join(t.TempDir(), "poster.png")
		if err := os.WriteFile(text, []byte("not an image"), 0o600); err != nil {
			t.Fatal(err)
		}
		rec := &recorder{Store: st}
		r := rec.request(t.TempDir(), clip, "x")
		r.Poster = text
		if _, err := ingestMovie(t.Context(), log, rec, r); !errors.Is(err, poster.ErrUnsupported) || rec.transcode != 0 {
			t.Errorf("text as a poster: err %v, %d transcodes", err, rec.transcode)
		}
	})

	t.Run("ingests a movie end to end", func(t *testing.T) {
		root := t.TempDir()
		rec := &recorder{Store: st}
		req := rec.request(root, clip, "  My test  ")
		req.Year, req.Overview = 2019, "  A test clip.  "
		id, err := ingestMovie(t.Context(), log, rec, req)
		if err != nil {
			t.Fatal(err)
		}
		if !model.ValidID(id) {
			t.Fatalf("id = %q", id)
		}

		m, err := st.GetMovie(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		if m.Title != "My test" || m.Status != model.StatusReady || m.SourcePath != "sources/"+id+".mp4" ||
			!strings.HasPrefix(*m.LibraryPath, "library/"+id+"/") || *m.BackdropPath != version(*m.LibraryPath)+"/"+ffmpeg.PosterName ||
			*m.RuntimeSec < 5 || *m.RuntimeSec > 7 ||
			m.Year == nil || *m.Year != 2019 || m.Overview == nil || *m.Overview != "A test clip." {
			t.Fatalf("movie = %+v", m)
		}

		// The sources copy is byte-identical; the library dir has what rows describe.
		assertSameFile(t, clip, filepath.Join(root, "sources", id+".mp4"))
		// Paths in the rows are relative to library/{id}/; the files sit in
		// the version folder the row names.
		lib := filepath.Join(root, "library", id)
		v := version(*m.LibraryPath)
		for _, f := range []string{medialayout.MasterPlaylist, ffmpeg.PosterName} {
			if _, err := os.Stat(filepath.Join(lib, v, f)); err != nil {
				t.Errorf("missing %s/%s: %v", v, f, err)
			}
		}
		rs, err := st.ListRenditions(t.Context(), id)
		if err != nil || len(rs) == 0 {
			t.Fatalf("renditions = %v, %v", rs, err)
		}
		if rs[0].Height != 360 {
			t.Errorf("top rendition = %dp, want 360p (source height, never upscaled)", rs[0].Height)
		}

		for _, r := range rs {
			fi, err := os.Stat(filepath.Join(lib, filepath.Dir(r.PlaylistPath)))
			if err != nil || !fi.IsDir() {
				t.Errorf("rendition %+v has no rung dir: %v", r, err)
			}
			if _, err := os.Stat(filepath.Join(lib, r.PlaylistPath)); err != nil {
				t.Errorf("rendition playlist %s missing: %v", r.PlaylistPath, err)
			}
			if *r.SizeBytes <= 0 || *r.BitrateKbps <= 0 {
				t.Errorf("rendition %+v: size and bitrate must be measured", r)
			}
		}
		assertEntries(t, filepath.Join(root, "library"), id)
		assertEntries(t, filepath.Join(root, "sources"), id+".mp4")
		if _, err := os.Stat(filepath.Join(root, "inbox")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("devingest touched the inbox: %v", err)
		}
	})

	t.Run("real file names never reach the disk", func(t *testing.T) {
		root := t.TempDir()
		named := filepath.Join(t.TempDir(), "My Movie (2020).mp4")
		copyForTest(t, clip, named)
		rec := &recorder{Store: st}
		id, err := ingestMovie(t.Context(), log, rec, rec.request(root, named, "Named"))
		if err != nil {
			t.Fatal(err)
		}
		m, err := st.GetMovie(t.Context(), id)
		if err != nil || m.SourcePath != "sources/"+id+".mp4" || m.Title != "Named" {
			t.Fatalf("movie = %+v, %v", m, err)
		}
		assertEntries(t, filepath.Join(root, "sources"), id+".mp4")
	})

	t.Run("failed transcode leaves no rows and no folders", func(t *testing.T) {
		root := t.TempDir()
		bogus := filepath.Join(t.TempDir(), "bogus.mp4")
		if err := os.WriteFile(bogus, []byte("not a video"), 0o600); err != nil {
			t.Fatal(err)
		}
		movies, renditions := rows(t)
		rec := &recorder{Store: st}
		if _, err := ingestMovie(t.Context(), log, rec, rec.request(root, bogus, "Bogus")); err == nil {
			t.Fatal("expected an error")
		}
		if m, r := rows(t); m != movies || r != renditions || rec.creates != 0 {
			t.Fatalf("rows written: movies %d→%d, renditions %d→%d", movies, m, renditions, r)
		}
		assertEntries(t, filepath.Join(root, "sources"))
		assertEntries(t, filepath.Join(root, "library"))
	})

	t.Run("failed insert removes the transcoded movie", func(t *testing.T) {
		root := t.TempDir()
		movies, renditions := rows(t)
		rec := &recorder{Store: st, failWith: errors.New("injected insert failure")}
		named := filepath.Join(t.TempDir(), "insert-fails.mp4")
		copyForTest(t, clip, named)
		_, err := ingestMovie(t.Context(), log, rec, rec.request(root, named, "Insert fails"))
		if err == nil || !strings.Contains(err.Error(), "injected") {
			t.Fatalf("err = %v, want the injected failure", err)
		}
		if rec.transcode != 1 {
			t.Fatal("the transcode should have run before the insert")
		}
		if m, r := rows(t); m != movies || r != renditions {
			t.Fatal("rows written despite the failed insert")
		}
		assertEntries(t, filepath.Join(root, "sources"))
		assertEntries(t, filepath.Join(root, "library"))
	})

	t.Run("usage errors", func(t *testing.T) {
		rec := &recorder{Store: st}
		withYear := func(y int) request { r := rec.request(t.TempDir(), clip, "Title"); r.Year = y; return r }
		longOverview := rec.request(t.TempDir(), clip, "Title")
		longOverview.Overview = strings.Repeat("x", 2001)
		for _, req := range []request{
			rec.request(t.TempDir(), "", "Title"),
			rec.request(t.TempDir(), clip, "   "),
			withYear(1879), withYear(2101), longOverview,
		} {
			if _, err := ingestMovie(t.Context(), log, rec, req); !errors.Is(err, errUsage) {
				t.Errorf("err = %v, want errUsage", err)
			}
		}
		if _, err := ingestMovie(t.Context(), log, rec, rec.request(t.TempDir(), t.TempDir(), "Dir")); err == nil ||
			!strings.Contains(err.Error(), "not a regular file") {
			t.Errorf("a directory as input: %v", err)
		}
		notes := filepath.Join(t.TempDir(), "notes.txt")
		copyForTest(t, clip, notes)
		if _, err := ingestMovie(t.Context(), log, rec, rec.request(t.TempDir(), notes, "Notes")); err == nil ||
			!strings.Contains(err.Error(), "not a video file") || rec.transcode != 0 {
			t.Errorf("a non-video extension: %v (transcodes %d)", err, rec.transcode)
		}
	})
}

func assertEntries(t *testing.T, dir string, want ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	if !slices.Equal(got, want) {
		t.Fatalf("%s contains %v, want %v", dir, got, want)
	}
}

func assertSameFile(t *testing.T, a, b string) {
	t.Helper()
	x, err := os.ReadFile(a)
	if err != nil {
		t.Fatal(err)
	}
	y, err := os.ReadFile(b)
	if err != nil {
		t.Fatal(err)
	}
	if string(x) != string(y) {
		t.Fatalf("%s is not a copy of %s", b, a)
	}
}

func copyForTest(t *testing.T, src, dst string) {
	t.Helper()
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, b, 0o600); err != nil { // #nosec G703 -- dst is under t.TempDir()
		t.Fatal(err)
	}
}

// version is the version folder of a library_path "library/{id}/{version}".
func version(libraryPath string) string {
	return libraryPath[strings.LastIndexByte(libraryPath, '/')+1:]
}

// writePNG writes a w×h PNG to path.
func writePNG(t *testing.T, path string, w, h int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 60, A: 255}) // #nosec G115 -- wraps on purpose
		}
	}
	f, err := os.Create(path) // #nosec G304 -- under t.TempDir()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}
