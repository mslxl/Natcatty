package transfer

import (
	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/locales"
	transferstate "github.com/mslxl/natcatty/ui/transfer/state"
)

// panelSide is the one pane the Session panel browses. The panel's state is
// its own, so the SFTP Tab's two panes are untouched by it.
const panelSide = transferstate.Left

// SidePanel renders the SFTP tool of a Session Tab's side panel, as the
// original's: the Host's files in one column, starting with the pane's
// breadcrumb and toolbar. The shell draws the panel's tool strip above it.
// width is about how wide the panel is; it decides which of the list's
// columns fit. Dual-pane copying between two file systems stays in the SFTP
// Tab.
func SidePanel(s *transferstate.State, width float32) func(*ui.Context) {
	return func(c *ui.Context) {
		s.Flush()
		s.EnsureStarted()

		m := locales.In(c.Locale())
		t := c.Theme()
		tab := s.Tab(panelSide)

		root := ui.Column(c).Fill().MinWidth(0).Background(t.Surface).Clip().
			Label(m.SessionFilesPanel()).Focusable()
		root.Children(func() {
			if tab == nil {
				ui.Column(c).Grow(1).FillWidth().AlignItems(ui.Center).Justify(ui.Center).Children(func() {
					ui.Text(c, m.SftpNoTab()).FontSize(12).TextColor(t.TextMuted).SingleLine().MinWidth(0)
				})
				return
			}
			browser(c, s, panelSide, tab, width, m, t)
			operationBar(c, s, m, t)
		})
		if tab != nil && tab.FS != nil {
			// OS files dropped on the panel upload into the current folder; a
			// folder row narrows the target to that folder.
			if files := root.DroppedFiles(); len(files) > 0 {
				s.DropFiles(panelSide, tab.Path, files)
			}
			if root.FileDragOver() {
				root.Border(1, t.Accent)
			}
		}
		if s.Dialog() == nil && !s.CenterOpen() {
			installShortcuts(root, s)
		}
		centerView(c, s, m, t)
		dialogView(c, s, m, t)
	}
}
