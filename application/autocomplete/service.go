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
	// LastCWD is the working directory of the latest execution. It feeds the
	// current-directory ranking bonus; an empty value means the shell never
	// reported one.
	LastCWD string
}

// matchRank orders a candidate by how its command matches the typed line:
// exact and prefix matches share the highest tier, then word prefix, then
// substring. A zero rank means the command does not match at all. Exact and
// prefix share a tier because the spec orders frequency before recency inside
// that tier; a single exact use must not outrank a frequent prefix command.
func matchRank(command, loweredInput string) int {
	switch {
	case command == loweredInput || strings.HasPrefix(command, loweredInput):
		return 3
	case wordPrefix(command, loweredInput):
		return 2
	case strings.Contains(command, loweredInput):
		return 1
	default:
		return 0
	}
}

// wordPrefix reports whether any word of command starts with input.
func wordPrefix(command, input string) bool {
	for _, word := range strings.Fields(command) {
		if strings.HasPrefix(word, input) {
			return true
		}
	}
	return false
}

// rankedSuggestion is the sort key for one suggestion. The match tier is
// compared before frequency and recency, with the cwd bonus breaking a
// remaining tie, so each criterion is strict: a frequent substring can never
// outrank a word prefix.
type rankedSuggestion struct {
	suggestion Suggestion
	rank       int
	lastUsed   time.Time
	inCWD      bool
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
			if newerExecution(entry.RecordedAt, aggregated[index].LastUsed) {
				aggregated[index].LastUsed = entry.RecordedAt
				aggregated[index].LastCWD = strings.TrimSpace(entry.CWD)
			}
			continue
		}
		indices[key] = len(aggregated)
		aggregated = append(aggregated, aggregatedEntry{
			Command: command, Scope: scope, Count: 1, LastUsed: entry.RecordedAt,
			LastCWD: strings.TrimSpace(entry.CWD),
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
	cwd := strings.TrimSpace(entry.CWD)
	s.mu.Lock()
	updated := make([]aggregatedEntry, len(s.entries))
	copy(updated, s.entries)
	found := false
	for index := range updated {
		if updated[index].Scope == scope && updated[index].Command == command {
			updated[index].Count++
			if newerExecution(at, updated[index].LastUsed) {
				updated[index].LastUsed = at
				updated[index].LastCWD = cwd
			}
			found = true
			break
		}
	}
	if !found {
		updated = append(updated, aggregatedEntry{
			Command: command, Scope: scope, Count: 1, LastUsed: at, LastCWD: cwd,
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

// Suggestions returns the ranked journal rows for input in scope without a
// working-directory bonus. SuggestionsIn is the Session path when the shell
// has reported its cwd.
func (s *Service) Suggestions(input, scope string) []Suggestion {
	return s.SuggestionsIn(input, scope, "")
}

// SuggestionsIn returns the ranked journal rows for input in scope. It returns
// nil when autocomplete is off, input is shorter than the configured minimum,
// or nothing matches. Ranking is deterministic: exact and prefix matches come
// before word-prefix and substring matches, then frequency, then recency, with
// a current-directory bonus and the command text as tie-breaks.
func (s *Service) SuggestionsIn(input, scope, cwd string) []Suggestion {
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
	cwd = strings.TrimSpace(cwd)
	ranked := make([]rankedSuggestion, 0, len(entries))
	indices := make(map[string]int, len(entries))
	add := func(candidate rankedSuggestion) {
		if candidate.suggestion.Display == "" {
			candidate.suggestion.Display = candidate.suggestion.Text
		}
		index, ok := indices[candidate.suggestion.Text]
		if !ok {
			indices[candidate.suggestion.Text] = len(ranked)
			ranked = append(ranked, candidate)
			return
		}
		if betterSuggestion(candidate, ranked[index]) {
			ranked[index] = candidate
		}
	}

	for _, entry := range entries {
		if config.Scope == domainterminal.AutocompleteHistoryHost && entry.Scope != scope {
			continue
		}
		rank := matchRank(strings.ToLower(entry.Command), lowered)
		if rank == 0 {
			continue
		}
		inCWD := cwd != "" && entry.LastCWD == cwd
		candidate := rankedSuggestion{
			suggestion: Suggestion{
				Text:      entry.Command,
				Source:    SourceHistory,
				Frequency: entry.Count,
				Score:     suggestionScore(rank, entry.Count, entry.LastUsed, inCWD, now),
			},
			rank:     rank,
			lastUsed: entry.LastUsed,
			inCWD:    inCWD,
		}
		add(candidate)
	}

	if len(ranked) == 0 {
		return nil
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		return betterSuggestion(ranked[i], ranked[j])
	})
	limit := config.MaxSuggestions
	if len(ranked) < limit {
		limit = len(ranked)
	}
	result := make([]Suggestion, 0, limit)
	for _, candidate := range ranked[:limit] {
		result = append(result, candidate.suggestion)
	}
	return result
}

// betterSuggestion orders two candidates by the documented ranking tuple.
func betterSuggestion(left, right rankedSuggestion) bool {
	switch {
	case left.rank != right.rank:
		return left.rank > right.rank
	case left.suggestion.Frequency != right.suggestion.Frequency:
		return left.suggestion.Frequency > right.suggestion.Frequency
	case !left.lastUsed.Equal(right.lastUsed):
		return left.lastUsed.After(right.lastUsed)
	case left.inCWD != right.inCWD:
		return left.inCWD
	default:
		return left.suggestion.Text < right.suggestion.Text
	}
}

// suggestionScore encodes the ranking tuple in the Suggestion's Score: the
// match tier dominates, then frequency, then recency, then the cwd bonus. Each
// term fits below one step of the term before it, so Score orders exactly like
// betterSuggestion; the command text remains the final tie-break and is not
// encoded.
func suggestionScore(rank, frequency int, lastUsed time.Time, inCWD bool, now time.Time) int {
	score := rank*1_000_000 + boundedFrequency(frequency)*1_000 +
		recencyBonus(lastUsed, now)*10
	if inCWD {
		score += cwdBonus
	}
	return score
}

// cwdBonus rewards a command whose latest execution ran in the Session's
// current working directory. It is smaller than one recency step, so it only
// breaks ties between commands with equal frequency and recency; it never
// outweighs either.
const cwdBonus = 1

// newerExecution reports whether at is later than the stored last-execution
// time. A zero stored time means no execution has been recorded yet.
func newerExecution(at, stored time.Time) bool {
	return stored.IsZero() || at.After(stored)
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

// boundedFrequency caps the frequency term so a long-lived command cannot
// overflow the tier above it.
func boundedFrequency(count int) int {
	if count > 100 {
		count = 100
	}
	if count < 0 {
		return 0
	}
	return count
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
