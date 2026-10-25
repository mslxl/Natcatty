//go:build !windows

package launch

import (
	"errors"
	"os/exec"
)

// hideConsole is a no-op outside Windows, where a GUI launch does not open a
// console window.
func hideConsole(*exec.Cmd) {}

// shellOpen is unused outside Windows; OpenSystem uses `open` or `xdg-open`
// there. Keeping the symbol makes the construction site platform-neutral.
func shellOpen(string) error {
	return errors.New("launch: ShellExecute is only available on Windows")
}
