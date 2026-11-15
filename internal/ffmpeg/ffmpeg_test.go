package ffmpeg

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vtnHAN-SOLO/hls-vod/internal/medialayout"
)

// fixtures holds short test videos generated once per run, in a temp dir
// outside the repo. Every test writes its own output under t.TempDir().
var fixtures struct {
	dir      string
	h264AAC  string // 1280x720 H.264 yuv420p + AAC, 7s  -> remuxable
	misnamed string // named "h264-aac.mp4" but MPEG-4 Part 2 + MP2, 640x360
	h264AC3  string // H.264 + AC-3 -> video fine, audio not
	noAudio  string // H.264 only -> remuxable
	coverArt string // MKV with a JPEG cover attachment (ffmpeg exposes it as an attached_pic video stream)
	notMedia string // a text file with a video extension

	hi10p      string // H.264 High 10 (yuv420p10le) + AAC -> must be encoded
	aac51      string // H.264 + 5.1 AAC -> video copied, audio downmixed
	multiAudio string // H.264 + 5.1 AC3 (not default) + stereo AAC (default)
	withSubs   string // H.264 + AAC + SRT (eng) + ASS (jpn, forced)
	long       string // 25s 320x180 H.264, for multi-tile thumbnails
}

const fixtureSeconds = 7 // > SegmentSeconds, so every rung has at least two segments

// TestMain fails the package outright when ffmpeg or ffprobe is missing.
// A skip would let `make check` pass without testing anything.
func TestMain(m *testing.M) {
	for _, bin := range []tool{ffmpegBin, ffprobeBin} {
		if _, err := exec.LookPath(bin.String()); err != nil {
			fmt.Fprintf(os.Stderr, "FAIL: %s not found in PATH; these tests need it.\n"+
				"Install with: sudo apt install ffmpeg\n", bin)
			os.Exit(1)
		}
	}

	dir, err := os.MkdirTemp("", "ffmpeg-fixtures-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: creating fixture dir: %v\n", err)
		os.Exit(1)
	}
	if err := makeFixtures(dir); err != nil {
		_ = os.RemoveAll(dir)
		fmt.Fprintf(os.Stderr, "FAIL: generating test videos: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func makeFixtures(dir string) error {
	f := &fixtures
	f.dir = dir
	f.h264AAC = filepath.Join(dir, "h264-aac.mkv")
	f.misnamed = filepath.Join(dir, "h264-aac.mp4")
	f.h264AC3 = filepath.Join(dir, "h264-ac3.mkv")
	f.noAudio = filepath.Join(dir, "no-audio.mkv")
	f.coverArt = filepath.Join(dir, "with-cover.mkv")
	f.notMedia = filepath.Join(dir, "not-media.mkv")
	f.hi10p = filepath.Join(dir, "hi10p.mkv")
	f.aac51 = filepath.Join(dir, "aac51.mkv")
	f.multiAudio = filepath.Join(dir, "multi-audio.mkv")
	f.withSubs = filepath.Join(dir, "with-subs.mkv")
	f.long = filepath.Join(dir, "long.mkv")

	dur := fmt.Sprint(fixtureSeconds)
	video := func(size string) []string {
		return []string{"-f", "lavfi", "-i", "testsrc2=size=" + size + ":rate=24:duration=" + dur}
	}
	audio := []string{"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=" + dur}
	x264 := []string{"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-g", "48"}

	gen := func(out string, parts ...[]string) error {
		args := []string{"-nostdin", "-hide_banner", "-loglevel", "error"}
		for _, p := range parts {
			args = append(args, p...)
		}
		args = append(args, "-y", out)
		if _, err := run(context.Background(), ffmpegBin, args...); err != nil {
			return fmt.Errorf("%s: %w", filepath.Base(out), err)
		}
		return nil
	}

	cover := filepath.Join(dir, "cover.jpg")
	srt := filepath.Join(dir, "subs.srt")
	srtBody := "1\n00:00:01,000 --> 00:00:03,000\nHello <i>world</i>\n\n" +
		"2\n00:00:04,000 --> 00:00:05,500\nSecond line\n"
	tone := func(hz string) []string {
		return []string{"-f", "lavfi", "-i", "sine=frequency=" + hz + ":sample_rate=48000:duration=" + dur}
	}
	steps := []func() error{
		func() error { return gen(f.h264AAC, video("1280x720"), audio, x264, []string{"-c:a", "aac"}) },
		func() error {
			return gen(f.misnamed, video("640x360"), audio, []string{"-c:v", "mpeg4", "-c:a", "mp2"})
		},
		func() error {
			return gen(f.h264AC3, video("640x360"), audio, x264, []string{"-c:a", "ac3"})
		},
		func() error { return gen(f.noAudio, video("640x360"), x264) },
		func() error {
			return gen(cover, []string{"-f", "lavfi", "-i", "color=c=red:size=300x300:duration=1", "-frames:v", "1"})
		},
		func() error {
			// Cover image mapped first, flagged attached_pic, then the real video.
			// How real MKVs carry cover art: an attachment, not a track.
			return gen(f.coverArt, []string{"-i", f.noAudio, "-map", "0", "-c", "copy",
				"-attach", cover, "-metadata:s:t:0", "mimetype=image/jpeg", "-metadata:s:t:0", "filename=cover.jpg"})
		},
		func() error { return os.WriteFile(f.notMedia, []byte("definitely not a video\n"), 0o600) },
		func() error {
			return gen(f.hi10p, video("640x360"), audio,
				[]string{"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p10le", "-g", "48", "-c:a", "aac"})
		},
		func() error {
			return gen(f.aac51, video("640x360"), audio, x264, []string{"-c:a", "aac", "-ac", "6"})
		},
		func() error {
			return gen(f.multiAudio, video("640x360"), tone("440"), tone("880"), x264,
				[]string{"-map", "0", "-map", "1", "-map", "2",
					"-c:a:0", "ac3", "-ac:a:0", "6", "-c:a:1", "aac", "-ac:a:1", "2",
					"-disposition:a:0", "0", "-disposition:a:1", "default"})
		},
		func() error { return os.WriteFile(srt, []byte(srtBody), 0o600) },
		func() error {
			return gen(f.withSubs, []string{"-i", f.h264AAC, "-i", srt, "-i", srt,
				"-map", "0", "-map", "1", "-map", "2", "-c:v", "copy", "-c:a", "copy",
				"-c:s:0", "srt", "-c:s:1", "ass",
				"-metadata:s:s:0", "language=eng", "-metadata:s:s:1", "language=jpn",
				"-disposition:s:1", "forced"})
		},
		func() error {
			return gen(f.long, []string{"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24:duration=25"}, x264)
		},
	}
	for _, step := range steps {
		if err := step(); err != nil {
			return err
		}
	}
	return nil
}

func TestProbe(t *testing.T) {
	tests := []struct {
		name  string
		path  string
		want  ProbeResult
		remux bool
	}{
		{
			name:  "h264 aac",
			path:  fixtures.h264AAC,
			want:  ProbeResult{VideoCodec: "h264", Width: 1280, Height: 720, PixFmt: "yuv420p", AudioCodec: "aac"},
			remux: true,
		},
		{
			// MP4 stores MP2 under the MPEG-audio object type, which demuxes as mp3.
			name: "misnamed mp4 is really mpeg4 + mpeg audio",
			path: fixtures.misnamed,
			want: ProbeResult{VideoCodec: "mpeg4", Width: 640, Height: 360, PixFmt: "yuv420p", AudioCodec: "mp3"},
		},
		{
			name: "h264 with ac3 audio",
			path: fixtures.h264AC3,
			want: ProbeResult{VideoCodec: "h264", Width: 640, Height: 360, PixFmt: "yuv420p", AudioCodec: "ac3"},
		},
		{
			name:  "no audio",
			path:  fixtures.noAudio,
			want:  ProbeResult{VideoCodec: "h264", Width: 640, Height: 360, PixFmt: "yuv420p"},
			remux: true,
		},
		{
			name:  "cover art attachment is ignored",
			path:  fixtures.coverArt,
			want:  ProbeResult{VideoCodec: "h264", Width: 640, Height: 360, PixFmt: "yuv420p"},
			remux: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Probe(t.Context(), tt.path)
			if err != nil {
				t.Fatal(err)
			}
			if got.VideoCodec != tt.want.VideoCodec || got.Width != tt.want.Width ||
				got.Height != tt.want.Height || got.PixFmt != tt.want.PixFmt ||
				got.AudioCodec != tt.want.AudioCodec {
				t.Fatalf("Probe = %+v, want %+v", got, tt.want)
			}
			if d := got.Duration - fixtureSeconds*time.Second; d < -200*time.Millisecond || d > 200*time.Millisecond {
				t.Errorf("Duration = %v, want ~%ds", got.Duration, fixtureSeconds)
			}
			if got.CanRemux() != tt.remux {
				t.Errorf("CanRemux = %v, want %v", got.CanRemux(), tt.remux)
			}
		})
	}
}

func TestProbeErrors(t *testing.T) {
	tests := []struct {
		name  string
		path  string
		check func(error) bool
	}{
		{"missing file", filepath.Join(t.TempDir(), "nope.mkv"), func(err error) bool { return err != nil }},
		{"not media", fixtures.notMedia, func(err error) bool { return err != nil }},
		{"audio only", writeAudioOnly(t), func(err error) bool { return errors.Is(err, ErrNoVideo) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Probe(t.Context(), tt.path)
			if !tt.check(err) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// A file whose name looks like an ffmpeg option must still be read as a file.
func TestProbeDashLeadingFilename(t *testing.T) {
	dir := t.TempDir()
	copyFile(t, fixtures.noAudio, filepath.Join(dir, "-i.mkv"))
	t.Chdir(dir)

	got, err := Probe(t.Context(), "-i.mkv")
	if err != nil {
		t.Fatalf("probing a dash-leading relative path: %v", err)
	}
	if got.Height != 360 {
		t.Fatalf("Height = %d, want 360", got.Height)
	}
}

// The remux decision comes from the bytes, never the name.
func TestRemuxDecisionIgnoresExtension(t *testing.T) {
	dir := t.TempDir()
	disguised := filepath.Join(dir, "movie.avi") // really H.264/AAC in Matroska
	copyFile(t, fixtures.h264AAC, disguised)

	tests := []struct {
		path string
		want bool
	}{
		{fixtures.misnamed, false}, // "h264-aac.mp4", but MPEG-4 Part 2
		{disguised, true},
	}
	for _, tt := range tests {
		t.Run(filepath.Base(tt.path), func(t *testing.T) {
			p, err := Probe(t.Context(), tt.path)
			if err != nil {
				t.Fatal(err)
			}
			if p.CanRemux() != tt.want {
				t.Fatalf("CanRemux = %v, want %v (probe: %+v)", p.CanRemux(), tt.want, p)
			}
		})
	}
}

func TestRemux(t *testing.T) {
	out := filepath.Join(t.TempDir(), "720p")
	start := time.Now()
	if err := Remux(t.Context(), fixtures.h264AAC, out, nil); err != nil {
		t.Fatal(err)
	}
	t.Logf("remux took %v", time.Since(start))

	got, err := Probe(t.Context(), filepath.Join(out, PlaylistName))
	if err != nil {
		t.Fatal(err)
	}
	// -c copy: identical codec, resolution and audio.
	if got.VideoCodec != "h264" || got.Height != 720 || got.Width != 1280 || got.AudioCodec != "aac" {
		t.Fatalf("remuxed rung = %+v", got)
	}
	if n := countSegments(t, out); n < 2 {
		t.Fatalf("got %d segments, want >= 2", n)
	}
}

func TestRemuxIgnoresCoverArt(t *testing.T) {
	out := filepath.Join(t.TempDir(), "360p")
	if err := Remux(t.Context(), fixtures.coverArt, out, nil); err != nil {
		t.Fatal(err)
	}
	got, err := Probe(t.Context(), filepath.Join(out, PlaylistName))
	if err != nil {
		t.Fatal(err)
	}
	if got.Width != 640 || got.Height != 360 {
		t.Fatalf("rung is %dx%d, want the 640x360 movie, not the cover", got.Width, got.Height)
	}
}

func TestRemuxRefusesNonRemuxableSource(t *testing.T) {
	for _, src := range []string{fixtures.misnamed, fixtures.h264AC3} {
		t.Run(filepath.Base(src), func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "rung")
			if err := Remux(t.Context(), src, out, nil); err == nil {
				t.Fatal("expected an error")
			}
			assertNotExist(t, out)
		})
	}
}

func TestEncode(t *testing.T) {
	// Source is MPEG-4 Part 2 + MP2: encoding must handle what remux cannot.
	out := filepath.Join(t.TempDir(), "240p")
	if err := Encode(t.Context(), fixtures.misnamed, out, 240, 400, nil); err != nil {
		t.Fatal(err)
	}
	got, err := Probe(t.Context(), filepath.Join(out, PlaylistName))
	if err != nil {
		t.Fatal(err)
	}
	want := ProbeResult{VideoCodec: "h264", Width: 426, Height: 240, PixFmt: "yuv420p", Profile: "High", AudioCodec: "aac"}
	if got.VideoCodec != want.VideoCodec || got.Width != want.Width || got.Height != want.Height ||
		got.PixFmt != want.PixFmt || got.Profile != want.Profile || got.AudioCodec != want.AudioCodec {
		t.Fatalf("encoded rung = %+v, want %+v", got, want)
	}

	// Forced keyframes: every full segment is exactly SegmentSeconds long.
	durs := segmentDurations(t, out)
	if len(durs) < 2 {
		t.Fatalf("got %d segments, want >= 2", len(durs))
	}
	for i, d := range durs[:len(durs)-1] {
		if d < SegmentSeconds-0.05 || d > SegmentSeconds+0.05 {
			t.Errorf("segment %d is %.3fs, want %ds (keyframes not aligned)", i, d, SegmentSeconds)
		}
	}
}

func TestEncodeRejects(t *testing.T) {
	tests := []struct {
		name    string
		height  int
		bitrate int
		wantErr error // nil: any error
	}{
		{"upscale 360p source to 480p", 480, 1400, ErrUpscale},
		{"upscale to 1080p", 1080, 5000, ErrUpscale},
		{"odd height", 241, 400, nil},
		{"zero height", 0, 400, nil},
		{"zero bitrate", 240, 0, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "rung")
			err := Encode(t.Context(), fixtures.noAudio, out, tt.height, tt.bitrate, nil)
			if err == nil {
				t.Fatal("expected an error")
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
			assertNotExist(t, out)
		})
	}
}

// An existing rung dir is never reused or modified.
func TestRungDirMustNotExist(t *testing.T) {
	out := filepath.Join(t.TempDir(), "720p")
	if err := os.Mkdir(out, 0o750); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(out, "keep.txt")
	if err := os.WriteFile(keep, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Remux(t.Context(), fixtures.h264AAC, out, nil); err == nil {
		t.Fatal("expected an error for an existing dir")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("existing content was touched: %v", err)
	}
}

// Full pipeline: probe -> plan -> remux/encode -> describe -> master, then
// ffprobe must be able to open the master and play through to a rung.
func TestLadderEndToEnd(t *testing.T) {
	ctx := t.Context()
	movie := t.TempDir()

	src, err := Probe(ctx, fixtures.h264AAC)
	if err != nil {
		t.Fatal(err)
	}
	rungs, err := PlanLadder(src)
	if err != nil {
		t.Fatal(err)
	}
	if got := rungNames(rungs); got != "720p(remux),480p" {
		t.Fatalf("ladder = %s, want 720p(remux),480p", got)
	}

	var variants []Variant
	for _, r := range rungs {
		dir := filepath.Join(movie, r.Name)
		if err := WriteRung(ctx, fixtures.h264AAC, dir, r, nil); err != nil {
			t.Fatalf("rung %s: %v", r.Name, err)
		}
		v, err := DescribeRung(ctx, dir, r.Name+"/"+PlaylistName)
		if err != nil {
			t.Fatal(err)
		}
		if v.Height > src.Height {
			t.Fatalf("rung %s is %dp, above the %dp source", r.Name, v.Height, src.Height)
		}
		if v.Bandwidth < v.AverageBandwidth || v.AverageBandwidth <= 0 {
			t.Fatalf("rung %s bandwidth peak=%d avg=%d", r.Name, v.Bandwidth, v.AverageBandwidth)
		}
		if !strings.HasPrefix(v.Codecs, "avc1.") || !strings.HasSuffix(v.Codecs, ",mp4a.40.2") {
			t.Fatalf("rung %s CODECS = %q", r.Name, v.Codecs)
		}
		variants = append(variants, v)
	}

	master := filepath.Join(movie, medialayout.MasterPlaylist)
	if err := WriteMasterPlaylist(master, variants); err != nil {
		t.Fatal(err)
	}
	body := readFile(t, master)
	for _, want := range []string{"RESOLUTION=1280x720", "RESOLUTION=854x480", "720p/index.m3u8", "480p/index.m3u8"} {
		if !strings.Contains(body, want) {
			t.Errorf("master missing %q:\n%s", want, body)
		}
	}
	// ffprobe resolves the master, picks a variant and reads its segments.
	if _, err := Probe(ctx, master); err != nil {
		t.Fatalf("ffprobe cannot open the master playlist: %v\n%s", err, body)
	}
}

// --- helpers ---

func writeAudioOnly(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "audio-only.mkv")
	_, err := run(t.Context(), ffmpegBin, "-nostdin", "-loglevel", "error",
		"-f", "lavfi", "-i", "sine=duration=1", "-c:a", "aac", "-y", out)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = in.Close() }()
	out, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}

func countSegments(t *testing.T, dir string) int {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(dir, "seg_*.ts"))
	if err != nil {
		t.Fatal(err)
	}
	return len(m)
}

func segmentDurations(t *testing.T, dir string) []float64 {
	t.Helper()
	var out []float64
	for _, line := range strings.Split(readFile(t, filepath.Join(dir, PlaylistName)), "\n") {
		if v, ok := strings.CutPrefix(line, "#EXTINF:"); ok {
			var d float64
			if _, err := fmt.Sscanf(strings.TrimSuffix(v, ","), "%g", &d); err != nil {
				t.Fatalf("parsing %q: %v", line, err)
			}
			out = append(out, d)
		}
	}
	return out
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func assertNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s should not exist after a failed call (stat err: %v)", path, err)
	}
}

func rungNames(rungs []Rung) string {
	parts := make([]string, len(rungs))
	for i, r := range rungs {
		parts[i] = r.Name
		if r.Mode != ModeEncode {
			parts[i] += "(" + r.Mode.String() + ")"
		}
	}
	return strings.Join(parts, ",")
}

// --- messy real-world sources ---

// H.264 with AC3 audio: the video is copied bit for bit, only audio encoded.
func TestCopyVideoEncodesAudioOnly(t *testing.T) {
	tests := []struct {
		name string
		src  string
	}{
		{"ac3", fixtures.h264AC3},
		{"5.1 aac is downmixed too", fixtures.aac51},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in, err := Probe(t.Context(), tt.src)
			if err != nil {
				t.Fatal(err)
			}
			rungs, err := PlanLadder(in)
			if err != nil {
				t.Fatal(err)
			}
			if rungs[0].Mode != ModeCopyVideo {
				t.Fatalf("top rung mode = %s, want copy-video (probe: %+v)", rungs[0].Mode, in)
			}

			out := filepath.Join(t.TempDir(), "360p")
			if err := WriteRung(t.Context(), tt.src, out, rungs[0], nil); err != nil {
				t.Fatal(err)
			}
			got, err := Probe(t.Context(), filepath.Join(out, PlaylistName))
			if err != nil {
				t.Fatal(err)
			}
			// Copied video keeps the source's exact profile and level
			// (ultrafast writes Constrained Baseline; an encode writes High).
			if got.VideoCodec != "h264" || got.Profile != in.Profile || got.Level != in.Level || got.Height != in.Height {
				t.Errorf("video was re-encoded: got %s %s L%d %dp, source %s L%d %dp",
					got.VideoCodec, got.Profile, got.Level, got.Height, in.Profile, in.Level, in.Height)
			}
			if got.AudioCodec != "aac" || got.AudioChannels != 2 {
				t.Errorf("audio = %s %dch, want aac stereo", got.AudioCodec, got.AudioChannels)
			}
		})
	}
}

// The default-flagged audio track is used, not the first one.
func TestDefaultAudioTrackIsUsed(t *testing.T) {
	p, err := Probe(t.Context(), fixtures.multiAudio)
	if err != nil {
		t.Fatal(err)
	}
	if p.AudioCodec != "aac" || p.AudioChannels != 2 || p.AudioIndex != 2 {
		t.Fatalf("chose audio stream %d (%s %dch), want stream 2 (aac stereo)", p.AudioIndex, p.AudioCodec, p.AudioChannels)
	}
	// With the default AAC chosen, the whole file is remuxable.
	out := filepath.Join(t.TempDir(), "360p")
	if err := Remux(t.Context(), fixtures.multiAudio, out, nil); err != nil {
		t.Fatal(err)
	}
	got, err := Probe(t.Context(), filepath.Join(out, PlaylistName))
	if err != nil {
		t.Fatal(err)
	}
	if got.AudioCodec != "aac" || got.AudioChannels != 2 {
		t.Fatalf("rung audio = %s %dch, want the default aac stereo track", got.AudioCodec, got.AudioChannels)
	}
}

// 10-bit H.264 (Hi10P) is valid H.264 that browsers cannot decode.
func TestHi10PIsEncoded(t *testing.T) {
	p, err := Probe(t.Context(), fixtures.hi10p)
	if err != nil {
		t.Fatal(err)
	}
	if p.VideoCodec != "h264" || p.PixFmt != "yuv420p10le" || p.Profile != "High 10" {
		t.Fatalf("fixture probe = %+v, want h264 High 10 yuv420p10le", p)
	}
	if p.VideoCopyable() || p.CanRemux() {
		t.Fatal("10-bit H.264 must not be copied")
	}
	rungs, err := PlanLadder(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := rungNames(rungs); got != "360p" {
		t.Fatalf("ladder = %s, want 360p (encoded)", got)
	}

	if err := CopyVideo(t.Context(), fixtures.hi10p, filepath.Join(t.TempDir(), "x"), nil); err == nil {
		t.Error("CopyVideo accepted a 10-bit source")
	}
	out := filepath.Join(t.TempDir(), "360p")
	if err := WriteRung(t.Context(), fixtures.hi10p, out, rungs[0], nil); err != nil {
		t.Fatal(err)
	}
	got, err := Probe(t.Context(), filepath.Join(out, PlaylistName))
	if err != nil {
		t.Fatal(err)
	}
	if got.PixFmt != "yuv420p" || got.Profile != "High" || !got.CanRemux() {
		t.Fatalf("encoded rung = %s %s, want 8-bit High", got.Profile, got.PixFmt)
	}
}

func TestExtractSubtitles(t *testing.T) {
	src, err := Probe(t.Context(), fixtures.withSubs)
	if err != nil {
		t.Fatal(err)
	}
	// ffmpeg has no bitmap-subtitle encoder, so a PGS track cannot be
	// generated; add a probed-looking one to prove it is skipped, not fed
	// to ffmpeg (stream 99 does not exist and would fail the run).
	src.Subtitles = append(src.Subtitles, SubtitleStream{Index: 99, Codec: "hdmv_pgs_subtitle", Language: "eng"})

	movie := t.TempDir()
	res, err := ExtractSubtitles(t.Context(), fixtures.withSubs, movie, src)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Written) != 2 || res.Written[0].Path != "subs/0_eng.vtt" || res.Written[1].Path != "subs/1_jpn_forced.vtt" {
		t.Fatalf("written = %+v, want subs/0_eng.vtt and subs/1_jpn_forced.vtt", res.Written)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].Codec != "hdmv_pgs_subtitle" || res.Skipped[0].Reason == "" {
		t.Fatalf("skipped = %+v, want the PGS track with a reason", res.Skipped)
	}
	for _, w := range res.Written {
		body := readFile(t, filepath.Join(movie, w.Path))
		if !strings.HasPrefix(body, "WEBVTT") || !strings.Contains(body, "00:01.000 --> 00:03.000") ||
			!strings.Contains(body, "Second line") {
			t.Errorf("%s is not the converted subtitle:\n%s", w.Path, body)
		}
	}

	// A file without text subtitles creates no subs/ dir at all.
	empty := t.TempDir()
	if _, err := ExtractSubtitles(t.Context(), fixtures.h264AAC, empty, ProbeResult{}); err != nil {
		t.Fatal(err)
	}
	assertNotExist(t, filepath.Join(empty, SubtitlesDir))
}

func TestExtractPoster(t *testing.T) {
	tests := []struct {
		name      string
		src       string
		maxHeight int
		w, h      int
	}{
		{"scaled down", fixtures.h264AAC, 360, 640, 360},
		{"never upscaled", fixtures.noAudio, 720, 640, 360},
		{"from 10-bit", fixtures.hi10p, 720, 640, 360},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src, err := Probe(t.Context(), tt.src)
			if err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(t.TempDir(), PosterName)
			if err := ExtractPoster(t.Context(), tt.src, out, src, time.Second, tt.maxHeight); err != nil {
				t.Fatal(err)
			}
			w, h := imageSize(t, out)
			if w != tt.w || h != tt.h {
				t.Fatalf("poster is %dx%d, want %dx%d", w, h, tt.w, tt.h)
			}
		})
	}

	// Past the end: no frame, a clear error, and no empty file left behind.
	src, err := Probe(t.Context(), fixtures.noAudio)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), PosterName)
	if err := ExtractPoster(t.Context(), fixtures.noAudio, out, src, time.Hour, 360); err == nil {
		t.Fatal("expected an error seeking past the end")
	}
	assertNotExist(t, out)
}

func TestGenerateThumbnails(t *testing.T) {
	src, err := Probe(t.Context(), fixtures.long)
	if err != nil {
		t.Fatal(err)
	}
	// 25s at 10s each = 3 thumbnails; a 2x1 grid forces two sheets.
	opt := ThumbnailOptions{Interval: 10 * time.Second, Cols: 2, Rows: 1, TileHeight: 90}
	movie := t.TempDir()
	res, err := GenerateThumbnails(t.Context(), fixtures.long, movie, src, 25*time.Second, opt, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := ThumbnailResult{Count: 3, Sheets: 2, TileWidth: 160, TileHeight: 90}
	if res != want {
		t.Fatalf("result = %+v, want %+v", res, want)
	}
	sheets, err := filepath.Glob(filepath.Join(movie, ThumbnailsDir, "sprite_*.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	if len(sheets) != 2 {
		t.Fatalf("sheets on disk = %v, want 2", sheets)
	}
	for _, s := range sheets {
		if w, h := imageSize(t, s); w != 320 || h != 90 {
			t.Errorf("%s is %dx%d, want 320x90 (2x1 tiles of 160x90)", filepath.Base(s), w, h)
		}
	}
	vtt := readFile(t, filepath.Join(movie, medialayout.ThumbnailsVTT))
	if vtt != thumbnailsVTT(res, opt, 25*time.Second) || strings.Count(vtt, "#xywh=") != 3 {
		t.Fatalf("thumbnails.vtt:\n%s", vtt)
	}

	// Default grid: one 10x10 sheet holds all three.
	movie = t.TempDir()
	res, err = GenerateThumbnails(t.Context(), fixtures.long, movie, src, 25*time.Second, DefaultThumbnails, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Sheets != 1 || res.Count != 3 {
		t.Fatalf("default grid result = %+v", res)
	}
	if w, h := imageSize(t, filepath.Join(movie, ThumbnailsDir, "sprite_000.jpg")); w != 1600 || h != 900 {
		t.Fatalf("default sheet is %dx%d, want 1600x900", w, h)
	}
}

func TestPlaylistDurationAndProgress(t *testing.T) {
	out := filepath.Join(t.TempDir(), "240p")
	var last time.Duration
	calls := 0
	progress := func(d time.Duration) { calls++; last = d }
	if err := Encode(t.Context(), fixtures.noAudio, out, 240, 400, progress); err != nil {
		t.Fatal(err)
	}
	if calls == 0 || last < (fixtureSeconds-1)*time.Second {
		t.Fatalf("progress called %d times, last = %v; want the final report near %ds", calls, last, fixtureSeconds)
	}
	d, err := PlaylistDuration(out)
	if err != nil {
		t.Fatal(err)
	}
	if diff := d - fixtureSeconds*time.Second; diff < -100*time.Millisecond || diff > 100*time.Millisecond {
		t.Fatalf("PlaylistDuration = %v, want ~%ds", d, fixtureSeconds)
	}
}

func imageSize(t *testing.T, path string) (w, h int) {
	t.Helper()
	out, err := run(t.Context(), ffprobeBin, "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=width,height", "-of", "csv=p=0:s=x", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Sscanf(strings.TrimSpace(string(out)), "%dx%d", &w, &h); err != nil {
		t.Fatalf("parsing size of %s: %q", path, out)
	}
	return w, h
}

// A text track ffmpeg cannot convert is skipped with its error; the good
// tracks around it are still written, and no error is returned.
func TestExtractSubtitlesSkipsBrokenTrack(t *testing.T) {
	src, err := Probe(t.Context(), fixtures.withSubs)
	if err != nil {
		t.Fatal(err)
	}
	// Stream 99 does not exist: ffmpeg fails the combined run on it, the
	// way it would on a corrupt track.
	broken := SubtitleStream{Index: 99, Codec: "subrip", Language: "fre"}
	src.Subtitles = []SubtitleStream{src.Subtitles[0], broken, src.Subtitles[1]}

	movie := t.TempDir()
	res, err := ExtractSubtitles(t.Context(), fixtures.withSubs, movie, src)
	if err != nil {
		t.Fatalf("a broken track must not fail extraction: %v", err)
	}
	var paths []string
	for _, w := range res.Written {
		paths = append(paths, w.Path)
		if body := readFile(t, filepath.Join(movie, w.Path)); !strings.Contains(body, "Second line") {
			t.Errorf("%s is incomplete:\n%s", w.Path, body)
		}
	}
	if got := strings.Join(paths, ","); got != "subs/0_eng.vtt,subs/2_jpn_forced.vtt" {
		t.Fatalf("written = %s, want the two good tracks", got)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].Index != 99 || res.Skipped[0].Err == nil {
		t.Fatalf("skipped = %+v, want stream 99 with its ffmpeg error", res.Skipped)
	}
	assertNotExist(t, filepath.Join(movie, "subs/1_fre.vtt"))

	// Only broken tracks: nothing written, no empty subs/ dir, still no error.
	only := t.TempDir()
	res, err = ExtractSubtitles(t.Context(), fixtures.withSubs, only, ProbeResult{Subtitles: []SubtitleStream{broken}})
	if err != nil || len(res.Written) != 0 || len(res.Skipped) != 1 {
		t.Fatalf("res = %+v, err = %v; want one skipped track and no error", res, err)
	}
	assertNotExist(t, filepath.Join(only, SubtitlesDir))

	// Cancellation is still an error, not a skipped track.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := ExtractSubtitles(ctx, fixtures.withSubs, t.TempDir(), src); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: err = %v, want context.Canceled", err)
	}
}
