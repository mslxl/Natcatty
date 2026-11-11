package shell

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	appsysmanager "github.com/mslxl/natcatty/application/sysmanager"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
	uisysmanager "github.com/mslxl/natcatty/ui/sysmanager"
)

const (
	// dockerImagesView is the Docker tab's image sub-view.
	dockerImagesView = 1
	// scrollToEndSteps is enough wheel steps to reach the end of a fixture
	// list; further steps change nothing.
	scrollToEndSteps = 4
)

// The Tabs menu was unreachable from independent acceptance at 24a1a10e: the
// button carried only an ExpandCollapse pattern, a real click and every key
// left the seven TabItems unchanged, and no preferences file was written, so
// a user could not hide or move a tab from the UI. These tests drive the menu
// through the rendered surface: the mouse opens it, the keyboard opens it, an
// item toggles a tab's visibility, Move Left reorders the strip, and both
// changes are read back from the shared preferences store by a second surface
// that models a restart.

// sysManagerTabsMenuItems is the menu a user sees: one submenu per known tab
// in canonical order, then a separator, the two move commands, another
// separator, and Reset layout.
func sysManagerTabsMenuItems(m locales.Messages,
	manager *uisysmanager.State) []string {
	labels := sysManagerTabLabels(m)
	items := make([]string, 0, len(manager.AllTabs())+6)
	for _, tab := range manager.AllTabs() {
		items = append(items, labels[tab])
	}
	items = append(items, "-", m.SysManagerMoveTabEarlier(), m.SysManagerMoveTabLater(),
		"-", m.SysManagerResetLayout())
	return items
}

func openTabsMenuWithMouse(t *testing.T, tester *ui.Tester, label, name string) {
	t.Helper()
	if err := tester.Click(label); err != nil {
		t.Fatalf("%s: click the Tabs menu button %q: %v", name, label, err)
	}
	tester.Frame()
	if tester.Menu() == nil {
		t.Fatalf("%s: clicking the Tabs menu button did not open a menu: %v",
			name, tester.Texts())
	}
}

func TestSysManagerTabsMenuTogglesAndMovesThroughTheUI(t *testing.T) {
	for _, locale := range []string{"en", "zh-CN"} {
		m := locales.In(locale)
		prefs := &sysManagerLayoutPrefs{settings: domainpreferences.Defaults()}
		tester, manager := newSysManagerLayoutTesterWithPrefs(t,
			sysManagerMinimumWidth, sysManagerMinimumHeight, locale,
			appsysmanager.TabOverview, 4, prefs)
		name := locale + " tabs menu"
		menuLabel := m.SysManagerTabsLayout()
		tabLabels := sysManagerTabLabels(m)
		leftEdge, rightEdge := sysManagerContentBounds(sysManagerMinimumWidth)

		// The mouse opens the persisted layout menu, with every tab entry
		// (GPU included) and the two move commands.
		openTabsMenuWithMouse(t, tester, menuLabel, name)
		want := sysManagerTabsMenuItems(m, manager)
		got := tester.Menu()
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Fatalf("%s: the menu shows %q, want %q", name, got, want)
		}
		if len(manager.AllTabs()) < 7 {
			t.Fatalf("%s: the menu has %d tab entries, want at least 7: %q",
				name, len(manager.AllTabs()), got)
		}

		// Collapse Docker through the layout menu: it leaves the strip and
		// appears in the Collapsed Tabs trigger's popover, which choosing it
		// returns to the strip and activates.
		openTabsMenuWithMouse(t, tester, menuLabel, name+" collapse")
		if err := tester.ChooseMenuItem(tabLabels[appsysmanager.TabDocker], m.SysManagerCollapseTab()); err != nil {
			t.Fatalf("%s: choose Docker > Collapse: %v", name, err)
		}
		tester.Frame()
		if !manager.TabCollapsed(appsysmanager.TabDocker) ||
			manager.TabVisible(appsysmanager.TabDocker) {
			t.Fatalf("%s: collapsing Docker left visible=%v collapsed=%v: %v",
				name, manager.TabVisible(appsysmanager.TabDocker),
				manager.TabCollapsed(appsysmanager.TabDocker), manager.Tabs())
		}
		if tester.HasText(tabLabels[appsysmanager.TabDocker]) {
			t.Fatalf("%s: the collapsed Docker tab stayed in the strip: %v",
				name, tester.Texts())
		}
		more, ok := tester.Find(m.SysManagerMoreTabs())
		if !ok {
			t.Fatalf("%s: collapsing Docker drew no Collapsed Tabs trigger: %v",
				name, tester.Texts())
		}
		if more.X < leftEdge || more.X+more.W > rightEdge {
			t.Fatalf("%s: the Collapsed Tabs trigger crosses the content column %.0f..%.0f: %+v",
				name, leftEdge, rightEdge, more)
		}
		if err := tester.Click(m.SysManagerMoreTabs()); err != nil {
			t.Fatalf("%s: open the Collapsed Tabs menu: %v", name, err)
		}
		tester.Frame()
		if !tester.HasText(m.SysManagerCollapsedTabs()) {
			t.Fatalf("%s: the Collapsed Tabs menu lacks its title: %v", name, tester.Texts())
		}
		if err := tester.Click(tabLabels[appsysmanager.TabDocker]); err != nil {
			t.Fatalf("%s: choose the collapsed Docker tab: %v", name, err)
		}
		tester.Frame()
		if manager.Tab() != appsysmanager.TabDocker ||
			!manager.TabVisible(appsysmanager.TabDocker) ||
			manager.TabCollapsed(appsysmanager.TabDocker) {
			t.Fatalf("%s: choosing collapsed Docker left tab=%s visible=%v collapsed=%v",
				name, manager.Tab(), manager.TabVisible(appsysmanager.TabDocker),
				manager.TabCollapsed(appsysmanager.TabDocker))
		}
		if _, ok := tester.Find(m.SysManagerMoreTabs()); ok {
			t.Fatalf("%s: the trigger stayed after the Collapsed Tabs menu emptied: %v",
				name, tester.Texts())
		}
		// Collapse it again so the restart below reads the persisted menu.
		openTabsMenuWithMouse(t, tester, menuLabel, name+" collapse again")
		if err := tester.ChooseMenuItem(tabLabels[appsysmanager.TabDocker], m.SysManagerCollapseTab()); err != nil {
			t.Fatalf("%s: choose Docker > Collapse again: %v", name, err)
		}
		tester.Frame()
		if !manager.TabCollapsed(appsysmanager.TabDocker) {
			t.Fatalf("%s: the second collapse was not saved: %v", name, manager.Collapsed())
		}

		// Choosing Hide on the GPU submenu hides the tab through the UI and
		// saves it.
		openTabsMenuWithMouse(t, tester, menuLabel, name+" hide")
		if err := tester.ChooseMenuItem(tabLabels[appsysmanager.TabGPU], m.SysManagerHideTab()); err != nil {
			t.Fatalf("%s: choose GPU > Hide: %v", name, err)
		}
		tester.Frame()
		if manager.TabVisible(appsysmanager.TabGPU) {
			t.Fatalf("%s: choosing GPU left it visible: %v", name, manager.Tabs())
		}
		if tester.HasText(tabLabels[appsysmanager.TabGPU]) {
			t.Fatalf("%s: the hidden GPU tab is still in the strip: %v",
				name, tester.Texts())
		}
		saved := prefs.Settings().SysManager
		if saved.Visible(domainpreferences.SysManagerTab(appsysmanager.TabGPU)) {
			t.Fatalf("%s: the hidden tab was not saved: %+v", name, saved)
		}

		// The keyboard opens the same menu while the button has the focus.
		// A mouse click focuses it; closing the menu keeps the focus.
		openTabsMenuWithMouse(t, tester, menuLabel, name+" keyboard focus")
		tester.CloseMenu()
		for _, key := range []ui.Key{ui.KeyEnter, ui.KeySpace, ui.KeyDown} {
			tester.Key(0, key)
			if tester.Menu() == nil {
				t.Fatalf("%s: %v did not open the focused Tabs menu", name, key)
			}
			tester.CloseMenu()
		}
		tester.Key(ui.Alt, ui.KeyDown)
		if tester.Menu() == nil {
			t.Fatalf("%s: Alt+Down did not open the focused Tabs menu", name)
		}
		tester.CloseMenu()

		// Move the active tmux tab one place left through the menu.
		manager.SetTab(appsysmanager.TabTmux)
		tester.Frame()
		tmuxBefore, ok := tester.Find(tabLabels[appsysmanager.TabTmux])
		if !ok || tmuxBefore.W <= 0 {
			t.Fatalf("%s: tmux is not rendered before Move Earlier: %v",
				name, tester.Texts())
		}
		before := persistedTabOrder(prefs)
		openTabsMenuWithMouse(t, tester, menuLabel, name+" move")
		if err := tester.ChooseMenuItem(m.SysManagerMoveTabEarlier()); err != nil {
			t.Fatalf("%s: choose Move Earlier: %v", name, err)
		}
		tester.Frame()
		after := persistedTabOrder(prefs)
		if !movedLeft(before, after, string(domainpreferences.SysManagerTmux)) {
			t.Fatalf("%s: Move Earlier moved %q to %q", name, before, after)
		}
		if got := slices.Index(manager.Tabs(), appsysmanager.TabTmux); got != 3 {
			t.Fatalf("%s: after Move Earlier tmux sits at %d, want 3: %v",
				name, got, manager.Tabs())
		}
		tmuxAfter, ok := tester.Find(tabLabels[appsysmanager.TabTmux])
		if !ok || tmuxAfter.W <= 0 || tmuxAfter.X >= tmuxBefore.X {
			t.Fatalf("%s: Move Earlier left tmux at x%.0f, want it left of x%.0f: %v",
				name, tmuxAfter.X, tmuxBefore.X, tester.Texts())
		}

		// A second surface over the same preferences is the restart: the
		// hidden tab and the order survive.
		restarted, restartedManager := newSysManagerLayoutTesterWithPrefs(t,
			sysManagerMinimumWidth, sysManagerMinimumHeight, locale,
			appsysmanager.TabOverview, 4, prefs)
		name += " restarted"
		if restartedManager.TabVisible(appsysmanager.TabGPU) {
			t.Fatalf("%s: the hidden GPU tab came back", name)
		}
		if restarted.HasText(tabLabels[appsysmanager.TabGPU]) {
			t.Fatalf("%s: the restarted strip shows the hidden GPU tab: %v",
				name, restarted.Texts())
		}
		if !restartedManager.TabCollapsed(appsysmanager.TabDocker) ||
			restartedManager.TabVisible(appsysmanager.TabDocker) {
			t.Fatalf("%s: the restart lost the collapsed Docker tab: visible=%v collapsed=%v",
				name, restartedManager.TabVisible(appsysmanager.TabDocker),
				restartedManager.TabCollapsed(appsysmanager.TabDocker))
		}
		if restarted.HasText(tabLabels[appsysmanager.TabDocker]) {
			t.Fatalf("%s: the restarted strip shows the collapsed Docker tab: %v",
				name, restarted.Texts())
		}
		if _, ok := restarted.Find(m.SysManagerMoreTabs()); !ok {
			t.Fatalf("%s: the restarted surface lost the Collapsed Tabs trigger: %v",
				name, restarted.Texts())
		}
		if got := slices.Index(restartedManager.Tabs(), appsysmanager.TabTmux); got != 3 {
			t.Fatalf("%s: the restored order puts tmux at %d, want 3: %v",
				name, got, restartedManager.Tabs())
		}
		// The menu still opens on the restored surface.
		openTabsMenuWithMouse(t, restarted, menuLabel, name+" menu")
		if !slices.Contains(restarted.Menu(), tabLabels[appsysmanager.TabGPU]) {
			t.Fatalf("%s: the restored menu lost the GPU entry: %q",
				name, restarted.Menu())
		}
		restarted.CloseMenu()
	}
}

// TestSysManagerTabsMenuOpensAtTheMinimumWindow proves the menu button itself
// stays inside the content column and opens there; the native menu is
// positioned by the platform below it, so the GUI acceptance records the
// rendered menu's bounds.
func TestSysManagerTabsMenuOpensAtTheMinimumWindow(t *testing.T) {
	left, right := sysManagerContentBounds(sysManagerMinimumWidth)
	for _, locale := range []string{"en", "zh-CN"} {
		m := locales.In(locale)
		tester, _ := newSysManagerLayoutTester(t, sysManagerMinimumWidth,
			sysManagerMinimumHeight, locale, appsysmanager.TabOverview, 4)
		name := locale + " tabs menu"
		label := m.SysManagerTabsLayout()
		rect, ok := tester.Find(label)
		if !ok || rect.W <= 0 {
			t.Fatalf("%s: the Tabs menu button is not rendered: %v", name, tester.Texts())
		}
		if rect.X < left-0.5 || rect.X+rect.W > right+0.5 {
			t.Fatalf("%s: the Tabs button crosses the content column %.0f..%.0f: %+v",
				name, left, right, rect)
		}
		openTabsMenuWithMouse(t, tester, label, name)
		if got := len(tester.Menu()); got < 12 {
			t.Fatalf("%s: the minimum-window menu has %d entries, want seven tab submenus, two separators, two moves, and Reset layout: %q",
				name, got, tester.Menu())
		}
		t.Logf("%s: Tabs button at %.0f,%.0f %.0fx%.0f; menu has %d entries",
			name, rect.X, rect.Y, rect.W, rect.H, len(tester.Menu()))
		tester.CloseMenu()
	}
}

// TestSysManagerExtendedLastRowsClearTheActionBar renders the tmux, Docker
// container, and Docker image lists in the real shell at the 704x421 client of
// the enforced minimum window. Independent acceptance at 24a1a10e measured the
// tmux and Docker last rows ending at y457 while the pinned action bar started
// at y443: 14 DIPs under the bar, its buttons drawn over the row, and the row's
// center unable to select it. Every sub-list now reserves the bar and the gap
// above it, so the last row scrolls fully clear and its center selects it.
func TestSysManagerExtendedLastRowsClearTheActionBar(t *testing.T) {
	type extendedCase struct {
		tab    appsysmanager.Tab
		view   int
		hint   func(locales.Messages) string
		labels func(rows int) []string
	}
	cases := []extendedCase{
		{appsysmanager.TabTmux, 0,
			func(m locales.Messages) string { return m.SysManagerSessionHint() },
			func(rows int) []string { return numberedLabels("session-%02d", "", rows) }},
		{appsysmanager.TabDocker, 0,
			func(m locales.Messages) string { return m.SysManagerContainerHint() },
			func(rows int) []string { return numberedLabels("container-%02d", "", rows) }},
		{appsysmanager.TabDocker, dockerImagesView,
			func(m locales.Messages) string { return m.SysManagerImageHint() },
			func(rows int) []string { return numberedLabels("image-%02d", ":latest", rows) }},
	}
	for _, locale := range []string{"en", "zh-CN"} {
		m := locales.In(locale)
		for _, test := range cases {
			for _, rows := range []int{4, 8} {
				name := fmt.Sprintf("%s %s view=%d rows=%d", locale, test.tab, test.view, rows)
				tester, manager := newSysManagerLayoutTester(t,
					sysManagerMinimumWidth, sysManagerMinimumHeight, locale, test.tab, rows)
				if test.view == dockerImagesView {
					manager.SetDockerView(dockerImagesView)
					tester.Frame()
				}
				actionBarTop(t, tester, test.hint(m), name)
				// Scroll the list to its end: the tmux tab has a New session
				// row above the table, so even four rows overflow its
				// viewport. Wheel over the list body, below the toolbar and
				// above the pinned bar.
				for range scrollToEndSteps {
					tester.Scroll(sysManagerMinimumWidth/2+80, 300, 0, 4000)
					tester.Frame()
				}
				barTop := actionBarTop(t, tester, test.hint(m), name+" scrolled")
				labels := test.labels(rows)
				rendered := renderedSysManagerRows(t, tester, labels)
				label := labels[len(labels)-1]
				var rect ui.Rect
				for _, row := range rendered {
					if row.label == label {
						rect = row.rect
					}
				}
				if rect.W <= 0 || rect.H <= 0 {
					t.Fatalf("%s: the last row %q is not shown at the end of the scroll: %v",
						name, label, tester.Texts())
				}
				// The fixture's last row must be the bottom row the user
				// sees, not merely the last name in the fixture: a table that
				// reorders its rows would otherwise let the real bottom row
				// stay clipped while this test measured a row above it.
				if bottom := rendered[len(rendered)-1]; bottom.label != label {
					t.Fatalf("%s: the bottom row is %q, not the fixture's last %q: %v",
						name, bottom.label, label, rendered)
				}
				// A clipped row is not fully above the bar however high its
				// top is: compare its height with the row above it, which the
				// end of the scroll leaves whole.
				rowHeight := rect.H
				if len(rendered) > 1 {
					if previous := rendered[len(rendered)-2]; previous.rect.H > 0 {
						rowHeight = previous.rect.H
					}
				}
				if rect.H < rowHeight-0.5 {
					t.Fatalf("%s: the last row %q is clipped to %.0f of %.0f DIPs: %+v",
						name, label, rect.H, rowHeight, rect)
				}
				if bottom := rect.Y + rect.H; bottom > barTop+0.5 {
					t.Fatalf("%s: the last row %q ends at %.0f, under the pinned action bar at %.0f: %+v",
						name, label, bottom, barTop, rect)
				}
				if center := rect.Y + rect.H/2; center > barTop+0.5 {
					t.Fatalf("%s: the last row %q has its center at %.0f, under the pinned action bar at %.0f: %+v",
						name, label, center, barTop, rect)
				}
				t.Logf("%s: last row %q at %.0f,%.0f %.0fx%.0f (bar top %.0f)",
					name, label, rect.X, rect.Y, rect.W, rect.H, barTop)

				if err := tester.Click(label); err != nil {
					t.Fatalf("%s: click the last row %q: %v", name, label, err)
				}
				tester.Frame()
				selected := extendedSelectedLabel(m, manager, test.tab, test.view)
				if selected != label {
					t.Fatalf("%s: clicking %q selected %q: %v",
						name, label, selected, tester.Texts())
				}
				bar := selectedExtendedBarLabel(t, m, manager, test.tab, test.view)
				barTop = actionBarTop(t, tester, bar, name+" selected")
				if bottom := rect.Y + rect.H; bottom > barTop+0.5 {
					t.Fatalf("%s: the selected last row %q ends at %.0f, under the action bar at %.0f",
						name, label, bottom, barTop)
				}
			}
		}
	}
}

// numberedLabels names a fixture's rows in fixture order.
func numberedLabels(format, suffix string, rows int) []string {
	labels := make([]string, rows)
	for index := range labels {
		labels[index] = fmt.Sprintf(format, index) + suffix
	}
	return labels
}

// renderedSysManagerRows returns the named rows the tester shows, top to
// bottom. Rows scrolled out of view are skipped; the caller checks that the
// row it wants is present and bottommost.
func renderedSysManagerRows(t *testing.T, tester *ui.Tester, labels []string) []sysManagerRow {
	t.Helper()
	rows := make([]sysManagerRow, 0, len(labels))
	for _, label := range labels {
		rect, ok := tester.Find(label)
		if !ok || rect.W <= 0 || rect.H <= 0 {
			continue
		}
		rows = append(rows, sysManagerRow{label: label, rect: rect})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].rect.Y < rows[j].rect.Y })
	return rows
}

func extendedSelectedLabel(m locales.Messages, manager *uisysmanager.State,
	tab appsysmanager.Tab, view int) string {
	switch tab {
	case appsysmanager.TabTmux:
		if session, ok := manager.SelectedSession(); ok {
			return session.Name
		}
	case appsysmanager.TabDocker:
		if view == dockerImagesView {
			if image, ok := manager.SelectedImage(); ok {
				return image.Name()
			}
			return ""
		}
		if container, ok := manager.SelectedContainer(); ok {
			if name := strings.TrimSpace(container.Name); name != "" {
				return name
			}
			return container.ID
		}
	}
	return ""
}

func selectedExtendedBarLabel(t *testing.T, m locales.Messages, manager *uisysmanager.State,
	tab appsysmanager.Tab, view int) string {
	t.Helper()
	switch tab {
	case appsysmanager.TabTmux:
		if session, ok := manager.SelectedSession(); ok {
			return m.SysManagerSelectedSession(session.Name, session.Windows)
		}
	case appsysmanager.TabDocker:
		if view == dockerImagesView {
			if image, ok := manager.SelectedImage(); ok {
				size := image.Size
				if strings.TrimSpace(size) == "" {
					size = m.SysManagerUnknown()
				}
				return m.SysManagerSelectedImage(image.Name(), size)
			}
		} else if container, ok := manager.SelectedContainer(); ok {
			name := strings.TrimSpace(container.Name)
			if name == "" {
				name = container.ID
			}
			return m.SysManagerSelectedContainer(name, container.Image)
		}
	}
	t.Fatalf("%s view=%d has no selected target", tab, view)
	return ""
}

// TestSysManagerGPUAndHeaderFitTheContentAtMinimumWindow re-verifies the two
// ticket-45 layout fixes at the 704x421 client: the active GPU tab is whole
// inside the content column, and the updated-at and latency status texts do
// not run past it.
func TestSysManagerGPUAndHeaderFitTheContentAtMinimumWindow(t *testing.T) {
	left, right := sysManagerContentBounds(sysManagerMinimumWidth)
	for _, locale := range []string{"en", "zh-CN"} {
		m := locales.In(locale)
		tester, manager := newSysManagerLayoutTester(t, sysManagerMinimumWidth,
			sysManagerMinimumHeight, locale, appsysmanager.TabOverview, 4)
		manager.SetTab(appsysmanager.TabGPU)
		tester.Frame()
		name := locale + " GPU"
		tab, ok := tester.Find(sysManagerTabLabels(m)[appsysmanager.TabGPU])
		if !ok || tab.W <= 0 {
			t.Fatalf("%s: the active GPU tab is not rendered: %v", name, tester.Texts())
		}
		if tab.X < left-0.5 || tab.X+tab.W > right+0.5 {
			t.Fatalf("%s: the active GPU tab is outside the content column %.0f..%.0f: %+v",
				name, left, right, tab)
		}
		nodes := []struct {
			what string
			text string
		}{
			{"updated-at", m.SysManagerUpdatedSeconds(3600)},
			{"latency", m.SysManagerLatency() + " " + m.SysManagerLatencyValue(12)},
			{"GPU card", m.SysManagerGPUDevice(0, "NVIDIA RTX 4090")},
			{"GPU driver", m.SysManagerGPUDriver() + " 555.85"},
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
			t.Logf("%s: %s at %.0f,%.0f %.0fx%.0f", name, node.what,
				rect.X, rect.Y, rect.W, rect.H)
		}
	}
}

// TestSysManagerCollapsedTabsTriggerFollowsVisibleTabsInBothStyles proves the
// trailing controls never cover a visible tab: at the minimum client and at a
// wide window, in both Interface Styles, the Collapsed Tabs trigger starts at
// or after the right edge of the last visible tab and after the layout button.
func TestSysManagerCollapsedTabsTriggerFollowsVisibleTabsInBothStyles(t *testing.T) {
	for _, style := range []struct {
		name  string
		style kit.Style
	}{{"classic", kit.Classic}, {"macos", kit.MacOS}} {
		t.Run(style.name, func(t *testing.T) {
			kit.SetStyle(style.style)
			t.Cleanup(func() { kit.SetStyle(nil) })
			for _, width := range []int{sysManagerMinimumWidth, 1100} {
				tester, manager := newSysManagerLayoutTester(t, width,
					sysManagerMinimumHeight, "en", appsysmanager.TabOverview, 4)
				m := locales.In("en")
				labels := sysManagerTabLabels(m)
				if err := manager.SetTabCollapsed(appsysmanager.TabTmux, true); err != nil {
					t.Fatalf("%s at %d: collapse tmux: %v", style.name, width, err)
				}
				if err := manager.SetTabCollapsed(appsysmanager.TabGPU, true); err != nil {
					t.Fatalf("%s at %d: collapse GPU: %v", style.name, width, err)
				}
				tester.Frame()
				name := fmt.Sprintf("%s at %d", style.name, width)
				trigger, ok := tester.Find(m.SysManagerMoreTabs())
				if !ok || trigger.W <= 0 {
					t.Fatalf("%s: the Collapsed Tabs trigger is missing: %v",
						name, tester.Texts())
				}
				layout, ok := tester.Find(m.SysManagerTabsLayout())
				if !ok || layout.W <= 0 {
					t.Fatalf("%s: the layout button is missing: %v", name, tester.Texts())
				}
				lastRight := float32(0)
				for _, tab := range manager.Tabs() {
					rect, ok := tester.Find(labels[tab])
					if !ok || rect.W <= 0 {
						t.Fatalf("%s: visible tab %s is missing: %v", name, tab, tester.Texts())
					}
					if right := rect.X + rect.W; right > lastRight {
						lastRight = right
					}
				}
				if trigger.X < lastRight-0.5 {
					t.Fatalf("%s: the Collapsed Tabs trigger at x%.0f starts before the last visible tab ending at x%.0f: %v",
						name, trigger.X, lastRight, tester.Texts())
				}
				if trigger.X < layout.X+layout.W-0.5 {
					t.Fatalf("%s: the Collapsed Tabs trigger at x%.0f does not follow the layout button ending at x%.0f",
						name, trigger.X, layout.X+layout.W)
				}
				left, right := sysManagerContentBounds(width)
				if trigger.X < left-0.5 || trigger.X+trigger.W > right+0.5 {
					t.Fatalf("%s: the Collapsed Tabs trigger crosses the content column %.0f..%.0f: %+v",
						name, left, right, trigger)
				}
			}
		})
	}
}

// TestSysManagerStripEntriesOpenTmuxAndDockerViews proves the tmux and Docker
// strip entries are more than labels: clicking each one activates that tab and
// renders its existing view with live fixture content. The overview's System
// card is not the destination here; the tmux sessions and Docker containers
// are the observable content of their own tabs.
func TestSysManagerStripEntriesOpenTmuxAndDockerViews(t *testing.T) {
	for _, style := range []struct {
		name  string
		style kit.Style
	}{{"classic", kit.Classic}, {"macos", kit.MacOS}} {
		t.Run(style.name, func(t *testing.T) {
			kit.SetStyle(style.style)
			t.Cleanup(func() { kit.SetStyle(nil) })
			tester, manager := newSysManagerLayoutTester(t, 1100, 600, "en",
				appsysmanager.TabOverview, 4)
			m := locales.In("en")
			labels := sysManagerTabLabels(m)
			cases := []struct {
				tab     appsysmanager.Tab
				hint    string
				content string
			}{
				{appsysmanager.TabTmux, m.SysManagerSessionHint(), "session-00"},
				{appsysmanager.TabDocker, m.SysManagerContainerHint(), "container-00"},
			}
			for _, test := range cases {
				name := style.name + " " + string(test.tab)
				if err := tester.Click(labels[test.tab]); err != nil {
					t.Fatalf("%s: click the %s strip entry: %v", name, test.tab, err)
				}
				tester.Frame()
				if manager.Tab() != test.tab {
					t.Fatalf("%s: clicking the strip entry chose tab %s: %v",
						name, manager.Tab(), tester.Texts())
				}
				if !tester.HasText(test.hint) {
					t.Fatalf("%s: the %s view's hint %q is not rendered: %v",
						name, test.tab, test.hint, tester.Texts())
				}
				if !tester.HasText(test.content) {
					t.Fatalf("%s: the %s view's fixture content %q is not rendered: %v",
						name, test.tab, test.content, tester.Texts())
				}
			}
		})
	}
}

func persistedTabOrder(prefs *sysManagerLayoutPrefs) []string {
	order := prefs.Settings().SysManager.TabOrder
	return append([]string(nil), order...)
}

// movedLeft reports whether want moved one place earlier in the persisted
// order, every other tab keeping its relative place.
func movedLeft(before, after []string, want string) bool {
	from := slices.Index(before, want)
	to := slices.Index(after, want)
	if from <= 0 || to != from-1 {
		return false
	}
	drop := func(value string) bool { return value == want }
	return slices.Equal(
		slices.DeleteFunc(slices.Clone(before), drop),
		slices.DeleteFunc(slices.Clone(after), drop))
}
