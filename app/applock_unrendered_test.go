package app

import (
	"context"
	"crypto/rand"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	appapplock "github.com/mslxl/natcatty/application/applock"
	"github.com/mslxl/natcatty/application/command"
	appdesktop "github.com/mslxl/natcatty/application/desktop"
	domainapplock "github.com/mslxl/natcatty/domain/applock"
	uiapplock "github.com/mslxl/natcatty/ui/applock"
	"github.com/mslxl/natcatty/ui/locales"
	shellstate "github.com/mslxl/natcatty/ui/shell/state"
)

// manualLockGate returns a gate that starts locked and never idle-locks: the
// zero timeout means LockIfIdle never closes it and IdleDeadline never asks
// for a wake-up frame, so ui.Tester.FramePending only reports motion.
func manualLockGate(t *testing.T, redraw func()) *uiapplock.Gate {
	return testLockGate(t, redraw, 0, nil)
}

// testLockGate returns a gate over an enabled App Lock with the given idle
// timeout. A nil clock uses the real time; a test shares one clock between the
// service and the gate so an idle lock is deterministic.
func testLockGate(t *testing.T, redraw func(), timeoutMinutes int, clock func() time.Time) *uiapplock.Gate {
	t.Helper()
	verifier, err := domainapplock.NewVerifier("secret", rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var serviceOptions []appapplock.Option
	var gateOptions []uiapplock.Option
	gateOptions = append(gateOptions, uiapplock.WithRedrawRequest(redraw))
	if clock != nil {
		serviceOptions = append(serviceOptions, appapplock.WithClock(clock))
		gateOptions = append(gateOptions, uiapplock.WithClock(clock))
	}
	service := appapplock.NewService(&windowSettingsRepository{settings: domainapplock.Settings{
		Enabled:          true,
		TimeoutMinutes:   timeoutMinutes,
		PasswordVerifier: verifier,
	}}, serviceOptions...)
	if err := service.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	return uiapplock.NewGate(service, gateOptions...)
}

// unlockThroughLockScreen types the password into the visible lock screen and
// renders the frame that releases the window's content.
func unlockThroughLockScreen(t *testing.T, tester *ui.Tester, what string) {
	t.Helper()
	unlock := locales.In("en").AppLockUnlock()
	if !tester.HasText(unlock) {
		t.Fatalf("%s is not showing the lock screen: %v", what, tester.Texts())
	}
	tester.Type("secret")
	tester.Key(0, ui.KeyEnter)
	tester.Frame()
	if tester.HasText(unlock) {
		t.Fatalf("%s stayed locked after the password: %v", what, tester.Texts())
	}
}

// TestAppLockMainViewWarpsThePaneEventItNeverShowed is the verifier's repro:
// while the lock screen replaces the main Workspace the production Split
// Right command still records a PaneOpen, because the shortcut/command
// registry binds outside the gate. The first unlocked frame must draw the
// final two-Pane layout and ask for no animation frame; the same split while
// unlocked must animate, so the test can tell warp from animate.
func TestAppLockMainViewWarpsThePaneEventItNeverShowed(t *testing.T) {
	var q *quakeGraph
	gate := manualLockGate(t, func() {
		if q != nil {
			q.manager.InvalidateAll()
		}
	})
	q = newQuakeGraphWithGate(t, gate)
	installMainShellView(q)
	q.manager.OpenMain()
	main := q.manager.Main().(*fakeWindow)
	main.Show()
	main.Focus()
	if err := q.state.NewTab(); err != nil {
		t.Fatalf("NewTab: %v", err)
	}
	waitForSurface(t, q.state, 0)

	tester := ui.NewTester(main.spec.Content, 900, 600)
	tester.SetLocale("en")
	m := locales.In("en")
	tester.Frame()
	unlockThroughLockScreen(t, tester, "the main window")
	if !tester.HasText("terminal") {
		t.Fatalf("unlocking did not release the main Workspace: %v", tester.Texts())
	}
	if tester.FramePending() {
		t.Fatal("the first unlocked main frame kept a motion frame pending")
	}
	// An idle frame presents the warped layout and records its boxes.
	tester.Frame()

	// Lock again; the lock screen is a frame the main renderer never builds.
	if err := gate.Lock(); err != nil {
		t.Fatalf("Lock: %v", err)
	}
	tester.Frame()
	if !tester.HasText(m.AppLockUnlock()) {
		t.Fatalf("the main window did not show the lock screen: %v", tester.Texts())
	}
	if tester.HasText("terminal") {
		t.Fatalf("the locked main window rendered the Workspace: %v", tester.Texts())
	}
	if tester.FramePending() {
		t.Fatal("the lock screen asks for animation frames")
	}

	// The production command registry is still reachable while locked: this
	// is the Split Right menu/keyboard action. It records a PaneOpen the main
	// view never showed.
	registry := newRegistry(q.state, func() {}, gate.Lock, nil, func() {})
	if err := registry.Dispatch(context.Background(), command.SplitPaneRight); err != nil {
		t.Fatalf("SplitPaneRight while locked: %v", err)
	}
	waitForTabPanes(t, q.state, 0, 2)

	// Unlock through the same lock screen. The first frame must be the final
	// two-Pane layout with no Pane-open run.
	unlockThroughLockScreen(t, tester, "the main window after the locked split")
	if tester.FramePending() {
		t.Fatal("the unlocked main frame replayed the Pane event it never showed")
	}
	if got := followTextCount(tester.Texts(), "terminal"); got != 2 {
		t.Fatalf("the unlocked main frame drew %d Pane surfaces, want the final 2; texts %v",
			got, tester.Texts())
	}

	// Control: the very same command while unlocked does request animation
	// frames, so the assertion above distinguishes a warp from an animation.
	tester.Frame()
	if err := registry.Dispatch(context.Background(), command.SplitPaneRight); err != nil {
		t.Fatalf("SplitPaneRight while unlocked: %v", err)
	}
	waitForTabPanes(t, q.state, 0, 3)
	tester.Frame()
	if !tester.FramePending() {
		t.Fatal("a split while unlocked asked for no animation frame; the comparison is meaningless")
	}
}

// TestAppLockIdleLockFrameMarksTheMainView drives the lock the gate applies
// itself while wrapping, not a manual gate.Lock() between frames. The gate
// must be asked every frame rather than pre-checked: this frame builds no
// content, so the event recorded while it is on screen has to warp.
func TestAppLockIdleLockFrameMarksTheMainView(t *testing.T) {
	var q *quakeGraph
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	gate := testLockGate(t, func() {
		if q != nil {
			q.manager.InvalidateAll()
		}
	}, 1, clock)
	q = newQuakeGraphWithGate(t, gate)
	installMainShellView(q)
	q.manager.OpenMain()
	main := q.manager.Main().(*fakeWindow)
	main.Show()
	main.Focus()
	if err := q.state.NewTab(); err != nil {
		t.Fatalf("NewTab: %v", err)
	}
	waitForSurface(t, q.state, 0)

	tester := ui.NewTester(main.spec.Content, 900, 600)
	tester.SetLocale("en")
	tester.Frame()
	unlockThroughLockScreen(t, tester, "the main window")
	if tester.FramePending() {
		t.Fatal("the first unlocked main frame kept a motion frame pending")
	}
	tester.Frame()

	// Move past the idle timeout; the next frame's gate call locks before it
	// can build the Workspace.
	now = now.Add(2 * time.Minute)
	tester.Frame()
	if !tester.HasText(locales.In("en").AppLockUnlock()) {
		t.Fatalf("the idle lock did not replace the Workspace: %v", tester.Texts())
	}

	registry := newRegistry(q.state, func() {}, gate.Lock, nil, func() {})
	if err := registry.Dispatch(context.Background(), command.SplitPaneRight); err != nil {
		t.Fatalf("SplitPaneRight while idle-locked: %v", err)
	}
	waitForTabPanes(t, q.state, 0, 2)

	unlockThroughLockScreen(t, tester, "the idle-locked main window")
	if tester.FramePending() {
		t.Fatal("the idle-locked main frame replayed the Pane event it never showed")
	}
	if got := followTextCount(tester.Texts(), "terminal"); got != 2 {
		t.Fatalf("the unlocked main frame drew %d Pane surfaces, want the final 2; texts %v",
			got, tester.Texts())
	}
}

// TestAppLockTopdownRouteWarpsThePaneEventItNeverShowed locks the Dropdown
// Panel on the top-down route, records a split through the production
// command, and proves the first unlocked Panel frame is final.
func TestAppLockTopdownRouteWarpsThePaneEventItNeverShowed(t *testing.T) {
	var q *quakeGraph
	gate := manualLockGate(t, func() {
		if q != nil {
			q.manager.InvalidateAll()
		}
	})
	q = newQuakeGraphWithGate(t, gate)
	installMainShellView(q)
	q.mode = appdesktop.HotkeyModeTopdown
	q.manager.OpenMain()
	if err := q.state.NewTab(); err != nil {
		t.Fatalf("NewTab: %v", err)
	}
	waitForSurface(t, q.state, 0)
	q.manager.ShowDropdown()
	panel := dropdownWindow(t, q.windowGraph)

	tester := ui.NewTester(panel.spec.Content, 900, 600)
	tester.SetLocale("en")
	tester.Frame()
	unlockThroughLockScreen(t, tester, "the top-down Panel")
	if !tester.HasText("terminal") {
		t.Fatalf("unlocking did not release the top-down shell: %v", tester.Texts())
	}
	if tester.FramePending() {
		t.Fatal("the first unlocked top-down frame kept a motion frame pending")
	}
	tester.Frame()

	if err := gate.Lock(); err != nil {
		t.Fatalf("Lock: %v", err)
	}
	tester.Frame()
	if !tester.HasText(locales.In("en").AppLockUnlock()) {
		t.Fatalf("the top-down Panel did not show the lock screen: %v", tester.Texts())
	}

	registry := newRegistry(q.state, func() {}, gate.Lock, nil, func() {})
	if err := registry.Dispatch(context.Background(), command.SplitPaneRight); err != nil {
		t.Fatalf("SplitPaneRight while locked: %v", err)
	}
	waitForTabPanes(t, q.state, 0, 2)

	unlockThroughLockScreen(t, tester, "the top-down Panel after the locked split")
	if tester.FramePending() {
		t.Fatal("the unlocked top-down Panel replayed the Pane event it never showed")
	}
	if got := followTextCount(tester.Texts(), "terminal"); got != 2 {
		t.Fatalf("the unlocked top-down Panel drew %d Pane surfaces, want the final 2; texts %v",
			got, tester.Texts())
	}
}

// TestAppLockQuakeOwnRouteWarpsTheTabEventItNeverShowed locks the Quake
// Panel, opens a Tab while the routed body and bar are replaced by the lock
// screen, and proves the first unlocked frame draws the final body and strip
// without animating.
func TestAppLockQuakeOwnRouteWarpsTheTabEventItNeverShowed(t *testing.T) {
	var q *quakeGraph
	gate := manualLockGate(t, func() {
		if q != nil {
			q.manager.InvalidateAll()
		}
	})
	q = newQuakeGraphWithGate(t, gate)
	q.manager.ShowDropdown()
	panel := dropdownWindow(t, q.windowGraph)
	waitForQuakeTabCount(t, q, 1)

	tester := ui.NewTester(panel.spec.Content, 800, 400)
	tester.SetLocale("en")
	tester.Frame()
	unlockThroughLockScreen(t, tester, "the Quake Panel")
	if !tester.HasText("quake-terminal-1") {
		t.Fatalf("unlocking did not release the Quake body: %v", tester.Texts())
	}
	if tester.FramePending() {
		t.Fatal("the first unlocked Quake frame kept a motion frame pending")
	}
	tester.Frame()

	if err := gate.Lock(); err != nil {
		t.Fatalf("Lock: %v", err)
	}
	tester.Frame()
	if !tester.HasText(locales.In("en").AppLockUnlock()) {
		t.Fatalf("the Quake Panel did not show the lock screen: %v", tester.Texts())
	}
	if tester.HasText("quake-terminal-1") {
		t.Fatalf("the locked Quake Panel rendered its body: %v", tester.Texts())
	}

	// A Tab event recorded while the gate replaces the routed body and the
	// gated bar: both runtimes must warp on the unlock frame.
	if err := q.quake.AddLocalTab(); err != nil {
		t.Fatalf("AddLocalTab: %v", err)
	}
	waitForQuakeTabCount(t, q, 2)

	unlockThroughLockScreen(t, tester, "the Quake Panel after the locked Tab open")
	if tester.FramePending() {
		t.Fatal("the unlocked Quake Panel replayed the Tab event it never showed")
	}
	if !tester.HasText(locales.In("en").LocalSessionTab(2)) {
		t.Fatalf("the unlocked Quake bar did not draw the final row: %v", tester.Texts())
	}
	if !tester.HasText("quake-terminal-2") {
		t.Fatalf("the unlocked Quake body did not draw the final Tab: %v", tester.Texts())
	}
}

// TestAppLockDropdownSkipProvidersMarkTheRoutedViews proves the installed
// skip providers record the routed views on a locked Panel frame. The show
// hook's own notices are consumed first, so only the gate's skip callbacks can
// set them again.
func TestAppLockDropdownSkipProvidersMarkTheRoutedViews(t *testing.T) {
	var q *quakeGraph
	gate := manualLockGate(t, func() {
		if q != nil {
			q.manager.InvalidateAll()
		}
	})
	q = newQuakeGraphWithGate(t, gate)
	if q.manager.dropdownContentSkip == nil {
		t.Fatal("the composition root installed no dropdown content skip provider")
	}
	if q.manager.dropdownBarSkip == nil {
		t.Fatal("the composition root installed no dropdown bar skip provider")
	}
	q.manager.ShowDropdown()
	for _, view := range []shellstate.MotionView{
		shellstate.MotionViewFollow,
		shellstate.MotionViewTopdown,
		shellstate.MotionViewFollowBar,
	} {
		if !q.state.TakeMotionViewUnrendered(view) {
			t.Fatalf("the show hook did not mark %v", view)
		}
	}
	// A locked Panel frame: the routed content and bar slots build nothing.
	render(dropdownWindow(t, q.windowGraph))
	for _, view := range []shellstate.MotionView{
		shellstate.MotionViewFollow,
		shellstate.MotionViewTopdown,
		shellstate.MotionViewFollowBar,
	} {
		if !q.state.TakeMotionViewUnrendered(view) {
			t.Fatalf("the locked Panel frame did not mark %v", view)
		}
	}
}

// TestAppLockDetachedWindowMarksItsOwnPaneRuntime proves the locked detached
// window records the notice for its own Tab, so its Pane runtime warps instead
// of animating, and that unlocking renders the Tab again.
func TestAppLockDetachedWindowMarksItsOwnPaneRuntime(t *testing.T) {
	var q *quakeGraph
	gate := manualLockGate(t, func() {
		if q != nil {
			q.manager.InvalidateAll()
		}
	})
	q = newQuakeGraphWithGate(t, gate)
	q.manager.OpenMain()
	if err := q.state.NewTab(); err != nil {
		t.Fatalf("NewTab: %v", err)
	}
	waitForSurface(t, q.state, 0)
	if !q.state.DetachTab(0) {
		t.Fatal("DetachTab returned false")
	}
	detachedID := q.state.DetachedTabIDs()[0]
	waitForDetachedSurface(t, q.state, detachedID)
	window, ok := q.manager.detached[detachedID].(*fakeWindow)
	if !ok || window == nil {
		t.Fatal("the detached Tab opened no window")
	}

	tester := ui.NewTester(window.spec.Content, 800, 600)
	tester.SetLocale("en")
	m := locales.In("en")
	tester.Frame()
	if !tester.HasText(m.AppLockUnlock()) {
		t.Fatalf("the detached window did not show the lock screen: %v", tester.Texts())
	}
	if tester.HasText("terminal") {
		t.Fatalf("the locked detached window rendered its Tab: %v", tester.Texts())
	}
	// The skip fired for this window's own Tab, not another window's.
	if !q.state.TakeDetachedViewUnrendered(detachedID) {
		t.Fatal("the locked detached frame did not mark its window's Pane runtime")
	}

	unlockThroughLockScreen(t, tester, "the detached window")
	if !tester.HasText("terminal") {
		t.Fatalf("the unlocked detached window did not render its Tab: %v", tester.Texts())
	}
}
