package ffmpeg

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/vtnHAN-SOLO/hls-vod/internal/medialayout"
)

func TestWriteMasterPlaylist(t *testing.T) {
	path := filepath.Join(t.TempDir(), medialayout.MasterPlaylist)
	variants := []Variant{
		{URI: "720p/index.m3u8", Width: 1280, Height: 720, Bandwidth: 3_200_000, AverageBandwidth: 2_800_000, Codecs: "avc1.64001f,mp4a.40.2"},
		{URI: "480p/index.m3u8", Width: 854, Height: 480, Bandwidth: 1_500_000, AverageBandwidth: 1_300_000, Codecs: "avc1.64001e,mp4a.40.2"},
		{URI: "360p/index.m3u8", Width: 640, Height: 360, Bandwidth: 800_000},
	}
	if err := WriteMasterPlaylist(path, variants); err != nil {
		t.Fatal(err)
	}

	want := `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=640x360
360p/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=1500000,AVERAGE-BANDWIDTH=1300000,RESOLUTION=854x480,CODECS="avc1.64001e,mp4a.40.2"
480p/index.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=3200000,AVERAGE-BANDWIDTH=2800000,RESOLUTION=1280x720,CODECS="avc1.64001f,mp4a.40.2"
720p/index.m3u8
`
	if got := readFile(t, path); got != want {
		t.Fatalf("master playlist:\n%s\nwant:\n%s", got, want)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v, want 0644 (nginx reads it under another UID)", fi.Mode().Perm())
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("temp files left behind: %v", entries)
	}
}

func TestWriteMasterPlaylistRejects(t *testing.T) {
	good := Variant{URI: "720p/index.m3u8", Width: 1280, Height: 720, Bandwidth: 1}
	tests := []struct {
		name string
		v    []Variant
	}{
		{"no variants", nil},
		{"parent traversal", []Variant{withURI(good, "../other/index.m3u8")}},
		{"nested traversal", []Variant{withURI(good, "720p/../../x.m3u8")}},
		{"absolute path", []Variant{withURI(good, "/etc/index.m3u8")}},
		{"url", []Variant{withURI(good, "http://evil.example/index.m3u8")}},
		{"newline injection", []Variant{withURI(good, "720p/index.m3u8\n#EXT-X-ENDLIST")}},
		{"not a playlist", []Variant{withURI(good, "720p/seg_0000.ts")}},
		{"unclean path", []Variant{withURI(good, "./720p/index.m3u8")}},
		{"url-encoded traversal", []Variant{withURI(good, "%2e%2e%2fother/index.m3u8")}},
		{"null byte", []Variant{withURI(good, "720p/index.m3u8\x00")}},
		{"empty uri", []Variant{withURI(good, "")}},
		{"zero bandwidth", []Variant{{URI: "a/index.m3u8", Width: 1, Height: 1}}},
		{"quote in codecs", []Variant{{URI: "a/index.m3u8", Width: 1, Height: 1, Bandwidth: 1, Codecs: `avc1"x`}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), medialayout.MasterPlaylist)
			if err := WriteMasterPlaylist(path, tt.v); err == nil {
				t.Fatalf("expected an error, wrote:\n%s", readFile(t, path))
			}
			if _, err := os.Stat(path); err == nil {
				t.Fatal("master written despite the error")
			}
		})
	}
}

func withURI(v Variant, uri string) Variant { v.URI = uri; return v }

func TestMeasureBandwidth(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, size int) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), make([]byte, size), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// 750 kB over 6s = 1 Mbit/s; 750 kB over 3s = 2 Mbit/s.
	write("seg_0000.ts", 750_000)
	write("seg_0001.ts", 750_000)
	playlist := filepath.Join(dir, PlaylistName)
	if err := os.WriteFile(playlist, []byte(
		"#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXTINF:6.000000,\nseg_0000.ts\n#EXTINF:3.000000,\nseg_0001.ts\n#EXT-X-ENDLIST\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}

	peak, avg, err := measureBandwidth(dir)
	if err != nil {
		t.Fatal(err)
	}
	if peak != 2_000_000 {
		t.Errorf("peak = %d, want 2000000", peak)
	}
	if avg != 1_333_333 {
		t.Errorf("avg = %d, want 1333333", avg)
	}
}

func TestMeasureBandwidthRejects(t *testing.T) {
	tests := []struct{ name, body string }{
		{"segment outside dir", "#EXTINF:6.0,\n../secret.ts\n"},
		{"absolute segment path", "#EXTINF:6.0,\n/etc/passwd\n"},
		{"url-encoded traversal", "#EXTINF:6.0,\n%2e%2e%2fsecret.ts\n"},
		{"null byte in segment", "#EXTINF:6.0,\nseg_0000.ts\x00\n"},
		{"no segments", "#EXTM3U\n#EXT-X-ENDLIST\n"},
		{"zero duration", "#EXTINF:0,\nseg_0000.ts\n"},
		{"missing segment file", "#EXTINF:6.0,\nseg_9999.ts\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, PlaylistName), []byte(tt.body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := measureBandwidth(dir); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

// os.Root refuses a segment that is a symlink to a file outside the rung dir.
func TestMeasureBandwidthRefusesSymlinkEscape(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "secret.ts")
	if err := os.WriteFile(outside, make([]byte, 1000), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "seg_0000.ts")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, PlaylistName),
		[]byte("#EXTINF:6.0,\nseg_0000.ts\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := measureBandwidth(dir); err == nil {
		t.Fatal("expected an error: segment escapes the rung dir via symlink")
	}
}

func TestCodecsString(t *testing.T) {
	tests := []struct {
		name string
		p    ProbeResult
		want string
	}{
		{"high 4.0 + aac", ProbeResult{VideoCodec: "h264", Profile: "High", Level: 40, AudioCodec: "aac", AudioProfile: "LC"}, "avc1.640028,mp4a.40.2"},
		{"remuxed he-aac", ProbeResult{VideoCodec: "h264", Profile: "High", Level: 40, AudioCodec: "aac", AudioProfile: "HE-AAC"}, "avc1.640028,mp4a.40.5"},
		{"remuxed he-aac v2", ProbeResult{VideoCodec: "h264", Profile: "High", Level: 40, AudioCodec: "aac", AudioProfile: "HE-AACv2"}, "avc1.640028,mp4a.40.29"},
		{"unknown aac profile omitted", ProbeResult{VideoCodec: "h264", Profile: "High", Level: 40, AudioCodec: "aac", AudioProfile: "Main"}, ""},
		{"main 3.1 no audio", ProbeResult{VideoCodec: "h264", Profile: "Main", Level: 31}, "avc1.4d401f"},
		{"constrained baseline 3.0", ProbeResult{VideoCodec: "h264", Profile: "Constrained Baseline", Level: 30, AudioCodec: "aac", AudioProfile: "LC"}, "avc1.42c01e,mp4a.40.2"},
		{"unknown profile omitted", ProbeResult{VideoCodec: "h264", Profile: "High 10", Level: 40}, ""},
		{"non-aac audio omitted", ProbeResult{VideoCodec: "h264", Profile: "High", Level: 40, AudioCodec: "ac3"}, ""},
		{"not h264", ProbeResult{VideoCodec: "hevc", Profile: "Main", Level: 120}, ""},
		{"missing level", ProbeResult{VideoCodec: "h264", Profile: "High"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := codecsString(tt.p); got != tt.want {
				t.Fatalf("codecsString = %q, want %q", got, tt.want)
			}
		})
	}
}
