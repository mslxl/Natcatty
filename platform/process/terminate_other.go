//go:build !windows

package process

import (
	"os/exec"
	"syscall"
)

// terminate kills the command's process group. The shell runs in its own group
// so the children it spawned receive the signal too.
func terminate(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		_ = cmd.Process.Kill()
	}
}
