package motion

import (
	"time"

	"github.com/egoist/mygo/ui"

	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
)

// TabStripBaseDuration is a Tab strip item's standard duration for an enter,
// an exit and a reorder. The speed tier scales this time alone. The shell's
// work strip, Follow's mini strip and the Quake bar all use it, so the three
// strips cannot drift apart.
const TabStripBaseDuration = 180 * time.Millisecond

// TabStripTransition is an animated or warped strip item: Position and Size
// only, with a collapsing enter and exit. Duration -1 shows the change at
// once, MyGo's warp. Colors are deliberately untouched, so the selected fill
// changes on the same frame.
func TabStripTransition(duration, exitDuration time.Duration) ui.ElementTransition {
	return ui.ElementTransition{
		Duration:     duration,
		ExitDuration: exitDuration,
		Ease:         ui.EaseOut,
		ExitEase:     ui.EaseOut,
		Position:     true,
		Size:         true,
		Enter:        &ui.Motion{Collapse: true},
		Exit:         &ui.Motion{Collapse: true},
	}
}

// TabStripPassiveTransition is an idle Tab's recording: its layout follows the
// frame at once, while a close copy leaves with the strip's duration. Position
// is set (and only Position) so MyGo does not fall back to animating every
// property, Colors included, with its 200 ms default.
func TabStripPassiveTransition(exitDuration time.Duration) ui.ElementTransition {
	return ui.ElementTransition{
		Duration:     -1,
		ExitDuration: exitDuration,
		Ease:         ui.EaseOut,
		ExitEase:     ui.EaseOut,
		Position:     true,
		Exit:         &ui.Motion{Collapse: true},
	}
}

// FollowStripTransition is the mini strip's animated or warped item: Position
// alone at the strip's duration, sliding in and out sideways. It never
// animates Size, so an entering mini Tab takes its final width on the first
// frame; only its place moves. Opacity stays 1 so the selected fill is the
// element's own from the first frame.
func FollowStripTransition(duration, exitDuration time.Duration) ui.ElementTransition {
	return ui.ElementTransition{
		Duration:     duration,
		ExitDuration: exitDuration,
		Ease:         ui.EaseOut,
		ExitEase:     ui.EaseOut,
		Position:     true,
		Enter:        &ui.Motion{XPercent: 100, Opacity: 1},
		Exit:         &ui.Motion{XPercent: -100, Opacity: 1},
	}
}

// FollowStripPassiveTransition is an idle mini Tab's recording: its layout
// follows at once, and a close copy slides out with the strip's duration.
func FollowStripPassiveTransition(exitDuration time.Duration) ui.ElementTransition {
	return ui.ElementTransition{
		Duration:     -1,
		ExitDuration: exitDuration,
		Ease:         ui.EaseOut,
		ExitEase:     ui.EaseOut,
		Position:     true,
		Exit:         &ui.Motion{XPercent: -100, Opacity: 1},
	}
}

// BodyOpenMotion is a whole Tab body's entrance for an open style. slide and
// gnomed must say opacity 1: MyGo's Motion opacity defaults to 0.
func BodyOpenMotion(style domainpreferences.MotionStyle) ui.Motion {
	switch style {
	case domainpreferences.MotionSlide:
		return ui.Motion{XPercent: -100, Opacity: 1}
	case domainpreferences.MotionGnomed:
		return ui.Motion{YPercent: 100, Collapse: true, Opacity: 1}
	case domainpreferences.MotionFade:
		return ui.Motion{Opacity: 0}
	default:
		return ui.Motion{Scale: PopinStartScale, Opacity: 0}
	}
}

// BodyCloseMotion is a whole Tab body's exit for a close style. It is the same
// geometry read from the body's shown box: popin shrinks to 87%, slide leaves
// left by its own width, gnomed folds to its bottom edge, fade keeps its
// geometry.
func BodyCloseMotion(style domainpreferences.MotionStyle) ui.Motion {
	switch style {
	case domainpreferences.MotionSlide:
		return ui.Motion{XPercent: -100, Opacity: 1}
	case domainpreferences.MotionGnomed:
		return ui.Motion{YPercent: 100, Collapse: true, Opacity: 1}
	case domainpreferences.MotionFade:
		return ui.Motion{Opacity: 0}
	default:
		return ui.Motion{Scale: PopinStartScale, Opacity: 0}
	}
}

// BodySwitchMotions is the incoming and outgoing body motion for a switch
// style. dir is +1 when the target Tab is later in the strip (the incoming
// body comes from the right and the outgoing leaves to the left) and -1 when
// it is earlier. slide and slidevert stay opaque; the fade variants use MyGo's
// default opacity 0 on both ends, and fade moves nothing.
func BodySwitchMotions(style domainpreferences.MotionSwitchStyle, dir float32) (enter, exit ui.Motion) {
	switch style {
	case domainpreferences.MotionSwitchSlideVert:
		return ui.Motion{YPercent: 100 * dir, Opacity: 1}, ui.Motion{YPercent: -100 * dir, Opacity: 1}
	case domainpreferences.MotionSwitchSlideFade:
		return ui.Motion{XPercent: 100 * dir}, ui.Motion{XPercent: -100 * dir}
	case domainpreferences.MotionSwitchSlideFadeVert:
		return ui.Motion{YPercent: 100 * dir}, ui.Motion{YPercent: -100 * dir}
	case domainpreferences.MotionSwitchFade:
		return ui.Motion{}, ui.Motion{}
	default:
		return ui.Motion{XPercent: 100 * dir, Opacity: 1}, ui.Motion{XPercent: -100 * dir, Opacity: 1}
	}
}

// SwitchDirection turns a Tab event's index direction into a motion sign: +1
// for a target later in the strip, -1 for one before it.
func SwitchDirection(direction int) float32 {
	if direction > 0 {
		return 1
	}
	return -1
}
