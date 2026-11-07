package autocomplete

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	domainhistory "github.com/mslxl/natcatty/domain/history"
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
)

func journalEntryFor(command, scope string, at time.Time) domainhistory.Entry {
	return domainhistory.Entry{Command: command, Scope: scope, RecordedAt: at}
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

func newTestService(t *testing.T, journal Journal, config Config, opts ...Option) *Service {
	t.Helper()
	service := NewService(journal, func() Config { return config }, opts...)
	service.Load(context.Background())
	return service
}

func TestSuggestionsComeFromTheJournal(t *testing.T) {
	journal := &journalMemory{}
	service := newTestService(t, journal, defaultConfig())
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
		if suggestion.Source != SourceHistory {
			t.Fatalf("source = %q, want %q", suggestion.Source, SourceHistory)
		}
	}
}

func TestSuggestionsRespectTheHistoryScope(t *testing.T) {
	journal := &journalMemory{}
	config := defaultConfig()
	service := NewService(journal, func() Config { return config })
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

func TestSuggestionsHonorEnabledMinimumCharsAndLimit(t *testing.T) {
	journal := &journalMemory{}
	config := defaultConfig()
	config.MinChars = 3
	config.MaxSuggestions = 2
	service := NewService(journal, func() Config { return config })
	service.Load(context.Background())
	// Equal frequency and recency keep the limit deterministic and expose the
	// command-text tie-break.
	fixed := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	for _, command := range []string{"alpha one", "alpha two", "alpha three"} {
		service.RecordAt("local", command, fixed)
	}

	if got := service.Suggestions("al", "local"); got != nil {
		t.Fatalf("short input returned %+v", got)
	}
	got := service.Suggestions("alpha", "local")
	if len(got) != 2 {
		t.Fatalf("limit = %+v, want 2", got)
	}
	if got[0].Text != "alpha one" || got[1].Text != "alpha three" {
		t.Fatalf("order = %+v, want the deterministic text tie-break", got)
	}

	config.Enabled = false
	if got := service.Suggestions("alpha", "local"); got != nil {
		t.Fatalf("disabled autocomplete returned %+v", got)
	}
}

func TestSuggestionsRankPrefixAboveSubstringAndRecentAboveOld(t *testing.T) {
	journal := &journalMemory{}
	service := NewService(journal, defaultConfig, WithClock(func() time.Time {
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

func TestSuggestionsRankExactAndPrefixThenWordPrefixThenSubstring(t *testing.T) {
	journal := &journalMemory{}
	service := NewService(journal, defaultConfig, WithClock(func() time.Time {
		return time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	}))
	service.Load(context.Background())
	// Every row has the same execution count and recency, so only the match
	// tier separates them.
	for _, command := range []string{"legit", "run git status", "git status", "git"} {
		service.RecordAt("local", command, time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC))
	}
	got := service.Suggestions("git", "local")
	want := []string{"git", "git status", "run git status", "legit"}
	if len(got) != len(want) {
		t.Fatalf("suggestions = %+v, want %v", got, want)
	}
	for index, command := range want {
		if got[index].Text != command {
			t.Fatalf("rank %d = %q, want %q (all: %+v)", index, got[index].Text, command, got)
		}
	}
}

// TestSuggestionsRankFrequencyInsideTheExactPrefixTier pins the tier boundary:
// an exact match and a prefix match are equal for ranking, so a frequently
// used prefix command beats a once-used exact command.
func TestSuggestionsRankFrequencyInsideTheExactPrefixTier(t *testing.T) {
	journal := &journalMemory{}
	service := NewService(journal, defaultConfig, WithClock(func() time.Time {
		return time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	}))
	service.Load(context.Background())
	at := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	service.RecordAt("local", "git", at)
	service.RecordAt("local", "git status", at)
	service.RecordAt("local", "git status", at)

	got := service.Suggestions("git", "local")
	if len(got) != 2 || got[0].Text != "git status" || got[1].Text != "git" {
		t.Fatalf("suggestions = %+v, want the frequent prefix command first", got)
	}
}

func TestSuggestionsRankFrequencyBeforeRecency(t *testing.T) {
	journal := &journalMemory{}
	service := NewService(journal, defaultConfig, WithClock(func() time.Time {
		return time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	}))
	service.Load(context.Background())
	old := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for index := 0; index < 3; index++ {
		service.RecordAt("local", "git frequent", old)
	}
	service.RecordAt("local", "git recent", time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC))
	got := service.Suggestions("git", "local")
	if len(got) != 2 || got[0].Text != "git frequent" {
		t.Fatalf("suggestions = %+v, want the more frequent command first", got)
	}

	// Equal frequency falls back to recency.
	service.RecordAt("local", "git newer", time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC))
	service.RecordAt("local", "git older", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	got = service.Suggestions("git", "local")
	if got[0].Text != "git frequent" {
		t.Fatalf("first = %q, want the most frequent", got[0].Text)
	}
	newer, older := -1, -1
	for index, suggestion := range got {
		switch suggestion.Text {
		case "git newer":
			newer = index
		case "git older":
			older = index
		}
	}
	if newer < 0 || older < 0 || newer > older {
		t.Fatalf("equal-frequency order = %+v, want newer before older", got)
	}
}

func TestSuggestionsPreferACommandLastRunInTheCurrentDirectory(t *testing.T) {
	journal := &journalMemory{
		entries: []domainhistory.Entry{
			{
				Command: "git status", Scope: "local", CWD: "/work/project",
				RecordedAt: time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC),
			},
			{
				Command: "git stash", Scope: "local", CWD: "/work/other",
				RecordedAt: time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC),
			},
		},
	}
	service := NewService(journal, defaultConfig, WithClock(func() time.Time {
		return time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	}))
	service.Load(context.Background())

	got := service.SuggestionsIn("git st", "local", "/work/project")
	if len(got) != 2 || got[0].Text != "git status" {
		t.Fatalf("cwd suggestions = %+v, want the command last run in the cwd first", got)
	}
	// Without a current directory there is no bonus; the tie breaks by text so
	// the result stays deterministic.
	got = service.SuggestionsIn("git st", "local", "")
	if len(got) != 2 || got[0].Text != "git stash" {
		t.Fatalf("no-cwd suggestions = %+v, want the deterministic tie-break", got)
	}
}

// TestSuggestionScoreMatchesTheReturnedOrder keeps the informational Score
// honest: the cwd bonus only breaks ties left after frequency and recency, so
// it must not lift an older cwd match above a newer command elsewhere.
func TestSuggestionScoreMatchesTheReturnedOrder(t *testing.T) {
	journal := &journalMemory{
		entries: []domainhistory.Entry{
			{
				Command: "git older here", Scope: "local", CWD: "/work/project",
				RecordedAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
			},
			{
				Command: "git newer there", Scope: "local", CWD: "/work/other",
				RecordedAt: time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC),
			},
		},
	}
	service := NewService(journal, defaultConfig, WithClock(func() time.Time {
		return time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	}))
	service.Load(context.Background())

	got := service.SuggestionsIn("git", "local", "/work/project")
	if len(got) != 2 || got[0].Text != "git newer there" {
		t.Fatalf("suggestions = %+v, want the newer command first", got)
	}
	if got[0].Score <= got[1].Score {
		t.Fatalf("scores newer=%d older-cwd=%d; want Score to follow the order",
			got[0].Score, got[1].Score)
	}
}

// TestScoreAndOrderAgreeWithinOneRecencyBucket pins the case a bucketed score
// got wrong: even when both commands are within the same day, an older cwd
// match must not outrank a newer command, and Score must say so.
func TestScoreAndOrderAgreeWithinOneRecencyBucket(t *testing.T) {
	journal := &journalMemory{
		entries: []domainhistory.Entry{
			{
				Command: "git older here", Scope: "local", CWD: "/work/project",
				RecordedAt: time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC),
			},
			{
				Command: "git newer there", Scope: "local", CWD: "/work/other",
				RecordedAt: time.Date(2026, 10, 20, 11, 0, 0, 0, time.UTC),
			},
		},
	}
	service := NewService(journal, defaultConfig, WithClock(func() time.Time {
		return time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	}))
	service.Load(context.Background())

	got := service.SuggestionsIn("git", "local", "/work/project")
	if len(got) != 2 || got[0].Text != "git newer there" {
		t.Fatalf("suggestions = %+v, want the newer command first", got)
	}
	if got[1].Frequency != got[0].Frequency || got[0].Score <= got[1].Score {
		t.Fatalf("scores newer=%d older-cwd=%d; want Score to follow the order",
			got[0].Score, got[1].Score)
	}
}

// TestSuggestionScoreStepsHoldAcrossTheWholeTimeRange pins the packing bounds:
// a zero or pre-epoch LastUsed is still older than every real timestamp in
// Go's range, and a one-use older command must score above a zero-use newest
// one, because the comparator ranks frequency before recency.
func TestSuggestionScoreStepsHoldAcrossTheWholeTimeRange(t *testing.T) {
	oldest := time.Time{}
	newest := time.Date(9999, 12, 31, 23, 59, 59, 999_000_000, time.UTC)
	if older := suggestionScore(3, 1, oldest, false); older <= suggestionScore(3, 0, newest, true) {
		t.Fatalf("a used oldest command scored %d, below a zero-use newest command",
			older)
	}
	if higherRank := suggestionScore(3, 0, oldest, false); higherRank <= suggestionScore(2, 100, newest, true) {
		t.Fatalf("rank 3 scored %d, below rank 2 at maximum frequency", higherRank)
	}
}

func TestAggregationKeepsTheCwdOfTheLatestExecution(t *testing.T) {
	journal := &journalMemory{
		entries: []domainhistory.Entry{
			{Command: "git status", Scope: "local", CWD: "/old", RecordedAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)},
			{Command: "git status", Scope: "local", CWD: "/new", RecordedAt: time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)},
		},
	}
	service := newTestService(t, journal, defaultConfig())
	if got := service.SuggestionsIn("git status", "local", "/new"); len(got) != 1 || got[0].Frequency != 2 {
		t.Fatalf("suggestions = %+v, want the aggregated row", got)
	}
	// The bonus follows the newest execution, not the oldest one.
	inNew := service.SuggestionsIn("git status", "local", "/new")[0].Score
	inOld := service.SuggestionsIn("git status", "local", "/old")[0].Score
	if inNew <= inOld {
		t.Fatalf("scores new=%d old=%d; want the bonus only for the latest cwd", inNew, inOld)
	}
}

func TestLoadKeepsTheNewestBoundedHistory(t *testing.T) {
	journal := &journalMemory{}
	for index := 0; index < maxHistoryEntries+10; index++ {
		journal.entries = append(journal.entries, journalEntryFor(
			fmt.Sprintf("command-%d", index), "local", time.Time{}))
	}
	service := newTestService(t, journal, defaultConfig())
	if len(service.entries) != maxHistoryEntries {
		t.Fatalf("loaded entries = %d, want %d", len(service.entries), maxHistoryEntries)
	}
}

func TestLoadFailureKeepsThePreviousView(t *testing.T) {
	journal := &journalMemory{}
	service := newTestService(t, journal, defaultConfig())
	service.Record("local", "pwd")
	journal.err = errors.New("disk")
	service.Load(context.Background())
	if got := service.Suggestions("pw", "local"); len(got) != 1 {
		t.Fatalf("suggestions after a load failure = %+v", got)
	}
}

// TestJournalIsTheOnlySuggestionSource pins the single-source behaviour: the
// only rows that can surface are the journal's, and any row that is not in the
// journal can never become a suggestion.
func TestJournalIsTheOnlySuggestionSource(t *testing.T) {
	journal := &journalMemory{}
	journal.entries = []domainhistory.Entry{
		journalEntryFor("git status", "local", time.Now()),
	}
	service := newTestService(t, journal, defaultConfig())

	// A command that exists only outside the journal is never suggested.
	if got := service.Suggestions("kubectl", "local"); got != nil {
		t.Fatalf("suggestions outside the journal = %+v", got)
	}
	got := service.Suggestions("git", "local")
	if len(got) != 1 || got[0].Text != "git status" || got[0].Source != SourceHistory {
		t.Fatalf("suggestions = %+v, want the journal row", got)
	}
}
