package phoneshell

// The phone's external keyboard: the desktop's application bindings run on the
// phone, minus the actions that have no phone surface; the focused terminal
// keeps its reserved keys; and Android's keyless modifier events and composed
// Alt text are read by physical position rather than by the text they carry.

import (
	"testing"

	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"

	domainkeybindings "github.com/mslxl/natcatty/domain/keybindings"
	"github.com/mslxl/natcatty/ui/terminal"
)

// phoneKeyInputAndroidQuirks are the exact events Android's keyboard bridge
// produces for the chords the ticket names: Ctrl and Meta combinations arrive
// with no key name and the text U+0000, while Alt combinations arrive as the
// composed character the layout would type.
func TestPhoneKeyInputAndroidKeyboardQuirks(t *testing.T) {
	cases := []struct {
		name string
		ev   ui.InputEvent
		mods ui.Modifiers
		key  ui.Key
		text string
		ok   bool
	}{
		{
			name: "Ctrl+C arrives keyless with U+0000",
			ev:   ui.InputEvent{Kind: ui.InputKeyDown, PhysicalKey: ui.KeyC, Mods: ui.Ctrl, Text: "\u0000"},
			mods: ui.Ctrl, key: ui.KeyC, text: "", ok: true,
		},
		{
			name: "Meta+V arrives keyless with U+0000",
			ev:   ui.InputEvent{Kind: ui.InputKeyDown, PhysicalKey: ui.KeyV, Mods: ui.Super, Text: "\u0000"},
			mods: ui.Super, key: ui.KeyV, text: "", ok: true,
		},
		{
			name: "Alt+C arrives as composed text",
			ev:   ui.InputEvent{Kind: ui.InputKeyDown, PhysicalKey: ui.KeyC, Mods: ui.Alt, Text: "ç"},
			mods: ui.Alt, key: ui.KeyC, text: "ç", ok: true,
		},
		{
			name: "a named key still matches its physical position",
			ev:   ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.KeyC, PhysicalKey: ui.KeyC, Mods: ui.Ctrl, Text: "\u0000"},
			mods: ui.Ctrl, key: ui.KeyC, text: "", ok: true,
		},
		{
			name: "a bare modifier names no key",
			ev:   ui.InputEvent{Kind: ui.InputKeyDown, Mods: ui.Ctrl, Text: "\u0000"},
			mods: ui.Ctrl, key: ui.KeyUnknown, text: "", ok: false,
		},
		{
			name: "composed text alone never becomes a shortcut",
			ev:   ui.InputEvent{Kind: ui.InputText, Mods: ui.Alt, Text: "ç"},
			mods: ui.Alt, key: ui.KeyUnknown, text: "ç", ok: false,
		},
		{
			name: "plain text names its letter",
			ev:   ui.InputEvent{Kind: ui.InputText, Text: "c"},
			mods: 0, key: ui.KeyC, text: "c", ok: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mods, key, text, ok := phoneKeyInput(tc.ev)
			if mods != tc.mods || key != tc.key || text != tc.text || ok != tc.ok {
				t.Fatalf("phoneKeyInput = (%v, %v, %q, %v), want (%v, %v, %q, %v)",
					mods, key, text, ok, tc.mods, tc.key, tc.text, tc.ok)
			}
		})
	}
}

// TestHandleInputUsesThePhysicalKeyForAndroidChords proves the screen's input
// hook matches a keyless Android chord by position, drops the U+0000 artifact,
// and leaves composed Alt text to the terminal when no binding takes the key.
func TestHandleInputUsesThePhysicalKeyForAndroidChords(t *testing.T) {
	screen := &ghosttyScreen{}
	newTabs := 0
	screen.SetKeyboard(&phoneKeyboard{bindings: []phoneShortcut{
		{mods: ui.Ctrl, key: ui.KeyT, run: func() { newTabs++ }},
	}})

	// Ctrl+T with no key name and the U+0000 artifact still runs New Tab.
	if !screen.handleInput(ui.InputEvent{
		Kind: ui.InputKeyDown, PhysicalKey: ui.KeyT, Mods: ui.Ctrl, Text: "\u0000",
	}) {
		t.Fatal("Ctrl+T with no key name was not dispatched")
	}
	if newTabs != 1 {
		t.Fatalf("New Tab ran %d times, want 1", newTabs)
	}
	// The platform's text event for the same key must not be typed as well.
	if !screen.handleInput(ui.InputEvent{Kind: ui.InputText, Text: "\u0000"}) {
		t.Fatal("the U+0000 artifact was left to the terminal")
	}

	// Alt+C with no binding is the terminal's: the composed text stays, and the
	// physical key is not mistaken for a plain C shortcut.
	screen.SetKeyboard(&phoneKeyboard{bindings: []phoneShortcut{
		{mods: 0, key: ui.KeyC, run: func() { newTabs++ }},
	}})
	if screen.handleInput(ui.InputEvent{
		Kind: ui.InputKeyDown, PhysicalKey: ui.KeyC, Mods: ui.Alt, Text: "ç",
	}) {
		t.Fatal("Alt+C ran the unmodified C binding")
	}
	if newTabs != 1 {
		t.Fatalf("the unmodified binding ran: New Tab count = %d", newTabs)
	}
	if screen.handleInput(ui.InputEvent{Kind: ui.InputText, Text: "ç"}) {
		t.Fatal("the composed Alt text was swallowed")
	}

	// Releasing a handled key ends its echo window: the next text the user
	// types on the on-screen keyboard is not mistaken for the key's own.
	screen.SetKeyboard(&phoneKeyboard{bindings: []phoneShortcut{
		{mods: ui.Ctrl, key: ui.KeyT, run: func() { newTabs++ }},
	}})
	if !screen.handleInput(ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.KeyT, Mods: ui.Ctrl}) {
		t.Fatal("Ctrl+T was not dispatched")
	}
	if screen.handleInput(ui.InputEvent{Kind: ui.InputKeyUp, Key: ui.KeyT, Mods: ui.Ctrl}) {
		t.Fatal("the key release was swallowed")
	}
	if screen.handleInput(ui.InputEvent{Kind: ui.InputText, Text: "a"}) {
		t.Fatal("the text typed after the key release was swallowed")
	}
}

// keyboardCalls records the application actions a dispatched chord ran.
type keyboardCalls struct {
	switched    []int
	next, prev  int
	newTab      int
	closeTab    int
	closePane   int
	focus       []ui.Key
	splits      []bool
	hosts       int
	settings    int
	historyOpen int
}

func (c *keyboardCalls) actions() phoneShortcutActions {
	return phoneShortcutActions{
		SwitchTab:    func(number int) { c.switched = append(c.switched, number) },
		NextTab:      func() { c.next++ },
		PrevTab:      func() { c.prev++ },
		NewTab:       func() { c.newTab++ },
		CloseTab:     func() { c.closeTab++ },
		ClosePane:    func() { c.closePane++ },
		MoveFocus:    func(key ui.Key) { c.focus = append(c.focus, key) },
		SplitPane:    func(vertical bool) { c.splits = append(c.splits, vertical) },
		OpenHosts:    func() { c.hosts++ },
		OpenSettings: func() { c.settings++ },
		History:      func() { c.historyOpen++ },
	}
}

// TestPhoneKeyboardDispatchesTheDesktopBindings walks the application bindings
// the catalog gives the phone and proves each chord runs its action.
func TestPhoneKeyboardDispatchesTheDesktopBindings(t *testing.T) {
	calls := &keyboardCalls{}
	keyboard := phoneKeyboardFor(newShortcutService(t), calls.actions())

	cases := []struct {
		name string
		mods ui.Modifiers
		key  ui.Key
		ran  func(t *testing.T)
	}{
		{"numbered tab", ui.Ctrl, ui.Key1, func(t *testing.T) {
			if len(calls.switched) != 1 || calls.switched[0] != 1 {
				t.Fatalf("switch-tab ran %v, want [1]", calls.switched)
			}
		}},
		{"next tab", ui.Ctrl, ui.KeyTab, func(t *testing.T) {
			if calls.next != 1 {
				t.Fatalf("next-tab ran %d times", calls.next)
			}
		}},
		{"previous tab", ui.Ctrl | ui.Shift, ui.KeyTab, func(t *testing.T) {
			if calls.prev != 1 {
				t.Fatalf("prev-tab ran %d times", calls.prev)
			}
		}},
		{"new tab", ui.Ctrl, ui.KeyT, func(t *testing.T) {
			if calls.newTab != 1 {
				t.Fatalf("new-tab ran %d times", calls.newTab)
			}
		}},
		{"close tab", ui.Ctrl, ui.KeyW, func(t *testing.T) {
			if calls.closeTab != 1 {
				t.Fatalf("close-tab ran %d times", calls.closeTab)
			}
		}},
		{"close pane", ui.Ctrl | ui.Shift, ui.KeyW, func(t *testing.T) {
			if calls.closePane != 1 {
				t.Fatalf("close-session ran %d times", calls.closePane)
			}
		}},
		{"move focus left", ui.Ctrl | ui.Alt, ui.KeyLeft, func(t *testing.T) {
			if len(calls.focus) == 0 || calls.focus[len(calls.focus)-1] != ui.KeyLeft {
				t.Fatalf("move-focus ran with %v", calls.focus)
			}
		}},
		{"split horizontal", ui.Ctrl | ui.Shift, ui.KeyD, func(t *testing.T) {
			if len(calls.splits) == 0 || calls.splits[len(calls.splits)-1] {
				t.Fatalf("split-horizontal ran with %v", calls.splits)
			}
		}},
		{"split vertical", ui.Ctrl | ui.Shift, ui.KeyE, func(t *testing.T) {
			if len(calls.splits) == 0 || !calls.splits[len(calls.splits)-1] {
				t.Fatalf("split-vertical ran with %v", calls.splits)
			}
		}},
		{"settings", ui.Ctrl, ui.KeyComma, func(t *testing.T) {
			if calls.settings != 1 {
				t.Fatalf("open-settings ran %d times", calls.settings)
			}
		}},
		{"history search", ui.Ctrl | ui.Shift, ui.KeyH, func(t *testing.T) {
			if calls.historyOpen != 1 {
				t.Fatalf("history-search ran %d times", calls.historyOpen)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !keyboard.run(tc.mods, tc.key) {
				t.Fatalf("%v+%v was not dispatched", tc.mods, tc.key)
			}
			tc.ran(t)
		})
	}

	// The ninth tab is the end of the range pattern.
	if !keyboard.run(ui.Ctrl, ui.Key9) {
		t.Fatal("Ctrl+9 was not dispatched")
	}
	if got := calls.switched[len(calls.switched)-1]; got != 9 {
		t.Fatalf("Ctrl+9 switched to %d, want 9", got)
	}
}

// TestPhoneKeyboardSkipsActionsWithNoPhoneSurface proves a desktop-only or
// desktop-surface binding never claims its chord: the key falls through to the
// terminal instead of running nothing.
func TestPhoneKeyboardSkipsActionsWithNoPhoneSurface(t *testing.T) {
	calls := &keyboardCalls{}
	keyboard := phoneKeyboardFor(newShortcutService(t), calls.actions())

	chords := []struct {
		name string
		mods ui.Modifiers
		key  ui.Key
	}{
		{"toggle opacity", ui.Ctrl | ui.Shift, ui.KeyU},
		{"new workspace", ui.Ctrl | ui.Shift, ui.KeyJ},
		{"open sftp", ui.Ctrl | ui.Shift, ui.KeyO},
		{"port forwarding", ui.Ctrl, ui.KeyP},
		{"command palette", ui.Ctrl, ui.KeyK},
		{"quick switch", ui.Ctrl, ui.KeyJ},
		{"broadcast", ui.Ctrl, ui.KeyB},
		{"snippets", ui.Ctrl | ui.Shift, ui.KeyS},
		{"transfer center", ui.Ctrl | ui.Shift, ui.KeyT},
		{"toggle host list", ui.Ctrl | ui.Shift, ui.KeyB},
		{"toggle side panel", ui.Ctrl, ui.KeyBackslash},
		{"toggle pane zoom", ui.Alt, ui.KeyM},
		// Terminal-category bindings belong to the terminal's own key table.
		{"copy", ui.Ctrl | ui.Shift, ui.KeyC},
		{"paste", ui.Ctrl | ui.Shift, ui.KeyV},
		{"select all", ui.Ctrl | ui.Shift, ui.KeyA},
		// A key no binding names goes to the terminal unchanged.
		{"unbound letter", 0, ui.KeyF},
		{"unbound chord", ui.Ctrl, ui.KeyQ},
	}
	for _, tc := range chords {
		t.Run(tc.name, func(t *testing.T) {
			if keyboard.run(tc.mods, tc.key) {
				t.Fatalf("%v+%v was claimed but has no phone action", tc.mods, tc.key)
			}
		})
	}
	if len(calls.switched) != 0 || calls.next != 0 || calls.prev != 0 || calls.newTab != 0 ||
		calls.closeTab != 0 || calls.closePane != 0 || len(calls.focus) != 0 ||
		len(calls.splits) != 0 || calls.hosts != 0 || calls.settings != 0 || calls.historyOpen != 0 {
		t.Fatalf("a skipped chord ran an action: %+v", calls)
	}
}

// TestPhoneKeyboardDispatchesOpenHostsAfterItIsBound covers a catalog binding
// that is Disabled by default: once the user gives it a chord, the phone runs
// its action rather than leaving the key to the terminal.
func TestPhoneKeyboardDispatchesOpenHostsAfterItIsBound(t *testing.T) {
	service := newShortcutService(t)
	conflicts, err := service.SetBinding("open-hosts", domainkeybindings.SchemePC, "Ctrl+Alt+H", nil)
	if err != nil {
		t.Fatalf("bind open-hosts: %v", err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("binding open-hosts conflicted: %v", conflicts)
	}
	calls := &keyboardCalls{}
	keyboard := phoneKeyboardFor(service, calls.actions())
	if !keyboard.run(ui.Ctrl|ui.Alt, ui.KeyH) {
		t.Fatal("the bound open-hosts chord was not dispatched")
	}
	if calls.hosts != 1 {
		t.Fatalf("open-hosts ran %d times", calls.hosts)
	}
}

// TestPhoneKeyboardReservedKeysWinOverApplicationBindings rebinds an
// application action onto the terminal's own Ctrl+C: the terminal keeps the
// key, as the desktop's rule says, and the action never runs.
func TestPhoneKeyboardReservedKeysWinOverApplicationBindings(t *testing.T) {
	service := newShortcutService(t)
	if err := service.ReplaceBinding("next-tab", domainkeybindings.SchemePC, "Ctrl+C", nil); err != nil {
		t.Fatalf("rebind next-tab: %v", err)
	}
	calls := &keyboardCalls{}
	keyboard := phoneKeyboardFor(service, calls.actions())
	for _, key := range []ui.Key{ui.KeyC, ui.KeyV, ui.KeyA} {
		if keyboard.run(ui.Ctrl, key) {
			t.Fatalf("reserved Ctrl+%v was dispatched to an application action", key)
		}
	}
	if calls.next != 0 {
		t.Fatalf("the rebound next-tab action ran %d times", calls.next)
	}
}

// TestRawTerminalKeepsReservedKeysAndDispatchesBindings drives the real
// terminal view: an application chord is taken before the terminal encodes it,
// a reserved control character still reaches the Session, and an unbound key is
// typed unchanged.
func TestRawTerminalKeepsReservedKeysAndDispatchesBindings(t *testing.T) {
	calls := &keyboardCalls{}
	keyboard := phoneKeyboardFor(newShortcutService(t), calls.actions())
	_, screen, tt, handle := newRawKeyboardTerminal(t)
	screen.SetKeyboard(keyboard)

	tt.Key(ui.Ctrl, ui.KeyT)
	if calls.newTab != 1 {
		t.Fatalf("Ctrl+T ran New Tab %d times, want 1", calls.newTab)
	}
	if got := handle.takeWritten(); got != "" {
		t.Fatalf("Ctrl+T also reached the terminal as %q", got)
	}

	tt.Key(ui.Ctrl, ui.KeyC)
	waitWritten(t, handle, "\x03", "Ctrl+C")

	tt.Key(0, ui.KeyX)
	waitWritten(t, handle, "x", "an unbound letter")
}

// keyboardFakeScreen adds the optional input seams installScreenInput looks
// for, so a test can see the body the Sessions page installs without the
// native terminal.
type keyboardFakeScreen struct {
	fakeScreen
	keyboard    *phoneKeyboard
	pasteTarget func(ui.Services, transfer.Data) bool
}

func (s *keyboardFakeScreen) SetKeyboard(keyboard *phoneKeyboard) { s.keyboard = keyboard }

func (s *keyboardFakeScreen) SetPasteTarget(target func(ui.Services, transfer.Data) bool) {
	s.pasteTarget = target
}

// TestInstallScreenInputWiresTheVisiblePane proves the Sessions page hands the
// visible Pane both the frame's binding table and the one paste decision.
func TestInstallScreenInputWiresTheVisiblePane(t *testing.T) {
	tabs := &sessionTabs{shortcuts: newShortcutService(t)}
	screen := &keyboardFakeScreen{fakeScreen: fakeScreen{handle: newFakeHandle("")}}
	view := tabView{id: "tab-1", focus: "pane-1", screen: screen, accessory: &accessoryState{}}
	tabs.installScreenInput(view)

	if screen.keyboard == nil {
		t.Fatal("the Pane got no external-keyboard table")
	}
	if !screen.keyboard.run(ui.Ctrl, ui.KeyTab) {
		t.Fatal("the Pane's table does not dispatch the next-tab binding")
	}
	if screen.pasteTarget == nil {
		t.Fatal("the Pane got no paste decision")
	}
	if !screen.pasteTarget(ui.Services{}, transfer.TextData("hello")) {
		t.Fatal("the Pane's paste decision did not take the data")
	}
	if text, err := screen.pasted.Read(transfer.Text); err != nil || string(text) != "hello" {
		t.Fatalf("the Pane's paste decision sent %q (%v)", text, err)
	}
}

// TestWithKeyboardShortcutsInstallsTheDocument proves the Shell option gives
// the Sessions page the shared binding document and the section transitions
// the catalog's application actions need.
func TestWithKeyboardShortcutsInstallsTheDocument(t *testing.T) {
	service := newShortcutService(t)
	shell := New(WithKeyboardShortcuts(service))
	if shell.sessions.shortcuts != service {
		t.Fatal("the Sessions page did not get the binding document")
	}
	if shell.sessions.openHosts == nil || shell.sessions.openSettings == nil || shell.sessions.openPanePicker == nil {
		t.Fatal("the phone's section transitions were not installed")
	}
}

// newRawKeyboardTerminal builds a real libghostty-vt terminal in the raw view
// with the screen's own OnKey hook, the seam the phone installs in production.
func newRawKeyboardTerminal(t *testing.T) (*terminal.Terminal, *ghosttyScreen, *ui.Tester, *fakeHandle) {
	t.Helper()
	handle := newFakeHandle("")
	screen := &ghosttyScreen{}
	term, err := terminal.New(terminal.Options{Conn: handle, OnKey: screen.onKey})
	if err != nil {
		t.Skipf("libghostty-vt unavailable: %v", err)
	}
	t.Cleanup(func() { _ = term.Close() })
	screen.term = term
	tt := ui.NewTester(func(c *ui.Context) { screen.View(c).Fill() }, 390, 300)
	tt.Frame()
	tt.ClickAt(20, 200)
	tt.Frame()
	if !tt.Focused("Terminal") {
		t.Fatal("the raw terminal did not take the keyboard focus")
	}
	return term, screen, tt, handle
}

// newLensKeyboardScreen builds the Lens screen around a real terminal, with
// the Session handle recording what the terminal sends it.
func newLensKeyboardScreen(t *testing.T) (*ghosttyScreen, *fakeHandle) {
	t.Helper()
	handle := newFakeHandle("")
	term, err := terminal.New(terminal.Options{Conn: handle})
	if err != nil {
		t.Skipf("libghostty-vt unavailable: %v", err)
	}
	t.Cleanup(func() { _ = term.Close() })
	return &ghosttyScreen{term: term}, handle
}

// TestHandleInputTypesThePlatformsComposedText: the text a hardware key's
// key-down carries is typed when the terminal's own encoding does not type
// it, and the platform's commit for the same key is swallowed so it is not
// typed twice.
func TestHandleInputTypesThePlatformsComposedText(t *testing.T) {
	for _, c := range []struct {
		name string
		ev   ui.InputEvent
		want string
	}{
		{
			name: "a punctuation key named by Android",
			ev:   ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.KeyComma, PhysicalKey: ui.KeyComma, Text: ","},
			want: ",",
		},
		{
			name: "a space key",
			ev:   ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.KeySpace, PhysicalKey: ui.KeySpace, Text: " "},
			want: " ",
		},
		{
			name: "a composed character with no key name",
			ev:   ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.KeyUnknown, Text: "é"},
			want: "é",
		},
		{
			name: "an Alt-composed letter",
			ev:   ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.KeyC, PhysicalKey: ui.KeyC, Mods: ui.Alt, Text: "ç"},
			want: "ç",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			screen, handle := newLensKeyboardScreen(t)
			if !screen.handleInput(c.ev) {
				t.Fatalf("the composed text %q was left to the terminal", c.want)
			}
			waitWritten(t, handle, c.want, "the composed text")
			// The platform's commit for the same key is its echo.
			if !screen.handleInput(ui.InputEvent{Kind: ui.InputText, Text: c.want}) {
				t.Fatal("the platform's commit was typed after the key as well")
			}
			if got := handle.takeWritten(); got != "" {
				t.Fatalf("the composed text was typed twice: %q", got)
			}
			// Releasing the key ends the echo window.
			if screen.handleInput(ui.InputEvent{Kind: ui.InputKeyUp, Key: c.ev.Key}) {
				t.Fatal("the key release was swallowed")
			}
			if screen.handleInput(ui.InputEvent{Kind: ui.InputText, Text: "a"}) {
				t.Fatal("the text typed after the key release was swallowed")
			}
		})
	}
}

// TestHandleInputLeavesEncodedKeysToTheTerminal: a key with meaning of its
// own and a control chord are encoded by the terminal, not typed from the
// platform's text, and the U+0000 artifact is still swallowed.
func TestHandleInputLeavesEncodedKeysToTheTerminal(t *testing.T) {
	screen, handle := newLensKeyboardScreen(t)
	for _, ev := range []ui.InputEvent{
		// Android's Return carries a newline; the Pane sends a carriage return.
		{Kind: ui.InputKeyDown, Key: ui.KeyEnter, Text: "\n"},
		// A Ctrl chord's control character is the terminal's.
		{Kind: ui.InputKeyDown, Key: ui.KeyComma, PhysicalKey: ui.KeyComma, Mods: ui.Ctrl, Text: ","},
		// Android's modifier artifact.
		{Kind: ui.InputKeyDown, Key: ui.KeyC, PhysicalKey: ui.KeyC, Mods: ui.Ctrl, Text: "\u0000"},
	} {
		if screen.handleInput(ev) {
			t.Errorf("the key %v was typed from its text %q", ev.Key, ev.Text)
		}
	}
	if got := handle.takeWritten(); got != "" {
		t.Fatalf("the terminal received %q from an encoded key", got)
	}
}

// TestPunctuationChordsReachTheDispatcher: punctuation keys now carry a name,
// so a Ctrl chord on one matches a binding instead of staying keyless.
func TestPunctuationChordsReachTheDispatcher(t *testing.T) {
	screen, handle := newLensKeyboardScreen(t)
	var ran []ui.Key
	screen.SetKeyboard(&phoneKeyboard{bindings: []phoneShortcut{
		{mods: ui.Ctrl, key: ui.KeyComma, run: func() { ran = append(ran, ui.KeyComma) }},
		{mods: ui.Ctrl, key: ui.KeyEqual, run: func() { ran = append(ran, ui.KeyEqual) }},
	}})
	for _, key := range []ui.Key{ui.KeyComma, ui.KeyEqual} {
		if !screen.handleInput(ui.InputEvent{
			Kind: ui.InputKeyDown, Key: key, PhysicalKey: key, Mods: ui.Ctrl, Text: "\u0000",
		}) {
			t.Fatalf("Ctrl+%v did not reach the dispatcher", key)
		}
	}
	if len(ran) != 2 || ran[0] != ui.KeyComma || ran[1] != ui.KeyEqual {
		t.Fatalf("the punctuation chords ran %v, want [Comma Equal]", ran)
	}
	if got := handle.takeWritten(); got != "" {
		t.Fatalf("a dispatched punctuation chord also reached the terminal as %q", got)
	}
}

// TestRawTerminalTypesPunctuation: in raw mode the terminal encodes the
// punctuation key itself, the same character Lens types from the platform's
// text.
func TestRawTerminalTypesPunctuation(t *testing.T) {
	_, screen, tt, handle := newRawKeyboardTerminal(t)
	screen.SetKeyboard(&phoneKeyboard{})
	tt.Key(0, ui.KeyComma)
	waitWritten(t, handle, ",", "a plain comma")
}
