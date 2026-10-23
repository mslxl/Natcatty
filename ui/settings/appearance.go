package settings

import (
	"math"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
	domain "github.com/mslxl/natcatty/domain/preferences"
	"github.com/mslxl/natcatty/ui/locales"
	uitheme "github.com/mslxl/natcatty/ui/theme"
)

// appearanceArea is the appearance form: window themes, accent, icon, opacity,
// and the terminal's color scheme. The window-theme catalogs open in a bounded
// popup, and the 184-scheme terminal list is the only inner scroll viewport,
// placed last so it cannot trap the wheel above the fields it follows.
func appearanceArea(c *ui.Context, state *PreferencesState, themes *ThemesState,
	m locales.Messages, t *ui.Theme) {
	sectionBody(c, t, func() {
		if state == nil {
			return
		}
		settings := state.Settings()

		themeLabels := []string{m.SettingsThemeSystem(), m.SettingsThemeLight(), m.SettingsThemeDark()}
		themeModes := []domain.ThemeMode{domain.ThemeSystem, domain.ThemeLight, domain.ThemeDark}
		themeLabel := themeLabels[indexOfModes(themeModes, settings.Appearance.ThemeMode)]
		fieldRow(c, state, m, t, domain.FieldThemeMode, m.SettingsThemeMode(), func() {
			if ui.Select(c, &themeLabel, themeLabels).Width(180).Changed() {
				state.Update(func(s *domain.Settings) {
					s.Appearance.ThemeMode = themeModes[indexOf(themeLabels, themeLabel)]
				})
				c.Invalidate()
			}
		})

		presetPicker(c, state, m, t, domain.FieldLightTheme, m.SettingsLightTheme(),
			domaintheme.LightPresets(), settings.Appearance.LightTheme, func(s *domain.Settings, id string) {
				s.Appearance.LightTheme = id
			})
		presetPicker(c, state, m, t, domain.FieldDarkTheme, m.SettingsDarkTheme(),
			domaintheme.DarkPresets(), settings.Appearance.DarkTheme, func(s *domain.Settings, id string) {
				s.Appearance.DarkTheme = id
			})

		accentPicker(c, state, m, t, settings)

		opacityPicker(c, state, m, t, settings)

		iconPicker(c, state, m, t, settings)

		fieldRow(c, state, m, t, domain.FieldUIFont, m.SettingsUIFont(), func() {
			if ui.TextInput(c, state.Field(domain.FieldUIFont)).Width(230).Changed() {
				state.Update(func(s *domain.Settings) {
					s.Appearance.UIFont = state.FieldValue(domain.FieldUIFont)
				})
				c.Invalidate()
			}
		})

		terminalPicker(c, state, themes, m, t, settings)

		fieldRow(c, state, m, t, domain.FieldTerminalFontFamily, m.SettingsTerminalFontFamily(), func() {
			if ui.TextInput(c, state.Field(domain.FieldTerminalFontFamily)).Width(230).Changed() {
				state.Update(func(s *domain.Settings) {
					s.Appearance.TerminalFontFamily = state.FieldValue(domain.FieldTerminalFontFamily)
				})
				c.Invalidate()
			}
		})
		size := settings.Appearance.TerminalFontSize
		fieldRow(c, state, m, t, domain.FieldTerminalFontSize, m.SettingsTerminalFontSize(), func() {
			if ui.NumberInput(c, &size, domain.MinTerminalFontSize, domain.MaxTerminalFontSize, 1).Width(110).Changed() {
				state.Update(func(s *domain.Settings) { s.Appearance.TerminalFontSize = size })
				c.Invalidate()
			}
		})

		groupReset(c, m, t, m.SettingsAppearanceTitle(), func() bool {
			return state.ResetGroup(domain.GroupAppearance)
		}, true)
		saveProblem(c, state, m, t)
	})
}

// presetPicker is one built-in window-theme catalog. The trigger previews the
// current theme with its own colors; the popup lists all 62 in a bounded
// scroll, so the catalog never stretches the form.
func presetPicker(c *ui.Context, state *PreferencesState, m locales.Messages, t *ui.Theme,
	field domain.Field, label string, presets []domaintheme.Preset, selected string,
	assign func(*domain.Settings, string)) {
	fieldRow(c, state, m, t, field, label, func() {
		current := selected
		sel := ui.SelectBase(c, &current)
		preset, _ := findPreset(presets, selected)
		sel.Trigger.Width(250).Height(32).Padding(4, 8).
			Border(1, t.Border).Background(uitheme.PresetBackground(preset)).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Box(c).Width(18).Height(18).Radius(3).
					Background(uitheme.PresetBackground(preset)).
					Border(1, t.TextMuted)
				ui.Text(c, preset.Name).FontSize(13).
					TextColor(uitheme.PresetForeground(preset))
			})
		})
		sel.Popup(func(panel ui.Element) {
			panel.Padding(4).Background(t.Surface).Border(1, t.Border)
			ui.Scroll(c).Height(260).Width(280).Children(func() {
				ui.Column(c).Gap(2).FillWidth().Children(func() {
					for _, option := range presets {
						option := option
						item := sel.Item(option.ID).Padding(4, 8).
							Background(uitheme.PresetBackground(option)).
							TextColor(uitheme.PresetForeground(option))
						if item.Highlighted() {
							item = item.Border(1, t.Accent)
						}
						item.Children(func() {
							ui.Text(c, option.Name).FontSize(13).
								TextColor(uitheme.PresetForeground(option))
						})
					}
				})
			})
		})
		if sel.Trigger.Changed() {
			state.Update(func(s *domain.Settings) { assign(s, current) })
			c.Invalidate()
		}
	})
}

// findPreset returns the preset with id from a light or dark catalog.
func findPreset(presets []domaintheme.Preset, id string) (domaintheme.Preset, bool) {
	for _, preset := range presets {
		if preset.ID == id {
			return preset, true
		}
	}
	return domaintheme.Preset{}, false
}

// accentPicker offers the original's eighteen presets and the free-form HSL
// token. A preset selects the custom-accent mode, because that is what the
// original did when a swatch was clicked.
func accentPicker(c *ui.Context, state *PreferencesState, m locales.Messages, t *ui.Theme,
	settings domain.Settings) {
	accent := settings.Appearance.AccentMode == domain.AccentCustom
	fieldRow(c, state, m, t, domain.FieldAccentMode, m.SettingsAccentMode(), func() {
		if ui.Checkbox(c, &accent, "").Changed() {
			state.Update(func(s *domain.Settings) {
				if accent {
					s.Appearance.AccentMode = domain.AccentCustom
				} else {
					s.Appearance.AccentMode = domain.AccentTheme
				}
			})
			c.Invalidate()
		}
	})
	ui.Field(c, m.SettingsAccentPresets(), func() {
		ui.Row(c).Wrap().Gap(6).FillWidth().Children(func() {
			for _, preset := range domaintheme.Accents() {
				preset := preset
				selected := settings.Appearance.AccentMode == domain.AccentCustom &&
					strings.EqualFold(settings.Appearance.AccentColor, preset.HSL)
				swatch := ui.Button(c, accentName(m, preset.Name)).Width(96).Height(28).
					Background(uitheme.AccentColor(preset.HSL)).
					TextColor(uitheme.AccentText(preset.HSL))
				if selected {
					swatch = swatch.Border(2, t.Text)
				} else {
					swatch = swatch.Border(1, t.Border)
				}
				if swatch.Clicked() {
					state.Update(func(s *domain.Settings) {
						s.Appearance.AccentMode = domain.AccentCustom
						s.Appearance.AccentColor = preset.HSL
					})
					c.Invalidate()
				}
			}
		})
	})
	fieldRow(c, state, m, t, domain.FieldAccentColor, m.SettingsAccentColor(), func() {
		if ui.TextInput(c, state.Field(domain.FieldAccentColor)).Width(230).Changed() {
			state.Update(func(s *domain.Settings) {
				s.Appearance.AccentColor = state.FieldValue(domain.FieldAccentColor)
			})
			c.Invalidate()
		}
	})
}

// opacityPicker pairs the continuous slider with the original's 100/85/70
// quick choices. The slider writes two decimal places, the precision the
// preference already stores.
func opacityPicker(c *ui.Context, state *PreferencesState, m locales.Messages, t *ui.Theme,
	settings domain.Settings) {
	opacity := settings.Appearance.WindowOpacity
	fieldRow(c, state, m, t, domain.FieldWindowOpacity, m.SettingsWindowOpacity(), func() {
		if ui.Slider(c, &opacity, domain.MinWindowOpacity, 1).Width(220).Changed() {
			value := math.Round(opacity*100) / 100
			state.Update(func(s *domain.Settings) { s.Appearance.WindowOpacity = value })
			c.Invalidate()
		}
		ui.Text(c, m.SettingsWindowOpacityValue(strconv.Itoa(int(math.Round(opacity*100))))).
			Width(42).TextColor(t.TextMuted)
		for _, value := range []float64{1, 0.85, 0.7} {
			value := value
			wanted := int(math.Round(value * 100))
			button := ui.Button(c, m.SettingsWindowOpacityValue(strconv.Itoa(wanted))).FontSize(11)
			if math.Abs(opacity-value) < 0.001 {
				button = button.Background(t.Accent).TextColor(t.AccentText)
			}
			if button.Clicked() {
				state.Update(func(s *domain.Settings) { s.Appearance.WindowOpacity = value })
				c.Invalidate()
			}
		}
	})
}

// iconPicker shows the original's three icon groups. The variants are named,
// not drawn: MyGo's taskbar and tray accept a PNG, but a themed icon preview
// would have to reproduce twelve 1024px assets in the settings surface.
func iconPicker(c *ui.Context, state *PreferencesState, m locales.Messages, t *ui.Theme,
	settings domain.Settings) {
	fieldRow(c, state, m, t, domain.FieldAppIcon, m.SettingsAppIcon(), func() {
		ui.Text(c, iconVariantName(m, settings.Appearance.AppIcon)).
			FontSize(11).TextColor(t.TextMuted)
	})
	ui.Column(c).Gap(6).FillWidth().Children(func() {
		for _, group := range domaintheme.IconGroupOrder() {
			ui.Text(c, iconGroupName(m, group)).FontSize(11).TextColor(t.TextMuted)
			ui.Row(c).Wrap().Gap(6).FillWidth().Children(func() {
				for _, variant := range domaintheme.IconVariantsInGroup(group) {
					variant := variant
					button := ui.Button(c, iconVariantName(m, variant.ID)).
						Width(112).Height(28).FontSize(12)
					if variant.ID == settings.Appearance.AppIcon {
						button = button.Background(t.Accent).TextColor(t.AccentText)
					}
					if button.Clicked() {
						state.Update(func(s *domain.Settings) { s.Appearance.AppIcon = variant.ID })
						c.Invalidate()
					}
				}
			})
		}
	})
}

// terminalPicker is the scheme list and its import control. Imported schemes
// are ordinary entries, and a successful import also selects one.
func terminalPicker(c *ui.Context, state *PreferencesState, themes *ThemesState,
	m locales.Messages, t *ui.Theme, settings domain.Settings) {
	if themes == nil {
		fieldRow(c, state, m, t, domain.FieldTerminalTheme, m.SettingsTerminalTheme(), func() {
			if ui.TextInput(c, state.Field(domain.FieldTerminalTheme)).Width(230).Changed() {
				state.Update(func(s *domain.Settings) {
					s.Appearance.TerminalTheme = state.FieldValue(domain.FieldTerminalTheme)
				})
				c.Invalidate()
			}
		})
		return
	}
	ui.Field(c, m.SettingsTerminalTheme(), func() {
		current := settings.Appearance.TerminalTheme
		sel := ui.SelectBase(c, &current)
		scheme, _ := findTerminalTheme(themes.Themes(), current)
		sel.Trigger.Width(250).Height(32).Padding(4, 8).
			Border(1, t.Border).Background(uitheme.TerminalBackground(scheme)).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Box(c).Width(18).Height(18).Radius(3).
					Background(uitheme.TerminalForeground(scheme)).Border(1, t.TextMuted)
				ui.Text(c, scheme.Name).FontSize(13).
					TextColor(uitheme.TerminalForeground(scheme))
			})
		})
		sel.Popup(func(panel ui.Element) {
			panel.Padding(4).Background(t.Surface).Border(1, t.Border)
			ui.Column(c).Gap(4).Width(290).Children(func() {
				if ui.TextInput(c, themes.Filter()).
					Placeholder(m.SettingsTerminalThemeFilter()).FillWidth().Changed() {
					c.Invalidate()
				}
				visible := themes.Filtered()
				if len(visible) == 0 {
					ui.Text(c, m.SettingsTerminalThemeNoResults(strings.TrimSpace(*themes.Filter()))).
						FontSize(12).TextColor(t.TextMuted)
					return
				}
				ui.Scroll(c).Height(260).FillWidth().Children(func() {
					ui.Column(c).Gap(2).FillWidth().Children(func() {
						for _, option := range visible {
							option := option
							item := sel.Item(option.ID).Padding(4, 8).
								Background(uitheme.TerminalBackground(option)).
								TextColor(uitheme.TerminalForeground(option))
							if item.Highlighted() {
								item = item.Border(1, t.Accent)
							}
							item.Children(func() {
								ui.Text(c, option.Name).FontSize(13).
									TextColor(uitheme.TerminalForeground(option))
							})
						}
					})
				})
			})
		})
		if sel.Trigger.Changed() {
			state.Update(func(s *domain.Settings) { s.Appearance.TerminalTheme = current })
			c.Invalidate()
		}
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			if ui.Button(c, m.SettingsTerminalThemeImport()).Clicked() {
				themes.RequestImport()
			}
			if !state.IsDefault(domain.FieldTerminalTheme) {
				if ui.Button(c, m.SettingsReset()).FontSize(11).Clicked() {
					state.ResetField(domain.FieldTerminalTheme)
					c.Invalidate()
				}
			}
			ui.Text(c, m.SettingsThemeCount(len(themes.Themes()))).
				FontSize(11).TextColor(t.TextMuted)
		})
		importStatus(c, themes, m, t)
	})
}

// findTerminalTheme returns the scheme with id, falling back to the default
// scheme so the trigger always shows a readable preview.
func findTerminalTheme(themes []domaintheme.TerminalTheme, id string) (domaintheme.TerminalTheme, bool) {
	for _, scheme := range themes {
		if scheme.ID == id {
			return scheme, true
		}
	}
	if fallback, ok := domaintheme.LookupTerminalTheme(id); ok {
		return fallback, true
	}
	fallback, _ := domaintheme.LookupTerminalTheme(domaintheme.DefaultTerminalTheme)
	return fallback, false
}

func importStatus(c *ui.Context, themes *ThemesState, m locales.Messages, t *ui.Theme) {
	switch themes.Problem() {
	case ThemeImportOK:
		ui.Text(c, m.SettingsTerminalThemeImported(themes.ImportedName())).
			FontSize(12).TextColor(t.Success)
	case ThemeImportInvalid:
		ui.Text(c, m.SettingsTerminalThemeImportInvalid()).FontSize(12).TextColor(t.Danger)
	case ThemeImportFailed:
		ui.Text(c, m.SettingsTerminalThemeImportFailed()).FontSize(12).TextColor(t.Danger)
	}
}

func accentName(m locales.Messages, name string) string {
	switch name {
	case "Sky":
		return m.SettingsAccentSky()
	case "Blue":
		return m.SettingsAccentBlue()
	case "Indigo":
		return m.SettingsAccentIndigo()
	case "Violet":
		return m.SettingsAccentViolet()
	case "Purple":
		return m.SettingsAccentPurple()
	case "Fuchsia":
		return m.SettingsAccentFuchsia()
	case "Pink":
		return m.SettingsAccentPink()
	case "Rose":
		return m.SettingsAccentRose()
	case "Red":
		return m.SettingsAccentRed()
	case "Orange":
		return m.SettingsAccentOrange()
	case "Amber":
		return m.SettingsAccentAmber()
	case "Yellow":
		return m.SettingsAccentYellow()
	case "Lime":
		return m.SettingsAccentLime()
	case "Green":
		return m.SettingsAccentGreen()
	case "Emerald":
		return m.SettingsAccentEmerald()
	case "Teal":
		return m.SettingsAccentTeal()
	case "Cyan":
		return m.SettingsAccentCyan()
	case "Slate":
		return m.SettingsAccentSlate()
	default:
		return name
	}
}

func iconVariantName(m locales.Messages, id string) string {
	switch id {
	case "original":
		return m.SettingsAppIconOriginal()
	case "bright":
		return m.SettingsAppIconBright()
	case "dark":
		return m.SettingsAppIconDark()
	case "colorful":
		return m.SettingsAppIconColorful()
	case "high-contrast":
		return m.SettingsAppIconHighContrast()
	case "white-navy":
		return m.SettingsAppIconWhiteNavy()
	case "white-sky":
		return m.SettingsAppIconWhiteSky()
	case "white-rose":
		return m.SettingsAppIconWhiteRose()
	case "white-emerald":
		return m.SettingsAppIconWhiteEmerald()
	case "white-amber":
		return m.SettingsAppIconWhiteAmber()
	case "white-violet":
		return m.SettingsAppIconWhiteViolet()
	case "rainbow":
		return m.SettingsAppIconRainbow()
	default:
		return id
	}
}

func iconGroupName(m locales.Messages, group domaintheme.IconGroup) string {
	switch group {
	case "classic":
		return m.SettingsAppIconGroupClassic()
	case "white":
		return m.SettingsAppIconGroupWhite()
	case "special":
		return m.SettingsAppIconGroupSpecial()
	default:
		return string(group)
	}
}
