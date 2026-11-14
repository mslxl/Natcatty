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

// BellScanner finds the bells in a byte stream, in order, across chunks.
type BellScanner struct {
	mark Mark
}

// Mark is the state the stream is in.
func (s *BellScanner) Mark() Mark { return s.mark }

// Next returns the index in b of the next bell and the bytes before it, or -1 and b
// when there is none. Bytes after the bell are left for the next call.
func (s *BellScanner) Next(b []byte) int {
	for i := 0; i < len(b); i++ {
		c := b[i]
		switch s.mark {
		case Ground:
			switch {
			case c == 0x07:
				return i
			case c == 0x1b:
				s.mark = Esc
			}
		case Esc:
			switch c {
			case '[':
				s.mark = Control
			case ']':
				s.mark = String
			default:
				s.mark = Ground // a two-byte escape (ESC 7, ESC =, …)
			}
		case Control:
			if c >= 0x40 && c <= 0x7e {
				s.mark = Ground
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
	return -1
}
