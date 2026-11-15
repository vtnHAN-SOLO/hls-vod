package ffmpeg

import (
	"errors"
	"fmt"
)

// Mode is how a rung is produced from the source.
type Mode int

const (
	// ModeEncode re-encodes video and audio (H.264 + AAC stereo).
	ModeEncode Mode = iota
	// ModeRemux copies video and audio unchanged: seconds, not minutes.
	ModeRemux
	// ModeCopyVideo copies the video and encodes only the audio to AAC
	// stereo. For H.264 movies with AC3/DTS/EAC3 or multichannel audio.
	ModeCopyVideo
)

func (m Mode) String() string {
	switch m {
	case ModeRemux:
		return "remux"
	case ModeCopyVideo:
		return "copy-video"
	default:
		return "encode"
	}
}

// Rung is one quality level of the HLS ladder.
type Rung struct {
	Name        string // directory and label, e.g. "720p"
	Height      int
	BitrateKbps int // video maxrate when encoding; unused when video is copied
	Mode        Mode
}

// lowerRungs are the standard heights encoded below the top rung.
var lowerRungs = []int{1080, 720, 480}

// bitrateTiers maps a height to its video maxrate. A height between tiers
// gets the next tier up, so an 800-pixel-tall widescreen top rung is
// treated like 1080p rather than starved like 720p.
var bitrateTiers = []struct{ height, kbps int }{
	{240, 400}, {360, 800}, {480, 1400}, {720, 3000},
	{1080, 5000}, {1440, 8000}, {2160, 14000},
}

func bitrateFor(height int) int {
	for _, t := range bitrateTiers {
		if height <= t.height {
			return t.kbps
		}
	}
	return bitrateTiers[len(bitrateTiers)-1].kbps
}

// PlanLadder decides the rungs for a probed source.
//
// The top rung is always the source height: remuxed when CanRemux, video
// copied with encoded audio when only the audio is unsuitable, otherwise
// encoded at that height. Lower rungs are the standard heights strictly below
// it. Nothing is ever planned above the source height.
func PlanLadder(p ProbeResult) ([]Rung, error) {
	if p.Height <= 0 {
		return nil, errors.New("planning ladder: source height unknown")
	}

	top := Rung{Height: p.Height, BitrateKbps: bitrateFor(p.Height), Mode: topMode(p)}
	if top.Mode == ModeEncode && top.Height%2 != 0 {
		// libx264 needs even dimensions; round down, never up.
		top.Height--
	}
	top.Name = fmt.Sprintf("%dp", top.Height)

	rungs := []Rung{top}
	for _, h := range lowerRungs {
		if h < top.Height {
			rungs = append(rungs, Rung{Name: fmt.Sprintf("%dp", h), Height: h, BitrateKbps: bitrateFor(h)})
		}
	}
	return rungs, nil
}

func topMode(p ProbeResult) Mode {
	switch {
	case p.CanRemux():
		return ModeRemux
	case p.VideoCopyable():
		return ModeCopyVideo
	default:
		return ModeEncode
	}
}
