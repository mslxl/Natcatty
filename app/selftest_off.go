//go:build !selftest

package app

import "github.com/mslxl/natcatty/ui/shell/state"

// installSelftest is a no-op in normal builds: the F11 self-test facility,
// its checks, and its result files exist only under the selftest build tag.
func installSelftest(*windowManager, *state.State, *desktopController, func([]string, string)) {}
