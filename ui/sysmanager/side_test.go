package sysmanager

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/locales"
)

func TestShowHostSamplesTheSessionsHostAndHideStops(t *testing.T) {
	service := newFakeService()
	service.snapshot = demoSnapshot()
	state := newTestState(service, newFakePreferences())

	state.ShowHost("beta")
	start, ok := service.lastStart()
	if !ok || start.host.ID != "beta" || state.HostID() != "beta" {
		t.Fatalf("the side panel samples %+v (%v), Host %q, want beta", start, ok, state.HostID())
	}
	// The Host is captured when the tool opens. Every frame may ask again, but
	// a later focus change must not retarget the side surface.
	starts := len(service.starts)
	state.ShowHost("beta")
	if len(service.starts) != starts {
		t.Fatalf("the same Host restarted sampling: %d starts, want %d", len(service.starts), starts)
	}
	state.ShowHost("alpha")
	if start, _ := service.lastStart(); start.host.ID != "beta" {
		t.Fatalf("an open side panel followed another Host: %+v", start)
	}

	stops := service.stops
	state.Hide()
	if service.stops <= stops {
		t.Fatal("hiding the System tool did not stop sampling")
	}
	starts = len(service.starts)
	state.ShowHost("alpha")
	if start, _ := service.lastStart(); start.host.ID != "alpha" || len(service.starts) == starts {
		t.Fatalf("showing the tool again did not restart sampling: %+v", start)
	}
}

func TestShowHostNeverFallsBackToAnotherHost(t *testing.T) {
	service := newFakeService()
	state := newTestState(service, newFakePreferences())
	state.ShowHost("missing")
	if state.HostID() != "" {
		t.Fatalf("a Host missing from the Vault showed %q", state.HostID())
	}
	if len(service.starts) != 0 {
		t.Fatalf("the side tool sampled another Host: %+v", service.starts)
	}
	tt := ui.NewTester(SideView(state), 360, 600)
	tt.SetLocale("en")
	tt.Frame()
	if !tt.HasText(locales.In("en").SidePanelSystemNoHost()) {
		t.Fatalf("no explanation for a missing Host, texts %q", tt.Texts())
	}
}

func TestSideViewShowsTheTabsWithoutAHostPicker(t *testing.T) {
	m := locales.In("en")
	service := newFakeService()
	service.snapshot = demoSnapshot()
	state := newTestState(service, newFakePreferences())
	state.ShowHost("alpha")
	tt := ui.NewTester(SideView(state), 360, 600)
	tt.SetLocale("en")
	tt.Frame()
	for _, want := range []string{m.SysManagerTabOverview(), m.SysManagerTabProcesses(), m.SysManagerRefreshNow()} {
		if !tt.HasText(want) {
			t.Errorf("the System tool lacks %q, texts %q", want, tt.Texts())
		}
	}
	if tt.HasText(m.SysManagerTitle()) || tt.HasText("Beta") {
		t.Errorf("the System tool shows the page's title or Host picker, texts %q", tt.Texts())
	}
}

func TestSideViewTablesKeepTheColumnsThePanelHolds(t *testing.T) {
	m := locales.In("en")
	service := newFakeService()
	service.snapshot = demoSnapshot()
	state := newTestState(service, newFakePreferences())
	state.ShowHost("alpha")
	tt := ui.NewTester(SideView(state), 280, 600)
	tt.SetLocale("en")
	tt.Frame()
	if err := tt.Click(m.SysManagerTabProcesses()); err != nil {
		t.Fatalf("click Processes: %v", err)
	}
	tt.Frame()
	for _, want := range []string{m.SysManagerPid(), m.SysManagerCPU(), m.SysManagerCommand()} {
		if !tt.HasText(want) {
			t.Errorf("the narrow Processes table lacks %q, texts %q", want, tt.Texts())
		}
	}
	if tt.HasText(m.SysManagerUser()) {
		t.Errorf("the narrow Processes table kept the User column, texts %q", tt.Texts())
	}
}
