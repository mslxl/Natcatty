package shell

import (
	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/domain/model"
	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/shell/state"
	uitheme "github.com/mslxl/natcatty/ui/theme"
	"github.com/mslxl/natcatty/ui/titlebar"
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

		ui.Column(c).Fill().Background(t.Background).Children(func() {
			// As the original's terminal popup, the title bar is the terminal's
			// own background with its title dimmed, and the controls follow.
			left, right := titlebar.Insets(c, titlebar.Popup)
			ui.Row(c).FillWidth().Height(topBarHeight).Shrink(0).Padding(0, 4+right, 0, 16+left).Gap(8).
				AlignItems(ui.Center).Background(t.Background).DragWindow().Children(func() {
				ui.Text(c, DetachedTitle(m, info)).FontSize(kit.M(c).Font.Footnote).FontWeight(500).TextColor(t.Text.Alpha(0.7)).
					SingleLine().MinWidth(0).Shrink(1)
				ui.Column(c).Grow(1)
				if barButton(c, "detached-move-main", icons.LogIn, m.DetachedMoveToMain()).Clicked() {
					s.ReattachDetachedTab(tabID)
					c.Invalidate()
				}
			})

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
