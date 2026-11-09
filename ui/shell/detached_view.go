package shell

import (
	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/domain/model"
	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/layout"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/shell/state"
	uitheme "github.com/mslxl/natcatty/ui/theme"
)

// DetachedView renders one detached Tab in its own window: the Tab's split
// tree under a title bar that drags the window and moves the Tab back.
// The window controls close it. The
// composition root wraps it in the same App Lock gate as every other window.
func DetachedView(s *state.State, tabID model.TabID) func(*ui.Context) {
	return func(c *ui.Context) {
		// Session opens started by a copy-to-window outcome finish off the UI
		// thread; applying them here keeps every Tab mutation on this thread.
		s.Flush()

		info, ok := s.DetachedTabInfo(tabID)
		if !ok {
			// The Tab moved back into the main window or closed. The
			// composition root closes this window; render nothing meanwhile.
			return
		}

		m := locales.In(c.Locale())
		// The window shows terminals only, so it takes their scheme's colors
		// as the main window's chrome does.
		if preset, ok := s.TerminalTheme(); ok {
			c.SetTheme(uitheme.Chrome(c.Theme(), preset))
		}
		t := c.Theme()

		layout.DetachedLayout(c, DetachedTitle(m, info), func() {
			if kit.IconButtonWith(c, icons.LogIn, m.DetachedMoveToMain(), kit.IconOptions{Role: kit.IconCard, Key: "detached-move-main"}).Clicked() {
				s.ReattachDetachedTab(tabID)
				c.Invalidate()
			}
		}, func() {
			if root, focus, ok := s.DetachedTabTree(tabID); ok {
				ordinal := 0
				paneTree(c, s, m, t, detachedTabRef(s, tabID), root, focus, &ordinal)
			}
		})
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
