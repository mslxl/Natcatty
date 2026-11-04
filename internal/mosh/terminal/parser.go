// SPDX-License-Identifier: GPL-3.0-or-later

package terminal

// This is a port of mosh 1.4.0's VT500-series parser, which follows Paul
// Williams's DEC ANSI parser. It produces the same action order as stock:
// leaving a state, the transition's action, then entering the next state.

type actionKind uint8

const (
	actionNone actionKind = iota
	actionPrint
	actionExecute
	actionClear
	actionCollect
	actionParam
	actionEscDispatch
	actionCSIDispatch
	actionOSCStart
	actionOSCPut
	actionOSCEnd
)

type action struct {
	kind actionKind
	ch   rune
}

type parserState uint8

const (
	stateGround parserState = iota
	stateEscape
	stateEscapeIntermediate
	stateCSIEntry
	stateCSIParam
	stateCSIIntermediate
	stateCSIIgnore
	stateDCSEntry
	stateDCSParam
	stateDCSIntermediate
	stateDCSPassthrough
	stateDCSIgnore
	stateOSCString
	stateSOSPMAPC
)

// parser is the 7-bit/Unicode state machine.
type parser struct {
	state parserState
}

func c0Prime(ch rune) bool {
	return ch <= 0x17 || ch == 0x19 || (ch >= 0x1c && ch <= 0x1f)
}

func glgr(ch rune) bool {
	return (ch >= 0x20 && ch <= 0x7f) || (ch >= 0xa0 && ch <= 0xff)
}

// anywhereRule is State::anywhere_rule. It reports whether it handled ch.
func anywhereRule(ch rune) (parserState, action, bool) {
	switch {
	case ch == 0x18 || ch == 0x1a ||
		(ch >= 0x80 && ch <= 0x8f) ||
		(ch >= 0x91 && ch <= 0x97) ||
		ch == 0x99 || ch == 0x9a:
		return stateGround, action{kind: actionExecute, ch: ch}, true
	case ch == 0x9c:
		return stateGround, action{}, true
	case ch == 0x1b:
		return stateEscape, action{}, true
	case ch == 0x98 || ch == 0x9e || ch == 0x9f:
		return stateSOSPMAPC, action{}, true
	case ch == 0x90:
		return stateDCSEntry, action{}, true
	case ch == 0x9d:
		return stateOSCString, action{}, true
	case ch == 0x9b:
		return stateCSIEntry, action{}, true
	}
	return 0, action{}, false
}

func (p *parser) Input(ch rune, out []action) []action {
	next, act, handled := anywhereRule(ch)
	if handled {
		return p.transition(out, next, act)
	}

	class := ch
	if ch >= 0xa0 {
		class = 0x41
	}
	next, act, hasNext := p.stateRule(class)
	act.ch = ch
	if hasNext {
		return p.transition(out, next, act)
	}
	if act.kind != actionNone {
		out = append(out, act)
	}
	return out
}

func (p *parser) transition(out []action, next parserState, act action) []action {
	out = appendExit(out, p.state)
	if act.kind != actionNone {
		out = append(out, act)
	}
	out = appendEnter(out, next)
	p.state = next
	return out
}

func appendExit(out []action, state parserState) []action {
	switch state {
	case stateDCSPassthrough:
		// Unhook has no effect in mosh's emulator.
	case stateOSCString:
		out = append(out, action{kind: actionOSCEnd})
	}
	return out
}

func appendEnter(out []action, state parserState) []action {
	switch state {
	case stateEscape, stateCSIEntry, stateDCSEntry:
		out = append(out, action{kind: actionClear})
	case stateDCSPassthrough:
		// Hook has no effect in mosh's emulator.
	case stateOSCString:
		out = append(out, action{kind: actionOSCStart})
	}
	return out
}

// stateRule returns the normal transition. The bool reports a state change;
// when false the action is performed in place.
func (p *parser) stateRule(ch rune) (parserState, action, bool) {
	switch p.state {
	case stateGround:
		if c0Prime(ch) {
			return 0, action{kind: actionExecute}, false
		}
		if glgr(ch) {
			return 0, action{kind: actionPrint}, false
		}
	case stateEscape:
		if c0Prime(ch) {
			return 0, action{kind: actionExecute}, false
		}
		if ch >= 0x20 && ch <= 0x2f {
			return stateEscapeIntermediate, action{kind: actionCollect}, true
		}
		if (ch >= 0x30 && ch <= 0x4f) || (ch >= 0x51 && ch <= 0x57) ||
			ch == 0x59 || ch == 0x5a || ch == 0x5c || (ch >= 0x60 && ch <= 0x7e) {
			return stateGround, action{kind: actionEscDispatch}, true
		}
		switch ch {
		case 0x5b:
			return stateCSIEntry, action{}, true
		case 0x5d:
			return stateOSCString, action{}, true
		case 0x50:
			return stateDCSEntry, action{}, true
		case 0x58, 0x5e, 0x5f:
			return stateSOSPMAPC, action{}, true
		}
	case stateEscapeIntermediate:
		if c0Prime(ch) {
			return 0, action{kind: actionExecute}, false
		}
		if ch >= 0x20 && ch <= 0x2f {
			return 0, action{kind: actionCollect}, false
		}
		if ch >= 0x30 && ch <= 0x7e {
			return stateGround, action{kind: actionEscDispatch}, true
		}
	case stateCSIEntry:
		if c0Prime(ch) {
			return 0, action{kind: actionExecute}, false
		}
		if ch >= 0x40 && ch <= 0x7e {
			return stateGround, action{kind: actionCSIDispatch}, true
		}
		if (ch >= 0x30 && ch <= 0x39) || ch == 0x3b {
			return stateCSIParam, action{kind: actionParam}, true
		}
		if ch >= 0x3c && ch <= 0x3f {
			return stateCSIParam, action{kind: actionCollect}, true
		}
		if ch == 0x3a {
			return stateCSIIgnore, action{}, true
		}
		if ch >= 0x20 && ch <= 0x2f {
			return stateCSIIntermediate, action{kind: actionCollect}, true
		}
	case stateCSIParam:
		if c0Prime(ch) {
			return 0, action{kind: actionExecute}, false
		}
		if (ch >= 0x30 && ch <= 0x39) || ch == 0x3b {
			return 0, action{kind: actionParam}, false
		}
		if ch == 0x3a || (ch >= 0x3c && ch <= 0x3f) {
			return stateCSIIgnore, action{}, true
		}
		if ch >= 0x20 && ch <= 0x2f {
			return stateCSIIntermediate, action{kind: actionCollect}, true
		}
		if ch >= 0x40 && ch <= 0x7e {
			return stateGround, action{kind: actionCSIDispatch}, true
		}
	case stateCSIIntermediate:
		if c0Prime(ch) {
			return 0, action{kind: actionExecute}, false
		}
		if ch >= 0x20 && ch <= 0x2f {
			return 0, action{kind: actionCollect}, false
		}
		if ch >= 0x40 && ch <= 0x7e {
			return stateGround, action{kind: actionCSIDispatch}, true
		}
		if ch >= 0x30 && ch <= 0x3f {
			return stateCSIIgnore, action{}, true
		}
	case stateCSIIgnore:
		if c0Prime(ch) {
			return 0, action{kind: actionExecute}, false
		}
		if ch >= 0x40 && ch <= 0x7e {
			return stateGround, action{}, true
		}
	case stateDCSEntry:
		if ch >= 0x20 && ch <= 0x2f {
			return stateDCSIntermediate, action{kind: actionCollect}, true
		}
		if ch == 0x3a {
			return stateDCSIgnore, action{}, true
		}
		if (ch >= 0x30 && ch <= 0x39) || ch == 0x3b {
			return stateDCSParam, action{kind: actionParam}, true
		}
		if ch >= 0x3c && ch <= 0x3f {
			return stateDCSParam, action{kind: actionCollect}, true
		}
		if ch >= 0x40 && ch <= 0x7e {
			return stateDCSPassthrough, action{}, true
		}
	case stateDCSParam:
		if (ch >= 0x30 && ch <= 0x39) || ch == 0x3b {
			return 0, action{kind: actionParam}, false
		}
		if ch == 0x3a || (ch >= 0x3c && ch <= 0x3f) {
			return stateDCSIgnore, action{}, true
		}
		if ch >= 0x20 && ch <= 0x2f {
			return stateDCSIntermediate, action{kind: actionCollect}, true
		}
		if ch >= 0x40 && ch <= 0x7e {
			return stateDCSPassthrough, action{}, true
		}
	case stateDCSIntermediate:
		if ch >= 0x20 && ch <= 0x2f {
			return 0, action{kind: actionCollect}, false
		}
		if ch >= 0x40 && ch <= 0x7e {
			return stateDCSPassthrough, action{}, true
		}
		if ch >= 0x30 && ch <= 0x3f {
			return stateDCSIgnore, action{}, true
		}
	case stateDCSPassthrough:
		if c0Prime(ch) || (ch >= 0x20 && ch <= 0x7e) {
			return 0, action{}, false // Put has no effect
		}
		if ch == 0x9c {
			return stateGround, action{}, true
		}
	case stateDCSIgnore:
		if ch == 0x9c {
			return stateGround, action{}, true
		}
	case stateOSCString:
		if ch >= 0x20 && ch <= 0x7f {
			return 0, action{kind: actionOSCPut}, false
		}
		if ch == 0x9c || ch == 0x07 {
			return stateGround, action{}, true
		}
	case stateSOSPMAPC:
		if ch == 0x9c {
			return stateGround, action{}, true
		}
	}
	return 0, action{}, false
}

// Reset returns the parser to Ground, as Parser::reset_input.
func (p *parser) Reset() { p.state = stateGround }

// UTF8Parser decodes bytes into runes with glibc mbrtowc's invalid-sequence
// policy: one U+FFFD for an invalid prefix, with the last buffered byte kept
// for reprocessing.
type UTF8Parser struct {
	parser parser
	buf    [8]byte
	n      int
}

// Reset returns to the initial state and drops a partial character.
func (u *UTF8Parser) Reset() {
	u.parser.Reset()
	u.n = 0
}

// Input feeds one byte and appends the resulting parser actions.
func (u *UTF8Parser) Input(b byte, out []action) []action {
	if u.n == 0 && b <= 0x7f {
		return u.parser.Input(rune(b), out)
	}
	if u.n >= len(u.buf) {
		u.n = 0
	}
	u.buf[u.n] = b
	u.n++

	for u.n > 0 {
		r, size, status := decodeMB(u.buf[:u.n])
		switch status {
		case 1: // incomplete
			return out
		case 2: // invalid
			out = u.parser.Input(0xfffd, out)
			if u.n > 1 {
				u.buf[0] = u.buf[u.n-1]
				u.n = 1
			} else {
				u.n = 0
			}
		default:
			out = u.parser.Input(r, out)
			copy(u.buf[:], u.buf[size:u.n])
			u.n -= size
		}
	}
	return out
}

// decodeMB reports status 0 for a valid complete sequence, 1 for a valid but
// incomplete prefix, and 2 for an invalid sequence. It reproduces the UTF-8
// mbrtowc used by glibc in a UTF-8 locale, which mosh 1.4.0 was compiled
// against on Linux. That decoder accepts the historical 4-, 5-, and 6-byte
// forms for code points above U+10FFFF (mosh then substitutes U+FFFD), rejects
// overlong forms and UTF-16 surrogates, and defers the overlong/surrogate check
// until the whole sequence has arrived.
func decodeMB(buf []byte) (rune, int, int) {
	if len(buf) == 0 {
		return 0, 0, 1
	}
	b0 := buf[0]
	if b0 < 0x80 {
		return rune(b0), 1, 0
	}

	var size int
	switch {
	case b0 >= 0xc2 && b0 <= 0xdf:
		size = 2
	case b0 >= 0xe0 && b0 <= 0xef:
		size = 3
	case b0 >= 0xf0 && b0 <= 0xf7:
		size = 4
	case b0 >= 0xf8 && b0 <= 0xfb:
		size = 5
	case b0 >= 0xfc && b0 <= 0xfd:
		size = 6
	default:
		return 0, 0, 2
	}

	available := len(buf)
	if available > size {
		available = size
	}
	for i := 1; i < available; i++ {
		if buf[i] < 0x80 || buf[i] > 0xbf {
			return 0, 0, 2
		}
	}
	if available < size {
		return 0, 0, 1
	}

	// Shortest-form and surrogate rules, checked only once the sequence is
	// complete, exactly as glibc does.
	switch b0 {
	case 0xe0:
		if buf[1] < 0xa0 {
			return 0, 0, 2
		}
	case 0xed:
		if buf[1] > 0x9f {
			return 0, 0, 2
		}
	case 0xf0:
		if buf[1] < 0x90 {
			return 0, 0, 2
		}
	case 0xf8:
		if buf[1] < 0x88 {
			return 0, 0, 2
		}
	case 0xfc:
		if buf[1] < 0x84 {
			return 0, 0, 2
		}
	}

	var value uint64
	switch size {
	case 2:
		value = uint64(b0&0x1f)<<6 | uint64(buf[1]&0x3f)
	case 3:
		value = uint64(b0&0x0f)<<12 | uint64(buf[1]&0x3f)<<6 | uint64(buf[2]&0x3f)
	case 4:
		value = uint64(b0&0x07)<<18 | uint64(buf[1]&0x3f)<<12 |
			uint64(buf[2]&0x3f)<<6 | uint64(buf[3]&0x3f)
	case 5:
		value = uint64(b0&0x03)<<24 | uint64(buf[1]&0x3f)<<18 |
			uint64(buf[2]&0x3f)<<12 | uint64(buf[3]&0x3f)<<6 | uint64(buf[4]&0x3f)
	default:
		value = uint64(b0&0x01)<<30 | uint64(buf[1]&0x3f)<<24 |
			uint64(buf[2]&0x3f)<<18 | uint64(buf[3]&0x3f)<<12 |
			uint64(buf[4]&0x3f)<<6 | uint64(buf[5]&0x3f)
	}

	if value > 0x10ffff || (value >= 0xd800 && value <= 0xdfff) {
		return 0xfffd, size, 0
	}
	return rune(value), size, 0
}
