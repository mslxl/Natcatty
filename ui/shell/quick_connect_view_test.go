package shell

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	domainvault "github.com/mslxl/natcatty/domain/vault"
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

// TestQuickConnectVNCShowsVNCControls proves the VNC form offers the endpoint
// mode, display, username, and its own password, and hides SSH-only controls.
func TestQuickConnectVNCShowsVNCControls(t *testing.T) {
	m := locales.In("en")
	graph := newTestGraph(t)
	tt := ui.NewTester(View(graph.state), 1100, 720)
	tt.SetLocale("en")
	tt.Frame()
	graph.state.OpenQuickConnect()
	graph.state.SetQuickConnectProtocol(domainvault.ProtocolVNC)
	tt.Frame()

	for _, text := range []string{
		m.VaultHostVNCInputMode(), m.VaultHostVNCInputDisplay(),
		m.VaultHostVNCDisplay(), m.VaultHostVNCPassword(), m.QuickConnectUsername(),
	} {
		if !tt.HasText(text) {
			t.Fatalf("the VNC form does not show %q; texts %q", text, tt.Texts())
		}
	}
	for _, text := range []string{
		m.VaultHostNetworkDevice(), m.VaultHostTransport(),
	} {
		if tt.HasText(text) {
			t.Fatalf("the VNC form still shows the SSH-only control %q", text)
		}
	}
	// "VNC Password" contains "Password", so compare the label exactly to prove
	// the SSH password field itself is gone.
	for _, text := range tt.Texts() {
		if text == m.QuickConnectPassword() {
			t.Fatal("the VNC form still shows the SSH password control")
		}
	}
}

// TestQuickConnectSSHShowsTransportAndPlainNetworkDevice proves the SSH form
// offers the transport selector, keeps the SSH credentials for Mosh/ET, and
// hides network-device mode for them.
func TestQuickConnectSSHShowsTransportAndPlainNetworkDevice(t *testing.T) {
	m := locales.In("en")
	graph := newTestGraph(t)
	tt := ui.NewTester(View(graph.state), 1100, 720)
	tt.SetLocale("en")
	tt.Frame()
	graph.state.OpenQuickConnect()
	tt.Frame()

	if !tt.HasText(m.VaultHostTransport()) || !tt.HasText(m.VaultHostTransportSSH()) {
		t.Fatalf("the SSH form does not offer the transport select; texts %q", tt.Texts())
	}
	if !tt.HasText(m.VaultHostNetworkDevice()) {
		t.Fatalf("plain SSH does not offer network-device mode; texts %q", tt.Texts())
	}
	if tt.HasText(m.VaultHostVNCInputMode()) {
		t.Fatal("the SSH form shows the VNC endpoint controls")
	}

	graph.state.SetQuickConnectTransport(domainvault.TransportMosh)
	tt.Frame()
	if !tt.HasText(m.VaultHostTransportMosh()) {
		t.Fatalf("the transport select does not show Mosh; texts %q", tt.Texts())
	}
	if !tt.HasText(m.QuickConnectUsername()) || !tt.HasText(m.QuickConnectPassword()) {
		t.Fatal("Mosh lost the shared SSH credential fields")
	}
	if tt.HasText(m.VaultHostNetworkDevice()) {
		t.Fatal("Mosh still shows network-device mode")
	}
}

// TestQuickConnectTelnetHidesProtocolSpecificControls proves Telnet keeps only
// its address and port.
func TestQuickConnectTelnetHidesProtocolSpecificControls(t *testing.T) {
	m := locales.In("en")
	graph := newTestGraph(t)
	tt := ui.NewTester(View(graph.state), 1100, 720)
	tt.SetLocale("en")
	tt.Frame()
	graph.state.OpenQuickConnect()
	graph.state.SetQuickConnectProtocol(domainvault.ProtocolTelnet)
	tt.Frame()

	if !tt.HasText(m.QuickConnectAddress()) || !tt.HasText(m.QuickConnectPort()) {
		t.Fatalf("the Telnet form lost its endpoint; texts %q", tt.Texts())
	}
	for _, text := range []string{
		m.QuickConnectUsername(), m.QuickConnectPassword(), m.VaultHostTransport(),
		m.VaultHostVNCInputMode(), m.VaultHostNetworkDevice(),
	} {
		if tt.HasText(text) {
			t.Fatalf("the Telnet form shows the hidden control %q", text)
		}
	}
}

// TestQuickConnectFieldsShowALabelAboveTheirInputs guards the visible labeling
// of every text field. A TextInput Label only names the control for assistive
// technology; it draws nothing, which once left the address, port, and
// credentials as unlabeled boxes. Each field must instead show a drawn label
// above its input, and clicking that label must focus the input below it.
func TestQuickConnectFieldsShowALabelAboveTheirInputs(t *testing.T) {
	cases := []struct {
		name   string
		labels []func(locales.Messages) string
	}{
		{"ssh", []func(locales.Messages) string{
			locales.Messages.QuickConnectAddress,
			locales.Messages.QuickConnectPort,
			locales.Messages.QuickConnectUsername,
			locales.Messages.QuickConnectPassword,
		}},
		{"vnc", []func(locales.Messages) string{
			locales.Messages.QuickConnectAddress,
			locales.Messages.VaultHostVNCDisplay,
			locales.Messages.QuickConnectUsername,
			locales.Messages.VaultHostVNCPassword,
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := locales.In("en")
			graph := newTestGraph(t)
			tt := ui.NewTester(View(graph.state), 1100, 720)
			tt.SetLocale("en")
			tt.Frame()
			graph.state.OpenQuickConnect()
			if tc.name == "vnc" {
				graph.state.SetQuickConnectProtocol(domainvault.ProtocolVNC)
			}
			tt.Frame()

			for _, labelOf := range tc.labels {
				label := labelOf(m)
				if err := tt.Click(label); err != nil {
					t.Fatalf("click %q: %v", label, err)
				}
				tt.Frame()
				labelRect, ok := tt.Find(label)
				if !ok {
					t.Fatalf("the form does not draw the label %q", label)
				}
				caret, caretOK := tt.TextCaret()
				if !caretOK {
					t.Fatalf("clicking %q did not focus an input", label)
				}
				if labelRect.Y+labelRect.H >= caret.Y {
					t.Fatalf("the label %q is not above its input: label %+v, caret %+v",
						label, labelRect, caret)
				}
			}
		})
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
