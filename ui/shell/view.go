package shell

import (
	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"
	uiconnection "github.com/mslxl/natcatty/ui/connection"
	"github.com/mslxl/natcatty/ui/locales"
	sessionui "github.com/mslxl/natcatty/ui/session"
	"github.com/mslxl/natcatty/ui/shell/state"
)

// sidebarWidth is the fixed width of the Vault navigation column. The content
// column takes the rest of the window.
const sidebarWidth float32 = 232

// View returns the main window's shell: the fixed Vault navigation, the
// Settings entry point, the five-language selector, and the ordered work tabs.
func View(s *state.State) func(*ui.Context) {
	return func(c *ui.Context) {
		// Connections complete off the UI thread; applying them here keeps
		// every Tab mutation on this thread.
		s.Flush()

		m := locales.In(c.Locale())
		t := c.Theme()

		ui.Row(c).Fill().Children(func() {
			sidebar(c, s, m, t)
			content(c, s, m, t)
		})

		if prompt := s.TrustPrompt(); prompt != nil {
			sessionui.TrustView(prompt)(c)
		}
		if prompt := s.AuthPrompt(); prompt != nil {
			sessionui.AuthPromptModal(prompt)(c)
		}
	}
}

func sidebar(c *ui.Context, s *state.State, m locales.Messages, t *ui.Theme) {
	// FillHeight, not Fill: the sidebar shares the window's row with the
	// content, and Fill would set its width to the whole window and push the
	// content past the right edge.
	ui.Column(c).Width(sidebarWidth).FillHeight().Padding(16).Gap(8).Background(t.Surface).Children(func() {
		ui.Text(c, m.AppName()).FontSize(20).Bold()

		// Vault is the main window's root surface. SFTP joins it in a later
		// ticket.
		if ui.PrimaryButton(c.Key("nav-vault"), m.NavVault()).Width(200).Clicked() {
			s.ShowVault()
		}

		if ui.Button(c.Key("nav-settings"), m.NavSettings()).Width(200).Clicked() {
			s.RequestSettings()
		}

		ui.Column(c).Grow(1)

		ui.Text(c, m.NavLanguage()).FontSize(12).TextColor(t.TextMuted)
		current := c.Locale()
		ui.RadioGroup(c, func() {
			for _, tag := range locales.Tags() {
				ui.Radio(c.Key("locale-"+tag), &current, tag, i18n.Autonym(tag))
			}
		})
		if current != c.Locale() {
			s.RequestLocale(current)
			c.Invalidate()
		}
	})
}

func content(c *ui.Context, s *state.State, m locales.Messages, t *ui.Theme) {
	// Grow takes the width the sidebar leaves; Fill would ask for the whole
	// window again and clip the tab strip and Panes at the right edge.
	ui.Column(c).Grow(1).FillHeight().Padding(16).Gap(12).Children(func() {
		for _, label := range s.PendingConnections() {
			ui.Text(c, m.ConnectionConnectingTo(label)).FontSize(12).TextColor(t.TextMuted)
		}
		if notice := desktopNoticeText(m, s.DesktopNotice()); notice != "" {
			ui.Text(c, notice).FontSize(12).TextColor(t.Danger)
		}
		if problem := uiconnection.ProblemText(m, s.Problem()); problem != "" {
			ui.Text(c, problem).TextColor(t.Danger)
		}
		// The ordered work Tabs stay visible over the Vault so a user who
		// returned to the Vault can select a Session again.
		if s.TabCount() > 0 {
			workTabs(c, s, m, t)
		}
		if s.VaultVisible() {
			if view := s.VaultView(); view != nil {
				view(c)
				return
			}
			ui.Text(c, m.NavVault()).FontSize(24).Bold()
			ui.Text(c, m.VaultPlaceholder()).TextColor(t.TextMuted)
			return
		}
		if s.TabCount() > 0 {
			activeTab(c, s, m, t)
			return
		}

		ui.Text(c, m.NavVault()).FontSize(24).Bold()
		ui.Text(c, m.VaultPlaceholder()).TextColor(t.TextMuted)
	})
}

func activeTab(c *ui.Context, s *state.State, m locales.Messages, t *ui.Theme) {
	if snapshot, ok := s.ActiveSnapshot(); ok {
		if status := statusText(m, snapshot); status != "" {
			ui.Text(c, status).FontSize(12).TextColor(t.TextMuted)
		}
		if notice := noticeText(m, snapshot.Notice); notice != "" {
			ui.Text(c, notice).FontSize(12).TextColor(t.Danger)
		}
	}
	if root, focus, ok := s.TabTree(s.ActiveTab()); ok {
		paneTree(c, s, m, t, mainTabRef(s, s.ActiveTab()), root, focus)
	}
}

// noticeText localizes a non-fatal Session notice. Unknown codes render
// nothing, so the UI never shows an untranslated token.
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
