package shell

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/domain/model"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
	"github.com/mslxl/natcatty/ui/motion"
)

// paneRectsNear compares two predicted box sets edge by edge.
func paneRectsNear(got, want motion.PaneRects, eps float32) bool {
	same := func(a, b ui.Rect) bool {
		return abs32(a.X-b.X) <= eps && abs32(a.Y-b.Y) <= eps &&
			abs32(a.W-b.W) <= eps && abs32(a.H-b.H) <= eps
	}
	return same(got.Slot, want.Slot) && same(got.Column, want.Column) &&
		same(got.Body, want.Body) && got.Framed == want.Framed
}

// TestPaneFinalRectsPredictsTheFinalBoxes pins the pin prediction the motion
// runtime uses at a change: half, asymmetric, and nested splits; a Pane that
// was already framed; and the native frame's top-only inset. A wrong body here
// would pin a wrong grid and reflow the terminal. The seven cases are the
// shapes that differ: direction, ratio, nesting, previous frame, and style.
func TestPaneFinalRectsPredictsTheFinalBoxes(t *testing.T) {
	pane := func(id model.PaneID) *domainworkspace.Node { return domainworkspace.NewPaneNode(id) }
	split := func(id model.SplitID, direction domainworkspace.Direction, ratio float32,
		first, second *domainworkspace.Node) *domainworkspace.Node {
		return domainworkspace.NewSplitNode(id, direction, ratio, first, second)
	}
	// A single Pane's measured boxes: the outer column, the inner Pane column
	// (drop gutters of 4), and the body below a 24-DIP header with a 2-DIP
	// footer, unframed.
	plain := motion.PaneRects{
		Slot:   ui.Rect{X: 0, Y: 0, W: 900, H: 600},
		Column: ui.Rect{X: 4, Y: 4, W: 892, H: 592},
		Body:   ui.Rect{X: 4, Y: 28, W: 892, H: 566},
	}
	// The same Pane already inside a split's frame: the insets below a 1-DIP
	// border on every side and a 2-DIP content footer.
	framed := motion.PaneRects{
		Slot:   ui.Rect{X: 0, Y: 0, W: 900, H: 600},
		Column: ui.Rect{X: 4, Y: 4, W: 892, H: 592},
		Body:   ui.Rect{X: 5, Y: 29, W: 890, H: 563},
		Framed: true,
	}
	region := ui.Rect{X: 0, Y: 0, W: 900, H: 600}
	classic := func() (float32, float32, float32, float32) { return 1, 1, 1, 1 }
	native := func() (float32, float32, float32, float32) { return 2, 0, 0, 0 }
	cell := func(id model.PaneID, slot, column, body ui.Rect) motion.PaneRects {
		return motion.PaneRects{Slot: slot, Column: column, Body: body, Framed: true}
	}
	halfRight := func() *domainworkspace.Node {
		return split("s1", domainworkspace.Vertical, 50, pane("a"), pane("b"))
	}
	halfDown := func() *domainworkspace.Node {
		return split("s1", domainworkspace.Horizontal, 50, pane("a"), pane("b"))
	}
	for _, tc := range []struct {
		name     string
		root     *domainworkspace.Node
		rects    map[model.PaneID]motion.PaneRects
		fallback motion.PaneRects
		insets   func() (float32, float32, float32, float32)
		want     map[model.PaneID]motion.PaneRects
	}{
		{
			name: "split-right-half",
			root: halfRight(),
			rects: map[model.PaneID]motion.PaneRects{
				"a": plain,
			},
			fallback: plain,
			insets:   classic,
			want: map[model.PaneID]motion.PaneRects{
				"a": cell("a",
					ui.Rect{X: 0, Y: 0, W: 449.5, H: 600},
					ui.Rect{X: 4, Y: 4, W: 441.5, H: 592},
					ui.Rect{X: 5, Y: 29, W: 439.5, H: 564}),
				"b": cell("b",
					ui.Rect{X: 450.5, Y: 0, W: 449.5, H: 600},
					ui.Rect{X: 454.5, Y: 4, W: 441.5, H: 592},
					ui.Rect{X: 455.5, Y: 29, W: 439.5, H: 564}),
			},
		},
		{
			name: "split-down-half",
			root: halfDown(),
			rects: map[model.PaneID]motion.PaneRects{
				"a": plain,
			},
			fallback: plain,
			insets:   classic,
			want: map[model.PaneID]motion.PaneRects{
				"a": cell("a",
					ui.Rect{X: 0, Y: 0, W: 900, H: 299.5},
					ui.Rect{X: 4, Y: 4, W: 892, H: 291.5},
					ui.Rect{X: 5, Y: 29, W: 890, H: 263.5}),
				"b": cell("b",
					ui.Rect{X: 0, Y: 300.5, W: 900, H: 299.5},
					ui.Rect{X: 4, Y: 304.5, W: 892, H: 291.5},
					ui.Rect{X: 5, Y: 329.5, W: 890, H: 263.5}),
			},
		},
		{
			name: "split-right-asymmetric",
			root: split("s1", domainworkspace.Vertical, 70, pane("a"), pane("b")),
			rects: map[model.PaneID]motion.PaneRects{
				"a": plain,
			},
			fallback: plain,
			insets:   classic,
			want: map[model.PaneID]motion.PaneRects{
				"a": cell("a",
					ui.Rect{X: 0, Y: 0, W: 629.3, H: 600},
					ui.Rect{X: 4, Y: 4, W: 621.3, H: 592},
					ui.Rect{X: 5, Y: 29, W: 619.3, H: 564}),
				"b": cell("b",
					ui.Rect{X: 630.3, Y: 0, W: 269.7, H: 600},
					ui.Rect{X: 634.3, Y: 4, W: 261.7, H: 592},
					ui.Rect{X: 635.3, Y: 29, W: 259.7, H: 564}),
			},
		},
		{
			name: "nested-left-column",
			root: split("s1", domainworkspace.Vertical, 50,
				split("s2", domainworkspace.Horizontal, 50, pane("a"), pane("b")), pane("c")),
			rects: map[model.PaneID]motion.PaneRects{
				"a": plain,
			},
			fallback: plain,
			insets:   classic,
			want: map[model.PaneID]motion.PaneRects{
				"a": cell("a",
					ui.Rect{X: 0, Y: 0, W: 449.5, H: 299.5},
					ui.Rect{X: 4, Y: 4, W: 441.5, H: 291.5},
					ui.Rect{X: 5, Y: 29, W: 439.5, H: 263.5}),
				"b": cell("b",
					ui.Rect{X: 0, Y: 300.5, W: 449.5, H: 299.5},
					ui.Rect{X: 4, Y: 304.5, W: 441.5, H: 291.5},
					ui.Rect{X: 5, Y: 329.5, W: 439.5, H: 263.5}),
				"c": cell("c",
					ui.Rect{X: 450.5, Y: 0, W: 449.5, H: 600},
					ui.Rect{X: 454.5, Y: 4, W: 441.5, H: 592},
					ui.Rect{X: 455.5, Y: 29, W: 439.5, H: 564}),
			},
		},
		{
			name: "nested-bottom-row",
			root: split("s1", domainworkspace.Horizontal, 50, pane("a"),
				split("s2", domainworkspace.Vertical, 50, pane("b"), pane("c"))),
			rects: map[model.PaneID]motion.PaneRects{
				"a": plain,
			},
			fallback: plain,
			insets:   classic,
			want: map[model.PaneID]motion.PaneRects{
				"a": cell("a",
					ui.Rect{X: 0, Y: 0, W: 900, H: 299.5},
					ui.Rect{X: 4, Y: 4, W: 892, H: 291.5},
					ui.Rect{X: 5, Y: 29, W: 890, H: 263.5}),
				"b": cell("b",
					ui.Rect{X: 0, Y: 300.5, W: 449.5, H: 299.5},
					ui.Rect{X: 4, Y: 304.5, W: 441.5, H: 291.5},
					ui.Rect{X: 5, Y: 329.5, W: 439.5, H: 263.5}),
				"c": cell("c",
					ui.Rect{X: 450.5, Y: 300.5, W: 449.5, H: 299.5},
					ui.Rect{X: 454.5, Y: 304.5, W: 441.5, H: 291.5},
					ui.Rect{X: 455.5, Y: 329.5, W: 439.5, H: 263.5}),
			},
		},
		{
			name: "already-framed-keeps-its-border",
			root: halfRight(),
			rects: map[model.PaneID]motion.PaneRects{
				"a": framed,
			},
			fallback: framed,
			insets:   classic,
			want: map[model.PaneID]motion.PaneRects{
				"a": cell("a",
					ui.Rect{X: 0, Y: 0, W: 449.5, H: 600},
					ui.Rect{X: 4, Y: 4, W: 441.5, H: 592},
					ui.Rect{X: 5, Y: 29, W: 439.5, H: 563}),
				"b": cell("b",
					ui.Rect{X: 450.5, Y: 0, W: 449.5, H: 600},
					ui.Rect{X: 454.5, Y: 4, W: 441.5, H: 592},
					ui.Rect{X: 455.5, Y: 29, W: 439.5, H: 563}),
			},
		},
		{
			name: "native-frame-insets-only-the-top",
			root: halfRight(),
			rects: map[model.PaneID]motion.PaneRects{
				"a": plain,
			},
			fallback: plain,
			insets:   native,
			want: map[model.PaneID]motion.PaneRects{
				"a": cell("a",
					ui.Rect{X: 0, Y: 0, W: 449.5, H: 600},
					ui.Rect{X: 4, Y: 4, W: 441.5, H: 592},
					ui.Rect{X: 4, Y: 30, W: 441.5, H: 564}),
				"b": cell("b",
					ui.Rect{X: 450.5, Y: 0, W: 449.5, H: 600},
					ui.Rect{X: 454.5, Y: 4, W: 441.5, H: 592},
					ui.Rect{X: 454.5, Y: 30, W: 441.5, H: 564}),
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			frameTop, frameRight, frameBottom, frameLeft := tc.insets()
			got := paneFinalRects(tc.root, region, tc.rects, 10, tc.fallback, true,
				paneChrome{top: frameTop, right: frameRight, bottom: frameBottom, left: frameLeft})
			if len(got) != len(tc.want) {
				t.Fatalf("predicted %d Panes, want %d: %+v", len(got), len(tc.want), got)
			}
			for id, want := range tc.want {
				gotRects, ok := got[id]
				if !ok {
					t.Fatalf("Pane %q has no prediction", id)
				}
				if !paneRectsNear(gotRects, want, 0.05) {
					t.Fatalf("Pane %q = %+v, want %+v", id, gotRects, want)
				}
			}
		})
	}
}

// TestPaneSlotsFollowsSplitPercentAndMinimumPane checks the final-slot math
// against MyGo's SplitPercent: a one-DIP divider, the rest shared by the
// ratio, each side clamped to the minimum, and both sides at the minimum when
// the room cannot hold two of them.
func TestPaneSlotsFollowsSplitPercentAndMinimumPane(t *testing.T) {
	split := func(ratio float32) *domainworkspace.Node {
		return domainworkspace.NewSplitNode("split", domainworkspace.Vertical, ratio,
			domainworkspace.NewPaneNode("a"), domainworkspace.NewPaneNode("b"))
	}
	for _, tc := range []struct {
		name   string
		ratio  float32
		width  float32
		min    float32
		first  ui.Rect
		second ui.Rect
	}{
		{
			name: "half", ratio: 50, width: 900, min: 40,
			first:  ui.Rect{X: 0, Y: 0, W: 449.5, H: 600},
			second: ui.Rect{X: 450.5, Y: 0, W: 449.5, H: 600},
		},
		{
			name: "first minimum", ratio: 10, width: 200, min: 40,
			first:  ui.Rect{X: 0, Y: 0, W: 40, H: 600},
			second: ui.Rect{X: 41, Y: 0, W: 159, H: 600},
		},
		{
			name: "second minimum", ratio: 90, width: 200, min: 40,
			first:  ui.Rect{X: 0, Y: 0, W: 159, H: 600},
			second: ui.Rect{X: 160, Y: 0, W: 40, H: 600},
		},
		{
			name: "too little room", ratio: 50, width: 60, min: 40,
			first:  ui.Rect{X: 0, Y: 0, W: 40, H: 600},
			second: ui.Rect{X: 41, Y: 0, W: 40, H: 600},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			slots := map[model.PaneID]ui.Rect{}
			paneSlots(split(tc.ratio), ui.Rect{X: 0, Y: 0, W: tc.width, H: 600}, tc.min, slots)
			if got := slots["a"]; got != tc.first {
				t.Fatalf("first slot = %+v, want %+v", got, tc.first)
			}
			if got := slots["b"]; got != tc.second {
				t.Fatalf("second slot = %+v, want %+v", got, tc.second)
			}
		})
	}
}
