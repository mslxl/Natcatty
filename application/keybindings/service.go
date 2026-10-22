// Package keybindings owns the loaded keyboard-binding state: the active
// scheme, user overrides, menu-accelerator suppressions, and conflict
// resolution. The domain package holds the catalog and rules; this service
// persists them and publishes changes.
package keybindings

import (
	"context"
	"errors"
	"sync"

	domain "github.com/mslxl/natcatty/domain/keybindings"
)

// Errors reported by the service.
var (
	ErrInvalidScheme  = errors.New("keybindings: invalid scheme")
	ErrUnknownBinding = errors.New("keybindings: unknown binding")
	ErrInvalidChord   = errors.New("keybindings: invalid chord")
)

// State is the persisted document.
type State struct {
	Scheme          domain.Scheme    `json:"scheme"`
	Overrides       domain.Overrides `json:"overrides,omitempty"`
	SuppressedMenus []string         `json:"suppressedMenus,omitempty"`
}

// Store loads and saves the binding state.
type Store interface {
	Load(context.Context) (State, error)
	Save(context.Context, State) error
}

// Option customizes a Service.
type Option func(*Service)

// WithGOOS sets the platform used for the default scheme and menu
// accelerator comparison. The app passes runtime.GOOS; tests pass a fixed
// value.
func WithGOOS(goos string) Option {
	return func(s *Service) { s.goos = goos }
}

// WithChangeHook observes successful changes. The app rebuilds its menu and
// dispatcher from the published state.
func WithChangeHook(fn func(State)) Option {
	return func(s *Service) { s.onChange = fn }
}

// Service owns the loaded binding state. It is safe from any goroutine.
type Service struct {
	store    Store
	goos     string
	catalog  []domain.Binding
	onChange func(State)

	mu    sync.Mutex
	state State
}

// NewService returns a Service backed by store.
func NewService(store Store, opts ...Option) *Service {
	service := &Service{
		store:   store,
		goos:    "windows",
		catalog: domain.Catalog(),
	}
	service.state = State{Scheme: domain.DefaultScheme(service.goos)}
	for _, opt := range opts {
		opt(service)
	}
	if service.state.Scheme == "" {
		service.state.Scheme = domain.DefaultScheme(service.goos)
	}
	return service
}

// Load reads the persisted state. A missing or malformed record is the
// store's concern; the service still exposes validated defaults.
func (s *Service) Load(ctx context.Context) error {
	s.mu.Lock()
	store := s.store
	defaultScheme := domain.DefaultScheme(s.goos)
	s.mu.Unlock()
	state := State{Scheme: defaultScheme}
	if store != nil {
		loaded, err := store.Load(ctx)
		if err == nil {
			state = loaded
		}
		if scheme, ok := domain.ParseScheme(string(state.Scheme)); ok {
			state.Scheme = scheme
		} else {
			state.Scheme = defaultScheme
		}
		state.Overrides = state.Overrides.Normalize()
		state.SuppressedMenus = dedupe(state.SuppressedMenus)
		s.mu.Lock()
		s.state = state
		s.mu.Unlock()
		return err
	}
	s.mu.Lock()
	s.state = state
	s.mu.Unlock()
	return nil
}

// SetChangeHook installs the change observer.
func (s *Service) SetChangeHook(fn func(State)) {
	s.mu.Lock()
	s.onChange = fn
	s.mu.Unlock()
}

// State returns a deep copy of the persisted state.
func (s *Service) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return State{
		Scheme:          s.state.Scheme,
		Overrides:       s.state.Overrides.Normalize(),
		SuppressedMenus: append([]string(nil), s.state.SuppressedMenus...),
	}
}

// Scheme returns the active scheme.
func (s *Service) Scheme() domain.Scheme {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.Scheme
}

// Catalog returns the original binding catalog.
func (s *Service) Catalog() []domain.Binding { return domain.Catalog() }

// Active returns every binding that fires in the active scheme.
func (s *Service) Active() []domain.Active {
	s.mu.Lock()
	defer s.mu.Unlock()
	return domain.ActiveBindings(s.catalog, s.state.Scheme, s.state.Overrides)
}

// Chord returns the effective chord for one binding under scheme.
func (s *Service) Chord(id string, scheme domain.Scheme) string {
	binding, ok := domain.Lookup(id)
	if !ok {
		return domain.Disabled
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return domain.EffectiveChord(binding, scheme, s.state.Overrides)
}

// IsOverridden reports whether id has a user override for scheme.
func (s *Service) IsOverridden(id string, scheme domain.Scheme) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.Overrides.Has(id, scheme)
}

// MenuSuppressed reports whether the user explicitly cleared a menu
// accelerator by resolving a conflict.
func (s *Service) MenuSuppressed(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, candidate := range s.state.SuppressedMenus {
		if candidate == id {
			return true
		}
	}
	return false
}

// SetScheme activates mac, PC, or disabled and persists the choice.
func (s *Service) SetScheme(scheme domain.Scheme) error {
	if _, ok := domain.ParseScheme(string(scheme)); !ok {
		return ErrInvalidScheme
	}
	return s.persist(func(state *State) { state.Scheme = scheme })
}

// SetBinding records an override, unless it conflicts with another active
// binding or a live menu accelerator. In that case it returns the conflicts
// and changes nothing, so the caller must ask the user to replace or cancel.
func (s *Service) SetBinding(id string, scheme domain.Scheme, chord string,
	menus []domain.MenuShortcut) ([]domain.Conflict, error) {
	_, err := s.validate(id, scheme, chord)
	if err != nil {
		return nil, err
	}
	candidate := s.candidateOverrides(id, scheme, chord)
	conflicts := s.conflictsFor(candidate, scheme, menus)
	if len(conflicts) > 0 {
		return conflicts, nil
	}
	if err := s.persist(func(state *State) {
		state.Overrides = candidate
	}); err != nil {
		return nil, err
	}
	return nil, nil
}

// ReplaceBinding applies an override and resolves every conflict in its
// favour: the displaced bindings become Disabled and a displaced menu
// accelerator is suppressed. This is the explicit "replace" the UI asks for.
func (s *Service) ReplaceBinding(id string, scheme domain.Scheme, chord string,
	menus []domain.MenuShortcut) error {
	if _, err := s.validate(id, scheme, chord); err != nil {
		return err
	}
	candidate := s.candidateOverrides(id, scheme, chord)
	conflicts := s.conflictsFor(candidate, scheme, menus)
	for _, conflict := range conflicts {
		for _, displaced := range conflict.Bindings {
			if displaced.ID == id {
				continue
			}
			candidate = candidate.Set(displaced.ID, scheme, domain.Disabled)
		}
	}
	suppressed := append([]string(nil), s.State().SuppressedMenus...)
	for _, conflict := range conflicts {
		for _, menu := range conflict.Menu {
			suppressed = append(suppressed, menu.ID)
		}
	}
	return s.persist(func(state *State) {
		state.Overrides = candidate
		state.SuppressedMenus = dedupe(suppressed)
	})
}

// Conflicts reports every conflict in the current state.
func (s *Service) Conflicts(menus []domain.MenuShortcut) []domain.Conflict {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conflictsLocked(s.state.Overrides, s.state.Scheme, menus)
}

// ResetBinding restores one binding's default for scheme.
func (s *Service) ResetBinding(id string, scheme domain.Scheme) error {
	if _, ok := domain.Lookup(id); !ok {
		return ErrUnknownBinding
	}
	if scheme != domain.SchemeMac && scheme != domain.SchemePC {
		return ErrInvalidScheme
	}
	return s.persist(func(state *State) {
		state.Overrides = state.Overrides.Reset(id, scheme)
	})
}

// ResetAll restores every binding and every suppressed menu accelerator.
// The scheme is the user's active scheme and is left alone.
func (s *Service) ResetAll() error {
	return s.persist(func(state *State) {
		state.Overrides = domain.Overrides{}
		state.SuppressedMenus = nil
	})
}

func (s *Service) validate(id string, scheme domain.Scheme, chord string) (domain.Binding, error) {
	binding, ok := domain.Lookup(id)
	if !ok {
		return domain.Binding{}, ErrUnknownBinding
	}
	if scheme != domain.SchemeMac && scheme != domain.SchemePC {
		return domain.Binding{}, ErrInvalidScheme
	}
	if chord != domain.Disabled {
		if _, ok := domain.ParseChord(chord); !ok {
			return domain.Binding{}, ErrInvalidChord
		}
	}
	return binding, nil
}

func (s *Service) candidateOverrides(id string, scheme domain.Scheme, chord string) domain.Overrides {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.Overrides.Set(id, scheme, chord)
}

func (s *Service) conflictsFor(overrides domain.Overrides, scheme domain.Scheme,
	menus []domain.MenuShortcut) []domain.Conflict {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conflictsLocked(overrides, scheme, menus)
}

func (s *Service) conflictsLocked(overrides domain.Overrides, scheme domain.Scheme,
	menus []domain.MenuShortcut) []domain.Conflict {
	active := domain.ActiveBindings(s.catalog, scheme, overrides)
	live := make([]domain.MenuShortcut, 0, len(menus))
	for _, menu := range menus {
		if s.menuSuppressedLocked(menu.ID) {
			continue
		}
		live = append(live, menu)
	}
	return domain.FindConflicts(active, live, s.goos)
}

func (s *Service) menuSuppressedLocked(id string) bool {
	for _, candidate := range s.state.SuppressedMenus {
		if candidate == id {
			return true
		}
	}
	return false
}

func (s *Service) persist(change func(*State)) error {
	s.mu.Lock()
	next := State{
		Scheme:          s.state.Scheme,
		Overrides:       s.state.Overrides.Normalize(),
		SuppressedMenus: append([]string(nil), s.state.SuppressedMenus...),
	}
	change(&next)
	next.Overrides = next.Overrides.Normalize()
	next.SuppressedMenus = dedupe(next.SuppressedMenus)
	store := s.store
	s.mu.Unlock()

	if store != nil {
		if err := store.Save(context.Background(), next); err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.state = next
	hook := s.onChange
	s.mu.Unlock()
	if hook != nil {
		hook(next)
	}
	return nil
}

func dedupe(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}
