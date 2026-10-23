// Package themes owns the terminal color-scheme catalog and the transactional
// import of iTerm2 .itermcolors files.
//
// Built-in schemes are compiled into domain/model/theme. Custom schemes are
// stored through the Store port, which persists only the imported documents:
// an upgrade never rewrites the built-ins.
package themes

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode"

	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
)

// ErrSave reports that an import parsed correctly but could not be persisted.
// The in-memory catalog is unchanged, so the failure is safe to retry.
var ErrSave = errors.New("themes: could not save the imported theme")

// ErrInvalidITermcolors re-exports the parser's validation error so the UI
// depends on this context rather than the domain package.
var ErrInvalidITermcolors = domaintheme.ErrInvalidITermcolors

// maxCustomThemes bounds a hand-edited or adversarial store. The original had
// no visible limit; 256 is far above normal use and keeps memory bounded.
const maxCustomThemes = 256

// Store persists the custom terminal themes. A missing document is empty; a
// malformed document loads as empty so startup is never blocked.
type Store interface {
	Load(context.Context) ([]domaintheme.TerminalTheme, error)
	Save(context.Context, []domaintheme.TerminalTheme) error
}

// Option customizes a Service.
type Option func(*Service)

// WithChangeHook observes successful loads and imports. It runs after the new
// catalog is published.
func WithChangeHook(fn func([]domaintheme.TerminalTheme)) Option {
	return func(s *Service) { s.onChange = fn }
}

// Service owns the custom terminal themes and exposes the combined catalog.
type Service struct {
	store    Store
	onChange func([]domaintheme.TerminalTheme)

	mu      sync.Mutex
	custom  []domaintheme.TerminalTheme
	problem error
}

// NewService returns a Service backed by store.
func NewService(store Store, opts ...Option) *Service {
	service := &Service{store: store}
	for _, opt := range opts {
		opt(service)
	}
	return service
}

// Load reads the custom schemes. An IO error is returned but the built-in
// catalog still works, and the service records it for callers that care.
func (s *Service) Load(ctx context.Context) error {
	s.mu.Lock()
	store := s.store
	s.mu.Unlock()
	if store == nil {
		return nil
	}
	loaded, err := store.Load(ctx)
	s.mu.Lock()
	s.custom = sanitizeCustomThemes(loaded)
	s.problem = err
	hook := s.onChange
	catalog := append([]domaintheme.TerminalTheme(nil), s.custom...)
	s.mu.Unlock()
	if err == nil && hook != nil {
		hook(catalog)
	}
	return err
}

// Problem returns the last load error, if any.
func (s *Service) Problem() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.problem
}

// CustomThemes returns the imported schemes.
func (s *Service) CustomThemes() []domaintheme.TerminalTheme {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domaintheme.TerminalTheme(nil), s.custom...)
}

// VisibleThemes returns the picker's catalog: the 184 built-ins the original
// showed, then the imported schemes.
func (s *Service) VisibleThemes() []domaintheme.TerminalTheme {
	builtins := domaintheme.VisibleTerminalThemes()
	s.mu.Lock()
	defer s.mu.Unlock()
	return append(builtins, s.custom...)
}

// Find resolves a built-in or imported scheme by ID.
func (s *Service) Find(id string) (domaintheme.TerminalTheme, bool) {
	s.mu.Lock()
	custom := append([]domaintheme.TerminalTheme(nil), s.custom...)
	s.mu.Unlock()
	return domaintheme.FindTerminalTheme(id, custom)
}

// Import parses data as an .itermcolors scheme, saves the enlarged custom
// catalog atomically through the Store, and only then publishes it. name is
// the picked filename; its extension is removed for the display name.
//
// A parse failure returns ErrInvalidITermcolors and changes nothing. A save
// failure returns ErrSave and also changes nothing.
func (s *Service) Import(ctx context.Context, name string, data []byte) (domaintheme.TerminalTheme, error) {
	if err := ctx.Err(); err != nil {
		return domaintheme.TerminalTheme{}, err
	}
	displayName := themeNameFromFile(name)
	parsed, err := domaintheme.ParseITermcolors(data, displayName)
	if err != nil {
		return domaintheme.TerminalTheme{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.custom) >= maxCustomThemes {
		return domaintheme.TerminalTheme{}, fmt.Errorf("themes: custom theme limit of %d reached", maxCustomThemes)
	}
	parsed.ID = nextCustomID(parsed.Name, s.custom)
	next := append(append([]domaintheme.TerminalTheme(nil), s.custom...), parsed)
	if s.store != nil {
		if err := s.store.Save(ctx, next); err != nil {
			return domaintheme.TerminalTheme{}, fmt.Errorf("%w: %v", ErrSave, err)
		}
	}
	s.custom = next
	s.problem = nil
	if s.onChange != nil {
		s.onChange(append([]domaintheme.TerminalTheme(nil), s.custom...))
	}
	return parsed, nil
}

// themeNameFromFile strips a directory and the .itermcolors or .xml suffix.
func themeNameFromFile(name string) string {
	base := name
	if index := strings.LastIndexAny(base, `/\`); index >= 0 {
		base = base[index+1:]
	}
	lower := strings.ToLower(base)
	for _, suffix := range []string{".itermcolors", ".xml"} {
		if strings.HasSuffix(lower, suffix) {
			base = base[:len(base)-len(suffix)]
			break
		}
	}
	base = strings.TrimSpace(base)
	if base == "" {
		return "Imported Theme"
	}
	return base
}

// nextCustomID returns a stable, readable ID. The original used a timestamp;
// a numeric suffix keeps a repeated import distinct without depending on the
// clock, so tests and persisted documents are deterministic.
func nextCustomID(name string, existing []domaintheme.TerminalTheme) string {
	slug := slugify(name)
	if slug == "" {
		slug = "theme"
	}
	base := "custom-" + slug
	used := make(map[string]bool, len(existing)+1)
	for _, preset := range existing {
		used[preset.ID] = true
	}
	if !used[base] {
		return base
	}
	for index := 2; ; index++ {
		candidate := fmt.Sprintf("%s-%d", base, index)
		if !used[candidate] {
			return candidate
		}
	}
}

func slugify(name string) string {
	var out []rune
	previousDash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			out = append(out, r)
			previousDash = false
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			// Keep non-Latin names readable in their own script.
			out = append(out, r)
			previousDash = false
		default:
			if !previousDash && len(out) > 0 {
				out = append(out, '-')
				previousDash = true
			}
		}
	}
	return strings.Trim(string(out), "-")
}

// sanitizeCustomThemes drops malformed records, built-in ID collisions,
// duplicates, and entries past the limit. Every surviving entry is marked
// custom so a hand-edited store cannot present a built-in as editable.
func sanitizeCustomThemes(input []domaintheme.TerminalTheme) []domaintheme.TerminalTheme {
	out := make([]domaintheme.TerminalTheme, 0, len(input))
	seen := make(map[string]bool, len(input))
	for _, preset := range input {
		if len(out) >= maxCustomThemes {
			break
		}
		preset.ID = strings.TrimSpace(preset.ID)
		preset.Name = strings.TrimSpace(preset.Name)
		if preset.ID == "" || preset.Name == "" || seen[preset.ID] {
			continue
		}
		if _, builtin := domaintheme.LookupTerminalTheme(preset.ID); builtin {
			continue
		}
		if domaintheme.ValidateTerminalColors(preset.Colors) != nil {
			continue
		}
		preset.Custom = true
		seen[preset.ID] = true
		out = append(out, preset)
	}
	return out
}
