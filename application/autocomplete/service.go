// Package autocomplete composes command suggestions for a Session from the
// commands the application recorded and from configured external sources such
// as the local shell's history file.
//
// The service owns no UI and no transport: ui/session reads Suggestions while
// the user types, and app/selftest drives the same paths as production.
package autocomplete

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	domainterminal "github.com/mslxl/natcatty/domain/terminal"
)

// Bounds that keep a malformed history file or a very long typed line from
// making the popup expensive.
const (
	maxHistoryEntries = 2000
	maxCommandLength  = 4096
	maxInputLength    = 4096
)

// Suggested source names. Sources may choose their own; the recorded history
// and the shell-history adapter use these so the UI does not compare raw
// strings.
const (
	SourceHistory      = "history"
	SourceShellHistory = "shell-history"
)

// Entry is one remembered command and the Host scope it ran in. Scope is
// "local" for the local shell or "host:<id>" for an SSH Host.
type Entry struct {
	Command  string    `json:"command"`
	Scope    string    `json:"scope"`
	Count    int       `json:"count"`
	LastUsed time.Time `json:"lastUsed"`
}

// Suggestion is one row the popup can show. Text is what Tab inserts; Display
// is what the popup draws. Source names where the row came from: "history" for
// recorded commands, otherwise the configured source's name.
type Suggestion struct {
	Text        string `json:"text"`
	Display     string `json:"display,omitempty"`
	Description string `json:"description,omitempty"`
	Source      string `json:"source,omitempty"`
	Score       int    `json:"score,omitempty"`
	Frequency   int    `json:"frequency,omitempty"`
}

// Store persists the recorded history. Save receives the full bounded set.
type Store interface {
	Load(context.Context) ([]Entry, error)
	Save(context.Context, []Entry) error
}

// Source contributes suggestions besides the recorded history, for example the
// shell's own history file or a snippet catalog. Implementations must return
// only rows that match input.
type Source interface {
	Name() string
	Suggestions(input string, limit int) []Suggestion
}

// Config is read on each call, so settings changes take effect without
// rebuilding the Session.
type Config struct {
	Enabled        bool
	MinChars       int
	MaxSuggestions int
	DebounceMs     int
	Scope          domainterminal.AutocompleteHistoryScope
}

// Option customizes a Service.
type Option func(*Service)

// WithSource adds a suggestion source. Sources are queried in registration
// order.
func WithSource(source Source) Option {
	return func(service *Service) {
		if source != nil {
			service.sources = append(service.sources, source)
		}
	}
}

// WithClock replaces the clock. Tests use it to make recency ranking
// deterministic.
func WithClock(clock func() time.Time) Option {
	return func(service *Service) {
		if clock != nil {
			service.now = clock
		}
	}
}

// Service records commands and composes suggestions. It is safe for
// concurrent use: the reader goroutine records commands while the UI asks for
// suggestions.
type Service struct {
	mu      sync.Mutex
	store   Store
	config  func() Config
	sources []Source
	now     func() time.Time
	entries []Entry
}

// NewService builds a Service. config must be non-nil and is called on every
// request.
func NewService(store Store, config func() Config, opts ...Option) *Service {
	service := &Service{
		store:  store,
		config: config,
		now:    time.Now,
	}
	for _, option := range opts {
		if option != nil {
			option(service)
		}
	}
	return service
}

// Load reads the persisted history. A missing or unreadable store leaves
// autocomplete usable from the commands recorded in this process.
func (s *Service) Load(ctx context.Context) {
	if s.store == nil {
		return
	}
	entries, err := s.store.Load(ctx)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cleaned := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		entry.Command = strings.TrimSpace(entry.Command)
		entry.Scope = strings.TrimSpace(entry.Scope)
		if entry.Command == "" || entry.Scope == "" ||
			len(entry.Command) > maxCommandLength || entry.Count <= 0 {
			continue
		}
		cleaned = append(cleaned, entry)
	}
	sort.SliceStable(cleaned, func(i, j int) bool {
		return cleaned[i].LastUsed.After(cleaned[j].LastUsed)
	})
	if len(cleaned) > maxHistoryEntries {
		cleaned = cleaned[:maxHistoryEntries]
	}
	s.entries = cleaned
}

// Record remembers a command that was submitted in scope. Blank commands and
// over-long lines are ignored. Consecutive uses of the same command increase
// its frequency instead of duplicating the row.
func (s *Service) Record(scope, command string) {
	s.RecordAt(scope, command, s.now())
}

// RecordAt is Record with an explicit time, used by tests and deterministic
// imports.
func (s *Service) RecordAt(scope, command string, at time.Time) {
	command = strings.TrimSpace(command)
	scope = strings.TrimSpace(scope)
	if command == "" || scope == "" || len(command) > maxCommandLength {
		return
	}
	if at.IsZero() {
		at = s.now()
	}

	s.mu.Lock()
	updated := make([]Entry, len(s.entries))
	copy(updated, s.entries)
	found := false
	for index := range updated {
		if updated[index].Scope == scope && updated[index].Command == command {
			updated[index].Count++
			updated[index].LastUsed = at
			found = true
			break
		}
	}
	if !found {
		updated = append(updated, Entry{Command: command, Scope: scope, Count: 1, LastUsed: at})
	}
	if len(updated) > maxHistoryEntries {
		sort.SliceStable(updated, func(i, j int) bool {
			return updated[i].LastUsed.After(updated[j].LastUsed)
		})
		updated = updated[:maxHistoryEntries]
	}
	s.entries = updated
	s.mu.Unlock()

	if s.store != nil {
		// Persistence is best effort: a failure must not break typing.
		_ = s.store.Save(context.Background(), updated)
	}
}

// Suggestions returns the ranked rows for input in scope. It returns nil when
// autocomplete is off, input is shorter than the configured minimum, or
// nothing matches.
func (s *Service) Suggestions(input, scope string) []Suggestion {
	config := s.normalizedConfig()
	if !config.Enabled {
		return nil
	}
	input = strings.TrimSpace(input)
	if input == "" || len(input) > maxInputLength {
		return nil
	}
	if len([]rune(input)) < config.MinChars {
		return nil
	}

	s.mu.Lock()
	entries := make([]Entry, len(s.entries))
	copy(entries, s.entries)
	s.mu.Unlock()

	now := s.now()
	lowered := strings.ToLower(input)
	result := make([]Suggestion, 0, len(entries))
	indices := make(map[string]int, len(entries))
	add := func(suggestion Suggestion) {
		if suggestion.Display == "" {
			suggestion.Display = suggestion.Text
		}
		if index, ok := indices[suggestion.Text]; ok {
			if result[index].Score < suggestion.Score {
				result[index] = suggestion
			}
			return
		}
		indices[suggestion.Text] = len(result)
		result = append(result, suggestion)
	}

	for _, entry := range entries {
		if config.Scope == domainterminal.AutocompleteHistoryHost && entry.Scope != scope {
			continue
		}
		command := strings.ToLower(entry.Command)
		base := 0
		switch {
		case strings.HasPrefix(command, lowered):
			base = 1000
		case strings.Contains(command, lowered):
			base = 500
		default:
			continue
		}
		add(Suggestion{
			Text:      entry.Command,
			Source:    SourceHistory,
			Frequency: entry.Count,
			Score:     base + countBonus(entry.Count) + recencyBonus(entry.LastUsed, now),
		})
	}

	for _, source := range s.sources {
		for _, suggestion := range source.Suggestions(input, config.MaxSuggestions) {
			suggestion.Text = strings.TrimSpace(suggestion.Text)
			if suggestion.Text == "" {
				continue
			}
			if !strings.Contains(strings.ToLower(suggestion.Text), lowered) {
				continue
			}
			if suggestion.Source == "" {
				suggestion.Source = source.Name()
			}
			add(suggestion)
		}
	}

	if len(result) == 0 {
		return nil
	}
	sort.SliceStable(result, func(i, j int) bool {
		return result[i].Score > result[j].Score
	})
	if len(result) > config.MaxSuggestions {
		result = result[:config.MaxSuggestions]
	}
	return result
}

func (s *Service) normalizedConfig() Config {
	config := Config{}
	if s.config != nil {
		config = s.config()
	}
	config.MinChars = domainterminal.NormalizeAutocompleteMinChars(config.MinChars)
	config.MaxSuggestions = domainterminal.NormalizeAutocompleteMaxSuggestions(config.MaxSuggestions)
	config.DebounceMs = domainterminal.NormalizeAutocompleteDebounce(config.DebounceMs)
	if !config.Scope.Valid() {
		config.Scope = domainterminal.AutocompleteHistoryHost
	}
	return config
}

func countBonus(count int) int {
	if count > 100 {
		count = 100
	}
	if count < 0 {
		return 0
	}
	return count * 10
}

func recencyBonus(lastUsed, now time.Time) int {
	if lastUsed.IsZero() {
		return 0
	}
	age := now.Sub(lastUsed)
	if age < 0 {
		age = 0
	}
	switch {
	case age <= 24*time.Hour:
		return 90
	case age <= 7*24*time.Hour:
		return 60
	case age <= 30*24*time.Hour:
		return 30
	default:
		return 0
	}
}
