// Package transcode is the ingest pipeline: one source file in, one complete
// movie directory (HLS ladder, poster, thumbnails, subtitles) out. It is used
// by cmd/transcoder and by the dev-only tools/devingest.
package transcode

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vtnHAN-SOLO/hls-vod/internal/ffmpeg"
	"github.com/vtnHAN-SOLO/hls-vod/internal/medialayout"
)

// ErrUsage means Input or Output is empty.
var ErrUsage = errors.New("both --input and --output are required")

// Options configures one transcode. Tests shrink the thumbnail grid and
// report progress on every update.
type Options struct {
	Input, Output string
	ProgressEvery time.Duration // minimum gap between progress log lines
	PosterHeight  int
	Thumbnails    ffmpeg.ThumbnailOptions

	// afterProbe, when set, may adjust the probe result. Tests use it to add
	// a broken subtitle track, which ffmpeg cannot be made to produce.
	afterProbe func(*ffmpeg.ProbeResult)
}

// DefaultOptions are the production settings for input → output.
func DefaultOptions(input, output string) Options {
	return Options{
		Input: input, Output: output,
		ProgressEvery: 30 * time.Second,
		PosterHeight:  720,
		Thumbnails:    ffmpeg.DefaultThumbnails,
	}
}

// partialPrefix marks an unfinished output dir next to --output. It starts
// with a dot and is never a movie UUID, so media-delivery never serves it.
func partialPrefix(output string) string { return "." + filepath.Base(output) + ".partial-" }

// Result describes a published movie directory.
type Result struct {
	Output   string        // absolute path of the published directory
	Duration time.Duration // length of the written top rung
	Rungs    []Rung        // tallest first
	Poster   string        // poster file, relative to Output
}

// Rung is one written HLS rendition.
type Rung struct {
	Name        string // directory name, e.g. "720p"
	Height      int    // probed from the output, not the plan
	BitrateKbps int    // measured average over all segments
	SizeBytes   int64  // playlist plus segments
	Playlist    string // relative to Output, e.g. "720p/index.m3u8"
}

// Run runs the whole pipeline for one source:
//
//	probe → top rung (remux / copy video / encode) → lower rungs → poster
//	→ thumbnail sprites + WebVTT → subtitles → master.m3u8
//
// Everything is written into a temp dir next to opt.Output, which is renamed
// into place only once complete. A failed or cancelled run removes it; a
// killed run (SIGKILL, OOM) leaves only a hidden ".partial-" dir that the
// next run for the same output removes.
func Run(ctx context.Context, log *slog.Logger, opt Options) (res Result, err error) {
	start := time.Now()
	output, err := checkPaths(opt.Input, opt.Output)
	if err != nil {
		return Result{}, err
	}
	parent := filepath.Dir(output)
	sweepPartials(log, output)

	tmp, err := os.MkdirTemp(parent, partialPrefix(output))
	if err != nil {
		return Result{}, fmt.Errorf("creating temp output dir: %w", err)
	}
	defer func() {
		if err != nil {
			if rmErr := os.RemoveAll(tmp); rmErr != nil {
				log.Error("removing partial output", "dir", tmp, "err", rmErr)
			}
		}
	}()
	log.Info("transcode started", "input", opt.Input, "output", output, "work_dir", tmp)

	src, err := ffmpeg.Probe(ctx, opt.Input)
	if err != nil {
		return Result{}, err
	}
	if opt.afterProbe != nil {
		opt.afterProbe(&src)
	}
	log.Info("probed source",
		"video_codec", src.VideoCodec, "profile", src.Profile, "pix_fmt", src.PixFmt,
		"width", src.Width, "height", src.Height, "duration_sec", math.Round(src.Duration.Seconds()),
		"audio_codec", src.AudioCodec, "audio_channels", src.AudioChannels,
		"subtitle_streams", len(src.Subtitles))

	rungs, err := ffmpeg.PlanLadder(src)
	if err != nil {
		return Result{}, err
	}
	plan := make([]string, len(rungs))
	for i, r := range rungs {
		plan[i] = r.Name + ":" + r.Mode.String()
	}
	log.Info("planned ladder", "rungs", plan)

	p := &pipeline{log: log, every: opt.ProgressEvery}
	variants := make([]ffmpeg.Variant, 0, len(rungs))
	written := make([]Rung, 0, len(rungs))
	for _, r := range rungs {
		dir := filepath.Join(tmp, r.Name)
		err := p.step(ctx, "rung", []any{"rung", r.Name, "mode", r.Mode.String()}, func(progress ffmpeg.ProgressFunc) error {
			return ffmpeg.WriteRung(ctx, opt.Input, dir, r, progress)
		}, src.Duration)
		if err != nil {
			return Result{}, err
		}
		v, err := ffmpeg.DescribeRung(ctx, dir, r.Name+"/"+ffmpeg.PlaylistName)
		if err != nil {
			return Result{}, err
		}
		size, err := dirSize(dir)
		if err != nil {
			return Result{}, err
		}
		variants = append(variants, v)
		written = append(written, Rung{
			Name: r.Name, Height: v.Height, BitrateKbps: (v.AverageBandwidth + 500) / 1000,
			SizeBytes: size, Playlist: v.URI,
		})
	}

	// The written top rung is the authoritative length, even when the
	// source container did not record one.
	duration, err := ffmpeg.PlaylistDuration(filepath.Join(tmp, rungs[0].Name))
	if err != nil {
		return Result{}, err
	}

	err = p.step(ctx, "poster", nil, func(ffmpeg.ProgressFunc) error {
		return ffmpeg.ExtractPoster(ctx, opt.Input, filepath.Join(tmp, ffmpeg.PosterName), src, duration/10, opt.PosterHeight)
	}, 0)
	if err != nil {
		return Result{}, err
	}

	var thumbs ffmpeg.ThumbnailResult
	// No progress lines here: with image output, ffmpeg's out_time counts
	// sprite sheets, not movie time, so a percentage would be wrong.
	err = p.step(ctx, "thumbnails", nil, func(ffmpeg.ProgressFunc) error {
		thumbs, err = ffmpeg.GenerateThumbnails(ctx, opt.Input, tmp, src, duration, opt.Thumbnails, nil)
		return err
	}, 0)
	if err != nil {
		return Result{}, err
	}

	var subs ffmpeg.SubtitleResult
	err = p.step(ctx, "subtitles", nil, func(ffmpeg.ProgressFunc) error {
		subs, err = ffmpeg.ExtractSubtitles(ctx, opt.Input, tmp, src)
		return err
	}, 0)
	if err != nil {
		return Result{}, err
	}
	// A subtitle that cannot be converted never blocks playback.
	for _, s := range subs.Skipped {
		attrs := []any{"stream", s.Index, "codec", s.Codec,
			"language", s.Language, "title", s.Title, "reason", s.Reason}
		if s.Err != nil {
			attrs = append(attrs, "err", s.Err)
		}
		log.Warn("subtitle skipped", attrs...)
	}

	if err := ffmpeg.WriteMasterPlaylist(filepath.Join(tmp, medialayout.MasterPlaylist), variants); err != nil {
		return Result{}, err
	}
	if err := publish(tmp, output); err != nil {
		return Result{}, err
	}

	subPaths := make([]string, len(subs.Written))
	for i, s := range subs.Written {
		subPaths[i] = s.Path
	}
	log.Info("transcode complete",
		"output", output, "rungs", plan, "duration_sec", math.Round(duration.Seconds()),
		"thumbnails", thumbs.Count, "sprite_sheets", thumbs.Sheets,
		"subtitles", subPaths, "subtitles_skipped", len(subs.Skipped),
		"took_ms", time.Since(start).Milliseconds())
	return Result{Output: output, Duration: duration, Rungs: written, Poster: ffmpeg.PosterName}, nil
}

// checkPaths validates the arguments before any work starts, so a typo fails
// in milliseconds rather than after an hour of encoding.
func checkPaths(input, output string) (string, error) {
	if input == "" || output == "" {
		return "", ErrUsage
	}
	fi, err := os.Stat(input)
	if err != nil {
		return "", fmt.Errorf("input: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("input %s is not a regular file", input)
	}

	output, err = filepath.Abs(output)
	if err != nil {
		return "", fmt.Errorf("resolving output: %w", err)
	}
	if strings.Contains(output, "%") {
		return "", fmt.Errorf("output %s must not contain %%", output)
	}
	if _, err := os.Lstat(output); err == nil {
		return "", fmt.Errorf("output %s already exists; remove it first", output)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("output: %w", err)
	}
	if fi, err := os.Stat(filepath.Dir(output)); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("output parent dir %s must exist", filepath.Dir(output))
	}
	return output, nil
}

// sweepPartials removes unfinished dirs left by a killed run for this same
// output. Safe because only one transcode per movie runs at a time: the
// database refuses a second active transcode_job for a movie.
func sweepPartials(log *slog.Logger, output string) {
	entries, err := os.ReadDir(filepath.Dir(output))
	if err != nil {
		return
	}
	prefix := partialPrefix(output)
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		dir := filepath.Join(filepath.Dir(output), e.Name())
		if err := os.RemoveAll(dir); err != nil {
			log.Warn("removing stale partial output", "dir", dir, "err", err)
			continue
		}
		log.Warn("removed stale partial output from an earlier killed run", "dir", dir)
	}
}

// publish makes the finished dir visible at output in one rename(2): a
// reader sees either nothing or the complete movie.
func publish(tmp, output string) error {
	// MkdirTemp creates 0700. nginx in media-delivery runs as another UID
	// and must traverse the movie dir to read its 0644 files.
	if err := os.Chmod(tmp, 0o755); err != nil { // #nosec G302 -- world-readable by design, see above
		return fmt.Errorf("chmod output dir: %w", err)
	}
	// rename(2) would silently replace an empty dir created meanwhile.
	if _, err := os.Lstat(output); err == nil {
		return fmt.Errorf("output %s appeared while transcoding; not replacing it", output)
	}
	if err := os.Rename(tmp, output); err != nil {
		return fmt.Errorf("moving output into place: %w", err)
	}
	return nil
}

// dirSize sums the regular files in one rung dir (this process created it).
func dirSize(dir string) (int64, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, fmt.Errorf("sizing %s: %w", dir, err)
	}
	var total int64
	for _, e := range entries {
		fi, err := e.Info()
		if err != nil {
			return 0, fmt.Errorf("sizing %s: %w", dir, err)
		}
		if fi.Mode().IsRegular() {
			total += fi.Size()
		}
	}
	return total, nil
}

// pipeline logs the start, progress and end of each step.
type pipeline struct {
	log   *slog.Logger
	every time.Duration
}

// step runs fn, logging "step started" / "step done" with timings, and hands
// fn a ProgressFunc that logs at most once per p.every. total is the media
// length the step works through; 0 when unknown or not meaningful.
func (p *pipeline) step(ctx context.Context, name string, attrs []any, fn func(ffmpeg.ProgressFunc) error, total time.Duration) error {
	base := append([]any{"step", name}, attrs...)
	p.log.InfoContext(ctx, "step started", base...)
	start := time.Now()

	var last time.Time
	progress := func(done time.Duration) {
		if time.Since(last) < p.every {
			return
		}
		last = time.Now()
		kv := append([]any{}, base...)
		kv = append(kv, "done_sec", int(done.Seconds()), "elapsed_sec", int(time.Since(start).Seconds()))
		if total > 0 {
			kv = append(kv, "percent", math.Round(min(100, 100*done.Seconds()/total.Seconds())*10)/10)
		}
		p.log.InfoContext(ctx, "progress", kv...)
	}

	if err := fn(progress); err != nil {
		return fmt.Errorf("step %s%s: %w", name, kvString(attrs), err)
	}
	p.log.InfoContext(ctx, "step done", append(base, "took_ms", time.Since(start).Milliseconds())...)
	return nil
}

// kvString renders slog-style key/value pairs for an error message,
// e.g. " rung=720p mode=remux".
func kvString(kv []any) string {
	var b strings.Builder
	for i := 0; i+1 < len(kv); i += 2 {
		fmt.Fprintf(&b, " %v=%v", kv[i], kv[i+1])
	}
	return b.String()
}
