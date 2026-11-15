package medialayout

import (
	"testing"

	"github.com/vtnHAN-SOLO/hls-vod/internal/mediapath"
)

func TestVideoExt(t *testing.T) {
	tests := []struct {
		name string
		ext  string
		ok   bool
	}{
		{"movie.mkv", "mkv", true},
		{"My Movie (2020).MP4", "mp4", true},
		{"a.b.m4v", "m4v", true},
		{"x.mov", "mov", true},
		{"x.avi", "avi", true},
		{"x.webm", "webm", true},
		{"movie.mkv.part", "part", false},
		{"movie.mp4.tmp", "tmp", false},
		{"movie.mp4.crdownload", "crdownload", false},
		{"notes.txt", "txt", false},
		{"noext", "", false},
		{"trailing.", "", false},
	}
	for _, tt := range tests {
		ext, ok := VideoExt(tt.name)
		if ext != tt.ext || ok != tt.ok {
			t.Errorf("VideoExt(%q) = %q, %v; want %q, %v", tt.name, ext, ok, tt.ext, tt.ok)
		}
	}
}

// Every name this package builds must be one mediapath accepts: it ends up
// in movies rows and in /media/ URLs.
func TestNamesPassMediapath(t *testing.T) {
	const id = "0b9d6b52-2d2c-4a7b-9e0e-6a4f39a0c1d2"
	for _, p := range []string{
		Inbox, Sources, Library, MasterPlaylist, ThumbnailsVTT,
		SourceName(id, "mkv"), SourcePath(id, "mkv"), LibraryPath(id),
	} {
		if _, err := mediapath.Rel(p); err != nil {
			t.Errorf("%q: %v", p, err)
		}
	}
	if got := SourcePath(id, "mp4"); got != "sources/"+id+".mp4" {
		t.Errorf("SourcePath = %q", got)
	}
	if got := LibraryPath(id); got != "library/"+id {
		t.Errorf("LibraryPath = %q", got)
	}
}

func TestVersion(t *testing.T) {
	const id = "0b9d6b52-2d2c-4a7b-9e0e-6a4f39a0c1d2"
	const v = "4f5a6b7c-8d9e-4f0a-9b2c-3d4e5f6a7b8c"
	tests := []struct {
		path, want string
		ok         bool
	}{
		{"library/" + id + "/" + v, v, true},
		{"library/" + id, "", true},
		{"library/" + id + "/", "", false},
		{"library/" + id + "/" + v + "/x", "", false},
		{"library/" + id + "x", "", false},
		{"library/other/" + v, "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		got, ok := Version(id, tt.path)
		if got != tt.want || ok != tt.ok {
			t.Errorf("Version(%q) = %q, %v; want %q, %v", tt.path, got, ok, tt.want, tt.ok)
		}
	}
	if VersionPath(id, v) != "library/"+id+"/"+v {
		t.Error("VersionPath")
	}
	if InVersion(v, MasterPlaylist) != v+"/master.m3u8" || InVersion("", MasterPlaylist) != "master.m3u8" {
		t.Error("InVersion")
	}
}
