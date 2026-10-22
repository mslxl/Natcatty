package keybindings

import (
	"reflect"
	"testing"
)

func TestCatalogMatchesTheOriginal42(t *testing.T) {
	catalog := Catalog()
	if len(catalog) != 42 {
		t.Fatalf("catalog = %d entries, want 42", len(catalog))
	}
	if got := catalog[0]; got.ID != "switch-tab-1-9" || got.Action != "switchToTab" ||
		got.Mac != "⌘ + [1...9]" || got.PC != "Ctrl + [1...9]" || got.Category != CategoryTabs {
		t.Errorf("first binding = %+v", got)
	}
	if got := catalog[41]; got.ID != "sftp-navigate-to" || got.Action != "sftpNavigateTo" ||
		got.Mac != "⌘ + Enter" || got.PC != "Ctrl + Enter" || got.Category != CategorySFTP {
		t.Errorf("last binding = %+v", got)
	}
	// The exact original ID order is part of the contract.
	wantIDs := []string{
		"switch-tab-1-9", "next-tab", "prev-tab", "close-tab", "close-session", "new-tab",
		"copy", "paste", "paste-selection", "select-all", "clear-buffer", "search-terminal",
		"increase-terminal-font-size", "decrease-terminal-font-size", "reset-terminal-font-size",
		"move-focus", "split-horizontal", "split-vertical", "toggle-pane-zoom",
		"open-hosts", "open-local", "open-sftp", "port-forwarding", "command-palette",
		"quick-switch", "new-workspace", "snippets", "broadcast", "toggle-side-panel",
		"open-settings", "toggle-opacity",
		"sftp-copy", "sftp-cut", "sftp-paste", "sftp-select-all", "sftp-rename",
		"sftp-delete", "sftp-refresh", "sftp-new-folder", "sftp-open", "sftp-go-parent",
		"sftp-navigate-to",
	}
	for i, id := range wantIDs {
		if catalog[i].ID != id {
			t.Fatalf("catalog[%d].ID = %q, want %q", i, catalog[i].ID, id)
		}
	}
	counts := map[Category]int{}
	for _, binding := range catalog {
		counts[binding.Category]++
	}
	want := map[Category]int{CategoryTabs: 6, CategoryTerminal: 9, CategoryNavigation: 4, CategoryApp: 12, CategorySFTP: 11}
	if !reflect.DeepEqual(counts, want) {
		t.Errorf("category counts = %v, want %v", counts, want)
	}
}

func TestCatalogShipsMacPCDisabledDefaults(t *testing.T) {
	byID := map[string]Binding{}
	for _, binding := range Catalog() {
		byID[binding.ID] = binding
	}
	if got := byID["open-hosts"]; got.Mac != Disabled || got.PC != Disabled {
		t.Errorf("open-hosts = %q/%q, want Disabled", got.Mac, got.PC)
	}
	if got := byID["open-local"]; got.Mac != "⌘ + L" || got.PC != Disabled {
		t.Errorf("open-local = %q/%q", got.Mac, got.PC)
	}
	if got := byID["sftp-delete"]; got.Mac != "⌘ + ⌫" || got.PC != "Delete" {
		t.Errorf("sftp-delete = %q/%q", got.Mac, got.PC)
	}
	if got := byID["move-focus"]; got.Mac != "⌘ + ⌥ + arrows" || got.PC != "Ctrl + Alt + arrows" {
		t.Errorf("move-focus = %q/%q", got.Mac, got.PC)
	}
}

func TestParseSchemeAndPlatformDefault(t *testing.T) {
	for _, scheme := range []Scheme{SchemeDisabled, SchemeMac, SchemePC} {
		got, ok := ParseScheme(string(scheme))
		if !ok || got != scheme {
			t.Errorf("ParseScheme(%q) = %q/%v", scheme, got, ok)
		}
	}
	if _, ok := ParseScheme("nonsense"); ok {
		t.Error("ParseScheme accepted nonsense")
	}
	if got := DefaultScheme("darwin"); got != SchemeMac {
		t.Errorf("DefaultScheme(darwin) = %q", got)
	}
	if got := DefaultScheme("windows"); got != SchemePC {
		t.Errorf("DefaultScheme(windows) = %q", got)
	}
}

func TestParseChordUnderstandsOriginalNotation(t *testing.T) {
	cases := map[string]Chord{
		"⌘ + Shift + K":       {Mods: ModSuper | ModShift, Key: "K"},
		"Ctrl + Alt + arrows": {Mods: ModCtrl | ModAlt, Arrows: true},
		"⌘ + [1...9]":         {Mods: ModSuper, Range: true},
		"⌘ + ⌫":               {Mods: ModSuper, Key: "Backspace"},
		"Enter":               {Key: "Enter"},
		"Ctrl + ,":            {Mods: ModCtrl, Key: ","},
		"F5":                  {Key: "F5"},
	}
	for input, want := range cases {
		got, ok := ParseChord(input)
		if !ok || got != want {
			t.Errorf("ParseChord(%q) = %+v/%v, want %+v", input, got, ok, want)
		}
	}
	if _, ok := ParseChord(Disabled); ok {
		t.Error("ParseChord accepted Disabled")
	}
	if _, ok := ParseChord(""); ok {
		t.Error("ParseChord accepted empty")
	}
}

func TestOverlapsTreatsRangesAndArrowsAsPatterns(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"Ctrl + [1...9]", "Ctrl + 1", true},
		{"Ctrl + [1...9]", "Ctrl + 0", false},
		{"Ctrl + [1...9]", "Ctrl + [1...9]", true},
		{"Ctrl + Alt + arrows", "Ctrl + Alt + Left", true},
		{"Ctrl + Alt + arrows", "Ctrl + Alt + Backspace", false},
		{"Ctrl + C", "Ctrl + Shift + C", false},
		{"Ctrl + C", "Ctrl + C", true},
	}
	for _, tc := range cases {
		a, _ := ParseChord(tc.a)
		b, _ := ParseChord(tc.b)
		if got := a.Overlaps(b); got != tc.want {
			t.Errorf("%q overlaps %q = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestOverridesResolveAndReset(t *testing.T) {
	catalog := Catalog()
	binding := catalog[0]
	overrides := Overrides{}.Set(binding.ID, SchemePC, "Ctrl + 9")
	if got := EffectiveChord(binding, SchemePC, overrides); got != "Ctrl + 9" {
		t.Errorf("effective = %q", got)
	}
	if got := EffectiveChord(binding, SchemeMac, overrides); got != binding.Mac {
		t.Errorf("other scheme effective = %q", got)
	}
	overrides = overrides.Set(binding.ID, SchemePC, Disabled)
	for _, entry := range ActiveBindings(catalog, SchemePC, overrides) {
		if entry.Binding.ID == binding.ID {
			t.Fatal("a disabled binding stayed active")
		}
	}
	overrides = overrides.Reset(binding.ID, SchemePC)
	if got := EffectiveChord(binding, SchemePC, overrides); got != binding.PC {
		t.Errorf("after reset effective = %q", got)
	}
}

func TestActiveBindingsSkipDisabledSchemeAndDisabledBindings(t *testing.T) {
	catalog := Catalog()
	if got := ActiveBindings(catalog, SchemeDisabled, nil); len(got) != 0 {
		t.Errorf("disabled scheme has %d active bindings", len(got))
	}
	active := ActiveBindings(catalog, SchemePC, nil)
	// open-hosts is Disabled on PC; open-local is Disabled on PC as well.
	seen := map[string]bool{}
	for _, entry := range active {
		seen[entry.Binding.ID] = true
	}
	if seen["open-hosts"] || seen["open-local"] {
		t.Error("disabled bindings were active")
	}
	if len(active) != 40 {
		t.Errorf("PC active bindings = %d, want 40", len(active))
	}
}

func TestFindConflictsReportsDuplicateChordsAndMenuAccelerators(t *testing.T) {
	catalog := Catalog()
	overrides := Overrides{}.
		Set("open-local", SchemePC, "Ctrl + T").   // new-tab already owns Ctrl + T
		Set("new-workspace", SchemePC, "Ctrl + Q") // the Quit menu owns Ctrl+Q
	active := ActiveBindings(catalog, SchemePC, overrides)
	menus := []MenuShortcut{{ID: "menu.quit", Label: "Quit", Accelerator: "CmdOrCtrl+Q"}}
	conflicts := FindConflicts(active, menus, "windows")
	if len(conflicts) != 2 {
		t.Fatalf("conflicts = %+v, want 2", conflicts)
	}
	foundTabs, foundQuit := false, false
	for _, conflict := range conflicts {
		ids := conflict.BindingIDs()
		if reflect.DeepEqual(ids, []string{"new-tab", "open-local"}) && !conflict.HasMenu() {
			foundTabs = true
		}
		if reflect.DeepEqual(ids, []string{"new-workspace"}) && conflict.HasMenu() {
			foundQuit = true
		}
	}
	if !foundTabs || !foundQuit {
		t.Errorf("conflicts = %+v", conflicts)
	}
}

// TestFindConflictsReportsAGlobalBindingOnATerminalChord covers precedence:
// a global binding that takes a terminal surface's chord would never fire
// while the terminal is focused, so the user must resolve it explicitly.
func TestFindConflictsReportsAGlobalBindingOnATerminalChord(t *testing.T) {
	overrides := Overrides{}.Set("broadcast", SchemePC, "Ctrl + Shift + C") // terminal copy
	active := ActiveBindings(Catalog(), SchemePC, overrides)
	conflicts := FindConflicts(active, nil, "windows")
	if len(conflicts) != 1 {
		t.Fatalf("conflicts = %+v, want one", conflicts)
	}
	ids := conflicts[0].BindingIDs()
	if !reflect.DeepEqual(ids, []string{"copy", "broadcast"}) {
		t.Fatalf("conflicting bindings = %v", ids)
	}
}

func TestFindConflictsIgnoresDisabledBindings(t *testing.T) {
	catalog := Catalog()
	active := ActiveBindings(catalog, SchemePC, nil)
	if conflicts := FindConflicts(active, nil, "windows"); len(conflicts) != 0 {
		t.Fatalf("defaults conflict: %+v", conflicts)
	}
}
