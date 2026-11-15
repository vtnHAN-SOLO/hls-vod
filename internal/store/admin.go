package store

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
)

// The admin area. The api never touches files: deleting a movie only
// marks it; the watcher removes its files and then the row.

var (
	// ErrLastAdmin means the change would leave no active (not disabled)
	// admin.
	ErrLastAdmin = errors.New("the last active admin cannot be disabled or demoted")
	// ErrNotAdmin means the acting user is no longer an active admin: an
	// admin demoted or disabled them since their request was checked.
	ErrNotAdmin = errors.New("the acting user is not an active admin")
	// ErrNotFailed means a retry of a movie that is not failed.
	ErrNotFailed = errors.New("only a failed movie can be retried")
	// ErrJobActive means the movie has a queued or running transcode job.
	ErrJobActive = errors.New("the movie has a queued or running transcode job")
	// ErrMovieNotReady means a retranscode of a movie that is not ready.
	ErrMovieNotReady = errors.New("only a ready movie can be retranscoded")
)

// AdminCheck is the admin endpoints' check, in one query: admin is whether
// the user is an admin and not disabled (the role in the database, not the
// one in the access token, which outlives a demotion by up to 15 min), live
// whether the session familyID can still refresh, as SessionLive.
func (s *Store) AdminCheck(ctx context.Context, userID, familyID string) (admin, live bool, err error) {
	if err := s.pool.QueryRow(ctx, `
		SELECT coalesce((SELECT role = 'admin' AND disabled_at IS NULL FROM users WHERE id = $1), false),
		       EXISTS (SELECT 1 FROM refresh_tokens WHERE user_id = $1 AND family_id = $2 AND `+canRefresh+`)`,
		userID, familyID).Scan(&admin, &live); err != nil {
		return false, false, fmt.Errorf("checking admin: %w", err)
	}
	return admin, live, nil
}

// UserFilter selects a page of users. Query nil: everyone; else a
// case-insensitive substring of the username or the email, matched
// literally.
type UserFilter struct {
	Query  *string
	Limit  int64
	Offset int64
}

const userWhere = `WHERE ($1::text IS NULL OR username ILIKE $1 ESCAPE '\' OR email ILIKE $1 ESCAPE '\')`

// summaryColumns are userColumns plus whether a Google account is linked.
const summaryColumns = userColumns + `,
	EXISTS (SELECT 1 FROM user_identities i WHERE i.user_id = users.id AND i.provider = 'google')`

func scanSummary(row pgx.Row) (model.UserSummary, error) {
	var u model.UserSummary
	var role string
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &role, &u.Email, &u.EmailVerifiedAt, &u.DisabledAt,
		&u.CreatedAt, &u.UpdatedAt, &u.GoogleLinked)
	u.Role = model.Role(role)
	return u, err
}

// ListUserSummaries returns one page of users by username, and how many
// match the filter.
func (s *Store) ListUserSummaries(ctx context.Context, f UserFilter) ([]model.UserSummary, int64, error) {
	var q *string
	if f.Query != nil {
		v := containsPattern(*f.Query)
		q = &v
	}
	rows, err := s.pool.Query(ctx, `SELECT `+summaryColumns+` FROM users `+userWhere+`
		ORDER BY username, id LIMIT $2 OFFSET $3`, q, f.Limit, f.Offset)
	if err != nil {
		return nil, 0, fmt.Errorf("listing users: %w", err)
	}
	users, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.UserSummary, error) { return scanSummary(r) })
	if err != nil {
		return nil, 0, fmt.Errorf("listing users: %w", err)
	}
	var total int64
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM users `+userWhere, q).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("counting users: %w", err)
	}
	return users, total, nil
}

// adminChange runs write on the target user inside a transaction that
// first locks every active admin's row, in id order, then the target's.
// Two admin changes therefore never run side by side: the second waits for
// the first to commit and then sees its result (an admin it demoted or
// disabled is no longer in the locked set), so two admins demoting each
// other at the same moment cannot leave zero admins: the second finds the
// other one is the last and gets ErrLastAdmin.
//
// In order: ErrNotFound for an unknown target; ErrLastAdmin when removes
// says the change takes an active admin away and the target is the only
// one left; ErrNotAdmin when actorID is not among the active admins any
// more. It returns the target afterwards.
func (s *Store) adminChange(ctx context.Context, actorID, targetID string, removes bool,
	write func(tx pgx.Tx, target model.User) error,
) (model.UserSummary, error) {
	var out model.UserSummary
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT id::text FROM users WHERE role = 'admin' AND disabled_at IS NULL ORDER BY id FOR UPDATE`)
		if err != nil {
			return fmt.Errorf("locking admins: %w", err)
		}
		admins, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return fmt.Errorf("locking admins: %w", err)
		}
		target, err := scanUser(tx.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE id = $1 FOR UPDATE`, targetID))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("loading user: %w", err)
		}
		if removes && activeAdmin(target) && len(admins) <= 1 {
			return ErrLastAdmin
		}
		if !slices.Contains(admins, actorID) {
			return ErrNotAdmin
		}
		if err := write(tx, target); err != nil {
			return err
		}
		if out, err = scanSummary(tx.QueryRow(ctx, `SELECT `+summaryColumns+` FROM users WHERE id = $1`, targetID)); err != nil {
			return fmt.Errorf("reloading user: %w", err)
		}
		return nil
	})
	if err != nil {
		return model.UserSummary{}, err
	}
	return out, nil
}

// activeAdmin reports whether u counts towards the active admins.
func activeAdmin(u model.User) bool { return u.Role == model.RoleAdmin && !u.Disabled() }

// SetUserDisabled disables or enables the target user. Disabling revokes
// every session of the user in the same transaction, and is ErrLastAdmin
// for the last active admin. Doing it twice is not an error. See
// adminChange for the locking and the other errors.
func (s *Store) SetUserDisabled(ctx context.Context, actorID, targetID string, disabled bool) (model.UserSummary, error) {
	return s.adminChange(ctx, actorID, targetID, disabled, func(tx pgx.Tx, target model.User) error {
		if _, err := tx.Exec(ctx, `
			UPDATE users SET disabled_at = CASE WHEN $2 THEN coalesce(disabled_at, now()) END WHERE id = $1`,
			target.ID, disabled); err != nil {
			return fmt.Errorf("updating disabled_at: %w", err)
		}
		if disabled {
			return revokeAllSessions(ctx, tx, target.ID)
		}
		return nil
	})
}

// SetUserRole changes the target user's role; demoting the last active
// admin is ErrLastAdmin. See adminChange for the locking and the other
// errors.
func (s *Store) SetUserRole(ctx context.Context, actorID, targetID string, role model.Role) (model.UserSummary, error) {
	return s.adminChange(ctx, actorID, targetID, role != model.RoleAdmin, func(tx pgx.Tx, target model.User) error {
		if _, err := tx.Exec(ctx, `UPDATE users SET role = $2 WHERE id = $1`, target.ID, string(role)); err != nil {
			return fmt.Errorf("updating role: %w", err)
		}
		return nil
	})
}

// JobFilter selects a page of transcode jobs. Status nil: every status.
type JobFilter struct {
	Status *model.JobStatus
	Limit  int64
	Offset int64
}

// ListJobs returns one page of transcode jobs, newest first, with their
// movie's title, and how many match the filter.
func (s *Store) ListJobs(ctx context.Context, f JobFilter) ([]model.Job, int64, error) {
	var status *string
	if f.Status != nil {
		v := string(*f.Status)
		status = &v
	}
	rows, err := s.pool.Query(ctx, `
		SELECT j.id::text, j.movie_id::text, m.title, j.status, j.purpose, j.started_at, j.finished_at, j.error_msg,
		       j.k8s_job_name, j.local_pid, j.created_at
		FROM transcode_jobs j JOIN movies m ON m.id = j.movie_id
		WHERE ($1::text IS NULL OR j.status = $1)
		ORDER BY j.created_at DESC, j.id DESC
		LIMIT $2 OFFSET $3`, status, f.Limit, f.Offset)
	if err != nil {
		return nil, 0, fmt.Errorf("listing jobs: %w", err)
	}
	jobs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.Job, error) {
		var j model.Job
		var st, purpose string
		err := r.Scan(&j.ID, &j.MovieID, &j.MovieTitle, &st, &purpose, &j.StartedAt, &j.FinishedAt, &j.ErrorMsg,
			&j.K8sJobName, &j.LocalPID, &j.CreatedAt)
		j.Status, j.Purpose = model.JobStatus(st), model.JobPurpose(purpose)
		return j, err
	})
	if err != nil {
		return nil, 0, fmt.Errorf("listing jobs: %w", err)
	}
	var total int64
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM transcode_jobs WHERE ($1::text IS NULL OR status = $1)`, status).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("counting jobs: %w", err)
	}
	return jobs, total, nil
}

// AdminMovies is ListMovies for the admin area: every status, and each
// movie's active (queued or running) job status, nil when it has none.
func (s *Store) AdminMovies(ctx context.Context, f MovieFilter) ([]model.AdminMovie, int64, error) {
	order, ok := movieOrder[SortAddedDesc]
	if f.Sort != "" {
		order, ok = movieOrder[f.Sort]
	}
	if !ok {
		return nil, 0, fmt.Errorf("listing movies: unknown sort %q", f.Sort)
	}
	var status, title *string
	if f.Status != nil {
		v := string(*f.Status)
		status = &v
	}
	if f.Title != nil {
		v := containsPattern(*f.Title)
		title = &v
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+movieColumns+`,
		       (SELECT j.status FROM transcode_jobs j WHERE j.movie_id = movies.id AND j.status IN ('queued', 'running')),
		       (SELECT j.purpose FROM transcode_jobs j WHERE j.movie_id = movies.id AND j.status IN ('queued', 'running'))
		FROM movies
		`+movieWhere+`
		ORDER BY `+order+`
		LIMIT $5 OFFSET $6`,
		status, title, f.YearFrom, f.YearTo, f.Limit, f.Offset)
	if err != nil {
		return nil, 0, fmt.Errorf("listing movies: %w", err)
	}
	movies, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (model.AdminMovie, error) {
		var am model.AdminMovie
		var st string
		var job, purpose *string
		m := &am.Movie
		err := row.Scan(&m.ID, &m.Title, &m.Year, &m.TMDBID, &m.Overview, &m.RuntimeSec,
			&m.SourcePath, &m.LibraryPath, &m.PosterPath, &st, &m.LibraryCleanupAfter, &m.CreatedAt, &m.UpdatedAt,
			&job, &purpose)
		m.Status = model.MovieStatus(st)
		if job != nil && purpose != nil {
			js, jp := model.JobStatus(*job), model.JobPurpose(*purpose)
			am.ActiveJob, am.ActiveJobPurpose = &js, &jp
		}
		return am, err
	})
	if err != nil {
		return nil, 0, fmt.Errorf("listing movies: %w", err)
	}
	var total int64
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM movies `+movieWhere, status, title, f.YearFrom, f.YearTo,
	).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("counting movies: %w", err)
	}
	return movies, total, nil
}

// lockMovie locks the movie's row and returns its status, or ErrNotFound.
// Retry and delete both take it first, so they never interleave.
func lockMovie(ctx context.Context, tx pgx.Tx, id string) (model.MovieStatus, error) {
	var status string
	err := tx.QueryRow(ctx, `SELECT status FROM movies WHERE id = $1 FOR UPDATE`, id).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("loading movie: %w", err)
	}
	return model.MovieStatus(status), nil
}

// RetryMovie queues a new transcode job for a failed movie and sets it
// pending, in one transaction; its source is still in sources/. ErrNotFailed
// for any other status, ErrJobActive if the database refuses a second
// active job (the one-active-job index), ErrNotFound for an unknown movie.
func (s *Store) RetryMovie(ctx context.Context, id string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		status, err := lockMovie(ctx, tx, id)
		if err != nil {
			return err
		}
		if status != model.StatusFailed {
			return ErrNotFailed
		}
		if _, err := tx.Exec(ctx, `UPDATE movies SET status = 'pending' WHERE id = $1`, id); err != nil {
			return fmt.Errorf("marking movie pending: %w", err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO transcode_jobs (movie_id) VALUES ($1)`, id); err != nil {
			if IsActiveJobConflict(err) {
				return ErrJobActive
			}
			return fmt.Errorf("queueing transcode job: %w", err)
		}
		return nil
	})
}

// DeleteMovie marks the movie deleting, in one transaction: from then on
// only admins see it, and the watcher's next run removes its files and the
// row. ErrJobActive while a job is queued or running (the transcoder would
// write into what is being removed); ErrNotFound for an unknown movie. A
// movie already deleting stays so.
func (s *Store) DeleteMovie(ctx context.Context, id string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := lockMovie(ctx, tx, id); err != nil {
			return err
		}
		var active bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM transcode_jobs WHERE movie_id = $1 AND status IN ('queued', 'running'))`,
			id).Scan(&active); err != nil {
			return fmt.Errorf("checking jobs: %w", err)
		}
		if active {
			return ErrJobActive
		}
		if _, err := tx.Exec(ctx, `UPDATE movies SET status = 'deleting' WHERE id = $1`, id); err != nil {
			return fmt.Errorf("marking movie deleting: %w", err)
		}
		return nil
	})
}

// RetranscodeMovie queues a job with purpose retranscode for a ready movie.
// The movie stays ready, and plays its current version, until the new one
// is published. ErrMovieNotReady for any other status, ErrJobActive while a
// job is queued or running (the one-active-job index), ErrNotFound for an
// unknown movie.
func (s *Store) RetranscodeMovie(ctx context.Context, id string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		status, err := lockMovie(ctx, tx, id)
		if err != nil {
			return err
		}
		if status != model.StatusReady {
			return ErrMovieNotReady
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO transcode_jobs (movie_id, purpose) VALUES ($1, 'retranscode')`, id); err != nil {
			if IsActiveJobConflict(err) {
				return ErrJobActive
			}
			return fmt.Errorf("queueing retranscode job: %w", err)
		}
		return nil
	})
}

// CleanupDue lists the movies whose library cleanup time has passed, no
// job is queued or running, and that are not being deleted (the purge
// removes those whole).
func (s *Store) CleanupDue(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id::text FROM movies m
		WHERE library_cleanup_after < now() AND status <> 'deleting'
		  AND NOT EXISTS (SELECT 1 FROM transcode_jobs j WHERE j.movie_id = m.id AND j.status IN ('queued', 'running'))
		ORDER BY library_cleanup_after, id`)
	if err != nil {
		return nil, fmt.Errorf("listing due cleanups: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("listing due cleanups: %w", err)
	}
	return ids, nil
}

// CleanupLibrary runs clean for movie id, with the movie's row locked, and
// then clears library_cleanup_after. clean gets the current library_path
// (nil when the movie has none) and must remove everything in
// library/{id}/ except that version.
//
// The row lock is what makes this safe: a retranscode can be neither
// queued (RetranscodeMovie locks the row) nor published (FinishTranscode
// updates it) while clean runs, so the version clean keeps is still the
// current one, and no transcoder is writing into library/{id}/. Inside the
// lock it checks again that the cleanup is due and no job is active;
// ran=false means it no longer was, and nothing was done. An error from
// clean leaves library_cleanup_after set: the next run tries again.
func (s *Store) CleanupLibrary(ctx context.Context, id string, clean func(libraryPath *string) error) (ran bool, err error) {
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var libraryPath *string
		err := tx.QueryRow(ctx, `
			SELECT library_path FROM movies m
			WHERE id = $1 AND library_cleanup_after < now() AND status <> 'deleting'
			  AND NOT EXISTS (SELECT 1 FROM transcode_jobs j WHERE j.movie_id = m.id AND j.status IN ('queued', 'running'))
			FOR UPDATE`, id).Scan(&libraryPath)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("locking movie: %w", err)
		}
		if err := clean(libraryPath); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE movies SET library_cleanup_after = NULL WHERE id = $1`, id); err != nil {
			return fmt.Errorf("clearing library_cleanup_after: %w", err)
		}
		ran = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return ran, nil
}

// DeletingMovie is a movie the watcher must remove: its id (library/{id}/)
// and its source file, relative to MEDIA_ROOT and untrusted until mediapath
// checks it.
type DeletingMovie struct {
	ID         string
	SourcePath string
}

// DeletingMovies lists every movie in status deleting, oldest first.
func (s *Store) DeletingMovies(ctx context.Context) ([]DeletingMovie, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id::text, source_path FROM movies WHERE status = 'deleting' ORDER BY updated_at, id`)
	if err != nil {
		return nil, fmt.Errorf("listing deleting movies: %w", err)
	}
	ms, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (DeletingMovie, error) {
		var m DeletingMovie
		err := r.Scan(&m.ID, &m.SourcePath)
		return m, err
	})
	if err != nil {
		return nil, fmt.Errorf("listing deleting movies: %w", err)
	}
	return ms, nil
}

// PurgeMovie deletes the row of a deleting movie, once its files are gone;
// renditions, jobs and watch progress go with it (ON DELETE CASCADE). A
// movie that is gone already is not an error.
func (s *Store) PurgeMovie(ctx context.Context, id string) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM movies WHERE id = $1 AND status = 'deleting'`, id); err != nil {
		return fmt.Errorf("deleting movie row: %w", err)
	}
	return nil
}
