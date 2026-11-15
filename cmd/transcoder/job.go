package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/vtnHAN-SOLO/hls-vod/internal/ingest"
	"github.com/vtnHAN-SOLO/hls-vod/internal/medialayout"
	"github.com/vtnHAN-SOLO/hls-vod/internal/mediapath"
	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
)

// errBadMovieID is a usage error: exit code 2.
var errBadMovieID = errors.New("--movie-id must be a movie UUID")

// reportTimeout bounds the final database write. It runs on a context that
// SIGTERM does not cancel: a finished transcode must still be recorded, and
// an interrupted one must still be marked failed.
const reportTimeout = 30 * time.Second

// maxErrorMsg bounds transcode_jobs.error_msg; ffmpeg errors carry a stderr tail.
const maxErrorMsg = 2000

// versionMargin is added to the renditions' sizes for what they do not
// count (poster, thumbnail sprites, subtitles, playlists) before a
// retranscode's free-space check.
const versionMargin = 64 << 20

// jobStore is the slice of the store job mode needs.
type jobStore interface {
	StartTranscode(ctx context.Context, movieID string) (store.TranscodeJob, error)
	FinishTranscode(ctx context.Context, t store.TranscodeJob, p store.Published) error
	FailJob(ctx context.Context, jobID, msg string) (bool, error)
	JobPublished(ctx context.Context, jobID, libraryPath string) (bool, error)
}

// jobEnv is what job mode needs besides the store.
type jobEnv struct {
	mediaRoot string
	opts      ingest.OptionsFunc // nil: transcode.DefaultOptions
	// grace is how long a retranscode keeps the replaced version
	// (mediattl.OldVersionGrace).
	grace time.Duration
	// freeBytes reports the space left for new files under dir; nil asks
	// the filesystem (mediapath.Root.FreeBytes). Tests replace it.
	freeBytes func(root *mediapath.Root, dir string) (uint64, error)
}

// runJob transcodes the movie whose job the dispatcher has started, into a
// new version folder library/{id}/{job id}/ (written aside, then one atomic
// rename). By the job's purpose:
//
//   - ingest: movie processing → version written → one transaction:
//     renditions, movie ready + paths + runtime, job succeeded
//   - retranscode: the movie stays ready and playable on its current
//     version throughout. First a free-space check (1.5 × the current
//     version). Then one transaction switches it to the new version and
//     schedules the old one's removal (env.grace from now), job succeeded
//
// On any failure after the job was found, the job is marked failed with the
// error (an ingested movie failed too; a retranscoded one stays ready on its
// current version), and the new version folder is not left behind.
func runJob(ctx context.Context, log *slog.Logger, st jobStore, env jobEnv, movieID string) (err error) {
	id := strings.ToLower(movieID)
	if !model.ValidID(id) {
		return errBadMovieID
	}
	t, err := st.StartTranscode(ctx, id)
	if err != nil {
		return fmt.Errorf("starting transcode of movie %s: %w", id, err)
	}
	log = log.With("movie_id", id, "job_id", t.JobID, "purpose", string(t.Purpose))
	if t.Purpose == model.PurposeRetranscode {
		log.Info("retranscoding; the movie stays ready on its current version", "source_path", t.SourcePath)
	} else {
		log.Info("movie processing", "source_path", t.SourcePath)
	}

	defer func() {
		if err == nil {
			return
		}
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), reportTimeout)
		defer cancel()
		ok, ferr := st.FailJob(rctx, t.JobID, errorMsg(ctx, err))
		switch {
		case ferr != nil:
			log.Error("could not mark the job failed", "err", ferr)
		case !ok:
			log.Warn("job was already finished by the dispatcher; left as it is")
		case t.Purpose == model.PurposeRetranscode:
			log.Info("job marked failed; the movie stays ready on its current version")
		default:
			log.Info("job and movie marked failed")
		}
	}()

	root, err := mediapath.Open(env.mediaRoot)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	// source_path comes from the database: only a regular file inside
	// MEDIA_ROOT, reached without a symlink, becomes an ffmpeg input.
	input, err := root.ExternalPath(t.SourcePath)
	if err != nil {
		return fmt.Errorf("source_path %q: %w", t.SourcePath, err)
	}
	if t.Purpose == model.PurposeRetranscode {
		if err := checkSpace(log, root, env, t.CurrentBytes); err != nil {
			return err
		}
	}

	pub, removeVersion, err := ingest.Library(ctx, log, env.mediaRoot, id, t.JobID, input, env.opts)
	if err != nil {
		return err
	}
	pub.OldVersionGrace = env.grace
	if err := finish(ctx, log, st, t, pub, removeVersion); err != nil {
		return err
	}
	if t.Purpose == model.PurposeRetranscode {
		log.Info("new version published; the old one is removed after the grace", "library_path", pub.LibraryPath,
			"renditions", len(pub.Renditions), "grace", env.grace.String())
		return nil
	}
	log.Info("movie ready", "library_path", pub.LibraryPath, "renditions", len(pub.Renditions),
		"runtime_sec", pub.RuntimeSec)
	return nil
}

// finish records the published version. If FinishTranscode returns an
// error, the COMMIT may still have gone through, so before removing the
// folder it asks again (store.JobPublished, a new query):
//
//   - committed (job succeeded, movie at this version): keep the folder,
//     success; removing it would leave the catalog pointing at nothing
//   - not committed: remove the folder, nothing would ever point at it
//   - cannot tell (the database still unreachable): keep the folder and
//     fail. If the commit did not happen, failing the job (here or by the
//     dispatcher's reconcile) sets library_cleanup_after, and the watcher
//     removes every folder the movie does not play; if it did, the folder
//     is the one the movie plays. Either way nothing is lost or left
func finish(ctx context.Context, log *slog.Logger, st jobStore, t store.TranscodeJob, pub store.Published, removeVersion func()) error {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), reportTimeout)
	defer cancel()
	err := st.FinishTranscode(rctx, t, pub)
	if err == nil {
		return nil
	}
	vctx, vcancel := context.WithTimeout(context.WithoutCancel(ctx), reportTimeout)
	defer vcancel()
	published, rerr := st.JobPublished(vctx, t.JobID, pub.LibraryPath)
	switch {
	case rerr == nil && published:
		log.Warn("recording the finished transcode returned an error, but it committed: keeping the new version",
			"library_path", pub.LibraryPath, "err", err)
		return nil
	case rerr != nil:
		log.Error("cannot tell whether the finished transcode was recorded: keeping the new version folder "+
			"(the watcher removes it unless the movie plays it)", "library_path", pub.LibraryPath, "err", err, "reread_err", rerr)
		return fmt.Errorf("recording the finished transcode: %w", err)
	default:
		removeVersion()
		return fmt.Errorf("recording the finished transcode: %w", err)
	}
}

// checkSpace refuses a retranscode that might fill the media filesystem:
// it needs 1.5 × the current version (its renditions plus versionMargin)
// free, since both versions sit side by side until the grace has passed.
// A platform that cannot tell is logged and let through.
func checkSpace(log *slog.Logger, root *mediapath.Root, env jobEnv, currentBytes int64) error {
	free := env.freeBytes
	if free == nil {
		free = func(r *mediapath.Root, dir string) (uint64, error) { return r.FreeBytes(dir) }
	}
	have, err := free(root, medialayout.Library)
	if errors.Is(err, errors.ErrUnsupported) {
		log.Warn("free space cannot be checked on this platform; retranscoding anyway")
		return nil
	}
	if err != nil {
		return fmt.Errorf("checking free space: %w", err)
	}
	need := uint64(max(currentBytes, 0)+versionMargin) / 2 * 3
	if have < need {
		return fmt.Errorf("not enough free space: need %s, have %s", formatBytes(need), formatBytes(have))
	}
	log.Info("free space checked", "need", formatBytes(need), "have", formatBytes(have))
	return nil
}

// formatBytes: 1610612736 → "1.5 GiB".
func formatBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func errorMsg(ctx context.Context, err error) string {
	msg := err.Error()
	if ctx.Err() != nil {
		msg = "interrupted by a stop signal: " + msg
	}
	if len(msg) > maxErrorMsg {
		msg = msg[:maxErrorMsg] + "…"
	}
	return strings.ToValidUTF8(msg, "�")
}
