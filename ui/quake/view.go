package quake

import (
	"fmt"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
)

// Bar draws the Quake Workspace's compact top bar: one entry per Tab, the add
// control that opens another local Shell, and switching and closing of the
// Tabs. The Dropdown Panel's frame owns the hide button beside it, outside the
// App Lock gate, so the Panel can still be hidden while the content is locked.
func Bar(s *State) func(*ui.Context) {
	return func(c *ui.Context) {
		m := locales.In(c.Locale())
		// Click actions are applied after the strip is built: closing a Tab
		// would shift the indexes of the entries still being drawn.
		selected, closed := -1, -1
		added := kit.TabStrip(c, kit.TabPane, kit.TabStripOptions{
			Label: m.AppName(),
			Add:   m.MenuNewTab(),
		}, func() {
			for _, tab := range s.Tabs() {
				title := tab.Label
				if title == "" {
					title = m.LocalSessionTab(tab.Number)
				}
				result := kit.Tab(c, kit.TabPane, kit.TabSpec{
					Key:    fmt.Sprintf("quake-tab-%d", tab.Number),
					Title:  title,
					Active: tab.Active,
					Close:  m.MenuCloseTab(),
				})
				if result.Closed {
					closed = tab.Index
					continue
				}
				if result.Clicked {
					selected = tab.Index
				}
			}
		})
		switch {
		case closed >= 0:
			s.CloseTab(closed)
		case selected >= 0:
			s.SelectTab(selected)
		}
		if added {
			_ = s.AddLocalTab()
		}
	}
}

// View draws the active Quake Tab's terminal in the Panel's remaining body.
// The active Surface is rendered every frame through FocusedSurface so the
// keyboard follows a Tab switch after MyGo pruned the previous subtree. A
// failed first show explains itself instead of leaving an empty Panel.
func View(s *State) func(*ui.Context) {
	return func(c *ui.Context) {
		tab, ok := s.Active()
		if !ok {
			if s.Problem() != nil {
				ui.Column(c).Fill().Justify(ui.Center).Children(func() {
					kit.EmptyState(c, icons.TriangleAlert, locales.In(c.Locale()).PaneOpenFailed(), "", nil)
				})
			}
			return
		}
		ui.Column(c).FillWidth().Grow(1).MinWidth(0).MinHeight(0).Children(func() {
			if focused, ok := tab.Surface.(FocusedSurface); ok {
				focused.ViewFocused(c, true)
				return
			}
			tab.Surface.View(c)
		})
	}
}
