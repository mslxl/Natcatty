// SPDX-License-Identifier: GPL-3.0-or-later

package terminal

import (
	"fmt"
	"time"
)

// Clock returns milliseconds on a monotonic timebase, like stock mosh's
// timestamp(). Tests can substitute a deterministic clock.
type Clock func() uint64

var processStart = time.Now()

// MonotonicMillis is the default overlay clock.
func MonotonicMillis() uint64 {
	return uint64(time.Since(processStart) / time.Millisecond)
}

// MonotonicMillisAt converts a wall-clock timestamp to the MonotonicMillis
// timebase. Stock's transport state stores timestamps from the same clock the
// overlay engine uses, so the conversion is numeric identity there; in Go the
// transport keeps time.Time values and the overlay keeps process-relative
// milliseconds. A zero or pre-start time has no in-process meaning and maps to
// zero.
func MonotonicMillisAt(at time.Time) uint64 {
	if at.IsZero() {
		return 0
	}
	elapsed := at.Sub(processStart)
	if elapsed <= 0 {
		return 0
	}
	return uint64(elapsed / time.Millisecond)
}

// Validity is Overlay::Validity: the state of one prediction.
type Validity uint8

const (
	ValidityPending Validity = iota
	ValidityCorrect
	ValidityCorrectNoCredit
	ValidityIncorrectOrExpired
	ValidityInactive
)

// DisplayPreference is PredictionEngine::DisplayPreference.
type DisplayPreference uint8

const (
	DisplayAlways DisplayPreference = iota
	DisplayNever
	DisplayAdaptive
	DisplayExperimental
)

// PredictionEngine is the stock prediction overlay's public seam. Ticket 30
// implements speculative cells, cursor moves, confirmation, and EchoAck
// handling; ticket 29 establishes the overlay order and leaves this engine
// inert so it cannot perturb terminal parity.
//
// The overlay manager deliberately calls Cull and Apply in stock's order, so
// ticket 30 can fill in the engine without touching the Session or renderer
// pipeline.
type PredictionEngine struct {
	displayPreference DisplayPreference
	predictOverwrite  bool
	sendInterval      uint
	localFrameSent    uint64
	localFrameAcked   uint64
	localFrameLateAck uint64
}

// NewPredictionEngine returns an engine with stock defaults: adaptive display
// and overwrite prediction disabled.
func NewPredictionEngine() *PredictionEngine {
	return &PredictionEngine{displayPreference: DisplayAdaptive}
}

// SetDisplayPreference selects always, never, adaptive, or experimental.
func (p *PredictionEngine) SetDisplayPreference(preference DisplayPreference) {
	p.displayPreference = preference
}

// DisplayPreference returns the selected mode.
func (p *PredictionEngine) DisplayPreference() DisplayPreference {
	return p.displayPreference
}

// SetPredictOverwrite sets stock's overwrite-prediction option.
func (p *PredictionEngine) SetPredictOverwrite(overwrite bool) {
	p.predictOverwrite = overwrite
}

// PredictOverwrite reports stock's overwrite-prediction option.
func (p *PredictionEngine) PredictOverwrite() bool { return p.predictOverwrite }

// SetLocalFrameSent records the last input frame sent.
func (p *PredictionEngine) SetLocalFrameSent(frame uint64) { p.localFrameSent = frame }

// SetLocalFrameAcked records the last input frame acknowledged.
func (p *PredictionEngine) SetLocalFrameAcked(frame uint64) { p.localFrameAcked = frame }

// SetLocalFrameLateAcked records the EchoAck from the latest remote state.
func (p *PredictionEngine) SetLocalFrameLateAcked(frame uint64) { p.localFrameLateAck = frame }

// SetSendInterval records the transport's current send interval in
// milliseconds.
func (p *PredictionEngine) SetSendInterval(interval uint) { p.sendInterval = interval }

// Cull discards invalid predictions. Inert until ticket 30.
func (p *PredictionEngine) Cull(*Framebuffer) {}

// Apply draws prediction overlays. Inert until ticket 30.
func (p *PredictionEngine) Apply(*Framebuffer) {}

// NewUserByte feeds a user keystroke to the predictor. Inert until ticket 30.
func (p *PredictionEngine) NewUserByte(byte, *Framebuffer) {}

// Reset drops all predictions. Inert until ticket 30.
func (p *PredictionEngine) Reset() {}

// WaitTime returns stock's prediction wakeup interval. Inert until ticket 30.
func (p *PredictionEngine) WaitTime() int { return int(^uint(0) >> 1) }

// Active reports whether any prediction is displayed.
func (p *PredictionEngine) Active() bool { return false }

// NotificationEngine is Overlay::NotificationEngine: the network-status bar at
// the top of the screen. The messages, timing, and drawing match stock mosh
// 1.4.0.
type NotificationEngine struct {
	clock Clock

	lastWordFromServer    uint64
	lastAckedState        uint64
	escapeKeyString       string
	message               []rune
	messageIsNetworkError bool
	messageExpiration     uint64
	showQuitKeystroke     bool
}

// NewNotificationEngine returns an engine whose network clocks start now.
func NewNotificationEngine(clock Clock) *NotificationEngine {
	if clock == nil {
		clock = MonotonicMillis
	}
	now := clock()
	return &NotificationEngine{
		clock:              clock,
		lastWordFromServer: now,
		lastAckedState:     now,
		messageExpiration:  ^uint64(0),
		showQuitKeystroke:  true,
	}
}

// SetClock replaces the engine's clock.
func (n *NotificationEngine) SetClock(clock Clock) {
	if clock == nil {
		clock = MonotonicMillis
	}
	n.clock = clock
}

// ServerHeard records the timestamp of the latest remote state.
func (n *NotificationEngine) ServerHeard(timestamp uint64) { n.lastWordFromServer = timestamp }

// ServerAcked records the timestamp of the server's latest acknowledgement.
func (n *NotificationEngine) ServerAcked(timestamp uint64) { n.lastAckedState = timestamp }

func (n *NotificationEngine) serverLate(ts uint64) bool {
	return ts-n.lastWordFromServer > 6500
}

func (n *NotificationEngine) replyLate(ts uint64) bool {
	return ts-n.lastAckedState > 10000
}

func (n *NotificationEngine) needCountup(ts uint64) bool {
	return n.serverLate(ts) || n.replyLate(ts)
}

// NotificationString returns the current message.
func (n *NotificationEngine) NotificationString() []rune {
	return append([]rune(nil), n.message...)
}

// SetNotificationString sets a message. A non-permanent message expires after
// one second. showQuitKeystroke appends the escape-key hint.
func (n *NotificationEngine) SetNotificationString(message []rune, permanent, showQuitKeystroke bool) {
	n.message = append([]rune(nil), message...)
	if permanent {
		n.messageExpiration = ^uint64(0)
	} else {
		n.messageExpiration = n.clock() + 1000
	}
	n.messageIsNetworkError = false
	n.showQuitKeystroke = showQuitKeystroke
}

// SetEscapeKeyString sets the quit hint shown in the bar.
func (n *NotificationEngine) SetEscapeKeyString(name string) {
	n.escapeKeyString = fmt.Sprintf(" [To quit: %s .]", name)
}

// SetNetworkError shows a network error message for one ack interval plus a
// short grace period.
func (n *NotificationEngine) SetNetworkError(message string) {
	n.message = []rune(message)
	n.messageIsNetworkError = true
	n.messageExpiration = n.clock() + 3000 + 100
}

// ClearNetworkError schedules a network-error message to disappear within one
// second.
func (n *NotificationEngine) ClearNetworkError() {
	if n.messageIsNetworkError {
		expiry := n.clock() + 1000
		if expiry < n.messageExpiration {
			n.messageExpiration = expiry
		}
	}
}

// AdjustMessage clears an expired message.
func (n *NotificationEngine) AdjustMessage() {
	if n.clock() >= n.messageExpiration {
		n.message = nil
	}
}

func humanReadableDuration(numSeconds int, secondsAbbr string) string {
	switch {
	case numSeconds < 60:
		return fmt.Sprintf("%d %s", numSeconds, secondsAbbr)
	case numSeconds < 3600:
		return fmt.Sprintf("%d:%02d", numSeconds/60, numSeconds%60)
	default:
		return fmt.Sprintf("%d:%02d:%02d", numSeconds/3600, (numSeconds/60)%60, numSeconds%60)
	}
}

// Apply formats and draws the bar exactly as stock does, including swprintf's
// 127-rune truncation.
func (n *NotificationEngine) Apply(fb *Framebuffer) {
	now := n.clock()
	timeExpired := n.needCountup(now)

	if len(n.message) == 0 && !timeExpired {
		return
	}

	// Hide the cursor if it would sit under the bar.
	if fb.DS.cursorRow == 0 {
		fb.DS.CursorVisible = false
	}

	// The bar spans the top row: white on blue.
	notificationBar := NewCell(0)
	notificationBar.Renditions.SetForegroundColor(7)
	notificationBar.Renditions.SetBackgroundColor(4)
	notificationBar.Append(' ')
	for i := 0; i < fb.DS.width; i++ {
		*fb.Cell(0, i) = notificationBar
	}

	sinceHeard := float64(now-n.lastWordFromServer) / 1000.0
	sinceAck := float64(now-n.lastAckedState) / 1000.0
	explanation := "contact"
	timeElapsed := sinceHeard
	if n.replyLate(now) && !n.serverLate(now) {
		timeElapsed = sinceAck
		explanation = "reply"
	}

	keystroke := ""
	if n.showQuitKeystroke {
		keystroke = n.escapeKeyString
	}

	var text string
	switch {
	case len(n.message) == 0 && timeExpired:
		text = fmt.Sprintf("mosh: Last %s %s ago.%s", explanation,
			humanReadableDuration(int(timeElapsed), "seconds"), keystroke)
	case len(n.message) != 0 && !timeExpired:
		text = fmt.Sprintf("mosh: %s%s", string(n.message), keystroke)
	default:
		text = fmt.Sprintf("mosh: %s (%s without %s.)%s", string(n.message),
			humanReadableDuration(int(timeElapsed), "s"), explanation, keystroke)
	}
	// swprintf(tmp, 128, ...) keeps at most 127 wide characters.
	runes := []rune(text)
	if len(runes) > 127 {
		runes = runes[:127]
	}

	overlayCol := 0
	combiningCell := fb.Cell(0, 0)
	for _, ch := range runes {
		if overlayCol >= fb.DS.width {
			break
		}
		chwidth := RuneWidth(ch)
		switch chwidth {
		case 1, 2:
			cell := fb.Cell(0, overlayCol)
			fb.ResetCell(cell)
			cell.Renditions.SetAttribute(AttrBold, true)
			cell.Renditions.SetForegroundColor(7)
			cell.Renditions.SetBackgroundColor(4)
			cell.Append(ch)
			cell.Wide = chwidth == 2
			combiningCell = cell
			overlayCol += chwidth
		case 0:
			if combiningCell == nil {
				continue
			}
			if combiningCell.Empty() {
				combiningCell.Fallback = true
				overlayCol++
			}
			if !combiningCell.Full() {
				combiningCell.Append(ch)
			}
		case -1:
			// Unprintable.
		}
	}
}

// WaitTime returns milliseconds until the next notification redraw, mirroring
// stock's unsigned arithmetic.
func (n *NotificationEngine) WaitTime() int {
	now := n.clock()
	nextExpiry := uint64(1<<31 - 1)
	if next := n.messageExpiration - now; next < nextExpiry {
		nextExpiry = next
	}
	if n.needCountup(now) {
		countupInterval := uint64(1000)
		if now-n.lastWordFromServer > 60000 {
			countupInterval = 3000
		}
		if countupInterval < nextExpiry {
			nextExpiry = countupInterval
		}
	}
	return int(nextExpiry)
}

// TitleEngine is Overlay::TitleEngine: it prepends a fixed prefix to the
// remote window title before display.
type TitleEngine struct {
	prefix []rune
}

// SetPrefix replaces the title prefix.
func (t *TitleEngine) SetPrefix(prefix []rune) {
	t.prefix = append([]rune(nil), prefix...)
}

// Prefix returns the title prefix.
func (t *TitleEngine) Prefix() []rune { return append([]rune(nil), t.prefix...) }

// Apply prepends the prefix to the framebuffer's title.
func (t *TitleEngine) Apply(fb *Framebuffer) {
	fb.PrefixWindowTitle(t.prefix)
}

// OverlayManager is Overlay::OverlayManager. Stock's order is significant:
// cull and draw predictions, expire and draw notifications, then prefix the
// title.
type OverlayManager struct {
	notifications *NotificationEngine
	predictions   *PredictionEngine
	title         *TitleEngine
}

// NewOverlayManager returns an overlay manager with stock defaults and no
// title prefix.
func NewOverlayManager(clock Clock) *OverlayManager {
	return &OverlayManager{
		notifications: NewNotificationEngine(clock),
		predictions:   NewPredictionEngine(),
		title:         &TitleEngine{},
	}
}

// Notifications returns the notification engine.
func (o *OverlayManager) Notifications() *NotificationEngine { return o.notifications }

// Predictions returns the prediction engine.
func (o *OverlayManager) Predictions() *PredictionEngine { return o.predictions }

// SetTitlePrefix sets the prefix applied to the remote window title.
func (o *OverlayManager) SetTitlePrefix(prefix []rune) { o.title.SetPrefix(prefix) }

// TitlePrefix returns the title prefix.
func (o *OverlayManager) TitlePrefix() []rune { return o.title.Prefix() }

// Apply draws the overlays onto a framebuffer that the caller owns and may
// discard afterwards.
func (o *OverlayManager) Apply(fb *Framebuffer) {
	o.predictions.Cull(fb)
	o.predictions.Apply(fb)
	o.notifications.AdjustMessage()
	o.notifications.Apply(fb)
	o.title.Apply(fb)
}

// WaitTime returns the earliest overlay wakeup in milliseconds.
func (o *OverlayManager) WaitTime() int {
	notificationWait := o.notifications.WaitTime()
	predictionWait := o.predictions.WaitTime()
	if predictionWait < notificationWait {
		return predictionWait
	}
	return notificationWait
}
