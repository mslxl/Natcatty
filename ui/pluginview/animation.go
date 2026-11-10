package pluginview

// Host-driven property transitions. A node with transition_ms asks the host
// to animate changes to its place, size, and colors. MyGo lays out and paints
// every intermediate frame; the guest is never called to advance an
// animation. When the transition settles, the renderer queues one
// animation_end event for the node. Reduced motion turns every transition
// into an immediate jump and queues the same event in the frame the change
// first renders.

import (
	"strconv"
	"time"

	"github.com/egoist/mygo/ui"

	domainnativeui "github.com/mslxl/natcatty/domain/nativeui"
)

// animationState is the host's record of one node's transition. fingerprint
// covers the properties whose change starts a transition; running is true
// while the previous frame moved the node.
type animationState struct {
	fingerprint string
	running     bool
}

// applyTransition arms the element's host transition and reports its end. It
// must run after every style and size property has been applied.
func (s *Surface) applyTransition(node *domainnativeui.Node, element ui.Element) ui.Element {
	milliseconds := node.IntProp(domainnativeui.PropTransitionMs, 0)
	state := s.animations[node.ID]
	if milliseconds <= 0 {
		if state != nil {
			delete(s.animations, node.ID)
		}
		return element
	}
	if milliseconds > domainnativeui.MaxTransitionMs {
		milliseconds = domainnativeui.MaxTransitionMs
	}
	element = element.Transition(ui.ElementTransition{
		Duration: time.Duration(milliseconds) * time.Millisecond,
		Ease:     mygoEase(domainnativeui.Ease(node.IntProp(domainnativeui.PropTransitionEase, int64(domainnativeui.EaseOut)))),
		Position: true,
		Size:     true,
		Colors:   true,
	})
	fingerprint := animationTarget(node)
	if state == nil {
		state = &animationState{fingerprint: fingerprint}
		s.animations[node.ID] = state
	}
	changed := state.fingerprint != fingerprint
	state.fingerprint = fingerprint
	if changed {
		if s.frameEnv.ReduceMotion {
			// The desktop asked for less motion: MyGo jumps to the new
			// value, so the transition is already over.
			state.running = false
			s.emit(node.ID, domainnativeui.Event{Kind: domainnativeui.EventAnimationEnd})
			return element
		}
		state.running = true
		return element
	}
	if state.running && !element.Transitioning() {
		state.running = false
		s.emit(node.ID, domainnativeui.Event{Kind: domainnativeui.EventAnimationEnd})
	}
	return element
}

// animationTarget fingerprints the properties a guest visibly changes. It
// covers the size, the layout properties that move a node's box or its
// content, and the colors the transition interpolates. The host owns layout,
// so a sibling's change that only moves this node does not begin an animation
// the guest asked for.
func animationTarget(node *domainnativeui.Node) string {
	value := func(prop domainnativeui.PropID, def float64) string {
		return strconv.FormatFloat(node.FloatProp(prop, def), 'g', -1, 64)
	}
	integer := func(prop domainnativeui.PropID, def int64) string {
		return strconv.FormatInt(node.IntProp(prop, def), 10)
	}
	optionalInteger := func(prop domainnativeui.PropID) string {
		if raw, ok := node.Prop(prop); ok && raw.Kind == domainnativeui.PropInt {
			return strconv.FormatInt(raw.Int, 10)
		}
		return "-"
	}
	return value(domainnativeui.PropWidth, 0) + ":" +
		value(domainnativeui.PropHeight, 0) + ":" +
		value(domainnativeui.PropGrow, 0) + ":" +
		value(domainnativeui.PropMarginStart, 0) + ":" +
		value(domainnativeui.PropMarginEnd, 0) + ":" +
		value(domainnativeui.PropMarginTop, 0) + ":" +
		value(domainnativeui.PropMarginBottom, 0) + ":" +
		value(domainnativeui.PropPaddingStart, 0) + ":" +
		value(domainnativeui.PropPaddingEnd, 0) + ":" +
		value(domainnativeui.PropPaddingTop, 0) + ":" +
		value(domainnativeui.PropPaddingBottom, 0) + ":" +
		value(domainnativeui.PropFontSize, 0) + ":" +
		integer(domainnativeui.PropFontWeight, 0) + ":" +
		value(domainnativeui.PropOpacity, 1) + ":" +
		optionalInteger(domainnativeui.PropBackgroundToken) + ":" +
		optionalInteger(domainnativeui.PropBorderToken) + ":" +
		optionalInteger(domainnativeui.PropTextToken)
}

func mygoEase(ease domainnativeui.Ease) ui.Easing {
	switch ease {
	case domainnativeui.EaseLinear:
		return ui.Linear
	case domainnativeui.EaseIn:
		return ui.EaseIn
	case domainnativeui.EaseInOut:
		return ui.EaseInOut
	default:
		return ui.EaseOut
	}
}
