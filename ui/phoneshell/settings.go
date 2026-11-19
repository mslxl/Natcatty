package phoneshell

import (
	"fmt"

	"github.com/egoist/mygo/ui"

	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
)

// SettingsService is the settings seam the phone pages edit. The shared
// preferences and terminal-settings services implement it through
// NewSettingsService, so the phone writes the same versioned documents the
// desktop reads: portable fields travel through Vault sync, while fields such
// as interface style and app icon stay on the phone and are stripped by
// domainpreferences.Portable.
type SettingsService interface {
	Preferences() domainpreferences.Settings
	UpdatePreferences(change func(*domainpreferences.Settings)) error
	Terminal() domainterminal.Settings
	UpdateTerminal(change func(*domainterminal.Settings)) error
}

// settingsPages owns the Settings section's forms. Every control writes
// through the service on the click that changes it, so the value is persisted
// and published before the next frame draws.
type settingsPages struct {
	service SettingsService
	problem string
}

func newSettingsPages() *settingsPages { return &settingsPages{} }

func (p *settingsPages) draw(c *ui.Context, page page) {
	width, _ := c.Size()
	ui.Scroll(c).Fill().Children(func() {
		ui.Column(c).Width(width).Padding(16).Gap(14).Children(func() {
			if p.service == nil {
				kit.Notice(c, icons.TriangleAlert, c.Theme().Danger,
					"Settings are not available on this device.", nil)
				return
			}
			if p.problem != "" {
				kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, p.problem, nil)
			}
			switch page.path {
			case "/appearance":
				p.appearance(c)
			case "/terminal":
				p.terminal(c)
			case "/input":
				p.input(c)
			case "/security":
				p.security(c)
			}
		})
	})
}

func (p *settingsPages) appearance(c *ui.Context) {
	appearance := p.service.Preferences().Appearance
	p.choices(c, "Theme", []choice{
		pick("System", appearance.ThemeMode == domainpreferences.ThemeSystem, func() {
			p.updatePreferences(func(s *domainpreferences.Settings) {
				s.Appearance.ThemeMode = domainpreferences.ThemeSystem
			})
		}),
		pick("Light", appearance.ThemeMode == domainpreferences.ThemeLight, func() {
			p.updatePreferences(func(s *domainpreferences.Settings) {
				s.Appearance.ThemeMode = domainpreferences.ThemeLight
			})
		}),
		pick("Dark", appearance.ThemeMode == domainpreferences.ThemeDark, func() {
			p.updatePreferences(func(s *domainpreferences.Settings) {
				s.Appearance.ThemeMode = domainpreferences.ThemeDark
			})
		}),
	})
	kit.EmptyHint(c, "Interface style and app icon stay on this phone.")
	p.choices(c, "Interface style", []choice{
		pick("Classic", appearance.InterfaceStyle == domainpreferences.InterfaceClassic, func() {
			p.updatePreferences(func(s *domainpreferences.Settings) {
				s.Appearance.InterfaceStyle = domainpreferences.InterfaceClassic
			})
		}),
		pick("macOS", appearance.InterfaceStyle == domainpreferences.InterfaceMacOS, func() {
			p.updatePreferences(func(s *domainpreferences.Settings) {
				s.Appearance.InterfaceStyle = domainpreferences.InterfaceMacOS
			})
		}),
	})
	iconChoices := make([]choice, 0, len(domaintheme.IconVariants()))
	for _, variant := range domaintheme.IconVariants() {
		variant := variant
		chosen := appearance.AppIcon == variant.ID ||
			(appearance.AppIcon == "" && variant.ID == domaintheme.DefaultIconVariant)
		iconChoices = append(iconChoices, pick(variant.Name, chosen, func() {
			p.updatePreferences(func(s *domainpreferences.Settings) {
				s.Appearance.AppIcon = variant.ID
			})
		}))
	}
	p.choices(c, "App icon", iconChoices)
	p.stepper(c, "Terminal font size", int(appearance.TerminalFontSize),
		int(domainpreferences.MinTerminalFontSize), int(domainpreferences.MaxTerminalFontSize), 1,
		func(value int) {
			p.updatePreferences(func(s *domainpreferences.Settings) {
				s.Appearance.TerminalFontSize = float64(value)
			})
		})
	p.choices(c, "Cursor", []choice{
		pick("Block", appearance.TerminalCursorStyle == domainpreferences.CursorBlock, func() {
			p.updatePreferences(func(s *domainpreferences.Settings) {
				s.Appearance.TerminalCursorStyle = domainpreferences.CursorBlock
			})
		}),
		pick("Bar", appearance.TerminalCursorStyle == domainpreferences.CursorBar, func() {
			p.updatePreferences(func(s *domainpreferences.Settings) {
				s.Appearance.TerminalCursorStyle = domainpreferences.CursorBar
			})
		}),
		pick("Underline", appearance.TerminalCursorStyle == domainpreferences.CursorUnderline, func() {
			p.updatePreferences(func(s *domainpreferences.Settings) {
				s.Appearance.TerminalCursorStyle = domainpreferences.CursorUnderline
			})
		}),
	})
}

func (p *settingsPages) terminal(c *ui.Context) {
	settings := p.service.Terminal()
	p.toggle(c, "Paste protection", settings.PasteProtection, func(on bool) {
		p.updateTerminal(func(s *domainterminal.Settings) { s.PasteProtection = on })
	})
	p.stepper(c, "Paste warning after lines", settings.PasteMinLines, 2, 50, 1, func(value int) {
		p.updateTerminal(func(s *domainterminal.Settings) { s.PasteMinLines = value })
	})
	p.choices(c, "Remote clipboard", []choice{
		pick("Off", settings.OSC52 == domainterminal.ClipboardOff, func() {
			p.updateTerminal(func(s *domainterminal.Settings) { s.OSC52 = domainterminal.ClipboardOff })
		}),
		pick("Write only", settings.OSC52 == domainterminal.ClipboardWriteOnly, func() {
			p.updateTerminal(func(s *domainterminal.Settings) { s.OSC52 = domainterminal.ClipboardWriteOnly })
		}),
		pick("Read and write", settings.OSC52 == domainterminal.ClipboardReadWrite, func() {
			p.updateTerminal(func(s *domainterminal.Settings) { s.OSC52 = domainterminal.ClipboardReadWrite })
		}),
		pick("Ask first", settings.OSC52 == domainterminal.ClipboardPrompt, func() {
			p.updateTerminal(func(s *domainterminal.Settings) { s.OSC52 = domainterminal.ClipboardPrompt })
		}),
	})
}

func (p *settingsPages) input(c *ui.Context) {
	settings := p.service.Terminal()
	p.toggle(c, "Autocomplete", settings.AutocompleteEnabled, func(on bool) {
		p.updateTerminal(func(s *domainterminal.Settings) { s.AutocompleteEnabled = on })
	})
	p.toggle(c, "Inline suggestion", settings.AutocompleteGhostText, func(on bool) {
		p.updateTerminal(func(s *domainterminal.Settings) { s.AutocompleteGhostText = on })
	})
	p.toggle(c, "Suggestion list", settings.AutocompletePopupMenu, func(on bool) {
		p.updateTerminal(func(s *domainterminal.Settings) { s.AutocompletePopupMenu = on })
	})
	p.stepper(c, "Suggest after characters", settings.AutocompleteMinChars, 1, 10, 1, func(value int) {
		p.updateTerminal(func(s *domainterminal.Settings) { s.AutocompleteMinChars = value })
	})
	p.stepper(c, "Suggestion delay (ms)", settings.AutocompleteDebounceMs, 0, 1000, 50, func(value int) {
		p.updateTerminal(func(s *domainterminal.Settings) { s.AutocompleteDebounceMs = value })
	})
}

func (p *settingsPages) security(c *ui.Context) {
	security := p.service.Preferences().Security
	p.toggle(c, "Confirm destructive actions", security.ConfirmDestructive, func(on bool) {
		p.updatePreferences(func(s *domainpreferences.Settings) { s.Security.ConfirmDestructive = on })
	})
	p.toggle(c, "Confirm closing sessions", security.ConfirmCloseSessions, func(on bool) {
		p.updatePreferences(func(s *domainpreferences.Settings) { s.Security.ConfirmCloseSessions = on })
	})
}

// choice is one value of a labeled group.
type choice struct {
	label  string
	chosen bool
	pick   func()
}

func pick(label string, chosen bool, apply func()) choice {
	return choice{label: label, chosen: chosen, pick: apply}
}

// choices draws a labeled group of buttons. The chosen value is the accent
// button; a tap writes the new value at once. The accessible label carries
// the group name, so "Dark" under Theme and under App icon stay distinct.
func (p *settingsPages) choices(c *ui.Context, title string, values []choice) {
	t := c.Theme()
	ui.Column(c).FillWidth().Gap(8).Children(func() {
		ui.Text(c, title).FontSize(13).FontWeight(600).TextColor(t.Text)
		ui.Row(c).FillWidth().Gap(8).Wrap().Children(func() {
			for _, value := range values {
				value := value
				var button ui.Element
				if value.chosen {
					button = kit.PrimaryButton(c, "", value.label)
				} else {
					button = kit.SecondaryButton(c, "", value.label)
				}
				button = button.Label(title + ": " + value.label)
				if button.Clicked() {
					value.pick()
				}
			}
		})
	})
}

// toggle draws a settings row that flips on a tap. The whole row is the
// target, so a phone tap on the label is enough; the state is spelled out as
// well as colored, which is what a tester and a screen reader both read.
func (p *settingsPages) toggle(c *ui.Context, label string, on bool, set func(bool)) {
	t := c.Theme()
	row := ui.ButtonBase(c).FillWidth().Padding(12, 14).Gap(12).AlignItems(ui.Center).
		Background(t.Surface).Radius(12).Label(label)
	row.Children(func() {
		ui.Text(c, label).Grow(1).MinWidth(0).TextColor(t.Text)
		if on {
			ui.Text(c, "On").FontSize(13).TextColor(t.Accent)
		} else {
			ui.Text(c, "Off").FontSize(13).TextColor(t.TextMuted)
		}
	})
	if row.Clicked() {
		set(!on)
	}
}

// stepper draws a titled integer with decrease and increase buttons.
func (p *settingsPages) stepper(c *ui.Context, title string, value, min, max, step int, set func(int)) {
	t := c.Theme()
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, fmt.Sprintf("%s: %d", title, value)).Grow(1).MinWidth(0).TextColor(t.Text)
		if kit.IconButton(c, icons.Minus, "Decrease "+title).Clicked() && value-step >= min {
			set(value - step)
		}
		if kit.IconButton(c, icons.Plus, "Increase "+title).Clicked() && value+step <= max {
			set(value + step)
		}
	})
}

func (p *settingsPages) updatePreferences(change func(*domainpreferences.Settings)) {
	if err := p.service.UpdatePreferences(change); err != nil {
		p.problem = "Could not save the setting."
		return
	}
	p.problem = ""
}

func (p *settingsPages) updateTerminal(change func(*domainterminal.Settings)) {
	if err := p.service.UpdateTerminal(change); err != nil {
		p.problem = "Could not save the setting."
		return
	}
	p.problem = ""
}
