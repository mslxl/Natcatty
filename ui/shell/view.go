package shell

import (
	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	appconnection "github.com/mslxl/natcatty/application/connection"
	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/ui/locales"
	sessionui "github.com/mslxl/natcatty/ui/session"
	"github.com/mslxl/natcatty/ui/shell/state"
)

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
	}
}

func sidebar(c *ui.Context, s *state.State, m locales.Messages, t *ui.Theme) {
	ui.Column(c).Width(232).Fill().Padding(16).Gap(8).Background(t.Surface).Children(func() {
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
	ui.Column(c).Grow(1).Fill().Padding(16).Gap(12).Children(func() {
		if problem := connectionProblemText(m, s.Problem()); problem != "" {
			ui.Text(c, problem).TextColor(t.Danger)
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
			workTabs(c, s, m)
			activeTab(c, s, m, t)
			return
		}

		ui.Text(c, m.NavVault()).FontSize(24).Bold()
		ui.Text(c, m.VaultPlaceholder()).TextColor(t.TextMuted)
	})
}

func workTabs(c *ui.Context, s *state.State, m locales.Messages) {
	selected := s.ActiveTab()
	labels := make([]string, 0, s.TabCount())
	for index := 0; index < s.TabCount(); index++ {
		label, number, _ := s.TabLabel(index)
		if label == "" {
			label = m.LocalSessionTab(number)
		}
		labels = append(labels, label)
	}
	if ui.Tabs(c, &selected, labels...).Changed() && selected != s.ActiveTab() {
		s.SelectTab(selected)
		c.Invalidate()
	}
}

func activeTab(c *ui.Context, s *state.State, m locales.Messages, t *ui.Theme) {
	if snapshot, ok := s.ActiveSnapshot(); ok {
		if status := statusText(m, snapshot); status != "" {
			ui.Text(c, status).FontSize(12).TextColor(t.TextMuted)
		}
	}
	if surface := s.ActiveSurface(); surface != nil {
		surface.View(c)
	}
}

// connectionProblemText localizes a connection failure. Unknown kinds render
// nothing, so the UI never shows an untranslated token.
func connectionProblemText(m locales.Messages, kind appconnection.Kind) string {
	switch kind {
	case appconnection.KindAuth:
		return m.ConnectionErrorAuth()
	case appconnection.KindNetwork:
		return m.ConnectionErrorNetwork()
	case appconnection.KindTimeout:
		return m.ConnectionErrorTimeout()
	case appconnection.KindHostKey:
		return m.ConnectionErrorHostKey()
	case appconnection.KindUnsupported:
		return m.ConnectionErrorUnsupported()
	case appconnection.KindCancelled:
		return m.ConnectionErrorCancelled()
	case appconnection.KindInternal:
		return m.ConnectionErrorInternal()
	}
	return ""
}

// statusText localizes a Session snapshot's status. It returns "" for a state
// this slice does not know, so the UI never shows an untranslated token.
func statusText(m locales.Messages, snapshot appsession.Snapshot) string {
	switch snapshot.Status {
	case appsession.StatusRunning:
		return m.SessionRunning()
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
