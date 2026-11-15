//go:build !linux

package ffmpeg

import "os/exec"

// killWithParent is Linux-only (Pdeathsig); elsewhere an orphaned ffmpeg
// runs to completion. Production runs on Linux.
func killWithParent(*exec.Cmd) {}
