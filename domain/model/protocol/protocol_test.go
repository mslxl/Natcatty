package protocol

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestBuiltinsResolveToTheirTransportFacts(t *testing.T) {
	// These are the values the Host editor, validation, and transport
	// selection relied on before the registry existed.
	tests := []struct {
		id           ID
		transport    Transport
		defaultPort  int
		usesSSHAuth  bool
		groupDefault bool
		hostProtocol bool
	}{
		{SSH, TransportSSH, 22, true, true, true},
		{Telnet, TransportTelnet, 23, false, true, true},
		{Local, TransportLocal, 0, false, false, true},
		{Mosh, TransportMosh, 22, true, false, true},
		{ET, TransportET, 22, true, false, true},
		{Serial, TransportSerial, 0, false, false, false},
	}
	for _, test := range tests {
		t.Run(string(test.id), func(t *testing.T) {
			descriptor, ok := Lookup(test.id)
			if !ok {
				t.Fatalf("Lookup(%q) did not resolve a built-in", test.id)
			}
			if descriptor.Transport != test.transport ||
				descriptor.DefaultPort != test.defaultPort ||
				descriptor.UsesSSHAuth != test.usesSSHAuth ||
				descriptor.GroupDefault != test.groupDefault ||
				descriptor.HostProtocol != test.hostProtocol {
				t.Fatalf("descriptor = %+v, want transport=%q port=%d ssh=%v group=%v host=%v",
					descriptor, test.transport, test.defaultPort, test.usesSSHAuth,
					test.groupDefault, test.hostProtocol)
			}
			if !descriptor.Builtin() || descriptor.ProviderOwned() {
				t.Fatalf("built-in descriptor classified as builtin=%v provider=%v",
					descriptor.Builtin(), descriptor.ProviderOwned())
			}
			if !Reserved(test.id) {
				t.Fatalf("Reserved(%q) = false, want true", test.id)
			}
		})
	}
}

func TestProviderIDsUseTheNamespacedShape(t *testing.T) {
	valid := []ID{
		"x-acme-loopback",
		"x-a-b",
		"x-publisher-name",
		"x-acme2-name3",
	}
	for _, id := range valid {
		if !ValidProviderID(id) {
			t.Errorf("ValidProviderID(%q) = false, want true", id)
		}
		if !ValidHostID(id) {
			t.Errorf("ValidHostID(%q) = false, want true", id)
		}
		if Reserved(id) {
			t.Errorf("Reserved(%q) = true, want false", id)
		}
	}

	invalid := []ID{
		"",
		"gopher",
		"x",
		"x-",
		"x-name",
		"x--name",
		"x-publisher-",
		"X-acme-loop",
		"x-Acme-loop",
		"x-acme_loop",
		"x-acme.loop",
		"x-acme loop",
		ID("x-" + strings.Repeat("a", MaxIDLength)),
	}
	for _, id := range invalid {
		if ValidProviderID(id) {
			t.Errorf("ValidProviderID(%q) = true, want false", id)
		}
		if ValidHostID(id) {
			t.Errorf("ValidHostID(%q) = true, want false", id)
		}
	}
}

func TestValidHostIDRejectsReservedNonHostProtocols(t *testing.T) {
	if ValidHostID(Serial) {
		t.Fatal("ValidHostID accepted serial, which is reserved but not a Host protocol")
	}
}

func TestRegisterRejectsBuiltinAndDuplicateProviderIDs(t *testing.T) {
	registry := NewBuiltinRegistry()

	for _, id := range []ID{SSH, Telnet, Local, Mosh, ET, Serial} {
		err := registry.Register(Descriptor{ID: id, Transport: TransportPlugin, Plugin: "acme"})
		if !errors.Is(err, ErrReservedProtocol) {
			t.Fatalf("Register(%q) error = %v, want ErrReservedProtocol", id, err)
		}
	}

	descriptor := Descriptor{
		ID: "x-acme-loop", Transport: TransportPlugin,
		HostProtocol: true, Plugin: "acme.loop",
	}
	if err := registry.Register(descriptor); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := registry.Register(descriptor); !errors.Is(err, ErrDuplicateProtocol) {
		t.Fatalf("second Register error = %v, want ErrDuplicateProtocol", err)
	}
}

func TestRegisterRejectsDescriptorsThisHostCannotUse(t *testing.T) {
	registry := NewBuiltinRegistry()
	tests := []struct {
		name       string
		descriptor Descriptor
	}{
		{"empty id", Descriptor{Transport: TransportPlugin}},
		{"malformed id", Descriptor{ID: "acme-loop", Transport: TransportPlugin}},
		{"unknown transport", Descriptor{ID: "x-acme-loop", Transport: "carrier-pigeon"}},
		{"provider on a builtin transport", Descriptor{ID: "x-acme-loop", Transport: TransportSSH}},
		{"negative port", Descriptor{ID: "x-acme-loop", Transport: TransportPlugin, DefaultPort: -1}},
		{"port too large", Descriptor{ID: "x-acme-loop", Transport: TransportPlugin, DefaultPort: 65536}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := registry.Register(test.descriptor); !errors.Is(err, ErrInvalidProtocol) {
				t.Fatalf("Register error = %v, want ErrInvalidProtocol", err)
			}
		})
	}
}

func TestRegistryListingsAreSorted(t *testing.T) {
	registry := NewBuiltinRegistry()
	for _, id := range []ID{"x-zeta-z", "x-alpha-a", "x-mid-m"} {
		if err := registry.Register(Descriptor{
			ID: id, Transport: TransportPlugin, HostProtocol: true, Plugin: "acme",
		}); err != nil {
			t.Fatalf("Register(%q): %v", id, err)
		}
	}
	ids := registry.IDs()
	if !slices.IsSorted(ids) {
		t.Fatalf("IDs() = %v, want lexical order", ids)
	}
	if ids[0] != "et" || ids[len(ids)-1] != "x-zeta-z" {
		t.Fatalf("IDs() = %v, want the built-ins and providers in lexical order", ids)
	}
	descriptors := registry.Descriptors()
	for i := 1; i < len(descriptors); i++ {
		if descriptors[i-1].ID >= descriptors[i].ID {
			t.Fatalf("Descriptors() is not strictly sorted at %d: %+v", i, descriptors)
		}
	}
}

func TestRegistryIsSafeForConcurrentReadsAndWrites(t *testing.T) {
	registry := NewBuiltinRegistry()
	const count = 64
	var wait sync.WaitGroup
	wait.Add(count)
	for i := 0; i < count; i++ {
		go func(i int) {
			defer wait.Done()
			id := ID(fmt.Sprintf("x-publisher-name%d", i))
			if err := registry.Register(Descriptor{
				ID: id, Transport: TransportPlugin, HostProtocol: true, Plugin: "acme",
			}); err != nil {
				t.Errorf("Register(%q): %v", id, err)
				return
			}
			if _, ok := registry.Resolve(id); !ok {
				t.Errorf("Resolve(%q) did not find the registration", id)
			}
			_ = registry.IDs()
			_ = registry.Descriptors()
		}(i)
	}
	wait.Wait()

	if got := registry.Len(); got != count+len(BuiltinIDs()) {
		t.Fatalf("Len() = %d, want %d", got, count+len(BuiltinIDs()))
	}
}

func TestConcurrentDuplicateRegistrationHasOneWinner(t *testing.T) {
	registry := NewRegistry()
	descriptor := Descriptor{
		ID: "x-acme-loop", Transport: TransportPlugin, HostProtocol: true, Plugin: "acme",
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			results <- registry.Register(descriptor)
		}()
	}
	close(start)

	var accepted, rejected int
	for i := 0; i < 2; i++ {
		switch err := <-results; {
		case err == nil:
			accepted++
		case errors.Is(err, ErrDuplicateProtocol):
			rejected++
		default:
			t.Fatalf("Register error = %v, want nil or ErrDuplicateProtocol", err)
		}
	}
	if accepted != 1 || rejected != 1 {
		t.Fatalf("accepted=%d rejected=%d, want exactly one of each", accepted, rejected)
	}
}
