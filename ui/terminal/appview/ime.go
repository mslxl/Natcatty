package appview

import (
	"strings"
	"sync"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

// InputClient is the text-input target the live screen offers to the system's input
// methods. The program draws its own input line (the shell's line editor is inside
// the VT), so composition is not inserted into the grid: the marked text is held
// here and the system's candidate window shows it, then the committed text goes to
// the program. That is how a terminal takes input-method text.
type InputClient struct {
	send func([]byte)

	mu     sync.Mutex
	marked string // the composition; ranges count UTF-16 units of it
}

// NewInputClient makes a client that sends committed text with send.
func NewInputClient(send func([]byte)) *InputClient {
	return &InputClient{send: send}
}

// Marked returns the composition text in force, for tests and the app.
func (c *InputClient) Marked() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.marked
}

// TextForRange returns the document text of a range; the document is the marked
// text, so every range is clamped to it.
func (c *InputClient) TextForRange(r ui.TextInputRange) (string, ui.TextInputRange) {
	c.mu.Lock()
	defer c.mu.Unlock()
	start, end := c.clamp(r)
	return substringUTF16(c.marked, start, end), ui.TextInputRange{Start: start, End: end}
}

// Selection reports the caret at the end of the marked text.
func (c *InputClient) Selection() ui.TextInputSelection {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := ui.UTF16Len(c.marked)
	return ui.TextInputSelection{Range: ui.TextInputRange{Start: n, End: n}}
}

// MarkedRange reports the composition range when one is in force.
func (c *InputClient) MarkedRange() (ui.TextInputRange, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.marked == "" {
		return ui.TextInputRange{}, false
	}
	return ui.TextInputRange{Start: 0, End: ui.UTF16Len(c.marked)}, true
}

// ReplaceText commits: whatever replaced what, the text goes to the program now.
// An input method commits with a replacement of the composition range, and a plain
// send is what a terminal needs.
func (c *InputClient) ReplaceText(_ *ui.TextInputRange, s string) {
	c.mu.Lock()
	c.marked = ""
	c.mu.Unlock()
	if s != "" && c.send != nil {
		c.send([]byte(s))
	}
}

// SetMarkedText records the composition the system is showing.
func (c *InputClient) SetMarkedText(_ *ui.TextInputRange, s string, _ ui.TextInputRange) {
	c.mu.Lock()
	c.marked = s
	c.mu.Unlock()
}

// UnmarkText ends the composition without committing anything.
func (c *InputClient) UnmarkText() {
	c.mu.Lock()
	c.marked = ""
	c.mu.Unlock()
}

// BoundsForRange has no rectangle to report: the composition is drawn by the system,
// not by this flow.
func (c *InputClient) BoundsForRange(r ui.TextInputRange) (ui.Rect, ui.TextInputRange, bool) {
	return ui.Rect{}, r, false
}

// IndexForPoint has no text layout to hit-test.
func (c *InputClient) IndexForPoint(ui.Point) (int, bool) { return 0, false }

func (c *InputClient) clamp(r ui.TextInputRange) (int, int) {
	n := ui.UTF16Len(c.marked)
	start, end := r.Start, r.End
	if start < 0 {
		start = 0
	}
	if end < start {
		end = start
	}
	if start > n {
		start = n
	}
	if end > n {
		end = n
	}
	return start, end
}

// substringUTF16 returns the text between two UTF-16 offsets, keeping a surrogate
// pair together when a range splits it.
func substringUTF16(s string, start, end int) string {
	if start >= end {
		return ""
	}
	var b strings.Builder
	units := 0
	for _, r := range s {
		u := ui.UTF16Len(string(r))
		if units+u > start && units < end {
			b.WriteRune(r)
		}
		units += u
		if units >= end {
			break
		}
	}
	return b.String()
}

// clients keeps one input client per terminal, across frames.
var (
	clientsMu sync.Mutex
	clients   = map[*terminal.Terminal]*InputClient{}
)

// clientFor returns the input client of a terminal, making it on first use.
func clientFor(term *terminal.Terminal) *InputClient {
	clientsMu.Lock()
	defer clientsMu.Unlock()
	if c, ok := clients[term]; ok {
		return c
	}
	c := NewInputClient(func(b []byte) { term.Send(b) })
	clients[term] = c
	return c
}
