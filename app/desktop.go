package app

import (
	"context"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"

	appdesktop "github.com/mslxl/natcatty/application/desktop"
	appforwarding "github.com/mslxl/natcatty/application/forwarding"
	appsession "github.com/mslxl/natcatty/application/session"
	domainforward "github.com/mslxl/natcatty/domain/forwarding"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/locales"
	quakeui "github.com/mslxl/natcatty/ui/quake"
	"github.com/mslxl/natcatty/ui/shell/state"
	uistry "github.com/mslxl/natcatty/ui/tray"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

// traySurface is the native notification-area icon the controller owns. MyGo's
// *Tray implements it; tests substitute a fake.
type traySurface interface {
	SetMenu(*mygo.Menu)
	SetToolTip(string)
	SetIcon(png []byte, template bool) error
	Destroy()
	OnClick(func()) func()
	OnRightClick(func()) func()
}

// trayBackend creates a tray icon. MyGo needs an application that is ready;
// tests supply a fake so they can exercise the menu without one.
type trayBackend interface {
	Create(mygo.TrayOptions) (traySurface, error)
}

type mygoTrayBackend struct{}

func (mygoTrayBackend) Create(options mygo.TrayOptions) (traySurface, error) {
	tray, err := mygo.NewTray(options)
	if err != nil {
		return nil, err
	}
	return tray, nil
}

// targetOpener opens a parsed deep-link or command-line target.
type targetOpener interface {
	Open(appdesktop.Target) error
}

// shellTargetOpener opens a launch target through the shell. A local-shell
// target starts a named shell in a new Tab; a Host target resolves against the
// Vault. A unique saved Host is reused; otherwise an unsaved Host is built
// with the original label and default port.
type shellTargetOpener struct {
	shell *state.State
	vault *vaultstate.State
}

func (o shellTargetOpener) Open(target appdesktop.Target) error {
	switch target.Kind {
	case appdesktop.TargetLocalShell:
		if o.shell == nil {
			return appdesktop.ErrUnsupportedScheme
		}
		return o.shell.NewTabWithShell(target.Shell)
	case appdesktop.TargetSSH:
	default:
		return appdesktop.ErrUnsupportedScheme
	}
	host, ok := appdesktop.MatchHost(o.vault.Items(), target)
	if !ok {
		host = appdesktop.DraftHost(target)
	}
	return o.shell.OpenHost(host)
}

type observedSession struct {
	status     appsession.Status
	connection appsession.ConnectionStatus
}

// desktopController wires the desktop use cases to the shell, the forwarding
// manager, and the native tray. Callbacks run on the UI thread unless noted.
type desktopController struct {
	windows *windowManager
	// shell is the main window's Workspace. quake is the Dropdown Panel's own
	// Workspace; it may be nil in a build or test without the Panel, in which
	// case the tray and notifications only see the main Workspace.
	shell      *state.State
	quake      *quakeui.State
	vault      *vaultstate.State
	forwarding *appforwarding.Manager
	service    *appdesktop.Service
	backends   trayBackend
	opener     targetOpener
	locale     func() string
	quit       func()
	schedule   func(time.Duration, func())

	tray          traySurface
	trayIcon      func() []byte
	trayAttempted bool
	trayFailed    bool
	lastMenu      string

	ready   bool
	pending []func()

	syncPending atomic.Bool

	mu                sync.Mutex
	sessions          map[model.SessionID]observedSession
	forwardingFailure map[model.ForwardingRuleID]bool
	lastProblem       string
	syncs             int
	toggles           int
	quits             int
}

// newDesktopController wires the production integration. quit goes through
// the same quit-intent path as the application menu.
func newDesktopController(windows *windowManager, shellState *state.State,
	quakeState *quakeui.State, vault *vaultstate.State,
	forwarding *appforwarding.Manager,
	service *appdesktop.Service, backends trayBackend, locale func() string,
	quit func()) *desktopController {
	controller := &desktopController{
		windows:           windows,
		shell:             shellState,
		quake:             quakeState,
		vault:             vault,
		forwarding:        forwarding,
		service:           service,
		backends:          backends,
		opener:            shellTargetOpener{shell: shellState, vault: vault},
		locale:            locale,
		quit:              quit,
		schedule:          func(delay time.Duration, fn func()) { time.AfterFunc(delay, fn) },
		sessions:          make(map[model.SessionID]observedSession),
		forwardingFailure: make(map[model.ForwardingRuleID]bool),
	}
	if service != nil {
		// The service owns registration and persistence; the controller owns
		// the windows. A successful mode change must retire a Panel that was
		// showing the previous mode.
		service.OnHotkeyModeChange(controller.HandleHotkeyModeChange)
		// Changing the Quake source changes what the open Panel renders and,
		// in follow mode, which window owns the Workspace surfaces. Retiring
		// the Panel re-routes from a clean state instead of leaving the main
		// window hidden under a stale ownership transfer.
		service.OnQuakeSourceChange(controller.HandleQuakeSourceChange)
	}
	return controller
}

// MarkReady releases launch work queued before the main window existed, then
// creates the tray if the settings ask for it. It runs on the UI thread.
func (c *desktopController) MarkReady() {
	c.mu.Lock()
	c.ready = true
	pending := c.pending
	c.pending = nil
	c.mu.Unlock()
	for _, fn := range pending {
		fn()
	}
	c.SyncNow()
}

// Shutdown releases the global shortcut and removes the tray icon. A tray
// sync already scheduled when quit begins must not run afterwards: it would
// find the destroyed tray and dereference it. Clearing ready makes SyncNow a
// no-op from here on.
func (c *desktopController) Shutdown() {
	c.mu.Lock()
	c.ready = false
	c.mu.Unlock()
	c.service.Close()
	c.destroyTray()
	// A failed creation leaves trayAttempted set without a tray, so reset it
	// even when destroyTray had nothing to destroy.
	c.trayAttempted = false
}

// HandleHotkey is the global hotkey's callback. The current Hotkey Mode
// decides what the one registered accelerator does: none is a no-op, toggle
// raises or hides the main window, and both dropdown modes toggle the single
// Dropdown Panel. It runs on the UI thread.
func (c *desktopController) HandleHotkey() {
	if c.service == nil {
		return
	}
	switch c.service.Settings().HotkeyMode {
	case appdesktop.HotkeyModeNone:
		// The accelerator is unregistered in none mode. A callback that still
		// arrives, for example from a registration that raced the change, must
		// not move a window.
		return
	case appdesktop.HotkeyModeToggle:
		c.ToggleMain()
	case appdesktop.HotkeyModeQuake, appdesktop.HotkeyModeTopdown:
		c.mu.Lock()
		c.toggles++
		c.mu.Unlock()
		c.windows.ToggleDropdown()
	}
}

// HandleHotkeyModeChange retires a visible Dropdown Panel when the global
// shortcut changes meaning: the open Panel belongs to the previous mode, and
// the next press must open the new mode instead of re-showing stale content.
// Hiding is also where a follow/top-down ownership transfer restores the main
// window. It runs on the UI thread, after the new mode is saved.
func (c *desktopController) HandleHotkeyModeChange(appdesktop.HotkeyMode) {
	if c.windows == nil {
		return
	}
	c.windows.HideDropdown()
	c.windows.InvalidateAll()
}

// HandleQuakeSourceChange retires a visible Dropdown Panel when the Quake
// content source changes. Follow mode moves the Workspace surfaces into the
// Panel, so a live switch must not leave the main window hidden or the Panel
// rendering the previous source's content. It runs on the UI thread, after the
// new source is saved.
func (c *desktopController) HandleQuakeSourceChange(appdesktop.QuakeSource) {
	if c.windows == nil {
		return
	}
	c.windows.HideDropdown()
	c.windows.InvalidateAll()
}

// ToggleMain shows, focuses, or hides the main window, as the original global
// hotkey did. It runs on the UI thread.
func (c *desktopController) ToggleMain() {
	c.mu.Lock()
	c.toggles++
	c.mu.Unlock()
	window := c.windows.Main()
	if window == nil || window.IsDestroyed() {
		c.windows.FocusMain()
		return
	}
	if window.IsVisible() && window.IsFocused() {
		window.Hide()
		return
	}
	window.Show()
	window.Focus()
}

// ShowMain raises the main window. Tray actions and deep links call it before
// opening their target: an open Dropdown Panel is hidden first, so the main
// window is the only window presenting the Workspace.
func (c *desktopController) ShowMain() {
	if c.windows == nil {
		return
	}
	c.windows.HideDropdown()
	c.windows.FocusMain()
	c.windows.InvalidateAll()
}

// HandleLoginLaunch applies a login start. When the system opened the app at
// login and there is a tray icon to reopen it from, the main window starts
// hidden and no Dropdown Panel is left on screen. Any other launch keeps the
// normal visible main window. It runs on the UI thread after readiness.
func (c *desktopController) HandleLoginLaunch(openedAtLogin bool) {
	if !openedAtLogin || c.windows == nil {
		return
	}
	settings := c.service.Settings()
	if !settings.ShowTrayIcon || !c.service.Capabilities().Tray {
		return
	}
	main := c.windows.Main()
	if main == nil || main.IsDestroyed() {
		return
	}
	// A login launch never displays the Panel. Hide one that a queued launch
	// target opened, then put the main window away in the tray.
	c.windows.HideDropdown()
	if main.IsVisible() {
		main.Hide()
	}
}

// HideOnClose reports whether closing the main window should hide it to the
// tray. It is read live so the Settings switch takes effect immediately.
func (c *desktopController) HideOnClose() bool {
	settings := c.service.Settings()
	return settings.ShowTrayIcon && settings.CloseToTray && c.tray != nil
}

// HandleDeepLink parses and opens an ssh:// link. The single-instance path
// calls it on the UI thread; before readiness it is queued.
func (c *desktopController) HandleDeepLink(raw string) {
	c.runOrQueue(func() {
		if !c.service.Settings().SSHDeepLinks {
			c.setProblem("deep-links-disabled")
			return
		}
		target, err := appdesktop.ParseDeepLink(raw)
		if err != nil {
			c.reportTargetProblem(err)
			return
		}
		c.openTarget(target)
	})
}

// HandleLaunchArgs opens Host and local-terminal targets passed on the command
// line. --open-terminal-shell reaches the same single-instance handoff as a
// Host target: a cold start queues until ready, a later launch opens into the
// running window. ssh:// URLs are skipped here because MyGo delivers them to
// OnOpenURL, which avoids opening a deep link twice after a single-instance
// handover. Other schemes stay in the list so an unsupported target is
// reported instead of ignored.
func (c *desktopController) HandleLaunchArgs(args []string) {
	filtered := make([]string, 0, len(args))
	var invalid []error
	for _, arg := range args {
		if !isSSHSchemeURL(arg) {
			filtered = append(filtered, arg)
			continue
		}
		// OnOpenURL opens a valid scheme URL; validate it here only so a
		// malformed one is reported instead of silently dropped.
		if _, err := appdesktop.ParseDeepLink(arg); err != nil {
			invalid = append(invalid, err)
		}
	}
	if len(filtered) == 0 && len(invalid) == 0 {
		return
	}
	c.runOrQueue(func() {
		targets, problems := appdesktop.ParseCommandLine(filtered)
		for _, target := range targets {
			c.openTarget(target)
		}
		// Report problems after the valid targets so a successful open does
		// not immediately clear the banner.
		if len(problems) > 0 {
			c.reportTargetProblem(problems[0])
		}
		if len(invalid) > 0 {
			c.reportTargetProblem(invalid[0])
		}
	})
}

func isSSHSchemeURL(arg string) bool {
	trimmed := strings.TrimSpace(arg)
	return len(trimmed) >= len("ssh://") &&
		strings.EqualFold(trimmed[:len("ssh://")], "ssh://")
}

func (c *desktopController) openTarget(target appdesktop.Target) {
	if target.Kind != appdesktop.TargetSSH && target.Kind != appdesktop.TargetLocalShell {
		c.reportTargetProblem(appdesktop.ErrUnsupportedScheme)
		return
	}
	c.ShowMain()
	if c.opener == nil {
		c.setProblem("no-target-opener")
		return
	}
	if err := c.opener.Open(target); err != nil {
		c.reportTargetProblem(err)
		return
	}
	if c.shell != nil {
		c.shell.SetDesktopNotice(state.DesktopNoticeNone)
	}
}

// reportTargetProblem records the failure for diagnostics and shows a
// localized, non-blocking shell banner. The app keeps running; a later
// successful target clears the banner.
func (c *desktopController) reportTargetProblem(err error) {
	c.setProblem(err.Error())
	if c.shell != nil {
		notice := state.DesktopNoticeInvalidTarget
		if appdesktop.KindOf(err) == appdesktop.KindUnsupported {
			notice = state.DesktopNoticeUnsupportedTarget
		}
		c.shell.SetDesktopNotice(notice)
	}
	c.ShowMain()
}

func (c *desktopController) runOrQueue(fn func()) {
	c.mu.Lock()
	if !c.ready {
		c.pending = append(c.pending, fn)
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()
	fn()
}

// LastProblem returns the last deep-link or launch failure, for the self-test
// and diagnostics. It is never shown as raw text in the UI.
func (c *desktopController) LastProblem() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastProblem
}

func (c *desktopController) setProblem(problem string) {
	c.mu.Lock()
	c.lastProblem = problem
	c.mu.Unlock()
}

// NoteSession observes one Session snapshot on the change hook's goroutine.
// It emits notifications for the first failure and the first clean end.
func (c *desktopController) NoteSession(snapshot appsession.Snapshot) {
	c.mu.Lock()
	previous, seen := c.sessions[snapshot.ID]
	c.sessions[snapshot.ID] = observedSession{
		status:     snapshot.Status,
		connection: snapshot.Connection,
	}
	c.mu.Unlock()
	if !seen {
		return
	}
	label, _ := c.sessionLabel(snapshot.ID)
	notify := func(event appdesktop.Event) {
		_ = c.service.Notify(appdesktop.Notification{
			Event:   event,
			Target:  label,
			Session: string(snapshot.ID),
		})
	}
	failed := snapshot.Connection == appsession.ConnectionStatusError &&
		previous.connection != appsession.ConnectionStatusError
	if failed {
		notify(appdesktop.EventConnectionFailed)
		c.ScheduleSync()
		return
	}
	// A tmux detach closes the Tab by design and leaves the remote session
	// running; it is not the Session-ended event a user should be notified
	// about.
	if snapshot.Status == appsession.StatusExited &&
		snapshot.EndReason != appsession.EndReasonDetached &&
		previous.status != appsession.StatusExited {
		notify(appdesktop.EventSessionEnded)
		c.ScheduleSync()
	}
}

// sessionLabel resolves a Session's display label from the Workspace that
// owns it. The main Workspace is checked first; the Quake Workspace owns its
// own Tabs, and a local Quake Shell has no stored label, so it is named the
// way its Tab strip names it.
func (c *desktopController) sessionLabel(id model.SessionID) (string, bool) {
	if c.shell != nil {
		if label, ok := c.shell.SessionLabel(id); ok {
			return label, true
		}
	}
	if c.quake != nil {
		if tab, ok := c.quake.TabForSession(id); ok {
			return c.quakeTabLabel(tab), true
		}
	}
	return "", false
}

// quakeTabLabel names a Quake Tab for the tray and notifications. A remote Tab
// carries its Host label; a local Shell falls back to the numbered local name
// the Quake Tab strip draws.
func (c *desktopController) quakeTabLabel(tab quakeui.TabInfo) string {
	if label := strings.TrimSpace(tab.Label); label != "" {
		return label
	}
	return c.messages().LocalSessionTab(tab.Number)
}

// HandleNotificationClick routes a clicked desktop notification. A Session
// event opens the window that owns the Session and selects its Tab; a
// Quake Session opens the Dropdown Panel, while a main or detached Session
// raises the main window. An event without a Session raises the main window,
// which is the original behavior.
func (c *desktopController) HandleNotificationClick(request appdesktop.Notification) {
	if request.Session != "" {
		c.FocusSession(model.SessionID(request.Session))
		return
	}
	c.ShowMain()
}

// FocusSession raises the window that owns id and selects its Tab. A Session
// that is no longer open falls back to the main window, so a stale
// notification never does nothing.
func (c *desktopController) FocusSession(id model.SessionID) {
	if c.quake != nil {
		if tab, ok := c.quake.TabForSession(id); ok {
			c.showQuakeTab(tab.Index)
			return
		}
	}
	if c.shell != nil && c.shell.FocusSession(id) {
		c.ShowMain()
		return
	}
	c.ShowMain()
}

// showQuakeTab selects a Quake Tab and presents the workspace that draws it.
// Only the quake + own route renders the Quake workspace, so a route that
// would show the placeholder or the main shell is switched explicitly; a
// route that cannot be switched refuses the reveal and raises the main window
// instead of opening a Panel that cannot show the selected Session.
func (c *desktopController) showQuakeTab(index int) {
	if c.quake == nil || c.windows == nil {
		return
	}
	c.quake.SelectTab(index)
	if !c.ensureQuakeRoute() {
		c.ShowMain()
		return
	}
	c.windows.ShowDropdown()
	c.windows.InvalidateAll()
}

// ensureQuakeRoute makes the live Hotkey Mode and Quake Source render the Quake
// workspace. It is called only from a user action that names a Quake Tab, so
// switching the route is the explicit choice that makes that Tab visible.
func (c *desktopController) ensureQuakeRoute() bool {
	if c.service == nil {
		return true
	}
	settings := c.service.Settings()
	if settings.HotkeyMode == appdesktop.HotkeyModeQuake && settings.QuakeSource == appdesktop.QuakeSourceOwn {
		return true
	}
	if settings.HotkeyMode != appdesktop.HotkeyModeQuake {
		if err := c.service.SetHotkeyMode(appdesktop.HotkeyModeQuake); err != nil {
			c.setProblem(err.Error())
			return false
		}
	}
	if c.service.Settings().QuakeSource != appdesktop.QuakeSourceOwn {
		if err := c.service.SetQuakeSource(appdesktop.QuakeSourceOwn); err != nil {
			c.setProblem(err.Error())
			return false
		}
	}
	return true
}

// NoteForwarding observes manager events on the manager's dispatch goroutine.
func (c *desktopController) NoteForwarding(event appforwarding.Event) {
	if event.Type == appforwarding.EventRemove {
		c.mu.Lock()
		delete(c.forwardingFailure, event.ID)
		c.mu.Unlock()
		return
	}
	failed := event.View.Status == domainforward.StatusError
	c.mu.Lock()
	previous := c.forwardingFailure[event.ID]
	c.forwardingFailure[event.ID] = failed
	c.mu.Unlock()
	if failed && !previous {
		_ = c.service.Notify(appdesktop.Notification{
			Event:  appdesktop.EventForwardingFailed,
			Target: event.View.Rule.Label,
		})
	}
	c.ScheduleSync()
}

// ScheduleSync coalesces redraw-driven tray updates off the caller's stack.
// Nothing is scheduled before MarkReady: there is no tray to update yet, and
// a post to MyGo's main-thread queue before its event loop starts can be lost
// for good (MyGo v0.5.5 can register a nil dispatch callback when a post
// races launch, which then swallows every later wakeup, quit included).
func (c *desktopController) ScheduleSync() {
	c.mu.Lock()
	ready := c.ready
	c.mu.Unlock()
	if !ready || !c.syncPending.CompareAndSwap(false, true) {
		return
	}
	if c.schedule == nil {
		c.syncPending.Store(false)
		return
	}
	c.schedule(150*time.Millisecond, func() {
		mygo.RunOnMain(func() {
			if !c.syncPending.Swap(false) {
				return
			}
			c.SyncNow()
		})
	})
}

// SyncNow reconciles the native tray with the settings and rebuilds its menu
// from the current shell, forwarding, and Vault state. UI thread only.
func (c *desktopController) SyncNow() {
	if !c.ready {
		return
	}
	c.mu.Lock()
	c.syncs++
	c.mu.Unlock()

	settings := c.service.Settings()
	if !settings.ShowTrayIcon || c.trayFailed {
		c.destroyTray()
		return
	}
	if err := c.ensureTray(); err != nil {
		c.setProblem(err.Error())
		return
	}
	if c.tray == nil {
		// ensureTray reports no error when there is nothing left to try (it
		// already failed, or shutdown retired the surface). Never assume a
		// tray exists just because creation reported success.
		return
	}
	c.tray.SetToolTip(c.messages().AppName())
	menu := c.BuildTrayMenu()
	fingerprint := menuFingerprint(menu)
	if fingerprint != c.lastMenu {
		c.tray.SetMenu(menu)
		c.lastMenu = fingerprint
	}
}

func (c *desktopController) ensureTray() error {
	if c.tray != nil {
		return nil
	}
	if c.trayAttempted || c.backends == nil {
		return nil
	}
	c.trayAttempted = true
	menu := c.BuildTrayMenu()
	icon := trayIconGlyphPNG()
	if c.trayIcon != nil {
		if data := c.trayIcon(); data != nil {
			icon = data
		}
	}
	tray, err := c.backends.Create(mygo.TrayOptions{
		Icon:           icon,
		IconIsTemplate: false,
		ToolTip:        c.messages().AppName(),
		Menu:           menu,
	})
	if err != nil {
		c.trayFailed = true
		c.service.SetTraySupported(false)
		return err
	}
	c.tray = tray
	c.lastMenu = menuFingerprint(menu)
	return nil
}

// SetIconProvider installs the live tray icon reader. A change applies to the
// live tray immediately; a tray created later reads it in ensureTray.
func (c *desktopController) SetIconProvider(fn func() []byte) {
	c.trayIcon = fn
	if c.tray == nil || fn == nil {
		return
	}
	data := fn()
	if data == nil {
		return
	}
	_ = c.tray.SetIcon(data, false)
}

func (c *desktopController) destroyTray() {
	if c.tray == nil {
		return
	}
	c.tray.Destroy()
	c.tray = nil
	c.trayAttempted = false
	c.lastMenu = ""
}

// SyncCount reports how many times the tray was synchronized.
func (c *desktopController) SyncCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.syncs
}

// ToggleCount reports how many times the toggle hotkey ran the action.
func (c *desktopController) ToggleCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.toggles
}

func (c *desktopController) messages() locales.Messages {
	if c.locale == nil {
		return locales.In("en")
	}
	return locales.In(c.locale())
}

// BuildTrayMenu returns the current menu template.
func (c *desktopController) BuildTrayMenu() *mygo.Menu {
	m := c.messages()
	items := []*mygo.MenuItem{
		{Label: m.TrayOpenMain(), Click: func(*mygo.MenuItem, *mygo.Window) { c.ShowMain() }},
		mygo.Separator(),
		{Label: m.TraySessions(), Disabled: true},
	}
	tabs := c.trayTabs(m)
	if len(tabs) == 0 {
		items = append(items, &mygo.MenuItem{Label: m.TrayNoSessions(), Disabled: true})
	} else {
		items = append(items, tabs...)
	}

	items = append(items, mygo.Separator(), &mygo.MenuItem{Label: m.TrayForwarding(), Disabled: true})
	rules := c.forwardingRules()
	if len(rules) == 0 {
		items = append(items, &mygo.MenuItem{Label: m.TrayNoForwarding(), Disabled: true})
	} else {
		for _, rule := range rules {
			rule := rule
			items = append(items, &mygo.MenuItem{
				Label: uistry.RuleText(m, rule),
				Click: func(*mygo.MenuItem, *mygo.Window) { c.toggleForwarding(rule) },
			})
		}
	}

	items = append(items, mygo.Separator(), &mygo.MenuItem{Label: m.TrayRecentHosts(), Disabled: true})
	hosts := c.recentHosts()
	if len(hosts) == 0 {
		items = append(items, &mygo.MenuItem{Label: m.TrayNoHosts(), Disabled: true})
	} else {
		for _, host := range hosts {
			host := host
			items = append(items, &mygo.MenuItem{
				Label: host.Label,
				Click: func(*mygo.MenuItem, *mygo.Window) {
					c.ShowMain()
					if err := c.shell.OpenHost(host); err != nil {
						c.setProblem(err.Error())
					}
				},
			})
		}
	}

	items = append(items, mygo.Separator(), &mygo.MenuItem{
		Label: m.TrayQuit(),
		Click: func(*mygo.MenuItem, *mygo.Window) {
			c.mu.Lock()
			c.quits++
			c.mu.Unlock()
			if c.quit != nil {
				c.quit()
			}
		},
	})
	return mygo.NewMenu(items)
}

// trayTabs builds the Session list: the main Window's Tabs first, then the
// Dropdown Panel's Quake Tabs with a source marker. Choosing a main Tab raises
// the main window; choosing a Quake Tab shows the Dropdown Panel and selects
// the Tab, switching the route first when the live mode or source does not
// render the Quake workspace. This way the session list never pretends the two
// Workspaces are one, and a Quake row never opens an empty Panel.
func (c *desktopController) trayTabs(m locales.Messages) []*mygo.MenuItem {
	var items []*mygo.MenuItem
	if c.shell != nil {
		for _, tab := range c.shell.TabSummaries() {
			tab := tab
			items = append(items, &mygo.MenuItem{
				Label: uistry.TabText(m, uistry.Tab{
					Number: tab.Number,
					Label:  tab.Label,
					Status: uistry.TabStatus(tab.Status),
				}),
				Click: func(*mygo.MenuItem, *mygo.Window) {
					c.ShowMain()
					if tab.Detached {
						c.windows.OpenDetached(tab.TabID)
						return
					}
					c.shell.SelectTab(tab.TabIndex)
					c.windows.InvalidateAll()
				},
			})
		}
	}
	if c.quake != nil {
		for index, tab := range c.quake.Tabs() {
			index, tab := index, tab
			items = append(items, &mygo.MenuItem{
				Label: uistry.TabText(m, uistry.Tab{
					Number: tab.Number,
					Label:  c.quakeTabLabel(tab),
					Status: c.quakeTabStatus(index),
					Source: uistry.TabSourceQuake,
				}),
				Click: func(*mygo.MenuItem, *mygo.Window) {
					// Selecting first keeps the Panel's show hook from opening a
					// fresh local Shell: the workspace already has the Tab the
					// user chose. showQuakeTab also switches a route that does not
					// render the Quake workspace, so the click never opens an
					// empty Panel.
					c.showQuakeTab(index)
				},
			})
		}
	}
	return items
}

// quakeTabStatus aggregates the transport state of the Quake Tab at index the
// same way the shell's tray rows do: an error or an ended Session outranks a
// dial in progress.
func (c *desktopController) quakeTabStatus(index int) uistry.TabStatus {
	tab, ok := c.quake.TabAt(index)
	if !ok || tab.Session == nil {
		return uistry.TabDisconnected
	}
	switch snapshot := tab.Session.Snapshot(); {
	case snapshot.Connection == appsession.ConnectionStatusError:
		return uistry.TabDisconnected
	case snapshot.Status == appsession.StatusExited:
		return uistry.TabDisconnected
	case snapshot.Connection == appsession.ConnectionStatusConnecting,
		snapshot.Connection == appsession.ConnectionStatusReconnecting:
		return uistry.TabConnecting
	default:
		return uistry.TabConnected
	}
}

// forwardingRules maps the manager's runtime snapshot into tray entries.
func (c *desktopController) forwardingRules() []uistry.Rule {
	if c.forwarding == nil {
		return nil
	}
	snapshot := c.forwarding.Snapshot()
	rules := make([]uistry.Rule, 0, len(snapshot.Rules))
	for _, view := range snapshot.Rules {
		rules = append(rules, uistry.Rule{
			ID:         view.Rule.ID,
			Direction:  uistry.RuleKindLetter(string(view.Rule.Type)),
			Label:      view.Rule.Label,
			LocalPort:  view.Rule.LocalPort,
			RemoteHost: view.Rule.RemoteHost,
			RemotePort: view.Rule.RemotePort,
			Status:     string(view.Status),
		})
	}
	return rules
}

func (c *desktopController) toggleForwarding(rule uistry.Rule) {
	c.ShowMain()
	if c.forwarding == nil {
		return
	}
	var err error
	switch rule.Status {
	case string(domainforward.StatusActive), string(domainforward.StatusConnecting):
		err = c.forwarding.Stop(context.Background(), rule.ID)
	default:
		err = c.forwarding.Start(context.Background(), rule.ID)
	}
	if err != nil {
		c.setProblem(err.Error())
	}
}

// menuFingerprint changes exactly when the visible menu text or enabled state
// does, so terminal output invalidations do not rebuild the native menu.
func menuFingerprint(menu *mygo.Menu) string {
	if menu == nil {
		return ""
	}
	var builder strings.Builder
	for _, item := range menu.Items() {
		builder.WriteString(item.Label)
		if item.Disabled {
			builder.WriteString("\x00d")
		}
		if item.Type == mygo.MenuItemSeparator {
			builder.WriteString("\x00s")
		}
		builder.WriteString("\x00")
	}
	return builder.String()
}

// recentHosts returns pinned Hosts first, then the most recently updated,
// matching the original tray ordering as closely as the stored fields allow.
func (c *desktopController) recentHosts() []domainvault.Host {
	if c.vault == nil {
		return nil
	}
	hosts := append([]domainvault.Host(nil), c.vault.Items()...)
	sort.SliceStable(hosts, func(i, j int) bool {
		if hosts[i].Pinned != hosts[j].Pinned {
			return hosts[i].Pinned
		}
		if !hosts[i].UpdatedAt.Equal(hosts[j].UpdatedAt) {
			return hosts[i].UpdatedAt.After(hosts[j].UpdatedAt)
		}
		return hosts[i].Label < hosts[j].Label
	})
	const limit = 6
	if len(hosts) > limit {
		hosts = hosts[:limit]
	}
	return hosts
}
