// Package transfer renders the SFTP surface as the original's: two
// independent panes, each with its own Tabs, a breadcrumb and icon toolbar, a
// sortable file list, and a footer, over the queue of running Transfers. It
// imports only the application, domain, and MyGo UI layers.
package transfer

import (
	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
	uitheme "github.com/mslxl/natcatty/ui/theme"
	transferstate "github.com/mslxl/natcatty/ui/transfer/state"
)

// sideBySideWidth is the narrowest window that shows the panes side by side;
// a narrower one stacks them. The original stacks below 1024, but this app's
// smallest window is 720 wide, and stacking there leaves each list a few rows.
const sideBySideWidth float32 = 640

// focusMark is the small triangle in the focused pane's top-left corner.
var focusMark = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 12 12"><path d="M0 0H12L0 12Z" fill="currentColor"/></svg>`))

// View renders the main window's SFTP surface.
func View(s *transferstate.State) func(*ui.Context) {
	return func(c *ui.Context) {
		// Listings and operations complete off the UI thread; applying their
		// outcomes here keeps every Tab mutation on this thread.
		s.Flush()
		s.EnsureStarted()

		m := locales.In(c.Locale())
		t := c.Theme()
		width, _ := c.Size()
		sideBySide := width >= sideBySideWidth
		paneWidth := width
		if sideBySide {
			paneWidth = width / 2
		}

		root := ui.Column(c).Grow(1).FillHeight().MinWidth(0).Background(t.Background).
			BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Focusable().AutoFocus()
		root.Children(func() {
			split := ui.Row(c)
			if !sideBySide {
				split = ui.Column(c)
			}
			split.Grow(1).FillWidth().MinHeight(0).MinWidth(0).Children(func() {
				pane(c, s, transferstate.Left, paneWidth, sideBySide, m, t)
				pane(c, s, transferstate.Right, paneWidth, sideBySide, m, t)
			})
			operationBar(c, s, m, t)
			status(c, s, m, t)
			// The open center lists the same Transfers; the strip would only
			// repeat them behind it.
			if !s.CenterOpen() {
				queueStrip(c, s, m, t)
			}
		})
		if s.Dialog() == nil && !s.CenterOpen() {
			installShortcuts(root, s)
		}
		centerView(c, s, m, t)
		dialogView(c, s, m, t)
	}
}

// pane is one side: its Tab bar, then the Tab's browser or the empty state.
// The left (or top) pane draws the divider on its trailing edge.
func pane(c *ui.Context, s *transferstate.State, side transferstate.Side, width float32,
	sideBySide bool, m locales.Messages, t *ui.Theme) {
	tab := s.Tab(side)
	el := ui.Column(c).Grow(1).Basis(0).MinWidth(0).MinHeight(0).Clip().Focusable()
	// The pane fills the split's cross axis; otherwise it takes its list's
	// height and the split centres it, pushing the toolbar out of view.
	if sideBySide {
		el.FillHeight()
	} else {
		el.FillWidth()
	}
	if side == transferstate.Left {
		if sideBySide {
			el.BorderWidth(0, 1, 0, 0)
		} else {
			el.BorderWidth(0, 0, 1, 0)
		}
		el.BorderColor(t.Border)
	}
	el.Children(func() {
		if len(s.Pane(side).Tabs) > 0 {
			tabBar(c, s, side, m, t)
		}
		if tab == nil {
			emptyPane(c, s, side, m, t)
		} else {
			browser(c, s, side, tab, width, m, t)
		}
		if s.Focused() == side {
			ui.Icon(c, focusMark).Size(12, 12).TextColor(t.Accent).
				Absolute().Left(0).Top(0)
		}
	})
	if el.Clicked() || el.RightClicked() {
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
		el.Border(2, t.Accent)
	}
}

// browser is a connected Tab: the toolbar, the filter bar when open, the
// list with its column header, and the footer.
func browser(c *ui.Context, s *transferstate.State, side transferstate.Side, tab *transferstate.Tab,
	width float32, m locales.Messages, t *ui.Theme) {
	toolbar(c, s, side, tab, m, t)
	if tab.FilterOpen {
		filterBar(c, s, side, tab, m, t)
	}
	if tab.Problem != nil {
		problemBanner(c, s, side, tab, m, t)
	}
	columns := columnsFor(width)
	listHeader(c, s, side, tab, columns, m, t)
	fileList(c, s, side, tab, columns, m, t)
	footer(c, s, side, tab, m, t)
}

// tabBar lists the pane's Tabs: a local or remote glyph, the title, and a
// close button, with the active Tab underlined. "+" picks a Host for a new
// Tab.
func tabBar(c *ui.Context, s *transferstate.State, side transferstate.Side, m locales.Messages, t *ui.Theme) {
	pane := s.Pane(side)
	ui.Row(c).FillWidth().Height(32).Shrink(0).Background(kit.Muted(t)).
		BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
		ui.ScrollHorizontal(c).Grow(1).MinWidth(0).Height(32).Children(func() {
			ui.Row(c).Height(32).Children(func() {
				for index, item := range pane.Tabs {
					paneTab(c, s, side, index, item, index == pane.Active, m, t)
				}
			})
		})
		add := ui.ButtonBase(c).Label(m.SftpAddTab()).Tooltip(m.SftpAddTab()).
			Width(32).FillHeight().Justify(ui.Center).AlignItems(ui.Center).Shrink(0).
			BorderWidth(0, 0, 0, 1).BorderColor(t.Border).TextColor(t.TextMuted)
		if add.Hovered() {
			add.Background(kit.Subtle(t)).TextColor(t.Text)
		}
		add.Children(func() { ui.Icon(c, icons.Lucide(icons.Plus)).Size(14, 14) })
		if add.Clicked() {
			s.RequestHostPicker(side)
		}
	})
}

func paneTab(c *ui.Context, s *transferstate.State, side transferstate.Side, index int,
	tab *transferstate.Tab, active bool, m locales.Messages, t *ui.Theme) {
	title := paneTitle(m, tab)
	el := ui.Row(c).Height(32).MinWidth(100).MaxWidth(180).Padding(0, 6, 0, 12).Gap(6).
		AlignItems(ui.Center).BorderWidth(0, 1, 0, 0).BorderColor(t.Border).
		Cursor(ui.CursorPointer).Label(title)
	text, glyph := t.TextMuted, t.TextMuted
	switch {
	case active:
		el.Background(t.Background)
		text, glyph = t.Text, t.Accent
	case el.Hovered():
		el.Background(kit.Subtle(t))
		text = t.Text
	}
	el.Children(func() {
		icon := icons.HardDrive
		if tab.Kind == transferstate.KindLocal {
			icon = icons.Monitor
		}
		ui.Icon(c, icons.Lucide(icon)).Size(12, 12).TextColor(glyph).Shrink(0)
		ui.Text(c, title).FontSize(12).FontWeight(500).SingleLine().MinWidth(0).Grow(1).TextColor(text)
		closer := ui.ButtonBase(c).Label(m.SftpCloseTab()).Tooltip(m.SftpCloseTab()).
			Size(18, 18).Radius(4).Justify(ui.Center).AlignItems(ui.Center).Shrink(0).
			TextColor(t.TextMuted)
		if closer.Hovered() {
			closer.Background(kit.Strong(t)).TextColor(uitheme.DangerText(t))
		}
		closer.Children(func() { ui.Icon(c, icons.Lucide(icons.X)).Size(12, 12) })
		if closer.Clicked() {
			s.CloseTabAt(side, index)
		}
		if active {
			ui.Box(c).Absolute().Left(0).Right(0).Bottom(0).Height(2).Background(t.Accent)
		}
	})
	if el.Clicked() {
		s.SelectTab(side, index)
	}
	el.ContextMenu(func(menu *ui.Menu) {
		if menu.Item(m.SftpDuplicateTab()).Chosen() {
			s.SelectTab(side, index)
			s.Duplicate(side)
		}
		if menu.Item(m.SftpCloseTab()).Chosen() {
			s.CloseTabAt(side, index)
		}
	})
}

// emptyPane invites the user to pick what the pane browses.
func emptyPane(c *ui.Context, s *transferstate.State, side transferstate.Side, m locales.Messages, t *ui.Theme) {
	ui.Column(c).Grow(1).FillWidth().MinHeight(0).AlignItems(ui.Center).Justify(ui.Center).
		Gap(12).Padding(24).Children(func() {
		icon := icons.HardDrive
		if side == transferstate.Left {
			icon = icons.Monitor
		}
		ui.Column(c).Size(56, 56).Radius(12).Background(kit.Muted(t)).
			Justify(ui.Center).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, icons.Lucide(icon)).Size(24, 24).TextColor(t.Accent)
		})
		ui.Column(c).Gap(4).AlignItems(ui.Center).Children(func() {
			ui.Text(c, m.SftpSelectHostTitle()).FontSize(14).FontWeight(600).TextColor(t.Text)
			ui.Text(c, m.SftpSelectHostSubtitle()).FontSize(12).TextColor(t.TextMuted)
		})
		pick := kit.PrimaryButton(c, icons.Plus, m.SftpSelectHost())
		if pick.Clicked() {
			s.RequestHostPicker(side)
		}
	})
}

// problemBanner reports why the listing failed, with Retry.
func problemBanner(c *ui.Context, s *transferstate.State, side transferstate.Side, tab *transferstate.Tab,
	m locales.Messages, t *ui.Theme) {
	ui.Row(c).FillWidth().Gap(8).Padding(6, 12).AlignItems(ui.Center).Shrink(0).
		Background(t.Background.Mix(t.Danger, 0.1)).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).
		Children(func() {
			ui.Icon(c, icons.Lucide(icons.TriangleAlert)).Size(14, 14).TextColor(uitheme.DangerText(t)).Shrink(0)
			ui.Text(c, errorText(m, tab.Problem)).FontSize(12).TextColor(uitheme.DangerText(t)).
				Grow(1).MinWidth(0).SingleLine()
			retry := kit.GhostButton(c, icons.RefreshCw, m.SftpRetry())
			retry.Height(24).FontSize(12)
			if retry.Clicked() {
				s.Refresh(side)
			}
		})
}

func operationBar(c *ui.Context, s *transferstate.State, m locales.Messages, t *ui.Theme) {
	op := s.Snapshot()
	if op == nil {
		return
	}
	ui.Row(c).FillWidth().Gap(8).Padding(6, 12).AlignItems(ui.Center).Shrink(0).
		Background(kit.Muted(t)).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
		text := operationLabel(m, op)
		color := t.Text
		if op.Err != nil {
			text, color = errorText(m, op.Err), uitheme.DangerText(t)
		}
		if op.Running {
			ui.Icon(c, icons.Lucide(icons.Loader)).Size(14, 14).TextColor(t.Accent).Shrink(0)
		}
		ui.Text(c, text).FontSize(12).Grow(1).MinWidth(0).SingleLine().TextColor(color)
		if op.Status != "" {
			// A queue-backed operation also shows the bar the Transfer center
			// uses, so progress is visible without opening the center.
			ui.Progress(c, float64(op.Percent())/100).Width(140)
		}
		label := m.SftpDismiss()
		if op.Running {
			label = m.SftpCancel()
		}
		button := kit.GhostButton(c, "", label)
		button.Height(24).FontSize(12)
		if button.Clicked() {
			if op.Running {
				s.CancelOperation()
			} else {
				s.DismissOperation()
			}
		}
	})
}

func status(c *ui.Context, s *transferstate.State, m locales.Messages, t *ui.Theme) {
	text, color := "", t.TextMuted
	if problem := s.Problem(); problem != nil {
		text, color = errorText(m, problem), uitheme.DangerText(t)
	} else if s.Snapshot() == nil {
		text = noticeText(m, s.Notice())
	}
	if text == "" {
		return
	}
	ui.Row(c).FillWidth().Padding(4, 12).Shrink(0).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).
		Children(func() {
			ui.Text(c, text).FontSize(11).TextColor(color).SingleLine().MinWidth(0)
		})
}
