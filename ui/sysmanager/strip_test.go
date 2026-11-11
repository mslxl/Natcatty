package sysmanager

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	appsysmanager "github.com/mslxl/natcatty/application/sysmanager"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
)

// stripSurface builds one of the two surfaces that share the System manager
// preferences: the side panel's System Tool at its 280- and 360-DIP widths, or
// the full-page System manager at 704 and 1100.
type stripSurface struct {
	name  string
	width int
	side  bool
}

func stripSurfaces() []stripSurface {
	return []stripSurface{
		{name: "side-280", width: 280, side: true},
		{name: "side-360", width: 360, side: true},
		{name: "page-704", width: 704},
		{name: "page-1100", width: 1100},
	}
}

func newStripState(t *testing.T) (*State, *fakeService, *fakePreferences) {
	t.Helper()
	service := newFakeService()
	service.snapshot = demoSnapshot()
	prefs := newFakePreferences()
	state := newTestState(service, prefs)
	state.ShowHost("alpha")
	return state, service, prefs
}

func stripTester(state *State, surface stripSurface, locale string) *ui.Tester {
	var tt *ui.Tester
	if surface.side {
		tt = ui.NewTester(SideView(state, float32(surface.width)), surface.width, 600)
	} else {
		tt = ui.NewTester(View(state), surface.width, 600)
	}
	tt.SetLocale(locale)
	tt.Frame()
	return tt
}

func withStripStyle(t *testing.T, style kit.Style) {
	t.Helper()
	kit.SetStyle(style)
	t.Cleanup(func() { kit.SetStyle(nil) })
}

func exactStripTexts(tt *ui.Tester, want string) int {
	count := 0
	for _, text := range tt.Texts() {
		if text == want {
			count++
		}
	}
	return count
}

// stripTabs returns each visible tab's box. It walks the accessible labels, so
// both the labeled and icon-only forms are reachable.
func stripTabs(t *testing.T, tt *ui.Tester, state *State, m locales.Messages,
	name string) map[appsysmanager.Tab]ui.Rect {
	t.Helper()
	boxes := make(map[appsysmanager.Tab]ui.Rect, len(state.Tabs()))
	for _, tab := range state.Tabs() {
		rect, ok := tt.Find(tabLabel(m, tab))
		if !ok || rect.W <= 0 || rect.H <= 0 {
			t.Fatalf("%s: tab %s is not rendered: %v", name, tab, tt.Texts())
		}
		boxes[tab] = rect
	}
	return boxes
}

// hoverTooltip moves over label and proves its tooltip appears: the exact
// label gains another rendered occurrence after the tooltip's delay.
func hoverTooltip(t *testing.T, tt *ui.Tester, label, name string) {
	t.Helper()
	rect, ok := tt.Find(label)
	if !ok || rect.W <= 0 {
		t.Fatalf("%s: %q is not reachable for its tooltip: %v", name, label, tt.Texts())
	}
	before := exactStripTexts(tt, label)
	tt.Move(rect.X+rect.W/2, rect.Y+rect.H/2)
	tt.Frame()
	tt.Advance(time.Second)
	tt.Frame()
	if after := exactStripTexts(tt, label); after <= before {
		t.Fatalf("%s: hovering %q showed no tooltip: %v", name, label, tt.Texts())
	}
}

// assertAboveStrip proves what sits above the strip really ends above it,
// rather than merely carrying a label in the rendered text.
func assertAboveStrip(t *testing.T, name, what string, rect, strip ui.Rect) {
	t.Helper()
	if rect.Y+rect.H > strip.Y+0.5 {
		t.Fatalf("%s: the %s at %.0f..%.0f is not above the strip starting at %.0f: %+v",
			name, what, rect.Y, rect.Y+rect.H, strip.Y, rect)
	}
}

func openLayoutMenu(t *testing.T, tt *ui.Tester, m locales.Messages, name string) {
	t.Helper()
	if err := tt.Click(m.SysManagerTabsLayout()); err != nil {
		t.Fatalf("%s: open the tab layout menu: %v", name, err)
	}
	tt.Frame()
	if tt.Menu() == nil {
		t.Fatalf("%s: the tab layout button opened no menu: %v", name, tt.Texts())
	}
}

func chooseLayout(t *testing.T, tt *ui.Tester, m locales.Messages, name string, path ...string) {
	t.Helper()
	openLayoutMenu(t, tt, m, name)
	if err := tt.ChooseMenuItem(path...); err != nil {
		t.Fatalf("%s: choose %q: %v", name, strings.Join(path, " > "), err)
	}
	tt.Frame()
}

// TestSystemTabStripFormsAcrossSurfacesAndStyles proves the strip is labeled
// on the wide full page and icon-only at the narrow page and both side panel
// widths, in both Interface Styles and both locales. The labeled form has a
// drawn label in addition to its accessible name; the compact form keeps the
// accessible name and a square glyph; the strip never mixes the two forms.
func TestSystemTabStripFormsAcrossSurfacesAndStyles(t *testing.T) {
	for _, style := range []struct {
		name  string
		style kit.Style
	}{{"classic", kit.Classic}, {"macos", kit.MacOS}} {
		t.Run(style.name, func(t *testing.T) {
			withStripStyle(t, style.style)
			for _, locale := range []string{"en", "zh-CN"} {
				m := locales.In(locale)
				for _, surface := range stripSurfaces() {
					name := locale + " " + surface.name
					state, _, _ := newStripState(t)
					tt := stripTester(state, surface, locale)
					boxes := stripTabs(t, tt, state, m, name)
					strip, ok := tt.Find(m.SysManagerSystemTabs())
					if !ok || strip.W <= 0 || strip.H <= 0 {
						t.Fatalf("%s: the strip's accessible label is missing: %v",
							name, tt.Texts())
					}

					labeled := surface.width >= 1100
					for tab, rect := range boxes {
						if labeled {
							if rect.W <= 40 {
								t.Fatalf("%s: tab %s is %.0f DIPs wide, want its label: %+v",
									name, tab, rect.W, rect)
							}
							if exactStripTexts(tt, tabLabel(m, tab)) < 2 {
								t.Fatalf("%s: tab %s has no drawn label: %v",
									name, tab, tt.Texts())
							}
							continue
						}
						if rect.W > 40 {
							t.Fatalf("%s: tab %s is %.0f DIPs wide, want icon-only: %+v",
								name, tab, rect.W, rect)
						}
						if exactStripTexts(tt, tabLabel(m, tab)) < 1 {
							t.Fatalf("%s: compact tab %s lost its accessible name: %v",
								name, tab, tt.Texts())
						}
					}

					// The status row and refresh controls sit above the same
					// strip on both surfaces. The status dot is a 7-DIP box
					// centered in the status text's row and has no accessible
					// text of its own, so its live color is checked through the
					// state that chooses it. Because the dot is centered in
					// this row and is shorter than the text, the text's bottom
					// above the strip also bounds the dot's bottom.
					refresh, ok := tt.Find(m.SysManagerRefreshNow())
					if !ok || refresh.W <= 0 {
						t.Fatalf("%s: Refresh is missing above the strip: %v",
							name, tt.Texts())
					}
					assertAboveStrip(t, name, "Refresh", refresh, strip)
					if surface.side {
						status, ok := tt.Find(m.SysManagerStatusLive())
						if !ok || status.W <= 0 {
							t.Fatalf("%s: the side panel's status text is missing: %v",
								name, tt.Texts())
						}
						assertAboveStrip(t, name, "status text", status, strip)
						interval, ok := tt.Find(m.SysManagerRefresh())
						if !ok || interval.W <= 0 {
							t.Fatalf("%s: the side panel's interval is missing: %v",
								name, tt.Texts())
						}
						assertAboveStrip(t, name, "refresh interval", interval, strip)
						if got := state.TabState(state.Tab()).Status; got != appsysmanager.StatusLive {
							t.Fatalf("%s: the status dot's state is %s, want live", name, got)
						}
						if tt.HasText(m.SysManagerTitle()) {
							t.Fatalf("%s: the side panel shows the page title: %v",
								name, tt.Texts())
						}
					} else {
						host, ok := tt.Find(m.SysManagerHost())
						if !ok || host.W <= 0 {
							t.Fatalf("%s: the full page's Host picker is missing: %v",
								name, tt.Texts())
						}
						assertAboveStrip(t, name, "Host picker", host, strip)
					}

					// The Collapsed Tabs trigger is hidden while nothing is
					// collapsed, in every form.
					if _, ok := tt.Find(m.SysManagerMoreTabs()); ok {
						t.Fatalf("%s: an empty Collapsed Tabs trigger is shown: %v",
							name, tt.Texts())
					}
					for tab := range boxes {
						hoverTooltip(t, tt, tabLabel(m, tab), name+" "+string(tab))
					}
				}
			}
		})
	}
}

// TestSystemTabStripCollapsedMenuListsExactlyCollapsedTabs proves the
// Collapsed Tabs menu's membership, not only that one row is present. Two
// collapsed tabs and one hidden tab give every category a different expected
// count: each collapsed tab adds its menu row's accessible name and drawn
// label, while the visible and hidden tabs add nothing to the strip's own
// texts. The menu's rows also keep their saved order.
func TestSystemTabStripCollapsedMenuListsExactlyCollapsedTabs(t *testing.T) {
	for _, style := range []struct {
		name  string
		style kit.Style
	}{{"classic", kit.Classic}, {"macos", kit.MacOS}} {
		t.Run(style.name, func(t *testing.T) {
			withStripStyle(t, style.style)
			m := locales.In("en")
			state, _, _ := newStripState(t)
			if err := state.SetTabCollapsed(appsysmanager.TabDocker, true); err != nil {
				t.Fatalf("%s: collapse Docker: %v", style.name, err)
			}
			if err := state.SetTabCollapsed(appsysmanager.TabGPU, true); err != nil {
				t.Fatalf("%s: collapse GPU: %v", style.name, err)
			}
			if err := state.SetTabVisible(appsysmanager.TabTmux, false); err != nil {
				t.Fatalf("%s: hide tmux: %v", style.name, err)
			}
			tt := stripTester(state, stripSurface{name: "side-360", width: 360, side: true}, "en")
			collapsed := state.Collapsed()
			if len(collapsed) != 2 || collapsed[0] != appsysmanager.TabDocker ||
				collapsed[1] != appsysmanager.TabGPU {
				t.Fatalf("%s: collapsed tabs are %v, want Docker then GPU",
					style.name, collapsed)
			}

			before := make(map[appsysmanager.Tab]int, len(state.AllTabs()))
			for _, tab := range state.AllTabs() {
				before[tab] = exactStripTexts(tt, tabLabel(m, tab))
			}
			if _, ok := tt.Find(m.SysManagerMoreTabs()); !ok {
				t.Fatalf("%s: the Collapsed Tabs trigger is missing: %v",
					style.name, tt.Texts())
			}
			if err := tt.Click(m.SysManagerMoreTabs()); err != nil {
				t.Fatalf("%s: open the Collapsed Tabs menu: %v", style.name, err)
			}
			tt.Frame()
			if !tt.HasText(m.SysManagerCollapsedTabs()) {
				t.Fatalf("%s: the Collapsed Tabs menu lacks its title: %v",
					style.name, tt.Texts())
			}

			wantAdded := map[appsysmanager.Tab]int{
				appsysmanager.TabDocker: 2, // row accessible name + drawn label
				appsysmanager.TabGPU:    2,
			}
			for _, tab := range state.AllTabs() {
				added := exactStripTexts(tt, tabLabel(m, tab)) - before[tab]
				want := wantAdded[tab]
				if added != want {
					t.Fatalf("%s: tab %s adds %d texts when the Collapsed Tabs menu opens, want %d; texts=%v",
						style.name, tab, added, want, tt.Texts())
				}
			}
			docker, ok := tt.Find(tabLabel(m, appsysmanager.TabDocker))
			if !ok || docker.W <= 0 || docker.H <= 0 {
				t.Fatalf("%s: collapsed Docker is absent from its menu: %v",
					style.name, tt.Texts())
			}
			gpu, ok := tt.Find(tabLabel(m, appsysmanager.TabGPU))
			if !ok || gpu.W <= 0 || gpu.H <= 0 {
				t.Fatalf("%s: collapsed GPU is absent from its menu: %v",
					style.name, tt.Texts())
			}
			if docker.Y+docker.H/2 >= gpu.Y+gpu.H/2 {
				t.Fatalf("%s: the menu lists GPU at y%.0f before Docker at y%.0f, want saved order",
					style.name, gpu.Y, docker.Y)
			}
		})
	}
}

// TestSystemTabStripMapsEachTabToItsIcon proves every System Tab draws the
// glyph the ticket names, including the deliberate archive mark for Docker.
func TestSystemTabStripMapsEachTabToItsIcon(t *testing.T) {
	want := map[appsysmanager.Tab]string{
		appsysmanager.TabOverview:  icons.Activity,
		appsysmanager.TabProcesses: icons.List,
		appsysmanager.TabPorts:     icons.Network,
		appsysmanager.TabServices:  icons.Settings,
		appsysmanager.TabTmux:      icons.Terminal,
		appsysmanager.TabDocker:    icons.Archive,
		appsysmanager.TabGPU:       icons.Cpu,
	}
	for tab, icon := range want {
		if got := TabIcon(tab); got != icon {
			t.Errorf("tab %s draws %q, want %q", tab, got, icon)
		}
	}
}

// TestSystemTabStripLayoutMenuEntriesCarryCheckedAndDisabledState proves the
// menu's state without the native menu: the tester reports a menu's labels but
// not its checks or disabled items, so the entries that build it carry the
// visible, collapsed, hidden, and locked state the menu shows.
func TestSystemTabStripLayoutMenuEntriesCarryCheckedAndDisabledState(t *testing.T) {
	state, _, _ := newStripState(t)
	byTab := func() map[appsysmanager.Tab]tabLayoutEntry {
		entries := tabLayoutEntries(state)
		if len(entries) != 7 {
			t.Fatalf("the layout menu has %d tab entries, want seven: %+v", len(entries), entries)
		}
		out := make(map[appsysmanager.Tab]tabLayoutEntry, len(entries))
		for _, entry := range entries {
			if _, duplicate := out[entry.Tab]; duplicate {
				t.Fatalf("the layout menu repeats tab %s: %+v", entry.Tab, entries)
			}
			out[entry.Tab] = entry
		}
		return out
	}

	entries := byTab()
	overview := entries[appsysmanager.TabOverview]
	if !overview.Locked || !overview.ShowDisabled || !overview.CollapseDisabled ||
		!overview.HideDisabled {
		t.Fatalf("Overview is not locked: %+v", overview)
	}
	if !overview.ShowChecked || overview.CollapseChecked || overview.HideChecked {
		t.Fatalf("Overview checks show=%v collapse=%v hide=%v, want show only",
			overview.ShowChecked, overview.CollapseChecked, overview.HideChecked)
	}
	docker := entries[appsysmanager.TabDocker]
	if docker.Locked || docker.ShowDisabled || docker.CollapseDisabled || docker.HideDisabled {
		t.Fatalf("a visible Docker tab is locked: %+v", docker)
	}
	if !docker.ShowChecked || docker.CollapseChecked || docker.HideChecked {
		t.Fatalf("visible Docker checks show=%v collapse=%v hide=%v, want show only",
			docker.ShowChecked, docker.CollapseChecked, docker.HideChecked)
	}

	if err := state.SetTabCollapsed(appsysmanager.TabDocker, true); err != nil {
		t.Fatalf("collapse Docker: %v", err)
	}
	if err := state.SetTabVisible(appsysmanager.TabGPU, false); err != nil {
		t.Fatalf("hide GPU: %v", err)
	}
	entries = byTab()
	docker = entries[appsysmanager.TabDocker]
	if !docker.CollapseChecked || docker.ShowChecked || docker.HideChecked {
		t.Fatalf("collapsed Docker checks show=%v collapse=%v hide=%v, want collapse only",
			docker.ShowChecked, docker.CollapseChecked, docker.HideChecked)
	}
	gpu := entries[appsysmanager.TabGPU]
	if !gpu.HideChecked || gpu.ShowChecked || gpu.CollapseChecked {
		t.Fatalf("hidden GPU checks show=%v collapse=%v hide=%v, want hide only",
			gpu.ShowChecked, gpu.CollapseChecked, gpu.HideChecked)
	}
	if gpu.Locked || gpu.ShowDisabled || gpu.CollapseDisabled || gpu.HideDisabled {
		t.Fatalf("a hidden GPU tab is locked: %+v", gpu)
	}
	overview = entries[appsysmanager.TabOverview]
	if !overview.Locked || !overview.ShowDisabled || !overview.CollapseDisabled ||
		!overview.HideDisabled || !overview.ShowChecked {
		t.Fatalf("Overview lost its lock after other tabs changed: %+v", overview)
	}
}

// TestSystemTabStripLocksOverviewVisible proves Overview cannot leave the
// strip: the layout menu disables its Show, Collapse, and Hide items, the
// state refuses both hiding and collapsing it, and neither reaches the shared
// preferences.
func TestSystemTabStripLocksOverviewVisible(t *testing.T) {
	for _, style := range []struct {
		name  string
		style kit.Style
	}{{"classic", kit.Classic}, {"macos", kit.MacOS}} {
		t.Run(style.name, func(t *testing.T) {
			withStripStyle(t, style.style)
			m := locales.In("en")
			state, _, prefs := newStripState(t)
			tt := stripTester(state, stripSurface{name: "page-1100", width: 1100}, "en")

			if err := state.SetTabVisible(appsysmanager.TabOverview, false); err != nil {
				t.Fatalf("%s: hiding Overview failed: %v", style.name, err)
			}
			if err := state.SetTabCollapsed(appsysmanager.TabOverview, true); err != nil {
				t.Fatalf("%s: collapsing Overview failed: %v", style.name, err)
			}
			if !state.TabVisible(appsysmanager.TabOverview) ||
				state.TabCollapsed(appsysmanager.TabOverview) {
				t.Fatalf("%s: Overview is visible=%v collapsed=%v",
					style.name, state.TabVisible(appsysmanager.TabOverview),
					state.TabCollapsed(appsysmanager.TabOverview))
			}
			if saved := prefs.Settings().SysManager; len(saved.HiddenTabs) != 0 ||
				len(saved.CollapsedTabs) != 0 {
				t.Fatalf("%s: locked Overview reached the preferences: hidden=%v collapsed=%v",
					style.name, saved.HiddenTabs, saved.CollapsedTabs)
			}

			openLayoutMenu(t, tt, m, style.name+" locked")
			for _, action := range []string{m.SysManagerCollapseTab(), m.SysManagerHideTab()} {
				err := tt.ChooseMenuItem(tabLabel(m, appsysmanager.TabOverview), action)
				if err == nil || !strings.Contains(err.Error(), "disabled") {
					t.Fatalf("%s: Overview > %s chose with err=%v, want a disabled item",
						style.name, action, err)
				}
			}
			tt.CloseMenu()
			tt.Frame()
			if !tt.HasText(tabLabel(m, appsysmanager.TabOverview)) {
				t.Fatalf("%s: Overview left the strip: %v", style.name, tt.Texts())
			}
			if _, ok := tt.Find(m.SysManagerMoreTabs()); ok {
				t.Fatalf("%s: a locked Overview left a Collapsed Tabs trigger: %v",
					style.name, tt.Texts())
			}
		})
	}
}

// TestSystemTabStripScrollsAtTheNarrowSidePanel proves the icon-only strip is
// the residual scroll case at the 280-DIP side panel: choosing the last tab
// reveals it wholly before the trailing controls, while the layout button and
// a Collapsed Tabs trigger stay reachable.
func TestSystemTabStripScrollsAtTheNarrowSidePanel(t *testing.T) {
	for _, style := range []struct {
		name  string
		style kit.Style
	}{{"classic", kit.Classic}, {"macos", kit.MacOS}} {
		t.Run(style.name, func(t *testing.T) {
			withStripStyle(t, style.style)
			m := locales.In("en")
			state, _, _ := newStripState(t)
			tt := stripTester(state, stripSurface{name: "side-280", width: 280, side: true}, "en")
			state.SetTab(appsysmanager.TabGPU)
			tt.Frame()
			tt.Frame()

			strip, ok := tt.Find(m.SysManagerSystemTabs())
			if !ok || strip.W <= 0 {
				t.Fatalf("%s: the strip is not rendered: %v", style.name, tt.Texts())
			}
			layout, ok := tt.Find(m.SysManagerTabsLayout())
			if !ok || layout.W <= 0 {
				t.Fatalf("%s: the layout button is not reachable: %v", style.name, tt.Texts())
			}
			gpu, ok := tt.Find(tabLabel(m, appsysmanager.TabGPU))
			if !ok || gpu.W <= 0 {
				t.Fatalf("%s: the active GPU tab is not rendered: %v", style.name, tt.Texts())
			}
			if gpu.X <= strip.X+0.5 {
				t.Fatalf("%s: the strip did not scroll to reveal the active GPU tab: GPU at %.0f, strip at %.0f",
					style.name, gpu.X, strip.X)
			}
			if gpu.X < strip.X-0.5 || gpu.X+gpu.W > layout.X+0.5 {
				t.Fatalf("%s: the active GPU tab %.0f..%.0f is not whole before the layout button at %.0f..%.0f",
					style.name, gpu.X, gpu.X+gpu.W, layout.X, layout.X+layout.W)
			}
			if layout.X+layout.W > 280.5 {
				t.Fatalf("%s: the layout button crosses the 280-DIP side panel: %+v", style.name, layout)
			}
			openLayoutMenu(t, tt, m, style.name+" layout")
			tt.CloseMenu()

			// A Collapsed Tabs trigger is also outside the scroll and reachable.
			chooseLayout(t, tt, m, style.name+" collapse", tabLabel(m, appsysmanager.TabDocker),
				m.SysManagerCollapseTab())
			more, ok := tt.Find(m.SysManagerMoreTabs())
			if !ok || more.W <= 0 {
				t.Fatalf("%s: the Collapsed Tabs trigger is not rendered: %v",
					style.name, tt.Texts())
			}
			layout, ok = tt.Find(m.SysManagerTabsLayout())
			if !ok || more.X < layout.X+layout.W-0.5 {
				t.Fatalf("%s: the Collapsed Tabs trigger at %.0f does not follow the layout button %.0f..%.0f",
					style.name, more.X, layout.X, layout.X+layout.W)
			}
			if more.X+more.W > 280.5 {
				t.Fatalf("%s: the Collapsed Tabs trigger crosses the side panel: %+v",
					style.name, more)
			}
			if err := tt.Click(m.SysManagerMoreTabs()); err != nil {
				t.Fatalf("%s: open the Collapsed Tabs menu: %v", style.name, err)
			}
			tt.Frame()
			if !tt.HasText(m.SysManagerCollapsedTabs()) {
				t.Fatalf("%s: the Collapsed Tabs menu has no title: %v", style.name, tt.Texts())
			}
		})
	}
}

// TestSystemTabStripCollapsesAndRestoresThroughTheMenu proves the tab layout
// submenu collapses a tab, the Collapsed Tabs menu lists exactly it and returns
// it to the strip, the trigger hides when empty, and both are persisted across
// a modeled restart shared with the other surface.
func TestSystemTabStripCollapsesAndRestoresThroughTheMenu(t *testing.T) {
	for _, style := range []struct {
		name  string
		style kit.Style
	}{{"classic", kit.Classic}, {"macos", kit.MacOS}} {
		t.Run(style.name, func(t *testing.T) {
			withStripStyle(t, style.style)
			for _, locale := range []string{"en", "zh-CN"} {
				m := locales.In(locale)
				name := style.name + " " + locale
				state, service, prefs := newStripState(t)
				tt := stripTester(state, stripSurface{name: "side-360", width: 360, side: true}, locale)

				if _, ok := tt.Find(m.SysManagerMoreTabs()); ok {
					t.Fatalf("%s: an empty Collapsed Tabs trigger is shown: %v",
						name, tt.Texts())
				}
				chooseLayout(t, tt, m, name+" collapse GPU",
					tabLabel(m, appsysmanager.TabGPU), m.SysManagerCollapseTab())
				if !state.TabCollapsed(appsysmanager.TabGPU) {
					t.Fatalf("%s: collapsing GPU was not saved: %v", name, state.Collapsed())
				}
				if state.TabVisible(appsysmanager.TabGPU) {
					t.Fatalf("%s: the collapsed GPU tab stayed visible: %v", name, state.Tabs())
				}
				if tt.HasText(tabLabel(m, appsysmanager.TabGPU)) {
					t.Fatalf("%s: the collapsed GPU tab stayed in the strip: %v",
						name, tt.Texts())
				}
				more, ok := tt.Find(m.SysManagerMoreTabs())
				if !ok {
					t.Fatalf("%s: the Collapsed Tabs trigger is missing: %v", name, tt.Texts())
				}
				layout, ok := tt.Find(m.SysManagerTabsLayout())
				if !ok || more.X < layout.X+layout.W-0.5 {
					t.Fatalf("%s: the Collapsed Tabs trigger at %.0f does not follow the layout button %+v",
						name, more.X, layout)
				}

				// The menu lists exactly the collapsed tab, with its label.
				if err := tt.Click(m.SysManagerMoreTabs()); err != nil {
					t.Fatalf("%s: open the Collapsed Tabs menu: %v", name, err)
				}
				tt.Frame()
				if !tt.HasText(m.SysManagerCollapsedTabs()) {
					t.Fatalf("%s: the Collapsed Tabs menu lacks its title: %v",
						name, tt.Texts())
				}
				if !tt.HasText(tabLabel(m, appsysmanager.TabGPU)) {
					t.Fatalf("%s: the Collapsed Tabs menu lacks GPU: %v", name, tt.Texts())
				}
				if err := tt.Click(tabLabel(m, appsysmanager.TabGPU)); err != nil {
					t.Fatalf("%s: choose the collapsed GPU tab: %v", name, err)
				}
				tt.Frame()
				if state.Tab() != appsysmanager.TabGPU || !state.TabVisible(appsysmanager.TabGPU) ||
					state.TabCollapsed(appsysmanager.TabGPU) {
					t.Fatalf("%s: choosing collapsed GPU left tab=%s visible=%v collapsed=%v",
						name, state.Tab(), state.TabVisible(appsysmanager.TabGPU),
						state.TabCollapsed(appsysmanager.TabGPU))
				}
				if _, ok := tt.Find(m.SysManagerMoreTabs()); ok {
					t.Fatalf("%s: the Collapsed Tabs trigger stayed after the menu emptied: %v",
						name, tt.Texts())
				}

				// Collapse again and read it from the full page over the same
				// preferences, as a restart and as the other surface would.
				chooseLayout(t, tt, m, name+" collapse again",
					tabLabel(m, appsysmanager.TabDocker), m.SysManagerCollapseTab())
				restored := NewState(service, fakeHosts{hosts: testHosts()}, prefs)
				restored.Activate()
				if !restored.TabCollapsed(appsysmanager.TabDocker) ||
					!restored.TabVisible(appsysmanager.TabGPU) {
					t.Fatalf("%s: the restart lost the layout: collapsed=%v visible=%v",
						name, restored.Collapsed(), restored.Tabs())
				}
				page := ui.NewTester(View(restored), 1100, 600)
				page.SetLocale(locale)
				page.Frame()
				if _, ok := page.Find(m.SysManagerMoreTabs()); !ok {
					t.Fatalf("%s: the other surface lost the Collapsed Tabs trigger: %v",
						name, page.Texts())
				}
				if page.HasText(tabLabel(m, appsysmanager.TabDocker)) {
					t.Fatalf("%s: the other surface kept the collapsed Docker tab in the strip: %v",
						name, page.Texts())
				}
			}
		})
	}
}

// TestSystemTabStripHidesNearestShowsRestoredAndResets proves Hide removes the
// active tab from the strip and the Collapsed Tabs menu, activates the nearest
// visible tab, Show restores the stored order, and Reset restores every default
// without changing the refresh interval. Both surfaces and both styles are
// exercised.
func TestSystemTabStripHidesNearestShowsRestoredAndResets(t *testing.T) {
	for _, style := range []struct {
		name  string
		style kit.Style
	}{{"classic", kit.Classic}, {"macos", kit.MacOS}} {
		t.Run(style.name, func(t *testing.T) {
			withStripStyle(t, style.style)
			m := locales.In("en")
			for _, surface := range []stripSurface{
				{name: "side-360", width: 360, side: true},
				{name: "page-1100", width: 1100},
			} {
				name := style.name + " " + surface.name
				state, _, prefs := newStripState(t)
				tt := stripTester(state, surface, "en")

				// Hiding the active GPU tab moves to the nearest visible tab,
				// Docker, and leaves neither a strip tab nor a Collapsed Tabs
				// entry.
				state.SetTab(appsysmanager.TabGPU)
				tt.Frame()
				chooseLayout(t, tt, m, name+" hide GPU",
					tabLabel(m, appsysmanager.TabGPU), m.SysManagerHideTab())
				if state.Tab() != appsysmanager.TabDocker {
					t.Fatalf("%s: hiding active GPU chose %s, want the nearest Docker",
						name, state.Tab())
				}
				if state.TabVisible(appsysmanager.TabGPU) ||
					state.TabCollapsed(appsysmanager.TabGPU) {
					t.Fatalf("%s: hidden GPU is visible=%v collapsed=%v",
						name, state.TabVisible(appsysmanager.TabGPU),
						state.TabCollapsed(appsysmanager.TabGPU))
				}
				if tt.HasText(tabLabel(m, appsysmanager.TabGPU)) {
					t.Fatalf("%s: hidden GPU stayed in the strip: %v", name, tt.Texts())
				}
				if _, ok := tt.Find(m.SysManagerMoreTabs()); ok {
					t.Fatalf("%s: hiding GPU left a Collapsed Tabs trigger: %v",
						name, tt.Texts())
				}

				// Showing GPU restores it in its stored position at the end.
				chooseLayout(t, tt, m, name+" show GPU",
					tabLabel(m, appsysmanager.TabGPU), m.SysManagerShowTab())
				tabs := state.Tabs()
				if len(tabs) != 7 || tabs[len(tabs)-1] != appsysmanager.TabGPU {
					t.Fatalf("%s: Show restored the strip as %v, want GPU last", name, tabs)
				}

				// Reset undoes a hidden tab, a collapsed tab, and a move while
				// keeping the refresh interval.
				state.SetInterval(10)
				chooseLayout(t, tt, m, name+" hide for reset",
					tabLabel(m, appsysmanager.TabGPU), m.SysManagerHideTab())
				chooseLayout(t, tt, m, name+" collapse for reset",
					tabLabel(m, appsysmanager.TabPorts), m.SysManagerCollapseTab())
				state.SetTab(appsysmanager.TabDocker)
				tt.Frame()
				tmuxBefore, ok := tt.Find(tabLabel(m, appsysmanager.TabTmux))
				if !ok || tmuxBefore.W <= 0 {
					t.Fatalf("%s: tmux is not rendered before the move: %v", name, tt.Texts())
				}
				dockerBefore, ok := tt.Find(tabLabel(m, appsysmanager.TabDocker))
				if !ok || dockerBefore.W <= 0 || dockerBefore.X <= tmuxBefore.X {
					t.Fatalf("%s: before the move Docker at %.0f is not after tmux at %.0f: %v",
						name, dockerBefore.X, tmuxBefore.X, tt.Texts())
				}
				chooseLayout(t, tt, m, name+" move before reset",
					m.SysManagerMoveTabEarlier())
				wantMoved := []appsysmanager.Tab{
					appsysmanager.TabOverview, appsysmanager.TabProcesses,
					appsysmanager.TabServices, appsysmanager.TabDocker,
					appsysmanager.TabTmux,
				}
				if got := state.Tabs(); !sameTabList(got, wantMoved) {
					t.Fatalf("%s: Move earlier shows %v, want %v", name, got, wantMoved)
				}
				tmuxMoved, ok := tt.Find(tabLabel(m, appsysmanager.TabTmux))
				if !ok || tmuxMoved.W <= 0 {
					t.Fatalf("%s: tmux is not rendered after Move earlier: %v", name, tt.Texts())
				}
				dockerMoved, ok := tt.Find(tabLabel(m, appsysmanager.TabDocker))
				if !ok || dockerMoved.W <= 0 {
					t.Fatalf("%s: Docker is not rendered after Move earlier: %v", name, tt.Texts())
				}
				if dockerMoved.X >= tmuxMoved.X {
					t.Fatalf("%s: Move earlier left Docker at %.0f, not before tmux at %.0f: %v",
						name, dockerMoved.X, tmuxMoved.X, tt.Texts())
				}
				if tmuxMoved.X <= tmuxBefore.X {
					t.Fatalf("%s: Move earlier did not shift tmux right: before %.0f, after %.0f",
						name, tmuxBefore.X, tmuxMoved.X)
				}
				chooseLayout(t, tt, m, name+" move later",
					m.SysManagerMoveTabLater())
				wantLater := []appsysmanager.Tab{
					appsysmanager.TabOverview, appsysmanager.TabProcesses,
					appsysmanager.TabServices, appsysmanager.TabTmux,
					appsysmanager.TabDocker,
				}
				if got := state.Tabs(); !sameTabList(got, wantLater) {
					t.Fatalf("%s: Move later shows %v, want %v", name, got, wantLater)
				}
				tmuxLater, ok := tt.Find(tabLabel(m, appsysmanager.TabTmux))
				if !ok || tmuxLater.W <= 0 {
					t.Fatalf("%s: tmux is not rendered after Move later: %v", name, tt.Texts())
				}
				dockerLater, ok := tt.Find(tabLabel(m, appsysmanager.TabDocker))
				if !ok || dockerLater.W <= 0 || dockerLater.X <= tmuxLater.X {
					t.Fatalf("%s: Move later left Docker at %.0f, not after tmux at %.0f: %v",
						name, dockerLater.X, tmuxLater.X, tt.Texts())
				}
				chooseLayout(t, tt, m, name+" reset", m.SysManagerResetLayout())

				want := []appsysmanager.Tab{
					appsysmanager.TabOverview, appsysmanager.TabProcesses,
					appsysmanager.TabPorts, appsysmanager.TabServices,
					appsysmanager.TabTmux, appsysmanager.TabDocker,
					appsysmanager.TabGPU,
				}
				if got := state.Tabs(); !sameTabList(got, want) {
					t.Fatalf("%s: Reset layout shows %v, want %v", name, got, want)
				}
				if len(state.Collapsed()) != 0 {
					t.Fatalf("%s: Reset layout left collapsed tabs: %v",
						name, state.Collapsed())
				}
				if state.Interval() != 10 {
					t.Fatalf("%s: Reset layout changed the interval to %d", name, state.Interval())
				}
				saved := prefs.Settings().SysManager
				if got := saved.Order(); len(got) != 7 || string(got[0]) != string(domainpreferences.SysManagerOverview) {
					t.Fatalf("%s: Reset layout saved the order %v", name, got)
				}
				if len(saved.HiddenTabs) != 0 || len(saved.CollapsedTabs) != 0 {
					t.Fatalf("%s: Reset layout saved hidden=%v collapsed=%v",
						name, saved.HiddenTabs, saved.CollapsedTabs)
				}
				if _, ok := tt.Find(m.SysManagerMoreTabs()); ok {
					t.Fatalf("%s: Reset layout left a Collapsed Tabs trigger: %v",
						name, tt.Texts())
				}
			}
		})
	}
}
