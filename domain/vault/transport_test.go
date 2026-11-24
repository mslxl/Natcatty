package vault

import (
	"encoding/json"
	"testing"

	domainprotocol "github.com/mslxl/natcatty/domain/model/protocol"
)

// TestHostJSONIgnoresTheRemovedImplementation keeps old Host documents
// loadable: a transportImplementation value written by an older build is
// ignored by encoding/json, so no migration is needed.
func TestHostJSONIgnoresTheRemovedImplementation(t *testing.T) {
	var host Host
	document := `{"id":"old","label":"web","hostname":"example.com",` +
		`"protocol":"ssh","transportImplementation":"bundled"}`
	if err := json.Unmarshal([]byte(document), &host); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := host.Validate(); err != nil {
		t.Fatalf("an old Host no longer validates: %v", err)
	}
}

// TestRemovedProtocolIDsGetNoCompatibilityMapping pins the clean break: a Host
// that stored one of the removed protocol IDs is rejected, not silently
// rewritten into an SSH Host.
func TestRemovedProtocolIDsGetNoCompatibilityMapping(t *testing.T) {
	for _, protocol := range []string{"local", "mosh", "et", "MOSH"} {
		var host Host
		document := `{"id":"old","label":"web","hostname":"example.com","protocol":"` + protocol + `"}`
		if err := json.Unmarshal([]byte(document), &host); err != nil {
			t.Fatalf("%s: unmarshal: %v", protocol, err)
		}
		normalized := NormalizeHost(host)
		if normalized.Transport != TransportPlain {
			t.Errorf("%s: NormalizeHost invented transport %q", protocol, normalized.Transport)
		}
		if err := normalized.Validate(); err == nil {
			t.Errorf("%s: a removed protocol still validates", protocol)
		}
	}
}

func TestNormalizeHostCanonicalizesTheTransport(t *testing.T) {
	cases := []struct {
		name      string
		host      Host
		protocol  Protocol
		transport Transport
	}{
		{"empty protocol is ssh", Host{}, ProtocolSSH, TransportPlain},
		{"ssh transport is plain", Host{Protocol: ProtocolSSH, Transport: "ssh"}, ProtocolSSH, TransportPlain},
		{"unknown transport is preserved for validation", Host{Protocol: ProtocolSSH, Transport: "SSH2"}, ProtocolSSH, "ssh2"},
		{"mosh", Host{Protocol: " SSH ", Transport: "Mosh"}, ProtocolSSH, TransportMosh},
		{"et", Host{Protocol: ProtocolSSH, Transport: "ET"}, ProtocolSSH, TransportET},
		{"non-ssh clears transport", Host{Protocol: ProtocolTelnet, Transport: TransportMosh}, ProtocolTelnet, TransportPlain},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			normalized := NormalizeHost(test.host)
			if normalized.Protocol != test.protocol || normalized.Transport != test.transport {
				t.Fatalf("protocol/transport = %q/%q, want %q/%q",
					normalized.Protocol, normalized.Transport, test.protocol, test.transport)
			}
		})
	}
}

func TestUnsupportedProtocolsAndTransportsFailClosed(t *testing.T) {
	unsupported := []Host{
		{Protocol: "local"},
		{Protocol: "mosh"},
		{Protocol: "et"},
		{Protocol: "ssh2"},
		{Protocol: "rdp"},
		{Protocol: ProtocolSSH, Transport: "quic"},
		{Protocol: ProtocolSSH, Transport: "ssh2"},
	}
	for _, host := range unsupported {
		if host.ConnectionSupported() {
			t.Errorf("%q/%q: ConnectionSupported = true", host.Protocol, host.Transport)
		}
		if got := host.SessionTransport(); got != domainprotocol.TransportUnsupported {
			t.Errorf("%q/%q: SessionTransport = %q, want unsupported",
				host.Protocol, host.Transport, got)
		}
		if got := host.Capabilities(); got != (HostCapabilities{}) {
			t.Errorf("%q/%q: capabilities = %+v, want none", host.Protocol, host.Transport, got)
		}
	}

	supported := []Host{
		{},
		{Protocol: ProtocolSSH},
		{Protocol: ProtocolSSH, Transport: TransportMosh},
		{Protocol: ProtocolSSH, Transport: TransportET},
		{Protocol: ProtocolTelnet},
		{Protocol: ProtocolVNC},
	}
	for _, host := range supported {
		if !host.ConnectionSupported() {
			t.Errorf("%q/%q: ConnectionSupported = false", host.Protocol, host.Transport)
		}
	}
}

func TestTransportSelectsSessionTransportAndCapabilities(t *testing.T) {
	tests := []struct {
		name      string
		host      Host
		transport domainprotocol.Transport
		label     Transport
		sshAuth   bool
		file      bool
		forward   bool
		monitor   bool
		proxy     bool
		x11       bool
	}{
		{"plain ssh", Host{Protocol: ProtocolSSH}, domainprotocol.TransportSSH, TransportPlain, true, true, true, true, true, true},
		{"mosh", Host{Protocol: ProtocolSSH, Transport: TransportMosh}, domainprotocol.TransportMosh, TransportMosh, true, false, true, true, true, false},
		{"et", Host{Protocol: ProtocolSSH, Transport: TransportET}, domainprotocol.TransportET, TransportET, true, false, true, true, true, false},
		{"telnet", Host{Protocol: ProtocolTelnet}, domainprotocol.TransportTelnet, TransportPlain, false, false, false, false, true, false},
		{"vnc", Host{Protocol: ProtocolVNC}, domainprotocol.TransportVNC, TransportPlain, false, false, false, false, true, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.host.SessionTransport(); got != test.transport {
				t.Errorf("SessionTransport = %q, want %q", got, test.transport)
			}
			if got := test.host.TransportLabel(); got != test.label {
				t.Errorf("TransportLabel = %q, want %q", got, test.label)
			}
			caps := test.host.Capabilities()
			if caps.SSHAuth != test.sshAuth || caps.FileTransfer != test.file ||
				caps.Forwarding != test.forward || caps.Monitoring != test.monitor ||
				caps.Proxy != test.proxy || caps.X11 != test.x11 {
				t.Errorf("capabilities = %+v", caps)
			}
			// The convenience predicates must agree with the struct.
			if test.host.SupportsFileTransfer() != test.file ||
				test.host.SupportsForwarding() != test.forward ||
				test.host.SupportsMonitoring() != test.monitor ||
				test.host.SupportsProxy() != test.proxy ||
				test.host.X11Capability() != test.x11 {
				t.Errorf("capability predicates disagree with %+v", caps)
			}
		})
	}
}

func TestHostJSONWritesTransportOnlyWhenSet(t *testing.T) {
	plain := Host{ID: "plain", Label: "web", Hostname: "example.com"}
	data, err := json.Marshal(plain)
	if err != nil {
		t.Fatalf("marshal plain: %v", err)
	}
	if got := string(data); containsKey(got, `"transport"`) {
		t.Fatalf("plain SSH wrote a transport: %s", got)
	}

	mosh := plain
	mosh.Transport = TransportMosh
	data, err = json.Marshal(mosh)
	if err != nil {
		t.Fatalf("marshal mosh: %v", err)
	}
	var decoded Host
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal mosh: %v", err)
	}
	if decoded.Transport != TransportMosh {
		t.Fatalf("round-trip transport = %q, want mosh", decoded.Transport)
	}
}

// containsKey reports whether a JSON document has the given quoted key at any
// depth. It is only used to prove omitempty kept a field out.
func containsKey(document, key string) bool {
	for i := 0; i+len(key) <= len(document); i++ {
		if document[i:i+len(key)] == key {
			return true
		}
	}
	return false
}
