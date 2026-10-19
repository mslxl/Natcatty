//go:build !windows

package process

import (
	"os/exec"
	"syscall"
)

// shellCommand builds the POSIX shell invocation. A dedicated process group
// lets Close terminate the shell and anything it spawned.
func shellCommand(command string) *exec.Cmd {
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd
}
