package catalog

import (
	"reflect"
	"testing"
)

func TestProgressiveSplitAssignsEveryToolExactlyOnce(t *testing.T) {
	seen := map[string]bool{}
	for _, name := range CoreNames {
		if seen[name] {
			t.Fatalf("core tool %q is listed twice", name)
		}
		seen[name] = true
	}
	for _, toolset := range ToolsetNames {
		for _, name := range Toolsets[toolset] {
			if seen[name] {
				t.Fatalf("tool %q is assigned twice", name)
			}
			seen[name] = true
		}
	}
	for _, tool := range All() {
		if !seen[tool.Name] {
			t.Fatalf("catalog tool %q is not assigned to core or a toolset", tool.Name)
		}
	}
	if len(seen) != len(All()) {
		t.Fatalf("assigned tools = %d, catalog tools = %d", len(seen), len(All()))
	}
}

func TestProgressiveInitialVisibilityIsCorePlusLoader(t *testing.T) {
	view := NewVisibility()
	visible := view.Visible()
	if len(visible) != 14 {
		t.Fatalf("visible catalog tools = %d, want 14", len(visible))
	}
	if got := view.VisibleCount(); got != 15 {
		t.Fatalf("visible tools including loader = %d, want 15", got)
	}
	if got := view.LoadedCatalogCount(); got != 14 {
		t.Fatalf("loaded catalog tools = %d, want 14", got)
	}
	for index, name := range CoreNames {
		if visible[index].Name != name {
			t.Fatalf("visible[%d] = %q, want %q", index, visible[index].Name, name)
		}
	}
	if !view.IsVisible("get_environment") || view.IsVisible("sftp_stat") {
		t.Fatalf("initial visibility is wrong")
	}
}

func TestProgressiveLoadingAToolsetAndReloading(t *testing.T) {
	view := NewVisibility()
	newly, already, err := view.Load("sftp_advanced")
	if err != nil {
		t.Fatalf("load sftp_advanced: %v", err)
	}
	if !reflect.DeepEqual(newly, Toolsets["sftp_advanced"]) {
		t.Fatalf("newly loaded = %v, want %v", newly, Toolsets["sftp_advanced"])
	}
	if len(already) != 0 {
		t.Fatalf("already loaded = %v, want none", already)
	}
	if view.VisibleCount() != 21 {
		t.Fatalf("visible tools = %d, want 21", view.VisibleCount())
	}
	for _, name := range Toolsets["sftp_advanced"] {
		if !view.IsVisible(name) {
			t.Fatalf("%q is not visible after loading sftp_advanced", name)
		}
	}

	// A second load reports the requested tools as already loaded and does not
	// duplicate them.
	again, already, err := view.Load("sftp_advanced")
	if err != nil {
		t.Fatalf("reload sftp_advanced: %v", err)
	}
	if len(again) != 0 || !reflect.DeepEqual(already, Toolsets["sftp_advanced"]) {
		t.Fatalf("second load newly=%v already=%v", again, already)
	}
	if view.VisibleCount() != 21 {
		t.Fatalf("visible tools after reload = %d, want 21", view.VisibleCount())
	}
}

func TestProgressiveLoadingAllExposesSeventy(t *testing.T) {
	view := NewVisibility()
	newly, _, err := view.Load("all")
	if err != nil {
		t.Fatalf("load all: %v", err)
	}
	if len(newly) != 55 {
		t.Fatalf("newly loaded = %d, want 55", len(newly))
	}
	if view.VisibleCount() != 70 {
		t.Fatalf("visible tools = %d, want 70", view.VisibleCount())
	}
	if view.RemainingCatalogCount() != 0 {
		t.Fatalf("remaining = %d, want 0", view.RemainingCatalogCount())
	}
	seen := map[string]bool{}
	for _, tool := range view.Visible() {
		if seen[tool.Name] {
			t.Fatalf("tool %q is visible twice", tool.Name)
		}
		seen[tool.Name] = true
	}
	for _, tool := range All() {
		if !seen[tool.Name] {
			t.Fatalf("tool %q is not visible after loading all", tool.Name)
		}
	}
}

func TestProgressiveRejectsUnknownToolset(t *testing.T) {
	view := NewVisibility()
	if _, _, err := view.Load("everything"); err == nil {
		t.Fatal("loading an unknown toolset succeeded")
	}
	if view.VisibleCount() != 15 {
		t.Fatalf("visible tools changed after a rejected load: %d", view.VisibleCount())
	}
}
