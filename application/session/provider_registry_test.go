package session

import (
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"

	protocolmodel "github.com/mslxl/natcatty/domain/model/protocol"
)

func TestProviderRegistryAcceptsNamespacedProtocols(t *testing.T) {
	registry := NewProviderRegistry()
	factory := &staticFactory{transport: newFakeTransport()}
	id := protocolmodel.ID("x-acme-loop")
	if err := registry.Register(id, factory); err != nil {
		t.Fatalf("Register: %v", err)
	}
	resolved, ok := registry.Resolve(id)
	if !ok || resolved != factory {
		t.Fatalf("Resolve(%q) = %v, %v; want the registered factory", id, resolved, ok)
	}
	if !registry.Unregister(id) {
		t.Fatal("Unregister reported an absent protocol")
	}
	if _, ok := registry.Resolve(id); ok {
		t.Fatal("Resolve found an unregistered protocol")
	}
	if registry.Unregister(id) {
		t.Fatal("second Unregister reported a removal")
	}
}

func TestProviderRegistryRejectsReservedMalformedAndDuplicateIDs(t *testing.T) {
	registry := NewProviderRegistry()
	factory := &staticFactory{transport: newFakeTransport()}

	for _, id := range []protocolmodel.ID{
		protocolmodel.SSH, protocolmodel.Telnet, protocolmodel.Local,
		protocolmodel.Mosh, protocolmodel.ET, protocolmodel.Serial,
	} {
		err := registry.Register(id, factory)
		if !errors.Is(err, protocolmodel.ErrReservedProtocol) {
			t.Fatalf("Register(%q) error = %v, want ErrReservedProtocol", id, err)
		}
	}

	for _, id := range []protocolmodel.ID{
		"", "gopher", "acme-loop", "x-name", "x--name", "x-acme_loop", "x-acme-loop-",
	} {
		err := registry.Register(id, factory)
		if !errors.Is(err, protocolmodel.ErrInvalidProtocol) {
			t.Fatalf("Register(%q) error = %v, want ErrInvalidProtocol", id, err)
		}
	}

	id := protocolmodel.ID("x-acme-loop")
	if err := registry.Register(id, nil); !errors.Is(err, ErrInvalidProviderFactory) {
		t.Fatalf("Register(nil) error = %v, want ErrInvalidProviderFactory", err)
	}
	if err := registry.Register(id, factory); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := registry.Register(id, factory); !errors.Is(err, ErrDuplicateProviderProtocol) {
		t.Fatalf("second Register error = %v, want ErrDuplicateProviderProtocol", err)
	}
}

func TestProviderRegistryListingsAreSorted(t *testing.T) {
	registry := NewProviderRegistry()
	factory := &staticFactory{transport: newFakeTransport()}
	for _, id := range []protocolmodel.ID{"x-zeta-z", "x-alpha-a", "x-mid-m"} {
		if err := registry.Register(id, factory); err != nil {
			t.Fatalf("Register(%q): %v", id, err)
		}
	}
	ids := registry.IDs()
	if !slices.IsSorted(ids) {
		t.Fatalf("IDs() = %v, want lexical order", ids)
	}
	if registry.Len() != 3 {
		t.Fatalf("Len() = %d, want 3", registry.Len())
	}
}

func TestProviderRegistryIsSafeForConcurrentUse(t *testing.T) {
	registry := NewProviderRegistry()
	factory := &staticFactory{transport: newFakeTransport()}
	const count = 64
	var wait sync.WaitGroup
	wait.Add(count)
	for i := 0; i < count; i++ {
		go func(i int) {
			defer wait.Done()
			id := protocolmodel.ID(fmt.Sprintf("x-publisher-name%d", i))
			if err := registry.Register(id, factory); err != nil {
				t.Errorf("Register(%q): %v", id, err)
				return
			}
			if _, ok := registry.Resolve(id); !ok {
				t.Errorf("Resolve(%q) did not find the registration", id)
			}
			_ = registry.IDs()
		}(i)
	}
	wait.Wait()
	if registry.Len() != count {
		t.Fatalf("Len() = %d, want %d", registry.Len(), count)
	}
}

func TestConcurrentProviderRegistrationHasOneWinner(t *testing.T) {
	registry := NewProviderRegistry()
	factory := &staticFactory{transport: newFakeTransport()}
	id := protocolmodel.ID("x-acme-loop")
	start := make(chan struct{})
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			results <- registry.Register(id, factory)
		}()
	}
	close(start)

	var accepted, rejected int
	for i := 0; i < 2; i++ {
		switch err := <-results; {
		case err == nil:
			accepted++
		case errors.Is(err, ErrDuplicateProviderProtocol):
			rejected++
		default:
			t.Fatalf("Register error = %v, want nil or ErrDuplicateProviderProtocol", err)
		}
	}
	if accepted != 1 || rejected != 1 {
		t.Fatalf("accepted=%d rejected=%d, want exactly one of each", accepted, rejected)
	}
}
