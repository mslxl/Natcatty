package shell

import (
	"slices"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/domain/model"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/motion"
	"github.com/mslxl/natcatty/ui/shell/state"
)

// The work Tab strip is its own motion surface. Ticket 19 animates the body;
// the strip's items enter, exit and make room at 180 ms × speed, with no
// color transition: the selected fill must change in the same frame
// (ticket 20, prototype/tab-motion). The body and strip runtimes read the
// same state event stream but keep separate bookkeeping, so a strip-only Tab
// reorder never starts a body run and a Tab switch never moves the strip.

// tabStripBaseDuration is the strip's standard duration for an enter, an
// exit and a reorder. The speed tier scales this time alone. It is the shared
// tab-strip duration so the work strip, Follow's mini strip and Quake's bar
// cannot drift apart.
const tabStripBaseDuration = motion.TabStripBaseDuration

// tabStripSignals is what a view knows about its strip before the build. The
// strip cannot see the layout this frame makes, so the view supplies the last
// built scroll offset and overflow: they decide whether a reveal or a scroll
// clamp would jump the items under a running transition, which must warp
// instead. A close's removal is predicted from the strip's own build-time row
// record, not from a signal (ticket 20).
type tabStripSignals struct {
	// revealActive asks the strip to scroll the active Tab into view this
	// frame.
	revealActive bool
	// overflow reports that the strip overflows now, or that this frame's
	// reveal will make it overflow.
	overflow bool
	// resized reports that the window width changed since the last build. The
	// whole row reflows and the reveal may clamp the scroll offset, so a
	// running enter, exit or reorder ends and this frame warps instead of
	// sliding under the jump. An event on a resize frame warps too: its final
	// layout is not the one the item started from.
	resized bool
	// scrollX and maxX are the strip's last built scroll offset and the
	// furthest it could scroll. A close that shrinks the content below
	// scrollX clamps the offset on this frame and jumps every item.
	scrollX float32
	maxX    float32
	// row is the row this build will draw, computed from the same state
	// snapshot the build renders: each Tab's id, title, marker and build-time
	// width bound, the Pending Connection labels, and the style and row gap.
	// Every strip that draws Tab items supplies it (the work strip, its
	// top-down route, and Follow's mini strip), so a run can only continue
	// over the row it started with, a reorder can only start on a pure
	// permutation of the committed row, and a removal the build omits is
	// suppressed on the frame it disappears. nil leaves those rules off: the
	// runtime probe drives the event gates without a layout.
	row *tabRowSnapshot
	// instant reports that no Pane or Tab body transition is moving in this
	// window this frame, so a strip warp may call DisableAnimations: the
	// switch is window-wide, and with another MyGo transition in flight it
	// would rest that too (the one-switch rule). A warp that cannot use it
	// renames the row instead, which loses a focused item control; the
	// runtime gives an equivalent control the focus back.
	instant bool
}

// tabRowItem is one Tab the last build drew, as the close-clamp prediction
// reads it. bound is an upper bound of the body width for the layout that
// build produced, computed from the title and marker it drew in that build's
// style; a measured LayoutBounds on the same pass still describes the layout
// before it, which is one rename or dirty change old (ticket 20).
type tabRowItem struct {
	id     model.TabID
	title  string
	marker string
	bound  float32
}

// tabRowSnapshot is the whole strip row one build drew: the Tabs in order with
// their width signatures, the Pending Connection labels, and the style and row
// gap the build laid them out with. A TabClose event may only be predicted
// from this row, and only while the state still describes exactly this row
// minus the closing Tab. Any other change in the same update, such as a
// detach, another close, a pending row finishing, or another Tab's rename,
// marker or style change, moves content the one-Tab subtraction cannot see, so
// the event warps instead (ticket 20).
type tabRowSnapshot struct {
	valid   bool
	style   string
	gap     float32
	tabs    []tabRowItem
	pending []string
}

// tabStripRun is one strip animation: a Tab opened, closed or reordered. It
// keeps the duration it started with, so changing the speed tier mid-run does
// not retime the visible movement (ticket 08); the next event reads the new
// tier.
type tabStripRun struct {
	kind     motion.Kind
	started  time.Time
	duration time.Duration
	// moved is the Tab a TabReorder carries over its siblings. Only that run
	// paints it above them; an enter or an exit keeps the normal order.
	moved model.TabID
	// revealPending marks a TabOpen whose enter started while its reveal was
	// still being laid out. The view's maximum-width prediction normally
	// warps such an open on its event frame; this is the backstop for an
	// overflow the prediction could not see. The next build checks the actual
	// overflow and warps if the reveal scrolled the strip: an enter under a
	// jumping scroll offset is not acceptable (ticket 20).
	revealPending bool
	// counted is how many build passes this run's enter was counted in. A
	// pending reveal cancelled on the build after it started was never shown
	// as a settled motion: those passes are taken back out of animationFrames.
	counted int
	// copy is how long the close copy this run makes moves for: the previous
	// build's exit duration, which can outlast the run when the speed tier
	// changed on the close frame or an earlier run armed it. Zero for an open
	// or a reorder, which leave no copy. The copy of a run still in flight is
	// reclaimed by the remount stopMotion takes for it; copy only arms the
	// ghost deadline when the run is retired without that remount (a natural
	// end or a replacement run).
	copy time.Duration
}

// tabStripMotion is one view's strip bookkeeping. A view owns one (the main
// window and the top-down route keep theirs in workTabReveal; Follow's mini
// strip keeps its own), so two visible renderers animate independently and a
// hidden one warps when it shows again.
type tabStripMotion struct {
	// frames counts the runtime frames this view built. The first frame has
	// no previous layout to leave from, so it never animates.
	frames int
	// wasHidden is the previous frame's window visibility. An event first
	// seen on the frame a window shows was not on screen and warps.
	wasHidden bool
	// skipped reports that the view built no strip on the previous frame (a
	// top-down browser placeholder or an unmounted route). The next build
	// treats the gap like a hidden frame and warps; it is a view-local signal,
	// never a wall-clock judgement.
	skipped bool
	// lastSeq is the last state event this view consumed.
	lastSeq uint64
	// run is the strip animation playing, or nil.
	run *tabStripRun
	// animate, warp and exit describe the Transition this build attaches.
	animate bool
	warp    bool
	exit    bool
	// positionOnly is Follow's mini strip: the transition moves Position
	// alone, so an entering or leaving item keeps its final width on every
	// frame and the mini strip never animates Size.
	positionOnly bool
	// warpSeq is the last BodyWarpSeq this runtime consumed. The state raises
	// it for a batch Tab close, which plays no motion event: the strip must
	// warp too, and a new row generation cancels the idle exit copies it had
	// armed for the removed items.
	warpSeq uint64
	// generation names the row that holds the items. A warp that has to
	// cancel something MyGo is already moving or copying raises it, so the
	// row becomes a new parent and every record under the old one is dropped:
	// an in-flight enter, exit copy or reorder stops at once without
	// DisableAnimations, which is window-wide and would rest a simultaneous
	// body or Pane transition. The Tab items themselves keep their stable
	// keys; only their parent's identity changes.
	generation int
	// ghostUntil is the latest time a close copy whose run was retired without
	// a remount can still be moving. MyGo's instant frame deletes only the
	// records whose copy has not been made yet
	// (third_party/mygo/ui/transition.go:913); an existing ghost keeps
	// sampling and asking for frames (:971-1005), so a rest warp cannot
	// reclaim it. A run in flight is remounted regardless (stopMotion); this
	// deadline covers the residual where a run ended or was replaced while
	// its copy outlives it: a speed tier changed on the close frame, or a
	// second close replaced the run with a shorter one. Until it passes,
	// stopMotion takes the remount path even with no run in flight. A close
	// run that its copy does not outlive arms no deadline; it is covered by
	// the in-flight remount alone.
	ghostUntil time.Time
	// lastExit is the exit duration the last build attached to its items, the
	// duration a copy made by the next build's removal will run with. A close
	// run starts at the new speed tier while MyGo copies from the previous
	// build's record, so ghostUntil takes the later of the two.
	lastExit time.Duration
	// widths is what the last build knew about the Tabs this view drew: the
	// width it measured. It is kept as the measured record, not as the
	// close-clamp prediction's input: LayoutBounds on a build describes the
	// layout before it, so a rename or dirty change built on the previous
	// frame leaves it one build old (ticket 20). row is that prediction's
	// input instead.
	widths map[model.TabID]float32
	// row is the row the last completed build drew: each Tab with the
	// title/marker/width bound that build laid out, the Pending Connection
	// labels, and the style and gap. A TabClose is predicted only from this
	// row and only while the state still describes exactly this row minus the
	// closing Tab; any other change in the same update warps. It is also the
	// baseline a running strip must still match: a detach, a rename, a dirty
	// marker or any other row change with no strip event ends the run and
	// warps (ticket 20).
	row tabRowSnapshot
	// current is the row this build will draw, filled by noteCurrentRow from
	// the state snapshot workTabs takes before beginFrame. The run rules
	// compare it with row: an active run may only continue while the two are
	// identical, and a TabReorder may only start on a permutation of row. It
	// is replaced every build before any rule reads it, so a warp or remount
	// needs to drop only row; a strip that supplies no row (Follow, the
	// runtime probe) leaves both invalid and the rules off.
	current tabRowSnapshot
	// building collects this pass's Tab records until workTabs commits them
	// as row. It is reset at the start of every build.
	building []tabRowItem
	// focusedTab and focusedControl are the Tab control the last build
	// reported as holding the keyboard focus. A warp that renames the row
	// loses that focus (MyGo keys it by element ID), so the runtime remembers
	// it, and the rebuilt row's same control takes it back: restoreTab and
	// restoreControl are that request, set only on a warp that remounts.
	focusedTab     model.TabID
	focusedControl kit.TabControlFocus
	restoreTab     model.TabID
	restoreControl kit.TabControlFocus
	// focusedAdd records whether the strip's add control held the keyboard
	// focus on the last build, and restoreAdd asks the rebuilt control to take
	// it back after a warp that renamed the row. The add control's stable key
	// keeps its identity through an at-rest warp.
	focusedAdd bool
	restoreAdd bool
	// animationFrames counts the build passes this runtime animated. A warp
	// asks for none; tests assert it.
	animationFrames int
}

// beginFrame consumes the latest state event before the strip is built. It
// starts a run for a visible, enabled Tab enter, exit or reorder; a Tab switch
// warps (the selected color alone changes); everything else leaves the strip
// resting. A batch close, a reveal that overflows an overflowing strip, a
// close that would clamp the strip's scroll, and a window resize all warp
// instead: the final layout shows on that frame instead of sliding under a
// jumping scroll offset.
func (m *tabStripMotion) beginFrame(c *ui.Context, s *state.State, sig tabStripSignals) {
	m.animate, m.warp, m.exit = false, false, false
	// A restore request only lasts the pass that set it: that pass rebuilds
	// the renamed row, and its same control takes the focus back at once.
	m.restoreTab, m.restoreControl, m.restoreAdd = "", kit.TabFocusNone, false
	// This pass records the row it draws; workTabs commits it at the end.
	m.building = m.building[:0]
	if c == nil || s == nil {
		return
	}
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
	setting := s.Motion()
	reduce := c.Preferences().ReduceMotion
	now := c.Now()
	// Read the event once: the run rules below need to know whether this frame
	// has an unseen strip event before the event switch consumes it.
	event, hasEvent := s.MotionEvent()
	unseen := hasEvent && event.Seq > m.lastSeq

	// A batch Tab close raises the body warp signal and records no motion
	// event (ticket 19). The strip must warp with it: the idle close exits it
	// armed last frame would otherwise animate every removed item. A warp at
	// rest rests MyGo's records where they are, so a focused item control
	// survives; with a Pane or body transition in flight the row takes a new
	// generation instead, which drops them (stopMotion).
	batchWarp := false
	if !first {
		if seq := s.BodyWarpSeq(); seq != m.warpSeq {
			m.warpSeq = seq
			batchWarp = true
			m.stopMotion(c, sig)
		}
	} else {
		m.warpSeq = s.BodyWarpSeq()
	}

	// An idle Tab keeps an exit on its last build, so the copy MyGo makes
	// when the Tab is gone goes as the close style. It is gated by the same
	// switch that gates the close event. The build's layout changes rest
	// instantly: only the exit animates.
	m.exit = !reduce && !hidden && motion.Enabled(setting, motion.TabClose)

	if m.run != nil {
		switch {
		case hidden || reduce || !motion.Enabled(setting, m.run.kind) ||
			!now.Before(m.run.started.Add(m.run.duration)):
			// A hidden window, Reduce Motion, or the event's switch going off
			// ends the run. A natural end needs no warp; an early one makes
			// this frame final and cancels what MyGo was moving. The cut cannot
			// use a window-wide instant frame, which would also rest a Pane or
			// body transition still playing under its own switch: the row takes
			// a new generation instead, and a focused control is given back to
			// the rebuilt row.
			cut := hidden || reduce || !motion.Enabled(setting, m.run.kind)
			if cut {
				m.rememberFocus()
				m.generation++
				m.dropRowRecords()
				// The remount drops the old row's children and every close
				// copy among them; no deadline is left to reclaim.
				m.ghostUntil = time.Time{}
			} else {
				// A natural end keeps the copy MyGo is playing. If that copy
				// was armed longer than the run, remember when it is done.
				m.retireRun(m.run)
			}
			m.run = nil
			if cut {
				m.warp = true
			}
		}
	}

	// A resize reflows the row and can clamp the strip's scroll offset under a
	// running enter, exit or reorder: every item would slide under a jump of
	// its own. End the run and show the final layout on this frame, under the
	// same instant/remount rules as every other warp. A close copy still
	// moving after its run ended is reclaimed by the same warp.
	if (m.run != nil || m.ghostMayMove(c)) && sig.resized {
		m.stopMotion(c, sig)
	}

	// A strip run may only continue over the row it started with. A detach, a
	// rename, a dirty marker, a Pending row change, or a style change records
	// no strip event of its own; the layout then shrinks or widens MaxX after
	// the run's event frame and the scroll clamp would slide every item under
	// the run without ending it. Compare the row this build will draw with the
	// committed one and warp instead. A frame whose unseen event is one of the
	// strip's own Tab events is left to that event's rule: an open's
	// state-derived bound, a close's exact row-minus-one prediction, a
	// reorder's permutation check, or a switch's warp. Changing a Tab's title
	// or marker during a 180 ms run therefore cancels it; over-warping on a
	// benign change is accepted so a clamp under a run is not.
	if m.run != nil && !stripEventKind(unseen, event) && sig.row != nil &&
		!sameTabRow(sig.row, &m.row) {
		m.stopMotion(c, sig)
	}

	if unseen {
		m.lastSeq = event.Seq
		switch event.Kind {
		case motion.TabOpen, motion.TabClose, motion.TabReorder:
			// The leaving Tab's measured record is dropped before the build
			// stops drawing it; the prediction itself reads the build-time
			// bound snapshot, which can see a rename the measurement cannot.
			if event.Kind == motion.TabClose {
				delete(m.widths, event.TabID)
			}
			warped := first || hidden || wasHidden || reduce || sig.resized ||
				!motion.Enabled(setting, event.Kind) || batchWarp
			if !warped && event.Kind == motion.TabClose &&
				m.closeWouldClamp(c, s, sig, event.TabID) {
				warped = true
			}
			// A reorder may only animate a row that is the committed row in a
			// different order. A close, a detach, a rename, a dirty marker or a
			// Pending row change in the same update moves content the reorder's
			// layout cannot account for, and a removal's whole row contribution
			// tightens MaxX under the slide. The close clamp's one-Tab
			// subtraction cannot see a row that also reorders, so a permutation
			// is the general gate: same Tabs, same titles, markers, bounds,
			// Pending labels, style and gap, only their order may differ. No
			// committed row, or no current row, warps.
			if !warped && event.Kind == motion.TabReorder && sig.row != nil &&
				!permutationOfRow(sig.row, &m.row) {
				warped = true
			}
			if !warped && sig.revealActive && sig.overflow {
				warped = true
			}
			if warped {
				// A close warped on its event frame must not leave the copy
				// its idle transition armed; any run in flight must stop too,
				// as must a close copy still moving after its run ended.
				if m.run != nil || m.ghostMayMove(c) || event.Kind == motion.TabClose {
					m.stopMotion(c, sig)
				}
				m.run = nil
				m.warp = true
			} else {
				run := &tabStripRun{
					kind:     event.Kind,
					started:  now,
					duration: motion.ScaleMotion(tabStripBaseDuration, setting.Speed),
					moved:    event.TabID,
					// A TabOpen's reveal is laid out after this build, so the
					// overflow it causes is not visible yet. The next build
					// reads the actual one and cancels the enter if the reveal
					// scrolled the strip (ticket 20).
					revealPending: event.Kind == motion.TabOpen && sig.revealActive,
				}
				// The copy this close makes runs with the previous build's
				// exit duration, which may be the old speed tier or a replaced
				// run's; this run ends at its own duration. While it plays, the
				// in-flight remount covers the copy; retiring the run records
				// the later end if the copy outlives it.
				if event.Kind == motion.TabClose {
					run.copy = run.duration
					if m.lastExit > run.copy {
						run.copy = m.lastExit
					}
				}
				// A live run replaced here will never reach its natural end;
				// account for a copy it left before the new run takes over.
				m.retireRun(m.run)
				m.run = run
			}
		case motion.TabSwitch:
			// A switch changes the selected color and the body, never the
			// strip's geometry. Warp so an earlier strip run cannot keep
			// sliding under the new selection. A run in flight had MyGo
			// moving the items toward the layout's target, which does not
			// change on a switch: a warp transition alone would not retarget
			// those channels, so the row takes a new generation and the
			// focused item control, if any, is given back to the rebuilt row.
			// A close copy still moving after its run ended needs the same
			// generation: a switch alone would not retarget it.
			if m.run != nil || m.ghostMayMove(c) {
				m.stopMotion(c, sig)
			}
			m.run = nil
			m.warp = true
		}
	}

	// An open started while its reveal was still being laid out. If the
	// layout it made overflows after all, the reveal scrolled the strip on
	// that frame: cancel the enter and show the final layout, with no strip
	// frame of its own, instead of sliding under the offset jump.
	if m.run != nil && m.run.revealPending && sig.overflow {
		m.animationFrames -= m.run.counted
		m.stopMotion(c, sig)
	}

	if hidden || reduce {
		// A run cut here, or a close copy still moving after its run ended,
		// stops at once: the hidden or still frame must not keep sampling it.
		if m.run != nil || m.ghostMayMove(c) {
			m.rememberFocus()
			m.generation++
			m.dropRowRecords()
			m.ghostUntil = time.Time{}
		}
		m.run = nil
		m.warp = true
	}
	// A removal must never replay an exit armed for a live Tab. MyGo copies an
	// element that stops being built from the transition of its last build,
	// and every build now attaches a positive exit, so the frame a Tab
	// disappears is the only place to suppress the copy. Exactly one shape may
	// paint it: the unseen TabClose event of the removed Tab, with the close
	// switch on and a close run started this frame. A detach, a batch or
	// restore row, a switch/open/reorder sharing the update with a detach, or a
	// close under an off switch, Reduce Motion, a hidden window, the first
	// frame, a clamp or reveal warp, a stale target or several removals must
	// instead end the strip's motion on the frame the Tab disappears. The
	// guard is what lets an off build keep a warm exit: the record exists for a
	// switch turned on together with a later close, but nothing paints it until
	// a legitimate close consumes it.
	//
	// stopMotion drops the committed row, so a removal guard after a row rule
	// that already warped this frame sees no row and does not remount twice; a
	// legitimate close is left to its run.
	if sig.row != nil {
		if removed := removedTabIDs(sig.row, &m.row); len(removed) > 0 {
			legitimateClose := unseen && event.Kind == motion.TabClose && m.exit &&
				!m.warp && !batchWarp &&
				m.run != nil && m.run.kind == motion.TabClose && m.run.moved == event.TabID &&
				len(removed) == 1 && removed[0] == event.TabID
			if !legitimateClose {
				m.stopMotion(c, sig)
			}
		}
	}
	m.animate = m.run != nil
	if m.animate {
		m.animationFrames++
		m.run.counted++
	}
}

// stripEventKind reports whether the frame's unseen event is one the strip's
// own switch handles: a Tab open, close, reorder or switch. Those events
// account for the row change they make (an open's state-derived bound, a
// close's exact row-minus-one prediction, a reorder's permutation check, a
// switch's warp), so the general row-consistency rule stands aside for them.
// Every other event, or no event, leaves a running strip to the row check.
func stripEventKind(unseen bool, event motion.Event) bool {
	if !unseen {
		return false
	}
	switch event.Kind {
	case motion.TabOpen, motion.TabClose, motion.TabReorder, motion.TabSwitch:
		return true
	}
	return false
}

// sameTabRow reports that two rows describe the same row in the same order:
// the same Tabs (id, title, marker and build-time width bound), the same
// Pending Connection labels, and the same style and row gap. A running strip
// may continue only while this holds; any difference means the layout moved
// content after the run's event frame, so the run ends and the frame warps.
func sameTabRow(current, committed *tabRowSnapshot) bool {
	if current == nil || committed == nil || !current.valid || !committed.valid {
		return false
	}
	return current.style == committed.style && current.gap == committed.gap &&
		slices.Equal(current.pending, committed.pending) &&
		slices.Equal(current.tabs, committed.tabs)
}

// removedTabIDs returns the Tabs the committed row drew that current no longer
// draws, in committed order. Only identities are compared: a rename, a marker
// change, a width change, a reorder or a Pending row change leaves every Tab
// built, and a built Tab refreshes its own record. A missing Tab is the one
// shape a build cannot refresh, and a detach records no strip event to warp it.
func removedTabIDs(current, committed *tabRowSnapshot) []model.TabID {
	if current == nil || committed == nil || !current.valid || !committed.valid {
		return nil
	}
	present := make(map[model.TabID]struct{}, len(current.tabs))
	for _, item := range current.tabs {
		present[item.id] = struct{}{}
	}
	var removed []model.TabID
	for _, item := range committed.tabs {
		if _, ok := present[item.id]; !ok {
			removed = append(removed, item.id)
		}
	}
	return removed
}

// permutationOfRow reports that current holds exactly the Tabs committed holds
// in a different order: the same multiset of id, title, marker and build-time
// width bound, with the same Pending Connection labels, style and row gap. A
// TabReorder may animate only such a row. A close, a detach, a rename, a dirty
// marker, a Pending row change, or an open in the same update makes the rows
// differ beyond order, and the reorder's layout cannot account for it: the
// removed Tab's whole row contribution tightens the maximum under the slide,
// so the event warps. An unknown row on either side is not a permutation.
func permutationOfRow(current, committed *tabRowSnapshot) bool {
	if current == nil || committed == nil || !current.valid || !committed.valid {
		return false
	}
	if current.style != committed.style || current.gap != committed.gap ||
		!slices.Equal(current.pending, committed.pending) ||
		len(current.tabs) != len(committed.tabs) {
		return false
	}
	remaining := make(map[model.TabID]tabRowItem, len(committed.tabs))
	for _, item := range committed.tabs {
		if _, duplicate := remaining[item.id]; duplicate {
			return false
		}
		remaining[item.id] = item
	}
	for _, item := range current.tabs {
		was, ok := remaining[item.id]
		if !ok || was.title != item.title || was.marker != item.marker || was.bound != item.bound {
			return false
		}
		delete(remaining, item.id)
	}
	return len(remaining) == 0
}

// noteUnrendered records that this view built no strip on the frame it just
// drew. The next beginFrame warps the gap instead of animating an event the
// user could not see, and asks for no frame of its own.
func (m *tabStripMotion) noteUnrendered() {
	if m != nil {
		m.skipped = true
	}
}

// stopMotion ends whatever the strip is moving so this frame builds the final
// layout. At rest, DisableAnimations rests MyGo's records in place, and the
// items keep their element IDs, so a focused control stays focused. MyGo's
// instant frame, however, deletes only the records whose copy has not been
// made yet: a close copy MyGo already started keeps sampling and asking for
// frames (third_party/mygo/ui/transition.go:913, :971-1005). A run in flight
// therefore never takes the instant path, even when no body or Pane
// transition is moving: the row takes a new generation, which drops the old
// row's children and their ghosts. The same remount covers the residual where
// a run already ended but its copy still moves (ghostUntil). With a Pane or
// body transition in flight the switch is window-wide and would rest that too
// (the one-switch rule), so that case remounts as well. Either way the
// committed row is forgotten, so a later rule that stops the same frame's
// motion sees no row to compare and does not remount twice. The build commits
// the new row at its end.
func (m *tabStripMotion) stopMotion(c *ui.Context, sig tabStripSignals) {
	hadRun := m.run != nil
	m.run = nil
	m.warp = true
	m.dropRowRecords()
	if sig.instant && !hadRun && !m.ghostMayMove(c) {
		c.DisableAnimations()
		return
	}
	m.rememberFocus()
	m.generation++
	// The remount drops the old row's children and their copies; nothing is
	// left for the deadline to reclaim.
	m.ghostUntil = time.Time{}
}

// retireRun accounts for a run that ends without a remount, naturally or by
// being replaced: its close copy keeps moving independently of the run, so
// record when that copy is done. A copy that ends with its run leaves a
// deadline that has already passed by the time it matters, and a run that
// leaves no copy records nothing. The copy of a run still in flight is
// covered by the remount stopMotion takes for it and needs no deadline.
func (m *tabStripMotion) retireRun(run *tabStripRun) {
	if run == nil || run.copy <= 0 {
		return
	}
	if until := run.started.Add(run.copy); until.After(m.ghostUntil) {
		m.ghostUntil = until
	}
}

// ghostMayMove reports that a close copy whose run was retired may still be
// moving: MyGo keeps sampling a record that already holds a ghost, even when
// its strip run has ended.
func (m *tabStripMotion) ghostMayMove(c *ui.Context) bool {
	return c != nil && c.Now().Before(m.ghostUntil)
}

// dropRowRecords forgets the build-time row snapshot. A remounted row (a new
// generation) starts a new record on its own build; until that build commits
// one, a close prediction has no row to trust and warps. The measured widths
// stay: they are the observable record of what each build laid out, not an
// input to any prediction, so a later build reading them sees the last
// measurement of a Tab the row no longer draws, which the open forecast
// deliberately does not use (ticket 20).
func (m *tabStripMotion) dropRowRecords() {
	m.row = tabRowSnapshot{}
}

// rememberFocus records the focused item control, or the add control, for the
// rebuilt row, if one held the keyboard focus.
func (m *tabStripMotion) rememberFocus() {
	m.restoreAdd = m.focusedAdd
	if m.focusedTab == "" || m.focusedControl == kit.TabFocusNone {
		return
	}
	m.restoreTab, m.restoreControl = m.focusedTab, m.focusedControl
}

// focusAdd reports whether the rebuilt add control must take the keyboard
// focus back after a row remount.
func (m *tabStripMotion) focusAdd() bool { return m.restoreAdd }

// noteAddFocus records whether the strip's add control holds the keyboard
// focus, so a later remount can give it back.
func (m *tabStripMotion) noteAddFocus(focused bool) { m.focusedAdd = focused }

// focusControl names the control Tab id must take the focus back into after a
// row remount, or TabFocusNone.
func (m *tabStripMotion) focusControl(id model.TabID) kit.TabControlFocus {
	if id == "" || id != m.restoreTab {
		return kit.TabFocusNone
	}
	return m.restoreControl
}

// noteFocus records which of a built Tab's controls holds the keyboard focus,
// so a later remount can give that control the focus back.
func (m *tabStripMotion) noteFocus(id model.TabID, tab kit.TabResult) {
	switch {
	case tab.MenuFocused:
		m.focusedTab, m.focusedControl = id, kit.TabFocusMenu
	case tab.CloseFocused:
		m.focusedTab, m.focusedControl = id, kit.TabFocusClose
	case id == m.focusedTab:
		m.focusedTab, m.focusedControl = "", kit.TabFocusNone
	}
}

// tabStripClampGuard is how far below a predicted maximum an offset must be
// before a close may animate. The layout clamps an offset only above the new
// maximum, so an offset within the guard of it is treated as a clamp risk: an
// offset of postMax+0.25 started a TabClose run while the layout clamped
// (ticket 20). Over-warping at or near equality is accepted; a clamp under an
// animation is not.
const tabStripClampGuard float32 = 0.5

// closeWouldClamp reports that a close of the Tab closing would shrink the
// strip's content under its current scroll offset. The layout then clamps the
// offset on the close frame and every item jumps, so the event must warp
// rather than animate.
//
// The removal is the Tab's whole row contribution, not just its body:
// tabRowRemoveWidth charges the body, both insertion placeholders and the
// three row gaps the close takes with its three children. Subtracting only the
// body and one gap undercounted the reduction by
// 2*tabInsertionStripWidth+2*WorkTabGap (4 DIP in Classic, 12 in macOS), so a
// background Tab closed just short of the end slid every item under the clamp
// (ticket 20).
//
// The body width is the upper bound the last build recorded for the layout it
// produced, not a measured LayoutBounds one build old: a Tab renamed or made
// dirty on the previous frame is already wider there, and a stale measured
// width undercounts the removal (ticket 20). The bound may overstate the
// removal, which only ever warps a close that would not have clamped.
// closeRemovalBound returns no bound when the current row is not exactly the
// last drawn row minus the closing Tab; that includes another Tab appearing,
// leaving or changing width in the same update, a Pending Connection row
// change, and a style or gap change. The one-Tab subtraction cannot see those,
// so the event warps too.
//
// Follow's mini strip stays out of this prediction: it is Position-only,
// passes no scroll offset or maximum from its own layout (FollowBar's signals
// carry the row alone), and it has no close control of its own; the mini
// strip's documented motion stays Position-only. A scrolling mini strip is a
// documented follow-up, not this ticket's change.
func (m *tabStripMotion) closeWouldClamp(c *ui.Context, s *state.State, sig tabStripSignals, closing model.TabID) bool {
	if m.positionOnly || sig.maxX <= 0 || sig.scrollX <= 0 {
		// Removing content cannot increase the maximum, so a strip that does
		// not overflow at all, or an offset that is already exactly at its
		// start, cannot be clamped. A small positive offset is not exempt: a
		// close can shrink a barely overflowing row to a zero maximum and
		// clamp that offset to the start, moving every item by up to half a
		// DIP under the exit (ticket 20).
		return false
	}
	bound, ok := m.closeRemovalBound(c, s, closing)
	if !ok {
		// The row the last build drew is unknown, or this update changes it
		// beyond the closing Tab: the prediction cannot see the whole shrink.
		return true
	}
	newMax := sig.maxX - tabRowRemoveWidth(c, bound)
	// The layout clamps the offset to [0, newMax]; a removal that shrinks the
	// content below the viewport has a zero maximum, not a negative one.
	if newMax < 0 {
		newMax = 0
	}
	return sig.scrollX > newMax-tabStripClampGuard
}

// closeRemovalBound returns the build-time width bound of closing in the row
// the last build drew, and reports whether the state still describes exactly
// that row minus closing. The row must have the same order and ids, every
// survivor the same title, marker and bound, the same Pending Connection
// labels, and the same style and gap; otherwise the close frame moves content
// next to the removed Tab that the subtraction cannot see.
//
// The closing Tab is read from the snapshot, not the state: by the time the
// view consumes the TabClose, the state no longer holds it, and the snapshot
// is exactly the body the last build laid out for it. A rename or dirty change
// made after that build does not change the removal the close itself takes.
func (m *tabStripMotion) closeRemovalBound(c *ui.Context, s *state.State, closing model.TabID) (float32, bool) {
	if !m.row.valid || s == nil || closing == "" {
		return 0, false
	}
	if m.row.style != kit.CurrentStyle().ID() || m.row.gap != kit.WorkTabGap(c) {
		return 0, false
	}
	if !slices.Equal(m.row.pending, s.PendingConnections()) {
		return 0, false
	}
	messages := locales.In(c.Locale())
	index := 0
	var bound float32
	found := false
	for _, drawn := range m.row.tabs {
		if drawn.id == closing {
			if found {
				return 0, false
			}
			found = true
			bound = drawn.bound
			continue
		}
		if index >= s.TabCount() {
			return 0, false
		}
		info, ok := s.TabInfo(index)
		if !ok {
			return 0, false
		}
		title, marker := tabTitle(messages, info), tabMarker(messages, info)
		if info.ID != drawn.id || title != drawn.title || marker != drawn.marker ||
			kit.WorkTabWidthBound(c, title, marker) != drawn.bound {
			return 0, false
		}
		index++
	}
	if !found || index != s.TabCount() || bound <= 0 {
		return 0, false
	}
	return bound, true
}

// noteCurrentRow records the row this build will draw from the state snapshot
// workTabs takes before beginFrame: every Tab it renders with the title,
// marker and build-time width bound this build lays out, the Pending
// Connection labels, and the style and row gap. The run rules compare it with
// the committed row: identical rows let a run continue, and a reorder may only
// start on a permutation of the committed row. Building from the same snapshot
// the build renders keeps the two in step when a drop mutates the live order
// mid-pass; the bounds are computed with the same functions tabBody uses, so
// a steady row compares exactly equal.
func (m *tabStripMotion) noteCurrentRow(c *ui.Context, messages locales.Messages, entries []tabEntry, pending []string) {
	row := &m.current
	row.valid = true
	row.style = kit.CurrentStyle().ID()
	row.gap = kit.WorkTabGap(c)
	row.tabs = row.tabs[:0]
	for _, entry := range entries {
		title, marker := tabTitle(messages, entry.info), tabMarker(messages, entry.info)
		row.tabs = append(row.tabs, tabRowItem{
			id:     entry.info.ID,
			title:  title,
			marker: marker,
			bound:  kit.WorkTabWidthBound(c, title, marker),
		})
	}
	row.pending = append(row.pending[:0], pending...)
}

// noteTabRow records one Tab this pass draws: the title and marker the build
// laid out and the upper bound of the body width for that layout. workTabs
// commits the pass as row once the build returns, so a later close predicts
// from the layout that build produced rather than the one before it.
func (m *tabStripMotion) noteTabRow(id model.TabID, title, marker string, bound float32) {
	if id == "" || bound <= 0 {
		return
	}
	m.building = append(m.building, tabRowItem{id: id, title: title, marker: marker, bound: bound})
}

// commitRow stores the row this build drew with the style and gap it used. A
// close may only be predicted from it while the state still describes exactly
// this row minus the closing Tab.
func (m *tabStripMotion) commitRow(c *ui.Context, pending []string) {
	// Reuse the snapshot's slices: a build runs every frame, and nothing keeps
	// a copy of the row beyond the next commit, which happens after any close
	// decision for the frame has already read it.
	row := &m.row
	row.valid = true
	row.style = kit.CurrentStyle().ID()
	row.gap = kit.WorkTabGap(c)
	row.tabs = append(row.tabs[:0], m.building...)
	row.pending = append(row.pending[:0], pending...)
}

// noteWidth records a built item's measured width. It is the observable
// measurement the strip's tests read, not the close-clamp prediction's input:
// LayoutBounds on a build describes the layout before it (ticket 20).
func (m *tabStripMotion) noteWidth(id model.TabID, width float32) {
	if id == "" || width <= 0 {
		return
	}
	if m.widths == nil {
		m.widths = map[model.TabID]float32{}
	}
	m.widths[id] = width
}

// transition is the ElementTransition of one Tab item for this build. The
// strip moves Position and Size only: never Colors, so workTabFill's
// immediate selected color is not faded. A Tab under the pointer keeps its
// layout instant and its exit, so the drag copy follows the pointer without a
// competing Position transition. Follow's mini strip slides with Position
// alone.
//
// Every build attaches a positive ExitDuration, whatever the switches say:
// MyGo copies a disappearing element from the transition of its last build,
// and the frame that removes the Tab builds no item for it. An off build must
// therefore leave an exit record the next update can use when a switch turns
// on together with the close (ticket 20's re-enable regression). Only a
// legitimate animated close paints that record: the removal guard suppresses
// every other disappearance on the frame it happens, so a warm record on an
// off build shows nothing and asks for no frame by itself. The layout Duration
// is -1 for idle and off builds (this build's layout shows at once) and the
// run's for an animated build; a warp shows its layout at once through -1.
func (m *tabStripMotion) transition(setting domainpreferences.Motion, dragging bool) (ui.ElementTransition, bool) {
	exitDuration := motion.ScaleMotion(tabStripBaseDuration, setting.Speed)
	if m.run != nil {
		exitDuration = m.run.duration
	}
	// The next build's removal copies this build's record for its exit
	// duration; remember it for ghostUntil.
	m.lastExit = exitDuration
	switch {
	case dragging:
		return m.passiveTransition(exitDuration), true
	case m.animate:
		return m.activeTransition(m.run.duration, m.run.duration), true
	case m.warp:
		return m.activeTransition(-1, exitDuration), true
	default:
		return m.passiveTransition(exitDuration), true
	}
}

// activeTransition is the transition this strip attaches to an animated or
// warped item.
func (m *tabStripMotion) activeTransition(duration, exitDuration time.Duration) ui.ElementTransition {
	if m.positionOnly {
		return followStripTransition(duration, exitDuration)
	}
	return tabStripTransition(duration, exitDuration)
}

// passiveTransition is the idle recording: the item's layout follows the
// frame at once, while its close copy leaves with the strip's duration.
func (m *tabStripMotion) passiveTransition(exitDuration time.Duration) ui.ElementTransition {
	if m.positionOnly {
		return followStripPassiveTransition(exitDuration)
	}
	return tabStripPassiveTransition(exitDuration)
}

// zIndex is the paint order of one Tab item. The dragged item and the Tab a
// reorder is moving paint above their siblings, so the movement does not pass
// under them (prototype/tab-motion's contact-reorder-z).
func (m *tabStripMotion) zIndex(tab model.TabID, dragging bool) int {
	if dragging {
		return 1
	}
	if m.run != nil && m.run.kind == motion.TabReorder && m.run.moved == tab {
		return 1
	}
	return 0
}

// tabStripTransition is an animated or warped strip item: Position and Size
// only, with a collapsing enter and exit. Duration -1 shows the change at
// once, MyGo's warp.
func tabStripTransition(duration, exitDuration time.Duration) ui.ElementTransition {
	return motion.TabStripTransition(duration, exitDuration)
}

// tabStripPassiveTransition is an idle Tab's recording: its layout follows
// the frame at once, while a close copy leaves with the strip's duration.
// Position is set (and only Position) so MyGo does not fall back to animating
// every property, Colors included, with its 200 ms default.
func tabStripPassiveTransition(exitDuration time.Duration) ui.ElementTransition {
	return motion.TabStripPassiveTransition(exitDuration)
}

// followStripTransition is the mini strip's animated or warped item: Position
// alone at the strip's duration, sliding in and out sideways. It never
// animates Size, so an entering mini Tab takes its final width on the first
// frame; only its place moves. Opacity stays 1 so the selected fill is the
// element's own from the first frame.
func followStripTransition(duration, exitDuration time.Duration) ui.ElementTransition {
	return motion.FollowStripTransition(duration, exitDuration)
}

// followStripPassiveTransition is an idle mini Tab's recording: its layout
// follows at once, and a close copy slides out with the strip's duration.
func followStripPassiveTransition(exitDuration time.Duration) ui.ElementTransition {
	return motion.FollowStripPassiveTransition(exitDuration)
}
