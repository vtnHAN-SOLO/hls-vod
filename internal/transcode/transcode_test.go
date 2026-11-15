package transcode

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/vtnHAN-SOLO/hls-vod/internal/ffmpeg"
	"github.com/vtnHAN-SOLO/hls-vod/internal/medialayout"
)

// movie is a generated "messy" source: 720p H.264 with 5.1 AC3 audio and an
// English SRT track, 14s. Expected: 720p copy-video + 480p encode.
var movie string

const movieSeconds = 14

func TestMain(m *testing.M) {
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			fmt.Fprintf(os.Stderr, "FAIL: %s not found in PATH; these tests need it.\n"+
				"Install with: sudo apt install ffmpeg\n", bin)
			os.Exit(1)
		}
	}
	dir, err := os.MkdirTemp("", "transcoder-fixtures-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: %v\n", err)
		os.Exit(1)
	}
	movie, err = makeMovie(dir)
	if err != nil {
		_ = os.RemoveAll(dir)
		fmt.Fprintf(os.Stderr, "FAIL: generating test movie: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func makeMovie(dir string) (string, error) {
	srt := filepath.Join(dir, "movie.srt")
	if err := os.WriteFile(srt, []byte("1\n00:00:01,000 --> 00:00:03,000\nHello\n"), 0o600); err != nil {
		return "", err
	}
	out := filepath.Join(dir, "movie.mkv")
	dur := fmt.Sprint(movieSeconds)
	// #nosec G204 -- fixed ffmpeg command; the only variables are paths under os.MkdirTemp.
	cmd := exec.CommandContext(context.Background(), "ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=24:duration="+dur,
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration="+dur,
		"-i", srt,
		"-map", "0", "-map", "1", "-map", "2",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-g", "48",
		"-c:a", "ac3", "-ac", "6", "-c:s", "srt", "-metadata:s:s:0", "language=eng",
		"-y", out)
	if b, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("%w: %s", err, b)
	}
	return out, nil
}

func testOptions(output string) Options {
	o := DefaultOptions(movie, output)
	o.ProgressEvery = 0 // log every update
	o.PosterHeight = 360
	return o
}

func TestTranscode(t *testing.T) {
	parent := t.TempDir()
	output := filepath.Join(parent, "movie")

	// Left behind by an earlier killed run: must be swept.
	stale := filepath.Join(parent, ".movie.partial-123")
	if err := os.MkdirAll(filepath.Join(stale, "720p"), 0o750); err != nil {
		t.Fatal(err)
	}

	var logs syncBuffer
	res, err := Run(t.Context(), jsonLogger(&logs), testOptions(output))
	if err != nil {
		t.Fatalf("transcode: %v\nlogs:\n%s", err, logs.String())
	}
	assertResult(t, res, output)

	for _, f := range []string{
		medialayout.MasterPlaylist,
		"720p/" + ffmpeg.PlaylistName, "720p/seg_0000.ts",
		"480p/" + ffmpeg.PlaylistName, "480p/seg_0000.ts",
		ffmpeg.PosterName, medialayout.ThumbnailsVTT, "thumbs/sprite_000.jpg",
		"subs/0_eng.vtt",
	} {
		if _, err := os.Stat(filepath.Join(output, f)); err != nil {
			t.Errorf("missing %s: %v", f, err)
		}
	}
	master, err := os.ReadFile(filepath.Join(output, medialayout.MasterPlaylist))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"RESOLUTION=1280x720", "RESOLUTION=854x480"} {
		if !strings.Contains(string(master), want) {
			t.Errorf("master missing %s:\n%s", want, master)
		}
	}
	// ffprobe follows the master into a rung: the output is playable HLS.
	top, err := ffmpeg.Probe(t.Context(), filepath.Join(output, medialayout.MasterPlaylist))
	if err != nil {
		t.Fatalf("probing master: %v", err)
	}
	if top.AudioCodec != "aac" || top.AudioChannels != 2 {
		t.Errorf("rung audio = %s %dch, want aac stereo from 5.1 ac3", top.AudioCodec, top.AudioChannels)
	}

	fi, err := os.Stat(output)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o755 {
		t.Errorf("output dir mode = %v, want 0755 for nginx", fi.Mode().Perm())
	}
	assertOnlyEntries(t, parent, "movie") // stale partial swept, no temp left

	lines := parseLogs(t, logs.String())
	want := map[string]bool{"planned ladder": false, "progress": false, "transcode complete": false,
		"removed stale partial output from an earlier killed run": false}
	for _, l := range lines {
		if _, ok := want[l.Msg]; ok {
			want[l.Msg] = true
		}
		if l.Msg == "planned ladder" {
			if got := fmt.Sprint(l.Rungs); got != "[720p:copy-video 480p:encode]" {
				t.Errorf("planned ladder = %s, want [720p:copy-video 480p:encode]", got)
			}
		}
	}
	for msg, seen := range want {
		if !seen {
			t.Errorf("no %q log line", msg)
		}
	}
}

// A subtitle track ffmpeg cannot convert is skipped with a WARN naming the
// stream and the error; the movie is still published and playable.
func TestTranscodeSurvivesBrokenSubtitle(t *testing.T) {
	output := filepath.Join(t.TempDir(), "movie")
	opt := testOptions(output)
	opt.afterProbe = func(p *ffmpeg.ProbeResult) {
		p.Subtitles = append(p.Subtitles, ffmpeg.SubtitleStream{Index: 99, Codec: "subrip", Language: "fre"})
	}
	var logs syncBuffer
	if _, err := Run(t.Context(), jsonLogger(&logs), opt); err != nil {
		t.Fatalf("a broken subtitle failed the movie: %v", err)
	}
	if _, err := os.Stat(filepath.Join(output, "subs/0_eng.vtt")); err != nil {
		t.Errorf("good subtitle missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(output, "subs/1_fre.vtt")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("broken subtitle left a file: %v", err)
	}
	if _, err := ffmpeg.Probe(t.Context(), filepath.Join(output, medialayout.MasterPlaylist)); err != nil {
		t.Fatalf("movie not playable: %v", err)
	}

	var warned bool
	for _, l := range parseLogs(t, logs.String()) {
		if l.Msg == "subtitle skipped" {
			warned = l.Level == "WARN" && l.Stream == 99 && strings.Contains(l.Err, "0:99")
		}
	}
	if !warned {
		t.Fatalf("no WARN \"subtitle skipped\" naming stream 99 and its ffmpeg error:\n%s", logs.String())
	}
}

// Not media: fails after the temp dir exists, and leaves nothing behind.
func TestTranscodeFailureLeavesNothing(t *testing.T) {
	parent := t.TempDir()
	bogus := filepath.Join(t.TempDir(), "movie.mkv")
	if err := os.WriteFile(bogus, []byte("not a video"), 0o600); err != nil {
		t.Fatal(err)
	}
	opt := testOptions(filepath.Join(parent, "movie"))
	opt.Input = bogus
	if _, err := Run(t.Context(), jsonLogger(io.Discard), opt); err == nil {
		t.Fatal("expected an error")
	}
	assertOnlyEntries(t, parent)
}

// SIGTERM in the middle of an encode: ffmpeg is stopped and the half-written
// movie (top rung already complete) is removed, never published.
func TestTranscodeCancelledMidEncode(t *testing.T) {
	parent := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	// Cancel on the first progress report of the 480p encode: ffmpeg is
	// running and the 720p rung is already on disk in the temp dir.
	trip := &tripWriter{match: [][]byte{[]byte(`"msg":"progress"`), []byte(`"rung":"480p"`)}, fire: cancel}
	_, err := Run(ctx, jsonLogger(trip), testOptions(filepath.Join(parent, "movie")))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if !trip.fired() {
		t.Fatal("never reached the 480p encode")
	}
	assertOnlyEntries(t, parent)
}

func TestTranscodeRefusesBadPaths(t *testing.T) {
	parent := t.TempDir()
	existing := filepath.Join(parent, "movie")
	if err := os.Mkdir(existing, 0o750); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(existing, "keep")
	if err := os.WriteFile(keep, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name, input, output, want string
	}{
		{"missing args", "", "", "required"},
		{"missing input", filepath.Join(parent, "nope.mkv"), filepath.Join(parent, "new"), "no such file"},
		{"input is a dir", parent, filepath.Join(parent, "new"), "not a regular file"},
		{"output exists", movie, existing, "already exists"},
		{"no parent", movie, filepath.Join(parent, "a", "b"), "parent dir"},
		{"percent in output", movie, filepath.Join(parent, "m%d"), "must not contain"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opt := testOptions(tt.output)
			opt.Input = tt.input
			_, err := Run(t.Context(), jsonLogger(io.Discard), opt)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want it to mention %q", err, tt.want)
			}
		})
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("existing output was touched: %v", err)
	}
	assertOnlyEntries(t, parent, "movie")
}

// --- helpers ---

type logLine struct {
	Level  string   `json:"level"`
	Stream int      `json:"stream"`
	Msg    string   `json:"msg"`
	Err    string   `json:"err"`
	Rungs  []string `json:"rungs"`
}

func jsonLogger(w io.Writer) *slog.Logger { return slog.New(slog.NewJSONHandler(w, nil)) }

// parseLogs fails the test if any line is not JSON: Job logs must be machine-readable.
func parseLogs(t *testing.T, s string) []logLine {
	t.Helper()
	var out []logLine
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		var l logLine
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			t.Fatalf("log line is not JSON: %q", sc.Text())
		}
		out = append(out, l)
	}
	return out
}

func assertOnlyEntries(t *testing.T, dir string, want ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("%s contains %v, want %v", dir, got, want)
	}
}

// syncBuffer is a bytes.Buffer safe for the progress goroutine to log into.
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

// tripWriter calls fire once, on the first log line containing every match.
type tripWriter struct {
	mu    sync.Mutex
	match [][]byte
	fire  func()
	done  bool
}

func (w *tripWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.done {
		all := true
		for _, m := range w.match {
			all = all && bytes.Contains(p, m)
		}
		if all {
			w.done = true
			w.fire()
		}
	}
	return len(p), nil
}

func (w *tripWriter) fired() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.done
}

// assertResult checks Result against what is really on disk: the catalog
// rows written from it must describe the files media-delivery will serve.
func assertResult(t *testing.T, res Result, output string) {
	t.Helper()
	if res.Output != output || res.Poster != ffmpeg.PosterName {
		t.Fatalf("result = %+v", res)
	}
	if d := res.Duration.Seconds(); d < movieSeconds-1 || d > movieSeconds+1 {
		t.Errorf("duration = %v, want ~%ds", res.Duration, movieSeconds)
	}
	if len(res.Rungs) != 2 {
		t.Fatalf("rungs = %+v, want 720p and 480p", res.Rungs)
	}
	for i, want := range []struct {
		name   string
		height int
	}{{"720p", 720}, {"480p", 480}} {
		r := res.Rungs[i]
		if r.Name != want.name || r.Height != want.height || r.Playlist != want.name+"/"+ffmpeg.PlaylistName || r.BitrateKbps <= 0 {
			t.Errorf("rung %d = %+v, want %s at %dp", i, r, want.name, want.height)
		}
		entries, err := os.ReadDir(filepath.Join(output, r.Name))
		if err != nil {
			t.Fatal(err)
		}
		var size int64
		for _, e := range entries {
			fi, err := e.Info()
			if err != nil {
				t.Fatal(err)
			}
			size += fi.Size()
		}
		if r.SizeBytes != size {
			t.Errorf("rung %s size_bytes = %d, on disk %d", r.Name, r.SizeBytes, size)
		}
	}
}
