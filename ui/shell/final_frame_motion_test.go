package shell

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
	"github.com/mslxl/natcatty/ui/dropdown"
	"github.com/mslxl/natcatty/ui/internal/styletest"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/motion"
	"github.com/mslxl/natcatty/ui/shell/state"
	"github.com/mslxl/natcatty/ui/terminal/appview"
	uitheme "github.com/mslxl/natcatty/ui/theme"
)

// Ticket 21c freezes the final frame of every motion path: the final Pane
// layout, the final terminal grid, and the final strip boxes and selection,
// per Interface Style. These snapshots are JSON, not bitmaps: fonts and DPR
// must never make them flaky. The layout is in DIPs, which the style (not the
// font) decides; a terminal grid is stored as the grid of the frozen final
// viewport, and on a host whose measured cell differs the test derives it from
// that viewport instead (the fixture still records the grid observed here).
// Intermediate frames are asserted by the geometry/opacity tests below and by
// the per-event suites; this file never compares pixels.
const (
	motionSnapshotUpdateEnv = "NATCATTY_MOTION_UPDATE_GOLDEN"
	motionSnapshotDir       = "testdata/motion"
)

// snapshotDIP rounds a DIP value to MyGo's 1/64 layout lattice, so a
// floating-point hair cannot fail a snapshot while a real layout change does.
func snapshotDIP(v float32) float32 {
	return float32(math.Round(float64(v)*64)) / 64
}

type snapshotRect struct {
	X float32 `json:"x"`
	Y float32 `json:"y"`
	W float32 `json:"w"`
	H float32 `json:"h"`
}

func snapshotRectOf(r ui.Rect) snapshotRect {
	return snapshotRect{X: snapshotDIP(r.X), Y: snapshotDIP(r.Y), W: snapshotDIP(r.W), H: snapshotDIP(r.H)}
}

type snapshotSize struct {
	W float32 `json:"w"`
	H float32 `json:"h"`
}

type snapshotGrid struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}

// snapshotPane is one final Pane in tree order. Pending and Failed Panes have
// no Surface, so they carry no viewport or grid; their placeholder/error is
// the final body content.
type snapshotPane struct {
	Pending  bool          `json:"pending,omitempty"`
	Failed   bool          `json:"failed,omitempty"`
	Slot     snapshotRect  `json:"slot"`
	Column   snapshotRect  `json:"column"`
	Body     snapshotRect  `json:"body"`
	Framed   bool          `json:"framed"`
	Viewport *snapshotSize `json:"viewport,omitempty"`
	Grid     *snapshotGrid `json:"grid,omitempty"`
}

// snapshotStripItem is one final Work Tab box in strip order. The title names
// the item; IDs are random per run and never stored. Selected is the final
// selected fill, which changes at once and does not animate.
type snapshotStripItem struct {
	Title    string       `json:"title"`
	Box      snapshotRect `json:"box"`
	Selected bool         `json:"selected"`
}

// finalFrameSnapshot is the frozen final frame. Body is the final body surface
// ("tab" or "home"); the style is the fixture file, the case is the map key.
type finalFrameSnapshot struct {
	Region snapshotRect        `json:"region"`
	Panes  []snapshotPane      `json:"panes"`
	Strip  []snapshotStripItem `json:"strip,omitempty"`
	Body   string              `json:"body"`
	Cell   snapshotSize        `json:"cell"`
}

// finalFrameTheme is the same UI theme the style benchmark uses, so the
// snapshots cover the styles' fonts and materials as the app draws them.
func finalFrameTheme() *ui.Theme {
	preset := domaintheme.ResolvePreset(true, domaintheme.DefaultLightTheme, domaintheme.DefaultDarkTheme)
	return uitheme.UI(true, preset, uitheme.Options{})
}

// snapshotWindow is one styled offscreen window over a real-terminal graph,
// with the motion runtimes exposed for the settle and capture helpers.
type snapshotWindow struct {
	graph  *realTerminalGraph
	tt     *ui.Tester
	wm     *workspacePaneMotion
	reveal *workTabReveal
	// followBar is Follow's mini Tab strip runtime. It is a second view over
	// the same events, like the Work Tab strip, but its items are found by
	// their short titles because the mini strip labels none of them.
	followBar *tabStripMotion
}

// newSnapshotWindow builds a styled main window with tabs single-Pane Tabs,
// shows every body once so its terminal sizes, and rests on the last Tab.
func newSnapshotWindow(t *testing.T, style kit.Style, setting domainpreferences.Motion, width, height, tabs int) *snapshotWindow {
	t.Helper()
	g := motionGraph(t, setting)
	for range tabs {
		if err := g.state.NewTab(); err != nil {
			t.Fatalf("NewTab: %v", err)
		}
	}
	waitForRealSurfaces(t, g.state, tabs)
	reveal := newWorkTabReveal()
	view, wm := viewWithPaneMotion(g.state, paneMotionMode{labels: true, stripReveal: reveal})
	tt := ui.NewTester(styletest.Styled(style, finalFrameTheme(), view), width, height)
	tt.SetLocale("en")
	w := &snapshotWindow{graph: g, tt: tt, wm: wm, reveal: reveal}
	for index := range tabs {
		g.state.SelectTab(index)
		w.settle(t)
	}
	g.state.SelectTab(tabs - 1)
	w.settle(t)
	return w
}

// newStyledViewWindow wraps a view that is not the main shell (Follow,
// Topdown, Detach) in the same styled tester.
func newStyledViewWindow(t *testing.T, style kit.Style, view func(*ui.Context),
	g *realTerminalGraph, wm *workspacePaneMotion, width, height int) *snapshotWindow {
	t.Helper()
	tt := ui.NewTester(styletest.Styled(style, finalFrameTheme(), view), width, height)
	tt.SetLocale("en")
	return &snapshotWindow{graph: g, tt: tt, wm: wm}
}

// settle builds frames until every runtime of the view is idle and every drawn
// Pane's terminal is at its final grid. A Pending or failed Pane has no
// terminal; its placeholder is already final. The loop advances the fake
// clock only when a run is still playing, and it has a hard deadline so a
// stuck run fails instead of hanging.
func (w *snapshotWindow) settle(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		w.tt.Frame()
		idle := motionIdle(w.wm, w.reveal) && (w.followBar == nil || w.followBar.run == nil)
		grids := true
		if root, _, ok := w.wm.tabTree(); ok {
			for _, id := range domainworkspace.CollectPaneIDs(root) {
				info, ok := w.wm.paneInfo(id)
				if !ok {
					grids = false
					continue
				}
				if info.Surface == nil {
					if !info.Pending && info.Err == nil {
						grids = false
					}
					continue
				}
				surface, ok := info.Surface.(*realTerminalSurface)
				if !ok || !gridsSettled(surface) {
					grids = false
				}
			}
		}
		if idle && grids {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the final frame did not settle: run %+v body idle %v strip %+v",
				w.wm.run, w.wm.body == nil || w.wm.body.phase == bodyIdle, w.reveal == nil || w.reveal.strip.run == nil)
		}
		w.tt.Advance(16 * time.Millisecond)
	}
}

// split splits the active Tab right and rests on the settled two-Pane layout.
func (w *snapshotWindow) split(t *testing.T) {
	t.Helper()
	if err := w.graph.state.SplitRight(); err != nil {
		t.Fatalf("SplitRight: %v", err)
	}
	w.settle(t)
}

// rename gives every Tab a short title. A short title keeps a Work Tab at its
// fixed minimum width independently of the host's font, which is what makes
// the strip boxes reviewable fixtures instead of a font-metric report.
func (w *snapshotWindow) rename(t *testing.T, titles ...string) {
	t.Helper()
	for index, title := range titles {
		if !w.graph.state.RenameTab(index, title) {
			t.Fatalf("RenameTab(%d, %q) failed", index, title)
		}
	}
	w.settle(t)
}

// capture freezes the window's final frame. It fails when any moving copy,
// mask, or close ghost outlives its run: a final frame is the real layout, not
// an overlay.
func (w *snapshotWindow) capture(t *testing.T) finalFrameSnapshot {
	t.Helper()
	for _, text := range w.tt.Texts() {
		if strings.HasPrefix(text, "pane-motion:") || strings.HasPrefix(text, "pane-motion-mask:") ||
			strings.HasPrefix(text, "pane-close-ghost:") {
			t.Fatalf("the final frame still draws %q", text)
		}
	}
	if w.wm.run != nil {
		t.Fatalf("the final frame still has a Pane run: %+v", w.wm.run)
	}
	if w.wm.body != nil && w.wm.body.phase != bodyIdle {
		t.Fatalf("the final frame still has a body phase: %v", w.wm.body.phase)
	}
	if w.reveal != nil && w.reveal.strip.run != nil {
		t.Fatalf("the final frame still has a strip run: %+v", w.reveal.strip.run)
	}
	if w.followBar != nil && w.followBar.run != nil {
		t.Fatalf("the final frame still has a mini strip run: %+v", w.followBar.run)
	}

	frame := finalFrameSnapshot{
		Region: snapshotRectOf(w.wm.region),
		Panes:  []snapshotPane{},
		Body:   "tab",
		Cell:   snapshotSize{W: snapshotDIP(cellW(t)), H: snapshotDIP(cellH(t))},
	}
	root, _, ok := w.wm.tabTree()
	if !ok || (w.graph.state.TabCount() == 0 && w.wm.body != nil) {
		frame.Body = "home"
	} else {
		ids := domainworkspace.CollectPaneIDs(root)
		// A magnified Tab draws only the zoomed Pane; the tree keeps the
		// hidden siblings for the moment the zoom ends.
		if magnified := w.wm.magnifiedPane(); magnified != "" {
			ids = []model.PaneID{magnified}
		}
		for _, id := range ids {
			info, ok := w.wm.paneInfo(id)
			if !ok {
				t.Fatalf("Pane %q is in the tree but has no info", id)
			}
			rects, ok := w.wm.rects[id]
			if !ok || !rects.Valid() {
				t.Fatalf("Pane %q has no final boxes: %+v", id, rects)
			}
			pane := snapshotPane{
				Pending: info.Pending,
				Failed:  info.Err != nil,
				Slot:    snapshotRectOf(rects.Slot),
				Column:  snapshotRectOf(rects.Column),
				Body:    snapshotRectOf(rects.Body),
				Framed:  rects.Framed,
			}
			if info.Surface != nil {
				surface, ok := info.Surface.(*realTerminalSurface)
				if !ok {
					t.Fatalf("Pane %q surface = %T, want the real terminal surface", id, info.Surface)
				}
				viewport := surface.Viewport()
				grid := surface.Grid()
				want, ok := appview.GridFor(viewport)
				if !ok || grid != want {
					t.Fatalf("Pane %q final grid = %+v, want its final viewport's %+v", id, grid, want)
				}
				last, ok := surface.transport.last()
				if !ok || last != grid {
					t.Fatalf("Pane %q PTY's last size = %+v, %v; want the final grid %+v", id, last, ok, grid)
				}
				pane.Viewport = &snapshotSize{W: snapshotDIP(viewport.W), H: snapshotDIP(viewport.H)}
				pane.Grid = &snapshotGrid{Cols: grid.Cols, Rows: grid.Rows}
			}
			frame.Panes = append(frame.Panes, pane)
		}
	}
	if frame.Body == "home" {
		if _, ok := w.tt.Find(bodySpec{kind: bodySurfaceHome}.key()); !ok {
			t.Fatalf("the final Home frame does not show the Home body")
		}
	} else if w.wm.body != nil {
		tabID, _ := w.wm.tabID()
		if _, ok := w.tt.Find(bodySpec{kind: bodySurfaceTab, tabID: tabID}.key()); !ok {
			t.Fatalf("the final frame does not show Tab %q's body", tabID)
		}
	} else if len(frame.Panes) == 0 || frame.Panes[0].Grid == nil {
		// Pane-only views (Follow, Topdown, Detach) draw the tree directly and
		// own no whole-body surface; the real terminal is their final content.
		t.Fatalf("the final Pane-only frame does not draw its terminal")
	}
	if w.reveal == nil && w.followBar == nil {
		return frame
	}
	for index := 0; index < w.graph.state.TabCount(); index++ {
		info, ok := w.graph.state.TabInfo(index)
		if !ok {
			t.Fatalf("no Tab at index %d", index)
		}
		var box ui.Rect
		if w.reveal != nil {
			var found bool
			box, found = w.tt.Find(tabStripBoxLabel(info.ID))
			if !found {
				t.Fatalf("the strip item of Tab %q is not labeled", info.ID)
			}
		} else {
			// The mini strip labels no item, so its item is found by the short
			// title the case gives every Tab. A short title keeps the item at
			// its fixed minimum width rather than a measured text width.
			var found bool
			box, found = w.tt.Find(tabTitle(locales.In("en"), info))
			if !found {
				t.Fatalf("the mini strip is missing the Tab %q", info.ID)
			}
		}
		title := info.Title
		if title == "" {
			title = info.Label
		}
		frame.Strip = append(frame.Strip, snapshotStripItem{
			Title:    title,
			Box:      snapshotRectOf(box),
			Selected: info.Active,
		})
	}
	return frame
}

// cellW and cellH read the measured terminal cell through the appview test
// hook, so a fixture can record the metrics it was made under.
func cellW(t *testing.T) float32 {
	t.Helper()
	w, _ := appview.CellSize()
	return w
}

func cellH(t *testing.T) float32 {
	t.Helper()
	_, h := appview.CellSize()
	return h
}

// finalFrameCase is one style-independent scene. capture builds the scene,
// drives the event, settles, and returns the final frame; the runner repeats
// it under Classic and macOS.
type finalFrameCase struct {
	name    string
	capture func(t *testing.T, style kit.Style) finalFrameSnapshot
}

// finalFrameCases is the ticket's list: the main view's seven events, the
// batch close, last-Tab Home, a non-current close, pending/ready/failure, a
// magnified split and close, and the narrow strip. Follow, Topdown and Detach
// own the Pane rows; Quake's own final frames live in ui/quake. Detach has no
// detached-window Pane split, close or reorder operation to animate (only its
// split ratio and Tab close), so its applicable final frame is the state the
// detached window opens on.
func finalFrameCases() []finalFrameCase {
	return []finalFrameCase{
		{"main/pane-open", func(t *testing.T, style kit.Style) finalFrameSnapshot {
			w := newSnapshotWindow(t, style, matrixAllOn(), 900, 600, 1)
			w.split(t)
			// The split focuses its new Pane, and the Tab title follows the
			// focused Pane, so the rename comes after the last focus change.
			w.rename(t, "a")
			return w.capture(t)
		}},
		{"main/pane-close", func(t *testing.T, style kit.Style) finalFrameSnapshot {
			w := newSnapshotWindow(t, style, matrixAllOn(), 900, 600, 1)
			w.split(t)
			if !w.graph.state.CloseFocusedPane() {
				t.Fatalf("CloseFocusedPane returned false")
			}
			w.settle(t)
			w.rename(t, "a")
			return w.capture(t)
		}},
		{"main/pane-reorder", func(t *testing.T, style kit.Style) finalFrameSnapshot {
			w := newSnapshotWindow(t, style, matrixAllOn(), 900, 600, 1)
			w.split(t)
			root, _, ok := w.graph.state.TabTree(0)
			if !ok {
				t.Fatalf("the split Tab is missing")
			}
			if !w.graph.state.SetSplitRatio(0, root.SplitID, 70) {
				t.Fatalf("SetSplitRatio failed")
			}
			w.settle(t)
			ids := motionPaneIDsMust(t, w.graph)
			tabID, _ := w.graph.state.TabID(0)
			if !w.graph.state.MovePane(tabID, ids[1], tabID, ids[0], domainworkspace.SideLeft) {
				t.Fatalf("MovePane in one Tab returned false")
			}
			w.settle(t)
			w.rename(t, "a")
			return w.capture(t)
		}},
		{"main/pane-zoom", func(t *testing.T, style kit.Style) finalFrameSnapshot {
			w := newSnapshotWindow(t, style, matrixAllOn(), 900, 600, 1)
			w.split(t)
			if !w.graph.state.TogglePaneZoom() {
				t.Fatalf("TogglePaneZoom returned false")
			}
			w.settle(t)
			w.rename(t, "a")
			return w.capture(t)
		}},
		{"main/tab-open", func(t *testing.T, style kit.Style) finalFrameSnapshot {
			w := newSnapshotWindow(t, style, matrixAllOn(), 900, 600, 1)
			w.rename(t, "a")
			if err := w.graph.state.NewTab(); err != nil {
				t.Fatalf("NewTab: %v", err)
			}
			w.settle(t)
			w.rename(t, "a", "b")
			return w.capture(t)
		}},
		{"main/tab-close", func(t *testing.T, style kit.Style) finalFrameSnapshot {
			w := newSnapshotWindow(t, style, matrixAllOn(), 900, 600, 2)
			w.rename(t, "a", "b")
			if !w.graph.state.CloseActiveTab() {
				t.Fatalf("CloseActiveTab returned false")
			}
			w.settle(t)
			return w.capture(t)
		}},
		{"main/tab-switch", func(t *testing.T, style kit.Style) finalFrameSnapshot {
			w := newSnapshotWindow(t, style, matrixAllOn(), 900, 600, 2)
			w.rename(t, "a", "b")
			w.graph.state.SelectTab(0)
			w.settle(t)
			return w.capture(t)
		}},
		{"main/tab-reorder", func(t *testing.T, style kit.Style) finalFrameSnapshot {
			w := newSnapshotWindow(t, style, matrixAllOn(), 900, 600, 3)
			w.rename(t, "a", "b", "c")
			first := w.graph.mustTabID(t, 0)
			last := w.graph.mustTabID(t, 2)
			if !w.graph.state.MoveTab(last, first, false) {
				t.Fatalf("MoveTab returned false")
			}
			w.settle(t)
			return w.capture(t)
		}},
		{"main/tab-close-batch", func(t *testing.T, style kit.Style) finalFrameSnapshot {
			w := newSnapshotWindow(t, style, matrixAllOn(), 900, 600, 4)
			w.rename(t, "a", "b", "c", "d")
			if !w.graph.state.CloseTabsRight(0) {
				t.Fatalf("CloseTabsRight returned false")
			}
			w.settle(t)
			return w.capture(t)
		}},
		{"main/tab-close-last-home", func(t *testing.T, style kit.Style) finalFrameSnapshot {
			w := newSnapshotWindow(t, style, matrixAllOn(), 900, 600, 1)
			w.rename(t, "a")
			if !w.graph.state.CloseActiveTab() {
				t.Fatalf("CloseActiveTab returned false")
			}
			w.settle(t)
			return w.capture(t)
		}},
		{"main/tab-close-noncurrent", func(t *testing.T, style kit.Style) finalFrameSnapshot {
			w := newSnapshotWindow(t, style, matrixAllOn(), 900, 600, 3)
			w.rename(t, "a", "b", "c")
			if !w.graph.state.CloseTab(0) {
				t.Fatalf("CloseTab(0) returned false")
			}
			w.settle(t)
			return w.capture(t)
		}},
		{"main/pane-open-magnified", func(t *testing.T, style kit.Style) finalFrameSnapshot {
			w := newSnapshotWindow(t, style, matrixAllOn(), 900, 600, 1)
			w.split(t)
			ids := motionPaneIDsMust(t, w.graph)
			if !w.graph.state.SetMagnifiedPane(0, ids[0]) {
				t.Fatalf("SetMagnifiedPane returned false")
			}
			w.settle(t)
			if err := w.graph.state.SplitFocusedPane(domainworkspace.SideRight); err != nil {
				t.Fatalf("SplitFocusedPane: %v", err)
			}
			w.settle(t)
			w.rename(t, "a")
			return w.capture(t)
		}},
		{"main/pane-close-magnified", func(t *testing.T, style kit.Style) finalFrameSnapshot {
			w := newSnapshotWindow(t, style, matrixAllOn(), 900, 600, 1)
			w.split(t)
			ids := motionPaneIDsMust(t, w.graph)
			if !w.graph.state.SetMagnifiedPane(0, ids[0]) {
				t.Fatalf("SetMagnifiedPane returned false")
			}
			w.settle(t)
			if !w.graph.state.CloseFocusedPane() {
				t.Fatalf("CloseFocusedPane returned false")
			}
			w.settle(t)
			w.rename(t, "a")
			return w.capture(t)
		}},
		{"main/narrow-strip", func(t *testing.T, style kit.Style) finalFrameSnapshot {
			w := newSnapshotWindow(t, style, matrixAllOn(), 720, 480, 6)
			w.rename(t, "a", "b", "c", "d", "e", "f")
			return w.capture(t)
		}},
		{"main/pane-open-pending", func(t *testing.T, style kit.Style) finalFrameSnapshot {
			w := newPendingSnapshotWindow(t, style)
			if err := w.graph.state.SplitRight(); err != nil {
				t.Fatalf("SplitRight: %v", err)
			}
			w.tt.Frame()
			run := w.wm.run
			if run == nil {
				t.Fatalf("the pending split did not start a run")
			}
			newID := run.anim.Visuals[len(run.anim.Visuals)-1].Pane
			w.tt.Advance(400 * time.Millisecond)
			w.tt.Frame()
			if w.wm.run != nil {
				t.Fatalf("the pending run did not finish")
			}
			if info, ok := w.graph.state.PaneInfoAt(0, newID); !ok || !info.Pending {
				t.Fatalf("the new Pane is not pending: %+v", info)
			}
			if !w.tt.HasText(locales.In("en").PaneConnecting()) {
				t.Fatalf("the pending Pane's placeholder is not drawn")
			}
			w.rename(t, "a")
			return w.capture(t)
		}},
		{"main/pane-open-ready", func(t *testing.T, style kit.Style) finalFrameSnapshot {
			w, release := newGatedSnapshotWindow(t, style)
			if err := w.graph.state.SplitRight(); err != nil {
				t.Fatalf("SplitRight: %v", err)
			}
			w.tt.Frame()
			run := w.wm.run
			if run == nil {
				t.Fatalf("the pending split did not start a run")
			}
			newID := run.anim.Visuals[len(run.anim.Visuals)-1].Pane
			release()
			pumpUntil(t, w.tt, "the released Pane's surface", func() bool {
				info, ok := w.graph.state.PaneInfoAt(0, newID)
				return ok && info.Surface != nil
			})
			if w.wm.run != run {
				t.Fatalf("the ready swap restarted the run")
			}
			w.settle(t)
			w.rename(t, "a")
			return w.capture(t)
		}},
		{"main/pane-open-failure", func(t *testing.T, style kit.Style) finalFrameSnapshot {
			w := newFailingSnapshotWindow(t, style)
			if err := w.graph.state.SplitRight(); err != nil {
				t.Fatalf("SplitRight: %v", err)
			}
			w.tt.Frame()
			run := w.wm.run
			if run == nil {
				t.Fatalf("the failing split did not start a run")
			}
			newID := run.anim.Visuals[len(run.anim.Visuals)-1].Pane
			pumpUntil(t, w.tt, "the failed Pane", func() bool {
				info, ok := w.graph.state.PaneInfoAt(0, newID)
				return ok && info.Err != nil
			})
			w.tt.Advance(400 * time.Millisecond)
			w.tt.Frame()
			if w.wm.run != nil {
				t.Fatalf("the failing run did not finish")
			}
			w.rename(t, "a")
			return w.capture(t)
		}},
		{"follow/pane-open", func(t *testing.T, style kit.Style) finalFrameSnapshot {
			w := newFollowSnapshotWindow(t, style, false, 1)
			w.split(t)
			w.rename(t, "a")
			return w.capture(t)
		}},
		{"follow/pane-close", func(t *testing.T, style kit.Style) finalFrameSnapshot {
			w := newFollowSnapshotWindow(t, style, false, 1)
			w.split(t)
			if !w.graph.state.CloseFocusedPane() {
				t.Fatalf("CloseFocusedPane returned false")
			}
			w.settle(t)
			w.rename(t, "a")
			return w.capture(t)
		}},
		{"follow/mini-strip-reorder", func(t *testing.T, style kit.Style) finalFrameSnapshot {
			w := newFollowSnapshotWindow(t, style, false, 3)
			w.rename(t, "a", "b", "c")
			first := w.graph.mustTabID(t, 0)
			last := w.graph.mustTabID(t, 2)
			if !w.graph.state.MoveTab(last, first, false) {
				t.Fatalf("MoveTab returned false")
			}
			w.settle(t)
			return w.capture(t)
		}},
		{"topdown/pane-open", func(t *testing.T, style kit.Style) finalFrameSnapshot {
			w := newFollowSnapshotWindow(t, style, true, 1)
			w.split(t)
			w.rename(t, "a")
			return w.capture(t)
		}},
		{"detach/final", func(t *testing.T, style kit.Style) finalFrameSnapshot {
			w := newDetachedSnapshotWindow(t, style)
			return w.capture(t)
		}},
	}
}

// newFollowSnapshotWindow builds the Follow or Topdown renderer over tabs
// Tabs and rests it. Follow is mounted the way the app mounts it: its own bar
// above its Pane renderer inside the Dropdown Panel's frame. Topdown draws the
// whole shell, including the Work Tab strip, so its reveal is exposed too.
func newFollowSnapshotWindow(t *testing.T, style kit.Style, topdown bool, tabs int) *snapshotWindow {
	t.Helper()
	g := motionGraph(t, matrixAllOn())
	for range tabs {
		if err := g.state.NewTab(); err != nil {
			t.Fatalf("NewTab: %v", err)
		}
	}
	waitForRealSurfaces(t, g.state, tabs)
	var view func(*ui.Context)
	var wm *workspacePaneMotion
	var reveal *workTabReveal
	var followBar *tabStripMotion
	if topdown {
		reveal = newWorkTabReveal()
		view, wm = topdownWithPaneMotion(g.state, paneMotionMode{labels: true, stripReveal: reveal})
	} else {
		followBar = &tabStripMotion{positionOnly: true}
		body, bodyMotion := followWithPaneMotion(g.state)
		wm = bodyMotion
		view = dropdown.View(nil, followBarWithStrip(g.state, followBar), body)
	}
	w := newStyledViewWindow(t, style, view, g, wm, 600, 400)
	w.reveal = reveal
	w.followBar = followBar
	// Follow draws only the active Tab's Panes; show each Tab once so every
	// Pane has its final viewport and grid before the captured event.
	for index := range tabs {
		g.state.SelectTab(index)
		w.settle(t)
	}
	g.state.SelectTab(tabs - 1)
	w.settle(t)
	return w
}

// newDetachedSnapshotWindow builds a detached window over a real-terminal Tab,
// with its Pane runtime and no Tab strip.
func newDetachedSnapshotWindow(t *testing.T, style kit.Style) *snapshotWindow {
	t.Helper()
	service := appsession.NewService(sessionTransportFactory{})
	t.Cleanup(service.CloseAll)
	surfaces := &realTerminalSurfaceFactory{}
	st := state.NewState(
		state.WithSessions(service, surfaces.open),
		state.WithMotion(matrixAllOn),
		state.WithDetachHandler(func(model.TabID) {}),
		state.WithDetachReattachHandler(func(model.TabID) {}),
	)
	t.Cleanup(func() {
		for _, surface := range surfaces.all() {
			surface.Close()
		}
	})
	if err := st.NewTab(); err != nil {
		t.Fatalf("NewTab: %v", err)
	}
	waitForRealSurfaces(t, st, 1)
	// The detached window's final frame is the split it never animated: the
	// Tab is split while still attached, then handed to its own window.
	if err := st.SplitRight(); err != nil {
		t.Fatalf("SplitRight: %v", err)
	}
	waitForRealSurfaces(t, st, 2)
	if !st.DetachTab(0) {
		t.Fatalf("DetachTab returned false")
	}
	ids := st.DetachedTabIDs()
	if len(ids) != 1 {
		t.Fatalf("detached Tabs = %v, want one", ids)
	}
	g := &realTerminalGraph{state: st, factory: surfaces}
	wm := newDetachedPaneMotion(st, ids[0])
	w := newStyledViewWindow(t, style, detachedView(st, ids[0], wm), g, wm, 640, 480)
	w.settle(t)
	return w
}

// newPendingSnapshotWindow builds the production-overlay route over a gated
// Session factory; the second Session never opens.
func newPendingSnapshotWindow(t *testing.T, style kit.Style) *snapshotWindow {
	t.Helper()
	var once sync.Once
	release := make(chan struct{})
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	return newControlledSnapshotWindow(t, style, &gatedTransportFactory{release: release})
}

// newGatedSnapshotWindow is the same graph with the release action returned,
// so a ready case can open the second Session and swap the placeholder without
// replaying the run.
func newGatedSnapshotWindow(t *testing.T, style kit.Style) (*snapshotWindow, func()) {
	t.Helper()
	var once sync.Once
	release := make(chan struct{})
	fire := func() { once.Do(func() { close(release) }) }
	t.Cleanup(fire)
	return newControlledSnapshotWindow(t, style, &gatedTransportFactory{release: release}), fire
}

// newFailingSnapshotWindow builds the route whose second Session fails.
func newFailingSnapshotWindow(t *testing.T, style kit.Style) *snapshotWindow {
	t.Helper()
	return newControlledSnapshotWindow(t, style, &failingTransportFactory{})
}

// newControlledSnapshotWindow is newControlledMotionGraph with a styled view
// and the strip runtime exposed; it rests on one Tab.
func newControlledSnapshotWindow(t *testing.T, style kit.Style, factory appsession.Factory) *snapshotWindow {
	t.Helper()
	service := appsession.NewService(factory)
	t.Cleanup(service.CloseAll)
	surfaces := &realTerminalSurfaceFactory{}
	st := state.NewState(
		state.WithSessions(service, surfaces.open),
		state.WithMotion(matrixAllOn),
	)
	t.Cleanup(func() {
		for _, surface := range surfaces.all() {
			surface.Close()
		}
	})
	if err := st.NewTab(); err != nil {
		t.Fatalf("NewTab: %v", err)
	}
	reveal := newWorkTabReveal()
	view, wm := viewWithPaneMotion(st, paneMotionMode{labels: true, stripReveal: reveal})
	g := &realTerminalGraph{state: st, factory: surfaces}
	waitForRealSurfaces(t, st, 1)
	w := newStyledViewWindow(t, style, view, g, wm, 900, 600)
	w.reveal = reveal
	w.rename(t, "a")
	return w
}

// TestMotionFinalFramesMatchSnapshots is ticket 21c's final-frame gate: every
// listed event and view is replayed under both Interface Styles, settled to
// its final frame, and compared with the recorded JSON snapshot. Intermediate
// frames are never part of these fixtures.
func TestMotionFinalFramesMatchSnapshots(t *testing.T) {
	styletest.Each(t, func(t *testing.T) {
		style := kit.CurrentStyle()
		cases := finalFrameCases()
		got := make(map[string]finalFrameSnapshot, len(cases))
		for _, tc := range cases {
			tc := tc
			t.Run(tc.name, func(t *testing.T) {
				got[tc.name] = tc.capture(t, style)
			})
		}
		if t.Failed() {
			return
		}
		checkMotionSnapshotFile(t, style.ID(), got)
	})
}

// checkMotionSnapshotFile compares one style's final frames with its JSON
// fixture, or rewrites the fixture when NATCATTY_MOTION_UPDATE_GOLDEN=1.
func checkMotionSnapshotFile(t *testing.T, styleID string, got map[string]finalFrameSnapshot) {
	t.Helper()
	path := filepath.Join(motionSnapshotDir, styleID+".json")
	if os.Getenv(motionSnapshotUpdateEnv) == "1" {
		data, err := json.MarshalIndent(got, "", "  ")
		if err != nil {
			t.Fatalf("marshal the %s motion snapshot: %v", styleID, err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create the motion snapshot directory: %v", err)
		}
		if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the %s motion snapshot (run with %s=1 to record it): %v", styleID, motionSnapshotUpdateEnv, err)
	}
	var want map[string]finalFrameSnapshot
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	for name, gotFrame := range got {
		wantFrame, ok := want[name]
		if !ok {
			t.Errorf("%s has no snapshot for %q (run with %s=1 to record it)", path, name, motionSnapshotUpdateEnv)
			continue
		}
		// A terminal grid is one viewport divided by the measured cell. The
		// viewport is layout, and therefore frozen; the grid is derived from
		// the host's font. Keep the literal grid when the metrics match, and
		// otherwise derive both the grid and the recorded cell from this host,
		// so a different font compares the frozen viewport instead of failing.
		if wantFrame.Cell != gotFrame.Cell {
			for index := range wantFrame.Panes {
				pane := &wantFrame.Panes[index]
				if pane.Viewport == nil {
					continue
				}
				grid, ok := appview.GridFor(appview.Viewport{W: pane.Viewport.W, H: pane.Viewport.H})
				if ok {
					pane.Grid = &snapshotGrid{Cols: grid.Cols, Rows: grid.Rows}
				}
			}
			wantFrame.Cell = gotFrame.Cell
		}
		if !reflect.DeepEqual(wantFrame, gotFrame) {
			wantJSON, _ := json.MarshalIndent(wantFrame, "", "  ")
			gotJSON, _ := json.MarshalIndent(gotFrame, "", "  ")
			t.Errorf("final frame %q in %s changed\nwant:\n%s\ngot:\n%s", name, path, wantJSON, gotJSON)
		}
	}
	for name := range want {
		if _, ok := got[name]; !ok {
			t.Errorf("%s records final frame %q but the test no longer builds it", path, name)
		}
	}
}

// TestMotionIntermediateFramesUseOnlyGeometryOpacityAndPaint is ticket 21c's
// intermediate-frame policy in executable form: at a mid-run frame, per
// Interface Style, the moving copy's box is strictly between its start and
// end, its opacity follows the style, and the labelled overlay and mask carry
// the paint attribution. No pixel is sampled or compared with a recorded
// image.
func TestMotionIntermediateFramesUseOnlyGeometryOpacityAndPaint(t *testing.T) {
	styletest.Each(t, func(t *testing.T) {
		style := kit.CurrentStyle()
		g := motionGraph(t, matrixAllOn())
		if err := g.state.NewTab(); err != nil {
			t.Fatalf("NewTab: %v", err)
		}
		waitForRealSurfaces(t, g.state, 1)
		view, wm := viewWithPaneMotion(g.state, paneMotionMode{labels: true})
		tt := ui.NewTester(styletest.Styled(style, finalFrameTheme(), view), 900, 600)
		tt.SetLocale("en")
		w := &snapshotWindow{graph: g, tt: tt, wm: wm}
		w.settle(t)
		oldID, _ := g.state.FocusedPaneID(0)
		oldRect, ok := wm.rects[oldID]
		if !ok || !oldRect.Valid() {
			t.Fatalf("the old Pane has no resting boxes: %+v", oldRect)
		}

		if err := g.state.SplitRight(); err != nil {
			t.Fatalf("SplitRight: %v", err)
		}
		tt.Frame()
		run := wm.run
		if run == nil {
			t.Fatalf("%s did not start a Pane-open run", style.ID())
		}
		newID, _ := g.state.FocusedPaneID(0)
		oldVisual, ok := run.anim.VisualFor(oldID)
		if !ok {
			t.Fatalf("the old Pane has no visual: %+v", run.anim.Visuals)
		}
		entry, ok := run.anim.VisualFor(newID)
		if !ok || entry.Kind != motion.Enter {
			t.Fatalf("the new Pane has no entrance visual: %+v", run.anim.Visuals)
		}
		tt.Advance(80 * time.Millisecond)
		k, done := run.anim.Progress(tt.Now())
		if done || k <= 0 || k >= 1 {
			t.Fatalf("the mid-run progress = %v, %v", k, done)
		}
		oldBox, oldOpacity := run.anim.State(oldVisual, k)
		if oldBox.W >= oldRect.Column.W || oldBox.W <= oldVisual.To.W {
			t.Fatalf("the old Pane's moving width = %v, want between %v and %v",
				oldBox.W, oldVisual.To.W, oldRect.Column.W)
		}
		if oldOpacity != 1 {
			t.Fatalf("the old Pane's moving opacity = %v, want 1 (geometry only)", oldOpacity)
		}
		entryBox, entryOpacity := run.anim.State(entry, k)
		if entryBox.W < entry.To.W*motion.PopinStartScale-0.5 || entryBox.W > entry.To.W+0.5 {
			t.Fatalf("the entrance box = %+v, want within [87%%, 100%%] of the final %+v", entryBox, entry.To)
		}
		if entryOpacity < 0 || entryOpacity > 1 {
			t.Fatalf("the entrance opacity = %v, want inside [0,1]", entryOpacity)
		}
		// Paint attribution: the moving old Pane's copy, the entrance copy and
		// the old Pane's mask are the elements that paint this frame.
		if _, ok := tt.Find("pane-motion:" + string(oldID)); !ok {
			t.Fatalf("%s: the old Pane's moving copy is not painted", style.ID())
		}
		if _, ok := tt.Find("pane-motion:" + string(newID)); !ok {
			t.Fatalf("%s: the new Pane's entrance copy is not painted", style.ID())
		}
		if _, ok := tt.Find("pane-motion-mask:" + string(oldID)); !ok {
			t.Fatalf("%s: the old Pane's final-position mask is not painted", style.ID())
		}
		// Paint attribution is the labels above, never a pixel: the same
		// background colour can sit under a resting and a moving frame, so a
		// pixel comparison would say nothing about what moved.
	})
}
