package appview

import (
	"strings"
	"sync"

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
	// echo is set while the key just dispatched carried its own encoding.
	// A platform may commit the characters a key types separately from its
	// key down, and a key the Pane already sent as an escape sequence must
	// not send those characters a second time. On macOS that is what
	// happens to an Option-modified letter: the Pane sends Meta-letter, and
	// AppKit's interpretKeyEvents: then commits the composed character
	// (Option-A is å) through insertText:, which ReplaceText would pass on
	// as a second write. Tab, Return and Escape are command selectors
	// there and commit no text, and mygo drops Windows's control-character
	// messages, so those keys never echo. Keys the Pane leaves to the
	// platform (a plain letter) clear the mark, so their text still takes
	// the ordinary path, and a key that commits an input-method
	// composition never sets it: that text is the user's.
	echo bool
}

// NewInputClient makes a client that sends committed text with send.
func NewInputClient(send func([]byte)) *InputClient {
	return &InputClient{send: send}
}

// keyEcho is the part of the text-input client that keeps the characters of a
// key the Pane already encoded out of the program. InputClient implements it;
// the Pane only needs the methods, so another client can replace it.
type keyEcho interface {
	NoteKey(encoded bool)
	NoteKeyUp()
	TakeEcho() bool
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

// NoteKey records whether the key event being dispatched was encoded by the
// Pane itself. The text the key types may follow as a commit; when the key
// already reached the program as its escape sequence, that commit is the same
// key and TakeEcho drops it. A key left to the platform clears the mark, so
// the text it types is sent as committed text.
func (c *InputClient) NoteKey(encoded bool) {
	c.mu.Lock()
	// A key pressed while a composition is in force commits that composition:
	// Return ends an input-method composition, and the characters it commits
	// are the user's text, not this key's echo. Keep sending them even though
	// the key down carried its own encoding.
	c.echo = encoded && c.marked == ""
	c.mu.Unlock()
}

// NoteKeyUp ends the key. A key that produced no text must not make the next
// commit — an input method's, later — look like its echo.
func (c *InputClient) NoteKeyUp() {
	c.mu.Lock()
	c.echo = false
	c.mu.Unlock()
}

// TakeEcho reports whether the next committed text is the characters of a key
// the Pane already encoded, and clears the mark. It is how the Pane's own
// InputText path drops the duplicate on platforms that deliver text to the
// element instead of the client. A composition is never an echo: its commit
// sends the user's text.
func (c *InputClient) TakeEcho() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	echo := c.echo && c.marked == ""
	c.echo = false
	return echo
}

// ReplaceText commits: whatever replaced what, the text goes to the program now.
// An input method commits with a replacement of the composition range, and a plain
// send is what a terminal needs. Text the key's own key down already sent is not
// sent again; see NoteKey.
func (c *InputClient) ReplaceText(_ *ui.TextInputRange, s string) {
	c.mu.Lock()
	c.marked = ""
	echo := c.echo
	c.echo = false
	c.mu.Unlock()
	if echo || s == "" {
		return
	}
	if c.send != nil {
		c.send([]byte(s))
	}
}

// SetMarkedText records the composition the system is showing. A composition is
// new text, not the echo of the key that started it, so it ends any key echo.
func (c *InputClient) SetMarkedText(_ *ui.TextInputRange, s string, _ ui.TextInputRange) {
	c.mu.Lock()
	c.marked = s
	c.echo = false
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
