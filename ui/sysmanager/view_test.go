package sysmanager

import (
	"fmt"
	"testing"

	"github.com/egoist/mygo/ui"

	appsysmanager "github.com/mslxl/natcatty/application/sysmanager"
	"github.com/mslxl/natcatty/ui/locales"
)

// TestOverviewRendersOneRowPerCore is the 8-core fixture: every reported core
// gets its own labelled row next to the aggregate.
func TestOverviewRendersOneRowPerCore(t *testing.T) {
	m := locales.In("en")
	data := appsysmanager.Overview{
		Hostname: "web-1", Cores: 8, CPUPercent: 50,
		CPUPerCore: []float64{10, 20, 30, 40, 50, 60, 70, 80},
	}
	tester := ui.NewTester(func(c *ui.Context) {
		overview(c, data, m, c.Theme())
	}, 700, 700)
	tester.SetLocale("en")
	tester.Frame()

	if !tester.HasText(m.SysManagerOverviewCPU()) || !tester.HasText("50%") {
		t.Errorf("the aggregate CPU row is missing; texts %q", tester.Texts())
	}
	for index := 1; index <= 8; index++ {
		label := fmt.Sprintf("C%d", index)
		if !tester.HasText(label) {
			t.Errorf("core row %q is missing; texts %q", label, tester.Texts())
		}
		value := fmt.Sprintf("%d%%", index*10)
		if !tester.HasText(value) {
			t.Errorf("core row %q lacks its utilization %q; texts %q", label, value, tester.Texts())
		}
	}
	if tester.HasText("C9") {
		t.Errorf("a ninth core row was invented; texts %q", tester.Texts())
	}
}

// TestOverviewWithoutPerCoreCountersKeepsTheAggregate is the fixture with a
// core count but no per-core readings: the aggregate is all there is.
func TestOverviewWithoutPerCoreCountersKeepsTheAggregate(t *testing.T) {
	m := locales.In("en")
	data := appsysmanager.Overview{Hostname: "web-1", Cores: 4, CPUPercent: 25}
	tester := ui.NewTester(func(c *ui.Context) {
		overview(c, data, m, c.Theme())
	}, 700, 700)
	tester.SetLocale("en")
	tester.Frame()

	if !tester.HasText(m.SysManagerOverviewCPU()) || !tester.HasText("25%") {
		t.Errorf("the aggregate CPU row is missing; texts %q", tester.Texts())
	}
	if tester.HasText("C1") {
		t.Errorf("per-core rows appeared without per-core readings; texts %q", tester.Texts())
	}
}
