package lensview

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/application/lens/lens"
	"github.com/mslxl/natcatty/ui/terminal/surface"
)

// A lens view is terminal text: its body inherits the terminal's configured
// font family and size, and the design system's character budgets are measured
// from that size. It used to be wrapped in a hardcoded "monospace" at the UI
// theme's size, so a block's text changed typeface and size next to the grid.
func TestLensViewUsesTheTerminalFont(t *testing.T) {
	t.Cleanup(func() { surface.SetTerminalFont("", 0) })
	surface.SetTerminalFont("Menlo, monospace", 17)
	if got := monoFamily(); got != "Menlo, monospace" {
		t.Errorf("monoFamily() = %q, want the configured terminal family", got)
	}

	height := func(size float32) float32 {
		surface.SetTerminalFont("Menlo, monospace", size)
		tester := ui.NewTester(func(c *ui.Context) {
			RenderScoped(c, lens.Text{Spans: []lens.Span{{Text: "hello"}}}, "font-test")
		}, 400, 200)
		tester.Frame()
		rect, ok := tester.Find("hello")
		if !ok {
			t.Fatalf("the body text was not drawn at %.0f pt", size)
		}
		return rect.H
	}
	if small, large := height(11), height(22); large <= small {
		t.Fatalf("the lens body did not follow the terminal size: 11pt %.1f, 22pt %.1f", small, large)
	}

	surface.SetTerminalFont("Menlo, monospace", 17)
	var s style
	tester := ui.NewTester(func(c *ui.Context) { s = styleFor(c) }, 400, 200)
	tester.Frame()
	if want := round(17 * 0.602); s.CharW != want {
		t.Errorf("CharW = %.2f, want %.2f from the terminal font size", s.CharW, want)
	}
}
