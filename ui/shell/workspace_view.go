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
	rename       func(model.PaneID, string) bool
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
		rename: func(paneID model.PaneID, title string) bool {
			return s.RenamePane(tabIndex, paneID, title)
		},
		retry: func(paneID model.PaneID) bool { return s.RetryPane(id, paneID) },
	}
}

func detachedTabRef(s *state.State, tabID model.TabID) tabRef {
	return tabRef{
		id:       tabID,
		pane:     func(id model.PaneID) (state.PaneInfo, bool) { return s.DetachedPaneInfoAt(tabID, id) },
		setRatio: func(id model.SplitID, ratio float32) bool { return s.SetDetachedSplitRatio(tabID, id, ratio) },
		focus:    func(id model.PaneID) bool { return s.FocusDetachedPaneID(tabID, id) },
		rename: func(paneID model.PaneID, title string) bool {
			return s.RenameDetachedPane(tabID, paneID, title)
		},
		retry: func(paneID model.PaneID) bool { return s.RetryPane(tabID, paneID) },
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

// workTabs renders the ordered terminal Tabs in the window's Tab bar and the
// button that opens a new one. Each terminal Tab is a drag source; the
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

	// The strip scrolls sideways at the minimum window width. Tabs keep a
	// readable title width instead of shrinking or clipping the last Tab;
	// every Tab stays reachable with the wheel, trackpad or keyboard.
	added := kit.TabStrip(c, kit.TabWork, kit.TabStripOptions{
		Label: m.WorkTabsLabel(), Height: topBarHeight, Add: m.MenuNewTab(),
	}, func() {
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
	})
	if added {
		_ = s.NewTab()
		c.Invalidate()
	}
}

// tabRadius rounds a Tab's top corners, as the original's rounded-t-md.
const tabRadius float32 = 6

// terminalVisible reports whether the active Tab's Panes are the visible
// surface rather than one of the root surfaces.
func terminalVisible(s *state.State) bool {
	return s.TabCount() > 0 && !s.VaultVisible() && !s.LogsVisible() &&
		!s.MonitorVisible() && !s.SysManagerVisible() && !s.SFTPVisible()
}

func tabInsertionStrip(c *ui.Context, s *state.State, t *ui.Theme, target model.TabID, after bool) {
	strip := ui.Column(c).Width(2).Height(22).Margin(0, 0, 3, 0).Radius(1)
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
	marker := ""
	if info.Dirty {
		marker = m.TabDirtyMarker()
	}
	// The menu and close controls show on the active and the hovered Tab,
	// as the original's; a right-click opens the same menu on any Tab. The
	// strip scrolls sideways, so reveal keeps the active Tab whole when it is
	// opened or selected while the strip is scrolled, and after a resize.
	tab := kit.Tab(c, kit.TabWork, kit.TabSpec{
		Title:  tabTitle(m, info),
		Active: info.Active,
		Lead: func() {
			snapshot, ok := s.TabSnapshot(index)
			kit.StatusDot(c, sessionTone(t, snapshot, ok))
		},
		Marker:      marker,
		Close:       m.CloseGlyph(),
		MenuLabel:   m.TabMenuGlyph(),
		Menu:        tabMenu,
		ContextMenu: tabMenu,
		Reveal:      reveal,
	})
	body := tab.Element
	body.Drag(state.TabDrag(info.ID))
	if tab.Clicked {
		s.SelectTab(index)
		c.Invalidate()
	}
	if tab.Closed {
		s.CloseTab(index)
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
}

// pendingTab is a connection still opening, shown after the Tabs.
func pendingTab(c *ui.Context, m locales.Messages, t *ui.Theme, label string) {
	ui.Row(c).Height(tabHeight).Padding(0, 12).Gap(8).Margin(0, 0, 0, 4).Radius(tabRadius, tabRadius, 0, 0).
		AlignItems(ui.Center).TextColor(t.TextMuted).Children(func() {
		kit.IconSized(c, icons.Loader, kit.M(c).Icon.Medium).TextColor(t.Warning)
		ui.Text(c, m.ConnectionConnectingTo(label)).FontSize(kit.M(c).Font.Footnote).SingleLine()
	})
}

func tabTitle(m locales.Messages, info state.TabInfo) string {
	number := info.TitleNumber
	if number == 0 {
		number = info.Number
	}
	title := info.Title
	if title == "" {
		title = info.Label
	}
	return composeTabTitle(m, title, number)
}

// TabTitle names a Tab summary the way the tab strip names the Tab. The Quick
// Switcher reuses it, so a Session is found by the title the user sees.
func TabTitle(m locales.Messages, info state.TabSummary) string {
	return composeTabTitle(m, info.Label, info.Number)
}

// composeTabTitle adds the numbered fallback for a Tab with no resolved title.
// A duplicate keeps its source's title, as the original does.
func composeTabTitle(m locales.Messages, label string, number int) string {
	if label == "" {
		return m.LocalSessionTab(number)
	}
	return label
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
		// Rename starts from the connection label, as the original's does, not
		// from a live OSC title that the user did not choose.
		renameStart := info.Label
		if renameStart == "" {
			renameStart = title
		}
		actions := sessionui.PaneActions{
			Title:     renameStart,
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
				if ref.rename != nil && ref.rename(info.ID, newTitle) {
					c.Invalidate()
				}
			}
			if host, ok := paneFiles(s, info); ok {
				tabID := ref.id
				actions.FilesOpen = s.FilesOpen(tabID)
				actions.FilesIcon = dockIcon(s.FilesDock())
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
				Background(t.Background).Clip()
			// Naming the Pane region gives assistive technology and the
			// layout tests the Pane's exact box. Several Panes in one window
			// must be distinguishable by their titles.
			pane.Label(m.PaneRegion(paneNumber))
			if ref.split {
				kit.PaneFrame(c, pane, info.Focused)
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
	// A detached window's lone Pane is already named by the window's title
	// bar, as the original's terminal popup, so it shows no header of its own
	// unless it has a status to report.
	lone := !ref.managesPanes && !ref.split
	if lone && !info.Pending && !(info.Focused && ref.status.text != "" && !ref.status.settled) {
		return
	}
	header := kit.Bar(c, kit.BarPane).Padding(0, 4, 0, 8).Gap(4)
	header.Children(func() {
		// The grip shows where a drop can land: another Pane of a split Tab,
		// or another Tab.
		if ref.managesPanes && (ref.split || s.TabCount() > 1) {
			handle := ui.Box(c).Label(m.PaneDragHandle()).Size(16, 22).Center().TextColor(t.TextMuted).
				Children(func() { kit.IconSized(c, icons.GripVertical, kit.M(c).Icon.Medium) })
			handle.Drag(state.PaneDrag(ref.id, info.ID))
		}

		if !lone {
			titleElement := ui.Text(c, paneTitle(m, info)).FontSize(kit.M(c).Font.Caption).FontWeight(600).SingleLine().MinWidth(0).Shrink(1)
			if info.Focused {
				titleElement.TextColor(t.Text)
			} else {
				titleElement.TextColor(t.TextMuted)
			}
			if titleElement.Clicked() {
				if ref.focus(info.ID) {
					c.Invalidate()
				}
			}
		}
		switch {
		case info.Pending:
			kit.Badge(c, t.Warning, m.PaneConnecting())
		case info.Focused && ref.status.text != "" && !ref.status.settled:
			kit.Badge(c, ref.status.tone, ref.status.text)
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
		if kit.IconButtonWith(c, icons.X, m.CloseGlyph(), kit.IconOptions{Role: kit.IconTab}).Clicked() {
			ref.focus(info.ID)
			ref.closeFocused()
			c.Invalidate()
		}
	})
}

// paneMenu is the Pane commands of a Pane with no Session to hold them: one
// that failed to open or is still connecting.
func paneMenu(c *ui.Context, m locales.Messages, ref tabRef, info state.PaneInfo) {
	kit.IconButtonWith(c, icons.EllipsisVertical, m.PaneMenuGlyph(), kit.IconOptions{Role: kit.IconTab}).Menu(func(menu *ui.Menu) {
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

// paneTitle is the Pane's display name: the Session's resolved title (which
// may be the shell-reported OSC title), or the numbered local-session name
// when the Session has neither.
func paneTitle(m locales.Messages, info state.PaneInfo) string {
	if info.Title != "" {
		return info.Title
	}
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
			ui.Text(c, message).FontSize(kit.M(c).Font.Callout).MaxWidth(420).TextColor(uitheme.DangerText(t))
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
			ui.Text(c, m.PaneConnecting()).TextColor(t.TextMuted).FontSize(kit.M(c).Font.Callout)
		})
	}
}
