package ffmpeg

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/vtnHAN-SOLO/hls-vod/internal/mediapath"
)

// SubtitlesDir holds the extracted WebVTT files, relative to the movie dir.
const SubtitlesDir = "subs"

// textSubtitleCodecs convert to WebVTT. Styling (ASS) is lost; text is kept.
var textSubtitleCodecs = map[string]bool{
	"subrip": true, "ass": true, "ssa": true, "webvtt": true, "mov_text": true, "text": true,
}

// imageSubtitleCodecs are bitmaps: turning them into text needs OCR, so
// they are skipped rather than failing the movie.
var imageSubtitleCodecs = map[string]bool{
	"hdmv_pgs_subtitle": true, "dvd_subtitle": true, "dvb_subtitle": true, "xsub": true,
}

// SubtitleTrack is one WebVTT file that was written.
type SubtitleTrack struct {
	SubtitleStream
	Path string // relative to the movie dir, e.g. "subs/0_eng.vtt"
}

// SkippedSubtitle is a subtitle stream that could not become WebVTT.
type SkippedSubtitle struct {
	SubtitleStream
	Reason string
	Err    error // the ffmpeg error when conversion was attempted and failed
}

// SubtitleResult lists what ExtractSubtitles wrote and what it skipped.
type SubtitleResult struct {
	Written []SubtitleTrack
	Skipped []SkippedSubtitle
}

// ExtractSubtitles converts every text subtitle stream of src into
// movieDir/subs/{n}_{lang}.vtt. Image-based and unknown codecs, and text
// tracks whose conversion fails, are returned in Skipped for the caller to
// report: a broken subtitle track never fails the movie. Only cancellation
// (or being unable to create subs/) is an error. movieDir/subs exists only
// when at least one file was written.
func ExtractSubtitles(ctx context.Context, input, movieDir string, src ProbeResult) (res SubtitleResult, err error) {
	var todo []SubtitleTrack
	for i, s := range src.Subtitles {
		switch {
		case textSubtitleCodecs[s.Codec]:
			todo = append(todo, SubtitleTrack{SubtitleStream: s, Path: subtitlePath(i, s)})
		case imageSubtitleCodecs[s.Codec]:
			res.Skipped = append(res.Skipped, SkippedSubtitle{SubtitleStream: s, Reason: "image-based subtitles cannot be converted to WebVTT"})
		default:
			res.Skipped = append(res.Skipped, SkippedSubtitle{SubtitleStream: s, Reason: "unsupported subtitle codec"})
		}
	}
	if len(todo) == 0 {
		return res, nil
	}

	dir := filepath.Join(movieDir, SubtitlesDir)
	if err := mkdirShared(dir); err != nil {
		return SubtitleResult{}, err
	}
	defer func() {
		if err != nil || len(res.Written) == 0 {
			_ = os.RemoveAll(dir)
		}
		if err != nil {
			res = SubtitleResult{}
		}
	}()

	// Fast path: every track in one read of the input.
	if err := convertSubtitles(ctx, input, movieDir, todo); err == nil {
		res.Written = todo
		return res, nil
	} else if ctx.Err() != nil {
		return res, err
	}

	// One track broke the combined run. Retry each alone so the good ones
	// are kept and only the broken one is skipped, with its own error.
	for _, t := range todo {
		err := convertSubtitles(ctx, input, movieDir, []SubtitleTrack{t})
		switch {
		case ctx.Err() != nil:
			return res, fmt.Errorf("extracting subtitles: %w", ctx.Err())
		case err != nil:
			if out, err := mediapath.Join(movieDir, t.Path); err == nil {
				_ = os.Remove(out)
			}
			res.Skipped = append(res.Skipped, SkippedSubtitle{
				SubtitleStream: t.SubtitleStream, Reason: "conversion to WebVTT failed", Err: err,
			})
		default:
			res.Written = append(res.Written, t)
		}
	}
	return res, nil
}

// convertSubtitles writes each track to its WebVTT file in one ffmpeg run.
func convertSubtitles(ctx context.Context, input, movieDir string, tracks []SubtitleTrack) error {
	in, err := fileURL(input)
	if err != nil {
		return err
	}
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error"}
	args = append(args, inputArgs...)
	args = append(args, "-i", in)
	for _, t := range tracks {
		name, err := mediapath.Join(movieDir, t.Path)
		if err != nil {
			return err
		}
		out, err := fileURL(name)
		if err != nil {
			return err
		}
		args = append(args, "-map", "0:"+strconv.Itoa(t.Index), "-c:s", "webvtt", "-f", "webvtt", "-y", out)
	}
	if err := runFFmpeg(ctx, nil, args...); err != nil {
		return fmt.Errorf("extracting subtitles: %w", err)
	}
	return nil
}

// subtitlePath names the n-th subtitle stream. The language tag comes from
// the file's metadata, so only a plain ISO 639 code is used in the name.
func subtitlePath(n int, s SubtitleStream) string {
	name := strconv.Itoa(n) + "_" + languageTag(s.Language)
	if s.Forced {
		name += "_forced"
	}
	return path.Join(SubtitlesDir, name+".vtt")
}

func languageTag(lang string) string {
	lang = strings.ToLower(lang)
	if len(lang) < 2 || len(lang) > 3 {
		return "und"
	}
	for _, r := range lang {
		if r < 'a' || r > 'z' {
			return "und"
		}
	}
	return lang
}
