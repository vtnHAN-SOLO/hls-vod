package mediapath

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestRel(t *testing.T) {
	tests := []struct {
		name string
		in   string
		ok   bool
	}{
		{"file", "poster.jpg", true},
		{"rung playlist", "720p/index.m3u8", true},
		{"segment", "seg_0042.ts", true},
		{"subtitle", "subs/0_eng_forced.vtt", true},
		{"sprite", "thumbs/sprite_000.jpg", true},
		{"dots inside a name", "a..b.ts", true},

		// ../ traversal
		{"parent", "..", false},
		{"parent prefix", "../secret.ts", false},
		{"nested traversal", "720p/../../etc/passwd", false},
		{"trailing parent", "720p/..", false},
		{"backslash traversal", `..\secret.ts`, false},
		{"current dir", ".", false},
		{"dot prefix", "./poster.jpg", false},
		{"hidden temp file", ".master.m3u8.tmp-123", false},
		{"partial dir", ".movie.partial-1/poster.jpg", false},

		// absolute paths
		{"absolute", "/etc/passwd", false},
		{"double slash", "//etc/passwd", false},
		{"windows drive", `C:\Windows\win.ini`, false},
		{"url", "http://evil.example/x.m3u8", false},
		{"file url", "file:/etc/passwd", false},

		// URL-encoded traversal: harmless as a file name, but nginx would
		// decode it once the name is part of a /media/ URL.
		{"encoded dots", "%2e%2e/secret.ts", false},
		{"encoded slash", "..%2fsecret.ts", false},
		{"encoded both", "%2e%2e%2fsecret.ts", false},
		{"double encoded", "%252e%252e%252f", false},
		{"overlong utf-8 dot", "\xc0\xae\xc0\xae/secret.ts", false},
		{"fullwidth dot", "\uff0e\uff0e/secret.ts", false},

		// null bytes and other control bytes
		{"null byte", "poster.jpg\x00.png", false},
		{"null byte alone", "\x00", false},
		{"newline injection", "720p/index.m3u8\n#EXT-X-ENDLIST", false},
		{"carriage return", "a\r.ts", false},

		// URL and playlist meaning
		{"query", "poster.jpg?x=1", false},
		{"fragment", "poster.jpg#x", false},
		{"quote", `a".m3u8`, false},
		{"space", "my movie.ts", false},
		{"ffmpeg pattern", "seg_%04d.ts", false},

		// shape
		{"empty", "", false},
		{"empty element", "720p//index.m3u8", false},
		{"trailing slash", "720p/", false},
		{"too long", strings.Repeat("a", maxLen+1), false},
		{"max length", strings.Repeat("a", maxLen), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Rel(tt.in)
			if tt.ok {
				if err != nil || got != tt.in {
					t.Fatalf("Rel(%q) = %q, %v; want it accepted unchanged", tt.in, got, err)
				}
				return
			}
			if !errors.Is(err, ErrUnsafe) {
				t.Fatalf("Rel(%q) = %q, %v; want ErrUnsafe", tt.in, got, err)
			}
		})
	}
}

func TestJoinAndURL(t *testing.T) {
	const id = "0b9d6b52-2d2c-4a7b-9e0e-6a4f39a0c1d2"
	if got, err := Join("/mnt/media/library/x", "subs/0_eng.vtt"); err != nil ||
		got != filepath.Join("/mnt/media/library/x", "subs", "0_eng.vtt") {
		t.Fatalf("Join = %q, %v", got, err)
	}
	if got := URL("/media/", id, "master.m3u8"); got != "/media/"+id+"/master.m3u8" {
		t.Fatalf("URL = %q", got)
	}
	for _, bad := range []string{"../x", "/etc/passwd", "%2e%2e%2fx", "a\x00b", ""} {
		if got, err := Join("/mnt/media", bad); !errors.Is(err, ErrUnsafe) {
			t.Errorf("Join(%q) = %q, %v; want ErrUnsafe", bad, got, err)
		}
		if got := URL("/media/", id, bad); got != "" {
			t.Errorf("URL(%q) = %q, want \"\"", bad, got)
		}
	}
}

// Root must hold even for names Rel would accept: os.Root stops symlinks that
// leave the directory, which no string check can see.
func TestRoot(t *testing.T) {
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "secret.ts"), "secret")
	if err := os.Mkdir(filepath.Join(outside, "dir"), 0o750); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(outside, "dir", "secret.ts"), "secret")

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "poster.jpg"), "poster")
	if err := os.Mkdir(filepath.Join(dir, "720p"), 0o750); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "720p", "seg_0000.ts"), "segment")
	links := map[string]string{
		"abs-link.ts":     filepath.Join(outside, "secret.ts"),           // absolute target outside
		"rel-link.ts":     "../" + filepath.Base(outside) + "/secret.ts", // relative target outside
		"dirlink":         filepath.Join(outside, "dir"),                 // directory outside
		"inside-link.jpg": "poster.jpg",                                  // stays inside: allowed
		"720p/up-link.ts": "../poster.jpg",                               // climbs, but stays inside: allowed
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(dir, filepath.FromSlash(name))); err != nil {
			t.Fatal(err)
		}
	}

	root, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })

	tests := []struct {
		name string
		in   string
		want string // file contents; "" means the read must fail
	}{
		{"plain file", "poster.jpg", "poster"},
		{"nested file", "720p/seg_0000.ts", "segment"},
		{"symlink inside the root", "inside-link.jpg", "poster"},
		{"symlink climbing within the root", "720p/up-link.ts", "poster"},

		{"symlink to absolute path outside", "abs-link.ts", ""},
		{"symlink to relative path outside", "rel-link.ts", ""},
		{"through a directory symlink outside", "dirlink/secret.ts", ""},
		{"../ traversal", "../" + filepath.Base(outside) + "/secret.ts", ""},
		{"absolute path", filepath.Join(outside, "secret.ts"), ""},
		{"url-encoded traversal", "%2e%2e%2f" + filepath.Base(outside) + "%2fsecret.ts", ""},
		{"null byte", "poster.jpg\x00", ""},
		{"missing file", "nope.ts", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := root.ReadFile(tt.in)
			_, statErr := root.Stat(tt.in)
			if tt.want == "" {
				if err == nil || statErr == nil {
					t.Fatalf("ReadFile(%q) = %q, %v; Stat err %v; want both to fail", tt.in, data, err, statErr)
				}
				if strings.Contains(string(data), "secret") {
					t.Fatal("read a file outside the root")
				}
				return
			}
			if err != nil || string(data) != tt.want || statErr != nil {
				t.Fatalf("ReadFile(%q) = %q, %v; Stat err %v", tt.in, data, err, statErr)
			}
		})
	}

	// The OS layer on its own: a name Rel accepts that still escapes is
	// refused by os.Root, not merely by the string check.
	_, err = root.ReadFile("abs-link.ts")
	if errors.Is(err, ErrUnsafe) || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("symlink escape should be refused by os.Root, got %v", err)
	}
}

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

// banned are the calls that validate or confine an untrusted relative path.
// Outside this package they mean a second, hand-rolled implementation.
var banned = []string{
	"os.OpenRoot", "os.OpenInRoot",
	"path.Clean", "filepath.Clean", "filepath.IsLocal", "filepath.Localize",
	"filepath.FromSlash", "filepath.Rel", "filepath.EvalSymlinks", "fs.ValidPath",
}

// TestNoOtherConfinement fails if any non-test file outside internal/mediapath
// calls a path-confinement primitive, so the helper stays the only one.
func TestNoOtherConfinement(t *testing.T) {
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	self, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	scanned := 0
	err = filepath.WalkDir(repo, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p == self || strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules" || d.Name() == "web" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		scanned++
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		names := importNames(f)
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			if full, ok := names[pkg.Name]; ok && slices.Contains(banned, full+"."+sel.Sel.Name) {
				rel, _ := filepath.Rel(repo, p)
				found = append(found, rel+":"+strconv.Itoa(fset.Position(sel.Pos()).Line)+" "+full+"."+sel.Sel.Name)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned < 10 {
		t.Fatalf("scanned only %d files; is the repo root right?", scanned)
	}
	if len(found) > 0 {
		t.Fatalf("path confinement outside internal/mediapath (use mediapath.Rel/Join/URL/Root):\n  %s",
			strings.Join(found, "\n  "))
	}
}

// importNames maps each local import name to its short package path
// ("filepath" for path/filepath), so aliased imports are caught too.
func importNames(f *ast.File) map[string]string {
	short := map[string]string{"os": "os", "path": "path", "path/filepath": "filepath", "io/fs": "fs"}
	names := map[string]string{}
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		s, ok := short[p]
		if !ok {
			continue
		}
		local := s
		if imp.Name != nil {
			local = imp.Name.Name
		}
		names[local] = s
	}
	return names
}

func TestEntry(t *testing.T) {
	tests := []struct {
		name string
		ok   bool
	}{
		{"movie.mkv", true},
		{"My Movie (2020).mkv", true},
		{"Amélie – l'été.mp4", true},
		{"..hidden-but-not-dotdot", true},
		{"", false},
		{".", false},
		{"..", false},
		{"../movie.mkv", false},
		{"a/b.mkv", false},
		{"/etc/passwd", false},
		{"nul\x00.mkv", false},
		{strings.Repeat("a", 256), false},
	}
	for _, tt := range tests {
		err := Entry(tt.name)
		if (err == nil) != tt.ok {
			t.Errorf("Entry(%q) = %v, want ok=%v", tt.name, err, tt.ok)
		}
		if err != nil && !errors.Is(err, ErrUnsafe) {
			t.Errorf("Entry(%q) error %v does not wrap ErrUnsafe", tt.name, err)
		}
	}
}

// The entry operations take people's file names, so the confinement must
// come from os.Root and from dir passing Rel, never from the entry itself.
func TestRootEntries(t *testing.T) {
	outside := t.TempDir()
	dir := t.TempDir()
	root, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })

	if err := root.MkdirAll("inbox", 0o750); err != nil {
		t.Fatal(err)
	}
	if err := root.MkdirAll("sources", 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	const name = "My Movie (2020).mkv"
	writeFile(t, filepath.Join(dir, "inbox", name), "movie")

	t.Run("lists and stats real names", func(t *testing.T) {
		es, err := root.ReadDir("inbox")
		if err != nil || len(es) != 1 || es[0].Name() != name {
			t.Fatalf("ReadDir = %v, %v", es, err)
		}
		fi, err := root.LstatEntry("inbox", name)
		if err != nil || !fi.Mode().IsRegular() {
			t.Fatalf("LstatEntry = %v, %v", fi, err)
		}
	})

	t.Run("rename, then no-replace move back", func(t *testing.T) {
		if err := root.RenameEntry("inbox", name, "sources", "id.mkv"); err != nil {
			t.Fatal(err)
		}
		if _, err := root.LstatEntry("inbox", name); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("old name still there: %v", err)
		}
		// A losing racer sees "not found", which is how the watcher skips.
		if err := root.RenameEntry("inbox", name, "sources", "other.mkv"); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("second rename = %v, want fs.ErrNotExist", err)
		}

		writeFile(t, filepath.Join(dir, "inbox", name), "someone else's")
		if err := root.MoveEntryNoReplace("sources", "id.mkv", "inbox", name); !errors.Is(err, fs.ErrExist) {
			t.Fatalf("MoveEntryNoReplace over an existing file = %v, want fs.ErrExist", err)
		}
		if b, _ := os.ReadFile(filepath.Join(dir, "inbox", name)); string(b) != "someone else's" {
			t.Fatal("MoveEntryNoReplace replaced the existing file")
		}
		if err := root.MoveEntryNoReplace("sources", "id.mkv", "inbox", "id.mkv"); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(filepath.Join(dir, "inbox", "id.mkv")); string(b) != "movie" {
			t.Fatal("moved file has the wrong contents")
		}
		if es, _ := root.ReadDir("sources"); len(es) != 0 {
			t.Fatalf("sources not empty after the move: %v", es)
		}
	})

	t.Run("refuses to leave the root", func(t *testing.T) {
		bad := []struct{ dir, entry string }{
			{"inbox", "../../" + filepath.Base(outside) + "/x"},
			{"inbox", ".."},
			{"../" + filepath.Base(outside), "x"},
			{"/tmp", "x"},
			{"inbox", "x\x00"},
			{"escape", "x"}, // a symlinked dir pointing outside: refused by os.Root
		}
		for _, b := range bad {
			if err := root.RenameEntry("inbox", "id.mkv", b.dir, b.entry); err == nil {
				t.Errorf("RenameEntry to %q/%q succeeded", b.dir, b.entry)
			}
			if _, err := root.LstatEntry(b.dir, b.entry); err == nil {
				t.Errorf("LstatEntry(%q, %q) succeeded", b.dir, b.entry)
			}
		}
		if _, err := root.ReadDir("escape"); err == nil {
			t.Error("ReadDir through a symlink outside succeeded")
		}
		if err := root.MkdirAll("escape/x", 0o750); err == nil {
			t.Error("MkdirAll through a symlink outside succeeded")
		}
		if es, _ := os.ReadDir(outside); len(es) != 0 {
			t.Fatalf("something was written outside the root: %v", es)
		}
	})
}

// ExternalPath is how a database value (source_path) becomes an ffmpeg input.
func TestExternalPath(t *testing.T) {
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "secret.mkv"), "secret")
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sources"), 0o750); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "sources", "movie.mkv"), "movie")
	for name, target := range map[string]string{
		"sources/abs-link.mkv":    filepath.Join(outside, "secret.mkv"),
		"sources/inside-link.mkv": "movie.mkv",
		"escape":                  outside,
	} {
		if err := os.Symlink(target, filepath.Join(dir, filepath.FromSlash(name))); err != nil {
			t.Fatal(err)
		}
	}
	root, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })

	got, err := root.ExternalPath("sources/movie.mkv")
	if err != nil || got != filepath.Join(dir, "sources", "movie.mkv") {
		t.Fatalf("ExternalPath = %q, %v", got, err)
	}
	for _, bad := range []string{
		"sources/abs-link.mkv",    // symlink out of the root
		"sources/inside-link.mkv", // any symlink: the watcher never makes one
		"escape/secret.mkv",       // through a symlinked dir outside
		"sources",                 // a directory
		"sources/missing.mkv",
		"../" + filepath.Base(outside) + "/secret.mkv",
		filepath.Join(outside, "secret.mkv"),
		"sources/movie.mkv\x00",
		"%2e%2e%2fsecret.mkv",
	} {
		if p, err := root.ExternalPath(bad); err == nil {
			t.Errorf("ExternalPath(%q) = %q, want an error", bad, p)
		}
	}
}

// The watcher removes a deleted movie's library dir and source through
// these: unsafe names are refused, missing ones are fine (RemoveAll) or
// fs.ErrNotExist (Remove), and a symlink is removed, never followed out.
func TestRootRemove(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	mustWrite := func(p string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(filepath.Join(dir, "library", "m1", "v0", "seg.ts"))
	mustWrite(filepath.Join(dir, "sources", "m1.mkv"))
	mustWrite(filepath.Join(outside, "keep.txt"))
	if err := os.Symlink(outside, filepath.Join(dir, "library", "m2")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "keep.txt"), filepath.Join(dir, "sources", "m2.mkv")); err != nil {
		t.Fatal(err)
	}
	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })

	for _, name := range []string{"../x", "/etc", "library/../../x", "", "a\x00b"} {
		if err := r.RemoveAll(name); !errors.Is(err, ErrUnsafe) {
			t.Errorf("RemoveAll(%q) = %v", name, err)
		}
		if err := r.Remove(name); !errors.Is(err, ErrUnsafe) {
			t.Errorf("Remove(%q) = %v", name, err)
		}
	}
	if err := r.RemoveAll("library/m1"); err != nil {
		t.Fatal(err)
	}
	if err := r.RemoveAll("library/m1"); err != nil {
		t.Fatalf("RemoveAll of a missing dir = %v", err)
	}
	if err := r.Remove("sources/m1.mkv"); err != nil {
		t.Fatal(err)
	}
	if err := r.Remove("sources/m1.mkv"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Remove of a missing file = %v", err)
	}
	if err := r.RemoveAll("library/m2"); err != nil {
		t.Fatal(err)
	}
	if err := r.Remove("sources/m2.mkv"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(outside, "keep.txt")); err != nil {
		t.Fatalf("a file outside the root was removed through a symlink: %v", err)
	}
	for _, p := range []string{"library/m1", "library/m2", "sources/m1.mkv", "sources/m2.mkv"} {
		if _, err := os.Lstat(filepath.Join(dir, p)); !os.IsNotExist(err) {
			t.Errorf("%s still there: %v", p, err)
		}
	}
}
