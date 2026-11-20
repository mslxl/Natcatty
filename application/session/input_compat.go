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
// moved. A bracketed paste is inserted literally by the remote line editor, so
// its payload bypasses every translation: only the markers are recognized.
type inputTranslator struct {
	mu     sync.Mutex
	compat modelterminal.InputCompatibility
	widths []int
	// encodedWidth reports the wire width of a rune in the Session's current
	// encoding. A byte-oriented line editor erases wire bytes, not UTF-8 bytes.
	encodedWidth func(rune) int
	pending      []byte
	// pasteScan and pasteParams recognize the bracketed-paste markers, which
	// may arrive split across writes.
	pasteScan   pasteScanState
	pasteParams []byte
	inPaste     bool
}

// pasteScanState is the subset of the terminal escape parser needed to spot
// the bracketed-paste markers without treating their payload as typed keys.
type pasteScanState uint8

const (
	pastePlain pasteScanState = iota
	pasteEscape
	pasteCSI
)

// newInputTranslator returns nil when the modes leave every byte unchanged, so
// a default Host pays no allocation or state.
func newInputTranslator(compat modelterminal.InputCompatibility, encodedWidth func(rune) int) *inputTranslator {
	compat = compat.Normalize()
	if compat.IsDefault() {
		return nil
	}
	return &inputTranslator{compat: compat, encodedWidth: encodedWidth}
}

// reset drops the local line state. A reconnected transport starts a new
// remote line, so a stale width must not over-delete on its first Backspace.
func (t *inputTranslator) reset() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.clearLine()
	t.mu.Unlock()
}

// translate returns the bytes to write for one chunk of typed input. Enter and
// the Backspace key (0x7F) are translated; a literal Ctrl-H stays a control
// key so a user can still send it on purpose. Bracketed-paste payloads pass
// through untouched, because they are text the line editor inserts literally,
// not keystrokes.
func (t *inputTranslator) translate(p []byte) []byte {
	if t == nil || len(p) == 0 {
		return p
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]byte, 0, len(p))
	for index := 0; index < len(p); index++ {
		value := p[index]
		// The paste scanner runs first so a marker's final byte is passed
		// through rather than translated as a line character.
		syntax := t.observePaste(value)
		if t.inPaste || syntax {
			out = append(out, value)
			continue
		}
		switch value {
		case '\r':
			out = append(out, t.compat.Newline.Sequence()...)
			if index+1 < len(p) && p[index+1] == '\n' {
				// The pair already carries a newline; translating the LF as
				// well would submit the line twice.
				index++
			}
			t.clearLine()
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
			out = append(out, value)
			if t.compat.LineEditing == modelterminal.LineEditingByte {
				t.track(value)
			}
		}
	}
	return out
}

// observePaste advances the bracketed-paste scanner by one byte. It reports
// whether the byte belongs to an escape sequence, in which case it is neither
// translated nor tracked. A completed "200~" enters a paste and "201~" leaves
// it; both clear the tracked line because the remote line editor, not this
// Session, decided what the pasted text did to the cursor.
func (t *inputTranslator) observePaste(value byte) (syntax bool) {
	switch t.pasteScan {
	case pasteEscape:
		if value == '[' {
			t.pasteScan = pasteCSI
			t.pasteParams = t.pasteParams[:0]
			return true
		}
		t.pasteScan = pastePlain
		if value == 0x1b {
			t.pasteScan = pasteEscape
		}
		return true
	case pasteCSI:
		if value >= 0x40 && value <= 0x7e {
			if value == '~' {
				switch string(t.pasteParams) {
				case "200":
					t.inPaste = true
					t.clearLine()
				case "201":
					t.inPaste = false
					t.clearLine()
				}
			}
			t.pasteScan = pastePlain
			return true
		}
		if len(t.pasteParams) < 8 {
			t.pasteParams = append(t.pasteParams, value)
		}
		return true
	default:
		if value == 0x1b {
			t.pasteScan = pasteEscape
			// Escape may begin a cursor sequence or a paste marker; either way
			// the remote line is no longer where this Session last left it.
			t.clearLine()
			return true
		}
		return false
	}
}

// clearLine forgets the tracked widths and any partial UTF-8 sequence.
func (t *inputTranslator) clearLine() {
	t.widths = t.widths[:0]
	t.pending = t.pending[:0]
}

// track records how many wire bytes the last character occupies. A character
// split across two writes is completed by the next call. The wire width comes
// from the Session's codec, so a GBK session counts two bytes for a CJK
// character rather than its three UTF-8 bytes. Without a codec the UTF-8 length
// is already the wire length.
func (t *inputTranslator) track(b byte) {
	switch {
	case b >= 0x20 && b < 0x7f:
		t.pending = t.pending[:0]
		t.widths = append(t.widths, 1)
	case b >= 0x80:
		t.pending = append(t.pending, b)
		if utf8.FullRune(t.pending) {
			r, size := utf8.DecodeRune(t.pending)
			width := size
			// An invalid source byte decodes as (RuneError, 1), but the wire
			// carries that byte itself (or one replacement byte), not the
			// three-byte UTF-8 encoding of U+FFFD. Ask the codec only for a
			// genuine rune; a real U+FFFD decodes with size 3 and still counts
			// its encoded width.
			if invalidSourceByte := r == utf8.RuneError && size == 1; !invalidSourceByte && t.encodedWidth != nil {
				if wire := t.encodedWidth(r); wire > 0 {
					width = wire
				}
			}
			t.widths = append(t.widths, width)
			t.pending = t.pending[:0]
		}
	default:
		// Any other control key (arrow, Ctrl-U, tab) invalidates the local
		// idea of the line, so the next Backspace must send one erase.
		t.widths = t.widths[:0]
		t.pending = t.pending[:0]
	}
}
