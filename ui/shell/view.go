package shell

import (
	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
	uiconnection "github.com/mslxl/natcatty/ui/connection"
	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
	uisearch "github.com/mslxl/natcatty/ui/search"
	sessionui "github.com/mslxl/natcatty/ui/session"
	"github.com/mslxl/natcatty/ui/shell/state"
	uitheme "github.com/mslxl/natcatty/ui/theme"
	"github.com/mslxl/natcatty/ui/titlebar"
	"github.com/mslxl/natcatty/ui/vault/section"
)

// sidebarWidth is the fixed width of the home Tab's navigation column. Only
// the home Tab has one: SFTP and the terminal Tabs take the window's width.
const sidebarWidth float32 = 220

// topBarHeight is the height of the window's Tab bar, and tabHeight of a Tab
// in it, as the original's h-9 bar and h-7 Tabs.
const (
	topBarHeight         = titlebar.Height
	tabHeight    float32 = 28
)

// shellPadding is the distance from the body's edges to a surface's content:
// the gap around the content card plus the card's own padding.
const shellPadding float32 = 16

// cardGap is the room between the window's edges and the content card.
// cardPadding insets a surface inside the card, so the two add up to
// shellPadding.
const (
	cardGap     float32 = 8
	cardPadding         = shellPadding - cardGap
)

// cardBorder is the width of the content card's outline.
const cardBorder float32 = 1

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

		ui.Column(c).Fill().Background(t.Background).Children(func() {
			workTabs(c, s, m, t, reveal)
			body(c, s, m, t)
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
	// MinHeight(0) and MinWidth(0) keep the body at the window's size when a
	// surface's content is larger; it scrolls or clips inside instead.
	ui.Row(c).FillWidth().Grow(1).MinHeight(0).MinWidth(0).Children(func() {
		switch {
		case terminalVisible(s):
			// The terminal reaches the window's edges, as the original's.
			ui.Column(c).Grow(1).FillHeight().MinWidth(0).Children(func() {
				notices(c, s, m, t)
				activeTab(c, s, m, t)
			})
		case s.SFTPVisible():
			contentCard(c, s, m, t, cardGap, func() {
				if view := s.SFTPView(); view != nil {
					view(c)
					return
				}
				placeholder(c, icons.FolderOpen, m.NavSftp(), "")
			})
		default:
			homeSidebar(c, s, m, t)
			contentCard(c, s, m, t, 0, func() { homeSurface(c, s, m, t) })
		}
	})
}

// contentCard is the raised, rounded card a home page or SFTP draws in.
// start is the gap at its left: none beside the home sidebar, which already
// leaves room, and cardGap when it spans the window.
func contentCard(c *ui.Context, s *state.State, m locales.Messages, t *ui.Theme, start float32, page func()) {
	ui.Column(c).Grow(1).FillHeight().MinWidth(0).Padding(0, cardGap, cardGap, start).Gap(cardGap).Children(func() {
		notices(c, s, m, t)
		ui.Column(c).FillWidth().Grow(1).MinHeight(0).MinWidth(0).Padding(cardPadding).
			Background(t.Surface).Border(cardBorder, t.Border).Radius(kit.M(c).Radius.Card).
			Shadow(0, 1, 3, 0, kit.CardShadow(t)).Clip().Children(page)
	})
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
// the brand, the Vault's pages with monitoring, System manager, and Logs among
// them, and Settings at the foot.
func homeSidebar(c *ui.Context, s *state.State, m locales.Messages, t *ui.Theme) {
	// FillHeight, not Fill: the sidebar shares the body's row with the
	// content, and Fill would set its width to the whole window and push the
	// content past the right edge.
	ui.Column(c).Width(sidebarWidth).FillHeight().Shrink(0).Padding(4, 8, 8, 8).Gap(2).Children(func() {
		ui.Row(c).FillWidth().Height(40).Padding(0, 8).Gap(10).AlignItems(ui.Center).Margin(0, 0, 8, 0).Children(func() {
			if mark := s.BrandMark(); mark != nil {
				ui.Image(c, mark).Size(28, 28).Shrink(0)
			} else {
				ui.Box(c).Size(28, 28).Radius(8).Background(t.Accent).Center().TextColor(t.AccentText).
					Children(func() { kit.IconSized(c, icons.SquareTerminal, kit.M(c).Icon.Regular) })
			}
			ui.Text(c, m.AppName()).FontSize(kit.M(c).Font.Title3).FontWeight(700).SingleLine()
		})

		// The list scrolls when the window is too short for every entry.
		ui.Scroll(c).FillWidth().Grow(1).MinHeight(0).Children(func() {
			kit.NavList(c, kit.NavHome, func() {
				nav := func(key, icon, label string, active bool, show func()) {
					if kit.NavItem(c, kit.NavHome, key, icon, label, active).Clicked() {
						show()
					}
				}
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
			})
		})

		// Settings is a window rather than a surface, so it is never active.
		if kit.NavItem(c, kit.NavHome, "nav-settings", icons.Settings, m.NavSettings(), false).Clicked() {
			s.RequestSettings()
		}
	})
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

// controlPanel is the Tab bar's control panel, as the original's: a quiet
// icon button opening the window opacity and theme. The language is in
// Settings, as the original's.
func controlPanel(c *ui.Context, s *state.State, m locales.Messages) {
	content := s.QuickControls()
	if content == nil {
		return
	}
	open := s.QuickControlsOpen()
	button := kit.IconButtonWith(c, icons.SlidersHorizontal, m.NavControlPanel(), kit.IconOptions{Role: kit.IconCard, Key: "bar-control-panel"})
	if *open {
		button.Background(c.Theme().Text.Alpha(0.1))
	}
	if button.Clicked() {
		*open = !*open
		c.Invalidate()
	}
	t := c.Theme()
	ui.PopoverBase(c, button, open, func(panel ui.Element) {
		panel.AttachTo(button, ui.AnchorBottomEnd, ui.AnchorTopEnd).Margin(6, 0, 0, 0).
			Background(t.Surface).Border(1, t.Border).Radius(kit.M(c).Radius.Card).
			Shadow(0, 8, 24, 0, kit.CardShadow(t)).Clip()
		content(c)
	})
}

func activeTab(c *ui.Context, s *state.State, m locales.Messages, t *ui.Theme) {
	terminalPanels(c, s, m, t)
	if snapshot, ok := s.ActiveSnapshot(); ok {
		if notice := noticeText(m, snapshot.Notice); notice != "" {
			ui.Box(c).FillWidth().Padding(0, 0, 6, 0).Children(func() {
				kit.Notice(c, icons.TriangleAlert, t.Warning, notice, nil)
			})
		}
	}
	if root, focus, ok := s.TabTree(s.ActiveTab()); ok {
		ordinal := 0
		ref := mainTabRef(s, s.ActiveTab())
		ref.split = !root.IsPane()
		ref.status = activeStatus(s, m, t)
		panes := func() {
			ui.Column(c).Fill().MinWidth(0).Children(func() {
				paneTree(c, s, m, t, ref, root, focus, &ordinal)
			})
		}
		dock := s.FilesDock()
		window, _ := c.Size()
		width := window
		if dock != state.DockBottom {
			width = *s.FilesWidth(window)
		}
		files := s.FilesView(ref.id, width)
		if files == nil {
			ui.Column(c).FillWidth().Grow(1).MinHeight(0).Children(panes)
			return
		}
		tabID := ref.id
		panel := func() { sidePanel(c, s, m, t, tabID, dock, files) }
		// The side panel docks at the Tab's left edge by default, as the
		// original's, flush with the terminal: the split's own one-DIP line
		// is the only separator, and the divider keeps the width the user
		// drags it to.
		switch dock {
		case state.DockBottom:
			ui.SplitVerticalPercent(c, s.FilesRatio(), panes, panel).FillWidth().Grow(1).MinHeight(0)
		case state.DockRight:
			// Split puts its first child, the panel and its width, at the
			// leading edge; a right-to-left box makes that the right one, and
			// each side turns back to left to right inside.
			ui.Row(c).Direction(ui.RightToLeft).FillWidth().Grow(1).MinHeight(0).Children(func() {
				ui.Split(c, s.FilesWidth(window), func() {
					ui.Column(c).Direction(ui.LeftToRight).Fill().MinWidth(0).Children(panel)
				}, func() {
					ui.Column(c).Direction(ui.LeftToRight).Fill().MinWidth(0).Children(panes)
				}).Fill()
			})
		default:
			ui.Split(c, s.FilesWidth(window), panel, panes).FillWidth().Grow(1).MinHeight(0)
		}
	}
}

// sidePanel is a Session Tab's side panel, as the original's: a strip of
// tools along the top (SFTP, Scripts, History, Theme, System, Notes), the
// panel's dock and close at its end, and the chosen tool below.
func sidePanel(c *ui.Context, s *state.State, m locales.Messages, t *ui.Theme,
	tab model.TabID, dock state.PanelDock, tool func(*ui.Context)) {
	// The panel shares the terminal's background, as the original's, with a
	// line under its strip.
	ui.Column(c).Fill().MinWidth(0).Background(t.Background).Children(func() {
		kit.Bar(c, kit.BarStrip).Padding(0, 6).Gap(4).Children(func() {
			chosen := s.SideTool(tab)
			for _, id := range s.SideTools(tab) {
				icon, label := sideToolFace(m, id)
				// The strip changes with the target: a local Session has no
				// System, so each button keeps its tool's identity.
				c.Key("side-tool-" + string(id))
				if sidePanelButton(c, icon, label, id == chosen).Clicked() && id != chosen {
					s.SetSideTool(tab, id)
					c.Invalidate()
				}
			}
			ui.Column(c).Grow(1)
			icon := icons.PanelLeft
			switch dock {
			case state.DockRight:
				icon = icons.PanelRight
			case state.DockBottom:
				icon = icons.PanelBottom
			}
			sidePanelButton(c, icon, m.SettingsSidePanelDock(), false).Menu(func(menu *ui.Menu) {
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
			if sidePanelButton(c, icons.X, m.SidePanelClose(), false).Clicked() {
				s.CloseFiles(tab)
			}
		})
		ui.Column(c).FillWidth().Grow(1).MinHeight(0).Children(func() { tool(c) })
	})
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

// sidePanelButton is one of the strip's 28-DIP icon buttons; the active
// tool is tinted with the accent.
func sidePanelButton(c *ui.Context, icon, label string, active bool) ui.Element {
	t := c.Theme()
	button := kit.IconButtonWith(c, icon, label, kit.IconOptions{Role: kit.IconCard})
	if active {
		button.Background(t.Accent.Alpha(0.24)).TextColor(t.Text)
	}
	return button
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
