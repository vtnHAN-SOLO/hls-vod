// Package medialayout is the layout of the media tree under MEDIA_ROOT, shared
// by every program that reads or writes it (watcher, transcoder, devingest,
// api) and mirrored by media-delivery's nginx config:
//
//	inbox/              files dropped by people, any name
//	sources/{id}.{ext}  claimed originals, renamed by the watcher
//	library/{id}/{version}/  published HLS: master.m3u8, thumbnails.vtt, rungs;
//	                         version = the job id that wrote it. Movies
//	                         published before versioned folders have these
//	                         files directly in library/{id}/ (the old layout)
//
// It only names things: building a path on disk or a URL from these names
// still goes through internal/mediapath.
package medialayout

import "strings"

// Top-level directories under MEDIA_ROOT.
const (
	Inbox   = "inbox"
	Sources = "sources"
	Library = "library"
)

// Files inside library/{id}/.
const (
	// MasterPlaylist is the multivariant playlist the player loads.
	MasterPlaylist = "master.m3u8"
	// ThumbnailsVTT indexes the sprite sheets for seek previews.
	ThumbnailsVTT = "thumbnails.vtt"
)

// videoExts are the source containers the watcher claims and devingest
// accepts, lowercase and without the dot.
// ts and m2ts are MPEG-TS (TV and camera recordings, Blu-ray streams).
var videoExts = map[string]bool{
	"mkv": true, "mp4": true, "m4v": true, "mov": true, "avi": true, "webm": true,
	"ts": true, "m2ts": true,
}

// VideoExt returns the lowercase extension of name (without the dot) and
// whether it is a video container we ingest. "Movie.MKV" is ("mkv", true);
// "movie.mkv.part" is ("part", false).
func VideoExt(name string) (string, bool) {
	i := strings.LastIndexByte(name, '.')
	if i < 0 {
		return "", false
	}
	ext := strings.ToLower(name[i+1:])
	return ext, videoExts[ext]
}

// SourceName is the file name of a claimed original: "{id}.{ext}".
func SourceName(id, ext string) string { return id + "." + ext }

// SourcePath is a claimed original relative to MEDIA_ROOT, as stored in
// movies.source_path: "sources/{id}.{ext}".
func SourcePath(id, ext string) string { return Sources + "/" + SourceName(id, ext) }

// LibraryPath is a movie's folder relative to MEDIA_ROOT: "library/{id}".
// Every version lives inside it, and one signed token covers all of it.
func LibraryPath(id string) string { return Library + "/" + id }

// VersionPath is one published version relative to MEDIA_ROOT, as stored in
// movies.library_path: "library/{id}/{version}".
func VersionPath(id, version string) string { return LibraryPath(id) + "/" + version }

// Version is the version folder named by libraryPath (movies.library_path)
// of movie id: ("{version}", true) for "library/{id}/{version}", ("", true)
// for the old layout "library/{id}", and ("", false) for anything else.
// It does not check the version name: callers pass the result through
// mediapath before using it.
func Version(id, libraryPath string) (string, bool) {
	rest, ok := strings.CutPrefix(libraryPath, LibraryPath(id))
	switch {
	case !ok:
		return "", false
	case rest == "":
		return "", true
	}
	v, ok := strings.CutPrefix(rest, "/")
	if !ok || v == "" || strings.Contains(v, "/") {
		return "", false
	}
	return v, true
}

// InVersion is name inside version, relative to library/{id}/ (what a
// signed URL and backdrop_path carry): "{version}/{name}", or just name for
// the old layout (version "").
func InVersion(version, name string) string {
	if version == "" {
		return name
	}
	return version + "/" + name
}
