package session

import (
	"sync"
	"unicode/utf8"

	modelterminal "github.com/mslxl/natcatty/domain/model/terminal"
)

// inputTranslator applies a Host's input compatibility modes to typed keys.
// It runs at the Session input boundary, after command tracking observed the
// user's bytes and before the wire encoding: no caller and no transport sees
// which local mode is active.
//
// The translator keeps only the widths of the characters still available to a
// Byte-oriented Backspace. Tracking is skipped unless that mode is selected,
// and every other control key clears it because the remote cursor may have
// moved.
type inputTranslator struct {
	mu      sync.Mutex
	compat  modelterminal.InputCompatibility
	widths  []int
	pending []byte
}

// newInputTranslator returns nil when the modes leave every byte unchanged, so
// a default Host pays no allocation or state.
func newInputTranslator(compat modelterminal.InputCompatibility) *inputTranslator {
	compat = compat.Normalize()
	if compat.IsDefault() {
		return nil
	}
	return &inputTranslator{compat: compat}
}

// reset drops the local line state. A reconnected transport starts a new
// remote line, so a stale width must not over-delete on its first Backspace.
func (t *inputTranslator) reset() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.widths = t.widths[:0]
	t.pending = t.pending[:0]
	t.mu.Unlock()
}

// translate returns the bytes to write for one chunk of typed input. Enter and
// the Backspace key (0x7F) are translated; a literal Ctrl-H stays a control
// key so a user can still send it on purpose.
func (t *inputTranslator) translate(p []byte) []byte {
	if t == nil || len(p) == 0 {
		return p
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]byte, 0, len(p))
	for index := 0; index < len(p); index++ {
		switch p[index] {
		case '\r':
			out = append(out, t.compat.Newline.Sequence()...)
			if index+1 < len(p) && p[index+1] == '\n' {
				// The pair already carries a newline; translating the LF as
				// well would submit the line twice.
				index++
			}
			t.widths = t.widths[:0]
			t.pending = t.pending[:0]
		case 0x7f:
			repeat := 1
			if t.compat.LineEditing == modelterminal.LineEditingByte && len(t.widths) > 0 {
				repeat = t.widths[len(t.widths)-1]
				t.widths = t.widths[:len(t.widths)-1]
			}
			for count := 0; count < repeat; count++ {
				out = append(out, t.compat.Backspace.Byte())
			}
		default:
			out = append(out, p[index])
			if t.compat.LineEditing == modelterminal.LineEditingByte {
				t.track(p[index])
			}
		}
	}
	return out
}

// track records how many UTF-8 bytes the last character occupies. A character
// split across two writes is completed by the next call.
func (t *inputTranslator) track(b byte) {
	switch {
	case b >= 0x20 && b < 0x7f:
		t.pending = t.pending[:0]
		t.widths = append(t.widths, 1)
	case b >= 0x80:
		t.pending = append(t.pending, b)
		if utf8.FullRune(t.pending) {
			_, size := utf8.DecodeRune(t.pending)
			t.widths = append(t.widths, size)
			t.pending = t.pending[:0]
		}
	default:
		// Any other control key (arrow, Ctrl-U, tab) invalidates the local
		// idea of the line, so the next Backspace must send one erase.
		t.widths = t.widths[:0]
		t.pending = t.pending[:0]
	}
}
