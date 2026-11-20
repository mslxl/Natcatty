package appview

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// The platform commits the characters a key types after its key down. For a
// key the Pane encoded itself — Tab, Return, Escape, a modified key — that
// commit is the same key and must not reach the program twice. Without this a
// Tab that accepted a completion inserted a literal tab after the accepted
// line.
func TestInputClientDropsTheTextOfAKeyThePaneAlreadySent(t *testing.T) {
	var sent []string
	c := NewInputClient(func(b []byte) { sent = append(sent, string(b)) })

	c.NoteKey(true)
	c.ReplaceText(nil, "\t")
	if len(sent) != 0 {
		t.Fatalf("an encoded key's commit was sent again: %q", sent)
	}

	// The echo covers the commit that follows the key, not later typing: once
	// the key is up, the next text is ordinary input.
	c.NoteKeyUp()
	c.ReplaceText(nil, "x")
	if len(sent) != 1 || sent[0] != "x" {
		t.Fatalf("text after the keyup reached %q, want x", sent)
	}
}

// A key the Pane leaves to the platform (a plain letter) types text through
// the client, so its commit must be sent.
func TestInputClientSendsTheTextOfAKeyThePaneLeftToThePlatform(t *testing.T) {
	var sent []string
	c := NewInputClient(func(b []byte) { sent = append(sent, string(b)) })

	c.NoteKey(false)
	c.ReplaceText(nil, "g")
	if len(sent) != 1 || sent[0] != "g" {
		t.Fatalf("a plain key's text reached %q, want g", sent)
	}
}

// An input method's commit is the user's text, not a key echo, even when the
// key that commits it (Return) is one the Pane encodes. Pressing Enter to
// confirm "日本" must send the text and not just the carriage return.
func TestInputClientSendsACompositionCommit(t *testing.T) {
	var sent []string
	c := NewInputClient(func(b []byte) { sent = append(sent, string(b)) })

	c.SetMarkedText(nil, "にほん", ui.TextInputRange{Start: 0, End: 3})
	c.NoteKey(true) // Return, which the Pane encodes as "\r"
	c.ReplaceText(nil, "日本")
	if len(sent) != 1 || sent[0] != "日本" {
		t.Fatalf("composition commit reached %q, want 日本", sent)
	}
	if c.Marked() != "" {
		t.Errorf("the composition survived the commit: %q", c.Marked())
	}
}

// TakeEcho is the Pane's InputText path and is one-shot, as the client's own
// ReplaceText is.
func TestInputClientTakeEchoIsOneShot(t *testing.T) {
	c := NewInputClient(nil)

	c.NoteKey(true)
	if !c.TakeEcho() {
		t.Fatal("the first commit was not recognized as the key's echo")
	}
	if c.TakeEcho() {
		t.Fatal("a second commit was dropped as the same key's echo")
	}

	// A composition commit is not an echo even when a key was marked encoded.
	c.SetMarkedText(nil, "に", ui.TextInputRange{Start: 0, End: 1})
	c.NoteKey(true)
	if c.TakeEcho() {
		t.Fatal("a composition commit was taken for a key echo")
	}
}
