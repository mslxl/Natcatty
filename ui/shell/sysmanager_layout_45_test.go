package shell

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	appsysmanager "github.com/mslxl/natcatty/application/sysmanager"
	"github.com/mslxl/natcatty/domain/model"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/shell/state"
	uisysmanager "github.com/mslxl/natcatty/ui/sysmanager"
)

// The app enforces a 720x480 minimum window. A real window of that size has a
// 704x421 client area once the border and the title bar take their space, and
// the 232-DIP sidebar takes another 232 from the width. A standalone 720x480
// surface is 59 DIPs taller, which is why the ticket's own frames at 480 DIPs
// showed all four service rows and hid the clipped fourth row that independent
// acceptance measured in the real window.
const (
	sysManagerMinimumWidth  = 704
	sysManagerMinimumHeight = 421
)

// sysManagerActionBarHeight mirrors the pinned action row's height in
// ui/sysmanager (actionBarHeight). The test needs the bar's top edge, not the
// top of the label inside it, so that a row ending anywhere in the bar's box
// counts as covered. The bar centers its content, so its top edge is half a
// bar above the label's center. A change to the surface's bar height must
// change this constant with it; the row measurements below then still hold.
const sysManagerActionBarHeight float32 = 32

type sysManagerLayoutHosts struct{ items []domainvault.Host }

func (h sysManagerLayoutHosts) List(context.Context) ([]domainvault.Host, error) {
	return append([]domainvault.Host(nil), h.items...), nil
}

type sysManagerLayoutPrefs struct {
	mu       sync.Mutex
	settings domainpreferences.Settings
}

func (p *sysManagerLayoutPrefs) Settings() domainpreferences.Settings {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.settings
}

func (p *sysManagerLayoutPrefs) Update(change func(*domainpreferences.Settings)) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	change(&p.settings)
	p.settings.SysManager = domainpreferences.NormalizeSysManager(p.settings.SysManager)
	return nil
}

// sysManagerLayoutService serves one fixed snapshot so the test measures
// layout, not sampling.
type sysManagerLayoutService struct {
	mu       sync.Mutex
	snapshot appsysmanager.Snapshot
}

func (s *sysManagerLayoutService) Start(host domainvault.Host, tab appsysmanager.Tab,
	_ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshot.HostID = string(host.ID)
	s.snapshot.Active = tab
	return nil
}

func (s *sysManagerLayoutService) Stop()    {}
func (s *sysManagerLayoutService) Refresh() {}

func (s *sysManagerLayoutService) Snapshot() appsysmanager.Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.snapshot
	out.Tabs = make(map[appsysmanager.Tab]appsysmanager.TabState, len(s.snapshot.Tabs))
	for tab, state := range s.snapshot.Tabs {
		out.Tabs[tab] = state
	}
	return out
}

func (s *sysManagerLayoutService) Subscribe(func()) func() { return func() {} }

func (s *sysManagerLayoutService) Signal(context.Context, domainvault.Host, int,
	string) (appsysmanager.ActionResult, error) {
	return appsysmanager.ActionResult{}, nil
}

func (s *sysManagerLayoutService) ServiceAction(context.Context, domainvault.Host,
	string, appsysmanager.ServiceAction,
	appsysmanager.Scope) (appsysmanager.ActionResult, error) {
	return appsysmanager.ActionResult{}, nil
}

// sysManagerLayoutUnit names row i of the service fixture. The names mirror
// the independent acceptance fixture: a failed system unit first and a
// user-scoped unit last.
func sysManagerLayoutUnit(index, rows int) (name, scope, active string) {
	name, scope, active = fmt.Sprintf("unit-%d.service", index+1), "system", "active"
	switch index {
	case 0:
		name, active = "brokend.service", "failed"
	case 1:
		name = "db.service"
	case 2:
		name = "nginx.service"
	default:
		if rows == 4 {
			name, scope = "user.service", "user"
		}
	}
	return name, scope, active
}

// sysManagerLayoutNow is the clock both the fixture and the surface read, so
// the updated-at and latency status texts are deterministic: one hour old and
// 12 ms, long enough to exercise the header's right edge.
var sysManagerLayoutNow = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// sysManagerLayoutPort is the port of row index of total: a plain run from
// 8000, and 8080 for the bottom row, the four-digit port independent
// acceptance measured as "8,080" in the selection bar.
func sysManagerLayoutPort(index, total int) int {
	if index == total-1 {
		return 8080
	}
	return 8000 + index
}

// sysManagerLayoutFixture builds a snapshot with rows processes, listeners,
// and units. The process list sorts by CPU descending, so the bottom row of
// every tab is the one the tests expect.
func sysManagerLayoutFixture(rows int) appsysmanager.Snapshot {
	now := sysManagerLayoutNow
	processes := make([]appsysmanager.Process, 0, rows)
	ports := make([]appsysmanager.Port, 0, rows)
	services := make([]appsysmanager.Unit, 0, rows)
	for i := 0; i < rows; i++ {
		processes = append(processes, appsysmanager.Process{
			PID: 100 + i, User: "app", CPUPercent: float64(i), MemPercent: 1,
			Command: fmt.Sprintf("worker-%d", i),
		})
		ports = append(ports, appsysmanager.Port{
			Protocol: "tcp", Address: "0.0.0.0", Port: sysManagerLayoutPort(i, rows),
			PID: 200 + i, ProcessName: fmt.Sprintf("svc-%d", i),
		})
		name, scope, active := sysManagerLayoutUnit(i, rows)
		services = append(services, appsysmanager.Unit{
			Name: name, Scope: scope, ActiveState: active, Description: "Unit " + name,
		})
	}
	return appsysmanager.Snapshot{
		HostID: "h1", Active: appsysmanager.TabOverview,
		Tabs: map[appsysmanager.Tab]appsysmanager.TabState{
			appsysmanager.TabOverview: {
				Tab: appsysmanager.TabOverview, Status: appsysmanager.StatusLive,
				At: now.Add(-time.Hour), Latency: 12 * time.Millisecond,
				Overview: appsysmanager.Overview{
					Hostname: "host-1", OSName: "Debian 12", KernelRelease: "6.1.0",
					UptimeSeconds: 90061, LoadAverage: []float64{0.5, 0.4, 0.3},
					Cores: 8, CPUPercent: 20, MemUsedMB: 3072, MemTotalMB: 8192,
					SwapUsedMB: 128, SwapTotalMB: 1024,
					DiskUsedGB: 25, DiskTotalGB: 100,
				},
			},
			appsysmanager.TabProcesses: {
				Tab: appsysmanager.TabProcesses, Status: appsysmanager.StatusLive,
				At: now.Add(-time.Hour), Latency: 12 * time.Millisecond, Processes: processes,
			},
			appsysmanager.TabPorts: {
				Tab: appsysmanager.TabPorts, Status: appsysmanager.StatusLive,
				At: now.Add(-time.Hour), Latency: 12 * time.Millisecond, Ports: ports,
			},
			appsysmanager.TabServices: {
				Tab: appsysmanager.TabServices, Status: appsysmanager.StatusLive,
				At: now.Add(-time.Hour), Latency: 12 * time.Millisecond, Services: services,
			},
		},
	}
}

func newSysManagerLayoutTester(t *testing.T, width, height int, locale string,
	tab appsysmanager.Tab, rows int) (*ui.Tester, *uisysmanager.State) {
	t.Helper()
	hosts := []domainvault.Host{
		{ID: model.HostID("h1"), Label: "Alpha", Hostname: "host-1"},
	}
	service := &sysManagerLayoutService{snapshot: sysManagerLayoutFixture(rows)}
	prefs := &sysManagerLayoutPrefs{settings: domainpreferences.Defaults()}
	manager := uisysmanager.NewState(service, sysManagerLayoutHosts{items: hosts}, prefs,
		uisysmanager.WithClock(func() time.Time { return sysManagerLayoutNow }))
	shellState := state.NewState(state.WithSystemManager(
		uisysmanager.View(manager), manager.Activate, manager.Deactivate))
	shellState.ShowSystemManager()
	t.Cleanup(func() {
		manager.Close()
		shellState.Shutdown()
	})
	tester := ui.NewTester(View(shellState), width, height)
	tester.SetLocale(locale)
	tester.Frame()
	if tab != appsysmanager.TabOverview {
		manager.SetTab(tab)
		tester.Frame()
	}
	return tester, manager
}

// TestSysManagerListRowsClearThePinnedActionBarAtMinimumWindow renders every
// list tab inside the real shell at the client area of the enforced 720x480
// window. It fails against the layout independent acceptance measured: the
// Services fourth row was laid out at 431..463 in window coordinates while the
// pinned action bar started at 452, so the row was clipped, its center was not
// clickable, and only three of four units were visible.
func TestSysManagerListRowsClearThePinnedActionBarAtMinimumWindow(t *testing.T) {
	cases := []struct {
		tab  appsysmanager.Tab
		hint func(locales.Messages) string
	}{
		{appsysmanager.TabProcesses,
			func(m locales.Messages) string { return m.SysManagerProcessHint() }},
		{appsysmanager.TabPorts,
			func(m locales.Messages) string { return m.SysManagerPortHint() }},
		{appsysmanager.TabServices,
			func(m locales.Messages) string { return m.SysManagerServiceHint() }},
	}
	for _, locale := range []string{"en", "zh-CN"} {
		m := locales.In(locale)
		for _, test := range cases {
			tester, manager := newSysManagerLayoutTester(t, sysManagerMinimumWidth,
				sysManagerMinimumHeight, locale, test.tab, 4)
			name := fmt.Sprintf("%s %s", locale, test.tab)
			barTop := actionBarTop(t, tester, test.hint(m), name)
			rows := listRows(t, tester, test.tab, 4, name)
			for index, row := range rows {
				if row.rect.X < sidebarWidth ||
					row.rect.X+row.rect.W > sysManagerMinimumWidth {
					t.Fatalf("%s: row %d %q is outside the content column: %+v",
						name, index, row.label, row.rect)
				}
				if row.rect.H < rows[0].rect.H-0.5 {
					t.Fatalf("%s: row %d %q is clipped to %.0f of %.0f DIPs: %+v",
						name, index, row.label, row.rect.H, rows[0].rect.H, row.rect)
				}
				if row.rect.Y+row.rect.H > barTop+0.5 {
					t.Fatalf("%s: row %d %q ends at %.0f, under the pinned action bar at %.0f: %+v",
						name, index, row.label, row.rect.Y+row.rect.H, barTop, row.rect)
				}
				if center := row.rect.Y + row.rect.H/2; center > barTop+0.5 {
					t.Fatalf("%s: row %d %q has its center at %.0f, under the pinned action bar at %.0f: %+v",
						name, index, row.label, center, barTop, row.rect)
				}
			}
			// The acceptance failure: the fourth row's center landed on the
			// action bar and did not select it.
			last := rows[len(rows)-1]
			clickAndSelect(t, tester, manager, last, name)
			// The bar stays pinned below the rows with the selected target.
			barTop = actionBarTop(t, tester, selectedBarLabel(t, m, manager, test.tab),
				name+" selected")
			if last.rect.Y+last.rect.H > barTop+0.5 {
				t.Fatalf("%s: the selected row %q ends at %.0f, under the action bar at %.0f",
					name, last.label, last.rect.Y+last.rect.H, barTop)
			}
		}
	}
}

// TestSysManagerLastRowScrollsClearOfTheActionBar covers a list longer than
// the tab: scrolled to its end, the last row must sit fully above the pinned
// action bar and its center must select it. The list reserves the bar's height
// and the gap below it, so no row can scroll under the bar.
func TestSysManagerLastRowScrollsClearOfTheActionBar(t *testing.T) {
	cases := []struct {
		tab  appsysmanager.Tab
		hint func(locales.Messages) string
	}{
		{appsysmanager.TabProcesses,
			func(m locales.Messages) string { return m.SysManagerProcessHint() }},
		{appsysmanager.TabPorts,
			func(m locales.Messages) string { return m.SysManagerPortHint() }},
		{appsysmanager.TabServices,
			func(m locales.Messages) string { return m.SysManagerServiceHint() }},
	}
	for _, locale := range []string{"en", "zh-CN"} {
		m := locales.In(locale)
		for _, test := range cases {
			tester, manager := newSysManagerLayoutTester(t, sysManagerMinimumWidth,
				sysManagerMinimumHeight, locale, test.tab, 8)
			name := fmt.Sprintf("%s %s (8 rows)", locale, test.tab)
			actionBarTop(t, tester, test.hint(m), name)
			// Wheel over the list body, below the toolbar and above the bar.
			tester.Scroll(sysManagerMinimumWidth/2+80, 300, 0, 2000)
			tester.Frame()
			barTop := actionBarTop(t, tester, test.hint(m), name+" scrolled")
			label := lastRowLabel(test.tab, 8)
			row, ok := tester.Find(label)
			if !ok || row.W <= 0 || row.H <= 0 {
				t.Fatalf("%s: the last row %q is not shown after scrolling to the end: %v",
					name, label, tester.Texts())
			}
			last := sysManagerRow{label: label, rect: row}
			if last.rect.Y+last.rect.H > barTop+0.5 {
				t.Fatalf("%s: the last row %q ends at %.0f, under the pinned action bar at %.0f: %+v",
					name, last.label, last.rect.Y+last.rect.H, barTop, last.rect)
			}
			if center := last.rect.Y + last.rect.H/2; center > barTop+0.5 {
				t.Fatalf("%s: the last row %q has its center at %.0f, under the pinned action bar at %.0f: %+v",
					name, last.label, center, barTop, last.rect)
			}
			clickAndSelect(t, tester, manager, last, name)
			barTop = actionBarTop(t, tester, selectedBarLabel(t, m, manager, test.tab),
				name+" selected")
			if last.rect.Y+last.rect.H > barTop+0.5 {
				t.Fatalf("%s: the selected last row %q ends at %.0f, under the action bar at %.0f",
					name, last.label, last.rect.Y+last.rect.H, barTop)
			}
		}
	}
}

type sysManagerRow struct {
	label string
	rect  ui.Rect
}

// lastRowLabel is the label of the bottom row of a fixture with total rows:
// the process table sorts by CPU descending, ports ascend, and services keep
// the fixture order.
func lastRowLabel(tab appsysmanager.Tab, total int) string {
	switch tab {
	case appsysmanager.TabProcesses:
		return fmt.Sprintf("PID %d worker-0", 100)
	case appsysmanager.TabPorts:
		return fmt.Sprintf("tcp 0.0.0.0:%d", sysManagerLayoutPort(total-1, total))
	case appsysmanager.TabServices:
		name, _, _ := sysManagerLayoutUnit(total-1, total)
		return name
	}
	return ""
}

// listRows returns every row of tab exactly as the table labels it, top to
// bottom, and fails when one row is missing. The order comes from the rendered
// rects, so a table that sorts its rows is still walked as the user sees it.
func listRows(t *testing.T, tester *ui.Tester, tab appsysmanager.Tab,
	total int, name string) []sysManagerRow {
	t.Helper()
	labels := make([]string, 0, total)
	switch tab {
	case appsysmanager.TabProcesses:
		// The process table sorts by CPU descending, highest first.
		for i := total - 1; i >= 0; i-- {
			labels = append(labels, fmt.Sprintf("PID %d worker-%d", 100+i, i))
		}
	case appsysmanager.TabPorts:
		for i := 0; i < total; i++ {
			labels = append(labels, fmt.Sprintf("tcp 0.0.0.0:%d", sysManagerLayoutPort(i, total)))
		}
	case appsysmanager.TabServices:
		for i := 0; i < total; i++ {
			label, _, _ := sysManagerLayoutUnit(i, total)
			labels = append(labels, label)
		}
	}
	rows := make([]sysManagerRow, 0, len(labels))
	for index, label := range labels {
		rect, ok := tester.Find(label)
		if !ok || rect.W <= 0 || rect.H <= 0 {
			t.Fatalf("%s: row %d %q is not shown: %v", name, index, label, tester.Texts())
		}
		rows = append(rows, sysManagerRow{label: label, rect: rect})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].rect.Y < rows[j].rect.Y })
	return rows
}

// clickAndSelect clicks the row's center and proves the state chose it, which
// is what the action bar reads.
func clickAndSelect(t *testing.T, tester *ui.Tester, manager *uisysmanager.State,
	row sysManagerRow, name string) {
	t.Helper()
	if err := tester.Click(row.label); err != nil {
		t.Fatalf("%s: click the last row %q: %v", name, row.label, err)
	}
	tester.Frame()
	selected := selectedRowLabel(manager)
	if selected == "" {
		t.Fatalf("%s: clicking the center of %q did not select it: %v",
			name, row.label, tester.Texts())
	}
	if selected != row.label {
		t.Fatalf("%s: clicking %q selected %q: %v", name, row.label, selected, tester.Texts())
	}
}

// actionBarTop returns the top of the row pinned below the list: the hint text
// while nothing is selected, or the selected target's label afterwards. The
// label is centered in the fixed-height bar, so the bar's top edge is half its
// height above the label's center; comparing a row's bottom to the label's own
// top would let a row overlap the upper part of the bar unnoticed.
func actionBarTop(t *testing.T, tester *ui.Tester, text, name string) float32 {
	t.Helper()
	rect, ok := tester.Find(text)
	if !ok {
		t.Fatalf("%s: the action bar %q is missing: %v", name, text, tester.Texts())
	}
	return rect.Y + rect.H/2 - sysManagerActionBarHeight/2
}

// selectedBarLabel is the action bar's target line once a row is chosen.
func selectedBarLabel(t *testing.T, m locales.Messages, manager *uisysmanager.State,
	tab appsysmanager.Tab) string {
	t.Helper()
	switch tab {
	case appsysmanager.TabProcesses:
		if process, ok := manager.SelectedProcess(); ok {
			return m.SysManagerSelectedProcess(process.PID, process.Command)
		}
	case appsysmanager.TabPorts:
		if port, ok := manager.SelectedPort(); ok {
			return m.SysManagerSelectedPort(port.Protocol, port.Address,
				strconv.Itoa(port.Port), strconv.Itoa(port.PID))
		}
	case appsysmanager.TabServices:
		if unit, ok := manager.SelectedService(); ok {
			scope := m.SysManagerScopeSystem()
			if unit.Scope == "user" {
				scope = m.SysManagerScopeUser()
			}
			return m.SysManagerSelectedService(unit.Name, scope)
		}
	}
	t.Fatalf("%s: nothing is selected", tab)
	return ""
}

// sysManagerContentBounds returns the content column's left and right edges in
// tester coordinates at width: the sidebar and the content column's 16-DIP
// padding insets. In the real 720x480 window the same edges are 276..716 on
// screen; the window's own origin is 28 DIPs in.
func sysManagerContentBounds(width int) (left, right float32) {
	return sidebarWidth + shellPadding, float32(width) - shellPadding
}

// sysManagerTabLabels names every visible tab in a locale.
func sysManagerTabLabels(m locales.Messages) map[appsysmanager.Tab]string {
	return map[appsysmanager.Tab]string{
		appsysmanager.TabOverview:  m.SysManagerTabOverview(),
		appsysmanager.TabProcesses: m.SysManagerTabProcesses(),
		appsysmanager.TabPorts:     m.SysManagerTabPorts(),
		appsysmanager.TabServices:  m.SysManagerTabServices(),
		appsysmanager.TabTmux:      m.SysManagerTabTmux(),
		appsysmanager.TabDocker:    m.SysManagerTabDocker(),
		appsysmanager.TabGPU:       m.SysManagerTabGPU(),
	}
}

// TestSysManagerTabStripKeepsEveryTabInsideTheContentAtMinimumWindow renders
// the real shell at the minimum window's 704x421 client with all seven tabs.
// The strip overflows the 440-DIP content column, so it must scroll to the
// chosen tab: every tab, including the last one (GPU), has to be whole and
// inside the column while it is active, and no tab label may cross the
// column's right edge.
//
// Independent acceptance at a2d30004 measured the English GPU tab at
// 718,190 51x34, a right edge of 769: 53 DIPs past the content column and 37
// past the client. The plain Tab row had no scroll container.
func TestSysManagerTabStripKeepsEveryTabInsideTheContentAtMinimumWindow(t *testing.T) {
	left, right := sysManagerContentBounds(sysManagerMinimumWidth)
	for _, locale := range []string{"en", "zh-CN"} {
		m := locales.In(locale)
		tester, manager := newSysManagerLayoutTester(t, sysManagerMinimumWidth,
			sysManagerMinimumHeight, locale, appsysmanager.TabOverview, 4)
		labels := sysManagerTabLabels(m)
		tabs := manager.Tabs()
		if len(tabs) < 7 {
			t.Fatalf("%s: visible tabs = %d, want the seven-tab strip: %v", locale, len(tabs), tabs)
		}

		// Natural label widths, measured where the strip is wide enough not
		// to scroll; a narrower frame that cuts a label is caught below.
		tester.SetSize(1100, sysManagerMinimumHeight)
		tester.Frame()
		natural := make(map[appsysmanager.Tab]float32, len(tabs))
		for _, tab := range tabs {
			manager.SetTab(tab)
			tester.Frame()
			rect, ok := tester.Find(labels[tab])
			if !ok || rect.W <= 0 {
				t.Fatalf("%s: label %q of %s is not rendered: %v",
					locale, labels[tab], tab, tester.Texts())
			}
			natural[tab] = rect.W
		}

		tester.SetSize(sysManagerMinimumWidth, sysManagerMinimumHeight)
		tester.Frame()
		for _, tab := range tabs {
			manager.SetTab(tab)
			tester.Frame()
			name := fmt.Sprintf("%s %s", locale, tab)
			rect, ok := tester.Find(labels[tab])
			if !ok || rect.W <= 0 {
				t.Fatalf("%s: the active tab's label %q is not rendered: %v",
					name, labels[tab], tester.Texts())
			}
			if rect.X < left-0.5 || rect.X+rect.W > right+0.5 {
				t.Fatalf("%s: the active tab's label %q is outside the content column %.0f..%.0f: %+v",
					name, labels[tab], left, right, rect)
			}
			if rect.W < natural[tab]-0.5 {
				t.Fatalf("%s: the active tab's label %q is clipped to %.1f of %.1f DIPs: %+v",
					name, labels[tab], rect.W, natural[tab], rect)
			}
			t.Logf("%s: active tab %q at %.0f,%.0f %.0fx%.0f (natural width %.1f, content %.0f..%.0f)",
				name, labels[tab], rect.X, rect.Y, rect.W, rect.H, natural[tab], left, right)
			// A neighbour that is partly scrolled out must stop at the
			// viewport edge rather than run past the content column.
			for _, other := range tabs {
				neighbour, ok := tester.Find(labels[other])
				if !ok || neighbour.W <= 0 {
					continue
				}
				if neighbour.X < left-0.5 || neighbour.X+neighbour.W > right+0.5 {
					t.Fatalf("%s: while %s is active, %q crosses the content column %.0f..%.0f: %+v",
						name, tab, labels[other], left, right, neighbour)
				}
			}
		}
	}
}

// TestSysManagerTabStripRevealsTheActiveTabAfterTheWindowNarrows proves the
// strip re-reveals its active tab when the window shrinks, not only when the
// user picks a tab. A tab chosen while the window is wide must stay whole when
// the window is restored to the minimum size, so the width change alone has to
// scroll it back into view.
func TestSysManagerTabStripRevealsTheActiveTabAfterTheWindowNarrows(t *testing.T) {
	left, right := sysManagerContentBounds(sysManagerMinimumWidth)
	for _, locale := range []string{"en", "zh-CN"} {
		m := locales.In(locale)
		labels := sysManagerTabLabels(m)
		tester, manager := newSysManagerLayoutTester(t, 1100, sysManagerMinimumHeight,
			locale, appsysmanager.TabOverview, 4)
		manager.SetTab(appsysmanager.TabGPU)
		tester.Frame()
		natural, ok := tester.Find(labels[appsysmanager.TabGPU])
		if !ok || natural.W <= 0 {
			t.Fatalf("%s: the GPU label is not rendered at 1100: %v", locale, tester.Texts())
		}

		tester.SetSize(sysManagerMinimumWidth, sysManagerMinimumHeight)
		tester.Frame()
		if manager.Tab() != appsysmanager.TabGPU {
			t.Fatalf("%s: the narrow window changed the active tab to %s",
				locale, manager.Tab())
		}
		rect, ok := tester.Find(labels[appsysmanager.TabGPU])
		if !ok || rect.W <= 0 {
			t.Fatalf("%s: the active GPU label is not rendered after narrowing: %v",
				locale, tester.Texts())
		}
		if rect.X < left-0.5 || rect.X+rect.W > right+0.5 {
			t.Fatalf("%s: after the window narrowed the active GPU label is outside the content column %.0f..%.0f: %+v",
				locale, left, right, rect)
		}
		if rect.W < natural.W-0.5 {
			t.Fatalf("%s: after the window narrowed the active GPU label is clipped to %.1f of %.1f DIPs: %+v",
				locale, rect.W, natural.W, rect)
		}
		t.Logf("%s: after 1100 -> 704 the active GPU label is %.0f,%.0f %.0fx%.0f (natural %.1f, content %.0f..%.0f)",
			locale, rect.X, rect.Y, rect.W, rect.H, natural.W, left, right)
	}
}

// TestSysManagerTabClickSelectsTheTabAtMinimumWindow proves a tab's hit target
// still works inside the scrolling strip: clicking a visible label chooses it.
// The strip keeps its choice in the surface state, not in a frame-local
// variable, because the click is applied after the frame that built the tabs.
func TestSysManagerTabClickSelectsTheTabAtMinimumWindow(t *testing.T) {
	for _, locale := range []string{"en", "zh-CN"} {
		m := locales.In(locale)
		labels := sysManagerTabLabels(m)
		tester, manager := newSysManagerLayoutTester(t, sysManagerMinimumWidth,
			sysManagerMinimumHeight, locale, appsysmanager.TabOverview, 4)
		if err := tester.Click(labels[appsysmanager.TabProcesses]); err != nil {
			t.Fatalf("%s: click the Processes tab: %v", locale, err)
		}
		tester.Frame()
		if manager.Tab() != appsysmanager.TabProcesses {
			t.Fatalf("%s: clicking the Processes tab chose %s: %v",
				locale, manager.Tab(), tester.Texts())
		}
		// A tab at the far end is reachable after its neighbour scrolls the
		// strip, and clicking it selects it.
		if err := tester.Click(labels[appsysmanager.TabPorts]); err != nil {
			t.Fatalf("%s: click the Ports tab: %v", locale, err)
		}
		tester.Frame()
		if manager.Tab() != appsysmanager.TabPorts {
			t.Fatalf("%s: clicking the Ports tab chose %s: %v",
				locale, manager.Tab(), tester.Texts())
		}
	}
}

// TestSysManagerHeaderAndStatusFitTheContentAtMinimumWindow measures every
// header and status element at the minimum window's client: the title, the
// live badge, the updated-at age, the latency, the refresh interval control
// and its button, the tab-layout menu, and each list's count. Independent
// acceptance at a2d30004 measured the English Latency text at 712,163 62x14,
// right edge 774 (42 DIPs past the client) and the zh-CN one 5 DIPs past.
func TestSysManagerHeaderAndStatusFitTheContentAtMinimumWindow(t *testing.T) {
	left, right := sysManagerContentBounds(sysManagerMinimumWidth)
	for _, locale := range []string{"en", "zh-CN"} {
		m := locales.In(locale)
		cases := []struct {
			tab     appsysmanager.Tab
			summary string
		}{
			{appsysmanager.TabOverview, ""},
			{appsysmanager.TabProcesses, m.SysManagerProcessesCount(4)},
			{appsysmanager.TabPorts, m.SysManagerPortsCount(4)},
			{appsysmanager.TabServices, m.SysManagerServicesCount(4)},
		}
		for _, test := range cases {
			tester, _ := newSysManagerLayoutTester(t, sysManagerMinimumWidth,
				sysManagerMinimumHeight, locale, test.tab, 4)
			name := fmt.Sprintf("%s %s", locale, test.tab)
			nodes := []struct {
				what string
				text string
			}{
				{"title", m.SysManagerTitle()},
				{"live status", m.SysManagerStatusLive()},
				{"updated-at", m.SysManagerUpdatedSeconds(3600)},
				{"latency", m.SysManagerLatency() + " " + m.SysManagerLatencyValue(12)},
				{"refresh interval", m.SysManagerRefresh()},
				{"refresh now", m.SysManagerRefreshNow()},
				{"tab layout menu", m.SysManagerTabsLayout()},
			}
			if test.summary != "" {
				nodes = append(nodes, struct {
					what string
					text string
				}{"count", test.summary})
			}
			for _, node := range nodes {
				rect, ok := tester.Find(node.text)
				if !ok || rect.W <= 0 {
					t.Fatalf("%s: the %s %q is not rendered: %v",
						name, node.what, node.text, tester.Texts())
				}
				if rect.X < left-0.5 || rect.X+rect.W > right+0.5 {
					t.Fatalf("%s: the %s %q crosses the content column %.0f..%.0f: %+v",
						name, node.what, node.text, left, right, rect)
				}
				t.Logf("%s: %s %q at %.0f,%.0f %.0fx%.0f (content %.0f..%.0f)",
					name, node.what, node.text, rect.X, rect.Y, rect.W, rect.H, left, right)
			}
		}
	}
}

// TestSysManagerPortSelectionLabelHasNoDigitGrouping proves a port is a label,
// not a quantity: the selection bar must name port 8080 as 8080, never 8,080.
// Acceptance saw "tcp *:8,080 · PID 904" because the catalog's int placeholder
// grouped the digits; the surface now formats both values itself.
func TestSysManagerPortSelectionLabelHasNoDigitGrouping(t *testing.T) {
	for _, locale := range []string{"en", "zh-CN"} {
		tester, manager := newSysManagerLayoutTester(t, sysManagerMinimumWidth,
			sysManagerMinimumHeight, locale, appsysmanager.TabPorts, 4)
		name := fmt.Sprintf("%s ports", locale)
		row := fmt.Sprintf("tcp 0.0.0.0:%d", 8080)
		if err := tester.Click(row); err != nil {
			t.Fatalf("%s: select the last port row %q: %v", name, row, err)
		}
		tester.Frame()
		port, ok := manager.SelectedPort()
		if !ok || port.Port != 8080 {
			t.Fatalf("%s: the last port row is not selected: %+v", name, port)
		}
		want := fmt.Sprintf("%s %s:%d · PID %d", port.Protocol, port.Address, port.Port, port.PID)
		if !tester.HasText(want) {
			t.Fatalf("%s: the selection bar does not read %q: %v", name, want, tester.Texts())
		}
		if tester.HasText("8,080") {
			t.Fatalf("%s: the port rendered with a thousands separator: %v", name, tester.Texts())
		}
		t.Logf("%s: selection bar reads %q", name, want)
	}
}

// selectedRowLabel returns the chosen row's label as the table names it, or
// "" when nothing is chosen.
func selectedRowLabel(manager *uisysmanager.State) string {
	switch manager.Tab() {
	case appsysmanager.TabProcesses:
		if process, ok := manager.SelectedProcess(); ok {
			return fmt.Sprintf("PID %d %s", process.PID, process.Command)
		}
	case appsysmanager.TabPorts:
		if port, ok := manager.SelectedPort(); ok {
			return fmt.Sprintf("%s %s:%d", port.Protocol, port.Address, port.Port)
		}
	case appsysmanager.TabServices:
		if unit, ok := manager.SelectedService(); ok {
			return unit.Name
		}
	}
	return ""
}
