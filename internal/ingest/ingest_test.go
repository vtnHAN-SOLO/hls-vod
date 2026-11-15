package ingest

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
)

// A re-run for a version that is already published must fail before any
// work and leave the published folder exactly as it was.
func TestLibraryNeverOverwrites(t *testing.T) {
	root := t.TempDir()
	id, version := model.NewID(), model.NewID()
	existing := filepath.Join(root, "library", id, version)
	if err := os.MkdirAll(existing, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(existing, "master.m3u8"), []byte("published"), 0o600); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(t.TempDir(), "movie.mkv")
	if err := os.WriteFile(input, []byte("not inspected"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, remove, err := Library(t.Context(), slog.New(slog.DiscardHandler), root, id, version, input, nil)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("err = %v, want a refusal", err)
	}
	remove() // a no-op after an error
	if b, err := os.ReadFile(filepath.Join(existing, "master.m3u8")); err != nil || string(b) != "published" {
		t.Fatalf("the published folder changed: %q, %v", b, err)
	}
}

func TestLibraryRefusesNonUUIDs(t *testing.T) {
	root := t.TempDir()
	for _, tt := range [][2]string{{model.NewID(), ".."}, {"..", model.NewID()}, {model.NewID(), "v1"}} {
		if _, _, err := Library(t.Context(), slog.New(slog.DiscardHandler), root, tt[0], tt[1], "in.mkv", nil); err == nil {
			t.Errorf("Library(%q, %q) accepted", tt[0], tt[1])
		}
	}
	if es, _ := os.ReadDir(root); len(es) != 0 {
		t.Fatalf("created %v", es)
	}
}
