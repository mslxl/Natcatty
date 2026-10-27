//go:build selftest

package app

// Ticket 14 self-test.
//
// Press F11, then 1, then 4. The check runs on the UI thread one step per
// frame and writes %TEMP%\natcatty-selftest-<pid>\natcatty-selftest-14.json.
//
// The steps exercise the production tray controller, deep-link and
// command-line routing, notification transition policy, global-hotkey
// registration and callback, autostart rollback, and close-to-tray behavior.
// Native surfaces that cannot run headless are replaced at their existing
// seams: a fake tray backend, a recording target opener, and a desktop
// Service with fake Hotkeys, AutoLauncher, Notifier, and Repository ports.
// Each step names the seam it used.

import (
	"context"
	"errors"
	"fmt"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	appdesktop "github.com/mslxl/natcatty/application/desktop"
	appforwarding "github.com/mslxl/natcatty/application/forwarding"
	appsession "github.com/mslxl/natcatty/application/session"
	domainforward "github.com/mslxl/natcatty/domain/forwarding"
	"github.com/mslxl/natcatty/domain/model"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/selftest"
	"github.com/mslxl/natcatty/ui/shell/state"
)

type selftest14 struct {
	desktop *desktopController
	windows *windowManager
	state   *state.State

	backend  *selftest14TrayBackend
	opener   *selftest14Opener
	service  *appdesktop.Service
	repo     *selftest14Repository
	hotkeys  *selftest14Hotkeys
	auto     *selftest14Auto
	notifier *selftest14Notifier
}

func (t *selftest14) check() selftest.Check {
	return selftest.Check{
		Ticket: 14,
		Name:   "Tray, deep links, notifications, hotkey, and autostart",
		Steps: []selftest.Step{
			{Name: "tray-menu-sync", Run: t.trayStep},
			{Name: "deep-link-handover", Run: t.deepLinkStep},
			{Name: "command-line-host", Run: t.commandLineStep},
			{Name: "notification-transition", Run: t.notificationStep},
			{Name: "global-hotkey", Run: t.hotkeyStep},
			{Name: "autostart-rollback", Run: t.autostartStep},
			{Name: "close-to-tray", Run: t.closeToTrayStep},
		},
	}
}

// resetRunState clears the controller state this check owns, so pressing
// F11,1,4 again in the same process starts from the same conditions as the
// first run. Without it, a forwarding failure observed by run one stays
// "already notified" and run two never emits it again.
func (t *selftest14) resetRunState() {
	if t.desktop == nil {
		return
	}
	t.desktop.mu.Lock()
	t.desktop.sessions = make(map[model.SessionID]observedSession)
	t.desktop.forwardingFailure = make(map[model.ForwardingRuleID]bool)
	t.desktop.lastProblem = ""
	t.desktop.syncs = 0
	t.desktop.toggles = 0
	t.desktop.quits = 0
	t.desktop.mu.Unlock()
	t.desktop.syncPending.Store(false)
}

func (t *selftest14) trayStep(c *ui.Context) selftest.Outcome {
	if t.desktop == nil || t.desktop.windows == nil {
		return selftest.Failed(errors.New("the desktop controller is not wired"))
	}
	t.resetRunState()
	t.backend = &selftest14TrayBackend{}
	t.desktop.backends = t.backend
	t.desktop.tray = nil
	t.desktop.trayAttempted = false
	t.desktop.trayFailed = false
	t.desktop.lastMenu = ""
	t.desktop.MarkReady()
	if len(t.backend.surfaces) != 1 || t.backend.surfaces[0].menu == nil {
		return selftest.Failed(fmt.Errorf("no tray menu was created"))
	}
	menu := t.backend.surfaces[0].menu
	m := locales.In("en")
	for _, want := range []string{m.TrayOpenMain(), m.TraySessions(), m.TrayForwarding(), m.TrayRecentHosts(), m.TrayQuit()} {
		if !selftest14HasLabel(menu, want) {
			return selftest.Failed(fmt.Errorf("tray menu lacks %q", want))
		}
	}
	focuses := t.windows.mainFocuses
	selftest14Click(menu, m.TrayOpenMain())
	if t.windows.mainFocuses != focuses+1 {
		return selftest.Failed(errors.New("the tray's Open Main action did not raise the window"))
	}
	return selftest.Passed(fmt.Sprintf(
		"the real controller built a %d-item menu (Sessions, Port Forwarding, Recent Hosts, Quit) and raised main",
		len(menu.Items())), "trayBackend")
}

func (t *selftest14) deepLinkStep(c *ui.Context) selftest.Outcome {
	if t.desktop == nil {
		return selftest.Failed(errors.New("the desktop controller is not wired"))
	}
	t.opener = &selftest14Opener{}
	t.desktop.opener = t.opener
	t.desktop.HandleDeepLink("ssh://alice:p%40ss@Example.COM:2222")
	if len(t.opener.targets) != 1 {
		return selftest.Failed(fmt.Errorf("opened %d targets", len(t.opener.targets)))
	}
	target := t.opener.targets[0]
	if target.Hostname != "example.com" || target.Username != "alice" ||
		target.Password != "p@ss" || target.Port != 2222 {
		return selftest.Failed(fmt.Errorf("parsed target = %+v", target))
	}
	t.desktop.HandleDeepLink("https://example.com")
	if len(t.opener.targets) != 1 || t.desktop.LastProblem() == "" {
		return selftest.Failed(errors.New("an unsupported link was opened or reported"))
	}
	return selftest.Passed("ssh:// parsing, percent decoding, validation, and unknown-scheme handling used the real controller",
		"targetOpener")
}

func (t *selftest14) commandLineStep(c *ui.Context) selftest.Outcome {
	if t.desktop == nil || t.opener == nil {
		return selftest.Failed(errors.New("the deep-link step did not run"))
	}
	before := len(t.opener.targets)
	// The URL is what MyGo delivers to OnOpenURL; the callback must not open
	// it a second time.
	t.desktop.HandleLaunchArgs([]string{"ssh://duplicate.example", "-l", "root", "-P", "2202", "-pw", "s3cret", "box.example"})
	if len(t.opener.targets) != before+1 {
		return selftest.Failed(fmt.Errorf("opened %d targets after one command-line Host", len(t.opener.targets)-before))
	}
	target := t.opener.targets[len(t.opener.targets)-1]
	if target.Hostname != "box.example" || target.Username != "root" || target.Port != 2202 ||
		target.Password != "s3cret" {
		return selftest.Failed(fmt.Errorf("command-line target = %+v", target))
	}
	return selftest.Passed("the second-instance command line opened one Host, consumed -pw, and left schemes to OnOpenURL",
		"targetOpener")
}

func (t *selftest14) notificationStep(c *ui.Context) selftest.Outcome {
	if t.desktop == nil {
		return selftest.Failed(errors.New("the desktop controller is not wired"))
	}
	t.notifier = &selftest14Notifier{}
	t.repo = &selftest14Repository{settings: appdesktop.Defaults("windows")}
	t.hotkeys = &selftest14Hotkeys{}
	t.auto = &selftest14Auto{supported: true}
	t.service = appdesktop.NewService(t.repo, t.hotkeys, t.auto, t.notifier,
		appdesktop.WithGOOS("windows"))
	if err := t.service.Load(context.Background()); err != nil {
		return selftest.Failed(err)
	}
	t.desktop.service = t.service

	id := model.SessionID("selftest-session")
	t.desktop.NoteSession(appsession.Snapshot{ID: id, Status: appsession.StatusRunning,
		Connection: appsession.ConnectionStatusActive})
	t.desktop.NoteSession(appsession.Snapshot{ID: id, Status: appsession.StatusExited,
		Connection: appsession.ConnectionStatusActive})
	if len(t.notifier.events) != 1 || t.notifier.events[0] != appdesktop.EventSessionEnded {
		return selftest.Failed(fmt.Errorf("session notifications = %v", t.notifier.events))
	}
	connectionID := model.SessionID("selftest-connection")
	t.desktop.NoteSession(appsession.Snapshot{ID: connectionID, Status: appsession.StatusRunning,
		Connection: appsession.ConnectionStatusActive})
	t.desktop.NoteSession(appsession.Snapshot{ID: connectionID, Status: appsession.StatusExited,
		Connection: appsession.ConnectionStatusError})
	ruleID := model.ForwardingRuleID("selftest-rule")
	t.desktop.NoteForwarding(appforwarding.Event{
		Type: appforwarding.EventUpsert, ID: ruleID,
		View: appforwarding.View{Rule: domainforward.Rule{ID: ruleID, Label: "tunnel"},
			Status: domainforward.StatusError},
	})
	if len(t.notifier.events) != 3 ||
		t.notifier.events[1] != appdesktop.EventConnectionFailed ||
		t.notifier.events[2] != appdesktop.EventForwardingFailed {
		return selftest.Failed(fmt.Errorf("notification events = %v", t.notifier.events))
	}
	return selftest.Passed("Session ended, connection failure, and forwarding failure each notified once",
		"desktop.Service ports")
}

func (t *selftest14) hotkeyStep(c *ui.Context) selftest.Outcome {
	if t.service == nil || t.hotkeys == nil || t.desktop == nil || t.windows == nil {
		return selftest.Failed(errors.New("the notification step did not install the service"))
	}
	if err := t.service.SetToggleWindowHotkey("Ctrl+Shift+Space"); err != nil {
		return selftest.Failed(err)
	}
	if err := t.service.Apply(t.desktop.ToggleMain); err != nil {
		return selftest.Failed(err)
	}
	callback := t.hotkeys.registered["Ctrl+Shift+Space"]
	if callback == nil {
		return selftest.Failed(fmt.Errorf("registered accelerators = %v", t.hotkeys.registered))
	}
	before := t.desktop.ToggleCount()
	callback()
	callback()
	if t.desktop.ToggleCount() != before+2 {
		return selftest.Failed(fmt.Errorf("toggle callback count = %d", t.desktop.ToggleCount()-before))
	}
	return selftest.Passed("the service registered the accelerator and its callback toggled the real main window",
		"desktop.Hotkeys")
}

func (t *selftest14) autostartStep(c *ui.Context) selftest.Outcome {
	if t.service == nil || t.auto == nil || t.repo == nil {
		return selftest.Failed(errors.New("the notification step did not install the service"))
	}
	if err := t.service.SetAutoStart(true); err != nil {
		return selftest.Failed(err)
	}
	if !t.auto.enabled || !t.service.Settings().AutoStart || t.repo.saves == 0 {
		return selftest.Failed(errors.New("enabling autostart did not reach the OS port and the repository"))
	}
	t.auto.fail = true
	if err := t.service.SetAutoStart(false); !errors.Is(err, appdesktop.ErrAutoStart) {
		return selftest.Failed(fmt.Errorf("rollback error = %v", err))
	}
	if !t.service.Settings().AutoStart {
		return selftest.Failed(errors.New("the cached autostart state did not roll back to the OS value"))
	}
	return selftest.Passed("enable persisted, and a refused disable rolled the cached state back to the OS value",
		"desktop.AutoLauncher")
}

func (t *selftest14) closeToTrayStep(c *ui.Context) selftest.Outcome {
	if t.desktop == nil || t.windows == nil || t.service == nil {
		return selftest.Failed(errors.New("the service was not installed"))
	}
	if t.backend == nil || t.desktop.tray == nil {
		return selftest.Failed(errors.New("the tray was not created by the first step"))
	}
	if err := t.service.SetShowTrayIcon(true); err != nil {
		return selftest.Failed(err)
	}
	if err := t.service.SetCloseToTray(true); err != nil {
		return selftest.Failed(err)
	}
	t.desktop.SyncNow()
	t.windows.SetCloseToTray(t.desktop.HideOnClose)
	main := t.windows.main
	main.Show()
	if t.windows.allowMainClose() {
		t.windows.SetCloseToTray(nil)
		return selftest.Failed(errors.New("close-to-tray did not cancel the main-window close"))
	}
	hidden := !main.IsVisible()
	// The self-test overlay lives in the main window; restore it so the
	// result remains visible.
	t.windows.SetCloseToTray(nil)
	t.desktop.ShowMain()
	if !hidden {
		return selftest.Failed(errors.New("close-to-tray did not hide the main window"))
	}
	return selftest.Passed("a main-window close hid the window instead of ending its Sessions", "trayBackend")
}

func selftest14HasLabel(menu *mygo.Menu, label string) bool {
	if menu == nil {
		return false
	}
	for _, item := range menu.Items() {
		if item.Label == label {
			return true
		}
	}
	return false
}

func selftest14Click(menu *mygo.Menu, label string) {
	for _, item := range menu.Items() {
		if item.Label == label && item.Click != nil {
			item.Click(item, nil)
			return
		}
	}
}

type selftest14TrayBackend struct {
	surfaces []*selftest14Tray
}

func (b *selftest14TrayBackend) Create(options mygo.TrayOptions) (traySurface, error) {
	surface := &selftest14Tray{menu: options.Menu, tooltip: options.ToolTip}
	b.surfaces = append(b.surfaces, surface)
	return surface, nil
}

type selftest14Tray struct {
	menu    *mygo.Menu
	tooltip string
	icon    []byte
}

func (t *selftest14Tray) SetMenu(menu *mygo.Menu)   { t.menu = menu }
func (t *selftest14Tray) SetToolTip(tooltip string) { t.tooltip = tooltip }
func (t *selftest14Tray) SetIcon(png []byte, template bool) error {
	t.icon = append([]byte(nil), png...)
	return nil
}
func (t *selftest14Tray) Destroy()                   {}
func (t *selftest14Tray) OnClick(func()) func()      { return func() {} }
func (t *selftest14Tray) OnRightClick(func()) func() { return func() {} }

type selftest14Opener struct {
	targets []appdesktop.Target
}

func (o *selftest14Opener) Open(target appdesktop.Target) error {
	o.targets = append(o.targets, target)
	return nil
}

type selftest14Repository struct {
	settings appdesktop.Settings
	saves    int
}

func (r *selftest14Repository) Load(context.Context) (appdesktop.Settings, error) {
	return r.settings, nil
}

func (r *selftest14Repository) Save(_ context.Context, settings appdesktop.Settings) error {
	r.settings = settings
	r.saves++
	return nil
}

type selftest14Hotkeys struct {
	registered map[string]func()
}

func (h *selftest14Hotkeys) Register(accelerator string, fn func()) error {
	if h.registered == nil {
		h.registered = map[string]func(){}
	}
	h.registered[accelerator] = fn
	return nil
}

func (h *selftest14Hotkeys) Unregister(accelerator string) { delete(h.registered, accelerator) }

type selftest14Auto struct {
	enabled   bool
	supported bool
	fail      bool
}

func (a *selftest14Auto) Supported() bool        { return a.supported }
func (a *selftest14Auto) Enabled() (bool, error) { return a.enabled, nil }
func (a *selftest14Auto) SetEnabled(enabled bool) error {
	if a.fail {
		return errors.New("the operating system refused the login item")
	}
	a.enabled = enabled
	return nil
}

type selftest14Notifier struct {
	events []appdesktop.Event
}

func (n *selftest14Notifier) Supported() bool { return true }
func (n *selftest14Notifier) Notify(notification appdesktop.Notification) error {
	n.events = append(n.events, notification.Event)
	return nil
}
