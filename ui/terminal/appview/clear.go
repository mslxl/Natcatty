package appview

import (
	"github.com/mslxl/natcatty/ui/terminal"

	"github.com/mslxl/natcatty/application/lens/model"
)

// clearSeq empties the live screen (and its scrollback) so the command that just
// finished stops occupying the terminal: its cell in the history flow is the
// record now, the way Terminal swaps a command's output for the cell.
// The order matters in this VT: 3J (erase saved lines) before 2J left the scrollback
// standing, so a finished command's output stayed above the new prompt and the prompt
// fell two rows down — which read as a line break out of nowhere. 2J then 3J empties
// both.
var clearSeq = []byte("\x1b[2J\x1b[3J\x1b[H")

// ClearScreen empties the terminal's screen and scrollback, for the app's own
// "clear blocks": the flow is emptied then, and leaving the old screen up would show
// a history the flow no longer has.
func ClearScreen(term *terminal.Terminal) {
	if term != nil {
		term.Feed(clearSeq)
	}
}

// ClearOnExit remembers which finished block the live screen was cleared for, so
// the clear happens once per command and a running command's screen is left
// alone.
type ClearOnExit struct {
	last int
	seen bool
}

// Exit reports the bytes to feed when the block just exited, or nil when there is
// nothing to do.
func (c *ClearOnExit) Exit(b *model.Block) []byte {
	if b == nil || !b.Exited {
		return nil
	}
	if c.seen && c.last == b.ID {
		return nil
	}
	c.last, c.seen = b.ID, true
	return clearSeq
}
