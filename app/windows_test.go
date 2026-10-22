package app

import (
	"context"
	"crypto/rand"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	appapplock "github.com/mslxl/natcatty/application/applock"
	appsession "github.com/mslxl/natcatty/application/session"
	domainapplock "github.com/mslxl/natcatty/domain/applock"
	"github.com/mslxl/natcatty/domain/model"
	uiapplock "github.com/mslxl/natcatty/ui/applock"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/settings"
	"github.com/mslxl/natcatty/ui/shell/state"
)

// fakeWindow is a hostWindow whose close follows the same contract as the
// native one: OnCloseRequest can cancel it, and OnClosed runs once.
type fakeWindow struct {
	spec          windowSpec
	hooks         windowHooks
	shown         bool
	focused       bool
	destroyed     bool
	invalidations int
	title         string
	opacity       float64
}

func (w *fakeWindow) Show() { w.shown = true }
func (w *fakeWindow) Hide() {
	w.shown = false
	w.focused = false
}
func (w *fakeWindow) Focus() {
	w.focused = true
}
func (w *fakeWindow) IsFocused() bool { return w.focused && !w.destroyed }
func (w *fakeWindow) Close() {
	if w.destroyed {
		return
	}
	if w.hooks.OnCloseRequest != nil && !w.hooks.OnCloseRequest() {
		return
	}
	w.destroyed = true
	if w.hooks.OnClosed != nil {
		w.hooks.OnClosed()
	}
}
func (w *fakeWindow) Invalidate()       { w.invalidations++ }
func (w *fakeWindow) IsDestroyed() bool { return w.destroyed }
func (w *fakeWindow) IsVisible() bool   { return w.shown && !w.destroyed }
func (w *fakeWindow) SetTitle(title string) {
	w.title = title
}
func (w *fakeWindow) SetOpacity(value float64) { w.opacity = value }

type fakeWindowHost struct {
	windows []*fakeWindow
}

func (h *fakeWindowHost) Create(spec windowSpec, hooks windowHooks) hostWindow {
	window := &fakeWindow{spec: spec, hooks: hooks, title: spec.Title}
	h.windows = append(h.windows, window)
	return window
}

func (h *fakeWindowHost) last() *fakeWindow {
	if len(h.windows) == 0 {
		return nil
	}
	return h.windows[len(h.windows)-1]
}

type recordingConfirmer struct {
	prompts []closePrompt
	answer  bool
}

func (c *recordingConfirmer) Confirm(prompt closePrompt) bool {
	c.prompts = append(c.prompts, prompt)
	return c.answer
}

type windowGraph struct {
	host      *fakeWindowHost
	manager   *windowManager
	state     *state.State
	service   *appsession.Service
	confirmer *recordingConfirmer
}

// newWindowGraph mirrors the composition root's wiring: one shell state whose
// detach actions drive one window manager.
func newWindowGraph(t *testing.T, gate *uiapplock.Gate) *windowGraph {
	t.Helper()
	service := appsession.NewService(&wiringFactory{
		transport: &wiringTransport{closed: make(chan struct{})},
	})
	t.Cleanup(service.CloseAll)
	graph := &windowGraph{
		host:      &fakeWindowHost{},
		service:   service,
		confirmer: &recordingConfirmer{},
	}
	graph.state = state.NewState(
		state.WithSessions(service, func(*appsession.Session) (state.Surface, error) {
			return &wiringSurface{}, nil
		}),
		state.WithDetachHandler(func(id model.TabID) {
			graph.manager.OpenDetached(id)
		}),
		state.WithDetachReattachHandler(func(id model.TabID) {
			graph.manager.ReattachClose(id)
		}),
		state.WithDetachedCloseRequest(func(id model.TabID) {
			graph.manager.RequestDetachedClose(id)
		}),
	)
	graph.manager = newWindowManager(
		graph.host,
		graph.confirmer,
		gate,
		graph.state,
		func() func(*ui.Context) {
			return func(c *ui.Context) { ui.Text(c, "settings-content") }
		},
		func() string { return "en" },
	)
	return graph
}

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func waitForSurface(t *testing.T, shellState *state.State, tab int) {
	t.Helper()
	waitFor(t, "the Session surface", func() bool {
		pane, ok := shellState.FocusedPaneID(tab)
		if !ok {
			return false
		}
		info, ok := shellState.PaneInfoAt(tab, pane)
		return ok && info.Surface != nil
	})
}

func waitForDetachedSurface(t *testing.T, shellState *state.State, id model.TabID) {
	t.Helper()
	waitFor(t, "the detached Session surface", func() bool {
		info, ok := shellState.DetachedTabInfo(id)
		if !ok || len(info.Panes) == 0 {
			return false
		}
		pane, ok := shellState.DetachedPaneInfoAt(id, info.Panes[0])
		return ok && pane.Surface != nil
	})
}

type windowSettingsRepository struct {
	settings domainapplock.Settings
}

func (r *windowSettingsRepository) Load(context.Context) (domainapplock.Settings, error) {
	return r.settings, nil
}

func (r *windowSettingsRepository) Save(_ context.Context, settings domainapplock.Settings) error {
	r.settings = settings
	return nil
}

func lockedGate(t *testing.T, redraw func()) *uiapplock.Gate {
	t.Helper()
	verifier, err := domainapplock.NewVerifier("secret", rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	service := appapplock.NewService(&windowSettingsRepository{settings: domainapplock.Settings{
		Enabled:          true,
		TimeoutMinutes:   15,
		PasswordVerifier: verifier,
	}})
	if err := service.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	return uiapplock.NewGate(service, uiapplock.WithRedrawRequest(redraw))
}

func render(window *fakeWindow) *ui.Tester {
	tester := ui.NewTester(window.spec.Content, 800, 600)
	tester.SetLocale("en")
	tester.Frame()
	return tester
}

// TestEveryWindowIsGatedAndLockUnlockPropagates proves that the main, Settings,
// and detached windows all go through the one App Lock gate, and that a lock
// or unlock reaches every window.
func TestEveryWindowIsGatedAndLockUnlockPropagates(t *testing.T) {
	var graph *windowGraph
	gate := lockedGate(t, func() {
		if graph != nil {
			graph.manager.InvalidateAll()
		}
	})
	graph = newWindowGraph(t, gate)
	graph.manager.OpenMain()
	graph.manager.OpenSettings()

	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	waitForSurface(t, graph.state, 0)
	if !graph.state.DetachTab(1) {
		t.Fatal("DetachTab returned false")
	}
	detachedID := graph.state.DetachedTabIDs()[0]
	waitForDetachedSurface(t, graph.state, detachedID)
	if len(graph.host.windows) != 3 {
		t.Fatalf("windows = %d, want main, settings, and one detached", len(graph.host.windows))
	}

	m := locales.In("en")
	for _, window := range graph.host.windows {
		tester := render(window)
		if !tester.HasText(m.AppLockUnlock()) {
			t.Fatalf("window %q does not show the lock screen: %v", window.spec.Key, tester.Texts())
		}
		if tester.HasText("terminal") || tester.HasText("settings-content") {
			t.Fatalf("window %q rendered its content while locked: %v", window.spec.Key, tester.Texts())
		}
	}

	// Unlocking in one window releases every window.
	main := graph.host.windows[0]
	tester := render(main)
	tester.Type("secret")
	tester.Key(0, ui.KeyEnter)
	tester.Frame()
	if !tester.HasText("terminal") {
		t.Fatalf("the main window stayed locked: %v", tester.Texts())
	}
	settings := render(graph.host.windows[1])
	if !settings.HasText("settings-content") {
		t.Fatalf("the Settings window stayed locked: %v", settings.Texts())
	}
	detached := render(graph.host.windows[2])
	if !detached.HasText("terminal") {
		t.Fatalf("the detached window stayed locked: %v", detached.Texts())
	}

	for _, window := range graph.host.windows {
		window.invalidations = 0
	}
	if err := gate.Lock(); err != nil {
		t.Fatalf("Lock: %v", err)
	}
	for _, window := range graph.host.windows {
		if window.invalidations == 0 {
			t.Fatalf("locking did not redraw window %q", window.spec.Key)
		}
		tester := render(window)
		if tester.HasText("terminal") || tester.HasText("settings-content") {
			t.Fatalf("window %q rendered content after locking: %v", window.spec.Key, tester.Texts())
		}
	}
}

func TestSettingsWindowIsASingleton(t *testing.T) {
	graph := newWindowGraph(t, nil)
	graph.manager.OpenSettings()
	graph.manager.OpenSettings()
	if len(graph.host.windows) != 1 {
		t.Fatalf("windows = %d, want one Settings window", len(graph.host.windows))
	}
	if !graph.host.windows[0].shown || !graph.host.windows[0].focused {
		t.Fatal("the existing Settings window was not raised")
	}
}

// TestSettingsWindowLetsTheRecorderTakeAnAppBoundChord is the precedence
// guard: Settings must not register the application shortcut tables on its
// window. MyGo delivers a registered window shortcut before it offers the key
// to a focused field, so a registered Ctrl+B would run Broadcast and the
// recorder would never see the chord it was asked to capture.
func TestSettingsWindowLetsTheRecorderTakeAnAppBoundChord(t *testing.T) {
	service, _ := newBindingFixture(t, "windows")
	shortcuts := settings.NewShortcutsState(service)
	search := settings.NewSearchState()
	search.SetArea(settings.AreaShortcuts)

	graph := newWindowGraph(t, nil)
	manager := graph.manager
	manager.settingsContent = func() func(*ui.Context) {
		return settings.ViewWithSettings(settings.Config{
			Shortcuts: shortcuts,
			Search:    search,
		})
	}
	acceleratorFires, shortcutFires := 0, 0
	manager.accelerators = []acceleratorBinding{{
		mods: ui.Ctrl, key: ui.KeyB,
		item: &mygo.MenuItem{Click: func(*mygo.MenuItem, *mygo.Window) { acceleratorFires++ }},
	}}
	manager.shortcuts = []keyShortcut{{
		mods: ui.Ctrl, key: ui.KeyB,
		run: func() { shortcutFires++ },
	}}

	manager.OpenSettings()
	window := graph.host.last()
	if window == nil || window.spec.Content == nil {
		t.Fatal("OpenSettings created no content")
	}
	tester := ui.NewTester(window.spec.Content, 1100, 720)
	tester.SetLocale("en")
	tester.Frame()

	for press := 0; press < 200; press++ {
		tester.Key(ui.Ctrl, ui.KeyB)
		tester.Frame()
		pending, ok := shortcuts.Pending()
		if !ok {
			tester.Key(0, ui.KeyTab)
			tester.Frame()
			continue
		}
		if pending.Chord != "Ctrl + B" {
			t.Fatalf("pending chord = %q", pending.Chord)
		}
		if acceleratorFires != 0 || shortcutFires != 0 {
			t.Fatalf("the app shortcut ran while recording: accelerators=%d shortcuts=%d",
				acceleratorFires, shortcutFires)
		}
		return
	}
	t.Fatalf("Ctrl+B never reached the recorder: accelerators=%d shortcuts=%d texts=%v",
		acceleratorFires, shortcutFires, tester.Texts())
}

func TestDetachedCloseAsksOnlyWithActiveSessionsAndKeepsMainAlive(t *testing.T) {
	graph := newWindowGraph(t, nil)
	graph.manager.OpenMain()
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	waitForSurface(t, graph.state, 0)
	if !graph.state.DetachTab(0) {
		t.Fatal("DetachTab returned false")
	}
	detachedID := graph.state.DetachedTabIDs()[0]
	window := graph.host.last()
	if window == nil || window.spec.Key != "detached:"+string(detachedID) {
		t.Fatalf("detached window = %+v", window)
	}

	graph.confirmer.answer = false
	window.Close()
	if window.destroyed {
		t.Fatal("the detached window closed without consent")
	}
	if len(graph.confirmer.prompts) != 1 || graph.confirmer.prompts[0].Title != locales.In("en").CloseWindowTitle() {
		t.Fatalf("prompts = %+v, want one close-window prompt", graph.confirmer.prompts)
	}
	if !graph.state.DetachedTabExists(detachedID) {
		t.Fatal("the cancelled close removed the detached Tab")
	}

	graph.confirmer.answer = true
	window.Close()
	if !window.destroyed {
		t.Fatal("the detached window did not close after consent")
	}
	if graph.state.DetachedTabExists(detachedID) {
		t.Fatal("closing the detached window left its Tab")
	}
	if graph.state.TabCount() != 0 {
		t.Fatalf("main tabs = %d, want closings confined to the detached window", graph.state.TabCount())
	}
	if main := graph.manager.Main(); main == nil || main.IsDestroyed() {
		t.Fatal("closing the detached window closed the main window")
	}
}

func TestReattachClosesTheWindowWithoutPromptOrEndingSessions(t *testing.T) {
	graph := newWindowGraph(t, nil)
	graph.manager.OpenMain()
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	waitForSurface(t, graph.state, 0)
	if !graph.state.DetachTab(0) {
		t.Fatal("DetachTab returned false")
	}
	detachedID := graph.state.DetachedTabIDs()[0]
	window := graph.host.last()

	if !graph.state.ReattachDetachedTab(detachedID) {
		t.Fatal("ReattachDetachedTab returned false")
	}
	if !window.destroyed {
		t.Fatal("moving back did not close the detached window")
	}
	if len(graph.confirmer.prompts) != 0 {
		t.Fatalf("prompts = %+v, want none for a move back", graph.confirmer.prompts)
	}
	if graph.state.TabCount() != 1 || graph.state.ActiveSessionCountMain() != 1 {
		t.Fatalf("main tabs = %d, active = %d; want the live Session back",
			graph.state.TabCount(), graph.state.ActiveSessionCountMain())
	}
}

func TestMainCloseAsksOnlyWithActiveSessions(t *testing.T) {
	t.Run("no sessions", func(t *testing.T) {
		graph := newWindowGraph(t, nil)
		main := graph.manager.OpenMain()
		main.Close()
		if !main.IsDestroyed() {
			t.Fatal("an empty main window did not close")
		}
		if len(graph.confirmer.prompts) != 0 {
			t.Fatalf("prompts = %+v, want none", graph.confirmer.prompts)
		}
	})

	t.Run("active session", func(t *testing.T) {
		graph := newWindowGraph(t, nil)
		main := graph.manager.OpenMain()
		if err := graph.state.NewTab(); err != nil {
			t.Fatal(err)
		}
		waitForSurface(t, graph.state, 0)
		graph.confirmer.answer = false
		main.Close()
		if main.IsDestroyed() {
			t.Fatal("the main window closed without consent")
		}
		if len(graph.confirmer.prompts) != 1 {
			t.Fatalf("prompts = %+v, want one", graph.confirmer.prompts)
		}

		graph.confirmer.answer = true
		main.Close()
		if !main.IsDestroyed() {
			t.Fatal("the main window did not close after consent")
		}
		waitFor(t, "the main Sessions to end", func() bool {
			return graph.state.ActiveSessionCountMain() == 0
		})
	})
}

// TestCloseIntentPromptsNameTheSessionCount pins the localized close and quit
// prompts to the number of Sessions the action would end.
func TestCloseIntentPromptsNameTheSessionCount(t *testing.T) {
	graph := newWindowGraph(t, nil)
	main := graph.manager.OpenMain()
	for index := 0; index < 2; index++ {
		if err := graph.state.NewTab(); err != nil {
			t.Fatal(err)
		}
	}
	waitForSurface(t, graph.state, 0)
	waitForSurface(t, graph.state, 1)

	graph.confirmer.answer = false
	main.Close()
	if graph.manager.ConfirmQuit() {
		t.Fatal("quit proceeded while the user declined")
	}
	if len(graph.confirmer.prompts) != 2 {
		t.Fatalf("prompts = %+v, want a close and a quit prompt", graph.confirmer.prompts)
	}
	m := locales.In("en")
	closePrompt := graph.confirmer.prompts[0]
	if closePrompt.Title != m.CloseWindowTitle() ||
		closePrompt.Message != m.CloseWindowMessage(2) ||
		closePrompt.Confirm != m.CloseWindowConfirm() ||
		closePrompt.Cancel != m.CloseWindowCancel() {
		t.Fatalf("close prompt = %+v, want the localized two-Session prompt", closePrompt)
	}
	quitPrompt := graph.confirmer.prompts[1]
	if quitPrompt.Title != m.QuitConfirmTitle() ||
		quitPrompt.Message != m.QuitConfirmMessage(2) ||
		quitPrompt.Confirm != m.QuitConfirmConfirm() {
		t.Fatalf("quit prompt = %+v, want the localized two-Session prompt", quitPrompt)
	}
}

// TestDetachedAndMainCloseIntentCountOnlyTheirOwnSessions keeps a window from
// warning about Sessions it does not own.
func TestDetachedAndMainCloseIntentCountOnlyTheirOwnSessions(t *testing.T) {
	graph := newWindowGraph(t, nil)
	main := graph.manager.OpenMain()
	for index := 0; index < 2; index++ {
		if err := graph.state.NewTab(); err != nil {
			t.Fatal(err)
		}
	}
	waitForSurface(t, graph.state, 0)
	waitForSurface(t, graph.state, 1)
	if !graph.state.DetachTab(1) {
		t.Fatal("DetachTab returned false")
	}
	detachedID := graph.state.DetachedTabIDs()[0]
	waitForDetachedSurface(t, graph.state, detachedID)
	detached := graph.manager.detached[detachedID]

	graph.confirmer.answer = false
	detached.Close()
	main.Close()
	if len(graph.confirmer.prompts) != 2 {
		t.Fatalf("prompts = %+v, want one per window", graph.confirmer.prompts)
	}
	m := locales.In("en")
	if got := graph.confirmer.prompts[0].Message; got != m.CloseWindowMessage(1) {
		t.Fatalf("detached prompt = %q, want one Session", got)
	}
	if got := graph.confirmer.prompts[1].Message; got != m.CloseWindowMessage(1) {
		t.Fatalf("main prompt = %q, want one Session", got)
	}
}

// TestClosingTheLastWindowAfterConsentMarksQuit proves the quit sequence that
// follows the accepted close does not ask again.
func TestClosingTheLastWindowAfterConsentMarksQuit(t *testing.T) {
	graph := newWindowGraph(t, nil)
	main := graph.manager.OpenMain()
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	waitForSurface(t, graph.state, 0)
	graph.confirmer.answer = true
	main.Close()
	if !main.IsDestroyed() {
		t.Fatal("the main window stayed open after consent")
	}
	if !graph.manager.Quitting() {
		t.Fatal("closing the last window after consent did not mark the quit")
	}
	if !graph.manager.ConfirmQuit() {
		t.Fatal("the quit after the accepted close asked again")
	}
	if len(graph.confirmer.prompts) != 1 {
		t.Fatalf("prompts = %+v, want only the window close prompt", graph.confirmer.prompts)
	}
}

func TestQuitIntentIsAskedOnce(t *testing.T) {
	graph := newWindowGraph(t, nil)
	main := graph.manager.OpenMain()
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	waitForSurface(t, graph.state, 0)

	graph.confirmer.answer = false
	if graph.manager.ConfirmQuit() {
		t.Fatal("quit proceeded without consent")
	}
	if graph.manager.Quitting() {
		t.Fatal("a cancelled quit was remembered as accepted")
	}
	if len(graph.confirmer.prompts) != 1 || graph.confirmer.prompts[0].Title != locales.In("en").QuitConfirmTitle() {
		t.Fatalf("prompts = %+v, want one quit prompt", graph.confirmer.prompts)
	}

	graph.confirmer.answer = true
	if !graph.manager.ConfirmQuit() {
		t.Fatal("quit did not proceed after consent")
	}
	if !graph.manager.Quitting() {
		t.Fatal("the accepted quit was not remembered")
	}
	if graph.manager.ConfirmQuit() != true || len(graph.confirmer.prompts) != 2 {
		t.Fatalf("a second quit asked again: prompts = %+v", graph.confirmer.prompts)
	}

	// The window closes that follow the confirmed quit do not ask again.
	main.Close()
	if !main.IsDestroyed() {
		t.Fatal("the main window did not close after the quit was accepted")
	}
	if len(graph.confirmer.prompts) != 2 {
		t.Fatalf("closing after quit asked again: %+v", graph.confirmer.prompts)
	}
}

func TestCopyToNewWindowOpensAWindowAndLeavesTheSource(t *testing.T) {
	graph := newWindowGraph(t, nil)
	graph.manager.OpenMain()
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	waitForSurface(t, graph.state, 0)

	if err := graph.state.DuplicateTabToWindow(0); err != nil {
		t.Fatal(err)
	}
	if graph.state.TabCount() != 1 {
		t.Fatalf("main tabs = %d, want the source to remain", graph.state.TabCount())
	}
	if len(graph.host.windows) != 2 {
		t.Fatalf("windows = %d, want the main and one new window", len(graph.host.windows))
	}
	detachedID := graph.state.DetachedTabIDs()[0]
	if graph.host.last().spec.Key != "detached:"+string(detachedID) {
		t.Fatalf("new window key = %q, want the detached Tab", graph.host.last().spec.Key)
	}
}

// TestSingleInstanceFallbacks pins the deterministic behavior of the
// inter-process seam without launching a second process.
type fakeInstanceLock struct {
	primary  bool
	err      error
	callback func([]string, string)
	asked    bool
}

func (l *fakeInstanceLock) Acquire() (bool, error) { return l.primary, l.err }
func (l *fakeInstanceLock) OnSecondInstance(fn func([]string, string)) func() {
	l.callback = fn
	return func() {}
}

func TestSingleInstancePrimaryWithoutASeamRuns(t *testing.T) {
	if !singleInstancePrimary(nil, func([]string, string) { t.Fatal("nil seam called back") }) {
		t.Fatal("a nil seam did not run as the only instance")
	}
}

func TestSingleInstancePrimaryRegistersTheSecondLaunchCallback(t *testing.T) {
	lock := &fakeInstanceLock{primary: true}
	raised := 0
	if !singleInstancePrimary(lock, func([]string, string) { raised++ }) {
		t.Fatal("the primary instance did not acquire the lock")
	}
	if lock.callback == nil {
		t.Fatal("the second-launch callback was not registered")
	}
	lock.callback([]string{"host"}, "")
	if raised != 1 {
		t.Fatalf("focus callbacks = %d, want 1", raised)
	}
}

func TestSingleInstanceExistingInstanceExits(t *testing.T) {
	lock := &fakeInstanceLock{primary: false}
	if singleInstancePrimary(lock, func([]string, string) { t.Fatal("a later instance ran the focus callback") }) {
		t.Fatal("a later launch ran as a second instance")
	}
	if lock.callback != nil {
		t.Fatal("a later launch registered a callback")
	}
}

func TestSingleInstanceAcquireFailureFallsBackToSoleInstance(t *testing.T) {
	if !singleInstancePrimary(&fakeInstanceLock{err: context.DeadlineExceeded}, nil) {
		t.Fatal("an acquisition error did not fall back to running")
	}
}
