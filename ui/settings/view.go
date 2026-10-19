// Package settings builds the singleton Settings window. ADR 0006 keeps
// Settings out of the main window's root surfaces, so it has its own window.
package settings

import (
	"slices"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/application/desktop"
	"github.com/mslxl/natcatty/domain/applock"
	"github.com/mslxl/natcatty/ui/locales"
)

// View builds the Settings window's native view with App Lock only. The app
// uses ViewWithDesktop.
func View(lock *AppLockState) func(*ui.Context) {
	return ViewWithDesktop(lock, nil)
}

// ViewWithDesktop builds the Settings window with the desktop integration
// sections and, when given, the terminal behavior section. The content
// scrolls, so the layout holds at the minimum window size.
func ViewWithDesktop(lock *AppLockState, desktopState *DesktopState, terminalStates ...*TerminalState) func(*ui.Context) {
	return func(c *ui.Context) {
		m := locales.In(c.Locale())
		t := c.Theme()

		ui.Column(c).Fill().Padding(24).Gap(16).Children(func() {
			ui.Text(c, m.NavSettings()).FontSize(24).Bold()
			ui.Scroll(c).Grow(1).Children(func() {
				ui.Column(c).Gap(16).Children(func() {
					if len(terminalStates) > 0 && terminalStates[0] != nil {
						terminalSection(c, terminalStates[0], m, t)
					}
					if desktopState != nil {
						desktopSections(c, desktopState, m, t)
					}
					appLockSection(c, lock, m, t)
				})
			})
		})
	}
}

func desktopSections(c *ui.Context, state *DesktopState, m locales.Messages, t *ui.Theme) {
	caps := state.Capabilities()
	settings := state.Settings()
	autostartSection(c, state, settings, caps, m, t)
	hotkeySection(c, state, settings, caps, m, t)
	traySection(c, state, settings, caps, m, t)
	deepLinkSection(c, state, settings, m, t)
	notificationSection(c, state, settings, caps, m, t)
	desktopProblem(c, state, m, t)
}

func autostartSection(c *ui.Context, state *DesktopState, settings desktop.Settings,
	caps desktop.Capabilities, m locales.Messages, t *ui.Theme) {
	section(c, m.SettingsAutostartTitle(), m.SettingsAutostartDescription(), t, func() {
		if !caps.AutoStart {
			ui.Text(c, m.SettingsAutostartUnsupported()).TextColor(t.TextMuted)
			return
		}
		enabled := settings.AutoStart
		if ui.Checkbox(c, &enabled, m.SettingsAutostartEnabled()).Changed() {
			state.SetAutoStart(enabled)
			c.Invalidate()
		}
	})
}

func hotkeySection(c *ui.Context, state *DesktopState, settings desktop.Settings,
	caps desktop.Capabilities, m locales.Messages, t *ui.Theme) {
	section(c, m.SettingsHotkeyTitle(), m.SettingsHotkeyDescription(), t, func() {
		if !caps.GlobalHotkey {
			ui.Text(c, m.SettingsHotkeyUnsupported()).TextColor(t.TextMuted)
			return
		}
		enabled := settings.GlobalHotkeyEnabled
		if ui.Checkbox(c, &enabled, m.SettingsHotkeyEnabled()).Changed() {
			state.SetGlobalHotkeyEnabled(enabled)
			c.Invalidate()
		}
		ui.Field(c, m.SettingsHotkeyAccelerator(), func() {
			ui.TextInput(c, state.Hotkey()).Width(220)
		})
		ui.Row(c).Gap(8).Children(func() {
			if ui.PrimaryButton(c, m.SettingsHotkeyApply()).Clicked() {
				state.ApplyHotkey()
				c.Invalidate()
			}
			if ui.Button(c, m.SettingsHotkeyReset()).Clicked() {
				state.ResetHotkey()
				c.Invalidate()
			}
		})
		// The registration failure belongs next to the control that caused
		// it, not at the bottom of the scroll area.
		if state.Problem() == DesktopProblemHotkey {
			ui.Text(c, m.SettingsHotkeyProblem()).TextColor(t.Danger)
		}
	})
}

func traySection(c *ui.Context, state *DesktopState, settings desktop.Settings,
	caps desktop.Capabilities, m locales.Messages, t *ui.Theme) {
	section(c, m.SettingsTrayTitle(), m.SettingsTrayDescription(), t, func() {
		if !caps.Tray {
			ui.Text(c, m.SettingsTrayUnsupported()).TextColor(t.TextMuted)
			return
		}
		show := settings.ShowTrayIcon
		if ui.Checkbox(c, &show, m.SettingsShowTrayIcon()).Changed() {
			state.SetShowTrayIcon(show)
			c.Invalidate()
		}
		closeToTray := settings.CloseToTray
		if ui.Checkbox(c, &closeToTray, m.SettingsCloseToTray()).Changed() {
			state.SetCloseToTray(closeToTray)
			c.Invalidate()
		}
	})
}

func deepLinkSection(c *ui.Context, state *DesktopState, settings desktop.Settings,
	m locales.Messages, t *ui.Theme) {
	section(c, m.SettingsDeepLinksTitle(), m.SettingsDeepLinksDescription(), t, func() {
		enabled := settings.SSHDeepLinks
		if ui.Checkbox(c, &enabled, m.SettingsSSHDeepLinks()).Changed() {
			state.SetSSHDeepLinks(enabled)
			c.Invalidate()
		}
	})
}

func notificationSection(c *ui.Context, state *DesktopState, settings desktop.Settings,
	caps desktop.Capabilities, m locales.Messages, t *ui.Theme) {
	section(c, m.SettingsNotificationsTitle(), m.SettingsNotificationsDescription(), t, func() {
		if !caps.Notifications {
			ui.Text(c, m.SettingsNotificationsUnsupported()).TextColor(t.TextMuted)
			return
		}
		notificationToggle(c, state, settings.NotifySessionEnded, m.SettingsNotifySessionEnded(),
			desktop.EventSessionEnded)
		notificationToggle(c, state, settings.NotifyConnectionFailed, m.SettingsNotifyConnectionFailed(),
			desktop.EventConnectionFailed)
		notificationToggle(c, state, settings.NotifyForwardingFailed, m.SettingsNotifyForwardingFailed(),
			desktop.EventForwardingFailed)
	})
}

func notificationToggle(c *ui.Context, state *DesktopState, value bool, label string, event desktop.Event) {
	enabled := value
	if ui.Checkbox(c, &enabled, label).Changed() {
		state.SetNotification(event, enabled)
		c.Invalidate()
	}
}

func section(c *ui.Context, title, description string, t *ui.Theme, build func()) {
	ui.Column(c).Padding(16).Gap(10).Background(t.Surface).Children(func() {
		ui.Text(c, title).FontSize(18).Bold()
		ui.Text(c, description).FontSize(12).TextColor(t.TextMuted)
		build()
	})
}

func appLockSection(c *ui.Context, lock *AppLockState, m locales.Messages, t *ui.Theme) {
	snapshot := lock.Snapshot()

	section(c, m.SettingsAppLockTitle(), m.SettingsAppLockDescription(), t, func() {
		if !snapshot.Enabled {
			enableSection(c, lock, m)
			return
		}

		ui.Text(c, m.SettingsAppLockEnabled()).Bold()
		timeoutField(c, lock, snapshot.TimeoutMinutes, m)
		ui.Field(c, m.SettingsAppLockCurrentPassword(), func() {
			ui.TextInput(c, lock.CurrentPassword()).Password().Width(240)
		})
		ui.Row(c).Gap(8).Children(func() {
			if ui.Button(c, m.SettingsAppLockDisable()).Clicked() {
				lock.Disable()
				c.Invalidate()
			}
			if ui.Button(c, m.SettingsAppLockLockNow()).Clicked() {
				lock.LockNow()
				c.Invalidate()
			}
		})
		appLockProblem(c, lock, t)
	})
}

func enableSection(c *ui.Context, lock *AppLockState, m locales.Messages) {
	ui.Text(c, m.SettingsAppLockDisabled()).TextColor(c.Theme().TextMuted)
	ui.Field(c, m.SettingsAppLockPassword(), func() {
		ui.TextInput(c, lock.Password()).Password().Width(240)
	})
	ui.Field(c, m.SettingsAppLockConfirm(), func() {
		ui.TextInput(c, lock.Confirmation()).Password().Width(240)
	})
	if ui.PrimaryButton(c, m.SettingsAppLockEnable()).Clicked() {
		lock.Enable()
		c.Invalidate()
	}
	appLockProblem(c, lock, c.Theme())
}

func timeoutField(c *ui.Context, lock *AppLockState, current int, m locales.Messages) {
	options := applock.TimeoutOptions
	labels := make([]string, len(options))
	for i, minutes := range options {
		labels[i] = timeoutLabel(m, minutes)
	}
	selected := timeoutLabel(m, current)
	ui.Field(c, m.SettingsAppLockTimeout(), func() {
		if ui.Select(c, &selected, labels).Width(180).Changed() {
			if index := slices.Index(labels, selected); index >= 0 {
				lock.SetTimeout(options[index])
				c.Invalidate()
			}
		}
	})
}

func timeoutLabel(m locales.Messages, minutes int) string {
	if minutes == 0 {
		return m.SettingsAppLockTimeoutNever()
	}
	return m.SettingsAppLockTimeoutMinutes(minutes)
}

func appLockProblem(c *ui.Context, lock *AppLockState, t *ui.Theme) {
	message := problemText(locales.In(c.Locale()), lock.Problem())
	if message == "" {
		return
	}
	ui.Text(c, message).TextColor(t.Danger)
}

func desktopProblem(c *ui.Context, state *DesktopState, m locales.Messages, t *ui.Theme) {
	var message string
	switch state.Problem() {
	case DesktopProblemSave:
		message = m.SettingsDesktopSaveFailed()
	case DesktopProblemApply:
		message = m.SettingsDesktopUpdateFailed()
	}
	if message != "" {
		ui.Text(c, message).TextColor(t.Danger)
	}
}

func problemText(m locales.Messages, problem AppLockProblem) string {
	switch problem {
	case AppLockProblemEmpty:
		return m.SettingsAppLockProblemEmpty()
	case AppLockProblemMismatch:
		return m.SettingsAppLockProblemMismatch()
	case AppLockProblemIncorrect:
		return m.SettingsAppLockProblemIncorrect()
	case AppLockProblemFailed:
		return m.SettingsAppLockProblemFailed()
	}
	return ""
}
