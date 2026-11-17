package state

import (
	"testing"

	appsession "github.com/mslxl/natcatty/application/session"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// TestQuickConnectOpensAnUnsavedHostSession is the quick-connect contract: the
// typed details become a Session, but no Host ID, so the Vault is never
// involved and the Session keeps the Unsaved Host policy.
func TestQuickConnectOpensAnUnsavedHostSession(t *testing.T) {
	graph := newTestGraph(t)
	graph.state.OpenQuickConnect()
	if !graph.state.QuickConnectOpen() {
		t.Fatal("OpenQuickConnect did not show the dialog")
	}

	form := graph.state.QuickConnectForm()
	form.Hostname = "  switch.local  "
	form.Port = "2222"
	form.Username = "admin"
	form.Password = "s3cret"
	form.NetworkDevice = true
	if err := graph.state.ConnectQuickConnect(); err != nil {
		t.Fatalf("ConnectQuickConnect: %v", err)
	}
	if graph.state.QuickConnectOpen() {
		t.Fatal("the dialog stayed open after Connect")
	}
	if form := graph.state.QuickConnectForm(); form.Password != "" || form.Hostname != "" {
		t.Fatalf("the closed form kept %q/%q, want the password cleared", form.Hostname, form.Password)
	}

	waitFor(t, "the quick-connect Tab", func() bool {
		graph.state.Flush()
		return graph.state.TabCount() == 1
	})
	spec := graph.factory.lastSpec()
	if spec.Kind != appsession.KindSSH || spec.HostID != "" {
		t.Fatalf("factory spec = %+v, want an SSH Session with no saved Host", spec)
	}
	if spec.Hostname != "switch.local" || spec.Port != 2222 || spec.Username != "admin" {
		t.Fatalf("factory spec = %+v, want the typed endpoint", spec)
	}
	if spec.Password != "s3cret" {
		t.Fatal("the typed credentials did not reach the Session")
	}
	if !spec.NetworkDevice {
		t.Fatal("the temporary network-device mode did not reach the Session")
	}
	if spec.Reconnect == nil {
		t.Fatal("the Unsaved Host Session has no reconnect policy")
	}
	if spec.StartupCommand != "" {
		t.Fatalf("quick connect carried a startup command %q", spec.StartupCommand)
	}
	if got := graph.sessions[0].HostID(); got != "" {
		t.Fatalf("Session HostID = %q, want empty so SFTP policy treats it as unsaved", got)
	}

	label, _, ok := graph.state.TabLabel(0)
	wantLabel := "admin" + "@" + "switch.local"
	if !ok || label != wantLabel {
		t.Fatalf("tab label = %q, %v; want %q", label, ok, wantLabel)
	}

	// The Unsaved Host policy holds across a reconnect: the redial must keep
	// the typed credentials and still name no saved Host.
	if !graph.sessions[0].ReconnectNow() {
		t.Fatal("the quick-connect Session refused an immediate reconnect")
	}
	waitFor(t, "the redial", func() bool { return graph.factory.openCount() == 2 })
	if got := graph.factory.lastSpec(); got.HostID != "" || got.Password != "s3cret" {
		t.Fatalf("reconnect spec = %+v, want the same unsaved Host and typed password", got)
	}
}

func TestQuickConnectTelnetSkipsSSHCredentials(t *testing.T) {
	graph := newTestGraph(t)
	graph.state.OpenQuickConnect()
	graph.state.SetQuickConnectProtocol(domainvault.ProtocolTelnet)
	form := graph.state.QuickConnectForm()
	form.Hostname = "console.local"
	if form.Port != "23" {
		t.Fatalf("Telnet default port = %q, want 23", form.Port)
	}
	if err := graph.state.ConnectQuickConnect(); err != nil {
		t.Fatalf("ConnectQuickConnect: %v", err)
	}
	waitFor(t, "the Telnet Tab", func() bool {
		graph.state.Flush()
		return graph.state.TabCount() == 1
	})
	spec := graph.factory.lastSpec()
	if spec.Kind != appsession.KindTelnet || spec.HostID != "" {
		t.Fatalf("factory spec = %+v, want a Telnet Session with no saved Host", spec)
	}
	if spec.Telnet == nil || spec.Telnet.Hostname != "console.local" || spec.Telnet.Port != 23 {
		t.Fatalf("Telnet config = %+v, want console.local:23", spec.Telnet)
	}
	if spec.Password != "" || spec.Username != "" || spec.NetworkDevice {
		t.Fatalf("Telnet carried SSH-only fields: %+v", spec)
	}
}

func TestQuickConnectValidatesBeforeOpening(t *testing.T) {
	graph := newTestGraph(t)
	graph.state.OpenQuickConnect()
	form := graph.state.QuickConnectForm()

	if err := graph.state.ConnectQuickConnect(); err != nil {
		t.Fatalf("ConnectQuickConnect: %v", err)
	}
	if got := graph.state.QuickConnectIssue(); got != QuickConnectIssueAddress {
		t.Fatalf("issue = %v, want the missing address", got)
	}
	if !graph.state.QuickConnectOpen() {
		t.Fatal("the dialog closed after a validation failure")
	}

	form.Hostname = "switch.local"
	form.Port = "70000"
	_ = graph.state.ConnectQuickConnect()
	if got := graph.state.QuickConnectIssue(); got != QuickConnectIssuePort {
		t.Fatalf("issue = %v, want the invalid port", got)
	}
	if graph.state.TabCount() != 0 {
		t.Fatalf("a validation failure opened %d Tabs", graph.state.TabCount())
	}
}

// TestQuickConnectKeepsAPartlyFilledFormUntilDiscarded is the dismissal rule:
// Escape (or an outside click) asks before throwing away typed details, and an
// untouched form closes at once.
func TestQuickConnectKeepsAPartlyFilledFormUntilDiscarded(t *testing.T) {
	graph := newTestGraph(t)
	graph.state.OpenQuickConnect()
	graph.state.QuickConnectForm().Hostname = "switch.local"

	graph.state.RequestQuickConnectClose()
	if !graph.state.QuickConnectConfirmingDiscard() {
		t.Fatal("closing a partly filled form did not ask to discard")
	}
	if !graph.state.QuickConnectOpen() {
		t.Fatal("the dialog closed before the user chose")
	}
	graph.state.KeepQuickConnectEditing()
	if graph.state.QuickConnectConfirmingDiscard() || !graph.state.QuickConnectOpen() {
		t.Fatal("Keep editing did not return to the form")
	}
	if got := graph.state.QuickConnectForm().Hostname; got != "switch.local" {
		t.Fatalf("address = %q after cancelling the discard, want it kept", got)
	}

	graph.state.RequestQuickConnectClose()
	graph.state.DiscardQuickConnect()
	if graph.state.QuickConnectOpen() || graph.state.QuickConnectConfirmingDiscard() {
		t.Fatal("Discard did not close the dialog")
	}
	if got := graph.state.QuickConnectForm().Hostname; got != "" {
		t.Fatalf("address = %q after Discard, want it cleared", got)
	}

	graph.state.OpenQuickConnect()
	graph.state.RequestQuickConnectClose()
	if graph.state.QuickConnectOpen() || graph.state.QuickConnectConfirmingDiscard() {
		t.Fatal("an untouched form did not close at once")
	}
}
