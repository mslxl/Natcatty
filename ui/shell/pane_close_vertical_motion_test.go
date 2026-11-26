package shell

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/motion"
	"github.com/mslxl/natcatty/ui/terminal/appview"
)

// TestPaneCloseVerticalSurvivorNeverScalesItsText is the vertical-split close
// regression: closing the bottom Pane grows the top survivor, and the native
// Match transition must lay the survivor out at each animated box rather than
// raster-scale its final paint. ScaleContent would draw the survivor's header
// and terminal smaller than life as the run starts and grow them back — the
// reported "the top Pane contracts" even though its box only grows.
func TestPaneCloseVerticalSurvivorNeverScalesItsText(t *testing.T) {
	g := motionGraph(t, paneClosePreferences(true, true, domainpreferences.MotionPopin))
	if err := g.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	waitForRealSurfaces(t, g.state, 1)
	view, wm := viewWithPaneMotion(g.state, paneMotionMode{labels: true})
	tt := ui.NewTester(view, 900, 600)
	tt.SetLocale("en")
	settlePane(t, tt, g, 0)

	if err := g.state.SplitDown(); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	waitForRealSurfaces(t, g.state, 2)
	tt.Frame()
	tt.Advance(400 * time.Millisecond)
	tt.Frame()
	if wm.run != nil {
		t.Fatalf("the split's open run did not finish")
	}
	pumpUntil(t, tt, "the settled split grids", func() bool {
		return gridsSettled(paneSurfaces(t, g, 0)...)
	})

	closedID, ok := g.state.FocusedPaneID(0)
	if !ok {
		t.Fatal("the vertical split has no focused Pane")
	}
	ids := motionPaneIDsMust(t, g)
	if len(ids) != 2 {
		t.Fatalf("the vertical split holds %d Panes, want 2", len(ids))
	}
	survivorID := ids[0]
	if survivorID == closedID {
		survivorID = ids[1]
	}
	if ids[0] != survivorID {
		t.Fatalf("the survivor %q is not the first Pane %q, so PaneRegion(1) is not it", survivorID, ids[0])
	}
	old, ok := wm.rects[survivorID]
	if !ok || !old.Valid() {
		t.Fatalf("the survivor has no boxes: %+v", old)
	}
	m := locales.In("en")
	glyph := m.PaneMenuGlyph()
	menu, found := tt.Find(glyph)
	if !found {
		t.Fatalf("the survivor's header menu %q is not shown", glyph)
	}
	if menu.Y < old.Column.Y || menu.Y+menu.H > old.Body.Y+1 {
		t.Fatalf("the menu %q at %+v is not in the survivor's header above %v", glyph, menu, old.Body)
	}
	survivorSurface := surfaceForPane(t, g, 0, survivorID)
	survivorSurface.ResetResizeLog()

	if !g.state.CloseFocusedPane() {
		t.Fatal("CloseFocusedPane returned false")
	}
	tt.Frame()
	run := wm.run
	if run == nil || !run.native || run.kind != motion.PaneClose {
		t.Fatalf("the vertical close did not start a native close run: %+v", run)
	}
	end := run.final[survivorID]
	if end.Column.H <= old.Column.H {
		t.Fatalf("the survivor's final column %+v does not grow past %+v", end.Column, old.Column)
	}
	transition, ok := wm.nativeTransition(survivorID)
	if !ok {
		t.Fatalf("the survivor has no native transition")
	}
	if transition.ScaleContent || !transition.Position || !transition.Size ||
		transition.Duration != 150*time.Millisecond ||
		transition.Match != (motion.PaneMatch{ID: survivorID}) {
		t.Fatalf("native transition = %+v, want a 150ms Match move laid out at each box of %q",
			transition, survivorID)
	}

	// Every frame of the run: the box grows toward the final one and the
	// header's own layout stays at its rest size. A raster-scaled Pane would
	// shrink the menu along with the text and grow it back.
	gridCalls := survivorSurface.GridForCalls()
	last := old.Column.H
	for frame := 0; ; frame++ {
		box, ok := tt.Find(glyph)
		if !ok {
			t.Fatalf("the survivor's header menu vanished at frame %d", frame)
		}
		if abs32(box.W-menu.W) > 0.5 || abs32(box.H-menu.H) > 0.5 {
			t.Fatalf("header menu at frame %d = %+v, want its rest box %+v; the Pane's content was scaled",
				frame, box, menu)
		}
		region, ok := tt.Find(m.PaneRegion(1))
		if !ok {
			t.Fatalf("the survivor has no PaneRegion label at frame %d", frame)
		}
		if region.H < last-0.5 {
			t.Fatalf("the survivor's box at frame %d = %+v shrank past %v; the close must only grow it",
				frame, region, last)
		}
		if region.H > end.Column.H+1 {
			t.Fatalf("the survivor's box at frame %d = %+v overshoots its final %+v",
				frame, region, end.Column)
		}
		last = region.H
		if wm.run != nil {
			// The pin is spent by the layout itself once the body reports the
			// final room, which is the last flying frame.
			if flying := region.H < end.Column.H-1; flying && !survivorSurface.viewportPinned {
				t.Fatalf("the survivor's final viewport is not pinned at frame %d", frame)
			}
			if got := survivorSurface.GridForCalls(); got != gridCalls {
				t.Fatalf("frame %d computed %d grids; the pin must keep animation frames free of grid math",
					frame, got-gridCalls)
			}
		}
		if wm.run == nil {
			break
		}
		tt.Advance(16 * time.Millisecond)
		tt.Frame()
	}

	tt.Advance(300 * time.Millisecond)
	tt.Frame()
	if wm.run != nil {
		t.Fatalf("the native close did not finish")
	}
	if got := len(survivorSurface.ResizeLog()); got != 1 {
		t.Fatalf("the vertical close delivered %d survivor resizes, want 1: %+v",
			got, survivorSurface.ResizeLog())
	}
	want, ok := appview.GridFor(survivorSurface.Viewport())
	if !ok || survivorSurface.Grid() != want {
		t.Fatalf("the survivor's final grid = %+v, want %+v", survivorSurface.Grid(), want)
	}
	if region, ok := tt.Find(m.PaneRegion(1)); !ok || !rectNearOK(region, end.Column, 1) {
		t.Fatalf("the settled survivor box = %+v, want %+v", region, end.Column)
	}
}
