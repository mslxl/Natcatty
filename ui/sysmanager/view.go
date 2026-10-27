package sysmanager

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	appsysmanager "github.com/mslxl/natcatty/application/sysmanager"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
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
			tabs(c, s, m)
			tabContent(c, s, m, t)
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
	ui.Row(c).FillWidth().Gap(8).Children(func() {
		ui.Text(c, m.SysManagerTitle()).FontSize(20).Bold()
		ui.Column(c).Grow(1)
		text, color := statusText(m, state, t)
		ui.Text(c, text).FontSize(12).TextColor(color).NoWrap()
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
		if state.Status == appsysmanager.StatusLive && !state.At.IsZero() {
			age := s.now().Sub(state.At)
			if age < 2*time.Second {
				age = 0
			}
			ui.Text(c, m.SysManagerUpdatedSeconds(int(age/time.Second))).
				FontSize(11).TextColor(t.TextMuted).SingleLine().Shrink(1).MinWidth(0)
			if state.Latency > 0 {
				ui.Text(c, m.SysManagerLatency()+" "+m.SysManagerLatencyValue(int(state.Latency/time.Millisecond))).
					FontSize(11).TextColor(t.TextMuted).SingleLine().Shrink(1).MinWidth(0)
			}
		}
	})

	ui.Row(c).FillWidth().Gap(8).Children(func() {
		choices := make([]string, 0, len(domainpreferences.SysManagerIntervalChoices))
		for _, seconds := range domainpreferences.SysManagerIntervalChoices {
			choices = append(choices, m.SysManagerSeconds(seconds))
		}
		current := m.SysManagerSeconds(s.Interval())
		chosen := current
		ui.Select(c, &chosen, choices).Width(84).Label(m.SysManagerRefresh())
		if chosen != current {
			for index, label := range choices {
				if label == chosen {
					s.SetInterval(domainpreferences.SysManagerIntervalChoices[index])
					break
				}
			}
			c.Invalidate()
		}
		if ui.Button(c, m.SysManagerRefreshNow()).Key("sysmanager-refresh").Clicked() {
			s.Refresh()
			c.Invalidate()
		}
		tabsMenu(c, s, m)
		ui.Column(c).Grow(1)
	})

	if problem := problemText(m, s.Problem()); problem != "" {
		ui.Text(c, problem).FontSize(12).TextColor(uitheme.DangerText(t))
	}
	if notice := actionNotice(s, m); notice != "" {
		ui.Text(c, notice).FontSize(12).TextColor(actionNoticeColor(s, t))
	}
}

// tabsMenu exposes the persisted tab layout: show or hide a tab, and move the
// active tab in the strip. Every change is saved by the preferences group, so
// it survives a restart.
func tabsMenu(c *ui.Context, s *State, m locales.Messages) {
	// Key the button before it is built. Element.Key on a menu button rereads
	// its identity after the menu's build was already registered for the
	// positional one, so the menu would never be built: the button showed its
	// label, yet clicking it opened nothing.
	c.Key("sysmanager-tabs-layout")
	ui.MenuButton(c, m.SysManagerTabsLayout(), func(menu *ui.Menu) {
		for _, tab := range s.AllTabs() {
			if menu.Item(tabLabel(m, tab)).
				Checked(s.TabVisible(tab)).
				Disabled(tab == appsysmanager.TabOverview).
				Chosen() {
				_ = s.SetTabVisible(tab, !s.TabVisible(tab))
			}
		}
		menu.Separator()
		index := s.TabIndex()
		if menu.Item(m.SysManagerMoveTabLeft()).Disabled(index <= 0).Chosen() {
			s.MoveTab(-1)
		}
		if menu.Item(m.SysManagerMoveTabRight()).
			Disabled(index >= len(s.Tabs())-1).Chosen() {
			s.MoveTab(1)
		}
	})
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

// tabStripHeight is the height of the scrolling tab strip: a tab's line plus
// the tab padding.
const tabStripHeight float32 = 32

// tabs renders the tab strip. Seven tabs need more width than the 440-DIP
// content column at the minimum window, so the strip scrolls sideways like the
// shell's work-Tab strip and the browser panel's tab strip. The active tab is
// scrolled fully into view when it changes or the window narrows, so its label
// and hit target are never cut at either edge; inactive tabs clip at the
// viewport edge as in the shell's strip.
func tabs(c *ui.Context, s *State, m locales.Messages) {
	visible := s.Tabs()
	labels := make([]string, 0, len(visible))
	for _, tab := range visible {
		labels = append(labels, tabLabel(m, tab))
	}
	index := s.TabIndex()
	chosen := index
	// A resize changes the room the strip has for its tabs. Reveal the active
	// tab again so one chosen at a wide window does not stay half cut after
	// the user narrows the window.
	if width, _ := c.Size(); width > 0 {
		if s.stripWidth > 0 && width != s.stripWidth {
			s.revealTab = true
		}
		s.stripWidth = width
	}
	reveal := s.revealTab
	s.revealTab = false

	ui.ScrollHorizontal(c).FillWidth().Height(tabStripHeight).Shrink(0).Children(func() {
		t := c.Theme()
		parts := ui.TabsBase(c, &chosen, len(labels))
		parts.List.Gap(t.Space(1)).Children(func() {
			for i, label := range labels {
				on := i == chosen
				tab := parts.Tab(i).Padding(t.Space(2), t.Space(3)).FocusRing(false)
				tab.TextColor(t.TextMuted)
				if on {
					tab.TextColor(t.Text).FontWeight(600)
					if reveal {
						tab = tab.ScrollIntoView()
					}
				} else if tab.Hovered() {
					tab.TextColor(t.Text)
				}
				tab.DrawOver(func(p *ui.Painter, r ui.Rect) {
					if on {
						in, height := t.Space(1.5), t.Space(0.5)
						p.Fill(ui.Rect{X: r.X + in, Y: r.Y + r.H - height, W: r.W - 2*in, H: height},
							t.Accent, height/2)
					}
					if tab.FocusVisible() {
						p.FocusRing(r, [4]float32{t.Radius, t.Radius, t.Radius, t.Radius})
					}
				})
				tab.Children(func() { ui.Text(c, label).SingleLine() })
			}
		})
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

func tabContent(c *ui.Context, s *State, m locales.Messages, t *ui.Theme) {
	state := s.TabState(s.Tab())
	switch s.Tab() {
	case appsysmanager.TabOverview:
		if state.Status != appsysmanager.StatusLive || state.Overview.Hostname == "" {
			statusPanel(c, s, state, m, t)
			return
		}
		overview(c, state.Overview, m, t)
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
			create := ui.Button(c, m.SysManagerCreate()).Key("sysmanager-tmux-create").
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
	ui.Table(c, &s.table, columns, len(rows), func(row, col int) {
		if row < 0 || row >= len(rows) {
			return
		}
		session := rows[row]
		switch col {
		case 0:
			ui.Text(c, session.Name).FontSize(12).SingleLine()
		case 1:
			ui.Text(c, strconv.Itoa(session.Windows)).FontSize(12)
		case 2:
			attached := m.SysManagerUnknown()
			if session.Attached {
				attached = m.SysManagerYes()
			}
			text := ui.Text(c, attached).FontSize(12)
			if s.selected != row {
				text.TextColor(t.TextMuted)
			}
		case 3:
			text := ui.Text(c, sessionCreatedText(session.Created)).
				FontSize(12).SingleLine()
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
			ui.Text(c, m.SysManagerSessionHint()).FontSize(11).TextColor(t.TextMuted)
		})
		return
	}
	actionBar(c, func() {
		ui.Text(c, m.SysManagerSelectedSession(session.Name, session.Windows)).
			FontSize(11).TextColor(t.TextMuted).Grow(1).SingleLine()
		if ui.Button(c, m.SysManagerAttach()).Key("sysmanager-tmux-attach").Clicked() {
			_ = s.AttachSession(session.Name)
			c.Invalidate()
		}
		if ui.Button(c, m.SysManagerKill()).Key("sysmanager-tmux-kill").Clicked() {
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
		ui.Segmented(c, &chosen, m.SysManagerContainers(), m.SysManagerImages()).
			Label(m.SysManagerDockerView())
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
		ui.Table(c, &s.table, columns, len(rows), func(row, col int) {
			if row < 0 || row >= len(rows) {
				return
			}
			container := rows[row]
			switch col {
			case 0:
				ui.Text(c, containerDisplayName(container)).FontSize(12).SingleLine()
			case 1:
				ui.Text(c, container.Image).FontSize(12).SingleLine()
			case 2:
				text := ui.Text(c, container.State).FontSize(12).SingleLine()
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
				text := ui.Text(c, container.Status).FontSize(12).SingleLine()
				if s.selected != row {
					text.TextColor(t.TextMuted)
				}
			case 4:
				ui.Text(c, blank(container.Ports, m)).FontSize(12).SingleLine()
			}
		}).Grow(1).MinHeight(0).FillWidth()
		dockerContainerActions(c, s, m, t)
	})
}

func dockerContainerActions(c *ui.Context, s *State, m locales.Messages, t *ui.Theme) {
	container, ok := s.SelectedContainer()
	if !ok {
		actionBar(c, func() {
			ui.Text(c, m.SysManagerContainerHint()).FontSize(11).TextColor(t.TextMuted)
		})
		documentPanel(c, s, m, t)
		return
	}
	running := strings.EqualFold(container.State, "running")
	actionBar(c, func() {
		ui.Text(c, m.SysManagerSelectedContainer(containerDisplayName(container),
			container.Image)).FontSize(11).TextColor(t.TextMuted).Grow(1).SingleLine()
		if ui.Button(c, m.SysManagerStart()).Key("sysmanager-docker-start").
			Disabled(s.ActionBusy() || running).Clicked() {
			request(s, s.RequestDockerAction(container, appsysmanager.DockerStart))
			c.Invalidate()
		}
		if ui.Button(c, m.SysManagerStop()).Key("sysmanager-docker-stop").
			Disabled(s.ActionBusy() || !running).Clicked() {
			request(s, s.RequestDockerAction(container, appsysmanager.DockerStop))
			c.Invalidate()
		}
		if ui.Button(c, m.SysManagerRemove()).Key("sysmanager-docker-remove").
			Disabled(s.ActionBusy()).Clicked() {
			request(s, s.RequestDockerAction(container, appsysmanager.DockerRemove))
			c.Invalidate()
		}
		if ui.Button(c, m.SysManagerInspect()).Key("sysmanager-docker-inspect").
			Clicked() {
			request(s, s.InspectContainer(container))
			c.Invalidate()
		}
		if ui.Button(c, m.SysManagerLogs()).Key("sysmanager-docker-logs").
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
		ui.Table(c, &s.table, columns, len(rows), func(row, col int) {
			if row < 0 || row >= len(rows) {
				return
			}
			image := rows[row]
			switch col {
			case 0:
				ui.Text(c, image.Name()).FontSize(12).SingleLine()
			case 1:
				ui.Text(c, blank(image.Size, m)).FontSize(12)
			case 2:
				text := ui.Text(c, imageCreatedText(image.CreatedAt, m)).
					FontSize(12).SingleLine()
				if s.selected != row {
					text.TextColor(t.TextMuted)
				}
			case 3:
				ui.Text(c, shortID(image.ID)).FontSize(12).SingleLine()
			}
		}).Grow(1).MinHeight(0).FillWidth()
		actionBar(c, func() {
			if image, ok := s.SelectedImage(); ok {
				ui.Text(c, m.SysManagerSelectedImage(image.Name(), blank(image.Size, m))).
					FontSize(11).TextColor(t.TextMuted).SingleLine()
				return
			}
			ui.Text(c, m.SysManagerImageHint()).FontSize(11).TextColor(t.TextMuted)
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
			ui.Text(c, title).FontSize(12).Bold().SingleLine()
			ui.Column(c).Grow(1)
			if ui.Button(c, m.SysManagerClose()).Key("sysmanager-document-close").Clicked() {
				s.CloseDocument()
				c.Invalidate()
			}
		})
		switch {
		case s.DocumentBusy():
			ui.Text(c, m.SysManagerDocumentLoading()).FontSize(11).TextColor(t.TextMuted)
		case s.DocumentError() != nil:
			ui.Text(c, m.SysManagerDocumentFailed()).FontSize(11).TextColor(uitheme.DangerText(t))
			ui.Text(c, s.DocumentError().Error()).FontSize(11).TextColor(t.TextMuted).SingleLine()
		default:
			if document.Truncated {
				ui.Text(c, m.SysManagerDocumentTruncated()).
					FontSize(10).TextColor(t.TextMuted)
			}
			ui.Scroll(c).Grow(1).MinHeight(0).FillWidth().Children(func() {
				text := document.Text
				if strings.TrimSpace(text) == "" {
					text = m.SysManagerUnknown()
				}
				ui.Text(c, text).FontSize(11).Selectable()
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
				FontSize(12).TextColor(t.TextMuted)
			if state.GPU.DriverVersion != "" {
				ui.Text(c, m.SysManagerGPUDriver()+" "+state.GPU.DriverVersion).
					FontSize(12).TextColor(t.TextMuted).NoWrap()
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
				FontSize(13).Bold().Grow(1).SingleLine()
			ui.Text(c, device.Vendor).FontSize(11).TextColor(t.TextMuted).NoWrap()
		})
		ui.Row(c).FillWidth().Gap(12).Children(func() {
			ui.Column(c).Grow(1).Gap(2).Children(func() {
				ui.Row(c).FillWidth().Children(func() {
					ui.Text(c, m.SysManagerGPUUtilization()).
						FontSize(11).TextColor(t.TextMuted)
					ui.Column(c).Grow(1)
					ui.Text(c, percentText(device.UtilizationPercent)).FontSize(11)
				})
				if ratio := ratio(device.UtilizationPercent, 100); ratio >= 0 {
					ui.Progress(c, ratio).Grow(1)
				}
			})
			ui.Column(c).Grow(1).Gap(2).Children(func() {
				ui.Row(c).FillWidth().Children(func() {
					ui.Text(c, m.SysManagerGPUMemory()).
						FontSize(11).TextColor(t.TextMuted)
					ui.Column(c).Grow(1)
					ui.Text(c, gpuMemoryText(device, m)).FontSize(11).NoWrap()
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
		ui.Text(c, label).FontSize(11).TextColor(t.TextMuted).NoWrap()
		ui.Text(c, value).FontSize(11).NoWrap()
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
	ui.Row(c).FillWidth().Gap(8).Children(func() {
		query := s.search
		ui.SearchField(c, &query).Placeholder(placeholder).Grow(1).MinWidth(120)
		if query != s.search {
			s.SetSearch(query)
			c.Invalidate()
		}
		ui.Text(c, summary).FontSize(11).TextColor(t.TextMuted).NoWrap()
		// The content's right edge sits under the window edge in a narrow
		// window; the reserve keeps the last summary character whole.
		ui.Column(c).Width(12)
	})
}

func emptyText(total int, none, noMatches string) string {
	if total == 0 {
		return none
	}
	return noMatches
}

func emptyState(c *ui.Context, message string, t *ui.Theme) {
	ui.Column(c).Grow(1).MinHeight(0).FillWidth().AlignItems(ui.Center).Justify(ui.Center).Children(func() {
		ui.Text(c, message).FontSize(12).TextColor(t.TextMuted)
	})
}

func statusPanel(c *ui.Context, s *State, state appsysmanager.TabState, m locales.Messages, t *ui.Theme) {
	text, color := statusText(m, state, t)
	ui.Column(c).Grow(1).MinHeight(0).FillWidth().AlignItems(ui.Center).Justify(ui.Center).Gap(6).Children(func() {
		ui.Text(c, text).FontSize(14).Bold().TextColor(color)
		if reason := reasonText(m, state.Reason); reason != "" {
			ui.Text(c, reason).FontSize(12).TextColor(t.TextMuted)
		}
		if state.Status == appsysmanager.StatusIdle ||
			state.Status == appsysmanager.StatusConnecting {
			return
		}
		if ui.Button(c, m.SysManagerRefreshNow()).Key("sysmanager-retry").Clicked() {
			s.Refresh()
			c.Invalidate()
		}
	})
}

func processTable(c *ui.Context, s *State, rows []appsysmanager.Process, m locales.Messages) {
	columns := []ui.TableColumn{
		{Title: m.SysManagerPid(), ID: "pid", Width: 56, MinWidth: 44, Align: ui.End, Sortable: true},
		{Title: m.SysManagerUser(), ID: "user", Width: 76, MinWidth: 56, Sortable: true},
		{Title: m.SysManagerCPU(), ID: "cpu", Width: 76, MinWidth: 64, Align: ui.End, Sortable: true},
		{Title: m.SysManagerMemory(), ID: "mem", Width: 56, MinWidth: 48, Align: ui.End, Sortable: true},
		{Title: m.SysManagerCommand(), ID: "command", MinWidth: 120, Sortable: true},
	}
	ui.Table(c, &s.table, columns, len(rows), func(row, col int) {
		if row < 0 || row >= len(rows) {
			return
		}
		process := rows[row]
		switch col {
		case 0:
			ui.Text(c, strconv.Itoa(process.PID)).FontSize(12)
		case 1:
			ui.Text(c, process.User).FontSize(12).SingleLine()
		case 2:
			ui.Text(c, percent(process.CPUPercent)).FontSize(12)
		case 3:
			ui.Text(c, percent(process.MemPercent)).FontSize(12)
		case 4:
			ui.Text(c, process.Command).FontSize(12).SingleLine()
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
	ui.Table(c, &s.table, columns, len(rows), func(row, col int) {
		if row < 0 || row >= len(rows) {
			return
		}
		port := rows[row]
		switch col {
		case 0:
			ui.Text(c, strconv.Itoa(port.Port)).FontSize(12)
		case 1:
			ui.Text(c, port.Protocol).FontSize(12)
		case 2:
			ui.Text(c, port.Address).FontSize(12).SingleLine()
		case 3:
			ui.Text(c, pidText(port.PID)).FontSize(12)
		case 4:
			ui.Text(c, port.ProcessName).FontSize(12).SingleLine()
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
	ui.Table(c, &s.table, columns, len(rows), func(row, col int) {
		if row < 0 || row >= len(rows) {
			return
		}
		unit := rows[row]
		switch col {
		case 0:
			ui.Text(c, unit.Name).FontSize(12).SingleLine()
		case 1:
			ui.Text(c, scopeText(m, unit.Scope)).FontSize(12)
		case 2:
			// The selected row inherits the accent's high-contrast foreground,
			// so a state color from the normal background would be illegible.
			text := ui.Text(c, unit.ActiveState).FontSize(12).SingleLine()
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
			text := ui.Text(c, unit.Description).FontSize(12).SingleLine()
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
			ui.Text(c, m.SysManagerProcessHint()).FontSize(11).TextColor(t.TextMuted)
		})
		return
	}
	actionBar(c, func() {
		ui.Text(c, m.SysManagerSelectedProcess(process.PID, process.Command)).
			FontSize(11).TextColor(t.TextMuted).Grow(1).SingleLine()
		stopped := strings.Contains(strings.ToUpper(process.Stat), "T")
		if !stopped {
			if ui.Button(c, m.SysManagerStop()).Key("sysmanager-process-stop").Clicked() {
				request(s, s.RequestSignal(process.PID, "STOP"))
				c.Invalidate()
			}
		} else {
			if ui.Button(c, m.SysManagerContinue()).Key("sysmanager-process-continue").Clicked() {
				request(s, s.RequestSignal(process.PID, "CONT"))
				c.Invalidate()
			}
		}
		if ui.Button(c, m.SysManagerTerminate()).Key("sysmanager-process-terminate").Clicked() {
			request(s, s.RequestSignal(process.PID, "TERM"))
			c.Invalidate()
		}
		if ui.Button(c, m.SysManagerKill()).Key("sysmanager-process-kill").Clicked() {
			request(s, s.RequestSignal(process.PID, "KILL"))
			c.Invalidate()
		}
	})
}

func portActions(c *ui.Context, s *State, m locales.Messages, t *ui.Theme) {
	port, ok := s.SelectedPort()
	if !ok {
		actionBar(c, func() {
			ui.Text(c, m.SysManagerPortHint()).FontSize(11).TextColor(t.TextMuted)
		})
		return
	}
	actionBar(c, func() {
		ui.Text(c, m.SysManagerSelectedPort(port.Protocol, port.Address,
			strconv.Itoa(port.Port), strconv.Itoa(port.PID))).
			FontSize(11).TextColor(t.TextMuted).Grow(1).SingleLine()
		if port.PID > 0 {
			if ui.Button(c, m.SysManagerTerminate()).Key("sysmanager-port-terminate").Clicked() {
				request(s, s.RequestPortTerminate(port))
				c.Invalidate()
			}
		} else {
			ui.Text(c, m.SysManagerPortOwnerUnknown()).FontSize(11).TextColor(t.TextMuted)
		}
	})
}

func serviceActions(c *ui.Context, s *State, m locales.Messages, t *ui.Theme) {
	unit, ok := s.SelectedService()
	if !ok {
		actionBar(c, func() {
			ui.Text(c, m.SysManagerServiceHint()).FontSize(11).TextColor(t.TextMuted)
		})
		return
	}
	scope := scopeText(m, unit.Scope)
	actionBar(c, func() {
		ui.Text(c, m.SysManagerSelectedService(unit.Name, scope)).
			FontSize(11).TextColor(t.TextMuted).Grow(1).SingleLine()
		if ui.Button(c, m.SysManagerStart()).Key("sysmanager-service-start").Clicked() {
			request(s, s.RequestServiceAction(unit.Name, appsysmanager.ServiceStart,
				appsysmanager.Scope(unit.Scope)))
			c.Invalidate()
		}
		if ui.Button(c, m.SysManagerStop()).Key("sysmanager-service-stop").Clicked() {
			request(s, s.RequestServiceAction(unit.Name, appsysmanager.ServiceStop,
				appsysmanager.Scope(unit.Scope)))
			c.Invalidate()
		}
		if ui.Button(c, m.SysManagerRestart()).Key("sysmanager-service-restart").Clicked() {
			request(s, s.RequestServiceAction(unit.Name, appsysmanager.ServiceRestart,
				appsysmanager.Scope(unit.Scope)))
			c.Invalidate()
		}
	})
}

func request(s *State, err error) {
	if err != nil {
		s.problem = ProblemActionData
	}
}

func overview(c *ui.Context, data appsysmanager.Overview, m locales.Messages, t *ui.Theme) {
	ui.Scroll(c).Grow(1).MinHeight(0).FillWidth().Children(func() {
		ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Start).Children(func() {
			systemCard(c, data, m, t)
			resourceCard(c, data, m, t)
		})
	})
}

func systemCard(c *ui.Context, data appsysmanager.Overview, m locales.Messages, t *ui.Theme) {
	ui.Column(c).Grow(1).MinWidth(150).Padding(10).Gap(5).Background(t.Surface).Radius(t.Radius).Children(func() {
		ui.Text(c, m.SysManagerOverviewSystem()).FontSize(13).Bold()
		field(c, m.SysManagerOverviewHostname(), blank(data.Hostname, m), t)
		field(c, m.SysManagerOverviewOS(), blank(data.OSName, m), t)
		field(c, m.SysManagerOverviewKernel(), blank(data.KernelRelease, m), t)
		field(c, m.SysManagerOverviewUptime(), uptimeText(data.UptimeSeconds, m), t)
		field(c, m.SysManagerOverviewLoad(), loadText(data.LoadAverage, m), t)
		field(c, m.SysManagerOverviewCoresLabel(), coresText(data.Cores, m), t)
	})
}

func resourceCard(c *ui.Context, data appsysmanager.Overview, m locales.Messages, t *ui.Theme) {
	memRatio := ratio(data.MemUsedMB, data.MemTotalMB)
	swapRatio := ratio(data.SwapUsedMB, data.SwapTotalMB)
	diskRatio := ratio(data.DiskUsedGB, data.DiskTotalGB)
	ui.Column(c).Grow(1).MinWidth(150).Padding(10).Gap(5).Background(t.Surface).Radius(t.Radius).Children(func() {
		ui.Text(c, m.SysManagerOverviewResources()).FontSize(13).Bold()
		metric(c, m.SysManagerOverviewCPU(), percentText(data.CPUPercent),
			ratio(data.CPUPercent, 100), t)
		metric(c, m.SysManagerOverviewMemory(),
			usedText(data.MemUsedMB, data.MemTotalMB, 1024, m), memRatio, t)
		metric(c, m.SysManagerOverviewSwap(),
			usedText(data.SwapUsedMB, data.SwapTotalMB, 1024, m), swapRatio, t)
		metric(c, m.SysManagerOverviewDisk(),
			usedText(data.DiskUsedGB, data.DiskTotalGB, 1, m), diskRatio, t)
	})
}

func metric(c *ui.Context, label, value string, ratio float64, t *ui.Theme) {
	ui.Column(c).FillWidth().Gap(2).Children(func() {
		ui.Row(c).FillWidth().Gap(6).Children(func() {
			ui.Text(c, label).FontSize(11).TextColor(t.TextMuted)
			ui.Column(c).Grow(1)
			ui.Text(c, value).FontSize(11).NoWrap()
		})
		if ratio >= 0 {
			ui.Progress(c, ratio).Grow(1)
		}
	})
}

func field(c *ui.Context, label, value string, t *ui.Theme) {
	ui.Row(c).FillWidth().Gap(6).Children(func() {
		ui.Text(c, label).FontSize(11).TextColor(t.TextMuted).NoWrap()
		ui.Column(c).Grow(1)
		ui.Text(c, value).FontSize(11).NoWrap()
	})
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
