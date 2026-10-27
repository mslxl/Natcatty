package settings

import (
	"github.com/egoist/mygo/ui"

	domainterminal "github.com/mslxl/natcatty/domain/terminal"
	"github.com/mslxl/natcatty/ui/locales"
	uitheme "github.com/mslxl/natcatty/ui/theme"
)

func terminalSection(c *ui.Context, state *TerminalState, m locales.Messages, t *ui.Theme) {
	settings := state.Settings()
	sectionBody(c, t, func() {
		ui.Text(c, m.SettingsTerminalLoggingTitle()).Bold()
		ui.Text(c, m.SettingsTerminalLoggingDescription()).FontSize(12).TextColor(t.TextMuted)

		enabled := settings.LoggingEnabled
		if ui.Checkbox(c, &enabled, m.SettingsLogEnabled()).Changed() {
			state.SetLoggingEnabled(enabled)
			c.Invalidate()
		}

		ui.Field(c, m.SettingsLogDirectory(), func() {
			input := ui.TextInput(c, state.Directory()).Width(320)
			if input.Changed() {
				state.SetLogDirectory(*state.Directory())
				c.Invalidate()
			}
		})

		formatLabels, formats := logFormatOptions(m)
		formatLabel := labelForFormat(formatLabels, formats, settings.LogFormat)
		ui.Field(c, m.SettingsLogFormat(), func() {
			if ui.Select(c, &formatLabel, formatLabels).Width(160).Changed() {
				state.SetLogFormat(formats[indexOf(formatLabels, formatLabel)])
				c.Invalidate()
			}
		})

		timestamps := settings.LogTimestamps
		if ui.Checkbox(c, &timestamps, m.SettingsLogTimestamps()).Changed() {
			state.SetLogTimestamps(timestamps)
			c.Invalidate()
		}

		ui.Text(c, m.SettingsOSC52Title()).Bold()
		ui.Text(c, m.SettingsOSC52Description()).FontSize(12).TextColor(t.TextMuted)
		accessLabels, accesses := osc52Options(m)
		accessLabel := labelForAccess(accessLabels, accesses, settings.OSC52)
		ui.Field(c, m.SettingsOSC52Title(), func() {
			if ui.Select(c, &accessLabel, accessLabels).Width(180).Changed() {
				state.SetOSC52(accesses[indexOf(accessLabels, accessLabel)])
				c.Invalidate()
			}
		})

		ui.Text(c, m.SettingsOSCNotificationsTitle()).Bold()
		ui.Text(c, m.SettingsOSCNotificationsDescription()).FontSize(12).TextColor(t.TextMuted)
		modeLabels, modes := notificationOptions(m)
		modeLabel := labelForMode(modeLabels, modes, settings.OSCNotifications)
		ui.Field(c, m.SettingsOSCNotificationsTitle(), func() {
			if ui.Select(c, &modeLabel, modeLabels).Width(180).Changed() {
				state.SetOSCNotifications(modes[indexOf(modeLabels, modeLabel)])
				c.Invalidate()
			}
		})

		renderPowerUxSection(c, state, settings, m, t)
		renderRetentionSection(c, state, settings, m, t)

		ui.Text(c, m.SettingsTerminalEncoding()).Bold()
		encodingLabels := make([]string, 0, len(domainterminal.SupportedEncodings()))
		encodings := domainterminal.SupportedEncodings()
		for _, encoding := range encodings {
			encodingLabels = append(encodingLabels, locales.EncodingLabel(m, encoding))
		}
		encodingLabel := locales.EncodingLabel(m, settings.Encoding)
		ui.Field(c, m.SettingsTerminalEncoding(), func() {
			if ui.Select(c, &encodingLabel, encodingLabels).Width(160).Changed() {
				for index, label := range encodingLabels {
					if label == encodingLabel {
						state.SetEncoding(encodings[index])
						break
					}
				}
				c.Invalidate()
			}
		})

		ui.Text(c, m.SettingsPasteProtection()).Bold()
		protection := settings.PasteProtection
		if ui.Checkbox(c, &protection, m.SettingsPasteProtection()).Changed() {
			state.SetPasteProtection(protection)
			c.Invalidate()
		}
		minLines := float64(domainterminal.NormalizePasteMinLines(settings.PasteMinLines))
		ui.Field(c, m.SettingsPasteMinLines(), func() {
			if ui.NumberInput(c, &minLines, 1, 1000, 1).Width(96).Changed() {
				state.SetPasteMinLines(int(minLines))
				c.Invalidate()
			}
		})

		dropLabels := []string{m.SettingsDropInsert(), m.SettingsDropUpload()}
		dropActions := []domainterminal.DropAction{domainterminal.DropInsert, domainterminal.DropUpload}
		dropLabel := dropLabels[0]
		if settings.DropAction == domainterminal.DropUpload {
			dropLabel = dropLabels[1]
		}
		ui.Field(c, m.SettingsDropAction(), func() {
			if ui.Select(c, &dropLabel, dropLabels).Width(180).Changed() {
				for index, label := range dropLabels {
					if label == dropLabel {
						state.SetDropAction(dropActions[index])
						break
					}
				}
				c.Invalidate()
			}
		})

		if state.Problem() {
			ui.Text(c, m.SettingsTerminalSaveFailed()).TextColor(uitheme.DangerText(t)).FontSize(12)
		}
	})
}

// renderPowerUxSection draws autocomplete, keyword highlighting, and the
// Session statistics switch. It is separate from the logging and encoding
// controls so each group changes for its own reasons.
func renderPowerUxSection(c *ui.Context, state *TerminalState,
	settings domainterminal.Settings, m locales.Messages, t *ui.Theme) {
	ui.Text(c, m.SettingsAutocompleteTitle()).Bold()
	ui.Text(c, m.SettingsAutocompleteDescription()).FontSize(12).TextColor(t.TextMuted)

	autocomplete := settings.AutocompleteEnabled
	if ui.Checkbox(c, &autocomplete, m.SettingsAutocompleteEnabled()).Changed() {
		state.SetAutocompleteEnabled(autocomplete)
		c.Invalidate()
	}
	popup := settings.AutocompletePopupMenu
	if ui.Checkbox(c, &popup, m.SettingsAutocompletePopupMenu()).Changed() {
		state.SetAutocompletePopupMenu(popup)
		c.Invalidate()
	}
	ghost := settings.AutocompleteGhostText
	if ui.Checkbox(c, &ghost, m.SettingsAutocompleteGhostText()).Changed() {
		state.SetAutocompleteGhostText(ghost)
		c.Invalidate()
	}
	minChars := float64(domainterminal.NormalizeAutocompleteMinChars(settings.AutocompleteMinChars))
	ui.Field(c, m.SettingsAutocompleteMinChars(), func() {
		if ui.NumberInput(c, &minChars, 1,
			float64(domainterminal.MaxAutocompleteMinChars), 1).Width(96).Changed() {
			state.SetAutocompleteMinChars(int(minChars))
			c.Invalidate()
		}
	})
	maxSuggestions := float64(domainterminal.NormalizeAutocompleteMaxSuggestions(settings.AutocompleteMaxSuggestions))
	ui.Field(c, m.SettingsAutocompleteMaxSuggestions(), func() {
		if ui.NumberInput(c, &maxSuggestions, 1,
			float64(domainterminal.MaxAutocompleteMaxSuggestions), 1).Width(96).Changed() {
			state.SetAutocompleteMaxSuggestions(int(maxSuggestions))
			c.Invalidate()
		}
	})
	debounce := float64(domainterminal.NormalizeAutocompleteDebounce(settings.AutocompleteDebounceMs))
	ui.Field(c, m.SettingsAutocompleteDebounce(), func() {
		if ui.NumberInput(c, &debounce, 0, 5000, 10).Width(96).Changed() {
			state.SetAutocompleteDebounceMs(int(debounce))
			c.Invalidate()
		}
	})
	scopeLabels, scopes := autocompleteScopeOptions(m)
	scopeLabel := labelForScope(scopeLabels, scopes, settings.AutocompleteHistoryScope)
	ui.Field(c, m.SettingsAutocompleteHistoryScope(), func() {
		if ui.Select(c, &scopeLabel, scopeLabels).Width(180).Changed() {
			state.SetAutocompleteHistoryScope(scopes[indexOf(scopeLabels, scopeLabel)])
			c.Invalidate()
		}
	})

	ui.Text(c, m.SettingsKeywordHighlightTitle()).Bold()
	ui.Text(c, m.SettingsKeywordHighlightDescription()).FontSize(12).TextColor(t.TextMuted)
	highlight := settings.KeywordHighlightEnabled
	if ui.Checkbox(c, &highlight, m.SettingsKeywordHighlightEnabled()).Changed() {
		state.SetKeywordHighlightEnabled(highlight)
		c.Invalidate()
	}
	for _, rule := range settings.KeywordHighlightRules {
		rule := rule
		ui.Row(c).FillWidth().Gap(12).AlignItems(ui.Start).Children(func() {
			ruleEnabled := rule.Enabled
			ui.Column(c).Width(180).Children(func() {
				if ui.Checkbox(c, &ruleEnabled, keywordRuleLabel(m, rule.ID)).Changed() {
					state.SetKeywordHighlightRuleEnabled(rule.ID, ruleEnabled)
					c.Invalidate()
				}
				ui.Text(c, rule.Color).FontSize(10).TextColor(t.TextMuted)
			})
			ui.Field(c, m.SettingsKeywordHighlightPatterns(), func() {
				patterns := state.RulePatterns(rule.ID)
				if ui.TextArea(c, patterns).Width(420).Height(56).Changed() {
					if !state.SetRulePatterns(rule.ID, *patterns) {
						c.Invalidate()
					}
				}
			})
		})
	}

	showStats := settings.ShowSessionStats
	if ui.Checkbox(c, &showStats, m.SettingsShowSessionStats()).Changed() {
		state.SetShowSessionStats(showStats)
		c.Invalidate()
	}

}

// renderRetentionSection draws the connection log and command history
// retention bounds. The two count fields and the two age fields are edited
// independently; each save carries the other field's current value so an
// edit never silently resets its neighbour.
func renderRetentionSection(c *ui.Context, state *TerminalState,
	settings domainterminal.Settings, m locales.Messages, t *ui.Theme) {
	ui.Text(c, m.SettingsRetentionTitle()).Bold()
	ui.Text(c, m.SettingsRetentionDescription()).FontSize(12).TextColor(t.TextMuted)

	logCount := float64(domainterminal.NormalizeRetentionCount(settings.ConnectionLogRetentionCount))
	ui.Field(c, m.SettingsConnectionLogRetentionCount(), func() {
		if ui.NumberInput(c, &logCount, 0,
			float64(domainterminal.MaxRetentionCount), 100).Width(120).Changed() {
			state.SetConnectionLogRetention(int(logCount), settings.ConnectionLogRetentionDays)
			c.Invalidate()
		}
	})
	logDays := float64(domainterminal.NormalizeRetentionDays(settings.ConnectionLogRetentionDays))
	ui.Field(c, m.SettingsConnectionLogRetentionDays(), func() {
		if ui.NumberInput(c, &logDays, 0,
			float64(domainterminal.MaxRetentionDays), 1).Width(120).Changed() {
			state.SetConnectionLogRetention(settings.ConnectionLogRetentionCount, int(logDays))
			c.Invalidate()
		}
	})

	historyCount := float64(domainterminal.NormalizeRetentionCount(settings.HistoryRetentionCount))
	ui.Field(c, m.SettingsHistoryRetentionCount(), func() {
		if ui.NumberInput(c, &historyCount, 0,
			float64(domainterminal.MaxRetentionCount), 100).Width(120).Changed() {
			state.SetHistoryRetention(int(historyCount), settings.HistoryRetentionDays)
			c.Invalidate()
		}
	})
	historyDays := float64(domainterminal.NormalizeRetentionDays(settings.HistoryRetentionDays))
	ui.Field(c, m.SettingsHistoryRetentionDays(), func() {
		if ui.NumberInput(c, &historyDays, 0,
			float64(domainterminal.MaxRetentionDays), 1).Width(120).Changed() {
			state.SetHistoryRetention(settings.HistoryRetentionCount, int(historyDays))
			c.Invalidate()
		}
	})
}

func logFormatOptions(m locales.Messages) ([]string, []domainterminal.LogFormat) {
	return []string{m.SessionLogFormatText(), m.SessionLogFormatRaw(), m.SessionLogFormatHTML()},
		[]domainterminal.LogFormat{
			domainterminal.LogFormatText,
			domainterminal.LogFormatRaw,
			domainterminal.LogFormatHTML,
		}
}

func osc52Options(m locales.Messages) ([]string, []domainterminal.ClipboardAccess) {
	return []string{
			m.SettingsOSC52Off(),
			m.SettingsOSC52WriteOnly(),
			m.SettingsOSC52ReadWrite(),
			m.SettingsOSC52Prompt(),
		},
		[]domainterminal.ClipboardAccess{
			domainterminal.ClipboardOff,
			domainterminal.ClipboardWriteOnly,
			domainterminal.ClipboardReadWrite,
			domainterminal.ClipboardPrompt,
		}
}

func notificationOptions(m locales.Messages) ([]string, []domainterminal.NotificationMode) {
	return []string{
			m.SettingsOSCNotificationsOff(),
			m.SettingsOSCNotificationsUnfocused(),
			m.SettingsOSCNotificationsAlways(),
		},
		[]domainterminal.NotificationMode{
			domainterminal.NotificationOff,
			domainterminal.NotificationUnfocused,
			domainterminal.NotificationAlways,
		}
}

// keywordRuleLabel resolves a rule's display name. The domain catalog carries
// machine IDs only, as ADR-0009 requires; an unknown ID (a user rule) shows as
// itself.
func keywordRuleLabel(m locales.Messages, id string) string {
	switch id {
	case "error":
		return m.SettingsKeywordHighlightRuleError()
	case "warning":
		return m.SettingsKeywordHighlightRuleWarning()
	case "ok":
		return m.SettingsKeywordHighlightRuleOK()
	case "info":
		return m.SettingsKeywordHighlightRuleInfo()
	case "debug":
		return m.SettingsKeywordHighlightRuleDebug()
	case "ip-mac":
		return m.SettingsKeywordHighlightRuleIPMAC()
	case "unix-path":
		return m.SettingsKeywordHighlightRuleUnixPath()
	default:
		return id
	}
}

func autocompleteScopeOptions(m locales.Messages) ([]string, []domainterminal.AutocompleteHistoryScope) {
	return []string{
			m.SettingsAutocompleteScopeHost(),
			m.SettingsAutocompleteScopeGlobal(),
		},
		[]domainterminal.AutocompleteHistoryScope{
			domainterminal.AutocompleteHistoryHost,
			domainterminal.AutocompleteHistoryGlobal,
		}
}

func labelForScope(labels []string, scopes []domainterminal.AutocompleteHistoryScope,
	scope domainterminal.AutocompleteHistoryScope) string {
	for index, candidate := range scopes {
		if candidate == scope {
			return labels[index]
		}
	}
	return labels[0]
}

func indexOf(labels []string, label string) int {
	for index, candidate := range labels {
		if candidate == label {
			return index
		}
	}
	return 0
}

func labelForFormat(labels []string, formats []domainterminal.LogFormat, format domainterminal.LogFormat) string {
	for index, candidate := range formats {
		if candidate == format {
			return labels[index]
		}
	}
	return labels[0]
}

func labelForAccess(labels []string, accesses []domainterminal.ClipboardAccess, access domainterminal.ClipboardAccess) string {
	for index, candidate := range accesses {
		if candidate == access {
			return labels[index]
		}
	}
	return labels[1]
}

func labelForMode(labels []string, modes []domainterminal.NotificationMode, mode domainterminal.NotificationMode) string {
	for index, candidate := range modes {
		if candidate == mode {
			return labels[index]
		}
	}
	return labels[2]
}
