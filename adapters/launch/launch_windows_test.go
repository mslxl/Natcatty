//go:build windows

package launch

import (
	"os/exec"
	"testing"
)

// TestDetachedCommandsHideTheirConsole proves a directly started external
// application does not open a console window over the app.
func TestDetachedCommandsHideTheirConsole(t *testing.T) {
	command := exec.Command("notepad.exe")
	hideConsole(command)
	if command.SysProcAttr == nil || !command.SysProcAttr.HideWindow {
		t.Fatalf("SysProcAttr = %+v, want HideWindow", command.SysProcAttr)
	}
}
