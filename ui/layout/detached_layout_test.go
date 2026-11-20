package layout

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// TestDetachedLayoutShrinksItsBodyByTheTitleBar renders a detached window
// whose body is shaped like a Pane tree: a Fill-height column holding content
// taller than the window. The body must end at the window's bottom, because
// the title bar takes its row out of it. A raw Fill-height body keeps its
// content's automatic minimum instead, and lays the Pane's flow (with the
// newest prompt and cursor at its end) below the window, where neither follow
// nor the wheel can reach it.
func TestDetachedLayoutShrinksItsBodyByTheTitleBar(t *testing.T) {
	const width, height = 400, 300
	var bodyBounds ui.Rect
	tt := ui.NewTester(func(c *ui.Context) {
		DetachedLayout(c, "detached", nil, func() {
			body := ui.Column(c).Fill()
			body.Children(func() {
				// The flow: taller than any window.
				ui.Box(c).Height(600).FillWidth()
			})
			// The layout box, not a label rect: label rects are clipped to
			// the window and would report an overflowing body as inside.
			bodyBounds = body.LayoutBounds()
		})
	}, width, height)
	tt.SetLocale("en")
	for i := 0; i < 2; i++ {
		tt.Frame()
	}
	if bodyBounds.W <= 0 || bodyBounds.H <= 0 {
		t.Fatalf("detached body was not laid out: %+v", bodyBounds)
	}
	if got := bodyBounds.Y + bodyBounds.H; got != float32(height) {
		t.Fatalf("detached body ends at %.1f in a %d-DIP window; the title bar's row must be taken out of the body", got, height)
	}
}
