package preferences

import "testing"

func TestSysManagerIntervalsDefaultAndNormalize(t *testing.T) {
	defaults := DefaultSysManager()
	if defaults.OverviewInterval != 3 || defaults.ProcessesInterval != 3 ||
		defaults.PortsInterval != 3 || defaults.ServicesInterval != 5 {
		t.Fatalf("defaults = %+v", defaults)
	}

	broken := SysManager{
		OverviewInterval:  0,
		ProcessesInterval: 7,
		PortsInterval:     -1,
		ServicesInterval:  10,
	}
	got := NormalizeSysManager(broken)
	if got.OverviewInterval != 3 || got.ProcessesInterval != 3 ||
		got.PortsInterval != 3 || got.ServicesInterval != 10 {
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
	if got := updated.WithInterval(SysManagerTab("unknown"), 30); got != updated {
		t.Errorf("an unknown tab changed the configuration: %+v", got)
	}
}
