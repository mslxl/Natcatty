//go:build ios || android

// The phone composition root. Phones have no desktop chrome and no local
// shell: this root builds the four-section shell and nothing that a phone
// cannot run. The desktop root in app.go is excluded by build tags, so
// desktop wiring (tray, menus, the single-instance lock, the browser, MCP,
// SFTP panels, local and serial transports) is not linked into a phone build.
//
// Connections arrive here in the Session ticket: the shell is deliberately
// empty of transports until then.
package app

import (
	"github.com/egoist/mygo"

	"github.com/mslxl/natcatty/ui/phoneshell"
)

// Run starts the phone app and blocks until it quits.
func Run() error {
	shell := phoneshell.New()
	var showErr error
	mygo.App.WhenReady(func() {
		if err := shell.Show(); err != nil {
			showErr = err
			mygo.App.Quit()
		}
	})
	err := mygo.App.Run()
	if showErr != nil {
		return showErr
	}
	return err
}
