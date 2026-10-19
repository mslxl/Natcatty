package settings

import (
	"github.com/egoist/mygo/ui"

	domainterminal "github.com/mslxl/natcatty/domain/terminal"
	"github.com/mslxl/natcatty/ui/locales"
)

func terminalSection(c *ui.Context, state *TerminalState, m locales.Messages, t *ui.Theme) {
	settings := state.Settings()
	section(c, m.SettingsTerminalTitle(), m.SettingsTerminalDescription(), t, func() {
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

		if state.Problem() {
			ui.Text(c, m.SettingsTerminalSaveFailed()).TextColor(t.Danger).FontSize(12)
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
