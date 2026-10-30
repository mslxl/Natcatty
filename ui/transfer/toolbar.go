package transfer

import (
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
	transferstate "github.com/mslxl/natcatty/ui/transfer/state"
)

// maxCrumbs is how many trailing folders the breadcrumb shows before it
// folds the middle into an ellipsis.
const maxCrumbs = 4

// toolbar is the pane's two rows, as the original's: the breadcrumb, which a
// double-click turns into a path field, and the icon actions.
func toolbar(c *ui.Context, s *transferstate.State, side transferstate.Side, tab *transferstate.Tab,
	m locales.Messages, t *ui.Theme) {
	ui.Column(c).FillWidth().Shrink(0).Background(kit.Muted(t)).
		BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
		ui.Row(c).FillWidth().Height(30).Padding(0, 8).Gap(2).AlignItems(ui.Center).Children(func() {
			if tab.EditingPath {
				pathField(c, s, side, tab, m)
				return
			}
			breadcrumb(c, s, side, tab, m, t)
		})
		ui.Row(c).FillWidth().Height(30).Padding(0, 6).Gap(2).AlignItems(ui.Center).
			BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
			actions(c, s, side, tab, m, t)
		})
	})
}

func pathField(c *ui.Context, s *transferstate.State, side transferstate.Side, tab *transferstate.Tab, m locales.Messages) {
	input := ui.TextInput(c, &tab.Draft)
	input.Placeholder(m.SftpPathPlaceholder())
	input.FontSize(12)
	input.Height(24)
	input.Grow(1)
	input.MinWidth(0)
	input.AutoFocus()
	if input.Submitted() {
		s.NavigateDraft(side)
	}
	if input.Shortcut(0, ui.KeyEscape) {
		s.CancelPathEdit(side)
	}
	if toolButton(c, icons.X, m.SftpCancel(), false).Clicked() {
		s.CancelPathEdit(side)
	}
}

// crumb is one folder of the breadcrumb and the path it opens.
type crumb struct {
	label, path string
}

// crumbs splits a path into its folders. A Windows path starts at its drive;
// a POSIX path starts below "/", which the breadcrumb shows as its own
// button.
func crumbs(path string) (posix bool, out []crumb) {
	if len(path) >= 2 && path[1] == ':' {
		drive := path[:2]
		out = append(out, crumb{drive, drive + `\`})
		current := drive
		for _, part := range strings.FieldsFunc(path[2:], func(r rune) bool { return r == '\\' || r == '/' }) {
			current += `\` + part
			out = append(out, crumb{part, current})
		}
		return false, out
	}
	current := ""
	for _, part := range strings.Split(path, "/") {
		if part == "" {
			continue
		}
		current += "/" + part
		out = append(out, crumb{part, current})
	}
	return true, out
}

func breadcrumb(c *ui.Context, s *transferstate.State, side transferstate.Side, tab *transferstate.Tab,
	m locales.Messages, t *ui.Theme) {
	posix, parts := crumbs(tab.Path)
	if toolButton(c, icons.House, m.SftpGoHome(), false).Disabled(tab.Home == "").Clicked() {
		s.GoHome(side)
	}
	if posix {
		root := crumbButton(c, "/", m.SftpGoRoot(), false, t)
		if root.Clicked() && tab.Path != "/" {
			s.Navigate(side, "/")
		}
	}
	area := ui.Row(c).Grow(1).MinWidth(0).FillHeight().Gap(2).AlignItems(ui.Center).
		Radius(4).Padding(0, 2).Cursor(ui.CursorText).Tooltip(m.SftpEditPathHint()).Clip()
	if area.Hovered() {
		area.Background(kit.Subtle(t))
	}
	area.Children(func() {
		shown := parts
		var folded []crumb
		if len(parts) > maxCrumbs {
			folded = parts[1 : len(parts)-maxCrumbs+1]
			shown = append([]crumb{parts[0]}, parts[len(parts)-maxCrumbs+1:]...)
		}
		for i, part := range shown {
			if i > 0 || posix {
				chevron(c, t)
			}
			if i == 1 && len(folded) > 0 {
				names := make([]string, len(folded))
				for j, f := range folded {
					names[j] = f.label
				}
				more := ui.ButtonBase(c).Label(strings.Join(names, " > ")).Tooltip(strings.Join(names, " > ")).
					Padding(0, 4).Height(20).Radius(4).AlignItems(ui.Center).TextColor(t.TextMuted).Shrink(0)
				more.Children(func() { ui.Icon(c, icons.Lucide(icons.Ellipsis)).Size(12, 12) })
				more.Menu(func(menu *ui.Menu) {
					for _, f := range folded {
						if menu.Item(f.label).Chosen() {
							s.Navigate(side, f.path)
						}
					}
				})
				chevron(c, t)
			}
			last := i == len(shown)-1
			if crumbButton(c, part.label, part.path, last, t).Clicked() && part.path != tab.Path {
				s.Navigate(side, part.path)
			}
		}
	})
	if area.DoubleClicked() {
		s.EditPath(side)
	}
}

func chevron(c *ui.Context, t *ui.Theme) {
	ui.Icon(c, icons.Lucide(icons.ChevronRight)).Size(12, 12).TextColor(t.TextMuted.Alpha(0.5)).Shrink(0)
}

func crumbButton(c *ui.Context, label, tooltip string, current bool, t *ui.Theme) ui.Element {
	button := ui.ButtonBase(c).Label(label).Tooltip(tooltip).Height(20).Padding(0, 4).Radius(4).
		AlignItems(ui.Center).MaxWidth(160).MinWidth(0).Shrink(0)
	color := t.TextMuted
	if current {
		color = t.Text
	}
	if button.Hovered() {
		button.Background(kit.Strong(t))
		color = t.Text
	}
	button.Children(func() {
		text := ui.Text(c, label).FontSize(12).SingleLine().MinWidth(0).TextColor(color)
		if current {
			text.FontWeight(500)
		}
	})
	return button
}

// actions is the toolbar's icon row, in the original's order.
func actions(c *ui.Context, s *transferstate.State, side transferstate.Side, tab *transferstate.Tab,
	m locales.Messages, t *ui.Theme) {
	ready := tab.FS != nil

	bookmark := toolButton(c, icons.Bookmark, m.SftpBookmarks(), false).Disabled(!ready)
	if s.Bookmarked(side) {
		bookmark.TextColor(uiYellow)
	}
	bookmark.Menu(func(menu *ui.Menu) { bookmarksMenu(menu, s, side, tab, m) })
	if toolButton(c, icons.ClipboardCopy, m.SftpCopyPath(), false).Disabled(tab.Path == "").Clicked() {
		mygo.Clipboard.WriteText(tab.Path)
	}
	density := m.SftpCompactList()
	if tab.Compact {
		density = m.SftpComfortableList()
	}
	if toolButton(c, icons.Rows3, density, tab.Compact).Clicked() {
		s.ToggleCompact(side)
	}
	if toolButton(c, icons.Search, m.SftpFilter(), tab.FilterOpen).Clicked() {
		s.ToggleFilter(side)
	}
	if toolButton(c, icons.FolderPlus, m.SftpNewFolder(), false).Disabled(!ready).Clicked() {
		s.RequestNewFolder(side)
	}
	if toolButton(c, icons.FilePlus, m.SftpNewFile(), false).Disabled(!ready).Clicked() {
		s.RequestNewFile(side)
	}
	hidden := icons.EyeOff
	if tab.ShowHidden {
		hidden = icons.Eye
	}
	if toolButton(c, hidden, m.SftpShowHiddenFiles(), tab.ShowHidden).Clicked() {
		s.ToggleHidden(side)
	}
	refresh := icons.RefreshCw
	if tab.Loading {
		refresh = icons.Loader
	}
	if toolButton(c, refresh, m.SftpRefresh(), false).Disabled(!ready).Clicked() {
		s.Refresh(side)
	}
	ui.Column(c).Grow(1)
	toolButton(c, icons.Ellipsis, m.SftpMore(), false).Menu(func(menu *ui.Menu) {
		menu.Submenu(m.SftpSort(), func(sort *ui.Menu) { sortMenu(sort, s, side, tab, m) })
		menu.Separator()
		if menu.Item(m.SftpTransferCenter()).Chosen() {
			s.OpenCenter()
		}
		menu.Separator()
		if menu.Item(m.SftpDuplicateTab()).Chosen() {
			s.Duplicate(side)
		}
		if menu.Item(m.SftpCloseTab()).Chosen() {
			s.CloseTab(side)
		}
	})
}

// uiYellow marks a bookmarked folder, as the original's yellow bookmark.
var uiYellow = ui.Hex("#EAB308")

// toolButton is the toolbar's 24-DIP ghost icon button; active fills it.
func toolButton(c *ui.Context, icon, label string, active bool) ui.Element {
	t := c.Theme()
	button := ui.ButtonBase(c).Label(label).Tooltip(label).Size(24, 24).Radius(5).
		Justify(ui.Center).AlignItems(ui.Center).TextColor(t.TextMuted).Shrink(0)
	switch {
	case button.Pressed():
		button.Background(kit.Strong(t)).TextColor(t.Text)
	case active:
		button.Background(kit.Strong(t)).TextColor(t.Text)
	case button.Hovered():
		button.Background(kit.Subtle(t)).TextColor(t.Text)
	}
	button.Children(func() { ui.Icon(c, icons.Lucide(icon)).Size(14, 14) })
	return button
}

// filterBar narrows the listing by name. Esc clears the text, then closes.
func filterBar(c *ui.Context, s *transferstate.State, side transferstate.Side, tab *transferstate.Tab,
	m locales.Messages, t *ui.Theme) {
	ui.Row(c).FillWidth().Height(34).Padding(0, 8, 0, 12).Gap(6).AlignItems(ui.Center).Shrink(0).
		BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
		ui.Row(c).Grow(1).MinWidth(0).Height(24).Padding(0, 8).Gap(6).AlignItems(ui.Center).
			Radius(5).Border(1, t.Border).Background(t.Surface).Children(func() {
			ui.Icon(c, icons.Lucide(icons.Search)).Size(12, 12).TextColor(t.TextMuted).Shrink(0)
			input := ui.TextInputBase(c, &tab.Filter)
			input.Placeholder(m.SftpFilterPlaceholder())
			input.FontSize(12)
			input.Grow(1)
			input.MinWidth(0)
			input.AutoFocus()
			if input.Shortcut(0, ui.KeyEscape) {
				s.EscapeFilter(side)
			}
			if tab.Filter != "" {
				clear := ui.ButtonBase(c).Label(m.SftpFilterClear()).Tooltip(m.SftpFilterClear()).
					Size(16, 16).Justify(ui.Center).AlignItems(ui.Center).TextColor(t.TextMuted)
				clear.Children(func() { ui.Icon(c, icons.Lucide(icons.X)).Size(12, 12) })
				if clear.Clicked() {
					tab.Filter = ""
				}
			}
		})
		if toolButton(c, icons.X, m.SftpFilterClose(), false).Clicked() {
			s.ToggleFilter(side)
		}
	})
}
