package quake

import (
	"strconv"
	"time"

	"github.com/egoist/mygo/ui"

	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/motion"
)

// The Quake Panel owns its own flat Tab workspace, so it also owns its own
// motion: one runtime for the bar's Tab strip and one for the body below it.
// They share the state's event sequence but keep separate last-seen stamps, so
// a strip-only event never drives the body and a switch changes only the
// selected fill and the body. Quake has no split Panes, so no Pane event kind
// is ever recorded or consumed here (ticket 21a).

// quakeBodyPhase is where a Tab body run is. The event frame builds the
// outgoing body (out) and the next builds the incoming one (in), the same
// two-phase shape the main shell uses.
type quakeBodyPhase int

const (
	quakeBodyIdle quakeBodyPhase = iota
	quakeBodyOut
	quakeBodyIn
)

// quakeBodyMotion is the Panel's whole-body runtime. A Quake Tab is one
// terminal, so a Tab open, close or switch animates the body as one piece; it
// never reflows a terminal or resizes a PTY.
type quakeBodyMotion struct {
	frames    int
	skipped   bool
	wasHidden bool
	lastSeq   uint64

	phase quakeBodyPhase
	// out is the Tab number leaving on this run; 0 means no previous body.
	out int
	// event is the TabOpen, TabClose or TabSwitch this run plays, kept so a
	// switch turned off mid-run warps at once.
	event motion.Event
	enter ui.Motion
	exit  ui.Motion
	// enterDuration is the incoming body's Duration, exitDuration the outgoing
	// body's explicit ExitDuration. A close uses 150 ms for the exit and 240 ms
	// for a replacement's switch entrance.
	enterDuration, exitDuration time.Duration
	// enterSet is false when no incoming body exists (the last Tab closed):
	// the outgoing copy leaves and the empty state follows with no animation.
	enterSet bool
	// phaseEnd is when the longest part of the run ends.
	phaseEnd time.Time
	// outBuilt reports that the outgoing body was built on the event frame.
	outBuilt bool
	// shown is the Tab number the last built frame drew; the next event starts
	// from it.
	shown int
	// gen is the body child's element generation. A warp bumps it so the final
	// body is a fresh element and MyGo drops the in-flight transition without
	// DisableAnimations, which is window-wide and would also rest the bar's
	// strip (the one-switch rule).
	gen int
	// animationFrames counts the frames this runtime asked for.
	animationFrames int
	// width and height are the Panel size the last frame was laid out at. A
	// resize reflows the terminal body outside any motion event, so a run
	// planned for the old geometry warps and this frame is final (ticket 08).
	width, height float32
	haveSize      bool
}

// skip records that the Panel body was not built on the last frame (its own
// route rendered no Quake view). The next frame warps the gap.
func (b *quakeBodyMotion) skip() { b.skipped = true }

// beginFrame consumes the latest Tab event before the body is built.
func (b *quakeBodyMotion) beginFrame(c *ui.Context, s *State) {
	first := b.frames == 0
	b.frames++
	skipped := b.skipped
	b.skipped = false
	// Record the window's own visibility as it reports it. A view-local skip
	// is not a window hide: it makes this frame warp, and the frame after it is
	// a normal visible one, so a later event still animates instead of being
	// treated as a second show frame (ticket 08).
	windowHidden := c.WindowHidden()
	hidden := windowHidden || skipped
	wasHidden := b.wasHidden
	b.wasHidden = windowHidden
	// A Panel resize reflows the terminal body outside any motion event. Like
	// a hide, it makes this frame final: a run planned for the old size warps,
	// and an event first seen here animates nothing (ticket 08).
	width, height := c.Size()
	resized := b.haveSize && (width != b.width || height != b.height)
	if width > 0 && height > 0 {
		b.width, b.height = width, height
		b.haveSize = true
	}
	setting := s.Motion()
	reduce := c.Preferences().ReduceMotion
	now := c.Now()

	event, hasEvent := s.MotionEvent()
	unseen := hasEvent && event.Seq > b.lastSeq

	if b.phase != quakeBodyIdle && unseen {
		// The new event supersedes the run; consume starts from the same shown
		// body. Nothing of the old run survives.
		b.abort()
	}
	if b.phase != quakeBodyIdle {
		switch {
		case hidden || resized || reduce || !motion.Enabled(setting, b.event.Kind):
			b.abort()
		case b.phase == quakeBodyOut:
			if !b.outBuilt {
				// The event frame did not build the outgoing body. Nothing was
				// shown moving; finish on the next build.
				b.abort()
			} else {
				b.phase = quakeBodyIn
				b.outBuilt = false
				duration := b.enterDuration
				if b.exitDuration > duration {
					duration = b.exitDuration
				}
				b.phaseEnd = now.Add(duration)
				b.animationFrames++
				c.AnimationFrame()
			}
		case b.phase == quakeBodyIn:
			if !now.Before(b.phaseEnd) {
				b.phase = quakeBodyIdle
				b.shown = activeNumber(s)
			} else {
				b.animationFrames++
				c.AnimationFrame()
			}
		}
	}
	if !unseen {
		return
	}
	b.lastSeq = event.Seq
	switch event.Kind {
	case motion.TabOpen, motion.TabClose, motion.TabSwitch:
	default:
		// Quake records no other kinds. A future non-Tab event is not this
		// view's to animate.
		return
	}
	visible := !first && !hidden && !wasHidden && !resized && !reduce
	b.consume(c, s, event, setting, visible)
}

// consume turns one Tab event into a two-phase body run, or warps it.
func (b *quakeBodyMotion) consume(c *ui.Context, s *State, event motion.Event,
	setting domainpreferences.Motion, visible bool) {
	animatable := visible && motion.Enabled(setting, event.Kind)
	shown := b.shown
	if shown == 0 || !animatable {
		b.abort()
		return
	}
	active := activeNumber(s)
	switch event.Kind {
	case motion.TabOpen:
		// A background Tab open, or the first body, has nothing to leave.
		if active == shown || active == 0 {
			b.abort()
			return
		}
		b.begin(c, shown, event,
			motion.BodyOpenMotion(setting.OpenStyle),
			motion.BodyCloseMotion(setting.CloseStyle),
			motion.ScaleMotion(motion.BaseDuration(motion.TabOpen), setting.Speed),
			motion.ScaleMotion(motion.BaseDuration(motion.TabClose), setting.Speed),
			true)
	case motion.TabSwitch:
		target := tabNumberFromID(event.TabID)
		// A switch names the incoming Tab; if the active body is not it there
		// is nothing to animate. A switch to the same body changes nothing.
		if target == 0 || active != target || target == shown {
			b.abort()
			return
		}
		enter, exit := motion.BodySwitchMotions(setting.SwitchStyle, motion.SwitchDirection(event.Direction))
		duration := motion.ScaleMotion(motion.BaseDuration(motion.TabSwitch), setting.Speed)
		b.begin(c, shown, event, enter, exit, duration, duration, true)
	case motion.TabClose:
		closed := tabNumberFromID(event.TabID)
		if closed == 0 || closed != shown {
			// Closing a non-current Tab moves only the bar.
			b.abort()
			return
		}
		exit := motion.BodyCloseMotion(setting.CloseStyle)
		exitDuration := motion.ScaleMotion(motion.BaseDuration(motion.TabClose), setting.Speed)
		if event.TargetTabID == "" {
			b.begin(c, shown, event, ui.Motion{}, exit, 0, exitDuration, false)
			return
		}
		enter, _ := motion.BodySwitchMotions(setting.SwitchStyle, motion.SwitchDirection(event.Direction))
		b.begin(c, shown, event, enter, exit,
			motion.ScaleMotion(motion.BaseDuration(motion.TabSwitch), setting.Speed),
			exitDuration, true)
	}
}

// begin starts the two-phase run: this frame draws out with its Exit and
// explicit ExitDuration, and the next draws the incoming body.
func (b *quakeBodyMotion) begin(c *ui.Context, out int, event motion.Event,
	enter, exit ui.Motion, enterDuration, exitDuration time.Duration, enterSet bool) {
	b.phase = quakeBodyOut
	b.out = out
	b.event = event
	b.enter, b.exit = enter, exit
	b.enterDuration, b.exitDuration = enterDuration, exitDuration
	b.enterSet = enterSet
	b.outBuilt = false
	b.animationFrames++
	c.AnimationFrame()
}

// abort ends a run at once. If its transition was already built, the body
// generation changes so MyGo drops the in-flight entrance and exit copy
// without the window-wide DisableAnimations, which would rest the bar's strip.
func (b *quakeBodyMotion) abort() {
	if b.phase != quakeBodyIdle {
		b.gen++
	}
	b.phase = quakeBodyIdle
	b.out = 0
	b.outBuilt = false
	b.enterSet = false
	b.phaseEnd = time.Time{}
}

// elementKey is the body root's key in the current generation.
func (b *quakeBodyMotion) elementKey() string {
	if b.gen == 0 {
		return "quake-body-root"
	}
	return "quake-body-root#" + strconv.Itoa(b.gen)
}

// view draws the Panel's body inside one stable keyed container. The container
// keeps its identity every frame except a warp; the child is absolute, so a
// transition moves or fades the whole terminal and nothing inside is reflowed.
func (b *quakeBodyMotion) view(c *ui.Context, s *State) {
	root := ui.Column(c).Key(b.elementKey()).FillWidth().Grow(1).MinWidth(0).MinHeight(0).Clip()
	root.Label("quake-body-root")
	switch b.phase {
	case quakeBodyOut:
		root.Children(func() {
			exit := b.exit
			child := ui.Column(c).Key(quakeBodyKey(b.out)).Absolute().Fill().
				Transition(ui.ElementTransition{
					Duration:     -1,
					Ease:         ui.EaseOut,
					ExitEase:     ui.EaseOut,
					ExitDuration: b.exitDuration,
					Exit:         &exit,
				})
			child.Children(func() { b.draw(c, s, b.out) })
		})
		b.outBuilt = true
	case quakeBodyIn:
		transition := ui.ElementTransition{Ease: ui.EaseOut}
		if b.enterSet {
			enter := b.enter
			transition.Duration = b.enterDuration
			transition.Enter = &enter
		} else {
			transition.Duration = -1
		}
		root.ChildExit(b.exit)
		root.Children(func() {
			b.buildCurrent(c, s, transition)
		})
		b.shown = activeNumber(s)
		b.outBuilt = false
	default:
		root.Children(func() {
			b.buildCurrent(c, s, ui.ElementTransition{Duration: -1})
		})
		b.shown = activeNumber(s)
		b.outBuilt = false
	}
}

// buildCurrent draws the active Quake Tab inside the body container.
func (b *quakeBodyMotion) buildCurrent(c *ui.Context, s *State, transition ui.ElementTransition) {
	number := activeNumber(s)
	child := ui.Column(c).Key(quakeBodyKey(number)).Absolute().Fill().Transition(transition)
	if number == 0 {
		child.Children(func() {
			if s.Problem() != nil {
				ui.Column(c).Fill().Justify(ui.Center).Children(func() {
					ui.Text(c, locales.In(c.Locale()).PaneOpenFailed()).TextColor(c.Theme().Danger)
				})
			}
		})
		return
	}
	child.Children(func() { b.draw(c, s, number) })
}

// draw draws one Quake Tab's terminal by number. A Tab whose Surface no longer
// exists (the closed Tab of a body close) is an opaque blank body: the Panel
// closes its Surface eagerly, so there is no live copy to retain.
func (b *quakeBodyMotion) draw(c *ui.Context, s *State, number int) {
	if number != 0 {
		if tab, ok := tabByNumber(s, number); ok && tab.Surface != nil {
			ui.Column(c).FillWidth().Grow(1).MinWidth(0).MinHeight(0).Children(func() {
				// A VNC Tab keeps its per-kind chrome in the Panel too, so the
				// scale, view-only, clipboard, Ctrl-Alt-Del, and grab controls
				// stay reachable. The Panel is not a host window, so it uses the
				// embedded toolbar, which omits fullscreen. Terminal Surfaces keep
				// the body-only layout.
				if bar, ok := viewerBar(tab.Surface); ok {
					kit.Bar(c, kit.BarPane).Padding(0, 4, 0, 8).Gap(4).Children(func() {
						ui.Column(c).Grow(1).MinWidth(0)
						bar.ToolbarEmbedded(c)
					})
				}
				if focused, ok := tab.Surface.(FocusedSurface); ok {
					focused.ViewFocused(c, number == activeNumber(s))
					return
				}
				tab.Surface.View(c)
			})
			return
		}
	}
	ui.Column(c).Fill().Background(c.Theme().Background)
}

// quakeBodyKey is one Tab body's stable child key.
func quakeBodyKey(number int) string {
	if number == 0 {
		return "quake-body:none"
	}
	return "quake-body:" + strconv.Itoa(number)
}

// activeNumber is the active Tab's number, or 0 when the workspace is empty.
func activeNumber(s *State) int {
	tab, ok := s.Active()
	if !ok {
		return 0
	}
	return tab.Number
}

// tabByNumber finds a live Quake Tab by its stable number.
func tabByNumber(s *State, number int) (Tab, bool) {
	if number <= 0 {
		return Tab{}, false
	}
	for index := 0; index < s.TabCount(); index++ {
		if tab, ok := s.TabAt(index); ok && tab.Number == number {
			return tab, true
		}
	}
	return Tab{}, false
}

// quakeStripRun is one bar animation: a Tab opened, closed or reordered. The
// strip changes its selected fill in the same frame; only geometry moves.
type quakeStripRun struct {
	kind     motion.Kind
	started  time.Time
	duration time.Duration
}

// quakeStripMotion is the Panel bar's own strip runtime. The bar and body keep
// separate last-seen stamps, so a Tab switch changes only the selected fill and
// the body, never the strip's position.
type quakeStripMotion struct {
	frames    int
	skipped   bool
	wasHidden bool
	lastSeq   uint64

	run *quakeStripRun
	// generation names the strip row that holds the items. A warp that must
	// cancel an in-flight item transition or exit copy raises it, so the row
	// becomes a new parent and MyGo drops the old records without the
	// window-wide DisableAnimations, which would also rest the body.
	generation int
	// animate and warp describe the transition this build attaches.
	animate bool
	warp    bool
	// exit reports that the bar may attach a close record this build; it is
	// gated by the TabClose switch, the window visibility and Reduce Motion.
	exit bool
	// animationFrames counts the frames this runtime asked for.
	animationFrames int
	// width and height are the Panel size the last frame was laid out at. A
	// resize reflows the row and can move every item under a run, so the run
	// warps instead of sliding from the old boxes (ticket 08).
	width, height float32
	haveSize      bool
}

// skip records that the bar was not built on the last frame.
func (m *quakeStripMotion) skip() { m.skipped = true }

// beginFrame consumes the latest Tab event before the bar is built.
func (m *quakeStripMotion) beginFrame(c *ui.Context, s *State) {
	m.animate, m.warp = false, false
	first := m.frames == 0
	m.frames++
	skipped := m.skipped
	m.skipped = false
	// Record the window's own visibility as it reports it. A view-local skip
	// is not a window hide: it makes this frame warp, and the frame after it is
	// a normal visible one, so a later event still animates instead of being
	// treated as a second show frame (ticket 08).
	windowHidden := c.WindowHidden()
	hidden := windowHidden || skipped
	wasHidden := m.wasHidden
	m.wasHidden = windowHidden
	width, height := c.Size()
	resized := m.haveSize && (width != m.width || height != m.height)
	if width > 0 && height > 0 {
		m.width, m.height = width, height
		m.haveSize = true
	}
	setting := s.Motion()
	reduce := c.Preferences().ReduceMotion
	now := c.Now()
	m.exit = !reduce && !hidden && !resized && motion.Enabled(setting, motion.TabClose)

	if m.run != nil {
		cut := hidden || resized || reduce || !motion.Enabled(setting, m.run.kind)
		switch {
		case cut:
			m.generation++
			m.run = nil
			m.warp = true
		case !now.Before(m.run.started.Add(m.run.duration)):
			m.run = nil
		}
	}

	event, hasEvent := s.MotionEvent()
	if !hasEvent || event.Seq <= m.lastSeq {
		if m.run != nil {
			m.animate = true
			m.animationFrames++
			c.AnimationFrame()
		}
		return
	}
	m.lastSeq = event.Seq
	switch event.Kind {
	case motion.TabOpen, motion.TabClose, motion.TabReorder:
		warped := first || hidden || wasHidden || resized || reduce ||
			!motion.Enabled(setting, event.Kind)
		if warped {
			if m.run != nil || event.Kind == motion.TabClose {
				m.generation++
			}
			m.run = nil
			m.warp = true
			return
		}
		if m.run != nil {
			// Latest event wins; the a new run owns the row from here.
			m.generation++
		}
		m.run = &quakeStripRun{
			kind:     event.Kind,
			started:  now,
			duration: motion.ScaleMotion(motion.TabStripBaseDuration, setting.Speed),
		}
		m.animate = true
	case motion.TabSwitch:
		// A switch never moves the bar; the selected fill changes in this
		// frame. Cancel any run still moving so it cannot slide under the new
		// selection.
		if m.run != nil {
			m.generation++
		}
		m.run = nil
		m.warp = true
		return
	}
	if m.animate {
		m.animationFrames++
		c.AnimationFrame()
	}
}

// transition is the Transition the bar attaches to one item this build.
func (m *quakeStripMotion) transition(setting domainpreferences.Motion) ui.ElementTransition {
	exitDuration := motion.ScaleMotion(motion.TabStripBaseDuration, setting.Speed)
	if m.run != nil {
		exitDuration = m.run.duration
	}
	switch {
	case m.animate:
		return motion.TabStripTransition(m.run.duration, exitDuration)
	case m.warp:
		return motion.TabStripTransition(-1, exitDuration)
	case m.exit:
		return motion.TabStripPassiveTransition(exitDuration)
	default:
		// The layout must follow at once, but a close record would animate a
		// removal under an off switch. Duration -1 is MyGo's warp, not the
		// zero-value 200 ms transition.
		return ui.ElementTransition{Duration: -1, Ease: ui.EaseOut, Position: true}
	}
}
