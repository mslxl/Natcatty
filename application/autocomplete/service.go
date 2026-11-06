// Package autocomplete composes command suggestions for a Session from the
// Command History journal. The journal is the only source: there is no
// separately persisted aggregate and no live shell-history file source.
//
// The service owns no UI and no transport: ui/session reads Suggestions while
// the user types, and the app tests drive the same paths as production.
package autocomplete

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	domainhistory "github.com/mslxl/natcatty/domain/history"
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
)

// Bounds that keep a very long typed line from making the popup expensive.
const (
	maxHistoryEntries = 2000
	maxCommandLength  = domainhistory.MaxCommandLength
	maxInputLength    = domainhistory.MaxCommandLength
)

// SourceHistory names the journal rows in a Suggestion. The history surface
// and autocomplete read the same rows, so there is no second source to label.
const SourceHistory = "history"

// Suggestion is one row the popup can show. Text is what Tab inserts; Display
// is what the popup draws.
type Suggestion struct {
	Text        string `json:"text"`
	Display     string `json:"display,omitempty"`
	Description string `json:"description,omitempty"`
	Source      string `json:"source,omitempty"`
	Score       int    `json:"score,omitempty"`
	Frequency   int    `json:"frequency,omitempty"`
}

// Journal is the shared Command History the history surface lists and
// autocomplete reads. The history application service implements it.
type Journal interface {
	Load(context.Context) ([]domainhistory.Entry, error)
	Record(context.Context, domainhistory.Entry) error
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

// WithClock replaces the clock. Tests use it to make recency ranking
// deterministic.
func WithClock(clock func() time.Time) Option {
	return func(service *Service) {
		if clock != nil {
			service.now = clock
		}
	}
}

// aggregatedEntry is one scope/command pair derived from the journal. It is
// the in-memory view autocomplete ranks; the journal stays authoritative.
type aggregatedEntry struct {
	Command  string
	Scope    string
	Count    int
	LastUsed time.Time
}

// Service records commands and composes suggestions. It is safe for
// concurrent use: the reader goroutine records commands while the UI asks for
// suggestions.
type Service struct {
	mu      sync.Mutex
	journal Journal
	config  func() Config
	now     func() time.Time
	entries []aggregatedEntry
}

// NewService builds a Service over the Command History journal. config must be
// non-nil and is called on every request.
func NewService(journal Journal, config func() Config, opts ...Option) *Service {
	service := &Service{
		journal: journal,
		config:  config,
		now:     time.Now,
	}
	for _, option := range opts {
		if option != nil {
			option(service)
		}
	}
	return service
}

// Load reads the journal and rebuilds the aggregated view. A missing or
// unreadable journal leaves the previous view in place. The app calls it at
// startup and after the History surface deletes or clears a row, so a removed
// row stops being suggested without a restart.
func (s *Service) Load(ctx context.Context) {
	if s.journal == nil {
		return
	}
	entries, err := s.journal.Load(ctx)
	if err != nil {
		return
	}
	s.mu.Lock()
	s.entries = aggregate(entries)
	s.mu.Unlock()
}

// aggregate folds per-execution journal rows into one row per scope and
// command, keeping the newest execution time and the execution count.
func aggregate(entries []domainhistory.Entry) []aggregatedEntry {
	indices := make(map[string]int, len(entries))
	aggregated := make([]aggregatedEntry, 0, len(entries))
	for _, entry := range entries {
		command := strings.TrimSpace(entry.Command)
		scope := strings.TrimSpace(entry.Scope)
		if command == "" || scope == "" || len(command) > maxCommandLength {
			continue
		}
		key := scope + "\x00" + command
		if index, ok := indices[key]; ok {
			aggregated[index].Count++
			if entry.RecordedAt.After(aggregated[index].LastUsed) {
				aggregated[index].LastUsed = entry.RecordedAt
			}
			continue
		}
		indices[key] = len(aggregated)
		aggregated = append(aggregated, aggregatedEntry{
			Command: command, Scope: scope, Count: 1, LastUsed: entry.RecordedAt,
		})
	}
	return sanitize(aggregated)
}

// sanitize orders aggregated rows newest first and bounds the set.
func sanitize(entries []aggregatedEntry) []aggregatedEntry {
	cleaned := make([]aggregatedEntry, 0, len(entries))
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
	return cleaned
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
	s.RecordEntry(domainhistory.Entry{
		Scope:      scope,
		Command:    command,
		Source:     domainhistory.SourceSession,
		RecordedAt: at,
	})
}

// RecordEntry remembers one command execution together with the Session and
// Host it ran in and appends it to the Command History journal. The app's
// command hook uses it; Record and RecordAt are the narrower convenience
// forms.
func (s *Service) RecordEntry(entry domainhistory.Entry) {
	command := strings.TrimSpace(entry.Command)
	scope := strings.TrimSpace(entry.Scope)
	if scope == "" {
		scope = domainhistory.NormalizeScope(entry.HostID)
	}
	if command == "" || scope == "" || len(command) > maxCommandLength {
		return
	}
	if entry.RecordedAt.IsZero() {
		entry.RecordedAt = s.now()
	}
	entry.Command = command
	entry.Scope = scope
	entry = domainhistory.NormalizeEntry(entry)

	at := entry.RecordedAt
	s.mu.Lock()
	updated := make([]aggregatedEntry, len(s.entries))
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
		updated = append(updated, aggregatedEntry{
			Command: command, Scope: scope, Count: 1, LastUsed: at,
		})
	}
	if len(updated) > maxHistoryEntries {
		sort.SliceStable(updated, func(i, j int) bool {
			return updated[i].LastUsed.After(updated[j].LastUsed)
		})
		updated = updated[:maxHistoryEntries]
	}
	s.entries = updated
	s.mu.Unlock()

	if s.journal != nil {
		// Persistence is best effort: a failure must not break typing. The
		// journal is the only history path; the view above derives from it.
		_ = s.journal.Record(context.Background(), entry)
	}
}

// Suggestions returns the ranked journal rows for input in scope. It returns
// nil when autocomplete is off, input is shorter than the configured minimum,
// or nothing matches.
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
	entries := make([]aggregatedEntry, len(s.entries))
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
