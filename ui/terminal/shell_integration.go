package terminal

import (
	"strings"
)

// Ghostty's C API preserves prompts by default. A source that redraws its
// whole prompt on resize, as zsh does, leaves the initial screen unmarked.
const promptRedraw = "\x1b]133;A;redraw=1\a\x1b]133;C\a"

// Track only OSC 133, across read boundaries. Other control strings are
// opaque and OSC payloads are bounded; a TUI's text must not mark a prompt.
type promptTracker struct {
	state    byte
	body     []byte
	length   int
	atPrompt bool
}

func (p *promptTracker) feed(data []byte) {
	for _, b := range data {
		if b == 0x18 || b == 0x1a { // CAN / SUB
			p.state, p.length = 0, 0
			p.body = p.body[:0]
			continue
		}
		switch p.state {
		case 0:
			if b == 0x1b {
				p.state = 1
			}
		case 1:
			p.escape(b)
		case 2:
			p.length++
			switch b {
			case 7:
				p.finish()
				p.state = 0
			case 0x1b:
				p.state = 3
			default:
				if p.length <= 64 {
					p.body = append(p.body, b)
				}
			}
		case 3:
			if b == '\\' {
				p.finish()
				p.state = 0
			} else {
				p.escape(b) // ESC aborted the previous OSC.
			}
		case 4:
			if b == 0x1b {
				p.state = 5
			}
		case 5:
			if b == '\\' {
				p.state = 0
			} else if b != 0x1b {
				p.state = 4
			}
		}
	}
}

func (p *promptTracker) escape(b byte) {
	p.state = 0
	switch b {
	case ']':
		p.state, p.length, p.body = 2, 0, p.body[:0]
	case 'P', '_', '^', 'X': // DCS, APC, PM, SOS
		p.state = 4
	case 'c': // RIS
		p.atPrompt = false
	case 0x1b:
		p.state = 1
	}
}

func (p *promptTracker) finish() {
	if p.length > 64 {
		return
	}
	mark, ok := strings.CutPrefix(string(p.body), "133;")
	if !ok || mark == "" || len(mark) > 1 && mark[1] != ';' {
		return
	}
	switch mark[0] {
	case 'A', 'B', 'I', 'N', 'P':
		p.atPrompt = true
	case 'C', 'D':
		p.atPrompt = false
	}
}
