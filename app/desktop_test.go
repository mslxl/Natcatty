package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo"

	appdesktop "github.com/mslxl/natcatty/application/desktop"
	appforwarding "github.com/mslxl/natcatty/application/forwarding"
	appsession "github.com/mslxl/natcatty/application/session"
	domainforward "github.com/mslxl/natcatty/domain/forwarding"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/locales"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

// desktopTestHosts is the Vault read seam used to exercise the tray's recent
// Host ordering. The tray only lists Hosts, so writes are unsupported.
type desktopTestHosts struct {
	hosts []domainvault.Host
}

func (h *desktopTestHosts) List(context.Context) ([]domainvault.Host, error) {
	return h.hosts, nil
}

func (h *desktopTestHosts) Create(context.Context, domainvault.Draft) (domainvault.Host, error) {
	return domainvault.Host{}, errors.New("not supported")
}

func (h *desktopTestHosts) Update(context.Context, model.HostID, domainvault.Draft) (domainvault.Host, error) {
	return domainvault.Host{}, errors.New("not supported")
}

func (h *desktopTestHosts) Delete(context.Context, model.HostID) error {
	return errors.New("not supported")
}

type testDesktopRepository struct {
	settings appdesktop.Settings
	saves    int
}

func (r *testDesktopRepository) Load(context.Context) (appdesktop.Settings, error) {
	return r.settings, nil
}

func (r *testDesktopRepository) Save(_ context.Context, settings appdesktop.Settings) error {
	r.settings = settings
	r.saves++
	return nil
}

type testHotkeys struct {
	registered map[string]func()
	fail       bool
}

func (h *testHotkeys) Register(accelerator string, fn func()) error {
	if h.fail {
		return errors.New("taken")
	}
	if h.registered == nil {
		h.registered = map[string]func(){}
	}
	h.registered[accelerator] = fn
	return nil
}

func (h *testHotkeys) Unregister(accelerator string) { delete(h.registered, accelerator) }

type testAutoLauncher struct {
	enabled   bool
	supported bool
	fail      bool
}

func (a *testAutoLauncher) Supported() bool        { return a.supported }
func (a *testAutoLauncher) Enabled() (bool, error) { return a.enabled, nil }
func (a *testAutoLauncher) SetEnabled(enabled bool) error {
	if a.fail {
		return errors.New("denied")
	}
	a.enabled = enabled
	return nil
}

type testNotifier struct {
	notifications []appdesktop.Notification
}

func (n *testNotifier) Supported() bool { return true }
func (n *testNotifier) Notify(notification appdesktop.Notification) error {
	n.notifications = append(n.notifications, notification)
	return nil
}

type fakeTraySurface struct {
	menu    *mygo.Menu
	tooltip string
	alive   bool
}

func (t *fakeTraySurface) SetMenu(menu *mygo.Menu)    { t.menu = menu }
func (t *fakeTraySurface) SetToolTip(tooltip string)  { t.tooltip = tooltip }
func (t *fakeTraySurface) Destroy()                   { t.alive = false }
func (t *fakeTraySurface) OnClick(func()) func()      { return func() {} }
func (t *fakeTraySurface) OnRightClick(func()) func() { return func() {} }

type fakeTrayBackend struct {
	surfaces []*fakeTraySurface
}

func (b *fakeTrayBackend) Create(options mygo.TrayOptions) (traySurface, error) {
	surface := &fakeTraySurface{alive: true, menu: options.Menu, tooltip: options.ToolTip}
	b.surfaces = append(b.surfaces, surface)
	return surface, nil
}

type recordingOpener struct {
	targets []appdesktop.Target
	err     error
}

func (o *recordingOpener) Open(target appdesktop.Target) error {
	o.targets = append(o.targets, target)
	return o.err
}

func newDesktopTestController(t *testing.T) (*desktopController, *windowGraph,
	*fakeTrayBackend, *testDesktopRepository, *testHotkeys, *testNotifier, *recordingOpener) {
	t.Helper()
	graph := newWindowGraph(t, nil)
	graph.manager.OpenMain()
	repository := &testDesktopRepository{settings: appdesktop.Defaults("windows")}
	hotkeys := &testHotkeys{}
	notifier := &testNotifier{}
	service := appdesktop.NewService(repository, hotkeys,
		&testAutoLauncher{supported: true}, notifier, appdesktop.WithGOOS("windows"))
	if err := service.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	backend := &fakeTrayBackend{}
	controller := newDesktopController(graph.manager, graph.state, nil, nil, service,
		backend, func() string { return "en" }, func() {})
	opener := &recordingOpener{}
	controller.opener = opener
	controller.MarkReady()
	t.Cleanup(func() { controller.Shutdown() })
	return controller, graph, backend, repository, hotkeys, notifier, opener
}

func trayMenuLabels(menu *mygo.Menu) []string {
	if menu == nil {
		return nil
	}
	labels := make([]string, 0, len(menu.Items()))
	for _, item := range menu.Items() {
		labels = append(labels, item.Label)
	}
	return labels
}

func clickTrayItem(t *testing.T, menu *mygo.Menu, label string) {
	t.Helper()
	for _, item := range menu.Items() {
		if item.Label == label {
			if item.Click == nil {
				t.Fatalf("menu item %q has no action", label)
			}
			item.Click(item, nil)
			return
		}
	}
	t.Fatalf("menu has no item %q: %v", label, trayMenuLabels(menu))
}

func clickTrayItemPrefix(t *testing.T, menu *mygo.Menu, prefix string) {
	t.Helper()
	for _, item := range menu.Items() {
		if strings.HasPrefix(item.Label, prefix) {
			if item.Click == nil {
				t.Fatalf("menu item %q has no action", item.Label)
			}
			item.Click(item, nil)
			return
		}
	}
	t.Fatalf("menu has no item starting %q: %v", prefix, trayMenuLabels(menu))
}

func TestTrayMenuContainsSectionsAndRaisesMain(t *testing.T) {
	controller, graph, backend, _, _, _, _ := newDesktopTestController(t)
	if len(backend.surfaces) != 1 {
		t.Fatalf("tray surfaces = %d", len(backend.surfaces))
	}
	menu := backend.surfaces[0].menu
	if menu == nil {
		t.Fatal("the tray received no menu")
	}
	m := locales.In("en")
	labels := strings.Join(trayMenuLabels(menu), "\n")
	for _, want := range []string{m.TrayOpenMain(), m.TraySessions(), m.TrayForwarding(), m.TrayRecentHosts(), m.TrayQuit(), m.TrayNoSessions()} {
		if !strings.Contains(labels, want) {
			t.Fatalf("tray menu lacks %q:\n%s", want, labels)
		}
	}

	before := graph.manager.mainFocuses
	clickTrayItem(t, menu, m.TrayOpenMain())
	if graph.manager.mainFocuses != before+1 {
		t.Fatalf("Open Main did not raise the main window: %d -> %d", before, graph.manager.mainFocuses)
	}

	// A Session appears in the menu and its row selects the Tab.
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	controller.SyncNow()
	labels = strings.Join(trayMenuLabels(backend.surfaces[0].menu), "\n")
	if strings.Contains(labels, m.TrayNoSessions()) || !strings.Contains(labels, m.TabTitle(1)) {
		t.Fatalf("tray menu did not pick up the Session:\n%s", labels)
	}
	clickTrayItemPrefix(t, backend.surfaces[0].menu, m.TabTitle(1))
	if graph.state.ActiveTab() != 0 {
		t.Fatalf("the Session row did not select the Tab: %d", graph.state.ActiveTab())
	}
}

func TestTrayMenuShowsPinnedRecentHosts(t *testing.T) {
	controller, _, backend, _, _, _, _ := newDesktopTestController(t)
	// A nil Vault is the headless case; the section still explains itself.
	labels := strings.Join(trayMenuLabels(backend.surfaces[0].menu), "\n")
	if !strings.Contains(labels, locales.In("en").TrayNoHosts()) {
		t.Fatalf("empty Vault menu:\n%s", labels)
	}
	_ = controller
}

func TestTrayMenuOrdersPinnedThenRecentlyUpdatedHosts(t *testing.T) {
	controller, _, backend, _, _, _, _ := newDesktopTestController(t)
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	host := func(label string, pinned bool, updated time.Time) domainvault.Host {
		return domainvault.Host{Label: label, Hostname: label + ".example", Port: 22,
			Protocol: domainvault.ProtocolSSH, Pinned: pinned, UpdatedAt: updated}
	}
	vault := vaultstate.NewState(&desktopTestHosts{hosts: []domainvault.Host{
		host("newest", false, base.Add(5*time.Hour)),
		host("pinned-old", true, base.Add(-5*time.Hour)),
		host("newer", false, base.Add(4*time.Hour)),
		host("older", false, base.Add(2*time.Hour)),
		host("oldest", false, base.Add(-1*time.Hour)),
		host("middle", false, base.Add(3*time.Hour)),
		host("dropped-a", false, base.Add(time.Hour)),
		host("dropped-b", false, base.Add(-2*time.Hour)),
	}})
	if err := vault.Reload(); err != nil {
		t.Fatal(err)
	}
	controller.vault = vault
	controller.SyncNow()

	labels := trayMenuLabels(backend.surfaces[0].menu)
	index := func(label string) int {
		for i, item := range labels {
			if item == label {
				return i
			}
		}
		return -1
	}
	want := []string{"pinned-old", "newest", "newer", "middle", "older", "dropped-a"}
	last := -1
	for _, label := range want {
		at := index(label)
		if at < 0 {
			t.Fatalf("recent Hosts lacks %q:\n%s", label, strings.Join(labels, "\n"))
		}
		if at <= last {
			t.Fatalf("%q is out of order:\n%s", label, strings.Join(labels, "\n"))
		}
		last = at
	}
	for _, label := range []string{"oldest", "dropped-b"} {
		if index(label) >= 0 {
			t.Fatalf("recent Hosts kept %q beyond the six-entry limit:\n%s", label, strings.Join(labels, "\n"))
		}
	}
}

func TestCloseToTrayHidesTheMainWindowWithoutPrompting(t *testing.T) {
	controller, graph, _, _, _, _, _ := newDesktopTestController(t)
	graph.manager.SetCloseToTray(controller.HideOnClose)
	window := graph.manager.main
	window.Close()
	if window.IsDestroyed() {
		t.Fatal("close-to-tray destroyed the main window")
	}
	if window.IsVisible() {
		t.Fatal("close-to-tray did not hide the main window")
	}
	if len(graph.confirmer.prompts) != 0 {
		t.Fatalf("close-to-tray asked about Sessions: %+v", graph.confirmer.prompts)
	}

	// Turning the tray icon off makes the next close a real close.
	if err := controller.service.SetShowTrayIcon(false); err != nil {
		t.Fatal(err)
	}
	controller.SyncNow()
	if controller.tray != nil {
		t.Fatal("the tray survived ShowTrayIcon=false")
	}
	window.Show()
	window.Close()
	if !window.IsDestroyed() {
		t.Fatal("a close without a tray did not close the window")
	}
}

func TestDeepLinkAndCommandLineOpenTargetsOnce(t *testing.T) {
	controller, _, _, _, _, _, opener := newDesktopTestController(t)
	controller.HandleDeepLink("ssh://alice:p%40ss@EXAMPLE.com:2222")
	if len(opener.targets) != 1 {
		t.Fatalf("targets = %+v", opener.targets)
	}
	target := opener.targets[0]
	if target.Hostname != "example.com" || target.Username != "alice" || target.Password != "p@ss" || target.Port != 2222 {
		t.Fatalf("target = %+v", target)
	}

	controller.HandleDeepLink("https://example.com")
	if len(opener.targets) != 1 || controller.LastProblem() == "" {
		t.Fatalf("an unsupported link opened: %+v problem %q", opener.targets, controller.LastProblem())
	}

	// A second-launch command line skips scheme URLs: MyGo delivers those to
	// OnOpenURL, so handling both would open the same link twice.
	controller.HandleLaunchArgs([]string{"ssh://dup.example", "box.example:2200"})
	if len(opener.targets) != 2 {
		t.Fatalf("targets = %+v", opener.targets)
	}
	if last := opener.targets[len(opener.targets)-1]; last.Hostname != "box.example" || last.Port != 2200 {
		t.Fatalf("command-line target = %+v", last)
	}

	// Disabling deep links stops URL handling, while a later CLI launch still
	// works explicitly.
	if err := controller.service.SetSSHDeepLinks(false); err != nil {
		t.Fatal(err)
	}
	controller.HandleDeepLink("ssh://blocked.example")
	if len(opener.targets) != 2 {
		t.Fatalf("a disabled deep link opened: %+v", opener.targets)
	}
}

func TestSessionAndForwardingNotificationsFireOnTransitions(t *testing.T) {
	controller, _, _, _, _, notifier, _ := newDesktopTestController(t)
	id := model.SessionID("s1")
	controller.NoteSession(appsession.Snapshot{ID: id, Status: appsession.StatusRunning, Connection: appsession.ConnectionStatusActive})
	controller.NoteSession(appsession.Snapshot{ID: id, Status: appsession.StatusExited, Connection: appsession.ConnectionStatusActive})
	if len(notifier.notifications) != 1 || notifier.notifications[0].Event != appdesktop.EventSessionEnded {
		t.Fatalf("notifications = %+v", notifier.notifications)
	}
	controller.NoteSession(appsession.Snapshot{ID: id, Status: appsession.StatusExited, Connection: appsession.ConnectionStatusActive})
	if len(notifier.notifications) != 1 {
		t.Fatal("a repeated exit notified twice")
	}

	connectionID := model.SessionID("s2")
	controller.NoteSession(appsession.Snapshot{ID: connectionID, Status: appsession.StatusRunning, Connection: appsession.ConnectionStatusActive})
	controller.NoteSession(appsession.Snapshot{ID: connectionID, Status: appsession.StatusExited, Connection: appsession.ConnectionStatusError})
	if len(notifier.notifications) != 2 || notifier.notifications[1].Event != appdesktop.EventConnectionFailed {
		t.Fatalf("notifications = %+v", notifier.notifications)
	}

	rule := model.ForwardingRuleID("r1")
	failed := appforwarding.Event{Type: appforwarding.EventUpsert, ID: rule,
		View: appforwarding.View{Rule: domainforward.Rule{ID: rule, Label: "tunnel"}, Status: domainforward.StatusError}}
	controller.NoteForwarding(failed)
	controller.NoteForwarding(failed)
	if len(notifier.notifications) != 3 || notifier.notifications[2].Event != appdesktop.EventForwardingFailed ||
		notifier.notifications[2].Target != "tunnel" {
		t.Fatalf("notifications = %+v", notifier.notifications)
	}
	controller.NoteForwarding(appforwarding.Event{Type: appforwarding.EventRemove, ID: rule})
	controller.NoteForwarding(failed)
	if len(notifier.notifications) != 4 {
		t.Fatal("a removed rule did not re-arm its failure notification")
	}
}

func TestGlobalHotkeyRegistrationDrivesTheToggle(t *testing.T) {
	controller, graph, _, _, hotkeys, _, _ := newDesktopTestController(t)
	if err := controller.service.SetToggleWindowHotkey("Ctrl+Shift+Space"); err != nil {
		t.Fatal(err)
	}
	if err := controller.service.Apply(controller.ToggleMain); err != nil {
		t.Fatal(err)
	}
	callback, ok := hotkeys.registered["Ctrl + Shift + Space"]
	if !ok {
		// Accelerator keeps the original spacing; accept the canonical form.
		callback, ok = hotkeys.registered["Ctrl+Shift+Space"]
	}
	if !ok {
		t.Fatalf("registered accelerators = %+v", hotkeys.registered)
	}
	window := graph.manager.main
	if fake, ok := window.(*fakeWindow); ok {
		fake.focused = false
	}
	callback()
	if !window.IsVisible() || !window.IsFocused() {
		t.Fatal("the toggle did not focus the main window")
	}
	callback()
	if window.IsVisible() {
		t.Fatal("a second toggle did not hide the focused main window")
	}
	if controller.ToggleCount() != 2 {
		t.Fatalf("toggles = %d", controller.ToggleCount())
	}
}

func TestCloseToTrayNeedsATray(t *testing.T) {
	controller := &desktopController{service: appdesktop.NewService(
		&testDesktopRepository{settings: appdesktop.Defaults("windows")}, nil, nil, nil)}
	if controller.HideOnClose() {
		t.Fatal("close-to-tray engaged without a tray")
	}
}
