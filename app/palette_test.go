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
