package appview

import (
	"fmt"

	"github.com/egoist/mygo/ui"
)

// MouseSeq encodes one pointer event for a program that asked for mouse reporting,
// or nil when the program did not ask for it or the event is not one it wants. col
// and row are zero-based cells.
func MouseSeq(m mouseMode, ev ui.InputEvent, col, row int) []byte {
	if !m.Enabled() || col < 0 || row < 0 {
		return nil
	}
	button := mouseButton(ev.Button)
	fin := byte('M')
	switch ev.Kind {
	case ui.InputPointerDown:
		if !(m.press || m.drag || m.motion) {
			return nil
		}
	case ui.InputPointerUp:
		if !(m.press || m.drag || m.motion) {
			return nil
		}
		fin = 'm'
	case ui.InputPointerMove:
		if !(m.drag || m.motion) {
			return nil
		}
		button += 32 // motion is flagged on top of the button in force
	case ui.InputScroll:
		switch {
		case ev.DY < 0:
			button = 64
		case ev.DY > 0:
			button = 65
		default:
			return nil
		}
	default:
		return nil
	}
	if m.sgr {
		return []byte(fmt.Sprintf("\x1b[<%d;%d;%d%c", button, col+1, row+1, fin))
	}
	if m.x10 {
		// The legacy form carries one byte per field, and reports a release as
		// button 3 (there is no separate release code).
		code := button
		if fin == 'm' {
			code = 3
		}
		return []byte{0x1b, '[', 'M', byte(32 + code), byte(32 + col + 1), byte(32 + row + 1)}
	}
	return nil
}

// mouseButton maps ui's button numbers (0 primary, 1 secondary, 2 middle) to the
// codes a program reads (0 left, 1 middle, 2 right).
func mouseButton(b int) int {
	switch b {
	case 0:
		return 0
	case 1:
		return 2
	case 2:
		return 1
	default:
		return 3 // no button, as motion without one reports
	}
}
