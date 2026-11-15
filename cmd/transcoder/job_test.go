package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vtnHAN-SOLO/hls-vod/internal/ingest"
	"github.com/vtnHAN-SOLO/hls-vod/internal/medialayout"
	"github.com/vtnHAN-SOLO/hls-vod/internal/mediapath"
	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
	"github.com/vtnHAN-SOLO/hls-vod/internal/pgtest"
	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
	"github.com/vtnHAN-SOLO/hls-vod/internal/transcode"
)

var (
	clipOnce sync.Once
	clipPath string
	clipErr  error
)

// testClip is a generated 4s 320x180 H.264/AAC mp4 (remux top rung only),
// made once per test binary. Missing ffmpeg is a failure, not a skip.
func testClip(t *testing.T) string {
	t.Helper()
	clipOnce.Do(func() {
		for _, bin := range []string{"ffmpeg", "ffprobe"} {
			if _, err := exec.LookPath(bin); err != nil {
				clipErr = fmt.Errorf("%s not found in PATH (sudo apt install ffmpeg): %w", bin, err)
				return
			}
		}
		dir, err := os.MkdirTemp("", "transcoder-job-*")
		if err != nil {
			clipErr = err
			return
		}
		clipPath = filepath.Join(dir, "clip.mp4")
		// #nosec G204 -- fixed ffmpeg command; the only variable is a path under os.MkdirTemp.
		cmd := exec.CommandContext(context.Background(), "ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error",
			"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24:duration=4",
			"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=4",
			"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-g", "48",
			"-c:a", "aac", "-ac", "2", "-y", clipPath)
		if b, err := cmd.CombinedOutput(); err != nil {
			clipErr = fmt.Errorf("generating clip: %w: %s", err, b)
		}
	})
	if clipErr != nil {
		t.Fatal(clipErr)
	}
	return clipPath
}

func smallOptions(in, out string) transcode.Options {
	o := transcode.DefaultOptions(in, out)
	o.PosterHeight = 90
	return o
}

// failingFinish makes the dispatcher fail the job (its deadline) just before
// the transcoder records success.
type failingFinish struct{ *store.Store }

func (f failingFinish) FinishTranscode(ctx context.Context, t store.TranscodeJob, p store.Published) error {
	if _, err := f.FailJob(ctx, t.JobID, "deadline exceeded"); err != nil {
		return err
	}
	return f.Store.FinishTranscode(ctx, t, p)
}

// errLostAnswer stands for a COMMIT whose answer never arrived.
var errLostAnswer = errors.New("commit: connection reset by peer")

// committedThenLost writes everything, then reports an error, as a commit
// whose answer was lost does.
type committedThenLost struct{ *store.Store }

func (c committedThenLost) FinishTranscode(ctx context.Context, t store.TranscodeJob, p store.Published) error {
	if err := c.Store.FinishTranscode(ctx, t, p); err != nil {
		return err
	}
	return errLostAnswer
}

// lostAndBlind: the commit did not happen, and the re-read cannot reach the
// database either.
type lostAndBlind struct{ *store.Store }

func (lostAndBlind) FinishTranscode(context.Context, store.TranscodeJob, store.Published) error {
	return errLostAnswer
}

func (lostAndBlind) JobPublished(context.Context, string, string) (bool, error) {
	return false, errors.New("dial tcp: connection refused")
}

func TestJobMode(t *testing.T) {
	clip := testClip(t)
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

	// queued sets up what the watcher and dispatcher leave behind: a movie
	// whose source is sources/{id}.mp4 (written from data unless nil), and
	// its job running.
	type fixture struct{ root, id string }
	running := func(t *testing.T, sourcePath string, data []byte) fixture {
		t.Helper()
		root := t.TempDir()
		id := model.NewID()
		if sourcePath == "" {
			sourcePath = medialayout.SourcePath(id, "mp4")
		}
		if data != nil {
			p := filepath.Join(root, filepath.FromSlash(sourcePath))
			if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, data, 0o600); err != nil { // #nosec G703 -- under t.TempDir()
				t.Fatal(err)
			}
		}
		if err := st.EnqueueMovie(t.Context(), store.PendingMovie{ID: id, Title: "Clip", SourcePath: sourcePath},
			func() error { return nil }); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := st.ClaimJob(t.Context(), 100, nil); err != nil || !ok {
			t.Fatalf("ClaimJob: %v, %v", ok, err)
		}
		return fixture{root, id}
	}
	type state struct{ movie, job, errMsg string }
	stateOf := func(t *testing.T, id string) state {
		t.Helper()
		var s state
		var msg *string
		if err := db.Pool.QueryRow(t.Context(), `
			SELECT m.status, j.status, j.error_msg FROM movies m JOIN transcode_jobs j ON j.movie_id = m.id
			WHERE m.id = $1 ORDER BY j.created_at DESC LIMIT 1`, id).Scan(&s.movie, &s.job, &msg); err != nil {
			t.Fatal(err)
		}
		if msg != nil {
			s.errMsg = *msg
		}
		return s
	}
	// entries lists library/{id}/: the version folders (and anything else).
	entries := func(t *testing.T, f fixture) []string {
		t.Helper()
		es, err := os.ReadDir(filepath.Join(f.root, "library", f.id))
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, e := range es {
			names = append(names, e.Name())
		}
		return names
	}
	assertNoLibrary := func(t *testing.T, f fixture) {
		t.Helper()
		if names := entries(t, f); len(names) != 0 {
			t.Fatalf("library/%s holds %v after a failure; want no version left behind", f.id, names)
		}
	}
	jobOf := func(t *testing.T, id string) string {
		t.Helper()
		var job string
		if err := db.Pool.QueryRow(t.Context(),
			`SELECT id::text FROM transcode_jobs WHERE movie_id = $1 ORDER BY created_at DESC LIMIT 1`, id).Scan(&job); err != nil {
			t.Fatal(err)
		}
		return job
	}
	clipBytes, err := os.ReadFile(clip)
	if err != nil {
		t.Fatal(err)
	}
	var opts ingest.OptionsFunc = smallOptions

	t.Run("transcodes, then records everything in one go", func(t *testing.T) {
		f := running(t, "", clipBytes)
		if err := runJob(t.Context(), log, st, jobEnv{mediaRoot: f.root, opts: opts}, strings.ToUpper(f.id)); err != nil {
			t.Fatal(err)
		}
		if s := stateOf(t, f.id); s != (state{"ready", "succeeded", ""}) {
			t.Fatalf("state = %+v", s)
		}
		// The version folder is the job's id.
		v := jobOf(t, f.id)
		m, err := st.GetMovie(t.Context(), f.id)
		if err != nil || *m.LibraryPath != "library/"+f.id+"/"+v || *m.PosterPath != v+"/poster.jpg" ||
			*m.RuntimeSec < 3 || *m.RuntimeSec > 5 || m.LibraryCleanupAfter != nil {
			t.Fatalf("movie = %+v, %v", m, err)
		}
		rs, err := st.ListRenditions(t.Context(), f.id)
		if err != nil || len(rs) != 1 || rs[0].Height != 180 || !strings.HasPrefix(rs[0].PlaylistPath, v+"/") {
			t.Fatalf("renditions = %+v, %v; want one 180p rung (source height, never upscaled) in the version", rs, err)
		}
		if _, err := os.Stat(filepath.Join(f.root, "library", f.id, v, medialayout.MasterPlaylist)); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("a broken source fails job and movie with the error", func(t *testing.T) {
		f := running(t, "", []byte("not a video"))
		if err := runJob(t.Context(), log, st, jobEnv{mediaRoot: f.root, opts: opts}, f.id); err == nil {
			t.Fatal("expected an error")
		}
		s := stateOf(t, f.id)
		if s.movie != "failed" || s.job != "failed" || !strings.Contains(s.errMsg, "ffprobe") {
			t.Fatalf("state = %+v, want both failed with the ffprobe error", s)
		}
		assertNoLibrary(t, f)
	})

	t.Run("an unsafe source_path never reaches ffmpeg", func(t *testing.T) {
		for _, src := range []string{"../../etc/passwd", "/etc/passwd", "sources/%2e%2e%2fx.mp4"} {
			f := running(t, src, nil)
			err := runJob(t.Context(), log, st, jobEnv{mediaRoot: f.root, opts: opts}, f.id)
			if err == nil || !strings.Contains(err.Error(), "unsafe media path") {
				t.Fatalf("source_path %q: err = %v, want unsafe", src, err)
			}
			if s := stateOf(t, f.id); s.job != "failed" || !strings.Contains(s.errMsg, "unsafe") {
				t.Fatalf("source_path %q: state = %+v", src, s)
			}
		}
	})

	t.Run("a symlinked source is refused", func(t *testing.T) {
		f := running(t, "", nil)
		p := filepath.Join(f.root, filepath.FromSlash(medialayout.SourcePath(f.id, "mp4")))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(clip, p); err != nil {
			t.Fatal(err)
		}
		if err := runJob(t.Context(), log, st, jobEnv{mediaRoot: f.root, opts: opts}, f.id); err == nil {
			t.Fatal("a symlink outside MEDIA_ROOT was transcoded")
		}
		if s := stateOf(t, f.id); s.job != "failed" {
			t.Fatalf("state = %+v", s)
		}
	})

	t.Run("an existing version folder is never overwritten", func(t *testing.T) {
		f := running(t, "", clipBytes)
		lib := filepath.Join(f.root, "library", f.id, jobOf(t, f.id))
		if err := os.MkdirAll(lib, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(lib, "master.m3u8"), []byte("earlier"), 0o600); err != nil {
			t.Fatal(err)
		}
		err := runJob(t.Context(), log, st, jobEnv{mediaRoot: f.root, opts: opts}, f.id)
		if err == nil || !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("err = %v, want a refusal", err)
		}
		if b, _ := os.ReadFile(filepath.Join(lib, "master.m3u8")); string(b) != "earlier" {
			t.Fatal("the existing folder was changed")
		}
		if s := stateOf(t, f.id); s.job != "failed" {
			t.Fatalf("state = %+v", s)
		}
	})

	t.Run("no running job: refused, nothing changed", func(t *testing.T) {
		id := model.NewID()
		if err := st.EnqueueMovie(t.Context(), store.PendingMovie{ID: id, Title: "Q", SourcePath: medialayout.SourcePath(id, "mp4")},
			func() error { return nil }); err != nil {
			t.Fatal(err)
		}
		// Left queued on purpose; removed afterwards so the other
		// subtests' ClaimJob (oldest first) does not pick it up.
		t.Cleanup(func() {
			if _, err := db.Pool.Exec(context.Background(), `DELETE FROM movies WHERE id = $1`, id); err != nil {
				t.Error(err)
			}
		})
		err := runJob(t.Context(), log, st, jobEnv{mediaRoot: t.TempDir(), opts: opts}, id)
		if !errors.Is(err, store.ErrNoRunningJob) {
			t.Fatalf("err = %v, want ErrNoRunningJob", err)
		}
		if s := stateOf(t, id); s != (state{"pending", "queued", ""}) {
			t.Fatalf("state = %+v, want untouched", s)
		}
	})

	t.Run("failed by the dispatcher meanwhile: not recorded as ready, folder removed", func(t *testing.T) {
		f := running(t, "", clipBytes)
		err := runJob(t.Context(), log, failingFinish{st}, jobEnv{mediaRoot: f.root, opts: opts}, f.id)
		if !errors.Is(err, store.ErrJobNotRunning) {
			t.Fatalf("err = %v, want ErrJobNotRunning", err)
		}
		if s := stateOf(t, f.id); s != (state{"failed", "failed", "deadline exceeded"}) {
			t.Fatalf("state = %+v, want the dispatcher's failure kept", s)
		}
		assertNoLibrary(t, f)
	})

	t.Run("an error from a commit that went through: re-read, folder kept, success", func(t *testing.T) {
		f := running(t, "", clipBytes)
		if err := runJob(t.Context(), log, committedThenLost{st}, jobEnv{mediaRoot: f.root, opts: opts}, f.id); err != nil {
			t.Fatalf("err = %v, want success after the re-read", err)
		}
		if s := stateOf(t, f.id); s != (state{"ready", "succeeded", ""}) {
			t.Fatalf("state = %+v", s)
		}
		v := jobOf(t, f.id)
		if _, err := os.Stat(filepath.Join(f.root, "library", f.id, v, medialayout.MasterPlaylist)); err != nil {
			t.Fatalf("the published version was removed: %v", err)
		}
	})

	t.Run("an error and no way to re-read: folder kept, job failed, cleanup scheduled", func(t *testing.T) {
		f := running(t, "", clipBytes)
		err := runJob(t.Context(), log, lostAndBlind{st}, jobEnv{mediaRoot: f.root, opts: opts}, f.id)
		if !errors.Is(err, errLostAnswer) {
			t.Fatalf("err = %v", err)
		}
		if s := stateOf(t, f.id); s.movie != "failed" || s.job != "failed" {
			t.Fatalf("state = %+v", s)
		}
		if names := entries(t, f); len(names) != 1 || names[0] != jobOf(t, f.id) {
			t.Fatalf("library/%s = %v, want the version kept for the watcher", f.id, names)
		}
		m, err := st.GetMovie(t.Context(), f.id)
		if err != nil || m.LibraryPath != nil || m.LibraryCleanupAfter == nil {
			t.Fatalf("movie = %+v, %v; want no library_path and a cleanup time, so the watcher removes the folder", m, err)
		}
	})

	t.Run("a stop signal marks the job failed as interrupted", func(t *testing.T) {
		f := running(t, "", clipBytes)
		// SIGTERM arrives once the transcode is under way.
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		cancelAtStart := func(in, out string) transcode.Options {
			cancel()
			return smallOptions(in, out)
		}
		if err := runJob(ctx, log, st, jobEnv{mediaRoot: f.root, opts: cancelAtStart}, f.id); err == nil {
			t.Fatal("expected an error")
		}
		if s := stateOf(t, f.id); s.job != "failed" || !strings.HasPrefix(s.errMsg, "interrupted by a stop signal") {
			t.Fatalf("state = %+v", s)
		}
		assertNoLibrary(t, f)
	})

	// published is a movie ingested for real, then a retranscode queued and
	// started for it, as the admin endpoint and the dispatcher do.
	published := func(t *testing.T) (f fixture, oldVersion string) {
		t.Helper()
		f = running(t, "", clipBytes)
		if err := runJob(t.Context(), log, st, jobEnv{mediaRoot: f.root, opts: opts}, f.id); err != nil {
			t.Fatal(err)
		}
		oldVersion = jobOf(t, f.id)
		if err := st.RetranscodeMovie(t.Context(), f.id); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := st.ClaimJob(t.Context(), 100, nil); err != nil || !ok {
			t.Fatalf("ClaimJob: %v, %v", ok, err)
		}
		return f, oldVersion
	}
	movieOf := func(t *testing.T, id string) model.Movie {
		t.Helper()
		m, err := st.GetMovie(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	const grace = 25 * time.Hour

	t.Run("retranscode: stays ready on the old version, then switches in one go", func(t *testing.T) {
		f, old := published(t)
		newVersion := jobOf(t, f.id)
		var during model.Movie
		watch := func(in, out string) transcode.Options {
			during = movieOf(t, f.id) // while ffmpeg is about to run
			return smallOptions(in, out)
		}
		env := jobEnv{mediaRoot: f.root, opts: watch, grace: grace,
			freeBytes: func(*mediapath.Root, string) (uint64, error) { return 1 << 40, nil }}
		before := time.Now()
		if err := runJob(t.Context(), log, st, env, f.id); err != nil {
			t.Fatal(err)
		}
		if during.Status != model.StatusReady || *during.LibraryPath != "library/"+f.id+"/"+old {
			t.Fatalf("during the job: %+v; want ready on the old version", during)
		}
		if s := stateOf(t, f.id); s != (state{"ready", "succeeded", ""}) {
			t.Fatalf("state = %+v", s)
		}
		m := movieOf(t, f.id)
		if *m.LibraryPath != "library/"+f.id+"/"+newVersion || *m.PosterPath != newVersion+"/poster.jpg" {
			t.Fatalf("movie = %+v; want the new version", m)
		}
		if m.LibraryCleanupAfter == nil || m.LibraryCleanupAfter.Before(before.Add(grace-time.Minute)) ||
			m.LibraryCleanupAfter.After(time.Now().Add(grace+time.Minute)) {
			t.Fatalf("cleanup after = %v, want about now + %v", m.LibraryCleanupAfter, grace)
		}
		rs, err := st.ListRenditions(t.Context(), f.id)
		if err != nil || len(rs) != 1 || !strings.HasPrefix(rs[0].PlaylistPath, newVersion+"/") {
			t.Fatalf("renditions = %+v, %v; want only the new version's", rs, err)
		}
		// Both versions on disk until the cleanup.
		if got := entries(t, f); len(got) != 2 || !slices.Contains(got, old) || !slices.Contains(got, newVersion) {
			t.Fatalf("library/%s = %v; want both versions", f.id, got)
		}
	})

	t.Run("retranscode that fails: job failed, movie still ready on the old version, nothing new left", func(t *testing.T) {
		f, old := published(t)
		// The source is broken since the first ingest.
		if err := os.WriteFile(filepath.Join(f.root, filepath.FromSlash(medialayout.SourcePath(f.id, "mp4"))), []byte("not a video"), 0o600); err != nil {
			t.Fatal(err)
		}
		env := jobEnv{mediaRoot: f.root, opts: opts, grace: grace,
			freeBytes: func(*mediapath.Root, string) (uint64, error) { return 1 << 40, nil }}
		if err := runJob(t.Context(), log, st, env, f.id); err == nil {
			t.Fatal("expected an error")
		}
		if s := stateOf(t, f.id); s.movie != "ready" || s.job != "failed" || !strings.Contains(s.errMsg, "ffprobe") {
			t.Fatalf("state = %+v", s)
		}
		if m := movieOf(t, f.id); *m.LibraryPath != "library/"+f.id+"/"+old {
			t.Fatalf("library_path = %s, want the old version", *m.LibraryPath)
		}
		if got := entries(t, f); len(got) != 1 || got[0] != old {
			t.Fatalf("library/%s = %v; want only the old version", f.id, got)
		}
	})

	t.Run("retranscode without the space for it: failed with the numbers, nothing written", func(t *testing.T) {
		f, old := published(t)
		env := jobEnv{mediaRoot: f.root, opts: opts, grace: grace,
			freeBytes: func(*mediapath.Root, string) (uint64, error) { return 10 << 20, nil }}
		if err := runJob(t.Context(), log, st, env, f.id); err == nil {
			t.Fatal("expected an error")
		}
		s := stateOf(t, f.id)
		if s.movie != "ready" || s.job != "failed" || !strings.HasPrefix(s.errMsg, "not enough free space: need 96.") ||
			!strings.HasSuffix(s.errMsg, "have 10.0 MiB") {
			t.Fatalf("state = %+v", s)
		}
		if got := entries(t, f); len(got) != 1 || got[0] != old {
			t.Fatalf("library/%s = %v; want only the old version", f.id, got)
		}
	})
}
