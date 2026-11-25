package sessionui

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/mslxl/natcatty/ui/terminal"
)

// The Edit menu's Paste arrives as an InputCommand in an input handler,
// outside any frame. Context.ReadClipboard is frame-gated and returns ""
// there, so the handler reads the clipboard through Services; the text then
// goes through the paste gate instead of straight to the terminal.
func TestTerminalPasteCommandReachesTheShell(t *testing.T) {
	_, transport, tester := newSelectionSurface(t)
	tester.SetClipboard("hello from the clipboard")
	tester.Command("paste")
	tester.Frame()
	if got := transport.Input(); !strings.Contains(got, "hello from the clipboard") {
		t.Fatalf("Edit › Paste sent %q to the shell, want the clipboard text", got)
	}
}

// The drawn grid does not run the terminal plugin's keybind dispatch, so a
// paste chord used to be encoded as raw bytes (Ctrl+Shift+V as 0x16,
// literal-next). The chord must paste instead, honoring the user's bindings.
func TestTerminalPasteChordReachesTheShell(t *testing.T) {
	surface, transport := newScriptedSurface(t, WithTerminalKeybinds(func() []terminal.Keybind {
		return []terminal.Keybind{
			{Mods: ui.Ctrl | ui.Shift, Key: ui.KeyV, Action: terminal.ActionPaste},
		}
	}))
	tester := ui.NewTester(func(c *ui.Context) { surfaceFrame(c, surface, true) }, 900, 600)
	tester.SetLocale("en")
	for i := 0; i < 4; i++ {
		tester.Frame()
	}
	tester.SetClipboard("paste by chord")
	tester.Key(ui.Ctrl|ui.Shift, ui.KeyV)
	tester.Frame()
	got := transport.Input()
	if !strings.Contains(got, "paste by chord") {
		t.Fatalf("Ctrl+Shift+V sent %q to the shell, want the clipboard text", got)
	}
	if strings.ContainsRune(got, '\x16') {
		t.Fatalf("the paste chord still reached the shell as literal-next: %q", got)
	}
}

// A paste that the multiline policy guards must send nothing until Accept,
// exactly like the Session menu's Paste. The Cmd+V path used to call
// Terminal.Paste directly and skipped this.
func TestTerminalPasteCommandHonoursTheGate(t *testing.T) {
	surface, transport := newScriptedSurface(t, WithPastePolicy(func() (bool, int) {
		return true, 2
	}))
	tester := ui.NewTester(func(c *ui.Context) { surfaceFrame(c, surface, true) }, 900, 600)
	tester.SetLocale("en")
	for i := 0; i < 4; i++ {
		tester.Frame()
	}

	tester.SetClipboard("a\r\nb\r\n")
	tester.Command("paste")
	tester.Frame()
	if got := transport.Input(); strings.Contains(got, "a") {
		t.Fatalf("a guarded paste reached the shell: %q", got)
	}
	pending, ok := surface.gate.Pending()
	if !ok {
		t.Fatal("the guarded paste was not held for confirmation")
	}
	// Line endings are normalized at the gate, before the policy and the send;
	// the trailing newline is part of what the user copied.
	if pending.Text != "a\nb\n" {
		t.Fatalf("pending paste = %q, want the normalized %q", pending.Text, "a\nb\n")
	}

	if !surface.gate.Accept() {
		t.Fatal("Accept found no pending paste")
	}
	// The terminal queues the paste for its input loop; pump the frames that
	// hand it to the connection.
	for i := 0; i < 4; i++ {
		tester.Frame()
	}
	// MyGo's paste encoder turns each LF into a carriage return for the wire.
	if got := transport.Input(); !strings.Contains(got, "a\rb") {
		t.Fatalf("Accept sent %q to the shell, want the normalized paste", got)
	}
}
