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
// route so the fallback stays exercised; labels names the overlays for tests.
type paneMotionMode struct {
	native       bool
	overlayClose bool
	labels       bool
}

// paneMotionRun is one Pane animation this view is playing: a Pane open, or a
// Pane close with its survivor(s) and its exit ghost.
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
	// final is every leaf Pane's final boxes. The overlay lays each whole
	// Pane out at final[id].Column and an opaque mask covers the real Pane
	// there while the copy moves; pinPanes froze the same body boxes.
	final map[model.PaneID]motion.PaneRects
	// ghosts are the exiting Panes' retained surfaces and the boxes they
	// showed. The view draws each as an opaque ghost and releases its surface
	// when the run ends, is replaced, or warps.
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
}

func newWorkspacePaneMotion(s *state.State, mode paneMotionMode) *workspacePaneMotion {
	return &workspacePaneMotion{
		state:      s,
		controller: motion.NewController(s.Motion),
		mode:       mode,
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

	activeIndex := s.ActiveTab()
	activeID, _ := s.TabID(activeIndex)
	if wm.run != nil && wm.run.tabID != activeID {
		// An animation belongs to its Tab; leaving the Tab warps it.
		wm.finish()
	}
	setting := s.Motion()
	if wm.run != nil && (!motion.Enabled(setting, wm.run.kind) || c.Preferences().ReduceMotion) {
		// Switching motion off mid-run warps at once and stops asking for
		// frames (ticket 08).
		wm.finish()
	}
	if wm.run != nil && hidden {
		// Nothing of the window shows while hidden; a run that outlives the
		// hide is over before it is seen again.
		wm.finish()
	}

	event, ok := s.MotionEvent()
	if !ok || !wm.controller.Unseen(event.Seq) {
		if wm.run != nil {
			if _, done := wm.run.anim.Progress(now); done {
				wm.finish()
			} else {
				wm.animationFrames++
				c.AnimationFrame()
			}
		}
		return
	}
	wm.controller.MarkSeen(event.Seq)
	if event.TabID != activeID || activeIndex < 0 {
		wm.finish()
		// A ghost retained for a Tab this view no longer shows has no run to
		// draw it; release its surface now.
		s.ReleasePaneGhosts()
		return
	}

	animate := motion.Enabled(setting, event.Kind) &&
		!c.Preferences().ReduceMotion &&
		!firstFrame && !hidden && !wasHidden
	// Only a visible view has the previous frame's boxes on screen; a hidden
	// or first frame must not pin a grid from geometry it never showed. The
	// final layout sizes each Pane once instead.
	pin := !hidden && !wasHidden && !firstFrame
	switch event.Kind {
	case motion.PaneOpen:
		wm.plan(c, s, activeIndex, event, setting, animate, pin, now)
	case motion.PaneClose:
		wm.planClose(c, s, activeIndex, event, setting, animate, pin, now)
	default:
		wm.finish()
		s.ReleasePaneGhosts()
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

// currentVisual reports what of Pane id the user saw at the change.
func (wm *workspacePaneMotion) currentVisual(prev *paneMotionRun, prevK float32,
	id model.PaneID) (paneVisualState, bool) {
	if prev != nil {
		if v, ok := prev.anim.VisualFor(id); ok {
			r, opacity := prev.anim.State(v, prevK)
			return paneVisualState{rect: r, opacity: opacity}, true
		}
	}
	if r, ok := wm.rects[id]; ok && r.Valid() {
		return paneVisualState{rect: r.Column, opacity: 1}, true
	}
	return paneVisualState{}, false
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
	var prevK float32
	if prev != nil {
		prevK, _ = prev.anim.Progress(now)
	}

	old, oldOK := wm.currentVisual(prev, prevK, event.TargetID)
	if !oldOK {
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
		native: wm.mode.native,
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
			from, opacity := prev.anim.State(v, prevK)
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
	var prevK float32
	if prev != nil {
		prevK, _ = prev.anim.Progress(now)
	}
	// Carry the ghosts of an earlier close that this event does not replace:
	// their Panes are already gone, so the new run keeps their surfaces alive
	// and continues them from where the user sees them.
	carried := map[model.PaneID]paneMotionGhost{}
	if prev != nil {
		for id, ghost := range prev.ghosts {
			if id != event.PaneID {
				carried[id] = ghost
			}
		}
		prev.ghosts = nil
	}
	old, oldOK := wm.currentVisual(prev, prevK, event.PaneID)
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
	if event.Magnified && prev == nil {
		// The magnified Pane filled the pane region. Its ghost starts there and
		// reveals the split at its final layout as it leaves.
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
	ghost := paneMotionGhost{rects: closeRects, handle: ghostHandle}
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
	// latest event's full duration.
	for id, ghost := range carried {
		if prev == nil {
			continue
		}
		v, ok := prev.anim.VisualFor(id)
		if !ok {
			continue
		}
		from, opacity := prev.anim.State(v, prevK)
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
			seen, ok := wm.currentVisual(prev, prevK, id)
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
		delete(wm.rects, id)
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

// recordPane stores the boxes a Pane showed this frame. LayoutBounds is the
// previous frame's box; a Tester frame settles, so the last pass records the
// frame's final boxes, and a real window records exactly the previous frame
// the next event needs as its start.
func (wm *workspacePaneMotion) recordPane(id model.PaneID, slot, column, body ui.Rect, framed bool) {
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
	wm.region = region.LayoutBounds()
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

// nativeKeys reports that this view installs the native Match route. The
// transition must be on every Pane on the frame before a change so MyGo has
// the previous box to record; an idle Pane's duration is -1 (arrive at once).
func (wm *workspacePaneMotion) nativeKeys() bool {
	return wm.mode.native || wm.nativeCloseEnabled()
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
		}
	}
	return ui.ElementTransition{
		Duration:     duration,
		Ease:         ui.EaseOut,
		Position:     true,
		Size:         true,
		ScaleContent: true,
		Match:        motion.PaneMatch{ID: id},
	}, true
}

// paneKey is the element key of a Pane. A Pane that starts moving is keyed
// fresh at the change, so its Match starts it from the previous frame's box:
// the native test route does that for an open's old Pane, and the native close
// route for each survivor. Every other Pane keeps a stable key.
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
	k, done := run.anim.Progress(c.Now())
	if done {
		return
	}
	ui.Overlay(c, func() {
		for _, v := range run.anim.Visuals {
			if v.Kind == motion.Exit || (run.native && run.kind == motion.PaneClose) {
				// A native close's survivors paint themselves; their visuals
				// are only the bookkeeping an interruption starts from.
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
			if v.Kind == motion.Exit || (run.native && run.kind == motion.PaneClose) {
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
			wm.paneMotionCopy(c, ref, m, t, run, v, info, end, k)
		}
		for _, v := range run.anim.Visuals {
			if v.Kind != motion.Exit {
				continue
			}
			ghost, ok := run.ghosts[v.Pane]
			if !ok {
				continue
			}
			rect, opacity := run.anim.State(v, k)
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
// visual rect at k. The content is the final grid, so the copy never reflows.
func (wm *workspacePaneMotion) paneMotionCopy(c *ui.Context, ref tabRef, m locales.Messages,
	t *ui.Theme, run *paneMotionRun, v motion.Visual, info state.PaneInfo,
	end motion.PaneRects, k float32) {
	rect, opacity := run.anim.State(v, k)
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
