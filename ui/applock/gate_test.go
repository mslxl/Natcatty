package applock

import (
	"context"
	"crypto/rand"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	applock "github.com/mslxl/natcatty/application/applock"
	domainapplock "github.com/mslxl/natcatty/domain/applock"
	"github.com/mslxl/natcatty/ui/locales"
)

type fakeSettings struct {
	settings domainapplock.Settings
}

func (f *fakeSettings) Load(context.Context) (domainapplock.Settings, error) {
	return f.settings, nil
}

func (f *fakeSettings) Save(_ context.Context, settings domainapplock.Settings) error {
	f.settings = settings
	return nil
}

func enabledSettings(t *testing.T, timeoutMinutes int) domainapplock.Settings {
	t.Helper()
	verifier, err := domainapplock.NewVerifier("secret", rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return domainapplock.Settings{
		Enabled:          true,
		TimeoutMinutes:   timeoutMinutes,
		PasswordVerifier: verifier,
	}
}

func newGate(t *testing.T, settings domainapplock.Settings) (*Gate, *time.Time) {
	t.Helper()
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	service := applock.NewService(&fakeSettings{settings: settings}, applock.WithClock(func() time.Time {
		return now
	}))
	if err := service.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	gate := NewGate(service, WithClock(func() time.Time { return now }))
	return gate, &now
}

func newGateTester(gate *Gate) *ui.Tester {
	tester := ui.NewTester(gate.Wrap(func(c *ui.Context) {
		ui.Text(c, "content")
	}), 640, 480)
	tester.SetLocale("en")
	tester.Frame()
	return tester
}

func TestLockedGateHidesContentAndUnlocks(t *testing.T) {
	gate, _ := newGate(t, enabledSettings(t, 15))
	tester := newGateTester(gate)
	m := locales.In("en")

	if tester.HasText("content") {
		t.Fatal("the locked gate rendered its content")
	}
	if !tester.HasText(m.AppLockUnlock()) {
		t.Fatal("the locked gate did not offer to unlock")
	}

	tester.Type("secret")
	tester.Key(0, ui.KeyEnter)
	tester.Frame()
	if !tester.HasText("content") {
		t.Fatalf("the gate stayed locked after the right password: %v", tester.Texts())
	}
}

func TestWrongPasswordKeepsTheGateClosed(t *testing.T) {
	gate, _ := newGate(t, enabledSettings(t, 15))
	tester := newGateTester(gate)
	m := locales.In("en")

	tester.Type("wrong")
	tester.Key(0, ui.KeyEnter)
	tester.Frame()
	if tester.HasText("content") {
		t.Fatal("a wrong password opened the gate")
	}
	if !tester.HasText(m.AppLockProblemIncorrect()) {
		t.Fatalf("no incorrect-password message: %v", tester.Texts())
	}
}

func TestIdleTimeoutLocksTheGate(t *testing.T) {
	gate, now := newGate(t, enabledSettings(t, 1))
	tester := newGateTester(gate)

	tester.Type("secret")
	tester.Key(0, ui.KeyEnter)
	tester.Frame()
	if !tester.HasText("content") {
		t.Fatal("the gate did not unlock")
	}

	*now = now.Add(61 * time.Second)
	tester.Frame()
	if tester.HasText("content") {
		t.Fatal("the gate stayed open after the idle timeout")
	}
}

func TestActivityPostponesTheIdleTimeout(t *testing.T) {
	gate, now := newGate(t, enabledSettings(t, 1))
	tester := newGateTester(gate)
	tester.Type("secret")
	tester.Key(0, ui.KeyEnter)
	tester.Frame()

	*now = now.Add(50 * time.Second)
	gate.RecordActivity()
	*now = now.Add(20 * time.Second)
	tester.Frame()
	if !tester.HasText("content") {
		t.Fatal("the gate locked although there was recent activity")
	}
}

// TestLockScreenFitsAtDefaultAndMinimumSizes renders the gate at the main
// window's default and minimum sizes and at the detached window's minimum. The
// lock screen must stay inside the window and its lines must not overlap.
func TestLockScreenFitsAtDefaultAndMinimumSizes(t *testing.T) {
	m := locales.In("en")
	for _, size := range [][2]int{{1100, 720}, {720, 480}, {480, 320}} {
		gate, _ := newGate(t, enabledSettings(t, 15))
		tester := ui.NewTester(gate.Wrap(func(c *ui.Context) {
			ui.Text(c, "content")
		}), size[0], size[1])
		tester.SetLocale("en")
		tester.Frame()

		if tester.HasText("content") {
			t.Fatalf("%dx%d rendered content while locked", size[0], size[1])
		}
		title := gateTextInside(t, tester, size[0], size[1], m.AppLockTitle())
		body := gateTextInside(t, tester, size[0], size[1], m.AppLockBody())
		password := gateTextInside(t, tester, size[0], size[1], m.AppLockPassword())
		unlock := gateTextInside(t, tester, size[0], size[1], m.AppLockUnlock())
		for index, pair := range [][2]ui.Rect{{title, body}, {body, password}, {password, unlock}} {
			if pair[0].Y+pair[0].H > pair[1].Y+0.5 {
				t.Fatalf("%dx%d lock-screen lines %d overlap: %+v then %+v",
					size[0], size[1], index, pair[0], pair[1])
			}
		}
	}
}

func gateTextInside(t *testing.T, tester *ui.Tester, width, height int, text string) ui.Rect {
	t.Helper()
	r, ok := tester.Find(text)
	if !ok {
		t.Fatalf("the lock screen does not show %q: %v", text, tester.Texts())
	}
	const epsilon = 0.5
	if r.X < -epsilon || r.Y < -epsilon ||
		r.X+r.W > float32(width)+epsilon || r.Y+r.H > float32(height)+epsilon {
		t.Fatalf("%q is clipped at %dx%d: box %+v", text, width, height, r)
	}
	return r
}

func TestDisabledAppLockNeverLocks(t *testing.T) {
	gate, now := newGate(t, domainapplock.DefaultSettings())
	tester := newGateTester(gate)

	*now = now.Add(24 * time.Hour)
	tester.Frame()
	if !tester.HasText("content") {
		t.Fatal("a disabled App Lock closed the gate")
	}
	if err := gate.Lock(); err != nil {
		t.Fatalf("Lock with App Lock disabled = %v, want a no-op", err)
	}
	if !tester.HasText("content") {
		t.Fatal("Lock closed the gate while App Lock was disabled")
	}
}
