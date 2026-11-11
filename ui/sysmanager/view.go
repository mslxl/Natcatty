package sysmanager

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	appsysmanager "github.com/mslxl/natcatty/application/sysmanager"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/layout"
	"github.com/mslxl/natcatty/ui/locales"
	uitheme "github.com/mslxl/natcatty/ui/theme"
)

// surfaceGap is the vertical space between the surface's rows. It is tighter
// than the shell's 12 DIPs so the four-tab surface keeps a full list row at
// the enforced 720x480 minimum, whose client is 704x421 once the sidebar and
// the window chrome take their space.
const surfaceGap float32 = 4

// View returns the System manager surface. It is one flex column that takes
// the height its parent leaves, so the list of every tab can scroll above its
// pinned action bar instead of running past the window at the minimum size.
func View(s *State) func(*ui.Context) {
	return func(c *ui.Context) {
		s.Flush()
		m := locales.In(c.Locale())
		t := c.Theme()

		ui.Column(c).FillWidth().Grow(1).MinHeight(0).Gap(surfaceGap).Children(func() {
			header(c, s, m, t)
			if len(s.Hosts()) == 0 {
				emptyState(c, m.SysManagerNoHosts(), t)
				return
			}
			if _, ok := s.CurrentHost(); !ok {
				emptyState(c, m.SysManagerNoHostSelected(), t)
				return
			}
			tabs(c, s, m, layout.HomeContentWidth(c, true))
			tabContent(c, s, m, t, layout.HomeContentWidth(c, true))
		})
		confirmation(c, s, m)
	}
}

// listTab is the scrolling body of a tab with rows: the table takes the
// height left above the pinned action bar, so the bar's height and the gap
// below the list are reserved. A row can never scroll under the bar, and the
// last row of a longer list scrolls fully clear so its center stays
// clickable. Without the reservation the Services table showed three of four
// units at the 704x421 client of the minimum window.
func listTab(c *ui.Context, body func()) {
	ui.Column(c).Grow(1).FillWidth().MinHeight(0).Gap(surfaceGap).Children(body)
}

// actionBarHeight is the height of a tab's pinned action row: the tallest of
// its states, the button row. A hint-only bar keeps the same height, so
// choosing a row does not shrink the list above the bar or move the bar's top
// edge up into the last row.
const actionBarHeight float32 = 32

// actionBar pins the tab's action row below the list. The list's viewport is
// sized above it, so no row can scroll under the bar.
func actionBar(c *ui.Context, body func()) {
	ui.Row(c).FillWidth().Height(actionBarHeight).AlignItems(ui.Center).Gap(6).
		Children(body)
}

func header(c *ui.Context, s *State, m locales.Messages, t *ui.Theme) {
	state := s.TabState(s.Tab())
	// The title shares its row with the refresh controls, which wraps below
	// it where the content is narrow: below the window's Tab bar, a row
	// apiece left the minimum window three of a tab's four list rows.
	ui.Row(c).FillWidth().Gap(8).Wrap().AlignItems(ui.Center).Children(func() {
		ui.Row(c).Grow(1).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, m.SysManagerTitle()).FontSize(kit.M(c).Font.Title).FontWeight(600).SingleLine()
			// Every other status fills the body as the status panel, so the
			// title names only a live connection.
			if state.Status == appsysmanager.StatusLive {
				text, _ := statusText(m, state, t)
				kit.StatusDot(c, t.Success)
				ui.Text(c, text).FontSize(kit.M(c).Font.Footnote).TextColor(t.TextMuted).NoWrap()
			}
		})
		if len(s.Hosts()) > 0 {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() { refreshControls(c, s, m) })
		}
	})

	if len(s.Hosts()) == 0 {
		return
	}
	ui.Row(c).FillWidth().Gap(8).Children(func() {
		labels, byLabel := hostOptions(s)
		selected := ""
		if host, ok := s.CurrentHost(); ok {
			selected = s.HostLabel(host)
			if _, duplicate := byLabel[selected]; duplicate {
				selected = selected + " — " + host.Hostname
			}
		}
		chosen := selected
		ui.Select(c, &chosen, labels).Grow(1).MinWidth(120).Label(m.SysManagerHost())
		if chosen != selected {
			if id, ok := byLabel[chosen]; ok {
				s.SetHost(id)
				c.Invalidate()
			}
		}
		// The age and latency share the Host row instead of trailing a
		// flexible spacer in the control row, where the refresh interval, its
		// button, and the tab-layout menu left them no room at the 704-DIP
		// client: acceptance at a2d30004 measured the English Latency text 42
		// DIPs past the client. They may shrink with an ellipsis rather than
		// push either edge out.
		if state.Status == appsysmanager.StatusLive {
			if at := overviewSampledAt(state); !at.IsZero() {
				age := s.now().Sub(at)
				if age < 2*time.Second {
					age = 0
				}
				ui.Text(c, m.SysManagerUpdatedSeconds(int(age/time.Second))).
					FontSize(kit.M(c).Font.Caption).TextColor(t.TextMuted).SingleLine().Shrink(1).MinWidth(0)
				if state.Latency > 0 {
					ui.Text(c, m.SysManagerLatency()+" "+m.SysManagerLatencyValue(int(state.Latency/time.Millisecond))).
						FontSize(kit.M(c).Font.Caption).TextColor(t.TextMuted).SingleLine().Shrink(1).MinWidth(0)
				}
			}
		}
	})

	if problem := problemText(m, s.Problem()); problem != "" {
		ui.Text(c, problem).FontSize(kit.M(c).Font.Footnote).TextColor(uitheme.DangerText(t))
	}
	if notice := actionNotice(s, m); notice != "" {
		ui.Text(c, notice).FontSize(kit.M(c).Font.Footnote).TextColor(actionNoticeColor(s, t))
	}
}

// refreshControls are the refresh interval and Refresh now. The tab layout
// lives in the strip's trailing controls, beside the tabs it arranges.
func refreshControls(c *ui.Context, s *State, m locales.Messages) {
	choices := make([]string, 0, len(domainpreferences.SysManagerIntervalChoices))
	for _, seconds := range domainpreferences.SysManagerIntervalChoices {
		choices = append(choices, m.SysManagerSeconds(seconds))
	}
	current := m.SysManagerSeconds(s.Interval())
	chosen := current
	ui.Select(c, &chosen, choices).Width(84).MinWidth(84).Label(m.SysManagerRefresh())
	if chosen != current {
		for index, label := range choices {
			if label == chosen {
				s.SetInterval(domainpreferences.SysManagerIntervalChoices[index])
				break
			}
		}
		c.Invalidate()
	}
	c.Key("sysmanager-refresh")
	if kit.GhostButton(c, icons.RefreshCw, m.SysManagerRefreshNow()).Clicked() {
		s.Refresh()
		c.Invalidate()
	}
}

// tabsMenu exposes the persisted tab layout: each tab can be shown, collapsed,
// or hidden; the active tab moves among the visible tabs; and Reset layout
// restores the seven-tab default. Every change is saved by the preferences
// group, so it survives a restart and reaches the other surface. The overview
// is always shown and its collapse and hide choices are disabled.
func tabsMenu(c *ui.Context, s *State, m locales.Messages) {
	// Key the button before it is built. Element.Key on a menu button rereads
	// its identity after the menu's build was already registered for the
	// positional one, so the menu would never be built: the button showed its
	// label, yet clicking it opened nothing.
	c.Key("sysmanager-tabs-layout")
	kit.IconButton(c, icons.Columns, m.SysManagerTabsLayout()).Menu(func(menu *ui.Menu) {
		for _, entry := range tabLayoutEntries(s) {
			tab := entry.Tab
			menu.Submenu(tabLabel(m, tab), func(sub *ui.Menu) {
				if sub.Item(m.SysManagerShowTab()).Checked(entry.ShowChecked).Disabled(entry.ShowDisabled).Chosen() {
					_ = s.SetTabVisible(tab, true)
				}
				if sub.Item(m.SysManagerCollapseTab()).Checked(entry.CollapseChecked).Disabled(entry.CollapseDisabled).Chosen() {
					_ = s.SetTabCollapsed(tab, true)
				}
				if sub.Item(m.SysManagerHideTab()).Checked(entry.HideChecked).Disabled(entry.HideDisabled).Chosen() {
					_ = s.SetTabVisible(tab, false)
				}
			})
		}
		menu.Separator()
		index := s.TabIndex()
		if menu.Item(m.SysManagerMoveTabEarlier()).Disabled(index <= 0).Chosen() {
			s.MoveTab(-1)
		}
		if menu.Item(m.SysManagerMoveTabLater()).
			Disabled(index >= len(s.Tabs())-1).Chosen() {
			s.MoveTab(1)
		}
		menu.Separator()
		if menu.Item(m.SysManagerResetLayout()).Chosen() {
			s.ResetLayout()
		}
	})
}

// collapsedTabsMenu is the System Tab Strip's collapse-only overflow. It
// lists exactly the Collapsed Tabs, and draws nothing at all when there are
// none, so the strip never shows an empty collapse affordance. Choosing one
// returns it to the visible strip and activates it.
func collapsedTabsMenu(c *ui.Context, s *State, m locales.Messages) {
	collapsed := s.Collapsed()
	items := make([]kit.OverflowItem, 0, len(collapsed))
	for _, tab := range collapsed {
		items = append(items, kit.OverflowItem{
			Key: string(tab), Label: tabLabel(m, tab), Icon: TabIcon(tab),
		})
	}
	c.Key("sysmanager-collapsed-tabs")
	kit.OverflowMenu(c, items, &s.collapsedOpen, kit.OverflowMenuOptions{
		Label: m.SysManagerMoreTabs(),
		Title: m.SysManagerCollapsedTabs(),
		Icon:  icons.Ellipsis,
	}, func(key string) {
		_ = s.ShowCollapsedTab(appsysmanager.Tab(key))
	})
}

// TabIcon maps each System Tab to the Lucide glyph the strip draws. It is the
// mapping seam the shell layout tests assert: a headless tester cannot see an
// icon's path, so the strip's decoration is checked here together with the
// tab's accessible label and tooltip.
// Docker has no Container glyph in the set, so it takes the deliberate
// archive/layers mark for stacked images and containers.
func TabIcon(tab appsysmanager.Tab) string {
	switch tab {
	case appsysmanager.TabProcesses:
		return icons.List
	case appsysmanager.TabPorts:
		return icons.Network
	case appsysmanager.TabServices:
		return icons.Settings
	case appsysmanager.TabTmux:
		return icons.Terminal
	case appsysmanager.TabDocker:
		return icons.Archive
	case appsysmanager.TabGPU:
		return icons.Cpu
	default:
		return icons.Activity
	}
}

func hostOptions(s *State) ([]string, map[string]string) {
	labels := make([]string, 0, len(s.Hosts()))
	byLabel := make(map[string]string, len(s.Hosts()))
	seen := make(map[string]int, len(s.Hosts()))
	for _, host := range s.Hosts() {
		label := s.HostLabel(host)
		seen[label]++
	}
	for _, host := range s.Hosts() {
		label := s.HostLabel(host)
		if seen[label] > 1 {
			label = label + " — " + host.Hostname
		}
		labels = append(labels, label)
		byLabel[label] = string(host.ID)
	}
	return labels, byLabel
}

// tabs renders the System Tab Strip. The strip measures its whole labeled
// form against width, trailing layout and Collapsed Tabs controls included;
// when it does not fit, every tab turns icon-only at once. At the side panel's
// narrowest width even that form scrolls, so the strip reveals the active tab
// wholly and keeps the trailing controls outside the scroll. The layout menu
// and the Collapsed Tabs menu follow the visible tabs.
func tabs(c *ui.Context, s *State, m locales.Messages, width float32) {
	visible := s.Tabs()
	items := make([]kit.IconTabItem, 0, len(visible))
	for _, tab := range visible {
		items = append(items, kit.IconTabItem{
			Key: string(tab), Label: tabLabel(m, tab), Icon: TabIcon(tab),
		})
	}
	index := s.TabIndex()
	chosen := index
	// A resize changes the room the strip has for its tabs. Reveal the active
	// tab again so one chosen at a wide window does not stay half cut after
	// the user narrows the window.
	if width > 0 {
		if s.stripWidth > 0 && width != s.stripWidth {
			s.revealTab = true
		}
		s.stripWidth = width
	}
	reveal := s.revealTab
	s.revealTab = false

	control := kit.M(c).Control.Regular
	trailingWidth := control
	if len(s.Collapsed()) > 0 {
		trailingWidth += kit.M(c).Space.Small + control
	}
	kit.IconTabs(c, items, &chosen, kit.IconTabsOptions{
		Label:         m.SysManagerSystemTabs(),
		Width:         width,
		Reveal:        reveal,
		TrailingWidth: trailingWidth,
		Trailing: func() {
			// Both controls share one trailing row: the outer strip lays out
			// one trailing child, and the layout button and Collapsed Tabs
			// trigger keep their own gap however many are shown.
			ui.Row(c).Gap(kit.M(c).Space.Small).AlignItems(ui.Center).Shrink(0).Children(func() {
				tabsMenu(c, s, m)
				collapsedTabsMenu(c, s, m)
			})
		},
	})

	if chosen != index && chosen >= 0 && chosen < len(visible) {
		s.SetTab(visible[chosen])
		c.Invalidate()
	}
}

func tabLabel(m locales.Messages, tab appsysmanager.Tab) string {
	switch tab {
	case appsysmanager.TabProcesses:
		return m.SysManagerTabProcesses()
	case appsysmanager.TabPorts:
		return m.SysManagerTabPorts()
	case appsysmanager.TabServices:
		return m.SysManagerTabServices()
	case appsysmanager.TabTmux:
		return m.SysManagerTabTmux()
	case appsysmanager.TabDocker:
		return m.SysManagerTabDocker()
	case appsysmanager.TabGPU:
		return m.SysManagerTabGPU()
	default:
		return m.SysManagerTabOverview()
	}
}

func tabContent(c *ui.Context, s *State, m locales.Messages, t *ui.Theme, width float32) {
	state := s.TabState(s.Tab())
	switch s.Tab() {
	case appsysmanager.TabOverview:
		overviewTab(c, s, state, m, t, width)
	case appsysmanager.TabProcesses:
		if state.Status != appsysmanager.StatusLive {
			statusPanel(c, s, state, m, t)
			return
		}
		listTab(c, func() {
			toolbar(c, s, m, t, m.SysManagerSearchProcesses(),
				m.SysManagerProcessesCount(len(s.DisplayProcesses())))
			rows := s.DisplayProcesses()
			if len(rows) == 0 {
				emptyState(c, emptyText(len(state.Processes), m.SysManagerNoProcesses(),
					m.SysManagerNoMatches()), t)
				return
			}
			processTable(c, s, rows, m)
			processActions(c, s, m, t)
		})
	case appsysmanager.TabPorts:
		if state.Status != appsysmanager.StatusLive {
			statusPanel(c, s, state, m, t)
			return
		}
		listTab(c, func() {
			toolbar(c, s, m, t, m.SysManagerSearchPorts(),
				m.SysManagerPortsCount(len(s.DisplayPorts())))
			rows := s.DisplayPorts()
			if len(rows) == 0 {
				emptyState(c, emptyText(len(state.Ports), m.SysManagerNoPorts(),
					m.SysManagerNoMatches()), t)
				return
			}
			portTable(c, s, rows, m)
			portActions(c, s, m, t)
		})
	case appsysmanager.TabServices:
		if state.Status != appsysmanager.StatusLive {
			statusPanel(c, s, state, m, t)
			return
		}
		listTab(c, func() {
			toolbar(c, s, m, t, m.SysManagerSearchServices(),
				m.SysManagerServicesCount(len(s.DisplayServices())))
			rows := s.DisplayServices()
			if len(rows) == 0 {
				emptyState(c, emptyText(len(state.Services), m.SysManagerNoServices(),
					m.SysManagerNoMatches()), t)
				return
			}
			serviceTable(c, s, rows, m, t)
			serviceActions(c, s, m, t)
		})
	case appsysmanager.TabTmux:
		if state.Status != appsysmanager.StatusLive {
			statusPanel(c, s, state, m, t)
			return
		}
		tmuxContent(c, s, state, m, t)
	case appsysmanager.TabDocker:
		if state.Status != appsysmanager.StatusLive {
			statusPanel(c, s, state, m, t)
			return
		}
		dockerContent(c, s, state, m, t)
	case appsysmanager.TabGPU:
		gpuContent(c, s, state, m, t)
	}
}

func tmuxContent(c *ui.Context, s *State, state appsysmanager.TabState, m locales.Messages, t *ui.Theme) {
	listTab(c, func() {
		ui.Row(c).FillWidth().Gap(8).Children(func() {
			name := s.TmuxName()
			ui.TextInput(c, &name).Placeholder(m.SysManagerNewSession()).
				Grow(1).MinWidth(120).Label(m.SysManagerNewSession())
			if name != s.TmuxName() {
				s.SetTmuxName(name)
			}
			create := kit.SecondaryButton(c, "", m.SysManagerCreate()).Key("sysmanager-tmux-create").
				Disabled(s.ActionBusy() || strings.TrimSpace(name) == "")
			if create.Clicked() {
				_ = s.CreateSession()
				c.Invalidate()
			}
		})
		toolbar(c, s, m, t, m.SysManagerSearchSessions(),
			m.SysManagerSessionsCount(len(s.DisplaySessions())))
		rows := s.DisplaySessions()
		if len(rows) == 0 {
			emptyState(c, emptyText(len(state.Sessions), m.SysManagerNoSessions(),
				m.SysManagerNoMatches()), t)
			return
		}
		sessionTable(c, s, rows, m, t)
		sessionActions(c, s, m, t)
	})
}

func sessionTable(c *ui.Context, s *State, rows []appsysmanager.TmuxSession,
	m locales.Messages, t *ui.Theme) {
	columns := []ui.TableColumn{
		{Title: m.SysManagerSession(), ID: "session", MinWidth: 120},
		{Title: m.SysManagerWindows(), ID: "windows", Width: 100, MinWidth: 88, Align: ui.End},
		{Title: m.SysManagerAttached(), ID: "attached", Width: 96, MinWidth: 80},
		{Title: m.SysManagerCreated(), ID: "created", MinWidth: 130},
	}
	columns, full := s.fitColumns(columns, "session", "attached")
	ui.Table(c, &s.table, columns, len(rows), func(row, col int) {
		col = full(col)
		if row < 0 || row >= len(rows) {
			return
		}
		session := rows[row]
		switch col {
		case 0:
			ui.Text(c, session.Name).FontSize(kit.M(c).Font.Footnote).SingleLine()
		case 1:
			ui.Text(c, strconv.Itoa(session.Windows)).FontSize(kit.M(c).Font.Footnote)
		case 2:
			attached := m.SysManagerUnknown()
			if session.Attached {
				attached = m.SysManagerYes()
			}
			text := ui.Text(c, attached).FontSize(kit.M(c).Font.Footnote)
			if s.selected != row {
				text.TextColor(t.TextMuted)
			}
		case 3:
			text := ui.Text(c, sessionCreatedText(session.Created)).
				FontSize(kit.M(c).Font.Footnote).SingleLine()
			if s.selected != row {
				text.TextColor(t.TextMuted)
			}
		}
	}).Grow(1).MinHeight(0).FillWidth()
}

func sessionCreatedText(created int64) string {
	if created <= 0 {
		return "—"
	}
	return time.Unix(created, 0).UTC().Format("2006-01-02 15:04")
}

func sessionActions(c *ui.Context, s *State, m locales.Messages, t *ui.Theme) {
	session, ok := s.SelectedSession()
	if !ok {
		actionBar(c, func() {
			ui.Text(c, m.SysManagerSessionHint()).FontSize(kit.M(c).Font.Caption).TextColor(t.TextMuted)
		})
		return
	}
	actionBar(c, func() {
		ui.Text(c, m.SysManagerSelectedSession(session.Name, session.Windows)).
			FontSize(kit.M(c).Font.Caption).TextColor(t.TextMuted).Grow(1).SingleLine()
		if kit.SecondaryButton(c, "", m.SysManagerAttach()).Key("sysmanager-tmux-attach").Clicked() {
			_ = s.AttachSession(session.Name)
			c.Invalidate()
		}
		if kit.SecondaryButton(c, "", m.SysManagerKill()).Key("sysmanager-tmux-kill").Clicked() {
			request(s, s.RequestTmuxKill(session.Name))
			c.Invalidate()
		}
	})
}

func dockerContent(c *ui.Context, s *State, state appsysmanager.TabState,
	m locales.Messages, t *ui.Theme) {
	view := s.DockerView()
	chosen := view
	ui.Row(c).FillWidth().Children(func() {
		kit.Segmented(c, kit.SegmentedText, &chosen, m.SysManagerDockerView(), []kit.Segment{
			{Label: m.SysManagerContainers()}, {Label: m.SysManagerImages()},
		})
	})
	if chosen != view {
		s.SetDockerView(chosen)
		c.Invalidate()
		view = chosen
	}
	if view == 1 {
		dockerImages(c, s, state, m, t)
		return
	}
	dockerContainers(c, s, state, m, t)
}

func dockerContainers(c *ui.Context, s *State, state appsysmanager.TabState,
	m locales.Messages, t *ui.Theme) {
	listTab(c, func() {
		toolbar(c, s, m, t, m.SysManagerSearchContainers(),
			m.SysManagerContainersCount(len(s.DisplayContainers())))
		rows := s.DisplayContainers()
		if len(rows) == 0 {
			emptyState(c, emptyText(len(state.Containers), m.SysManagerNoContainers(),
				m.SysManagerNoMatches()), t)
			return
		}
		columns := []ui.TableColumn{
			{Title: m.SysManagerContainer(), ID: "container", MinWidth: 90},
			{Title: m.SysManagerImage(), ID: "image", MinWidth: 90},
			{Title: m.SysManagerState(), ID: "state", Width: 68, MinWidth: 58},
			{Title: m.SysManagerStatus(), ID: "status", MinWidth: 100},
			{Title: m.SysManagerPorts(), ID: "ports", MinWidth: 70},
		}
		columns, full := s.fitColumns(columns, "container", "state")
		ui.Table(c, &s.table, columns, len(rows), func(row, col int) {
			col = full(col)
			if row < 0 || row >= len(rows) {
				return
			}
			container := rows[row]
			switch col {
			case 0:
				ui.Text(c, containerDisplayName(container)).FontSize(kit.M(c).Font.Footnote).SingleLine()
			case 1:
				ui.Text(c, container.Image).FontSize(kit.M(c).Font.Footnote).SingleLine()
			case 2:
				text := ui.Text(c, container.State).FontSize(kit.M(c).Font.Footnote).SingleLine()
				if s.selected != row {
					switch strings.ToLower(container.State) {
					case "running":
						color := t.Text
						text.TextColor(color)
					case "exited", "dead":
						text.TextColor(t.TextMuted)
					}
				}
			case 3:
				text := ui.Text(c, container.Status).FontSize(kit.M(c).Font.Footnote).SingleLine()
				if s.selected != row {
					text.TextColor(t.TextMuted)
				}
			case 4:
				ui.Text(c, blank(container.Ports, m)).FontSize(kit.M(c).Font.Footnote).SingleLine()
			}
		}).Grow(1).MinHeight(0).FillWidth()
		dockerContainerActions(c, s, m, t)
	})
}

func dockerContainerActions(c *ui.Context, s *State, m locales.Messages, t *ui.Theme) {
	container, ok := s.SelectedContainer()
	if !ok {
		actionBar(c, func() {
			ui.Text(c, m.SysManagerContainerHint()).FontSize(kit.M(c).Font.Caption).TextColor(t.TextMuted)
		})
		documentPanel(c, s, m, t)
		return
	}
	running := strings.EqualFold(container.State, "running")
	actionBar(c, func() {
		ui.Text(c, m.SysManagerSelectedContainer(containerDisplayName(container),
			container.Image)).FontSize(kit.M(c).Font.Caption).TextColor(t.TextMuted).Grow(1).SingleLine()
		if kit.SecondaryButton(c, "", m.SysManagerStart()).Key("sysmanager-docker-start").
			Disabled(s.ActionBusy() || running).Clicked() {
			request(s, s.RequestDockerAction(container, appsysmanager.DockerStart))
			c.Invalidate()
		}
		if kit.SecondaryButton(c, "", m.SysManagerStop()).Key("sysmanager-docker-stop").
			Disabled(s.ActionBusy() || !running).Clicked() {
			request(s, s.RequestDockerAction(container, appsysmanager.DockerStop))
			c.Invalidate()
		}
		if kit.SecondaryButton(c, "", m.SysManagerRemove()).Key("sysmanager-docker-remove").
			Disabled(s.ActionBusy()).Clicked() {
			request(s, s.RequestDockerAction(container, appsysmanager.DockerRemove))
			c.Invalidate()
		}
		if kit.SecondaryButton(c, "", m.SysManagerInspect()).Key("sysmanager-docker-inspect").
			Clicked() {
			request(s, s.InspectContainer(container))
			c.Invalidate()
		}
		if kit.SecondaryButton(c, "", m.SysManagerLogs()).Key("sysmanager-docker-logs").
			Clicked() {
			request(s, s.ContainerLogs(container))
			c.Invalidate()
		}
	})
	documentPanel(c, s, m, t)
}

func dockerImages(c *ui.Context, s *State, state appsysmanager.TabState,
	m locales.Messages, t *ui.Theme) {
	listTab(c, func() {
		toolbar(c, s, m, t, m.SysManagerSearchImages(),
			m.SysManagerImagesCount(len(s.DisplayImages())))
		rows := s.DisplayImages()
		if len(rows) == 0 {
			emptyState(c, emptyText(len(state.Images), m.SysManagerNoImages(),
				m.SysManagerNoMatches()), t)
			return
		}
		columns := []ui.TableColumn{
			{Title: m.SysManagerImage(), ID: "image", MinWidth: 120},
			{Title: m.SysManagerSize(), ID: "size", Width: 80, MinWidth: 64, Align: ui.End},
			{Title: m.SysManagerCreated(), ID: "created", MinWidth: 120},
			{Title: m.SysManagerID(), ID: "id", MinWidth: 80},
		}
		columns, full := s.fitColumns(columns, "image", "size")
		ui.Table(c, &s.table, columns, len(rows), func(row, col int) {
			col = full(col)
			if row < 0 || row >= len(rows) {
				return
			}
			image := rows[row]
			switch col {
			case 0:
				ui.Text(c, image.Name()).FontSize(kit.M(c).Font.Footnote).SingleLine()
			case 1:
				ui.Text(c, blank(image.Size, m)).FontSize(kit.M(c).Font.Footnote)
			case 2:
				text := ui.Text(c, imageCreatedText(image.CreatedAt, m)).
					FontSize(kit.M(c).Font.Footnote).SingleLine()
				if s.selected != row {
					text.TextColor(t.TextMuted)
				}
			case 3:
				ui.Text(c, shortID(image.ID)).FontSize(kit.M(c).Font.Footnote).SingleLine()
			}
		}).Grow(1).MinHeight(0).FillWidth()
		actionBar(c, func() {
			if image, ok := s.SelectedImage(); ok {
				ui.Text(c, m.SysManagerSelectedImage(image.Name(), blank(image.Size, m))).
					FontSize(kit.M(c).Font.Caption).TextColor(t.TextMuted).SingleLine()
				return
			}
			ui.Text(c, m.SysManagerImageHint()).FontSize(kit.M(c).Font.Caption).TextColor(t.TextMuted)
		})
	})
}

// imageCreatedText trims Docker's Go-style zone suffix ("+0000 UTC") from an
// image creation stamp: the table holds the wall clock the daemon reports,
// and the zone it prints is always the daemon's own.
func imageCreatedText(created string, m locales.Messages) string {
	trimmed := strings.TrimSpace(created)
	if trimmed == "" {
		return m.SysManagerUnknown()
	}
	for _, suffix := range []string{" +0000 UTC", " +0000", " UTC"} {
		if shortened, ok := strings.CutSuffix(trimmed, suffix); ok {
			trimmed = shortened
			break
		}
	}
	return trimmed
}

func shortID(id string) string {
	trimmed := strings.TrimPrefix(strings.TrimSpace(id), "sha256:")
	if len(trimmed) > 12 {
		return trimmed[:12]
	}
	return trimmed
}

func documentPanel(c *ui.Context, s *State, m locales.Messages, t *ui.Theme) {
	document, open := s.Document()
	if !open {
		return
	}
	title := m.SysManagerDocumentInspect(shortID(document.ContainerID))
	if document.Kind == appsysmanager.DocumentLogs {
		title = m.SysManagerDocumentLogs(shortID(document.ContainerID))
	}
	ui.Column(c).FillWidth().Height(150).Padding(8).Gap(4).
		Background(t.Surface).Radius(t.Radius).Children(func() {
		ui.Row(c).FillWidth().Gap(6).Children(func() {
			ui.Text(c, title).FontSize(kit.M(c).Font.Footnote).Bold().SingleLine()
			ui.Column(c).Grow(1)
			if kit.SecondaryButton(c, "", m.SysManagerClose()).Key("sysmanager-document-close").Clicked() {
				s.CloseDocument()
				c.Invalidate()
			}
		})
		switch {
		case s.DocumentBusy():
			ui.Text(c, m.SysManagerDocumentLoading()).FontSize(kit.M(c).Font.Caption).TextColor(t.TextMuted)
		case s.DocumentError() != nil:
			ui.Text(c, m.SysManagerDocumentFailed()).FontSize(kit.M(c).Font.Caption).TextColor(uitheme.DangerText(t))
			ui.Text(c, s.DocumentError().Error()).FontSize(kit.M(c).Font.Caption).TextColor(t.TextMuted).SingleLine()
		default:
			if document.Truncated {
				ui.Text(c, m.SysManagerDocumentTruncated()).
					FontSize(kit.M(c).Font.Caption2).TextColor(t.TextMuted)
			}
			ui.Scroll(c).Grow(1).MinHeight(0).FillWidth().Children(func() {
				text := document.Text
				if strings.TrimSpace(text) == "" {
					text = m.SysManagerUnknown()
				}
				ui.Text(c, text).FontSize(kit.M(c).Font.Caption).Selectable()
			})
		}
	})
}

func gpuContent(c *ui.Context, s *State, state appsysmanager.TabState,
	m locales.Messages, t *ui.Theme) {
	if state.Status != appsysmanager.StatusLive {
		statusPanel(c, s, state, m, t)
		return
	}
	ui.Scroll(c).Grow(1).MinHeight(0).FillWidth().Children(func() {
		if len(state.GPU.Devices) == 0 {
			emptyState(c, m.SysManagerGPUEmpty(), t)
			return
		}
		ui.Row(c).FillWidth().Gap(8).Children(func() {
			ui.Text(c, m.SysManagerGPUCount(len(state.GPU.Devices))).
				FontSize(kit.M(c).Font.Footnote).TextColor(t.TextMuted)
			if state.GPU.DriverVersion != "" {
				ui.Text(c, m.SysManagerGPUDriver()+" "+state.GPU.DriverVersion).
					FontSize(kit.M(c).Font.Footnote).TextColor(t.TextMuted).NoWrap()
			}
			ui.Column(c).Grow(1)
		})
		for _, device := range state.GPU.Devices {
			gpuCard(c, device, m, t)
		}
	})
}

func gpuCard(c *ui.Context, device appsysmanager.GPUDevice, m locales.Messages, t *ui.Theme) {
	ui.Column(c).FillWidth().Padding(10).Gap(4).
		Background(t.Surface).Radius(t.Radius).Children(func() {
		ui.Row(c).FillWidth().Gap(8).Children(func() {
			ui.Text(c, m.SysManagerGPUDevice(device.Index, device.Name)).
				FontSize(kit.M(c).Font.Callout).Bold().Grow(1).SingleLine()
			ui.Text(c, device.Vendor).FontSize(kit.M(c).Font.Caption).TextColor(t.TextMuted).NoWrap()
		})
		ui.Row(c).FillWidth().Gap(12).Children(func() {
			ui.Column(c).Grow(1).Gap(2).Children(func() {
				ui.Row(c).FillWidth().Children(func() {
					ui.Text(c, m.SysManagerGPUUtilization()).
						FontSize(kit.M(c).Font.Caption).TextColor(t.TextMuted)
					ui.Column(c).Grow(1)
					ui.Text(c, percentText(device.UtilizationPercent)).FontSize(kit.M(c).Font.Caption)
				})
				if ratio := ratio(device.UtilizationPercent, 100); ratio >= 0 {
					ui.Progress(c, ratio).Grow(1)
				}
			})
			ui.Column(c).Grow(1).Gap(2).Children(func() {
				ui.Row(c).FillWidth().Children(func() {
					ui.Text(c, m.SysManagerGPUMemory()).
						FontSize(kit.M(c).Font.Caption).TextColor(t.TextMuted)
					ui.Column(c).Grow(1)
					ui.Text(c, gpuMemoryText(device, m)).FontSize(kit.M(c).Font.Caption).NoWrap()
				})
				if ratio := ratio(device.MemoryUsedMB, device.MemoryTotalMB); ratio >= 0 {
					ui.Progress(c, ratio).Grow(1)
				}
			})
		})
		ui.Row(c).FillWidth().Gap(14).Children(func() {
			gpuValue(c, m.SysManagerGPUTemperature(), temperatureText(device.TemperatureC), t)
			gpuValue(c, m.SysManagerGPUPower(), gpuPowerText(device), t)
			gpuValue(c, m.SysManagerGPUFan(), gpuFanText(device), t)
			if device.DriverVersion != "" {
				gpuValue(c, m.SysManagerGPUDriver(), device.DriverVersion, t)
			}
		})
	})
}

func gpuValue(c *ui.Context, label, value string, t *ui.Theme) {
	ui.Row(c).Gap(4).Children(func() {
		ui.Text(c, label).FontSize(kit.M(c).Font.Caption).TextColor(t.TextMuted).NoWrap()
		ui.Text(c, value).FontSize(kit.M(c).Font.Caption).NoWrap()
	})
}

func gpuMemoryText(device appsysmanager.GPUDevice, m locales.Messages) string {
	if math.IsNaN(device.MemoryUsedMB) || math.IsNaN(device.MemoryTotalMB) ||
		device.MemoryTotalMB <= 0 {
		return m.SysManagerUnknown()
	}
	return usedText(device.MemoryUsedMB, device.MemoryTotalMB, 1024, m)
}

func temperatureText(value float64) string {
	if math.IsNaN(value) {
		return "—"
	}
	return fmt.Sprintf("%.0f °C", value)
}

func gpuPowerText(device appsysmanager.GPUDevice) string {
	switch {
	case math.IsNaN(device.PowerDrawW):
		return "—"
	case math.IsNaN(device.PowerLimitW) || device.PowerLimitW <= 0:
		return fmt.Sprintf("%.0f W", device.PowerDrawW)
	default:
		return fmt.Sprintf("%.0f / %.0f W", device.PowerDrawW, device.PowerLimitW)
	}
}

func gpuFanText(device appsysmanager.GPUDevice) string {
	if math.IsNaN(device.FanPercent) {
		return "—"
	}
	return fmt.Sprintf("%.0f%%", device.FanPercent)
}

func toolbar(c *ui.Context, s *State, m locales.Messages, t *ui.Theme,
	placeholder, summary string) {
	ui.Row(c).FillWidth().Gap(8).Wrap().AlignItems(ui.Center).Children(func() {
		query := s.search
		ui.SearchField(c, &query).Placeholder(placeholder).Label(placeholder).Grow(1).MinWidth(120)
		if query != s.search {
			s.SetSearch(query)
			c.Invalidate()
		}
		filterControls(c, s, m)
		ui.Text(c, summary).FontSize(kit.M(c).Font.Caption).TextColor(t.TextMuted).NoWrap()
		// The content's right edge sits under the window edge in a narrow
		// window; the reserve keeps the last summary character whole.
		ui.Column(c).Width(12)
	})
}

// filterControls draws the active list tab's narrowing controls beside its
// search: the running-only checkbox for Processes and the protocol or state
// segments for Ports and Services. Every control starts on the choice that
// keeps the full list. The toolbar wraps them below the search when the
// surface is too narrow to hold the row.
func filterControls(c *ui.Context, s *State, m locales.Messages) {
	switch s.Tab() {
	case appsysmanager.TabProcesses:
		running := s.ProcessRunningOnly()
		c.Key("sysmanager-process-running-only")
		if ui.Checkbox(c, &running, m.SysManagerRunningOnly()).Changed() {
			s.SetProcessRunningOnly(running)
			c.Invalidate()
		}
	case appsysmanager.TabPorts:
		filters := []appsysmanager.PortFilter{
			appsysmanager.PortFilterAll,
			appsysmanager.PortFilterTCP,
			appsysmanager.PortFilterUDP,
		}
		chosen := 0
		for i, filter := range filters {
			if filter == s.PortFilter() {
				chosen = i
			}
		}
		segments := []kit.Segment{
			{Key: "sysmanager-ports-all", Label: m.SysManagerFilterAll()},
			{Key: "sysmanager-ports-tcp", Label: m.SysManagerFilterTCP()},
			{Key: "sysmanager-ports-udp", Label: m.SysManagerFilterUDP()},
		}
		if kit.Segmented(c, kit.SegmentedTrack, &chosen, m.SysManagerProtocol(), segments) {
			s.SetPortFilter(filters[chosen])
			c.Invalidate()
		}
	case appsysmanager.TabServices:
		filters := []appsysmanager.ServiceFilter{
			appsysmanager.ServiceFilterAll,
			appsysmanager.ServiceFilterRunning,
			appsysmanager.ServiceFilterFailed,
			appsysmanager.ServiceFilterInactive,
		}
		chosen := 0
		for i, filter := range filters {
			if filter == s.ServiceFilter() {
				chosen = i
			}
		}
		segments := []kit.Segment{
			{Key: "sysmanager-services-all", Label: m.SysManagerFilterAll()},
			{Key: "sysmanager-services-running", Label: m.SysManagerFilterRunning()},
			{Key: "sysmanager-services-failed", Label: m.SysManagerFilterFailed()},
			{Key: "sysmanager-services-inactive", Label: m.SysManagerFilterInactive()},
		}
		if kit.Segmented(c, kit.SegmentedTrack, &chosen, m.SysManagerState(), segments) {
			s.SetServiceFilter(filters[chosen])
			c.Invalidate()
		}
	}
}

func emptyText(total int, none, noMatches string) string {
	if total == 0 {
		return none
	}
	return noMatches
}

func emptyState(c *ui.Context, message string, t *ui.Theme) {
	ui.Column(c).Grow(1).MinHeight(0).FillWidth().AlignItems(ui.Center).Justify(ui.Center).Children(func() {
		ui.Text(c, message).FontSize(kit.M(c).Font.Footnote).TextColor(t.TextMuted)
	})
}

func statusPanel(c *ui.Context, s *State, state appsysmanager.TabState, m locales.Messages, t *ui.Theme) {
	text, color := statusText(m, state, t)
	ui.Column(c).Grow(1).MinHeight(0).FillWidth().AlignItems(ui.Center).Justify(ui.Center).Gap(6).Children(func() {
		ui.Text(c, text).FontSize(kit.M(c).Font.Body).Bold().TextColor(color)
		if reason := reasonText(m, state.Reason); reason != "" {
			ui.Text(c, reason).FontSize(kit.M(c).Font.Footnote).TextColor(t.TextMuted)
		}
		if state.Status == appsysmanager.StatusIdle ||
			state.Status == appsysmanager.StatusConnecting ||
			!statusRetryable(state) {
			return
		}
		c.Key("sysmanager-retry")
		if kit.SecondaryButton(c, icons.RefreshCw, m.SysManagerRetry()).Clicked() {
			s.Refresh()
			c.Invalidate()
		}
	})
}

func processTable(c *ui.Context, s *State, rows []appsysmanager.Process, m locales.Messages) {
	columns := []ui.TableColumn{
		{Title: m.SysManagerPid(), ID: "pid", Width: 56, MinWidth: 44, Align: ui.End, Sortable: true},
		{Title: m.SysManagerPpid(), ID: "ppid", Width: 56, MinWidth: 44, Align: ui.End},
		{Title: m.SysManagerUser(), ID: "user", Width: 76, MinWidth: 56, Sortable: true},
		{Title: m.SysManagerCPU(), ID: "cpu", Width: 76, MinWidth: 64, Align: ui.End, Sortable: true},
		{Title: m.SysManagerMemory(), ID: "mem", Width: 56, MinWidth: 48, Align: ui.End, Sortable: true},
		{Title: m.SysManagerCommand(), ID: "command", MinWidth: 120, Sortable: true},
	}
	columns, full := s.fitColumns(columns, "pid", "cpu", "mem", "command")
	ui.Table(c, &s.table, columns, len(rows), func(row, col int) {
		col = full(col)
		if row < 0 || row >= len(rows) {
			return
		}
		process := rows[row]
		switch col {
		case 0:
			ui.Text(c, strconv.Itoa(process.PID)).FontSize(kit.M(c).Font.Footnote)
		case 1:
			ui.Text(c, strconv.Itoa(process.PPID)).FontSize(kit.M(c).Font.Footnote)
		case 2:
			ui.Text(c, process.User).FontSize(kit.M(c).Font.Footnote).SingleLine()
		case 3:
			ui.Text(c, percent(process.CPUPercent)).FontSize(kit.M(c).Font.Footnote)
		case 4:
			ui.Text(c, percent(process.MemPercent)).FontSize(kit.M(c).Font.Footnote)
		case 5:
			ui.Text(c, process.Command).FontSize(kit.M(c).Font.Footnote).SingleLine()
		}
	}).Grow(1).MinHeight(0).FillWidth()
}

func portTable(c *ui.Context, s *State, rows []appsysmanager.Port, m locales.Messages) {
	columns := []ui.TableColumn{
		{Title: m.SysManagerPort(), ID: "port", Width: 58, MinWidth: 46, Align: ui.End},
		{Title: m.SysManagerProtocol(), ID: "protocol", Width: 92, MinWidth: 72},
		{Title: m.SysManagerAddress(), ID: "address", MinWidth: 80},
		{Title: m.SysManagerPid(), ID: "pid", Width: 58, MinWidth: 46, Align: ui.End},
		{Title: m.SysManagerProcess(), ID: "process", MinWidth: 80},
	}
	columns, full := s.fitColumns(columns, "port", "address", "process")
	ui.Table(c, &s.table, columns, len(rows), func(row, col int) {
		col = full(col)
		if row < 0 || row >= len(rows) {
			return
		}
		port := rows[row]
		switch col {
		case 0:
			ui.Text(c, strconv.Itoa(port.Port)).FontSize(kit.M(c).Font.Footnote)
		case 1:
			ui.Text(c, port.Protocol).FontSize(kit.M(c).Font.Footnote)
		case 2:
			ui.Text(c, port.Address).FontSize(kit.M(c).Font.Footnote).SingleLine()
		case 3:
			ui.Text(c, pidText(port.PID)).FontSize(kit.M(c).Font.Footnote)
		case 4:
			ui.Text(c, port.ProcessName).FontSize(kit.M(c).Font.Footnote).SingleLine()
		}
	}).Grow(1).MinHeight(0).FillWidth()
}

func serviceTable(c *ui.Context, s *State, rows []appsysmanager.Unit, m locales.Messages, t *ui.Theme) {
	columns := []ui.TableColumn{
		{Title: m.SysManagerUnit(), ID: "unit", MinWidth: 140},
		{Title: m.SysManagerScope(), ID: "scope", Width: 64, MinWidth: 54},
		{Title: m.SysManagerActive(), ID: "active", Width: 70, MinWidth: 58},
		{Title: m.SysManagerDescription(), ID: "description", MinWidth: 100},
	}
	columns, full := s.fitColumns(columns, "unit", "active")
	ui.Table(c, &s.table, columns, len(rows), func(row, col int) {
		col = full(col)
		if row < 0 || row >= len(rows) {
			return
		}
		unit := rows[row]
		switch col {
		case 0:
			ui.Text(c, unit.Name).FontSize(kit.M(c).Font.Footnote).SingleLine()
		case 1:
			ui.Text(c, scopeText(m, unit.Scope)).FontSize(kit.M(c).Font.Footnote)
		case 2:
			// The selected row inherits the accent's high-contrast foreground,
			// so a state color from the normal background would be illegible.
			text := ui.Text(c, unit.ActiveState).FontSize(kit.M(c).Font.Footnote).SingleLine()
			if s.selected != row {
				color := t.TextMuted
				switch unit.ActiveState {
				case "failed":
					color = uitheme.DangerText(t)
				case "active":
					color = t.Text
				}
				text.TextColor(color)
			}
		case 3:
			text := ui.Text(c, unit.Description).FontSize(kit.M(c).Font.Footnote).SingleLine()
			if s.selected != row {
				text.TextColor(t.TextMuted)
			}
		}
	}).Grow(1).MinHeight(0).FillWidth()
}

func processActions(c *ui.Context, s *State, m locales.Messages, t *ui.Theme) {
	process, ok := s.SelectedProcess()
	if !ok {
		actionBar(c, func() {
			ui.Text(c, m.SysManagerProcessHint()).FontSize(kit.M(c).Font.Caption).TextColor(t.TextMuted)
		})
		return
	}
	actionBar(c, func() {
		ui.Text(c, m.SysManagerSelectedProcess(process.PID, process.Command)).
			FontSize(kit.M(c).Font.Caption).TextColor(t.TextMuted).Grow(1).SingleLine()
		stopped := strings.Contains(strings.ToUpper(process.Stat), "T")
		if !stopped {
			if kit.SecondaryButton(c, "", m.SysManagerStop()).Key("sysmanager-process-stop").Clicked() {
				request(s, s.RequestSignal(process.PID, "STOP"))
				c.Invalidate()
			}
		} else {
			if kit.SecondaryButton(c, "", m.SysManagerContinue()).Key("sysmanager-process-continue").Clicked() {
				request(s, s.RequestSignal(process.PID, "CONT"))
				c.Invalidate()
			}
		}
		if kit.SecondaryButton(c, "", m.SysManagerTerminate()).Key("sysmanager-process-terminate").Clicked() {
			request(s, s.RequestSignal(process.PID, "TERM"))
			c.Invalidate()
		}
		if kit.SecondaryButton(c, "", m.SysManagerKill()).Key("sysmanager-process-kill").Clicked() {
			request(s, s.RequestSignal(process.PID, "KILL"))
			c.Invalidate()
		}
	})
}

func portActions(c *ui.Context, s *State, m locales.Messages, t *ui.Theme) {
	port, ok := s.SelectedPort()
	if !ok {
		actionBar(c, func() {
			ui.Text(c, m.SysManagerPortHint()).FontSize(kit.M(c).Font.Caption).TextColor(t.TextMuted)
		})
		return
	}
	actionBar(c, func() {
		ui.Text(c, m.SysManagerSelectedPort(port.Protocol, port.Address,
			strconv.Itoa(port.Port), strconv.Itoa(port.PID))).
			FontSize(kit.M(c).Font.Caption).TextColor(t.TextMuted).Grow(1).SingleLine()
		if port.PID > 0 {
			if kit.SecondaryButton(c, "", m.SysManagerTerminate()).Key("sysmanager-port-terminate").Clicked() {
				request(s, s.RequestPortTerminate(port))
				c.Invalidate()
			}
		} else {
			ui.Text(c, m.SysManagerPortOwnerUnknown()).FontSize(kit.M(c).Font.Caption).TextColor(t.TextMuted)
		}
	})
}

func serviceActions(c *ui.Context, s *State, m locales.Messages, t *ui.Theme) {
	unit, ok := s.SelectedService()
	if !ok {
		actionBar(c, func() {
			ui.Text(c, m.SysManagerServiceHint()).FontSize(kit.M(c).Font.Caption).TextColor(t.TextMuted)
		})
		return
	}
	scope := scopeText(m, unit.Scope)
	actionBar(c, func() {
		ui.Text(c, m.SysManagerSelectedService(unit.Name, scope)).
			FontSize(kit.M(c).Font.Caption).TextColor(t.TextMuted).Grow(1).SingleLine()
		for _, action := range unit.Actions() {
			switch action {
			case appsysmanager.ServiceStart:
				if kit.SecondaryButton(c, "", m.SysManagerStart()).Key("sysmanager-service-start").Clicked() {
					request(s, s.RequestServiceAction(unit.Name, appsysmanager.ServiceStart,
						appsysmanager.Scope(unit.Scope)))
					c.Invalidate()
				}
			case appsysmanager.ServiceStop:
				if kit.SecondaryButton(c, "", m.SysManagerStop()).Key("sysmanager-service-stop").Clicked() {
					request(s, s.RequestServiceAction(unit.Name, appsysmanager.ServiceStop,
						appsysmanager.Scope(unit.Scope)))
					c.Invalidate()
				}
			case appsysmanager.ServiceRestart:
				if kit.SecondaryButton(c, "", m.SysManagerRestart()).Key("sysmanager-service-restart").Clicked() {
					request(s, s.RequestServiceAction(unit.Name, appsysmanager.ServiceRestart,
						appsysmanager.Scope(unit.Scope)))
					c.Invalidate()
				}
			}
		}
	})
}

func request(s *State, err error) {
	if err != nil {
		s.problem = ProblemActionData
	}
}

// overviewTab chooses what the Overview draws from its five states. A
// successful or cached Stats Sample keeps the cards visible; a first
// collection shows a spinner; an empty answer, a blocking failure, and an
// unavailable Host show their typed reason instead of inventing a zero card.
func overviewTab(c *ui.Context, s *State, state appsysmanager.TabState,
	m locales.Messages, t *ui.Theme, width float32) {
	if overviewHasSample(state) {
		overview(c, s, state, m, t, width)
		return
	}
	switch state.Status {
	case appsysmanager.StatusIdle, appsysmanager.StatusConnecting:
		overviewLoading(c, m, t)
	default:
		overviewBlocking(c, s, state, m, t)
	}
}

// overviewHasSample reports whether a Stats Sample is behind the Overview.
// Samples from the shared collector always carry SampledAt; the Overview
// fields are the fallback for a surface driven by an older snapshot.
func overviewHasSample(state appsysmanager.TabState) bool {
	if !state.Stats.SampledAt.IsZero() {
		return true
	}
	return state.Status == appsysmanager.StatusLive &&
		(state.Overview.Hostname != "" || state.Overview.Cores > 0)
}

// overviewRetryable reports whether a manual retry can change this state.
// A missing command, an unsupported operating system, and a single-channel or
// exec-less Host will answer the same way until the Host changes, so they show
// their reason without a Retry. A transient failure, an empty answer, and a
// refused sign-in may be fixed and retried.
func overviewRetryable(status appsysmanager.Status) bool {
	switch status {
	case appsysmanager.StatusError, appsysmanager.StatusEmpty, appsysmanager.StatusLogin:
		return true
	default:
		return false
	}
}

// statusRetryable reports whether Retry can change a non-Overview tab's
// panel. A missing command, an unsupported system, a Host without exec, and a
// permission refusal answer the same way until the Host changes, so they show
// their reason without a Retry. A stopped Docker daemon may be started, and a
// transient failure or empty answer may be fixed and retried.
func statusRetryable(state appsysmanager.TabState) bool {
	switch state.Reason {
	case appsysmanager.ReasonMissingCommand, appsysmanager.ReasonMissingProcesses,
		appsysmanager.ReasonMissingPorts, appsysmanager.ReasonMissingSystemctl,
		appsysmanager.ReasonMissingTmux, appsysmanager.ReasonMissingDocker,
		appsysmanager.ReasonNoExec, appsysmanager.ReasonOS,
		appsysmanager.ReasonUnsupported, appsysmanager.ReasonPermission:
		return false
	case appsysmanager.ReasonDockerDaemon:
		return true
	}
	return overviewRetryable(state.Status)
}

func overviewLoading(c *ui.Context, m locales.Messages, t *ui.Theme) {
	ui.Column(c).Grow(1).MinHeight(0).FillWidth().AlignItems(ui.Center).
		Justify(ui.Center).Gap(8).Children(func() {
		ui.Spinner(c).Size(18, 18).Label(m.SysManagerOverviewLoading())
		ui.Text(c, m.SysManagerOverviewLoading()).FontSize(kit.M(c).Font.Footnote).TextColor(t.TextMuted)
	})
}

// overviewBlocking shows a reason when no card can be drawn. Retry appears
// only for states a retry can change.
func overviewBlocking(c *ui.Context, s *State, state appsysmanager.TabState,
	m locales.Messages, t *ui.Theme) {
	text, color := statusText(m, state, t)
	ui.Column(c).Grow(1).MinHeight(0).FillWidth().AlignItems(ui.Center).
		Justify(ui.Center).Gap(6).Children(func() {
		ui.Text(c, text).FontSize(kit.M(c).Font.Body).Bold().TextColor(color)
		if reason := reasonText(m, state.Reason); reason != "" {
			ui.Text(c, reason).FontSize(kit.M(c).Font.Footnote).TextColor(t.TextMuted)
		}
		if overviewRetryable(state.Status) {
			overviewRetry(c, s, m)
		}
	})
}

func overviewRetry(c *ui.Context, s *State, m locales.Messages) {
	c.Key("sysmanager-overview-retry")
	if kit.SecondaryButton(c, icons.RefreshCw, m.SysManagerRetry()).Clicked() {
		s.Refresh()
		c.Invalidate()
	}
}

func overview(c *ui.Context, s *State, state appsysmanager.TabState,
	m locales.Messages, t *ui.Theme, width float32) {
	data := state.Overview
	ui.Scroll(c).Grow(1).MinHeight(0).FillWidth().Children(func() {
		ui.Column(c).FillWidth().Gap(10).Children(func() {
			if state.Status != appsysmanager.StatusLive {
				overviewInlineNotice(c, s, state, m, t)
			}
			overviewCards(c, s, data, m, t, width)
			overviewDiskIOLine(c, data, m, t)
			overviewPills(c, state, m, t)
			overviewCores(c, data, m, t)
			overviewDisks(c, data, m, t)
			overviewInterfaces(c, data, m, t)
			overviewProcesses(c, data, m, t)
		})
	})
}

// overviewCards lays the four metric cards out two by two at 320 DIP and
// wider, and in one column below it. Both surfaces use the same information
// architecture; extra width goes to the cards, not to another layout.
func overviewCards(c *ui.Context, s *State, data appsysmanager.Overview,
	m locales.Messages, t *ui.Theme, width float32) {
	cards := []func(grow bool){
		func(grow bool) {
			overviewMetricCard(c, m, t, grow, overviewCardData{
				Label: m.SysManagerOverviewCPU(), Icon: icons.Cpu,
				Value:  percentText(data.CPUPercent),
				Detail: coresText(data.Cores, m),
				Gauge:  data.CPUPercent, Tone: kit.ToneForPercent(data.CPUPercent),
				History: s.CPUHistory(), Max: 100,
			})
		},
		func(grow bool) {
			memory := knownPercent(data.MemUsedMB, data.MemTotalMB)
			overviewMetricCard(c, m, t, grow, overviewCardData{
				Label: m.SysManagerOverviewMemory(), Icon: icons.MemoryStick,
				Value:  percentText(memory),
				Detail: memoryDetail(data.MemUsedMB, data.MemTotalMB, m),
				Gauge:  memory, Tone: kit.ToneForPercent(memory),
				History: s.MemoryHistory(), Max: 100,
			})
		},
		func(grow bool) {
			disk := overviewDiskPercent(data)
			overviewMetricCard(c, m, t, grow, overviewCardData{
				Label: m.SysManagerOverviewDisk(), Icon: icons.HardDrive,
				Value:  percentText(disk),
				Detail: storageDetail(data.DiskUsedGB, data.DiskTotalGB, m),
				Gauge:  disk, Tone: kit.ToneForPercent(disk),
				History: s.DiskHistory(), Max: 100,
			})
		},
		func(grow bool) {
			total := overviewNetworkTotal(data)
			gauge := kit.NetworkGaugePercent(total)
			overviewMetricCard(c, m, t, grow, overviewCardData{
				Label: m.SysManagerOverviewNetwork(), Icon: icons.Network,
				Value: rateText(total),
				Detail: m.SysManagerOverviewNetworkDetail(
					rateText(data.NetworkRxBps), rateText(data.NetworkTxBps)),
				Gauge: gauge, Tone: kit.ToneForPercent(gauge),
				History: s.NetworkHistory(), Max: 0,
			})
		},
	}
	if width < overviewTwoColumnWidth {
		for _, card := range cards {
			card(false)
		}
		return
	}
	for index := 0; index < len(cards); index += 2 {
		first, second := cards[index], cards[index+1]
		ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Start).Children(func() {
			first(true)
			second(true)
		})
	}
}

// overviewTwoColumnWidth is the narrowest surface that still holds the four
// cards in two columns.
const overviewTwoColumnWidth float32 = 320

// overviewCardData is the content of one metric card, shared by all four so
// their value, detail, gauge, and sparkline rows stay aligned.
type overviewCardData struct {
	Label   string
	Icon    string
	Value   string
	Detail  string
	Gauge   float64
	Tone    kit.GaugeTone
	History []float64
	Max     float64
}

func overviewMetricCard(c *ui.Context, m locales.Messages, t *ui.Theme,
	grow bool, data overviewCardData) {
	card := ui.Column(c).MinWidth(0).Padding(10).Gap(6).
		Background(t.Surface).Radius(t.Radius)
	if grow {
		card.Grow(1)
	} else {
		card.FillWidth()
	}
	card.Label(data.Label + " card").Children(func() {
		ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
			kit.IconSized(c, data.Icon, kit.M(c).Icon.Medium)
			ui.Text(c, data.Label).FontSize(kit.M(c).Font.Callout).Bold()
		})
		ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
			kit.Gauge(c, data.Gauge, kit.GaugeOptions{
				Size: 58, Tone: data.Tone, Label: data.Label + " gauge",
			})
			ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
				ui.Text(c, data.Value).FontSize(kit.M(c).Font.LargeTitle).
					FontWeight(600).TextColor(kit.GaugeColor(t, data.Tone)).SingleLine()
				ui.Text(c, data.Detail).FontSize(kit.M(c).Font.Caption).
					TextColor(t.TextMuted).SingleLine()
			})
		})
		kit.Sparkline(c, data.History, kit.SparklineOptions{
			Height: 34, Max: data.Max, Tone: data.Tone,
			Label: data.Label + " history",
		})
	})
}

// overviewDiskIOLine is the read and write rate under the cards. When the
// Host reports neither, as macOS does, the value is "—" rather than a zero.
func overviewDiskIOLine(c *ui.Context, data appsysmanager.Overview,
	m locales.Messages, t *ui.Theme) {
	value := m.SysManagerOverviewRead() + " " + rateText(data.DiskReadBps) +
		" · " + m.SysManagerOverviewWrite() + " " + rateText(data.DiskWriteBps)
	ui.Row(c).FillWidth().Gap(8).Wrap().AlignItems(ui.Center).Padding(8, 10).
		Radius(t.Radius).Background(t.Surface).
		Label(m.SysManagerOverviewDiskIO() + " line").Children(func() {
		ui.Text(c, m.SysManagerOverviewDiskIO()).FontSize(kit.M(c).Font.Caption).
			TextColor(t.TextMuted).SingleLine()
		ui.Text(c, value).FontSize(kit.M(c).Font.Caption).TextColor(t.Text)
	})
}

// overviewPills are the six identity and health facts. The Hostname stays in
// the surface's header, not as a seventh pill.
func overviewPills(c *ui.Context, state appsysmanager.TabState,
	m locales.Messages, t *ui.Theme) {
	data := state.Overview
	ui.Row(c).FillWidth().Gap(6).Wrap().AlignItems(ui.Center).
		Label(m.SysManagerOverviewInfo() + " pills").Children(func() {
		kit.InfoPill(c, m.SysManagerOverviewLoad(), loadText(data.LoadAverage, m))
		kit.InfoPill(c, m.SysManagerOverviewUptime(), uptimeText(data.UptimeSeconds, m))
		kit.InfoPill(c, m.SysManagerOverviewSystem(), blank(data.OSName, m))
		kit.InfoPill(c, m.SysManagerOverviewKernel(), blank(data.KernelRelease, m))
		kit.InfoPill(c, m.SysManagerOverviewSwap(),
			memoryDetail(data.SwapUsedMB, data.SwapTotalMB, m))
		kit.InfoPill(c, m.SysManagerLatency(), overviewLatencyText(state, m))
	})
}

func overviewLatencyText(state appsysmanager.TabState, m locales.Messages) string {
	if state.Stats.SampledAt.IsZero() {
		return m.SysManagerUnknown()
	}
	latency := state.Stats.LatencyMS
	if !known(latency) || latency < 0 {
		return m.SysManagerUnknown()
	}
	return m.SysManagerLatencyValue(int(latency))
}

// overviewCores lists every per-core reading as C1..Cn in a two-column grid.
// The grid scrolls inside its 260-DIP cap. macOS has no per-core reading, so
// its section says "No data" rather than inventing an even split.
func overviewCores(c *ui.Context, data appsysmanager.Overview,
	m locales.Messages, t *ui.Theme) {
	ui.Column(c).FillWidth().Gap(4).Children(func() {
		kit.SectionHeader(c, kit.SectionCards, icons.Cpu,
			m.SysManagerOverviewCoresSection(), overviewLoadText(data, m))
		if len(data.CPUPerCore) == 0 {
			kit.EmptyHint(c, m.SysManagerOverviewNoData())
			return
		}
		ui.Scroll(c).FillWidth().MaxHeight(overviewCoresHeight).
			Label(m.SysManagerOverviewCoresSection() + " grid").Children(func() {
			ui.Column(c).FillWidth().Gap(6).Children(func() {
				for index := 0; index < len(data.CPUPerCore); index += 2 {
					first := data.CPUPerCore[index]
					ui.Row(c).FillWidth().Gap(12).AlignItems(ui.Start).Children(func() {
						overviewCoreCell(c, "C"+strconv.Itoa(index+1), first.Percent, t)
						if index+1 < len(data.CPUPerCore) {
							second := data.CPUPerCore[index+1]
							overviewCoreCell(c, "C"+strconv.Itoa(index+2), second.Percent, t)
						} else {
							ui.Column(c).Grow(1).MinWidth(0)
						}
					})
				}
			})
		})
	})
}

// overviewCoresHeight caps the per-core grid, after which it scrolls.
const overviewCoresHeight float32 = 260

func overviewCoreCell(c *ui.Context, label string, percent float64, t *ui.Theme) {
	ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
		ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Text(c, label).FontSize(kit.M(c).Font.Caption).TextColor(t.TextMuted).SingleLine()
			ui.Column(c).Grow(1)
			ui.Text(c, percentText(percent)).FontSize(kit.M(c).Font.Caption).SingleLine()
		})
		kit.ResourceBar(c, percent, kit.ResourceBarOptions{
			Height: 4, Label: label + " usage",
		})
	})
}

// overviewLoadText is the CPU-cores header's one-minute load as a percentage
// of the core count, clamped to 0–100, or "No data" when either is unknown.
func overviewLoadText(data appsysmanager.Overview, m locales.Messages) string {
	if len(data.CPUPerCore) == 0 || len(data.LoadAverage) == 0 ||
		data.Cores <= 0 || !known(data.LoadAverage[0]) {
		return m.SysManagerOverviewNoData()
	}
	return percentText(min(100, max(0, data.LoadAverage[0]/float64(data.Cores)*100)))
}

// overviewDisks lists every kept mount with its usage.
func overviewDisks(c *ui.Context, data appsysmanager.Overview,
	m locales.Messages, t *ui.Theme) {
	ui.Column(c).FillWidth().Gap(4).Children(func() {
		kit.SectionHeader(c, kit.SectionCards, icons.HardDrive,
			m.SysManagerOverviewDisksSection(), "")
		if len(data.Disks) == 0 {
			kit.EmptyHint(c, m.SysManagerOverviewNoDiskData())
			return
		}
		ui.Column(c).FillWidth().Gap(6).
			Label(m.SysManagerOverviewDisksSection() + " list").Children(func() {
			for _, disk := range data.Disks {
				percent, _ := finitePercent(disk.UsedGB, disk.TotalGB, disk.Percent)
				mount := blank(disk.MountPoint, m)
				ui.Column(c).FillWidth().Gap(2).Children(func() {
					ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
						ui.Text(c, mount).FontSize(kit.M(c).Font.Caption).Bold().
							Grow(1).MinWidth(0).SingleLine()
						ui.Text(c, storageDetail(disk.UsedGB, disk.TotalGB, m)).
							FontSize(kit.M(c).Font.Caption).TextColor(t.TextMuted).SingleLine()
					})
					kit.ResourceBar(c, percent, kit.ResourceBarOptions{
						Height: 4, Label: mount + " usage",
					})
				})
			}
		})
	})
}

// overviewInterfaces lists the first five interfaces with their cumulative
// counters and current rates.
func overviewInterfaces(c *ui.Context, data appsysmanager.Overview,
	m locales.Messages, t *ui.Theme) {
	ui.Column(c).FillWidth().Gap(4).Children(func() {
		kit.SectionHeader(c, kit.SectionCards, icons.Network,
			m.SysManagerOverviewNetworkSection(), "")
		if len(data.Interfaces) == 0 {
			kit.EmptyHint(c, m.SysManagerOverviewNoInterfaceData())
			return
		}
		ui.Column(c).FillWidth().Gap(6).
			Label(m.SysManagerOverviewNetworkSection() + " list").Children(func() {
			limit := min(5, len(data.Interfaces))
			for index := 0; index < limit; index++ {
				iface := data.Interfaces[index]
				ui.Column(c).FillWidth().Gap(1).Children(func() {
					ui.Text(c, blank(iface.Name, m)).FontSize(kit.M(c).Font.Caption).
						Bold().SingleLine()
					ui.Text(c, m.SysManagerOverviewRX()+" "+byteCount(float64(iface.RxBytes))+
						" · "+rateText(iface.RxBps)).FontSize(kit.M(c).Font.Caption).
						TextColor(t.TextMuted).SingleLine()
					ui.Text(c, m.SysManagerOverviewTX()+" "+byteCount(float64(iface.TxBytes))+
						" · "+rateText(iface.TxBps)).FontSize(kit.M(c).Font.Caption).
						TextColor(t.TextMuted).SingleLine()
				})
			}
		})
	})
}

// overviewProcesses lists the first five rows of the Stats Sample's own
// memory-ordered top list. It never reads the Processes System Tab's full ps
// list.
func overviewProcesses(c *ui.Context, data appsysmanager.Overview,
	m locales.Messages, t *ui.Theme) {
	ui.Column(c).FillWidth().Gap(4).Children(func() {
		kit.SectionHeader(c, kit.SectionCards, icons.ListTree,
			m.SysManagerOverviewProcessesSection(), "")
		if len(data.TopProcesses) == 0 {
			kit.EmptyHint(c, m.SysManagerOverviewNoProcessData())
			return
		}
		ui.Column(c).FillWidth().Gap(6).
			Label(m.SysManagerOverviewProcessesSection() + " list").Children(func() {
			limit := min(5, len(data.TopProcesses))
			for index := 0; index < limit; index++ {
				process := data.TopProcesses[index]
				command := blank(process.Command, m)
				ui.Column(c).FillWidth().Gap(2).Children(func() {
					ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
						ui.Text(c, command).FontSize(kit.M(c).Font.Caption).Bold().
							Grow(1).MinWidth(0).SingleLine()
						ui.Text(c, m.SysManagerPid()+" "+pidText(process.PID)).
							FontSize(kit.M(c).Font.Caption).TextColor(t.TextMuted).SingleLine()
					})
					ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
						ui.Column(c).Grow(1).MinWidth(0).Children(func() {
							kit.ResourceBar(c, process.MemPercent, kit.ResourceBarOptions{
								Height: 4, Label: command + " memory",
							})
						})
						ui.Text(c, percentText(process.MemPercent)).
							FontSize(kit.M(c).Font.Caption).TextColor(t.TextMuted).SingleLine()
					})
				})
			}
		})
	})
}

// overviewDiskPercent is the Disk card's aggregate percentage: the projected
// aggregate, or the used/total pair when an older snapshot carries only the
// capacities.
func overviewDiskPercent(data appsysmanager.Overview) float64 {
	if percent, ok := finitePercent(data.DiskUsedGB, data.DiskTotalGB, data.DiskPercent); ok {
		return percent
	}
	return data.DiskPercent
}

// finitePercent prefers a used/total pair and falls back to an explicit
// percent. It reports false when neither can form a value.
func finitePercent(used, total, percent float64) (float64, bool) {
	if value := knownPercent(used, total); known(value) {
		return value, true
	}
	if known(percent) {
		return percent, true
	}
	return math.NaN(), false
}

// overviewNetworkTotal is the Network card's primary value: RX plus TX bytes
// per second, unknown when either rate is unknown.
func overviewNetworkTotal(data appsysmanager.Overview) float64 {
	if !known(data.NetworkRxBps) || !known(data.NetworkTxBps) {
		return math.NaN()
	}
	return data.NetworkRxBps + data.NetworkTxBps
}

// overviewInlineNotice keeps cached cards visible above a failed refresh and
// offers the retry that asks the collector for a fresh sample.
func overviewInlineNotice(c *ui.Context, s *State, state appsysmanager.TabState,
	m locales.Messages, t *ui.Theme) {
	reason := reasonText(m, state.Reason)
	if reason == "" {
		reason, _ = statusText(m, state, t)
	}
	if reason == "" {
		return
	}
	kit.Notice(c, icons.TriangleAlert, uitheme.DangerText(t), reason, func() {
		if overviewRetryable(state.Status) {
			overviewRetry(c, s, m)
		}
	})
}

// memoryDetail is a memory quantity's used and total, in MB or GB. An
// unknown pair is "—", never a zero.
func memoryDetail(used, total float64, m locales.Messages) string {
	if !known(used) || !known(total) || total <= 0 {
		return m.SysManagerUnknown()
	}
	return m.SysManagerOverviewUsedOf(memoryText(used), memoryText(total))
}

func memoryText(mb float64) string {
	if !known(mb) {
		return "—"
	}
	if mb >= 1024 {
		return fmt.Sprintf("%.1f GB", mb/1024)
	}
	if mb == math.Trunc(mb) {
		return fmt.Sprintf("%.0f MB", mb)
	}
	return fmt.Sprintf("%.1f MB", mb)
}

// storageDetail is a storage quantity's used and total, in GB or TB, with
// zero or one decimal.
func storageDetail(used, total float64, m locales.Messages) string {
	if !known(used) || !known(total) || total <= 0 {
		return m.SysManagerUnknown()
	}
	return m.SysManagerOverviewUsedOf(storageText(used), storageText(total))
}

func storageText(gb float64) string {
	if !known(gb) {
		return "—"
	}
	if gb >= 1024 {
		return fmt.Sprintf("%.1f TB", gb/1024)
	}
	if gb == math.Trunc(gb) {
		return fmt.Sprintf("%.0f GB", gb)
	}
	return fmt.Sprintf("%.1f GB", gb)
}

// byteCount is one binary byte unit for counters and transfers. It is the
// single convention shared by the Network card, the interfaces, and the disk
// I/O line.
func byteCount(bytes float64) string {
	if !known(bytes) {
		return "—"
	}
	switch {
	case bytes >= 1<<40:
		return fmt.Sprintf("%.1f TiB", bytes/(1<<40))
	case bytes >= 1<<30:
		return fmt.Sprintf("%.1f GiB", bytes/(1<<30))
	case bytes >= 1<<20:
		return fmt.Sprintf("%.1f MiB", bytes/(1<<20))
	case bytes >= 1<<10:
		return fmt.Sprintf("%.1f KiB", bytes/(1<<10))
	default:
		return fmt.Sprintf("%.0f B", bytes)
	}
}

func rateText(value float64) string {
	if !known(value) {
		return "—"
	}
	return byteCount(value) + "/s"
}

func confirmation(c *ui.Context, s *State, m locales.Messages) {
	pending, ok := s.Pending()
	if !ok {
		return
	}
	title, message, confirm := confirmationText(pending, m)
	open := true
	choice := ui.AlertDialog(c, &open, title, message, m.SysManagerCancel(), confirm)
	switch {
	case choice == 1:
		s.ConfirmPending()
		c.Invalidate()
	case choice == 0 || choice == ui.AlertDismissed:
		s.CancelPending()
		c.Invalidate()
	}
}

func confirmationText(pending PendingAction, m locales.Messages) (string, string, string) {
	switch pending.Kind {
	case appsysmanager.ActionServiceAction:
		action := serviceActionText(pending.Action, m)
		return action, m.SysManagerConfirmService(action, pending.Unit, pending.Command), action
	case appsysmanager.ActionTmuxKill:
		return m.SysManagerKill(),
			m.SysManagerConfirmTmuxKill(pending.Session, pending.Command), m.SysManagerKill()
	case appsysmanager.ActionDockerStart, appsysmanager.ActionDockerStop,
		appsysmanager.ActionDockerRemove:
		action := dockerActionText(pending.DockerAction, m)
		return action, m.SysManagerConfirmDocker(action, pending.ContainerName,
			pending.Command), action
	}
	signal := pending.Signal
	title := m.SysManagerTerminate()
	switch signal {
	case "STOP":
		title = m.SysManagerStop()
	case "CONT":
		title = m.SysManagerContinue()
	case "KILL":
		title = m.SysManagerKill()
	}
	if pending.Origin == OriginPort {
		return title, m.SysManagerConfirmPort(pending.PID, pending.Address, pending.Port,
			pending.Protocol, pending.Command), title
	}
	return title, m.SysManagerConfirmSignal(signal, pending.PID, pending.Command), title
}

func dockerActionText(action appsysmanager.DockerAction, m locales.Messages) string {
	switch action {
	case appsysmanager.DockerStart:
		return m.SysManagerStart()
	case appsysmanager.DockerStop:
		return m.SysManagerStop()
	case appsysmanager.DockerRemove:
		return m.SysManagerRemove()
	default:
		return string(action)
	}
}

func serviceActionText(action appsysmanager.ServiceAction, m locales.Messages) string {
	switch action {
	case appsysmanager.ServiceStart:
		return m.SysManagerStart()
	case appsysmanager.ServiceStop:
		return m.SysManagerStop()
	case appsysmanager.ServiceRestart:
		return m.SysManagerRestart()
	default:
		return string(action)
	}
}

func statusText(m locales.Messages, state appsysmanager.TabState, t *ui.Theme) (string, ui.Color) {
	switch state.Status {
	case appsysmanager.StatusLive:
		return m.SysManagerStatusLive(), t.Text
	case appsysmanager.StatusConnecting:
		return m.SysManagerStatusConnecting(), t.TextMuted
	case appsysmanager.StatusLogin:
		return m.SysManagerStatusLogin(), uitheme.DangerText(t)
	case appsysmanager.StatusError:
		return m.SysManagerStatusError(), uitheme.DangerText(t)
	case appsysmanager.StatusEmpty:
		return m.SysManagerStatusEmpty(), t.TextMuted
	case appsysmanager.StatusUnsupported:
		return m.SysManagerStatusUnsupported(), t.TextMuted
	case appsysmanager.StatusUnavailable:
		return m.SysManagerStatusUnavailable(), uitheme.DangerText(t)
	default:
		return m.SysManagerStatusIdle(), t.TextMuted
	}
}

func reasonText(m locales.Messages, reason appsysmanager.Reason) string {
	switch reason {
	case appsysmanager.ReasonHostKey:
		return m.SysManagerReasonHostKey()
	case appsysmanager.ReasonAuth:
		return m.SysManagerReasonAuth()
	case appsysmanager.ReasonNetwork:
		return m.SysManagerReasonNetwork()
	case appsysmanager.ReasonTimeout:
		return m.SysManagerReasonTimeout()
	case appsysmanager.ReasonOS:
		return m.SysManagerReasonOS()
	case appsysmanager.ReasonCancelled:
		return m.SysManagerReasonCancelled()
	case appsysmanager.ReasonInternal:
		return m.SysManagerReasonInternal()
	case appsysmanager.ReasonUnsupported:
		return m.SysManagerReasonUnsupported()
	case appsysmanager.ReasonPermission:
		return m.SysManagerReasonPermission()
	case appsysmanager.ReasonNoExec:
		return m.SysManagerReasonNoExec()
	case appsysmanager.ReasonNoData:
		return m.SysManagerReasonNoData()
	case appsysmanager.ReasonMissingCommand:
		return m.SysManagerReasonMissingCommand()
	case appsysmanager.ReasonMissingProcesses:
		return m.SysManagerReasonMissingProcesses()
	case appsysmanager.ReasonMissingPorts:
		return m.SysManagerReasonMissingPorts()
	case appsysmanager.ReasonMissingSystemctl:
		return m.SysManagerReasonMissingSystemctl()
	case appsysmanager.ReasonMissingTmux:
		return m.SysManagerReasonMissingTmux()
	case appsysmanager.ReasonMissingDocker:
		return m.SysManagerReasonMissingDocker()
	case appsysmanager.ReasonDockerDaemon:
		return m.SysManagerReasonDockerDaemon()
	default:
		return ""
	}
}

func problemText(m locales.Messages, problem Problem) string {
	switch problem {
	case ProblemLoadHosts:
		return m.SysManagerProblemLoadHosts()
	case ProblemSave:
		return m.SysManagerProblemSave()
	case ProblemStart:
		return m.SysManagerProblemStart()
	case ProblemNoHost:
		return m.SysManagerProblemNoHost()
	case ProblemActionData:
		return m.SysManagerProblemActionData()
	case ProblemInternal:
		return m.SysManagerProblemInternal()
	case ProblemAttach:
		return m.SysManagerProblemAttach()
	case ProblemDocument:
		return m.SysManagerDocumentFailed()
	default:
		return ""
	}
}

func actionNotice(s *State, m locales.Messages) string {
	if s.ActionBusy() {
		result, _ := s.ActionResult()
		command := result.Command
		if command == "" {
			if pending, ok := s.Pending(); ok {
				command = pending.Command
			}
		}
		return m.SysManagerActionRunning(command)
	}
	if !s.ActionDone() {
		return ""
	}
	if err := s.ActionError(); err != nil {
		if errors.Is(err, context.Canceled) {
			return m.SysManagerActionCancelled()
		}
		var typed *appsysmanager.Error
		if errors.As(err, &typed) && typed.Reason == appsysmanager.ReasonPermission {
			return m.SysManagerActionPermission()
		}
		return m.SysManagerActionFailed()
	}
	return m.SysManagerActionSucceeded(s.ActionCommand())
}

func actionNoticeColor(s *State, t *ui.Theme) ui.Color {
	if s.ActionBusy() {
		return uitheme.WarningText(t)
	}
	if s.ActionError() != nil && !errors.Is(s.ActionError(), context.Canceled) {
		return uitheme.DangerText(t)
	}
	return t.TextMuted
}

func scopeText(m locales.Messages, scope string) string {
	switch scope {
	case "user":
		return m.SysManagerScopeUser()
	default:
		return m.SysManagerScopeSystem()
	}
}

// overviewSampledAt is the age the status row must report: when the counters
// were read, not when the surface last learned a state change. An older
// snapshot without a sample time falls back to its state time.
func overviewSampledAt(state appsysmanager.TabState) time.Time {
	if !state.Stats.SampledAt.IsZero() {
		return state.Stats.SampledAt
	}
	return state.At
}

func percent(value float64) string {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return "—"
	}
	return fmt.Sprintf("%.1f%%", value)
}

func percentText(value float64) string {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return "—"
	}
	return fmt.Sprintf("%.0f%%", value)
}

func ratio(value, total float64) float64 {
	if total <= 0 || math.IsNaN(value) || math.IsNaN(total) {
		return -1
	}
	result := value / total
	if result < 0 {
		return 0
	}
	if result > 1 {
		return 1
	}
	return result
}

func usedText(used, total, divisor float64, m locales.Messages) string {
	if total <= 0 {
		return "—"
	}
	return m.SysManagerOverviewUsedOf(sizeText(used, divisor), sizeText(total, divisor))
}

func sizeText(value, divisor float64) string {
	if divisor <= 0 {
		return "—"
	}
	value /= divisor
	switch {
	case value >= 1024:
		return fmt.Sprintf("%.1f TB", value/1024)
	case value >= 1:
		return fmt.Sprintf("%.1f GB", value)
	default:
		return fmt.Sprintf("%.0f MB", value*1024)
	}
}

func uptimeText(seconds float64, m locales.Messages) string {
	if seconds <= 0 || math.IsNaN(seconds) {
		return m.SysManagerUnknown()
	}
	total := int(seconds)
	days := total / 86400
	hours := (total % 86400) / 3600
	minutes := (total % 3600) / 60
	switch {
	case days > 0:
		return m.SysManagerDurationDays(days, hours)
	case hours > 0:
		return m.SysManagerDurationHours(hours, minutes)
	default:
		return m.SysManagerDurationMinutes(minutes)
	}
}

func loadText(load []float64, m locales.Messages) string {
	if len(load) == 0 {
		return m.SysManagerUnknown()
	}
	fields := make([]string, 0, len(load))
	for _, value := range load {
		if math.IsNaN(value) {
			continue
		}
		fields = append(fields, fmt.Sprintf("%.2f", value))
	}
	if len(fields) == 0 {
		return m.SysManagerUnknown()
	}
	return strings.Join(fields, " ")
}

func coresText(cores int, m locales.Messages) string {
	if cores <= 0 {
		return m.SysManagerUnknown()
	}
	return m.SysManagerOverviewCores(cores)
}

func pidText(pid int) string {
	if pid <= 0 {
		return "—"
	}
	return strconv.Itoa(pid)
}

func blank(value string, m locales.Messages) string {
	if strings.TrimSpace(value) == "" {
		return m.SysManagerUnknown()
	}
	return value
}

// fitColumns keeps, in the side panel, only the columns named in keep, at
// most compactColumnWidth wide at their minimum, so the table fits the
// panel's width without scrolling sideways. full maps a shown column back to
// its index in columns, which the cell function switches on. The System
// manager page shows every column.
func (s *State) fitColumns(columns []ui.TableColumn, keep ...string) ([]ui.TableColumn, func(int) int) {
	if !s.compact {
		return columns, func(col int) int { return col }
	}
	shown := make([]ui.TableColumn, 0, len(keep))
	index := make([]int, 0, len(keep))
	for i, column := range columns {
		if !slices.Contains(keep, column.ID) {
			continue
		}
		column.MinWidth = min(column.MinWidth, compactColumnWidth)
		if column.Width > 0 {
			column.Width = min(column.Width, max(column.MinWidth, 64))
		}
		shown = append(shown, column)
		index = append(index, i)
	}
	return shown, func(col int) int {
		if col < 0 || col >= len(index) {
			return -1
		}
		return index[col]
	}
}

// compactColumnWidth caps a column's minimum width in the side panel.
const compactColumnWidth float32 = 64
