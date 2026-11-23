package appview

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/terminal/surface"
)

// The grid's cell is measured and drawn with the terminal's configured font,
// not a hardcoded "monospace" and not the UI theme's size: the rows are
// terminal text, and they must look the same as the plugin's own grid.
func TestCellSizeFollowsTheTerminalFont(t *testing.T) {
	t.Cleanup(func() { surface.SetTerminalFont("", 0) })
	measure := func(family string, size float32) (w, h float32) {
		surface.SetTerminalFont(family, size)
		tester := ui.NewTester(func(c *ui.Context) { measureCell(c) }, 400, 160)
		tester.Frame()
		return CellSize()
	}
	wSmall, hSmall := measure("Menlo, monospace", 11)
	wLarge, hLarge := measure("Menlo, monospace", 22)
	if wSmall <= 0 || hSmall <= 0 {
		t.Fatalf("the cell was not measured: %.2f x %.2f", wSmall, hSmall)
	}
	if wLarge <= wSmall || hLarge <= hSmall {
		t.Fatalf("the cell did not grow with the configured size: 11pt %.2fx%.2f, 22pt %.2fx%.2f",
			wSmall, hSmall, wLarge, hLarge)
	}
	if got := spanFont(ui.Span{Text: "M"}, cellFontSize); got.Family != "Menlo, monospace" || got.Size != 22 {
		t.Errorf("span font = %q at %.1f, want the configured terminal font", got.Family, got.Size)
	}
	// A span that names its own family still wins.
	if got := spanFont(ui.Span{Text: "M", Font: "Courier New"}, cellFontSize); got.Family != "Courier New" {
		t.Errorf("a span's own family was overridden: %q", got.Family)
	}
}
