package shell

import (
	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"
	uiconnection "github.com/mslxl/natcatty/ui/connection"
	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
	uisearch "github.com/mslxl/natcatty/ui/search"
	sessionui "github.com/mslxl/natcatty/ui/session"
	"github.com/mslxl/natcatty/ui/shell/state"
	"github.com/mslxl/natcatty/ui/vault/section"
)

// sidebarWidth is the fixed width of the home Tab's navigation column. Only
// the home Tab has one: SFTP and the terminal Tabs take the window's width.
const sidebarWidth float32 = 220

// topBarHeight is the height of the window's Tab bar.
const topBarHeight float32 = 40

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
			ui.Column(c).Grow(1).FillHeight().MinWidth(0).Padding(0, 6, 6, 6).Children(func() {
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
			Background(t.Surface).Border(cardBorder, t.Border).Radius(kit.RadiusCard).
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
			ui.Box(c).Size(28, 28).Radius(8).Background(t.Accent).Center().TextColor(t.AccentText).
				Children(func() { ui.Icon(c, icons.Lucide(icons.SquareTerminal)).Size(16, 16) })
			ui.Text(c, m.AppName()).FontSize(16).FontWeight(700).SingleLine()
		})

		// The list scrolls when the window is too short for every entry.
		ui.Scroll(c).FillWidth().Grow(1).MinHeight(0).Children(func() {
			ui.Column(c).FillWidth().Gap(2).Children(func() {
				vaultPage := func(page section.Section) {
					navItem(c, "nav-vault-"+string(page), vaultPageIcon(page), vaultPageLabel(m, page),
						s.VaultVisible() && s.VaultSection() == page,
						func() { s.ShowVaultSection(page) })
				}
				vaultPage(section.Hosts)
				navItem(c, "nav-monitor", icons.Gauge, m.NavMonitor(), s.MonitorVisible(), s.ShowMonitor)
				navItem(c, "nav-sysmanager", icons.MonitorCog, m.NavSysManager(), s.SysManagerVisible(), s.ShowSystemManager)
				for _, page := range section.Pages() {
					if page != section.Hosts {
						vaultPage(page)
					}
				}
				navItem(c, "nav-logs", icons.Activity, m.NavLogs(), s.LogsVisible(), s.ShowLogs)
			})
		})

		// Settings is a window rather than a surface, so it is never active.
		navItem(c, "nav-settings", icons.Settings, m.NavSettings(), false, s.RequestSettings)
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

// navItem renders one sidebar entry: an icon and a label, filled while its
// page is the visible one.
func navItem(c *ui.Context, key, icon, label string, active bool, show func()) {
	t := c.Theme()
	item := ui.ButtonBase(c).Key(key).Label(label).FillWidth().Height(36).Padding(0, 12).Gap(12).
		Radius(8).AlignItems(ui.Center).TextColor(t.TextMuted)
	switch {
	case active:
		item.Background(kit.Strong(t)).TextColor(t.Text)
	case item.Pressed():
		item.Background(kit.Strong(t)).TextColor(t.Text)
	case item.Hovered():
		item.Background(kit.Subtle(t)).TextColor(t.Text)
	}
	item.Children(func() {
		kit.Icon(c, icon)
		text := ui.Text(c, label).SingleLine().Grow(1).MinWidth(0)
		if active {
			text.FontWeight(500)
		}
	})
	if item.Clicked() {
		show()
	}
}

// languageMenu is the Tab bar's language picker: a quiet icon button opening
// a menu of the five languages the app ships, the current one checked.
func languageMenu(c *ui.Context, s *state.State, m locales.Messages) {
	current := c.Locale()
	if current == "" {
		current = locales.Tags()[0]
	}
	barButton(c, "nav-language", icons.Languages, m.NavLanguage()).Menu(func(menu *ui.Menu) {
		for _, tag := range locales.Tags() {
			if menu.Item(i18n.Autonym(tag)).Checked(tag == current).Chosen() && tag != current {
				s.RequestLocale(tag)
				c.Invalidate()
			}
		}
	})
}

// barButton is an icon-only control at the right of the Tab bar.
func barButton(c *ui.Context, key, icon, label string) ui.Element {
	t := c.Theme()
	button := ui.ButtonBase(c).Key(key).Label(label).Tooltip(label).Size(30, 30).Radius(kit.RadiusControl).
		Justify(ui.Center).AlignItems(ui.Center).TextColor(t.TextMuted).Shrink(0)
	switch {
	case button.Pressed():
		button.Background(kit.Strong(t)).TextColor(t.Text)
	case button.Hovered():
		button.Background(kit.Subtle(t)).TextColor(t.Text)
	}
	button.Children(func() { ui.Icon(c, icons.Lucide(icon)).Size(16, 16) })
	return button
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
		files := s.FilesView(ref.id)
		if files == nil {
			ui.Column(c).FillWidth().Grow(1).MinHeight(0).Children(panes)
			return
		}
		// The SFTP panel sits at the Tab's right edge, as the original's, and
		// the divider between it and the terminal keeps the width the user
		// drags it to.
		ui.SplitPercent(c, s.FilesRatio(), panes, func() {
			// The Panes keep a 4-DIP drop gutter on every side; the panel
			// keeps the same inset so the two headers line up.
			ui.Column(c).Fill().MinWidth(0).Padding(4, 4, 4, 0).Children(func() { files(c) })
		}).FillWidth().Grow(1).MinHeight(0)
	}
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
