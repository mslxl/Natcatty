package shell

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/shell/state"
)

// newHostListState wires the host-list chord the way the app does: the
// surfaces ask the state for the effective key, and the state's toggle is the
// action. The chord is fixed here so the test does not depend on the user's
// key-binding store.
func newHostListState(t *testing.T, factory *typingFactory) *state.State {
	t.Helper()
	return newTypingState(t, factory, state.WithHostListChord(func() (ui.Modifiers, ui.Key, bool) {
		return ui.Ctrl | ui.Shift, ui.KeyB, true
	}))
}

// TestHostListChordShowsTheVaultFromTheTerminal proves the chord registered on
// the Pane's focus subtree runs while the terminal has the keyboard.
func TestHostListChordShowsTheVaultFromTheTerminal(t *testing.T) {
	factory := &typingFactory{}
	st := newHostListState(t, factory)
	if err := st.NewTab(); err != nil {
		t.Fatal(err)
	}
	waitForRealSurfaces(t, st, 1)

	tester := ui.NewTester(View(st), 900, 600)
	tester.SetLocale("en")
	tester.Frame()
	if st.VaultVisible() {
		t.Fatal("the Vault was already visible before the chord")
	}
	tester.Key(ui.Ctrl|ui.Shift, ui.KeyB)
	tester.Frame()
	if !st.VaultVisible() {
		t.Fatal("the host-list chord did not show the Vault")
	}
}

// TestHostListChordLeavesAFocusedTextFieldAlone proves the chord is scoped to
// the terminal Pane and the host list, not to the window: the compose draft is
// a text field outside the Pane's subtree, so the chord must not toggle the
// Vault or consume the draft.
func TestHostListChordLeavesAFocusedTextFieldAlone(t *testing.T) {
	factory := &typingFactory{}
	st := newHostListState(t, factory)
	if err := st.NewTab(); err != nil {
		t.Fatal(err)
	}
	waitForRealSurfaces(t, st, 1)
	st.OpenCompose()

	tester := ui.NewTester(View(st), 900, 600)
	tester.SetLocale("en")
	tester.Frame()
	if err := tester.Click(locales.In("en").ComposePlaceholder()); err != nil {
		t.Fatalf("focus the compose draft: %v", err)
	}
	tester.Type("a draft")
	tester.Frame()

	tester.Key(ui.Ctrl|ui.Shift, ui.KeyB)
	tester.Frame()
	if st.VaultVisible() {
		t.Fatal("the host-list chord fired while the compose draft had focus")
	}
	if got := *st.ComposeDraft(); got != "a draft" {
		t.Fatalf("the chord changed the compose draft: %q", got)
	}
}

// TestHostListToggleKeepsTheFocusedSession proves the state's toggle returns
// to the same work Tab and lets its terminal take the keyboard again without a
// click. The chord's own hide path is covered where the Vault list registers
// it; this pins the surface change and focus preservation.
func TestHostListToggleKeepsTheFocusedSession(t *testing.T) {
	factory := &typingFactory{}
	st := newHostListState(t, factory)
	if err := st.NewTab(); err != nil {
		t.Fatal(err)
	}
	waitForRealSurfaces(t, st, 1)

	tester := ui.NewTester(View(st), 900, 600)
	tester.SetLocale("en")
	tester.Frame()
	tester.Type("one")
	if !eventuallyInput(t, tester, factory.at(0), "one") {
		t.Fatalf("the terminal did not take the keyboard: %q", factory.at(0).input())
	}
	beforeTab := st.ActiveTab()
	beforeSession, hadSession := st.FocusedSessionID()

	if !st.ToggleHostList() {
		t.Fatal("showing the host list reported no change")
	}
	tester.Frame()
	if !st.VaultVisible() {
		t.Fatal("the host list was not visible after the toggle")
	}
	if !st.ToggleHostList() {
		t.Fatal("hiding the host list reported no change")
	}
	tester.Frame()
	if st.VaultVisible() {
		t.Fatal("the host list stayed visible after the second toggle")
	}
	if st.ActiveTab() != beforeTab {
		t.Fatalf("the active Tab changed from %d to %d", beforeTab, st.ActiveTab())
	}
	if after, ok := st.FocusedSessionID(); ok != hadSession || after != beforeSession {
		t.Fatalf("the focused Session changed from %q/%v to %q/%v",
			beforeSession, hadSession, after, ok)
	}

	tester.Type("two")
	if !eventuallyInput(t, tester, factory.at(0), "two") {
		t.Fatalf("the returning terminal did not take the keyboard: %q", factory.at(0).input())
	}
}
