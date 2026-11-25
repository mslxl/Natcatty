package shell

import (
	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/shell/state"
)

// FollowOption customizes the Dropdown Panel's shell renderers, FollowView
// and TopdownView.
type FollowOption func(*followOptions)

type followOptions struct {
	openMain func()
}

// WithFollowOpenMain installs the action behind the "Open main window" button
// of the follow and top-down routes. The app wires it to hide the Panel and
// raise the main window, so a borrowed or blocked Workspace view can return to
// its owner.
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
	focus := &focusEpochTracker{}
	return func(c *ui.Context) {
		m, t := shellPreamble(c, s, func() bool { return terminalVisible(s) })
		followBody(c, s, m, t, options.openMain, focus.next(s))
		s.FinishSideFrameFor(state.RenderOwnerDropdown)
	}
}

// followBody draws the current follow surface. It is separate so the one
// FinishSideFrameFor call above runs for every branch, including the empty
// states.
func followBody(c *ui.Context, s *state.State, m locales.Messages, t *ui.Theme,
	openMain func(), focusEpoch uint64) {
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
	activeTabBody(c, s, m, t, readOnlyTabRef(state.RenderOwnerDropdown, tab, focusEpoch))
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

// TopdownView renders the complete main shell in the Dropdown Panel's
// top-down route: the same Tab bar, home sidebar, Vault, SFTP, terminal Tabs,
// and global overlays the main window draws, from the same shell state. It is
// not a second application; only the rendering window changes.
//
// The renderer runs the shell's per-frame preamble itself, because the main
// window renders a neutral placeholder while the Panel owns the surfaces:
// Flush and FinishSideFrame therefore execute exactly once, under the dropdown
// owner. The Pane commands remain the window's own, since the main window is
// hidden and the Panel is the only place the user can act on the Workspace.
//
// A Session whose browser panel is open owns a native webview MyGo cannot
// mount in a second window. That Tab renders the neutral browser placeholder
// and offers the main window instead; the browser is never closed on the
// user's behalf.
func TopdownView(s *state.State, opts ...FollowOption) func(*ui.Context) {
	options := followOptions{}
	for _, opt := range opts {
		opt(&options)
	}
	reveal := newWorkTabReveal()
	focus := &focusEpochTracker{}
	return func(c *ui.Context) {
		m, t := shellPreamble(c, s, func() bool { return terminalVisible(s) })
		if s.ActiveBrowserOpen() {
			followEmpty(c, m, icons.Globe, m.FollowBrowserTitle(), m.FollowBrowserDescription(), options.openMain)
		} else {
			shellLayout(c, s, m, t, state.RenderOwnerDropdown, reveal, focus.next(s), nil)
		}
		s.FinishSideFrameFor(state.RenderOwnerDropdown)
	}
}

// FollowBar draws the mini Tab strip in the Panel's top bar. It is selection
// only: no close, add, menu, split, or drag control exists, so the only
// Workspace change a click can make is the active index.
func FollowBar(s *state.State) func(*ui.Context) {
	// The mini strip is its own strip surface: it consumes the same events as
	// the main strip so an open, close or reorder animates here too, and a
	// switch changes only the selected color (ticket 20's Follow consistency).
	// It moves Position alone: a mini Tab takes its final width on the frame
	// it appears, and the selected fill never fades.
	return followBarWithStrip(s, &tabStripMotion{positionOnly: true})
}

// followBarWithStrip is FollowBar with the mini strip's runtime supplied, so a
// regression can read its run, generation and frame count. FollowBar owns one
// runtime per mounted bar; tests are the only other caller.
func followBarWithStrip(s *state.State, strip *tabStripMotion) func(*ui.Context) {
	return func(c *ui.Context) {
		m := locales.In(c.Locale())
		// Snapshot the row this build draws before beginFrame, as workTabs
		// does: the removal guard compares it with the committed row, so a
		// mini Tab removed with no close event (a detach or a batch close) is
		// suppressed on the frame it disappears instead of replaying the exit
		// record the previous build attached (ticket 20).
		entries := make([]tabEntry, 0, s.TabCount())
		for index := 0; index < s.TabCount(); index++ {
			if info, ok := s.TabInfo(index); ok {
				entries = append(entries, tabEntry{index: index, info: info})
			}
		}
		strip.noteCurrentRow(c, m, entries, nil)
		strip.beginFrame(c, s, tabStripSignals{row: &strip.current})
		selected := -1
		kit.TabStrip(c, kit.TabPane, kit.TabStripOptions{
			Label: m.FollowTabsLabel(), Generation: strip.generation,
		}, func() {
			for _, entry := range entries {
				info := entry.info
				title, marker := tabTitle(m, info), tabMarker(m, info)
				body, tab := kit.Tab(c, kit.TabPane, kit.TabSpec{
					Key:    "follow-tab-" + string(info.ID),
					Title:  title,
					Active: info.Active,
				})
				// The row the next build compares against is recorded with the
				// same title/marker/bound noteCurrentRow uses, so a steady
				// mini strip compares exactly equal. The mini strip draws no
				// Pending Connection rows, so its pending list stays empty.
				strip.noteTabRow(info.ID, title, marker, kit.WorkTabWidthBound(c, title, marker))
				strip.noteWidth(info.ID, body.LayoutBounds().W)
				if transition, ok := strip.transition(s.Motion(), false); ok {
					body.Transition(transition)
				}
				if strip.zIndex(info.ID, false) > 0 {
					body.ZIndex(1)
				}
				if tab.Clicked {
					selected = entry.index
				}
			}
		})
		strip.commitRow(c, nil)
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
