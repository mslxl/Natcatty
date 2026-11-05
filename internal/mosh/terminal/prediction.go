// SPDX-License-Identifier: GPL-3.0-or-later

package terminal

// This file is an independent Go port of mosh 1.4.0's Overlay::PredictionEngine
// and its ConditionalOverlay* helpers (src/frontend/terminaloverlay.{h,cc}).
// The overlay is display-only: it never feeds the emulator, the encoder input,
// or the SSP. Every quantity it tracks is either a client-side synthetic
// framebuffer mutation or a timing hint from the transport.

const (
	srttTriggerLow  uint = 20 // <= ms cures the SRTT trigger
	srttTriggerHigh uint = 30 // > ms starts the SRTT trigger

	flagTriggerLow  uint = 50 // <= ms cures flagging
	flagTriggerHigh uint = 80 // > ms starts flagging

	glitchThreshold         uint64 = 250 // a prediction outstanding this long is a glitch
	glitchRepairCount       uint   = 10  // quick confirmations required to cure a glitch
	glitchRepairMinInterval uint64 = 150 // minimum time between quick confirmations

	glitchFlagThreshold uint64 = 5000 // outstanding this long underlines the prediction
)

// conditionalOverlay is ConditionalOverlay: one prediction's expiry and
// tentativeness state.
type conditionalOverlay struct {
	expirationFrame     uint64
	col                 int
	active              bool
	tentativeUntilEpoch uint64
	predictionTime      uint64
}

func newConditionalOverlay(expirationFrame uint64, col int, tentativeUntilEpoch uint64) conditionalOverlay {
	return conditionalOverlay{
		expirationFrame:     expirationFrame,
		col:                 col,
		tentativeUntilEpoch: tentativeUntilEpoch,
		predictionTime:      ^uint64(0),
	}
}

// tentative reports whether the prediction is still waiting for an earlier
// epoch to be confirmed.
func (c *conditionalOverlay) tentative(confirmedEpoch uint64) bool {
	return c.tentativeUntilEpoch > confirmedEpoch
}

// reset drops the overlay while leaving its replacement cell contents intact,
// exactly as ConditionalOverlay::reset does.
func (c *conditionalOverlay) reset() {
	c.expirationFrame = ^uint64(0)
	c.tentativeUntilEpoch = ^uint64(0)
	c.active = false
}

func (c *conditionalOverlay) expire(expirationFrame, now uint64) {
	c.expirationFrame = expirationFrame
	c.predictionTime = now
}

// conditionalCursorMove is ConditionalCursorMove.
type conditionalCursorMove struct {
	conditionalOverlay
	row int
}

func newConditionalCursorMove(expirationFrame uint64, row, col int, tentativeUntilEpoch uint64) *conditionalCursorMove {
	return &conditionalCursorMove{
		conditionalOverlay: newConditionalOverlay(expirationFrame, col, tentativeUntilEpoch),
		row:                row,
	}
}

// positionInBounds reports whether a predicted position still exists in the
// framebuffer. Stock asserts; a resize between cull and apply must not panic.
func positionInBounds(fb *Framebuffer, row, col int) bool {
	return row >= 0 && col >= 0 && row < fb.DS.height && col < fb.DS.width
}

func (c *conditionalCursorMove) apply(fb *Framebuffer, confirmedEpoch uint64) {
	if !c.active {
		return
	}
	if c.tentative(confirmedEpoch) {
		return
	}
	if !positionInBounds(fb, c.row, c.col) {
		return
	}
	fb.DS.MoveRow(c.row, false)
	fb.DS.MoveCol(c.col, false, false)
}

func (c *conditionalCursorMove) getValidity(fb *Framebuffer, lateAck uint64) Validity {
	if !c.active {
		return ValidityInactive
	}
	if !positionInBounds(fb, c.row, c.col) {
		return ValidityIncorrectOrExpired
	}
	if lateAck >= c.expirationFrame {
		if fb.DS.cursorCol == c.col && fb.DS.cursorRow == c.row {
			return ValidityCorrect
		}
		return ValidityIncorrectOrExpired
	}
	return ValidityPending
}

// conditionalOverlayCell is ConditionalOverlayCell: a speculative cell and the
// contents it replaced.
type conditionalOverlayCell struct {
	conditionalOverlay
	replacement      Cell
	unknown          bool
	originalContents []Cell
}

func newConditionalOverlayCell(expirationFrame uint64, col int, tentativeUntilEpoch uint64) conditionalOverlayCell {
	return conditionalOverlayCell{
		conditionalOverlay: newConditionalOverlay(expirationFrame, col, tentativeUntilEpoch),
	}
}

func (c *conditionalOverlayCell) reset() {
	c.unknown = false
	c.originalContents = nil
	c.conditionalOverlay.reset()
}

// resetWithOrig preserves the current replacement as an original when the
// prediction was known and active, so a later matching server cell does not
// earn prediction credit.
func (c *conditionalOverlayCell) resetWithOrig() {
	if !c.active || c.unknown {
		c.reset()
		return
	}
	c.originalContents = append(c.originalContents, c.replacement)
	c.conditionalOverlay.reset()
}

func (c *conditionalOverlayCell) apply(fb *Framebuffer, confirmedEpoch uint64, row int, flag bool) {
	if !c.active || !positionInBounds(fb, row, c.col) {
		return
	}
	if c.tentative(confirmedEpoch) {
		return
	}
	if c.replacement.IsBlank() && fb.Cell(row, c.col).IsBlank() {
		flag = false
	}
	if c.unknown {
		if flag && c.col != fb.DS.width-1 {
			fb.Cell(row, c.col).Renditions.SetAttribute(AttrUnderlined, true)
		}
		return
	}
	if *fb.Cell(row, c.col) != c.replacement {
		*fb.Cell(row, c.col) = c.replacement
		if flag {
			fb.Cell(row, c.col).Renditions.SetAttribute(AttrUnderlined, true)
		}
	}
}

func (c *conditionalOverlayCell) getValidity(fb *Framebuffer, row int, lateAck uint64) Validity {
	if !c.active {
		return ValidityInactive
	}
	if !positionInBounds(fb, row, c.col) {
		return ValidityIncorrectOrExpired
	}
	current := fb.Cell(row, c.col)
	if lateAck < c.expirationFrame {
		return ValidityPending
	}
	if c.unknown {
		return ValidityCorrectNoCredit
	}
	if c.replacement.IsBlank() {
		return ValidityCorrectNoCredit
	}
	if current.ContentsMatch(&c.replacement) {
		for i := range c.originalContents {
			if c.originalContents[i].ContentsMatch(&c.replacement) {
				return ValidityCorrectNoCredit
			}
		}
		return ValidityCorrect
	}
	return ValidityIncorrectOrExpired
}

// conditionalOverlayRow is ConditionalOverlayRow.
type conditionalOverlayRow struct {
	rowNum       int
	overlayCells []conditionalOverlayCell
}

func (r *conditionalOverlayRow) apply(fb *Framebuffer, confirmedEpoch uint64, flag bool) {
	for i := range r.overlayCells {
		r.overlayCells[i].apply(fb, confirmedEpoch, r.rowNum, flag)
	}
}

// PredictionEngine is Overlay::PredictionEngine. It speculatively draws the
// user's keystrokes on the client's copy of the framebuffer, confirms them when
// the server's authoritative state agrees, and drops them when it does not.
type PredictionEngine struct {
	lastByte byte
	parser   UTF8Parser

	overlays []*conditionalOverlayRow
	cursors  []*conditionalCursorMove

	localFrameSent    uint64
	localFrameAcked   uint64
	localFrameLateAck uint64

	predictionEpoch uint64
	confirmedEpoch  uint64

	flagging              bool
	srttTrigger           bool
	glitchTrigger         uint
	lastQuickConfirmation uint64

	sendInterval uint
	lastHeight   int
	lastWidth    int

	displayPreference DisplayPreference
	predictOverwrite  bool

	clock Clock
}

// NewPredictionEngine returns an engine with stock defaults: adaptive display
// and overwrite prediction disabled.
func NewPredictionEngine(clock Clock) *PredictionEngine {
	if clock == nil {
		clock = MonotonicMillis
	}
	return &PredictionEngine{
		predictionEpoch:   1,
		confirmedEpoch:    0,
		sendInterval:      250,
		displayPreference: DisplayAdaptive,
		clock:             clock,
	}
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

// timingTestsNecessary reports whether a timing trigger still has room to
// change. Stock: !(glitch_trigger && flagging).
func (p *PredictionEngine) timingTestsNecessary() bool {
	return !(p.glitchTrigger != 0 && p.flagging)
}

// Active reports whether any prediction is stored.
func (p *PredictionEngine) Active() bool {
	if len(p.cursors) > 0 {
		return true
	}
	for _, row := range p.overlays {
		for i := range row.overlayCells {
			if row.overlayCells[i].active {
				return true
			}
		}
	}
	return false
}

// WaitTime returns stock's prediction wakeup interval: 50 ms while a timing
// trigger can still change and a prediction exists, otherwise no wakeup.
func (p *PredictionEngine) WaitTime() int {
	if p.timingTestsNecessary() && p.Active() {
		return 50
	}
	return int(^uint(0) >> 1)
}

func (p *PredictionEngine) cursor() *conditionalCursorMove {
	return p.cursors[len(p.cursors)-1]
}

// Cull validates every prediction against the server-authoritative framebuffer
// and removes or resets the ones the server has superseded.
func (p *PredictionEngine) Cull(fb *Framebuffer) {
	if p.displayPreference == DisplayNever {
		return
	}
	if fb == nil {
		return
	}
	if p.lastHeight != fb.DS.height || p.lastWidth != fb.DS.width {
		p.lastHeight = fb.DS.height
		p.lastWidth = fb.DS.width
		p.Reset()
	}

	now := p.clock()

	// Control srtt_trigger with hysteresis.
	if p.sendInterval > srttTriggerHigh {
		p.srttTrigger = true
	} else if p.srttTrigger && p.sendInterval <= srttTriggerLow && !p.Active() {
		p.srttTrigger = false
	}

	// Control underlining with hysteresis.
	if p.sendInterval > flagTriggerHigh {
		p.flagging = true
	} else if p.sendInterval <= flagTriggerLow {
		p.flagging = false
	}

	// Really big glitches also activate underlining.
	if p.glitchTrigger > glitchRepairCount {
		p.flagging = true
	}

	// Go through cell predictions. Rows are pointers so the slice can shrink
	// without invalidating a row reached in this pass.
	for i := 0; i < len(p.overlays); {
		row := p.overlays[i]
		if row.rowNum < 0 || row.rowNum >= fb.DS.height {
			p.overlays = append(p.overlays[:i], p.overlays[i+1:]...)
			continue
		}
		for j := range row.overlayCells {
			cell := &row.overlayCells[j]
			switch cell.getValidity(fb, row.rowNum, p.localFrameLateAck) {
			case ValidityIncorrectOrExpired:
				if cell.tentative(p.confirmedEpoch) {
					if p.displayPreference == DisplayExperimental {
						cell.reset()
					} else {
						p.killEpoch(cell.tentativeUntilEpoch, fb)
					}
				} else {
					if p.displayPreference == DisplayExperimental {
						cell.reset()
					} else {
						p.Reset()
						return
					}
				}
			case ValidityCorrect:
				if cell.tentativeUntilEpoch > p.confirmedEpoch {
					p.confirmedEpoch = cell.tentativeUntilEpoch
				}

				// When predictions are confirmed quickly, slowly take away the
				// glitch trigger.
				if now-cell.predictionTime < glitchThreshold &&
					(p.glitchTrigger > 0 && now-glitchRepairMinInterval >= p.lastQuickConfirmation) {
					p.glitchTrigger--
					p.lastQuickConfirmation = now
				}

				// Match the rest of the row to the actual renditions.
				actualRenditions := fb.Cell(row.rowNum, cell.col).Renditions
				for k := j; k < len(row.overlayCells); k++ {
					row.overlayCells[k].replacement.Renditions = actualRenditions
				}

				cell.reset()
			case ValidityCorrectNoCredit:
				cell.reset()
			case ValidityPending:
				// A prediction that stays outstanding activates predictions even
				// when the round-trip time is low.
				elapsed := now - cell.predictionTime
				if elapsed >= glitchFlagThreshold {
					p.glitchTrigger = glitchRepairCount * 2 // display and underline
				} else if elapsed >= glitchThreshold && p.glitchTrigger < glitchRepairCount {
					p.glitchTrigger = glitchRepairCount // just display
				}
			}
		}
		i++
	}

	// Go through cursor predictions.
	if len(p.cursors) > 0 &&
		p.cursor().getValidity(fb, p.localFrameLateAck) == ValidityIncorrectOrExpired {
		if p.displayPreference == DisplayExperimental {
			p.cursors = nil
		} else {
			p.Reset()
			return
		}
	}

	kept := p.cursors[:0]
	for _, cursor := range p.cursors {
		if cursor.getValidity(fb, p.localFrameLateAck) != ValidityPending {
			continue
		}
		kept = append(kept, cursor)
	}
	for i := len(kept); i < len(p.cursors); i++ {
		p.cursors[i] = nil
	}
	p.cursors = kept
}

// Apply draws the still-valid predictions onto the framebuffer the caller is
// about to encode. The framebuffer is a client-side copy; the server state is
// never mutated.
func (p *PredictionEngine) Apply(fb *Framebuffer) {
	if fb == nil {
		return
	}
	if p.displayPreference == DisplayNever ||
		!(p.srttTrigger || p.glitchTrigger != 0 ||
			p.displayPreference == DisplayAlways ||
			p.displayPreference == DisplayExperimental) {
		return
	}
	for _, cursor := range p.cursors {
		cursor.apply(fb, p.confirmedEpoch)
	}
	for _, row := range p.overlays {
		row.apply(fb, p.confirmedEpoch, p.flagging)
	}
}

// Reset drops all predictions and starts a new tentative epoch.
func (p *PredictionEngine) Reset() {
	p.cursors = nil
	p.overlays = nil
	p.becomeTentative()
}

// becomeTentative moves new predictions behind the confirmation point so they
// are not displayed until an echo from the same epoch confirms them.
func (p *PredictionEngine) becomeTentative() {
	if p.displayPreference != DisplayExperimental {
		p.predictionEpoch++
	}
}

// initCursor starts a cursor prediction at the framebuffer cursor, or carries
// the previous predicted cursor into a new epoch.
func (p *PredictionEngine) initCursor(fb *Framebuffer) {
	if len(p.cursors) == 0 {
		cursor := newConditionalCursorMove(p.localFrameSent+1,
			fb.DS.cursorRow, fb.DS.cursorCol, p.predictionEpoch)
		cursor.active = true
		p.cursors = append(p.cursors, cursor)
		return
	}
	if p.cursor().tentativeUntilEpoch != p.predictionEpoch {
		cursor := newConditionalCursorMove(p.localFrameSent+1,
			p.cursor().row, p.cursor().col, p.predictionEpoch)
		cursor.active = true
		p.cursors = append(p.cursors, cursor)
	}
}

// killEpoch drops predictions older than an epoch whose server state turned
// out wrong, then starts a fresh tentative epoch from the server cursor.
func (p *PredictionEngine) killEpoch(epoch uint64, fb *Framebuffer) {
	kept := p.cursors[:0]
	for _, cursor := range p.cursors {
		if cursor.tentative(epoch - 1) {
			continue
		}
		kept = append(kept, cursor)
	}
	for i := len(kept); i < len(p.cursors); i++ {
		p.cursors[i] = nil
	}
	p.cursors = kept

	cursor := newConditionalCursorMove(p.localFrameSent+1,
		fb.DS.cursorRow, fb.DS.cursorCol, p.predictionEpoch)
	cursor.active = true
	p.cursors = append(p.cursors, cursor)

	for _, row := range p.overlays {
		for i := range row.overlayCells {
			if row.overlayCells[i].tentative(epoch - 1) {
				row.overlayCells[i].reset()
			}
		}
	}
	p.becomeTentative()
}

// getOrMakeRow returns the prediction row for rowNum, creating it with one
// disabled cell per column.
func (p *PredictionEngine) getOrMakeRow(rowNum, numCols int) *conditionalOverlayRow {
	for _, row := range p.overlays {
		if row.rowNum == rowNum {
			return row
		}
	}
	row := &conditionalOverlayRow{rowNum: rowNum}
	row.overlayCells = make([]conditionalOverlayCell, numCols)
	for i := range row.overlayCells {
		row.overlayCells[i] = newConditionalOverlayCell(0, i, p.predictionEpoch)
	}
	p.overlays = append(p.overlays, row)
	return row
}

// newlineCarriageReturn predicts a carriage return, either moving down or
// blanking the last row. Scrolling is deliberately not predicted.
func (p *PredictionEngine) newlineCarriageReturn(fb *Framebuffer) {
	now := p.clock()
	p.initCursor(fb)
	p.cursor().col = 0
	if p.cursor().row == fb.DS.height-1 {
		row := p.getOrMakeRow(p.cursor().row, fb.DS.width)
		for i := range row.overlayCells {
			cell := &row.overlayCells[i]
			cell.active = true
			cell.tentativeUntilEpoch = p.predictionEpoch
			cell.expire(p.localFrameSent+1, now)
			cell.replacement.Clear()
		}
		return
	}
	p.cursor().row++
}

// armCell turns cell into an active prediction for the current epoch and saves
// the server cell it replaces, so an echo that merely restores the original
// contents does not earn confirmation credit. A negative col means the cursor
// column, matching stock's fb.get_cell() defaults.
func (p *PredictionEngine) armCell(cell *conditionalOverlayCell, fb *Framebuffer,
	row, col int, now uint64) {
	cell.resetWithOrig()
	cell.active = true
	cell.tentativeUntilEpoch = p.predictionEpoch
	cell.expire(p.localFrameSent+1, now)
	cell.originalContents = append(cell.originalContents, *fb.Cell(row, col))
}

// NewUserByte feeds one raw user keystroke to the predictor. The caller sends
// the byte to the server unchanged; prediction is a separate, display-only
// path.
func (p *PredictionEngine) NewUserByte(theByte byte, fb *Framebuffer) {
	if p.displayPreference == DisplayNever || fb == nil {
		return
	}
	if p.displayPreference == DisplayExperimental {
		p.predictionEpoch = p.confirmedEpoch
	}

	p.Cull(fb)

	now := p.clock()

	// Translate the application-mode cursor control function to the ANSI
	// cursor control sequence.
	if p.lastByte == 0x1b && theByte == 'O' {
		theByte = '['
	}
	p.lastByte = theByte

	actions := p.parser.Input(theByte, nil)
	for _, act := range actions {
		switch act.kind {
		case actionPrint:
			p.initCursor(fb)
			ch := act.ch

			if ch == 0x7f { // backspace
				row := p.getOrMakeRow(p.cursor().row, fb.DS.width)
				if p.cursor().col > 0 {
					p.cursor().col--
					p.cursor().expire(p.localFrameSent+1, now)

					if p.predictOverwrite {
						cell := &row.overlayCells[p.cursor().col]
						origCell := *fb.Cell(-1, -1)
						p.armCell(cell, fb, p.cursor().row, -1, now)
						cell.replacement = origCell
						cell.replacement.Clear()
						cell.replacement.Append(' ')
					} else {
						for i := p.cursor().col; i < fb.DS.width; i++ {
							cell := &row.overlayCells[i]
							p.armCell(cell, fb, p.cursor().row, i, now)

							if i+2 < fb.DS.width {
								nextCell := &row.overlayCells[i+1]
								nextActual := fb.Cell(p.cursor().row, i+1)
								if nextCell.active {
									if nextCell.unknown {
										cell.unknown = true
									} else {
										cell.unknown = false
										cell.replacement = nextCell.replacement
									}
								} else {
									cell.unknown = false
									cell.replacement = *nextActual
								}
							} else {
								cell.unknown = true
							}
						}
					}
				}
			} else if ch < 0x20 || RuneWidth(ch) != 1 {
				// Unknown print.
				p.becomeTentative()
			} else {
				if p.cursor().row < 0 || p.cursor().col < 0 ||
					p.cursor().row >= fb.DS.height || p.cursor().col >= fb.DS.width {
					// Stock asserts. A stale cursor from a resize becomes
					// tentative rather than corrupting the overlay.
					p.becomeTentative()
					continue
				}
				row := p.getOrMakeRow(p.cursor().row, fb.DS.width)

				if p.cursor().col+1 >= fb.DS.width {
					// Prediction in the last column is tricky: emacs may show a
					// wrap marker while a shell just puts the character there.
					p.becomeTentative()
				}

				rightmostColumn := fb.DS.width - 1
				if p.predictOverwrite {
					rightmostColumn = p.cursor().col
				}
				for i := rightmostColumn; i > p.cursor().col; i-- {
					cell := &row.overlayCells[i]
					p.armCell(cell, fb, p.cursor().row, i, now)

					prevCell := &row.overlayCells[i-1]
					prevActual := fb.Cell(p.cursor().row, i-1)
					switch {
					case i == fb.DS.width-1:
						cell.unknown = true
					case prevCell.active:
						if prevCell.unknown {
							cell.unknown = true
						} else {
							cell.unknown = false
							cell.replacement = prevCell.replacement
						}
					default:
						cell.unknown = false
						cell.replacement = *prevActual
					}
				}

				cell := &row.overlayCells[p.cursor().col]
				p.armCell(cell, fb, p.cursor().row, p.cursor().col, now)
				cell.replacement.Renditions = fb.DS.renditions

				// Heuristic: match the renditions of the character to the left.
				if p.cursor().col > 0 {
					prevCell := &row.overlayCells[p.cursor().col-1]
					prevActual := fb.Cell(p.cursor().row, p.cursor().col-1)
					if prevCell.active && !prevCell.unknown {
						cell.replacement.Renditions = prevCell.replacement.Renditions
					} else {
						cell.replacement.Renditions = prevActual.Renditions
					}
				}

				cell.replacement.Clear()
				cell.replacement.Append(ch)

				p.cursor().expire(p.localFrameSent+1, now)

				if p.cursor().col < fb.DS.width-1 {
					p.cursor().col++
				} else {
					p.becomeTentative()
					p.newlineCarriageReturn(fb)
				}
			}
		case actionExecute:
			if act.ch == 0x0d { // CR
				p.becomeTentative()
				p.newlineCarriageReturn(fb)
			} else {
				p.becomeTentative()
			}
		case actionEscDispatch:
			p.becomeTentative()
		case actionCSIDispatch:
			switch act.ch {
			case 'C': // right arrow
				p.initCursor(fb)
				if p.cursor().col < fb.DS.width-1 {
					p.cursor().col++
					p.cursor().expire(p.localFrameSent+1, now)
				}
			case 'D': // left arrow
				p.initCursor(fb)
				if p.cursor().col > 0 {
					p.cursor().col--
					p.cursor().expire(p.localFrameSent+1, now)
				}
			default:
				p.becomeTentative()
			}
		}
	}
}
