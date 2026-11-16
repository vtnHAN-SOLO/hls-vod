package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
)

var (
	// ErrNoRunningJob means the movie has no transcode job in status running:
	// the transcoder only runs for a job the dispatcher has started.
	ErrNoRunningJob = errors.New("movie has no running transcode job")
	// ErrJobNotRunning means the job left status running while the transcoder
	// worked, e.g. the dispatcher failed it for exceeding its deadline.
	ErrJobNotRunning = errors.New("transcode job is no longer running")
	// ErrNotTranscodable means the movie's status does not fit the job's
	// purpose: ingest of a ready or deleting movie, retranscode of one that
	// is not ready.
	ErrNotTranscodable = errors.New("movie cannot be transcoded in its current status")
)

// dispatchLock serialises ClaimJob across dispatcher replicas, so the
// running-job count and the claim are one atomic step and MAX_CONCURRENT is
// exact. Distinct from claimLock (the watcher's).
const dispatchLock int64 = 0x6d65_6469_6100_0002

// RunningJob is a transcode job in status running, as the dispatcher sees it.
type RunningJob struct {
	ID         string
	MovieID    string
	K8sJobName *string // kubernetes backend
	LocalPID   *int    // local backend, once the process has started
	StartedAt  time.Time
}

// ClaimJob starts the oldest queued job if fewer than maxRunning jobs are
// running: it marks it running with started_at = now and, if jobName is not
// nil, k8s_job_name = jobName(job id). It returns ok=false when the limit is
// reached or nothing is queued.
//
// The row is taken with FOR UPDATE SKIP LOCKED, so a row another transaction
// holds is skipped rather than waited for, and two replicas never take the
// same job. The transaction also holds dispatchLock, so the count of running
// jobs cannot change between the check and the claim.
func (s *Store) ClaimJob(ctx context.Context, maxRunning int, jobName func(jobID string) string) (job RunningJob, ok bool, err error) {
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, dispatchLock); err != nil {
			return fmt.Errorf("taking the dispatch lock: %w", err)
		}
		var running int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM transcode_jobs WHERE status = 'running'`).Scan(&running); err != nil {
			return fmt.Errorf("counting running jobs: %w", err)
		}
		if running >= maxRunning {
			return nil
		}
		err := tx.QueryRow(ctx, `
			SELECT id::text, movie_id::text FROM transcode_jobs
			WHERE status = 'queued'
			ORDER BY created_at, id
			LIMIT 1
			FOR UPDATE SKIP LOCKED`).Scan(&job.ID, &job.MovieID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("selecting a queued job: %w", err)
		}
		if jobName != nil {
			name := jobName(job.ID)
			job.K8sJobName = &name
		}
		if err := tx.QueryRow(ctx, `
			UPDATE transcode_jobs SET status = 'running', started_at = now(), k8s_job_name = $2
			WHERE id = $1 RETURNING started_at`, job.ID, job.K8sJobName).Scan(&job.StartedAt); err != nil {
			return fmt.Errorf("marking job running: %w", err)
		}
		ok = true
		return nil
	})
	if err != nil {
		return RunningJob{}, false, err
	}
	return job, ok, nil
}

// SetJobPID records the local transcoder's pid on a running job.
func (s *Store) SetJobPID(ctx context.Context, jobID string, pid int) error {
	if _, err := s.pool.Exec(ctx,
		`UPDATE transcode_jobs SET local_pid = $2 WHERE id = $1 AND status = 'running'`, jobID, pid); err != nil {
		return fmt.Errorf("recording pid: %w", err)
	}
	return nil
}

// RunningJobs lists every job in status running, oldest first.
func (s *Store) RunningJobs(ctx context.Context) ([]RunningJob, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id::text, movie_id::text, k8s_job_name, local_pid, started_at
		FROM transcode_jobs WHERE status = 'running' ORDER BY started_at, id`)
	if err != nil {
		return nil, fmt.Errorf("listing running jobs: %w", err)
	}
	jobs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (RunningJob, error) {
		var j RunningJob
		err := r.Scan(&j.ID, &j.MovieID, &j.K8sJobName, &j.LocalPID, &j.StartedAt)
		return j, err
	})
	if err != nil {
		return nil, fmt.Errorf("listing running jobs: %w", err)
	}
	return jobs, nil
}

// FailJob marks a running job failed with msg and finished_at = now, and its
// movie failed unless it is already ready, in one transaction. A ready movie
// (a failed retranscode) stays ready on its current version. It returns
// false, writing nothing, if the job was no longer running: whoever reported
// first (transcoder or dispatcher) wins.
//
// It also asks the watcher to tidy library/{id}/ (library_cleanup_after,
// now unless a later time is already set): a killed run can leave a
// .partial-* folder that nothing else would ever remove. The current
// version is never touched.
func (s *Store) FailJob(ctx context.Context, jobID, msg string) (bool, error) {
	failed := false
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var movieID string
		err := tx.QueryRow(ctx, `
			UPDATE transcode_jobs SET status = 'failed', error_msg = $2, finished_at = now()
			WHERE id = $1 AND status = 'running' RETURNING movie_id::text`, jobID, msg).Scan(&movieID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("marking job failed: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE movies SET
			    status = CASE WHEN status IN ('pending', 'processing') THEN 'failed' ELSE status END,
			    library_cleanup_after = coalesce(library_cleanup_after, now())
			WHERE id = $1`, movieID); err != nil {
			return fmt.Errorf("marking movie failed: %w", err)
		}
		failed = true
		return nil
	})
	return failed, err
}

// KnownJobNames returns the subset of names recorded as a job's
// k8s_job_name, in any status.
func (s *Store) KnownJobNames(ctx context.Context, names []string) (map[string]bool, error) {
	known := make(map[string]bool, len(names))
	if len(names) == 0 {
		return known, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT k8s_job_name FROM transcode_jobs WHERE k8s_job_name = ANY($1)`, names)
	if err != nil {
		return nil, fmt.Errorf("looking up job names: %w", err)
	}
	found, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("looking up job names: %w", err)
	}
	for _, n := range found {
		known[n] = true
	}
	return known, nil
}

// TranscodeJob is the transcoder's view of its job.
type TranscodeJob struct {
	JobID      string
	MovieID    string
	Purpose    model.JobPurpose
	SourcePath string // relative to MEDIA_ROOT, untrusted until mediapath checks it
	// CurrentBytes is the size of the version the movie plays now: the sum
	// of its renditions' size_bytes (0 when unknown). Retranscode only.
	CurrentBytes int64
}

// StartTranscode finds the movie's running job and, for purpose ingest,
// marks the movie processing. ErrNotFound for an unknown movie,
// ErrNoRunningJob when the dispatcher has not started a job for it,
// ErrNotTranscodable when the status does not fit the purpose: ingest
// needs a movie that is not ready or deleting, retranscode a ready one
// (whose status it leaves alone: it stays playable).
func (s *Store) StartTranscode(ctx context.Context, movieID string) (TranscodeJob, error) {
	t := TranscodeJob{MovieID: movieID}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var status string
		err := tx.QueryRow(ctx, `SELECT source_path, status FROM movies WHERE id = $1 FOR UPDATE`, movieID).
			Scan(&t.SourcePath, &status)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("loading movie: %w", err)
		}
		var purpose string
		err = tx.QueryRow(ctx,
			`SELECT id::text, purpose FROM transcode_jobs WHERE movie_id = $1 AND status = 'running'`, movieID).
			Scan(&t.JobID, &purpose)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNoRunningJob
		}
		if err != nil {
			return fmt.Errorf("loading running job: %w", err)
		}
		t.Purpose = model.JobPurpose(purpose)
		if t.Purpose == model.PurposeRetranscode {
			if status != "ready" {
				return fmt.Errorf("%w: retranscode of a %s movie", ErrNotTranscodable, status)
			}
			if err := tx.QueryRow(ctx,
				`SELECT coalesce(sum(size_bytes), 0) FROM renditions WHERE movie_id = $1`, movieID).Scan(&t.CurrentBytes); err != nil {
				return fmt.Errorf("sizing the current version: %w", err)
			}
			return nil
		}
		if status == "ready" || status == "deleting" {
			return fmt.Errorf("%w: %s", ErrNotTranscodable, status)
		}
		if _, err := tx.Exec(ctx, `UPDATE movies SET status = 'processing' WHERE id = $1`, movieID); err != nil {
			return fmt.Errorf("marking movie processing: %w", err)
		}
		return nil
	})
	if err != nil {
		return TranscodeJob{}, err
	}
	return t, nil
}

// FinishTranscode records a successful transcode in one transaction: the
// job succeeded with finished_at, the renditions, and the movie's
// library_path, backdrop_path and runtime. ErrJobNotRunning, with nothing
// written, if the job is no longer running.
//
// Ingest: the movie processing → ready. Retranscode: the movie must still
// be ready; its renditions are replaced, and library_cleanup_after is set
// to now + p.OldVersionGrace, when every URL signed for the version it
// played until now has expired and the watcher may remove that version.
// The switch is this one commit: before it every request gets the old
// version, after it the new one.
func (s *Store) FinishTranscode(ctx context.Context, t TranscodeJob, p Published) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE transcode_jobs SET status = 'succeeded', finished_at = now()
			WHERE id = $1 AND status = 'running'`, t.JobID)
		if err != nil {
			return fmt.Errorf("marking job succeeded: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrJobNotRunning
		}
		if t.Purpose == model.PurposeRetranscode {
			tag, err = tx.Exec(ctx, `
				UPDATE movies SET library_path = $2, backdrop_path = $3, runtime_sec = $4,
				    library_cleanup_after = now() + make_interval(secs => $5)
				WHERE id = $1 AND status = 'ready'`,
				t.MovieID, p.LibraryPath, p.BackdropPath, p.RuntimeSec, p.OldVersionGrace.Seconds())
			if err != nil {
				return fmt.Errorf("switching the movie to its new version: %w", err)
			}
			if tag.RowsAffected() == 0 {
				return fmt.Errorf("movie %s is no longer ready", t.MovieID)
			}
			if _, err := tx.Exec(ctx, `DELETE FROM renditions WHERE movie_id = $1`, t.MovieID); err != nil {
				return fmt.Errorf("removing the old renditions: %w", err)
			}
			if err := replaceSubtitles(ctx, tx, t.MovieID, p.Subtitles); err != nil {
				return err
			}
			return insertRenditions(ctx, tx, t.MovieID, p.Renditions)
		}
		if err := insertRenditions(ctx, tx, t.MovieID, p.Renditions); err != nil {
			return err
		}
		// A retried ingest may follow a version the backfill described.
		if err := replaceSubtitles(ctx, tx, t.MovieID, p.Subtitles); err != nil {
			return err
		}
		tag, err = tx.Exec(ctx, `
			UPDATE movies SET status = 'ready', library_path = $2, backdrop_path = $3, runtime_sec = $4
			WHERE id = $1 AND status = 'processing'`, t.MovieID, p.LibraryPath, p.BackdropPath, p.RuntimeSec)
		if err != nil {
			return fmt.Errorf("marking movie ready: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("movie %s is no longer processing", t.MovieID)
		}
		return nil
	})
}

func insertRenditions(ctx context.Context, tx pgx.Tx, movieID string, rs []NewRendition) error {
	for _, r := range rs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO renditions (movie_id, height, bitrate_kbps, playlist_path, size_bytes)
			VALUES ($1, $2, $3, $4, $5)`,
			movieID, r.Height, r.BitrateKbps, r.PlaylistPath, r.SizeBytes,
		); err != nil {
			return fmt.Errorf("inserting %dp rendition: %w", r.Height, err)
		}
	}
	return nil
}

// IsActiveJobConflict reports whether err is the database refusing a second
// queued or running job for one movie (the transcode_jobs_one_active_per_movie
// partial unique index).
func IsActiveJobConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" &&
		pgErr.ConstraintName == "transcode_jobs_one_active_per_movie"
}
