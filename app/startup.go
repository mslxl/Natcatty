package app

import (
	"context"

	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
)

// startupShell is the part of the shell the launch landing drives.
type startupShell interface {
	Restore(context.Context)
	TabCount() int
	ShowMonitor()
	ShowVault()
	NewTab() error
}

// applyStartupLanding shows the surface the Layout asks for once the main
// window is ready. Restore wins when it is on and there is a Workspace to
// restore; with nothing to restore, the landing still decides.
func applyStartupLanding(layout domainpreferences.Layout, shell startupShell) {
	if layout.RestorePreviousSession {
		shell.Restore(context.Background())
		if shell.TabCount() > 0 {
			return
		}
	}
	switch layout.StartupLanding {
	case domainpreferences.StartupMonitoring:
		shell.ShowMonitor()
	case domainpreferences.StartupTerminal, domainpreferences.StartupLast:
		// StartupLast keeps its historical behavior of opening a local shell.
		_ = shell.NewTab()
	default:
		// StartupVault, and any value this build does not know, shows the
		// Vault rather than inventing a surface.
		shell.ShowVault()
	}
}
