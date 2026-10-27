// Package transfer renders the SFTP surface: the two independent panes, their
// Tabs, bookmarks, sorting, hidden-file control, file operations, and the
// host picker. It imports only the application, domain, and MyGo UI layers.
package transfer

import (
	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"

	domainsftp "github.com/mslxl/natcatty/domain/transfer"
	"github.com/mslxl/natcatty/ui/locales"
	uitheme "github.com/mslxl/natcatty/ui/theme"
	transferstate "github.com/mslxl/natcatty/ui/transfer/state"
)

// surfaceGutter is the SFTP surface's own inset from the shell's content
// column. The shell already pads the column against the window, but the
// surface is embedded in a 456-DIP column at a 720x480 window, so without its
// own inset its right-aligned controls (the header's Connect button, the
// operation bar) and the right pane's column sit flush against the surface's
// edge: an overlay (a dialog's edge) or the window frame then appears to cut
// them. The gutter matches the shell's own padding, so every SFTP control keeps
// the same distance from the window frame at both supported sizes.
const surfaceGutter float32 = 16

// View renders the main window's SFTP surface.
func View(s *transferstate.State) func(*ui.Context) {
	return func(c *ui.Context) {
		// Listings and operations complete off the UI thread; applying their
		// outcomes here keeps every Tab mutation on this thread.
		s.Flush()
		s.EnsureStarted()

		m := locales.In(c.Locale())
		t := c.Theme()

		root := ui.Column(c).Grow(1).FillHeight().MinWidth(0).Gap(8).
			PaddingX(surfaceGutter).Focusable().AutoFocus()
		root.Children(func() {
			header(c, s, m, t)
			toolbar(c, s, m, t)
			operationBar(c, s, m, t)
			panes(c, s, m, t)
			status(c, s, m, t)
		})
		if s.Dialog() == nil && !s.CenterOpen() {
			installShortcuts(root, s)
		}
		centerView(c, s, m, t)
		dialogView(c, s, m, t)
	}
}

func header(c *ui.Context, s *transferstate.State, m locales.Messages, t *ui.Theme) {
	// The header wraps: the surface is only 456 DIPs wide at the minimum
	// window, so a longer localized title cannot be allowed to push the two
	// right-aligned buttons off the surface. They move to a second line
	// instead.
	ui.Row(c).FillWidth().Wrap().Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, m.SftpTitle()).FontSize(20).Bold().SingleLine().MinWidth(0).TextColor(t.Text)
		ui.Column(c).Grow(1)
		if ui.Button(c, m.SftpTransferCenterButton()).Clicked() {
			s.OpenCenter()
		}
		connect := ui.Button(c, m.SftpConnect())
		connect.Disabled(!s.RemoteAvailable())
		if connect.Clicked() {
			s.RequestHostPicker(s.Focused())
		}
	})
}

// toolbar holds the file actions for the focused pane. It wraps, so the
// 720-DIP window shows two rows and the 1100-DIP window shows one.
func toolbar(c *ui.Context, s *transferstate.State, m locales.Messages, t *ui.Theme) {
	side := s.Focused()
	tab := s.Tab(side)
	other := s.Tab(side.Other())
	ready := tab != nil && tab.FS != nil
	otherReady := other != nil && other.FS != nil
	selected := ready && len(tab.SelectedPaths()) > 0
	one := ready && len(tab.SelectedPaths()) == 1

	ui.Row(c).FillWidth().Wrap().Gap(6).Children(func() {
		actionButton(c, m.SftpUp(), ready, func() { s.GoParent(side) })
		actionButton(c, m.SftpRefresh(), ready, func() { s.Refresh(side) })
		actionButton(c, m.SftpNewFolder(), ready, func() { s.RequestNewFolder(side) })
		actionButton(c, m.SftpNewFile(), ready, func() { s.RequestNewFile(side) })
		actionButton(c, m.SftpRename(), one, func() { s.RequestRename(side) })
		actionButton(c, m.SftpPermissions(), one, func() { s.RequestChmod(side) })
		actionButton(c, m.SftpDelete(), selected, func() { s.RequestDelete(side) })
		actionButton(c, m.SftpCopyTo(), selected && otherReady, func() { s.Transfer(side) })
		actionButton(c, m.SftpMoveTo(), selected && otherReady, func() { s.MoveToOther(side) })

		// The same action is an upload from a local pane and a download from a
		// remote one, so its label names the direction the user sees.
		label := m.SftpUpload()
		if tab != nil && tab.Kind == transferstate.KindRemote {
			label = m.SftpDownload()
		}
		actionButton(c, label, selected && otherReady, func() { s.Transfer(side) })
	})
}

// actionButton renders a small toolbar button that is disabled when the
// focused pane cannot run the action.
func actionButton(c *ui.Context, label string, enabled bool, run func()) {
	button := ui.Button(c, label)
	button.FontSize(12)
	button.Disabled(!enabled)
	if button.Clicked() {
		run()
	}
}

// panes renders the two independent panes side by side. The width is shared
// equally; each pane keeps its own Tabs and browsing state.
func panes(c *ui.Context, s *transferstate.State, m locales.Messages, t *ui.Theme) {
	ui.Row(c).Grow(1).FillWidth().MinHeight(0).MinWidth(0).Gap(8).Children(func() {
		pane(c, s, transferstate.Left, m, t)
		pane(c, s, transferstate.Right, m, t)
	})
}

func pane(c *ui.Context, s *transferstate.State, side transferstate.Side, m locales.Messages, t *ui.Theme) {
	tab := s.Tab(side)
	focused := s.Focused() == side
	border := t.Border
	if focused {
		border = t.Accent
	}
	el := ui.Column(c).Grow(1).FillHeight().MinWidth(0).MinHeight(0).Gap(6).Padding(8).
		Background(t.Surface).Radius(t.Radius).Border(1, border).Focusable()
	el.Children(func() {
		tabStrip(c, s, side, m, t)
		if tab == nil {
			emptyPane(c, s, side, m, t)
			return
		}
		pathRow(c, s, side, tab, m, t)
		if tab.Problem != nil {
			ui.Text(c, errorText(m, tab.Problem)).FontSize(11).TextColor(uitheme.DangerText(t)).
				SingleLine().MinWidth(0)
		}
		viewRow(c, s, side, tab, m, t)
		fileList(c, s, side, tab, m, t)
		footer(c, s, side, tab, m, t)
	})
	if el.Clicked() {
		s.Focus(side)
	}
	if tab == nil || tab.FS == nil {
		return
	}
	// OS files dropped anywhere in the pane go to the current directory; a
	// folder row narrows the target to that folder.
	if files := el.DroppedFiles(); len(files) > 0 {
		s.DropFiles(side, tab.Path, files)
	}
	if payload, ok := ui.Drop[*transferstate.DragPayload](el); ok {
		s.DropPane(side, tab.Path, payload)
	}
	if el.FileDragOver() {
		el.Border(1, t.Accent)
	}
}

func tabStrip(c *ui.Context, s *transferstate.State, side transferstate.Side, m locales.Messages, t *ui.Theme) {
	pane := s.Pane(side)
	ui.Row(c).FillWidth().Wrap().Gap(4).AlignItems(ui.Center).Children(func() {
		for index, item := range pane.Tabs {
			index, item := index, item
			label := paneTitle(m, item)
			var button ui.Element
			if index == pane.Active {
				button = ui.PrimaryButton(c, label)
			} else {
				button = ui.Button(c, label)
			}
			button.FontSize(11)
			if button.Clicked() {
				s.SelectTab(side, index)
			}
		}
		menu := ui.MenuButton(c, "+", func(menu *ui.Menu) {
			local := menu.Item(m.SftpOpenLocal())
			local.Disabled(!s.LocalAvailable())
			if local.Chosen() {
				s.OpenLocal(side)
			}
			connect := menu.Item(m.SftpConnect())
			connect.Disabled(!s.RemoteAvailable())
			if connect.Chosen() {
				s.RequestHostPicker(side)
			}
			menu.Separator()
			duplicate := menu.Item(m.SftpDuplicateTab())
			duplicate.Disabled(pane.Tab() == nil)
			if duplicate.Chosen() {
				s.Duplicate(side)
			}
			closeTab := menu.Item(m.SftpCloseTab())
			closeTab.Disabled(pane.Tab() == nil)
			if closeTab.Chosen() {
				s.CloseTab(side)
			}
		})
		menu.FontSize(11)
	})
}

func emptyPane(c *ui.Context, s *transferstate.State, side transferstate.Side, m locales.Messages, t *ui.Theme) {
	ui.Column(c).Grow(1).FillWidth().AlignItems(ui.Center).Justify(ui.Center).Gap(8).Children(func() {
		ui.Text(c, m.SftpNoTab()).FontSize(12).TextColor(t.TextMuted).SingleLine().MinWidth(0)
		ui.Row(c).Gap(6).Children(func() {
			local := ui.Button(c, m.SftpOpenLocal())
			local.Disabled(!s.LocalAvailable())
			if local.Clicked() {
				s.OpenLocal(side)
			}
			remote := ui.Button(c, m.SftpConnect())
			remote.Disabled(!s.RemoteAvailable())
			if remote.Clicked() {
				s.RequestHostPicker(side)
			}
		})
	})
}

func pathRow(c *ui.Context, s *transferstate.State, side transferstate.Side, tab *transferstate.Tab, m locales.Messages, t *ui.Theme) {
	ui.Row(c).FillWidth().Gap(4).AlignItems(ui.Center).Children(func() {
		up := ui.Button(c, "↑")
		up.FontSize(12)
		up.Width(28)
		if up.Clicked() {
			s.GoParent(side)
		}
		input := ui.TextInput(c, &tab.Draft)
		input.Placeholder(m.SftpPathPlaceholder())
		input.FontSize(12)
		input.Grow(1)
		input.MinWidth(0)
		if input.Submitted() {
			s.NavigateDraft(side)
		}
		refresh := ui.Button(c, "⟳")
		refresh.FontSize(12)
		refresh.Width(28)
		if refresh.Clicked() {
			s.Refresh(side)
		}
	})
}

func viewRow(c *ui.Context, s *transferstate.State, side transferstate.Side, tab *transferstate.Tab, m locales.Messages, t *ui.Theme) {
	ui.Row(c).FillWidth().Wrap().Gap(4).AlignItems(ui.Center).Children(func() {
		sort := ui.MenuButton(c, m.SftpSort(), func(menu *ui.Menu) {
			sortMenu(menu, s, side, tab, m)
		})
		sort.FontSize(11)
		ui.Text(c, sortSummary(m, tab.Sort)).FontSize(10).TextColor(t.TextMuted).SingleLine().MinWidth(0)

		hidden := ui.Button(c, m.SftpShowHidden())
		hidden.FontSize(11)
		if tab.ShowHidden {
			hidden.Background(t.Accent).TextColor(t.AccentText)
		}
		if hidden.Clicked() {
			s.ToggleHidden(side)
		}

		star := ui.Button(c, "★")
		star.FontSize(12)
		star.Width(28)
		if s.Bookmarked(side) {
			star.Background(t.Accent).TextColor(t.AccentText)
		}
		if star.Clicked() {
			s.ToggleBookmark(side)
		}

		saved := ui.MenuButton(c, m.SftpBookmarks(), func(menu *ui.Menu) {
			bookmarksMenu(menu, s, side, tab, m)
		})
		saved.FontSize(11)
	})
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
		field := field
		item := menu.Item(field.label).Checked(tab.Sort.Field == field.field)
		if item.Chosen() {
			s.ToggleSort(side, field.field)
		}
	}
	menu.Separator()
	descending := menu.Item(m.SftpSortDescending()).Checked(tab.Sort.Descending)
	if descending.Chosen() {
		s.SetSort(side, domainsftp.Sort{Field: tab.Sort.Field, Descending: !tab.Sort.Descending, MixKinds: tab.Sort.MixKinds})
	}
	folders := menu.Item(m.SftpSortFoldersFirst()).Checked(!tab.Sort.MixKinds)
	if folders.Chosen() {
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
	if len(items) == 0 {
		menu.Separator()
		menu.Item(m.SftpBookmarksEmpty()).Disabled(true)
	} else {
		menu.Separator()
		for _, bookmark := range items {
			bookmark := bookmark
			item := menu.Item(bookmark.Label)
			if item.Chosen() {
				s.GoBookmark(side, bookmark)
			}
		}
		menu.Separator()
		menu.Submenu(m.SftpBookmarksRemoveMenu(), func(remove *ui.Menu) {
			for _, bookmark := range items {
				bookmark := bookmark
				item := remove.Item(bookmark.Label)
				if item.Chosen() {
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

func fileList(c *ui.Context, s *transferstate.State, side transferstate.Side, tab *transferstate.Tab, m locales.Messages, t *ui.Theme) {
	ui.Scroll(c).Grow(1).FillWidth().MinHeight(0).TrackScroll(&tab.Scroll).Children(func() {
		ui.Column(c).FillWidth().MinWidth(0).Children(func() {
			rows := tab.Visible()
			if len(rows) == 0 {
				ui.Text(c, m.SftpEmpty()).FontSize(12).TextColor(t.TextMuted).Padding(8)
				return
			}
			for _, entry := range rows {
				fileRow(c, s, side, tab, entry, m, t)
			}
		})
	})
}

func fileRow(c *ui.Context, s *transferstate.State, side transferstate.Side, tab *transferstate.Tab, entry domainsftp.Entry, m locales.Messages, t *ui.Theme) {
	selected := tab.Selection[entry.Path]
	row := ui.Row(c).FillWidth().MinWidth(0).Gap(6).Padding(3, 4).AlignItems(ui.Center).
		Radius(4).Cursor(ui.CursorPointer)
	if selected {
		row.Background(t.Selection)
	} else if row.Hovered() {
		row.Background(t.SurfaceHover)
	}
	row.Children(func() {
		name := ui.Text(c, displayName(entry)).FontSize(12).SingleLine().MinWidth(0).Grow(1).TextColor(t.Text)
		if entry.IsDir() {
			name.Bold()
		}
		if entry.Symlink {
			name.TextColor(t.TextMuted)
		}
		if detail := secondaryText(entry); detail != "" {
			ui.Text(c, detail).FontSize(10).TextColor(t.TextMuted).SingleLine().MinWidth(0)
		}
	})

	clicks := row.Clicks()
	double := row.DoubleClicked()
	if clicks > 0 || double {
		s.Click(side, entry, row.ClickModifiers(), double)
	}
	if entry.Name != ".." {
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
	}
	if entry.IsDir() {
		if files := row.DroppedFiles(); len(files) > 0 {
			s.DropFiles(side, entry.Path, files)
		}
		if payload, ok := ui.Drop[*transferstate.DragPayload](row); ok {
			s.DropPane(side, entry.Path, payload)
		}
	}
}

func footer(c *ui.Context, s *transferstate.State, side transferstate.Side, tab *transferstate.Tab, m locales.Messages, t *ui.Theme) {
	text := ""
	if selected := tab.SelectedPaths(); len(selected) > 0 {
		text = m.SftpSelected(len(selected))
	} else if tab.Loading {
		text = m.SftpLoading()
	} else {
		text = m.SftpItemCount(visibleCount(tab))
	}
	ui.Text(c, text).FontSize(10).TextColor(t.TextMuted).SingleLine().MinWidth(0)
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

func operationBar(c *ui.Context, s *transferstate.State, m locales.Messages, t *ui.Theme) {
	op := s.Snapshot()
	if op == nil {
		return
	}
	// Wraps like the toolbar: the progress bar and the Cancel button are
	// fixed-width, so at the minimum surface width a long operation label
	// must move them to a second line rather than pushing Cancel off the
	// surface's right edge.
	ui.Row(c).FillWidth().Wrap().Gap(8).Padding(6, 8).AlignItems(ui.Center).
		Background(t.SurfaceHover).Radius(t.Radius).Children(func() {
		text := operationLabel(m, op)
		if op.Err != nil {
			text = errorText(m, op.Err)
		}
		color := t.Text
		if op.Err != nil {
			color = uitheme.DangerText(t)
		}
		ui.Text(c, text).FontSize(12).Grow(1).MinWidth(0).SingleLine().TextColor(color)
		if op.Status != "" {
			// A queue-backed operation also shows the bar the Transfer center
			// uses, so progress is visible without opening the center.
			ui.Progress(c, float64(op.Percent())/100).Width(140)
		}
		if op.Running {
			if ui.Button(c, m.SftpCancel()).Clicked() {
				s.CancelOperation()
			}
			return
		}
		if ui.Button(c, m.SftpDismiss()).Clicked() {
			s.DismissOperation()
		}
	})
}

func status(c *ui.Context, s *transferstate.State, m locales.Messages, t *ui.Theme) {
	if problem := s.Problem(); problem != nil {
		ui.Text(c, errorText(m, problem)).FontSize(11).TextColor(uitheme.DangerText(t)).
			SingleLine().MinWidth(0)
		return
	}
	if s.Snapshot() != nil {
		return
	}
	if notice := noticeText(m, s.Notice()); notice != "" {
		ui.Text(c, notice).FontSize(11).TextColor(t.TextMuted).SingleLine().MinWidth(0)
	}
}
