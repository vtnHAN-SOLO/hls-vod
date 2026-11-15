// Package mediapath is the one place that turns an untrusted name into a
// location under a media directory: a value from a request, a database row,
// or a media file's own contents (playlist lines, stream metadata).
//
// Two layers, both required:
//
//   - Rel validates the name itself against a strict allowlist. It is the
//     only check for names that leave this process, as a media-delivery URL
//     or as a path handed to ffmpeg, where os.Root cannot follow.
//   - Root opens files through os.Root, so the kernel resolves every
//     component under the directory and refuses "../" and symlinks that
//     leave it, even if a name slipped past Rel or the tree was tampered with.
//
// No other package may validate or confine media paths on its own; see
// TestNoOtherConfinement.
package mediapath

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ErrUnsafe is returned for a name that could leave its media directory or
// change meaning in a URL or playlist.
var ErrUnsafe = errors.New("unsafe media path")

// maxLen bounds a relative name; real ones are like "720p/seg_0042.ts".
const maxLen = 255

// Rel returns name if it is a safe slash-separated path relative to a media
// directory, else an error wrapping ErrUnsafe.
//
// Allowlist, not denylist: every element is one or more of [A-Za-z0-9._-]
// and does not start with ".". That excludes, by construction:
//
//   - "..", "." and hidden temp files (".master.m3u8.tmp-*", ".x.partial-*")
//   - absolute paths, empty elements ("a//b") and trailing slashes
//   - "%" (URL-encoded traversal once the name is in a URL, and an ffmpeg
//     output pattern), "?", "#", ":" (a URL scheme), "\", quotes
//   - NUL, CR/LF and every other control byte, and non-ASCII
func Rel(name string) (string, error) {
	if name == "" || len(name) > maxLen {
		return "", fmt.Errorf("%w: %q: length must be 1..%d", ErrUnsafe, name, maxLen)
	}
	for elem := range strings.SplitSeq(name, "/") {
		if elem == "" || elem[0] == '.' {
			return "", fmt.Errorf("%w: %q", ErrUnsafe, name)
		}
		for i := range len(elem) {
			if !allowed(elem[i]) {
				return "", fmt.Errorf("%w: %q: byte %q not allowed", ErrUnsafe, name, elem[i])
			}
		}
	}
	return name, nil
}

func allowed(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' ||
		c == '.' || c == '_' || c == '-'
}

// Join returns dir joined with a validated rel, for handing to an external
// tool (ffmpeg) that cannot be given an os.Root. dir is trusted (operator
// configuration or a directory this process created).
func Join(dir, rel string) (string, error) {
	if _, err := Rel(rel); err != nil {
		return "", err
	}
	return filepath.Join(dir, filepath.FromSlash(rel)), nil
}

// URL returns prefix + id + "/" + rel for media-delivery, or "" when rel is
// not safe. id must already be validated by the caller (a canonical UUID).
func URL(prefix, id, rel string) string {
	if _, err := Rel(rel); err != nil {
		return ""
	}
	return prefix + id + "/" + rel
}

// Root reads files under one media directory through os.Root.
type Root struct {
	root *os.Root
	dir  string // as passed to Open, for ExternalPath
}

// Open opens dir as a Root. dir is trusted; names passed to the methods are not.
func Open(dir string) (*Root, error) {
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("opening media dir: %w", err)
	}
	return &Root{root: r, dir: dir}, nil
}

// Close releases the directory handle.
func (r *Root) Close() error { return r.root.Close() }

// ReadFile reads name, which must pass Rel and must not escape the root.
func (r *Root) ReadFile(name string) ([]byte, error) {
	if _, err := Rel(name); err != nil {
		return nil, err
	}
	return r.root.ReadFile(filepath.FromSlash(name))
}

// Stat stats name, which must pass Rel and must not escape the root.
func (r *Root) Stat(name string) (fs.FileInfo, error) {
	if _, err := Rel(name); err != nil {
		return nil, err
	}
	return r.root.Stat(filepath.FromSlash(name))
}

// Entry checks that name is one directory entry as a listing returns it,
// such as a file someone dropped in the inbox ("My Movie (2020).mkv"). It is
// deliberately looser than Rel, so an Entry name is only ever used through a
// Root, where the kernel confines it: never in a URL, never handed to ffmpeg.
// It rejects "", ".", "..", any "/" and NUL.
func Entry(name string) error {
	if name == "" || name == "." || name == ".." || len(name) > maxLen ||
		strings.ContainsAny(name, "/\x00") {
		return fmt.Errorf("%w: entry %q", ErrUnsafe, name)
	}
	return nil
}

// at is entry inside dir: dir must pass Rel, entry must pass Entry.
func at(dir, entry string) (string, error) {
	if _, err := Rel(dir); err != nil {
		return "", err
	}
	if err := Entry(entry); err != nil {
		return "", err
	}
	return filepath.Join(filepath.FromSlash(dir), entry), nil
}

// MkdirAll creates dir (which must pass Rel) and any missing parents.
func (r *Root) MkdirAll(dir string, perm fs.FileMode) error {
	if _, err := Rel(dir); err != nil {
		return err
	}
	return r.root.MkdirAll(filepath.FromSlash(dir), perm)
}

// ReadDir lists dir, which must pass Rel. Entry names are whatever people
// called their files: use them only with the *Entry methods below.
func (r *Root) ReadDir(dir string) ([]fs.DirEntry, error) {
	if _, err := Rel(dir); err != nil {
		return nil, err
	}
	f, err := r.root.Open(filepath.FromSlash(dir))
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return f.ReadDir(-1)
}

// LstatEntry stats entry in dir without following a final symlink.
func (r *Root) LstatEntry(dir, entry string) (fs.FileInfo, error) {
	p, err := at(dir, entry)
	if err != nil {
		return nil, err
	}
	return r.root.Lstat(p)
}

// RenameEntry atomically renames fromDir/from to toDir/to. Like rename(2) it
// replaces an existing destination, so use it only when to is new (a fresh
// id); MoveEntryNoReplace keeps someone else's file.
func (r *Root) RenameEntry(fromDir, from, toDir, to string) error {
	src, err := at(fromDir, from)
	if err != nil {
		return err
	}
	dst, err := at(toDir, to)
	if err != nil {
		return err
	}
	return r.root.Rename(src, dst)
}

// MoveEntryNoReplace moves fromDir/from to toDir/to and fails with an error
// matching fs.ErrExist when the destination exists: a hard link (which
// refuses to replace) followed by removing the old name. Both names must be
// on the same filesystem, as they are under one Root.
func (r *Root) MoveEntryNoReplace(fromDir, from, toDir, to string) error {
	src, err := at(fromDir, from)
	if err != nil {
		return err
	}
	dst, err := at(toDir, to)
	if err != nil {
		return err
	}
	if err := r.root.Link(src, dst); err != nil {
		return err
	}
	return r.root.Remove(src)
}

// RemoveAll removes name, which must pass Rel, and everything under it,
// without following symlinks out of the root. A missing name is not an
// error. The watcher uses it for library/{id}/ of a deleted movie.
func (r *Root) RemoveAll(name string) error {
	if _, err := Rel(name); err != nil {
		return err
	}
	return r.root.RemoveAll(filepath.FromSlash(name))
}

// Remove removes the file (or empty directory) name, which must pass Rel; a
// final symlink is removed, not followed. A missing name is an error
// matching fs.ErrNotExist. Use it for names from the database, such as
// movies.source_path.
func (r *Root) Remove(name string) error {
	if _, err := Rel(name); err != nil {
		return err
	}
	return r.root.Remove(filepath.FromSlash(name))
}

// RemoveAllEntry removes entry (one name from ReadDir of dir, checked by
// Entry) and everything under it, like RemoveAll. It is for names Rel
// refuses but the media tree has, such as a ".{version}.partial-*" folder.
func (r *Root) RemoveAllEntry(dir, entry string) error {
	p, err := at(dir, entry)
	if err != nil {
		return err
	}
	return r.root.RemoveAll(p)
}

// ExternalPath returns the on-disk path of rel for an external tool (ffmpeg)
// that cannot be given the Root. rel must pass Rel and, checked through the
// Root, be a regular file inside it: not a symlink, not a directory, and not
// reached through a symlink that leaves the root. Use it for names from the
// database, such as movies.source_path.
func (r *Root) ExternalPath(rel string) (string, error) {
	if _, err := Rel(rel); err != nil {
		return "", err
	}
	fi, err := r.root.Lstat(filepath.FromSlash(rel))
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%w: %q is not a regular file", ErrUnsafe, rel)
	}
	return filepath.Join(r.dir, filepath.FromSlash(rel)), nil
}
