package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vtnHAN-SOLO/hls-vod/internal/medialayout"
	"github.com/vtnHAN-SOLO/hls-vod/internal/mediapath"
	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
	"github.com/vtnHAN-SOLO/hls-vod/internal/pgtest"
	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
)

// One Postgres for every subtest; each one truncates it and gets its own
// media tree under t.TempDir().
func TestWatcher(t *testing.T) {
	db := pgtest.Start(t)
	if _, err := db.Migrator(t).Up(t.Context()); err != nil {
		t.Fatalf("migrating: %v", err)
	}
	st, err := store.New(t.Context(), db.DSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)

	type env struct {
		dir  string
		root *mediapath.Root
	}
	setup := func(t *testing.T) env {
		t.Helper()
		if _, err := db.Pool.Exec(t.Context(), `TRUNCATE movies CASCADE`); err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		for _, d := range []string{medialayout.Inbox, medialayout.Sources} {
			if err := os.Mkdir(filepath.Join(dir, d), 0o750); err != nil {
				t.Fatal(err)
			}
		}
		root, err := mediapath.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = root.Close() })
		return env{dir, root}
	}
	newScanner := func(e env, q queue) *scanner {
		return &scanner{root: e.root, q: q, log: slog.New(slog.DiscardHandler), now: time.Now, newID: model.NewID}
	}
	type row struct{ id, title, source, status, job string }
	rows := func(t *testing.T) []row {
		t.Helper()
		r, err := db.Pool.Query(t.Context(), `
			SELECT m.id::text, m.title, m.source_path, m.status, j.status
			FROM movies m JOIN transcode_jobs j ON j.movie_id = m.id ORDER BY m.title`)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		var out []row
		for r.Next() {
			var x row
			if err := r.Scan(&x.id, &x.title, &x.source, &x.status, &x.job); err != nil {
				t.Fatal(err)
			}
			out = append(out, x)
		}
		var movies int
		if err := db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM movies`).Scan(&movies); err != nil {
			t.Fatal(err)
		}
		if movies != len(out) {
			t.Fatalf("%d movies but %d with a job: every movie must have exactly one", movies, len(out))
		}
		return out
	}
	// assertQueued checks that every row is pending + queued and its
	// source_path is a real file in sources/, and that sources/ holds
	// nothing else.
	assertQueued := func(t *testing.T, e env, rs []row) {
		t.Helper()
		var want []string
		for _, r := range rs {
			if r.status != "pending" || r.job != "queued" {
				t.Errorf("%+v: want movie pending, job queued", r)
			}
			if !strings.HasPrefix(r.source, "sources/"+r.id+".") {
				t.Errorf("source_path %q is not sources/{id}.{ext}", r.source)
			}
			want = append(want, strings.TrimPrefix(r.source, "sources/"))
		}
		slices.Sort(want)
		assertEntries(t, filepath.Join(e.dir, medialayout.Sources), want...)
	}
	titles := func(rs []row) []string {
		out := make([]string, len(rs))
		for i, r := range rs {
			out[i] = r.title
		}
		return out
	}

	t.Run("claims eligible files and titles them from the original name", func(t *testing.T) {
		e := setup(t)
		inbox := filepath.Join(e.dir, medialayout.Inbox)
		for _, name := range []string{
			"My Movie (2020).mkv", "Amélie [Director's Cut].MP4", "The.Matrix.1999.m4v",
		} {
			writeOld(t, filepath.Join(inbox, name), "movie")
		}
		skipped := []string{
			".hidden.mkv", "movie.mkv.part", "movie.mp4.tmp", "movie.mp4.crdownload", "notes.txt",
		}
		for _, name := range skipped {
			writeOld(t, filepath.Join(inbox, name), "x")
		}
		if err := os.Mkdir(filepath.Join(inbox, "Folder.mkv"), 0o750); err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(t.TempDir(), "elsewhere.mkv")
		writeOld(t, outside, "not in the inbox")
		if err := os.Symlink(outside, filepath.Join(inbox, "link.mkv")); err != nil {
			t.Fatal(err)
		}
		skipped = append(skipped, "Folder.mkv", "link.mkv")

		r, err := newScanner(e, st).run(t.Context())
		if err != nil || r.Claimed != 3 || r.Skipped != len(skipped) || r.Failed != 0 {
			t.Fatalf("report %+v, err %v", r, err)
		}
		rs := rows(t)
		if got, want := titles(rs), []string{"Amélie [Director's Cut]", "My Movie (2020)", "The.Matrix.1999"}; !slices.Equal(got, want) {
			t.Fatalf("titles = %q, want %q", got, want)
		}
		for _, x := range rs {
			if x.title == "Amélie [Director's Cut]" && !strings.HasSuffix(x.source, ".mp4") {
				t.Errorf("extension not lowercased: %s", x.source)
			}
		}
		assertQueued(t, e, rs)
		slices.Sort(skipped)
		assertEntries(t, inbox, skipped...)

		// Nothing new: a second run writes nothing.
		if r, err := newScanner(e, st).run(t.Context()); err != nil || r.Claimed != 0 {
			t.Fatalf("second run: %+v, %v", r, err)
		}
		if n := len(rows(t)); n != 3 {
			t.Fatalf("second run changed the catalog: %d rows", n)
		}
	})

	t.Run("a file younger than 2 minutes is skipped until it settles", func(t *testing.T) {
		e := setup(t)
		path := filepath.Join(e.dir, medialayout.Inbox, "copying.mkv")
		writeAt(t, path, "half", time.Now().Add(-minAge+5*time.Second))

		r, err := newScanner(e, st).run(t.Context())
		if err != nil || r.Claimed != 0 || r.Skipped != 1 {
			t.Fatalf("report %+v, err %v", r, err)
		}
		if len(rows(t)) != 0 {
			t.Fatal("a fresh file was catalogued")
		}
		assertEntries(t, filepath.Join(e.dir, medialayout.Inbox), "copying.mkv")

		// The copy finished long enough ago: now it is claimed.
		if err := os.Chtimes(path, time.Time{}, time.Now().Add(-minAge-time.Second)); err != nil {
			t.Fatal(err)
		}
		if r, err := newScanner(e, st).run(t.Context()); err != nil || r.Claimed != 1 {
			t.Fatalf("after settling: %+v, %v", r, err)
		}
		assertQueued(t, e, rows(t))
	})

	t.Run("two concurrent runs claim each file exactly once", func(t *testing.T) {
		e := setup(t)
		const n = 40
		var want []string
		for i := range n {
			name := fmt.Sprintf("Movie %02d (19%02d).mkv", i, 50+i)
			writeOld(t, filepath.Join(e.dir, medialayout.Inbox, name), name)
			want = append(want, strings.TrimSuffix(name, ".mkv"))
		}

		// Both runs list the full inbox before either claims anything, so
		// every file is contended.
		var listed sync.WaitGroup
		listed.Add(2)
		reports := make([]report, 2)
		errs := make([]error, 2)
		var wg sync.WaitGroup
		for i := range 2 {
			s := newScanner(e, st)
			s.afterList = func() { listed.Done(); listed.Wait() }
			wg.Go(func() { reports[i], errs[i] = s.run(t.Context()) })
		}
		wg.Wait()

		for i := range 2 {
			if errs[i] != nil || reports[i].Failed != 0 {
				t.Fatalf("run %d: %+v, %v", i, reports[i], errs[i])
			}
		}
		if c := reports[0].Claimed + reports[1].Claimed; c != n {
			t.Fatalf("claimed %d + %d = %d, want %d", reports[0].Claimed, reports[1].Claimed, c, n)
		}
		if s := reports[0].Skipped + reports[1].Skipped; s != n {
			t.Fatalf("skipped %d, want %d: each file lost by exactly one run", s, n)
		}
		rs := rows(t)
		if got := titles(rs); !slices.Equal(got, want) {
			t.Fatalf("titles = %q, want each original name once", got)
		}
		assertQueued(t, e, rs)
		assertEntries(t, filepath.Join(e.dir, medialayout.Inbox))
		// Same contents as before: the file under each id is the one it was titled from.
		for _, r := range rs {
			b, err := os.ReadFile(filepath.Join(e.dir, filepath.FromSlash(r.source)))
			if err != nil || string(b) != r.title+".mkv" {
				t.Errorf("%s holds %q, want the file titled %q", r.source, b, r.title)
			}
		}
	})

	// The other run wins in the gap between this run's stat and its rename.
	t.Run("losing the rename race writes nothing", func(t *testing.T) {
		e := setup(t)
		writeOld(t, filepath.Join(e.dir, medialayout.Inbox, "contended.mkv"), "x")
		q := &hookQueue{Store: st, beforeClaim: func() {
			other := medialayout.SourceName(model.NewID(), "mkv")
			if err := e.root.RenameEntry(medialayout.Inbox, "contended.mkv", medialayout.Sources, other); err != nil {
				t.Fatal(err)
			}
		}}
		r, err := newScanner(e, q).run(t.Context())
		if err != nil || r.Claimed != 0 || r.Skipped != 1 || r.Failed != 0 {
			t.Fatalf("report %+v, err %v", r, err)
		}
		if len(rows(t)) != 0 {
			t.Fatal("the losing run wrote rows")
		}
	})

	t.Run("a failed insert puts the file back under its own name", func(t *testing.T) {
		e := setup(t)
		inbox := filepath.Join(e.dir, medialayout.Inbox)
		writeOld(t, filepath.Join(inbox, "My Movie (2020).mkv"), "mine")
		q := &hookQueue{Store: st, failInsert: errors.New("database went away")}
		r, err := newScanner(e, q).run(t.Context())
		if err == nil || !strings.Contains(err.Error(), "database went away") || r.Failed != 1 {
			t.Fatalf("report %+v, err %v; want the insert failure", r, err)
		}
		if len(rows(t)) != 0 {
			t.Fatal("rows written despite the failed insert")
		}
		assertEntries(t, inbox, "My Movie (2020).mkv")
		assertEntries(t, filepath.Join(e.dir, medialayout.Sources))

		// Someone drops a new file with the same name meanwhile: it is
		// never overwritten, ours comes back as {id}.mkv instead.
		q.beforeInsertFail = func() { writeOld(t, filepath.Join(inbox, "My Movie (2020).mkv"), "theirs") }
		if _, err := newScanner(e, q).run(t.Context()); err == nil {
			t.Fatal("want the insert failure")
		}
		es, _ := os.ReadDir(inbox)
		if len(es) != 2 {
			t.Fatalf("inbox = %v, want theirs and ours", es)
		}
		if b, _ := os.ReadFile(filepath.Join(inbox, "My Movie (2020).mkv")); string(b) != "theirs" {
			t.Fatalf("the new file was overwritten: %q", b)
		}
		assertEntries(t, filepath.Join(e.dir, medialayout.Sources))
	})

	t.Run("an error from a commit that went through: the claim stands, the file stays", func(t *testing.T) {
		e := setup(t)
		inbox := filepath.Join(e.dir, medialayout.Inbox)
		writeOld(t, filepath.Join(inbox, "My Movie (2020).mkv"), "mine")
		q := &hookQueue{Store: st, lostCommit: errors.New("commit: connection reset by peer")}
		r, err := newScanner(e, q).run(t.Context())
		if err != nil || r.Claimed != 1 || r.Failed != 0 {
			t.Fatalf("report %+v, err %v; want the claim counted after the re-read", r, err)
		}
		rs := rows(t)
		if len(rs) != 1 || rs[0].title != "My Movie (2020)" {
			t.Fatalf("rows = %+v", rs)
		}
		assertQueued(t, e, rs)
		assertEntries(t, inbox)
	})

	t.Run("an insert error and no way to re-read: the file waits in sources/ for reconcile", func(t *testing.T) {
		e := setup(t)
		inbox := filepath.Join(e.dir, medialayout.Inbox)
		writeOld(t, filepath.Join(inbox, "My Movie (2020).mkv"), "mine")
		q := &hookQueue{Store: st, failInsert: errors.New("database went away"), rereadErr: errors.New("connection refused")}
		if _, err := newScanner(e, q).run(t.Context()); err == nil {
			t.Fatal("want the insert failure")
		}
		assertEntries(t, inbox)
		es, _ := os.ReadDir(filepath.Join(e.dir, medialayout.Sources))
		if len(es) != 1 {
			t.Fatalf("sources/ = %v, want the claimed file left there", es)
		}
		// With no row, reconcile returns it once it is old enough. (The
		// rename kept the hour-old mtime.)
		r, err := newScanner(e, st).run(t.Context())
		if err != nil || r.Released != 1 {
			t.Fatalf("report %+v, err %v; want it released", r, err)
		}
	})

	t.Run("each run prunes auth rows no longer in use and reports them", func(t *testing.T) {
		e := setup(t)
		if _, err := db.Pool.Exec(t.Context(), `INSERT INTO oauth_states (state_hash, nonce, pkce_verifier, intent, expires_at)
			VALUES (decode(md5(random()::text), 'hex'), 'n', 'v', 'login', now() - interval '1 minute')`); err != nil {
			t.Fatal(err)
		}
		r, err := newScanner(e, st).run(t.Context())
		if err != nil || r.Pruned.OAuthStates != 1 {
			t.Fatalf("report %+v, err %v; want the expired sign-in pruned", r, err)
		}
		if r, err := newScanner(e, st).run(t.Context()); err != nil || r.Pruned != (store.Pruned{}) {
			t.Fatalf("second run: %+v, %v", r, err)
		}
	})

	t.Run("a crash between rename and insert is repaired by the next run", func(t *testing.T) {
		e := setup(t)
		sources := filepath.Join(e.dir, medialayout.Sources)

		// The crash: renamed into sources/, no row. Rename keeps the
		// original (old) mtime.
		crashed := model.NewID()
		writeOld(t, filepath.Join(sources, crashed+".mkv"), "crashed")
		// Not orphans: one has its row; one is a fresh devingest copy still
		// waiting for its insert; one is not a claimed name at all.
		kept := store.PendingMovie{ID: model.NewID(), Title: "Kept", SourcePath: medialayout.SourcePath(model.NewID(), "mkv")}
		writeOld(t, filepath.Join(e.dir, filepath.FromSlash(kept.SourcePath)), "kept")
		if err := st.EnqueueMovie(t.Context(), kept, func() error { return nil }); err != nil {
			t.Fatal(err)
		}
		fresh := model.NewID() + ".mp4"
		writeAt(t, filepath.Join(sources, fresh), "fresh", time.Now())
		writeOld(t, filepath.Join(sources, "README.txt"), "not ours")

		r, err := newScanner(e, st).run(t.Context())
		if err != nil || r.Released != 1 || r.Claimed != 1 {
			t.Fatalf("report %+v, err %v; want one released and re-claimed", r, err)
		}
		rs := rows(t)
		if len(rs) != 2 {
			t.Fatalf("rows = %+v", rs)
		}
		for _, x := range rs {
			if x.id == kept.ID {
				continue
			}
			// The original name was lost in the crash; the id stands in.
			if x.title != crashed || x.source == "sources/"+crashed+".mkv" {
				t.Fatalf("re-claimed row %+v, want titled %s under a new id", x, crashed)
			}
			if b, _ := os.ReadFile(filepath.Join(e.dir, filepath.FromSlash(x.source))); string(b) != "crashed" {
				t.Fatalf("re-claimed file holds %q", b)
			}
		}
		if _, err := os.Stat(filepath.Join(sources, fresh)); err != nil {
			t.Fatalf("the fresh copy was moved: %v", err)
		}
		if _, err := os.Stat(filepath.Join(sources, "README.txt")); err != nil {
			t.Fatalf("a non-source file was moved: %v", err)
		}
		assertEntries(t, filepath.Join(e.dir, medialayout.Inbox))
	})

	// deleted puts a movie in the tree and the catalog: source and library
	// files, a rendition, a finished job and someone's watch progress, then
	// marks it deleting as DELETE /admin/movies/{id} does.
	deleted := func(t *testing.T, e env, title string, files bool) string {
		t.Helper()
		id := model.NewID()
		source := medialayout.SourcePath(id, "mkv")
		if files {
			writeOld(t, filepath.Join(e.dir, filepath.FromSlash(source)), "source")
			lib := filepath.Join(e.dir, filepath.FromSlash(medialayout.LibraryPath(id)), "v0")
			if err := os.MkdirAll(lib, 0o750); err != nil {
				t.Fatal(err)
			}
			writeOld(t, filepath.Join(lib, "seg0.ts"), "segment")
			writeOld(t, filepath.Join(e.dir, filepath.FromSlash(medialayout.LibraryPath(id)), medialayout.MasterPlaylist), "#EXTM3U")
		}
		if _, err := db.Pool.Exec(t.Context(), `
			WITH m AS (INSERT INTO movies (id, title, source_path, library_path, status)
			           VALUES ($1, $2, $3, $4, 'ready') RETURNING id),
			     r AS (INSERT INTO renditions (movie_id, height, playlist_path) SELECT id, 480, 'v0/index.m3u8' FROM m),
			     j AS (INSERT INTO transcode_jobs (movie_id, status) SELECT id, 'succeeded' FROM m),
			     u AS (INSERT INTO users (username) VALUES ($5) RETURNING id)
			INSERT INTO watch_progress (user_id, movie_id, position_sec) SELECT u.id, m.id, 60 FROM u, m`,
			id, title, source, medialayout.LibraryPath(id), "viewer-"+id[:8]); err != nil {
			t.Fatal(err)
		}
		if err := st.DeleteMovie(t.Context(), id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	count := func(t *testing.T, sql, id string) int {
		t.Helper()
		var n int
		if err := db.Pool.QueryRow(t.Context(), sql, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	t.Run("a deleted movie's files and row are removed, and only its", func(t *testing.T) {
		e := setup(t)
		gone := deleted(t, e, "Gone", true)
		// Files already removed by an earlier, interrupted run: fine.
		halfDone := deleted(t, e, "Half done", false)
		kept := store.PendingMovie{ID: model.NewID(), Title: "Kept", SourcePath: medialayout.SourcePath(model.NewID(), "mkv")}
		writeOld(t, filepath.Join(e.dir, filepath.FromSlash(kept.SourcePath)), "kept")
		if err := st.EnqueueMovie(t.Context(), kept, func() error { return nil }); err != nil {
			t.Fatal(err)
		}

		r, err := newScanner(e, st).run(t.Context())
		if err != nil || r.Deleted != 2 || r.Released != 0 || r.Claimed != 0 {
			t.Fatalf("report %+v, err %v; want two deleted, nothing released or claimed", r, err)
		}
		for _, id := range []string{gone, halfDone} {
			for _, sql := range []string{
				`SELECT count(*) FROM movies WHERE id = $1`,
				`SELECT count(*) FROM renditions WHERE movie_id = $1`,
				`SELECT count(*) FROM transcode_jobs WHERE movie_id = $1`,
				`SELECT count(*) FROM watch_progress WHERE movie_id = $1`,
			} {
				if n := count(t, sql, id); n != 0 {
					t.Errorf("%s: %q = %d after the purge", id, sql, n)
				}
			}
		}
		assertEntries(t, filepath.Join(e.dir, medialayout.Library))
		assertEntries(t, filepath.Join(e.dir, medialayout.Sources), strings.TrimPrefix(kept.SourcePath, "sources/"))
		assertEntries(t, filepath.Join(e.dir, medialayout.Inbox)) // never handed back to be ingested again
		if rs := rows(t); len(rs) != 1 || rs[0].id != kept.ID {
			t.Fatalf("rows = %+v, want only the kept movie", rs)
		}

		// A second run, with everything already gone, is fine.
		if r, err := newScanner(e, st).run(t.Context()); err != nil || r.Deleted != 0 {
			t.Fatalf("second run: report %+v, err %v", r, err)
		}
	})

	t.Run("a source that cannot be removed keeps the row, and reconcile leaves it alone", func(t *testing.T) {
		e := setup(t)
		id := deleted(t, e, "Stuck", true)
		// A non-empty directory where the source file was: Remove fails.
		source := filepath.Join(e.dir, medialayout.Sources, id+".mkv")
		if err := os.Remove(source); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(source, 0o750); err != nil {
			t.Fatal(err)
		}
		writeOld(t, filepath.Join(source, "x"), "x")

		r, err := newScanner(e, st).run(t.Context())
		if err == nil || r.Failed != 1 || r.Deleted != 0 {
			t.Fatalf("report %+v, err %v; want the removal to fail", r, err)
		}
		if n := count(t, `SELECT count(*) FROM movies WHERE id = $1 AND status = 'deleting'`, id); n != 1 {
			t.Fatal("the row went before its source")
		}
		assertEntries(t, filepath.Join(e.dir, medialayout.Inbox))
		if _, err := os.Stat(filepath.Join(e.dir, medialayout.Library, id)); !os.IsNotExist(err) {
			t.Fatalf("library/%s/ still there: %v", id, err)
		}
	})

	// tree writes library/{id}/ with the given entries (folders with a
	// master playlist, or plain files for names with a dot).
	tree := func(t *testing.T, e env, id string, names ...string) {
		t.Helper()
		lib := filepath.Join(e.dir, medialayout.Library, id)
		if err := os.MkdirAll(lib, 0o750); err != nil {
			t.Fatal(err)
		}
		for _, n := range names {
			p := filepath.Join(lib, n)
			if strings.Contains(n, ".") && !strings.HasPrefix(n, ".") {
				writeOld(t, p, "file")
				continue
			}
			if err := os.MkdirAll(p, 0o750); err != nil {
				t.Fatal(err)
			}
			writeOld(t, filepath.Join(p, medialayout.MasterPlaylist), "#EXTM3U")
		}
	}
	// readyMovie inserts a ready movie playing libraryPath, whose cleanup is
	// due at cleanupAt (nil: none).
	readyMovie := func(t *testing.T, id, libraryPath string, cleanupAt *time.Time) {
		t.Helper()
		if _, err := db.Pool.Exec(t.Context(), `
			WITH m AS (INSERT INTO movies (id, title, source_path, library_path, status, library_cleanup_after)
			           VALUES ($1, 'R', $2, $3, 'ready', $4) RETURNING id)
			INSERT INTO transcode_jobs (movie_id, status) SELECT id, 'succeeded' FROM m`,
			id, medialayout.SourcePath(id, "mkv"), libraryPath, cleanupAt); err != nil {
			t.Fatal(err)
		}
	}
	cleanupAt := func(t *testing.T, id string) *time.Time {
		t.Helper()
		var at *time.Time
		if err := db.Pool.QueryRow(t.Context(), `SELECT library_cleanup_after FROM movies WHERE id = $1`, id).Scan(&at); err != nil {
			t.Fatal(err)
		}
		return at
	}

	t.Run("old versions go only after their time, and only the current one stays", func(t *testing.T) {
		e := setup(t)
		past, future := time.Now().Add(-time.Minute), time.Now().Add(time.Hour)
		oldV, curV := model.NewID(), model.NewID()

		due := model.NewID() // a retranscode published curV; oldV is past its grace
		tree(t, e, due, oldV, curV, "."+oldV+".partial-123")
		readyMovie(t, due, medialayout.VersionPath(due, curV), &past)

		// Published before versions, then retranscoded: the old files sit
		// directly in library/{id}/.
		legacy := model.NewID()
		tree(t, e, legacy, curV, "master.m3u8", "poster.jpg", "thumbnails.vtt", "720p", "thumbs")
		readyMovie(t, legacy, medialayout.VersionPath(legacy, curV), &past)

		early := model.NewID() // grace not over: untouched
		tree(t, e, early, oldV, curV)
		readyMovie(t, early, medialayout.VersionPath(early, curV), &future)

		busy := model.NewID() // due, but a job is running: untouched
		tree(t, e, busy, oldV, curV, "."+model.NewID()+".partial-9")
		readyMovie(t, busy, medialayout.VersionPath(busy, curV), &past)
		if _, err := db.Pool.Exec(t.Context(), `INSERT INTO transcode_jobs (movie_id, status, purpose) VALUES ($1, 'running', 'retranscode')`, busy); err != nil {
			t.Fatal(err)
		}

		r, err := newScanner(e, st).run(t.Context())
		if err != nil || r.Cleaned != 2 {
			t.Fatalf("report %+v, err %v; want two cleaned", r, err)
		}
		lib := func(id string) string { return filepath.Join(e.dir, medialayout.Library, id) }
		assertEntries(t, lib(due), curV)
		assertEntries(t, lib(legacy), curV)
		if _, err := os.Stat(filepath.Join(lib(due), curV, medialayout.MasterPlaylist)); err != nil {
			t.Fatalf("the current version lost a file: %v", err)
		}
		if cleanupAt(t, due) != nil || cleanupAt(t, legacy) != nil {
			t.Fatal("library_cleanup_after not cleared")
		}
		if got := entries(t, lib(early)); len(got) != 2 || cleanupAt(t, early) == nil {
			t.Fatalf("cleaned before its time: %v", got)
		}
		if got := entries(t, lib(busy)); len(got) != 3 || cleanupAt(t, busy) == nil {
			t.Fatalf("cleaned while a job runs: %v", got)
		}

		// Once the job is over, the next run does it; a run after that has
		// nothing left to do.
		if _, err := db.Pool.Exec(t.Context(), `UPDATE transcode_jobs SET status = 'failed' WHERE movie_id = $1 AND status = 'running'`, busy); err != nil {
			t.Fatal(err)
		}
		if r, err := newScanner(e, st).run(t.Context()); err != nil || r.Cleaned != 1 {
			t.Fatalf("second run: %+v, %v", r, err)
		}
		assertEntries(t, lib(busy), curV)
		if r, err := newScanner(e, st).run(t.Context()); err != nil || r.Cleaned != 0 {
			t.Fatalf("third run: %+v, %v", r, err)
		}
	})

	t.Run("cleanup of an old-layout movie keeps its files; missing folder is fine", func(t *testing.T) {
		e := setup(t)
		past := time.Now().Add(-time.Minute)
		// A failed retranscode of a movie published before versions: only
		// the killed run's leftovers go.
		legacy := model.NewID()
		tree(t, e, legacy, "master.m3u8", "poster.jpg", "720p", "."+model.NewID()+".partial-1")
		readyMovie(t, legacy, medialayout.LibraryPath(legacy), &past)
		gone := model.NewID() // no folder at all
		readyMovie(t, gone, medialayout.VersionPath(gone, model.NewID()), &past)
		odd := model.NewID() // a library_path of another shape: nothing removed
		tree(t, e, odd, "a", "b")
		readyMovie(t, odd, "library/"+odd+"/a/b", &past)

		r, err := newScanner(e, st).run(t.Context())
		if err == nil || r.Cleaned != 2 || r.Failed != 1 {
			t.Fatalf("report %+v, err %v; want two cleaned, the odd one refused", r, err)
		}
		assertEntries(t, filepath.Join(e.dir, medialayout.Library, legacy), "720p", "master.m3u8", "poster.jpg")
		assertEntries(t, filepath.Join(e.dir, medialayout.Library, odd), "a", "b")
		if cleanupAt(t, odd) == nil {
			t.Fatal("a refused cleanup cleared its time")
		}
	})

	t.Run("purging a deleted movie removes every version", func(t *testing.T) {
		e := setup(t)
		id := deleted(t, e, "Versions", true)
		tree(t, e, id, model.NewID(), model.NewID(), "."+model.NewID()+".partial-7")
		if r, err := newScanner(e, st).run(t.Context()); err != nil || r.Deleted != 1 {
			t.Fatalf("report %+v, err %v", r, err)
		}
		assertEntries(t, filepath.Join(e.dir, medialayout.Library))
	})
}

func TestTitleFrom(t *testing.T) {
	tests := []struct{ in, want string }{
		{"My Movie (2020).mkv", "My Movie (2020)"},
		{"  padded  .mp4", "padded"},
		{"The.Matrix.1999.1080p.mkv", "The.Matrix.1999.1080p"},
		{"Amélie.mkv", "Amélie"},
		{"bad\xffbyte.mkv", "bad�byte"},
		{" .mkv", " .mkv"},
	}
	for _, tt := range tests {
		if got := titleFrom(tt.in); got != tt.want {
			t.Errorf("titleFrom(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// hookQueue wraps the real store to interleave another run or fail the insert.
type hookQueue struct {
	*store.Store
	beforeClaim      func()
	failInsert       error
	beforeInsertFail func()
	// lostCommit: EnqueueMovie commits, then reports this error, as a
	// commit whose answer was lost does.
	lostCommit error
	// rereadErr makes SourceClaimed fail.
	rereadErr error
}

func (h *hookQueue) SourceClaimed(ctx context.Context, sourcePath string) (bool, error) {
	if h.rereadErr != nil {
		return false, h.rereadErr
	}
	return h.Store.SourceClaimed(ctx, sourcePath)
}

func (h *hookQueue) EnqueueMovie(ctx context.Context, m store.PendingMovie, claim func() error) error {
	if h.beforeClaim != nil {
		h.beforeClaim()
	}
	if h.lostCommit != nil {
		if err := h.Store.EnqueueMovie(ctx, m, claim); err != nil {
			return err
		}
		return h.lostCommit
	}
	if h.failInsert == nil {
		return h.Store.EnqueueMovie(ctx, m, claim)
	}
	if err := claim(); err != nil {
		return err
	}
	if h.beforeInsertFail != nil {
		h.beforeInsertFail()
	}
	return h.failInsert
}

func writeOld(t *testing.T, path, data string) {
	t.Helper()
	writeAt(t, path, data, time.Now().Add(-time.Hour))
}

func writeAt(t *testing.T, path, data string, mtime time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, time.Time{}, mtime); err != nil {
		t.Fatal(err)
	}
}

func assertEntries(t *testing.T, dir string, want ...string) {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, e := range es {
		got = append(got, e.Name())
	}
	if want == nil {
		want = []string{}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("%s = %q, want %q", dir, got, want)
	}
}

func entries(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}
