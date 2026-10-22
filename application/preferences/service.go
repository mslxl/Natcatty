// Package preferences persists the typed non-AI settings groups owned by
// domain/preferences. Every update is saved before it is published, so the
// Settings window never shows a value the app is not using.
package preferences

import (
	"context"
	"sync"

	domain "github.com/mslxl/natcatty/domain/preferences"
)

// Store loads and saves the preferences document.
type Store interface {
	Load(context.Context) (domain.Settings, error)
	Save(context.Context, domain.Settings) error
}

// Option customizes a Service.
type Option func(*Service)

// WithChangeHook observes successful updates. The app applies live changes
// such as window opacity, terminal font size, and close-confirmation.
func WithChangeHook(fn func(domain.Settings)) Option {
	return func(s *Service) { s.onChange = fn }
}

// Service owns the loaded preferences. It is safe from any goroutine.
type Service struct {
	store    Store
	onChange func(domain.Settings)

	mu       sync.Mutex
	settings domain.Settings
}

// NewService returns a Service backed by store.
func NewService(store Store, opts ...Option) *Service {
	service := &Service{store: store, settings: domain.Defaults()}
	for _, opt := range opts {
		opt(service)
	}
	return service
}

// Load reads the persisted document. A malformed file normalizes to the
// defaults; an IO failure is returned but the service still exposes usable
// defaults so the window opens.
func (s *Service) Load(ctx context.Context) error {
	s.mu.Lock()
	store := s.store
	s.mu.Unlock()
	if store == nil {
		return nil
	}
	settings, err := store.Load(ctx)
	s.mu.Lock()
	s.settings = domain.Normalize(settings)
	s.mu.Unlock()
	return err
}

// Settings returns the current document.
func (s *Service) Settings() domain.Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settings
}

// Update applies change to a copy, normalizes it, saves it, and publishes it.
// A save failure leaves the previous value in place.
func (s *Service) Update(change func(*domain.Settings)) error {
	if change == nil {
		return nil
	}
	s.mu.Lock()
	next := s.settings
	change(&next)
	next = domain.Normalize(next)
	store := s.store
	hook := s.onChange
	s.mu.Unlock()

	if store != nil {
		if err := store.Save(context.Background(), next); err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.settings = next
	s.mu.Unlock()
	if hook != nil {
		hook(next)
	}
	return nil
}

// ResetGroup restores one group to its defaults.
func (s *Service) ResetGroup(group domain.Group) error {
	return s.Update(func(settings *domain.Settings) {
		*settings = domain.ResetGroup(*settings, group)
	})
}

// ResetAll restores every group.
func (s *Service) ResetAll() error {
	return s.Update(func(settings *domain.Settings) {
		*settings = domain.ResetAll()
	})
}

// SetChangeHook installs the change observer.
func (s *Service) SetChangeHook(fn func(domain.Settings)) {
	s.mu.Lock()
	s.onChange = fn
	s.mu.Unlock()
}
