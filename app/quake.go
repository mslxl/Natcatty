package app

import (
	"context"

	"github.com/egoist/mygo/ui"

	appdesktop "github.com/mslxl/natcatty/application/desktop"
	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/ui/dropdown"
	quakeui "github.com/mslxl/natcatty/ui/quake"
)

// quakeSessionOpener adapts the shared Session service to the Quake
// workspace's consumer-owned port. A local Shell opened here uses the same
// transport, logging, Command History, and notification path as the main
// shell.
type quakeSessionOpener struct {
	sessions *appsession.Service
}

func (o quakeSessionOpener) OpenLocal() (*appsession.Session, error) {
	if o.sessions == nil {
		return nil, quakeui.ErrNoSessions
	}
	return o.sessions.OpenLocal(context.Background())
}

// wireDropdownQuake routes the Dropdown Panel to the Quake Workspace while the
// Hotkey Mode is quake. The mode reader is live: a Settings change re-routes
// the same Panel's root content and top bar on the next frame, and the native
// window is never recreated. Other modes keep the neutral placeholder until
// their own renderers land.
func wireDropdownQuake(windows *windowManager, state *quakeui.State,
	mode func() appdesktop.HotkeyMode) {
	if windows == nil || state == nil {
		return
	}
	bar := quakeui.Bar(state)
	view := quakeui.View(state)
	windows.SetDropdownShowHook(func() {
		if mode() != appdesktop.HotkeyModeQuake {
			return
		}
		// The first show creates the local Shell; later shows reuse it.
		_ = state.Activate()
	})
	windows.SetDropdownBarProvider(func() func(*ui.Context) {
		return func(c *ui.Context) {
			if mode() == appdesktop.HotkeyModeQuake {
				bar(c)
			}
		}
	})
	windows.SetDropdownContentProvider(func() func(*ui.Context) {
		return func(c *ui.Context) {
			if mode() == appdesktop.HotkeyModeQuake {
				view(c)
				return
			}
			dropdown.Placeholder(c)
		}
	})
}
