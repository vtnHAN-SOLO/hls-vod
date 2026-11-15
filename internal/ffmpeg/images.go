package ffmpeg

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/vtnHAN-SOLO/hls-vod/internal/medialayout"
)

const (
	// PosterName is the poster frame, relative to the movie dir.
	PosterName = "poster.jpg"
	// ThumbnailsDir holds the sprite sheets, relative to the movie dir.
	ThumbnailsDir = "thumbs"
	spriteGlob    = "sprite_%03d.jpg"
)

// squarePixels resizes anamorphic sources (e.g. DVD, sample aspect ratio not
// 1:1) to their display shape, so stills are not squashed. Rungs keep the SAR
// in the stream instead and the player applies it.
const squarePixels = "scale=trunc(ih*dar/2)*2:ih,setsar=1"

// ExtractPoster writes a representative frame near at into outPath as JPEG,
// at most maxHeight tall and never taller than the source.
func ExtractPoster(ctx context.Context, input, outPath string, src ProbeResult, at time.Duration, maxHeight int) error {
	if err := checkPattern(outPath); err != nil {
		return err
	}
	in, err := fileURL(input)
	if err != nil {
		return err
	}
	out, err := fileURL(outPath)
	if err != nil {
		return err
	}
	height := min(maxHeight, src.Height) &^ 1 // even, never upscaled
	if height <= 0 {
		return fmt.Errorf("poster: invalid height %d", height)
	}

	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error"}
	// -ss before -i seeks by keyframe index instead of decoding up to it.
	args = append(args, "-ss", seconds(at))
	args = append(args, inputArgs...)
	args = append(args, "-i", in, "-map", "0:"+strconv.Itoa(src.VideoIndex),
		// Scale down first (cheap), then let thumbnail pick the most typical
		// of the next 24 frames, which avoids black or mid-fade frames.
		"-vf", squarePixels+",scale=-2:"+strconv.Itoa(height)+",thumbnail=n=24",
		"-frames:v", "1", "-q:v", "3", "-update", "1", "-y", out)
	if err := runFFmpeg(ctx, nil, args...); err != nil {
		_ = os.Remove(outPath)
		return fmt.Errorf("extracting poster: %w", err)
	}
	// Seeking past the last frame exits 0 with no output.
	if fi, err := os.Stat(outPath); err != nil || fi.Size() == 0 {
		_ = os.Remove(outPath)
		return fmt.Errorf("extracting poster: no frame at %s", at)
	}
	return nil
}

// ThumbnailOptions controls the seek-preview sprites.
type ThumbnailOptions struct {
	Interval   time.Duration // one thumbnail per interval
	Cols, Rows int           // tiles per sprite sheet
	TileHeight int           // pixels; width follows the display aspect ratio
}

// DefaultThumbnails is one frame every 10s, 10x10 tiles per sheet (1000s of
// movie per ~1600x900 JPEG), so a 2h movie is 8 sheets, not one huge image.
var DefaultThumbnails = ThumbnailOptions{Interval: 10 * time.Second, Cols: 10, Rows: 10, TileHeight: 90}

// ThumbnailResult describes the sprites that were written.
type ThumbnailResult struct {
	Count                 int // thumbnails, one per Interval
	Sheets                int
	TileWidth, TileHeight int
}

// GenerateThumbnails writes movieDir/thumbs/sprite_NNN.jpg sheets and the
// movieDir/thumbnails.vtt index that maps each Interval of the movie to its
// tile (#xywh=), the format hls.js-based players use for seek previews.
//
// duration is the length of the output (see PlaylistDuration).
func GenerateThumbnails(ctx context.Context, input, movieDir string, src ProbeResult,
	duration time.Duration, opt ThumbnailOptions, progress ProgressFunc,
) (res ThumbnailResult, err error) {
	if opt.Interval <= 0 || opt.Cols <= 0 || opt.Rows <= 0 || opt.TileHeight <= 0 {
		return res, fmt.Errorf("thumbnails: invalid options %+v", opt)
	}
	if duration <= 0 {
		return res, errors.New("thumbnails: unknown duration")
	}
	if err := checkPattern(movieDir); err != nil {
		return res, err
	}
	res.TileHeight = opt.TileHeight &^ 1
	res.TileWidth = max(2, (res.TileHeight*src.DisplayWidth/src.Height)&^1)
	res.Count = int((duration + opt.Interval - 1) / opt.Interval)
	perSheet := opt.Cols * opt.Rows
	res.Sheets = (res.Count + perSheet - 1) / perSheet

	in, err := fileURL(input)
	if err != nil {
		return res, err
	}
	dir := filepath.Join(movieDir, ThumbnailsDir)
	sprites, err := fileURL(filepath.Join(dir, spriteGlob))
	if err != nil {
		return res, err
	}
	if err := mkdirShared(dir); err != nil {
		return res, err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(dir)
			_ = os.Remove(filepath.Join(movieDir, medialayout.ThumbnailsVTT))
		}
	}()

	filter := fmt.Sprintf(
		// eof_action=pass keeps the final partial interval: ceil(d/interval) frames.
		"fps=1/%s:round=down:eof_action=pass,scale=%d:%d,setsar=1,tile=%dx%d",
		seconds(opt.Interval), res.TileWidth, res.TileHeight, opt.Cols, opt.Rows)
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error",
		// Decode keyframes only: ~10x faster than decoding every frame, and
		// a keyframe within a few seconds is precise enough for a preview.
		"-skip_frame", "nokey"}
	args = append(args, inputArgs...)
	args = append(args, "-i", in, "-map", "0:"+strconv.Itoa(src.VideoIndex),
		"-vf", filter, "-fps_mode", "passthrough",
		"-q:v", "5", "-start_number", "0", "-y", sprites)
	if err := runFFmpeg(ctx, progress, args...); err != nil {
		return res, fmt.Errorf("generating thumbnails: %w", err)
	}
	for i := range res.Sheets {
		if _, err := os.Stat(filepath.Join(dir, spriteName(i))); err != nil {
			return res, fmt.Errorf("generating thumbnails: expected %d sheets: %w", res.Sheets, err)
		}
	}

	vtt := thumbnailsVTT(res, opt, duration)
	if err := writeFileAtomic(filepath.Join(movieDir, medialayout.ThumbnailsVTT), []byte(vtt)); err != nil {
		return res, err
	}
	return res, nil
}

func thumbnailsVTT(res ThumbnailResult, opt ThumbnailOptions, duration time.Duration) string {
	perSheet := opt.Cols * opt.Rows
	var b strings.Builder
	b.WriteString("WEBVTT\n")
	for i := range res.Count {
		start := time.Duration(i) * opt.Interval
		end := min(start+opt.Interval, duration)
		cell := i % perSheet
		x, y := (cell%opt.Cols)*res.TileWidth, (cell/opt.Cols)*res.TileHeight
		fmt.Fprintf(&b, "\n%s --> %s\n%s#xywh=%d,%d,%d,%d\n",
			vttTime(start), vttTime(end),
			path.Join(ThumbnailsDir, spriteName(i/perSheet)),
			x, y, res.TileWidth, res.TileHeight)
	}
	return b.String()
}

func spriteName(sheet int) string { return fmt.Sprintf(spriteGlob, sheet) }

// vttTime formats d as WebVTT HH:MM:SS.mmm.
func vttTime(d time.Duration) string {
	ms := d.Milliseconds()
	return fmt.Sprintf("%02d:%02d:%02d.%03d", ms/3_600_000, ms/60_000%60, ms/1000%60, ms%1000)
}

// seconds formats d for ffmpeg options such as -ss.
func seconds(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', 3, 64)
}
