package shell

import (
	"strconv"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/domain/model"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
	"github.com/mslxl/natcatty/ui/layout"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/motion"
	"github.com/mslxl/natcatty/ui/shell/state"
)

// The work body is the surface below the Tab bar. A Tab event animates it as
// one whole body: the outgoing body leaves as a still copy and the incoming
// body enters, both carrying the event's style. It is separate from the Pane
// runtime because a Tab change is never a split change: the new Tab's tree is
// built once at its final grids and every frame only moves or fades the
// already-painted body (ADR 0026).

// bodySurfaceKind is the kind of surface the work body shows.
type bodySurfaceKind int

const (
	// bodySurfaceNone is no body yet, as the first frame before any surface
	// has been drawn.
	bodySurfaceNone bodySurfaceKind = iota
	// bodySurfaceTab is the active Tab's split tree.
	bodySurfaceTab
	// bodySurfaceHome is the Vault (or one of its pages).
	bodySurfaceHome
	// bodySurfaceSFTP is the SFTP surface.
	bodySurfaceSFTP
)

// bodySpec names one body the runtime draws: the state's current one, or the
// outgoing copy of a phase. A Tab body is drawn live (the current Tab), inert
// (a still copy of a Tab that is no longer active), or from a retained
// TabGhost (a closed Tab). A blank spec is the last-resort opaque body used
// when no Surface was retained.
type bodySpec struct {
	kind  bodySurfaceKind
	tabID model.TabID
	ghost *state.TabGhost
	inert bool
	blank bool
}

// key is the stable element key of the body. MyGo needs the same key from
// frame to frame for a body that stays, and a different one for the body
// going and the body arriving, so Enter, Exit, and AfterExits can tell them
// apart.
func (s bodySpec) key() string {
	switch s.kind {
	case bodySurfaceTab:
		if s.tabID == "" {
			return "work-body:none"
		}
		return "work-body:" + string(s.tabID)
	case bodySurfaceHome:
		return "work-body:home"
	case bodySurfaceSFTP:
		return "work-body:sftp"
	default:
		return "work-body:none"
	}
}

// elementKey is the MyGo child key of spec in the current generation. A
// body-only warp changes the generation, so MyGo sees a different element and
// drops the in-flight transition; the label stays spec.key() so the body is
// still found by its stable name.
func (b *workspaceBodyMotion) elementKey(spec bodySpec) string {
	if b.gen == 0 {
		return spec.key()
	}
	return spec.key() + "#" + strconv.Itoa(b.gen)
}

// bodyPhase is where a body run is. A run always builds the outgoing body on
// the event's frame (out) and the incoming one on the next (in). MyGo copies
// the outgoing child as it leaves phase out, so its Exit and ExitDuration must
// be on that frame; ChildExit is given on the phase-in frame.
type bodyPhase int

const (
	bodyIdle bodyPhase = iota
	bodyOut
	bodyIn
)

// workspaceBodyMotion is one window's Tab body runtime. The state records the
// event; this runtime turns it into one two-phase MyGo transition, keeps the
// closed Tab's Surfaces alive until its copy is gone, and asks for animation
// frames while the body moves.
type workspaceBodyMotion struct {
	mode paneMotionMode

	phase bodyPhase
	// out is the body leaving, drawn on the event frame. It carries the
	// retained TabGhost of a closed current Tab; end releases it.
	out bodySpec
	// event is the TabOpen, TabClose, or TabSwitch this run plays. tick
	// re-checks its switch so turning motion off mid-run warps.
	event motion.Event
	enter ui.Motion
	exit  ui.Motion
	// enterDuration is the incoming body's Duration, exitDuration the
	// outgoing body's explicit ExitDuration. A Tab close uses 150 ms for the
	// exit and 240 ms for the replacement's switch entrance.
	enterDuration, exitDuration time.Duration
	// enterSet is false for the Home appearance after a last-Tab close: Home
	// has no Enter of its own and waits for AfterExits.
	enterSet   bool
	afterExits bool
	// phaseEnd is when the longest part of the run ends. The frame at or after
	// it is built as the final layout.
	phaseEnd time.Time
	// outBuilt reports that the view built the outgoing body on the event
	// frame, so the next frame may build the incoming one.
	outBuilt bool
	// shown is the body the last built frame drew. The next event starts from
	// it, which is what makes an interruption continue from what the user saw.
	shown bodySpec
	// gen is the body child's element generation. A body-only warp bumps it,
	// so the view builds the final body under a fresh key: MyGo drops the
	// in-flight transition at once without DisableAnimations, which is
	// window-wide and would rest a simultaneous Pane transition.
	gen int
	// outElem is the outgoing body's element key in the generation it was
	// built. A warp rebuilds that key at rest to cancel its exit copy.
	outElem string
	// liveElem is the element key of the live current body the last frame
	// built, or "" when that frame built an outgoing body instead. A
	// body-only warp rebuilds it unseen: that drops its entrance and measures
	// the last commit's translation.
	liveElem string
	// warpKey is the outgoing body's element key when a run was warped while
	// MyGo was playing its exit copy. Every build pass of the warp frame,
	// which is instant, builds that key as an unlabeled still, which makes
	// MyGo drop the copy instead of moving it for the rest of the run; the
	// next frame does not build it and it goes with no copy.
	warpKey string
	// warpFrom is the live body's element key before a body-only warp. The
	// next build rebuilds that key once, unseen: MyGo then drops the
	// in-flight entrance without a copy, and its box measures the transform
	// of the last commit so Pane rectangles stay untransformed.
	warpFrom string
	// warpOut is the outgoing body's element key on a body-only warp. The
	// next build rebuilds it once, unseen, so its exit copy is cancelled
	// without DisableAnimations.
	warpOut string
	// recordOffset is the body child's show offset from the container in the
	// commit the current build reads. recordPane and recordRegion subtract it
	// so Pane geometry stays in the untransformed final layout on every frame
	// of a body transition.
	recordOffset bodyOffset
	// warpSeq is the last BodyWarpSeq this runtime consumed. The state raises
	// it for a layout change with no motion event (a batch Tab close), which
	// must make this frame final even while a run plays. It is a sequence, not
	// an event: no per-Tab transition ever starts from it.
	warpSeq uint64
	// layoutWarped reports that a body warp signal arrived on this frame. An
	// event consumed on the same frame must not animate over the newer layout:
	// the batch close is the final word.
	layoutWarped bool
	// animationFrames counts the frames this runtime asked for. A warped event
	// asks for none; tests assert it.
	animationFrames int
}

// bodyOffset is a translation from the work body's container to the box its
// child showed in. Only translation can differ: a Motion moves the child in
// the layout by X/Y (or XPercent/YPercent); Scale and Collapse are a transform
// or a clip and leave what is inside laid out at its final boxes.
type bodyOffset struct{ x, y float32 }

// bodyOffsetOf returns the child's translation from the container, if both
// boxes are laid out.
func bodyOffsetOf(container, child ui.Rect) (bodyOffset, bool) {
	if container.W <= 0 || container.H <= 0 || child.W <= 0 || child.H <= 0 {
		return bodyOffset{}, false
	}
	return bodyOffset{x: child.X - container.X, y: child.Y - container.Y}, true
}

// untranslate moves r back into the container's coordinates.
func (o bodyOffset) untranslate(r ui.Rect) ui.Rect {
	r.X -= o.x
	r.Y -= o.y
	return r
}

func newWorkspaceBodyMotion(mode paneMotionMode) *workspaceBodyMotion {
	return &workspaceBodyMotion{mode: mode}
}

// tick advances the body run before the frame is built. The event frame
// already built the outgoing body; this frame becomes the incoming one, and
// the run lasts until its longest part ends. A hidden or resized window,
// Reduce Motion, or a setting switched off mid-run warps without asking for
// more frames. paneMotionPending is a Pane event consumed on this same frame
// that will animate; paneMotionActive is a Pane transition already carried by
// MyGo. An instant warp would rest either transition too, so the body ends
// without one (the one-switch rule).
//
// A body warp signal is consumed before anything else: a layout change with no
// motion event (a batch Tab close) makes this frame final, and a run that was
// playing must not keep animating. While the body rests the signal is a
// no-op; only its sequence advances.
func (b *workspaceBodyMotion) tick(c *ui.Context, s *state.State, setting domainpreferences.Motion,
	hidden, resized, paneMotionPending, paneMotionActive, otherClose bool) {
	if b == nil {
		return
	}
	b.layoutWarped = false
	if s != nil {
		if seq := s.BodyWarpSeq(); seq != b.warpSeq {
			b.warpSeq = seq
			if b.phase != bodyIdle {
				// A run was playing when the layout changed under it. The event
				// consumed on this same frame must not animate over the final
				// layout this warp demands.
				b.layoutWarped = true
				if paneMotionPending || paneMotionActive {
					b.warpBodyOnly()
				} else {
					b.warp(c)
				}
			}
			// At rest the signal is a true no-op: it neither restarts MyGo's
			// transitions nor suppresses a newer event on this frame.
		}
	}
	if b.phase == bodyIdle {
		return
	}
	if otherClose {
		// The event on this frame closes a Tab this body does not show, so
		// consume ends the run with a warp on this frame. Asking for an
		// animation frame here would only schedule a repaint the warp discards.
		return
	}
	if hidden || resized {
		// A hidden window shows nothing of the run; a resize has reflowed the
		// body it was moving. Both make this frame final (ticket 08).
		b.warp(c)
		return
	}
	if !motion.Enabled(setting, b.event.Kind) || c.Preferences().ReduceMotion {
		if paneMotionPending {
			// DisableAnimations is window-wide: the Pane change on this frame
			// animates with its own switch still on. Warp the body alone by
			// remounting its child under a fresh generation, so the body is
			// final on this frame and the Pane's transition keeps playing.
			b.warpBodyOnly()
			return
		}
		// Switching an event off mid-run, or Reduce Motion turning on, warps
		// at once: the frame is the final layout and MyGo's in-flight
		// entrance and exit copy rest (ticket 08's rule for Pane runs).
		b.warp(c)
		return
	}
	now := c.Now()
	switch b.phase {
	case bodyOut:
		if !b.outBuilt {
			// The event frame did not build the outgoing body, so there is
			// nothing to copy yet. The next frame builds the incoming body
			// with no exit copy, and the run still ends at its duration.
			return
		}
		b.phase = bodyIn
		b.outBuilt = false
		duration := b.enterDuration
		if b.exitDuration > duration {
			duration = b.exitDuration
		}
		b.phaseEnd = now.Add(duration)
		b.animationFrames++
		c.AnimationFrame()
	case bodyIn:
		if !now.Before(b.phaseEnd) {
			// This frame is the final layout; release the retained body and
			// stop animating.
			b.end()
			return
		}
		b.animationFrames++
		c.AnimationFrame()
	}
}

// endsOtherBody reports that event closes a Tab this runtime is not showing,
// so a run already playing ends with a warp rather than a transition. consume
// makes that decision with the same shown body; tick reads it only to avoid
// asking for a frame the warp would discard.
func (b *workspaceBodyMotion) endsOtherBody(event motion.Event) bool {
	if b == nil || b.phase == bodyIdle || event.Kind != motion.TabClose {
		return false
	}
	return b.shown.kind != bodySurfaceTab || b.shown.tabID != event.TabID
}

// consume turns the latest Tab event into a body run, or warps it. A Tab close
// only moves the body when the closed Tab is the one on screen; a non-current
// close animates only the strip. A Tab open or switch leaves from whatever
// was shown, so an interruption continues from the frame the user saw.
// paneMotionPending is the same-frame Pane transition tick was given: when the
// body must end without DisableAnimations, it does so by generation instead.
func (b *workspaceBodyMotion) consume(c *ui.Context, s *state.State, event motion.Event,
	setting domainpreferences.Motion, visible, paneMotionPending bool) {
	if b == nil {
		return
	}
	// Latest event wins. A ghost the previous phase retained is released now;
	// MyGo's already-painted copy keeps going on its own until a new run
	// retargets it.
	prevWarp := b.warpOutKey()
	hadRun := b.phase != bodyIdle
	b.end()
	// A warp signal from a batch close on this same frame is newer than every
	// event: nothing may animate over the final layout it demands.
	animatable := visible && motion.Enabled(setting, event.Kind) && !b.layoutWarped
	if !animatable {
		// Nothing takes over the previous run's movement, so this frame must
		// already be the final layout: warp rather than let MyGo finish the
		// entrance and exit copy it was painting.
		b.warpTo(c, prevWarp)
	}

	switch event.Kind {
	case motion.TabClose:
		ghost, haveGhost := s.TakeTabGhost(event.TabID)
		// A Tab this view does not draw has no body copy, and any other
		// retained close is stale.
		s.ReleaseTabGhosts()
		shown := b.shown
		if shown.kind != bodySurfaceTab || shown.tabID != event.TabID {
			// Closing a Tab that is not the body on screen plays no transition
			// of its own. But a run that was already playing cannot simply
			// stop: MyGo keeps painting its in-flight entrance over the body
			// that stays. Make this frame final instead. With no run, a
			// non-current close at rest must not move the body at all.
			ghost.Release()
			if hadRun && animatable {
				if paneMotionPending {
					b.warpBodyOnly()
				} else {
					b.warpTo(c, prevWarp)
				}
			}
			return
		}
		if !animatable {
			ghost.Release()
			return
		}
		out := bodySpec{kind: bodySurfaceTab, tabID: event.TabID}
		if haveGhost && ghost != nil {
			out.ghost = ghost
		} else {
			// No Surface was retained: an opaque body-shaped copy is the last
			// resort, never a live placeholder.
			out.blank = true
		}
		exit := bodyCloseMotion(setting.CloseStyle)
		exitDuration := motion.ScaleMotion(motion.BaseDuration(motion.TabClose), setting.Speed)
		if event.TargetTabID == "" {
			// No Tab remains: Home appears after the outgoing copy is gone,
			// with no second animation of its own.
			b.begin(c, out, event, ui.Motion{}, exit, 0, exitDuration, false, true)
			return
		}
		dir := switchDirection(event.Direction)
		enter, _ := bodySwitchMotions(setting.SwitchStyle, dir)
		b.begin(c, out, event, enter, exit,
			motion.ScaleMotion(motion.BaseDuration(motion.TabSwitch), setting.Speed),
			exitDuration, true, false)
	case motion.TabOpen, motion.TabSwitch:
		s.ReleaseTabGhosts()
		current := bodyCurrentSpec(s)
		if !animatable {
			return
		}
		if b.shown.kind == bodySurfaceNone {
			// The first body has no previous frame to leave; it appears at its
			// final layout.
			return
		}
		if event.Kind == motion.TabOpen && (current.kind != bodySurfaceTab || current.tabID != event.TabID) {
			// A future background Tab open animates only the strip. The body
			// first appears through a normal switch.
			return
		}
		if event.Kind == motion.TabSwitch && (current.kind != bodySurfaceTab || current.tabID != event.TabID) {
			// The active body is not the Tab the switch names, so there is no
			// incoming Tab body to animate.
			return
		}
		out := b.shown
		if out.kind == bodySurfaceTab && out.ghost == nil {
			if _, ok := s.TabIndexByID(out.tabID); !ok {
				// The last body was a Tab that left the layout without a
				// retained ghost: an opaque body-shaped copy leaves in its
				// place.
				out = bodySpec{kind: bodySurfaceTab, tabID: out.tabID, blank: true}
			}
		}
		if out.kind == bodySurfaceTab && out.ghost == nil {
			out.inert = true
		}
		if event.Kind == motion.TabOpen {
			b.begin(c, out, event,
				bodyOpenMotion(setting.OpenStyle),
				bodyCloseMotion(setting.CloseStyle),
				motion.ScaleMotion(motion.BaseDuration(motion.TabOpen), setting.Speed),
				motion.ScaleMotion(motion.BaseDuration(motion.TabClose), setting.Speed),
				true, false)
			return
		}
		dir := switchDirection(event.Direction)
		enter, exit := bodySwitchMotions(setting.SwitchStyle, dir)
		duration := motion.ScaleMotion(motion.BaseDuration(motion.TabSwitch), setting.Speed)
		b.begin(c, out, event, enter, exit, duration, duration, true, false)
	}
}

// begin starts the two-phase run: the frame being built draws out with its
// Exit and explicit ExitDuration, and the next frame draws the incoming body
// with its Enter and the container's ChildExit.
func (b *workspaceBodyMotion) begin(c *ui.Context, out bodySpec, event motion.Event,
	enter, exit ui.Motion, enterDuration, exitDuration time.Duration, enterSet, afterExits bool) {
	b.phase = bodyOut
	b.out = out
	b.outElem = b.elementKey(out)
	b.event = event
	b.enter, b.exit = enter, exit
	b.enterDuration, b.exitDuration = enterDuration, exitDuration
	b.enterSet, b.afterExits = enterSet, afterExits
	b.outBuilt = false
	b.animationFrames++
	c.AnimationFrame()
}

// end releases the retained outgoing body and stops the run. The frame the
// run ends on is built as the final layout by the view's idle path. shown
// keeps the last body the user saw, so the next event starts there.
func (b *workspaceBodyMotion) end() {
	if b.out.ghost != nil {
		b.out.ghost.Release()
	}
	b.out = bodySpec{}
	b.outElem, b.liveElem = "", ""
	b.phase = bodyIdle
	b.outBuilt = false
	b.enterSet, b.afterExits = false, false
	b.warpKey, b.warpFrom, b.warpOut = "", "", ""
}

// warp ends a still-playing run at once: this frame is the final layout and
// nothing MyGo was painting for the body keeps moving or asks for a frame.
func (b *workspaceBodyMotion) warp(c *ui.Context) {
	b.warpTo(c, b.warpOutKey())
}

// warpTo is warp with the outgoing key captured before end clears the run. An
// exit copy that already left keeps moving even on an instant frame, so the
// idle view builds outKey once at rest to reclaim it. A warp earlier in the
// same frame may already have recorded a key; an empty outKey keeps it.
func (b *workspaceBodyMotion) warpTo(c *ui.Context, outKey string) {
	key := b.warpKey
	b.end()
	if outKey != "" {
		key = outKey
	}
	b.warpKey = key
	c.DisableAnimations()
}

// warpOutKey is the outgoing body's element key when the run is past its
// event frame: that body's element has left and MyGo is animating its copy.
// The event frame itself has no copy yet, so it needs none.
func (b *workspaceBodyMotion) warpOutKey() string {
	if b.phase != bodyIn || b.out.kind != bodySurfaceTab || b.out.tabID == "" {
		return ""
	}
	return b.outElem
}

// warpBodyOnly ends a run at once without DisableAnimations, which is
// window-wide and would rest a simultaneous Pane transition (the one-switch
// rule). It bumps the body's generation, so the next build makes the body a
// fresh element at its final box and MyGo drops the in-flight entrance; the
// old live and outgoing keys are rebuilt unseen on that build, so their
// transitions end without a copy and the live one's box measures the last
// commit's translation for the Pane rectangles.
func (b *workspaceBodyMotion) warpBodyOnly() {
	if b == nil || b.phase == bodyIdle {
		return
	}
	from, out := b.liveElem, b.outElem
	b.end()
	b.warpFrom, b.warpOut = from, out
	b.gen++
}

// bodyCurrentSpec names the body the state shows now.
func bodyCurrentSpec(s *state.State) bodySpec {
	switch {
	case terminalVisible(s):
		id, ok := s.TabID(s.ActiveTab())
		if !ok {
			return bodySpec{kind: bodySurfaceNone}
		}
		return bodySpec{kind: bodySurfaceTab, tabID: id}
	case s.SFTPVisible():
		return bodySpec{kind: bodySurfaceSFTP}
	default:
		return bodySpec{kind: bodySurfaceHome}
	}
}

// view draws the work body inside one stable keyed container. The container
// keeps its ID every frame, which MyGo requires for a child's Enter, Exit, and
// AfterExits. The child is absolute, so it is laid out at the whole body and
// only its transition moves or fades it; nothing inside is reflowed.
func (b *workspaceBodyMotion) view(c *ui.Context, s *state.State, m locales.Messages, t *ui.Theme,
	owner state.RenderOwner, focusEpoch uint64, wm *workspacePaneMotion) {
	root := ui.Column(c).Key("work-body-root").Grow(1).FillWidth().Clip()
	if b.mode.labels {
		root.Label("work-body-root")
	}
	rootBounds := root.LayoutBounds()
	// Every build starts untransformed; only a live Tab body measures the
	// offset its transition showed on the commit this build reads.
	b.recordOffset = bodyOffset{}
	switch b.phase {
	case bodyOut:
		b.liveElem = ""
		root.Children(func() {
			b.build(c, s, m, t, owner, focusEpoch, wm, b.out, ui.ElementTransition{
				// The body is already laid out at its final box; only the exit
				// moves it. ExitDuration is explicit so a Tab close leaves in
				// 150 ms even when the incoming replacement takes 240 ms.
				Duration:     -1,
				Ease:         ui.EaseOut,
				ExitEase:     ui.EaseOut,
				ExitDuration: b.exitDuration,
				Exit:         &ui.Motion{},
			}, rootBounds, false)
		})
		b.outBuilt = true
	case bodyIn:
		spec := bodyCurrentSpec(s)
		transition := ui.ElementTransition{Ease: ui.EaseOut}
		if b.enterSet {
			enter := b.enter
			transition.Duration = b.enterDuration
			transition.Enter = &enter
		} else {
			// Home waits for the outgoing body's copies and then shows at
			// once: it gets no animation of its own.
			transition.Duration = -1
		}
		transition.AfterExits = b.afterExits
		root.ChildExit(b.exit)
		root.Children(func() {
			b.build(c, s, m, t, owner, focusEpoch, wm, spec, transition, rootBounds, true)
		})
		b.shown = spec
		b.outBuilt = false
	default:
		spec := bodyCurrentSpec(s)
		finalKey := b.elementKey(spec)
		// The reclaim lasts every build pass of the warp frame, which MyGo
		// marks instant; the next engine frame drops it. Clearing it after
		// the first pass would lose it should MyGo build again in one frame.
		warpKey := ""
		if c.Instant() {
			warpKey = b.warpKey
		} else {
			b.warpKey = ""
		}
		// A body-only warp leaves the old live and outgoing keys for this
		// build alone: rebuilding them unseen drops their in-flight
		// transitions without a copy, and the live one's box is the last
		// commit's translation, which the Pane rectangles subtract.
		warpOut, warpFrom := b.warpOut, b.warpFrom
		b.warpOut, b.warpFrom = "", ""
		root.Children(func() {
			if warpKey != "" && warpKey != finalKey {
				// Reclaim MyGo's moving exit copy of the outgoing body by
				// building its key at rest: it snaps to the final box under
				// the body it left and is dropped with no copy next frame. It
				// is unlabeled and opaque, so only the final body shows.
				ui.Column(c).Key(warpKey).Absolute().Fill().
					Transition(ui.ElementTransition{Duration: -1}).
					Children(func() { ui.Column(c).Fill().Background(t.Background) })
			}
			if warpFrom != "" && warpFrom != finalKey && warpFrom != warpKey {
				old := ui.Column(c).Key(warpFrom).Absolute().Fill().
					Transition(ui.ElementTransition{Duration: -1})
				if off, ok := bodyOffsetOf(rootBounds, old.LayoutBounds()); ok {
					b.recordOffset = off
				}
			}
			if warpOut != "" && warpOut != finalKey && warpOut != warpKey && warpOut != warpFrom {
				ui.Column(c).Key(warpOut).Absolute().Fill().
					Transition(ui.ElementTransition{Duration: -1})
			}
			b.build(c, s, m, t, owner, focusEpoch, wm, spec, ui.ElementTransition{Duration: -1}, rootBounds, true)
		})
		b.shown = spec
		b.outBuilt = false
	}
}

// build draws one keyed body child inside the container already open in c.
// The child's element key carries the body generation; its label stays the
// stable body key. A live Tab body measures the show offset of the commit
// this build reads, so recordPane and recordRegion stay in the untransformed
// layout while MyGo moves the body.
func (b *workspaceBodyMotion) build(c *ui.Context, s *state.State, m locales.Messages, t *ui.Theme,
	owner state.RenderOwner, focusEpoch uint64, wm *workspacePaneMotion,
	spec bodySpec, transition ui.ElementTransition, rootBounds ui.Rect, live bool) {
	key := b.elementKey(spec)
	child := ui.Column(c).Key(key).Absolute().Fill().Transition(transition)
	if live {
		b.liveElem = key
		if spec.kind == bodySurfaceTab && spec.ghost == nil && !spec.blank && !spec.inert {
			if off, ok := bodyOffsetOf(rootBounds, child.LayoutBounds()); ok {
				b.recordOffset = off
			}
		}
	}
	if b.mode.labels {
		child.Label(spec.key())
	}
	child.Children(func() {
		b.draw(c, s, m, t, owner, focusEpoch, wm, spec)
	})
}

// draw draws one body's content. The current body is the shell's live
// renderer; an outgoing Tab is a still copy whose surfaces never take focus,
// ask for it, or size their terminals; a blank body is an opaque shape.
func (b *workspaceBodyMotion) draw(c *ui.Context, s *state.State, m locales.Messages, t *ui.Theme,
	owner state.RenderOwner, focusEpoch uint64, wm *workspacePaneMotion, spec bodySpec) {
	switch spec.kind {
	case bodySurfaceTab:
		switch {
		case spec.ghost != nil:
			b.drawTab(c, s, m, t, ghostTabRef(spec.ghost))
		case spec.blank:
			ui.Column(c).Fill().Background(t.Background)
		case spec.inert:
			if index, ok := s.TabIndexByID(spec.tabID); ok {
				b.drawTab(c, s, m, t, inertTabRef(s, index))
				return
			}
			ui.Column(c).Fill().Background(t.Background)
		default:
			activeTab(c, s, m, t, owner, focusEpoch, wm)
		}
	case bodySurfaceSFTP:
		sftpBody(c, s, m, t)
	case bodySurfaceHome:
		homeBody(c, s, m, t)
	default:
		ui.Column(c).Fill().Background(t.Background)
	}
}

// drawTab draws one whole Tab's split tree as a still body: every Pane uses
// ViewInert, the tree's split chrome, and the header a Tab shows. A Tab close
// with several Panes leaves as this one body, never as per-Pane close runs.
func (b *workspaceBodyMotion) drawTab(c *ui.Context, s *state.State, m locales.Messages,
	t *ui.Theme, ref tabRef) {
	root, focus, ok := ref.tree()
	if !ok || root == nil {
		ui.Column(c).Fill().Background(t.Background)
		return
	}
	ref.split = !root.IsPane()
	ordinal := 0
	layout.WorkLayout(c, layout.WorkSlots{Panes: func() {
		region := ui.Column(c).Fill()
		region.Children(func() { paneTree(c, s, m, t, ref, root, focus, &ordinal) })
	}})
}

// inertTabRef binds the split renderer to a live Tab as a still copy. It holds
// no Pane commands, draws no drop zones, and never records motion geometry;
// focus is a no-op so a click in the copy cannot change the live Tab.
func inertTabRef(s *state.State, index int) tabRef {
	id, _ := s.TabID(index)
	return tabRef{
		id:    id,
		inert: true,
		tree:  func() (*domainworkspace.Node, []int, bool) { return s.TabTree(index) },
		pane: func(paneID model.PaneID) (state.PaneInfo, bool) {
			return s.PaneInfoAt(index, paneID)
		},
		focus:     func(model.PaneID) bool { return false },
		magnified: func() model.PaneID { return s.MagnifiedPaneID(index) },
	}
}

// ghostTabRef binds the split renderer to a closed Tab's retained tree and
// Pane infos.
func ghostTabRef(ghost *state.TabGhost) tabRef {
	return tabRef{
		id:    ghost.ID,
		inert: true,
		tree: func() (*domainworkspace.Node, []int, bool) {
			return ghost.Root, ghost.FocusPath, ghost.Root != nil
		},
		pane:      ghost.Pane,
		focus:     func(model.PaneID) bool { return false },
		magnified: func() model.PaneID { return ghost.Magnified },
	}
}

// switchDirection turns a Tab event's index direction into a motion sign:
// +1 for a target later in the strip, -1 for one before it.
func switchDirection(direction int) float32 {
	return motion.SwitchDirection(direction)
}

// bodyOpenMotion is the whole body's entrance for an open style. slide and
// gnomed must say opacity 1: MyGo's Motion opacity defaults to 0.
func bodyOpenMotion(style domainpreferences.MotionStyle) ui.Motion {
	return motion.BodyOpenMotion(style)
}

// bodyCloseMotion is the whole body's exit for a close style. It is the same
// geometry read from the body's shown box: popin shrinks to 87%, slide leaves
// left by its own width, gnomed folds to its bottom edge, fade keeps its
// geometry.
func bodyCloseMotion(style domainpreferences.MotionStyle) ui.Motion {
	return motion.BodyCloseMotion(style)
}

// bodySwitchMotions is the incoming and outgoing body motion for a switch
// style. dir is +1 when the target Tab is later in the strip (the incoming
// body comes from the right and the outgoing leaves to the left) and -1 when
// it is earlier. slide and slidevert stay opaque; the fade variants use MyGo's
// default opacity 0 on both ends, and fade moves nothing.
func bodySwitchMotions(style domainpreferences.MotionSwitchStyle, dir float32) (enter, exit ui.Motion) {
	return motion.BodySwitchMotions(style, dir)
}
