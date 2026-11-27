package blockview

import (
	"fmt"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/application/lens/lens"
	"github.com/mslxl/natcatty/application/lens/model"
)

// A raw body and a plain cell are flow text, as tall as their lines: neither
// gets a scroll of its own inside the flow. The raw body used to sit in a 260
// DIP box (and a cell past 40 lines in one too), so everything past the box was
// reachable only through a second scrollbar inside the flow's own.
func TestLongOutputGrowsWithTheFlow(t *testing.T) {
	var out strings.Builder
	for i := 0; i < 120; i++ {
		fmt.Fprintf(&out, "line %03d\r\n", i)
	}
	out.WriteString("last-line")
	// "raw" is a lens block switched to its raw body; "cell" is a command no
	// lens claimed. Both go through RenderCommand, as the Pane draws them, so a
	// cap or clip reintroduced above the row drawing is caught too.
	for name, native := range map[string]bool{"raw": true, "cell": false} {
		t.Run(name, func(t *testing.T) {
			store := model.NewStore(nil)
			// The stream opens with the line break that ended the prompt row,
			// as the tap captures it; rawLines drops it.
			store.Begin(model.Cmd{Line: "seq 1 120", Typed: "seq 1 120"})
			store.Append([]byte("\n" + out.String()))
			store.End(0)
			if native {
				id := store.LastID()
				store.SetNative(id, "natcatty-test", nil, lens.Text{Spans: []lens.Span{{Text: "native"}}}, nil)
				store.SetMode(id, model.ViewRaw)
			}
			blocks := store.Snapshot()
			var scroll ui.ScrollState
			tester := ui.NewTester(func(c *ui.Context) {
				ui.Scroll(c).Fill().TrackScroll(&scroll).Children(func() {
					ui.Column(c).Padding(10).Children(func() {
						RenderCommand(c, store, nil, &blocks[0], ui.Rect{}, func(string) {}, nil)
					})
				})
			}, 600, 300)
			tester.Frame()
			tester.Frame()
			// 120 rows are well over the 260 DIP box a viewport gave them, so a
			// body that follows its content reports far more scroll than the cap
			// would allow.
			if scroll.MaxY < 800 {
				t.Fatalf("%s output reports MaxY=%v, want the flow to scroll it", name, scroll.MaxY)
			}
			// The end is reachable by the flow alone: scrolled to the end, the
			// last line has a visible box inside the window. Behind an inner
			// scroll it stays clipped out (an empty visible rect) however far the
			// flow scrolls.
			scroll.Y = scroll.MaxY
			tester.Frame()
			tester.Frame()
			last, ok := tester.Find("last-line")
			if !ok || last.W <= 0 || last.H <= 0 {
				t.Fatalf("%s output's last line is not reachable by the flow: %v (line %v)", name, tester.Texts(), last)
			}
			if last.Y < 0 || last.Y+last.H > 300 {
				t.Fatalf("%s output's last line is at %v after scrolling to the end, want it inside the 300 DIP window", name, last)
			}
		})
	}
}

// Raw views and non-lens commands must never truncate long terminal output;
// only native lens blocks may fold/truncate their own views.
func TestRawAndCellOutputNeverTruncatePast500Lines(t *testing.T) {
	const total = 800
	var out strings.Builder
	for i := 0; i < total; i++ {
		fmt.Fprintf(&out, "row-%04d\n", i)
	}
	for name, native := range map[string]bool{"raw": true, "cell": false} {
		t.Run(name, func(t *testing.T) {
			store := model.NewStore(nil)
			store.Begin(model.Cmd{Line: "seq 800", Typed: "seq 800"})
			store.Append([]byte("\n" + out.String()))
			store.End(0)
			if native {
				id := store.LastID()
				store.SetNative(id, "natcatty-test", nil, lens.Text{Spans: []lens.Span{{Text: "native"}}}, nil)
				store.SetMode(id, model.ViewRaw)
			}
			blocks := store.Snapshot()
			tester := ui.NewTester(func(c *ui.Context) {
				ui.Scroll(c).Fill().Children(func() {
					ui.Column(c).Padding(10).Children(func() {
						RenderCommand(c, store, nil, &blocks[0], ui.Rect{}, func(string) {}, nil)
					})
				})
			}, 600, 300)
			tester.Frame()
			tester.Frame()
			texts := tester.Texts()
			joined := strings.Join(texts, "\n")
			if !strings.Contains(joined, "row-0799") {
				t.Fatalf("%s output truncated before final row-0799 (got %d text items)", name, len(texts))
			}
			if strings.Contains(joined, "output limit") || strings.Contains(joined, "taken out") {
				t.Fatalf("%s output unexpectedly showed truncation notice", name)
			}
		})
	}
}
