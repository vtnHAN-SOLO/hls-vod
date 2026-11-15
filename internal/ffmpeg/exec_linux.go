package ffmpeg

import (
	"os/exec"
	"syscall"
)

// killWithParent makes the kernel SIGKILL ffmpeg if the transcoder dies,
// even by SIGKILL/OOM. Without it an orphaned ffmpeg keeps writing into the
// partial output dir that the next run is about to sweep.
//
// Pdeathsig fires when the spawning OS thread exits. Go only ends threads
// whose goroutine called runtime.LockOSThread and returned; nothing in this
// package locks threads, so the signal fires only when the process dies.
func killWithParent(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}
