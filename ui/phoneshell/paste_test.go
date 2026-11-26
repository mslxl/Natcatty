package phoneshell

// The phone's one paste path. The touch bar, an external keyboard's paste key,
// and the system's Paste command all read the clipboard's text and PNG
// explicitly and hand them to pasteData, so a multi-line payload asks the same
// one confirmation however it arrived.

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"

	domainterminal "github.com/mslxl/natcatty/domain/terminal"
	"github.com/mslxl/natcatty/ui/terminal"
)

func hasFormat(d transfer.Data, format transfer.Format) bool {
	for _, f := range d.Formats() {
		if f == format {
			return true
		}
	}
	return false
}

// TestPhonePasteReadsOnlyTextAndPNG proves the touch paste control names the
// representations it can use, so an HTML copy alongside plain text never
// reaches the terminal, and an empty clipboard pastes nothing without an error.
func TestPhonePasteReadsOnlyTextAndPNG(t *testing.T) {
	handle := newFakeHandle("")
	screen := &fakeScreen{handle: handle}
	acc := &accessoryState{}
	tt := ui.NewTester(func(c *ui.Context) { accessoryBar(c, acc, screen, nil) }, 390, 300)
	tt.Frame()

	tt.SetClipboardData(transfer.New(transfer.NewItem(
		transfer.Bytes(transfer.Text, []byte("rich text")),
		transfer.Bytes(transfer.PNG, []byte("png bytes")),
		transfer.Bytes(transfer.HTML, []byte("<b>rich text</b>")),
	)))
	if err := tt.Click("Paste"); err != nil {
		t.Fatalf("click Paste: %v", err)
	}
	tt.Frame()
	pasted := screen.pasted
	if text, err := pasted.Read(transfer.Text); err != nil || string(text) != "rich text" {
		t.Fatalf("pasted text = %q (%v)", text, err)
	}
	if png, err := pasted.Read(transfer.PNG); err != nil || string(png) != "png bytes" {
		t.Fatalf("pasted PNG = %q (%v)", png, err)
	}
	if hasFormat(pasted, transfer.HTML) {
		t.Fatalf("the HTML representation reached the terminal: %v", pasted.Formats())
	}

	// MyGo returns empty data, not an error, for an empty clipboard. The phone
	// treats that as nothing to paste and sends nothing.
	screen.pasted = transfer.Data{}
	tt.SetClipboard("")
	if err := tt.Click("Paste"); err != nil {
		t.Fatalf("click Paste on an empty clipboard: %v", err)
	}
	tt.Frame()
	if len(screen.pasted.Items()) != 0 {
		t.Fatalf("an empty clipboard pasted %v", screen.pasted.Formats())
	}
}

// TestEveryPasteEntryPointSharesTheOneConfirmation drives the touch control,
// an external keyboard's paste chord, and the system Paste command against the
// same guarded paste. Each holds the payload for the one confirmation row, and
// none reaches the terminal before the user answers.
func TestEveryPasteEntryPointSharesTheOneConfirmation(t *testing.T) {
	guard := func(text string) bool { return domainterminal.ShouldConfirmPaste(text, true, 2) }
	screen, acc, tt, handle := newPasteTerminal(t, "ready\r\n", guard)
	screen.SetPasteTarget(func(services ui.Services, d transfer.Data) bool {
		return pasteData(services, screen, acc, d, guard)
	})

	// The touch control.
	tt.SetClipboard("one\ntwo\nthree")
	if err := tt.Click("Paste"); err != nil {
		t.Fatalf("click Paste: %v", err)
	}
	tt.Frame()
	if !tt.HasText("Paste 3 lines?") {
		t.Fatalf("the touch paste skipped the confirmation; texts: %q", tt.Texts())
	}
	if got := handle.takeWritten(); got != "" {
		t.Fatalf("the touch paste reached the terminal before confirmation: %q", got)
	}
	if err := tt.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	tt.Frame()
	if acc.pendingPaste != "" || len(acc.pendingData.Items()) != 0 {
		t.Fatal("Cancel kept the held paste")
	}

	// An external keyboard's paste key, through the terminal's own key hook.
	if !tt.Focused("Terminal") {
		tt.ClickAt(20, 400)
		tt.Frame()
	}
	tt.SetClipboard("one\ntwo\nthree")
	tt.Key(ui.Ctrl|ui.Shift, ui.KeyV)
	tt.Frame()
	if !tt.HasText("Paste 3 lines?") {
		t.Fatalf("the paste key skipped the confirmation; texts: %q", tt.Texts())
	}
	if got := handle.takeWritten(); got != "" {
		t.Fatalf("the paste key reached the terminal before confirmation: %q", got)
	}
	if err := tt.Click("Paste anyway"); err != nil {
		t.Fatalf("click Paste anyway: %v", err)
	}
	waitWritten(t, handle, "one\rtwo\rthree", "the confirmed paste key")
	if acc.pendingPaste != "" {
		t.Fatal("the confirmed paste stays held")
	}

	// The system's Paste command and an on-screen keyboard's paste.
	tt.SetClipboard("alpha\nbeta")
	if !screen.handleInput(ui.InputEvent{Kind: ui.InputCommand, Text: "paste"}) {
		t.Fatal("the system paste command was not taken")
	}
	tt.Frame()
	if !tt.HasText("Paste 2 lines?") {
		t.Fatalf("the system paste skipped the confirmation; texts: %q", tt.Texts())
	}
	if got := handle.takeWritten(); got != "" {
		t.Fatalf("the system paste reached the terminal before confirmation: %q", got)
	}
	if err := tt.Click("Paste anyway"); err != nil {
		t.Fatalf("click Paste anyway: %v", err)
	}
	waitWritten(t, handle, "alpha\rbeta", "the confirmed system paste")
}

// TestSystemPasteCommandReadsOnlyTextAndPNG covers the system path's clipboard
// read: the command hands the shared paste decision the text and PNG, never an
// HTML copy that came with them.
func TestSystemPasteCommandReadsOnlyTextAndPNG(t *testing.T) {
	screen, _, tt, _ := newPasteTerminal(t, "ready\r\n", nil)
	var captured transfer.Data
	screen.SetPasteTarget(func(_ ui.Services, d transfer.Data) bool {
		captured = d
		return true
	})
	tt.SetClipboardData(transfer.New(transfer.NewItem(
		transfer.Bytes(transfer.Text, []byte("rich")),
		transfer.Bytes(transfer.PNG, []byte("png")),
		transfer.Bytes(transfer.HTML, []byte("<b>rich</b>")),
	)))
	if !screen.handleInput(ui.InputEvent{Kind: ui.InputCommand, Text: "paste"}) {
		t.Fatal("the system paste command was not taken")
	}
	if text, err := captured.Read(transfer.Text); err != nil || string(text) != "rich" {
		t.Fatalf("system paste text = %q (%v)", text, err)
	}
	if png, err := captured.Read(transfer.PNG); err != nil || string(png) != "png" {
		t.Fatalf("system paste PNG = %q (%v)", png, err)
	}
	if hasFormat(captured, transfer.HTML) {
		t.Fatalf("the system paste kept HTML: %v", captured.Formats())
	}
}

// TestPhonePasteKeysFollowTheTerminalKeyTable proves the touch control and the
// hardware keys agree: the paste chords are the terminal key table's effective
// ActionPaste bindings, including a moved binding and an unbound default.
func TestPhonePasteKeysFollowTheTerminalKeyTable(t *testing.T) {
	var defaults []terminal.Keybind
	for _, bind := range terminal.DefaultKeybinds() {
		if bind.Action == terminal.ActionPaste {
			defaults = append(defaults, bind)
		}
	}
	if len(defaults) == 0 {
		t.Fatal("MyGo's terminal has no default paste key")
	}
	keys := phonePasteKeys(nil)
	for _, bind := range defaults {
		if !keys[phoneChordKey{mods: bind.Mods, key: bind.Key}] {
			t.Fatalf("the default paste chord %v+%v is not recognized", bind.Mods, bind.Key)
		}
	}

	moved := phonePasteKeys([]terminal.Keybind{
		{Mods: ui.Ctrl | ui.Alt, Key: ui.KeyP, Action: terminal.ActionPaste},
	})
	if !moved[phoneChordKey{mods: ui.Ctrl | ui.Alt, key: ui.KeyP}] {
		t.Fatal("a moved paste binding is not recognized")
	}

	for _, bind := range defaults {
		unbound := phonePasteKeys([]terminal.Keybind{
			{Mods: bind.Mods, Key: bind.Key, Action: terminal.ActionNone},
		})
		if unbound[phoneChordKey{mods: bind.Mods, key: bind.Key}] {
			t.Fatalf("the unbound default %v+%v still pastes", bind.Mods, bind.Key)
		}
		replaced := phonePasteKeys([]terminal.Keybind{
			{Mods: bind.Mods, Key: bind.Key, Action: terminal.ActionCopy},
		})
		if replaced[phoneChordKey{mods: bind.Mods, key: bind.Key}] {
			t.Fatalf("the replaced default %v+%v still pastes", bind.Mods, bind.Key)
		}
	}
}

// newPasteTerminal builds a real terminal in the raw view with both the key and
// paste hooks, and the accessory bar above it, as the session page draws them.
// It returns the shared latch state so a test can see a held paste.
func newPasteTerminal(t *testing.T, output string, guard func(string) bool) (*ghosttyScreen, *accessoryState, *ui.Tester, *fakeHandle) {
	t.Helper()
	handle := newFakeHandle(output)
	screen := &ghosttyScreen{}
	term, err := terminal.New(terminal.Options{
		Conn:    handle,
		OnKey:   screen.onKey,
		OnPaste: screen.onPaste,
	})
	if err != nil {
		t.Skipf("libghostty-vt unavailable: %v", err)
	}
	t.Cleanup(func() { _ = term.Close() })
	screen.term = term
	// The runtime's own paste chords differ by platform; name one explicitly so
	// the test proves interception rather than MyGo's table.
	screen.pasteKeys = map[phoneChordKey]bool{
		{mods: ui.Ctrl | ui.Shift, key: ui.KeyV}: true,
	}
	acc := &accessoryState{}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Fill().Children(func() {
			accessoryBar(c, acc, screen, guard)
			screen.View(c).Fill()
		})
	}, 390, 500)
	tt.Frame()
	deadline := time.Now().Add(5 * time.Second)
	for strings.TrimSpace(term.Text()) == "" {
		if time.Now().After(deadline) {
			t.Fatalf("terminal text = %q, want the Session's output", term.Text())
		}
		tt.Frame()
	}
	tt.ClickAt(20, 400)
	tt.Frame()
	if !tt.Focused("Terminal") {
		t.Fatal("the terminal did not take the keyboard focus")
	}
	return screen, acc, tt, handle
}
