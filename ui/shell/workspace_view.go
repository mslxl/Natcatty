package shell

import (
	"errors"

	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"

	"github.com/mslxl/natcatty/domain/model"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
	uiconnection "github.com/mslxl/natcatty/ui/connection"
	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
	sessionui "github.com/mslxl/natcatty/ui/session"
	"github.com/mslxl/natcatty/ui/shell/state"
	uitheme "github.com/mslxl/natcatty/ui/theme"
)

// tabRef binds the split renderer to either a main-window Tab or a detached
// Tab. The renderer asks it for Pane state and hands back focus and resize.
type tabRef struct {
	id       model.TabID
	pane     func(model.PaneID) (state.PaneInfo, bool)
	setRatio func(model.SplitID, float32) bool
	focus    func(model.PaneID) bool
	// managesPanes marks a Tab whose window owns Pane commands, drag/drop, and
	// Pane close. A detached window holds one Tab and has no split commands
	// yet.
	managesPanes bool
	splitRight   func() error
	splitDown    func() error
	closeFocused func() bool
	detach       func(model.PaneID) bool
	rename       func(string) bool
	retry        func(model.PaneID) bool
	// split marks a Tab with more than one Pane: only then does the focused
	// Pane need an outline to tell it apart.
	split bool
	// status is the focused Pane's Session status, shown in its header while
	// it is not settled.
	status paneStatus
}

func mainTabRef(s *state.State, tabIndex int) tabRef {
	id, _ := s.TabID(tabIndex)
	return tabRef{
		id:           id,
		pane:         func(id model.PaneID) (state.PaneInfo, bool) { return s.PaneInfoAt(tabIndex, id) },
		setRatio:     func(id model.SplitID, ratio float32) bool { return s.SetSplitRatio(tabIndex, id, ratio) },
		focus:        func(id model.PaneID) bool { return s.FocusPaneID(tabIndex, id) },
		managesPanes: true,
		splitRight:   s.SplitRight,
		splitDown:    s.SplitDown,
		closeFocused: func() bool {
			return s.CloseFocusedPane()
		},
		detach: func(paneID model.PaneID) bool { return s.DetachPane(tabIndex, paneID) },
		rename: func(title string) bool { return s.RenameTab(tabIndex, title) },
		retry:  func(paneID model.PaneID) bool { return s.RetryPane(id, paneID) },
	}
}

func detachedTabRef(s *state.State, tabID model.TabID) tabRef {
	return tabRef{
		id:       tabID,
		pane:     func(id model.PaneID) (state.PaneInfo, bool) { return s.DetachedPaneInfoAt(tabID, id) },
		setRatio: func(id model.SplitID, ratio float32) bool { return s.SetDetachedSplitRatio(tabID, id, ratio) },
		focus:    func(id model.PaneID) bool { return s.FocusDetachedPaneID(tabID, id) },
		rename:   func(title string) bool { return s.RenameDetachedTab(tabID, title) },
		retry:    func(paneID model.PaneID) bool { return s.RetryPane(tabID, paneID) },
	}
}

// workTabReveal remembers which work Tab the strip last showed and how wide
// the window was. The strip scrolls sideways, so without this a Tab opened or
// chosen while the strip is scrolled could stay out of view; the active Tab is
// scrolled into view whenever either changes.
type workTabReveal struct {
	active model.TabID
	width  float32
	reveal bool
}

func newWorkTabReveal() *workTabReveal { return &workTabReveal{} }

// workTabs renders the window's Tab bar, as the original's: the home Tab and
// SFTP first, then the ordered terminal Tabs and the button that opens a new
// one, and at the right the language menu and Settings. Each terminal Tab is a drag source; the
// thin gaps on either side of it are the insertion targets that light up
// where a dropped Tab will land. A drop directly on a Tab inserts before it.
func workTabs(c *ui.Context, s *state.State, m locales.Messages, t *ui.Theme, reveal *workTabReveal) {
	if reveal == nil {
		reveal = newWorkTabReveal()
	}
	activeID, _ := s.TabID(s.ActiveTab())
	if activeID != "" && activeID != reveal.active {
		reveal.active = activeID
		reveal.reveal = true
	}
	// A resize changes the room the strip has for its Tabs. Reveal the active
	// Tab again so one chosen at a wider window does not stay half cut.
	if width, _ := c.Size(); width > 0 {
		if reveal.width > 0 && width != reveal.width {
			reveal.reveal = true
		}
		reveal.width = width
	}
	revealActive := reveal.reveal
	reveal.reveal = false
	terminal := terminalVisible(s)

	// The bar keeps its height when the surface below needs room. Without
	// Shrink(0) a Column under height pressure shrinks the first flexible
	// child to nothing, hiding every Tab at the minimum window size.
	ui.Row(c).FillWidth().Height(topBarHeight).Shrink(0).Padding(0, 8).Gap(4).AlignItems(ui.Center).Children(func() {
		if fixedTab(c, "tab-home", icons.LayoutGrid, m.NavVault(), homeVisible(s)).Clicked() {
			s.ShowHome()
			c.Invalidate()
		}
		if s.SFTPTabShown() {
			if fixedTab(c, "tab-sftp", icons.FolderOpen, m.NavSftp(), s.SFTPVisible()).Clicked() {
				s.ShowSFTP()
				c.Invalidate()
			}
		} else if s.SFTPVisible() {
			// Hiding the Tab while it is shown leaves for the Vault, as the
			// original's; the Session panel still browses files.
			s.ShowHome()
			c.Invalidate()
		}
		ui.Box(c).Size(1, 18).Margin(0, 4).Background(t.Border).Shrink(0)
		// The strip scrolls sideways at the minimum window width. Tabs keep a
		// readable title width instead of shrinking or clipping the last Tab;
		// every Tab stays reachable with the wheel, trackpad or keyboard.
		ui.ScrollHorizontal(c).Grow(1).MinWidth(0).Height(topBarHeight).Label(m.WorkTabsLabel()).
			Children(func() {
				ui.Row(c).Height(topBarHeight).AlignItems(ui.Center).Children(func() {
					for index := 0; index < s.TabCount(); index++ {
						info, ok := s.TabInfo(index)
						if !ok {
							continue
						}
						if !terminal {
							info.Active = false
						}
						tabInsertionStrip(c, s, t, info.ID, false)
						tabBody(c, s, m, t, index, info, revealActive && terminal)
						tabInsertionStrip(c, s, t, info.ID, true)
					}
					for _, label := range s.PendingConnections() {
						pendingTab(c, m, t, label)
					}
					if tabControl(c, icons.Plus, m.MenuNewTab()).Margin(0, 0, 0, 4).Clicked() {
						_ = s.NewTab()
						c.Invalidate()
					}
				})
			})
		languageMenu(c, s, m)
		if barButton(c, "bar-settings", icons.Settings, m.NavSettings()).Clicked() {
			s.RequestSettings()
		}
	})
}

// fixedTab is one of the Tabs every window has, the home Tab and SFTP: an
// icon and a label, raised while it is the chosen one.
func fixedTab(c *ui.Context, key, icon, label string, active bool) ui.Element {
	t := c.Theme()
	tab := ui.ButtonBase(c).Key(key).Label(label).Height(30).Padding(0, 12).Gap(8).
		Radius(kit.RadiusControl).AlignItems(ui.Center).Shrink(0).TextColor(t.TextMuted)
	switch {
	case active:
		tab.Background(t.Surface).Border(1, t.Border).Shadow(0, 1, 2, 0, kit.CardShadow(t)).TextColor(t.Text)
	case tab.Pressed():
		tab.Background(kit.Strong(t)).TextColor(t.Text)
	case tab.Hovered():
		tab.Background(kit.Subtle(t)).TextColor(t.Text)
	}
	tab.Children(func() {
		ui.Icon(c, icons.Lucide(icon)).Size(15, 15)
		text := ui.Text(c, label).FontSize(13).SingleLine()
		if active {
			text.FontWeight(500)
		}
	})
	return tab
}

// terminalVisible reports whether the active Tab's Panes are the visible
// surface rather than one of the root surfaces.
func terminalVisible(s *state.State) bool {
	return s.TabCount() > 0 && !s.VaultVisible() && !s.LogsVisible() &&
		!s.MonitorVisible() && !s.SysManagerVisible() && !s.SFTPVisible()
}

func tabInsertionStrip(c *ui.Context, s *state.State, t *ui.Theme, target model.TabID, after bool) {
	strip := ui.Column(c).Width(4).Height(24).Radius(2)
	if drag, over := ui.DragOver[state.DragItem](strip); over &&
		drag.Kind == state.DragKindTab && drag.TabID != target {
		strip.Background(t.Accent)
	}
	if drag, dropped := ui.Drop[state.DragItem](strip); dropped &&
		drag.Kind == state.DragKindTab {
		if s.MoveTab(drag.TabID, target, after) {
			c.Invalidate()
		}
	}
}

func tabBody(c *ui.Context, s *state.State, m locales.Messages, t *ui.Theme, index int, info state.TabInfo, reveal bool) {
	title := tabTitle(m, info)
	body := ui.Row(c).Height(30).MinWidth(150).MaxWidth(240).Padding(0, 4, 0, 10).Gap(8).
		Radius(kit.RadiusControl).AlignItems(ui.Center).Tooltip(title)
	hovered := body.Hovered()
	switch {
	case info.Active:
		body.Background(t.Surface).Border(1, t.Border).Shadow(0, 1, 2, 0, kit.CardShadow(t))
	case hovered:
		body.Background(kit.Subtle(t))
	}
	body.Drag(state.TabDrag(info.ID))
	if body.Clicked() {
		s.SelectTab(index)
		c.Invalidate()
	}
	if drag, over := ui.DragOver[state.DragItem](body); over && drag.TabID != info.ID {
		body.Border(1, t.Accent)
	}
	if drag, dropped := ui.Drop[state.DragItem](body); dropped && drag.TabID != info.ID {
		moved := false
		switch drag.Kind {
		case state.DragKindTab:
			moved = s.MoveTab(drag.TabID, info.ID, false)
		case state.DragKindPane:
			moved = s.MovePaneToTab(drag.TabID, drag.PaneID, info.ID)
		}
		if moved {
			c.Invalidate()
		}
	}
	tabMenu := func(menu *ui.Menu) {
		if menu.Item(m.MenuCloseTab()).Chosen() {
			s.CloseTab(index)
			c.Invalidate()
		}
		if menu.Item(m.TabCloseOthers()).Disabled(s.TabCount() <= 1).Chosen() {
			s.CloseOtherTabs(index)
			c.Invalidate()
		}
		if menu.Item(m.TabCloseRight()).Disabled(index >= s.TabCount()-1).Chosen() {
			s.CloseTabsRight(index)
			c.Invalidate()
		}
		menu.Separator()
		if menu.Item(m.TabDuplicate()).Chosen() {
			_ = s.DuplicateTab(index)
			c.Invalidate()
		}
		if menu.Item(m.TabCopyToNewWindow()).Chosen() {
			_ = s.DuplicateTabToWindow(index)
			c.Invalidate()
		}
		if menu.Item(m.TabDetach()).Chosen() {
			s.DetachTab(index)
			c.Invalidate()
		}
	}
	body.ContextMenu(tabMenu)
	body.Children(func() {
		snapshot, ok := s.TabSnapshot(index)
		kit.StatusDot(c, sessionTone(t, snapshot, ok))
		// SingleLine text can shrink to nothing in a flex row; give the title
		// a readable minimum so it is not squeezed out beside the controls.
		titleText := ui.Text(c, title).FontSize(13).SingleLine().MinWidth(72).Grow(1)
		if info.Active {
			titleText.FontWeight(500).TextColor(t.Text)
		} else {
			titleText.TextColor(t.TextMuted)
		}
		if info.Dirty {
			ui.Text(c, m.TabDirtyMarker()).FontSize(9).TextColor(t.Accent)
		}
		// The menu and close controls show on the active and the hovered Tab,
		// as the original's; a right-click opens the same menu on any Tab.
		if info.Active || hovered {
			tabControl(c, icons.Ellipsis, m.TabMenuGlyph()).Menu(tabMenu)
			if tabControl(c, icons.X, m.CloseGlyph()).Clicked() {
				s.CloseTab(index)
				c.Invalidate()
			}
		}
	})
	if info.Active && reveal {
		// The strip scrolls sideways; keep the active Tab whole when it is
		// opened or selected while the strip is scrolled, and after a resize.
		body.ScrollIntoView()
	}
}

// tabControl is a small icon button inside a Tab or a Pane header.
func tabControl(c *ui.Context, icon, label string) ui.Element {
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

// pendingTab is a connection still opening, shown after the Tabs.
func pendingTab(c *ui.Context, m locales.Messages, t *ui.Theme, label string) {
	ui.Row(c).Height(30).Padding(0, 10).Gap(8).Margin(0, 0, 0, 4).Radius(kit.RadiusControl).
		Border(1, t.Border).AlignItems(ui.Center).TextColor(t.TextMuted).Children(func() {
		ui.Icon(c, icons.Lucide(icons.Loader)).Size(14, 14).TextColor(t.Warning)
		ui.Text(c, m.ConnectionConnectingTo(label)).FontSize(12).SingleLine()
	})
}

func tabTitle(m locales.Messages, info state.TabInfo) string {
	number := info.TitleNumber
	if number == 0 {
		number = info.Number
	}
	return composeTabTitle(m, info.Label, number, info.Copy)
}

// TabTitle names a Tab summary the way the tab strip names the Tab. The Quick
// Switcher reuses it, so a Session is found by the title the user sees.
func TabTitle(m locales.Messages, info state.TabSummary) string {
	return composeTabTitle(m, info.Label, info.Number, info.Copy)
}

func composeTabTitle(m locales.Messages, label string, number, copy int) string {
	title := label
	if title == "" {
		title = m.LocalSessionTab(number)
	}
	if copy > 0 {
		title = m.TabCopyTitle(title, copy)
	}
	return title
}

// paneTree renders a Tab's split tree with nested binary MyGo splits. A
// horizontal split in the domain is top/bottom, so it renders with
// SplitVerticalPercent; a vertical split is left/right. Dragging a divider
// updates the domain ratio so it survives restore.
func paneTree(c *ui.Context, s *state.State, m locales.Messages, t *ui.Theme, ref tabRef, node *domainworkspace.Node, focus []int, ordinal *int) {
	if node == nil {
		return
	}
	if node.IsPane() {
		paneView(c, s, m, t, ref, node.PaneID, ordinal)
		return
	}
	ratio := node.Ratio
	if ratio <= 0 || ratio >= 100 {
		ratio = domainworkspace.DefaultRatio
	}
	splitID := node.SplitID
	first := func() { paneTree(c, s, m, t, ref, node.First, focus, ordinal) }
	second := func() { paneTree(c, s, m, t, ref, node.Second, focus, ordinal) }
	var split ui.Element
	if node.Direction == domainworkspace.Horizontal {
		split = ui.SplitVerticalPercent(c, &ratio, first, second)
	} else {
		split = ui.SplitPercent(c, &ratio, first, second)
	}
	if split.Changed() {
		ref.setRatio(splitID, ratio)
	}
	split.Fill()
}

func paneView(c *ui.Context, s *state.State, m locales.Messages, t *ui.Theme, ref tabRef, paneID model.PaneID, ordinal *int) {
	info, ok := ref.pane(paneID)
	if !ok {
		return
	}
	// The Session menu lives inside the terminal surface, so the shell hands
	// it the Pane commands for this exact Pane. A detached Pane gets the zero
	// value and its split/close items show disabled.
	title := paneTitle(m, info)
	*ordinal++
	paneNumber := *ordinal
	if aware, ok := info.Surface.(interface {
		SetPaneActions(sessionui.PaneActions)
	}); ok {
		actions := sessionui.PaneActions{
			Title:     title,
			MenuLabel: m.SessionMenuFor(paneNumber),
		}
		if ref.managesPanes {
			actions.SplitRight = func() {
				ref.focus(info.ID)
				_ = ref.splitRight()
				c.Invalidate()
			}
			actions.SplitDown = func() {
				ref.focus(info.ID)
				_ = ref.splitDown()
				c.Invalidate()
			}
			actions.Detach = func() {
				if ref.detach(info.ID) {
					c.Invalidate()
				}
			}
			actions.Close = func() {
				ref.focus(info.ID)
				ref.closeFocused()
				c.Invalidate()
			}
			actions.Rename = func(newTitle string) {
				if ref.rename != nil && ref.rename(newTitle) {
					c.Invalidate()
				}
			}
			if host, ok := paneFiles(s, info); ok {
				tabID := ref.id
				actions.FilesOpen = s.FilesOpen(tabID)
				actions.ToggleFiles = func() {
					ref.focus(info.ID)
					s.ToggleFiles(tabID, host, info.Label)
					c.Invalidate()
				}
			}
		}
		aware.SetPaneActions(actions)
	}
	tabID := ref.id
	column := ui.Column(c).Fill()
	column.Children(func() {
		if ref.managesPanes {
			paneDropZone(c, s, t, tabID, paneID, domainworkspace.SideTop, true)
		}
		ui.Row(c).Fill().Children(func() {
			if ref.managesPanes {
				paneDropZone(c, s, t, tabID, paneID, domainworkspace.SideLeft, false)
			}
			pane := ui.Column(c).Grow(1).FillHeight().MinWidth(0).
				Background(t.Surface).Clip()
			// Naming the Pane region gives assistive technology and the
			// layout tests the Pane's exact box. Several Panes in one window
			// must be distinguishable by their titles.
			pane.Label(m.PaneRegion(paneNumber))
			if ref.split {
				pane.Border(1, t.Border)
				if info.Focused {
					pane.Border(1, t.Accent.Alpha(0.7))
				}
			}
			pane.Children(func() {
				paneHeader(c, s, m, t, ref, info)
				paneContent(c, s, m, t, ref, info)
			})
			if ref.managesPanes {
				paneDropZone(c, s, t, tabID, paneID, domainworkspace.SideRight, false)
			}
		})
		if ref.managesPanes {
			paneDropZone(c, s, t, tabID, paneID, domainworkspace.SideBottom, true)
		}
	})
}

// paneDropZone is one side target for moving a Pane: a thin gutter that
// lights while a Pane is dragged over it. A drag of the Pane itself never
// lights it: a Pane cannot be dropped on itself.
func paneDropZone(c *ui.Context, s *state.State, t *ui.Theme,
	tabID model.TabID, paneID model.PaneID, side domainworkspace.Side, horizontal bool) {
	var zone ui.Element
	if horizontal {
		zone = ui.Column(c).Height(4).FillWidth().Radius(2)
	} else {
		// FillHeight, not Fill: a side zone is a few DIPs wide, and Fill
		// would claim the row's whole width and squeeze the Pane beside it.
		zone = ui.Column(c).Width(4).FillHeight().Radius(2)
	}
	if drag, over := ui.DragOver[state.DragItem](zone); over &&
		drag.Kind == state.DragKindPane && drag.PaneID != paneID {
		zone.Background(t.Accent)
	}
	if drag, dropped := ui.Drop[state.DragItem](zone); dropped &&
		drag.Kind == state.DragKindPane {
		if s.MovePane(drag.TabID, drag.PaneID, tabID, paneID, side) {
			c.Invalidate()
		}
	}
}

// paneHeader is the Pane's one header row, as the original's terminal
// toolbar: the drag grip of a split Tab, the title, a status only while the
// Session is not simply connected, and at the right the Session's controls
// (Files, the splits, its menu), the window's broadcast and compose toggles
// on the focused Pane, and Close.
func paneHeader(c *ui.Context, s *state.State, m locales.Messages, t *ui.Theme,
	ref tabRef, info state.PaneInfo) {
	header := ui.Row(c).FillWidth().Height(30).Padding(0, 4, 0, 8).Gap(4).Shrink(0).
		Background(kit.Muted(t)).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).AlignItems(ui.Center)
	header.Children(func() {
		// The grip shows where a drop can land: another Pane of a split Tab,
		// or another Tab.
		if ref.managesPanes && (ref.split || s.TabCount() > 1) {
			handle := ui.Box(c).Label(m.PaneDragHandle()).Size(16, 22).Center().TextColor(t.TextMuted).
				Children(func() { ui.Icon(c, icons.Lucide(icons.GripVertical)).Size(14, 14) })
			handle.Drag(state.PaneDrag(ref.id, info.ID))
		}

		titleElement := ui.Text(c, paneTitle(m, info)).FontSize(12).SingleLine().MinWidth(0).Shrink(1)
		if info.Focused {
			titleElement.FontWeight(500).TextColor(t.Text)
		} else {
			titleElement.TextColor(t.TextMuted)
		}
		if titleElement.Clicked() {
			if ref.focus(info.ID) {
				c.Invalidate()
			}
		}
		switch {
		case info.Pending:
			statusPill(c, t.Warning, m.PaneConnecting())
		case info.Focused && ref.status.text != "" && !ref.status.settled:
			statusPill(c, ref.status.tone, ref.status.text)
		}

		ui.Column(c).Grow(1).MinWidth(0)

		if !ref.managesPanes {
			return
		}
		if info.Focused {
			terminalToolbar(c, s, m, t)
		}
		if bar, ok := info.Surface.(interface{ Toolbar(*ui.Context) }); ok && info.Err == nil {
			bar.Toolbar(c)
		} else {
			paneMenu(c, m, ref, info)
		}
		if tabControl(c, icons.X, m.CloseGlyph()).Clicked() {
			ref.focus(info.ID)
			ref.closeFocused()
			c.Invalidate()
		}
	})
}

// paneMenu is the Pane commands of a Pane with no Session to hold them: one
// that failed to open or is still connecting.
func paneMenu(c *ui.Context, m locales.Messages, ref tabRef, info state.PaneInfo) {
	tabControl(c, icons.EllipsisVertical, m.PaneMenuGlyph()).Menu(func(menu *ui.Menu) {
		if menu.Item(m.PaneSplitRight()).Chosen() {
			ref.focus(info.ID)
			_ = ref.splitRight()
			c.Invalidate()
		}
		if menu.Item(m.PaneSplitDown()).Chosen() {
			ref.focus(info.ID)
			_ = ref.splitDown()
			c.Invalidate()
		}
		menu.Separator()
		if menu.Item(m.PaneDetach()).Chosen() {
			if ref.detach(info.ID) {
				c.Invalidate()
			}
		}
		menu.Separator()
		if menu.Item(m.PaneClose()).Chosen() {
			ref.focus(info.ID)
			ref.closeFocused()
			c.Invalidate()
		}
	})
}

// paneFiles reports what the Pane's Files button browses: its Host for an SSH
// Session, this computer (an empty Host) for a local one. Other Sessions,
// such as serial and Telnet, have no file system to show.
func paneFiles(s *state.State, info state.PaneInfo) (model.HostID, bool) {
	if !s.FilesAvailable() {
		return "", false
	}
	owner, ok := info.Surface.(interface{ Session() *appsession.Session })
	if !ok || owner.Session() == nil {
		return "", false
	}
	session := owner.Session()
	switch session.Snapshot().Kind {
	case appsession.KindSSH:
		if session.HostID() == "" {
			return "", false
		}
		return session.HostID(), true
	case appsession.KindLocal:
		return "", true
	}
	return "", false
}

// statusPill is a Session status: a dot in tone and its text.
func statusPill(c *ui.Context, tone ui.Color, text string) {
	ui.Row(c).Height(20).Padding(0, 8).Gap(6).Radius(10).Background(tone.Alpha(0.12)).
		AlignItems(ui.Center).Shrink(0).Children(func() {
		ui.Box(c).Size(6, 6).Radius(3).Background(tone)
		ui.Text(c, text).FontSize(11).SingleLine().TextColor(c.Theme().Text)
	})
}

// paneTitle is the Pane's display name: the Session/Host label, or the
// numbered local-session name when the Session has none.
func paneTitle(m locales.Messages, info state.PaneInfo) string {
	if info.Label != "" {
		return info.Label
	}
	return m.LocalSessionTab(info.Number)
}

func paneContent(c *ui.Context, s *state.State, m locales.Messages, t *ui.Theme,
	ref tabRef, info state.PaneInfo) {
	switch {
	case info.Err != nil:
		// A missing Host cannot come back by retrying, so it keeps the shell's
		// own text and offers no control. Every other failure is localized from
		// its typed connection error and can be retried in place.
		message := m.PaneOpenFailed()
		retryable := true
		if errors.Is(info.Err, state.ErrHostUnavailable) {
			message = m.PaneHostMissing()
			retryable = false
		} else if problem := uiconnection.ProblemTextFor(m, info.Err); problem != "" {
			message = problem
		}
		ui.Column(c).Fill().Padding(24).Gap(10).Justify(ui.Center).AlignItems(ui.Center).Children(func() {
			ui.Box(c).Size(44, 44).Radius(12).Background(t.Danger.Alpha(0.1)).Center().TextColor(t.Danger).
				Children(func() { ui.Icon(c, icons.Lucide(icons.TriangleAlert)).Size(20, 20) })
			ui.Text(c, message).FontSize(13).MaxWidth(420).TextColor(uitheme.DangerText(t))
			if !retryable {
				return
			}
			retry := kit.PrimaryButton(c, icons.RefreshCw, m.SessionMenuReconnect())
			// The focused Pane owns the keyboard, so Enter and Space reconnect
			// it. A Pane that is not focused must not steal the keyboard.
			if info.Focused {
				retry.AutoFocus()
			}
			if retry.Clicked() && ref.retry != nil && ref.retry(info.ID) {
				c.Invalidate()
			}
		})
	case info.Surface != nil:
		// The focused Pane's terminal takes the keyboard; every other Pane
		// renders without asking for it, so a split cannot steal the focus.
		if target, ok := info.Surface.(state.FocusedSurface); ok {
			target.ViewFocused(c, info.Focused)
			return
		}
		info.Surface.View(c)
	default:
		ui.Column(c).Fill().Gap(10).Justify(ui.Center).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, icons.Lucide(icons.Loader)).Size(22, 22).TextColor(t.TextMuted)
			ui.Text(c, m.PaneConnecting()).TextColor(t.TextMuted).FontSize(13)
		})
	}
}
