package shell

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/domain/model"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/motion"
)

// newRowRuleReorderWindow builds eight maximum-width Tabs in a strip-only
// window at width and lets the strip rest, so a test can start a reorder and
// then change the row under it between two builds.
func newRowRuleReorderWindow(t *testing.T, width int) (*followGraph, *ui.Tester, *workTabReveal, []model.TabID) {
	t.Helper()
	setting := tabStripPreferences()
	g := newTabStripGraph(t, &setting)
	ids := addStripTabs(t, g.state, 8)
	for index := range 8 {
		if !g.state.RenameTab(index, maxWidthTabTitle) {
			t.Fatalf("RenameTab(%d) failed", index)
		}
	}
	reveal := newWorkTabReveal()
	reveal.labels = true
	tt := ui.NewTester(steppingStripView(stripOnlyView(g.state, reveal)), width, 480)
	tt.SetLocale("en")
	tt.Advance(400 * time.Millisecond)
	if reveal.strip.run != nil {
		t.Fatalf("the reorder window does not rest: %+v", reveal.strip.run)
	}
	if !reveal.strip.row.valid || len(reveal.strip.row.tabs) != 8 {
		t.Fatalf("the resting row record = %+v, want eight tabs", reveal.strip.row)
	}
	if reveal.scroll.MaxX <= 0.5 {
		t.Fatalf("the eight-Tab strip does not overflow at %d (MaxX=%v)", width, reveal.scroll.MaxX)
	}
	return g, tt, reveal, ids
}

// startRowRuleReorder moves ids[0] after ids[6] and consumes the event paint,
// leaving the strip mid-run with the offset at the row's maximum so any later
// row shrink must clamp it.
func startRowRuleReorder(t *testing.T, g *followGraph, tt *ui.Tester, reveal *workTabReveal, ids []model.TabID) {
	t.Helper()
	reveal.scroll.X = reveal.scroll.MaxX
	tt.Advance(17 * time.Millisecond)
	if !g.state.MoveTab(ids[0], ids[6], true) {
		t.Fatalf("MoveTab failed")
	}
	tt.Advance(17 * time.Millisecond)
	stripEvent(t, g.state, motion.TabReorder)
	if reveal.strip.run == nil || reveal.strip.run.kind != motion.TabReorder {
		t.Fatalf("the reorder did not start a run: %+v", reveal.strip.run)
	}
	tt.Advance(30 * time.Millisecond)
	if reveal.strip.run == nil {
		t.Fatalf("the reorder ended before the row change; the case needs it mid-run")
	}
}

// assertRowChangeCancelled proves one build on the row-change frame ended the
// run, warped, asked for no animation frame, clamped the offset to the new
// maximum at once, and left the watched boxes final and stable.
func assertRowChangeCancelled(t *testing.T, tt *ui.Tester, reveal *workTabReveal,
	watched []model.TabID, preBuilds, preFrames int, preX float32) {
	t.Helper()
	if got := reveal.strip.frames - preBuilds; got != 1 {
		t.Fatalf("the row change built %d paints, want exactly its event paint's one", got)
	}
	if reveal.strip.run != nil {
		t.Fatalf("the row change left the strip running: %+v", reveal.strip.run)
	}
	if !reveal.strip.warp {
		t.Fatalf("the row change did not warp the strip")
	}
	if got := reveal.strip.animationFrames - preFrames; got != 0 {
		t.Fatalf("the row change asked for %d strip frames, want none", got)
	}
	postMax := reveal.scroll.MaxX
	if preX <= postMax+0.5 {
		t.Fatalf("the row change did not clamp the offset (X=%v postMax=%v); the case needs the jump",
			preX, postMax)
	}
	if got := reveal.scroll.X; abs32(got-postMax) > 1 {
		t.Fatalf("the row change rests at X=%v but MaxX=%v, want the new maximum", got, postMax)
	}
	boxes := make([]ui.Rect, len(watched))
	for index, id := range watched {
		boxes[index] = stripTravel(t, tt, id)
	}
	eventX, eventMax := reveal.scroll.X, reveal.scroll.MaxX
	tt.Advance(400 * time.Millisecond)
	if reveal.strip.run != nil {
		t.Fatalf("the warped row change left a run after settling: %+v", reveal.strip.run)
	}
	if abs32(reveal.scroll.X-eventX) > 1 || abs32(reveal.scroll.MaxX-eventMax) > 1 {
		t.Fatalf("the settled strip scrolls to X=%v MaxX=%v, want the event paint's X=%v MaxX=%v",
			reveal.scroll.X, reveal.scroll.MaxX, eventX, eventMax)
	}
	for index, id := range watched {
		if got := stripTravel(t, tt, id); !rectNearOK(boxes[index], got, 1) {
			t.Fatalf("the watched Tab %q moved from %+v to %+v after the row change",
				id, boxes[index], got)
		}
	}
}

// TestTabStripReorderAfterRowLossWarpsOnTheEventPaint covers a reorder that
// arrives in the same update as a close or a detach. The state keeps only the
// latest motion event, so the TabReorder is the event the strip consumes; the
// committed row still holds every Tab while the state no longer does. The
// reorder's layout then removes a Tab and clamps the scroll offset under the
// slide, and the close clamp's one-Tab subtraction cannot see the removal.
// The row rule must reject a reorder that is not a permutation of the
// committed row and warp on the event paint: no run, no animation frame, the
// offset at the new maximum, and stable boxes.
func TestTabStripReorderAfterRowLossWarpsOnTheEventPaint(t *testing.T) {
	for _, style := range []struct {
		name string
		s    kit.Style
		gap  float32
	}{
		{"classic", kit.Classic, 0},
		{"macos", kit.MacOS, 4},
	} {
		t.Run(style.name, func(t *testing.T) {
			for _, tc := range []struct {
				name    string
				mutate  func(t *testing.T, w *closeClampWindow)
				watched []int
				moved   int
			}{
				{
					name: "close one Tab then reorder another",
					mutate: func(t *testing.T, w *closeClampWindow) {
						t.Helper()
						if !w.graph.state.CloseTab(1) {
							t.Fatalf("CloseTab failed")
						}
						if !w.graph.state.MoveTab(w.ids[6], w.ids[0], false) {
							t.Fatalf("MoveTab failed")
						}
					},
					watched: []int{5, 7},
					moved:   6,
				},
				{
					name: "detach a Tab then reorder another",
					mutate: func(t *testing.T, w *closeClampWindow) {
						t.Helper()
						if !w.graph.state.DetachTab(6) {
							t.Fatalf("DetachTab failed")
						}
						if !w.graph.state.MoveTab(w.ids[7], w.ids[0], false) {
							t.Fatalf("MoveTab failed")
						}
					},
					watched: []int{5, 7},
					moved:   7,
				},
			} {
				t.Run(tc.name, func(t *testing.T) {
					w := newCloseClampWindow(t, style.s, style.gap)
					// Scroll to the end first: the row loss must clamp the
					// offset, so a run left playing would slide every item.
					w.reveal.scroll.X = w.reveal.scroll.MaxX
					w.tester.Advance(17 * time.Millisecond)
					preMax := w.reveal.scroll.MaxX
					if preMax <= 1 {
						t.Fatalf("the strip does not scroll (MaxX=%v); the case needs the clamp", preMax)
					}
					builds, frames := w.bounds()

					tc.mutate(t, w)
					event := stripEvent(t, w.graph.state, motion.TabReorder)
					if event.TabID != w.ids[tc.moved] || event.TargetTabID != w.ids[0] {
						t.Fatalf("the reorder event = %+v, want %q before %q",
							event, w.ids[tc.moved], w.ids[0])
					}
					w.tester.Advance(17 * time.Millisecond)
					w.assertEventPaintWarp(t, builds, frames)
					postMax := w.reveal.scroll.MaxX
					if preMax <= postMax+0.5 {
						t.Fatalf("the row loss did not shrink the content (preMax=%v postMax=%v)",
							preMax, postMax)
					}
					if got := w.reveal.scroll.X; abs32(got-postMax) > 1 {
						t.Fatalf("the reorder after the row loss rests at X=%v but MaxX=%v", got, postMax)
					}
					watched := make([]model.TabID, len(tc.watched))
					for index, id := range tc.watched {
						watched[index] = w.ids[id]
					}
					boxes := make([]ui.Rect, len(watched))
					for index, id := range watched {
						boxes[index] = stripTravel(t, w.tester, id)
					}
					eventX, eventMax := w.reveal.scroll.X, w.reveal.scroll.MaxX
					w.tester.Advance(400 * time.Millisecond)
					if w.reveal.strip.run != nil {
						t.Fatalf("the warped reorder left a run after settling: %+v", w.reveal.strip.run)
					}
					if abs32(w.reveal.scroll.X-eventX) > 1 || abs32(w.reveal.scroll.MaxX-eventMax) > 1 {
						t.Fatalf("the settled strip scrolls to X=%v MaxX=%v, want the event paint's X=%v MaxX=%v",
							w.reveal.scroll.X, w.reveal.scroll.MaxX, eventX, eventMax)
					}
					for index, id := range watched {
						if got := stripTravel(t, w.tester, id); !rectNearOK(boxes[index], got, 1) {
							t.Fatalf("the watched Tab %q jumped from %+v to %+v after the row loss and reorder",
								id, boxes[index], got)
						}
					}
				})
			}
		})
	}
}

// TestTabStripRowChangeCancelsLiveReorder covers a row that changes with no
// strip event while a reorder is playing: detaching the Tab the run is moving,
// and shortening a survivor's title. The layout shrinks MaxX after the run's
// event frame and clamps the offset under it; the general row rule must end
// the run and warp on the change's paint, with no animated clamp. The control
// proves the rule does not touch a pure reorder: it still runs its full 180 ms
// and reaches the final layout.
func TestTabStripRowChangeCancelsLiveReorder(t *testing.T) {
	kit.SetStyle(kit.Classic)
	t.Cleanup(func() { kit.SetStyle(kit.Classic) })

	for _, tc := range []struct {
		name    string
		mutate  func(t *testing.T, g *followGraph, ids []model.TabID)
		watched []int
	}{
		{
			name: "detach the moving Tab during the run",
			mutate: func(t *testing.T, g *followGraph, ids []model.TabID) {
				t.Helper()
				// The reorder put ids[0] after ids[6], so index 6 is the
				// moving Tab.
				if !g.state.DetachTab(6) {
					t.Fatalf("DetachTab failed")
				}
			},
			watched: []int{1, 7},
		},
		{
			name: "shorten a survivor's title during the run",
			mutate: func(t *testing.T, g *followGraph, ids []model.TabID) {
				t.Helper()
				// The reorder's order is [ids1..ids6, ids0, ids7]; index 0 is
				// a maximum-width survivor.
				if !g.state.RenameTab(0, "a") {
					t.Fatalf("RenameTab failed")
				}
			},
			watched: []int{0, 7},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, tt, reveal, ids := newRowRuleReorderWindow(t, 1600)
			startRowRuleReorder(t, g, tt, reveal, ids)
			preX := reveal.scroll.X
			preBuilds, preFrames := reveal.strip.frames, reveal.strip.animationFrames

			tc.mutate(t, g, ids)
			tt.Advance(17 * time.Millisecond)
			watched := make([]model.TabID, len(tc.watched))
			for index, id := range tc.watched {
				watched[index] = ids[id]
			}
			assertRowChangeCancelled(t, tt, reveal, watched, preBuilds, preFrames, preX)
		})
	}

	t.Run("a pure reorder still runs and finishes", func(t *testing.T) {
		g, tt, reveal, ids := newRowRuleReorderWindow(t, 1600)
		preFrames := reveal.strip.animationFrames
		startRowRuleReorder(t, g, tt, reveal, ids)
		preX := reveal.scroll.X
		if reveal.strip.run == nil || reveal.strip.run.kind != motion.TabReorder {
			t.Fatalf("the pure reorder stopped: %+v", reveal.strip.run)
		}
		if reveal.strip.warp {
			t.Fatalf("the pure reorder warped")
		}
		tt.Advance(90 * time.Millisecond)
		if reveal.strip.run == nil {
			t.Fatalf("the pure reorder ended before its 180 ms")
		}
		if reveal.strip.animationFrames <= preFrames {
			t.Fatalf("the pure reorder asked for no animation frame")
		}
		tt.Advance(200 * time.Millisecond)
		if reveal.strip.run != nil {
			t.Fatalf("the pure reorder outlived its 180 ms: %+v", reveal.strip.run)
		}
		if abs32(reveal.scroll.X-preX) > 1 {
			t.Fatalf("the pure reorder moved the offset from %v to %v", preX, reveal.scroll.X)
		}
	})
}

// TestTabStripSubHalfOriginCloseWarps is the origin-clamp regression. A row
// that overflows by less than the clamp guard is still a real overflow: at
// rest the offset is that small positive maximum, and a close that shrinks the
// content below the viewport clamps it to zero. The old prediction exempted
// every offset at or below the guard, started a TabClose exit, and the layout
// snapped every item by the remaining fraction under it. The event paint must
// warp instead: no run, no frame, and the offset at zero at once.
func TestTabStripSubHalfOriginCloseWarps(t *testing.T) {
	kit.SetStyle(kit.Classic)
	t.Cleanup(func() { kit.SetStyle(kit.Classic) })
	setting := tabStripPreferences()
	g := newTabStripGraph(t, &setting)
	ids := addStripTabs(t, g.state, 3)
	for index := range 3 {
		if !g.state.RenameTab(index, "medium-title-here!") {
			t.Fatalf("RenameTab(%d) failed", index)
		}
	}
	reveal := newWorkTabReveal()
	reveal.labels = true
	// At 636 DIPs this row's content is 636.419: a genuine 0.419-DIP
	// overflow, below the 0.5-DIP clamp guard. The fraction comes from the
	// title's measured width and is deterministic for the test font.
	tt := ui.NewTester(steppingStripView(stripOnlyView(g.state, reveal)), 636, 480)
	tt.SetLocale("en")
	tt.Advance(400 * time.Millisecond)
	if reveal.scroll.MaxX <= 0 || reveal.scroll.MaxX >= tabStripClampGuard {
		t.Fatalf("the row overflows by %v, want a positive overflow below the %v clamp guard; adjust the window width",
			reveal.scroll.MaxX, tabStripClampGuard)
	}
	reveal.scroll.X = reveal.scroll.MaxX
	tt.Advance(17 * time.Millisecond)
	if reveal.scroll.X <= 0 || reveal.scroll.X >= tabStripClampGuard {
		t.Fatalf("the strip rests at X=%v, want a positive offset below the clamp guard", reveal.scroll.X)
	}
	watched := []model.TabID{ids[0], ids[2]}
	preX := reveal.scroll.X
	preBuilds, preFrames := reveal.strip.frames, reveal.strip.animationFrames

	if !g.state.CloseTab(1) {
		t.Fatalf("CloseTab failed")
	}
	tt.Advance(17 * time.Millisecond)
	stripEvent(t, g.state, motion.TabClose)
	if got := reveal.strip.frames - preBuilds; got != 1 {
		t.Fatalf("the sub-half close built %d paints, want exactly its event paint's one", got)
	}
	if reveal.strip.run != nil {
		t.Fatalf("the sub-half close started a strip run: %+v", reveal.strip.run)
	}
	if !reveal.strip.warp {
		t.Fatalf("the sub-half close did not warp")
	}
	if got := reveal.strip.animationFrames - preFrames; got != 0 {
		t.Fatalf("the sub-half close asked for %d strip frames, want none", got)
	}
	if reveal.scroll.X != 0 || reveal.scroll.MaxX != 0 {
		t.Fatalf("the sub-half close rests at X=%v MaxX=%v, want the origin", reveal.scroll.X, reveal.scroll.MaxX)
	}
	if preX <= 0 {
		t.Fatalf("the case needs a positive pre-close offset, got %v", preX)
	}
	boxes := make([]ui.Rect, len(watched))
	for index, id := range watched {
		boxes[index] = stripTravel(t, tt, id)
	}
	tt.Advance(400 * time.Millisecond)
	if reveal.strip.run != nil {
		t.Fatalf("the sub-half close left a run after settling: %+v", reveal.strip.run)
	}
	for index, id := range watched {
		if got := stripTravel(t, tt, id); !rectNearOK(boxes[index], got, 1) {
			t.Fatalf("the watched Tab %q jumped from %+v to %+v under the origin clamp",
				id, boxes[index], got)
		}
	}
}

// TestTabStripRowRuleFirstAndUnknownRowWarps pins the reorder rule's unknowns:
// a reorder on the first frame has no previous layout to leave from, and a
// reorder on a row whose record was dropped (a remount) has no committed row
// to compare. Both must warp with no run and no frame, even though the
// reorder itself is a pure permutation of the live Tabs.
func TestTabStripRowRuleFirstAndUnknownRowWarps(t *testing.T) {
	setting := tabStripPreferences()

	t.Run("first frame", func(t *testing.T) {
		g := newTabStripGraph(t, &setting)
		ids := addStripTabs(t, g.state, 3)
		reveal := newWorkTabReveal()
		if !g.state.MoveTab(ids[0], ids[2], true) {
			t.Fatalf("MoveTab failed")
		}
		// Record the build that consumes the event, not the Tester's later
		// settling builds: the first build has no previous layout to leave.
		base := stripOnlyView(g.state, reveal)
		type paint struct {
			running, warp bool
			frames        int
		}
		var paints []paint
		view := func(c *ui.Context) {
			base(c)
			got := paint{warp: reveal.strip.warp, frames: reveal.strip.animationFrames}
			if reveal.strip.run != nil {
				got.running = true
			}
			paints = append(paints, got)
		}
		tt := ui.NewTester(view, 1600, 480)
		tt.SetLocale("en")
		if len(paints) == 0 {
			tt.Frame()
		}
		stripEvent(t, g.state, motion.TabReorder)
		first := paints[0]
		if first.running || !first.warp {
			t.Fatalf("the first-frame reorder left run=%v warp=%v, want a warp alone",
				first.running, first.warp)
		}
		if first.frames != 0 {
			t.Fatalf("the first-frame reorder asked for %d strip frames", first.frames)
		}
	})

	t.Run("dropped row record", func(t *testing.T) {
		g, tt, reveal, ids := newRowRuleReorderWindow(t, 1600)
		if !g.state.MoveTab(ids[0], ids[6], true) {
			t.Fatalf("MoveTab failed")
		}
		// A remount drops the committed row; the reorder arrives before a
		// build can commit a new one.
		reveal.strip.dropRowRecords()
		preFrames := reveal.strip.animationFrames
		tt.Advance(17 * time.Millisecond)
		stripEvent(t, g.state, motion.TabReorder)
		if reveal.strip.run != nil {
			t.Fatalf("the unknown-row reorder started a run: %+v", reveal.strip.run)
		}
		if !reveal.strip.warp {
			t.Fatalf("the unknown-row reorder did not warp")
		}
		if got := reveal.strip.animationFrames - preFrames; got != 0 {
			t.Fatalf("the unknown-row reorder asked for %d strip frames", got)
		}
	})
}

// TestTabStripRowRuleWarpBoundaries documents the row rule's over-warping and
// its event combinations:
//
//   - a benign title change during a run cancels it even when the row still
//     fits and no offset clamps: the rule is about whether the layout is the
//     one the run started on, not only about clamps;
//   - a close with a rename in the same update is not the committed row minus
//     one Tab, so the close clamp's subtraction must refuse it and warp;
//   - a pure reorder is still a permutation and is left to animate (the
//     control in TestTabStripRowChangeCancelsLiveReorder covers the full run;
//     this case pins that the event paint itself starts it).
func TestTabStripRowRuleWarpBoundaries(t *testing.T) {
	kit.SetStyle(kit.Classic)
	t.Cleanup(func() { kit.SetStyle(kit.Classic) })
	setting := tabStripPreferences()

	t.Run("benign title change cancels a fitting run", func(t *testing.T) {
		g := newTabStripGraph(t, &setting)
		ids := addStripTabs(t, g.state, 3)
		for index := range 3 {
			if !g.state.RenameTab(index, "a") {
				t.Fatalf("RenameTab(%d) failed", index)
			}
		}
		reveal := newWorkTabReveal()
		reveal.labels = true
		tt := ui.NewTester(steppingStripView(stripOnlyView(g.state, reveal)), 1600, 480)
		tt.SetLocale("en")
		tt.Advance(400 * time.Millisecond)
		if reveal.scroll.MaxX > 0.5 {
			t.Fatalf("the strip overflows (MaxX=%v); the case needs a fitting row with no clamp", reveal.scroll.MaxX)
		}
		if !g.state.MoveTab(ids[0], ids[2], true) {
			t.Fatalf("MoveTab failed")
		}
		tt.Advance(17 * time.Millisecond)
		if reveal.strip.run == nil || reveal.strip.run.kind != motion.TabReorder {
			t.Fatalf("the reorder did not start a run: %+v", reveal.strip.run)
		}
		tt.Advance(30 * time.Millisecond)
		preFrames := reveal.strip.animationFrames
		preBuilds := reveal.strip.frames

		// A title change records no strip event and causes no clamp here: the
		// row still fits. The general rule cancels anyway, which is the
		// documented over-warp.
		if !g.state.RenameTab(0, "benign-new-title") {
			t.Fatalf("RenameTab failed")
		}
		tt.Advance(17 * time.Millisecond)
		if got := reveal.strip.frames - preBuilds; got != 1 {
			t.Fatalf("the benign rename built %d paints, want exactly its event paint's one", got)
		}
		if reveal.strip.run != nil {
			t.Fatalf("a benign title change left the run playing: %+v", reveal.strip.run)
		}
		if !reveal.strip.warp {
			t.Fatalf("a benign title change did not warp")
		}
		if got := reveal.strip.animationFrames - preFrames; got != 0 {
			t.Fatalf("a benign title change asked for %d strip frames, want none", got)
		}
		if reveal.scroll.MaxX > 0.5 {
			t.Fatalf("the renamed row overflows (MaxX=%v); the case needs no clamp", reveal.scroll.MaxX)
		}
	})

	t.Run("close with a rename in the same update warps", func(t *testing.T) {
		w := newCloseClampWindow(t, kit.Classic, 0)
		w.reveal.scroll.X = w.reveal.scroll.MaxX
		w.tester.Advance(17 * time.Millisecond)
		preX := w.reveal.scroll.X
		builds, frames := w.bounds()
		if !w.graph.state.CloseTab(1) {
			t.Fatalf("CloseTab failed")
		}
		if !w.graph.state.RenameTab(0, "a") {
			t.Fatalf("RenameTab failed")
		}
		event := stripEvent(t, w.graph.state, motion.TabClose)
		if event.TabID != w.ids[1] {
			t.Fatalf("the close event names %q, want %q", event.TabID, w.ids[1])
		}
		w.tester.Advance(17 * time.Millisecond)
		w.assertEventPaintWarp(t, builds, frames)
		postMax := w.reveal.scroll.MaxX
		if preX <= postMax+0.5 {
			t.Fatalf("the close and rename did not clamp (X=%v postMax=%v)", preX, postMax)
		}
		if got := w.reveal.scroll.X; abs32(got-postMax) > 1 {
			t.Fatalf("the close and rename rest at X=%v but MaxX=%v", got, postMax)
		}
	})

	t.Run("a pure reorder starts the run", func(t *testing.T) {
		g, tt, reveal, ids := newRowRuleReorderWindow(t, 1600)
		preFrames := reveal.strip.animationFrames
		if !g.state.MoveTab(ids[0], ids[6], true) {
			t.Fatalf("MoveTab failed")
		}
		tt.Advance(17 * time.Millisecond)
		stripEvent(t, g.state, motion.TabReorder)
		if reveal.strip.run == nil || reveal.strip.run.kind != motion.TabReorder {
			t.Fatalf("the pure reorder did not start a run: %+v", reveal.strip.run)
		}
		if reveal.strip.warp {
			t.Fatalf("the pure reorder warped")
		}
		if got := reveal.strip.animationFrames - preFrames; got != 1 {
			t.Fatalf("the pure reorder asked for %d strip frames on its event paint, want one", got)
		}
	})
}
