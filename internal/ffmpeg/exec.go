// Package ffmpeg wraps the ffmpeg and ffprobe binaries: probe a source, remux
// or encode HLS rungs, and write the master playlist.
//
// Every decision (remux vs encode, which rungs exist) is made from Probe
// output, never from a file extension. Rungs are never taller than the source.
package ffmpeg

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// tool is one of the two binaries this package runs. The command name is
// always a literal, never caller-supplied.
type tool int

const (
	ffmpegBin tool = iota
	ffprobeBin
)

func (t tool) String() string {
	if t == ffprobeBin {
		return "ffprobe"
	}
	return "ffmpeg"
}

// command builds the process. Arguments are necessarily variable (they carry
// file paths), but they are safe: there is no shell, every path is passed as
// an absolute file: URL so it cannot be read as an option, and inputs are
// limited to -protocol_whitelist file.
func (t tool) command(ctx context.Context, args []string) *exec.Cmd {
	if t == ffprobeBin {
		return exec.CommandContext(ctx, "ffprobe", args...) // #nosec G204 -- see above
	}
	return exec.CommandContext(ctx, "ffmpeg", args...) // #nosec G204 -- see above
}

// maxStderr bounds how much tool output is kept for error messages.
const maxStderr = 4 << 10

// fileURL turns a path into an ffmpeg "file:" URL.
//
// Two reasons: an absolute path can never start with "-" and be parsed as an
// option, and the explicit protocol stops ffmpeg from treating names such as
// "concat:..." or "http://..." as anything other than a local file.
func fileURL(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolving %q: %w", path, err)
	}
	return "file:" + abs, nil
}

// inputArgs are prepended before every -i: local files only, so a crafted
// source (e.g. an HLS playlist in the inbox) cannot make ffmpeg fetch URLs.
var inputArgs = []string{"-protocol_whitelist", "file"}

// ProgressFunc receives how much of the input ffmpeg has processed so far.
// It is called from a separate goroutine, roughly every second.
type ProgressFunc func(done time.Duration)

// run executes a tool and returns its stdout. On failure the error carries
// the tail of stderr, which is where ffmpeg explains what went wrong.
func run(ctx context.Context, bin tool, args ...string) ([]byte, error) {
	var stdout bytes.Buffer
	if err := execute(ctx, bin, args, &stdout); err != nil {
		return nil, err
	}
	return stdout.Bytes(), nil
}

// runFFmpeg runs ffmpeg, reporting progress to fn when it is non-nil.
// ffmpeg writes key=value progress blocks to stdout with -progress pipe:1.
func runFFmpeg(ctx context.Context, fn ProgressFunc, args ...string) error {
	if fn == nil {
		return execute(ctx, ffmpegBin, args, io.Discard)
	}
	pr, pw := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		parseProgress(pr, fn)
	}()
	args = append([]string{"-progress", "pipe:1", "-stats_period", "1"}, args...)
	err := execute(ctx, ffmpegBin, args, pw)
	_ = pw.Close()
	<-done
	return err
}

func parseProgress(r io.Reader, fn ProgressFunc) {
	var last time.Duration
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		key, val, _ := strings.Cut(sc.Text(), "=")
		switch key {
		case "out_time_us": // "N/A" until the first frame is written
			if us, err := strconv.ParseInt(val, 10, 64); err == nil && us >= 0 {
				last = time.Duration(us) * time.Microsecond
			}
		case "progress": // ends each block: "continue" or "end"
			fn(last)
		}
	}
	_, _ = io.Copy(io.Discard, r) // never block ffmpeg on a full pipe
}

func execute(ctx context.Context, bin tool, args []string, stdout io.Writer) error {
	var stderr bytes.Buffer
	cmd := bin.command(ctx, args)
	cmd.Stdout = stdout
	cmd.Stderr = &stderr
	// After the context is cancelled (SIGTERM to the Job), give ffmpeg's
	// pipes a moment to drain, then stop waiting on them.
	cmd.WaitDelay = 5 * time.Second
	killWithParent(cmd)

	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return fmt.Errorf("%s not found in PATH (install with: sudo apt install ffmpeg): %w", bin, err)
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("running %s: %w", bin, ctxErr)
		}
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > maxStderr {
			msg = "…" + msg[len(msg)-maxStderr:]
		}
		return fmt.Errorf("running %s: %w: %s", bin, err, msg)
	}
	return nil
}

// mkdirShared creates one directory of HLS output.
//
// 0755, not 0750: media-delivery's nginx runs as a different UID and must
// traverse these dirs to read the 0644 files ffmpeg writes into them.
func mkdirShared(path string) error {
	if err := os.Mkdir(path, 0o755); err != nil { // #nosec G301 -- world-readable by design, see above
		return fmt.Errorf("creating %s: %w", path, err)
	}
	return nil
}

// checkPattern rejects paths that ffmpeg would read as an output filename
// pattern (HLS segments and image sequences expand "%d").
func checkPattern(path string) error {
	if strings.Contains(path, "%") {
		return fmt.Errorf("output path %q must not contain %%", path)
	}
	return nil
}
