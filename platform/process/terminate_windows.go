//go:build windows

package process

import (
	"os/exec"
	"strconv"
	"syscall"
)

// terminate kills the command and its children. Kill alone stops cmd.exe but
// leaves the spawned program running when the shell started it directly.
func terminate(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	kill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
	kill.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := kill.Run(); err != nil {
		_ = cmd.Process.Kill()
	}
}
