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
	"github.com/mslxl/natcatty/ui/shell/state"
	uistry "github.com/mslxl/natcatty/ui/tray"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

// traySurface is the native notification-area icon the controller owns. MyGo's
// *Tray implements it; the selftest substitutes a fake.
type traySurface interface {
	SetMenu(*mygo.Menu)
	SetToolTip(string)
	SetIcon(png []byte, template bool) error
	Destroy()
	OnClick(func()) func()
	OnRightClick(func()) func()
}

// trayBackend creates a tray icon. MyGo needs an application that is ready;
// the selftest supplies a fake so it can exercise the menu without one.
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

// shellTargetOpener resolves the target against the Vault and opens a Session
// through the shell. A unique saved Host is reused; otherwise an unsaved Host
// is built with the original label and default port.
type shellTargetOpener struct {
	shell *state.State
	vault *vaultstate.State
}

func (o shellTargetOpener) Open(target appdesktop.Target) error {
	if target.Kind != appdesktop.TargetSSH {
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
	windows    *windowManager
	shell      *state.State
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
	vault *vaultstate.State, forwarding *appforwarding.Manager,
	service *appdesktop.Service, backends trayBackend, locale func() string,
	quit func()) *desktopController {
	return &desktopController{
		windows:           windows,
		shell:             shellState,
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

// ShowMain raises the main window. Tray actions call it before opening their
// target.
func (c *desktopController) ShowMain() {
	c.windows.FocusMain()
	c.windows.InvalidateAll()
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

// HandleLaunchArgs opens Host targets passed on the command line. ssh:// URLs
// are skipped here because MyGo delivers them to OnOpenURL, which avoids
// opening a deep link twice after a single-instance handover. Other schemes
// stay in the list so an unsupported target is reported instead of ignored.
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
	if target.Kind != appdesktop.TargetSSH {
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
	label, _ := c.shell.SessionLabel(snapshot.ID)
	notify := func(event appdesktop.Event) {
		_ = c.service.Notify(appdesktop.Notification{Event: event, Target: label})
	}
	failed := snapshot.Connection == appsession.ConnectionStatusError &&
		previous.connection != appsession.ConnectionStatusError
	if failed {
		notify(appdesktop.EventConnectionFailed)
		c.ScheduleSync()
		return
	}
	if snapshot.Status == appsession.StatusExited &&
		previous.status != appsession.StatusExited {
		notify(appdesktop.EventSessionEnded)
		c.ScheduleSync()
	}
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
func (c *desktopController) ScheduleSync() {
	if !c.syncPending.CompareAndSwap(false, true) {
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

// BuildTrayMenu returns the current menu template. It is exported to the
// selftest so it can inspect the exact menu the native tray receives.
func (c *desktopController) BuildTrayMenu() *mygo.Menu {
	m := c.messages()
	items := []*mygo.MenuItem{
		{Label: m.TrayOpenMain(), Click: func(*mygo.MenuItem, *mygo.Window) { c.ShowMain() }},
		mygo.Separator(),
		{Label: m.TraySessions(), Disabled: true},
	}
	tabs := c.shell.TabSummaries()
	if len(tabs) == 0 {
		items = append(items, &mygo.MenuItem{Label: m.TrayNoSessions(), Disabled: true})
	} else {
		for _, tab := range tabs {
			tab := tab
			items = append(items, &mygo.MenuItem{
				Label: uistry.TabText(m, uistry.Tab{Number: tab.Number, Label: tab.Label, Status: uistry.TabStatus(tab.Status)}),
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
