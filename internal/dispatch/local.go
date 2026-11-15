package dispatch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
)

// Local runs `<Bin> --movie-id <id>` as a child process on this machine.
// It is for a laptop with one dispatcher: the pid it records means nothing
// on another host.
//
// Each transcoder gets its own process group, so Ctrl-C on the dispatcher
// does not stop an encode: the transcoder keeps going, and the next
// dispatcher start finds it by its recorded pid (checked against
// /proc/<pid>/cmdline, so a reused pid is not mistaken for it).
type Local struct {
	Bin string    // transcoder binary
	Env []string  // added to the dispatcher's own environment
	Out io.Writer // the transcoder's stdout and stderr (its JSON logs)
	Log *slog.Logger
	// StopGrace is how long Stop waits after SIGTERM before SIGKILL.
	StopGrace time.Duration

	mu       sync.Mutex
	children map[string]*child // by job id: processes this dispatcher started
}

type child struct {
	pid  int
	done chan struct{}
	err  error // from Wait, valid once done is closed
}

// JobName implements Backend: local jobs have no k8s_job_name.
func (l *Local) JobName() func(string) string { return nil }

// Start implements Backend.
func (l *Local) Start(ctx context.Context, job store.RunningJob) (int, error) {
	// WithoutCancel: the transcoder must outlive a dispatcher shutdown or
	// restart; only a deadline (Stop) ends it early.
	cmd := exec.CommandContext(context.WithoutCancel(ctx), l.Bin, "--movie-id", job.MovieID) // #nosec G204 -- Bin is operator config; the id is a UUID from the database
	cmd.Env = append(os.Environ(), l.Env...)
	cmd.Stdout, cmd.Stderr = l.Out, l.Out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	c := &child{pid: cmd.Process.Pid, done: make(chan struct{})}
	go func() {
		c.err = cmd.Wait()
		close(c.done)
	}()
	l.mu.Lock()
	if l.children == nil {
		l.children = map[string]*child{}
	}
	l.children[job.ID] = c
	l.mu.Unlock()
	return c.pid, nil
}

// Status implements Backend.
func (l *Local) Status(_ context.Context, job store.RunningJob) (Status, error) {
	l.mu.Lock()
	c, ours := l.children[job.ID]
	l.mu.Unlock()
	if ours {
		select {
		case <-c.done:
			detail := "exit status 0"
			if c.err != nil {
				detail = c.err.Error()
			}
			return Status{State: Exited, Detail: detail}, nil
		default:
			return Status{State: Running}, nil
		}
	}
	// Started by an earlier dispatcher: look for it by pid.
	if job.LocalPID == nil {
		return Status{State: Missing, Detail: "no pid was recorded", MayStillStart: true}, nil
	}
	if l.isTranscoder(*job.LocalPID, job.MovieID) {
		return Status{State: Running}, nil
	}
	return Status{State: Missing, Detail: fmt.Sprintf("process %d is no longer running", *job.LocalPID)}, nil
}

// Stop implements Backend: SIGTERM to the transcoder's process group (it and
// its ffmpeg), then SIGKILL after StopGrace if it is still there. It returns
// at once; the kill happens in the background.
func (l *Local) Stop(_ context.Context, job store.RunningJob) error {
	l.mu.Lock()
	c, ours := l.children[job.ID]
	l.mu.Unlock()
	var pid int
	var gone func() bool
	switch {
	case ours:
		pid = c.pid
		gone = func() bool {
			select {
			case <-c.done:
				return true
			default:
				return false
			}
		}
	case job.LocalPID != nil && l.isTranscoder(*job.LocalPID, job.MovieID):
		pid = *job.LocalPID
		gone = func() bool { return !l.isTranscoder(pid, job.MovieID) }
	default:
		return nil // already gone
	}
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("SIGTERM to process group %d: %w", pid, err)
	}
	go func() {
		deadline := time.Now().Add(l.StopGrace)
		for time.Now().Before(deadline) {
			if gone() {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		if !gone() {
			l.Log.Warn("transcoder ignored SIGTERM; killing it", "pid", pid)
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
	}()
	return nil
}

// Active returns the pids of the transcoders this dispatcher started that
// are still running, for the shutdown log.
func (l *Local) Active() []int {
	l.mu.Lock()
	defer l.mu.Unlock()
	var pids []int
	for _, c := range l.children {
		select {
		case <-c.done:
		default:
			pids = append(pids, c.pid)
		}
	}
	return pids
}

// isTranscoder reports whether pid is running `<Bin> --movie-id <movieID>`.
func (l *Local) isTranscoder(pid int, movieID string) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline") // #nosec G304 -- fixed /proc path built from an integer
	if err != nil {
		return false
	}
	args := bytes.Split(bytes.TrimRight(b, "\x00"), []byte{0})
	if len(args) < 3 || filepath.Base(string(args[0])) != filepath.Base(l.Bin) {
		return false
	}
	for i := 1; i+1 < len(args); i++ {
		if string(args[i]) == "--movie-id" && string(args[i+1]) == movieID {
			return true
		}
	}
	return false
}
