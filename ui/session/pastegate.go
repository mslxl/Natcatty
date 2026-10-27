package sessionui

import (
	"sync"

	domainterminal "github.com/mslxl/natcatty/domain/terminal"
)

// PendingPaste is a paste waiting for the user's decision. Text is exactly
// what Send will deliver.
type PendingPaste struct {
	Text      string
	Lines     int
	Chars     int
	Preview   string
	Truncated bool
}

// PasteGate applies the multiline paste policy to every paste path: the
// terminal's own paste key and menu, the Session menu, and the clipboard
// banner. A guarded paste is stored without sending anything. Accept sends the
// exact stored text once; Cancel sends nothing. A newer paste supersedes the
// waiting one as the original did, and the superseded text is never sent.
type PasteGate struct {
	mu     sync.Mutex
	policy func() (protection bool, minLines int)
	send   func(string)
	holder *PendingPaste
}

// NewPasteGate returns a gate that asks policy for the current Settings and
// delivers an accepted text with send.
func NewPasteGate(policy func() (bool, int), send func(string)) *PasteGate {
	if policy == nil {
		policy = func() (bool, int) { return false, domainterminal.DefaultPasteMinLines }
	}
	if send == nil {
		send = func(string) {}
	}
	return &PasteGate{policy: policy, send: send}
}

// Request applies the policy to text. It reports true when the text was sent
// immediately, false when it is waiting for Accept or Cancel.
//
// Request normalizes line endings exactly once, before the policy or any send.
// This is the single input boundary shared by the terminal's paste key, the
// Session menu, and the clipboard banner, so a Windows CRLF or an old-Mac CR
// reaches every Session as one LF per line. MyGo's terminal encoder turns each
// LF into a carriage return for the wire; without this normalization a CRLF
// would become CR CR and open a blank line.
func (g *PasteGate) Request(text string) bool {
	text = domainterminal.NormalizePasteText(text)
	protection, minLines := g.policy()
	if !domainterminal.ShouldConfirmPaste(text, protection, minLines) {
		g.mu.Lock()
		g.holder = nil
		send := g.send
		g.mu.Unlock()
		send(text)
		return true
	}
	preview, truncated := domainterminal.PreviewPaste(text, 320)
	pending := &PendingPaste{
		Text:      text,
		Lines:     domainterminal.PasteLineCount(text),
		Chars:     len([]rune(text)),
		Preview:   preview,
		Truncated: truncated,
	}
	g.mu.Lock()
	g.holder = pending
	g.mu.Unlock()
	return false
}

// Pending returns a copy of the waiting paste, if there is one.
func (g *PasteGate) Pending() (PendingPaste, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.holder == nil {
		return PendingPaste{}, false
	}
	return *g.holder, true
}

// Accept sends the waiting text exactly once and clears it. It reports false
// when nothing was waiting.
func (g *PasteGate) Accept() bool {
	g.mu.Lock()
	pending := g.holder
	g.holder = nil
	send := g.send
	g.mu.Unlock()
	if pending == nil {
		return false
	}
	send(pending.Text)
	return true
}

// Cancel discards the waiting text without sending any part of it.
func (g *PasteGate) Cancel() bool {
	g.mu.Lock()
	pending := g.holder
	g.holder = nil
	g.mu.Unlock()
	return pending != nil
}
