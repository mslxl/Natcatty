package phoneshell

import (
	"context"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	fileapplock "github.com/mslxl/natcatty/adapters/applock"
	appapplock "github.com/mslxl/natcatty/application/applock"
	"github.com/mslxl/natcatty/ui/locales"
)

// newLockedPhoneFixture writes enabled App Lock settings through the real
// adapter, loads them into the shared service, and builds a shell whose gate
// uses the same injected clock. It returns the mutable clock the idle test
// advances.
func newLockedPhoneFixture(t *testing.T, opener *fakeOpener, timeoutMinutes int) (*phoneFixture, *appapplock.Service, *time.Time) {
	t.Helper()
	return newLockedPhoneFixtureWith(t, opener, timeoutMinutes)
}

// newLockedPhoneFixtureWith is newLockedPhoneFixture with extra shell options,
// so an idle test can use a terminal screen whose input handling matches the
// real libghostty-vt view.
func newLockedPhoneFixtureWith(t *testing.T, opener *fakeOpener, timeoutMinutes int,
	opts ...Option) (*phoneFixture, *appapplock.Service, *time.Time) {

	t.Helper()
	now := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }

	path := filepath.Join(t.TempDir(), "app-lock-settings.json")
	ctx := context.Background()
	initial := appapplock.NewService(fileapplock.Open(path), appapplock.WithClock(clock))
	if err := initial.Enable(ctx, "correct horse"); err != nil {
		t.Fatalf("enable App Lock: %v", err)
	}
	if err := initial.SetTimeout(ctx, timeoutMinutes); err != nil {
		t.Fatalf("set App Lock timeout: %v", err)
	}
	service := appapplock.NewService(fileapplock.Open(path), appapplock.WithClock(clock))
	if err := service.Load(ctx); err != nil {
		t.Fatalf("load App Lock: %v", err)
	}

	options := append([]Option{WithAppLock(service), WithAppLockClock(clock)}, opts...)
	fixture := newPhoneFixtureWith(t, phoneHosts(), opener, options...)
	t.Cleanup(fixture.shell.Close)
	fixture.tester.SetLocale("en")
	return fixture, service, &now
}

// unlockPhone types password into the focused lock field and presses Unlock.
func unlockPhone(t *testing.T, tester *ui.Tester, m locales.Messages, password string) {
	t.Helper()
	tester.Type(password)
	if err := tester.Click(m.AppLockUnlock()); err != nil {
		t.Fatalf("press Unlock: %v", err)
	}
	tester.Frame()
}

// TestAppLockColdStartCoversEverySection proves an enabled App Lock hides the
// whole Workspace until the correct password opens it, across all four
// sections, and that the page selected before the lock is the one that shows
// afterwards.
func TestAppLockColdStartCoversEverySection(t *testing.T) {
	fixture, service, _ := newLockedPhoneFixture(t, &fakeOpener{}, 1)
	m := locales.In("en")

	for _, section := range []Section{Sessions, Hosts, Vault, Settings} {
		fixture.shell.Select(section)
		fixture.tester.Frame()
		if !fixture.tester.HasText(m.AppLockUnlock()) {
			t.Fatalf("section %d did not show the lock page: %q", section, fixture.tester.Texts())
		}
		if fixture.tester.HasText("prod box") {
			t.Fatalf("section %d drew Workspace content behind the cold-start lock", section)
		}
	}

	// The requested page is preserved: the Hosts list the fixture selected is
	// what appears after unlocking, not a fresh root elsewhere.
	fixture.shell.Select(Hosts)
	fixture.tester.Frame()
	unlockPhone(t, fixture.tester, m, "correct horse")
	if !fixture.tester.HasText("prod box") {
		t.Fatalf("unlock did not reveal the requested Hosts page: %q", fixture.tester.Texts())
	}
	if fixture.tester.HasText(m.AppLockUnlock()) {
		t.Fatalf("the lock page is still shown after unlock: %q", fixture.tester.Texts())
	}
	if service.Snapshot().Locked {
		t.Fatal("the shared App Lock service is still locked after the correct password")
	}
}

// TestAppLockRejectsTheWrongPasswordAndStaysClosed proves a wrong passphrase
// reports the shared error and neither reveals content nor unlocks the state
// machine, and that the correct password still works afterwards.
func TestAppLockRejectsTheWrongPasswordAndStaysClosed(t *testing.T) {
	fixture, service, _ := newLockedPhoneFixture(t, &fakeOpener{}, 1)
	m := locales.In("en")

	const wrong = "incorrect horse"
	fixture.tester.Type(wrong)
	if err := fixture.tester.Click(m.AppLockUnlock()); err != nil {
		t.Fatalf("press Unlock: %v", err)
	}
	fixture.tester.Frame()
	if !fixture.tester.HasText(m.AppLockProblemIncorrect()) {
		t.Fatalf("a wrong password did not show the error: %q", fixture.tester.Texts())
	}
	if !service.Snapshot().Locked {
		t.Fatal("a wrong password unlocked the shared App Lock service")
	}
	if fixture.tester.HasText("prod box") {
		t.Fatal("a wrong password revealed Workspace content")
	}

	// Clear the rejected text; the auto-focused field takes the correction.
	for range wrong {
		fixture.tester.Key(0, ui.KeyBackspace)
	}
	fixture.tester.Frame()
	unlockPhone(t, fixture.tester, m, "correct horse")
	if !fixture.tester.HasText("prod box") {
		t.Fatalf("the correct password after a rejection did not unlock: %q", fixture.tester.Texts())
	}
	if service.Snapshot().Locked {
		t.Fatal("the correct password after a rejection left the service locked")
	}
}

// TestAppLockIdleTimeoutRelocks proves the configured idle threshold closes
// the gate again, using the injected clock rather than wall time.
func TestAppLockIdleTimeoutRelocks(t *testing.T) {
	fixture, service, now := newLockedPhoneFixture(t, &fakeOpener{}, 1)
	m := locales.In("en")

	unlockPhone(t, fixture.tester, m, "correct horse")
	if !fixture.tester.HasText("prod box") {
		t.Fatalf("unlock did not reveal the Hosts page: %q", fixture.tester.Texts())
	}

	// One minute is the threshold; step past it and draw one frame.
	*now = now.Add(61 * time.Second)
	fixture.tester.Frame()
	if !service.Snapshot().Locked {
		t.Fatal("the idle timeout did not lock the shared App Lock service")
	}
	if !fixture.tester.HasText(m.AppLockUnlock()) {
		t.Fatalf("the idle lock did not show the lock page: %q", fixture.tester.Texts())
	}
	if fixture.tester.HasText("prod box") {
		t.Fatal("the idle lock left Workspace content on screen")
	}
}

// TestAppLockBackgroundHidesTheSnapshotAndHonorsTheLock proves leaving the
// foreground closes the gate before the system snapshots the app, so terminal
// text is absent while covered, and that returning stays locked until the
// password is entered again.
func TestAppLockBackgroundHidesTheSnapshotAndHonorsTheLock(t *testing.T) {
	const secret = "super secret terminal output"
	opener := &fakeOpener{output: secret + "\r\n"}
	fixture, service, _ := newLockedPhoneFixture(t, opener, 1)
	m := locales.In("en")

	unlockPhone(t, fixture.tester, m, "correct horse")
	handle := fixture.openSession(t, "prod box")
	fixture.await(t, "the terminal output", func() bool {
		return fixture.tester.HasText(secret)
	})

	// Leaving the foreground releases the view and closes the gate: the frame
	// the system would snapshot shows the lock page, not the terminal.
	fixture.shell.Background()
	fixture.tester.Frame()
	if !service.Snapshot().Locked {
		t.Fatal("backgrounding did not lock the shared App Lock service")
	}
	if fixture.tester.HasText(secret) {
		t.Fatal("the background snapshot exposed terminal content")
	}
	if !fixture.tester.HasText(m.AppLockUnlock()) {
		t.Fatalf("the background cover is not the lock page: %q", fixture.tester.Texts())
	}

	// Returning honors the lock: the gate stays closed until the password is
	// entered again, and then the resumed Session is visible.
	fixture.shell.Foreground()
	fixture.tester.Frame()
	if !service.Snapshot().Locked {
		t.Fatal("returning to the foreground opened the gate without a password")
	}
	if fixture.tester.HasText(secret) {
		t.Fatal("returning to the foreground exposed terminal content while locked")
	}
	unlockPhone(t, fixture.tester, m, "correct horse")
	fixture.await(t, "the resumed terminal", func() bool {
		return handle.isAttached() && fixture.tester.HasText(secret)
	})
}

// TestAppLockPageFitsSafeAreasOnPhoneViewports proves the unlock page is
// reachable and clickable inside the safe area at the three probe viewports:
// a small screen, a notched portrait screen, and landscape with side insets.
func TestAppLockPageFitsSafeAreasOnPhoneViewports(t *testing.T) {
	fixture, service, _ := newLockedPhoneFixture(t, &fakeOpener{}, 1)
	m := locales.In("en")

	layouts := []struct {
		name                     string
		width, height            int
		top, right, bottom, left float32
	}{
		{"small portrait", 320, 568, 20, 0, 16, 0},
		{"notched portrait", 390, 844, 47, 0, 34, 0},
		{"phone landscape", 844, 390, 0, 44, 21, 44},
	}
	for _, layout := range layouts {
		if err := service.Lock(appapplock.ReasonManual); err != nil {
			t.Fatalf("%s: lock: %v", layout.name, err)
		}
		fixture.tester.SetSize(layout.width, layout.height)
		fixture.tester.SetInsets(layout.top, layout.right, layout.bottom, layout.left)
		fixture.tester.Frame()

		unlock, ok := fixture.tester.Find(m.AppLockUnlock())
		if !ok {
			t.Fatalf("%s: no Unlock control: %q", layout.name, fixture.tester.Texts())
		}
		field, ok := fixture.tester.Find(m.AppLockPassword())
		if !ok {
			t.Fatalf("%s: no password field: %q", layout.name, fixture.tester.Texts())
		}
		assertInsideSafeArea(t, layout.name+" Unlock", unlock, layout.width, layout.height,
			layout.top, layout.right, layout.bottom, layout.left)
		assertInsideSafeArea(t, layout.name+" password", field, layout.width, layout.height,
			layout.top, layout.right, layout.bottom, layout.left)

		// The page is not only laid out but usable: the field takes keyboard
		// text and the button unlocks.
		unlockPhone(t, fixture.tester, m, "correct horse")
		if !fixture.tester.HasText("prod box") {
			t.Fatalf("%s: unlock did not reveal the Hosts page: %q", layout.name, fixture.tester.Texts())
		}
	}
}

// consumingScreen stands in for the real libghostty-vt view on the input path.
// The terminal takes its own keys, text, scroll and pointer presses, so none of
// them bubble to an ancestor HandleInput. The shared fakeScreen deliberately
// leaves everything but text to bubble, which would hide the App Lock defect
// these tests cover.
type consumingScreen struct {
	*fakeScreen
}

func (s *consumingScreen) View(c *ui.Context) ui.Element {
	column := ui.Column(c).Fill().Gap(4).Padding(8)
	column.Children(func() {
		ui.Text(c, s.handle.rendered())
		ui.Box(c).Fill().Label("Terminal").HandleInput(func(ev ui.InputEvent) bool {
			switch ev.Kind {
			case ui.InputText:
				if ev.Text != "" {
					_, _ = s.handle.Write([]byte(ev.Text))
				}
			case ui.InputKeyDown, ui.InputKeyUp, ui.InputScroll,
				ui.InputPointerDown, ui.InputPointerUp, ui.InputPointerMove:
				// The terminal consumes these itself.
			default:
				return false
			}
			return true
		}).AutoFocus()
	})
	c.After(10 * time.Millisecond)
	return column
}

func consumingScreens(conn io.ReadWriteCloser, appearance TerminalAppearance) (TerminalScreen, error) {
	screen, err := fakeScreens(conn, appearance)
	if err != nil {
		return nil, err
	}
	return &consumingScreen{fakeScreen: screen.(*fakeScreen)}, nil
}

// phoneIdleDeadline returns the App Lock idle deadline, failing the test when
// the unlocked gate has none.
func phoneIdleDeadline(t *testing.T, service *appapplock.Service) time.Time {
	t.Helper()
	deadline, ok := service.IdleDeadline()
	if !ok {
		t.Fatal("the unlocked App Lock has no idle deadline")
	}
	return deadline
}

// TestAppLockTypedTerminalInputPostponesTheIdleLock proves a key typed into the
// terminal counts as user activity. The terminal's own handler takes the key,
// as libghostty-vt does, so the gate's bubbling ancestor never sees it; the
// Session change hook the phone root installs is what keeps the app open.
func TestAppLockTypedTerminalInputPostponesTheIdleLock(t *testing.T) {
	opener := &fakeOpener{output: "remote prompt\r\n"}
	fixture, service, now := newLockedPhoneFixtureWith(t, opener, 1,
		WithScreenFactory(consumingScreens))
	m := locales.In("en")

	unlockPhone(t, fixture.tester, m, "correct horse")
	fixture.openSession(t, "prod box")
	handle := opener.last()

	*now = now.Add(30 * time.Second)
	fixture.tester.Frame()
	stale := phoneIdleDeadline(t, service)

	fixture.tester.TypeKey(0, ui.KeyX, "x")
	fixture.tester.Frame()
	if got := handle.input(); got != "x" {
		t.Fatalf("typed input = %q, want the terminal's own handler to take it", got)
	}
	fresh := phoneIdleDeadline(t, service)
	if want := now.Add(time.Minute); !fresh.Equal(want) {
		t.Fatalf("typing moved the idle deadline to %v, want %v", fresh, want)
	}
	if !fresh.After(stale) {
		t.Fatalf("the idle deadline did not move: %v -> %v", stale, fresh)
	}

	// The old deadline no longer locks, so sustained typing cannot lock the
	// gate mid-command.
	*now = stale.Add(time.Second)
	fixture.tester.Frame()
	if service.Snapshot().Locked {
		t.Fatalf("the gate locked at the old deadline %v despite the typed key", stale)
	}

	// Once activity stops, the configured timeout still closes the gate.
	*now = fresh.Add(time.Second)
	fixture.tester.Frame()
	if !service.Snapshot().Locked {
		t.Fatalf("the gate stayed open past the moved deadline %v", fresh)
	}
	if !fixture.tester.HasText(m.AppLockUnlock()) {
		t.Fatalf("the idle lock did not show the lock page: %q", fixture.tester.Texts())
	}
}

// TestAppLockPointerPressInsideTheTerminalPostponesTheIdleLock proves a tap (or
// click) on the terminal counts as activity even though the terminal consumes
// the press. The gate observes input in its subtree ahead of the widget that
// takes it, so a press never reaches an ancestor HandleInput.
func TestAppLockPointerPressInsideTheTerminalPostponesTheIdleLock(t *testing.T) {
	opener := &fakeOpener{output: "remote prompt\r\n"}
	fixture, service, now := newLockedPhoneFixtureWith(t, opener, 1,
		WithScreenFactory(consumingScreens))
	m := locales.In("en")

	unlockPhone(t, fixture.tester, m, "correct horse")
	fixture.openSession(t, "prod box")

	*now = now.Add(30 * time.Second)
	fixture.tester.Frame()
	stale := phoneIdleDeadline(t, service)

	terminal, ok := fixture.tester.Find("Terminal")
	if !ok {
		t.Fatalf("no terminal to press: %q", fixture.tester.Texts())
	}
	x, y := terminal.X+terminal.W/2, terminal.Y+terminal.H/2
	fixture.tester.Press(x, y)
	fixture.tester.Release(x, y)
	fixture.tester.Frame()

	fresh := phoneIdleDeadline(t, service)
	if want := now.Add(time.Minute); !fresh.Equal(want) {
		t.Fatalf("the pointer press moved the idle deadline to %v, want %v", fresh, want)
	}
	if !fresh.After(stale) {
		t.Fatalf("the idle deadline did not move: %v -> %v", stale, fresh)
	}

	*now = stale.Add(time.Second)
	fixture.tester.Frame()
	if service.Snapshot().Locked {
		t.Fatalf("the gate locked at the old deadline %v despite the pointer press", stale)
	}

	*now = fresh.Add(time.Second)
	fixture.tester.Frame()
	if !service.Snapshot().Locked {
		t.Fatalf("the gate stayed open past the moved deadline %v", fresh)
	}
	if !fixture.tester.HasText(m.AppLockUnlock()) {
		t.Fatalf("the idle lock did not show the lock page: %q", fixture.tester.Texts())
	}
}

func assertInsideSafeArea(t *testing.T, name string, r ui.Rect, width, height int,
	top, right, bottom, left float32) {
	t.Helper()
	minX, maxX := left, float32(width)-right
	minY, maxY := top, float32(height)-bottom
	if r.X < minX-0.5 || r.X+r.W > maxX+0.5 || r.Y < minY-0.5 || r.Y+r.H > maxY+0.5 {
		t.Fatalf("%s %+v is outside the safe area [%g,%g]x[%g,%g] of %dx%d",
			name, r, minX, maxX, minY, maxY, width, height)
	}
}

// TestAppLockKeyboardInsetKeepsTheFieldReachable proves that with the native
// keyboard covering the bottom of the screen, MyGo's safe-area layout and the
// gate's scroll container keep the password field visible, and that the
// keyboard's submit key unlocks the same as the button does.
func TestAppLockKeyboardInsetKeepsTheFieldReachable(t *testing.T) {
	fixture, service, _ := newLockedPhoneFixture(t, &fakeOpener{}, 1)
	m := locales.In("en")

	// A tall phone with the status bar at the top and the keyboard over the
	// bottom third of the screen.
	fixture.tester.SetSize(390, 844)
	fixture.tester.SetInsets(47, 0, 300, 0)
	fixture.tester.Frame()
	if !service.Snapshot().Locked {
		t.Fatal("the cold-start lock is not closed")
	}

	field, ok := fixture.tester.Find(m.AppLockPassword())
	if !ok {
		t.Fatalf("no password field above the keyboard: %q", fixture.tester.Texts())
	}
	assertInsideSafeArea(t, "keyboard password field", field, 390, 844, 47, 0, 300, 0)

	// The auto-focused field takes the password, and the keyboard's submit
	// key opens the gate without reaching for the button.
	fixture.tester.Type("correct horse")
	fixture.tester.Key(0, ui.KeyEnter)
	fixture.tester.Frame()
	if service.Snapshot().Locked {
		t.Fatal("the keyboard submit key did not unlock the shared service")
	}
	if !fixture.tester.HasText("prod box") {
		t.Fatalf("the keyboard unlock did not reveal the Hosts page: %q", fixture.tester.Texts())
	}
}
