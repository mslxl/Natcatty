// SPDX-License-Identifier: GPL-3.0-or-later

package terminal

// Complete is Terminal::Complete: a UTF8Parser feeding an Emulator, with the
// display encoder and echo-ack timing stock keeps alongside it.
type Complete struct {
	parser       UTF8Parser
	terminal     *Emulator
	display      *Display
	actions      []action
	inputHistory []inputHistoryEntry
	echoAck      uint64
}

type inputHistoryEntry struct {
	frame uint64
	time  uint64
}

const echoTimeout = 50 // ms, for a late ack

// NewComplete returns a Complete terminal of the given size. Its display uses
// the default (MOSH_NO_TERM_INIT-style) capabilities; the client constructs its
// own Display from the local renderer's capabilities.
func NewComplete(width, height int) *Complete {
	return &Complete{
		terminal: NewEmulator(width, height),
		display:  NewDisplay(DefaultDisplayOptions()),
	}
}

// Act feeds a string of terminal octets and returns the terminal-to-host reply
// stream generated while parsing them.
func (c *Complete) Act(s string) string {
	for i := 0; i < len(s); i++ {
		c.actions = c.parser.Input(s[i], c.actions[:0])
		for _, a := range c.actions {
			c.terminal.act(a)
		}
	}
	return c.terminal.ReadOctetsToHost()
}

// Clone returns a deep copy, as the C++ copy constructor stock's transport
// uses when it applies a diff to a reference state. The parser and dispatcher
// state are copied because a host-state boundary can fall inside a control
// sequence.
func (c *Complete) Clone() *Complete {
	return &Complete{
		parser:       c.parser,
		terminal:     c.terminal.Clone(),
		display:      c.display,
		inputHistory: append([]inputHistoryEntry(nil), c.inputHistory...),
		echoAck:      c.echoAck,
	}
}

// Framebuffer returns the current framebuffer. It must not be mutated by
// callers and must be cloned before keeping it across calls.
func (c *Complete) Framebuffer() *Framebuffer { return c.terminal.Framebuffer() }

// CloneFramebuffer returns a deep copy of the current framebuffer.
func (c *Complete) CloneFramebuffer() *Framebuffer {
	return c.terminal.CloneFramebuffer()
}

// Display returns the display encoder held by this Complete.
func (c *Complete) Display() *Display { return c.display }

// ResetInput resets the parser state, as Complete::reset_input.
func (c *Complete) ResetInput() { c.parser.Reset() }

// Resize resizes the terminal, as a remote resize instruction.
func (c *Complete) Resize(width, height int) { c.terminal.Resize(width, height) }

// EchoAck returns the newest input frame the server has echoed.
func (c *Complete) EchoAck() uint64 { return c.echoAck }

// ApplyEchoAck records an echoed input frame from a host instruction. Stock
// asserts that the number only grows; a stale value is ignored here so a
// misbehaving server cannot panic the client. It reports whether the ack
// changed.
func (c *Complete) ApplyEchoAck(frame uint64) bool {
	if frame < c.echoAck {
		return false
	}
	changed := frame != c.echoAck
	c.echoAck = frame
	return changed
}

// SetEchoAck recomputes the echo ack from the input history. It returns true
// when the ack changed.
func (c *Complete) SetEchoAck(now uint64) bool {
	var newestEchoAck uint64
	for _, entry := range c.inputHistory {
		if entry.time <= now-echoTimeout {
			newestEchoAck = entry.frame
		}
	}

	kept := c.inputHistory[:0]
	for _, entry := range c.inputHistory {
		if entry.frame >= newestEchoAck {
			kept = append(kept, entry)
		}
	}
	c.inputHistory = kept

	changed := c.echoAck != newestEchoAck
	c.echoAck = newestEchoAck
	return changed
}

// RegisterInputFrame records that input frame n was sent at time now.
func (c *Complete) RegisterInputFrame(n, now uint64) {
	c.inputHistory = append(c.inputHistory, inputHistoryEntry{frame: n, time: now})
}

// WaitTime returns milliseconds until the next echo timeout, or a very large
// value when no timed history entry exists.
func (c *Complete) WaitTime(now uint64) int {
	if len(c.inputHistory) < 2 {
		return int(^uint(0) >> 1)
	}
	next := c.inputHistory[1].time + echoTimeout
	if next <= now {
		return 0
	}
	return int(next - now)
}

// Equal reports whether two Complete terminals are equal, as
// Complete::operator==: same framebuffer and echo ack.
func (c *Complete) Equal(other *Complete) bool {
	return c.terminal.Framebuffer().Equal(other.terminal.Framebuffer()) &&
		c.echoAck == other.echoAck
}
