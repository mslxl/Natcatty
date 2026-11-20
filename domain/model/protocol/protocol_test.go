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
	for _, id := range []ID{SSH, Telnet, Local, Mosh, ET} {
		if !seen[id] {
			t.Fatalf("built-in protocol %q is missing", id)
		}
	}
}

// TestCapabilities pins the matrix the Vault editor, the transports, and the
// connectors read. A change here is a product decision, not an incidental
// refactor.
func TestCapabilities(t *testing.T) {
	tests := []struct {
		id        ID
		transport Transport
		port      int
		sshAuth   bool
		file      bool
		forward   bool
		monitor   bool
		proxy     bool
		x11       bool
	}{
		{SSH, TransportSSH, 22, true, true, true, true, true, true},
		{Mosh, TransportMosh, 22, true, false, true, true, true, false},
		{ET, TransportET, 22, true, false, true, true, true, false},
		// The Telnet target has no SSH handshake or SSH application channel,
		// but its route can still run through SSH jumps and a proxy.
		{Telnet, TransportTelnet, 23, false, false, false, false, true, false},
		{Local, TransportLocal, 0, false, false, false, false, false, false},
	}
	for _, test := range tests {
		descriptor, ok := Resolve(test.id)
		if !ok {
			t.Fatalf("Resolve(%q) failed", test.id)
		}
		if descriptor.Transport != test.transport || descriptor.DefaultPort != test.port {
			t.Fatalf("%s transport/port = %q/%d, want %q/%d",
				test.id, descriptor.Transport, descriptor.DefaultPort, test.transport, test.port)
		}
		got := []bool{
			descriptor.SSHAuth, descriptor.FileTransfer,
			descriptor.Forwarding, descriptor.Monitoring, descriptor.Proxy, descriptor.X11,
		}
		want := []bool{
			test.sshAuth, test.file, test.forward,
			test.monitor, test.proxy, test.x11,
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%s capability %d = %v, want %v (all: %+v)",
					test.id, i, got[i], want[i], descriptor)
			}
		}
	}
}

func TestResolveRejectsUnknownIDs(t *testing.T) {
	for _, id := range []ID{"", "serial", "telnet-plugin", "SSH"} {
		if descriptor, ok := Resolve(id); ok {
			t.Fatalf("Resolve(%q) = %+v, want no descriptor", id, descriptor)
		}
	}
}

func TestHostAndGroupEligibility(t *testing.T) {
	for _, id := range []ID{SSH, Telnet, Local, Mosh, ET} {
		if !ValidHostID(id) {
			t.Fatalf("ValidHostID(%q) = false, want a Host protocol", id)
		}
	}
	if ValidHostID("") || ValidHostID("serial") {
		t.Fatal("a blank or unknown value became a Host protocol")
	}
	// A Host Group may use the protocols that make sense as an inherited
	// default; a blank value means the group sets none.
	for _, id := range []ID{"", SSH, Telnet} {
		if !ValidGroupDefault(id) {
			t.Fatalf("ValidGroupDefault(%q) = false", id)
		}
	}
	for _, id := range []ID{Local, Mosh, ET, "serial"} {
		if ValidGroupDefault(id) {
			t.Fatalf("ValidGroupDefault(%q) = true, want group default rejected", id)
		}
	}
}
