package session

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	protocolmodel "github.com/mslxl/natcatty/domain/model/protocol"
)

// Provider protocol registry errors.
var (
	// ErrInvalidProviderFactory reports a nil transport factory.
	ErrInvalidProviderFactory = errors.New("session: invalid provider factory")
	// ErrDuplicateProviderProtocol reports a second registration of a protocol
	// a provider already owns.
	ErrDuplicateProviderProtocol = errors.New("session: duplicate provider protocol")
	// ErrProviderUnavailable reports a Host protocol owned by a provider this
	// build has not registered, because the provider is missing or disabled.
	// Opening fails closed: it never falls back to a built-in transport.
	ErrProviderUnavailable = errors.New("session: provider unavailable")
)

// ProviderRegistry maps plugin-owned protocol ids to the Factory that opens
// their transport. Built-in protocols are not registered here: the transport
// factory selects them by Kind, and their names are reserved. A Registry is
// safe for concurrent use; listings are sorted so callers never depend on map
// order.
type ProviderRegistry struct {
	mu        sync.RWMutex
	factories map[protocolmodel.ID]Factory
}

// NewProviderRegistry returns an empty registry.
func NewProviderRegistry() *ProviderRegistry {
	return &ProviderRegistry{factories: make(map[protocolmodel.ID]Factory)}
}

// Register adds a provider factory. It rejects a built-in name, an id that
// does not have the x-<publisher>-<name> shape, a nil factory, and a second
// registration of the same id.
func (r *ProviderRegistry) Register(id protocolmodel.ID, factory Factory) error {
	if protocolmodel.Reserved(id) {
		return fmt.Errorf("%w: %q", protocolmodel.ErrReservedProtocol, id)
	}
	if !protocolmodel.ValidProviderID(id) {
		return fmt.Errorf("%w: %q", protocolmodel.ErrInvalidProtocol, id)
	}
	if factory == nil {
		return ErrInvalidProviderFactory
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.factories[id]; exists {
		return fmt.Errorf("%w: %q", ErrDuplicateProviderProtocol, id)
	}
	r.factories[id] = factory
	return nil
}

// Unregister removes a provider factory. It reports whether id was registered,
// so disabling an already-absent provider is harmless.
func (r *ProviderRegistry) Unregister(id protocolmodel.ID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.factories[id]; !exists {
		return false
	}
	delete(r.factories, id)
	return true
}

// Resolve returns the factory registered for id.
func (r *ProviderRegistry) Resolve(id protocolmodel.ID) (Factory, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	factory, ok := r.factories[id]
	return factory, ok
}

// IDs returns every registered protocol id sorted lexicographically.
func (r *ProviderRegistry) IDs() []protocolmodel.ID {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]protocolmodel.ID, 0, len(r.factories))
	for id := range r.factories {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b protocolmodel.ID) int {
		return strings.Compare(string(a), string(b))
	})
	return ids
}

// Len reports the number of registered provider protocols.
func (r *ProviderRegistry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.factories)
}
