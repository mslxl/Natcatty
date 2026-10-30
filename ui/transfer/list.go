package transfer

import (
	"path/filepath"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"

	domainsftp "github.com/mslxl/natcatty/domain/transfer"
	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
	transferstate "github.com/mslxl/natcatty/ui/transfer/state"
)

// The list's fixed column widths. Name takes the rest.
const (
	modifiedWidth float32 = 128
	sizeWidth     float32 = 72
	kindWidth     float32 = 64
	ownerWidth    float32 = 76
)

// columns are the list's optional columns. A narrow pane drops the least
// useful first, so the name never gets squeezed.
type columns struct {
	modified, size, kind, owner bool
}

func columnsFor(width float32) columns {
	return columns{
		modified: width >= 420,
		size:     true,
		kind:     width >= 560,
		owner:    width >= 560,
	}
}

// listHeader is the column header: a click sorts by that column, a second
// click reverses it.
func listHeader(c *ui.Context, s *transferstate.State, side transferstate.Side, tab *transferstate.Tab,
	cols columns, m locales.Messages, t *ui.Theme) {
	row := ui.Row(c).FillWidth().Height(28).Padding(0, 12).Gap(8).AlignItems(ui.Center).Shrink(0).
		Background(kit.Muted(t)).BorderWidth(0, 0, 1, 0).BorderColor(t.Border)
	row.Children(func() {
		headerCell(c, s, side, tab, domainsftp.SortName, m.SftpColumnName(), 0, false, t)
		if cols.modified {
			headerCell(c, s, side, tab, domainsftp.SortModified, m.SftpColumnModified(), modifiedWidth, false, t)
		}
		if cols.size {
			headerCell(c, s, side, tab, domainsftp.SortSize, m.SftpColumnSize(), sizeWidth, true, t)
		}
		if cols.kind {
			headerCell(c, s, side, tab, domainsftp.SortType, m.SftpColumnKind(), kindWidth, true, t)
		}
		if cols.owner {
			headerCell(c, s, side, tab, domainsftp.SortOwner, m.SftpColumnOwner(), ownerWidth, true, t)
		}
	})
	row.ContextMenu(func(menu *ui.Menu) { sortMenu(menu, s, side, tab, m) })
}

// headerCell is one column title; width 0 is the growing Name column.
func headerCell(c *ui.Context, s *transferstate.State, side transferstate.Side, tab *transferstate.Tab,
	field domainsftp.SortField, label string, width float32, right bool, t *ui.Theme) {
	cell := ui.ButtonBase(c).Label(label).FillHeight().Gap(4).AlignItems(ui.Center).MinWidth(0)
	if width == 0 {
		cell.Grow(1).Basis(0)
	} else {
		cell.Width(width).Shrink(0)
	}
	if right {
		cell.Justify(ui.End)
	} else {
		cell.Justify(ui.Start)
	}
	sorted := tab.Sort.Field == field
	color := t.TextMuted
	if sorted || cell.Hovered() {
		color = t.Text
	}
	cell.Children(func() {
		ui.Text(c, strings.ToUpper(label)).FontSize(11).FontWeight(600).SingleLine().MinWidth(0).TextColor(color)
		if sorted {
			arrow := icons.ArrowUp
			if tab.Sort.Descending {
				arrow = icons.ArrowDown
			}
			ui.Icon(c, icons.Lucide(arrow)).Size(11, 11).TextColor(color).Shrink(0)
		}
	})
	if cell.Clicked() {
		s.ToggleSort(side, field)
	}
}

func sortMenu(menu *ui.Menu, s *transferstate.State, side transferstate.Side, tab *transferstate.Tab, m locales.Messages) {
	fields := []struct {
		field domainsftp.SortField
		label string
	}{
		{domainsftp.SortName, m.SftpSortName()},
		{domainsftp.SortSize, m.SftpSortSize()},
		{domainsftp.SortModified, m.SftpSortModified()},
		{domainsftp.SortType, m.SftpSortType()},
		{domainsftp.SortOwner, m.SftpSortOwner()},
	}
	for _, field := range fields {
		if menu.Item(field.label).Checked(tab.Sort.Field == field.field).Chosen() {
			s.ToggleSort(side, field.field)
		}
	}
	menu.Separator()
	if menu.Item(m.SftpSortDescending()).Checked(tab.Sort.Descending).Chosen() {
		s.SetSort(side, domainsftp.Sort{Field: tab.Sort.Field, Descending: !tab.Sort.Descending, MixKinds: tab.Sort.MixKinds})
	}
	if menu.Item(m.SftpSortFoldersFirst()).Checked(!tab.Sort.MixKinds).Chosen() {
		s.ToggleFoldersFirst(side)
	}
}

func bookmarksMenu(menu *ui.Menu, s *transferstate.State, side transferstate.Side, tab *transferstate.Tab, m locales.Messages) {
	items := s.Bookmarks(side)
	label := m.SftpBookmark()
	if s.Bookmarked(side) {
		label = m.SftpBookmarkRemove()
	}
	if menu.Item(label).Chosen() {
		s.ToggleBookmark(side)
	}
	menu.Separator()
	if len(items) == 0 {
		menu.Item(m.SftpBookmarksEmpty()).Disabled(true)
	} else {
		for _, bookmark := range items {
			if menu.Item(bookmark.Label).Chosen() {
				s.GoBookmark(side, bookmark)
			}
		}
		menu.Separator()
		menu.Submenu(m.SftpBookmarksRemoveMenu(), func(remove *ui.Menu) {
			for _, bookmark := range items {
				if remove.Item(bookmark.Label).Chosen() {
					s.RemoveBookmark(side, bookmark.Key())
				}
			}
		})
	}
	if tab != nil && tab.Kind == transferstate.KindLocal {
		menu.Separator()
		menu.Item(m.SftpBookmarksLocalHint()).Disabled(true)
	}
}

// fileList is the Tab's listing. A right-click on the empty area offers the
// folder's commands; one on a row offers the row's.
func fileList(c *ui.Context, s *transferstate.State, side transferstate.Side, tab *transferstate.Tab,
	cols columns, m locales.Messages, t *ui.Theme) {
	list := ui.Scroll(c).Grow(1).FillWidth().MinHeight(0).TrackScroll(&tab.Scroll)
	list.Children(func() {
		ui.Column(c).FillWidth().MinWidth(0).Padding(2, 0).Children(func() {
			rows := tab.Visible()
			if len(rows) == 0 || (len(rows) == 1 && rows[0].Name == "..") {
				for _, entry := range rows {
					fileRow(c, s, side, tab, entry, cols, m, t)
				}
				listPlaceholder(c, tab, m, t)
				return
			}
			for _, entry := range rows {
				fileRow(c, s, side, tab, entry, cols, m, t)
			}
		})
	})
	if tab.FS != nil {
		list.ContextMenu(func(menu *ui.Menu) { folderMenu(menu, s, side, m) })
	}
}

// listPlaceholder fills a list with no entries: loading, or empty.
func listPlaceholder(c *ui.Context, tab *transferstate.Tab, m locales.Messages, t *ui.Theme) {
	if tab.Problem != nil {
		return
	}
	ui.Column(c).FillWidth().Padding(48, 16).Gap(8).AlignItems(ui.Center).Children(func() {
		icon, text := icons.Folder, m.SftpEmptyDirectory()
		if tab.Loading || tab.FS == nil {
			icon, text = icons.Loader, m.SftpLoading()
		}
		ui.Icon(c, icons.Lucide(icon)).Size(32, 32).TextColor(t.TextMuted.Alpha(0.6))
		ui.Text(c, text).FontSize(12).TextColor(t.TextMuted)
	})
}

func folderMenu(menu *ui.Menu, s *transferstate.State, side transferstate.Side, m locales.Messages) {
	if menu.Item(m.SftpRefresh()).Chosen() {
		s.Refresh(side)
	}
	menu.Separator()
	if menu.Item(m.SftpNewFolder()).Chosen() {
		s.RequestNewFolder(side)
	}
	if menu.Item(m.SftpNewFile()).Chosen() {
		s.RequestNewFile(side)
	}
	if s.HasClipboard() {
		menu.Separator()
		if menu.Item(m.SessionMenuPaste()).Chosen() {
			s.Paste(side)
		}
	}
}

// fileRow is one entry: an icon tile, the name, and the visible columns.
func fileRow(c *ui.Context, s *transferstate.State, side transferstate.Side, tab *transferstate.Tab,
	entry domainsftp.Entry, cols columns, m locales.Messages, t *ui.Theme) {
	selected := tab.Selection[entry.Path]
	height, tile, glyph := float32(40), float32(28), float32(15)
	if tab.Compact {
		height, tile, glyph = 28, 20, 12
	}
	row := ui.Row(c).FillWidth().MinWidth(0).Height(height).Padding(0, 12).Gap(8).AlignItems(ui.Center).
		Cursor(ui.CursorPointer).Label(entry.Name)
	switch {
	case selected && s.Focused() == side:
		row.Background(t.Accent.Alpha(0.16))
	case selected:
		row.Background(kit.Strong(t))
	case row.Hovered():
		row.Background(kit.Subtle(t))
	}
	row.Children(func() {
		ui.Row(c).Grow(1).Basis(0).MinWidth(0).Gap(10).AlignItems(ui.Center).Children(func() {
			icon, tint, fill := fileIcon(entry), t.TextMuted, kit.Muted(t)
			if entry.IsDir() {
				tint, fill = t.Accent, t.Accent.Alpha(0.1)
			}
			ui.Column(c).Size(tile, tile).Radius(6).Background(fill).Shrink(0).
				Justify(ui.Center).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, icons.Lucide(icon)).Size(glyph, glyph).TextColor(tint)
			})
			name := entry.Name
			if entry.Symlink && entry.LinkTarget != "" {
				name += " → " + entry.LinkTarget
			}
			text := ui.Text(c, name).FontSize(13).SingleLine().MinWidth(0).TextColor(t.Text)
			if entry.IsDir() {
				text.FontWeight(500)
			}
		})
		if entry.Name == ".." {
			return
		}
		if cols.modified {
			cell(c, modifiedText(entry.ModTime), modifiedWidth, false, t)
		}
		if cols.size {
			size := "--"
			if !entry.IsDir() {
				size = domainsftp.FormatSize(entry.Size)
			}
			cell(c, size, sizeWidth, true, t)
		}
		if cols.kind {
			cell(c, kindText(m, entry), kindWidth, true, t)
		}
		if cols.owner {
			cell(c, entry.Owner, ownerWidth, true, t)
		}
	})

	clicks := row.Clicks()
	double := row.DoubleClicked()
	if clicks > 0 || double {
		s.Click(side, entry, row.ClickModifiers(), double)
	}
	if entry.Name == ".." {
		return
	}
	if row.RightClicked() {
		s.SelectForMenu(side, entry)
	}
	row.ContextMenu(func(menu *ui.Menu) { rowMenu(menu, s, side, tab, entry, m) })
	if payload := s.DragFor(side, entry); payload != nil {
		row.Drag(payload)
		if tab.Kind == transferstate.KindLocal {
			paths := payload.Paths
			row.DragDataFrom(func() transfer.Data {
				data, err := transfer.FileData(paths...)
				if err != nil {
					return transfer.Data{}
				}
				return data
			})
		}
	}
	if entry.IsDir() {
		if files := row.DroppedFiles(); len(files) > 0 {
			s.DropFiles(side, entry.Path, files)
		}
		if payload, ok := ui.Drop[*transferstate.DragPayload](row); ok {
			s.DropPane(side, entry.Path, payload)
		}
		if row.FileDragOver() {
			row.Background(t.Accent.Alpha(0.16))
		}
	}
}

func cell(c *ui.Context, text string, width float32, right bool, t *ui.Theme) {
	box := ui.Row(c).Width(width).Shrink(0).AlignItems(ui.Center)
	if right {
		box.Justify(ui.End)
	}
	box.Children(func() {
		ui.Text(c, text).FontSize(12).SingleLine().MinWidth(0).TextColor(t.TextMuted)
	})
}

func rowMenu(menu *ui.Menu, s *transferstate.State, side transferstate.Side, tab *transferstate.Tab,
	entry domainsftp.Entry, m locales.Messages) {
	one := len(tab.SelectedPaths()) <= 1
	other := s.Tab(side.Other())
	if menu.Item(m.SftpOpen()).Disabled(!one).Chosen() {
		s.Open(side, entry)
	}
	if menu.Item(m.SftpCopyToOtherPane()).Disabled(other == nil || other.FS == nil).Chosen() {
		s.Transfer(side)
	}
	if menu.Item(m.SftpMoveToOtherPane()).Disabled(other == nil || other.FS == nil).Chosen() {
		s.MoveToOther(side)
	}
	if menu.Item(m.SftpCopyFilePath()).Chosen() {
		mygo.Clipboard.WriteText(entry.Path)
	}
	menu.Separator()
	if menu.Item(m.SftpRename()).Disabled(!one).Chosen() {
		s.RequestRename(side)
	}
	if menu.Item(m.SftpPermissions()).Disabled(!one || tab.Kind == transferstate.KindLocal).Chosen() {
		s.RequestChmod(side)
	}
	if menu.Item(m.SftpDelete()).Chosen() {
		s.RequestDelete(side)
	}
	menu.Separator()
	folderMenu(menu, s, side, m)
}

// fileIcon picks a row's glyph from its kind and extension.
func fileIcon(entry domainsftp.Entry) string {
	switch {
	case entry.Name == "..":
		return icons.FolderOpen
	case entry.IsDir():
		return icons.Folder
	case entry.Kind == domainsftp.KindLink || entry.Symlink:
		return icons.Link
	}
	switch strings.ToLower(filepath.Ext(entry.Name)) {
	case ".zip", ".tar", ".gz", ".tgz", ".xz", ".bz2", ".7z", ".rar", ".zst":
		return icons.Archive
	case ".go", ".js", ".ts", ".tsx", ".jsx", ".py", ".rs", ".c", ".h", ".cpp", ".java", ".sh", ".rb",
		".php", ".json", ".yaml", ".yml", ".toml", ".xml", ".html", ".css", ".lua", ".sql":
		return icons.FileCode
	case ".txt", ".md", ".log", ".conf", ".cfg", ".ini", ".csv":
		return icons.FileText
	}
	return icons.File
}

// kindText is the Kind column: folder, link, or the file's extension.
func kindText(m locales.Messages, entry domainsftp.Entry) string {
	switch {
	case entry.IsDir():
		return m.SftpKindFolder()
	case entry.Kind == domainsftp.KindLink:
		return m.SftpKindLink()
	}
	if ext := strings.TrimPrefix(filepath.Ext(entry.Name), "."); ext != "" && len(ext) <= 8 {
		return strings.ToLower(ext)
	}
	return m.SftpKindFile()
}

// footer counts the listing and the selection, and names the folder.
func footer(c *ui.Context, s *transferstate.State, side transferstate.Side, tab *transferstate.Tab,
	m locales.Messages, t *ui.Theme) {
	ui.Row(c).FillWidth().Height(30).Padding(0, 12).Gap(8).AlignItems(ui.Center).Shrink(0).
		Background(kit.Muted(t)).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
		text := m.SftpItemCount(visibleCount(tab))
		if selected := len(tab.SelectedPaths()); selected > 0 {
			text += " · " + m.SftpSelected(selected)
		}
		ui.Text(c, text).FontSize(11).TextColor(t.TextMuted).SingleLine().MinWidth(0).Grow(1)
		ui.Text(c, tab.Path).FontSize(11).TextColor(t.TextMuted).SingleLine().MinWidth(0).MaxWidth(200).
			Tooltip(tab.Path)
	})
}

func visibleCount(tab *transferstate.Tab) int {
	count := 0
	for _, entry := range tab.Visible() {
		if entry.Name != ".." {
			count++
		}
	}
	return count
}

// queueStrip lists the Transfers under the panes, as the original's, and is
// gone when there are none.
func queueStrip(c *ui.Context, s *transferstate.State, m locales.Messages, t *ui.Theme) {
	tasks := s.StripTasks()
	if len(tasks) == 0 {
		return
	}
	active := 0
	for _, task := range tasks {
		if !task.Status.Terminal() {
			active++
		}
	}
	ui.Column(c).FillWidth().Shrink(0).Background(kit.Muted(t)).
		BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
		ui.Row(c).FillWidth().Height(30).Padding(0, 6, 0, 12).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, icons.Lucide(icons.ArrowDownUp)).Size(13, 13).TextColor(t.TextMuted)
			ui.Text(c, m.SftpTransfers()).FontSize(12).FontWeight(600).TextColor(t.Text)
			if active > 0 {
				ui.Text(c, m.SftpTransferActive(active)).FontSize(11).TextColor(t.TextMuted).SingleLine().MinWidth(0)
			}
			ui.Column(c).Grow(1)
			if s.HasFinished() {
				clear := kit.GhostButton(c, "", m.SftpClearCompleted())
				clear.Height(22).FontSize(11)
				if clear.Clicked() {
					s.ClearFinished()
				}
			}
			if toolButton(c, icons.Maximize, m.SftpTransferCenterOpen(), false).Clicked() {
				s.OpenCenter()
			}
		})
		ui.Scroll(c).FillWidth().MaxHeight(132).Children(func() {
			ui.Column(c).FillWidth().Padding(0, 0, 4, 0).Children(func() {
				for _, task := range tasks {
					stripRow(c, s, m, t, task)
				}
			})
		})
	})
}

func stripRow(c *ui.Context, s *transferstate.State, m locales.Messages, t *ui.Theme, task domainsftp.Task) {
	ui.Column(c).FillWidth().Padding(4, 12).Gap(4).Children(func() {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, icons.Lucide(transferStatusIcon(task.Status))).Size(13, 13).
				TextColor(transferStatusColor(t, task.Status)).Shrink(0)
			ui.Text(c, transferName(m, task)).FontSize(12).FontWeight(500).SingleLine().MinWidth(0).Shrink(1).
				TextColor(t.Text)
			if task.DestDir != "" {
				ui.Text(c, "→ "+task.DestDir).FontSize(11).SingleLine().MinWidth(0).Shrink(2).TextColor(t.TextMuted)
			}
			ui.Column(c).Grow(1)
			ui.Text(c, transferDetail(m, task)).FontSize(11).NoWrap().TextColor(t.TextMuted)
			transferButtons(c, s, m, task)
		})
		if !task.Status.Terminal() {
			ui.Progress(c, float64(task.Percent())/100).FillWidth().Height(3)
		}
	})
}

func transferStatusIcon(status domainsftp.TaskStatus) string {
	switch status {
	case domainsftp.TaskCompleted:
		return icons.CircleCheck
	case domainsftp.TaskFailed, domainsftp.TaskInterrupted:
		return icons.CircleX
	case domainsftp.TaskCancelled:
		return icons.X
	case domainsftp.TaskPaused, domainsftp.TaskPausing:
		return icons.Clock
	case domainsftp.TaskAttention:
		return icons.TriangleAlert
	case domainsftp.TaskTransferring:
		return icons.Loader
	}
	return icons.Clock
}
