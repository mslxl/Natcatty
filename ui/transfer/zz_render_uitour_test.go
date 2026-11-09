//go:build uitour

package transfer

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"

	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
	uitheme "github.com/mslxl/natcatty/ui/theme"
	transferstate "github.com/mslxl/natcatty/ui/transfer/state"
)

// TestRenderUITour writes offscreen PNGs of the SFTP surface over temporary
// directories, inside a card like the shell's, for a visual review.
func TestRenderUITour(t *testing.T) {
	out := os.Getenv("NATCATTY_SCREENSHOT_DIR")
	if out == "" {
		t.Skip("set NATCATTY_SCREENSHOT_DIR")
	}
	for _, dark := range []bool{false, true} {
		f := newViewFixture(t, 1100, 680, "en")
		preset := domaintheme.ResolvePreset(dark, domaintheme.DefaultLightTheme, domaintheme.DefaultDarkTheme)
		theme := uitheme.UI(dark, preset, uitheme.Options{})
		view := View(f.state)
		tt := ui.NewTester(func(c *ui.Context) {
			c.SetTheme(theme)
			t := c.Theme()
			ui.Column(c).Fill().Padding(8).Background(t.Background).Children(func() {
				ui.Column(c).Fill().Padding(8).Background(t.Surface).Border(1, t.Border).Radius(10).Clip().
					Children(func() { view(c) })
			})
		}, 1100, 680)
		tt.SetLocale("en")
		f.state.Focus(transferstate.Left)
		tt.Frame()
		tt.Frame()
		name := "sftp-light"
		if dark {
			name = "sftp-dark"
		}
		file, err := os.Create(filepath.Join(out, name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(file, tt.Image()); err != nil {
			t.Fatal(err)
		}
		file.Close()
	}
}
