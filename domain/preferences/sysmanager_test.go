package preferences

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestSysManagerIntervalsDefaultAndNormalize(t *testing.T) {
	defaults := DefaultSysManager()
	if defaults.OverviewInterval != 3 || defaults.ProcessesInterval != 3 ||
		defaults.PortsInterval != 3 || defaults.ServicesInterval != 5 ||
		defaults.TmuxInterval != 3 || defaults.DockerInterval != 5 ||
		defaults.GPUInterval != 3 {
		t.Fatalf("defaults = %+v", defaults)
	}

	broken := SysManager{
		OverviewInterval:  0,
		ProcessesInterval: 7,
		PortsInterval:     -1,
		ServicesInterval:  10,
		TmuxInterval:      30,
		DockerInterval:    2,
		GPUInterval:       11,
	}
	got := NormalizeSysManager(broken)
	if got.OverviewInterval != 3 || got.ProcessesInterval != 3 ||
		got.PortsInterval != 3 || got.ServicesInterval != 10 ||
		got.TmuxInterval != 30 || got.DockerInterval != 2 ||
		got.GPUInterval != 3 {
		t.Errorf("normalized = %+v", got)
	}
}

func TestSysManagerIntervalReadsAndWritesOnlyItsTab(t *testing.T) {
	manager := DefaultSysManager()
	updated := manager.WithInterval(SysManagerPorts, 10)
	if updated.PortsInterval != 10 {
		t.Errorf("ports interval = %d", updated.PortsInterval)
	}
	if updated.OverviewInterval != manager.OverviewInterval ||
		updated.ProcessesInterval != manager.ProcessesInterval ||
		updated.ServicesInterval != manager.ServicesInterval {
		t.Errorf("other intervals changed: %+v", updated)
	}
	if got := updated.Interval(SysManagerPorts); got != 10 {
		t.Errorf("Interval(ports) = %d", got)
	}
	if got := updated.WithInterval(SysManagerTab("unknown"), 30); !reflect.DeepEqual(got, updated) {
		t.Errorf("an unknown tab changed the configuration: %+v", got)
	}
}

func TestSysManagerTabOrderAndVisibilityNormalize(t *testing.T) {
	defaults := DefaultSysManager()
	if got := defaults.Order(); !reflect.DeepEqual(got, SysManagerTabs) {
		t.Fatalf("default order = %v", got)
	}
	if got := defaults.VisibleTabs(); !reflect.DeepEqual(got, SysManagerTabs) {
		t.Fatalf("default visible tabs = %v", got)
	}

	broken := SysManager{
		TabOrder:   []string{"gpu", "unknown", "gpu", "processes"},
		HiddenTabs: []string{"overview", "nope", "gpu", "gpu"},
	}
	got := NormalizeSysManager(broken)
	want := []SysManagerTab{
		SysManagerGPU, SysManagerProcesses, SysManagerOverview, SysManagerPorts,
		SysManagerServices, SysManagerTmux, SysManagerDocker,
	}
	if !reflect.DeepEqual(got.Order(), want) {
		t.Errorf("normalized order = %v, want %v", got.Order(), want)
	}
	if !reflect.DeepEqual(got.HiddenTabs, []string{"gpu"}) {
		t.Errorf("normalized hidden = %v", got.HiddenTabs)
	}

	visible := defaults.VisibleTabs()
	if len(visible) != len(SysManagerTabs) {
		t.Fatalf("visible = %v", visible)
	}
}

func TestSysManagerCollapsedTabsNormalizeAndAreExclusiveWithHidden(t *testing.T) {
	defaults := DefaultSysManager()
	if defaults.CollapsedTabs == nil || len(defaults.CollapsedTabs) != 0 {
		t.Fatalf("default collapsed tabs = %#v, want an empty list", defaults.CollapsedTabs)
	}
	if got := defaults.Collapsed(); len(got) != 0 {
		t.Fatalf("default Collapsed() = %v, want none", got)
	}

	broken := SysManager{
		CollapsedTabs: []string{"gpu", "unknown", "gpu", "overview", "docker", "services"},
		HiddenTabs:    []string{"docker"},
	}
	got := NormalizeSysManager(broken)
	want := []string{"services", "gpu"}
	if !reflect.DeepEqual(got.CollapsedTabs, want) {
		t.Fatalf("normalized collapsed = %v, want %v", got.CollapsedTabs, want)
	}
	if got.Visible(SysManagerServices) || got.Visible(SysManagerGPU) ||
		got.Visible(SysManagerDocker) || !got.Visible(SysManagerOverview) {
		t.Fatalf("visible tabs after normalization are wrong: %v", got.VisibleTabs())
	}

	manager := DefaultSysManager().WithTabCollapsed(SysManagerDocker, true)
	if !manager.IsCollapsed(SysManagerDocker) || manager.Visible(SysManagerDocker) {
		t.Fatalf("collapsing Docker did not remove it from the strip: %+v", manager)
	}
	if got := manager.Collapsed(); !reflect.DeepEqual(got, []SysManagerTab{SysManagerDocker}) {
		t.Fatalf("Collapsed() = %v", got)
	}
	// Showing a collapsed tab returns it to the strip.
	if shown := manager.WithTabHidden(SysManagerDocker, false); !shown.Visible(SysManagerDocker) ||
		shown.IsCollapsed(SysManagerDocker) {
		t.Fatalf("show did not restore the collapsed tab: %+v", shown)
	}
	// Hiding a collapsed tab takes it out of the menu and the strip.
	hidden := manager.WithTabHidden(SysManagerDocker, true)
	if hidden.Visible(SysManagerDocker) || hidden.IsCollapsed(SysManagerDocker) ||
		!reflect.DeepEqual(hidden.CollapsedTabs, []string{}) {
		t.Fatalf("hiding a collapsed tab left it reachable: %+v", hidden)
	}
	// Collapsing a hidden tab shows it in the menu.
	recollapsed := hidden.WithTabCollapsed(SysManagerDocker, true)
	if recollapsed.Visible(SysManagerDocker) || !recollapsed.IsCollapsed(SysManagerDocker) ||
		!reflect.DeepEqual(recollapsed.HiddenTabs, []string{}) {
		t.Fatalf("collapsing a hidden tab did not move it to the menu: %+v", recollapsed)
	}
	// The overview is locked visible.
	if locked := manager.WithTabCollapsed(SysManagerOverview, true); !locked.Visible(SysManagerOverview) ||
		locked.IsCollapsed(SysManagerOverview) {
		t.Fatalf("the overview could be collapsed: %+v", locked)
	}
	if locked := manager.WithTabHidden(SysManagerOverview, true); !locked.Visible(SysManagerOverview) ||
		locked.IsCollapsed(SysManagerOverview) || len(locked.HiddenTabs) != 0 {
		t.Fatalf("the overview could be hidden: %+v", locked)
	}
}

func TestSysManagerCollapsedTabsPersistAndResetKeepsIntervals(t *testing.T) {
	manager := DefaultSysManager().
		WithInterval(SysManagerDocker, 10).
		WithTabOrder([]SysManagerTab{
			SysManagerOverview, SysManagerProcesses, SysManagerPorts, SysManagerGPU,
			SysManagerServices, SysManagerTmux, SysManagerDocker,
		}).
		WithTabCollapsed(SysManagerDocker, true).
		WithTabCollapsed(SysManagerGPU, true).
		WithTabHidden(SysManagerTmux, true)

	data, err := json.Marshal(manager)
	if err != nil {
		t.Fatal(err)
	}
	var stored SysManager
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(NormalizeSysManager(stored), manager) {
		t.Fatalf("stored layout changed:\n got %+v\nwant %+v", NormalizeSysManager(stored), manager)
	}
	if !reflect.DeepEqual(manager.Collapsed(), []SysManagerTab{SysManagerDocker, SysManagerGPU}) {
		t.Fatalf("collapsed after reload = %v", manager.Collapsed())
	}

	reset := manager.ResetTabLayout()
	if !reflect.DeepEqual(reset.Order(), SysManagerTabs) {
		t.Errorf("reset order = %v", reset.Order())
	}
	if len(reset.HiddenTabs) != 0 || len(reset.CollapsedTabs) != 0 {
		t.Errorf("reset left hidden %v or collapsed %v", reset.HiddenTabs, reset.CollapsedTabs)
	}
	if reset.Interval(SysManagerDocker) != 10 {
		t.Errorf("reset changed the Docker interval to %d", reset.Interval(SysManagerDocker))
	}
	if !reset.Visible(SysManagerGPU) || !reset.Visible(SysManagerTmux) {
		t.Errorf("reset did not restore every tab: %v", reset.VisibleTabs())
	}

	// An older layout without the field loads as no collapsed tabs.
	var old SysManager
	if err := json.Unmarshal([]byte(`{"overviewInterval":3,"tabOrder":["gpu","processes","overview"],"hiddenTabs":["tmux"]}`), &old); err != nil {
		t.Fatal(err)
	}
	old = NormalizeSysManager(old)
	if old.CollapsedTabs == nil || len(old.CollapsedTabs) != 0 {
		t.Errorf("a layout without collapsedTabs loaded as %#v", old.CollapsedTabs)
	}
	if old.TabOrder[0] != "gpu" || old.HiddenTabs[0] != "tmux" {
		t.Errorf("an existing order or hidden list was migrated: %+v", old)
	}

	// A stored order naming all seven tabs and an existing hidden list load
	// unchanged: only unknown, duplicate, and locked names are normalized.
	order := []string{"gpu", "docker", "tmux", "services", "ports", "processes", "overview"}
	hidden := order[:6]
	full := NormalizeSysManager(SysManager{TabOrder: order, HiddenTabs: hidden})
	if !reflect.DeepEqual(full.TabOrder, order) || !reflect.DeepEqual(full.HiddenTabs, hidden) {
		t.Errorf("an all-seven stored layout changed: order %v hidden %v",
			full.TabOrder, full.HiddenTabs)
	}
}

func TestSysManagerTabLayoutReadsAndWritesOnlyTheRequestedTab(t *testing.T) {
	manager := DefaultSysManager()
	moved := manager.MoveTab(SysManagerDocker, -1)
	want := []SysManagerTab{
		SysManagerOverview, SysManagerProcesses, SysManagerPorts, SysManagerServices,
		SysManagerDocker, SysManagerTmux, SysManagerGPU,
	}
	if !reflect.DeepEqual(moved.Order(), want) {
		t.Fatalf("moved order = %v", moved.Order())
	}
	if moved.WithInterval(SysManagerTmux, 10).Interval(SysManagerTmux) != 10 {
		t.Error("moving a tab disturbed its interval")
	}
	if moved.MoveTab(SysManagerOverview, -1).Order()[0] != SysManagerOverview {
		t.Error("a move past the front changed the order")
	}
	if moved.MoveTab(SysManagerGPU, 1).Order()[len(SysManagerTabs)-1] != SysManagerGPU {
		t.Error("a move past the end changed the order")
	}

	hidden := moved.WithTabHidden(SysManagerTmux, true)
	if hidden.Visible(SysManagerTmux) {
		t.Error("a hidden tab stayed visible")
	}
	if !hidden.Visible(SysManagerOverview) {
		t.Error("hiding one tab hid the overview")
	}
	if got := hidden.VisibleTabs(); len(got) != len(SysManagerTabs)-1 {
		t.Errorf("visible after hide = %v", got)
	}
	shown := hidden.WithTabHidden(SysManagerTmux, false)
	if !reflect.DeepEqual(shown, moved) {
		t.Errorf("show did not restore the layout: %+v", shown)
	}
	if shown.WithTabHidden(SysManagerOverview, true).Visible(SysManagerOverview) == false {
		t.Error("the overview could be hidden")
	}
}
