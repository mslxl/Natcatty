package shell

import (
	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
	uiconnection "github.com/mslxl/natcatty/ui/connection"
	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/layout"
	"github.com/mslxl/natcatty/ui/locales"
	uisearch "github.com/mslxl/natcatty/ui/search"
	sessionui "github.com/mslxl/natcatty/ui/session"
	"github.com/mslxl/natcatty/ui/shell/state"
	uitheme "github.com/mslxl/natcatty/ui/theme"
	"github.com/mslxl/natcatty/ui/titlebar"
	"github.com/mslxl/natcatty/ui/vault/section"
)

// sidebarWidth is the home Tab's navigation column width.
const sidebarWidth = layout.SidebarWidth

// topBarHeight is the height of the window's Tab bar, and tabHeight of a Tab
// in it, as the original's h-9 bar and h-7 Tabs.
const (
	topBarHeight         = titlebar.Height
	tabHeight    float32 = 28
)

// The content card's geometry, as the layout draws it.
const (
	shellPadding = layout.ShellPadding
	cardGap      = layout.CardGap
	cardPadding  = layout.CardPadding
	cardBorder   = layout.CardBorder
)

// View returns the main window's shell, laid out as the original's: one Tab
// bar across the window holding the home Tab (the Vault and the pages its
// sidebar lists), SFTP, and the terminal Tabs; below it the chosen Tab's
// surface. Only the home Tab has a sidebar, so a terminal gets the whole
// window.
func View(s *state.State) func(*ui.Context) {
	// The work Tab strip scrolls sideways at the minimum window. This tracker
	// belongs to this window's content function, so the active Tab is revealed
	// when it changes or the window is resized.
	reveal := newWorkTabReveal()
	return func(c *ui.Context) {
		// Connections complete off the UI thread; applying them here keeps
		// every Tab mutation on this thread.
		s.Flush()

		m := locales.In(c.Locale())
		// While a terminal is shown the window takes its scheme's colors, as
		// the original's active chrome theme, so the Tab bar, the Pane
		// headers, and the side panel read as one surface with the terminal.
		if preset, ok := s.TerminalTheme(); ok && terminalVisible(s) {
			c.SetTheme(uitheme.Chrome(c.Theme(), preset))
		}
		t := c.Theme()

		// Hiding the SFTP Tab while it is shown leaves for the Vault, as the
		// original's; the Session panel still browses files.
		if !s.SFTPTabShown() && s.SFTPVisible() {
			s.ShowHome()
			c.Invalidate()
		}
		layout.MainLayout(c, layout.MainSlots{
			HomeTabs: func() { homeTabs(c, s, m) },
			Tabs:     func() { workTabs(c, s, m, t, reveal) },
			Flush:    terminalVisible(s),
			Body:     func() { body(c, s, m, t) },
		})

		if prompt := s.TrustPrompt(); prompt != nil {
			sessionui.TrustView(prompt)(c)
		}
		if prompt := s.AuthPrompt(); prompt != nil {
			sessionui.AuthPromptModal(prompt)(c)
		}
		if serial := s.SerialView(); serial != nil {
			serial(c)
		}
		// The editor sits above the work surfaces so a remote-change conflict
		// from an external edit is visible wherever the user is.
		if editor := s.EditorView(); editor != nil {
			editor(c)
		}
		// The Quick Switcher sits above every surface, including a prompt, so
		// its chord works wherever the focus is.
		if search := s.SearchState(); search != nil {
			uisearch.Overlay(search)(c)
		}
		// A side panel tool no longer drawn, such as System sampling a Host,
		// stops once the frame is done.
		s.FinishSideFrame()
	}
}

// homeVisible reports whether the home Tab is the chosen one. With no work
// Tab and no other surface shown, the window falls back to it.
func homeVisible(s *state.State) bool {
	return !terminalVisible(s) && !s.SFTPVisible()
}

// body draws the chosen Tab's surface below the Tab bar.
func body(c *ui.Context, s *state.State, m locales.Messages, t *ui.Theme) {
	switch {
	case terminalVisible(s):
		activeTab(c, s, m, t)
	case s.SFTPVisible():
		layout.HomeLayout(c, layout.HomeSlots{
			Notices: func() { notices(c, s, m, t) },
			Content: func() {
				if view := s.SFTPView(); view != nil {
					view(c)
					return
				}
				placeholder(c, icons.FolderOpen, m.NavSftp(), "")
			},
		})
	default:
		layout.HomeLayout(c, layout.HomeSlots{
			Sidebar: func() { layout.Sidebar(c, homeSidebar(c, s, m, t)) },
			Notices: func() { notices(c, s, m, t) },
			Content: func() { homeSurface(c, s, m, t) },
		})
	}
}

// notices shows the window's desktop and connection problems above the
// surface.
func notices(c *ui.Context, s *state.State, m locales.Messages, t *ui.Theme) {
	if notice := desktopNoticeText(m, s.DesktopNotice()); notice != "" {
		kit.Notice(c, icons.TriangleAlert, t.Danger, notice, nil)
	}
	if problem := uiconnection.ProblemTextFor(m, s.ProblemError()); problem != "" {
		kit.Notice(c, icons.CircleX, t.Danger, problem, nil)
	}
}

func placeholder(c *ui.Context, icon, title, description string) {
	ui.Column(c).Fill().Justify(ui.Center).Children(func() {
		kit.EmptyState(c, icon, title, description, nil)
	})
}

// homeSidebar is the home Tab's navigation, as the original's Vault sidebar:
// the brand, the Vault's pages with monitoring, System manager, and Logs
// among them, and Settings at the foot.
func homeSidebar(c *ui.Context, s *state.State, m locales.Messages, t *ui.Theme) layout.SidebarSlots {
	nav := func(key, icon, label string, active bool, show func()) {
		if kit.NavItem(c, kit.NavHome, key, icon, label, active).Clicked() {
			show()
		}
	}
	return layout.SidebarSlots{
		Brand: func() { brand(c, s, m, t) },
		Pages: func() {
			vaultPage := func(page section.Section) {
				nav("nav-vault-"+string(page), vaultPageIcon(page), vaultPageLabel(m, page),
					s.VaultVisible() && s.VaultSection() == page,
					func() { s.ShowVaultSection(page) })
			}
			vaultPage(section.Hosts)
			nav("nav-monitor", icons.Gauge, m.NavMonitor(), s.MonitorVisible(), s.ShowMonitor)
			nav("nav-sysmanager", icons.MonitorCog, m.NavSysManager(), s.SysManagerVisible(), s.ShowSystemManager)
			for _, page := range section.Pages() {
				if page != section.Hosts {
					vaultPage(page)
				}
			}
			nav("nav-logs", icons.Activity, m.NavLogs(), s.LogsVisible(), s.ShowLogs)
		},
		SettingsLabel: m.NavSettings(),
		Settings:      s.RequestSettings,
	}
}

// brand heads the sidebar: the app's mark and name.
func brand(c *ui.Context, s *state.State, m locales.Messages, t *ui.Theme) {
	ui.Row(c).FillWidth().Height(40).Padding(0, 8).Gap(10).AlignItems(ui.Center).Margin(0, 0, 8, 0).Children(func() {
		if mark := s.BrandMark(); mark != nil {
			ui.Image(c, mark).Size(28, 28).Shrink(0)
		} else {
			ui.Box(c).Size(28, 28).Radius(8).Background(t.Accent).Center().TextColor(t.AccentText).
				Children(func() { kit.IconSized(c, icons.SquareTerminal, kit.M(c).Icon.Regular) })
		}
		ui.Text(c, m.AppName()).FontSize(kit.M(c).Font.Title3).FontWeight(700).SingleLine()
	})
}

// homeTabs are the Tab bar's fixed home and SFTP Tabs.
func homeTabs(c *ui.Context, s *state.State, m locales.Messages) {
	if kit.Tab(c, kit.TabWork, kit.TabSpec{Key: "tab-home", Icon: icons.LayoutGrid, Title: m.NavVault(), Fixed: true, Active: homeVisible(s)}).Clicked {
		s.ShowHome()
		c.Invalidate()
	}
	if s.SFTPTabShown() {
		if kit.Tab(c, kit.TabWork, kit.TabSpec{Key: "tab-sftp", Icon: icons.FolderOpen, Title: m.NavSftp(), Fixed: true, Active: s.SFTPVisible()}).Clicked {
			s.ShowSFTP()
			c.Invalidate()
		}
	}
}

// homeSurface draws the home Tab's chosen page.
func homeSurface(c *ui.Context, s *state.State, m locales.Messages, t *ui.Theme) {
	switch {
	case s.LogsVisible():
		if view := s.LogsView(); view != nil {
			view(c)
			return
		}
		placeholder(c, icons.Activity, m.NavLogs(), "")
	case s.MonitorVisible():
		if view := s.MonitorView(); view != nil {
			view(c)
			return
		}
		placeholder(c, icons.Gauge, m.NavMonitor(), "")
	case s.SysManagerVisible():
		if view := s.SysManagerView(); view != nil {
			view(c)
			return
		}
		placeholder(c, icons.MonitorCog, m.NavSysManager(), "")
	default:
		if view := s.VaultView(); view != nil {
			view(c)
			return
		}
		placeholder(c, icons.LayoutGrid, m.NavVault(), m.VaultPlaceholder())
	}
}

// vaultPageIcon and vaultPageLabel are a Vault page's sidebar entry, as the
// original's Vault sidebar drew them.
func vaultPageIcon(page section.Section) string {
	switch page {
	case section.Keychain:
		return icons.KeyRound
	case section.Proxies:
		return icons.Globe
	case section.Networks:
		return icons.Network
	case section.Forwarding:
		return icons.Plug
	case section.Snippets:
		return icons.FileCode
	case section.Notes:
		return icons.NotebookPen
	case section.KnownHosts:
		return icons.BookMarked
	default:
		return icons.LayoutGrid
	}
}

func vaultPageLabel(m locales.Messages, page section.Section) string {
	switch page {
	case section.Keychain:
		return m.NavKeychain()
	case section.Proxies:
		return m.NavProxies()
	case section.Networks:
		return m.NavNetworks()
	case section.Forwarding:
		return m.NavForwarding()
	case section.Snippets:
		return m.NavSnippets()
	case section.Notes:
		return m.NavNotes()
	case section.KnownHosts:
		return m.NavKnownHosts()
	default:
		return m.NavHosts()
	}
}

func activeTab(c *ui.Context, s *state.State, m locales.Messages, t *ui.Theme) {
	slots := layout.WorkSlots{Top: func() {
		notices(c, s, m, t)
		terminalPanels(c, s, m, t)
		if snapshot, ok := s.ActiveSnapshot(); ok {
			if notice := noticeText(m, snapshot.Notice); notice != "" {
				ui.Box(c).FillWidth().Padding(0, 0, 6, 0).Children(func() {
					kit.Notice(c, icons.TriangleAlert, t.Warning, notice, nil)
				})
			}
		}
	}}
	if root, focus, ok := s.TabTree(s.ActiveTab()); ok {
		ordinal := 0
		ref := mainTabRef(s, s.ActiveTab())
		ref.split = !root.IsPane()
		ref.status = activeStatus(s, m, t)
		slots.Panes = func() { paneTree(c, s, m, t, ref, root, focus, &ordinal) }
		dock := s.FilesDock()
		window, _ := c.Size()
		width := window
		if dock != state.DockBottom {
			width = *s.FilesWidth(window)
		}
		if files := s.FilesView(ref.id, width); files != nil {
			tabID := ref.id
			slots.Panel = func() { sidePanel(c, s, m, t, tabID, dock, files) }
			slots.Dock = layoutDock(dock)
			slots.Width, slots.Ratio = s.FilesWidth(window), s.FilesRatio()
		}
	}
	layout.WorkLayout(c, slots)
}

// layoutDock is the layout's edge for the side panel's dock.
func layoutDock(dock state.PanelDock) layout.Dock {
	switch dock {
	case state.DockRight:
		return layout.DockRight
	case state.DockBottom:
		return layout.DockBottom
	}
	return layout.DockLeft
}

// sidePanel is a Session Tab's side panel, as the original's: a strip of
// tools along the top (SFTP, Scripts, History, Theme, System, Notes), the
// panel's dock and close at its end, and the chosen tool below.
func sidePanel(c *ui.Context, s *state.State, m locales.Messages, t *ui.Theme,
	tab model.TabID, dock state.PanelDock, tool func(*ui.Context)) {
	// The panel shares the terminal's background, as the original's, with a
	// line under its strip; the macOS style raises it as a card.
	ui.Column(c).Fill().MinWidth(0).Background(kit.CardInnerFill(t, t.Background)).Children(func() {
		ids := s.SideTools(tab)
		chosen, tools := -1, make([]kit.Segment, len(ids))
		for i, id := range ids {
			icon, label := sideToolFace(m, id)
			// Each button keys its tool's identity, so a tool's state
			// follows it however the strip changes.
			tools[i] = kit.Segment{Key: "side-tool-" + string(id), Label: label, Icon: icon}
			if id == s.SideTool(tab) {
				chosen = i
			}
		}
		if kit.ToolStrip(c, tools, &chosen, m.SessionFilesPanel(), func() { sidePanelControls(c, s, m, tab, dock) }) {
			s.SetSideTool(tab, ids[chosen])
			c.Invalidate()
		}
		ui.Column(c).FillWidth().Grow(1).MinHeight(0).Children(func() { tool(c) })
	})
}

// sidePanelControls are the side panel's dock menu and close button.
func sidePanelControls(c *ui.Context, s *state.State, m locales.Messages, tab model.TabID, dock state.PanelDock) {
	sidePanelButton(c, dockIcon(dock), m.SettingsSidePanelDock()).Menu(func(menu *ui.Menu) {
		for _, option := range []struct {
			dock  state.PanelDock
			label string
		}{
			{state.DockLeft, m.SettingsDockLeft()},
			{state.DockRight, m.SettingsDockRight()},
			{state.DockBottom, m.SettingsDockBottom()},
		} {
			if menu.Item(option.label).Checked(dock == option.dock).Chosen() {
				s.SetFilesDock(option.dock)
			}
		}
	})
	if sidePanelButton(c, icons.X, m.SidePanelClose()).Clicked() {
		s.CloseFiles(tab)
	}
}

// dockIcon is the glyph of a side panel docked at dock.
func dockIcon(dock state.PanelDock) string {
	switch dock {
	case state.DockRight:
		return icons.PanelRight
	case state.DockBottom:
		return icons.PanelBottom
	}
	return icons.PanelLeft
}

// sideToolFace is a side panel tool's icon and name in the strip.
func sideToolFace(m locales.Messages, id state.SideToolID) (string, string) {
	switch id {
	case state.SideScripts:
		return icons.Zap, m.SidePanelScripts()
	case state.SideHistory:
		return icons.History, m.SidePanelHistory()
	case state.SideTheme:
		return icons.Palette, m.SidePanelTheme()
	case state.SideSystem:
		return icons.Activity, m.SidePanelSystem()
	case state.SideNotes:
		return icons.NotebookPen, m.SidePanelNotes()
	}
	return icons.FolderTree, "SFTP"
}

// sidePanelButton is one of the strip's 28-DIP icon buttons.
func sidePanelButton(c *ui.Context, icon, label string) ui.Element {
	return kit.IconButtonWith(c, icon, label, kit.IconOptions{Role: kit.IconCard})
}

// sessionTone is the color of a Session's status dot: green while it runs
// connected, amber while it connects, red once it failed or stopped.
func sessionTone(t *ui.Theme, snapshot appsession.Snapshot, ok bool) ui.Color {
	if !ok {
		return t.TextMuted
	}
	if snapshot.Connection == appsession.ConnectionStatusError {
		return t.Danger
	}
	switch snapshot.Status {
	case appsession.StatusRunning:
		switch snapshot.Connection {
		case appsession.ConnectionStatusConnecting, appsession.ConnectionStatusReconnecting:
			return t.Warning
		}
		return t.Success
	case appsession.StatusExited, appsession.StatusClosed:
		return t.Danger
	}
	return t.TextMuted
}

// paneStatus is the focused Pane's localized Session status and its tone.
// settled marks a running, connected Session: the header says nothing then,
// as the terminal itself shows the Session is live.
type paneStatus struct {
	text    string
	tone    ui.Color
	settled bool
}

func activeStatus(s *state.State, m locales.Messages, t *ui.Theme) paneStatus {
	snapshot, ok := s.ActiveSnapshot()
	if !ok {
		return paneStatus{}
	}
	settled := snapshot.Status == appsession.StatusRunning &&
		(snapshot.Connection == appsession.ConnectionStatusActive || snapshot.Connection == "")
	return paneStatus{text: statusText(m, snapshot), tone: sessionTone(t, snapshot, true), settled: settled}
}

// desktopNoticeText localizes a desktop target failure. Unknown codes render
// nothing, so the UI never shows an untranslated token.
func desktopNoticeText(m locales.Messages, notice state.DesktopNotice) string {
	switch notice {
	case state.DesktopNoticeInvalidTarget:
		return m.DeepLinkProblemInvalid()
	case state.DesktopNoticeUnsupportedTarget:
		return m.DeepLinkProblemUnsupported()
	}
	return ""
}

// noticeText localizes a non-fatal Session notice. Unknown codes render
// nothing, so the UI never shows an untranslated token.
func noticeText(m locales.Messages, notice *appsession.Notice) string {
	if notice == nil {
		return ""
	}
	switch notice.Code {
	case appsession.NoticeX11LocalDisplay:
		return m.ConnectionX11DisplayUnavailable(notice.Target)
	case appsession.NoticeX11Cookie:
		return m.ConnectionX11CookieUnavailable(notice.Target)
	case appsession.NoticeX11Channel:
		return m.ConnectionX11ChannelFailed(notice.Target)
	case appsession.NoticeTmuxRestored:
		return m.ConnectionTmuxSessionRestored()
	case appsession.NoticeTmuxNested:
		return m.ConnectionTmuxNested()
	case appsession.NoticeTmuxUnavailable:
		return m.ConnectionTmuxUnavailable()
	case appsession.NoticeETDisplaced:
		return m.ConnectionEternalTerminalDisplaced()
	case appsession.NoticeETResumeFallback:
		return m.ConnectionEternalTerminalResumeUnsupported()
	default:
		return ""
	}
}

// statusText localizes a Session snapshot's status. While a Session is
// running the connection indicator (active, reconnecting, error) is the useful
// state; an empty value is treated as active for compatibility. It returns ""
// for a state this slice does not know, so the UI never shows an untranslated
// token.
func statusText(m locales.Messages, snapshot appsession.Snapshot) string {
	// A terminal failure is the useful state even when the Session has already
	// stopped: the exit placeholder must not hide why it stopped.
	if snapshot.Connection == appsession.ConnectionStatusError {
		return m.ConnectionStatusError()
	}
	switch snapshot.Status {
	case appsession.StatusRunning:
		switch snapshot.Connection {
		case appsession.ConnectionStatusConnecting:
			return m.ConnectionStatusConnecting()
		case appsession.ConnectionStatusReconnecting:
			return m.ConnectionStatusReconnecting()
		}
		return m.ConnectionStatusActive()
	case appsession.StatusExited:
		if snapshot.HasExitCode {
			return m.SessionExitedWithCode(snapshot.ExitCode)
		}
		return m.SessionExited()
	case appsession.StatusClosed:
		return m.SessionClosed()
	}
	return ""
}
