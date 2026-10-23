package search

import (
	"testing"

	appsearch "github.com/mslxl/natcatty/application/search"
)

func records(mode Mode, _ string) []appsearch.Record {
	if mode == ModeCommandPalette {
		return []appsearch.Record{
			{Kind: appsearch.KindCommand, ID: "tab.new", Title: "New Tab", Group: CategoryTab},
			{Kind: appsearch.KindCommand, ID: "tab.close", Title: "Close Tab", Group: CategoryTab},
			{Kind: appsearch.KindCommand, ID: "app.settings", Title: "Settings", Group: CategoryApp},
		}
	}
	return []appsearch.Record{
		{Kind: appsearch.KindHost, ID: "h1", Title: "web", Subtitle: "10.0.0.1"},
		{Kind: appsearch.KindHost, ID: "h2", Title: "db", Subtitle: "10.0.0.2"},
		{Kind: appsearch.KindSession, ID: "s1", Title: "web session"},
	}
}

func newTestState(t *testing.T, opened *[]string) *State {
	t.Helper()
	return New(
		WithSource(records),
		WithOpen(func(mode Mode, result appsearch.Result) bool {
			*opened = append(*opened, result.Record.ID)
			return true
		}),
		WithSection(func(record appsearch.Record) string {
			if record.Kind == appsearch.KindCommand {
				return record.Group
			}
			return string(record.Kind)
		}),
	)
}

// TestEmptyQueryListsSourceOrder keeps the Quick Switcher's Hosts-then-
// Sessions default when nothing is typed.
func TestEmptyQueryListsSourceOrder(t *testing.T) {
	state := newTestState(t, new([]string))
	state.Open(ModeQuickSwitcher)
	results := state.Results()
	if len(results) != 3 {
		t.Fatalf("results = %d, want 3", len(results))
	}
	if results[0].Record.ID != "h1" || results[2].Record.ID != "s1" {
		t.Fatalf("order = %v", ids(results))
	}
}

// TestSectionsKeepSourceOrderAndRankInside proves a matching Session does not
// push Hosts below its own section, while Host rows stay ranked.
func TestSectionsKeepSourceOrderAndRankInside(t *testing.T) {
	state := New(WithSource(func(Mode, string) []appsearch.Record {
		return []appsearch.Record{
			{Kind: appsearch.KindHost, ID: "h1", Title: "db"},
			{Kind: appsearch.KindHost, ID: "h2", Title: "web"},
			{Kind: appsearch.KindSession, ID: "s1", Title: "web"},
		}
	}))
	state.Open(ModeQuickSwitcher)
	*state.QueryPtr() = "web"
	sections := state.Sections()
	if len(sections) != 2 {
		t.Fatalf("sections = %d, want 2", len(sections))
	}
	if sections[0].Key != string(appsearch.KindHost) || sections[1].Key != string(appsearch.KindSession) {
		t.Fatalf("section order = %q, %q", sections[0].Key, sections[1].Key)
	}
	if got := sections[0].Results[0].Record.ID; got != "h2" {
		t.Fatalf("first host = %q, want the matching one", got)
	}
}

// TestTypingResetsSelectionAfterTypeAhead keeps Enter from opening a row the
// new query no longer ranks first.
func TestTypingResetsSelectionAfterTypeAhead(t *testing.T) {
	state := newTestState(t, new([]string))
	state.Open(ModeQuickSwitcher)
	state.Move(1)
	if state.Selected() != 1 {
		t.Fatalf("selected = %d, want 1", state.Selected())
	}
	*state.QueryPtr() = "web"
	state.Sections()
	if state.Selected() != 0 {
		t.Fatalf("selected after typing = %d, want 0", state.Selected())
	}
}

// TestMoveClampsWithoutWrapping matches the original Quick Switcher: the
// arrows stop at the ends.
func TestMoveClampsWithoutWrapping(t *testing.T) {
	state := newTestState(t, new([]string))
	state.Open(ModeQuickSwitcher)
	if state.Move(-1) {
		t.Fatal("moving up from the first row changed the selection")
	}
	if !state.Move(1) || state.Selected() != 1 {
		t.Fatalf("selected = %d, want 1", state.Selected())
	}
	state.Move(10)
	if state.Selected() != 2 {
		t.Fatalf("selected = %d, want the last row", state.Selected())
	}
	if state.Move(1) {
		t.Fatal("moving down from the last row changed the selection")
	}
}

// TestActivateOpensAndCloses proves Enter opens the highlighted row and the
// surface starts fresh next time.
func TestActivateOpensAndCloses(t *testing.T) {
	var opened []string
	state := newTestState(t, &opened)
	state.Open(ModeQuickSwitcher)
	state.Move(1)
	if !state.Activate() {
		t.Fatal("Activate reported the row unhandled")
	}
	if len(opened) != 1 || opened[0] != "h2" {
		t.Fatalf("opened = %v, want h2", opened)
	}
	if state.IsOpen() {
		t.Fatal("the surface stayed open")
	}
	if state.Query() != "" {
		t.Fatalf("query = %q after close, want empty", state.Query())
	}
}

// TestRefusedActivationStaysOpen lets a surface report an error in place.
func TestRefusedActivationStaysOpen(t *testing.T) {
	state := New(
		WithSource(records),
		WithOpen(func(Mode, appsearch.Result) bool { return false }),
	)
	state.Open(ModeQuickSwitcher)
	if state.Activate() {
		t.Fatal("Activate reported a refused row handled")
	}
	if !state.IsOpen() {
		t.Fatal("a refused activation closed the surface")
	}
}

// TestToggleOnlyClosesTheSameMode lets the Quick Switcher's chord toggle
// while the palette's chord switches mode in place.
func TestToggleOnlyClosesTheSameMode(t *testing.T) {
	state := newTestState(t, new([]string))
	state.Open(ModeQuickSwitcher)
	state.Toggle(ModeCommandPalette)
	if !state.IsOpen() || state.Mode() != ModeCommandPalette {
		t.Fatalf("mode = %v, open = %v", state.Mode(), state.IsOpen())
	}
	state.Toggle(ModeCommandPalette)
	if state.IsOpen() {
		t.Fatal("the same mode's toggle did not close")
	}
}

// TestCommandSectionsUseCategories groups the palette by command category.
func TestCommandSectionsUseCategories(t *testing.T) {
	state := newTestState(t, new([]string))
	state.Open(ModeCommandPalette)
	sections := state.Sections()
	if len(sections) != 2 {
		t.Fatalf("sections = %d, want 2", len(sections))
	}
	if sections[0].Key != CategoryTab || len(sections[0].Results) != 2 {
		t.Fatalf("first section = %q with %d results", sections[0].Key, len(sections[0].Results))
	}
	if sections[1].Key != CategoryApp {
		t.Fatalf("second section = %q, want %q", sections[1].Key, CategoryApp)
	}
}

// TestSelectionSurvivesRendering proves Clamp keeps a selection valid when
// the record set shrinks, as deleting a Host does.
func TestSelectionSurvivesRendering(t *testing.T) {
	state := newTestState(t, new([]string))
	state.Open(ModeQuickSwitcher)
	state.Move(2)
	state.Clamp(1)
	if state.Selected() != 0 {
		t.Fatalf("selected = %d, want 0", state.Selected())
	}
	state.Clamp(0)
	if state.Selected() != 0 {
		t.Fatalf("empty selection = %d, want 0", state.Selected())
	}
}

// TestRevealIsTakenOnce keeps the list from fighting a user's scroll.
func TestRevealIsTakenOnce(t *testing.T) {
	state := newTestState(t, new([]string))
	state.Open(ModeQuickSwitcher)
	if !state.TakeReveal() {
		t.Fatal("opening did not ask for a reveal")
	}
	if state.TakeReveal() {
		t.Fatal("the reveal was not consumed once")
	}
	state.Move(1)
	if !state.TakeReveal() {
		t.Fatal("moving did not ask for a reveal")
	}
}

func ids(results []appsearch.Result) []string {
	out := make([]string, len(results))
	for i, result := range results {
		out[i] = result.Record.ID
	}
	return out
}
