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

// paneMotionMode selects the test-only routes. Production uses the overlay
// route: MyGo's native Match cannot cross the newly created split parent the
// prototype measured (ticket 04). Native keeps a Match transition on every
// Pane so the old Pane's native route can be exercised against the overlay
// fallback; labels names the overlays for tests.
type paneMotionMode struct {
	native bool
	labels bool
}

// paneMotionRun is one Pane-open animation this view is playing.
type paneMotionRun struct {
	tabID  model.TabID
	seq    uint64
	target model.PaneID
	anim   *motion.Animation
	// native marks a run whose old Pane is moved by MyGo's own
	// ElementTransition instead of an overlay.
	native bool
	// final is every leaf Pane's final boxes. The overlay lays each whole
	// Pane out at final[id].Column and an opaque mask covers the real Pane
	// there while the copy moves; pinPanes froze the same body boxes.
	final map[model.PaneID]motion.PaneRects
	// pins are the Surfaces whose final grid this run set at the change;
	// they are released if their own layout never confirms the pin.
	pins map[model.PaneID]state.ViewportMotion
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
	if wm.run != nil && (!motion.Enabled(setting, motion.PaneOpen) || c.Preferences().ReduceMotion) {
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
	if event.Kind != motion.PaneOpen || event.TabID != activeID || activeIndex < 0 {
		wm.finish()
		return
	}

	animate := motion.Enabled(setting, motion.PaneOpen) &&
		!c.Preferences().ReduceMotion &&
		!firstFrame && !hidden && !wasHidden
	// Only a visible view has the previous frame's boxes on screen; a hidden
	// or first frame must not pin a grid from geometry it never showed. The
	// final layout sizes each Pane once instead.
	pin := !hidden && !wasHidden && !firstFrame
	wm.plan(c, s, activeIndex, event, setting, animate, pin, now)
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
	// visible is what the user sees of a Pane at the change: the rect and
	// opacity the previous run's overlay painted, or the settled boxes of a
	// Pane that was not moving. An interruption restarts both, so a Pane that
	// was half faded in does not snap to opaque.
	type visible struct {
		rect    ui.Rect
		opacity float32
	}
	current := func(id model.PaneID) (visible, bool) {
		if prev != nil {
			if v, ok := prev.anim.VisualFor(id); ok {
				r, opacity := prev.anim.State(v, prevK)
				return visible{rect: r, opacity: opacity}, true
			}
		}
		if r, ok := wm.rects[id]; ok && r.Valid() {
			return visible{rect: r.Column, opacity: 1}, true
		}
		return visible{}, false
	}

	old, oldOK := current(event.TargetID)
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

// finish ends the run and lets every pinned Pane follow its own layout again.
// Pins that their layout already confirmed were spent by the Surface itself.
func (wm *workspacePaneMotion) finish() {
	run := wm.run
	if run == nil {
		return
	}
	for _, pin := range run.pins {
		pin.ReleaseViewport()
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

// nativeTransition is the old Pane's native no-reflow route, used only by the
// test mode. It stays on every Pane so the previous frame records the Match
// the change can start from.
func (wm *workspacePaneMotion) nativeTransition(id model.PaneID) (ui.ElementTransition, bool) {
	if !wm.mode.native {
		return ui.ElementTransition{}, false
	}
	duration := motion.ScaleMotion(motion.BaseDuration(motion.PaneOpen), wm.state.Motion().Speed)
	if wm.run != nil && id == wm.run.target {
		duration = wm.run.anim.Duration
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

// paneKey is the element key of a Pane. Only the native test route changes
// it, so the element appearing at the change is fresh and its Match can start
// it from the previous frame's box.
func (wm *workspacePaneMotion) paneKey(id model.PaneID) (string, bool) {
	if !wm.mode.native {
		return "", false
	}
	if wm.run != nil && id == wm.run.target {
		return "pane-" + string(id) + "-motion-" + strconv.FormatUint(wm.run.seq, 10), true
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

// drawOverlay paints the run's moving copies. Each is a whole Pane, frame and
// header included, laid out once at its final column and mapped to the visual
// rect. An opaque mask covers each real Pane at its final column while its
// copy moves, so the real Pane stays built, focused, and receiving pointer and
// keyboard underneath; the masks and copies ignore input. A native old Pane is
// moved by MyGo itself and has no visual, so nothing masks it.
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
			info, ok := ref.pane(v.Pane)
			if !ok {
				continue
			}
			end, ok := run.final[v.Pane]
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
				e.Label("pane-motion:" + string(v.Pane))
			}
			e.Children(func() { wm.paneMotionPane(c, ref, m, t, info, end, v.Pane) })
		}
	})
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

// paneMotionPane draws one whole Pane for its overlay: PaneFrame when the Tab
// is split, the real header row, and the body. final gives the final column
// and body, so the header height is the same one the real Pane has. The header
// is inert because the whole copy ignores input; the body uses ViewInert when
// the Surface offers it, so the copy never takes focus, asks for it, or sizes
// the terminal.
func (wm *workspacePaneMotion) paneMotionPane(c *ui.Context, ref tabRef, m locales.Messages, t *ui.Theme,
	info state.PaneInfo, final motion.PaneRects, id model.PaneID) {
	frameTop := float32(0)
	if ref.split {
		frameTop, _, _, _ = kit.PaneFrameInset()
	}
	column := ui.Column(c).Fill().MinWidth(0).Background(t.Background).Clip()
	if ref.split {
		column = kit.PaneFrame(c, column, info.Focused)
	}
	if wm.mode.labels {
		column.Label("pane-motion-frame:" + string(id))
	}
	header := final.Body.Y - final.Column.Y - frameTop
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
		if split && !r.Framed {
			header += frameTop
			footer += frameBottom
			frameLeftInset += frameLeft
			frameRightInset += frameRight
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
