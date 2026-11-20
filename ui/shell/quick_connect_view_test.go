package shell

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/locales"
)

// TestQuickConnectButtonOpensTheDialog clicks the tab bar's quick-connect
// control and expects the dialog with its form.
func TestQuickConnectButtonOpensTheDialog(t *testing.T) {
	m := locales.In("en")
	graph := newTestGraph(t)
	tt := ui.NewTester(View(graph.state), 1100, 720)
	tt.SetLocale("en")
	tt.Frame()

	if err := tt.Click(m.MenuQuickConnect()); err != nil {
		t.Fatalf("click %q: %v", m.MenuQuickConnect(), err)
	}
	tt.Frame()
	if !graph.state.QuickConnectOpen() {
		t.Fatal("the quick-connect button did not open the dialog")
	}
	for _, text := range []string{
		m.QuickConnectDescription(), m.QuickConnectProtocol(), m.QuickConnectAddress(),
		m.QuickConnectPort(), m.QuickConnectUsername(), m.QuickConnectPassword(),
	} {
		if !tt.HasText(text) {
			t.Fatalf("the dialog does not show %q; texts %q", text, tt.Texts())
		}
	}
}

// TestQuickConnectOutsideClickKeepsTypedDetails proves an outside click does
// not throw away a partly filled form.
func TestQuickConnectOutsideClickKeepsTypedDetails(t *testing.T) {
	graph := newTestGraph(t)
	tt := ui.NewTester(View(graph.state), 1100, 720)
	tt.SetLocale("en")
	tt.Frame()
	graph.state.OpenQuickConnect()
	tt.Frame()

	tt.Type("switch.local")
	tt.Frame()
	if got := graph.state.QuickConnectForm().Hostname; got != "switch.local" {
		t.Fatalf("address = %q after typing, want switch.local", got)
	}

	// The dialog is centered; (5, 300) is on the backdrop.
	tt.ClickAt(5, 300)
	tt.Frame()
	if !graph.state.QuickConnectOpen() {
		t.Fatal("an outside click closed the dialog")
	}
	if got := graph.state.QuickConnectForm().Hostname; got != "switch.local" {
		t.Fatalf("address = %q after an outside click, want it kept", got)
	}
}

// TestQuickConnectEscapeAsksBeforeDiscarding follows the dismissal rule: Escape
// on a partly filled form asks, Keep editing returns to the form, and Discard
// closes it.
func TestQuickConnectEscapeAsksBeforeDiscarding(t *testing.T) {
	m := locales.In("en")
	graph := newTestGraph(t)
	tt := ui.NewTester(View(graph.state), 1100, 720)
	tt.SetLocale("en")
	tt.Frame()
	graph.state.OpenQuickConnect()
	tt.Frame()
	tt.Type("switch.local")
	tt.Frame()

	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if !graph.state.QuickConnectConfirmingDiscard() {
		t.Fatal("Escape on a partly filled form did not ask before discarding")
	}
	if !tt.HasText(m.QuickConnectDiscardTitle()) {
		t.Fatalf("the discard confirmation is not shown; texts %q", tt.Texts())
	}
	if !tt.HasText(m.QuickConnectDiscardDescription()) {
		t.Fatal("the discard confirmation does not explain what is lost")
	}

	if err := tt.Click(m.QuickConnectKeepEditing()); err != nil {
		t.Fatalf("click %q: %v", m.QuickConnectKeepEditing(), err)
	}
	tt.Frame()
	if graph.state.QuickConnectConfirmingDiscard() || !graph.state.QuickConnectOpen() {
		t.Fatal("Keep editing did not return to the open form")
	}
	if got := graph.state.QuickConnectForm().Hostname; got != "switch.local" {
		t.Fatalf("address = %q after Keep editing, want it kept", got)
	}

	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if err := tt.Click(m.QuickConnectDiscard()); err != nil {
		t.Fatalf("click %q: %v", m.QuickConnectDiscard(), err)
	}
	tt.Frame()
	if graph.state.QuickConnectOpen() || graph.state.QuickConnectConfirmingDiscard() {
		t.Fatal("Discard did not close the dialog")
	}
}

// TestQuickConnectEscapeClosesAnUntouchedForm keeps the confirmation for
// forms that hold something: an untouched dialog closes at once.
func TestQuickConnectEscapeClosesAnUntouchedForm(t *testing.T) {
	graph := newTestGraph(t)
	tt := ui.NewTester(View(graph.state), 1100, 720)
	tt.SetLocale("en")
	tt.Frame()
	graph.state.OpenQuickConnect()
	tt.Frame()

	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if graph.state.QuickConnectOpen() {
		t.Fatal("Escape did not close an untouched quick-connect form")
	}
}

// TestQuickConnectConnectOpensAnUnsavedTab drives the whole entry point: type
// an address, connect, and expect a Tab with no Vault Host behind it.
func TestQuickConnectConnectOpensAnUnsavedTab(t *testing.T) {
	m := locales.In("en")
	graph := newTestGraph(t)
	tt := ui.NewTester(View(graph.state), 1100, 720)
	tt.SetLocale("en")
	tt.Frame()
	graph.state.OpenQuickConnect()
	tt.Frame()

	tt.Type("switch.local")
	tt.Frame()
	if err := tt.Click(m.VaultHostConnect()); err != nil {
		t.Fatalf("click %q: %v", m.VaultHostConnect(), err)
	}
	waitForQuickConnectFrame(t, tt, "the quick-connect Tab", func() bool {
		return graph.state.TabCount() == 1
	})
	if graph.state.QuickConnectOpen() {
		t.Fatal("the dialog stayed open after Connect")
	}
	if len(graph.sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(graph.sessions))
	}
	if got := graph.sessions[0].HostID(); got != "" {
		t.Fatalf("Session HostID = %q, want an Unsaved Host", got)
	}
}

// waitForQuickConnectFrame polls an observable condition with a hard deadline;
// the poll interval is a cadence, not a synchronization assumption.
func waitForQuickConnectFrame(t *testing.T, tt *ui.Tester, what string, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		tt.Frame()
	}
	t.Fatalf("timed out waiting for %s", what)
}
