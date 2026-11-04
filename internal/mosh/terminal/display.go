// SPDX-License-Identifier: GPL-3.0-or-later

package terminal

import (
	"fmt"
	"strings"
)

// DisplayOptions are the terminfo-derived capabilities stock's Display reads
// through ncurses. The zero value matches mosh's built-in defaults
// (MOSH_NO_TERM_INIT without a smcup/rmcup).
type DisplayOptions struct {
	HasECH   bool
	HasBCE   bool
	HasTitle bool
	Smcup    string
	Rmcup    string
}

// DefaultDisplayOptions returns the capabilities of the desktop renderer:
// ECH, BCE, and title support, and no alternate-screen sequences because MyGo
// is an embedded renderer rather than the outer terminal.
func DefaultDisplayOptions() DisplayOptions {
	return DisplayOptions{HasECH: true, HasBCE: true, HasTitle: true}
}

// XtermDisplayOptions returns the capabilities mosh-client gets from
// TERM=xterm-256color with MOSH_NO_TERM_INIT unset. The parity harness uses it
// to compare byte-for-byte with stock mosh-client.
func XtermDisplayOptions() DisplayOptions {
	return DisplayOptions{
		HasECH:   true,
		HasBCE:   true,
		HasTitle: true,
		Smcup:    "\033[?1049h",
		Rmcup:    "\033[?1049l",
	}
}

// Display is Terminal::Display: it encodes a framebuffer difference as the
// ECMA-48 byte stream that drives the renderer.
type Display struct {
	opts DisplayOptions
}

// NewDisplay returns a Display with the given capabilities.
func NewDisplay(opts DisplayOptions) *Display { return &Display{opts: opts} }

// Options returns the display capabilities.
func (d *Display) Options() DisplayOptions { return d.opts }

// Open is Display::open: enter the alternate screen if configured, then force
// application-cursor-key mode in the local terminal.
func (d *Display) Open() string { return d.opts.Smcup + "\033[?1h" }

// Close is Display::close: restore the local terminal and leave the alternate
// screen if configured.
func (d *Display) Close() string {
	return "\033[?1l\033[0m\033[?25h" +
		"\033[?1003l\033[?1002l\033[?1001l\033[?1000l" +
		"\033[?1015l\033[?1006l\033[?1005l" + d.opts.Rmcup
}

func initialRendition() Renditions { return NewRenditions(0) }

// NewFrame is Display::new_frame. initialized is false on the first frame and
// after a forced repaint; last is the framebuffer currently on screen; f is
// the target framebuffer.
func (d *Display) NewFrame(initialized bool, last, f *Framebuffer) string {
	frame := newFrameState(last)

	// Has the bell been rung?
	if f.BellCount != last.BellCount {
		frame.appendByte('\a')
	}

	// Has the icon name or window title changed?
	if d.opts.HasTitle && f.TitleInitialized &&
		(!initialized || !runesEqual(f.IconName, last.IconName) ||
			!runesEqual(f.WindowTitle, last.WindowTitle)) {
		if runesEqual(f.IconName, f.WindowTitle) {
			frame.appendString("\033]0;")
			for _, r := range f.WindowTitle {
				frame.appendRune(r)
			}
			frame.appendByte('\a')
		} else {
			frame.appendString("\033]1;")
			for _, r := range f.IconName {
				frame.appendRune(r)
			}
			frame.appendByte('\a')

			frame.appendString("\033]2;")
			for _, r := range f.WindowTitle {
				frame.appendRune(r)
			}
			frame.appendByte('\a')
		}
	}

	// Has the clipboard changed?
	if !runesEqual(f.Clipboard, last.Clipboard) {
		frame.appendString("\033]52;c;")
		for _, r := range f.Clipboard {
			frame.appendRune(r)
		}
		frame.appendByte('\a')
	}

	// Has reverse video state changed?
	if !initialized || f.DS.ReverseVideo != last.DS.ReverseVideo {
		if f.DS.ReverseVideo {
			frame.appendString("\033[?5h")
		} else {
			frame.appendString("\033[?5l")
		}
	}

	// Has the size changed?
	if !initialized || f.DS.width != last.DS.width || f.DS.height != last.DS.height {
		// Reset the scrolling region, then clear the screen.
		frame.appendString("\033[r")
		frame.appendString("\033[0m\033[H\033[2J")
		initialized = false
		frame.cursorX = 0
		frame.cursorY = 0
		frame.currentRendition = initialRendition()
	} else {
		frame.cursorX = last.DS.cursorCol
		frame.cursorY = last.DS.cursorRow
		frame.currentRendition = last.DS.renditions
	}

	// Is cursor visibility initialized?
	if !initialized {
		frame.cursorVisible = false
		frame.appendString("\033[?25l")
	}

	frameY := 0
	var blankRow *Row
	rows := append([]*Row(nil), last.Rows...)

	// Extend rows if a resize made the new framebuffer wider than the old.
	if last.DS.width < f.DS.width {
		for i, p := range rows {
			clone := p.Clone()
			for len(clone.Cells) < f.DS.width {
				clone.Cells = append(clone.Cells, NewCell(f.DS.BackgroundRendition()))
			}
			rows[i] = clone
		}
	}
	// Add rows if a resize made the new framebuffer taller than the old.
	if len(rows) < f.DS.height {
		blankRow = NewRow(f.DS.width, 0)
		for len(rows) < f.DS.height {
			rows = append(rows, blankRow)
		}
	}

	// Shortcut: has the display moved up by a number of lines?
	if initialized {
		linesScrolled := 0
		scrollHeight := 0

		for row := 0; row < f.DS.height; row++ {
			newRow := f.Rows[0]
			oldRow := rows[row]
			if !(newRow == oldRow || newRow.Equal(oldRow)) {
				continue
			}
			// Row 0 is ourselves and probably did not scroll.
			if row == 0 {
				break
			}
			linesScrolled = row
			scrollHeight = 1

			for regionHeight := 1; linesScrolled+regionHeight < f.DS.height; regionHeight++ {
				if f.Rows[regionHeight].Equal(rows[linesScrolled+regionHeight]) {
					scrollHeight = regionHeight + 1
				} else {
					break
				}
			}
			break
		}

		if scrollHeight != 0 {
			frameY = scrollHeight

			if linesScrolled != 0 {
				// We need a proper blank row.
				if blankRow == nil {
					blankRow = NewRow(f.DS.width, 0)
				}
				frame.updateRendition(initialRendition(), true)

				topMargin := 0
				bottomMargin := topMargin + linesScrolled + scrollHeight - 1

				// Common case: already on the bottom line and scrolling the
				// whole screen. Just CR and LFs.
				if scrollHeight+linesScrolled == f.DS.height &&
					frame.cursorY+1 == f.DS.height {
					frame.appendByte('\r')
					for i := 0; i < linesScrolled; i++ {
						frame.appendByte('\n')
					}
					frame.cursorX = 0
				} else {
					frame.appendString(fmt.Sprintf("\033[%d;%dr",
						topMargin+1, bottomMargin+1))
					frame.cursorX = -1
					frame.cursorY = -1
					frame.appendSilentMove(bottomMargin, 0)
					for i := 0; i < linesScrolled; i++ {
						frame.appendByte('\n')
					}
					frame.appendString("\033[r")
					frame.cursorX = -1
					frame.cursorY = -1
				}

				// Do the move in the local index.
				for i := topMargin; i <= bottomMargin; i++ {
					if i+linesScrolled <= bottomMargin {
						rows[i] = rows[i+linesScrolled]
					} else {
						rows[i] = blankRow
					}
				}
			}
		}
	}

	// Update the display, row by row.
	wrap := false
	for ; frameY < f.DS.height; frameY++ {
		wrap = d.putRow(initialized, frame, f, frameY, rows[frameY], wrap)
	}

	// Has cursor location changed?
	if !initialized || f.DS.cursorRow != frame.cursorY || f.DS.cursorCol != frame.cursorX {
		frame.appendMove(f.DS.cursorRow, f.DS.cursorCol)
	}

	// Has cursor visibility changed?
	if !initialized || f.DS.CursorVisible != frame.cursorVisible {
		if f.DS.CursorVisible {
			frame.appendString("\033[?25h")
		} else {
			frame.appendString("\033[?25l")
		}
	}

	// Have renditions changed?
	frame.updateRendition(f.DS.renditions, !initialized)

	// Has bracketed paste mode changed?
	if !initialized || f.DS.BracketedPaste != last.DS.BracketedPaste {
		if f.DS.BracketedPaste {
			frame.appendString("\033[?2004h")
		} else {
			frame.appendString("\033[?2004l")
		}
	}

	// Has mouse reporting mode changed?
	if !initialized || f.DS.MouseReportingMode != last.DS.MouseReportingMode {
		if f.DS.MouseReportingMode == MouseReportingNone {
			frame.appendString("\033[?1003l")
			frame.appendString("\033[?1002l")
			frame.appendString("\033[?1001l")
			frame.appendString("\033[?1000l")
		} else {
			if last.DS.MouseReportingMode != MouseReportingNone {
				frame.appendString(fmt.Sprintf("\033[?%dl", int(last.DS.MouseReportingMode)))
			}
			frame.appendString(fmt.Sprintf("\033[?%dh", int(f.DS.MouseReportingMode)))
		}
	}

	// Has mouse focus mode changed?
	if !initialized || f.DS.MouseFocusEvent != last.DS.MouseFocusEvent {
		if f.DS.MouseFocusEvent {
			frame.appendString("\033[?1004h")
		} else {
			frame.appendString("\033[?1004l")
		}
	}

	// Has mouse encoding mode changed?
	if !initialized || f.DS.MouseEncodingMode != last.DS.MouseEncodingMode {
		if f.DS.MouseEncodingMode == MouseEncodingDefault {
			frame.appendString("\033[?1015l")
			frame.appendString("\033[?1006l")
			frame.appendString("\033[?1005l")
		} else {
			if last.DS.MouseEncodingMode != MouseEncodingDefault {
				frame.appendString(fmt.Sprintf("\033[?%dl", int(last.DS.MouseEncodingMode)))
			}
			frame.appendString(fmt.Sprintf("\033[?%dh", int(f.DS.MouseEncodingMode)))
		}
	}

	return frame.str.String()
}

func (d *Display) putRow(initialized bool, frame *frameState, f *Framebuffer,
	frameY int, oldRow *Row, wrap bool) bool {
	frameX := 0

	row := f.Rows[frameY]
	cells := row.Cells
	oldCells := oldRow.Cells

	// If forced to write the first column because of wrap, do so.
	if wrap {
		cell := cells[0]
		frame.updateRendition(cell.Renditions, false)
		frame.appendCell(&cell)
		frameX += cell.Width()
		frame.cursorX += cell.Width()
	}

	// If rows are the same object, nothing to do.
	if initialized && row == oldRow {
		return false
	}

	wrapThis := row.Wrap()
	rowWidth := f.DS.width
	clearCount := 0
	wroteLastCell := false
	blankRenditions := initialRendition()

	for frameX < rowWidth {
		cell := &cells[frameX]

		// Does the cell need to be drawn?
		if initialized && clearCount == 0 && *cell == oldCells[frameX] {
			frameX += cell.Width()
			continue
		}

		// Slurp up all the empty cells.
		if cell.Empty() {
			if clearCount == 0 {
				blankRenditions = cell.Renditions
			}
			if cell.Renditions == blankRenditions {
				// Remember run of blank cells.
				clearCount++
				frameX++
				continue
			}
		}

		// Clear or write cells within the row (not to the end).
		if clearCount != 0 {
			frame.appendSilentMove(frameY, frameX-clearCount)
			frame.updateRendition(blankRenditions, false)
			canUseErase := d.opts.HasBCE || frame.currentRendition == initialRendition()
			if canUseErase && d.opts.HasECH && clearCount > 4 {
				frame.appendString(fmt.Sprintf("\033[%dX", clearCount))
			} else {
				for i := 0; i < clearCount; i++ {
					frame.appendByte(' ')
				}
				frame.cursorX = frameX
			}
			clearCount = 0
			// If this is another empty cell in a different rendition, restart
			// counting and continue here.
			if cell.Empty() {
				blankRenditions = cell.Renditions
				clearCount = 1
				frameX++
				continue
			}
		}

		// Draw a character cell.
		cellWidth := cell.Width()
		// Trash the cursor position when printing the last character of a
		// wrapping row, to force explicit positioning.
		if wrapThis && frameX+cellWidth >= rowWidth {
			frame.cursorX = -1
			frame.cursorY = -1
		}
		frame.appendSilentMove(frameY, frameX)
		frame.updateRendition(cell.Renditions, false)
		frame.appendCell(cell)
		frameX += cellWidth
		frame.cursorX += cellWidth
		if frameX >= rowWidth {
			wroteLastCell = true
		}
	}

	// End of line: clear or write empty cells at EOL.
	if clearCount != 0 {
		frame.appendSilentMove(frameY, frameX-clearCount)
		frame.updateRendition(blankRenditions, false)

		canUseErase := d.opts.HasBCE || frame.currentRendition == initialRendition()
		if canUseErase && !wrapThis {
			frame.appendString("\033[K")
		} else {
			for i := 0; i < clearCount; i++ {
				frame.appendByte(' ')
			}
			frame.cursorX = frameX
			wroteLastCell = true
		}
	}

	if !(wroteLastCell && frameY < f.DS.height-1) {
		return false
	}
	// Let the real cursor wrap to hint that a word-select should group the end
	// of one line with the beginning of the next.
	if wrapThis {
		frame.cursorX = 0
		frame.cursorY++
		return true
	}
	frame.appendString("\r\n")
	frame.cursorX = 0
	frame.cursorY++
	return false
}

type frameState struct {
	str              strings.Builder
	cursorX, cursorY int
	currentRendition Renditions
	cursorVisible    bool
	lastFrame        *Framebuffer
}

func newFrameState(last *Framebuffer) *frameState {
	width, height := last.DS.Size()
	frame := &frameState{
		cursorVisible: last.DS.CursorVisible,
		lastFrame:     last,
	}
	if width > 0 && height > 0 {
		frame.str.Grow(width * height * 4)
	}
	return frame
}

func (f *frameState) appendByte(b byte)     { f.str.WriteByte(b) }
func (f *frameState) appendString(s string) { f.str.WriteString(s) }
func (f *frameState) appendRune(r rune)     { f.str.WriteRune(r) }
func (f *frameState) appendCell(cell *Cell) { cell.PrintGrapheme(&f.str) }

func (f *frameState) appendSilentMove(y, x int) {
	if f.cursorX == x && f.cursorY == y {
		return
	}
	// Turn off the cursor if necessary before moving it.
	if f.cursorVisible {
		f.appendString("\033[?25l")
		f.cursorVisible = false
	}
	f.appendMove(y, x)
}

func (f *frameState) appendMove(y, x int) {
	lastX := f.cursorX
	lastY := f.cursorY
	f.cursorX = x
	f.cursorY = y

	// Only optimize if the cursor position is known.
	if lastX != -1 && lastY != -1 {
		// Can we use CR and/or LF? They are cheap and easy to trace.
		if x == 0 && y-lastY >= 0 && y-lastY < 5 {
			if lastX != 0 {
				f.appendByte('\r')
			}
			for i := 0; i < y-lastY; i++ {
				f.appendByte('\n')
			}
			return
		}
		// Backspaces are good too.
		if y == lastY && x-lastX < 0 && x-lastX > -5 {
			for i := 0; i < lastX-x; i++ {
				f.appendByte('\b')
			}
			return
		}
	}
	f.appendString(fmt.Sprintf("\033[%d;%dH", y+1, x+1))
}

func (f *frameState) updateRendition(r Renditions, force bool) {
	if force || f.currentRendition != r {
		f.appendString(r.SGR())
		f.currentRendition = r
	}
}
