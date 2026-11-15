package model

import (
	"strings"
	"testing"
)

func TestValidID(t *testing.T) {
	tests := []struct {
		id   string
		want bool
	}{
		{"0b9d6b52-2d2c-4a7b-9e0e-6a4f39a0c1d2", true},
		{"00000000-0000-0000-0000-000000000000", true},
		{"0B9D6B52-2D2C-4A7B-9E0E-6A4F39A0C1D2", false}, // callers lowercase first
		{"0b9d6b522d2c4a7b9e0e6a4f39a0c1d2", false},
		{"0b9d6b52-2d2c-4a7b-9e0e-6a4f39a0c1d", false},
		{"0b9d6b52-2d2c-4a7b-9e0e-6a4f39a0c1d2x", false},
		{"0b9d6b52_2d2c-4a7b-9e0e-6a4f39a0c1d2", false},
		{"0b9d6b52-2d2c-4a7b-9e0e-6a4f39a0c1g2", false},
		{"../../../../etc/passwd-aaaaaaaaaaaaa", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := ValidID(tt.id); got != tt.want {
			t.Errorf("ValidID(%q) = %v, want %v", tt.id, got, tt.want)
		}
	}
}

func TestMovieStatusValid(t *testing.T) {
	for _, s := range []MovieStatus{StatusPending, StatusProcessing, StatusReady, StatusFailed} {
		if !s.Valid() {
			t.Errorf("%q should be valid", s)
		}
	}
	for _, s := range []MovieStatus{"", "READY", "deleted"} {
		if s.Valid() {
			t.Errorf("%q should be invalid", s)
		}
	}
}

func TestNewID(t *testing.T) {
	seen := map[string]bool{}
	for range 1000 {
		id := NewID()
		if !ValidID(id) || id[14] != '4' || !strings.ContainsRune("89ab", rune(id[19])) {
			t.Fatalf("NewID() = %q, want a canonical v4 UUID", id)
		}
		if seen[id] {
			t.Fatalf("NewID() repeated %q", id)
		}
		seen[id] = true
	}
}
