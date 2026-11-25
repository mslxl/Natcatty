package shell

import (
	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/domain/model"
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
	// Follow draws the main Workspace's active Tab through a read-only ref. It
	// still owns a Pane motion runtime of its own: the main window and Follow
	// may show the same Tab and each plays its own copy from its own recorded
	// boxes. It is Pane-only, so it never consumes the main window's Tab body
	// or ghost pools.
	return followView(s, newActivePaneOnlyMotion(s), opts...)
}

// followView is FollowView with the view's Pane motion runtime supplied, so a
// test can read its run, recorded boxes and frame count. The runtime belongs
// to this mounted view and must never be shared with the main window.
func followView(s *state.State, wm *workspacePaneMotion, opts ...FollowOption) func(*ui.Context) {
	options := followOptions{}
	for _, opt := range opts {
		opt(&options)
	}
	focus := &focusEpochTracker{}
	return func(c *ui.Context) {
		m, t := shellPreamble(c, s, func() bool { return terminalVisible(s) })
		// The app marks this route when another route or a hidden Panel
		// bypassed it. The events recorded in the gap were never on screen, so
		// the next real frame warps instead of replaying them.
		if s.TakeMotionViewUnrendered(state.MotionViewFollow) {
			wm.noteSkipped()
		}
		tab, tabOK := followReadOnlyTab(s)
		if tabOK {
			wm.preFrame(c, s)
		} else {
			wm.noteSkipped()
		}
		followBody(c, s, m, t, options.openMain, focus.next(s), tab, tabOK, wm)
		s.FinishSideFrameFor(state.RenderOwnerDropdown)
	}
}

// followReadOnlyTab reports the Tab Follow draws, or false for every empty
// placeholder. A browser panel owns a native webview MyGo cannot mount in a
// second window, so it also has no read-only Tab here.
func followReadOnlyTab(s *state.State) (state.ReadOnlyTab, bool) {
	if !terminalVisible(s) || s.ActiveBrowserOpen() {
		return state.ReadOnlyTab{}, false
	}
	return s.ActiveReadOnlyTab()
}

// followBody draws the current follow surface. It is separate so the one
// FinishSideFrameFor call above runs for every branch, including the empty
// states. tab and tabOK are the snapshot the caller already took for the
// motion runtime; wm is Follow's Pane runtime.
func followBody(c *ui.Context, s *state.State, m locales.Messages, t *ui.Theme,
	openMain func(), focusEpoch uint64, tab state.ReadOnlyTab, tabOK bool, wm *workspacePaneMotion) {
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
	if !tabOK {
		followEmpty(c, m, icons.SquareTerminal, m.FollowEmptyTitle(), m.FollowEmptyDescription(), openMain)
		return
	}
	ref := readOnlyTabRef(state.RenderOwnerDropdown, tab, focusEpoch)
	if wm != nil {
		ref.motion = wm
		ref.magnified = func() model.PaneID { return wm.effectiveMagnified }
	}
	activeTabBody(c, s, m, t, ref)
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
	return topdownView(s, newWorkspacePaneMotion(s, paneMotionMode{}), opts...)
}

// topdownView is TopdownView with the view's motion runtime supplied, so a
// test can read its run, recorded boxes and frame count. The dropdown route
// owns this runtime for as long as it is mounted; it must never share the main
// window's controller, rects or strip.
func topdownView(s *state.State, wm *workspacePaneMotion, opts ...FollowOption) func(*ui.Context) {
	options := followOptions{}
	for _, opt := range opts {
		opt(&options)
	}
	// The work Tab strip's reveal and its motion runtime belong to this view
	// too: the same Tab strip is drawn in a second window, and its progress
	// must not follow the main window's.
	reveal := newWorkTabReveal()
	if wm.mode.stripReveal != nil {
		// Tests inject the strip's reveal to read its runtime; a mounted
		// Topdown route always keeps its own.
		reveal = wm.mode.stripReveal
	}
	reveal.labels = wm.mode.labels
	reveal.canInstant = wm.stripWarpCanBeInstant
	focus := &focusEpochTracker{}
	return func(c *ui.Context) {
		m, t := shellPreamble(c, s, func() bool { return terminalVisible(s) })
		// A route or hidden Panel that bypassed this view leaves events it never
		// drew; the next real frame warps both runtimes. It ORs with the
		// browser placeholder's own notice below.
		if s.TakeMotionViewUnrendered(state.MotionViewTopdown) {
			wm.noteSkipped()
			reveal.strip.noteUnrendered()
		}
		if s.ActiveBrowserOpen() {
			// No Pane tree or Tab strip is built on this frame. The next real
			// frame must warp events the placeholder never showed.
			wm.noteSkipped()
			reveal.strip.noteUnrendered()
			followEmpty(c, m, icons.Globe, m.FollowBrowserTitle(), m.FollowBrowserDescription(), options.openMain)
		} else {
			wm.preFrame(c, s)
			shellLayout(c, s, m, t, state.RenderOwnerDropdown, reveal, focus.next(s), wm)
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
		// The app marks the mini strip when another route (Quake own, top-down,
		// or the neutral placeholder) bypassed it. Its next frame warps the
		// events it never drew instead of sliding stale items into place.
		if s.TakeMotionViewUnrendered(state.MotionViewFollowBar) {
			strip.noteUnrendered()
		}
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
