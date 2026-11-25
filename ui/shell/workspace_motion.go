package shell

import (
	"errors"
	"strconv"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/domain/model"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
	uiconnection "github.com/mslxl/natcatty/ui/connection"
	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/motion"
	"github.com/mslxl/natcatty/ui/shell/state"
	uitheme "github.com/mslxl/natcatty/ui/theme"
)

// paneMotionZIndex puts the Pane overlays above the final layout but below
// the dialogs and the Quick Switcher, which are built after them.
const paneMotionZIndex = 1

// paneMotionMode selects the routes a test observes. Production animates a
// close's survivors natively: the ticket-17 measurement showed Match +
// ScaleContent crossing the split a close collapses, unlike the split an open
// creates (ticket 04, which the overlay still handles). Native keeps a Match
// transition on every Pane so a layout change can start from the previous
// frame's recorded box; overlayClose forces the older opaque-overlay survivor
// route so the fallback stays exercised; overlayReorder forces a same-Tab
// move's siblings through the opaque overlay so both reorder routes have
// coverage; labels names the overlays for tests.
type paneMotionMode struct {
	native         bool
	overlayClose   bool
	overlayReorder bool
	labels         bool
	// stripReveal, if set, is the work Tab strip's reveal the test renders
	// with, so a test can read the strip runtime's run, generation and
	// previous-frame boxes. A window always leaves it nil and builds its own.
	stripReveal *workTabReveal
}

// zoomRender is which Pane tree a zoom run shows while its spring plays. A
// zoom in renders the real split until the overlay covers it, so the hidden
// Panes keep their split grids and no tree is rebuilt per frame; a direct
// A→B renders only the Pane that was magnified, which stays full and does
// not resize. Every other zoom renders the state's layout.
type zoomRender int

const (
	// zoomRenderState shows the state's current zoom: the split after a
	// zoom out.
	zoomRenderState zoomRender = iota
	// zoomRenderSplit suppresses the new zoom while a zoom-in grows over
	// the split it covers.
	zoomRenderSplit
	// zoomRenderPane shows one Pane alone while another grows over it.
	zoomRenderPane
)

// paneMotionRun is one Pane animation this view is playing: a Pane open, a
// Pane close with its survivor(s) and its exit ghost, or a Pane reorder/zoom.
type paneMotionRun struct {
	kind   motion.Kind
	tabID  model.TabID
	seq    uint64
	target model.PaneID
	anim   *motion.Animation
	// native marks a run whose moving Panes are moved by MyGo's own
	// ElementTransition instead of an overlay.
	native bool
	// magnified marks a close whose Pane filled the Tab at the change. Its
	// ghost starts at the whole pane region and the split is revealed at its
	// final layout, so its survivors do not animate.
	magnified bool
	// zoom marks a PaneReorder that changes magnification. zoomPane is the
	// Pane whose overlay springs, and render/renderPane select the tree the
	// view shows behind it.
	zoom     bool
	zoomPane model.PaneID
	// zoomHome is where the zoomPane came from: the boxes it showed before
	// this zoom. A direct magnification reversed mid-run sends its outgoing
	// copy back there, so it is captured at the change before recordPane can
	// replace wm.rects[zoomPane] with the zoom's final boxes.
	zoomHome   motion.PaneRects
	render     zoomRender
	renderPane model.PaneID
	// spring is the exact ui.SpringOptions this run's native siblings and
	// overlay copies use, kept so a test and an interruption read one value.
	spring *ui.SpringOptions
	// nativePanes are the siblings MyGo's Match + ScaleContent carries; their
	// visuals are bookkeeping only and their real Panes are not masked.
	nativePanes map[model.PaneID]bool
	// final is every leaf Pane's final boxes. The overlay lays each whole
	// Pane out at final[id].Column and an opaque mask covers the real Pane
	// there while the copy moves; pinPanes froze the same body boxes.
	final map[model.PaneID]motion.PaneRects
	// ghosts are the exiting Panes' retained surfaces and the boxes they
	// showed. The view draws each as an opaque ghost and releases its surface
	// when the run ends, is replaced, or warps. A ghost is owned when the
	// state retained its surface (a closed Pane); a same-Tab move's old
	// position is a live copy of the moving Pane and must never be released.
	ghosts map[model.PaneID]paneMotionGhost
	// pins are the Surfaces whose final grid this run set at the change;
	// they are released if their own layout never confirms the pin.
	pins map[model.PaneID]state.ViewportMotion
}

// paneMotionGhost is one exiting Pane a close run draws. info keeps its last
// state, so a pending Pane's placeholder and a failed Pane's error are the
// ghost; rects are the whole-Pane boxes it showed when the close began. A
// blank ghost is the last resort when no state retained a Pane info: it is an
// opaque pane-shaped background, never the connecting placeholder.
//
// The preferred snapshot route is unavailable in production. MyGo v0.9.2's
// ui.NewBitmap wraps an image.Image the caller already owns; it exposes no
// application-level raster capture of an element or window (the only frame
// image accessor is the test harness's Tester.Image). A production Surface
// therefore keeps its live terminal and the view paints a still copy through
// ViewInert while the state delays the Surface's destruction until the run
// ends, warps, or is superseded. A Surface without ViewInert paints only its
// opaque background (the blank last resort). Ticket 17's real-terminal tests
// exercise the live route and that blank fallback; the fake transport proves
// the ghost shows the terminal's actual screen content.
type paneMotionGhost struct {
	info   state.PaneInfo
	rects  motion.PaneRects
	handle *state.PaneGhost
	blank  bool
	// owned marks a closed Pane's retained surface. The run releases it and
	// forgets its boxes when the ghost ends; an unowned ghost is the live
	// moving Pane copied at its old position for a same-Tab move.
	owned bool
}

// rectWithin reports that every edge of a is within eps of b.
func rectWithin(a, b ui.Rect, eps float32) bool {
	diff := func(v float32) float32 {
		if v < 0 {
			return -v
		}
		return v
	}
	return diff(a.X-b.X) <= eps && diff(a.Y-b.Y) <= eps &&
		diff(a.W-b.W) <= eps && diff(a.H-b.H) <= eps
}

// workspacePaneMotion is the main window's Pane motion runtime. It records
// the last frame's Pane boxes, consumes the state's latest event, freezes the
// final grids at the change, and draws the overlay for the run. Each window
// renderer owns one, so a second renderer never shares progress or pins.
type workspacePaneMotion struct {
	state      *state.State
	controller *motion.Controller
	mode       paneMotionMode
	// body plays Tab open, close, and switch as one whole-body transition
	// below the Tab strip. It is separate from the Pane run: a Tab change
	// never splits or reflows a Pane tree.
	body *workspaceBodyMotion

	rects  map[model.PaneID]motion.PaneRects
	region ui.Rect

	frames int
	run    *paneMotionRun
	// windowHidden is the answer c.WindowHidden gave on the last frame. It is
	// the view's explicit visibility signal: asking for it is also what makes
	// MyGo build one frame as the window hides and one as it shows. An event
	// seen while hidden, or on the first frame after it, warps instead of
	// replaying motion the user did not see. Wall-clock event age cannot tell
	// a hidden view from a late one, so it is never consulted.
	windowHidden bool
	// animationFrames counts the frames the runtime asked for while a run
	// played. A warped event asks for none; tests assert it.
	animationFrames int
	// effectiveMagnified is the Pane the next layout must show alone. It is
	// the state's zoom except while a zoom run keeps the split (or the old
	// magnified Pane) on screen behind its overlay. The view reads it instead
	// of the state so a zoom in does not rebuild the tree per frame.
	effectiveMagnified model.PaneID
}

func newWorkspacePaneMotion(s *state.State, mode paneMotionMode) *workspacePaneMotion {
	return &workspacePaneMotion{
		state:      s,
		controller: motion.NewController(s.Motion),
		mode:       mode,
		body:       newWorkspaceBodyMotion(mode),
		rects:      map[model.PaneID]motion.PaneRects{},
	}
}

// preFrame consumes the latest event before the frame is laid out. The event
// either starts a run, which freezes the final grids and covers the real
// Panes with their moving copies until it ends, or warps: the final grids are
// set once and the layout jumps to its final boxes.
func (wm *workspacePaneMotion) preFrame(c *ui.Context, s *state.State) {
	now := c.Now()
	firstFrame := wm.frames == 0
	wm.frames++
	// Reading the window's visibility every frame is the deterministic warp
	// signal (ticket 08): MyGo builds the view once as the window hides and
	// once as it shows, and holds other frames while hidden. An event the
	// view first sees on either of those frames was not on screen, so it
	// warps; a visible view animates whatever it sees, however late the frame.
	hidden := c.WindowHidden()
	wasHidden := wm.windowHidden
	wm.windowHidden = hidden
	setting := s.Motion()
	event, ok := s.MotionEvent()
	unseen := ok && wm.controller.Unseen(event.Seq)
	// A Pane event consumed on this frame starts its own animation. MyGo's
	// DisableAnimations is window-wide, so a Tab body warp using it would also
	// rest that native Pane transition while the Pane's switch is still on;
	// the body waits it out instead (the one-switch rule).
	paneMotionPending := unseen && wm.paneMotionUsesMyGo(c, setting, event, firstFrame, hidden, wasHidden)
	// A Pane transition MyGo is already carrying also keeps a body warp from
	// using DisableAnimations on this frame, even when the newest layout change
	// records no event at all (a batch Tab close): the Pane run must keep
	// playing (the one-switch rule).
	paneMotionActive := wm.paneTransitionInFlight()
	// A close of a Tab this body is not showing ends its run with a warp on
	// this frame; tick must not schedule the animation frame that warp drops.
	otherClose := unseen && wm.body.endsOtherBody(event)
	// A body run advances or warps before this frame is laid out. A hidden
	// window, an off switch, Reduce Motion, or a batch close with no event ends
	// it without asking for another frame.
	wm.body.tick(c, s, setting, hidden, paneMotionPending, paneMotionActive, otherClose)

	activeIndex := s.ActiveTab()
	activeID, _ := s.TabID(activeIndex)
	// The tree is built after preFrame returns, so the zoom the layout shows
	// is decided here and read by mainTabRef. A run that keeps the split (or
	// the old magnified Pane) overrides the state for this frame; every exit
	// path updates it.
	defer wm.updateEffective(activeIndex)

	if wm.run != nil && wm.run.tabID != activeID {
		// An animation belongs to its Tab; leaving the Tab warps it.
		wm.finish()
	}
	if wm.run != nil && (!setting.Enabled || c.Preferences().ReduceMotion) {
		// Switching motion off mid-run warps at once and stops asking for
		// frames (ticket 08).
		wm.finish()
	}
	if wm.run != nil && wm.run.kind == motion.PaneReorder {
		// A reorder is the one event whose three parts have their own
		// switches. Switching one off mid-run warps only that part; the
		// rest keeps playing. An empty run is over (ticket 18).
		wm.gateReorder(wm.run, setting)
		if len(wm.run.anim.Visuals) == 0 {
			wm.finish()
		}
	} else if wm.run != nil && !motion.Enabled(setting, wm.run.kind) {
		wm.finish()
	}
	if wm.run != nil && hidden {
		// Nothing of the window shows while hidden; a run that outlives the
		// hide is over before it is seen again.
		wm.finish()
	}

	if !unseen {
		wm.advanceRun(c, now)
		return
	}
	wm.controller.MarkSeen(event.Seq)
	if event.Kind == motion.TabReorder {
		// A Tab reorder moves the strip's items only; the body does not move
		// (ticket 20). It must not warp, restart, or release a body run, and a
		// body switch in flight keeps playing to its normal final. A Pane run
		// continues for the same reason: the reorder changes no Pane's layout.
		wm.advanceRun(c, now)
		return
	}
	switch event.Kind {
	case motion.TabOpen, motion.TabClose, motion.TabSwitch:
		// A Tab event is a whole-body transition, never a Pane run. Any Pane
		// run or retained Pane ghost from the Tab being left is over.
		wm.finish()
		s.ReleasePaneGhosts()
		visible := !firstFrame && !hidden && !wasHidden && !c.Preferences().ReduceMotion
		wm.body.consume(c, s, event, setting, visible, paneMotionPending)
		return
	}
	// Any other event (a Pane event, or a strip-only Tab reorder) ends a body
	// run that is still playing; this frame is the final layout. When the Pane
	// event animates through MyGo, its transition shares this frame and must
	// not be rested, so the body warps alone by generation instead of with
	// DisableAnimations (the one-switch rule).
	if wm.body.phase != bodyIdle {
		if paneMotionPending {
			wm.body.warpBodyOnly()
		} else {
			wm.body.warp(c)
		}
	}
	s.ReleaseTabGhosts()
	if event.TabID != activeID || activeIndex < 0 {
		wm.finish()
		// A ghost retained for a Tab this view no longer shows has no run to
		// draw it; release its surface now.
		s.ReleasePaneGhosts()
		return
	}

	visible := setting.Enabled && !c.Preferences().ReduceMotion &&
		!firstFrame && !hidden && !wasHidden
	// Only a visible view has the previous frame's boxes on screen; a hidden
	// or first frame must not pin a grid from geometry it never showed. The
	// final layout sizes each Pane once instead.
	pin := !hidden && !wasHidden && !firstFrame
	switch event.Kind {
	case motion.PaneOpen:
		animate := visible && setting.PaneOpen
		wm.plan(c, s, activeIndex, event, setting, animate, pin, now)
	case motion.PaneClose:
		animate := visible && setting.PaneClose
		wm.planClose(c, s, activeIndex, event, setting, animate, pin, now)
	case motion.PaneReorder:
		wm.planReorder(c, s, activeIndex, event, setting, visible, pin, now)
	default:
		wm.finish()
		s.ReleasePaneGhosts()
	}
}

// advanceRun moves a playing Pane run to now and asks for its next frame, or
// ends it on the frame its animation completes. A frame that consumes no
// event, and a Tab reorder no part of the Pane or body motion owns, use it so
// the run keeps its normal final instead of being cut.
func (wm *workspacePaneMotion) advanceRun(c *ui.Context, now time.Time) {
	if wm.run == nil {
		return
	}
	if _, done := wm.run.anim.Progress(now); done {
		wm.finish()
		return
	}
	wm.animationFrames++
	c.AnimationFrame()
}

// updateEffective decides the Pane the next layout shows alone. While a zoom
// run is still covering the split, the state's new zoom must not switch the
// tree yet; the run's render says what stays behind the overlay.
func (wm *workspacePaneMotion) updateEffective(index int) {
	var pane model.PaneID
	if index >= 0 {
		pane = wm.state.MagnifiedPaneID(index)
	}
	if run := wm.run; run != nil && run.zoom {
		switch run.render {
		case zoomRenderSplit:
			pane = ""
		case zoomRenderPane:
			pane = run.renderPane
		}
	}
	wm.effectiveMagnified = pane
}

// gateReorder applies the per-part switches to a running same-Tab move: the
// close ghost obeys PaneClose, the new-position overlay PaneOpen, and the
// sibling spring PaneReorder. A part whose switch went off is removed and
// warps to its final position while the other parts continue. A closed
// Pane's carried ghost is released with its part; the live moving Pane's old
// copy is only a view of the still-running Pane and is never released.
func (wm *workspacePaneMotion) gateReorder(run *paneMotionRun, setting domainpreferences.Motion) {
	if run == nil || run.kind != motion.PaneReorder {
		return
	}
	keep := run.anim.Visuals[:0]
	for _, v := range run.anim.Visuals {
		on := true
		switch v.Kind {
		case motion.Exit:
			on = setting.PaneClose
		case motion.Enter:
			on = setting.PaneOpen
		default:
			on = setting.PaneReorder
		}
		if on {
			keep = append(keep, v)
			continue
		}
		if v.Kind == motion.Exit {
			if ghost, ok := run.ghosts[v.Pane]; ok {
				if ghost.owned {
					// A closed Pane's retained surface is the run's to
					// release. A same-Tab move's old-position copy has no
					// handle: the moving Pane is alive at its final place.
					if ghost.handle != nil {
						ghost.handle.Release()
					}
					delete(wm.rects, v.Pane)
				}
				delete(run.ghosts, v.Pane)
			}
		}
	}
	run.anim.Visuals = keep
	if !setting.PaneReorder {
		run.nativePanes = nil
		run.spring = nil
	}
}

// paneVisualState is what the user sees of a Pane at a change: the rect and
// opacity the previous run's overlay painted, or the settled boxes of a Pane
// that was not moving. An interruption restarts from it, so a Pane that was
// half faded or half moved does not snap.
type paneVisualState struct {
	rect    ui.Rect
	opacity float32
}

// currentVisual reports what of Pane id the user saw at the change. A part
// with its own duration or spring is read at now, never with the run's eased
// progress, so an interrupting event starts from exactly what the frame
// painted.
func (wm *workspacePaneMotion) currentVisual(prev *paneMotionRun, now time.Time,
	id model.PaneID) (paneVisualState, bool) {
	if prev != nil {
		if v, ok := prev.anim.VisualFor(id); ok {
			r, opacity := prev.anim.StateAt(v, now)
			return paneVisualState{rect: r, opacity: opacity}, true
		}
	}
	if r, ok := wm.rects[id]; ok && r.Valid() {
		return paneVisualState{rect: r.Column, opacity: 1}, true
	}
	return paneVisualState{}, false
}

// currentVelocity is the rect velocity of Pane id at the change, or zero
// when the Pane was not moving. A new spring starts with it, so retargeting
// a zoom or a sibling does not restart from rest.
func (wm *workspacePaneMotion) currentVelocity(prev *paneMotionRun, now time.Time, id model.PaneID) ui.Rect {
	if prev == nil {
		return ui.Rect{}
	}
	v, ok := prev.anim.VisualFor(id)
	if !ok {
		return ui.Rect{}
	}
	return prev.anim.VelocityAt(v, now)
}

// visualInFlight reports that Pane id is still moving at now in the previous
// run. MyGo retargets a native element's channel only while that element
// survives the layout change; a same-Tab move recreates the split around the
// target, so the sibling is a new element and its Match starts from rest.
// Such a moving Pane must therefore continue through the overlay, whose
// spring carries the measured velocity; a Pane at rest can still start
// natively from its matched box.
func (wm *workspacePaneMotion) visualInFlight(prev *paneMotionRun, now time.Time, id model.PaneID) bool {
	if prev == nil || prev.anim == nil {
		return false
	}
	v, ok := prev.anim.VisualFor(id)
	if !ok || v.Kind != motion.Move {
		return false
	}
	rect, _ := prev.anim.StateAt(v, now)
	return !rectWithin(rect, v.To, 0.5)
}

// plan turns one Pane-open event into a run, or warps it. The final boxes are
// computed from the Tab's tree and the pane region the previous frame showed,
// never from LayoutBounds at the change: the layout has not run yet and its
// report would lag.
func (wm *workspacePaneMotion) plan(c *ui.Context, s *state.State, index int,
	event motion.Event, setting domainpreferences.Motion, animate, pin bool, now time.Time) {
	root, _, ok := s.TabTree(index)
	if !ok || root == nil {
		wm.finish()
		return
	}
	targetRects, targetOK := wm.rects[event.TargetID]
	frameTop, frameRight, frameBottom, frameLeft := kit.PaneFrameInset()
	final := paneFinalRects(root, wm.region, wm.rects, c.Theme().Space(10), targetRects,
		!root.IsPane(), frameTop, frameRight, frameBottom, frameLeft)
	targetFinal, haveTarget := final[event.TargetID]
	newFinal, haveNew := final[event.PaneID]
	if !targetOK || !targetRects.Valid() || wm.region.W <= 0 || wm.region.H <= 0 || !haveTarget || !haveNew {
		animate = false
	}
	if !animate {
		wm.finish()
		if pin {
			wm.pinPanes(index, []model.PaneID{event.TargetID, event.PaneID}, final, event.TargetID)
		}
		return
	}

	prev := wm.run
	old, oldOK := wm.currentVisual(prev, now, event.TargetID)
	if !oldOK {
		wm.finish()
		if pin {
			wm.pinPanes(index, []model.PaneID{event.TargetID, event.PaneID}, final, event.TargetID)
		}
		return
	}
	if event.FromMagnified != "" {
		// A split from a magnified Pane is one continuous open whose old
		// Pane starts at the full window, not at LayoutBounds or at a
		// suppression frame's split slot. Rebuild that whole-Pane box from
		// the same tree geometry the zoom used.
		single := domainworkspace.NewPaneNode(event.TargetID)
		full := paneFinalRects(single, wm.region, wm.rects, c.Theme().Space(10), targetRects,
			!root.IsPane(), frameTop, frameRight, frameBottom, frameLeft)
		fullOld, ok := full[event.TargetID]
		if !ok || fullOld.Column.W <= 0 || fullOld.Column.H <= 0 {
			animate = false
		} else {
			old = paneVisualState{rect: fullOld.Column, opacity: 1}
		}
	}
	if !animate {
		wm.finish()
		if pin {
			wm.pinPanes(index, []model.PaneID{event.TargetID, event.PaneID}, final, event.TargetID)
		}
		return
	}
	content := old.rect

	// Latest event wins: stop the old run, whose current visuals are the new
	// starts, before pinning the new finals.
	wm.finish()

	run := &paneMotionRun{
		kind:   motion.PaneOpen,
		tabID:  event.TabID,
		seq:    event.Seq,
		target: event.TargetID,
		// A magnified split grows the real old Pane from the full window;
		// the native test route starts from the previous frame's split box
		// and cannot represent that, so this one case uses the overlay.
		native: wm.mode.native && event.FromMagnified == "",
		final:  final,
		pins:   map[model.PaneID]state.ViewportMotion{},
	}
	var visuals []motion.Visual
	seen := map[model.PaneID]bool{}
	if !run.native {
		// The old Pane is animated as a whole Pane: its frame, header, and
		// body move together, laid out once at the final column and mapped
		// back to the column the previous frame showed. It starts at the
		// opacity the user last saw and becomes fully visible over the run.
		visuals = append(visuals, motion.Visual{
			Pane: event.TargetID,
			Box:  targetFinal.Column, From: old.rect, To: targetFinal.Column, Content: targetFinal.Column,
			Kind: motion.Move, FromOpacity: old.opacity, ToOpacity: 1,
		})
	}
	seen[event.TargetID] = true
	if prev != nil {
		// Panes the old run was moving keep going from what the user sees to
		// their new final boxes, so an interrupting event does not snap them
		// in place or in opacity.
		for _, v := range prev.anim.Visuals {
			if seen[v.Pane] {
				continue
			}
			end, ok := final[v.Pane]
			if !ok {
				continue
			}
			box := end.Column
			from, opacity := prev.anim.StateAt(v, now)
			// Continue the fade from what is on screen to the old visual's
			// target. Every current route targets opaque; an unset target
			// stays visible instead of fading the Pane out.
			toOpacity := v.ToOpacity
			if toOpacity == 0 {
				toOpacity = 1
			}
			kind, style := motion.Move, setting.OpenStyle
			visuals = append(visuals, motion.Visual{
				Pane: v.Pane, Box: box, From: from, To: box, Content: box,
				Kind: kind, Style: style, FromOpacity: opacity, ToOpacity: toOpacity,
			})
			seen[v.Pane] = true
		}
	}
	visuals = append(visuals, motion.Visual{
		Pane: event.PaneID,
		Box:  newFinal.Column, To: newFinal.Column, Content: content,
		Kind: motion.Enter, Style: setting.OpenStyle,
		FromOpacity: 0, ToOpacity: 1,
	})

	run.anim = &motion.Animation{
		Started:  now,
		Duration: motion.ScaleMotion(motion.BaseDuration(motion.PaneOpen), setting.Speed),
		Visuals:  visuals,
	}
	wm.run = run
	wm.pinPanes(index, []model.PaneID{event.TargetID, event.PaneID}, final, event.TargetID)
	wm.animationFrames++
	c.AnimationFrame()
}

// planReorder turns one PaneReorder into a same-Tab move or a zoom, or
// warps it. The three parts of a move have independent switches; a zoom has
// only the PaneReorder spring.
func (wm *workspacePaneMotion) planReorder(c *ui.Context, s *state.State, index int,
	event motion.Event, setting domainpreferences.Motion, visible, pin bool, now time.Time) {
	if event.IsZoom() {
		wm.planZoom(c, s, index, event, setting, visible, pin, now)
		return
	}
	wm.planMove(c, s, index, event, setting, visible, pin, now)
}

// planMove animates a same-Tab move: the Pane's old position is an opaque
// close-style ghost of the still-live Surface, its new position is an
// open-style overlay over the already-final real Pane, and every sibling
// that changes box makes room with the reorder spring. Each part obeys its
// own switch; a part that is off warps while the others play.
func (wm *workspacePaneMotion) planMove(c *ui.Context, s *state.State, index int,
	event motion.Event, setting domainpreferences.Motion, visible, pin bool, now time.Time) {
	root, _, ok := s.TabTree(index)
	if !ok || root == nil || wm.region.W <= 0 || wm.region.H <= 0 {
		wm.finish()
		return
	}
	moved := event.PaneID
	old, haveOld := wm.rects[moved]
	frameTop, frameRight, frameBottom, frameLeft := kit.PaneFrameInset()
	final := paneFinalRects(root, wm.region, wm.rects, c.Theme().Space(10), old,
		!root.IsPane(), frameTop, frameRight, frameBottom, frameLeft)
	end, haveEnd := final[moved]
	closeOn := visible && setting.PaneClose
	openOn := visible && setting.PaneOpen
	reorderOn := visible && setting.PaneReorder
	if !haveOld || !old.Valid() || !haveEnd {
		// The old-position ghost, the new-position overlay and the sibling
		// spring all start from the previous frame's recorded boxes. Without
		// them the whole change warps; the final grid is still set once.
		wm.finish()
		if pin {
			wm.pinSurvivors(index, "", final)
		}
		return
	}
	if !closeOn && !openOn && !reorderOn {
		wm.finish()
		if pin {
			wm.pinSurvivors(index, "", final)
		}
		return
	}

	prev := wm.run
	// Carry a closed Pane's ghost that this move does not replace, but never
	// an old-position copy of a live moving Pane: that Pane keeps its
	// Surface and the new layout is the truth.
	carried := map[model.PaneID]paneMotionGhost{}
	if prev != nil && closeOn {
		rest := map[model.PaneID]paneMotionGhost{}
		for id, ghost := range prev.ghosts {
			if id != moved && ghost.owned {
				carried[id] = ghost
				continue
			}
			rest[id] = ghost
		}
		prev.ghosts = rest
	}
	oldState, _ := wm.currentVisual(prev, now, moved)
	oldStart, oldOpacity := oldState.rect, oldState.opacity
	ghostBox := old.Column
	if prev != nil {
		if v, ok := prev.anim.VisualFor(moved); ok && v.Box.W > 0 && v.Box.H > 0 {
			ghostBox = v.Box
		}
	}

	// Latest event wins: stop the old run, whose current visuals are the new
	// starts, before pinning the new finals.
	wm.finish()

	spring := motion.ReorderSpring(setting.Speed)
	run := &paneMotionRun{
		kind:        motion.PaneReorder,
		tabID:       event.TabID,
		seq:         event.Seq,
		target:      event.TargetID,
		final:       final,
		ghosts:      carried,
		pins:        map[model.PaneID]state.ViewportMotion{},
		nativePanes: map[model.PaneID]bool{},
	}
	if reorderOn {
		run.spring = &spring
	}
	var visuals []motion.Visual
	native := wm.nativeReorderEnabled()
	if reorderOn {
		for id, sibling := range final {
			if id == moved {
				continue
			}
			seen, ok := wm.currentVisual(prev, now, id)
			if !ok {
				continue
			}
			if rectWithin(seen.rect, sibling.Column, 0.5) && seen.opacity >= 0.999 {
				continue
			}
			visual := motion.Visual{
				Pane: id, Box: sibling.Column, From: seen.rect, To: sibling.Column,
				Content: sibling.Column, Kind: motion.Move,
				FromOpacity: seen.opacity, ToOpacity: 1,
				Spring:   &spring,
				Velocity: wm.currentVelocity(prev, now, id),
			}
			// A native sibling can start from rest (a fresh move) but cannot
			// carry a velocity through an interruption: the move recreates
			// the split around the target, so MyGo sees a new element and its
			// Match starts from rest. Paint that Pane with the opaque overlay
			// instead, whose spring keeps the velocity measured above.
			if native && !wm.visualInFlight(prev, now, id) {
				visual.Native = true
				run.nativePanes[id] = true
			}
			visuals = append(visuals, visual)
		}
	}
	if openOn {
		// The real Pane is already at its final position; the copy enters
		// there over a mask. Slide resolves against the Pane's old box, so it
		// comes from where the Pane was. If the previous run was already
		// opening this Pane, its visible rect and opacity are the new start
		// and the open restarts its full duration from there, so a reverse
		// move does not flash the masked real Pane.
		enter := motion.Visual{
			Pane: moved, Box: end.Column, To: end.Column, Content: old.Column,
			Kind: motion.Enter, Style: setting.OpenStyle,
			FromOpacity: 0, ToOpacity: 1,
			Duration: motion.ScaleMotion(motion.BaseDuration(motion.PaneOpen), setting.Speed),
		}
		if prev != nil {
			for _, was := range prev.anim.Visuals {
				if was.Pane != moved || was.Kind != motion.Enter {
					continue
				}
				from, opacity := prev.anim.StateAt(was, now)
				enter.From = from
				enter.FromOpacity = opacity
				enter.Interrupted = true
				enter.Duration = motion.ScaleMotion(motion.BaseDuration(motion.PaneOpen), setting.Speed)
				break
			}
		}
		visuals = append(visuals, enter)
	}
	// A closed Pane's ghost carried from an earlier close keeps leaving from
	// where the user saw it, at the close part's duration.
	for id, ghost := range carried {
		if id == moved || prev == nil {
			continue
		}
		v, ok := prev.anim.VisualFor(id)
		if !ok {
			continue
		}
		from, opacity := prev.anim.StateAt(v, now)
		visuals = append(visuals, motion.Visual{
			Pane: id, Box: ghost.rects.Column, From: from, To: from, Content: wm.region,
			Kind: motion.Exit, Style: setting.CloseStyle, FromOpacity: opacity,
			Duration: motion.ScaleMotion(motion.BaseDuration(motion.PaneClose), setting.Speed),
		})
	}
	if closeOn {
		ghost := paneMotionGhost{rects: old}
		if info, ok := s.PaneInfoAt(index, moved); ok {
			ghost.info = info
		} else {
			ghost.blank = true
		}
		run.ghosts[moved] = ghost
		visuals = append(visuals, motion.Visual{
			Pane: moved, Box: ghostBox, From: oldStart, To: oldStart,
			Content: wm.region, Kind: motion.Exit, Style: setting.CloseStyle,
			FromOpacity: oldOpacity,
			Duration:    motion.ScaleMotion(motion.BaseDuration(motion.PaneClose), setting.Speed),
		})
	}
	if len(visuals) == 0 {
		wm.finish()
		if pin {
			wm.pinSurvivors(index, "", final)
		}
		return
	}
	run.anim = &motion.Animation{Started: now, Visuals: visuals}
	run.anim.Measure()
	wm.run = run
	if pin {
		wm.pinSurvivors(index, "", final)
	}
	wm.animationFrames++
	c.AnimationFrame()
}

// planZoom springs the magnified Pane between its slot and the full window.
// Zoom in keeps the split (or, for a direct A→B, the Pane that was
// magnified) on screen until the opaque copy covers it; zoom out renders the
// split behind the shrinking copy at once. The Pane's own grid is the final
// one throughout, so the PTY gets at most one resize.
func (wm *workspacePaneMotion) planZoom(c *ui.Context, s *state.State, index int,
	event motion.Event, setting domainpreferences.Motion, visible, pin bool, now time.Time) {
	root, _, ok := s.TabTree(index)
	if !ok || root == nil || wm.region.W <= 0 || wm.region.H <= 0 {
		wm.finish()
		return
	}
	from, to := event.FromMagnified, event.ToMagnified
	zoomPane := event.PaneID
	if zoomPane == "" {
		zoomPane = zoomedZoomPane(from, to)
	}
	if zoomPane == "" {
		wm.finish()
		return
	}
	split := !root.IsPane()
	frameTop, frameRight, frameBottom, frameLeft := kit.PaneFrameInset()
	var final map[model.PaneID]motion.PaneRects
	var end motion.PaneRects
	render := zoomRenderState
	renderPane := model.PaneID("")
	if to != "" {
		// The final tree is the single magnified Pane. It still wears a
		// PaneFrame when the Tab has more than one leaf, exactly as the real
		// single-Pane layout will after the run.
		single := domainworkspace.NewPaneNode(to)
		final = paneFinalRects(single, wm.region, wm.rects, c.Theme().Space(10), wm.rects[to],
			split, frameTop, frameRight, frameBottom, frameLeft)
		var have bool
		end, have = final[to]
		if !have || end.Column.W <= 0 || end.Column.H <= 0 {
			wm.finish()
			if pin {
				wm.pinSurvivors(index, "", final)
			}
			return
		}
		render = zoomRenderSplit
		// A direct A→B covers the Pane that was magnified. Rendering that
		// Pane alone keeps its full-window grid and hides the split, which is
		// what the user saw a frame ago; B's copy then grows over it.
		if from != "" && from == wm.effectiveMagnified && from != to {
			render = zoomRenderPane
			renderPane = from
		}
	} else {
		final = paneFinalRects(root, wm.region, wm.rects, c.Theme().Space(10), wm.rects[from],
			split, frameTop, frameRight, frameBottom, frameLeft)
		var have bool
		end, have = final[from]
		if !have || end.Column.W <= 0 || end.Column.H <= 0 {
			wm.finish()
			if pin {
				wm.pinSurvivors(index, "", final)
			}
			return
		}
	}
	prev := wm.run
	old, oldOK := wm.currentVisual(prev, now, zoomPane)
	if !oldOK || (!visible || !setting.PaneReorder) {
		wm.finish()
		if pin {
			wm.pinSurvivors(index, "", final)
		}
		return
	}
	if rectWithin(old.rect, end.Column, 0.5) && old.opacity >= 0.999 {
		// The newly magnified Pane is already where it must end. A one-Pane
		// Tab's zoom changes no geometry; switch the tree at once without
		// spending an animation frame. A direct magnification reversed while
		// its outgoing copy is still visible is the exception: that copy is
		// what the user sees moving, so carry it back to where it came from
		// over the newly final layout instead of warping it away.
		if home, ok := wm.zoomReversalHome(prev, from, to); ok {
			if out, moving := wm.movingVisual(prev, now, from); moving {
				wm.planZoomReversal(c, index, event, setting, pin, now, final,
					zoomPane, from, home, out, prev)
				return
			}
		}
		wm.finish()
		if pin {
			wm.pinSurvivors(index, "", final)
		}
		return
	}

	// Latest event wins. A zoom supersedes a close the view still had in
	// flight; the state already retained no newer ghost for this event.
	wm.finish()

	spring := motion.ReorderSpring(setting.Speed)
	run := &paneMotionRun{
		kind:        motion.PaneReorder,
		tabID:       event.TabID,
		seq:         event.Seq,
		target:      zoomPane,
		zoom:        true,
		zoomPane:    zoomPane,
		zoomHome:    wm.rects[zoomPane],
		render:      render,
		renderPane:  renderPane,
		spring:      &spring,
		final:       final,
		pins:        map[model.PaneID]state.ViewportMotion{},
		nativePanes: map[model.PaneID]bool{},
	}
	visual := motion.Visual{
		Pane: zoomPane, Box: end.Column, From: old.rect, To: end.Column,
		Content: wm.region, Kind: motion.Move,
		FromOpacity: old.opacity, ToOpacity: 1,
		Spring:   &spring,
		Velocity: wm.currentVelocity(prev, now, zoomPane),
		NoMask:   true,
	}
	run.anim = &motion.Animation{Started: now, Visuals: []motion.Visual{visual}}
	run.anim.Measure()
	wm.run = run
	if pin {
		wm.pinSurvivors(index, "", final)
	}
	wm.animationFrames++
	c.AnimationFrame()
}

// zoomedZoomPane is the Pane a zoom moves when the event names none: the
// new magnified Pane, otherwise the one leaving.
func zoomedZoomPane(from, to model.PaneID) model.PaneID {
	if to != "" {
		return to
	}
	return from
}

// movingVisual reports what Pane id showed in prev at now, but only while its
// visual is still in flight: a settled or absent visual is not a copy an
// interruption can carry.
func (wm *workspacePaneMotion) movingVisual(prev *paneMotionRun, now time.Time,
	id model.PaneID) (paneVisualState, bool) {
	if prev == nil || prev.anim == nil {
		return paneVisualState{}, false
	}
	v, ok := prev.anim.VisualFor(id)
	if !ok || v.Kind != motion.Move {
		return paneVisualState{}, false
	}
	rect, opacity := prev.anim.StateAt(v, now)
	if rectWithin(rect, v.To, 0.5) {
		return paneVisualState{}, false
	}
	return paneVisualState{rect: rect, opacity: opacity}, true
}

// zoomReversalHome reports where a mid-run direct-magnification reversal must
// send its outgoing copy: the boxes the previous zoom's Pane showed before it
// grew. Only the Pane that was growing can reverse; a zoom of a different
// Pane has no outgoing copy to carry.
func (wm *workspacePaneMotion) zoomReversalHome(prev *paneMotionRun,
	from, to model.PaneID) (motion.PaneRects, bool) {
	if prev == nil || !prev.zoom || from == "" || to == "" || from == to {
		return motion.PaneRects{}, false
	}
	if prev.zoomPane != from || !prev.zoomHome.Valid() {
		return motion.PaneRects{}, false
	}
	return prev.zoomHome, true
}

// planZoomReversal carries a direct magnification's outgoing copy back to its
// remembered slot. The newly magnified Pane is already at its final full
// window, so it needs no visual; the outgoing copy keeps the rect, opacity
// and velocity the user saw and springs home over that layout. The outgoing
// Pane is returning to its slot, so its grid is pinned there once and its
// recorded boxes are the same home pair: the real Pane is not laid out while
// the new Pane is magnified, so nothing else would reconcile its full-window
// viewport with the split boxes the next event reads as a pin source.
func (wm *workspacePaneMotion) planZoomReversal(c *ui.Context, index int, event motion.Event,
	setting domainpreferences.Motion, pin bool, now time.Time, final map[model.PaneID]motion.PaneRects,
	zoomPane, outgoing model.PaneID, home motion.PaneRects, out paneVisualState, prev *paneMotionRun) {
	// Latest event wins: out was read from the run the user saw; stop it
	// before pinning the new finals.
	wm.finish()
	final[outgoing] = home
	spring := motion.ReorderSpring(setting.Speed)
	run := &paneMotionRun{
		kind:        motion.PaneReorder,
		tabID:       event.TabID,
		seq:         event.Seq,
		target:      zoomPane,
		zoom:        true,
		zoomPane:    zoomPane,
		zoomHome:    wm.rects[zoomPane],
		render:      zoomRenderState,
		spring:      &spring,
		final:       final,
		pins:        map[model.PaneID]state.ViewportMotion{},
		nativePanes: map[model.PaneID]bool{},
	}
	visual := motion.Visual{
		Pane: outgoing, Box: home.Column, From: out.rect, To: home.Column,
		Content: home.Column, Kind: motion.Move,
		FromOpacity: out.opacity, ToOpacity: 1,
		Spring:   &spring,
		Velocity: wm.currentVelocity(prev, now, outgoing),
		NoMask:   true,
	}
	run.anim = &motion.Animation{Started: now, Visuals: []motion.Visual{visual}}
	run.anim.Measure()
	wm.run = run
	if pin {
		// The outgoing Pane is zooming out: its surface currently fills the
		// window, so its grid returns to the remembered slot now, once. The
		// pin target is the home body expressed in the same viewport units
		// the zoom pinned; deriving it from the stale split boxes instead
		// would widen it past the window.
		wm.pinZoomHome(index, prev, outgoing, home)
		wm.pinSurvivors(index, outgoing, final)
	}
	wm.animationFrames++
	c.AnimationFrame()
}

// pinZoomHome returns the outgoing Pane of a direct-magnification reversal to
// the slot grid it showed before the zoom, and records the same home boxes.
// The pair must agree: the next magnification reads wm.rects[outgoing] as its
// pin source and motionViewport as the room that source currently has, so a
// full-window viewport left beside split boxes would make the next pin widen
// the grid out of the window.
func (wm *workspacePaneMotion) pinZoomHome(index int, prev *paneMotionRun,
	outgoing model.PaneID, home motion.PaneRects) {
	if !home.Valid() {
		return
	}
	info, ok := wm.state.PaneInfoAt(index, outgoing)
	if !ok || info.Surface == nil {
		return
	}
	vm, ok := info.Surface.(state.ViewportMotion)
	if !ok {
		return
	}
	// The outgoing Pane's full grid corresponds to the box the interrupted
	// zoom laid it out in, not to its stale split record. Keep the same
	// body-to-viewport difference pinPanes uses (layout rounding can make
	// them differ sub-DIP), so the PTY lands exactly on the room it had at
	// its slot: one resize home.
	source := home
	if prev != nil {
		if end, ok := prev.final[outgoing]; ok && end.Body.W > 0 && end.Body.H > 0 {
			source = end
		}
	}
	viewW, viewH, ok := wm.motionViewport(index, outgoing, outgoing)
	if !ok {
		return
	}
	vm.PinViewport(home.Body.W-(source.Body.W-viewW), home.Body.H-(source.Body.H-viewH))
	if wm.run != nil {
		wm.run.pins[outgoing] = vm
	}
	// The Pane is not laid out while the new Pane is magnified, so nothing
	// else can correct these boxes before the next event.
	wm.rects[outgoing] = home
}

// planClose turns one Pane-close event into an exit run, or warps it. The
// final layout is the collapsed tree; each survivor's grid is pinned at the
// change, and the exiting Pane is an opaque ghost laid out at the boxes it
// showed. A magnified close starts the ghost at the whole pane region and
// reveals the split at its final layout; its survivors warp rather than
// animate under the ghost.
func (wm *workspacePaneMotion) planClose(c *ui.Context, s *state.State, index int,
	event motion.Event, setting domainpreferences.Motion, animate, pin bool, now time.Time) {
	root, _, ok := s.TabTree(index)
	if !ok || root == nil {
		wm.finish()
		s.ReleasePaneGhosts()
		return
	}
	ghostHandle, haveGhost := s.TakePaneGhost(event.PaneID)
	closeRects, hadClosing := wm.rects[event.PaneID]
	frameTop, frameRight, frameBottom, frameLeft := kit.PaneFrameInset()
	split := !root.IsPane()
	final := paneFinalRects(root, wm.region, wm.rects, c.Theme().Space(10), closeRects,
		split, frameTop, frameRight, frameBottom, frameLeft)
	if !hadClosing || !closeRects.Valid() || wm.region.W <= 0 || wm.region.H <= 0 {
		animate = false
	}
	if !animate {
		wm.finish()
		if ghostHandle != nil {
			ghostHandle.Release()
		}
		if pin {
			wm.pinSurvivors(index, event.PaneID, final)
		}
		return
	}

	prev := wm.run
	// Carry the ghosts of an earlier close that this event does not replace:
	// their Panes are already gone, so the new run keeps their surfaces alive
	// and continues them from where the user sees them. A live copy of a
	// moving Pane is not retained by the state and is dropped here; that Pane
	// is still in the tree and its new layout is the truth.
	carried := map[model.PaneID]paneMotionGhost{}
	if prev != nil {
		rest := map[model.PaneID]paneMotionGhost{}
		for id, ghost := range prev.ghosts {
			if id != event.PaneID && ghost.owned {
				carried[id] = ghost
				continue
			}
			rest[id] = ghost
		}
		prev.ghosts = rest
	}
	old, oldOK := wm.currentVisual(prev, now, event.PaneID)
	if !oldOK {
		wm.finish()
		for _, ghost := range carried {
			ghost.handle.Release()
		}
		if ghostHandle != nil {
			ghostHandle.Release()
		}
		if pin {
			wm.pinSurvivors(index, event.PaneID, final)
		}
		return
	}
	// The ghost's content is laid out in the box it was painted in. An
	// interrupted open may have laid it out at its final column; the current
	// visual rect is only where it shows.
	ghostBox := closeRects.Column
	if prev != nil {
		if v, ok := prev.anim.VisualFor(event.PaneID); ok && v.Box.W > 0 && v.Box.H > 0 {
			ghostBox = v.Box
		}
	}
	start := old
	if event.Magnified {
		// The magnified Pane filled the pane region, whatever an in-flight
		// zoom run was painting: a close during a zoom in must still ghost
		// from the full window and reveal the split at its final layout.
		start = paneVisualState{rect: wm.region, opacity: 1}
	}

	// Latest event wins: stop the old run, whose current visuals are the new
	// starts and whose carried ghosts move to the new run, before pinning.
	wm.finish()

	// Native can start only from the previous frame's recorded box, so a run
	// that interrupts another (or reveals a magnified split) uses the overlay
	// instead: its start is the current visual, and a native transition would
	// animate the real Pane underneath the overlay as well.
	nativeSurvivors := wm.nativeCloseEnabled() && prev == nil && !event.Magnified
	run := &paneMotionRun{
		kind:      motion.PaneClose,
		tabID:     event.TabID,
		seq:       event.Seq,
		target:    event.TargetID,
		native:    nativeSurvivors,
		magnified: event.Magnified,
		final:     final,
		ghosts:    carried,
		pins:      map[model.PaneID]state.ViewportMotion{},
	}
	ghost := paneMotionGhost{rects: closeRects, handle: ghostHandle, owned: true}
	if haveGhost {
		ghost.info = ghostHandle.Info
	} else {
		ghost.blank = true
	}
	run.ghosts[event.PaneID] = ghost

	visuals := []motion.Visual{{
		Pane: event.PaneID,
		Box:  ghostBox, From: start.rect, To: start.rect, Content: wm.region,
		Kind: motion.Exit, Style: setting.CloseStyle,
		FromOpacity: start.opacity,
	}}
	// A carried ghost continues its exit from what the user saw, with the
	// latest event's full duration. The run's own ghost was just added to
	// the same map; it already has the visual above.
	for id, ghost := range carried {
		if prev == nil || id == event.PaneID {
			continue
		}
		v, ok := prev.anim.VisualFor(id)
		if !ok {
			continue
		}
		from, opacity := prev.anim.StateAt(v, now)
		visuals = append(visuals, motion.Visual{
			Pane: id,
			Box:  ghost.rects.Column, From: from, To: from, Content: wm.region,
			Kind: motion.Exit, Style: setting.CloseStyle, FromOpacity: opacity,
		})
	}
	// Every survivor that changes has a visual, even on the native route: the
	// native route paints the real Pane and the overlay skips this bookkeeping,
	// but an interrupting event needs it to restart from what the user sees. A
	// magnified close reveals the split at its final layout under the ghost.
	if !event.Magnified {
		for id, end := range final {
			if id == event.PaneID {
				continue
			}
			seen, ok := wm.currentVisual(prev, now, id)
			if !ok {
				continue
			}
			if rectWithin(seen.rect, end.Column, 0.5) && seen.opacity >= 0.999 {
				continue
			}
			visuals = append(visuals, motion.Visual{
				Pane: id, Box: end.Column, From: seen.rect, To: end.Column, Content: end.Column,
				Kind: motion.Move, FromOpacity: seen.opacity, ToOpacity: 1,
			})
		}
	}

	run.anim = &motion.Animation{
		Started:  now,
		Duration: motion.ScaleMotion(motion.BaseDuration(motion.PaneClose), setting.Speed),
		Visuals:  visuals,
	}
	wm.run = run
	panes := make([]model.PaneID, 0, len(final))
	for id := range final {
		if id != event.PaneID {
			panes = append(panes, id)
		}
	}
	wm.pinPanes(index, panes, final, event.TargetID)
	wm.animationFrames++
	c.AnimationFrame()
}

// pinSurvivors freezes every surviving Pane's final grid at the change. A
// Pane whose body did not change needs no pin: its own layout already reports
// the same room and delivers no resize.
func (wm *workspacePaneMotion) pinSurvivors(index int, closing model.PaneID, final map[model.PaneID]motion.PaneRects) {
	panes := make([]model.PaneID, 0, len(final))
	for id, end := range final {
		if id == closing {
			continue
		}
		if before, ok := wm.rects[id]; ok && before.Valid() &&
			before.Framed == end.Framed && rectWithin(before.Body, end.Body, 0.5) {
			continue
		}
		panes = append(panes, id)
	}
	wm.pinPanes(index, panes, final, closing)
}

// finish ends the run, lets every pinned Pane follow its own layout again,
// releases the retained surfaces of its exiting Panes, and forgets their
// boxes. Pins that their layout already confirmed were spent by the Surface
// itself. An interruption detaches the ghosts it carries before calling this,
// so a ghost that continues is not closed and reopened.
func (wm *workspacePaneMotion) finish() {
	run := wm.run
	if run == nil {
		return
	}
	for _, pin := range run.pins {
		pin.ReleaseViewport()
	}
	for id, ghost := range run.ghosts {
		ghost.handle.Release()
		if ghost.owned {
			// The Pane is gone; its boxes belonged only to the ghost.
			delete(wm.rects, id)
		}
	}
	wm.run = nil
}

// pinPanes freezes each affected Pane's final grid at the change, before its
// own layout reports the new box. The final viewport is the Pane's final body
// less the flow chrome the previous frame showed, so exactly one PTY resize is
// delivered and animation frames do no grid math (ADR 0026). A Pane that has
// no previous frame of its own (the one the split creates) takes its flow
// chrome from the split target, whose surface it replaces.
func (wm *workspacePaneMotion) pinPanes(index int, panes []model.PaneID, final map[model.PaneID]motion.PaneRects,
	fallback model.PaneID) {
	type pin struct {
		id   model.PaneID
		vm   state.ViewportMotion
		body ui.Rect
		dw   float32
		dh   float32
	}
	var pins []pin
	for _, id := range panes {
		body, ok := final[id]
		if !ok || body.Body.W <= 0 || body.Body.H <= 0 {
			continue
		}
		info, ok := wm.state.PaneInfoAt(index, id)
		if !ok || info.Surface == nil {
			continue
		}
		vm, ok := info.Surface.(state.ViewportMotion)
		if !ok {
			continue
		}
		source, ok := wm.rects[id]
		if !ok || !source.Valid() {
			source = wm.rects[fallback]
		}
		if !source.Valid() {
			continue
		}
		viewW, viewH, ok := wm.motionViewport(index, id, fallback)
		if !ok {
			// The Pane has not laid its screen out yet; its first body in
			// the final box sizes it once.
			continue
		}
		pins = append(pins, pin{id: id, vm: vm, body: body.Body,
			dw: source.Body.W - viewW, dh: source.Body.H - viewH})
	}
	// Pin after measuring every source: pinning the split target changes its
	// viewport, and the new Pane's chrome comes from that source.
	for _, p := range pins {
		p.vm.PinViewport(p.body.W-p.dw, p.body.H-p.dh)
		if wm.run != nil {
			wm.run.pins[p.id] = p.vm
		}
	}
}

// motionViewport reports the flow room of the surface that supplies a Pane's
// chrome. The Pane's own surface is preferred; a Pane with no previous layout
// takes the split target's surface.
func (wm *workspacePaneMotion) motionViewport(index int, id, fallback model.PaneID) (w, h float32, ok bool) {
	info, found := wm.state.PaneInfoAt(index, id)
	if found && info.Surface != nil {
		if vm, isVM := info.Surface.(state.ViewportMotion); isVM {
			if w, h, ok := vm.MotionViewport(); ok {
				return w, h, true
			}
		}
	}
	info, found = wm.state.PaneInfoAt(index, fallback)
	if !found || info.Surface == nil {
		return 0, 0, false
	}
	vm, isVM := info.Surface.(state.ViewportMotion)
	if !isVM {
		return 0, 0, false
	}
	return vm.MotionViewport()
}

// nativeMoved reports that MyGo's own transition is moving Pane id in the
// current run. Such an element's LayoutBounds is its shifted flight box, not
// its layout box, so it must be recorded from the run's computed final boxes.
func (wm *workspacePaneMotion) nativeMoved(id model.PaneID) bool {
	run := wm.run
	if run == nil {
		return false
	}
	switch run.kind {
	case motion.PaneOpen:
		return run.native && wm.mode.native && id == run.target
	case motion.PaneClose:
		_, survivor := run.final[id]
		return survivor && run.native && !run.magnified
	case motion.PaneReorder:
		return run.nativePanes[id]
	}
	return false
}

// recordPane stores the boxes a Pane showed this frame. LayoutBounds is the
// previous frame's box; a Tester frame settles, so the last pass records the
// frame's final boxes, and a real window records exactly the previous frame
// the next event needs as its start. A Pane MyGo moves natively is recorded
// at the run's final boxes instead: its live LayoutBounds is the shifted
// flight box, whose slot and column insets would corrupt the next event's
// final geometry.
func (wm *workspacePaneMotion) recordPane(id model.PaneID, slot, column, body ui.Rect, framed bool) {
	if wm.body != nil {
		// The body's Enter/Exit moves its whole child in the layout; the Pane
		// rectangles the next event starts from must be the untransformed
		// final-layout boxes, not the animated show positions.
		off := wm.body.recordOffset
		slot, column, body = off.untranslate(slot), off.untranslate(column), off.untranslate(body)
	}
	if wm.nativeMoved(id) {
		if end, ok := wm.run.final[id]; ok {
			wm.rects[id] = end
			return
		}
	}
	if run := wm.run; run != nil && run.zoom && run.render == zoomRenderSplit && id == run.zoomPane {
		// A zoom in keeps the split on screen while the Pane's overlay
		// grows. Its real Pane is still laid out at the split slot, but the
		// box the next event must start from is the full window the user is
		// being shown: the ghost/overlay is already there, and the PTY is
		// pinned to the final full grid.
		if end, ok := run.final[id]; ok {
			wm.rects[id] = end
			return
		}
	}
	rects := motion.PaneRects{Slot: slot, Column: column, Body: body, Framed: framed}
	if !rects.Valid() {
		return
	}
	wm.rects[id] = rects
}

// recordRegion stores the pane tree's region, the box every final split is
// computed in. It changes only with the window or the side panel, so the
// previous frame's box is the region the change happens in.
func (wm *workspacePaneMotion) recordRegion(region ui.Element) {
	r := region.LayoutBounds()
	if wm.body != nil {
		// Keep the region in the untransformed final layout while a Tab body
		// transition moves the whole child.
		r = wm.body.recordOffset.untranslate(r)
	}
	wm.region = r
}

// nativeCloseEnabled reports that this view animates a close's survivors
// natively. Production takes this route; overlayClose forces the opaque
// fallback a test can compare with.
func (wm *workspacePaneMotion) nativeCloseEnabled() bool {
	if wm.mode.overlayClose {
		return false
	}
	return motion.Enabled(wm.controller.Setting(), motion.PaneClose)
}

// nativeReorderEnabled reports that a same-Tab move's siblings use MyGo's
// Match + ScaleContent route. Production takes it; overlayReorder forces the
// opaque fallback a test compares with.
func (wm *workspacePaneMotion) nativeReorderEnabled() bool {
	if wm.mode.overlayReorder {
		return false
	}
	return motion.Enabled(wm.controller.Setting(), motion.PaneReorder)
}

// nativeKeys reports that this view installs the native Match route. The
// transition must be on every Pane on the frame before a change so MyGo has
// the previous box to record; an idle Pane's duration is -1 (arrive at once).
func (wm *workspacePaneMotion) nativeKeys() bool {
	return wm.mode.native || wm.nativeCloseEnabled() || wm.nativeReorderEnabled()
}

// paneTransitionInFlight reports that a Pane animation already playing is
// carried by MyGo's own ElementTransition. DisableAnimations is window-wide,
// so a body warp must not use it on such a frame even when the layout change
// that forces the warp records no event (a batch Tab close): the running Pane
// transition keeps playing (the one-switch rule). An overlay run does not need
// this protection; DisableAnimations does not touch it.
func (wm *workspacePaneMotion) paneTransitionInFlight() bool {
	run := wm.run
	if run == nil {
		return false
	}
	switch run.kind {
	case motion.PaneOpen:
		return wm.mode.native && run.native
	case motion.PaneClose:
		return run.native && !run.magnified
	case motion.PaneReorder:
		return len(run.nativePanes) > 0
	}
	return false
}

// stripWarpCanBeInstant reports that a Tab strip warp may call
// DisableAnimations on this frame: no Tab body transition is playing and no
// Pane transition MyGo carries is playing or starting. The switch is
// window-wide, so resting one of those would stop motion under a switch that
// is still on (the one-switch rule). An overlay-carried Pane run is not
// MyGo's, so it does not hold the strip back.
func (wm *workspacePaneMotion) stripWarpCanBeInstant() bool {
	if wm == nil {
		return false
	}
	return wm.body.phase == bodyIdle && !wm.paneTransitionInFlight()
}

// paneMotionUsesMyGo reports that the unseen event on this frame starts a Pane
// animation carried by MyGo's own ElementTransition rather than the opaque
// overlay. Only those transitions respond to DisableAnimations, so only they
// keep a Tab body warp from using it on their frame: the Pane's own switch is
// still on, and the one-switch rule must hold. A hidden or still window
// animates nothing.
func (wm *workspacePaneMotion) paneMotionUsesMyGo(c *ui.Context, setting domainpreferences.Motion,
	event motion.Event, firstFrame, hidden, wasHidden bool) bool {
	if firstFrame || hidden || wasHidden || c.Preferences().ReduceMotion {
		return false
	}
	switch event.Kind {
	case motion.PaneOpen:
		// The native open route is test-only; production overlays it.
		return wm.mode.native && motion.Enabled(setting, motion.PaneOpen)
	case motion.PaneClose:
		return !wm.mode.overlayClose && motion.Enabled(setting, motion.PaneClose)
	case motion.PaneReorder:
		return !wm.mode.overlayReorder && motion.Enabled(setting, motion.PaneReorder)
	}
	return false
}

// nativeTransition is the native no-reflow route. A close run's survivors get
// the run's duration and move from the previous frame's recorded box; every
// other Pane keeps Duration -1, which records the Match without animating. A
// magnified close reveals the split at its final layout under the ghost, so
// its survivors do not animate. The test-only native open route is also kept.
func (wm *workspacePaneMotion) nativeTransition(id model.PaneID) (ui.ElementTransition, bool) {
	if !wm.nativeKeys() {
		return ui.ElementTransition{}, false
	}
	duration := time.Duration(-1)
	var spring *ui.Animation
	if wm.run != nil {
		switch wm.run.kind {
		case motion.PaneOpen:
			if wm.mode.native && id == wm.run.target {
				duration = wm.run.anim.Duration
			}
		case motion.PaneClose:
			if _, survivor := wm.run.final[id]; survivor && wm.run.native && !wm.run.magnified {
				duration = wm.run.anim.Duration
			}
		case motion.PaneReorder:
			if wm.run.nativePanes[id] && wm.run.spring != nil {
				// The sibling makes room with the ticket's exact spring. A
				// stable element retargets its channel and keeps velocity;
				// planMove sends an interrupted sibling through the overlay
				// because the recreated split makes MyGo start a new Match
				// flight at rest.
				spring = &ui.Animation{Spring: *wm.run.spring}
			}
		}
	}
	transition := ui.ElementTransition{
		Ease:         ui.EaseOut,
		Position:     true,
		Size:         true,
		ScaleContent: true,
		Match:        motion.PaneMatch{ID: id},
	}
	if spring != nil {
		transition.Animation = spring
	} else {
		transition.Duration = duration
	}
	return transition, true
}

// paneKey is the element key of a Pane. A Pane that starts moving is keyed
// fresh at the change, so its Match starts it from the previous frame's box:
// the native test route does that for an open's old Pane, and the native close
// route for each survivor. A reorder sibling keeps its stable key: MyGo then
// retargets the channel it already has when the element survives the change,
// which is what carries velocity through an interruption. The move's recreated
// split usually makes the sibling a new element anyway; its Match then starts
// from the old box and planMove paints the interruption with the overlay.
func (wm *workspacePaneMotion) paneKey(id model.PaneID) (string, bool) {
	if !wm.nativeKeys() {
		return "", false
	}
	if wm.run != nil {
		fresh := false
		switch wm.run.kind {
		case motion.PaneOpen:
			fresh = wm.mode.native && id == wm.run.target
		case motion.PaneClose:
			_, fresh = wm.run.final[id]
			fresh = fresh && wm.run.native && !wm.run.magnified
		}
		if fresh {
			return "pane-" + string(id) + "-motion-" + strconv.FormatUint(wm.run.seq, 10), true
		}
	}
	return "pane-" + string(id) + "-native", true
}

// inertBody is implemented by a Surface that can draw a still copy of itself
// without taking the keyboard, requesting focus, resizing its terminal, or
// recording a focus epoch. A motion overlay uses it so the real Pane keeps
// the live Surface and every input path.
type inertBody interface {
	ViewInert(c *ui.Context) ui.Element
}

// drawOverlay paints the run's moving copies and its exit ghosts. Each copy is
// a whole Pane, frame and header included, laid out once at its box and mapped
// to the visual rect. An opaque mask covers each real survivor at its final
// column while its copy moves, so the real Pane stays built, focused, and
// receiving pointer and keyboard underneath; the masks and copies ignore
// input. A native close's survivors are moved by MyGo itself, so nothing masks
// or copies them; their visuals are only the bookkeeping an interruption
// starts from. The exiting Panes' ghosts are drawn last, opaque and above the
// survivors, from the surfaces the state kept alive.
func (wm *workspacePaneMotion) drawOverlay(c *ui.Context, ref tabRef, t *ui.Theme, m locales.Messages) {
	run := wm.run
	if run == nil || run.anim == nil || ref.motion != wm {
		return
	}
	now := c.Now()
	if _, done := run.anim.Progress(now); done {
		return
	}
	// A native visual (a close's survivor or a reorder's sibling) paints
	// itself; its overlay entry is only the bookkeeping an interruption
	// starts from. A zoom copy masks nothing: it covers its own real Pane
	// as it grows, and the split behind it must stay visible.
	copyable := func(v motion.Visual) bool {
		return v.Kind != motion.Exit && !v.Native && !(run.native && run.kind == motion.PaneClose)
	}
	ui.Overlay(c, func() {
		for _, v := range run.anim.Visuals {
			if !copyable(v) || v.NoMask {
				continue
			}
			if _, ok := ref.pane(v.Pane); !ok {
				continue
			}
			end, ok := run.final[v.Pane]
			if !ok {
				continue
			}
			mask := ui.Column(c).Absolute().
				Left(end.Column.X).Top(end.Column.Y).Size(end.Column.W, end.Column.H).
				ZIndex(paneMotionZIndex).Background(t.Background).IgnoreInput(true)
			if wm.mode.labels {
				mask.Label("pane-motion-mask:" + string(v.Pane))
			}
		}
		for _, v := range run.anim.Visuals {
			if !copyable(v) {
				continue
			}
			info, ok := ref.pane(v.Pane)
			if !ok {
				continue
			}
			end, ok := run.final[v.Pane]
			if !ok {
				continue
			}
			wm.paneMotionCopy(c, ref, m, t, run, v, info, end, now)
		}
		for _, v := range run.anim.Visuals {
			if v.Kind != motion.Exit {
				continue
			}
			ghost, ok := run.ghosts[v.Pane]
			if !ok {
				continue
			}
			rect, opacity := run.anim.StateAt(v, now)
			box := v.Box
			e := ui.Column(c).Absolute().
				Left(box.X).Top(box.Y).Size(box.W, box.H).
				ZIndex(paneMotionZIndex).Opacity(opacity).
				TransformOrigin(0, 0).Transform(paneOverlayTransform(box, rect)).
				Background(t.Background).Clip().IgnoreInput(true)
			if wm.mode.labels {
				e.Label("pane-close-ghost:" + string(v.Pane))
			}
			if ghost.blank {
				// No state kept this Pane's info: an opaque pane-shaped
				// background is the last resort, never a live placeholder.
				continue
			}
			e.Children(func() {
				wm.paneMotionPane(c, ref, m, t, ghost.info, ghost.rects, ghost.rects.Framed, v.Pane)
			})
		}
	})
}

// paneMotionCopy draws one moving survivor: laid out at box, mapped to the
// visual rect at now. The content is the final grid, so the copy never
// reflows.
func (wm *workspacePaneMotion) paneMotionCopy(c *ui.Context, ref tabRef, m locales.Messages,
	t *ui.Theme, run *paneMotionRun, v motion.Visual, info state.PaneInfo,
	end motion.PaneRects, now time.Time) {
	rect, opacity := run.anim.StateAt(v, now)
	box := v.Box
	e := ui.Column(c).Absolute().
		Left(box.X).Top(box.Y).Size(box.W, box.H).
		ZIndex(paneMotionZIndex).Opacity(opacity).
		TransformOrigin(0, 0).Transform(paneOverlayTransform(box, rect)).
		Background(t.Background).Clip().IgnoreInput(true)
	if wm.mode.labels {
		e.Label("pane-motion:" + string(v.Pane))
	}
	e.Children(func() { wm.paneMotionPane(c, ref, m, t, info, end, end.Framed, v.Pane) })
}

// paneOverlayTransform maps the content laid out in box onto target, around
// the box's top-left: the same map the prototype measured.
func paneOverlayTransform(box, target ui.Rect) ui.Transform {
	kx, ky := float32(1), float32(1)
	if box.W > 0 {
		kx = target.W / box.W
	}
	if box.H > 0 {
		ky = target.H / box.H
	}
	return ui.Scaling(kx, ky).Then(ui.Translation(target.X-box.X, target.Y-box.Y))
}

// paneMotionPane draws one whole Pane for its overlay or ghost: PaneFrame when
// the Pane showed inside a split, the header row, and the body. rects gives
// the column and body the copy is laid out from, so the header height is the
// same one the real Pane had. The header is inert because the whole copy
// ignores input; the body uses ViewInert when the Surface offers it, so the
// copy never takes focus, asks for it, or sizes the terminal.
func (wm *workspacePaneMotion) paneMotionPane(c *ui.Context, ref tabRef, m locales.Messages, t *ui.Theme,
	info state.PaneInfo, rects motion.PaneRects, framed bool, id model.PaneID) {
	frameTop := float32(0)
	if framed {
		frameTop, _, _, _ = kit.PaneFrameInset()
	}
	column := ui.Column(c).Fill().MinWidth(0).Background(t.Background).Clip()
	if framed {
		column = kit.PaneFrame(c, column, info.Focused)
	}
	if wm.mode.labels {
		column.Label("pane-motion-frame:" + string(id))
	}
	header := rects.Body.Y - rects.Column.Y - frameTop
	if header < 0 {
		header = 0
	}
	column.Children(func() {
		if header > 0 {
			row := ui.Column(c).FillWidth().Height(header).Shrink(0)
			if wm.mode.labels {
				row.Label("pane-motion-header:" + string(id))
			}
			row.Children(func() { paneHeader(c, wm.state, m, t, ref, info) })
		}
		body := ui.Column(c).Grow(1).FillWidth().MinHeight(0)
		body.Children(func() { paneMotionBody(c, m, t, info) })
	})
}

// paneMotionBody draws a Pane's content for its overlay. A live Surface is
// drawn through ViewInert, its still copy; a Surface without that path draws
// only its background, because its live View would take focus and resize the
// terminal, which the real Pane behind the mask already owns. A pending Pane
// shows its placeholder and swaps to its Session when the outcome arrives,
// without restarting the run; a failed one keeps the error body.
func paneMotionBody(c *ui.Context, m locales.Messages, t *ui.Theme, info state.PaneInfo) {
	switch {
	case info.Err != nil:
		message := m.PaneOpenFailed()
		if errors.Is(info.Err, state.ErrHostUnavailable) {
			message = m.PaneHostMissing()
		} else if problem := uiconnection.ProblemTextFor(m, info.Err); problem != "" {
			message = problem
		}
		ui.Column(c).Fill().Padding(24).Justify(ui.Center).AlignItems(ui.Center).Children(func() {
			ui.Text(c, message).FontSize(kit.M(c).Font.Callout).MaxWidth(420).
				TextColor(uitheme.DangerText(t))
		})
	case info.Surface != nil:
		if inert, ok := info.Surface.(inertBody); ok {
			inert.ViewInert(c)
			return
		}
		ui.Column(c).Fill().Background(t.Background)
	default:
		ui.Column(c).Fill().Gap(10).Justify(ui.Center).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, icons.Lucide(icons.Loader)).Size(22, 22).TextColor(t.TextMuted)
			ui.Text(c, m.PaneConnecting()).TextColor(t.TextMuted).FontSize(kit.M(c).Font.Callout)
		})
	}
}

// paneFinalRects returns each leaf Pane's final boxes for the Tab tree in
// region. Slots follow MyGo's SplitPercent: a one-DIP divider, the rest shared
// by the ratio, with each side keeping the split's minimum pane. A Pane's
// chrome insets come from its own last boxes; a Pane with no previous frame
// uses the split target's, which is the Pane it replaces. A Pane that gains a
// split's PaneFrame adds that border, which its previous single-Pane boxes did
// not include.
func paneFinalRects(root *domainworkspace.Node, region ui.Rect,
	rects map[model.PaneID]motion.PaneRects, minPane float32,
	fallback motion.PaneRects, split bool,
	frameTop, frameRight, frameBottom, frameLeft float32) map[model.PaneID]motion.PaneRects {
	final := map[model.PaneID]motion.PaneRects{}
	if root == nil || region.W <= 0 || region.H <= 0 {
		return final
	}
	slots := map[model.PaneID]ui.Rect{}
	paneSlots(root, region, minPane, slots)
	for id, slot := range slots {
		r, ok := rects[id]
		if !ok || !r.Valid() {
			// A Pane with no previous frame replaces the split target and
			// inherits its chrome insets.
			r = fallback
		}
		if !r.Valid() {
			continue
		}
		top := r.Column.Y - r.Slot.Y
		left := r.Column.X - r.Slot.X
		right := r.Slot.X + r.Slot.W - (r.Column.X + r.Column.W)
		bottom := r.Slot.Y + r.Slot.H - (r.Column.Y + r.Column.H)
		header := r.Body.Y - r.Column.Y
		footer := r.Column.Y + r.Column.H - (r.Body.Y + r.Body.H)
		frameLeftInset := r.Body.X - r.Column.X
		frameRightInset := r.Column.X + r.Column.W - (r.Body.X + r.Body.W)
		switch {
		case split && !r.Framed:
			// The Pane gains a split's PaneFrame: its border joins the inset.
			header += frameTop
			footer += frameBottom
			frameLeftInset += frameLeft
			frameRightInset += frameRight
		case !split && r.Framed:
			// The split collapses around this Pane: the PaneFrame goes, so
			// its border leaves the inset and the body grows by it.
			header -= frameTop
			footer -= frameBottom
			frameLeftInset -= frameLeft
			frameRightInset -= frameRight
			header = max(header, 0)
			footer = max(footer, 0)
			frameLeftInset = max(frameLeftInset, 0)
			frameRightInset = max(frameRightInset, 0)
		}
		column := ui.Rect{
			X: slot.X + left,
			Y: slot.Y + top,
			W: slot.W - left - right,
			H: slot.H - top - bottom,
		}
		w := column.W - frameLeftInset - frameRightInset
		h := column.H - header - footer
		if column.W <= 0 || column.H <= 0 || w <= 0 || h <= 0 {
			continue
		}
		final[id] = motion.PaneRects{
			Slot:   slot,
			Column: column,
			Body: ui.Rect{
				X: column.X + frameLeftInset,
				Y: column.Y + header,
				W: w,
				H: h,
			},
			Framed: split,
		}
	}
	return final
}

// paneSlots fills out with the slot each leaf gets from the tree in rect.
func paneSlots(node *domainworkspace.Node, rect ui.Rect, minPane float32, out map[model.PaneID]ui.Rect) {
	if node == nil {
		return
	}
	if node.IsPane() {
		out[node.PaneID] = rect
		return
	}
	ratio := node.Ratio
	if ratio <= 0 || ratio >= 100 {
		ratio = domainworkspace.DefaultRatio
	}
	vertical := node.Direction == domainworkspace.Horizontal
	length := rect.W
	if vertical {
		length = rect.H
	}
	usable := max(length-1, 0)
	first := usable * ratio / 100
	second := usable - first
	if usable >= 2*minPane {
		if first < minPane {
			first = minPane
			second = usable - first
		}
		if second < minPane {
			second = minPane
			first = usable - second
		}
	} else if minPane > 0 {
		// With too little room for both minimums MyGo freezes each side at
		// the minimum and lets them overflow; the first does not keep a
		// proportional size it cannot show.
		first, second = minPane, minPane
	}
	firstRect, secondRect := rect, rect
	if vertical {
		firstRect.H, secondRect.H = first, second
		secondRect.Y = rect.Y + first + 1
	} else {
		firstRect.W, secondRect.W = first, second
		secondRect.X = rect.X + first + 1
	}
	paneSlots(node.First, firstRect, minPane, out)
	paneSlots(node.Second, secondRect, minPane, out)
}
