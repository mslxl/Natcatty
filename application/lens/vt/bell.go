package vt

// Mark is the byte stream's state where a BEL means something: a bell in the ground
// state, or only the end of an OSC string inside one. The integration's marks end with
// BEL (`ESC ] 133 ; A BEL`), so a stream that treats every BEL as a bell hears one at
// every prompt and every command end, and a ripple started then has nothing to start
// from.
type Mark int

const (
	// Ground is text: a BEL here is a bell.
	Ground Mark = iota
	// Esc is just after ESC.
	Esc
	// Control is inside a CSI (ESC [) sequence, which ends at a final byte.
	Control
	// String is inside an OSC (ESC ]) string, which ends at BEL or ST.
	String
	// StringEsc is the ESC of a possible ST inside a string.
	StringEsc
)

// BellScanner finds the bells in a byte stream, in order, across chunks. It
// also finds main-screen clears (CSI 2 J and CSI 3 J), which end the prompt run
// a shell's line editor repaints without a fresh OSC 133 marker: a clear moves
// the prompt to a new row, and the flow must re-anchor its boundary there
// before it can crop to it.
type BellScanner struct {
	mark  Mark
	param []byte
}

// Event is what the scanner found at a stream position.
type Event int

const (
	// NoEvent is the end of the scanned bytes with nothing found.
	NoEvent Event = iota
	// BellEvent is a BEL in the ground state: a real bell.
	BellEvent
	// ClearEvent is the last byte of CSI 2 J or CSI 3 J: the main screen is
	// being cleared, and a prompt repainted after it starts at the cursor.
	ClearEvent
)

// Mark is the state the stream is in.
func (s *BellScanner) Mark() Mark { return s.mark }

// Next returns the index in b of the next bell and the bytes before it, or -1 and b
// when there is none. Bytes after the bell are left for the next call.
func (s *BellScanner) Next(b []byte) int {
	off := 0
	for off < len(b) {
		i, ev := s.NextEvent(b[off:])
		if ev == BellEvent {
			return off + i
		}
		if i < 0 {
			return -1
		}
		off += i + 1
	}
	return -1
}

// NextEvent returns the index in b of the next bell or clear and which it is, or
// -1 and NoEvent when there is none. Bytes after the event are left for the next
// call. Both kinds come from one scan so their parser state stays on the same
// bytes: two scanners over one stream would each consume through their own
// event and disagree after the first difference.
func (s *BellScanner) NextEvent(b []byte) (int, Event) {
	for i := 0; i < len(b); i++ {
		c := b[i]
		switch s.mark {
		case Ground:
			switch {
			case c == 0x07:
				return i, BellEvent
			case c == 0x1b:
				s.mark = Esc
			}
		case Esc:
			switch c {
			case '[':
				s.mark = Control
				s.param = s.param[:0]
			case ']':
				s.mark = String
			default:
				s.mark = Ground // a two-byte escape (ESC 7, ESC =, …)
			}
		case Control:
			if c >= 0x40 && c <= 0x7e {
				s.mark = Ground
				// 2J clears the screen the shell repaints its prompt on; 3J
				// also drops the scrollback, so every recorded row moves up
				// and the boundary has to follow the cursor as well.
				if c == 'J' && (string(s.param) == "2" || string(s.param) == "3") {
					return i, ClearEvent
				}
				continue
			}
			if len(s.param) < 24 {
				s.param = append(s.param, c)
			}
		case String:
			switch c {
			case 0x07: // the string's terminator, not a bell
				s.mark = Ground
			case 0x1b:
				s.mark = StringEsc
			}
		case StringEsc:
			if c == '\\' {
				s.mark = Ground // ST ends the string
			} else {
				s.mark = String // the ESC was text inside the string
			}
		}
	}
	return -1, NoEvent
}
