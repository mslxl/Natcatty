package shell

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/domain/model"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/motion"
)

// closeClampTitle is the equal-width title every close-clamp window in this
// file builds: eight Tabs of it overflow the 720-DIP minimum strip, and the
// row's width is the same whichever Tab closes.
const closeClampTitle = "medium-title-here"

// newInteriorCloseWindowAt builds the eight-Tab window at the 720-DIP minimum,
// applies the offset in a real build, and returns it with the closing Tab
// still open. delta is how far below MaxX-measuredWidth the offset sits.
func newInteriorCloseWindowAt(t *testing.T, delta float32) *interiorCloseWindow {
	t.Helper()
	return newInteriorCloseWindowWithTitle(t, closeClampTitle, delta)
}

// newInteriorCloseWindowWithTitle is newInteriorCloseWindowAt for a chosen
// title. The maximum-width title is the exact-bound case: its build-time bound
// is the Work Tab maximum, which is also its laid-out width, so the close
// prediction is not conservative by the usual 4-DIP safety.
func newInteriorCloseWindowWithTitle(t *testing.T, title string, delta float32) *interiorCloseWindow {
	t.Helper()
	setting := tabStripPreferences()
	g := newTabStripGraph(t, &setting)
	ids := addStripTabs(t, g.state, 8)
	for index := range 8 {
		if !g.state.RenameTab(index, title) {
			t.Fatalf("RenameTab(%d) failed", index)
		}
	}
	reveal := newWorkTabReveal()
	reveal.labels = true
	tt := ui.NewTester(steppingStripView(stripOnlyView(g.state, reveal)), 720, 480)
	tt.SetLocale("en")
	// Move past the mount's still clock so every following Advance builds one
	// frame at a refresh boundary and the strip's open run can end.
	tt.Advance(400 * time.Millisecond)
	if reveal.strip.run != nil {
		t.Fatalf("the scenario does not rest before its close: %+v", reveal.strip.run)
	}
	if reveal.scroll.MaxX <= 0.5 {
		t.Fatalf("the eight-Tab strip does not overflow (MaxX=%v)", reveal.scroll.MaxX)
	}
	closing := ids[1]
	width := reveal.strip.widths[closing]
	if width <= 0 {
		t.Fatalf("no width recorded for the closing Tab (%v)", width)
	}
	reveal.scroll.X = reveal.scroll.MaxX - width - delta
	tt.Advance(17 * time.Millisecond)
	w := &interiorCloseWindow{
		graph:     g,
		tester:    tt,
		reveal:    reveal,
		watched:   [2]model.TabID{ids[5], ids[6]},
		startX:    reveal.scroll.X,
		preMax:    reveal.scroll.MaxX,
		preBuilds: reveal.strip.frames,
		preFrames: reveal.strip.animationFrames,
	}
	if w.startX > w.preMax+0.5 {
		t.Fatalf("the interior offset did not apply (X=%v MaxX=%v)", w.startX, w.preMax)
	}
	if w.startX < 0.5 {
		t.Fatalf("the interior offset starts before the strip (X=%v)", w.startX)
	}
	return w
}

// closeClampWindow is a resting eight-Tab strip-only window at the 720-DIP
// minimum whose build-time row the strip has committed. A test changes the
// row, sets the offset and issues the close between two builds, so the event
// paint is the one a real window would show.
type closeClampWindow struct {
	graph  *followGraph
	tester *ui.Tester
	reveal *workTabReveal
	ids    []model.TabID
	gap    float32
}

// newCloseClampWindow builds the window in style and waits for it to rest.
func newCloseClampWindow(t *testing.T, style kit.Style, gap float32) *closeClampWindow {
	t.Helper()
	kit.SetStyle(style)
	t.Cleanup(func() { kit.SetStyle(kit.Classic) })
	setting := tabStripPreferences()
	g := newTabStripGraph(t, &setting)
	ids := addStripTabs(t, g.state, 8)
	for index := range 8 {
		if !g.state.RenameTab(index, closeClampTitle) {
			t.Fatalf("RenameTab(%d) failed", index)
		}
	}
	reveal := newWorkTabReveal()
	reveal.labels = true
	tt := ui.NewTester(steppingStripView(stripOnlyView(g.state, reveal)), 720, 480)
	tt.SetLocale("en")
	tt.Advance(400 * time.Millisecond)
	if reveal.strip.run != nil {
		t.Fatalf("the scenario does not rest before its close: %+v", reveal.strip.run)
	}
	if reveal.scroll.MaxX <= 0.5 {
		t.Fatalf("the eight-Tab strip does not overflow (MaxX=%v)", reveal.scroll.MaxX)
	}
	if !reveal.strip.row.valid || len(reveal.strip.row.tabs) != 8 {
		t.Fatalf("the resting row record = %+v, want eight tabs", reveal.strip.row)
	}
	return &closeClampWindow{graph: g, tester: tt, reveal: reveal, ids: ids, gap: gap}
}

// overhead is the content width a close removes besides the Tab body: both
// insertion placeholders and the three row gaps its three children take.
func (w *closeClampWindow) overhead() float32 {
	return 2*tabInsertionStripWidth + tabRowChildCount*w.gap
}

// recordedWidth is the measured width the last build recorded for id.
func (w *closeClampWindow) recordedWidth(t *testing.T, id model.TabID) float32 {
	t.Helper()
	width, ok := w.reveal.strip.widths[id]
	if !ok || width <= 0 {
		t.Fatalf("no measured width recorded for Tab %q (%v, present %v)", id, width, ok)
	}
	return width
}

// bounds reads the build and animation-frame counters before a close.
func (w *closeClampWindow) bounds() (builds, frames int) {
	return w.reveal.strip.frames, w.reveal.strip.animationFrames
}

// assertEventPaintWarp proves the close's event paint built once, started no
// run and asked for no animation frame.
func (w *closeClampWindow) assertEventPaintWarp(t *testing.T, builds, frames int) {
	t.Helper()
	if got := w.reveal.strip.frames - builds; got != 1 {
		t.Fatalf("the close built %d event paints, want exactly one", got)
	}
	if w.reveal.strip.run != nil {
		t.Fatalf("the close started a strip run: %+v", w.reveal.strip.run)
	}
	if !w.reveal.strip.warp {
		t.Fatalf("the close did not warp on its event paint")
	}
	if got := w.reveal.strip.animationFrames - frames; got != 0 {
		t.Fatalf("the close asked for %d strip frames", got)
	}
}

// assertStableBoxes proves the watched Tabs stay put after the event paint.
func (w *closeClampWindow) assertStableBoxes(t *testing.T, watched []model.TabID, wantX, wantMax float32) {
	t.Helper()
	boxes := make([]ui.Rect, len(watched))
	for index, id := range watched {
		boxes[index] = stripTravel(t, w.tester, id)
	}
	if abs32(w.reveal.scroll.X-wantX) > 1 || abs32(w.reveal.scroll.MaxX-wantMax) > 1 {
		t.Fatalf("the event paint rests at X=%v MaxX=%v, want X=%v MaxX=%v",
			w.reveal.scroll.X, w.reveal.scroll.MaxX, wantX, wantMax)
	}
	w.tester.Advance(400 * time.Millisecond)
	if w.reveal.strip.run != nil {
		t.Fatalf("the warped close left a run after settling: %+v", w.reveal.strip.run)
	}
	if abs32(w.reveal.scroll.X-wantX) > 1 || abs32(w.reveal.scroll.MaxX-wantMax) > 1 {
		t.Fatalf("the settled strip rests at X=%v MaxX=%v, want X=%v MaxX=%v",
			w.reveal.scroll.X, w.reveal.scroll.MaxX, wantX, wantMax)
	}
	for index, id := range watched {
		if got := stripTravel(t, w.tester, id); !rectNearOK(boxes[index], got, 1) {
			t.Fatalf("the watched Tab %q jumped from %+v to %+v under the close",
				id, boxes[index], got)
		}
	}
}

// rowBound is the build-time body width bound the strip recorded for id.
func rowBound(t *testing.T, reveal *workTabReveal, id model.TabID) float32 {
	t.Helper()
	for _, item := range reveal.strip.row.tabs {
		if item.id == id {
			return item.bound
		}
	}
	t.Fatalf("the strip's row record has no Tab %q: %+v", id, reveal.strip.row.tabs)
	return 0
}

// TestTabStripCloseClampUsesTheBuildTimeWidthBound is the stale-width
// regression. A Tab is renamed to its maximum title, or made dirty, and the
// frame that builds the change records LayoutBounds from the layout before it:
// the measured record still holds the old width while the name lookup means
// the next build will lay out the wider row. Closing that Tab then predicts a
// removal from the old body: the strip starts the exit while the layout clamps
// the offset under it. The build-time bound is computed from the title and
// marker the build draws, so it covers the layout that build produced.
func TestTabStripCloseClampUsesTheBuildTimeWidthBound(t *testing.T) {
	for _, style := range []struct {
		name string
		s    kit.Style
		gap  float32
	}{
		{"classic", kit.Classic, 0},
		{"macos", kit.MacOS, 4},
	} {
		t.Run(style.name, func(t *testing.T) {
			for _, variant := range []struct {
				name       string
				change     func(t *testing.T, w *closeClampWindow)
				wantBound  float32
				boundGrows bool
			}{
				{
					name: "rename to the maximum title",
					change: func(t *testing.T, w *closeClampWindow) {
						t.Helper()
						if !w.graph.state.RenameTab(1, maxWidthTabTitle) {
							t.Fatalf("RenameTab failed")
						}
					},
					wantBound: kit.WorkTabMaxWidth,
				},
				{
					name: "dirty marker",
					change: func(t *testing.T, w *closeClampWindow) {
						t.Helper()
						w.graph.state.SetTabDirty(1, true)
					},
					boundGrows: true,
				},
			} {
				t.Run(variant.name, func(t *testing.T) {
					w := newCloseClampWindow(t, style.s, style.gap)
					closing := w.ids[1]
					oldWidth := w.recordedWidth(t, closing)
					oldBound := rowBound(t, w.reveal, closing)
					variant.change(t, w)
					// One build lays out the changed row. Its measured record is
					// the layout before it; its row record is the new title and
					// marker.
					w.tester.Advance(17 * time.Millisecond)
					preMax := w.reveal.scroll.MaxX
					if preMax <= 0.5 {
						t.Fatalf("the renamed row does not overflow (MaxX=%v)", preMax)
					}
					bound := rowBound(t, w.reveal, closing)
					if variant.boundGrows {
						if bound <= oldBound+1 {
							t.Fatalf("the dirty marker's build-time bound %v did not widen from %v",
								bound, oldBound)
						}
					} else if abs32(bound-variant.wantBound) > 1 {
						t.Fatalf("the renamed Tab's build-time bound = %v, want its maximum %v",
							bound, variant.wantBound)
					}
					if got := w.recordedWidth(t, closing); abs32(got-oldWidth) > 1 {
						t.Fatalf("the measured record moved to %v; the case needs the stale %v",
							got, oldWidth)
					}
					overhead := w.overhead()
					// X is past the real post-close maximum by 8 minus the bound's
					// slack, but still below what the stale measured width would
					// have predicted, so the old prediction animated and clamped.
					X := preMax - (bound + overhead) + 8
					if staleMax := preMax - (oldWidth + overhead); staleMax < X+0.5 {
						t.Fatalf("the case needs the stale prediction to animate: stale MaxX=%v, X=%v",
							staleMax, X)
					}
					w.reveal.scroll.X = X
					builds, frames := w.bounds()
					if !w.graph.state.CloseTab(1) {
						t.Fatalf("CloseTab failed")
					}
					w.tester.Advance(17 * time.Millisecond)
					event := stripEvent(t, w.graph.state, motion.TabClose)
					if event.TabID != closing {
						t.Fatalf("the close event names %q, want %q", event.TabID, closing)
					}
					w.assertEventPaintWarp(t, builds, frames)
					postMax := w.reveal.scroll.MaxX
					if X <= postMax+0.5 {
						t.Fatalf("the case needs the layout to clamp: X=%v postMax=%v", X, postMax)
					}
					w.assertStableBoxes(t, []model.TabID{w.ids[5], w.ids[6]}, postMax, postMax)
				})
			}
		})
	}
}

// TestTabStripCloseClampBoundaryWarpsAtMinimumWidth pins the clamp guard: the
// layout clamps only above the new maximum, so an offset just past it, exactly
// at it, or a quarter DIP below it must warp rather than run a TabClose exit
// under the clamp. An offset comfortably below still animates.
func TestTabStripCloseClampBoundaryWarpsAtMinimumWidth(t *testing.T) {
	for _, style := range []struct {
		name string
		s    kit.Style
		gap  float32
	}{
		{"classic", kit.Classic, 0},
		{"macos", kit.MacOS, 4},
	} {
		t.Run(style.name, func(t *testing.T) {
			kit.SetStyle(style.s)
			t.Cleanup(func() { kit.SetStyle(kit.Classic) })
			// X = preMax - measured - delta = postMax + overhead - delta.
			overhead := 2*tabInsertionStripWidth + tabRowChildCount*style.gap
			for _, title := range []struct {
				name  string
				title string
			}{
				// The medium title's bound is 4 DIPs above its laid-out width, so
				// the prediction is conservative and would warp these offsets
				// even without the guard. The maximum title's bound is exact,
				// which isolates the guard itself.
				{"medium title", closeClampTitle},
				{"maximum title", maxWidthTabTitle},
			} {
				t.Run(title.name, func(t *testing.T) {
					for _, tc := range []struct {
						name   string
						offset float32 // X relative to the real post-close maximum
						warp   bool
					}{
						{"past the boundary", 0.25, true},
						{"at the boundary", 0, true},
						{"just below the boundary", -0.25, true},
						{"comfortably below", -8, false},
					} {
						t.Run(tc.name, func(t *testing.T) {
							w := newInteriorCloseWindowWithTitle(t, title.title, overhead-tc.offset)
							closing, ok := w.graph.state.TabID(1)
							if !ok {
								t.Fatalf("no closing Tab")
							}
							width := w.reveal.strip.widths[closing]
							postMaxWant := w.preMax - (width + overhead)
							if !w.graph.state.CloseTab(1) {
								t.Fatalf("CloseTab failed")
							}
							w.tester.Advance(17 * time.Millisecond)
							event := stripEvent(t, w.graph.state, motion.TabClose)
							if event.TabID != closing {
								t.Fatalf("the close event names %q, want %q", event.TabID, closing)
							}
							postMax := w.reveal.scroll.MaxX
							if abs32(postMax-postMaxWant) > 1 {
								t.Fatalf("the close's maximum moved to %v, want %v", postMax, postMaxWant)
							}
							if got := w.startX - postMax; abs32(got-tc.offset) > 1 {
								t.Fatalf("the offset sits %v from the new maximum, want %v", got, tc.offset)
							}
							if tc.warp {
								if w.reveal.strip.run != nil {
									t.Fatalf("the boundary close started a strip run: %+v", w.reveal.strip.run)
								}
								if !w.reveal.strip.warp {
									t.Fatalf("the boundary close did not warp on its event paint")
								}
								if got := w.reveal.strip.animationFrames - w.preFrames; got != 0 {
									t.Fatalf("the boundary close asked for %d strip frames", got)
								}
								wantX := min(w.startX, postMax)
								if abs32(w.reveal.scroll.X-wantX) > 1 {
									t.Fatalf("the warped close rests at X=%v, want %v", w.reveal.scroll.X, wantX)
								}
								boxes := [2]ui.Rect{
									stripTravel(t, w.tester, w.watched[0]),
									stripTravel(t, w.tester, w.watched[1]),
								}
								w.tester.Advance(400 * time.Millisecond)
								for index, id := range w.watched {
									if got := stripTravel(t, w.tester, id); !rectNearOK(boxes[index], got, 1) {
										t.Fatalf("the boundary close moved Tab %q from %+v to %+v",
											id, boxes[index], got)
									}
								}
								return
							}
							if w.reveal.strip.run == nil || w.reveal.strip.run.kind != motion.TabClose {
								t.Fatalf("the comfortable close did not start a TabClose run: %+v",
									w.reveal.strip.run)
							}
							if w.reveal.strip.warp {
								t.Fatalf("the comfortable close warped")
							}
							if got := w.reveal.strip.animationFrames - w.preFrames; got != 1 {
								t.Fatalf("the comfortable close asked for %d strip frames on its event paint, want one",
									got)
							}
							if abs32(w.reveal.scroll.X-w.startX) > 1 {
								t.Fatalf("the comfortable close moved the offset from %v to %v",
									w.startX, w.reveal.scroll.X)
							}
							if w.startX >= postMax-0.5 {
								t.Fatalf("the comfortable close is not below the new maximum (X=%v postMax=%v)",
									w.startX, postMax)
							}
						})
					}
				})
			}
		})
	}
}

// TestTabStripCloseClampWarpsWhenTheRowLosesAnotherTab covers a close that
// arrives with another row change in the same update: a detach plus a close,
// and two closes. The state keeps only the latest TabClose, so the one-Tab
// subtraction cannot see the other removal; without the row snapshot the
// event paint starts the exit while the layout clamps by the other Tab's whole
// row contribution.
func TestTabStripCloseClampWarpsWhenTheRowLosesAnotherTab(t *testing.T) {
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
				name   string
				latest func(w *closeClampWindow) model.TabID
				mutate func(t *testing.T, w *closeClampWindow)
			}{
				{
					name: "detach another Tab and close",
					latest: func(w *closeClampWindow) model.TabID {
						return w.ids[1]
					},
					mutate: func(t *testing.T, w *closeClampWindow) {
						t.Helper()
						if !w.graph.state.DetachTab(6) {
							t.Fatalf("DetachTab failed")
						}
						if !w.graph.state.CloseTab(1) {
							t.Fatalf("CloseTab failed")
						}
					},
				},
				{
					name: "close two Tabs",
					latest: func(w *closeClampWindow) model.TabID {
						return w.ids[2]
					},
					mutate: func(t *testing.T, w *closeClampWindow) {
						t.Helper()
						if !w.graph.state.CloseTab(1) || !w.graph.state.CloseTab(1) {
							t.Fatalf("CloseTab failed")
						}
					},
				},
			} {
				t.Run(tc.name, func(t *testing.T) {
					w := newCloseClampWindow(t, style.s, style.gap)
					closing := tc.latest(w)
					bound := rowBound(t, w.reveal, closing)
					// The one-Tab prediction alone would animate: the offset is
					// just below the maximum it forecasts.
					singleMax := w.reveal.scroll.MaxX - (bound + w.overhead())
					X := singleMax - 1
					if X <= 0.5 || X > w.reveal.scroll.MaxX {
						t.Fatalf("the case's offset X=%v is outside the strip (MaxX=%v)",
							X, w.reveal.scroll.MaxX)
					}
					w.reveal.scroll.X = X
					tc.mutate(t, w)
					event := stripEvent(t, w.graph.state, motion.TabClose)
					if event.TabID != closing {
						t.Fatalf("the close event names %q, want %q", event.TabID, closing)
					}
					builds, frames := w.bounds()
					w.tester.Advance(17 * time.Millisecond)
					w.assertEventPaintWarp(t, builds, frames)
					postMax := w.reveal.scroll.MaxX
					if X <= postMax+0.5 {
						t.Fatalf("the case does not clamp: X=%v postMax=%v", X, postMax)
					}
					w.assertStableBoxes(t, []model.TabID{w.ids[7]}, postMax, postMax)
				})
			}
		})
	}
}

// TestTabStripCloseClampWarpsAfterAStyleChange changes the Interface Style
// between the last build and a close. The last row was laid out in one style
// and the close frame in the other; every surviving Tab's body width and the
// row gap change, so the one-Tab subtraction from the old maximum cannot
// describe the close frame. Predicting from the old row's bound and the new
// gap would animate at an offset the new layout clamps by roughly 30 DIPs.
func TestTabStripCloseClampWarpsAfterAStyleChange(t *testing.T) {
	w := newCloseClampWindow(t, kit.MacOS, 4)
	closing := w.ids[1]
	bound := rowBound(t, w.reveal, closing)
	// The close frame is Classic: its gap is 0.
	overhead := 2 * tabInsertionStripWidth
	// The ablated prediction (macOS row bound, Classic gap) would see this
	// maximum and animate; the real Classic layout clamps well below X.
	X := w.reveal.scroll.MaxX - (bound + overhead) - 4
	if X <= 0.5 {
		t.Fatalf("the case's offset X=%v is outside the strip", X)
	}
	kit.SetStyle(kit.Classic)
	t.Cleanup(func() { kit.SetStyle(kit.Classic) })
	w.reveal.scroll.X = X
	builds, frames := w.bounds()
	if !w.graph.state.CloseTab(1) {
		t.Fatalf("CloseTab failed")
	}
	w.tester.Advance(17 * time.Millisecond)
	event := stripEvent(t, w.graph.state, motion.TabClose)
	if event.TabID != closing {
		t.Fatalf("the close event names %q, want %q", event.TabID, closing)
	}
	w.assertEventPaintWarp(t, builds, frames)
	postMax := w.reveal.scroll.MaxX
	if X <= postMax+0.5 {
		t.Fatalf("the case needs the new style's layout to clamp: X=%v postMax=%v", X, postMax)
	}
	w.assertStableBoxes(t, []model.TabID{w.ids[5], w.ids[6]}, postMax, postMax)
}

// TestTabStripClosePredictionWarpsWhenItsRowIsNotTrustworthy pins the row
// snapshot's seam for changes a live graph cannot construct between two
// builds: a Pending Connection row, and a closing Tab the last build never
// recorded. In both cases the one-Tab prediction has no trustworthy row, so
// the decision must warp even at an offset that would otherwise not clamp.
// The matched row at the same offset uses the prediction and does not warp.
func TestTabStripClosePredictionWarpsWhenItsRowIsNotTrustworthy(t *testing.T) {
	setting := tabStripPreferences()
	g := newTabStripGraph(t, &setting)
	ids := addStripTabs(t, g.state, 2)
	reveal := newWorkTabReveal()
	reveal.labels = true
	tt := ui.NewTester(stripOnlyView(g.state, reveal), 720, 480)
	tt.SetLocale("en")
	tt.Advance(400 * time.Millisecond)
	if !reveal.strip.row.valid || len(reveal.strip.row.tabs) != 2 {
		t.Fatalf("the resting row record = %+v, want two tabs", reveal.strip.row)
	}
	closing := ids[1]
	if !g.state.CloseTab(1) {
		t.Fatalf("CloseTab failed")
	}
	// The state now describes the last row minus the closing Tab.
	var matched, clamped, pendingChanged, missingBound, unknownRow bool
	check := func(c *ui.Context) {
		signals := tabStripSignals{maxX: 1000, scrollX: 1}
		matched = reveal.strip.closeWouldClamp(c, g.state, signals, closing)
		clamped = reveal.strip.closeWouldClamp(c, g.state,
			tabStripSignals{maxX: 1000, scrollX: 900}, closing)
		saved := reveal.strip.row
		reveal.strip.row.pending = append(append([]string(nil), saved.pending...), "pending-host")
		pendingChanged = reveal.strip.closeWouldClamp(c, g.state, signals, closing)
		reveal.strip.row = saved
		reveal.strip.row.tabs = append([]tabRowItem(nil), saved.tabs[:1]...)
		missingBound = reveal.strip.closeWouldClamp(c, g.state, signals, closing)
		reveal.strip.row = saved
		reveal.strip.row.valid = false
		unknownRow = reveal.strip.closeWouldClamp(c, g.state, signals, closing)
		reveal.strip.row = saved
	}
	probe := ui.NewTester(check, 720, 480)
	probe.SetLocale("en")
	probe.Frame()
	if matched {
		t.Fatalf("the matched row at an offset far below its maximum warped")
	}
	if !clamped {
		t.Fatalf("the matched row at an offset past its maximum did not warp")
	}
	if !pendingChanged {
		t.Fatalf("a Pending row change in the close's update did not warp")
	}
	if !missingBound {
		t.Fatalf("a closing Tab with no build-time bound did not warp")
	}
	if !unknownRow {
		t.Fatalf("a close with no row snapshot did not warp")
	}
}
