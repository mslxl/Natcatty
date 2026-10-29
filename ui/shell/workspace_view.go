package shell

import (
	"errors"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/domain/model"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
	uiconnection "github.com/mslxl/natcatty/ui/connection"
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

// workTabs renders the ordered Tab strip. Each Tab is a drag source; the thin
// strips on either side of it are the insertion targets that show where a
// dropped Tab will land. A drop directly on a Tab inserts before it.
func workTabs(c *ui.Context, s *state.State, m locales.Messages, t *ui.Theme, reveal *workTabReveal) {
	// The strip scrolls sideways at the minimum window width. Tabs keep a
	// readable title width instead of shrinking or clipping the last Tab;
	// every Tab stays reachable with the wheel, trackpad or keyboard.
	// The strip keeps its 30 DIPs when the panels below need room. Without
	// Shrink(0) a Column under height pressure shrinks the first flexible
	// child to nothing, hiding every Tab at the minimum window size.
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

	ui.ScrollHorizontal(c).FillWidth().Height(30).Shrink(0).Label(m.WorkTabsLabel()).
		Children(func() {
			ui.Row(c).Gap(2).Children(func() {
				for index := 0; index < s.TabCount(); index++ {
					info, ok := s.TabInfo(index)
					if !ok {
						continue
					}
					tabInsertionStrip(c, s, t, info.ID, false)
					tabBody(c, s, m, t, index, info, revealActive)
					tabInsertionStrip(c, s, t, info.ID, true)
				}
			})
		})
}

func tabInsertionStrip(c *ui.Context, s *state.State, t *ui.Theme, target model.TabID, after bool) {
	// FillHeight, not Fill: Fill would make this 5-DIP insertion strip claim
	// the whole tab strip width and push the Tab bodies apart.
	strip := ui.Column(c).Width(5).FillHeight().Background(t.Border)
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
	background := t.Background
	if info.Active {
		background = t.Surface
	}
	body := ui.Row(c).Height(30).Padding(2, 8).Gap(6).Radius(t.Radius).
		Background(background).AlignItems(ui.Center)
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
	body.Children(func() {
		// SingleLine text can shrink to nothing in a flex row; give the title
		// a readable minimum so it is not squeezed out beside the menu and
		// close controls.
		title := ui.Text(c, tabTitle(m, info)).FontSize(12).SingleLine().MinWidth(96)
		if info.Active {
			title.Bold()
		}
		if info.Dirty {
			ui.Text(c, m.TabDirtyMarker()).FontSize(10).TextColor(uitheme.WarningText(t))
		}
		ui.Column(c).Grow(1)
		ui.MenuButton(c, m.TabMenuGlyph(), func(menu *ui.Menu) {
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
				if err := s.DuplicateTab(index); err != nil {
					_ = err
				}
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
		})
		closeButton := ui.Text(c, m.CloseGlyph()).FontSize(14).TextColor(t.TextMuted).Padding(0, 2)
		if closeButton.Clicked() {
			s.CloseTab(index)
			c.Invalidate()
		}
	})
	if info.Active && reveal {
		// The strip scrolls sideways; keep the active Tab whole when it is
		// opened or selected while the strip is scrolled, and after a resize.
		body.ScrollIntoView()
	}
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
		}
		aware.SetPaneActions(actions)
	}
	tabID := ref.id
	column := ui.Column(c).Fill().Background(t.Background)
	column.Children(func() {
		if ref.managesPanes {
			paneDropZone(c, s, t, tabID, paneID, domainworkspace.SideTop, true)
		}
		ui.Row(c).Fill().Children(func() {
			if ref.managesPanes {
				paneDropZone(c, s, t, tabID, paneID, domainworkspace.SideLeft, false)
			}
			pane := ui.Column(c).Grow(1).FillHeight().Background(t.Background)
			// Naming the Pane region gives assistive technology and the
			// layout tests the Pane's exact box. Several Panes in one window
			// must be distinguishable by their titles.
			pane.Label(m.PaneRegion(paneNumber))
			if info.Focused {
				pane.Border(1, t.Accent)
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

// paneDropZone is one side target for moving a Pane. A drag of the Pane
// itself never lights it: a Pane cannot be dropped on itself.
func paneDropZone(c *ui.Context, s *state.State, t *ui.Theme,
	tabID model.TabID, paneID model.PaneID, side domainworkspace.Side, horizontal bool) {
	var zone ui.Element
	if horizontal {
		zone = ui.Column(c).Height(6).FillWidth().Background(t.Border)
	} else {
		// FillHeight, not Fill: a side zone is 6 DIPs wide, and Fill would
		// claim the row's whole width and squeeze the Pane beside it.
		zone = ui.Column(c).Width(6).FillHeight().Background(t.Border)
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

func paneHeader(c *ui.Context, s *state.State, m locales.Messages, t *ui.Theme,
	ref tabRef, info state.PaneInfo) {
	header := ui.Row(c).Height(24).Padding(1, 6).Gap(6).
		Background(t.Surface).AlignItems(ui.Center)
	header.Children(func() {
		handle := ui.Text(c, m.PaneDragHandle()).FontSize(11).TextColor(t.TextMuted)
		if ref.managesPanes {
			handle.Drag(state.PaneDrag(ref.id, info.ID))
		}

		titleElement := ui.Text(c, paneTitle(m, info)).FontSize(11)
		if info.Focused {
			titleElement.Bold().TextColor(t.Text)
		} else {
			titleElement.TextColor(t.TextMuted)
		}
		if titleElement.Clicked() {
			if ref.focus(info.ID) {
				c.Invalidate()
			}
		}
		if info.Pending {
			ui.Text(c, m.PaneConnecting()).FontSize(10).TextColor(t.TextMuted)
		}

		ui.Column(c).Grow(1)

		if ref.managesPanes {
			ui.MenuButton(c, m.PaneMenuGlyph(), func(menu *ui.Menu) {
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
			closeButton := ui.Text(c, m.CloseGlyph()).FontSize(13).TextColor(t.TextMuted).Padding(0, 2)
			if closeButton.Clicked() {
				ref.focus(info.ID)
				ref.closeFocused()
				c.Invalidate()
			}
		}
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
		ui.Column(c).Fill().Padding(12).Children(func() {
			ui.Text(c, message).TextColor(uitheme.DangerText(t)).FontSize(12)
			if !retryable {
				return
			}
			retry := ui.Button(c, m.SessionMenuReconnect())
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
		ui.Column(c).Fill().Padding(12).Children(func() {
			ui.Text(c, m.PaneConnecting()).TextColor(t.TextMuted).FontSize(12)
		})
	}
}
