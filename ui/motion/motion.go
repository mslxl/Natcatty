// Package motion is the view-side runtime for Pane and Tab motion. It is
// shared by the shell and Quake views and imports no view package: a state
// records an Event when a layout-changing action happens, and each visible
// view consumes only the latest event it has not seen and animates the
// frame it already painted (ADR 0026). It knows nothing about terminals or
// surfaces; the view builds the overlays from the geometry here.
package motion

import (
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/domain/model"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
)

// Kind is one of the seven events that can animate.
type Kind int

const (
	// PaneOpen is splitting a Pane and the new position of a same-Tab move.
	PaneOpen Kind = iota
	// PaneClose is closing a Pane and the old position of a same-Tab move.
	PaneClose
	// PaneReorder is a same-Tab move's sibling, zoom, and magnification.
	PaneReorder
	// TabOpen is creating a Tab.
	TabOpen
	// TabClose is closing a Tab, including its last Pane.
	TabClose
	// TabSwitch is selecting a Tab, next/previous, the Quick Switcher, or
	// Follow.
	TabSwitch
	// TabReorder is dragging or moving a Tab in the strip.
	TabReorder
)

// String names the kind, for tests and diagnostics.
func (k Kind) String() string {
	switch k {
	case PaneOpen:
		return "pane-open"
	case PaneClose:
		return "pane-close"
	case PaneReorder:
		return "pane-reorder"
	case TabOpen:
		return "tab-open"
	case TabClose:
		return "tab-close"
	case TabSwitch:
		return "tab-switch"
	case TabReorder:
		return "tab-reorder"
	default:
		return "unknown"
	}
}

// Event is one layout change a view may animate. Seq is monotonic across the
// state, so a view consumes only the latest event it has not seen; multiple
// events between two frames collapse to the latest. TargetID is a Pane ID;
// TargetTabID is the target of a Tab close, switch, or reorder.
type Event struct {
	Kind        Kind
	TabID       model.TabID
	TargetTabID model.TabID
	PaneID      model.PaneID
	TargetID    model.PaneID
	// Direction is the side or order a Tab/Pane moves towards: +1 for a
	// target after the source, -1 before it. For PaneOpen it is +1 when the
	// new Pane is the split's second child and -1 when it is the first.
	Direction int
	Seq       uint64
	At        time.Time
}

// BaseDuration is the standard (1x speed) duration of kind.
func BaseDuration(kind Kind) time.Duration {
	switch kind {
	case PaneOpen, TabOpen:
		return 200 * time.Millisecond
	case PaneClose, TabClose:
		return 150 * time.Millisecond
	case PaneReorder, TabSwitch:
		return 240 * time.Millisecond
	case TabReorder:
		return 180 * time.Millisecond
	default:
		return 0
	}
}

// ScaleMotion multiplies a base duration by the speed tier: slow 1.5x,
// standard 1x, fast 0.6x. The tier scales time only, never a distance,
// opacity, or the fixed popin percentage.
func ScaleMotion(base time.Duration, speed domainpreferences.MotionSpeed) time.Duration {
	switch speed {
	case domainpreferences.MotionSlow:
		return base * 3 / 2
	case domainpreferences.MotionFast:
		return base * 3 / 5
	default:
		return base
	}
}

// Enabled reports whether setting animates kind: the master switch and the
// event's own switch must both be on. Reduce Motion is checked by the view,
// which has the frame's ui.Context.
func Enabled(setting domainpreferences.Motion, kind Kind) bool {
	if !setting.Enabled {
		return false
	}
	switch kind {
	case PaneOpen:
		return setting.PaneOpen
	case PaneClose:
		return setting.PaneClose
	case PaneReorder:
		return setting.PaneReorder
	case TabOpen:
		return setting.TabOpen
	case TabClose:
		return setting.TabClose
	case TabSwitch:
		return setting.TabSwitch
	case TabReorder:
		return setting.TabReorder
	default:
		return false
	}
}

// PaneRects are the three boxes a Pane has in a frame: the slot the split
// tree gives it, the Pane column inside that slot (the slot less the drop
// gutters), and the body inside the column (the column less the header). The
// view records all three from the previous frame's layout; a Pane open
// derives the final boxes from the split target's slot.
type PaneRects struct {
	Slot   ui.Rect
	Column ui.Rect
	Body   ui.Rect
	// Framed reports that the Pane was drawn inside a split's PaneFrame, so
	// its body insets already include the frame's border. A Pane that becomes
	// split gains the border, and the next event's final box must add it.
	Framed bool
}

// Valid reports that the boxes were measured.
func (r PaneRects) Valid() bool {
	return r.Slot.W > 0 && r.Slot.H > 0 && r.Column.W > 0 && r.Column.H > 0 && r.Body.W > 0 && r.Body.H > 0
}

// PaneMatch is the identity an old Pane's native ElementTransition matches
// across the layout change. The overlay route does not need it.
type PaneMatch struct{ ID model.PaneID }

// Controller is one view's motion bookkeeping: the live setting, the last
// event sequence it consumed, the running animation, and the previous
// frame's Pane and Tab-body boxes.
type Controller struct {
	motion  func() domainpreferences.Motion
	lastSeq uint64
	anim    *Animation
	rects   map[model.PaneID]PaneRects
	bodies  map[model.TabID]ui.Rect
}

// NewController returns a controller reading the live motion setting.
func NewController(motion func() domainpreferences.Motion) *Controller {
	if motion == nil {
		motion = func() domainpreferences.Motion {
			return domainpreferences.Defaults().Appearance.Motion
		}
	}
	return &Controller{motion: motion}
}

// Setting is the live motion preference, pulled when a view consumes an
// event so a Settings change needs no rebuild.
func (c *Controller) Setting() domainpreferences.Motion { return c.motion() }

// LastSeq is the sequence of the last event the view consumed.
func (c *Controller) LastSeq() uint64 { return c.lastSeq }

// Unseen reports whether seq is newer than the last event the view consumed.
func (c *Controller) Unseen(seq uint64) bool { return seq > c.lastSeq }

// MarkSeen consumes seq without animating it.
func (c *Controller) MarkSeen(seq uint64) {
	if seq > c.lastSeq {
		c.lastSeq = seq
	}
}

// Animation is the run the view is playing, or nil.
func (c *Controller) Animation() *Animation { return c.anim }

// Begin starts anim, replacing any run in progress. The caller has already
// turned the previous run's current visuals into anim's start boxes, which
// is what makes the latest event win from what the user sees.
func (c *Controller) Begin(anim *Animation) { c.anim = anim }

// Clear stops the run and cleans the boxes recorded for Panes that no longer
// exist. It reports the animation that was running, if any.
func (c *Controller) Clear() *Animation {
	anim := c.anim
	c.anim = nil
	return anim
}

// RecordPane stores a Pane's boxes for the next event to use as its start.
// An unmeasured box keeps the previous value: a Pane that just appeared has
// no previous frame of its own, and its previous box is the one the old
// layout showed.
func (c *Controller) RecordPane(id model.PaneID, rects PaneRects) {
	if id == "" || !rects.Valid() {
		return
	}
	if c.rects == nil {
		c.rects = map[model.PaneID]PaneRects{}
	}
	c.rects[id] = rects
}

// Pane returns the boxes a Pane last showed.
func (c *Controller) Pane(id model.PaneID) (PaneRects, bool) {
	rects, ok := c.rects[id]
	return rects, ok
}

// ForgetPane drops a Pane's recorded boxes after its animation ended.
func (c *Controller) ForgetPane(id model.PaneID) { delete(c.rects, id) }

// ResetRects drops every recorded Pane box, as a Tab switch or a rebuild
// does when the old geometry no longer names this view's Panes.
func (c *Controller) ResetRects() {
	clear(c.rects)
}

// RecordBody stores a Tab body's box for the next event to use.
func (c *Controller) RecordBody(tab model.TabID, rect ui.Rect) {
	if tab == "" || rect.W <= 0 || rect.H <= 0 {
		return
	}
	if c.bodies == nil {
		c.bodies = map[model.TabID]ui.Rect{}
	}
	c.bodies[tab] = rect
}

// Body returns a Tab body's last box.
func (c *Controller) Body(tab model.TabID) (ui.Rect, bool) {
	rect, ok := c.bodies[tab]
	return rect, ok
}

// Edge is the side a slide style enters from.
type Edge int

const (
	// EdgeAuto picks the nearest content edge.
	EdgeAuto Edge = iota
	EdgeLeft
	EdgeRight
	EdgeTop
	EdgeBottom
)

// VisualKind is how a Pane's visual rect moves.
type VisualKind int

const (
	// Move lerps the rect from its start to the final body box.
	Move VisualKind = iota
	// Enter uses an open style to bring the final body box in.
	Enter
)

// Visual is one Pane being animated: the content is laid out once at Box
// (the Pane's final whole-Pane column, frame and header included) and painted
// scaled/moved to the rect State returns. The terminal grid never changes
// during the run.
type Visual struct {
	Pane model.PaneID
	// Box is where the content is laid out: the Pane's final column. It is
	// also the rect's end.
	Box ui.Rect
	// From is the visual rect at the start of a Move.
	From ui.Rect
	// To is the final visual rect. It equals Box for every current route,
	// but keeping it explicit lets an interrupted run restart from a moved
	// start.
	To ui.Rect
	// Content is the content area a slide measures its nearest edge
	// against, normally the split target's old Pane column.
	Content ui.Rect
	Kind    VisualKind
	Style   domainpreferences.MotionStyle
	Edge    Edge
	// FromOpacity and ToOpacity are the fade at each end.
	FromOpacity float32
	ToOpacity   float32
}

// Animation is one run: a start, a duration, and the Panes it moves. A
// view creates a new Animation for each event, using the previous run's
// current rects as the new start so an interruption continues from what the
// user sees.
type Animation struct {
	Started  time.Time
	Duration time.Duration
	Visuals  []Visual
}

// Progress returns the eased progress at now, from 0 to 1, and whether the
// run is done.
func (a *Animation) Progress(now time.Time) (float32, bool) {
	if a == nil {
		return 1, true
	}
	if a.Duration <= 0 {
		return 1, true
	}
	elapsed := now.Sub(a.Started)
	if elapsed >= a.Duration {
		return 1, true
	}
	t := float32(elapsed) / float32(a.Duration)
	if t < 0 {
		t = 0
	}
	return ui.EaseOut(t), false
}

// VisualFor returns the running visual for a Pane.
func (a *Animation) VisualFor(pane model.PaneID) (Visual, bool) {
	if a == nil {
		return Visual{}, false
	}
	for _, v := range a.Visuals {
		if v.Pane == pane {
			return v, true
		}
	}
	return Visual{}, false
}

// State returns the visual rect and opacity of v at eased progress k.
func (a *Animation) State(v Visual, k float32) (ui.Rect, float32) {
	var rect ui.Rect
	switch v.Kind {
	case Enter:
		rect = EnterRect(v.To, v.Style, v.Edge, v.Content, k)
	default:
		rect = LerpRect(v.From, v.To, k)
	}
	opacity := Lerp(v.FromOpacity, v.ToOpacity, k)
	if v.Kind == Enter {
		// A style's own opacity curve is the open contract: popin and fade
		// go 0 to 1, slide and gnomed stay opaque.
		opacity = EnterOpacity(v.Style, k)
	}
	return rect, opacity
}

// Lerp moves a to b by k.
func Lerp(a, b, k float32) float32 { return a + (b-a)*k }

// LerpRect moves a to b by k on every edge.
func LerpRect(a, b ui.Rect, k float32) ui.Rect {
	return ui.Rect{
		X: Lerp(a.X, b.X, k),
		Y: Lerp(a.Y, b.Y, k),
		W: Lerp(a.W, b.W, k),
		H: Lerp(a.H, b.H, k),
	}
}

// PopinScaleAt is the popin scale at k: 0.87 at the start, 1 at rest. The
// 87% is fixed by the design; it is not a setting.
const PopinStartScale float32 = 0.87

// EnterRect is the visual rect a Pane with an open style has at eased
// progress k. final is the Pane's final whole-Pane box; content is the area
// whose nearest edge a slide enters from. popin scales around the final
// center, slide moves in from edge by 100% of the Pane's size, gnomed grows
// upward with its bottom edge fixed, and fade keeps the geometry at rest.
func EnterRect(final ui.Rect, style domainpreferences.MotionStyle, edge Edge, content ui.Rect, k float32) ui.Rect {
	switch style {
	case domainpreferences.MotionPopin:
		scale := PopinStartScale + (1-PopinStartScale)*k
		cx, cy := final.X+final.W/2, final.Y+final.H/2
		w, h := final.W*scale, final.H*scale
		return ui.Rect{X: cx - w/2, Y: cy - h/2, W: w, H: h}
	case domainpreferences.MotionSlide:
		edge = ResolveEdge(edge, final, content)
		switch edge {
		case EdgeLeft:
			return ui.Rect{X: final.X - final.W*(1-k), Y: final.Y, W: final.W, H: final.H}
		case EdgeRight:
			return ui.Rect{X: final.X + final.W*(1-k), Y: final.Y, W: final.W, H: final.H}
		case EdgeTop:
			return ui.Rect{X: final.X, Y: final.Y - final.H*(1-k), W: final.W, H: final.H}
		case EdgeBottom:
			return ui.Rect{X: final.X, Y: final.Y + final.H*(1-k), W: final.W, H: final.H}
		default:
			return final
		}
	case domainpreferences.MotionGnomed:
		return ui.Rect{X: final.X, Y: final.Y + final.H*(1-k), W: final.W, H: final.H * k}
	default:
		return final
	}
}

// EnterOpacity is the opacity a Pane with an open style has at k. MyGo's
// Motion opacity defaults to 0, so slide and gnomed must say 1 explicitly.
func EnterOpacity(style domainpreferences.MotionStyle, k float32) float32 {
	switch style {
	case domainpreferences.MotionPopin, domainpreferences.MotionFade:
		return k
	default:
		return 1
	}
}

// ResolveEdge returns edge, or the nearest edge of final within content for
// EdgeAuto. A Pane that touches several content edges (a full-width top Pane
// touches left, top, and right) enters from the side with the most room on
// the opposite side: the split's outer edge, not a seam along the divider.
// Remaining ties prefer left, right, top, then bottom.
func ResolveEdge(edge Edge, final, content ui.Rect) Edge {
	if edge != EdgeAuto {
		return edge
	}
	if content.W <= 0 || content.H <= 0 {
		return EdgeAuto
	}
	left := final.X - content.X
	right := content.X + content.W - (final.X + final.W)
	top := final.Y - content.Y
	bottom := content.Y + content.H - (final.Y + final.H)
	best := min(left, right, top, bottom)
	const eps = 0.5
	type candidate struct {
		edge     Edge
		opposite float32
	}
	var candidates []candidate
	if left <= best+eps {
		candidates = append(candidates, candidate{EdgeLeft, right})
	}
	if right <= best+eps {
		candidates = append(candidates, candidate{EdgeRight, left})
	}
	if top <= best+eps {
		candidates = append(candidates, candidate{EdgeTop, bottom})
	}
	if bottom <= best+eps {
		candidates = append(candidates, candidate{EdgeBottom, top})
	}
	chosen := candidates[0]
	for _, c := range candidates[1:] {
		if c.opposite > chosen.opposite {
			chosen = c
		}
	}
	return chosen.edge
}
