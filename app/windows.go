//go:build !ios && !android

package app

import (
	"math"
	"sync"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	appdesktop "github.com/mslxl/natcatty/application/desktop"
	"github.com/mslxl/natcatty/domain/model"
	uiapplock "github.com/mslxl/natcatty/ui/applock"
	"github.com/mslxl/natcatty/ui/dropdown"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/shell"
	"github.com/mslxl/natcatty/ui/shell/state"
	"github.com/mslxl/natcatty/ui/titlebar"
)

// windowBounds is a window's screen rectangle in DIPs.
type windowBounds struct {
	X, Y          int
	Width, Height int
}

// dropdownMinHeight is the smallest readable Quake dropdown. The actual
// height also clamps to a shorter work area.
const dropdownMinHeight = 120

// topdownMinHeight is the smallest useful top-down dropdown: the complete
// application shell needs far more room than the Quake terminal. Like the
// Quake minimum, a shorter work area wins.
const topdownMinHeight = 480

// dropdownFallbackWorkArea is the last-resort rectangle when the platform
// reports no displays at all. It keeps the Panel constructible with a usable
// size.
var dropdownFallbackWorkArea = windowBounds{Width: 1280, Height: 800}

// screenProbe is the display seam the Dropdown Panel positions itself
// against. MyGo's Screen module implements it; tests supply a fake so they
// can move the pointer between displays without a real screen.
type screenProbe interface {
	CursorScreenPoint() mygo.Point
	DisplayNearestPoint(mygo.Point) mygo.Display
	PrimaryDisplay() mygo.Display
}

// windowSpec describes a native window the composition root asks the host to
// create. Content is already wrapped in the App Lock gate.
type windowSpec struct {
	Key       string
	Title     string
	StateKey  string
	Width     int
	Height    int
	MinWidth  int
	MinHeight int
	// Chrome is the window's title bar style. On macOS it places the traffic
	// lights level with that title bar.
	Chrome titlebar.Style
	// Panel asks for a non-activating, always-on-top panel: it floats over
	// other apps and full-screen Spaces, stays out of the taskbar and the app
	// switcher, and draws no native window controls.
	Panel bool
	// Hidden creates the window without showing it, so the caller can position
	// it with Bounds before its first Show.
	Hidden bool
	// Bounds is the initial screen rectangle. Nil keeps the host's default
	// placement. It is applied after creation and before any Show. Bounds does
	// not imply a StateKey: geometry persistence stays opt-in.
	Bounds  *windowBounds
	Content func(*ui.Context)
}

// windowHooks are the events the composition root handles. OnCloseRequest
// reports whether the close may proceed; the host cancels the close when it
// returns false. OnBlur runs when the window loses focus.
type windowHooks struct {
	OnFocus        func()
	OnBlur         func()
	OnCloseRequest func() bool
	OnClosed       func()
}

// hostWindow is the native window surface the composition root controls.
// MyGo implements it through mygoWindowHost; tests provide a fake.
type hostWindow interface {
	Show()
	Hide()
	Focus()
	Close()
	Invalidate()
	IsDestroyed() bool
	IsVisible() bool
	IsFocused() bool
	// SetBounds places and sizes the window. A hidden window can be positioned
	// before it is first shown.
	SetBounds(windowBounds)
	Bounds() windowBounds
	SetTitle(string)
	SetOpacity(float64)
	SetVibrancy(mygo.Vibrancy)
	SetIcon([]byte) error
	// The self-drawn title bar's controls.
	Minimize()
	ToggleMaximize()
	IsMaximized() bool
	// ToggleFullScreen enters or leaves native fullscreen, for the VNC
	// viewer's chrome action.
	ToggleFullScreen()
}

// windowHost creates native windows. The real host uses MyGo; tests record
// specs and drive hooks directly.
type windowHost interface {
	Create(windowSpec, windowHooks) hostWindow
}

// closePrompt is the localized close/exit intent question.
type closePrompt struct {
	Title   string
	Message string
	Confirm string
	Cancel  string
}

// dialogConfirmer asks the user and reports their decision. A failed dialog
// keeps the window open.
type dialogConfirmer interface {
	Confirm(closePrompt) bool
}

// transferQuitGuard reports how many Transfers a quit would abandon. It is
// consulted live at quit time; an error means the state could not be read, so
// the quit asks rather than quits silently.
type transferQuitGuard interface {
	UnfinishedTransfers() (int, error)
}

// nativeConfirmer shows a MyGo native warning dialog.
type nativeConfirmer struct{}

func (nativeConfirmer) Confirm(prompt closePrompt) bool {
	result, err := mygo.Dialog.Message(mygo.MessageOptions{
		Type:          mygo.MessageWarning,
		Title:         prompt.Title,
		Message:       prompt.Message,
		Buttons:       []string{prompt.Cancel, prompt.Confirm},
		DefaultButton: 0,
		CancelButton:  0,
	})
	return err == nil && result.Button == 1
}

// mygoWindowHost creates real MyGo windows.
type mygoWindowHost struct{}

func (mygoWindowHost) Create(spec windowSpec, hooks windowHooks) hostWindow {
	window := mygo.NewWindow(windowOptions(spec))
	wireWindowHooks(window, hooks)
	return mygoHostWindow{Window: window}
}

// windowOptions maps a windowSpec to MyGo's creation options. It is separate
// from Create so the mapping, including the absence of an implicit StateKey,
// is testable without a native window. Panel windows are always frameless
// because they draw their own chrome and have no native controls. Initial
// Bounds is applied with hostWindow.SetBounds by windowManager.create instead
// of creation coordinates: MyGo centers a window whose X and Y are both zero,
// which would misplace a top-left dropdown.
func windowOptions(spec windowSpec) mygo.WindowOptions {
	// Every window draws its own title bar, as the original's: no native
	// caption or menu bar. Elsewhere the window is frameless and the content
	// draws the window controls too (see withFrame); on macOS the title bar is
	// hidden instead, so the window keeps its traffic lights, set level with
	// the content's title bar. The menu still drives the accelerators.
	options := mygo.WindowOptions{
		Title:     spec.Title,
		Width:     spec.Width,
		Height:    spec.Height,
		MinWidth:  spec.MinWidth,
		MinHeight: spec.MinHeight,
		StateKey:  spec.StateKey,
		Panel:     spec.Panel,
		Hidden:    spec.Hidden,
		Frameless: true,
		Content:   ui.View(spec.Content),
	}
	// A Panel floats over the windows of every app, above full-screen apps
	// too. The composition root positions it from the pointer's work area
	// before each show; v1 does not let the user drag or resize it.
	options.AlwaysOnTop = spec.Panel
	options.DisableResize = spec.Panel
	options.DisableMove = spec.Panel
	if titlebar.NativeControls && !spec.Panel {
		options.Frameless = false
		options.TitleBarStyle = mygo.TitleBarHidden
		options.TrafficLightPosition = trafficLights(spec.Chrome)
	}
	return options
}

// nativeWindow is the hook surface of *mygo.Window. Create wires the
// composition root's hooks onto it; tests drive a fake instead of a native
// window.
type nativeWindow interface {
	OnFocus(func()) (off func())
	OnClose(func(*mygo.CloseEvent)) (off func())
	OnClosed(func()) (off func())
	OnBlur(func()) (off func())
}

// wireWindowHooks subscribes the composition root's hooks on a native window.
func wireWindowHooks(window nativeWindow, hooks windowHooks) {
	if hooks.OnFocus != nil {
		window.OnFocus(hooks.OnFocus)
	}
	window.OnClose(func(event *mygo.CloseEvent) {
		if hooks.OnCloseRequest != nil && !hooks.OnCloseRequest() {
			event.PreventDefault()
		}
	})
	if hooks.OnClosed != nil {
		window.OnClosed(hooks.OnClosed)
	}
	if hooks.OnBlur != nil {
		window.OnBlur(hooks.OnBlur)
	}
}

// mygoHostWindow adapts *mygo.Window to the manager's bounds type.
type mygoHostWindow struct{ *mygo.Window }

func (w mygoHostWindow) SetBounds(bounds windowBounds) {
	w.Window.SetBounds(mygo.Rectangle{
		X: bounds.X, Y: bounds.Y, Width: bounds.Width, Height: bounds.Height,
	})
}

func (w mygoHostWindow) Bounds() windowBounds {
	rect := w.Window.Bounds()
	return windowBounds{X: rect.X, Y: rect.Y, Width: rect.Width, Height: rect.Height}
}

var _ hostWindow = mygoHostWindow{}

// trafficLights places the macOS window controls so they sit centered on the
// title bar the content draws: the Tab bar, a detached terminal's bar, or the
// Settings header row.
func trafficLights(style titlebar.Style) *mygo.Point {
	// The lights are about 14 high; center them on a bar of the given height
	// starting at top.
	const lights = 14
	top, height := float32(0), titlebar.Height
	if style == titlebar.Dialog {
		top, height = titlebar.CloseTop, titlebar.CloseSize
	}
	return &mygo.Point{X: 14, Y: int(top + (height-lights)/2)}
}

// windowManager owns every application window and the shared App Lock gate.
// MyGo calls the hooks on the UI thread; tests drive them directly.
type windowManager struct {
	host            windowHost
	confirmer       dialogConfirmer
	gate            *uiapplock.Gate
	state           *state.State
	mainContent     func() func(*ui.Context)
	settingsContent func() func(*ui.Context)
	locale          func() string
	// closeToTray asks whether a main-window close should hide the window
	// instead. It is consulted live, so disabling the tray icon or
	// close-to-tray takes effect without reopening the window.
	closeToTray func() bool
	// accelerators are the menu shortcuts every window handles at window
	// scope. SetAccelerators replaces them when the menu is rebuilt.
	accelerators []acceleratorBinding
	// shortcuts are binding-derived global actions. They sit beside the menu
	// accelerators at window scope, and the dispatcher omits any chord a
	// terminal owns so terminal copy and SIGINT keep precedence.
	shortcuts []keyShortcut
	// keyboardGrab reports whether the main window's focused pane has taken
	// every chord for the guest. detachedKeyboardGrab does the same for one
	// detached Tab's window. When either is true that window registers no
	// shortcuts at all, so MyGo delivers the keys to the pane instead of an
	// accelerator; other windows keep their accelerators.
	keyboardGrab         func() bool
	detachedKeyboardGrab func(model.TabID) bool
	// dropdownKeyboardGrab reports whether the Pane the Dropdown Panel renders
	// has grabbed the keyboard: the Quake workspace's active Pane in own mode,
	// or the main Workspace's active Pane in follow and top-down modes.
	dropdownKeyboardGrab func() bool
	// focusedWindow resolves the window a viewer action (such as fullscreen)
	// targets when it runs, so a detached VNC Pane drives its own window
	// rather than the main one. Nil reads MyGo's focused window.
	focusedWindow func() hostWindow
	// opacity is the live Appearance setting applied to every window.
	opacity float64
	// theme and icon read the live Appearance settings. Both are resolved per
	// frame so an open window follows a change without being recreated.
	theme func() *ui.Theme
	icon  func() []byte
	// style reads the live Interface Style, resolved per frame beside the
	// theme for the same reason.
	style func() kit.Style
	// confirmPolicy asks the Security preference whether closing Sessions
	// needs a confirmation. Nil keeps the original behavior (always ask).
	confirmPolicy func() bool
	// quakeSessionCount reports the Dropdown Panel's live Quake Sessions. Quit
	// intent counts them beside the shell's Sessions, so a Quake Tab cannot be
	// discarded without the user's confirmation. Nil means the build has no
	// Quake Workspace.
	quakeSessionCount func() int
	// transferGuard counts unfinished Transfers at quit time. Nil leaves the
	// quit guard off, as for hosts without a Transfer queue.
	transferGuard transferQuitGuard
	// onFocus runs after any window gains focus, beside the App Lock activity
	// record. The sync engine uses it for its debounced focus trigger.
	onFocus func()
	// windowsMu guards the window collections that windows() enumerates. A
	// background redraw can call it while the UI thread creates or destroys a
	// window, so the list must be built under a lock even though every writer
	// is otherwise UI-thread only. Per-window state is not covered.
	windowsMu sync.RWMutex
	main      hostWindow
	settings  hostWindow
	detached  map[model.TabID]hostWindow
	// panels are auxiliary native windows that follow the shared appearance
	// and invalidation paths but never keep the application alive. The
	// Dropdown Panel is the first one.
	panels []hostWindow
	// dropdown is the singleton Dropdown Panel. It is created on its first
	// show and only hidden afterwards until quit.
	dropdown hostWindow
	// dropdownContent supplies the Panel's routed view. The provider is
	// resolved once at creation; the returned function runs every frame.
	dropdownContent func() func(*ui.Context)
	// dropdownBar supplies the routed view's own top-bar items, drawn beside
	// the frame's hide button in the same row. It is resolved once at
	// creation, like dropdownContent.
	dropdownBar func() func(*ui.Context)
	// dropdownContentSkip and dropdownBarSkip run on a frame the App Lock gate
	// replaced the routed content or bar slot. They mark the per-view motion
	// notices so the route's runtimes warp an event the user never saw
	// instead of animating it on the unlock frame. Nil is allowed: a host
	// without motion runtimes behind the Panel has nothing to mark.
	dropdownContentSkip func()
	dropdownBarSkip     func()
	// dropdownShow runs before the Panel is created or shown. The composition
	// root uses it to activate the routed workspace so its first frame already
	// has content.
	dropdownShow func()
	// dropdownDismiss handles a Panel close request before the Panel hides. An
	// open flyout inside the Panel, such as the Quake Host picker, consumes the
	// first Escape; the next one hides the Panel. Nil always hides.
	dropdownDismiss func() bool
	// dropdownHeight is the live visible-height calculation in DIPs. The
	// default uses half the work area.
	dropdownHeight func(windowBounds) int
	// dropdownHideOnBlur and dropdownHideOnEscape gate the two hide signals
	// MyGo reports. Nil keeps the original behavior (hide on either). The
	// click-outside gesture has no separate signal here: it is the blur
	// signal, so HideOnBlur is its gate too; see Settings.HideOnClickOutside.
	dropdownHideOnBlur   func() bool
	dropdownHideOnEscape func() bool
	// dropdownOwnsRender reports whether the currently routed dropdown mode
	// and content source take the main Workspace's surfaces. Follow and
	// top-down do; Quake's own Workspace does not.
	dropdownOwnsRender func() bool
	// renderOwner is the window that renders the shell surfaces this frame.
	// The manager keeps the invariant that at most one window draws a Surface.
	renderOwner state.RenderOwner
	// mainVisibleBeforeDropdown remembers whether showing the dropdown hid a
	// visible main window, so hiding it restores only that state.
	mainVisibleBeforeDropdown bool
	// dropdownClosing lets the Panel's close request proceed during quit;
	// otherwise every close request only hides it.
	dropdownClosing bool
	// screen reports the displays and pointer. Nil reads MyGo's live screen.
	screen     screenProbe
	forceClose map[string]bool
	quitting   bool
	// mainFocuses counts FocusMain calls so the desktop test can observe that
	// a second launch raised the existing main window.
	mainFocuses int
}

func newWindowManager(host windowHost, confirmer dialogConfirmer, gate *uiapplock.Gate,
	shellState *state.State, settingsContent func() func(*ui.Context),
	locale func() string) *windowManager {
	return &windowManager{
		host:            host,
		confirmer:       confirmer,
		gate:            gate,
		state:           shellState,
		settingsContent: settingsContent,
		locale:          locale,
		detached:        make(map[model.TabID]hostWindow),
		forceClose:      make(map[string]bool),
		renderOwner:     state.RenderOwnerMain,
	}
}

// SetAccelerators installs the menu shortcuts every window dispatches. It is
// called whenever the menu is built, including after a language change.
func (m *windowManager) SetAccelerators(bindings []acceleratorBinding) {
	m.accelerators = bindings
}

// SetShortcuts installs binding-derived global actions beside the menu
// accelerators. It is called whenever the bindings are rebuilt.
func (m *windowManager) SetShortcuts(shortcuts []keyShortcut) {
	m.shortcuts = shortcuts
}

// SetKeyboardGrabProvider installs the predicates that report a focused pane
// has grabbed the keyboard: main for the main window, detached for a detached
// Tab's window. They are read on every frame, so a grab toggled in the chrome
// takes effect without rebuilding any table, and one window's grab never
// silences another window's accelerators.
func (m *windowManager) SetKeyboardGrabProvider(main func() bool, detached func(model.TabID) bool) {
	m.keyboardGrab = main
	m.detachedKeyboardGrab = detached
}

// SetDropdownKeyboardGrabProvider installs the accelerator gate for the
// Dropdown Panel's routed content. The Panel renders either the Quake
// workspace or the main Workspace, and each mode resolves its own active Pane.
func (m *windowManager) SetDropdownKeyboardGrabProvider(fn func() bool) {
	m.dropdownKeyboardGrab = fn
}

// ToggleFocusedFullScreen toggles the native fullscreen state of the window
// hosting the action at the moment it runs. MyGo resolves the focused window,
// so the same VNC Surface toggles the main window while it is there and its
// detached window after it moves. The Dropdown Panel never offers this action.
// focusedWindow is the injectable seam for offscreen tests.
func (m *windowManager) ToggleFocusedFullScreen() {
	window := m.focusedWindowValue()
	if window != nil && !window.IsDestroyed() {
		window.ToggleFullScreen()
	}
}

// focusedWindowValue resolves the window an action targets: the injected
// provider first, then MyGo's focused window.
func (m *windowManager) focusedWindowValue() hostWindow {
	if m.focusedWindow != nil {
		return m.focusedWindow()
	}
	if focused := mygo.FocusedWindow(); focused != nil {
		return mygoHostWindow{Window: focused}
	}
	return nil
}

// SetOpacity applies the Appearance opacity to every open window and to
// windows created later.
func (m *windowManager) SetOpacity(value float64) {
	m.opacity = value
	m.each(func(window hostWindow) { window.SetOpacity(value) })
	// The vibrancy depends on the opacity.
	m.SyncStyle()
}

// managedWindow is one entry of the manager's single window enumeration. A
// new window kind joins here once; every shared per-window operation walks
// this list, so a kind never needs its own appearance, redraw, locale, or
// quit-count loop. counts reports whether an open window keeps the
// application alive when it is the last one; title resolves the OS title a
// locale change writes, and nil leaves the title alone.
type managedWindow struct {
	window hostWindow
	counts bool
	title  func(locales.Messages) string
}

// windows returns every window the manager owns. This is the one place a new
// window kind is added: everything that applies to all windows, or that
// decides what keeps the app alive, derives from this list.
func (m *windowManager) windows() []managedWindow {
	m.windowsMu.RLock()
	defer m.windowsMu.RUnlock()
	windows := make([]managedWindow, 0, 2+len(m.detached)+len(m.panels))
	windows = append(windows,
		managedWindow{
			window: m.main,
			counts: true,
			title:  func(messages locales.Messages) string { return messages.AppName() },
		},
		managedWindow{
			window: m.settings,
			counts: true,
			title:  func(messages locales.Messages) string { return messages.NavSettings() },
		},
	)
	for tabID, window := range m.detached {
		id := tabID
		windows = append(windows, managedWindow{
			window: window,
			counts: true,
			title: func(messages locales.Messages) string {
				info, ok := m.state.DetachedTabInfo(id)
				if !ok {
					return ""
				}
				return shell.DetachedTitle(messages, info)
			},
		})
	}
	for _, window := range m.panels {
		// A panel never keeps the app alive, and it draws no OS title.
		windows = append(windows, managedWindow{window: window, counts: false})
	}
	return windows
}

// each runs fn on every open window.
func (m *windowManager) each(fn func(hostWindow)) {
	for _, managed := range m.windows() {
		if managed.window != nil && !managed.window.IsDestroyed() {
			fn(managed.window)
		}
	}
}

// create opens a host window and applies the manager's shared appearance. A
// spec with Bounds is positioned before it can be shown: MyGo creates Hidden
// windows invisibly, so the first Show lands at the requested rectangle with
// no flash at a stale position.
func (m *windowManager) create(spec windowSpec, hooks windowHooks) hostWindow {
	window := m.host.Create(spec, hooks)
	if spec.Bounds != nil {
		window.SetBounds(*spec.Bounds)
	}
	m.applyOpacity(window)
	m.applyVibrancy(window)
	m.applyIcon(window)
	return window
}

// currentStyle is the live Interface Style.
func (m *windowManager) currentStyle() kit.Style {
	if m.style != nil {
		if style := m.style(); style != nil {
			return style
		}
	}
	return kit.Classic
}

// vibrancy is the material a window shows behind its sidebar: the sidebar's
// when the style is vibrant and the window opaque. A see-through window
// would show the desktop through the material as well, so it shows none.
func (m *windowManager) vibrancy() mygo.Vibrancy {
	if !kit.Vibrant(m.currentStyle()) || (m.opacity > 0 && m.opacity < 1) {
		return mygo.VibrancyNone
	}
	return mygo.VibrancySidebar
}

// SyncStyle applies the Interface Style's window material to every open
// window, after the style or the opacity changed. The frame itself follows
// the style on its own; see withTheme.
func (m *windowManager) SyncStyle() {
	v := m.vibrancy()
	m.each(func(window hostWindow) { window.SetVibrancy(v) })
}

// SetThemeProvider installs the live widget theme. Every window calls it at
// its root, before the App Lock gate builds anything.
func (m *windowManager) SetThemeProvider(fn func() *ui.Theme) { m.theme = fn }

// SetMainContentProvider installs the root view of the main window. The
// provider is resolved when the window is created; the function it returns
// runs every frame, so render ownership can change without recreating the
// window. Nil falls back to the plain shell view.
func (m *windowManager) SetMainContentProvider(fn func() func(*ui.Context)) {
	m.mainContent = fn
}

func (m *windowManager) mainView() func(*ui.Context) {
	if m.mainContent != nil {
		if content := m.mainContent(); content != nil {
			return content
		}
	}
	return shell.View(m.state)
}

// SetStyleProvider sets how each window resolves the Interface Style.
func (m *windowManager) SetStyleProvider(fn func() kit.Style) { m.style = fn }

// SetIconProvider installs the live application icon. Windows created later
// read it, and SetIcon applies a change to every open window.
func (m *windowManager) SetIconProvider(fn func() []byte) {
	m.icon = fn
	m.applyIconEverywhere()
}

// SetIcon applies the current icon provider to every open window. A window
// whose host cannot set an icon is left alone.
func (m *windowManager) applyIconEverywhere() {
	data := m.currentIcon()
	if data == nil {
		return
	}
	m.each(func(window hostWindow) { _ = window.SetIcon(data) })
}

func (m *windowManager) currentIcon() []byte {
	if m.icon == nil {
		return nil
	}
	return m.icon()
}

// SetSessionConfirmation installs the live Security preference. When it
// returns false, closing Sessions skips the dialog.
func (m *windowManager) SetSessionConfirmation(enabled func() bool) {
	m.confirmPolicy = enabled
}

// SetTransferGuard installs the unfinished-Transfer check the quit runs after
// the Session confirmation. A nil guard skips the check.
func (m *windowManager) SetTransferGuard(guard transferQuitGuard) {
	m.transferGuard = guard
}

func (m *windowManager) applyIcon(window hostWindow) {
	if window == nil {
		return
	}
	if data := m.currentIcon(); data != nil {
		_ = window.SetIcon(data)
	}
}

func (m *windowManager) applyOpacity(window hostWindow) {
	if window == nil || m.opacity <= 0 {
		return
	}
	window.SetOpacity(m.opacity)
}

func (m *windowManager) applyVibrancy(window hostWindow) {
	if window == nil {
		return
	}
	if v := m.vibrancy(); v != mygo.VibrancyNone {
		window.SetVibrancy(v)
	}
}

// withTheme applies the live widget theme at the window's root context. It
// runs before the gate and the content, so every surface is painted with the
// current preset, accent, and interface font.
func (m *windowManager) withTheme(content func(*ui.Context)) func(*ui.Context) {
	return func(c *ui.Context) {
		if m.theme != nil {
			if theme := m.theme(); theme != nil {
				c.SetTheme(theme)
			}
		}
		style := m.currentStyle()
		kit.SetStyle(style)
		c.SetTheme(kit.StyleTheme(c, style, c.Theme()))
		content(c)
	}
}

// withAccelerators binds the menu shortcuts at the window's root context,
// before the App Lock gate or the content builds anything. MyGo then handles
// them as window shortcuts, ahead of the focused terminal's input handler.
// grab is that window's keyboard-grab predicate; nil means it never grabs.
func (m *windowManager) withAccelerators(grab func() bool, content func(*ui.Context)) func(*ui.Context) {
	return func(c *ui.Context) {
		if grab != nil && grab() {
			// The guest owns every chord in this window. Registering even a
			// no-op shortcut would claim the key before the focused pane could
			// send it.
			content(c)
			return
		}
		bindAccelerators(c, m.accelerators)
		bindShortcuts(c, m.shortcuts)
		content(c)
	}
}

// withFrame draws the window controls over the top-right corner of the
// content, outside the App Lock gate so a locked window can still be moved
// aside or closed. window is resolved per frame: the content is built before
// the host creates the window.
func withFrame(style titlebar.Style, window func() hostWindow, content func(*ui.Context)) func(*ui.Context) {
	return titlebar.Frame(style, func() titlebar.Window {
		if w := window(); w != nil && !w.IsDestroyed() {
			return w
		}
		return nil
	}, content)
}

// wrap puts every window's content behind the one shared App Lock gate. The
// current gate is resolved per frame, so replacing it also governs windows
// that already exist, and no window builds its content while locked.
//
// skipped, when non-nil, runs on a frame the gate replaced the content with
// some other slot: the lock screen, or nothing for chrome. A locked-app frame
// is a frame the content's motion runtimes never saw, so the composition root
// uses the callback to record the per-view unrendered notice; the view then
// warps the events it missed instead of animating them when the unlock frame
// arrives. The gate is asked rather than pre-checked because it can lock
// itself while wrapping (an idle timeout), which is exactly such a frame.
func (m *windowManager) wrap(content func(*ui.Context), skipped func()) func(*ui.Context) {
	return func(c *ui.Context) {
		if m.gate == nil {
			content(c)
			return
		}
		built := false
		m.gate.Wrap(func(inner *ui.Context) {
			built = true
			content(inner)
		})(c)
		if !built && skipped != nil {
			skipped()
		}
	}
}

// wrapChrome gates a window's shared chrome, such as the Dropdown Panel's
// routed bar. While locked the chrome renders nothing; the content slot draws
// the one lock screen, so chrome that shares a row with the frame's own
// controls does not paint a second one. skipped mirrors wrap's: it runs on a
// frame the chrome slot built nothing.
func (m *windowManager) wrapChrome(content func(*ui.Context), skipped func()) func(*ui.Context) {
	return func(c *ui.Context) {
		if m.gate == nil {
			content(c)
			return
		}
		built := false
		m.gate.WrapChrome(func(inner *ui.Context) {
			built = true
			content(inner)
		})(c)
		if !built && skipped != nil {
			skipped()
		}
	}
}

// detachedContent is the content of a detached Tab's window: the Tab's view
// behind the same shared App Lock gate as every other window.
func (m *windowManager) detachedContent(tabID model.TabID) func(*ui.Context) {
	window := func() hostWindow { return m.detached[tabID] }
	grab := func() bool {
		return m.detachedKeyboardGrab != nil && m.detachedKeyboardGrab(tabID)
	}
	content := m.withTheme(m.withAccelerators(grab, withFrame(titlebar.Popup, window,
		m.wrap(shell.DetachedView(m.state, tabID), func() {
			// The lock screen replaced this window's Tab. Mark its own
			// renderer: each detached window owns one Pane runtime for its
			// fixed Tab, so the notice is keyed by that Tab, not by view kind.
			m.state.NoteDetachedViewUnrendered(tabID)
		}))))
	return m.withDetachedTitle(tabID, content)
}

// withDetachedTitle keeps a detached window's OS title in step with the Tab's
// resolved title. The title can change after the window opens: a shell reports
// OSC 0/2 titles, and focusing another Pane changes a split Tab's name. The
// title written to the window is the same one its header draws, so the taskbar
// and the header never disagree.
func (m *windowManager) withDetachedTitle(tabID model.TabID, content func(*ui.Context)) func(*ui.Context) {
	var last string
	return func(c *ui.Context) {
		if info, ok := m.state.DetachedTabInfo(tabID); ok {
			title := shell.DetachedTitle(locales.In(c.Locale()), info)
			if title != last {
				if window := m.detached[tabID]; window != nil && !window.IsDestroyed() {
					window.SetTitle(title)
				}
				last = title
			}
		}
		content(c)
	}
}

func (m *windowManager) messages() locales.Messages {
	if m.locale == nil {
		return locales.In("en")
	}
	return locales.In(m.locale())
}

// OpenMain creates the main window or focuses the existing one.
func (m *windowManager) OpenMain() hostWindow {
	if m.main != nil && !m.main.IsDestroyed() {
		m.main.Show()
		m.main.Focus()
		return m.main
	}
	// The content is inside the gate: while App Lock is closed no window
	// builds anything. The view is a provider so a later render-ownership
	// route can change what the main window shows without recreating it.
	gated := withFrame(titlebar.Tabs, func() hostWindow { return m.main }, m.wrap(m.mainView(), func() {
		// The lock screen replaced the main Workspace. Mark its renderer so
		// the first unlocked frame warps Pane and Tab events recorded while
		// it was not on screen (the production commands and shortcuts stay
		// bound at the window root, outside the gate).
		m.state.NoteMotionViewUnrendered(state.MotionViewMain)
	}))
	// withAccelerators binds at the root context, before the gate builds its
	// container, so MyGo registers window shortcuts. Inside the gate they
	// would become element-scoped and fail from the Vault view with no
	// terminal focus.
	content := m.withTheme(m.withAccelerators(m.keyboardGrab, gated))
	window := m.create(windowSpec{
		Key:       "main",
		Title:     m.messages().AppName(),
		StateKey:  "main",
		Width:     1100,
		Height:    720,
		MinWidth:  720,
		MinHeight: 480,
		Chrome:    titlebar.Tabs,
		Content:   content,
	}, windowHooks{
		OnFocus:        m.recordActivity,
		OnCloseRequest: m.allowMainClose,
		OnClosed: func() {
			m.state.Persist()
			// A detached window keeps the app alive: closing the main window
			// ends only its own Sessions. Their resumable credentials survive so
			// the next app run can reattach; MyGo calls OnWillQuit only after
			// every window closed, so this close is the last chance to preserve
			// them.
			m.state.CloseMainSessionsPreserving()
			m.windowsMu.Lock()
			m.main = nil
			m.windowsMu.Unlock()
			m.markQuittingIfNoWindows()
		},
	})
	m.windowsMu.Lock()
	m.main = window
	m.windowsMu.Unlock()
	return window
}

// Main returns the main window, or nil before it is created.
func (m *windowManager) Main() hostWindow {
	m.windowsMu.RLock()
	defer m.windowsMu.RUnlock()
	return m.main
}

// OpenSettings creates the singleton Settings window or focuses the existing
// one. Settings holds no Sessions, so it never asks close intent. It uses the
// same 1100x720 default and 720x480 minimum as the main window because the
// desktop integration sections need a readable, scrollable layout.
//
// Unlike the workspace windows, Settings does not bind the application's
// shortcut tables. MyGo sends a registered window shortcut to its handler
// before a focused field sees the key, which would make the shortcut recorder
// impossible to use for any chord an application binding already owns
// (Ctrl+B, Ctrl+T, and so on). The window's own controls still handle typing,
// Enter, and Escape; menu items remain reachable by pointer.
func (m *windowManager) OpenSettings() {
	if m.settings != nil && !m.settings.IsDestroyed() {
		m.settings.Show()
		m.settings.Focus()
		return
	}
	if m.settingsContent == nil {
		return
	}
	window := m.create(windowSpec{
		Key:       "settings",
		Title:     m.messages().NavSettings(),
		StateKey:  "settings",
		Width:     1100,
		Height:    720,
		MinWidth:  720,
		MinHeight: 480,
		Chrome:    titlebar.Dialog,
		Content:   m.withTheme(withFrame(titlebar.Dialog, func() hostWindow { return m.settings }, m.wrap(m.settingsContent(), nil))),
	}, windowHooks{
		OnFocus: m.recordActivity,
		// Settings owns no Sessions, but closing the last counted window quits
		// and would end the Quake Workspace's live Sessions.
		OnCloseRequest: func() bool { return m.allowQuakeClose(m.settings) },
		OnClosed: func() {
			m.windowsMu.Lock()
			m.settings = nil
			m.windowsMu.Unlock()
			m.markQuittingIfNoWindows()
		},
	})
	m.windowsMu.Lock()
	m.settings = window
	m.windowsMu.Unlock()
}

// OpenDetached creates the window that owns one detached Tab. Every window,
// including windows created after App Lock engaged, gets the shared gate.
func (m *windowManager) OpenDetached(tabID model.TabID) {
	if window := m.detached[tabID]; window != nil && !window.IsDestroyed() {
		window.Show()
		window.Focus()
		return
	}
	info, ok := m.state.DetachedTabInfo(tabID)
	if !ok {
		return
	}
	content := m.detachedContent(tabID)
	window := m.create(windowSpec{
		Key:       "detached:" + string(tabID),
		Title:     shell.DetachedTitle(m.messages(), info),
		Width:     900,
		Height:    600,
		MinWidth:  480,
		MinHeight: 320,
		Chrome:    titlebar.Popup,
		Content:   content,
	}, windowHooks{
		OnFocus: func() {
			m.recordActivity()
			// Focusing the detached window acknowledges its bell marker, the
			// same way selecting a main Tab clears its activity marker.
			m.state.ClearDetachedBell(tabID)
		},
		OnBlur: func() {
			// A bell that arrives while the window is already focused has no
			// focus transition to acknowledge it, so leaving the window is the
			// acknowledgement. ClearDetachedBell is idempotent.
			m.state.ClearDetachedBell(tabID)
		},
		OnCloseRequest: func() bool { return m.allowDetachedClose(tabID) },
		OnClosed: func() {
			m.windowsMu.Lock()
			delete(m.detached, tabID)
			m.windowsMu.Unlock()
			delete(m.forceClose, "detached:"+string(tabID))
			// Detached Tabs are not part of the persisted Workspace, so a
			// saved credential pair could never be reattached after a restart.
			// Closing the window therefore clears it, whether this is a quit or
			// an explicit close.
			// Idempotent: a re-attached Tab is no longer detached, so its
			// Sessions stay alive in the main window.
			m.state.CloseDetachedTab(tabID)
			m.markQuittingIfNoWindows()
		},
	})
	m.windowsMu.Lock()
	m.detached[tabID] = window
	m.windowsMu.Unlock()
}

// ReattachClose closes a detached window after its Tab moved back into the
// main Workspace. The move itself already kept the Sessions alive.
func (m *windowManager) ReattachClose(tabID model.TabID) {
	m.forceClose["detached:"+string(tabID)] = true
	if window := m.detached[tabID]; window != nil && !window.IsDestroyed() {
		window.Close()
	}
}

// SetScreenProvider installs the display seam the Dropdown Panel positions
// itself against. Nil reads MyGo's live displays.
func (m *windowManager) SetScreenProvider(screen screenProbe) { m.screen = screen }

// SetDropdownContentProvider installs the Panel's routed content. The
// provider is resolved once when the Panel is created; the function it
// returns runs every frame, so later content routing does not recreate the
// native window. Nil leaves the neutral placeholder.
func (m *windowManager) SetDropdownContentProvider(fn func() func(*ui.Context)) {
	m.dropdownContent = fn
}

// SetDropdownBarProvider installs the Panel's routed top-bar items. The
// provider is resolved once when the Panel is created; the function it returns
// runs every frame, so switching Tabs changes the strip without recreating the
// native window. Nil leaves the bar's leading space empty.
func (m *windowManager) SetDropdownBarProvider(fn func() func(*ui.Context)) {
	m.dropdownBar = fn
}

// SetDropdownContentSkip installs the notice the Panel's content slot runs on
// a frame the App Lock gate replaced it. The composition root marks every
// routed view that keeps a motion runtime; nil leaves such frames unmarked.
func (m *windowManager) SetDropdownContentSkip(fn func()) {
	m.dropdownContentSkip = fn
}

// SetDropdownBarSkip installs the notice the Panel's routed bar runs on a
// frame the App Lock gate skipped it. Nil leaves such frames unmarked.
func (m *windowManager) SetDropdownBarSkip(fn func()) {
	m.dropdownBarSkip = fn
}

// SetDropdownShowHook installs the work the composition root runs before the
// Panel is created or shown. The Quake workspace creates its first local Shell
// here, so the Panel's first frame already has content.
func (m *windowManager) SetDropdownShowHook(fn func()) {
	m.dropdownShow = fn
}

// SetDropdownDismissHandler installs the work that consumes a Panel close
// request before the Panel hides. The Quake Host picker uses it so the first
// Escape closes the picker and the next hides the Panel. Nil keeps the old
// behavior: every close request hides.
func (m *windowManager) SetDropdownDismissHandler(fn func() bool) {
	m.dropdownDismiss = fn
}

// SetDropdownHeightProvider installs the live visible-height calculation in
// DIPs for a work area. Nil keeps the default half height; the height
// settings replace it. It is asked on every show, so a changed height takes
// effect the next time the Panel appears.
func (m *windowManager) SetDropdownHeightProvider(fn func(windowBounds) int) {
	m.dropdownHeight = fn
}

// SetDropdownHideOnBlurProvider installs the live blur-hide switch. Blur is
// also the only click-outside signal MyGo reports, so this switch gates that
// gesture too; the platform note lives on Settings.HideOnClickOutside. Nil
// keeps the original behavior (hide on blur).
func (m *windowManager) SetDropdownHideOnBlurProvider(fn func() bool) {
	m.dropdownHideOnBlur = fn
}

// SetDropdownHideOnEscapeProvider installs the live Escape-hide switch. Nil
// keeps the original behavior (Escape hides the Panel). An Escape that is
// switched off is still answered: the close request is vetoed without hiding
// or destroying the Panel.
func (m *windowManager) SetDropdownHideOnEscapeProvider(fn func() bool) {
	m.dropdownHideOnEscape = fn
}

// SetDropdownOwnershipProvider installs the rule that says whether the current
// dropdown route renders the main Workspace's Surfaces. Follow and top-down
// do; Quake's own Workspace does not. The provider is read when the Panel is
// shown, so a route change applies to the next show without wiring new
// content.
func (m *windowManager) SetDropdownOwnershipProvider(fn func() bool) {
	m.dropdownOwnsRender = fn
}

// DropdownOwnsMain reports whether the Dropdown Panel currently renders the
// main Workspace's Surfaces. The main window renders a neutral placeholder
// while it is true, so one Surface is never drawn by two windows at once.
func (m *windowManager) DropdownOwnsMain() bool {
	return m.renderOwner == state.RenderOwnerDropdown
}

// syncRenderOwnership moves the Workspace surfaces to the window that the
// routed dropdown mode names. Transferring hides a visible main window and
// remembers that it was visible; keeping the ownership leaves it alone.
func (m *windowManager) syncRenderOwnership() {
	if m.dropdownOwnsRender != nil && m.dropdownOwnsRender() {
		m.transferToDropdown()
		return
	}
	m.restoreMainOwnership(true)
}

// transferToDropdown gives the Dropdown Panel the Workspace surfaces for the
// first time. The main window is hidden rather than destroyed, so showing it
// again later is cheap and its native content is never rebuilt.
func (m *windowManager) transferToDropdown() {
	if m.renderOwner == state.RenderOwnerDropdown {
		return
	}
	m.renderOwner = state.RenderOwnerDropdown
	m.mainVisibleBeforeDropdown = m.main != nil && !m.main.IsDestroyed() && m.main.IsVisible()
	if m.mainVisibleBeforeDropdown {
		m.main.Hide()
	}
	m.invalidateRenderWindows()
}

// restoreMainOwnership returns the Workspace surfaces to the main window. It
// shows the main window again only when showing the Panel hid it; a main
// window the user had already hidden stays hidden, so hiding the Panel never
// pops a window the user put away. show is false for shutdown paths that must
// not resurrect a window during quit.
func (m *windowManager) restoreMainOwnership(show bool) {
	if m.renderOwner != state.RenderOwnerDropdown {
		return
	}
	m.renderOwner = state.RenderOwnerMain
	wasVisible := m.mainVisibleBeforeDropdown
	m.mainVisibleBeforeDropdown = false
	if show && wasVisible && m.main != nil && !m.main.IsDestroyed() {
		m.main.Show()
		m.main.Focus()
	}
	m.invalidateRenderWindows()
}

// invalidateRenderWindows redraws both windows after ownership moved, so the
// losing window drops the Surfaces on its next frame and the gaining one
// paints them.
func (m *windowManager) invalidateRenderWindows() {
	if m.main != nil && !m.main.IsDestroyed() {
		m.main.Invalidate()
	}
	if m.dropdown != nil && !m.dropdown.IsDestroyed() {
		m.dropdown.Invalidate()
	}
}

// DropdownVisible reports whether the Dropdown Panel is on screen.
func (m *windowManager) DropdownVisible() bool {
	return m.dropdown != nil && !m.dropdown.IsDestroyed() && m.dropdown.IsVisible()
}

// ToggleDropdown shows the Dropdown Panel, or hides it when it is visible.
func (m *windowManager) ToggleDropdown() {
	if m.DropdownVisible() {
		m.HideDropdown()
		return
	}
	m.ShowDropdown()
}

// ShowDropdown creates the singleton Panel on its first use and shows it.
// Every show re-reads the pointer and recomputes the display, work area, and
// bounds before Show, so a moved pointer, a changed work area, or a removed
// display is honored. The show hook runs first, so the routed workspace exists
// before the content function is resolved. The Panel has no StateKey: MyGo
// never restores stale bounds for it.
func (m *windowManager) ShowDropdown() {
	if m.dropdownShow != nil {
		m.dropdownShow()
	}
	// Move the Workspace surfaces before the first frame is built, so a follow
	// or top-down Panel paints them and the main window paints its placeholder
	// for the same frame. The decision is read live; a source change made this
	// show is already reflected without rewiring the Panel.
	m.syncRenderOwnership()
	bounds := m.dropdownBounds()
	window := m.dropdown
	if window == nil || window.IsDestroyed() {
		window = m.createDropdown(bounds)
	} else {
		window.SetBounds(bounds)
	}
	window.Show()
	window.Focus()
}

// HideDropdown hides the Panel without destroying it, so the next show reuses
// the same native window and its content state. When the Panel had taken the
// Workspace surfaces, hiding it returns them to the main window first.
func (m *windowManager) HideDropdown() {
	if m.dropdown != nil && !m.dropdown.IsDestroyed() {
		m.dropdown.Hide()
	}
	m.restoreMainOwnership(true)
}

// ShutdownDropdown hides and destroys the Panel for quit. It answers the
// close request instead of vetoing it, and never prompts, so quitting cannot
// block on the Panel or leave a native window behind.
func (m *windowManager) ShutdownDropdown() {
	window := m.dropdown
	if window == nil || window.IsDestroyed() {
		return
	}
	m.dropdownClosing = true
	window.Hide()
	// Quit destroys the Panel without showing anything: the surfaces go back
	// to the main window only for whatever frame remains, never by popping a
	// window open during shutdown.
	m.renderOwner = state.RenderOwnerMain
	m.mainVisibleBeforeDropdown = false
	window.Close()
}

// createDropdown creates the hidden Panel at bounds and registers it with the
// shared window enumeration. Hidden means the window is created invisibly and
// its bounds are applied before the first Show, so it never flashes at a stale
// position.
func (m *windowManager) createDropdown(bounds windowBounds) hostWindow {
	var window hostWindow
	window = m.create(windowSpec{
		Key:     "dropdown",
		Title:   m.messages().AppName(),
		Panel:   true,
		Hidden:  true,
		Bounds:  &bounds,
		Width:   bounds.Width,
		Height:  bounds.Height,
		Content: m.dropdownView(),
	}, windowHooks{
		OnFocus: m.recordActivity,
		// Blur is also the platform's "click outside" signal; both hide the
		// Panel and neither destroys it. The switch is read live, so turning
		// the trigger off keeps an already-open Panel visible when it loses
		// focus. With no separate mouse signal, clicking outside shares this
		// gate: turning it off makes the click-outside trigger unavailable
		// too, as the Settings note explains.
		OnBlur: func() {
			if m.dropdownHideOnBlur != nil && !m.dropdownHideOnBlur() {
				return
			}
			m.HideDropdown()
		},
		// Escape reaches a MyGo Panel as a close request. It hides the Panel
		// instead of closing it, unless the application is quitting. MyGo's
		// quit sequence closes every window before OnWillQuit runs, so the
		// already-confirmed quit has to answer the close here: a veto would
		// abort the quit and leave the process alive with no main window.
		OnCloseRequest: func() bool {
			if m.dropdownClosing || m.quitting {
				return true
			}
			// An open flyout inside the Panel gets the first close request,
			// so Escape dismisses the Host picker before the Panel hides. A
			// locked App Lock gate replaces the content with the lock screen,
			// so no flyout is on screen then.
			if m.dropdownDismiss != nil && m.dropdownContentRendered() && m.dropdownDismiss() {
				return false
			}
			if m.dropdownHideOnEscape != nil && !m.dropdownHideOnEscape() {
				return false
			}
			m.HideDropdown()
			return false
		},
		// A destroyed dropdown leaves neither the manager's field nor the
		// window enumeration behind, and it never counts toward quit. If it is
		// destroyed while the application keeps running it must not leave the
		// Workspace surfaces owned by a window that no longer exists.
		OnClosed: func() {
			m.dropdownClosing = false
			m.forgetDropdown(window)
			m.restoreMainOwnership(!m.quitting)
		},
	})
	m.windowsMu.Lock()
	m.dropdown = window
	m.panels = append(m.panels, window)
	m.windowsMu.Unlock()
	return window
}

// forgetDropdown clears a destroyed Panel from the manager.
func (m *windowManager) forgetDropdown(window hostWindow) {
	m.windowsMu.Lock()
	defer m.windowsMu.Unlock()
	if m.dropdown == window {
		m.dropdown = nil
	}
	for index, panel := range m.panels {
		if panel != window {
			continue
		}
		m.panels = append(m.panels[:index], m.panels[index+1:]...)
		break
	}
}

// dropdownContentRendered reports whether the Panel's routed content and bar
// are built. The shared App Lock gate replaces the content with the lock
// screen and skips the routed bar while closed, so a flyout inside either is
// not on screen then and must not consume a close request.
func (m *windowManager) dropdownContentRendered() bool {
	return m.gate == nil || !m.gate.Locked()
}

// dropdownView is the Panel's root content: the shared theme and shortcut
// preamble, the dropdown frame with its hide button and routed bar, then the
// routed content behind the one App Lock gate. The frame sits outside the
// gate, as every window's controls do, so the Panel can be hidden while
// locked. The routed bar is gated like content: the Quake tab strip and Host
// picker expose Session and Host names, so locking hides them while the frame
// and its hide button stay usable.
func (m *windowManager) dropdownView() func(*ui.Context) {
	content := dropdown.Placeholder
	if m.dropdownContent != nil {
		if provided := m.dropdownContent(); provided != nil {
			content = provided
		}
	}
	var bar func(*ui.Context)
	if m.dropdownBar != nil {
		if provided := m.dropdownBar(); provided != nil {
			bar = m.wrapChrome(provided, m.dropdownBarSkip)
		}
	}
	// The Dropdown Panel renders either the main Workspace or the Quake
	// workspace, so it gates its accelerators on the Pane that content shows.
	// A VNC Pane there can take the keyboard exactly as one in the main
	// window does.
	return m.withTheme(m.withAccelerators(m.dropdownKeyboardGrab,
		dropdown.View(m.HideDropdown, bar, m.wrap(content, m.dropdownContentSkip))))
}

// dropdownBounds is the Panel's rectangle: the work area's top-left corner,
// its full width, and the configured height.
func (m *windowManager) dropdownBounds() windowBounds {
	workArea := m.dropdownWorkArea()
	return windowBounds{
		X:      workArea.X,
		Y:      workArea.Y,
		Width:  workArea.Width,
		Height: m.dropdownHeightFor(workArea),
	}
}

// dropdownWorkArea picks the work area of the display under the pointer and
// falls back to the primary display when the platform cannot name one. It is
// recomputed for every show.
func (m *windowManager) dropdownWorkArea() windowBounds {
	screen := m.screen
	if screen == nil {
		screen = mygo.Screen
	}
	display := screen.DisplayNearestPoint(screen.CursorScreenPoint())
	if !hasWorkArea(display.WorkArea) {
		display = screen.PrimaryDisplay()
	}
	area := display.WorkArea
	if !hasWorkArea(area) {
		return dropdownFallbackWorkArea
	}
	return windowBounds{X: area.X, Y: area.Y, Width: area.Width, Height: area.Height}
}

func hasWorkArea(area mygo.Rectangle) bool {
	return area.Width > 0 && area.Height > 0
}

// percentHeight returns percent of the work area's height in DIPs.
func percentHeight(workArea windowBounds, percent int) int {
	return int(math.Round(float64(workArea.Height) * float64(percent) / 100))
}

// dropdownHeightFor clamps the visible height to [minimum, work area height].
// A work area shorter than the minimum uses its own height.
func (m *windowManager) dropdownHeightFor(workArea windowBounds) int {
	height := percentHeight(workArea, appdesktop.DefaultQuakeHeight)
	if m.dropdownHeight != nil {
		height = m.dropdownHeight(workArea)
	}
	minimum := dropdownMinHeight
	if workArea.Height < minimum {
		minimum = workArea.Height
	}
	if height < minimum {
		height = minimum
	}
	if height > workArea.Height {
		height = workArea.Height
	}
	return height
}

// RequestDetachedClose handles the detached view's close action with the same
// intent prompt as the native window close button.
func (m *windowManager) RequestDetachedClose(tabID model.TabID) {
	window := m.detached[tabID]
	if window == nil || window.IsDestroyed() {
		return
	}
	if !m.allowDetachedClose(tabID) {
		return
	}
	m.forceClose["detached:"+string(tabID)] = true
	window.Close()
}

func (m *windowManager) allowMainClose() bool {
	if m.quitting {
		return true
	}
	if m.closeToTray != nil && m.closeToTray() {
		if m.main != nil && !m.main.IsDestroyed() {
			m.main.Hide()
		}
		// The close request is canceled: the window and its Sessions stay
		// alive in the tray.
		return false
	}
	count := m.state.ActiveSessionCountMain()
	// Closing the last counted window quits, and quitting also ends the Quake
	// Workspace's Sessions. They belong in this one close confirmation even
	// though they live outside the main Workspace.
	if m.closingLastCountedWindow(m.main) {
		count += m.quakeSessionCountValue()
	}
	return m.confirmSessions(count, false)
}

// SetCloseToTray installs the tray-hiding policy. The window manager asks it
// on every main-window close.
func (m *windowManager) SetCloseToTray(policy func() bool) {
	m.closeToTray = policy
}

// SetFocusHandler installs the callback every window focus runs after the
// App Lock activity record. The handler must not block the UI thread.
func (m *windowManager) SetFocusHandler(fn func()) { m.onFocus = fn }

func (m *windowManager) allowDetachedClose(tabID model.TabID) bool {
	key := "detached:" + string(tabID)
	if m.quitting || m.forceClose[key] {
		return true
	}
	count := m.state.ActiveSessionCountDetached(tabID)
	// A detached window that is the last counted one ends the app and its
	// Quake Sessions too.
	if m.closingLastCountedWindow(m.detached[tabID]) {
		count += m.quakeSessionCountValue()
	}
	return m.confirmSessions(count, false)
}

// allowQuakeClose handles a counted window that owns no Sessions of its own,
// such as Settings. Closing the last such window quits, which also ends the
// live Quake Sessions, so that close still asks about them.
func (m *windowManager) allowQuakeClose(window hostWindow) bool {
	if m.quitting || !m.closingLastCountedWindow(window) {
		return true
	}
	return m.confirmSessions(m.quakeSessionCountValue(), false)
}

// closingLastCountedWindow reports whether closing window leaves no counted
// window open, which makes that close the application's quit trigger.
func (m *windowManager) closingLastCountedWindow(window hostWindow) bool {
	for _, managed := range m.windows() {
		if !managed.counts || managed.window == nil || managed.window.IsDestroyed() {
			continue
		}
		if managed.window == window {
			continue
		}
		return false
	}
	return true
}

// quakeSessionCountValue reads the live Quake Session count, or zero when the
// build has no Quake Workspace.
func (m *windowManager) quakeSessionCountValue() int {
	if m.quakeSessionCount == nil {
		return 0
	}
	return m.quakeSessionCount()
}

// SetQuakeSessionCount installs the live count of the Dropdown Panel's Quake
// Sessions. Quit intent adds it to the shell's count; nil leaves the count
// unchanged for windows wired without a Quake Workspace.
func (m *windowManager) SetQuakeSessionCount(fn func() int) {
	m.quakeSessionCount = fn
}

// ConfirmQuit handles application quit intent once. It reports whether the
// quit may proceed and remembers the decision for the window closes that
// follow. The count includes the Quake Workspace's Sessions: quitting ends
// them too, so the user is asked once about every Session that will end.
// Unfinished Transfers are asked about after the Session confirmation;
// declining either question leaves the application running and the Transfers
// untouched.
func (m *windowManager) ConfirmQuit() bool {
	if m.quitting {
		return true
	}
	count := m.state.ActiveSessionCount()
	if m.quakeSessionCount != nil {
		count += m.quakeSessionCount()
	}
	if !m.confirmSessions(count, true) {
		return false
	}
	if !m.confirmTransfers() {
		return false
	}
	m.quitting = true
	return true
}

// Quitting reports whether quit intent is already confirmed.
func (m *windowManager) Quitting() bool { return m.quitting }

func (m *windowManager) confirmSessions(count int, quitting bool) bool {
	if count <= 0 {
		return true
	}
	if m.confirmPolicy != nil && !m.confirmPolicy() {
		return true
	}
	if m.confirmer == nil {
		// A host without dialogs (tests, headless) must still make a
		// deterministic decision. Keeping the window open is the safe default.
		return false
	}
	messages := m.messages()
	prompt := closePrompt{
		Title:   messages.CloseWindowTitle(),
		Message: messages.CloseWindowMessage(count),
		Confirm: messages.CloseWindowConfirm(),
		Cancel:  messages.CloseWindowCancel(),
	}
	if quitting {
		prompt.Title = messages.QuitConfirmTitle()
		prompt.Message = messages.QuitConfirmMessage(count)
		prompt.Confirm = messages.QuitConfirmConfirm()
	}
	return m.confirmer.Confirm(prompt)
}

// confirmTransfers asks before quitting while a Transfer is unfinished. A
// state the guard cannot read is asked about too: the quit must never drop a
// Transfer silently. Declining leaves the queue exactly as it was, and the
// persisted rows keep their next-start downgrade path.
func (m *windowManager) confirmTransfers() bool {
	if m.transferGuard == nil {
		return true
	}
	count, err := m.transferGuard.UnfinishedTransfers()
	if err == nil && count <= 0 {
		return true
	}
	if m.confirmer == nil {
		return false
	}
	messages := m.messages()
	prompt := closePrompt{
		Title:   messages.QuitTransferTitle(),
		Confirm: messages.QuitConfirmConfirm(),
		Cancel:  messages.CloseWindowCancel(),
	}
	if err != nil {
		prompt.Message = messages.QuitTransferUnknown()
	} else {
		prompt.Message = messages.QuitTransferMessage(count)
	}
	return m.confirmer.Confirm(prompt)
}

func (m *windowManager) recordActivity() {
	if m.gate != nil {
		m.gate.RecordActivity()
	}
	if m.onFocus != nil {
		m.onFocus()
	}
}

// noWindowsLeft reports whether every window that keeps the application alive
// has closed. A panel does not count, so a hidden dropdown cannot block quit.
func (m *windowManager) noWindowsLeft() bool {
	for _, managed := range m.windows() {
		if !managed.counts {
			continue
		}
		if managed.window != nil && !managed.window.IsDestroyed() {
			return false
		}
	}
	return true
}

// markQuittingIfNoWindows records that the user approved closing the last
// application window, so the quit sequence that follows does not ask again.
func (m *windowManager) markQuittingIfNoWindows() {
	if !m.noWindowsLeft() {
		return
	}
	m.quitting = true
	// MyGo's own quit trigger is "no window left", and a created Panel stays
	// in that list even while hidden. The Panel never keeps the app alive, so
	// destroy it here; otherwise closing the last counted window would leave
	// the process running with no visible window and never reach OnWillQuit.
	m.ShutdownDropdown()
}

// FocusMain brings the main window forward, creating it if it was closed.
func (m *windowManager) FocusMain() {
	window := m.OpenMain()
	window.Show()
	window.Focus()
	m.mainFocuses++
}

// SecondInstance is what the single-instance callback does when a later
// launch hands over: raise the existing main window, or recreate it when it
// was closed. A handover is a startup route, so an open Dropdown Panel is
// hidden first and the main window becomes the window in front. It returns
// the window it raised.
func (m *windowManager) SecondInstance() hostWindow {
	m.HideDropdown()
	m.FocusMain()
	return m.main
}

// InvalidateAll redraws every window, so a lock or unlock propagates to all of
// them.
func (m *windowManager) InvalidateAll() {
	m.each(func(window hostWindow) { window.Invalidate() })
}

// ApplyLocale updates every window title after a language change and redraws
// each window so the new strings are built.
func (m *windowManager) ApplyLocale() {
	messages := m.messages()
	for _, managed := range m.windows() {
		if managed.window == nil || managed.window.IsDestroyed() {
			continue
		}
		if managed.title != nil {
			if title := managed.title(messages); title != "" {
				managed.window.SetTitle(title)
			}
		}
		managed.window.Invalidate()
	}
}
