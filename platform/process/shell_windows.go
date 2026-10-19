//go:build windows

package process

import (
	"os/exec"
	"syscall"
)

// shellCommand builds the cmd.exe invocation. Go's default argument escaping
// turns the quotes inside command into \" , which cmd.exe does not recognize,
// so the command line is supplied verbatim. cmd /S strips exactly the outer
// pair of quotes and hands the rest to the command parser.
func shellCommand(command string) *exec.Cmd {
	cmd := exec.Command("cmd.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `/D /S /C "` + command + `"`}
	return cmd
}
