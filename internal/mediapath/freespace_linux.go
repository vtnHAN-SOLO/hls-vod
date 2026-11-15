//go:build linux

package mediapath

import (
	"fmt"
	"path/filepath"
	"syscall"
)

// FreeBytes is the space an unprivileged writer can still use on the
// filesystem holding dir (which must pass Rel), as statfs reports it.
func (r *Root) FreeBytes(dir string) (uint64, error) {
	if _, err := Rel(dir); err != nil {
		return 0, err
	}
	f, err := r.root.Open(filepath.FromSlash(dir))
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	var st syscall.Statfs_t
	if err := syscall.Fstatfs(int(f.Fd()), &st); err != nil { // #nosec G115 -- a file descriptor fits in an int
		return 0, fmt.Errorf("statfs %s: %w", dir, err)
	}
	if st.Bsize <= 0 {
		return 0, fmt.Errorf("statfs %s: block size %d", dir, st.Bsize)
	}
	return st.Bavail * uint64(st.Bsize), nil
}
