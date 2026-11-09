// Package styletest runs a UI test under every Interface Style.
package styletest

import (
	"testing"

	"github.com/mslxl/natcatty/ui/kit"
)

// Styles are the Interface Styles a layout test covers.
func Styles() []kit.Style { return []kit.Style{kit.Classic, kit.MacOS} }

// Each runs fn as a subtest per Interface Style, named by its ID, with that
// style active, and restores Classic after. The style is process-wide, so a
// test that uses Each must not run in parallel.
func Each(t *testing.T, fn func(t *testing.T)) {
	t.Helper()
	for _, style := range Styles() {
		t.Run(style.ID(), func(t *testing.T) {
			kit.SetStyle(style)
			t.Cleanup(func() { kit.SetStyle(nil) })
			fn(t)
		})
	}
}
