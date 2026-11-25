package shell

import (
	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/domain/model"
	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/layout"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/shell/state"
)

// DetachedView renders one detached Tab in its own window: the Tab's split
// tree under a title bar that drags the window and moves the Tab back.
// The window controls close it. The
// composition root wraps it in the same App Lock gate as every other window.
func DetachedView(s *state.State, tabID model.TabID) func(*ui.Context) {
	return detachedView(s, tabID, newDetachedPaneMotion(s, tabID))
}

// detachedView is DetachedView with the window's Pane motion runtime
// supplied, so a test can read its run, recorded boxes and frame count. Each
// detached window owns one runtime for its fixed Tab; it has no Tab strip and
// no Tab events.
func detachedView(s *state.State, tabID model.TabID, wm *workspacePaneMotion) func(*ui.Context) {
	return func(c *ui.Context) {
		// Session opens started by a copy-to-window outcome finish off the UI
		// thread; the shared preamble applies them here so every Tab mutation
		// stays on this thread. A detached window shows terminals only, so it
		// always takes the terminal scheme's colors.
		m, t := shellPreamble(c, s, func() bool { return true })

		// The composition root records a frame it replaced with the App Lock
		// lock screen. This window's Pane runtime saw nothing of that frame,
		// so its next one warps every event recorded in the gap instead of
		// animating boxes it never showed.
		if s.TakeDetachedViewUnrendered(tabID) {
			wm.noteSkipped()
		}

		info, ok := s.DetachedTabInfo(tabID)
		if !ok {
			// The Tab moved back into the main window or closed. The
			// composition root closes this window; render nothing meanwhile.
			wm.noteSkipped()
			return
		}

		ref := detachedTabRef(s, tabID)
		ref.motion = wm
		ref.magnified = func() model.PaneID { return wm.effectiveMagnified }
		// preFrame runs before the tree is built so a Pane event freezes its
		// final grids and starts its overlay from this window's own recorded
		// boxes.
		wm.preFrame(c, s)
		layout.DetachedLayout(c, DetachedTitle(m, info), func() {
			if kit.IconButtonWith(c, icons.LogIn, m.DetachedMoveToMain(), kit.IconOptions{Role: kit.IconCard, Key: "detached-move-main"}).Clicked() {
				s.ReattachDetachedTab(tabID)
				c.Invalidate()
			}
		}, func() {
			root, focus, ok := s.DetachedTabTree(tabID)
			if !ok {
				return
			}
			ordinal := 0
			ref.split = !root.IsPane()
			// The wrapper records the region every final split box is computed
			// in. It fills the window body exactly, so the tree's layout is
			// unchanged.
			region := ui.Column(c).Fill()
			wm.recordRegion(region)
			region.Children(func() { paneTree(c, s, m, t, ref, root, focus, &ordinal) })
		})
		// The Pane overlays are window-level and are built after the final
		// layout, so every Pane's box and grid are already the final ones.
		wm.drawOverlay(c, ref, t, m)
	}
}

// DetachedTitle is the window and header title of a detached Tab. It uses the
// same rules as the main tab strip: an empty label names the local Session by
// number.
func DetachedTitle(m locales.Messages, info state.DetachedTabInfo) string {
	return tabTitle(m, state.TabInfo{
		Number:      info.Number,
		TitleNumber: info.TitleNumber,
		Label:       info.Label,
		Title:       info.Title,
		AutoTitle:   info.AutoTitle,
	})
}
