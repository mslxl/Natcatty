package settings

import (
	"math"
	"strconv"

	"github.com/egoist/mygo/ui"

	domain "github.com/mslxl/natcatty/domain/preferences"
	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
)

// QuickControls is the content of the Tab bar's control panel, as the
// original's: the window opacity and the light, dark, or system theme,
// changed without opening Settings.
func (s *PreferencesState) QuickControls() func(*ui.Context) {
	return func(c *ui.Context) {
		m := locales.In(c.Locale())
		t := c.Theme()
		appearance := s.Settings().Appearance
		ui.Column(c).Width(288).Children(func() {
			ui.Row(c).FillWidth().Padding(8, 12).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
				ui.Text(c, m.NavControlPanel()).FontSize(kit.M(c).Font.Callout).FontWeight(500).SingleLine()
			})
			ui.Column(c).FillWidth().Padding(8).Gap(4).Children(func() {
				quickOpacity(c, s, m, t, appearance.WindowOpacity)
				quickTheme(c, s, m, t, appearance.ThemeMode)
			})
		})
	}
}

// quickOpacity is the opacity row: a slider with the original's presets.
func quickOpacity(c *ui.Context, s *PreferencesState, m locales.Messages, t *ui.Theme, opacity float64) {
	if opacity <= 0 {
		opacity = 1
	}
	percent := strconv.Itoa(int(math.Round(opacity * 100)))
	ui.Column(c).FillWidth().Padding(4, 8).Gap(8).Children(func() {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			color := t.TextMuted
			if opacity < 1 {
				color = t.Accent
			}
			kit.IconSized(c, icons.Droplets, kit.M(c).Icon.Medium).TextColor(color)
			ui.Text(c, m.SettingsWindowOpacity()).FontSize(kit.M(c).Font.Callout).SingleLine().Grow(1).MinWidth(0)
			ui.Text(c, m.SettingsWindowOpacityValue(percent)).FontSize(kit.M(c).Font.Footnote).TextColor(t.TextMuted)
		})
		value := opacity
		if ui.Slider(c, &value, domain.MinWindowOpacity, 1).FillWidth().Label(m.SettingsWindowOpacity()).Changed() {
			rounded := math.Round(value*100) / 100
			s.Update(func(settings *domain.Settings) { settings.Appearance.WindowOpacity = rounded })
			c.Invalidate()
		}
		ui.Row(c).FillWidth().Gap(4).Children(func() {
			for _, preset := range []float64{1, 0.95, 0.9, 0.85, 0.8} {
				label := m.SettingsWindowOpacityValue(strconv.Itoa(int(math.Round(preset * 100))))
				c.Key("quick-opacity-" + label)
				button := ui.ButtonBase(c).Label(label).Grow(1).Height(24).Radius(kit.M(c).Radius.Control).
					Justify(ui.Center).AlignItems(ui.Center).FontSize(kit.M(c).Font.Caption).FontWeight(500)
				switch {
				case math.Abs(opacity-preset) < 0.005:
					button.Background(t.Accent).TextColor(t.AccentText)
				case button.Hovered():
					button.Background(kit.Strong(t)).TextColor(t.Text)
				default:
					button.Background(kit.Subtle(t)).TextColor(t.TextMuted)
				}
				button.Children(func() { ui.Text(c, label).SingleLine() })
				if button.Clicked() {
					s.Update(func(settings *domain.Settings) { settings.Appearance.WindowOpacity = preset })
					c.Invalidate()
				}
			}
		})
	})
}

// quickTheme is the theme row: System, Light, and Dark side by side.
func quickTheme(c *ui.Context, s *PreferencesState, m locales.Messages, t *ui.Theme, mode domain.ThemeMode) {
	icon := icons.Monitor
	switch {
	case mode == domain.ThemeLight:
		icon = icons.Sun
	case mode == domain.ThemeDark:
		icon = icons.Moon
	}
	ui.Row(c).FillWidth().Padding(4, 8).Gap(8).AlignItems(ui.Center).Children(func() {
		kit.IconSized(c, icon, kit.M(c).Icon.Medium).TextColor(t.TextMuted)
		ui.Text(c, m.SettingsThemeMode()).FontSize(kit.M(c).Font.Callout).SingleLine().Grow(1).MinWidth(0)
		ui.Row(c).Padding(2).Gap(2).Radius(kit.M(c).Radius.Control).Background(kit.Subtle(t)).Children(func() {
			for _, option := range []struct {
				mode  domain.ThemeMode
				label string
			}{
				{domain.ThemeSystem, m.SettingsThemeSystem()},
				{domain.ThemeLight, m.SettingsThemeLight()},
				{domain.ThemeDark, m.SettingsThemeDark()},
			} {
				c.Key("quick-theme-" + string(option.mode))
				button := ui.ButtonBase(c).Label(option.label).Height(20).Padding(0, 6).Radius(4).
					AlignItems(ui.Center).FontSize(kit.M(c).Font.Caption).FontWeight(500).TextColor(t.TextMuted)
				if option.mode == mode {
					button.Background(t.Background).TextColor(t.Text).Shadow(0, 1, 2, 0, kit.CardShadow(t))
				} else if button.Hovered() {
					button.TextColor(t.Text)
				}
				button.Children(func() { ui.Text(c, option.label).SingleLine() })
				if button.Clicked() && option.mode != mode {
					chosen := option.mode
					s.Update(func(settings *domain.Settings) { settings.Appearance.ThemeMode = chosen })
					c.Invalidate()
				}
			}
		})
	})
}
