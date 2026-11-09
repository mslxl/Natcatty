// Package styletest runs a UI test under every Interface Style.
package styletest

import (
	"testing"

	"github.com/egoist/mygo/ui"

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

// Styled draws view in style, with the theme style derives from theme, as
// the app's window does. It leaves the style active; the test restores it.
func Styled(style kit.Style, theme *ui.Theme, view func(*ui.Context)) func(*ui.Context) {
	return func(c *ui.Context) {
		kit.SetStyle(style)
		c.SetTheme(style.Theme(theme))
		view(c)
	}
}
