package shell

import (
	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/domain/model"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/shell/state"
)

// DetachedView renders one detached Tab in its own window: the Tab's split
// tree plus the actions that move it back or close the window. The
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
		t := c.Theme()

		ui.Column(c).Fill().Padding(8).Gap(8).Background(t.Background).Children(func() {
			ui.Row(c).Height(28).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, DetachedTitle(m, info)).FontSize(14).Bold().SingleLine()
				ui.Column(c).Grow(1)
				if ui.Button(c.Key("detached-move-main"), m.DetachedMoveToMain()).Clicked() {
					s.ReattachDetachedTab(tabID)
					c.Invalidate()
				}
				if ui.Button(c.Key("detached-close"), m.DetachedCloseWindow()).Clicked() {
					s.RequestDetachedClose(tabID)
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
// number, and a copy generation is marked.
func DetachedTitle(m locales.Messages, info state.DetachedTabInfo) string {
	return tabTitle(m, state.TabInfo{
		Number:      info.Number,
		TitleNumber: info.TitleNumber,
		Label:       info.Label,
		AutoTitle:   info.AutoTitle,
		Copy:        info.Copy,
	})
}
