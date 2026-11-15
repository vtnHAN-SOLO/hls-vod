package ffmpeg

import (
	"testing"
)

func TestCanRemux(t *testing.T) {
	base := ProbeResult{VideoCodec: "h264", Profile: "High", PixFmt: "yuv420p", AudioCodec: "aac", AudioChannels: 2, Height: 720}
	tests := []struct {
		name   string
		mutate func(*ProbeResult)
		want   bool
	}{
		{"h264 + aac", func(*ProbeResult) {}, true},
		{"mono aac", func(p *ProbeResult) { p.AudioChannels = 1 }, true},
		{"5.1 aac", func(p *ProbeResult) { p.AudioChannels = 6 }, false},
		{"main profile", func(p *ProbeResult) { p.Profile = "Main" }, true},
		{"high 10 profile", func(p *ProbeResult) { p.Profile, p.PixFmt = "High 10", "yuv420p10le" }, false},
		{"high 10 profile misreported as yuv420p", func(p *ProbeResult) { p.Profile = "High 10" }, false},
		{"high 4:2:2 profile", func(p *ProbeResult) { p.Profile, p.PixFmt = "High 4:2:2", "yuv422p" }, false},
		{"unknown profile", func(p *ProbeResult) { p.Profile = "" }, false},
		{"h264, no audio", func(p *ProbeResult) { p.AudioCodec = "" }, true},
		{"full-range yuvj420p", func(p *ProbeResult) { p.PixFmt = "yuvj420p" }, true},
		{"10-bit h264", func(p *ProbeResult) { p.PixFmt = "yuv420p10le" }, false},
		{"4:4:4 h264", func(p *ProbeResult) { p.PixFmt = "yuv444p" }, false},
		{"hevc", func(p *ProbeResult) { p.VideoCodec = "hevc" }, false},
		{"av1", func(p *ProbeResult) { p.VideoCodec = "av1" }, false},
		{"ac3 audio", func(p *ProbeResult) { p.AudioCodec = "ac3" }, false},
		{"dts audio", func(p *ProbeResult) { p.AudioCodec = "dts" }, false},
		{"eac3 audio", func(p *ProbeResult) { p.AudioCodec = "eac3" }, false},
		{"opus audio", func(p *ProbeResult) { p.AudioCodec = "opus" }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := base
			tt.mutate(&p)
			if got := p.CanRemux(); got != tt.want {
				t.Fatalf("CanRemux(%+v) = %v, want %v", p, got, tt.want)
			}
		})
	}
}

func TestPlanLadder(t *testing.T) {
	h264 := func(h int) ProbeResult {
		return ProbeResult{VideoCodec: "h264", Profile: "High", PixFmt: "yuv420p", AudioCodec: "aac", AudioChannels: 2, Height: h}
	}
	hevc := func(h int) ProbeResult {
		return ProbeResult{VideoCodec: "hevc", Profile: "Main", PixFmt: "yuv420p", AudioCodec: "aac", AudioChannels: 2, Height: h}
	}
	withAudio := func(p ProbeResult, codec string, ch int) ProbeResult {
		p.AudioCodec, p.AudioChannels = codec, ch
		return p
	}
	hi10p := func(h int) ProbeResult {
		p := h264(h)
		p.Profile, p.PixFmt = "High 10", "yuv420p10le"
		return p
	}
	tests := []struct {
		name string
		src  ProbeResult
		want string
	}{
		{"2160p remux", h264(2160), "2160p(remux),1080p,720p,480p"},
		{"1080p remux", h264(1080), "1080p(remux),720p,480p"},
		{"1080p hevc encodes top rung", hevc(1080), "1080p,720p,480p"},
		{"720p never gets 1080p", h264(720), "720p(remux),480p"},
		{"480p is a single rung", h264(480), "480p(remux)"},
		{"360p below every standard rung", h264(360), "360p(remux)"},
		{"widescreen 800 tall", h264(800), "800p(remux),720p,480p"},
		{"odd height remux keeps it", h264(817), "817p(remux),720p,480p"},
		{"odd height encode rounds down", hevc(817), "816p,720p,480p"},
		{"h264 + ac3 copies video", withAudio(h264(1080), "ac3", 6), "1080p(copy-video),720p,480p"},
		{"h264 + dts copies video", withAudio(h264(1080), "dts", 6), "1080p(copy-video),720p,480p"},
		{"h264 + 5.1 aac copies video", withAudio(h264(720), "aac", 6), "720p(copy-video),480p"},
		{"odd height copy-video keeps it", withAudio(h264(817), "eac3", 6), "817p(copy-video),720p,480p"},
		{"10-bit h264 encodes", hi10p(1080), "1080p,720p,480p"},
		{"10-bit h264 with ac3 still encodes", withAudio(hi10p(720), "ac3", 6), "720p,480p"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rungs, err := PlanLadder(tt.src)
			if err != nil {
				t.Fatal(err)
			}
			if got := rungNames(rungs); got != tt.want {
				t.Fatalf("ladder = %s, want %s", got, tt.want)
			}
			for i, r := range rungs {
				if r.Height > tt.src.Height {
					t.Errorf("rung %s is above the %dp source", r.Name, tt.src.Height)
				}
				if r.BitrateKbps <= 0 {
					t.Errorf("rung %s has no bitrate", r.Name)
				}
				if i > 0 && r.Mode != ModeEncode {
					t.Errorf("lower rung %s is %s, want encode", r.Name, r.Mode)
				}
			}
		})
	}

	if _, err := PlanLadder(ProbeResult{}); err == nil {
		t.Error("expected an error for an unknown source height")
	}
}

func TestBitrateFor(t *testing.T) {
	tests := []struct{ height, want int }{
		{240, 400}, {360, 800}, {480, 1400}, {481, 3000}, {720, 3000},
		{800, 5000}, {1080, 5000}, {2160, 14000}, {4320, 14000},
	}
	for _, tt := range tests {
		if got := bitrateFor(tt.height); got != tt.want {
			t.Errorf("bitrateFor(%d) = %d, want %d", tt.height, got, tt.want)
		}
	}
}
