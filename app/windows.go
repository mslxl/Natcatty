package app

import (
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/domain/model"
	uiapplock "github.com/mslxl/natcatty/ui/applock"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/shell"
	"github.com/mslxl/natcatty/ui/shell/state"
)

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
	Content   func(*ui.Context)
}

// windowHooks are the events the composition root handles. OnCloseRequest
// reports whether the close may proceed; the host cancels the close when it
// returns false.
type windowHooks struct {
	OnFocus        func()
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
	SetTitle(string)
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
	window := mygo.NewWindow(mygo.WindowOptions{
		Title:     spec.Title,
		Width:     spec.Width,
		Height:    spec.Height,
		MinWidth:  spec.MinWidth,
		MinHeight: spec.MinHeight,
		StateKey:  spec.StateKey,
		Content:   ui.View(spec.Content),
	})
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
	return window
}

// windowManager owns every application window and the shared App Lock gate.
// MyGo calls the hooks on the UI thread; tests drive them directly.
type windowManager struct {
	host            windowHost
	confirmer       dialogConfirmer
	gate            *uiapplock.Gate
	state           *state.State
	settingsContent func() func(*ui.Context)
	locale          func() string
	// closeToTray asks whether a main-window close should hide the window
	// instead. It is consulted live, so disabling the tray icon or
	// close-to-tray takes effect without reopening the window.
	closeToTray func() bool
	// accelerators are the menu shortcuts every window handles at window
	// scope. SetAccelerators replaces them when the menu is rebuilt.
	accelerators []acceleratorBinding
	// selftestBind registers the main window's self-test shortcuts at window
	// scope, and selftestView draws its overlay around the content. Both are
	// nil in normal builds.
	selftestBind func(*ui.Context)
	selftestView func(func(*ui.Context)) func(*ui.Context)

	main     hostWindow
	settings hostWindow
	detached map[model.TabID]hostWindow
	// detachedViews keeps the exact content function each detached window was
	// given, so the selftest can render the live window's surface rather than
	// a re-built copy.
	detachedViews map[model.TabID]func(*ui.Context)
	forceClose    map[string]bool
	quitting      bool
	// mainFocuses counts FocusMain calls. The selftest uses it to observe
	// that a second launch raised the existing main window.
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
		detachedViews:   make(map[model.TabID]func(*ui.Context)),
		forceClose:      make(map[string]bool),
	}
}

// SetAccelerators installs the menu shortcuts every window dispatches. It is
// called whenever the menu is built, including after a language change.
func (m *windowManager) SetAccelerators(bindings []acceleratorBinding) {
	m.accelerators = bindings
}

// withAccelerators binds the menu shortcuts at the window's root context,
// before the App Lock gate or the content builds anything. MyGo then handles
// them as window shortcuts, ahead of the focused terminal's input handler.
func (m *windowManager) withAccelerators(content func(*ui.Context)) func(*ui.Context) {
	return func(c *ui.Context) {
		bindAccelerators(c, m.accelerators)
		content(c)
	}
}

// wrap puts every window's content behind the one shared App Lock gate. The
// current gate is resolved per frame, so replacing it (as the selftest does)
// also governs windows that already exist, and no window builds its content
// while locked.
func (m *windowManager) wrap(content func(*ui.Context)) func(*ui.Context) {
	return func(c *ui.Context) {
		if m.gate == nil {
			content(c)
			return
		}
		m.gate.Wrap(content)(c)
	}
}

// detachedContent is the content of a detached Tab's window: the Tab's view
// behind the same shared App Lock gate as every other window. The selftest
// reuses it to prove the gate hides detached content.
func (m *windowManager) detachedContent(tabID model.TabID) func(*ui.Context) {
	return m.withAccelerators(m.wrap(shell.DetachedView(m.state, tabID)))
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
	var build func(*ui.Context) = shell.View(m.state)
	if m.selftestView != nil {
		build = m.selftestView(build)
	}
	// The self-test overlay is inside the gate: while App Lock is closed no
	// window, including the developer facility, builds anything.
	gated := m.wrap(build)
	// withAccelerators binds at the root context, before the gate builds its
	// container, so MyGo registers window shortcuts. Inside the gate they
	// would become element-scoped and fail from the Vault view with no
	// terminal focus.
	content := m.withAccelerators(func(c *ui.Context) {
		if m.selftestBind != nil {
			m.selftestBind(c)
		}
		gated(c)
	})
	m.main = m.host.Create(windowSpec{
		Key:       "main",
		Title:     m.messages().AppName(),
		StateKey:  "main",
		Width:     1100,
		Height:    720,
		MinWidth:  720,
		MinHeight: 480,
		Content:   content,
	}, windowHooks{
		OnFocus:        m.recordActivity,
		OnCloseRequest: m.allowMainClose,
		OnClosed: func() {
			m.state.Persist()
			// A detached window keeps the app alive: closing the main window
			// ends only its own Sessions.
			m.state.CloseMainSessions()
			m.main = nil
			m.markQuittingIfNoWindows()
		},
	})
	return m.main
}

// Main returns the main window, or nil before it is created.
func (m *windowManager) Main() hostWindow { return m.main }

// OpenSettings creates the singleton Settings window or focuses the existing
// one. Settings holds no Sessions, so it never asks close intent. It uses the
// same 1100x720 default and 720x480 minimum as the main window because the
// desktop integration sections need a readable, scrollable layout.
func (m *windowManager) OpenSettings() {
	if m.settings != nil && !m.settings.IsDestroyed() {
		m.settings.Show()
		m.settings.Focus()
		return
	}
	if m.settingsContent == nil {
		return
	}
	m.settings = m.host.Create(windowSpec{
		Key:       "settings",
		Title:     m.messages().NavSettings(),
		StateKey:  "settings",
		Width:     1100,
		Height:    720,
		MinWidth:  720,
		MinHeight: 480,
		Content:   m.withAccelerators(m.wrap(m.settingsContent())),
	}, windowHooks{
		OnFocus:  m.recordActivity,
		OnClosed: func() { m.settings = nil; m.markQuittingIfNoWindows() },
	})
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
	m.detachedViews[tabID] = content
	m.detached[tabID] = m.host.Create(windowSpec{
		Key:       "detached:" + string(tabID),
		Title:     shell.DetachedTitle(m.messages(), info),
		Width:     900,
		Height:    600,
		MinWidth:  480,
		MinHeight: 320,
		Content:   content,
	}, windowHooks{
		OnFocus:        m.recordActivity,
		OnCloseRequest: func() bool { return m.allowDetachedClose(tabID) },
		OnClosed: func() {
			delete(m.detached, tabID)
			delete(m.detachedViews, tabID)
			delete(m.forceClose, "detached:"+string(tabID))
			// Idempotent: a re-attached Tab is no longer detached, so its
			// Sessions stay alive in the main window.
			m.state.CloseDetachedTab(tabID)
			m.markQuittingIfNoWindows()
		},
	})
}

// ReattachClose closes a detached window after its Tab moved back into the
// main Workspace. The move itself already kept the Sessions alive.
func (m *windowManager) ReattachClose(tabID model.TabID) {
	m.forceClose["detached:"+string(tabID)] = true
	if window := m.detached[tabID]; window != nil && !window.IsDestroyed() {
		window.Close()
	}
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
	return m.confirmSessions(m.state.ActiveSessionCountMain(), false)
}

// SetCloseToTray installs the tray-hiding policy. The window manager asks it
// on every main-window close.
func (m *windowManager) SetCloseToTray(policy func() bool) {
	m.closeToTray = policy
}

func (m *windowManager) allowDetachedClose(tabID model.TabID) bool {
	key := "detached:" + string(tabID)
	if m.quitting || m.forceClose[key] {
		return true
	}
	return m.confirmSessions(m.state.ActiveSessionCountDetached(tabID), false)
}

// ConfirmQuit handles application quit intent once. It reports whether the
// quit may proceed and remembers the decision for the window closes that
// follow.
func (m *windowManager) ConfirmQuit() bool {
	if m.quitting {
		return true
	}
	if m.confirmSessions(m.state.ActiveSessionCount(), true) {
		m.quitting = true
		return true
	}
	return false
}

// Quitting reports whether quit intent is already confirmed.
func (m *windowManager) Quitting() bool { return m.quitting }

func (m *windowManager) confirmSessions(count int, quitting bool) bool {
	if count <= 0 {
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

func (m *windowManager) recordActivity() {
	if m.gate != nil {
		m.gate.RecordActivity()
	}
}

// markQuittingIfNoWindows records that the user approved closing the last
// application window, so the quit sequence that follows does not ask again.
func (m *windowManager) markQuittingIfNoWindows() {
	if m.main != nil && !m.main.IsDestroyed() {
		return
	}
	if m.settings != nil && !m.settings.IsDestroyed() {
		return
	}
	for _, window := range m.detached {
		if window != nil && !window.IsDestroyed() {
			return
		}
	}
	m.quitting = true
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
// was closed. It returns the window it raised so the selftest can confirm the
// production path focused the main window.
func (m *windowManager) SecondInstance() hostWindow {
	m.FocusMain()
	return m.main
}

// InvalidateAll redraws every window, so a lock or unlock propagates to all of
// them.
func (m *windowManager) InvalidateAll() {
	invalidate := func(window hostWindow) {
		if window != nil && !window.IsDestroyed() {
			window.Invalidate()
		}
	}
	invalidate(m.main)
	invalidate(m.settings)
	for _, window := range m.detached {
		invalidate(window)
	}
}

// ApplyLocale updates every window title after a language change.
func (m *windowManager) ApplyLocale() {
	messages := m.messages()
	if m.main != nil && !m.main.IsDestroyed() {
		m.main.SetTitle(messages.AppName())
		m.main.Invalidate()
	}
	if m.settings != nil && !m.settings.IsDestroyed() {
		m.settings.SetTitle(messages.NavSettings())
		m.settings.Invalidate()
	}
	for tabID, window := range m.detached {
		if window == nil || window.IsDestroyed() {
			continue
		}
		if info, ok := m.state.DetachedTabInfo(tabID); ok {
			window.SetTitle(shell.DetachedTitle(messages, info))
		}
		window.Invalidate()
	}
}
