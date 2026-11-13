package app

import (
	"context"

	"github.com/egoist/mygo/ui"

	appdesktop "github.com/mslxl/natcatty/application/desktop"
	appsession "github.com/mslxl/natcatty/application/session"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/dropdown"
	quakeui "github.com/mslxl/natcatty/ui/quake"
	"github.com/mslxl/natcatty/ui/shell"
	shellstate "github.com/mslxl/natcatty/ui/shell/state"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

// quakeSessionOpener adapts the shared Session service to the Quake
// workspace's consumer-owned port. A local Shell or Host opened here uses the
// same transport, logging, Command History, and notification path as the main
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

// OpenHost starts the Session a remote Quake Tab shows. It builds the same
// StartSpec the main shell uses, so protocol mapping, capture hooks, tmux
// attachment, and reconnect behavior do not fork between the windows.
func (o quakeSessionOpener) OpenHost(host domainvault.Host) (*appsession.Session, error) {
	if o.sessions == nil {
		return nil, quakeui.ErrNoSessions
	}
	return o.sessions.Open(context.Background(), shellstate.HostSessionSpec(host))
}

// quakeHostSource adapts the Vault's listed Hosts to the Quake Host picker's
// consumer-owned port. It reads the effective list, so Host Group defaults
// and Proxy Profiles reach the connection exactly as the Vault list shows
// them.
type quakeHostSource struct {
	vault *vaultstate.State
}

func (s quakeHostSource) Hosts() []domainvault.Host {
	if s.vault == nil {
		return nil
	}
	return s.vault.EffectiveItems()
}

// wireDropdownQuake routes the Dropdown Panel by Hotkey Mode and Quake Source.
// The readers are live: a Settings change re-routes the same Panel's root
// content and top bar on the next frame, and the native window is never
// recreated.
//
// Quake with source own renders its private workspace and leaves the main
// window visible. Quake with source follow renders the main Workspace's active
// Tab in the Panel and transfers the Surface ownership to it. Top-down renders
// the complete main shell in the Panel and always transfers ownership; the
// main window then draws only its neutral placeholder. Other modes keep the
// placeholder.
//
// The Panel's default height is mode-specific: Quake takes half the work area
// and top-down takes 80%. Both are replaced by the configured height and unit
// from the dropdown settings, and recomputed on every show.
func wireDropdownQuake(windows *windowManager, state *quakeui.State, shellState *shellstate.State,
	mode func() appdesktop.HotkeyMode, source func() appdesktop.QuakeSource,
	settings func() appdesktop.Settings, openMain func()) {
	if windows == nil || state == nil {
		return
	}
	quakeBar := quakeui.Bar(state)
	quakeView := quakeui.View(state)
	var followBar, followView, topdownView func(*ui.Context)
	if shellState != nil {
		followBar = shell.FollowBar(shellState)
		followView = shell.FollowView(shellState, shell.WithFollowOpenMain(openMain))
		topdownView = shell.TopdownView(shellState, shell.WithFollowOpenMain(openMain))
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
	topdown := func() bool {
		return mode() == appdesktop.HotkeyModeTopdown
	}
	// currentSettings keeps the geometry and hide switches live: the settings
	// page writes them while the Panel may already exist, and every show asks
	// again. A nil provider keeps the defaults, so old test wiring behaves as
	// it did.
	currentSettings := func() appdesktop.Settings {
		if settings == nil {
			return appdesktop.Defaults("")
		}
		return settings()
	}
	// The top-down shell needs the whole work area's height budget; Quake's
	// compact terminal keeps its half-height default. Both are clamped to the
	// work area and their mode-specific minimum by configuredDropdownHeight.
	windows.SetDropdownHeightProvider(func(workArea windowBounds) int {
		return configuredDropdownHeight(currentSettings(), topdown(), workArea)
	})
	windows.SetDropdownHideOnBlurProvider(func() bool {
		return currentSettings().HideOnBlur
	})
	windows.SetDropdownHideOnEscapeProvider(func() bool {
		return currentSettings().HideOnEscape
	})
	windows.SetDropdownShowHook(func() {
		if !quakeOwn() {
			return
		}
		// The first show creates the local Shell; later shows reuse it. Follow
		// mode must not create a Quake workspace: its content is the main
		// Workspace, which the user already owns.
		_ = state.Activate()
	})
	// The first close request (an Escape the page leaves unhandled) dismisses
	// the Host picker; the next one hides the Panel. Only the own route draws
	// the picker: a picker left open before a mode or source switch is not on
	// screen in the follow, top-down, or placeholder routes, so their first
	// Escape must hide the Panel instead of being consumed.
	windows.SetDropdownDismissHandler(func() bool {
		return quakeOwn() && state.DismissOverlay()
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
			case topdown() && topdownView != nil:
				topdownView(c)
			default:
				dropdown.Placeholder(c)
			}
		}
	})
	windows.SetDropdownOwnershipProvider(func() bool {
		return topdown() || quakeFollow()
	})
}

// configuredDropdownHeight turns the persisted height and unit into the
// Panel's visible height for workArea. Percentages round to whole DIPs; pixel
// values pass through. The result is clamped to the mode's minimum (120 for
// Quake, 480 for top-down) and to the work area itself, so a work area
// shorter than the minimum uses its own height. A record that somehow still
// carries a zero height or an unknown unit falls back to the mode's default,
// matching the defensive clamp in the settings layer.
func configuredDropdownHeight(settings appdesktop.Settings, topdown bool, workArea windowBounds) int {
	value, unit := settings.QuakeHeight, settings.QuakeHeightUnit
	fallback, minimum := appdesktop.DefaultQuakeHeight, dropdownMinHeight
	if topdown {
		value, unit = settings.TopdownHeight, settings.TopdownHeightUnit
		fallback, minimum = appdesktop.DefaultTopdownHeight, topdownMinHeight
	}
	if !unit.Valid() {
		// An unknown unit makes the stored number meaningless; use the whole
		// mode default rather than reading it as percent.
		unit = appdesktop.DefaultHeightUnit
		value = fallback
	}
	if value <= 0 {
		value = fallback
	}
	height := value
	if unit == appdesktop.HeightUnitPercent {
		height = percentHeight(workArea, value)
	}
	if workArea.Height < minimum {
		minimum = workArea.Height
	}
	if height < minimum {
		height = minimum
	}
	if height > workArea.Height {
		height = workArea.Height
	}
	return height
}
