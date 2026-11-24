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
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/vtnHAN-SOLO/hls-vod/internal/ffmpeg"
	"github.com/vtnHAN-SOLO/hls-vod/internal/medialayout"
)

// movie is a generated "messy" source: 1080p H.264 with 5.1 AC3 audio and an
// English SRT track, 14s. Expected: 1080p copy-video + 720p encode.
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
		"-f", "lavfi", "-i", "testsrc2=size=1920x1080:rate=24:duration="+dur,
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
	if want := []Subtitle{{Path: "subs/0_eng.vtt", Language: "eng", Position: 0}}; !slices.Equal(res.Subtitles, want) {
		t.Errorf("subtitles = %+v, want %+v", res.Subtitles, want)
	}

	for _, f := range []string{
		medialayout.MasterPlaylist,
		"1080p/" + ffmpeg.PlaylistName, "1080p/seg_0000.ts",
		"720p/" + ffmpeg.PlaylistName, "720p/seg_0000.ts",
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
	for _, want := range []string{"RESOLUTION=1920x1080", "RESOLUTION=1280x720"} {
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
			if got := fmt.Sprint(l.Rungs); got != "[1080p:copy-video 720p:encode]" {
				t.Errorf("planned ladder = %s, want [1080p:copy-video 720p:encode]", got)
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
	res, err := Run(t.Context(), jsonLogger(&logs), opt)
	if err != nil {
		t.Fatalf("a broken subtitle failed the movie: %v", err)
	}
	// Only the track that was written is recorded.
	if len(res.Subtitles) != 1 || res.Subtitles[0].Path != "subs/0_eng.vtt" {
		t.Errorf("subtitles = %+v, want only subs/0_eng.vtt", res.Subtitles)
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

	// Cancel on the first progress report of the 720p encode: ffmpeg is
	// running and the 1080p rung is already on disk in the temp dir.
	trip := &tripWriter{match: [][]byte{[]byte(`"msg":"progress"`), []byte(`"rung":"720p"`)}, fire: cancel}
	_, err := Run(ctx, jsonLogger(trip), testOptions(filepath.Join(parent, "movie")))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if !trip.fired() {
		t.Fatal("never reached the 720p encode")
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
		t.Fatalf("rungs = %+v, want 1080p and 720p", res.Rungs)
	}
	for i, want := range []struct {
		name   string
		height int
	}{{"1080p", 1080}, {"720p", 720}} {
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

// MPEG-TS sources (.ts, .m2ts): TV and camera recordings. Each fixture is
// generated here, into t.TempDir(), never committed.
func TestTranscodeMPEGTS(t *testing.T) {
	const seconds = 7 // > one segment
	dur := fmt.Sprint(seconds)
	video := func(size string) []string {
		return []string{"-f", "lavfi", "-i", "testsrc2=size=" + size + ":rate=25:duration=" + dur}
	}
	tone := func(hz string) []string {
		return []string{"-f", "lavfi", "-i", "sine=frequency=" + hz + ":sample_rate=48000:duration=" + dur}
	}
	h264AAC := []string{"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-g", "50", "-c:a", "aac"}
	join := func(parts ...[]string) []string { return slices.Concat(parts...) }

	tests := []struct {
		name, file string
		inputs     []string // ffmpeg inputs and options, before the output
		ladder     string   // the planned ladder
		likeMP4    bool     // an .mp4 with the same streams plans the same ladder
	}{
		{
			name: "H.264 + AAC in .ts is remuxed", file: "movie.ts",
			inputs: join(video("1920x1080"), tone("440"), h264AAC),
			ladder: "[1080p:remux 720p:encode]", likeMP4: true,
		},
		{
			name: "MPEG-2 + MP2 in .ts (a TV recording) is encoded", file: "tv.ts",
			inputs: join(video("1920x1080"), tone("440"), []string{"-c:v", "mpeg2video", "-b:v", "8M", "-c:a", "mp2", "-b:a", "192k"}),
			ladder: "[1080p:encode 720p:encode]",
		},
		{
			// Starts at 10 s (11.4 s with the muxer's delay), a second audio
			// track and a data stream: only the video and one audio reach HLS.
			name: "offset start, second audio track and a data stream", file: "recording.ts",
			inputs: join(video("1920x1080"), tone("440"), tone("880"), []string{"-f", "data", "-i", "DATA"},
				[]string{"-map", "0", "-map", "1", "-map", "2", "-map", "3"}, h264AAC,
				[]string{"-c:d", "copy", "-output_ts_offset", "10"}),
			ladder: "[1080p:remux 720p:encode]",
		},
		{
			name: ".m2ts is probed and remuxed", file: "00001.m2ts",
			inputs: join(video("1280x720"), tone("440"), h264AAC),
			ladder: "[720p:remux]", likeMP4: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			data := filepath.Join(dir, "data.bin")
			if err := os.WriteFile(data, bytes.Repeat([]byte("klv\x00"), 1024), 0o600); err != nil {
				t.Fatal(err)
			}
			inputs := slices.Clone(tt.inputs)
			for i, a := range inputs {
				if a == "DATA" {
					inputs[i] = data
				}
			}
			src := filepath.Join(dir, tt.file)
			genMedia(t, src, inputs)
			if tt.likeMP4 {
				mp4 := filepath.Join(dir, "same.mp4")
				genMedia(t, mp4, inputs)
				if a, b := planOf(t, src), planOf(t, mp4); a != b {
					t.Fatalf("ladder for %s = %s, for the same streams in .mp4 = %s", tt.file, a, b)
				}
			}

			output := filepath.Join(dir, "movie")
			opt := testOptions(output)
			opt.Input = src
			var logs syncBuffer
			res, err := Run(t.Context(), jsonLogger(&logs), opt)
			if err != nil {
				t.Fatalf("transcode: %v\nlogs:\n%s", err, logs.String())
			}
			for _, l := range parseLogs(t, logs.String()) {
				if l.Msg == "planned ladder" {
					if got := fmt.Sprint(l.Rungs); got != tt.ladder {
						t.Errorf("planned ladder = %s, want %s", got, tt.ladder)
					}
				}
			}
			if d := res.Duration.Seconds(); d < seconds-1 || d > seconds+1 {
				t.Errorf("duration = %v, want %d s within 1 s", res.Duration, seconds)
			}
			for _, r := range res.Rungs {
				segs, err := filepath.Glob(filepath.Join(output, r.Name, "seg_*.ts"))
				if err != nil || len(segs) < 2 {
					t.Fatalf("rung %s segments %v: %v", r.Name, segs, err)
				}
				for i, seg := range []string{segs[0], segs[len(segs)-1]} {
					streams, start := probeSegment(t, seg)
					if streams != "audio,video" {
						t.Errorf("%s/%s streams = %s, want exactly one video and one audio", r.Name, filepath.Base(seg), streams)
					}
					// The MPEG-TS muxer starts every output at 1.4 s, whatever
					// the source's own start: never 11.4.
					if i == 0 && start > 2 {
						t.Errorf("%s/%s starts at %.3f s, want near 0", r.Name, filepath.Base(seg), start)
					}
				}
			}
			for _, f := range []string{ffmpeg.PosterName, medialayout.ThumbnailsVTT, "thumbs/sprite_000.jpg"} {
				if fi, err := os.Stat(filepath.Join(output, f)); err != nil || fi.Size() == 0 {
					t.Errorf("%s: %v", f, err)
				}
			}
			// It plays: every rung decodes from the master playlist to the end.
			decodeAll(t, filepath.Join(output, medialayout.MasterPlaylist))
		})
	}
}

func genMedia(t *testing.T, out string, inputs []string) {
	t.Helper()
	args := slices.Concat([]string{"-nostdin", "-hide_banner", "-loglevel", "error"}, inputs, []string{"-y", out})
	// #nosec G204 -- fixed ffmpeg arguments built by the test; paths under t.TempDir().
	if b, err := exec.CommandContext(t.Context(), "ffmpeg", args...).CombinedOutput(); err != nil {
		t.Fatalf("generating %s: %v: %s", filepath.Base(out), err, b)
	}
}

func planOf(t *testing.T, path string) string {
	t.Helper()
	p, err := ffmpeg.Probe(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	rungs, err := ffmpeg.PlanLadder(p)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, len(rungs))
	for i, r := range rungs {
		out[i] = r.Name + ":" + r.Mode.String()
	}
	return fmt.Sprint(out)
}

// probeSegment returns a segment's stream types, sorted and comma-joined,
// and its start time in seconds.
func probeSegment(t *testing.T, seg string) (string, float64) {
	t.Helper()
	// #nosec G204 -- fixed ffprobe arguments; the path is under t.TempDir().
	b, err := exec.CommandContext(t.Context(), "ffprobe", "-v", "error", "-of", "json",
		"-show_entries", "stream=codec_type:format=start_time", seg).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", seg, err)
	}
	var j struct {
		Streams []struct {
			CodecType string `json:"codec_type"`
		} `json:"streams"`
		Format struct {
			StartTime string `json:"start_time"`
		} `json:"format"`
	}
	if err := json.Unmarshal(b, &j); err != nil {
		t.Fatal(err)
	}
	types := make([]string, len(j.Streams))
	for i, s := range j.Streams {
		types[i] = s.CodecType
	}
	slices.Sort(types)
	var start float64
	if _, err := fmt.Sscan(j.Format.StartTime, &start); err != nil {
		t.Fatalf("start_time %q of %s: %v", j.Format.StartTime, seg, err)
	}
	return strings.Join(types, ","), start
}

// decodeAll decodes every variant of an HLS master playlist to the end.
func decodeAll(t *testing.T, master string) {
	t.Helper()
	// #nosec G204 -- fixed ffmpeg arguments; the path is under t.TempDir().
	cmd := exec.CommandContext(t.Context(), "ffmpeg", "-nostdin", "-v", "error", "-xerror",
		"-i", master, "-map", "0", "-f", "null", "-")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("decoding %s: %v: %s", master, err, b)
	}
}
