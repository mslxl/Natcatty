package protocol

import "testing"

// TestBuiltinsAreUniqueAndResolvable keeps the registry the single source of
// truth: every built-in ID resolves to its own descriptor, and the display
// order has no duplicate.
func TestBuiltinsAreUniqueAndResolvable(t *testing.T) {
	seen := make(map[ID]bool)
	for _, descriptor := range Builtins() {
		if descriptor.ID == "" {
			t.Fatalf("a built-in descriptor has no ID: %+v", descriptor)
		}
		if seen[descriptor.ID] {
			t.Fatalf("duplicate built-in protocol %q", descriptor.ID)
		}
		seen[descriptor.ID] = true
		resolved, ok := Resolve(descriptor.ID)
		if !ok || resolved != descriptor {
			t.Fatalf("Resolve(%q) = %+v, %v; want %+v", descriptor.ID, resolved, ok, descriptor)
		}
	}
	for _, id := range []ID{SSH, Telnet, VNC} {
		if !seen[id] {
			t.Fatalf("built-in protocol %q is missing", id)
		}
	}
	// Mosh and ET are SSH transports, not Host protocols. "local" is not a
	// saved Host protocol either.
	for _, id := range []ID{"local", "mosh", "et"} {
		if seen[id] {
			t.Fatalf("%q must not be a built-in Host protocol", id)
		}
		if descriptor, ok := Resolve(id); ok {
			t.Fatalf("Resolve(%q) = %+v, want no descriptor", id, descriptor)
		}
	}
}

// TestCapabilities pins the matrix the Vault editor, the transports, and the
// connectors read. A change here is a product decision, not an incidental
// refactor.
func TestCapabilities(t *testing.T) {
	tests := []struct {
		name      string
		protocol  ID
		transport Transport
		port      int
		sshAuth   bool
		file      bool
		forward   bool
		monitor   bool
		proxy     bool
		x11       bool
		plaintext bool
	}{
		{"ssh", SSH, TransportSSH, 22, true, true, true, true, true, true, false},
		// The Telnet target has no SSH handshake or SSH application channel,
		// but its route can still run through SSH jumps and a proxy.
		{"telnet", Telnet, TransportTelnet, 23, false, false, false, false, true, false, true},
		// VNC is a pixel stream: no SSH application channel, but the route can
		// still run through SSH jumps and a proxy. Plain RFB is unencrypted.
		{"vnc", VNC, TransportVNC, 5900, false, false, false, false, true, false, true},
		// Mosh and ET bootstrap over SSH: they keep SSH authentication and
		// routing but have no SFTP or X11 channel.
		{"mosh", "", TransportMosh, 22, true, false, true, true, true, false, false},
		{"et", "", TransportET, 22, true, false, true, true, true, false, false},
	}
	for _, test := range tests {
		var descriptor Descriptor
		var ok bool
		if test.protocol != "" {
			descriptor, ok = Resolve(test.protocol)
		} else {
			descriptor, ok = DescriptorForTransport(test.transport)
		}
		if !ok {
			t.Fatalf("%s: descriptor resolution failed", test.name)
		}
		if descriptor.Transport != test.transport || descriptor.DefaultPort != test.port {
			t.Fatalf("%s transport/port = %q/%d, want %q/%d",
				test.name, descriptor.Transport, descriptor.DefaultPort, test.transport, test.port)
		}
		got := []bool{
			descriptor.SSHAuth, descriptor.FileTransfer,
			descriptor.Forwarding, descriptor.Monitoring, descriptor.Proxy, descriptor.X11,
			descriptor.Plaintext,
		}
		want := []bool{
			test.sshAuth, test.file, test.forward,
			test.monitor, test.proxy, test.x11, test.plaintext,
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%s capability %d = %v, want %v (all: %+v)",
					test.name, i, got[i], want[i], descriptor)
			}
		}
	}
}

func TestResolveRejectsUnknownIDs(t *testing.T) {
	for _, id := range []ID{"", "serial", "telnet-plugin", "SSH", "local", "mosh", "et"} {
		if descriptor, ok := Resolve(id); ok {
			t.Fatalf("Resolve(%q) = %+v, want no descriptor", id, descriptor)
		}
	}
}

func TestDescriptorForTransportRejectsUnknown(t *testing.T) {
	for _, transport := range []Transport{"", "serial", "ssh2", "SSH"} {
		if descriptor, ok := DescriptorForTransport(transport); ok {
			t.Fatalf("DescriptorForTransport(%q) = %+v, want no descriptor", transport, descriptor)
		}
	}
}

func TestHostAndGroupEligibility(t *testing.T) {
	for _, id := range []ID{SSH, Telnet, VNC} {
		if !ValidHostID(id) {
			t.Fatalf("ValidHostID(%q) = false, want a Host protocol", id)
		}
	}
	// Removed protocol IDs are not Host protocols and get no compatibility
	// mapping.
	for _, id := range []ID{"", "serial", "local", "mosh", "et"} {
		if ValidHostID(id) {
			t.Fatalf("ValidHostID(%q) = true, want rejection", id)
		}
	}
	// A Host Group may use the protocols that make sense as an inherited
	// default; a blank value means the group sets none.
	for _, id := range []ID{"", SSH, Telnet, VNC} {
		if !ValidGroupDefault(id) {
			t.Fatalf("ValidGroupDefault(%q) = false", id)
		}
	}
	for _, id := range []ID{"serial", "local", "mosh", "et"} {
		if ValidGroupDefault(id) {
			t.Fatalf("ValidGroupDefault(%q) = true, want group default rejected", id)
		}
	}
}
