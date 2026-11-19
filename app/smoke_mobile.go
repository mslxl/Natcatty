//go:build natcatty_smoke && (ios || android)

package app

import "github.com/mslxl/natcatty/ui/phoneshell"

// smokeStart opens the terminal diagnostic from Sessions. The smoke build
// has no UI driver: opening it here is what lets the simulator job assert
// the echo result from the log.
func smokeStart(shell *phoneshell.Shell) {
	shell.Select(phoneshell.Sessions)
	shell.Router(phoneshell.Sessions).Push("/diagnostic")
}
