package main

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vtnHAN-SOLO/hls-vod/internal/config"
	"github.com/vtnHAN-SOLO/hls-vod/internal/dispatch"
	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
	"github.com/vtnHAN-SOLO/hls-vod/internal/pgtest"
	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
)

// syncBuffer collects the transcoder's logs from its own goroutines.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// The whole local pipeline with the real binaries: a queued job becomes a
// ready movie, through `transcoder --movie-id` started by the local backend.
func TestLocalPipelineEndToEnd(t *testing.T) {
	for _, bin := range []string{"ffmpeg", "ffprobe", "go"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Fatalf("%s not found in PATH: %v", bin, err)
		}
	}
	work := t.TempDir()
	transcoder := filepath.Join(work, "transcoder")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", transcoder, "../transcoder") // #nosec G204 -- fixed build of this repo's transcoder into t.TempDir()
	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the transcoder: %v\n%s", err, b)
	}

	root := t.TempDir()
	id := model.NewID()
	source := filepath.Join(root, "sources", id+".mp4")
	if err := os.MkdirAll(filepath.Dir(source), 0o750); err != nil {
		t.Fatal(err)
	}
	// #nosec G204 -- fixed ffmpeg command; the only variable is a path under t.TempDir().
	gen := exec.CommandContext(t.Context(), "ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24:duration=4",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=4",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-g", "48",
		"-c:a", "aac", "-ac", "2", "-y", source)
	if b, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("generating clip: %v\n%s", err, b)
	}

	db := pgtest.Start(t)
	if _, err := db.Migrator(t).Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	st, err := store.New(t.Context(), db.DSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.EnqueueMovie(t.Context(), store.PendingMovie{ID: id, Title: "Clip (2024)", SourcePath: "sources/" + id + ".mp4"},
		func() error { return nil }); err != nil {
		t.Fatal(err)
	}

	logs := &syncBuffer{}
	local := &dispatch.Local{Bin: transcoder, Env: []string{"MEDIA_ROOT=" + root, "DATABASE_URL=" + db.DSN},
		Out: logs, Log: slog.New(slog.DiscardHandler), StopGrace: 5 * time.Second}
	d := &dispatch.Dispatcher{Store: st, Backend: local, Log: slog.New(slog.DiscardHandler),
		MaxConcurrent: 1, Deadline: time.Hour, StartGrace: time.Minute}

	var job, movie string
	statuses := func() {
		if err := db.Pool.QueryRow(t.Context(), `
			SELECT j.status, m.status FROM transcode_jobs j JOIN movies m ON m.id = j.movie_id WHERE m.id = $1`, id).
			Scan(&job, &movie); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(90 * time.Second)
	for statuses(); job == "running"; statuses() {
		if time.Now().After(deadline) {
			t.Fatalf("still running after 90s; transcoder logs:\n%s", logs)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err := d.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	statuses()
	if job != "succeeded" || movie != "ready" {
		t.Fatalf("job %s, movie %s; want succeeded and ready. transcoder logs:\n%s", job, movie, logs)
	}
	// Published into its version folder, the one the row names.
	m, err := st.GetMovie(t.Context(), id)
	if err != nil || m.LibraryPath == nil || !strings.HasPrefix(*m.LibraryPath, "library/"+id+"/") {
		t.Fatalf("movie = %+v, %v; want library_path library/{id}/{version}", m, err)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(*m.LibraryPath), "master.m3u8")); err != nil {
		t.Fatal(err)
	}
	if rs, err := st.ListRenditions(t.Context(), id); err != nil || len(rs) != 1 || rs[0].Height != 180 {
		t.Fatalf("renditions = %+v, %v", rs, err)
	}
}

// The service itself: it starts, serves its probes, and stops on cancel.
func TestRunServesProbes(t *testing.T) {
	db := pgtest.Start(t)
	if _, err := db.Migrator(t).Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Dispatcher{
		DatabaseURL: db.DSN, Backend: "local", TranscoderBin: "/nonexistent", MediaRoot: t.TempDir(),
		MaxConcurrent: 1, PollInterval: time.Hour, JobDeadline: time.Hour, ShutdownTimeout: 5 * time.Second,
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- run(ctx, cfg, slog.New(slog.DiscardHandler), ln) }()

	base := "http://" + ln.Addr().String()
	for _, path := range []string{"/healthz", "/readyz", "/metrics"} {
		var code int
		for range 50 {
			req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, base+path, nil)
			resp, err := http.DefaultClient.Do(req)
			if err == nil {
				code = resp.StatusCode
				_ = resp.Body.Close()
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", path, code)
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not return after cancel")
	}
}
