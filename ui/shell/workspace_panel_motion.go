package shell

import (
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/domain/model"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
	"github.com/mslxl/natcatty/ui/layout"
	"github.com/mslxl/natcatty/ui/motion"
	"github.com/mslxl/natcatty/ui/shell/state"
)

// The side panel beside a terminal opens and closes like a drawer: its slot
// grows from nothing (or shrinks to nothing) along the dock axis, the divider
// moves with it, and the panes take the room it leaves. The panel's content is
// laid out once at its final size and anchored to the dock's fixed edge, so
// the moving edge reveals it instead of squeezing it.
//
// The terminal follows ADR 0026. Its final grid is computed at the change and
// pinned once; the drawer re-lays the split's boxes on every frame, but the
// terminal draws that one grid at its own cell size and is clipped by its box.
// Animation frames do no grid math and deliver no PTY resize, and the text
// never scales: a shrinking Pane would otherwise draw its final grid enlarged
// to the old box before settling.

// sidePanelSpec is the side panel a frame built: the tools, the chosen one,
// and the tool view itself. The motion runtime keeps the last built spec so a
// closing panel can keep drawing the frame the user saw after the state has
// already closed it.
type sidePanelSpec struct {
	tab    model.TabID
	dock   state.PanelDock
	ids    []state.SideToolID
	chosen state.SideToolID
	tool   func(*ui.Context)
}

// sidePanelRun is one panel open or close, measured in DIPs along the dock
// axis. Pins are the Panes whose final viewport this run set at the change;
// they are released when the run ends or another layout change takes over.
type sidePanelRun struct {
	kind     motion.Kind
	started  time.Time
	duration time.Duration
	from, to float32
	pins     map[model.PaneID]state.ViewportMotion
}

// sizeAt is the panel's visible size at now and whether the run is over.
func (r *sidePanelRun) sizeAt(now time.Time) (float32, bool) {
	if r == nil {
		return 0, true
	}
	if r.duration <= 0 || !now.Before(r.started.Add(r.duration)) {
		return r.to, true
	}
	k := float32(now.Sub(r.started)) / float32(r.duration)
	if k < 0 {
		k = 0
	}
	if k > 1 {
		k = 1
	}
	return r.from + (r.to-r.from)*ui.EaseOut(k), false
}

// sidePanelMotion is one view's side panel runtime. It records what the last
// frame built, notices the panel's open or close in the state, and plays one
// drawer run per change.
type sidePanelMotion struct {
	// enabled is false in a renderer that never draws the side panel (a
	// detached window). Follow and the top-down route do draw it.
	enabled bool
	// known is false until this runtime has seen a Tab. The first frame is
	// final: a restored or freshly mounted view never animates the panel it
	// already shows.
	known bool
	tab   model.TabID
	// open is the panel state on the last frame, not whether that frame drew
	// it: leaving the terminal for the Vault must not look like a close and
	// re-entering must not animate the panel that stayed open.
	open bool
	dock state.PanelDock
	// width and ratio are the panel's state geometry at the last rendered
	// frame. A divider drag or dock change warps.
	width, ratio float32
	// spec and drawn are the last panel this view built. A close keeps
	// drawing that spec while its slot shrinks.
	spec  sidePanelSpec
	drawn bool
	// visible is the panel's size along the dock axis on the last frame. A
	// close starts from it, because the state already reports no panel.
	visible float32
	// reveal and revealSet are the drawer's visible size for this frame.
	reveal    float32
	revealSet bool
	run       *sidePanelRun
	// animationFrames counts the frames this runtime asked for. A warped
	// change asks for none; tests assert it.
	animationFrames int
}

// observePanel is called before the frame is laid out. It reports whether the
// panel changed the layout this frame, so the Pane and Tab runtimes warp
// instead of animating boxes measured beside the old panel. On an open or
// close it freezes every Pane's final viewport before the drawer lays out its
// first frame; animation frames then do no grid math (ADR 0026).
func (wm *workspacePaneMotion) observePanel(c *ui.Context, s *state.State,
	setting domainpreferences.Motion, now time.Time, activeID model.TabID, activeOK bool,
	firstFrame, hidden, wasHidden, resized bool, event motion.Event, unseen bool) bool {
	sm := &wm.panel
	if !sm.enabled {
		return false
	}
	wasDrawn := sm.drawn
	sm.drawn = false
	sm.revealSet = false

	if !activeOK || !sm.known || sm.tab != activeID {
		// A new Tab (or the first frame) is final. Nothing the panel did in
		// another Tab is this renderer's to animate.
		sm.releaseRun()
		sm.known = activeOK
		sm.tab = activeID
		sm.spec = sidePanelSpec{}
		sm.visible = 0
		if activeOK {
			sm.open = s.FilesOpen(activeID) && s.FilesAvailable()
			if sm.open {
				sm.dock = s.FilesDock()
				window, _ := c.Size()
				if sm.dock != state.DockBottom {
					sm.width = *s.FilesWidth(window)
					sm.visible = sm.width
				}
				sm.ratio = *s.FilesRatio()
			}
		} else {
			sm.open = false
		}
		return false
	}

	open := s.FilesOpen(activeID) && s.FilesAvailable()
	// showing is whether this frame draws a terminal at all. The panel's open
	// state is read regardless: leaving for the Vault must not look like a
	// close, and coming back must not animate a panel that stayed open.
	showing := terminalVisible(s)
	rendered := open && showing
	if rendered {
		if root, _, ok := wm.tabTree(); !ok || root == nil {
			rendered = false
		}
	}
	dock := sm.dock
	var width, ratio float32
	if rendered {
		dock = s.FilesDock()
		ratio = *s.FilesRatio()
		if dock != state.DockBottom {
			window, _ := c.Size()
			width = *s.FilesWidth(window)
		}
	}
	geomChanged := rendered && sm.open && wasDrawn &&
		(sm.width != width || sm.ratio != ratio)
	dockChanged := rendered && sm.open && dock != sm.dock
	changed := open != sm.open
	reflow := changed || geomChanged || dockChanged
	reduceMotion := c.Preferences().ReduceMotion

	if reflow && wm.run != nil {
		// The panel is a layout change beside the Panes. A Pane run measured
		// against the old geometry is over; its final frame is this one.
		wm.finish()
	}

	// A run that is no longer on screen, no longer enabled, was resized
	// underneath, or loses the layout to another event warps at once. A view
	// showing the Vault instead of a terminal paints no panel, so it must not
	// keep asking for frames.
	if run := sm.run; run != nil {
		if firstFrame || hidden || wasHidden || resized || !showing || reduceMotion ||
			!motion.Enabled(setting, run.kind) || (unseen && !changed) {
			sm.releaseRun()
		}
	}

	// A dock change or a divider drag is not an expand/collapse; it warps to
	// the new geometry. It is still a layout change for the Pane runtime.
	if geomChanged || dockChanged {
		sm.releaseRun()
		sm.open = open
		sm.dock = dock
		sm.width, sm.ratio = width, ratio
		if open {
			sm.visible = wm.openPanelVisible(dock, width, ratio)
		} else {
			sm.visible = 0
		}
		return true
	}

	if !changed {
		sm.dock = dock
		if rendered {
			sm.width, sm.ratio = width, ratio
		}
		if sm.run != nil {
			size, done := sm.run.sizeAt(now)
			if done {
				sm.releaseRun()
				if open {
					sm.visible = wm.openPanelVisible(dock, width, ratio)
				} else {
					sm.visible = 0
				}
			} else {
				sm.reveal, sm.revealSet = size, true
				sm.visible = size
				sm.animationFrames++
				c.AnimationFrame()
			}
		} else if open {
			sm.visible = wm.openPanelVisible(dock, width, ratio)
		} else {
			sm.visible = 0
		}
		return false
	}

	kind := motion.PaneOpen
	if !open {
		kind = motion.PaneClose
	}
	// An unseen Pane or Tab event on this frame owns the layout; nothing may
	// animate over the boxes it never showed. A Tab reorder changes no
	// geometry, so the panel may still play.
	busyEvent := unseen && event.Kind != motion.TabReorder
	animate := !firstFrame && !hidden && !wasHidden && !resized && !busyEvent &&
		!reduceMotion && motion.Enabled(setting, kind) && wm.region.W > 0 && wm.region.H > 0

	// The split's total room: last frame's pane area plus the panel and the
	// divider the panel holds while it shows. A normal open starts with no
	// panel (the area is already the whole split); an interrupted close has
	// one on screen.
	total := splitRoomForPanel(dock, wm.region, sm.visible)
	var final float32
	if open {
		final = openPanelSize(dock, total, width, ratio)
		// The opening panel must actually be built this frame, or there is
		// no drawer to show.
		animate = animate && rendered && final > 0
	} else {
		// A close starts from the size the user last saw; the state already
		// reports no panel.
		final = sm.visible
		// A close can only draw a panel that was on screen with a tool.
		animate = animate && wasDrawn && final > 0 && sm.spec.tool != nil
	}
	var run *sidePanelRun
	if duration := motion.ScaleMotion(motion.BaseDuration(kind), setting.Speed); animate && duration > 0 {
		from, to := float32(0), final
		if !open {
			from, to = final, 0
		}
		if interrupted := sm.run; interrupted != nil {
			// Latest event wins: the drawer restarts from what the user sees.
			from, _ = interrupted.sizeAt(now)
		}
		run = &sidePanelRun{
			kind:     kind,
			started:  now,
			duration: duration,
			from:     from,
			to:       to,
			pins:     map[model.PaneID]state.ViewportMotion{},
		}
	}
	sm.releaseRun()
	if run != nil {
		sm.run = run
		sm.reveal, sm.revealSet = run.from, true
		sm.visible = run.from
		wm.pinSidePanel(c, s, run, total, dock, open, final)
		sm.animationFrames++
		c.AnimationFrame()
	} else if open {
		sm.visible = final
	} else {
		sm.visible = 0
	}
	sm.open = open
	sm.dock = dock
	if rendered {
		sm.width, sm.ratio = width, ratio
	}
	return true
}

// panelFrame reports the panel this frame must build and the drawer it hides
// behind. stateOpen is whether the Tab's panel is open in the state, and fresh
// is the spec built from it; while a close run plays the state is already
// closed, so the last built spec keeps painting the panel the user saw.
func (sm *sidePanelMotion) panelFrame(stateOpen bool, fresh sidePanelSpec) (sidePanelSpec, *layout.PanelReveal, bool) {
	if !sm.enabled {
		return fresh, nil, stateOpen
	}
	if stateOpen {
		return fresh, sm.panelReveal(), true
	}
	if run := sm.run; run != nil && run.kind == motion.PaneClose && sm.spec.tool != nil {
		return sm.spec, sm.panelReveal(), true
	}
	return sidePanelSpec{}, nil, false
}

// panelReveal is the drawer this frame shows, or nil when the panel sits in
// its final slot.
func (sm *sidePanelMotion) panelReveal() *layout.PanelReveal {
	if !sm.revealSet || sm.run == nil {
		return nil
	}
	size := sm.run.to
	if sm.run.kind == motion.PaneClose {
		size = sm.run.from
	}
	return &layout.PanelReveal{Visible: max(sm.reveal, 0), Size: size}
}

// notePanel records the panel a frame drew, so a close can keep drawing it
// after the state has closed it.
func (sm *sidePanelMotion) notePanel(spec sidePanelSpec) {
	if !sm.enabled {
		return
	}
	sm.drawn = true
	sm.spec = spec
}

// splitRoomForPanel is the split's whole room: last frame's pane area plus the
// panel and the one-DIP divider that sit beside it while it shows.
func splitRoomForPanel(dock state.PanelDock, region ui.Rect, shown float32) ui.Rect {
	total := region
	if shown <= 0 {
		return total
	}
	switch dock {
	case state.DockLeft:
		total.X -= shown + 1
		total.W += shown + 1
	case state.DockRight:
		total.W += shown + 1
	case state.DockBottom:
		total.H += shown + 1
	}
	return total
}

// openPanelSize is the panel's final size along the dock axis. The side dock
// takes the width the state stores; the bottom dock takes the share of the
// split the panes do not use.
func openPanelSize(dock state.PanelDock, total ui.Rect, width, ratio float32) float32 {
	if dock != state.DockBottom {
		return width
	}
	share := ratio / 100
	if share <= 0 || share >= 1 {
		return 0
	}
	return max(total.H-1, 0) * (1 - share)
}

// openPanelVisible is the panel's size on a settled open frame. The region is
// the panes' room, so the split's shared room is that over the panes' share
// and the panel takes the rest.
func (wm *workspacePaneMotion) openPanelVisible(dock state.PanelDock, width, ratio float32) float32 {
	if dock != state.DockBottom {
		return width
	}
	share := ratio / 100
	if share <= 0 || share >= 1 || wm.region.H <= 0 {
		return 0
	}
	return wm.region.H * (1 - share) / share
}

// releaseRun lets every Pane the run pinned follow its own layout again and
// forgets the run.
func (sm *sidePanelMotion) releaseRun() {
	if sm.run == nil {
		return
	}
	sm.releasePins()
	sm.run = nil
}

// releasePins releases the Pane viewports a run froze. A Pane or Tab event
// that arrives mid-run sizes its Panes from the new layout instead.
func (sm *sidePanelMotion) releasePins() {
	if sm.run == nil {
		return
	}
	for _, vm := range sm.run.pins {
		vm.ReleaseViewport()
	}
	sm.run.pins = nil
}

// pinSidePanel freezes every Pane's final viewport at the change, before the
// drawer's first layout. The final panes' room is the split with the panel's
// final size and divider taken out (open) or the whole split (close). Pinning
// from the tree, not by adding a delta to each viewport, keeps nested splits
// right. Exactly one PTY resize is delivered per Pane; animation frames then
// only move the drawer.
func (wm *workspacePaneMotion) pinSidePanel(c *ui.Context, s *state.State, run *sidePanelRun,
	total ui.Rect, dock state.PanelDock, open bool, size float32) {
	if run == nil || size <= 0 || total.W <= 0 || total.H <= 0 {
		return
	}
	finalRegion := total
	if open {
		switch dock {
		case state.DockLeft:
			finalRegion.X += size + 1
			finalRegion.W -= size + 1
		case state.DockRight:
			finalRegion.W -= size + 1
		case state.DockBottom:
			finalRegion.H -= size + 1
		default:
			return
		}
	}
	if finalRegion.W <= 0 || finalRegion.H <= 0 {
		return
	}
	root, _, ok := wm.tabTree()
	if !ok || root == nil {
		return
	}
	chrome := wm.chrome(c, s)
	final := paneFinalRects(root, finalRegion, wm.rects, c.Theme().Space(10),
		motion.PaneRects{}, !root.IsPane(), chrome)
	wm.pinPanesInto(run.pins, domainworkspace.CollectPaneIDs(root), final, "")
}
