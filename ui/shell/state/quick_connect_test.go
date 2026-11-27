package state

import (
	"errors"
	"testing"

	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
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

// TestQuickConnectVNCWebSocketSeedsTheHTTPPort proves enabling the WebSocket
// transport moves the form to port input on websockify's 6080 and carries the
// path and TLS choice into the Session start section.
func TestQuickConnectVNCWebSocketSeedsTheHTTPPort(t *testing.T) {
	graph := newTestGraph(t)
	graph.state.OpenQuickConnect()
	graph.state.SetQuickConnectProtocol(domainvault.ProtocolVNC)
	form := graph.state.QuickConnectForm()
	if form.Port != "5900" || form.VNCMode != domainvault.VNCDisplayNumber {
		t.Fatalf("VNC defaults = %+v, want display 0", *form)
	}

	graph.state.SetQuickConnectVNCWebSocket(true)
	if !form.VNCWebSocket || form.VNCMode != domainvault.VNCPort ||
		form.Port != "6080" || form.VNCDisplay != "" {
		t.Fatalf("WebSocket form = %+v, want the HTTP port", *form)
	}
	form.Hostname = "desk.local"
	form.VNCWebSocketPath = "/vnc?token=s3cret"
	form.VNCWebSocketSecure = true
	if err := graph.state.ConnectQuickConnect(); err != nil {
		t.Fatalf("ConnectQuickConnect: %v", err)
	}
	waitFor(t, "the VNC WebSocket dial", func() bool {
		return graph.factory.openCount() == 1
	})
	spec := graph.factory.lastSpec()
	if spec.Kind != appsession.KindVNC || spec.Port != domainvault.VNCDefaultWebSocketPort {
		t.Fatalf("factory spec = %+v, want a VNC Session on the HTTP port", spec)
	}
	if spec.VNC == nil {
		t.Fatal("the VNC Session has no VNC start section")
	}
	configuration := spec.VNC.Configuration
	if !configuration.WebSocket || !configuration.WebSocketSecure ||
		configuration.WebSocketPath != "/vnc?token=s3cret" ||
		configuration.DisplayMode != domainvault.VNCPort {
		t.Fatalf("VNC configuration = %+v, want the WebSocket endpoint", configuration)
	}
}

// TestQuickConnectVNCWebSocketToggleRestoresTheRFBEndpoint proves turning the
// transport back off clears the hidden path and TLS choice and returns the
// form to display 0 while the port is still websockify's default.
func TestQuickConnectVNCWebSocketToggleRestoresTheRFBEndpoint(t *testing.T) {
	graph := newTestGraph(t)
	graph.state.OpenQuickConnect()
	graph.state.SetQuickConnectProtocol(domainvault.ProtocolVNC)
	form := graph.state.QuickConnectForm()

	graph.state.SetQuickConnectVNCWebSocket(true)
	form.VNCWebSocketPath = "/custom"
	form.VNCWebSocketSecure = true
	graph.state.SetQuickConnectVNCWebSocket(false)
	if form.VNCWebSocket || form.VNCWebSocketPath != "" || form.VNCWebSocketSecure {
		t.Fatalf("form = %+v, want the WebSocket settings cleared", *form)
	}
	if form.Port != "5900" || form.VNCMode != domainvault.VNCDisplayNumber || form.VNCDisplay != "0" {
		t.Fatalf("endpoint = %+v, want display 0 back", *form)
	}
}

// TestQuickConnectVNCWebSocketRejectsABadPath keeps the dialog open and reports
// a path that cannot be requested, instead of failing during the handshake.
func TestQuickConnectVNCWebSocketRejectsABadPath(t *testing.T) {
	graph := newTestGraph(t)
	graph.state.OpenQuickConnect()
	graph.state.SetQuickConnectProtocol(domainvault.ProtocolVNC)
	graph.state.SetQuickConnectVNCWebSocket(true)
	form := graph.state.QuickConnectForm()
	form.Hostname = "desk.local"
	form.VNCWebSocketPath = "/bad path"
	if err := graph.state.ConnectQuickConnect(); err != nil {
		t.Fatalf("ConnectQuickConnect: %v", err)
	}
	if !graph.state.QuickConnectOpen() {
		t.Fatal("the dialog closed after a bad WebSocket path")
	}
	if graph.state.QuickConnectIssue() != QuickConnectIssueWebSocketPath {
		t.Fatalf("issue = %v, want the WebSocket path issue", graph.state.QuickConnectIssue())
	}
	if graph.factory.openCount() != 0 {
		t.Fatal("a Session opened despite the bad WebSocket path")
	}
}

// TestQuickConnectVNCWebSocketCountsAsTypedInput proves the one-click transport
// switch makes the form worth confirming before it is discarded.
func TestQuickConnectVNCWebSocketCountsAsTypedInput(t *testing.T) {
	graph := newTestGraph(t)
	graph.state.OpenQuickConnect()
	graph.state.SetQuickConnectProtocol(domainvault.ProtocolVNC)
	graph.state.SetQuickConnectVNCWebSocket(true)
	graph.state.RequestQuickConnectClose()
	if !graph.state.QuickConnectConfirmingDiscard() {
		t.Fatal("closing the form after enabling WebSocket did not ask to discard")
	}
}

// TestQuickConnectVNCWebSocketBlankPortInheritsTheDefault proves clearing the
// port field keeps the meaning of its websockify placeholder.
func TestQuickConnectVNCWebSocketBlankPortInheritsTheDefault(t *testing.T) {
	graph := newTestGraph(t)
	graph.state.OpenQuickConnect()
	graph.state.SetQuickConnectProtocol(domainvault.ProtocolVNC)
	graph.state.SetQuickConnectVNCWebSocket(true)
	form := graph.state.QuickConnectForm()
	form.Hostname = "desk.local"
	form.Port = ""
	if err := graph.state.ConnectQuickConnect(); err != nil {
		t.Fatalf("ConnectQuickConnect: %v", err)
	}
	waitFor(t, "the VNC WebSocket dial", func() bool {
		return graph.factory.openCount() == 1
	})
	spec := graph.factory.lastSpec()
	if spec.Port != domainvault.VNCDefaultWebSocketPort {
		t.Fatalf("WebSocket port = %d, want websockify's default", spec.Port)
	}
}

// TestQuickConnectVNCWebSocketSeedsFromTheVisibleDisplayNotAHiddenPort proves a
// Port left over from another protocol is not mistaken for the endpoint.
func TestQuickConnectVNCWebSocketSeedsFromTheVisibleDisplayNotAHiddenPort(t *testing.T) {
	graph := newTestGraph(t)
	graph.state.OpenQuickConnect()
	graph.state.SetQuickConnectProtocol(domainvault.ProtocolVNC)
	form := graph.state.QuickConnectForm()
	form.VNCDisplay = ""
	form.Port = "2222"
	graph.state.SetQuickConnectVNCWebSocket(true)
	if got, want := form.Port, "6080"; got != want {
		t.Fatalf("WebSocket port = %q, want %q from the visible display 0", got, want)
	}
}

// TestQuickConnectRetryAfterAFailedDial is the regression test for the missing
// retry: an Unsaved Host has no Vault record, so the descriptor-driven reopen
// must not look one up, and the typed one-shot password must reach the redial
// exactly as it reached the first dial. The workspace graph wires the Host
// resolver production uses, so a lookup of the empty Host ID would fail
// exactly as the user's report described.
func TestQuickConnectRetryAfterAFailedDial(t *testing.T) {
	graph := newWorkspaceGraph(t)
	var attempts int
	graph.factory.fail = func(appsession.StartSpec) error {
		attempts++
		if attempts == 1 {
			return errors.New("fake: the endpoint refused the connection")
		}
		return nil
	}
	graph.state.OpenQuickConnect()
	form := graph.state.QuickConnectForm()
	form.Hostname = "switch.local"
	form.Username = "admin"
	form.Password = "s3cret"
	if err := graph.state.ConnectQuickConnect(); err != nil {
		t.Fatalf("ConnectQuickConnect: %v", err)
	}

	paneID := onlyPaneID(t, graph.state, 0)
	graph.flushUntil(t, "the failed dial", func() bool {
		info, ok := graph.state.PaneInfoAt(0, paneID)
		return ok && !info.Pending && info.Err != nil
	})
	if graph.factory.specCount() != 1 {
		t.Fatalf("dials = %d, want exactly the first attempt", graph.factory.specCount())
	}
	if info, _ := graph.state.PaneInfoAt(0, paneID); errors.Is(info.Err, ErrHostUnavailable) {
		t.Fatal("the failed Unsaved Host dial was misreported as a missing Host")
	}

	// The endpoint comes back; the retry must redial the same endpoint with
	// the typed password, without any Vault lookup.
	tabID, ok := graph.state.TabID(0)
	if !ok {
		t.Fatal("the quick-connect Tab has no identity")
	}
	if !graph.state.RetryPane(tabID, paneID) {
		t.Fatal("RetryPane refused the failed quick-connect Pane")
	}
	graph.flushUntil(t, "the retried dial", func() bool {
		return graph.factory.specCount() == 2
	})
	spec := graph.factory.lastSpec()
	if spec.HostID != "" || spec.Hostname != "switch.local" || spec.Username != "admin" {
		t.Fatalf("retry spec = %+v, want the same unsaved endpoint", spec)
	}
	if spec.Password != "s3cret" {
		t.Fatalf("retry spec password = %q, want the one-shot password reused", spec.Password)
	}

	// The credential lives only as long as the Pane: closing the Tab drops it.
	if _, ok := graph.state.paneCredentials.get(paneID); !ok {
		t.Fatal("the in-memory credential was dropped before the Pane closed")
	}
	graph.state.CloseTab(0)
	// A Tab close with motion on retains the body as an exit ghost; the view
	// releasing it is what ends the Sessions and drops the credentials.
	graph.state.ReleasePaneGhosts()
	graph.state.ReleaseTabGhosts()
	if _, ok := graph.state.paneCredentials.get(paneID); ok {
		t.Fatal("the in-memory credential survived the Pane close")
	}
}

func onlyPaneID(t *testing.T, s *State, index int) model.PaneID {
	t.Helper()
	root, _, ok := s.TabTree(index)
	if !ok {
		t.Fatalf("Tab %d does not exist", index)
	}
	ids := domainworkspace.CollectPaneIDs(root)
	if len(ids) != 1 {
		t.Fatalf("Tab %d panes = %v, want exactly one", index, ids)
	}
	return ids[0]
}
