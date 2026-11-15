package ffmpeg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ErrNoVideo means the file has no video stream, so there is nothing to stream.
var ErrNoVideo = errors.New("no video stream")

// ProbeResult describes the streams of a media file that the pipeline uses:
// the first real video stream, the chosen audio stream, and every subtitle.
type ProbeResult struct {
	VideoIndex   int    // absolute stream index, for -map 0:N
	VideoCodec   string // ffprobe codec_name, e.g. "h264", "hevc"
	Width        int    // stored width
	Height       int
	DisplayWidth int    // width after applying the sample aspect ratio
	PixFmt       string // e.g. "yuv420p", "yuv420p10le"
	Profile      string // e.g. "High", "High 10"
	Level        int    // H.264 level × 10, e.g. 40 for 4.0
	Duration     time.Duration

	AudioIndex    int    // absolute stream index; -1 when there is no audio
	AudioCodec    string // "" when the file has no audio
	AudioProfile  string // e.g. "LC", "HE-AAC"
	AudioChannels int

	Subtitles []SubtitleStream
}

// SubtitleStream is one subtitle track of the source.
type SubtitleStream struct {
	Index    int    // absolute stream index
	Codec    string // e.g. "subrip", "ass", "hdmv_pgs_subtitle"
	Language string // ISO 639-2 tag as stored, "" if absent
	Title    string
	Default  bool
	Forced   bool
}

// browserPixFmts are the H.264 pixel formats browsers decode: 8-bit 4:2:0.
var browserPixFmts = map[string]bool{"yuv420p": true, "yuvj420p": true}

// browserProfiles are the 8-bit 4:2:0 H.264 profiles browsers decode. "High
// 10" (Hi10P), "High 4:2:2" and "High 4:4:4" are valid H.264 that plays almost
// nowhere, so they are encoded. Checked together with the pixel format: either
// one alone can be misreported by unusual encoders.
var browserProfiles = map[string]bool{
	"Constrained Baseline": true, "Baseline": true, "Main": true,
	"High": true, "Progressive High": true, "Constrained High": true,
}

// VideoCopyable reports whether the video stream can go into HLS unchanged:
// 8-bit 4:2:0 H.264 in a browser-decodable profile.
func (p ProbeResult) VideoCopyable() bool {
	return p.VideoCodec == "h264" && browserPixFmts[p.PixFmt] && browserProfiles[p.Profile]
}

// AudioCopyable reports whether the chosen audio stream can be copied: AAC
// that is already stereo or mono (or no audio at all). Everything else (AC3,
// DTS, EAC3, 5.1 AAC) is encoded to AAC stereo.
func (p ProbeResult) AudioCopyable() bool {
	return p.AudioCodec == "" || (p.AudioCodec == "aac" && p.AudioChannels <= 2)
}

// CanRemux reports whether the source can become an HLS rung with -c copy.
func (p ProbeResult) CanRemux() bool {
	return p.VideoCopyable() && p.AudioCopyable()
}

type ffprobeOutput struct {
	Streams []struct {
		Index        int    `json:"index"`
		CodecType    string `json:"codec_type"`
		CodecName    string `json:"codec_name"`
		Width        int    `json:"width"`
		Height       int    `json:"height"`
		PixFmt       string `json:"pix_fmt"`
		Profile      string `json:"profile"`
		Level        int    `json:"level"`
		Channels     int    `json:"channels"`
		SampleAspect string `json:"sample_aspect_ratio"`
		Disposition  struct {
			Default     int `json:"default"`
			Forced      int `json:"forced"`
			AttachedPic int `json:"attached_pic"`
		} `json:"disposition"`
		Tags struct {
			Language string `json:"language"`
			Title    string `json:"title"`
		} `json:"tags"`
	} `json:"streams"`
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
}

// Probe runs ffprobe on path and describes its streams.
func Probe(ctx context.Context, path string) (ProbeResult, error) {
	url, err := fileURL(path)
	if err != nil {
		return ProbeResult{}, err
	}
	args := append([]string{"-v", "error"}, inputArgs...)
	args = append(args, "-print_format", "json", "-show_format", "-show_streams", url)

	out, err := run(ctx, ffprobeBin, args...)
	if err != nil {
		return ProbeResult{}, fmt.Errorf("probing %s: %w", path, err)
	}
	return parseProbe(out)
}

func parseProbe(data []byte) (ProbeResult, error) {
	var raw ffprobeOutput
	if err := json.Unmarshal(data, &raw); err != nil {
		return ProbeResult{}, fmt.Errorf("decoding ffprobe output: %w", err)
	}

	res := ProbeResult{AudioIndex: -1}
	haveVideo, haveDefaultAudio := false, false
	for _, s := range raw.Streams {
		switch s.CodecType {
		case "video":
			// Cover art in MKV/MP4 is a "video" stream flagged attached_pic.
			if haveVideo || s.Disposition.AttachedPic == 1 || s.Width == 0 || s.Height == 0 {
				continue
			}
			haveVideo = true
			res.VideoIndex, res.VideoCodec = s.Index, s.CodecName
			res.Width, res.Height = s.Width, s.Height
			res.DisplayWidth = displayWidth(s.Width, s.SampleAspect)
			res.PixFmt, res.Profile, res.Level = s.PixFmt, s.Profile, s.Level
		case "audio":
			// The default-flagged track wins; otherwise the first one.
			isDefault := s.Disposition.Default == 1
			if res.AudioIndex >= 0 && (haveDefaultAudio || !isDefault) {
				continue
			}
			haveDefaultAudio = isDefault
			res.AudioIndex, res.AudioCodec = s.Index, s.CodecName
			res.AudioProfile, res.AudioChannels = s.Profile, s.Channels
		case "subtitle":
			res.Subtitles = append(res.Subtitles, SubtitleStream{
				Index: s.Index, Codec: s.CodecName,
				Language: s.Tags.Language, Title: s.Tags.Title,
				Default: s.Disposition.Default == 1, Forced: s.Disposition.Forced == 1,
			})
		}
	}
	if !haveVideo {
		return ProbeResult{}, ErrNoVideo
	}

	// "N/A" or missing for some containers; leave zero rather than fail.
	if secs, err := strconv.ParseFloat(raw.Format.Duration, 64); err == nil && secs > 0 {
		res.Duration = time.Duration(secs * float64(time.Second))
	}
	return res, nil
}

// displayWidth applies a sample aspect ratio such as "64:45" (anamorphic
// DVD) to the stored width. Unknown or square ratios return width unchanged.
func displayWidth(width int, sar string) int {
	n, d, ok := strings.Cut(sar, ":")
	if !ok {
		return width
	}
	num, err1 := strconv.Atoi(n)
	den, err2 := strconv.Atoi(d)
	if err1 != nil || err2 != nil || num <= 0 || den <= 0 {
		return width
	}
	return (width*num + den/2) / den
}
