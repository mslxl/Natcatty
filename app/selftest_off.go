//go:build !selftest

package app

import (
	sessionui "github.com/mslxl/natcatty/ui/session"
	"github.com/mslxl/natcatty/ui/shell/state"
)

// installSelftest is a no-op in normal builds: the F12 self-test facility,
// its checks, and its result files exist only under the selftest build tag.
func installSelftest(*windowManager, *state.State, func()) {}

// sessionSurfaceOptions adds nothing in normal builds, so the shipped app
// links none of the self-test key bridge.
func sessionSurfaceOptions() []sessionui.Option { return nil }
