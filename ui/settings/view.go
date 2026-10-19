// Package settings builds the singleton Settings window. ADR 0006 keeps
// Settings out of the main window's root surfaces, so it has its own window.
package settings

import (
	"slices"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/domain/applock"
	"github.com/mslxl/natcatty/ui/locales"
)

// View builds the Settings window's native view.
func View(lock *AppLockState) func(*ui.Context) {
	return func(c *ui.Context) {
		m := locales.In(c.Locale())
		t := c.Theme()

		ui.Column(c).Fill().Padding(24).Gap(16).Children(func() {
			ui.Text(c, m.NavSettings()).FontSize(24).Bold()
			appLockSection(c, lock, m, t)
		})
	}
}

func appLockSection(c *ui.Context, lock *AppLockState, m locales.Messages, t *ui.Theme) {
	snapshot := lock.Snapshot()

	ui.Column(c).Fill().Padding(16).Gap(10).Background(t.Surface).Children(func() {
		ui.Text(c, m.SettingsAppLockTitle()).FontSize(18).Bold()
		ui.Text(c, m.SettingsAppLockDescription()).FontSize(12).TextColor(t.TextMuted)

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
