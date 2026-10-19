//go:build selftest

package shell

import (
	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/shell/state"
)

// RenderSelftestInputs renders the production broadcast and compose controls
// over a padded background so the selftest can measure them at the default and
// minimum window sizes without opening a native terminal.
func RenderSelftestInputs(c *ui.Context, s *state.State) {
	t := c.Theme()
	m := locales.In(c.Locale())
	ui.Column(c).Fill().Padding(16).Gap(12).Background(t.Background).Children(func() {
		if s.TabCount() == 0 {
			ui.Text(c, m.TrayNoSessions()).TextColor(t.TextMuted)
			return
		}
		terminalInputs(c, s, m, t)
	})
}
