package preferences

import (
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
