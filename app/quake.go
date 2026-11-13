package app

import (
	"context"

	"github.com/egoist/mygo/ui"

	appdesktop "github.com/mslxl/natcatty/application/desktop"
	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/ui/dropdown"
	quakeui "github.com/mslxl/natcatty/ui/quake"
	"github.com/mslxl/natcatty/ui/shell"
	shellstate "github.com/mslxl/natcatty/ui/shell/state"
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

// wireDropdownQuake routes the Dropdown Panel by Hotkey Mode and Quake Source.
// The readers are live: a Settings change re-routes the same Panel's root
// content and top bar on the next frame, and the native window is never
// recreated.
//
// Quake with source own renders its private workspace and leaves the main
// window visible. Quake with source follow renders the main Workspace's active
// Tab in the Panel and transfers the Surface ownership to it. Top-down mode
// transfers ownership too; its own renderer lands with a later ticket, so it
// shows the neutral placeholder. Other modes keep the placeholder.
func wireDropdownQuake(windows *windowManager, state *quakeui.State, shellState *shellstate.State,
	mode func() appdesktop.HotkeyMode, source func() appdesktop.QuakeSource, openMain func()) {
	if windows == nil || state == nil {
		return
	}
	quakeBar := quakeui.Bar(state)
	quakeView := quakeui.View(state)
	var followBar, followView func(*ui.Context)
	if shellState != nil {
		followBar = shell.FollowBar(shellState)
		followView = shell.FollowView(shellState, shell.WithFollowOpenMain(openMain))
	}
	// sourceOrDefault keeps old wiring that predates the setting on the own
	// workspace, which was the only Quake renderer.
	sourceOrDefault := func() appdesktop.QuakeSource {
		if source == nil {
			return appdesktop.QuakeSourceOwn
		}
		return source()
	}
	quakeOwn := func() bool {
		return mode() == appdesktop.HotkeyModeQuake && sourceOrDefault() == appdesktop.QuakeSourceOwn
	}
	quakeFollow := func() bool {
		return mode() == appdesktop.HotkeyModeQuake && sourceOrDefault() == appdesktop.QuakeSourceFollow
	}
	windows.SetDropdownShowHook(func() {
		if !quakeOwn() {
			return
		}
		// The first show creates the local Shell; later shows reuse it. Follow
		// mode must not create a Quake workspace: its content is the main
		// Workspace, which the user already owns.
		_ = state.Activate()
	})
	windows.SetDropdownBarProvider(func() func(*ui.Context) {
		return func(c *ui.Context) {
			switch {
			case quakeOwn():
				quakeBar(c)
			case quakeFollow() && followBar != nil:
				followBar(c)
			}
		}
	})
	windows.SetDropdownContentProvider(func() func(*ui.Context) {
		return func(c *ui.Context) {
			switch {
			case quakeOwn():
				quakeView(c)
			case quakeFollow() && followView != nil:
				followView(c)
			default:
				dropdown.Placeholder(c)
			}
		}
	})
	windows.SetDropdownOwnershipProvider(func() bool {
		return mode() == appdesktop.HotkeyModeTopdown || quakeFollow()
	})
}
