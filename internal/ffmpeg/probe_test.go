package ffmpeg

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

// Pure parsing tests on ffprobe JSON: covers stream orders and flags that
// muxers never produce on demand (e.g. cover art listed before the movie).
func TestParseProbe(t *testing.T) {
	tests := []struct {
		name    string
		json    string
		want    ProbeResult
		wantErr error
	}{
		{
			name: "cover art listed first is skipped",
			json: `{"streams":[
				{"index":0,"codec_type":"video","codec_name":"mjpeg","width":300,"height":300,"disposition":{"attached_pic":1}},
				{"index":1,"codec_type":"video","codec_name":"h264","width":1920,"height":1080,"pix_fmt":"yuv420p","profile":"High","level":40,"sample_aspect_ratio":"1:1"},
				{"index":2,"codec_type":"audio","codec_name":"aac","profile":"LC","channels":2}],
				"format":{"duration":"5400.5"}}`,
			want: ProbeResult{VideoIndex: 1, VideoCodec: "h264", Width: 1920, Height: 1080, DisplayWidth: 1920,
				PixFmt: "yuv420p", Profile: "High", Level: 40, Duration: 5400500 * time.Millisecond,
				AudioIndex: 2, AudioCodec: "aac", AudioProfile: "LC", AudioChannels: 2},
		},
		{
			name: "default audio track wins over the first",
			json: `{"streams":[
				{"index":0,"codec_type":"video","codec_name":"h264","width":1280,"height":720,"pix_fmt":"yuv420p"},
				{"index":1,"codec_type":"audio","codec_name":"ac3","channels":6,"disposition":{"default":0}},
				{"index":2,"codec_type":"audio","codec_name":"aac","profile":"LC","channels":2,"disposition":{"default":1}},
				{"index":3,"codec_type":"audio","codec_name":"dts","channels":6,"disposition":{"default":1}}],
				"format":{"duration":"10"}}`,
			want: ProbeResult{VideoCodec: "h264", Width: 1280, Height: 720, DisplayWidth: 1280, PixFmt: "yuv420p",
				Duration: 10 * time.Second, AudioIndex: 2, AudioCodec: "aac", AudioProfile: "LC", AudioChannels: 2},
		},
		{
			name: "no default audio: the first one",
			json: `{"streams":[
				{"index":0,"codec_type":"video","codec_name":"h264","width":1280,"height":720},
				{"index":1,"codec_type":"audio","codec_name":"eac3","channels":6},
				{"index":2,"codec_type":"audio","codec_name":"aac","channels":2}]}`,
			want: ProbeResult{VideoCodec: "h264", Width: 1280, Height: 720, DisplayWidth: 1280,
				AudioIndex: 1, AudioCodec: "eac3", AudioChannels: 6},
		},
		{
			name: "anamorphic DVD and subtitles",
			json: `{"streams":[
				{"index":0,"codec_type":"video","codec_name":"mpeg2video","width":720,"height":576,"sample_aspect_ratio":"64:45"},
				{"index":1,"codec_type":"subtitle","codec_name":"dvd_subtitle","tags":{"language":"fre"}},
				{"index":2,"codec_type":"subtitle","codec_name":"subrip","tags":{"language":"eng","title":"SDH"},"disposition":{"default":1,"forced":1}}]}`,
			want: ProbeResult{VideoCodec: "mpeg2video", Width: 720, Height: 576, DisplayWidth: 1024, AudioIndex: -1,
				Subtitles: []SubtitleStream{
					{Index: 1, Codec: "dvd_subtitle", Language: "fre"},
					{Index: 2, Codec: "subrip", Language: "eng", Title: "SDH", Default: true, Forced: true},
				}},
		},
		{
			name: "duration N/A leaves zero, SAR 0:1 means square",
			json: `{"streams":[{"codec_type":"video","codec_name":"h264","width":2,"height":2,"sample_aspect_ratio":"0:1"}],
				"format":{"duration":"N/A"}}`,
			want: ProbeResult{VideoCodec: "h264", Width: 2, Height: 2, DisplayWidth: 2, AudioIndex: -1},
		},
		{
			name:    "only cover art means no video",
			json:    `{"streams":[{"codec_type":"video","codec_name":"png","width":300,"height":300,"disposition":{"attached_pic":1}}]}`,
			wantErr: ErrNoVideo,
		},
		{
			name:    "subtitles only",
			json:    `{"streams":[{"codec_type":"subtitle","codec_name":"subrip"}]}`,
			wantErr: ErrNoVideo,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseProbe([]byte(tt.json))
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("parseProbe = %+v\nwant          %+v", got, tt.want)
			}
		})
	}

	if _, err := parseProbe([]byte("not json")); err == nil {
		t.Error("expected an error for invalid JSON")
	}
}

func TestSubtitlePath(t *testing.T) {
	tests := []struct {
		n    int
		s    SubtitleStream
		want string
	}{
		{0, SubtitleStream{Language: "eng"}, "subs/0_eng.vtt"},
		{3, SubtitleStream{Language: "FR"}, "subs/3_fr.vtt"},
		{1, SubtitleStream{}, "subs/1_und.vtt"},
		{2, SubtitleStream{Language: "../../etc"}, "subs/2_und.vtt"},
		{4, SubtitleStream{Language: "en-US"}, "subs/4_und.vtt"},
		{5, SubtitleStream{Language: "spa", Forced: true}, "subs/5_spa_forced.vtt"},
	}
	for _, tt := range tests {
		if got := subtitlePath(tt.n, tt.s); got != tt.want {
			t.Errorf("subtitlePath(%d, %+v) = %q, want %q", tt.n, tt.s, got, tt.want)
		}
	}
}

func TestVTTTime(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{0, "00:00:00.000"},
		{10 * time.Second, "00:00:10.000"},
		{time.Hour + 2*time.Minute + 3*time.Second + 45*time.Millisecond, "01:02:03.045"},
	}
	for _, tt := range tests {
		if got := vttTime(tt.d); got != tt.want {
			t.Errorf("vttTime(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func TestThumbnailsVTT(t *testing.T) {
	// 25s at 10s intervals, 2x1 grid: three tiles over two sheets.
	opt := ThumbnailOptions{Interval: 10 * time.Second, Cols: 2, Rows: 1, TileHeight: 90}
	res := ThumbnailResult{Count: 3, Sheets: 2, TileWidth: 160, TileHeight: 90}
	want := `WEBVTT

00:00:00.000 --> 00:00:10.000
thumbs/sprite_000.jpg#xywh=0,0,160,90

00:00:10.000 --> 00:00:20.000
thumbs/sprite_000.jpg#xywh=160,0,160,90

00:00:20.000 --> 00:00:25.000
thumbs/sprite_001.jpg#xywh=0,0,160,90
`
	if got := thumbnailsVTT(res, opt, 25*time.Second); got != want {
		t.Fatalf("thumbnails.vtt:\n%s\nwant:\n%s", got, want)
	}
}
