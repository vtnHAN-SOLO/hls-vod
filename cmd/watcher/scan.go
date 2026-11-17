package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/vtnHAN-SOLO/hls-vod/internal/ffmpeg"
	"github.com/vtnHAN-SOLO/hls-vod/internal/medialayout"
	"github.com/vtnHAN-SOLO/hls-vod/internal/mediapath"
	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
)

const (
	// minAge: a file modified more recently may still be being copied in.
	minAge = 2 * time.Minute
	// orphanAge: a source with no movie row is returned to the inbox only
	// after this long. The claim lock is what protects the watcher's own
	// claims (a rename keeps the original mtime, so their files look old at
	// once); this age protects writers that don't take the lock, such as
	// devingest, whose fresh copy waits for its insert.
	orphanAge = 10 * time.Minute
	// claimTimeout bounds one claim's transaction, so a hung database
	// cannot hold the claim lock and block reconcile forever.
	claimTimeout = time.Minute
)

// errTaken means another watcher run renamed the file first.
var errTaken = errors.New("claimed by another run")

// queue is the slice of the store the watcher needs.
type queue interface {
	EnqueueMovie(ctx context.Context, m store.PendingMovie, claim func() error) error
	ReleaseOrphans(ctx context.Context, sourcePaths []string, release func(sourcePath string) error) (bool, error)
	DeletingMovies(ctx context.Context) ([]store.DeletingMovie, error)
	PurgeMovie(ctx context.Context, id string) error
	CleanupDue(ctx context.Context) ([]string, error)
	CleanupLibrary(ctx context.Context, id string, clean func(libraryPath *string) error) (bool, error)
	SubtitleBackfillCandidates(ctx context.Context) ([]store.SubtitleCandidate, error)
	BackfillSubtitles(ctx context.Context, movieID, libraryPath string, ts []model.SubtitleTrack) (bool, error)
	SourceClaimed(ctx context.Context, sourcePath string) (bool, error)
	PruneAuthRows(ctx context.Context, batch, maxBatches int) (store.Pruned, error)
}

type scanner struct {
	root  *mediapath.Root // MEDIA_ROOT
	q     queue
	log   *slog.Logger
	now   func() time.Time
	newID func() string

	// afterList, if set, runs once the inbox has been listed. Tests use it
	// to line two runs up on the same listing.
	afterList func()
}

type report struct {
	Deleted   int // movies an admin deleted: files and row removed
	Cleaned   int // movies whose old versions (or leftovers) were removed
	Subtitled int // movies published before subtitle_tracks that got their rows
	Claimed   int // moved to sources/ and queued
	Skipped   int // not a candidate yet, or taken by another run
	Released  int // orphaned sources returned to the inbox
	Failed    int
	Pruned    store.Pruned // auth rows that can never be used again
}

// Row cleanup bounds: at most pruneBatch × pruneMaxBatches rows per table
// per run (every 5 minutes); a backlog shrinks over a few runs.
const (
	pruneBatch      = 1000
	pruneMaxBatches = 10
)

// run is one scan, in this order: purge (remove the movies admins
// deleted), cleanup (old versions whose grace has passed), subtitles (rows
// for movies published before subtitle_tracks existed), prune (auth rows
// no longer in use), reconcile (return crash leftovers to the inbox), then
// claim every eligible inbox file. It is safe to run concurrently with itself: the
// rename into sources/ is the lock, so each file is claimed exactly once.
// Per-file failures are logged and joined into the returned error after
// every file has been tried.
func (s *scanner) run(ctx context.Context) (report, error) {
	var r report
	// The three top-level folders, so an empty media volume (a new cluster)
	// becomes a valid tree on the first run: media-delivery's readiness
	// lists library/, which no transcode has created yet.
	for _, d := range []string{medialayout.Inbox, medialayout.Sources, medialayout.Library} {
		if err := s.root.MkdirAll(d, 0o755); err != nil { // #nosec G301 -- the transcoder and nginx run as other UIDs
			return r, fmt.Errorf("creating %s/: %w", d, err)
		}
	}

	var errs []error
	// Before reconcile: a deleted movie's row goes only after its source
	// file, so reconcile never sees that source without a row and never
	// returns it to the inbox to be ingested again.
	if err := s.purge(ctx, &r); err != nil {
		errs = append(errs, err)
	}
	if err := s.cleanup(ctx, &r); err != nil {
		errs = append(errs, err)
	}
	if err := s.backfillSubtitles(ctx, &r); err != nil {
		errs = append(errs, err)
	}
	if err := s.prune(ctx, &r); err != nil {
		errs = append(errs, err)
	}
	if err := s.reconcile(ctx, &r); err != nil {
		errs = append(errs, err)
	}

	entries, err := s.root.ReadDir(medialayout.Inbox)
	if err != nil {
		return r, errors.Join(append(errs, fmt.Errorf("listing inbox/: %w", err))...)
	}
	if s.afterList != nil {
		s.afterList()
	}
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		ext, why := candidate(e)
		if why != "" {
			s.log.Debug("skipping", "file", e.Name(), "reason", why)
			r.Skipped++
			continue
		}
		switch err := s.claim(ctx, e.Name(), ext); {
		case err == nil:
			r.Claimed++
		case errors.Is(err, errTaken), errors.Is(err, errTooNew):
			r.Skipped++
		default:
			s.log.Error("claim failed", "file", e.Name(), "err", err)
			errs = append(errs, err)
			r.Failed++
		}
	}
	return r, errors.Join(errs...)
}

// candidate returns the lowercase video extension of e, or why it is not a
// file the watcher takes.
func candidate(e fs.DirEntry) (ext, why string) {
	name := e.Name()
	if strings.HasPrefix(name, ".") {
		return "", "hidden"
	}
	if !e.Type().IsRegular() {
		return "", "not a regular file"
	}
	// .part, .tmp and .crdownload are partial downloads: not video
	// extensions, so this rejects them too.
	ext, ok := medialayout.VideoExt(name)
	if !ok {
		return "", "not a video file"
	}
	return ext, ""
}

var errTooNew = errors.New("modified too recently")

// claim moves inbox/name to sources/{new id}.ext and queues it. If the queue
// insert fails after the rename, the file goes back to the inbox, unless the
// error came from a COMMIT that went through (see afterFailedInsert).
func (s *scanner) claim(ctx context.Context, name, ext string) error {
	fi, err := s.root.LstatEntry(medialayout.Inbox, name)
	if errors.Is(err, fs.ErrNotExist) {
		s.log.Info("skipping", "file", name, "reason", errTaken.Error())
		return errTaken
	}
	if err != nil {
		return fmt.Errorf("stat %q: %w", name, err)
	}
	if age := s.now().Sub(fi.ModTime()); age < minAge {
		s.log.Info("skipping", "file", name, "reason", "modified too recently, may still be copying",
			"age", age.Round(time.Second).String())
		return errTooNew
	}

	id := s.newID()
	dst := medialayout.SourceName(id, ext)
	m := store.PendingMovie{ID: id, Title: titleFrom(name), SourcePath: medialayout.SourcePath(id, ext)}

	ctx, cancel := context.WithTimeout(ctx, claimTimeout)
	defer cancel()
	renamed := false
	err = s.q.EnqueueMovie(ctx, m, func() error {
		// The rename is the lock: of two runs racing for the same file,
		// exactly one rename succeeds, the other finds it gone.
		if err := s.root.RenameEntry(medialayout.Inbox, name, medialayout.Sources, dst); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return errTaken
			}
			return fmt.Errorf("moving %q into sources/: %w", name, err)
		}
		renamed = true
		// Swapped for a symlink or a dir since the listing? Not ours to take.
		if fi, err := s.root.LstatEntry(medialayout.Sources, dst); err != nil || !fi.Mode().IsRegular() {
			return fmt.Errorf("%q is no longer a regular file", name)
		}
		return nil
	})
	if errors.Is(err, errTaken) {
		s.log.Info("skipping", "file", name, "reason", errTaken.Error())
		return errTaken
	}
	if err != nil {
		if !renamed {
			return err
		}
		if !s.afterFailedInsert(ctx, m, dst, name, err) {
			return err
		}
	}
	s.log.Info("queued", "file", name, "movie_id", id, "title", m.Title, "source_path", m.SourcePath)
	return nil
}

// afterFailedInsert decides what to do with sources/dst once EnqueueMovie
// failed after the rename. The COMMIT may have gone through anyway, so it
// asks again (store.SourceClaimed, a new query) before any undo:
//
//   - the movie row exists: the claim stands, the file stays in sources/
//     (true: count it as queued). Returning it to the inbox would ingest
//     it twice
//   - no row: give the file back to the inbox
//   - cannot tell: leave the file in sources/. If no row refers to it,
//     reconcile returns it to the inbox once it is orphanAge old; if one
//     does, it is where it belongs
func (s *scanner) afterFailedInsert(ctx context.Context, m store.PendingMovie, dst, name string, insertErr error) bool {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), claimTimeout)
	defer cancel()
	claimed, err := s.q.SourceClaimed(rctx, m.SourcePath)
	switch {
	case err != nil:
		s.log.Error("cannot tell whether the claim was recorded: leaving the file in sources/ for reconcile",
			"file", name, "source_path", m.SourcePath, "err", insertErr, "reread_err", err)
		return false
	case claimed:
		s.log.Warn("the queue insert returned an error, but it committed: keeping the claim",
			"file", name, "movie_id", m.ID, "err", insertErr)
		return true
	default:
		s.giveBack(dst, name)
		return false
	}
}

// giveBack returns sources/dst to inbox/name after a failed insert, never
// over a file someone has put there meanwhile: then it uses inbox/dst.
func (s *scanner) giveBack(dst, name string) {
	err := s.root.MoveEntryNoReplace(medialayout.Sources, dst, medialayout.Inbox, name)
	if errors.Is(err, fs.ErrExist) {
		err = s.root.MoveEntryNoReplace(medialayout.Sources, dst, medialayout.Inbox, dst)
		name = dst
	}
	if err != nil {
		s.log.Error("could not return the file to the inbox; a later run will, once it is "+
			orphanAge.String()+" old", "source", medialayout.Sources+"/"+dst, "err", err)
		return
	}
	s.log.Info("returned to the inbox after the failed insert", "file", name)
}

// purge removes every movie in status deleting: library/{id}/, then its
// source file, then the row (renditions, jobs and watch progress cascade).
// Files already gone are fine, so a run that crashed half way, or a second
// run, just finishes the job. The row stays until both are gone: while it
// exists, reconcile leaves the source alone.
func (s *scanner) purge(ctx context.Context, r *report) error {
	movies, err := s.q.DeletingMovies(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, m := range movies {
		if err := s.purgeOne(ctx, m); err != nil {
			s.log.Error("removing a deleted movie failed; the next run retries", "movie_id", m.ID, "err", err)
			errs = append(errs, err)
			r.Failed++
			continue
		}
		r.Deleted++
	}
	return errors.Join(errs...)
}

func (s *scanner) purgeOne(ctx context.Context, m store.DeletingMovie) error {
	if !model.ValidID(m.ID) {
		return fmt.Errorf("movie id %q is not a UUID", m.ID)
	}
	library := medialayout.LibraryPath(m.ID)
	if err := s.root.RemoveAll(library); err != nil {
		return fmt.Errorf("removing %s/: %w", library, err)
	}
	if err := s.root.Remove(m.SourcePath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("removing %s: %w", m.SourcePath, err)
	}
	if err := s.q.PurgeMovie(ctx, m.ID); err != nil {
		return err
	}
	s.log.Info("deleted movie removed", "movie_id", m.ID, "library", library+"/", "source_path", m.SourcePath)
	return nil
}

// cleanupTimeout bounds one movie's cleanup: its row stays locked meanwhile,
// so a retranscode of that movie waits.
const cleanupTimeout = time.Minute

// cleanup removes, for every movie whose library_cleanup_after has passed
// and that has no queued or running job, everything in library/{id}/ but
// the version it plays now: the versions a retranscode replaced (every URL
// signed for them has expired by now), files of the layout from before
// versions, and .partial-* folders of killed runs. The store holds the
// movie's row lock while it runs (see store.CleanupLibrary). Missing
// entries are fine; an error keeps the time set, so the next run retries.
func (s *scanner) cleanup(ctx context.Context, r *report) error {
	ids, err := s.q.CleanupDue(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, id := range ids {
		cctx, cancel := context.WithTimeout(ctx, cleanupTimeout)
		ran, err := s.q.CleanupLibrary(cctx, id, func(libraryPath *string) error { return s.cleanOne(id, libraryPath) })
		cancel()
		switch {
		case err != nil:
			s.log.Error("cleaning up a movie's old versions failed; the next run retries", "movie_id", id, "err", err)
			errs = append(errs, err)
			r.Failed++
		case ran:
			r.Cleaned++
		}
	}
	return errors.Join(errs...)
}

// cleanOne removes what library/{id}/ holds besides the current version:
// with library_path "library/{id}/{version}" everything but {version}; with
// the old layout ("library/{id}" itself) only hidden .partial-* folders; with
// no library_path (a failed ingest) everything. Any other library_path is
// refused: nothing is removed on a guess.
func (s *scanner) cleanOne(id string, libraryPath *string) error {
	if !model.ValidID(id) {
		return fmt.Errorf("movie id %q is not a UUID", id)
	}
	keep, oldLayout := "", false
	if libraryPath != nil {
		v, ok := medialayout.Version(id, *libraryPath)
		if !ok {
			return fmt.Errorf("library_path %q is not library/%s[/{version}]; nothing removed", *libraryPath, id)
		}
		keep, oldLayout = v, v == ""
	}
	dir := medialayout.LibraryPath(id)
	entries, err := s.root.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("listing %s/: %w", dir, err)
	}
	for _, e := range entries {
		name := e.Name()
		if (keep != "" && name == keep) || (oldLayout && !strings.HasPrefix(name, ".")) {
			continue
		}
		if err := s.root.RemoveAllEntry(dir, name); err != nil {
			return fmt.Errorf("removing %s/%s: %w", dir, name, err)
		}
		s.log.Info("old version removed", "movie_id", id, "entry", dir+"/"+name, "kept", keep)
	}
	return nil
}

// backfillSubtitles gives the movies published before subtitle_tracks
// existed their rows: ready movies with no rows whose current version has a
// subs/ folder. The files are read back by name ({n}_{lang}[_forced].vtt,
// as the transcoder writes them); the stream titles were never stored, so
// labels stay empty. Idempotent: the store writes only while the movie
// still plays that version and has no rows, so a second run, or one racing
// a retranscode, changes nothing.
func (s *scanner) backfillSubtitles(ctx context.Context, r *report) error {
	cs, err := s.q.SubtitleBackfillCandidates(ctx)
	if err != nil {
		r.Failed++
		return err
	}
	var errs []error
	for _, c := range cs {
		ts, err := s.subtitleFiles(c)
		if err == nil && len(ts) > 0 {
			var wrote bool
			wrote, err = s.q.BackfillSubtitles(ctx, c.ID, c.LibraryPath, ts)
			if wrote {
				r.Subtitled++
				s.log.Info("subtitle rows backfilled", "movie_id", c.ID, "tracks", len(ts))
			}
		}
		if err != nil {
			s.log.Error("subtitle backfill failed; the next run retries", "movie_id", c.ID, "err", err)
			errs = append(errs, err)
			r.Failed++
		}
	}
	return errors.Join(errs...)
}

// subtitleFiles lists the WebVTT tracks in the current version's subs/
// folder, in stream order; none when there is no such folder.
func (s *scanner) subtitleFiles(c store.SubtitleCandidate) ([]model.SubtitleTrack, error) {
	version, ok := medialayout.Version(c.ID, c.LibraryPath)
	if !model.ValidID(c.ID) || !ok {
		// Cleanup reports such a movie as a failure already; nothing is read on a guess.
		s.log.Warn("subtitle backfill skipped: library_path is not library/{id}[/{version}]",
			"movie_id", c.ID, "library_path", c.LibraryPath)
		return nil, nil
	}
	dir := c.LibraryPath + "/" + ffmpeg.SubtitlesDir
	entries, err := s.root.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("listing %s/: %w", dir, err)
	}
	var ts []model.SubtitleTrack
	for _, e := range entries {
		pos, lang, _, ok := ffmpeg.ParseSubtitleName(e.Name())
		if !ok || !e.Type().IsRegular() {
			continue
		}
		ts = append(ts, model.SubtitleTrack{
			Path: medialayout.InVersion(version, ffmpeg.SubtitlesDir+"/"+e.Name()), Language: lang, Position: pos,
		})
	}
	slices.SortFunc(ts, func(a, b model.SubtitleTrack) int {
		return cmp.Or(a.Position-b.Position, strings.Compare(a.Path, b.Path))
	})
	return ts, nil
}

// prune deletes the database rows that can never be used again (see
// store.PruneAuthRows): refresh-token families dead for 7 days, email tokens
// used or expired 7 days ago, expired sign-in states. Not file work, but
// the watcher is the one periodic job, so it does this too.
func (s *scanner) prune(ctx context.Context, r *report) error {
	p, err := s.q.PruneAuthRows(ctx, pruneBatch, pruneMaxBatches)
	r.Pruned = p
	if err != nil {
		s.log.Error("pruning old rows failed; the next run retries", "err", err)
		r.Failed++
		return err
	}
	if p != (store.Pruned{}) {
		s.log.Info("old rows pruned", "refresh_tokens", p.RefreshTokens, "email_tokens", p.EmailTokens,
			"oauth_states", p.OAuthStates)
	}
	return nil
}

// reconcile returns sources/ files that no movie row refers to, older than
// orphanAge, to the inbox: a crash between rename and insert. The original
// name is gone by then, so the file returns as inbox/{id}.{ext} and its next
// claim is titled with that id.
func (s *scanner) reconcile(ctx context.Context, r *report) error {
	entries, err := s.root.ReadDir(medialayout.Sources)
	if err != nil {
		return fmt.Errorf("listing sources/: %w", err)
	}
	var old []string
	for _, e := range entries {
		id, ext, ok := strings.Cut(e.Name(), ".")
		if _, video := medialayout.VideoExt(e.Name()); !ok || !video || !model.ValidID(id) || !e.Type().IsRegular() ||
			medialayout.SourceName(id, ext) != e.Name() {
			continue // not a claimed source (a devingest temp file, say)
		}
		fi, err := s.root.LstatEntry(medialayout.Sources, e.Name())
		if err != nil {
			continue // gone since the listing
		}
		if s.now().Sub(fi.ModTime()) >= orphanAge {
			old = append(old, medialayout.SourcePath(id, ext))
		}
	}

	ran, err := s.q.ReleaseOrphans(ctx, old, func(sourcePath string) error {
		name := strings.TrimPrefix(sourcePath, medialayout.Sources+"/")
		err := s.root.MoveEntryNoReplace(medialayout.Sources, name, medialayout.Inbox, name)
		if errors.Is(err, fs.ErrNotExist) {
			// Gone since the listing: another run purged a deleted movie.
			return nil
		}
		if err != nil {
			return fmt.Errorf("returning orphaned %s to the inbox: %w", sourcePath, err)
		}
		s.log.Warn("returned an orphaned source to the inbox (crash between claim and insert?)",
			"source_path", sourcePath, "file", name)
		r.Released++
		return nil
	})
	if !ran && err == nil {
		s.log.Info("reconcile skipped: another run is claiming right now")
	}
	return err
}

// titleFrom is the catalog title for an inbox file: its name without the
// extension, exactly as the person typed it ("My Movie (2020)"). Invalid
// UTF-8 is replaced, since Postgres text refuses it.
func titleFrom(name string) string {
	base := name
	if i := strings.LastIndexByte(name, '.'); i > 0 {
		base = name[:i]
	}
	if t := strings.TrimSpace(strings.ToValidUTF8(base, "�")); t != "" {
		return t
	}
	return strings.ToValidUTF8(name, "�")
}
