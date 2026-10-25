package preferences

import (
	"context"
	"reflect"
	"testing"

	domain "github.com/mslxl/natcatty/domain/preferences"
)

func TestMonitorConfigurationPersistsThroughTheStore(t *testing.T) {
	store := &fakeStore{}
	service := NewService(store)
	if err := service.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := service.Update(func(settings *domain.Settings) {
		settings.Monitor.Panels = append(settings.Monitor.Panels, domain.MonitorPanel{
			ID:       "panel-1",
			Name:     "Production",
			HostIDs:  []string{"h1", "h2"},
			View:     domain.MonitorViewCharts,
			Interval: 10,
			Range:    15,
			Sort:     domain.MonitorSortName,
		})
		settings.Monitor.HiddenColumns = []string{"cpu", "memory"}
		settings.Monitor.ColumnWidths["host"] = 300
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if store.saves != 1 {
		t.Fatalf("saves = %d, want 1", store.saves)
	}

	reloaded := NewService(store)
	if err := reloaded.Load(context.Background()); err != nil {
		t.Fatalf("reload: %v", err)
	}
	monitor := reloaded.Settings().Monitor
	if len(monitor.Panels) != 1 {
		t.Fatalf("panels = %+v", monitor.Panels)
	}
	panel := monitor.Panels[0]
	if panel.Name != "Production" || panel.View != domain.MonitorViewCharts ||
		panel.Interval != 10 || panel.Range != 15 || panel.Sort != domain.MonitorSortName {
		t.Errorf("panel = %+v", panel)
	}
	if !reflect.DeepEqual(panel.HostIDs, []string{"h1", "h2"}) {
		t.Errorf("host ids = %v", panel.HostIDs)
	}
	if !reflect.DeepEqual(monitor.HiddenColumns, []string{"cpu", "memory"}) {
		t.Errorf("hidden columns = %v", monitor.HiddenColumns)
	}
	if monitor.ColumnWidths["host"] != 300 {
		t.Errorf("host width = %d", monitor.ColumnWidths["host"])
	}
	if err := reloaded.ResetGroup(domain.GroupMonitor); err != nil {
		t.Fatalf("ResetGroup: %v", err)
	}
	if !reflect.DeepEqual(reloaded.Settings().Monitor, domain.DefaultMonitor()) {
		t.Errorf("monitor reset = %+v", reloaded.Settings().Monitor)
	}
}
