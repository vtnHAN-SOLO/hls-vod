package mediattl

import (
	"testing"
	"time"
)

func TestOldVersionGrace(t *testing.T) {
	tests := []struct {
		dev, want time.Duration
	}{
		{0, 25 * time.Hour}, // posters: 24 h rounded up by 1 h
		{2 * time.Minute, 2*time.Minute + 5*time.Second},
		{10 * time.Second, 11 * time.Second}, // the step is at least a second
	}
	for _, tt := range tests {
		if got := OldVersionGrace(tt.dev); got != tt.want {
			t.Errorf("OldVersionGrace(%v) = %v, want %v", tt.dev, got, tt.want)
		}
	}
	// The grace must cover every URL the signer can hand out.
	for _, ttl := range []time.Duration{PlaybackURLTTL, PosterURLTTL} {
		if ttl+Step(ttl) > OldVersionGrace(0) {
			t.Errorf("a %v URL outlives the grace", ttl)
		}
	}
}
