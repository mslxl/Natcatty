package app

import (
	"context"
	"strings"
	"testing"

	appdesktop "github.com/mslxl/natcatty/application/desktop"
	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
	"github.com/mslxl/natcatty/ui/locales"
)

// newDesktopQuakeController wires the desktop controller over a graph that has
// both the main Workspace and the Quake Workspace, mirroring the composition
// root's tray, notification, and quit-count integration.
func newDesktopQuakeController(t *testing.T) (*desktopController, *quakeGraph, *fakeTrayBackend, *testNotifier) {
	t.Helper()
	q := newQuakeGraph(t)
	q.manager.OpenMain()
	repository := &testDesktopRepository{settings: appdesktop.Defaults("windows")}
	hotkeys := &testHotkeys{}
	notifier := &testNotifier{}
	service := appdesktop.NewService(repository, hotkeys,
		&testAutoLauncher{supported: true}, notifier, appdesktop.WithGOOS("windows"))
	if err := service.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	backend := &fakeTrayBackend{}
	controller := newDesktopController(q.manager, q.state, q.quake, nil, nil, service,
		backend, func() string { return "en" }, func() {})
	controller.opener = &recordingOpener{}
	controller.MarkReady()
	t.Cleanup(func() { controller.Shutdown() })
	return controller, q, backend, notifier
}

// TestShowMainHidesTheOpenDropdownAndRaisesTheMainWindow pins the tray and
// deep-link route: the shared "show the main window" action retires the
// Dropdown Panel first, so the two windows never present the Workspace at once.
func TestShowMainHidesTheOpenDropdownAndRaisesTheMainWindow(t *testing.T) {
	_, q, backend, _ := newDesktopQuakeController(t)
	q.manager.ShowDropdown()
	panel := dropdownWindow(t, q.windowGraph)
	if !q.manager.DropdownVisible() || panel.destroyed {
		t.Fatal("the test did not open a live Dropdown Panel")
	}

	// The tray's Open Main item is the route the ticket names; it must hide the
	// Panel before raising the main window.
	clickTrayItem(t, backend.surfaces[0].menu, locales.In("en").TrayOpenMain())

	if q.manager.DropdownVisible() {
		t.Fatal("ShowMain left the Dropdown Panel visible")
	}
	if panel.destroyed {
		t.Fatal("ShowMain destroyed the Panel instead of hiding it for reuse")
	}
	main := q.manager.Main()
	if !main.IsVisible() || !main.IsFocused() {
		t.Fatalf("ShowMain did not raise the main window: visible=%v focused=%v", main.IsVisible(), main.IsFocused())
	}
}

// TestTrayListsQuakeTabsWithSourceAndSelectsThem proves the Session menu
// contains both Workspaces, marks the Quake rows, and that choosing one shows
// the Dropdown Panel with that Tab active.
func TestTrayListsQuakeTabsWithSourceAndSelectsThem(t *testing.T) {
	controller, q, backend, _ := newDesktopQuakeController(t)
	if err := q.quake.Activate(); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if err := q.quake.AddHostTab(quakeTestHosts()[0]); err != nil {
		t.Fatalf("AddHostTab: %v", err)
	}
	// Leave the first Tab active so the click has to move the selection.
	q.quake.SelectTab(0)
	if err := q.state.NewTab(); err != nil {
		t.Fatalf("main NewTab: %v", err)
	}
	waitForSurface(t, q.state, 0)
	controller.SyncNow()

	menu := backend.surfaces[0].menu
	m := locales.In("en")
	labels := strings.Join(trayMenuLabels(menu), "\n")
	marker := "[" + m.TrayQuakeSource() + "]"
	if !strings.Contains(labels, marker) {
		t.Fatalf("tray Sessions lack the Quake source marker:\n%s", labels)
	}
	for _, want := range []string{m.LocalSessionTab(1), "Alpha"} {
		if !strings.Contains(labels, want) {
			t.Fatalf("tray Sessions lack %q:\n%s", want, labels)
		}
	}
	if strings.Contains(labels, m.TrayNoSessions()) {
		t.Fatalf("tray says there are no Sessions while both Workspaces have Tabs:\n%s", labels)
	}

	clickTrayItemPrefix(t, menu, marker+" Alpha")
	if q.quake.ActiveIndex() != 1 {
		t.Fatalf("the Quake tray row selected Tab %d, want 1", q.quake.ActiveIndex())
	}
	if !q.manager.DropdownVisible() {
		t.Fatal("the Quake tray row did not show the Dropdown Panel")
	}
	if q.manager.Main().IsFocused() {
		t.Fatal("the Quake tray row raised the main window instead of the Panel")
	}
}

// TestNotificationsResolveBothWorkspacesAndRouteClicks covers the label and
// click path: a Quake Session is named the way its Tab strip names it, a main
// Session keeps its shell label, and clicking either notification opens the
// owning window and selects the Tab.
func TestNotificationsResolveBothWorkspacesAndRouteClicks(t *testing.T) {
	controller, q, _, notifier := newDesktopQuakeController(t)
	if err := q.state.NewTab(); err != nil {
		t.Fatalf("main NewTab: %v", err)
	}
	waitForSurface(t, q.state, 0)
	if err := q.state.NewTab(); err != nil {
		t.Fatalf("second main NewTab: %v", err)
	}
	waitForSurface(t, q.state, 1)
	mainSnapshot, ok := q.state.TabSnapshot(1)
	if !ok {
		t.Fatal("the second main Tab has no Session snapshot")
	}
	q.state.SelectTab(0)
	mainLabel, ok := q.state.SessionLabel(mainSnapshot.ID)
	if !ok {
		t.Fatalf("the shell cannot name its own Session %q", mainSnapshot.ID)
	}

	if err := q.quake.Activate(); err != nil {
		t.Fatalf("quake Activate: %v", err)
	}
	firstQuakeTab, _ := q.quake.TabAt(0)
	quakeID := firstQuakeTab.Session.ID()
	if err := q.quake.AddLocalTab(); err != nil {
		t.Fatalf("quake AddLocalTab: %v", err)
	}
	if q.quake.ActiveIndex() != 1 {
		t.Fatalf("quake active = %d, want the second Tab", q.quake.ActiveIndex())
	}

	emitted := make(map[string]appdesktop.Notification)
	for _, id := range []model.SessionID{mainSnapshot.ID, quakeID} {
		controller.NoteSession(appsession.Snapshot{ID: id, Status: appsession.StatusRunning, Connection: appsession.ConnectionStatusActive})
		controller.NoteSession(appsession.Snapshot{ID: id, Status: appsession.StatusExited, Connection: appsession.ConnectionStatusActive})
	}
	if len(notifier.notifications) != 2 {
		t.Fatalf("notifications = %+v, want one per Workspace", notifier.notifications)
	}
	for _, notification := range notifier.notifications {
		if notification.Event != appdesktop.EventSessionEnded {
			t.Fatalf("notification = %+v, want a Session-ended event", notification)
		}
		emitted[notification.Session] = notification
	}
	if got := emitted[string(mainSnapshot.ID)].Target; got != mainLabel {
		t.Fatalf("main notification target = %q, want %q", got, mainLabel)
	}
	m := locales.In("en")
	if got := emitted[string(quakeID)].Target; got != m.LocalSessionTab(1) {
		t.Fatalf("Quake notification target = %q, want %q", got, m.LocalSessionTab(1))
	}

	// A Quake notification opens the Panel and selects the owning Tab.
	controller.HandleNotificationClick(emitted[string(quakeID)])
	if !q.manager.DropdownVisible() {
		t.Fatal("a Quake notification click did not show the Dropdown Panel")
	}
	if q.quake.ActiveIndex() != 0 {
		t.Fatalf("a Quake notification click selected Tab %d, want 0", q.quake.ActiveIndex())
	}

	// A main notification hides the Panel again, raises the main window, and
	// selects the Tab that owns the Session.
	controller.HandleNotificationClick(emitted[string(mainSnapshot.ID)])
	if q.manager.DropdownVisible() {
		t.Fatal("a main-window notification click left the Dropdown Panel open")
	}
	if !q.manager.Main().IsVisible() || !q.manager.Main().IsFocused() {
		t.Fatal("a main-window notification click did not raise the main window")
	}
	if q.state.ActiveTab() != 1 {
		t.Fatalf("main notification click selected Tab %d, want 1", q.state.ActiveTab())
	}
}

// TestDeepLinkAndHandoverRaiseMainAndHideTheDropdown covers the startup
// routes: a deep link and a second-instance handover both put the main window
// in front and never leave the Panel floating over it.
func TestDeepLinkAndHandoverRaiseMainAndHideTheDropdown(t *testing.T) {
	controller, q, _, _ := newDesktopQuakeController(t)
	opener := controller.opener.(*recordingOpener)

	q.manager.ShowDropdown()
	controller.HandleDeepLink("ssh://alice@example.com:2222")
	if q.manager.DropdownVisible() {
		t.Fatal("a deep link left the Dropdown Panel visible")
	}
	if len(opener.targets) != 1 || opener.targets[0].Hostname != "example.com" {
		t.Fatalf("deep link targets = %+v", opener.targets)
	}
	if !q.manager.Main().IsFocused() {
		t.Fatal("a deep link did not focus the main window")
	}

	q.manager.ShowDropdown()
	controller.HandleLaunchArgs([]string{"box.example:2200"})
	if q.manager.DropdownVisible() {
		t.Fatal("a command-line launch target left the Dropdown Panel visible")
	}
	if last := opener.targets[len(opener.targets)-1]; last.Hostname != "box.example" || last.Port != 2200 {
		t.Fatalf("command-line target = %+v", last)
	}

	q.manager.ShowDropdown()
	if raised := q.manager.SecondInstance(); raised != q.manager.Main() {
		t.Fatal("SecondInstance did not return the main window")
	}
	if q.manager.DropdownVisible() {
		t.Fatal("a second-instance handover left the Dropdown Panel visible")
	}
	if !q.manager.Main().IsFocused() {
		t.Fatal("a second-instance handover did not focus the main window")
	}
}

// TestLoginLaunchHidesMainWithoutShowingTheDropdown proves a quiet login start
// never creates or displays the Panel, while an ordinary launch leaves the
// main window visible.
func TestLoginLaunchHidesMainWithoutShowingTheDropdown(t *testing.T) {
	controller, q, _, _ := newDesktopQuakeController(t)
	controller.ShowMain()
	if len(q.host.windows) != 1 {
		t.Fatalf("windows = %d, want only the main window before login handling", len(q.host.windows))
	}

	controller.HandleLoginLaunch(true)
	if q.manager.Main().IsVisible() {
		t.Fatal("a login launch left the main window visible")
	}
	if len(q.host.windows) != 1 || q.manager.dropdown != nil {
		t.Fatal("a login launch created a Dropdown Panel")
	}

	// A Panel opened by a queued launch target is hidden as well.
	q.manager.ShowDropdown()
	controller.HandleLoginLaunch(true)
	if q.manager.DropdownVisible() {
		t.Fatal("a login launch left a Dropdown Panel visible")
	}

	// An ordinary launch is not quiet: the main window stays visible.
	q.manager.Main().Show()
	controller.HandleLoginLaunch(false)
	if !q.manager.Main().IsVisible() {
		t.Fatal("a non-login launch hid the main window")
	}
}
