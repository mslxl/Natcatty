package transfer

import (
	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
	transferstate "github.com/mslxl/natcatty/ui/transfer/state"
)

// panelSide is the one pane the Session panel browses. The panel's state is
// its own, so the SFTP Tab's two panes are untouched by it.
const panelSide = transferstate.Left

// panelWidth is about how wide the panel beside a terminal is; it decides
// which of the list's columns fit.
const panelWidth float32 = 360

// SidePanel renders the SFTP panel a Session Tab shows beside its terminal,
// as the original's: the Host's files in one column, with the path, the list,
// and the file commands in a header and a menu. Dual-pane copying between two
// file systems stays in the SFTP Tab. onClose closes the panel.
func SidePanel(s *transferstate.State, onClose func()) func(*ui.Context) {
	return func(c *ui.Context) {
		s.Flush()
		s.EnsureStarted()

		m := locales.In(c.Locale())
		t := c.Theme()
		tab := s.Tab(panelSide)

		root := ui.Column(c).Fill().MinWidth(0).Background(t.Surface).
			Border(1, t.Border).Clip().Label(m.SessionFilesPanel()).Focusable()
		root.Children(func() {
			panelHeader(c, s, tab, m, t, onClose)
			if tab == nil {
				ui.Column(c).Grow(1).FillWidth().AlignItems(ui.Center).Justify(ui.Center).Children(func() {
					ui.Text(c, m.SftpNoTab()).FontSize(12).TextColor(t.TextMuted).SingleLine().MinWidth(0)
				})
				return
			}
			// The panel is the SFTP Tab's pane browser in one column: the
			// same breadcrumb, toolbar, list, and footer.
			browser(c, s, panelSide, tab, panelWidth, m, t)
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

// panelHeader matches the Pane header beside it: the Tab's title, then New
// folder, Refresh, the file menu, and Close as small icon buttons.
func panelHeader(c *ui.Context, s *transferstate.State, tab *transferstate.Tab,
	m locales.Messages, t *ui.Theme, onClose func()) {
	ready := tab != nil && tab.FS != nil
	ui.Row(c).FillWidth().Height(30).Padding(0, 4, 0, 10).Gap(4).Shrink(0).
		Background(kit.Muted(t)).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).
		AlignItems(ui.Center).Children(func() {
		ui.Icon(c, icons.Lucide(icons.FolderOpen)).Size(14, 14).TextColor(t.TextMuted)
		title := m.SessionFilesPanel()
		if tab != nil {
			title = paneTitle(m, tab)
		}
		ui.Text(c, title).FontSize(12).FontWeight(500).SingleLine().MinWidth(0).Grow(1).TextColor(t.Text)
		if ready {
			if panelButton(c, icons.FolderPlus, m.SftpNewFolder()).Clicked() {
				s.RequestNewFolder(panelSide)
			}
			if panelButton(c, icons.RefreshCw, m.SftpRefresh()).Clicked() {
				s.Refresh(panelSide)
			}
			panelButton(c, icons.EllipsisVertical, m.SftpBookmarks()).Menu(func(menu *ui.Menu) {
				panelMenu(menu, s, tab, m)
			})
		}
		if panelButton(c, icons.X, m.SessionFilesClose()).Clicked() && onClose != nil {
			onClose()
		}
	})
}

// panelMenu holds the commands the header has no room for: the file actions
// on the selection, the listing's view options, and the bookmarks.
func panelMenu(menu *ui.Menu, s *transferstate.State, tab *transferstate.Tab, m locales.Messages) {
	selected := len(tab.SelectedPaths()) > 0
	one := len(tab.SelectedPaths()) == 1
	if menu.Item(m.SftpNewFile()).Chosen() {
		s.RequestNewFile(panelSide)
	}
	if menu.Item(m.SftpRename()).Disabled(!one).Chosen() {
		s.RequestRename(panelSide)
	}
	if menu.Item(m.SftpPermissions()).Disabled(!one).Chosen() {
		s.RequestChmod(panelSide)
	}
	if menu.Item(m.SftpDelete()).Disabled(!selected).Chosen() {
		s.RequestDelete(panelSide)
	}
	menu.Separator()
	menu.Submenu(m.SftpSort(), func(sort *ui.Menu) { sortMenu(sort, s, panelSide, tab, m) })
	if menu.Item(m.SftpShowHidden()).Checked(tab.ShowHidden).Chosen() {
		s.ToggleHidden(panelSide)
	}
	menu.Submenu(m.SftpBookmarks(), func(saved *ui.Menu) { bookmarksMenu(saved, s, panelSide, tab, m) })
	menu.Separator()
	if menu.Item(m.SftpTransferCenterButton()).Chosen() {
		s.OpenCenter()
	}
}

func panelButton(c *ui.Context, icon, label string) ui.Element {
	t := c.Theme()
	button := ui.ButtonBase(c).Label(label).Tooltip(label).Size(22, 22).Radius(5).
		Justify(ui.Center).AlignItems(ui.Center).TextColor(t.TextMuted).Shrink(0)
	switch {
	case button.Pressed():
		button.Background(kit.Strong(t)).TextColor(t.Text)
	case button.Hovered():
		button.Background(kit.Subtle(t)).TextColor(t.Text)
	}
	button.Children(func() { ui.Icon(c, icons.Lucide(icon)).Size(14, 14) })
	return button
}
