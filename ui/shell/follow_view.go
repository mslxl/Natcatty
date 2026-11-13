package shell

import (
	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/shell/state"
)

// FollowOption customizes the Dropdown Panel's follow renderer.
type FollowOption func(*followOptions)

type followOptions struct {
	openMain func()
}

// WithFollowOpenMain installs the action behind the empty states' "Open main
// window" button. The app wires it to hide the Panel and raise the main
// window, so the borrowed Workspace returns to its owner.
func WithFollowOpenMain(fn func()) FollowOption {
	return func(o *followOptions) { o.openMain = fn }
}

// FollowView renders the main Workspace's active Tab in the Dropdown Panel.
// The Tab does not leave the main Workspace: the renderer takes a read-only
// reference, so it cannot close, split, move, detach, hibernate, or resume
// anything. It runs the shell's per-frame preamble itself, because the main
// window renders a neutral placeholder while the Panel owns the surfaces:
// Flush and FinishSideFrame therefore execute exactly once, in this renderer.
//
// The active Tab must still be a terminal. The Vault, Logs, monitoring, the
// System manager, SFTP, and an open browser panel each render a neutral state
// that offers the main window instead of mounting a surface that cannot move
// between windows.
func FollowView(s *state.State, opts ...FollowOption) func(*ui.Context) {
	options := followOptions{}
	for _, opt := range opts {
		opt(&options)
	}
	return func(c *ui.Context) {
		m, t := shellPreamble(c, s, func() bool { return terminalVisible(s) })
		followBody(c, s, m, t, options.openMain)
		s.FinishSideFrameFor(state.RenderOwnerDropdown)
	}
}

// followBody draws the current follow surface. It is separate so the one
// FinishSideFrameFor call above runs for every branch, including the empty
// states.
func followBody(c *ui.Context, s *state.State, m locales.Messages, t *ui.Theme, openMain func()) {
	if !terminalVisible(s) {
		followEmpty(c, m, icons.SquareTerminal, m.FollowEmptyTitle(), m.FollowEmptyDescription(), openMain)
		return
	}
	// A browser panel owns a native webview that MyGo cannot mount in a second
	// window. The placeholder keeps the main Workspace untouched; closing the
	// browser is not something the dropdown does on its own.
	if s.ActiveBrowserOpen() {
		followEmpty(c, m, icons.Globe, m.FollowBrowserTitle(), m.FollowBrowserDescription(), openMain)
		return
	}
	tab, ok := s.ActiveReadOnlyTab()
	if !ok {
		followEmpty(c, m, icons.SquareTerminal, m.FollowEmptyTitle(), m.FollowEmptyDescription(), openMain)
		return
	}
	activeTabBody(c, s, m, t, readOnlyTabRef(state.RenderOwnerDropdown, tab))
}

// followEmpty centers a placeholder and, when the app supplied the action,
// the button that returns the user to the main window.
func followEmpty(c *ui.Context, m locales.Messages, icon, title, description string, openMain func()) {
	ui.Column(c).Fill().Justify(ui.Center).Children(func() {
		kit.EmptyState(c, icon, title, description, func() {
			if openMain == nil {
				return
			}
			if kit.PrimaryButton(c, "", m.FollowOpenMain()).Clicked() {
				openMain()
			}
		})
	})
}

// FollowBar draws the mini Tab strip in the Panel's top bar. It is selection
// only: no close, add, menu, split, or drag control exists, so the only
// Workspace change a click can make is the active index.
func FollowBar(s *state.State) func(*ui.Context) {
	return func(c *ui.Context) {
		m := locales.In(c.Locale())
		selected := -1
		kit.TabStrip(c, kit.TabPane, kit.TabStripOptions{Label: m.FollowTabsLabel()}, func() {
			for index := 0; index < s.TabCount(); index++ {
				info, ok := s.TabInfo(index)
				if !ok {
					continue
				}
				tab := kit.Tab(c, kit.TabPane, kit.TabSpec{
					Key:    "follow-tab-" + string(info.ID),
					Title:  tabTitle(m, info),
					Active: info.Active,
				})
				if tab.Clicked {
					selected = index
				}
			}
		})
		if selected >= 0 {
			s.SelectTab(selected)
			c.Invalidate()
		}
	}
}

// MainPlaceholder is the main window's neutral body while the Dropdown Panel
// owns the shell surfaces. It renders no Session, so the same Surface can
// never be drawn by two windows in one frame. It deliberately runs neither
// Flush nor FinishSideFrame: those belong to the current owner alone.
func MainPlaceholder(c *ui.Context) {
	t := c.Theme()
	c.Root().Background(t.Background)
	ui.Column(c).Fill()
}
