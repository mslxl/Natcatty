package phoneshell

// The phone accessory bar's contract: every key it shows reaches the
// terminal as that key, the Ctrl latch applies to the next one, and tapping
// the bar never takes the keyboard away from the terminal.

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// openTerminal opens one Host and waits for its live terminal page.
func openTerminal(t *testing.T) (*phoneFixture, *fakeHandle) {
	t.Helper()
	opener := &fakeOpener{output: "hello from remote\r\n"}
	fixture := newPhoneFixture(t, phoneHosts(), opener)
	if err := fixture.tester.Click("Open prod box"); err != nil {
		t.Fatalf("click Host row: %v", err)
	}
	fixture.tester.Frame()
	fixture.await(t, "the terminal", func() bool {
		return fixture.tester.HasText("hello from remote")
	})
	handle := opener.last()
	if handle == nil {
		t.Fatal("the opener returned no handle")
	}
	return fixture, handle
}

// expectKeys reports whether the bar sent exactly want since the last check.
func expectKeys(t *testing.T, handle *fakeHandle, label string, want ...sentKey) {
	t.Helper()
	got := handle.sentKeys()
	if len(got) != len(want) {
		t.Fatalf("%s sent %d keys %v, want %v", label, len(got), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s sent %v, want %v", label, got, want)
		}
	}
}

func TestPhoneAccessoryBarSendsItsKeys(t *testing.T) {
	fixture, handle := openTerminal(t)
	cases := []struct {
		label string
		key   ui.Key
		mods  ui.Modifiers
	}{
		{"Esc", ui.KeyEscape, 0},
		{"Tab", ui.KeyTab, 0},
		{"←", ui.KeyLeft, 0},
		{"↑", ui.KeyUp, 0},
		{"↓", ui.KeyDown, 0},
		{"→", ui.KeyRight, 0},
		{"PgUp", ui.KeyPageUp, 0},
		{"PgDn", ui.KeyPageDown, 0},
		{"|", ui.KeyBackslash, ui.Shift},
		{"-", ui.KeyMinus, 0},
		{"~", ui.KeyBackquote, ui.Shift},
		{"/", ui.KeySlash, 0},
		{"^C", ui.KeyC, ui.Ctrl},
		{"^D", ui.KeyD, ui.Ctrl},
		{"^Z", ui.KeyZ, ui.Ctrl},
	}
	for _, c := range cases {
		if err := fixture.tester.Click(c.label); err != nil {
			t.Fatalf("click %q: %v", c.label, err)
		}
		fixture.tester.Frame()
		expectKeys(t, handle, c.label,
			sentKey{key: c.key, mods: c.mods})
	}
}

func TestPhoneAccessoryBarCtrlLatchesForOneKey(t *testing.T) {
	fixture, handle := openTerminal(t)
	if err := fixture.tester.Click("Ctrl"); err != nil {
		t.Fatalf("click Ctrl: %v", err)
	}
	fixture.tester.Frame()
	expectKeys(t, handle, "the Ctrl latch")
	if err := fixture.tester.Click("←"); err != nil {
		t.Fatalf("click left: %v", err)
	}
	fixture.tester.Frame()
	expectKeys(t, handle, "Ctrl+Left", sentKey{key: ui.KeyLeft, mods: ui.Ctrl})
	// The latch is spent: the next arrow has no Control.
	if err := fixture.tester.Click("↑"); err != nil {
		t.Fatalf("click up: %v", err)
	}
	fixture.tester.Frame()
	expectKeys(t, handle, "the plain Up", sentKey{key: ui.KeyUp})
	// Tapping the latch again without a key cancels it.
	if err := fixture.tester.Click("Ctrl"); err != nil {
		t.Fatalf("click Ctrl: %v", err)
	}
	if err := fixture.tester.Click("Ctrl"); err != nil {
		t.Fatalf("click Ctrl again: %v", err)
	}
	fixture.tester.Frame()
	if err := fixture.tester.Click("→"); err != nil {
		t.Fatalf("click right: %v", err)
	}
	fixture.tester.Frame()
	expectKeys(t, handle, "the cancelled latch", sentKey{key: ui.KeyRight})
}

func TestPhoneAccessoryBarKeepsTheKeyboardTarget(t *testing.T) {
	fixture, _ := openTerminal(t)
	if !fixture.tester.Focused("Terminal") {
		t.Fatal("the terminal is not the focused element")
	}
	if err := fixture.tester.Click("Esc"); err != nil {
		t.Fatalf("click Esc: %v", err)
	}
	fixture.tester.Frame()
	if !fixture.tester.Focused("Terminal") {
		t.Fatal("tapping the accessory bar took the focus from the terminal")
	}
	// The bar sits above the terminal, so it stays above the keyboard the
	// root's safe area holds the content over.
	bar, ok := fixture.tester.Find("Esc")
	if !ok {
		t.Fatal("no Esc button")
	}
	terminal, ok := fixture.tester.Find("Terminal")
	if !ok {
		t.Fatal("no terminal element")
	}
	if bar.Y+bar.H > terminal.Y+1 {
		t.Fatalf("the accessory bar at y=%v h=%v is not above the terminal at y=%v", bar.Y, bar.H, terminal.Y)
	}
}
