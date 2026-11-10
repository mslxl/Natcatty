package plugin

import (
	"context"
	"sync"

	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// MemoryTrustStore is the in-memory trust-on-first-use store used when the
// composition supplies no persistent store. It enforces the same source-change
// rule for the life of the process; the platform adapter persists pins across
// restarts.
type MemoryTrustStore struct {
	mu   sync.Mutex
	pins map[string]domainplugin.SourcePin
}

// NewMemoryTrustStore returns an empty in-memory pin store.
func NewMemoryTrustStore() *MemoryTrustStore {
	return &MemoryTrustStore{pins: make(map[string]domainplugin.SourcePin)}
}

// SourcePin returns the pin for id, if any.
func (s *MemoryTrustStore) SourcePin(_ context.Context,
	id string) (domainplugin.SourcePin, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pin, ok := s.pins[id]
	return pin, ok, nil
}

// SaveSourcePin records or replaces the pin for its plugin id.
func (s *MemoryTrustStore) SaveSourcePin(_ context.Context,
	pin domainplugin.SourcePin) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pins[pin.ID] = pin
	return nil
}

// MemoryPreferencesStore is the in-memory registry policy store used when the
// composition supplies no persistent store. Its zero value is the safe
// default: automatic updates off and every plugin on stable.
type MemoryPreferencesStore struct {
	mu          sync.Mutex
	preferences domainplugin.RegistryPreferences
}

// NewMemoryPreferencesStore returns an empty policy store.
func NewMemoryPreferencesStore() *MemoryPreferencesStore {
	return &MemoryPreferencesStore{}
}

// RegistryPreferences returns a copy of the current policy.
func (s *MemoryPreferencesStore) RegistryPreferences(
	_ context.Context) (domainplugin.RegistryPreferences, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return clonePreferences(s.preferences), nil
}

// SaveRegistryPreferences replaces the current policy.
func (s *MemoryPreferencesStore) SaveRegistryPreferences(
	_ context.Context, preferences domainplugin.RegistryPreferences) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.preferences = clonePreferences(preferences)
	return nil
}

func clonePreferences(preferences domainplugin.RegistryPreferences) domainplugin.RegistryPreferences {
	out := domainplugin.RegistryPreferences{AutomaticUpdates: preferences.AutomaticUpdates}
	if preferences.Channels != nil {
		out.Channels = make(map[string]domainplugin.Channel, len(preferences.Channels))
		for id, channel := range preferences.Channels {
			out.Channels[id] = channel
		}
	}
	return out
}
