//go:build selftest

package sessionui

import (
	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
	"github.com/mslxl/natcatty/ui/locales"
)

// RenderSelftestChrome renders the production Session chrome from a synthetic
// snapshot. The self-test uses it to prove that logging controls, command and
// activity indicators, the transfer gate, and the clipboard prompt lay out
// inside the real default and minimum window sizes. It is built only under the
// selftest tag.
func RenderSelftestChrome(c *ui.Context, snapshot appsession.Snapshot,
	clipboardPending bool, directory string) {
	format := snapshot.Logging.Format
	if !validLogFormat(format) {
		format = domainterminal.LogFormatText
	}
	renderChrome(c, chromeModel{
		Command:          snapshot.Command,
		Busy:             snapshot.Busy,
		Transfer:         snapshot.Transfer,
		Logging:          snapshot.Logging,
		ClipboardPending: clipboardPending,
		LogFormat:        format,
		LogDirectory:     directory,
	}, locales.In(c.Locale()), c.Theme(), chromeActions{})
}
