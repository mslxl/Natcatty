package shell

import (
	"fmt"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/domain/model"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	"github.com/mslxl/natcatty/ui/internal/styletest"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/motion"
	"github.com/mslxl/natcatty/ui/shell/state"
	"github.com/mslxl/natcatty/ui/terminal/appview"
)

// sidePanelSetting is a motion setting with the side-panel's two events at
// the given switches and styles, at standard speed. The drawer's own geometry
// does not depend on the styles; the matrix passes them to hold that.
func sidePanelSetting(open, close bool, openStyle, closeStyle domainpreferences.MotionStyle) domainpreferences.Motion {
	setting := domainpreferences.Defaults().Appearance.Motion
	setting.Enabled = true
	setting.PaneOpen = open
	setting.PaneClose = close
	setting.OpenStyle = openStyle
	setting.CloseStyle = closeStyle
	setting.Speed = domainpreferences.MotionStandard
	return setting
}

// sidePanelViewText is the files panel's content, so a test can find it in the
// real panel and its closing drawer.
func sidePanelViewText(text string) func(float32) func(*ui.Context) {
	return func(float32) func(*ui.Context) {
		return func(c *ui.Context) { ui.Text(c, text) }
	}
}

// startSidePanelTerminal mounts the real shell over one local terminal Tab
// with the side panel closed. The test keeps the Pane motion runtime so it can
// read the panel run and its reveal.
func startSidePanelTerminal(t *testing.T, setting domainpreferences.Motion,
	dock state.PanelDock, view func(float32) func(*ui.Context), tools ...state.SideTool) (
	*realTerminalGraph, *ui.Tester, *workspacePaneMotion, model.TabID, *realTerminalSurface) {
	t.Helper()
	panel := state.FilesPanel{View: view}
	if dock != "" {
		panel.Dock = func() state.PanelDock { return dock }
		panel.SetDock = func(state.PanelDock) {}
	}
	options := []state.Option{
		state.WithMotion(func() domainpreferences.Motion { return setting }),
		state.WithFilesPanel(panel),
	}
	if len(tools) > 0 {
		options = append(options, state.WithSideTools(tools...))
	}
	g := newRealTerminalGraph(t, options...)
	if err := g.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	waitForRealSurfaces(t, g.state, 1)
	tabID, ok := g.state.TabID(0)
	if !ok {
		t.Fatal("the new Tab has no id")
	}
	render, wm := viewWithPaneMotion(g.state, paneMotionMode{labels: true})
	tt := ui.NewTester(render, 900, 600)
	tt.SetLocale("en")
	surfaces := settlePane(t, tt, g, 0)
	tt.Frame()
	return g, tt, wm, tabID, surfaces[0]
}

// openSidePanel toggles the Tab's panel and builds the change frame.
func openSidePanel(t *testing.T, tt *ui.Tester, g *realTerminalGraph, tabID model.TabID) {
	t.Helper()
	g.state.ToggleFiles(tabID, "", "")
	if !g.state.FilesOpen(tabID) {
		t.Fatal("the side panel did not open")
	}
	tt.Frame()
}

// finishPanelRun advances past the run's duration, builds the final frame,
// and checks the panel runtime stopped asking for frames. The window may still
// animate other things (a terminal cursor), so only the panel's own count is
// read.
func finishPanelRun(t *testing.T, tt *ui.Tester, wm *workspacePaneMotion) {
	t.Helper()
	tt.Advance(2 * time.Second)
	tt.Frame()
	if wm.panel.run != nil {
		t.Fatalf("the panel run is still playing after its duration: %+v", wm.panel.run)
	}
	frames := wm.panel.animationFrames
	tt.Advance(50 * time.Millisecond)
	tt.Frame()
	if wm.panel.animationFrames != frames {
		t.Fatalf("the panel runtime asked for %d frames after its run ended",
			wm.panel.animationFrames-frames)
	}
}

// assertNoPanelGridWork asserts the animation frames since the change did no
// grid math and delivered no PTY resize.
func assertNoPanelGridWork(t *testing.T, what string, surface *realTerminalSurface,
	calls, resizes int) {
	t.Helper()
	if got := surface.GridForCalls() - calls; got != 0 {
		t.Errorf("%s computed %d grids during panel animation frames, want none", what, got)
	}
	if got := len(surface.ResizeLog()) - resizes; got != 0 {
		t.Errorf("%s delivered %d PTY resizes during panel animation frames, want none", what, got)
	}
}

// assertViewportNear asserts a surface viewport within a DIP, the split's
// own layout rounding.
func assertViewportNear(t *testing.T, what string, got, want appview.Viewport) {
	t.Helper()
	if abs(got.W-want.W) > 1 || abs(got.H-want.H) > 1 {
		t.Fatalf("%s = %+v, want %+v", what, got, want)
	}
}

// panelRevealSize is the panel slot's size along its dock axis.
func panelRevealSize(box ui.Rect, dock state.PanelDock) float32 {
	if dock == state.DockBottom {
		return box.H
	}
	return box.W
}

// TestSidePanelOpenCloseMatrix covers every dock in both Interface Styles.
// The drawer starts with no panel, moves the divider and the terminal with it,
// keeps the terminal at its pinned final grid, and delivers exactly one PTY
// resize per change. The open style settings do not change the drawer.
func TestSidePanelOpenCloseMatrix(t *testing.T) {
	styletest.Each(t, testSidePanelOpenCloseMatrix)
}

func testSidePanelOpenCloseMatrix(t *testing.T) {
	cases := []struct {
		name       string
		dock       state.PanelDock
		openStyle  domainpreferences.MotionStyle
		closeStyle domainpreferences.MotionStyle
	}{
		{"left-popin-fade", state.DockLeft, domainpreferences.MotionPopin, domainpreferences.MotionFade},
		{"right-slide-slide", state.DockRight, domainpreferences.MotionSlide, domainpreferences.MotionSlide},
		{"bottom-fade-popin", state.DockBottom, domainpreferences.MotionFade, domainpreferences.MotionPopin},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setting := sidePanelSetting(true, true, tc.openStyle, tc.closeStyle)
			g, tt, wm, tabID, surface := startSidePanelTerminal(t, setting, tc.dock, sidePanelViewText("files-panel"))
			preOpen := surface.Viewport()
			paneRegion, ok := tt.Find(locales.In("en").PaneRegion(1))
			if !ok {
				t.Fatal("the Pane region is not labeled")
			}
			surface.ResetResizeLog()

			openSidePanel(t, tt, g, tabID)
			run := wm.panel.run
			if run == nil {
				t.Fatal("opening the panel started no run")
			}
			if run.kind != motion.PaneOpen {
				t.Fatalf("open run kind = %v, want PaneOpen", run.kind)
			}
			if got, want := run.duration, 200*time.Millisecond; got != want {
				t.Fatalf("open duration = %s, want %s", got, want)
			}
			if wm.panel.animationFrames == 0 {
				t.Fatal("the open run asked for no animation frame")
			}
			finalSize := run.to
			if finalSize <= 0 {
				t.Fatalf("the open's final panel size = %v", finalSize)
			}
			// The terminal's final grid is pinned at once; the drawer's first
			// layout shows no panel and keeps the old Pane box.
			if got := len(surface.ResizeLog()); got != 1 {
				t.Fatalf("opening the panel delivered %d PTY resizes, want exactly 1; log %+v",
					got, surface.ResizeLog())
			}
			if !surface.viewportPinned {
				t.Fatal("the open did not pin the Pane's final viewport")
			}
			pinned := surface.Viewport()
			if box, found := tt.Find("side-panel"); found && panelRevealSize(box, tc.dock) > 1 {
				t.Fatalf("the open's first frame shows %v DIPs of panel, want none", panelRevealSize(box, tc.dock))
			}
			changeCalls := surface.GridForCalls()
			changeResizes := len(surface.ResizeLog())

			// Mid-run: the drawer has revealed part of the panel and the
			// terminal's boundary moved with it, while its content stays the
			// pinned final grid.
			tt.Advance(80 * time.Millisecond)
			tt.Frame()
			mid, found := tt.Find("side-panel")
			if !found {
				t.Fatal("the opening drawer is not built")
			}
			visible := panelRevealSize(mid, tc.dock)
			if visible <= 1 || visible >= finalSize-1 {
				t.Fatalf("the drawer at 80ms = %v DIPs, want between 0 and its final %v", visible, finalSize)
			}
			moved, found := tt.Find(locales.In("en").PaneRegion(1))
			if !found {
				t.Fatal("the Pane region left the opening frame")
			}
			// The panel slot and the split's one-DIP divider move the Pane's
			// boundary by exactly the revealed size, so the drawer and the
			// terminal are in sync.
			if lost := paneLost(paneRegion, moved, tc.dock); abs(lost-(visible+1)) > 1.5 {
				t.Fatalf("the drawer shows %v DIPs but moved the Pane by %v, want %v",
					visible, lost, visible+1)
			}
			assertNoPanelGridWork(t, "the opening panel", surface, changeCalls, changeResizes)
			assertViewportNear(t, "the Pane during the open", surface.Viewport(), pinned)
			if !surface.viewportPinned {
				t.Fatal("the Pane's viewport lost its pin mid-open")
			}

			finishPanelRun(t, tt, wm)
			if surface.viewportPinned {
				t.Fatal("the Pane's viewport pin outlived the open run")
			}
			if got := len(surface.ResizeLog()); got != 1 {
				t.Fatalf("the open run delivered %d PTY resizes over its frames, want exactly 1; log %+v",
					got, surface.ResizeLog())
			}
			panelBox, found := tt.Find("side-panel")
			if !found {
				t.Fatal("the settled panel has no box")
			}
			if got := panelRevealSize(panelBox, tc.dock); abs(got-finalSize) > 1 {
				t.Fatalf("the settled panel = %v DIPs, want its final %v", got, finalSize)
			}

			// The settled Split gives the panel its final size and the divider
			// its one DIP, and the Pane's own layout reports the pinned box.
			full := surface.Viewport()
			assertViewportNear(t, "the Pane after the open", full, pinned)
			lost := preOpen.W - full.W
			if tc.dock == state.DockBottom {
				lost = preOpen.H - full.H
			}
			if abs(lost-(finalSize+1)) > 1 {
				t.Fatalf("the settled panel took %v DIPs of the Pane, want its final %v plus the divider",
					lost, finalSize)
			}
			settledPane, ok := tt.Find(locales.In("en").PaneRegion(1))
			if !ok {
				t.Fatal("the settled Pane region is not labeled")
			}

			// Close: the drawer starts from the panel the user saw and
			// shrinks its slot while the terminal takes the room back.
			surface.ResetResizeLog()
			g.state.CloseFiles(tabID)
			tt.Frame()
			closeRun := wm.panel.run
			if closeRun == nil {
				t.Fatal("closing the panel started no run")
			}
			if closeRun.kind != motion.PaneClose {
				t.Fatalf("close run kind = %v, want PaneClose", closeRun.kind)
			}
			if got, want := closeRun.duration, 150*time.Millisecond; got != want {
				t.Fatalf("close duration = %s, want %s", got, want)
			}
			closing, found := tt.Find("side-panel")
			if !found {
				t.Fatal("the closing drawer is not built")
			}
			if got := panelRevealSize(closing, tc.dock); abs(got-closeRun.from) > 1 {
				t.Fatalf("the closing drawer starts at %v DIPs, want the panel's last %v", got, closeRun.from)
			}
			if got := len(surface.ResizeLog()); got != 1 {
				t.Fatalf("closing the panel delivered %d PTY resizes, want exactly 1; log %+v",
					got, surface.ResizeLog())
			}
			if !surface.viewportPinned {
				t.Fatal("the close did not pin the Pane's final viewport")
			}
			closePinned := surface.Viewport()
			assertViewportNear(t, "the Pane at the close", closePinned, preOpen)
			closeChangeCalls := surface.GridForCalls()
			closeChangeResizes := len(surface.ResizeLog())

			tt.Advance(60 * time.Millisecond)
			tt.Frame()
			closing, found = tt.Find("side-panel")
			if !found {
				t.Fatal("the closing drawer left before its duration")
			}
			visible = panelRevealSize(closing, tc.dock)
			if visible <= 1 || visible >= closeRun.from-1 {
				t.Fatalf("the close drawer at 60ms = %v DIPs, want between its full %v and 0",
					visible, closeRun.from)
			}
			moved, found = tt.Find(locales.In("en").PaneRegion(1))
			if !found {
				t.Fatal("the Pane region left the closing frame")
			}
			if back := -paneLost(settledPane, moved, tc.dock); abs(back-(closeRun.from-visible)) > 1.5 {
				t.Fatalf("the close drawer shows %v DIPs but gave the Pane back %v, want %v",
					visible, back, closeRun.from-visible)
			}
			assertNoPanelGridWork(t, "the closing panel", surface, closeChangeCalls, closeChangeResizes)
			assertViewportNear(t, "the Pane during the close", surface.Viewport(), closePinned)

			finishPanelRun(t, tt, wm)
			if _, found := tt.Find("side-panel"); found {
				t.Fatal("the closed panel is still in the layout")
			}
			if surface.viewportPinned {
				t.Fatal("the Pane's viewport pin outlived the close run")
			}
			assertViewportNear(t, "the Pane after closing the panel", surface.Viewport(), preOpen)
			if got := len(surface.ResizeLog()); got != 1 {
				t.Fatalf("the close run delivered %d PTY resizes over its frames, want exactly 1; log %+v",
					got, surface.ResizeLog())
			}
		})
	}
}

// abs is the DIP tolerance helper for layout rounding.
func abs(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

// paneLost is how much of its region a Pane gave to the drawer along the dock
// axis. A negative value means the Pane grew by that much.
func paneLost(before, after ui.Rect, dock state.PanelDock) float32 {
	if dock == state.DockBottom {
		return before.H - after.H
	}
	return before.W - after.W
}

// TestSidePanelMotionSwitches covers the master switch, the two event
// switches, and Reduce Motion: a disabled change warps and asks for no
// animation frames, while the other change still animates.
func TestSidePanelMotionSwitches(t *testing.T) {
	cases := []struct {
		name      string
		setting   func(domainpreferences.Motion) domainpreferences.Motion
		reduce    bool
		wantOpen  bool
		wantClose bool
	}{
		{
			name: "master-off",
			setting: func(m domainpreferences.Motion) domainpreferences.Motion {
				m.Enabled = false
				return m
			},
		},
		{
			name: "pane-open-off",
			setting: func(m domainpreferences.Motion) domainpreferences.Motion {
				m.PaneOpen = false
				return m
			},
			wantClose: true,
		},
		{
			name: "pane-close-off",
			setting: func(m domainpreferences.Motion) domainpreferences.Motion {
				m.PaneClose = false
				return m
			},
			wantOpen: true,
		},
		{
			name:    "reduce-motion",
			setting: func(m domainpreferences.Motion) domainpreferences.Motion { return m },
			reduce:  true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setting := tc.setting(sidePanelSetting(true, true,
				domainpreferences.MotionPopin, domainpreferences.MotionPopin))
			g, tt, wm, tabID, surface := startSidePanelTerminal(t, setting, state.DockLeft, sidePanelViewText("files-panel"))
			if tc.reduce {
				tt.SetPreferences(ui.Preferences{ReduceMotion: true})
			}
			surface.ResetResizeLog()
			openSidePanel(t, tt, g, tabID)
			if got := wm.panel.run != nil; got != tc.wantOpen {
				t.Fatalf("open run = %v, want %v", got, tc.wantOpen)
			}
			if !tc.wantOpen && wm.panel.animationFrames != 0 {
				t.Fatalf("the warped open asked for %d animation frames", wm.panel.animationFrames)
			}
			if !tc.wantOpen {
				// The warped open is already in its final slot.
				if _, found := tt.Find("side-panel"); !found {
					t.Fatal("the warped open did not build the panel")
				}
			}
			// A warped change still lets the Pane's own layout deliver its one
			// resize; wait for the grid to settle before counting.
			pumpUntil(t, tt, "the warped open grid", func() bool {
				return gridsSettled(surface)
			})
			if got := len(surface.ResizeLog()); got != 1 {
				t.Fatalf("the warped open delivered %d PTY resizes, want exactly 1; log %+v",
					got, surface.ResizeLog())
			}
			finishPanelRun(t, tt, wm)
			framesAfterOpen := wm.panel.animationFrames

			surface.ResetResizeLog()
			g.state.CloseFiles(tabID)
			tt.Frame()
			if got := wm.panel.run != nil; got != tc.wantClose {
				t.Fatalf("close run = %v, want %v", got, tc.wantClose)
			}
			if !tc.wantClose && wm.panel.animationFrames != framesAfterOpen {
				t.Fatalf("the warped close asked for %d animation frames",
					wm.panel.animationFrames-framesAfterOpen)
			}
			if !tc.wantClose {
				if _, found := tt.Find("side-panel"); found {
					t.Fatal("a warped close kept the panel in the layout")
				}
			}
			pumpUntil(t, tt, "the warped close grid", func() bool {
				return gridsSettled(surface)
			})
			if got := len(surface.ResizeLog()); got != 1 {
				t.Fatalf("the warped close delivered %d PTY resizes, want exactly 1; log %+v",
					got, surface.ResizeLog())
			}
		})
	}
}

// TestSidePanelInterruptionContinuesFromThePaintedSize checks that a close
// during an opening drawer, and an open during the close, start from what is
// on screen instead of snapping.
func TestSidePanelInterruptionContinuesFromThePaintedSize(t *testing.T) {
	setting := sidePanelSetting(true, true,
		domainpreferences.MotionPopin, domainpreferences.MotionPopin)
	g, tt, wm, tabID, surface := startSidePanelTerminal(t, setting, state.DockLeft, sidePanelViewText("files-panel"))
	surface.ResetResizeLog()

	openSidePanel(t, tt, g, tabID)
	if wm.panel.run == nil || wm.panel.run.kind != motion.PaneOpen {
		t.Fatalf("the open run = %+v, want PaneOpen", wm.panel.run)
	}
	if got := len(surface.ResizeLog()); got != 1 {
		t.Fatalf("the open delivered %d resizes, want 1", got)
	}
	tt.Advance(80 * time.Millisecond)
	tt.Frame()
	openSize, done := wm.panel.run.sizeAt(tt.Now())
	if done {
		t.Fatal("the open run ended before the interruption")
	}
	if abs(wm.panel.visible-openSize) > 1 {
		t.Fatalf("the open's painted size = %v, want the run's %v", wm.panel.visible, openSize)
	}

	surface.ResetResizeLog()
	g.state.CloseFiles(tabID)
	tt.Frame()
	closeRun := wm.panel.run
	if closeRun == nil || closeRun.kind != motion.PaneClose {
		t.Fatalf("the interrupted close run = %+v, want PaneClose", closeRun)
	}
	if abs(closeRun.from-openSize) > 1 {
		t.Fatalf("the close starts from %v, want the open's painted %v", closeRun.from, openSize)
	}
	closing, found := tt.Find("side-panel")
	if !found {
		t.Fatal("the interrupted close has no drawer")
	}
	if got := panelRevealSize(closing, state.DockLeft); abs(got-closeRun.from) > 1 {
		t.Fatalf("the interrupted close draws %v DIPs, want its start %v", got, closeRun.from)
	}
	if got := len(surface.ResizeLog()); got != 1 {
		t.Fatalf("the close delivered %d resizes, want 1", got)
	}
	tt.Advance(60 * time.Millisecond)
	tt.Frame()
	closeSize, done := wm.panel.run.sizeAt(tt.Now())
	if done {
		t.Fatal("the close run ended before the reverse interruption")
	}

	surface.ResetResizeLog()
	g.state.ToggleFiles(tabID, "", "")
	tt.Frame()
	reopenRun := wm.panel.run
	if reopenRun == nil || reopenRun.kind != motion.PaneOpen {
		t.Fatalf("the reversed run = %+v, want PaneOpen", reopenRun)
	}
	if abs(reopenRun.from-closeSize) > 1 {
		t.Fatalf("the reopen starts from %v, want the close's painted %v", reopenRun.from, closeSize)
	}
	if _, found := tt.Find("side-panel"); !found {
		t.Fatal("the reopened panel is not in the layout")
	}
	if got := len(surface.ResizeLog()); got != 1 {
		t.Fatalf("the reopen delivered %d resizes, want 1", got)
	}
	finishPanelRun(t, tt, wm)
}

// TestSidePanelCloseKeepsItsToolAlive checks that the tool painted in the
// closing drawer is held for every frame, then told to rest once the panel
// leaves.
func TestSidePanelCloseKeepsItsToolAlive(t *testing.T) {
	setting := sidePanelSetting(true, true,
		domainpreferences.MotionPopin, domainpreferences.MotionPopin)
	hides := 0
	notes := state.SideTool{
		ID: state.SideNotes,
		View: func(state.SideTarget, float32) func(*ui.Context) {
			return func(c *ui.Context) { ui.Text(c, "notes-panel") }
		},
		Hide: func() { hides++ },
	}
	g, tt, wm, tabID, _ := startSidePanelTerminal(t, setting, state.DockLeft,
		sidePanelViewText("files-panel"), notes)
	openSidePanel(t, tt, g, tabID)
	finishPanelRun(t, tt, wm)
	g.state.SetSideTool(tabID, state.SideNotes)
	pumpUntil(t, tt, "the notes tool", func() bool { return tt.HasText("notes-panel") })
	if hides != 0 {
		t.Fatalf("the notes tool was hidden %d times while shown", hides)
	}

	g.state.CloseFiles(tabID)
	tt.Frame()
	if wm.panel.run == nil || wm.panel.run.kind != motion.PaneClose {
		t.Fatalf("the close run = %+v, want PaneClose", wm.panel.run)
	}
	if !tt.HasText("notes-panel") {
		t.Fatal("the closing drawer does not paint the tool the user saw")
	}
	if hides != 0 {
		t.Fatalf("the closing drawer stopped its tool %d frames early", hides)
	}
	tt.Advance(60 * time.Millisecond)
	tt.Frame()
	if !tt.HasText("notes-panel") {
		t.Fatal("the closing drawer lost its tool mid-run")
	}
	if hides != 0 {
		t.Fatalf("the closing drawer stopped its tool %d frames early", hides)
	}
	finishPanelRun(t, tt, wm)
	if tt.HasText("notes-panel") {
		t.Fatal("the notes tool is still painted after the drawer left")
	}
	if hides != 1 {
		t.Fatalf("the notes tool was hidden %d times after the drawer, want once", hides)
	}
}

// TestSidePanelCloseWhileHiddenWarps checks that a panel closed while the
// window is hidden does not replay its animation when the window shows.
func TestSidePanelCloseWhileHiddenWarps(t *testing.T) {
	setting := sidePanelSetting(true, true,
		domainpreferences.MotionPopin, domainpreferences.MotionPopin)
	g, tt, wm, tabID, _ := startSidePanelTerminal(t, setting, state.DockLeft, sidePanelViewText("files-panel"))
	openSidePanel(t, tt, g, tabID)
	finishPanelRun(t, tt, wm)
	frames := wm.panel.animationFrames

	tt.SetWindowHidden(true)
	tt.Frame()
	g.state.CloseFiles(tabID)
	tt.SetWindowHidden(false)
	tt.Frame()
	if wm.panel.run != nil {
		t.Fatalf("a close seen across the hidden frame started a run: %+v", wm.panel.run)
	}
	if wm.panel.animationFrames != frames {
		t.Fatalf("the hidden close asked for %d animation frames, want none",
			wm.panel.animationFrames-frames)
	}
	if _, found := tt.Find("side-panel"); found {
		t.Fatal("the closed panel is still in the layout")
	}
}

// TestSidePanelDrawerPinsNestedPanes covers a split tree: the drawer shrinks
// every Pane by its own share of the lost width, and each Pane's final layout
// confirms the pinned viewport, which proves the pin was the true final box.
func TestSidePanelDrawerPinsNestedPanes(t *testing.T) {
	setting := sidePanelSetting(true, true,
		domainpreferences.MotionPopin, domainpreferences.MotionPopin)
	panel := state.FilesPanel{
		View:    sidePanelViewText("files-panel"),
		Dock:    func() state.PanelDock { return state.DockLeft },
		SetDock: func(state.PanelDock) {},
	}
	g := newRealTerminalGraph(t,
		state.WithMotion(func() domainpreferences.Motion { return setting }),
		state.WithFilesPanel(panel),
	)
	if err := g.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	waitForRealSurfaces(t, g.state, 1)
	if err := g.state.SplitRight(); err != nil {
		t.Fatal(err)
	}
	waitForRealSurfaces(t, g.state, 2)
	if err := g.state.SplitDown(); err != nil {
		t.Fatal(err)
	}
	waitForRealSurfaces(t, g.state, 3)
	tabID, ok := g.state.TabID(0)
	if !ok {
		t.Fatal("the new Tab has no id")
	}
	render, wm := viewWithPaneMotion(g.state, paneMotionMode{labels: true})
	tt := ui.NewTester(render, 900, 600)
	tt.SetLocale("en")
	tt.Frame()
	surfaces := settlePane(t, tt, g, 0)
	if len(surfaces) != 3 {
		t.Fatalf("the Tab holds %d surfaces, want 3", len(surfaces))
	}
	for _, surface := range surfaces {
		surface.ResetResizeLog()
	}

	openSidePanel(t, tt, g, tabID)
	if wm.panel.run == nil {
		t.Fatal("opening the panel started no run")
	}
	for i, surface := range surfaces {
		if !surface.viewportPinned {
			t.Fatalf("Pane %d was not pinned at the open", i+1)
		}
		if got := len(surface.ResizeLog()); got != 1 {
			t.Fatalf("Pane %d delivered %d resizes at the open, want 1", i+1, got)
		}
	}
	pinned := make([]appview.Viewport, len(surfaces))
	calls := make([]int, len(surfaces))
	resizes := make([]int, len(surfaces))
	for i, surface := range surfaces {
		pinned[i] = surface.Viewport()
		calls[i] = surface.GridForCalls()
		resizes[i] = len(surface.ResizeLog())
	}

	tt.Advance(80 * time.Millisecond)
	tt.Frame()
	for i, surface := range surfaces {
		assertNoPanelGridWork(t, fmt.Sprintf("Pane %d during the open", i+1), surface, calls[i], resizes[i])
		assertViewportNear(t, "a nested Pane during the open", surface.Viewport(), pinned[i])
	}

	finishPanelRun(t, tt, wm)
	for i, surface := range surfaces {
		if surface.viewportPinned {
			t.Fatalf("Pane %d's layout never confirmed the open's pinned viewport", i+1)
		}
		if got := len(surface.ResizeLog()); got != 1 {
			t.Fatalf("Pane %d delivered %d resizes over the open, want 1", i+1, got)
		}
		assertViewportNear(t, "a nested Pane after the open", surface.Viewport(), pinned[i])
	}

	for _, surface := range surfaces {
		surface.ResetResizeLog()
	}
	g.state.CloseFiles(tabID)
	tt.Frame()
	if wm.panel.run == nil || wm.panel.run.kind != motion.PaneClose {
		t.Fatalf("the close run = %+v, want PaneClose", wm.panel.run)
	}
	for i, surface := range surfaces {
		if !surface.viewportPinned {
			t.Fatalf("Pane %d was not pinned at the close", i+1)
		}
		if got := len(surface.ResizeLog()); got != 1 {
			t.Fatalf("Pane %d delivered %d resizes at the close, want 1", i+1, got)
		}
	}
	finishPanelRun(t, tt, wm)
	for i, surface := range surfaces {
		if surface.viewportPinned {
			t.Fatalf("Pane %d's layout never confirmed the close's pinned viewport", i+1)
		}
		if got := len(surface.ResizeLog()); got != 1 {
			t.Fatalf("Pane %d delivered %d resizes over the close, want 1", i+1, got)
		}
	}
}
