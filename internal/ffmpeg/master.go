package ffmpeg

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/vtnHAN-SOLO/hls-vod/internal/mediapath"
)

// Variant is one #EXT-X-STREAM-INF entry in the master playlist.
type Variant struct {
	URI              string // relative to the master, e.g. "720p/index.m3u8"
	Width, Height    int
	Bandwidth        int    // peak bits/s over any segment (required by RFC 8216)
	AverageBandwidth int    // bits/s over the whole rung
	Codecs           string // RFC 6381, e.g. `avc1.640028,mp4a.40.2`; "" to omit
}

// DescribeRung measures a finished rung in dir and returns its Variant, with
// URI set to uri. Resolution and codecs come from probing the output itself,
// bandwidth from the real segment sizes.
func DescribeRung(ctx context.Context, dir, uri string) (Variant, error) {
	playlist := filepath.Join(dir, PlaylistName)
	p, err := Probe(ctx, playlist)
	if err != nil {
		return Variant{}, err
	}
	peak, avg, err := measureBandwidth(dir)
	if err != nil {
		return Variant{}, err
	}
	return Variant{
		URI: uri, Width: p.Width, Height: p.Height,
		Bandwidth: peak, AverageBandwidth: avg,
		Codecs: codecsString(p),
	}, nil
}

// measureBandwidth reads dir's media playlist and its segment files and
// returns the peak and average bitrate in bits/s.
//
// Segment names come from the playlist's contents, so every access goes
// through mediapath.Root: a crafted playlist cannot reach outside dir, not
// even via "../" or a symlink.
func measureBandwidth(dir string) (peak, avg int, err error) {
	root, err := mediapath.Open(dir)
	if err != nil {
		return 0, 0, fmt.Errorf("opening rung dir: %w", err)
	}
	defer func() { _ = root.Close() }()

	playlist := filepath.Join(dir, PlaylistName)
	data, err := root.ReadFile(PlaylistName)
	if err != nil {
		return 0, 0, fmt.Errorf("reading %s: %w", playlist, err)
	}

	var totalBits, totalSecs float64
	var pending float64 // duration from the last #EXTINF, waiting for its URI
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(line, "#EXTINF:"):
			v, _, _ := strings.Cut(strings.TrimPrefix(line, "#EXTINF:"), ",")
			if pending, err = strconv.ParseFloat(v, 64); err != nil || pending <= 0 {
				return 0, 0, fmt.Errorf("bad #EXTINF in %s: %q", playlist, line)
			}
		case line == "" || strings.HasPrefix(line, "#"):
		default:
			fi, err := root.Stat(line)
			if err != nil {
				return 0, 0, fmt.Errorf("segment of %s: %w", playlist, err)
			}
			bits := float64(fi.Size()) * 8
			if rate := int(bits / pending); rate > peak {
				peak = rate
			}
			totalBits += bits
			totalSecs += pending
		}
	}
	if err := sc.Err(); err != nil {
		return 0, 0, fmt.Errorf("scanning %s: %w", playlist, err)
	}
	if totalSecs == 0 {
		return 0, 0, fmt.Errorf("%s lists no segments", playlist)
	}
	return peak, int(totalBits / totalSecs), nil
}

// codecsString builds the CODECS attribute, or "" when the profile is not one
// we can name exactly (a wrong CODECS is worse than none: players may refuse
// the variant).
func codecsString(p ProbeResult) string {
	if p.VideoCodec != "h264" || p.Level <= 0 || p.Level > 0xff {
		return ""
	}
	// profile_idc + constraint flags, per RFC 6381 avc1.PPCCLL. These are
	// the flags libx264 writes; plain "Baseline" varies by encoder, so omit it.
	prefix, ok := map[string]string{
		"Constrained Baseline": "42c0",
		"Main":                 "4d40",
		"High":                 "6400",
	}[p.Profile]
	if !ok {
		return ""
	}
	codecs := fmt.Sprintf("avc1.%s%02x", prefix, p.Level)
	switch p.AudioCodec {
	case "":
	case "aac":
		// RFC 6381 audio object types. A remuxed track keeps its profile.
		aot, ok := map[string]string{"LC": "2", "HE-AAC": "5", "HE-AACv2": "29"}[p.AudioProfile]
		if !ok {
			return ""
		}
		codecs += ",mp4a.40." + aot
	default:
		return ""
	}
	return codecs
}

// WriteMasterPlaylist writes the master playlist at path, listing every
// variant ordered by bandwidth. The file is written to a temp name and renamed,
// so a player never sees a half-written master.
func WriteMasterPlaylist(path string, variants []Variant) error {
	if len(variants) == 0 {
		return errors.New("writing master playlist: no variants")
	}
	sorted := append([]Variant(nil), variants...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Bandwidth < sorted[j].Bandwidth })

	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:3\n")
	for _, v := range sorted {
		if err := validateVariant(v); err != nil {
			return fmt.Errorf("writing master playlist: %w", err)
		}
		fmt.Fprintf(&b, "#EXT-X-STREAM-INF:BANDWIDTH=%d", v.Bandwidth)
		if v.AverageBandwidth > 0 {
			fmt.Fprintf(&b, ",AVERAGE-BANDWIDTH=%d", v.AverageBandwidth)
		}
		fmt.Fprintf(&b, ",RESOLUTION=%dx%d", v.Width, v.Height)
		if v.Codecs != "" {
			fmt.Fprintf(&b, ",CODECS=%q", v.Codecs)
		}
		b.WriteString("\n" + v.URI + "\n")
	}
	return writeFileAtomic(path, []byte(b.String()))
}

func validateVariant(v Variant) error {
	if v.Bandwidth <= 0 || v.Width <= 0 || v.Height <= 0 {
		return fmt.Errorf("variant %q: bandwidth and resolution must be positive", v.URI)
	}
	// URIs are written verbatim into the playlist: no line breaks, no
	// absolute paths or URLs, no climbing out of the movie's directory.
	if _, err := mediapath.Rel(v.URI); err != nil || path.Ext(v.URI) != ".m3u8" {
		return fmt.Errorf("variant URI %q must be a safe relative path to a .m3u8", v.URI)
	}
	if strings.ContainsAny(v.Codecs, "\"\r\n") {
		return fmt.Errorf("variant %q: invalid CODECS %q", v.URI, v.Codecs)
	}
	return nil
}

func writeFileAtomic(path string, data []byte) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", path, err)
	}
	// CreateTemp makes the file 0600. It must match the 0644 segments ffmpeg
	// writes, or nginx (media-delivery, another UID) gets 403 on the master only.
	if err = os.Chmod(tmp.Name(), 0o644); err != nil { // #nosec G302 -- world-readable by design, see above
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	if err = os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("renaming into place %s: %w", path, err)
	}
	return nil
}
