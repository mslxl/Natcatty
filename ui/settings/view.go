// Package settings builds the singleton Settings window. ADR 0006 keeps
// Settings out of the main window's root surfaces, so it has its own window.
package settings

import (
	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/locales"
)

// View builds the Settings window's native view.
func View(c *ui.Context) {
	m := locales.In(c.Locale())
	t := c.Theme()

	ui.Column(c).Fill().Padding(24).Gap(16).Children(func() {
		ui.Text(c, m.NavSettings()).FontSize(24).Bold()
		ui.Text(c, m.SettingsPlaceholder()).TextColor(t.TextMuted)
	})
}
