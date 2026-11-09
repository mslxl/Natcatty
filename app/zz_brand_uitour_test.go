//go:build uitour

package app

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
)

// TestRenderBrandMarkUITour draws the sidebar's brand mark for two variants.
func TestRenderBrandMarkUITour(t *testing.T) {
	out := os.Getenv("NATCATTY_SCREENSHOT_DIR")
	if out == "" {
		t.Skip("set NATCATTY_SCREENSHOT_DIR")
	}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Fill().Padding(12).Gap(12).Children(func() {
			for _, id := range []string{"original", "white-rose"} {
				ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
					ui.Image(c, brandMarkBitmap(id)).Size(28, 28).Shrink(0)
					ui.Text(c, "Natcatty").FontSize(16).FontWeight(700)
				})
			}
		})
	}, 200, 110)
	tt.Frame()
	file, err := os.Create(filepath.Join(out, "brand-mark.png"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := png.Encode(file, tt.Image()); err != nil {
		t.Fatal(err)
	}
}
