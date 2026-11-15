package ffmpeg

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/vtnHAN-SOLO/hls-vod/internal/mediapath"
)

const (
	// SegmentSeconds is the target HLS segment length.
	SegmentSeconds = 6
	// PlaylistName is the media playlist written into every rung directory.
	PlaylistName = "index.m3u8"
	segmentGlob  = "seg_%04d.ts"
)

// ErrUpscale means a rung was requested taller than its source.
var ErrUpscale = errors.New("rung height exceeds source height")

// WriteRung produces one planned rung into outDir using the rung's Mode.
func WriteRung(ctx context.Context, input, outDir string, r Rung, progress ProgressFunc) error {
	switch r.Mode {
	case ModeRemux:
		return Remux(ctx, input, outDir, progress)
	case ModeCopyVideo:
		return CopyVideo(ctx, input, outDir, progress)
	default:
		return Encode(ctx, input, outDir, r.Height, r.BitrateKbps, progress)
	}
}

// Remux writes outDir/index.m3u8 and its segments by copying the source's
// video and chosen audio stream without re-encoding. Callers decide when this
// is valid using ProbeResult.CanRemux; Remux itself re-checks.
func Remux(ctx context.Context, input, outDir string, progress ProgressFunc) error {
	src, err := Probe(ctx, input)
	if err != nil {
		return err
	}
	if !src.CanRemux() {
		return fmt.Errorf("remuxing %s: source is %s %s %s with %s, not 8-bit H.264 with stereo AAC",
			input, src.VideoCodec, src.Profile, src.PixFmt, describeAudio(src))
	}
	return writeRung(ctx, input, outDir, src, []string{"-c", "copy"}, progress)
}

// CopyVideo copies the source's video unchanged and encodes the chosen audio
// stream to AAC stereo. It is the path for H.264 movies whose audio is AC3,
// DTS, EAC3 or multichannel: seconds of audio work instead of a full encode.
func CopyVideo(ctx context.Context, input, outDir string, progress ProgressFunc) error {
	src, err := Probe(ctx, input)
	if err != nil {
		return err
	}
	if !src.VideoCopyable() {
		return fmt.Errorf("copying video of %s: %s %s %s is not 8-bit H.264",
			input, src.VideoCodec, src.Profile, src.PixFmt)
	}
	codec := append([]string{"-c:v", "copy"}, audioArgs(src.Height)...)
	return writeRung(ctx, input, outDir, src, codec, progress)
}

// Encode writes outDir/index.m3u8 and its segments as H.264/AAC at the given
// height, with video capped at bitrateKbps. It refuses to upscale: height
// must not exceed the source's.
func Encode(ctx context.Context, input, outDir string, height, bitrateKbps int, progress ProgressFunc) error {
	if height <= 0 || height%2 != 0 {
		return fmt.Errorf("encoding %s: height %d must be positive and even", input, height)
	}
	if bitrateKbps <= 0 {
		return fmt.Errorf("encoding %s: bitrate %d kbps must be positive", input, bitrateKbps)
	}
	src, err := Probe(ctx, input)
	if err != nil {
		return err
	}
	if height > src.Height {
		return fmt.Errorf("encoding %s at %dp from %dp source: %w", input, height, src.Height, ErrUpscale)
	}

	codec := []string{
		"-vf", "scale=-2:" + strconv.Itoa(height),
		// -pix_fmt yuv420p also turns 10-bit (Hi10P) sources into 8-bit.
		"-c:v", "libx264", "-preset", "medium", "-profile:v", "high", "-pix_fmt", "yuv420p",
		"-crf", "23",
		"-maxrate", strconv.Itoa(bitrateKbps) + "k",
		"-bufsize", strconv.Itoa(2*bitrateKbps) + "k",
		// Keyframe exactly every segment boundary, so every encoded rung cuts
		// at the same timestamps and the player can switch cleanly.
		"-force_key_frames", "expr:gte(t,n_forced*" + strconv.Itoa(SegmentSeconds) + ")",
		"-sc_threshold", "0",
	}
	codec = append(codec, audioArgs(height)...)
	return writeRung(ctx, input, outDir, src, codec, progress)
}

// audioArgs encodes the chosen audio stream to AAC-LC stereo. -ac 2 downmixes
// 5.1/7.1 with ffmpeg's standard matrix (centre and surrounds folded in).
func audioArgs(height int) []string {
	kbps := 96
	if height >= 720 {
		kbps = 128
	}
	return []string{"-c:a", "aac", "-b:a", strconv.Itoa(kbps) + "k", "-ac", "2"}
}

// writeRung runs ffmpeg with the HLS muxer into a fresh outDir. outDir must
// not already exist, so stale segments from an earlier run can never mix with
// new ones; on failure it is removed again.
func writeRung(ctx context.Context, input, outDir string, src ProbeResult, codecArgs []string, progress ProgressFunc) (err error) {
	if err := checkPattern(outDir); err != nil {
		return err
	}
	in, err := fileURL(input)
	if err != nil {
		return err
	}
	playlist, err := fileURL(filepath.Join(outDir, PlaylistName))
	if err != nil {
		return err
	}
	segments, err := fileURL(filepath.Join(outDir, segmentGlob))
	if err != nil {
		return err
	}

	if err := mkdirShared(outDir); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(outDir)
		}
	}()

	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error"}
	args = append(args, inputArgs...)
	// Map the probed streams by absolute index: the real video (never cover
	// art) and the chosen audio track (the default one, else the first).
	args = append(args, "-i", in, "-map", "0:"+strconv.Itoa(src.VideoIndex))
	if src.AudioIndex >= 0 {
		args = append(args, "-map", "0:"+strconv.Itoa(src.AudioIndex))
	}
	args = append(args, codecArgs...)
	args = append(args,
		"-f", "hls",
		"-hls_time", strconv.Itoa(SegmentSeconds),
		"-hls_playlist_type", "vod",
		"-hls_segment_filename", segments,
		"-y", playlist,
	)

	if err := runFFmpeg(ctx, progress, args...); err != nil {
		return fmt.Errorf("writing rung %s: %w", outDir, err)
	}
	return nil
}

// PlaylistDuration sums the #EXTINF durations of dir's media playlist. It is
// the exact length of what was written, even when the source container did
// not record a duration.
func PlaylistDuration(dir string) (time.Duration, error) {
	root, err := mediapath.Open(dir)
	if err != nil {
		return 0, fmt.Errorf("opening rung dir: %w", err)
	}
	defer func() { _ = root.Close() }()
	data, err := root.ReadFile(PlaylistName)
	if err != nil {
		return 0, fmt.Errorf("reading playlist in %s: %w", dir, err)
	}

	var total float64
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		v, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "#EXTINF:")
		if !ok {
			continue
		}
		v, _, _ = strings.Cut(v, ",")
		d, err := strconv.ParseFloat(v, 64)
		if err != nil || d <= 0 {
			return 0, fmt.Errorf("bad #EXTINF in %s: %q", dir, v)
		}
		total += d
	}
	if err := sc.Err(); err != nil {
		return 0, fmt.Errorf("scanning playlist in %s: %w", dir, err)
	}
	if total == 0 {
		return 0, fmt.Errorf("playlist in %s lists no segments", dir)
	}
	return time.Duration(total * float64(time.Second)), nil
}

func describeAudio(p ProbeResult) string {
	if p.AudioCodec == "" {
		return "no audio"
	}
	return fmt.Sprintf("%s %dch audio", p.AudioCodec, p.AudioChannels)
}
