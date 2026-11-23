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
		return tabAttached(graph.state, 0)
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
		return tabAttached(graph.state, 0)
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

// TestQuickConnectMoshAndETReuseSSHCredentials proves Mosh and ET are SSH
// transports: they keep the typed credentials, open the matching Session kind,
// and never claim network-device mode.
func TestQuickConnectMoshAndETReuseSSHCredentials(t *testing.T) {
	for _, transport := range []domainvault.Transport{domainvault.TransportMosh, domainvault.TransportET} {
		t.Run(string(transport), func(t *testing.T) {
			graph := newTestGraph(t)
			graph.state.OpenQuickConnect()
			form := graph.state.QuickConnectForm()
			form.Hostname = "hop.local"
			form.Username = "admin"
			form.Password = "s3cret"
			graph.state.SetQuickConnectTransport(transport)
			if form.Protocol != domainvault.ProtocolSSH || form.Transport != transport {
				t.Fatalf("form = %s/%s, want SSH/%s", form.Protocol, form.Transport, transport)
			}
			if err := graph.state.ConnectQuickConnect(); err != nil {
				t.Fatalf("ConnectQuickConnect: %v", err)
			}
			waitFor(t, "the "+string(transport)+" Tab", func() bool {
				graph.state.Flush()
				return tabAttached(graph.state, 0)
			})
			spec := graph.factory.lastSpec()
			wantKind := appsession.KindMosh
			if transport == domainvault.TransportET {
				wantKind = appsession.KindET
			}
			if spec.Kind != wantKind || spec.HostID != "" {
				t.Fatalf("factory spec = %+v, want a %s Session with no saved Host", spec, wantKind)
			}
			if spec.Username != "admin" || spec.Password != "s3cret" {
				t.Fatalf("factory spec credentials = %q/%q, want the typed SSH credentials",
					spec.Username, spec.Password)
			}
			if spec.NetworkDevice {
				t.Fatal("a Mosh/ET Session carried network-device mode")
			}
			if transport == domainvault.TransportMosh && spec.Mosh == nil {
				t.Fatal("the Mosh Session has no Mosh configuration")
			}
			if transport == domainvault.TransportET && spec.ET == nil {
				t.Fatal("the ET Session has no ET configuration")
			}
		})
	}
}

// TestQuickConnectVNCDisplayBuildsAnUnsavedVNCViewer proves the display number
// becomes the real TCP port and the one-shot VNC password travels only in the
// start specification.
func TestQuickConnectVNCDisplayBuildsAnUnsavedVNCViewer(t *testing.T) {
	graph := newTestGraph(t)
	graph.state.OpenQuickConnect()
	graph.state.SetQuickConnectProtocol(domainvault.ProtocolVNC)
	form := graph.state.QuickConnectForm()
	form.Hostname = "desk.local"
	form.Username = "viewer"
	form.VNCDisplay = "1"
	typedPassword := "typed-vnc-password"
	form.VNCPassword = typedPassword
	if err := graph.state.ConnectQuickConnect(); err != nil {
		t.Fatalf("ConnectQuickConnect: %v", err)
	}
	waitFor(t, "the VNC dial", func() bool {
		return graph.factory.openCount() == 1
	})
	spec := graph.factory.lastSpec()
	if spec.Kind != appsession.KindVNC || spec.HostID != "" {
		t.Fatalf("factory spec = %+v, want a VNC Session with no saved Host", spec)
	}
	if spec.Port != 5901 {
		t.Fatalf("VNC port = %d, want display 1 to mean 5901", spec.Port)
	}
	if spec.VNC == nil {
		t.Fatal("the VNC Session has no VNC start section")
	}
	if spec.VNC.Username != "viewer" || spec.VNC.Port != 5901 {
		t.Fatalf("VNC start section = %+v, want the typed endpoint", spec.VNC)
	}
	if spec.VNC.Configuration.DisplayMode != domainvault.VNCDisplayNumber {
		t.Fatalf("VNC DisplayMode = %q, want display input", spec.VNC.Configuration.DisplayMode)
	}
	if spec.Password != typedPassword {
		t.Fatal("the typed VNC password did not reach the Session as a one-shot credential")
	}
}

// TestQuickConnectVNCPortModeUsesTheTypedPort proves raw-port input validates
// and connects under the typed TCP port.
func TestQuickConnectVNCPortModeUsesTheTypedPort(t *testing.T) {
	graph := newTestGraph(t)
	graph.state.OpenQuickConnect()
	graph.state.SetQuickConnectProtocol(domainvault.ProtocolVNC)
	graph.state.SetQuickConnectVNCMode(domainvault.VNCPort)
	form := graph.state.QuickConnectForm()
	form.Hostname = "desk.local"
	form.Port = "6000"
	if form.VNCMode != domainvault.VNCPort {
		t.Fatalf("VNC mode = %q, want port input", form.VNCMode)
	}
	if err := graph.state.ConnectQuickConnect(); err != nil {
		t.Fatalf("ConnectQuickConnect: %v", err)
	}
	waitFor(t, "the VNC dial", func() bool {
		return graph.factory.openCount() == 1
	})
	spec := graph.factory.lastSpec()
	if spec.Kind != appsession.KindVNC || spec.Port != 6000 {
		t.Fatalf("factory spec = %+v, want a VNC Session on port 6000", spec)
	}
	if spec.VNC == nil || spec.VNC.Configuration.DisplayMode != domainvault.VNCPort {
		t.Fatalf("VNC start section = %+v, want port input mode", spec.VNC)
	}
}

// TestQuickConnectVNCValidation keeps the dialog open and reports the endpoint
// issue before any Session is opened.
func TestQuickConnectVNCValidation(t *testing.T) {
	t.Run("invalid display", func(t *testing.T) {
		graph := newTestGraph(t)
		graph.state.OpenQuickConnect()
		graph.state.SetQuickConnectProtocol(domainvault.ProtocolVNC)
		form := graph.state.QuickConnectForm()
		form.Hostname = "desk.local"
		form.VNCDisplay = "100.5"
		_ = graph.state.ConnectQuickConnect()
		if got := graph.state.QuickConnectIssue(); got != QuickConnectIssueDisplay {
			t.Fatalf("issue = %v, want the invalid display", got)
		}
		if !graph.state.QuickConnectOpen() {
			t.Fatal("the dialog closed after an invalid display")
		}
		if graph.state.TabCount() != 0 {
			t.Fatalf("an invalid display opened %d Tabs", graph.state.TabCount())
		}
	})

	t.Run("display out of range", func(t *testing.T) {
		graph := newTestGraph(t)
		graph.state.OpenQuickConnect()
		graph.state.SetQuickConnectProtocol(domainvault.ProtocolVNC)
		form := graph.state.QuickConnectForm()
		form.Hostname = "desk.local"
		form.VNCDisplay = "100"
		_ = graph.state.ConnectQuickConnect()
		if got := graph.state.QuickConnectIssue(); got != QuickConnectIssueDisplay {
			t.Fatalf("issue = %v, want the invalid display", got)
		}
		if !graph.state.QuickConnectOpen() {
			t.Fatal("the dialog closed after an out-of-range display")
		}
	})

	t.Run("invalid port", func(t *testing.T) {
		graph := newTestGraph(t)
		graph.state.OpenQuickConnect()
		graph.state.SetQuickConnectProtocol(domainvault.ProtocolVNC)
		graph.state.SetQuickConnectVNCMode(domainvault.VNCPort)
		form := graph.state.QuickConnectForm()
		form.Hostname = "desk.local"
		form.Port = "70000"
		_ = graph.state.ConnectQuickConnect()
		if got := graph.state.QuickConnectIssue(); got != QuickConnectIssuePort {
			t.Fatalf("issue = %v, want the invalid port", got)
		}
		if !graph.state.QuickConnectOpen() {
			t.Fatal("the dialog closed after an invalid VNC port")
		}
	})
}

// TestQuickConnectProtocolSwitchingClearsHiddenValues pins the rule that a
// hidden control never keeps a value that could reach the Session.
func TestQuickConnectProtocolSwitchingClearsHiddenValues(t *testing.T) {
	graph := newTestGraph(t)
	graph.state.OpenQuickConnect()
	form := graph.state.QuickConnectForm()
	form.Hostname = "switch.local"
	form.Username = "admin"
	form.Password = "s3cret"
	form.NetworkDevice = true

	// A non-plain SSH transport hides network-device mode.
	graph.state.SetQuickConnectTransport(domainvault.TransportMosh)
	if form.NetworkDevice {
		t.Fatal("network-device mode survived a switch to Mosh")
	}
	form.NetworkDevice = true

	// Leaving SSH clears the transport and every SSH-only control, including
	// the one-shot password.
	graph.state.SetQuickConnectProtocol(domainvault.ProtocolVNC)
	if form.Transport != domainvault.TransportPlain || form.NetworkDevice {
		t.Fatalf("SSH-only fields survived: transport=%q network=%v", form.Transport, form.NetworkDevice)
	}
	if form.Password != "" {
		t.Fatalf("the hidden SSH password survived: %q", form.Password)
	}
	if form.Username != "admin" {
		t.Fatalf("the VNC username = %q, want the visible username kept", form.Username)
	}
	if form.VNCMode != domainvault.VNCDisplayNumber || form.VNCDisplay != "0" {
		t.Fatalf("fresh VNC form = %q/%q, want display 0", form.VNCMode, form.VNCDisplay)
	}
	form.VNCPassword = "pw"
	form.VNCMode = domainvault.VNCPort

	// Leaving VNC clears its password and returns to display input.
	graph.state.SetQuickConnectProtocol(domainvault.ProtocolSSH)
	if form.VNCPassword != "" || form.VNCMode != domainvault.VNCDisplayNumber {
		t.Fatalf("VNC fields survived: password=%q mode=%q", form.VNCPassword, form.VNCMode)
	}

	// Telnet shows no credential or device controls at all.
	form.VNCPassword = "pw"
	form.NetworkDevice = true
	graph.state.SetQuickConnectProtocol(domainvault.ProtocolTelnet)
	if form.Username != "" || form.Password != "" || form.VNCPassword != "" ||
		form.NetworkDevice {
		t.Fatalf("Telnet kept hidden values: %+v", form)
	}
	if form.Port != "23" {
		t.Fatalf("Telnet port = %q, want 23", form.Port)
	}
}

// TestQuickConnectProtocolSwitchKeepsATypedPort pins the port rule: a port the
// user typed follows them across protocols, while an untouched default port
// follows the protocol, so switching alone leaves nothing to discard.
func TestQuickConnectProtocolSwitchKeepsATypedPort(t *testing.T) {
	graph := newTestGraph(t)
	graph.state.OpenQuickConnect()
	form := graph.state.QuickConnectForm()
	form.Port = "2222"

	graph.state.SetQuickConnectProtocol(domainvault.ProtocolVNC)
	if form.Port != "2222" {
		t.Fatalf("VNC port = %q, want the typed port kept", form.Port)
	}
	graph.state.SetQuickConnectProtocol(domainvault.ProtocolSSH)
	if form.Port != "2222" {
		t.Fatalf("SSH port = %q, want the typed port kept", form.Port)
	}

	form.Port = "22"
	graph.state.SetQuickConnectProtocol(domainvault.ProtocolTelnet)
	if form.Port != "23" {
		t.Fatalf("Telnet port = %q, want the protocol default", form.Port)
	}

	// Switching on the defaults leaves an untouched form that closes at once.
	other := newTestGraph(t)
	other.state.OpenQuickConnect()
	other.state.SetQuickConnectProtocol(domainvault.ProtocolVNC)
	other.state.RequestQuickConnectClose()
	if other.state.QuickConnectOpen() || other.state.QuickConnectConfirmingDiscard() {
		t.Fatal("switching to VNC on its defaults did not count as untouched")
	}
}

// TestQuickConnectPartlyFilledVNCFormAsksToDiscard proves the dismissal rule
// covers a transport or VNC endpoint the user changed.
func TestQuickConnectPartlyFilledVNCFormAsksToDiscard(t *testing.T) {
	for name, fill := range map[string]func(*QuickConnectForm){
		"transport": func(form *QuickConnectForm) {
			form.Transport = domainvault.TransportMosh
		},
		"vnc mode": func(form *QuickConnectForm) {
			form.VNCMode = domainvault.VNCPort
		},
		"vnc display": func(form *QuickConnectForm) {
			form.VNCDisplay = "2"
		},
		"vnc password": func(form *QuickConnectForm) {
			form.VNCPassword = "pw"
		},
	} {
		t.Run(name, func(t *testing.T) {
			graph := newTestGraph(t)
			graph.state.OpenQuickConnect()
			fill(graph.state.QuickConnectForm())
			graph.state.RequestQuickConnectClose()
			if !graph.state.QuickConnectConfirmingDiscard() {
				t.Fatal("closing the form did not ask to discard")
			}
		})
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
