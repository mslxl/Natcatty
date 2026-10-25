//go:build windows

package launch

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// hideConsole keeps a launched application from opening a console window over
// the app. ShellExecute already avoids one; a directly started external
// application would otherwise show a flash or a persistent console.
func hideConsole(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}

// shellOpen hands the path to the shell's default handler through
// ShellExecute, which is the same path Explorer uses and does not flash a
// console window.
func shellOpen(path string) error {
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	file, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL)
}
