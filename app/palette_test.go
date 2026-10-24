package app

import (
	"context"
	"testing"

	"github.com/mslxl/natcatty/application/command"
	appsearch "github.com/mslxl/natcatty/application/search"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	uisearch "github.com/mslxl/natcatty/ui/search"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

// paletteHosts is the minimal Vault seam the palette reads.
type paletteHosts struct{ items []domainvault.Host }

func (h *paletteHosts) List(context.Context) ([]domainvault.Host, error) {
	return append([]domainvault.Host(nil), h.items...), nil
}
func (h *paletteHosts) Create(context.Context, domainvault.Draft) (domainvault.Host, error) {
	return domainvault.Host{}, nil
}
func (h *paletteHosts) Update(context.Context, model.HostID, domainvault.Draft) (domainvault.Host, error) {
	return domainvault.Host{}, nil
}
func (h *paletteHosts) Delete(context.Context, model.HostID) error { return nil }

func titleOf(records []appsearch.Record, id command.ID) string {
	for _, record := range records {
		if record.ID == string(id) {
			return record.Title
		}
	}
	return ""
}

func paletteRegistry(t *testing.T, ids ...command.ID) *command.Registry {
	t.Helper()
	registry := command.NewRegistry()
	for _, id := range ids {
		if err := registry.Register(id, func(context.Context) error { return nil }); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}
	return registry
}

// TestPaletteExposesEveryPresentableCommand keeps the new listing API and the
// per-command labels in step: a registered command with a label appears, and
// the synthetic locale commands stay out.
func TestPaletteExposesEveryPresentableCommand(t *testing.T) {
	ids := []command.ID{
		command.Quit, command.LockApp, command.OpenSettings, command.NewTab,
		command.CloseTab, command.CloseOtherTabs, command.CloseTabsRight,
		command.DuplicateTab, command.DetachTab, command.CopyTabToNewWindow,
		command.SplitPaneRight, command.SplitPaneDown, command.ClosePane,
		command.DetachPane, command.FocusPaneLeft, command.FocusPaneRight,
		command.FocusPaneUp, command.FocusPaneDown, command.OpenBackups,
		command.LocaleCommandID("en"),
	}
	registry := paletteRegistry(t, ids...)
	source := &palette{registry: func() *command.Registry { return registry }}

	records := source.records(uisearch.ModeCommandPalette, "en")
	if len(records) != len(ids)-1 {
		t.Fatalf("records = %d, want %d (the locale command is synthetic)",
			len(records), len(ids)-1)
	}
	seen := make(map[string]bool, len(records))
	for _, record := range records {
		if record.Title == "" {
			t.Fatalf("%s has no title", record.ID)
		}
		if record.Group == "" {
			t.Fatalf("%s has no category", record.ID)
		}
		seen[record.ID] = true
	}
	if seen[string(command.LocaleCommandID("en"))] {
		t.Fatal("a locale command appeared in the palette")
	}
	for _, id := range ids[:len(ids)-1] {
		if !seen[string(id)] {
			t.Errorf("registered command %s is missing", id)
		}
	}
}

// TestPaletteLocalizesTitlesAndFindsByEnglishKeywords keeps a command
// reachable in every language, whichever words the user knows.
func TestPaletteLocalizesTitlesAndFindsByEnglishKeywords(t *testing.T) {
	registry := paletteRegistry(t, command.NewTab, command.OpenSettings)
	source := &palette{registry: func() *command.Registry { return registry }}

	english := source.records(uisearch.ModeCommandPalette, "en")
	chinese := source.records(uisearch.ModeCommandPalette, "zh-CN")
	if title := titleOf(english, command.NewTab); title != "New Tab" {
		t.Fatalf("English title = %q", title)
	}
	if title := titleOf(chinese, command.NewTab); title != "新建标签页" {
		t.Fatalf("Chinese title = %q", title)
	}
	results := appsearch.Rank("preferences", chinese)
	if len(results) != 1 || results[0].Record.ID != string(command.OpenSettings) {
		t.Fatalf("English keyword search = %v", results)
	}
}

// TestPaletteGroupsCommandsByCategory feeds the view one section per
// category, which it localizes.
func TestPaletteGroupsCommandsByCategory(t *testing.T) {
	// Registered out of order so a lexical ID order would put Panes before
	// Tabs; the palette's own order is Application, Tabs, Panes, Vault.
	registry := paletteRegistry(t, command.OpenBackups, command.SplitPaneRight,
		command.NewTab, command.OpenSettings)
	source := &palette{registry: func() *command.Registry { return registry }}
	records := source.records(uisearch.ModeCommandPalette, "en")
	want := []string{
		uisearch.CategoryApp, uisearch.CategoryTab,
		uisearch.CategoryPane, uisearch.CategoryVault,
	}
	if len(records) != len(want) {
		t.Fatalf("records = %d, want %d", len(records), len(want))
	}
	for index, category := range want {
		if records[index].Group != category {
			t.Fatalf("category %d = %q, want %q", index, records[index].Group, category)
		}
	}
}

// TestPaletteDispatchesTheChosenCommand proves Enter runs a command through
// the one registry the menus use.
func TestPaletteDispatchesTheChosenCommand(t *testing.T) {
	dispatched := ""
	registry := command.NewRegistry()
	if err := registry.Register(command.NewTab, func(context.Context) error {
		dispatched = string(command.NewTab)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	source := &palette{registry: func() *command.Registry { return registry }}
	handled := source.open(uisearch.ModeCommandPalette, appsearch.Result{
		Record: appsearch.Record{Kind: appsearch.KindCommand, ID: string(command.NewTab)},
	})
	if !handled || dispatched != string(command.NewTab) {
		t.Fatalf("handled = %v, dispatched = %q", handled, dispatched)
	}
}

func rankedIDs(results []appsearch.Result) []string {
	out := make([]string, len(results))
	for index, result := range results {
		out[index] = result.Record.ID
	}
	return out
}

func sameIDs(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// TestSwitcherRanksExactHostLabelFirst keeps the shared quality ranking
// sensible for navigation: typing a full Host label selects that Host before
// a Host whose label merely starts with it or contains it.
func TestSwitcherRanksExactHostLabelFirst(t *testing.T) {
	state := vaultstate.NewState(&paletteHosts{items: []domainvault.Host{
		{ID: "h3", Label: "my-prod-box", Hostname: "10.0.0.3"},
		{ID: "h1", Label: "prod", Hostname: "10.0.0.1"},
		{ID: "h2", Label: "prod-db", Hostname: "10.0.0.2"},
	}})
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	source := &palette{vault: state}
	records := source.records(uisearch.ModeQuickSwitcher, "en")
	want := []string{"h1", "h2", "h3"}
	for run := 0; run < 50; run++ {
		got := rankedIDs(appsearch.Rank("prod", records))
		if !sameIDs(got, want) {
			t.Fatalf("run %d = %v, want %v", run, got, want)
		}
	}
}

// categoryOrder returns the palette's fixed section index for a category.
func categoryOrder(category string) int {
	for index, candidate := range commandCategories {
		if candidate == category {
			return index
		}
	}
	return len(commandCategories)
}

// TestPaletteOrderingStaysDeterministic ranks real command records and keeps
// their category order stable across frames. The shared matcher now leads with
// match quality, so the palette's own grouping and tie-breaking must not drift.
func TestPaletteOrderingStaysDeterministic(t *testing.T) {
	registry := paletteRegistry(t,
		command.Quit, command.OpenSettings, command.NewTab, command.CloseTab,
		command.SplitPaneRight, command.OpenBackups)
	source := &palette{registry: func() *command.Registry { return registry }}
	records := source.records(uisearch.ModeCommandPalette, "en")

	for _, query := range []string{"tab", "settings", "split", "backups", "p"} {
		first := rankedIDs(appsearch.Rank(query, records))
		if len(first) == 0 {
			t.Fatalf("%q matched no palette command", query)
		}
		for run := 0; run < 50; run++ {
			if got := rankedIDs(appsearch.Rank(query, records)); !sameIDs(got, first) {
				t.Fatalf("%q run %d = %v, want %v", query, run, got, first)
			}
		}
	}

	state := uisearch.New(
		uisearch.WithSource(source.records),
		uisearch.WithSection(searchSection),
	)
	state.Open(uisearch.ModeCommandPalette)
	*state.QueryPtr() = "p"
	state.Sync()
	sections := state.Sections()
	if len(sections) == 0 {
		t.Fatal("the palette rendered no sections for p")
	}
	previous := -1
	for _, section := range sections {
		current := categoryOrder(section.Key)
		if current <= previous {
			t.Fatalf("section order = %v, want the palette's category order", section.Key)
		}
		previous = current
	}
}

// TestSwitcherListsHostsWithTheirSearchableFields keeps the Quick Switcher's
// Host records ranked by label, address, group, and tag.
func TestSwitcherListsHostsWithTheirSearchableFields(t *testing.T) {
	state := vaultstate.NewState(&paletteHosts{items: []domainvault.Host{
		{ID: "h1", Label: "web", Hostname: "10.0.0.1", Group: "prod", Tags: []string{"frontend"}},
	}})
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	source := &palette{vault: state}
	records := source.records(uisearch.ModeQuickSwitcher, "en")
	if len(records) != 1 || records[0].ID != "h1" {
		t.Fatalf("records = %v", records)
	}
	if records[0].Subtitle != "10.0.0.1" {
		t.Fatalf("subtitle = %q", records[0].Subtitle)
	}
	for _, query := range []string{"web", "10.0.0.1", "prod", "frontend"} {
		if results := appsearch.Rank(query, records); len(results) != 1 {
			t.Errorf("%q did not find the Host", query)
		}
	}
}
