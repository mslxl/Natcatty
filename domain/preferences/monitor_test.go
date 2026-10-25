package preferences

import (
	"reflect"
	"testing"
)

func TestDefaultsCarryEveryMonitorColumnWidth(t *testing.T) {
	monitor := Defaults().Monitor
	if len(monitor.ColumnWidths) != len(MonitorColumnKeys) {
		t.Fatalf("column widths = %v, want one per column", monitor.ColumnWidths)
	}
	for _, key := range MonitorColumnKeys {
		if monitor.ColumnWidths[key] != MonitorColumnDefaults[key] {
			t.Errorf("width %q = %d, want %d", key, monitor.ColumnWidths[key], MonitorColumnDefaults[key])
		}
	}
	if monitor.Panels != nil || monitor.HiddenColumns != nil {
		t.Errorf("defaults = %+v, want no panels and no hidden columns", monitor)
	}
}

func TestNormalizeMonitorRepairsPanelsAndLayout(t *testing.T) {
	monitor := NormalizeMonitor(Monitor{
		Panels: []MonitorPanel{
			{ID: "a", Name: " Fleet ", HostIDs: []string{"h1", "h1", " h2 ", ""},
				View: "grid", Interval: 7, Range: 12, Sort: "cpu"},
			{ID: "a", Name: "duplicate id", Interval: 5},
			{ID: "b", Name: "Fleet", Interval: 1, Range: 5, View: MonitorViewCharts, Sort: MonitorSortName},
			{ID: "", Name: "no id"},
			{ID: "c", Name: "   "},
		},
		HiddenColumns: []string{"host", "terminal", "notes", "notes", "bogus"},
		ColumnWidths:  map[string]int{"host": 10, "cpu": 9999, "bogus": 100},
	})

	if len(monitor.Panels) != 2 {
		t.Fatalf("panels = %+v, want two", monitor.Panels)
	}
	first := monitor.Panels[0]
	if first.ID != "a" || first.Name != "Fleet" {
		t.Errorf("first panel = %+v", first)
	}
	if !reflect.DeepEqual(first.HostIDs, []string{"h1", "h2"}) {
		t.Errorf("host ids = %v", first.HostIDs)
	}
	if first.View != MonitorViewList || first.Interval != DefaultMonitorInterval ||
		first.Range != DefaultMonitorRange || first.Sort != MonitorSortManual {
		t.Errorf("first preferences = %+v, want safe defaults", first)
	}
	if monitor.Panels[1].Name != "Fleet (2)" {
		t.Errorf("duplicate name = %q, want Fleet (2)", monitor.Panels[1].Name)
	}
	if !reflect.DeepEqual(monitor.HiddenColumns, []string{"notes"}) {
		t.Errorf("hidden columns = %v", monitor.HiddenColumns)
	}
	if monitor.ColumnWidths["host"] != monitorColumnMinimum["host"] {
		t.Errorf("host width = %d, want the minimum", monitor.ColumnWidths["host"])
	}
	if monitor.ColumnWidths["cpu"] != 640 {
		t.Errorf("cpu width = %d, want the maximum", monitor.ColumnWidths["cpu"])
	}
	if _, ok := monitor.ColumnWidths["bogus"]; ok {
		t.Error("normalization kept an unknown column")
	}
}

func TestNormalizeMonitorKeepsValidPanelsAndIntervals(t *testing.T) {
	valid := MonitorPanel{ID: "p", Name: "Prod", HostIDs: []string{"h"},
		View: MonitorViewCharts, Interval: 30, Range: 15, Sort: MonitorSortName}
	monitor := NormalizeMonitor(Monitor{Panels: []MonitorPanel{valid}})
	if len(monitor.Panels) != 1 {
		t.Fatalf("panels = %+v", monitor.Panels)
	}
	if got := monitor.Panels[0]; !reflect.DeepEqual(got, valid) {
		t.Errorf("valid panel = %+v, want %+v", got, valid)
	}
}

func TestResetGroupMonitorRestoresTheLayout(t *testing.T) {
	settings := Defaults()
	settings.Monitor.Panels = []MonitorPanel{{ID: "p", Name: "Prod", Interval: 1}}
	settings.Monitor.HiddenColumns = []string{"notes"}
	settings.Monitor.ColumnWidths["host"] = 400

	reset := ResetGroup(settings, GroupMonitor)
	if !reflect.DeepEqual(reset.Monitor, DefaultMonitor()) {
		t.Errorf("reset monitor = %+v, want defaults", reset.Monitor)
	}
}
