package store_test

import (
	"encoding/hex"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
	"github.com/vtnHAN-SOLO/hls-vod/internal/pgtest"
	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
)

func TestAdmin(t *testing.T) {
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
	pwHash := hex.EncodeToString(randomHash(t)) // the store never inspects it
	movie := func(t *testing.T, title, source string, status model.MovieStatus) string {
		t.Helper()
		var id string
		if err := db.Pool.QueryRow(ctx, `INSERT INTO movies (title, source_path, status) VALUES ($1, $2, $3) RETURNING id::text`,
			title, source, string(status)).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}

	user := func(t *testing.T, name string, role model.Role) model.User {
		t.Helper()
		u, err := st.CreateUser(ctx, name, pwHash, role)
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	login := func(t *testing.T, userID string) (string, []byte) {
		t.Helper()
		h := randomHash(t)
		family, err := st.CreateRefreshToken(ctx, userID, store.NewRefresh{Hash: h, TTL: time.Hour})
		if err != nil {
			t.Fatal(err)
		}
		return family, h
	}
	reset := func(t *testing.T) {
		t.Helper()
		if _, err := db.Pool.Exec(ctx, `TRUNCATE users, movies CASCADE`); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("AdminCheck reads the role, disabled_at and the session from the database", func(t *testing.T) {
		reset(t)
		ann := user(t, "ann", model.RoleAdmin)
		bo := user(t, "bo", model.RoleUser)
		annSession, _ := login(t, ann.ID)
		boSession, _ := login(t, bo.ID)
		check := func(userID, family string) (bool, bool) {
			t.Helper()
			admin, live, err := st.AdminCheck(ctx, userID, family)
			if err != nil {
				t.Fatal(err)
			}
			return admin, live
		}
		if admin, live := check(ann.ID, annSession); !admin || !live {
			t.Fatalf("ann = %v, %v", admin, live)
		}
		if admin, live := check(bo.ID, boSession); admin || !live {
			t.Fatalf("bo = %v, %v", admin, live)
		}
		if admin, live := check(ann.ID, boSession); !admin || live {
			t.Fatalf("ann with bo's session = %v, %v", admin, live)
		}
		if admin, _ := check(model.NewID(), annSession); admin {
			t.Fatal("an unknown user is an admin")
		}
		if _, err := db.Pool.Exec(ctx, `UPDATE users SET disabled_at = now() WHERE id = $1`, ann.ID); err != nil {
			t.Fatal(err)
		}
		if admin, _ := check(ann.ID, annSession); admin {
			t.Fatal("a disabled admin is still an admin")
		}
	})

	t.Run("users list: search, Google, no password, pages", func(t *testing.T) {
		reset(t)
		_ = user(t, "ann", model.RoleAdmin)
		if _, err := db.Pool.Exec(ctx, `
			WITH u AS (INSERT INTO users (username, email, email_verified_at) VALUES ('gina-x7k2', 'gina@example.com', now()) RETURNING id)
			INSERT INTO user_identities (user_id, provider, subject) SELECT id, 'google', 'sub-1' FROM u`); err != nil {
			t.Fatal(err)
		}
		_ = user(t, "a_b", model.RoleUser)
		list := func(q *string, limit, offset int64) ([]model.UserSummary, int64) {
			t.Helper()
			us, total, err := st.ListUserSummaries(ctx, store.UserFilter{Query: q, Limit: limit, Offset: offset})
			if err != nil {
				t.Fatal(err)
			}
			return us, total
		}
		all, total := list(nil, 10, 0)
		if total != 3 || len(all) != 3 || all[0].Username != "a_b" || all[2].Username != "gina-x7k2" {
			t.Fatalf("all = %+v (total %d)", all, total)
		}
		gina := all[2]
		if !gina.GoogleLinked || gina.HasPassword() || all[0].GoogleLinked || !all[0].HasPassword() {
			t.Fatalf("flags: %+v / %+v", gina, all[0])
		}
		for _, tt := range []struct {
			q    string
			want int64
		}{{"GINA@", 1}, {"example.com", 1}, {"ann", 1}, {"_", 1}, {"%", 0}, {"nobody", 0}} {
			q := tt.q
			if _, n := list(&q, 10, 0); n != tt.want {
				t.Errorf("q=%q: %d, want %d", q, n, tt.want)
			}
		}
		if page, n := list(nil, 1, 1); n != 3 || len(page) != 1 || page[0].Username != "ann" {
			t.Fatalf("page 2 = %+v", page)
		}
	})

	t.Run("disable revokes every session; enable keeps them revoked", func(t *testing.T) {
		reset(t)
		ann := user(t, "ann", model.RoleAdmin)
		bo := user(t, "bo", model.RoleUser)
		_, h1 := login(t, bo.ID)
		_, h2 := login(t, bo.ID)
		u, err := st.SetUserDisabled(ctx, ann.ID, bo.ID, true)
		if err != nil || u.DisabledAt == nil {
			t.Fatalf("disable = %+v, %v", u, err)
		}
		for _, h := range [][]byte{h1, h2} {
			if _, _, err := st.RotateRefreshToken(ctx, h, store.NewRefresh{Hash: randomHash(t), TTL: time.Hour}); err == nil {
				t.Fatal("a session survived the disable")
			}
		}
		// A session started after the disable (it cannot be, but if): refused.
		_, h3 := login(t, bo.ID)
		if _, _, err := st.RotateRefreshToken(ctx, h3, store.NewRefresh{Hash: randomHash(t), TTL: time.Hour}); !errors.Is(err, store.ErrRefreshInvalid) {
			t.Fatalf("disabled user's refresh = %v", err)
		}
		again, err := st.SetUserDisabled(ctx, ann.ID, bo.ID, true)
		if err != nil || !again.DisabledAt.Equal(*u.DisabledAt) {
			t.Fatalf("disabling twice = %+v, %v; want the first time kept", again, err)
		}
		if u, err := st.SetUserDisabled(ctx, ann.ID, bo.ID, false); err != nil || u.DisabledAt != nil {
			t.Fatalf("enable = %+v, %v", u, err)
		}
		if _, _, err := st.RotateRefreshToken(ctx, h1, store.NewRefresh{Hash: randomHash(t), TTL: time.Hour}); err == nil {
			t.Fatal("enable brought a revoked session back")
		}
		if _, err := st.SetUserDisabled(ctx, ann.ID, model.NewID(), true); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("unknown user = %v", err)
		}
		cy := user(t, "cy", model.RoleUser)
		if _, err := st.SetUserDisabled(ctx, bo.ID, cy.ID, true); !errors.Is(err, store.ErrNotAdmin) {
			t.Fatalf("a non-admin acting = %v", err)
		}
	})

	t.Run("the last active admin is never disabled or demoted", func(t *testing.T) {
		reset(t)
		ann := user(t, "ann", model.RoleAdmin)
		bo := user(t, "bo", model.RoleAdmin)
		if _, err := st.SetUserRole(ctx, ann.ID, bo.ID, model.RoleUser); err != nil {
			t.Fatal(err)
		}
		// bo is a user now. An admin acting on themselves is refused by the
		// service; here: ann is the last admin, whoever asks.
		if _, err := st.SetUserRole(ctx, ann.ID, ann.ID, model.RoleUser); !errors.Is(err, store.ErrLastAdmin) {
			t.Fatalf("demote the last admin = %v", err)
		}
		if _, err := st.SetUserDisabled(ctx, ann.ID, ann.ID, true); !errors.Is(err, store.ErrLastAdmin) {
			t.Fatalf("disable the last admin = %v", err)
		}
		// A disabled admin does not count: with bo an admin again but
		// disabled, ann is still the last active one.
		if _, err := st.SetUserRole(ctx, ann.ID, bo.ID, model.RoleAdmin); err != nil {
			t.Fatal(err)
		}
		if _, err := st.SetUserDisabled(ctx, ann.ID, bo.ID, true); err != nil {
			t.Fatal(err)
		}
		if _, err := st.SetUserRole(ctx, ann.ID, ann.ID, model.RoleUser); !errors.Is(err, store.ErrLastAdmin) {
			t.Fatalf("demote with only a disabled admin left = %v", err)
		}
		// Demoting a disabled admin is fine: it leaves the active count as is.
		if _, err := st.SetUserRole(ctx, ann.ID, bo.ID, model.RoleUser); err != nil {
			t.Fatalf("demote a disabled admin = %v", err)
		}
	})

	t.Run("two admins demoting or disabling each other at once: one fails", func(t *testing.T) {
		for _, tt := range []struct {
			name string
			act  func(actor, target string) error
		}{
			{"demote", func(a, b string) error { _, err := st.SetUserRole(ctx, a, b, model.RoleUser); return err }},
			{"disable", func(a, b string) error { _, err := st.SetUserDisabled(ctx, a, b, true); return err }},
		} {
			t.Run(tt.name, func(t *testing.T) {
				for range 20 {
					reset(t)
					ann := user(t, "ann", model.RoleAdmin)
					bo := user(t, "bo", model.RoleAdmin)
					var wg sync.WaitGroup
					errs := make([]error, 2)
					start := make(chan struct{})
					for i, pair := range [][2]string{{ann.ID, bo.ID}, {bo.ID, ann.ID}} {
						wg.Go(func() {
							<-start
							errs[i] = tt.act(pair[0], pair[1])
						})
					}
					close(start)
					wg.Wait()
					failed := 0
					for _, err := range errs {
						switch {
						case err == nil:
						case errors.Is(err, store.ErrLastAdmin):
							failed++
						default:
							t.Fatalf("unexpected error: %v", err)
						}
					}
					var admins int
					if err := db.Pool.QueryRow(ctx,
						`SELECT count(*) FROM users WHERE role = 'admin' AND disabled_at IS NULL`).Scan(&admins); err != nil {
						t.Fatal(err)
					}
					if failed != 1 || admins != 1 {
						t.Fatalf("errors %v, %d active admins left; want exactly one failure and one admin", errs, admins)
					}
				}
			})
		}
	})

	t.Run("retry: only a failed movie, once", func(t *testing.T) {
		reset(t)
		failed := movie(t, "Broken", "sources/broken.mkv", model.StatusFailed)
		if _, err := db.Pool.Exec(ctx,
			`INSERT INTO transcode_jobs (movie_id, status, error_msg) VALUES ($1, 'failed', 'ffprobe: invalid data')`, failed); err != nil {
			t.Fatal(err)
		}
		ready := movie(t, "Fine", "sources/fine.mkv", model.StatusReady)
		if err := st.RetryMovie(ctx, ready); !errors.Is(err, store.ErrNotFailed) {
			t.Fatalf("retry ready = %v", err)
		}
		if err := st.RetryMovie(ctx, model.NewID()); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("retry unknown = %v", err)
		}

		// A double click: both at once, exactly one queues a job.
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i := range errs {
			wg.Go(func() { errs[i] = st.RetryMovie(ctx, failed) })
		}
		wg.Wait()
		if (errs[0] == nil) == (errs[1] == nil) {
			t.Fatalf("double retry = %v; want one success", errs)
		}
		var status, job string
		var jobs int
		if err := db.Pool.QueryRow(ctx, `
			SELECT m.status, j.status, (SELECT count(*) FROM transcode_jobs WHERE movie_id = m.id)
			FROM movies m JOIN transcode_jobs j ON j.movie_id = m.id AND j.status = 'queued' WHERE m.id = $1`, failed).
			Scan(&status, &job, &jobs); err != nil {
			t.Fatal(err)
		}
		if status != "pending" || jobs != 2 {
			t.Fatalf("after retry: movie %s, %d jobs; want pending and the old job kept", status, jobs)
		}
		// The jobs list shows both, newest first, with the title.
		js, total, err := st.ListJobs(ctx, store.JobFilter{Limit: 10})
		if err != nil || total != 2 || js[0].Status != model.JobQueued || js[0].MovieTitle != "Broken" ||
			js[1].ErrorMsg == nil || *js[1].ErrorMsg != "ffprobe: invalid data" {
			t.Fatalf("jobs = %+v (%d), %v", js, total, err)
		}
		q := model.JobFailed
		if _, n, err := st.ListJobs(ctx, store.JobFilter{Status: &q, Limit: 10}); err != nil || n != 1 {
			t.Fatalf("failed jobs = %d, %v", n, err)
		}
	})

	t.Run("delete: refused with an active job, hidden at once, purged by row", func(t *testing.T) {
		reset(t)
		busy := movie(t, "Busy", "sources/busy.mkv", model.StatusProcessing)
		if _, err := db.Pool.Exec(ctx, `INSERT INTO transcode_jobs (movie_id, status) VALUES ($1, 'running')`, busy); err != nil {
			t.Fatal(err)
		}
		if err := st.DeleteMovie(ctx, busy); !errors.Is(err, store.ErrJobActive) {
			t.Fatalf("delete with a running job = %v", err)
		}
		ms, _, err := st.AdminMovies(ctx, store.MovieFilter{Limit: 10})
		if err != nil || len(ms) != 1 || ms[0].ActiveJob == nil || *ms[0].ActiveJob != model.JobRunning {
			t.Fatalf("admin movies = %+v, %v", ms, err)
		}

		ready := movie(t, "Ready", "sources/ready.mkv", model.StatusReady)
		if err := st.DeleteMovie(ctx, ready); err != nil {
			t.Fatal(err)
		}
		if err := st.DeleteMovie(ctx, ready); err != nil {
			t.Fatalf("delete twice = %v", err)
		}
		if err := st.RetryMovie(ctx, ready); !errors.Is(err, store.ErrNotFailed) {
			t.Fatalf("retry a deleting movie = %v", err)
		}
		if _, err := st.StartTranscode(ctx, ready); err == nil {
			t.Fatal("a deleting movie was transcoded")
		}
		deleting := model.StatusDeleting
		if ms, n, err := st.AdminMovies(ctx, store.MovieFilter{Status: &deleting, Limit: 10}); err != nil || n != 1 ||
			ms[0].ID != ready || ms[0].ActiveJob != nil {
			t.Fatalf("deleting movies = %+v, %v", ms, err)
		}
		gone, err := st.DeletingMovies(ctx)
		if err != nil || len(gone) != 1 || gone[0].ID != ready || gone[0].SourcePath != "sources/ready.mkv" {
			t.Fatalf("DeletingMovies = %+v, %v", gone, err)
		}
		// PurgeMovie removes only a deleting movie.
		if err := st.PurgeMovie(ctx, busy); err != nil {
			t.Fatal(err)
		}
		if err := st.PurgeMovie(ctx, ready); err != nil {
			t.Fatal(err)
		}
		if err := st.PurgeMovie(ctx, ready); err != nil {
			t.Fatalf("purge twice = %v", err)
		}
		var left []string
		rows, err := db.Pool.Query(ctx, `SELECT title FROM movies`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var s string
			_ = rows.Scan(&s)
			left = append(left, s)
		}
		if len(left) != 1 || left[0] != "Busy" {
			t.Fatalf("movies left = %v", left)
		}
		if err := st.DeleteMovie(ctx, ready); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("delete a purged movie = %v", err)
		}
	})

	t.Run("retranscode: only a ready movie, one job, the movie stays ready", func(t *testing.T) {
		reset(t)
		ready := movie(t, "Ready", "sources/r.mkv", model.StatusReady)
		failed := movie(t, "Broken", "sources/f.mkv", model.StatusFailed)
		if err := st.RetranscodeMovie(ctx, failed); !errors.Is(err, store.ErrMovieNotReady) {
			t.Fatalf("retranscode failed = %v", err)
		}
		if err := st.RetranscodeMovie(ctx, model.NewID()); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("retranscode unknown = %v", err)
		}
		if err := st.RetranscodeMovie(ctx, ready); err != nil {
			t.Fatal(err)
		}
		if err := st.RetranscodeMovie(ctx, ready); !errors.Is(err, store.ErrJobActive) {
			t.Fatalf("second retranscode = %v", err)
		}
		if err := st.DeleteMovie(ctx, ready); !errors.Is(err, store.ErrJobActive) {
			t.Fatalf("delete during a retranscode = %v", err)
		}
		ms, _, err := st.AdminMovies(ctx, store.MovieFilter{Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range ms {
			if m.ID == ready && (m.Status != model.StatusReady || m.ActiveJob == nil || *m.ActiveJob != model.JobQueued ||
				*m.ActiveJobPurpose != model.PurposeRetranscode) {
				t.Fatalf("movie = %+v", m)
			}
		}
		js, _, err := st.ListJobs(ctx, store.JobFilter{Limit: 10})
		if err != nil || len(js) != 1 || js[0].Purpose != model.PurposeRetranscode {
			t.Fatalf("jobs = %+v, %v", js, err)
		}

		// The dispatcher starts it; it fails: the job is failed, the movie
		// stays ready, and the watcher is asked to tidy up now.
		job, ok, err := st.ClaimJob(ctx, 10, nil)
		if err != nil || !ok {
			t.Fatal(ok, err)
		}
		tj, err := st.StartTranscode(ctx, ready)
		if err != nil || tj.Purpose != model.PurposeRetranscode {
			t.Fatalf("StartTranscode = %+v, %v", tj, err)
		}
		if ok, err := st.FailJob(ctx, job.ID, "boom"); err != nil || !ok {
			t.Fatal(ok, err)
		}
		m, err := st.GetMovie(ctx, ready)
		if err != nil || m.Status != model.StatusReady || m.LibraryCleanupAfter == nil {
			t.Fatalf("after a failed retranscode: %+v, %v", m, err)
		}

		// CleanupLibrary re-checks under the lock: with a job active again
		// it does nothing.
		if err := st.RetranscodeMovie(ctx, ready); err != nil {
			t.Fatal(err)
		}
		called := false
		ran, err := st.CleanupLibrary(ctx, ready, func(*string) error { called = true; return nil })
		if err != nil || ran || called {
			t.Fatalf("cleanup with a job queued: ran %v, called %v, %v", ran, called, err)
		}
		if due, err := st.CleanupDue(ctx); err != nil || len(due) != 0 {
			t.Fatalf("due with a job queued = %v, %v", due, err)
		}
		if _, err := db.Pool.Exec(ctx, `UPDATE transcode_jobs SET status = 'failed' WHERE movie_id = $1 AND status = 'queued'`, ready); err != nil {
			t.Fatal(err)
		}
		if due, err := st.CleanupDue(ctx); err != nil || len(due) != 1 || due[0] != ready {
			t.Fatalf("due = %v, %v", due, err)
		}
		// An error from clean keeps the time; success clears it.
		if _, err := st.CleanupLibrary(ctx, ready, func(*string) error { return errors.New("disk") }); err == nil {
			t.Fatal("clean's error was lost")
		}
		if m, _ := st.GetMovie(ctx, ready); m.LibraryCleanupAfter == nil {
			t.Fatal("a failed cleanup cleared its time")
		}
		if ran, err := st.CleanupLibrary(ctx, ready, func(*string) error { return nil }); err != nil || !ran {
			t.Fatal(ran, err)
		}
		if m, _ := st.GetMovie(ctx, ready); m.LibraryCleanupAfter != nil {
			t.Fatal("cleanup time not cleared")
		}
	})

	t.Run("a disabled account: no verify, no reset mail, no Google, no link", func(t *testing.T) {
		reset(t)
		ann := user(t, "ann", model.RoleAdmin)
		verifyHash := randomHash(t)
		reg, err := st.Register(ctx, store.NewAccount{Email: "dee@example.com", Username: "dee", PasswordHash: pwHash,
			VerifyHash: verifyHash, VerifyTTL: time.Hour})
		if err != nil {
			t.Fatal(err)
		}
		dee := reg.User
		if _, err := st.SetUserDisabled(ctx, ann.ID, dee.ID, true); err != nil {
			t.Fatal(err)
		}
		if _, _, err := st.VerifyEmail(ctx, verifyHash, store.NewRefresh{Hash: randomHash(t), TTL: time.Hour}); !errors.Is(err, store.ErrAccountDisabled) {
			t.Fatalf("verify = %v", err)
		}
		if _, o, err := st.ResendVerification(ctx, "dee@example.com", randomHash(t), time.Hour); err != nil || o != store.ResendDisabled {
			t.Fatalf("resend = %v, %v", o, err)
		}
		if _, err := st.SetUserDisabled(ctx, ann.ID, dee.ID, false); err != nil {
			t.Fatal(err)
		}
		// Enabled again, the link that was refused works: it was not used up.
		if _, _, err := st.VerifyEmail(ctx, verifyHash, store.NewRefresh{Hash: randomHash(t), TTL: time.Hour}); err != nil {
			t.Fatalf("verify after enable = %v", err)
		}
		if _, err := st.SetUserDisabled(ctx, ann.ID, dee.ID, true); err != nil {
			t.Fatal(err)
		}
		if _, o, err := st.IssueResetToken(ctx, "dee@example.com", randomHash(t), time.Hour); err != nil || o != store.ResetDisabled {
			t.Fatalf("reset = %v, %v", o, err)
		}
		if err := st.IssueResetTokenForUser(ctx, dee.ID, randomHash(t), time.Hour); !errors.Is(err, store.ErrAccountDisabled) {
			t.Fatalf("set-link = %v", err)
		}
		if err := st.LinkGoogle(ctx, dee.ID, "sub-dee", "dee@example.com"); !errors.Is(err, store.ErrAccountDisabled) {
			t.Fatalf("link = %v", err)
		}
		in := store.GoogleSignIn{Subject: "sub-new", Email: "dee@example.com", AllowCreate: true,
			NextUsername: func() string { return "dee-g" }, Refresh: store.NewRefresh{Hash: randomHash(t), TTL: time.Hour}}
		if _, err := st.GoogleSignIn(ctx, in); !errors.Is(err, store.ErrAccountDisabled) {
			t.Fatalf("Google by email = %v", err)
		}
		// Rule a: a linked Google account of a disabled user.
		if _, err := st.SetUserDisabled(ctx, ann.ID, dee.ID, false); err != nil {
			t.Fatal(err)
		}
		if err := st.LinkGoogle(ctx, dee.ID, "sub-dee", "dee@example.com"); err != nil {
			t.Fatal(err)
		}
		if _, err := st.SetUserDisabled(ctx, ann.ID, dee.ID, true); err != nil {
			t.Fatal(err)
		}
		in.Subject = "sub-dee"
		if _, err := st.GoogleSignIn(ctx, in); !errors.Is(err, store.ErrAccountDisabled) {
			t.Fatalf("Google by subject = %v", err)
		}
		var families int
		if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM refresh_tokens WHERE user_id = $1 AND revoked_at IS NULL`, dee.ID).
			Scan(&families); err != nil || families != 0 {
			t.Fatalf("%d live tokens for a disabled user (%v)", families, err)
		}
	})
}
