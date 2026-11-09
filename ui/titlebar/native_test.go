package titlebar

import (
	"testing"

	"github.com/mslxl/natcatty/ui/locales"
)

// The other tests in this package cover the drawn controls of Linux and
// Windows, whatever platform runs them.
func init() { NativeControls = false }

// TestNativeControlsDrawNothingAndLeaveNoRightRoom covers macOS, where the
// window keeps its traffic lights: the app draws no controls of its own, and
// a bar keeps no room for them at its right end.
func TestNativeControlsDrawNothingAndLeaveNoRightRoom(t *testing.T) {
	NativeControls = true
	t.Cleanup(func() { NativeControls = false })
	m := locales.In("en")
	for _, style := range []Style{Tabs, Popup, Dialog} {
		tt := framed(style, &fakeWindow{}, 800, 500)
		for _, label := range []string{m.WindowMinimize(), m.WindowMaximize(), m.WindowClose()} {
			if tt.HasText(label) {
				t.Fatalf("style %d draws %q over native controls", style, label)
			}
		}
	}
}
