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
	mu sync.Mutex
	// journalMu serializes journal I/O between Load and RecordEntry so a
	// snapshot cannot overwrite a completion recorded concurrently. Journal
	// implementations must not call back into the Service from Load or Record.
	journalMu sync.Mutex
	journal   Journal
	config    func() Config
	now       func() time.Time
	entries   []aggregatedEntry
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
	// Hold journalMu across the read and the replacement so a completion
	// recorded while the snapshot is in flight is not overwritten.
	s.journalMu.Lock()
	defer s.journalMu.Unlock()
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
	s.journalMu.Lock()
	defer s.journalMu.Unlock()
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
	return s.SuggestionsInWith(input, scope, cwd, nil)
}

// SuggestionsInWith is SuggestionsIn over the journal plus extra transient
// rows. A Host-Atuin Session passes its in-memory commands here so they are
// suggestible without ever being journaled. The extra rows are aggregated the
// same way and filtered by the same enabled, minimum-length, scope, and limit
// rules; they are never written back to the service's view and never reach the
// journal.
func (s *Service) SuggestionsInWith(input, scope, cwd string, extra []domainhistory.Entry) []Suggestion {
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
	// aggregate returns a fresh slice; appending it leaves the service's own
	// view untouched.
	entries = append(entries, aggregate(extra)...)

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
				Score:     suggestionScore(rank, entry.Count, entry.LastUsed, inCWD),
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

// betterSuggestion orders two candidates by the documented ranking tuple. It
// compares the same bounded frequency the packed Score uses, so counts above
// the cap are ties resolved by recency in both places and Score cannot
// contradict the returned order.
func betterSuggestion(left, right rankedSuggestion) bool {
	leftFrequency := boundedFrequency(left.suggestion.Frequency)
	rightFrequency := boundedFrequency(right.suggestion.Frequency)
	switch {
	case left.rank != right.rank:
		return left.rank > right.rank
	case leftFrequency != rightFrequency:
		return leftFrequency > rightFrequency
	case left.lastUsed.UnixMilli() != right.lastUsed.UnixMilli():
		return left.lastUsed.UnixMilli() > right.lastUsed.UnixMilli()
	case left.inCWD != right.inCWD:
		return left.inCWD
	default:
		return left.suggestion.Text < right.suggestion.Text
	}
}

// The Score packing uses UnixMilli recency, the same precision
// betterSuggestion compares. Across Go's whole Time range, including the zero
// time and pre-epoch imports, the recency term spans less than one frequency
// step, and the bounded frequency term spans less than one rank step. Because
// betterSuggestion also compares the bounded frequency, Score is non-increasing
// over the returned list: equal scores mean the command-text tie-break decided
// the order, and the text itself is not encoded.
const (
	scoreRankStep      int64 = 2_000_000_000_000_000_000
	scoreFrequencyStep int64 = 1_000_000_000_000_000
	scoreRecencyStep   int64 = 2
	// scoreFrequencyCap bounds the frequency term: 1000 * scoreFrequencyStep
	// stays below one rank step, so a long-lived command cannot overflow the
	// tier above it. betterSuggestion applies the same cap.
	scoreFrequencyCap = 1000
	// cwdBonus rewards a command whose latest execution ran in the Session's
	// current working directory. It is smaller than one recency step, so it
	// only breaks ties between commands with equal frequency and recency.
	cwdBonus int64 = 1
)

// suggestionScore encodes the ranking tuple in the Suggestion's Score: the
// match tier dominates, then frequency, then recency, then the cwd bonus.
func suggestionScore(rank, frequency int, lastUsed time.Time, inCWD bool) int {
	recency := lastUsed.UnixMilli()
	score := int64(rank)*scoreRankStep +
		int64(boundedFrequency(frequency))*scoreFrequencyStep +
		recency*scoreRecencyStep
	if inCWD {
		score += cwdBonus
	}
	return int(score)
}

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
// overflow the rank tier above it. betterSuggestion and suggestionScore share
// this function: counts above the cap tie on frequency and fall through to
// recency, so the exported Score stays consistent with the returned order.
func boundedFrequency(count int) int {
	if count > scoreFrequencyCap {
		count = scoreFrequencyCap
	}
	if count < 0 {
		return 0
	}
	return count
}
