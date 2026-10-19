package shell

import (
	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/shell/state"
)

// View returns the main window's shell: the fixed Vault navigation, the
// Settings entry point, the five-language selector, and the ordered work tabs.
func View(s *state.State) func(*ui.Context) {
	return func(c *ui.Context) {
		m := locales.In(c.Locale())
		t := c.Theme()

		ui.Row(c).Fill().Children(func() {
			sidebar(c, s, m, t)
			content(c, s, m, t)
		})
	}
}

func sidebar(c *ui.Context, s *state.State, m locales.Messages, t *ui.Theme) {
	ui.Column(c).Width(232).Fill().Padding(16).Gap(8).Background(t.Surface).Children(func() {
		ui.Text(c, m.AppName()).FontSize(20).Bold()

		// Vault is the main window's root surface, so its navigation entry is
		// already selected. SFTP joins it in a later ticket.
		ui.PrimaryButton(c.Key("nav-vault"), m.NavVault()).Width(200)

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
	ui.Column(c).Grow(1).Fill().Padding(24).Gap(16).Children(func() {
		if s.TabCount() > 0 {
			ui.Row(c).Gap(8).Children(func() {
				for index, number := range s.TabNumbers() {
					label := m.TabTitle(number)
					if index == s.ActiveTab() {
						if ui.PrimaryButton(c.Key(number), label).Clicked() {
							s.SelectTab(index)
						}
					} else if ui.Button(c.Key(number), label).Clicked() {
						s.SelectTab(index)
					}
				}
			})
		}

		ui.Text(c, m.NavVault()).FontSize(24).Bold()
		ui.Text(c, m.VaultPlaceholder()).TextColor(t.TextMuted)
	})
}
