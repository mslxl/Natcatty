package autocomplete

import (
	"context"
	"errors"
	"testing"
	"time"

	domainterminal "github.com/mslxl/natcatty/domain/terminal"
)

type memoryStore struct {
	entries []Entry
	saves   int
	err     error
}

func (s *memoryStore) Load(context.Context) ([]Entry, error) {
	if s.err != nil {
		return nil, s.err
	}
	return append([]Entry(nil), s.entries...), nil
}

func (s *memoryStore) Save(_ context.Context, entries []Entry) error {
	if s.err != nil {
		return s.err
	}
	s.saves++
	s.entries = append([]Entry(nil), entries...)
	return nil
}

type staticSource struct {
	name    string
	entries []Suggestion
	calls   int
	last    string
}

func (s *staticSource) Name() string { return s.name }
func (s *staticSource) Suggestions(input string, limit int) []Suggestion {
	s.calls++
	s.last = input
	result := make([]Suggestion, 0, len(s.entries))
	for _, entry := range s.entries {
		if len(result) >= limit {
			break
		}
		result = append(result, entry)
	}
	return result
}

func defaultConfig() Config {
	return Config{
		Enabled:        true,
		MinChars:       1,
		MaxSuggestions: 50,
		DebounceMs:     100,
		Scope:          domainterminal.AutocompleteHistoryHost,
	}
}

func newTestService(t *testing.T, store Store, config Config, opts ...Option) *Service {
	t.Helper()
	service := NewService(store, func() Config { return config }, opts...)
	service.Load(context.Background())
	return service
}

func TestSuggestionsComeFromRecordedHistory(t *testing.T) {
	store := &memoryStore{}
	service := newTestService(t, store, defaultConfig())
	service.Record("local", "git status")
	service.Record("local", "git status")
	service.Record("local", "git stash")
	service.Record("local", "make test")

	got := service.Suggestions("git s", "local")
	if len(got) != 2 {
		t.Fatalf("suggestions = %+v, want git status and git stash", got)
	}
	if got[0].Text != "git status" || got[0].Frequency != 2 {
		t.Fatalf("first = %+v, want the twice-used command", got[0])
	}
	if got[1].Text != "git stash" {
		t.Fatalf("second = %+v", got[1])
	}
	for _, suggestion := range got {
		if suggestion.Source != "history" {
			t.Fatalf("source = %q, want history", suggestion.Source)
		}
	}
}

func TestSuggestionsRespectTheHistoryScope(t *testing.T) {
	store := &memoryStore{}
	config := defaultConfig()
	service := NewService(store, func() Config { return config })
	service.Load(context.Background())
	service.Record("host:a", "deploy staging")
	service.Record("host:b", "deploy production")

	if got := service.Suggestions("deploy", "host:a"); len(got) != 1 ||
		got[0].Text != "deploy staging" {
		t.Fatalf("host scope = %+v, want only host:a", got)
	}
	config.Scope = domainterminal.AutocompleteHistoryGlobal
	if got := service.Suggestions("deploy", "host:a"); len(got) != 2 {
		t.Fatalf("global scope = %+v, want both hosts", got)
	}
}

func TestSuggestionsComeFromConfiguredSources(t *testing.T) {
	source := &staticSource{name: "shell", entries: []Suggestion{
		{Text: "kubectl get pods", Display: "kubectl get pods", Source: "shell", Score: 2000},
		{Text: "unrelated", Display: "unrelated", Source: "shell", Score: 10},
	}}
	service := newTestService(t, &memoryStore{}, defaultConfig(), WithSource(source))
	service.Record("local", "kubectl rollout status")

	got := service.Suggestions("kubectl", "local")
	if source.calls != 1 || source.last != "kubectl" {
		t.Fatalf("source calls = %d input = %q", source.calls, source.last)
	}
	if len(got) != 2 {
		t.Fatalf("suggestions = %+v, want history and shell", got)
	}
	if got[0].Text != "kubectl get pods" || got[0].Source != "shell" {
		t.Fatalf("first = %+v, want the configured-source suggestion", got[0])
	}
	if got[1].Text != "kubectl rollout status" || got[1].Source != "history" {
		t.Fatalf("second = %+v", got[1])
	}
}

func TestSuggestionsDedupeAndPreferTheHigherScore(t *testing.T) {
	source := &staticSource{name: "shell", entries: []Suggestion{
		{Text: "ls -la", Source: "shell", Score: 900},
	}}
	service := newTestService(t, &memoryStore{}, defaultConfig(), WithSource(source))
	service.Record("local", "ls -la")

	got := service.Suggestions("ls", "local")
	if len(got) != 1 || got[0].Source != "history" {
		// The recorded history scores above a bare source suggestion.
		t.Fatalf("suggestions = %+v, want one history entry", got)
	}
}

func TestSuggestionsHonorEnabledMinimumCharsAndLimit(t *testing.T) {
	store := &memoryStore{}
	config := defaultConfig()
	config.MinChars = 3
	config.MaxSuggestions = 2
	service := NewService(store, func() Config { return config })
	service.Load(context.Background())
	for _, command := range []string{"alpha one", "alpha two", "alpha three"} {
		service.Record("local", command)
	}

	if got := service.Suggestions("al", "local"); got != nil {
		t.Fatalf("short input returned %+v", got)
	}
	got := service.Suggestions("alpha", "local")
	if len(got) != 2 {
		t.Fatalf("limit = %+v, want 2", got)
	}
	if got[0].Text != "alpha one" || got[1].Text != "alpha two" {
		t.Fatalf("order = %+v", got)
	}

	config.Enabled = false
	if got := service.Suggestions("alpha", "local"); got != nil {
		t.Fatalf("disabled autocomplete returned %+v", got)
	}
}

func TestSuggestionsRankPrefixAboveSubstringAndRecentAboveOld(t *testing.T) {
	store := &memoryStore{}
	service := NewService(store, defaultConfig, WithClock(func() time.Time {
		return time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	}))
	service.Load(context.Background())
	service.RecordAt("local", "echo git", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	service.RecordAt("local", "git rebase", time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC))
	service.RecordAt("local", "git status", time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC))

	got := service.Suggestions("git", "local")
	if len(got) != 3 {
		t.Fatalf("suggestions = %+v", got)
	}
	if got[0].Text != "git status" || got[1].Text != "git rebase" || got[2].Text != "echo git" {
		t.Fatalf("order = %+v, want prefix matches newest first and the substring last", got)
	}
}

func TestRecordPersistsDeduplicatedHistory(t *testing.T) {
	store := &memoryStore{}
	service := newTestService(t, store, defaultConfig())
	service.Record("local", "  ls  ")
	service.Record("local", "ls")
	service.Record("local", "")

	if len(store.entries) != 1 {
		t.Fatalf("saved entries = %+v", store.entries)
	}
	if store.entries[0].Command != "ls" || store.entries[0].Count != 2 {
		t.Fatalf("entry = %+v", store.entries[0])
	}
	if store.saves != 2 {
		t.Fatalf("saves = %d, want 2", store.saves)
	}
}

func TestLoadKeepsTheNewestBoundedHistory(t *testing.T) {
	entries := make([]Entry, maxHistoryEntries+10)
	for index := range entries {
		entries[index] = Entry{Command: string(rune('a' + index%26)), Scope: "local", Count: 1}
	}
	store := &memoryStore{entries: entries}
	service := newTestService(t, store, defaultConfig())
	if len(service.entries) != maxHistoryEntries {
		t.Fatalf("loaded entries = %d, want %d", len(service.entries), maxHistoryEntries)
	}
}

func TestLoadFailureKeepsAutocompleteUsable(t *testing.T) {
	store := &memoryStore{err: errors.New("disk")}
	service := newTestService(t, store, defaultConfig())
	service.Record("local", "pwd")
	if got := service.Suggestions("pw", "local"); len(got) != 1 {
		t.Fatalf("suggestions after a load failure = %+v", got)
	}
}
