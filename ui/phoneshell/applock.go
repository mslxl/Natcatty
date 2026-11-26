package phoneshell

// App Lock on the phone reuses the shared App Lock state machine
// (application/applock) and draws its own gate in the phone's UI language.
// The desktop gate lives in ui/applock, which pulls the desktop layout package
// into the phone's dependency graph; the phone dependency gate forbids that, so
// only the state machine is shared.

import (
	"errors"
	"time"

	"github.com/egoist/mygo/ui"

	appapplock "github.com/mslxl/natcatty/application/applock"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
	uitheme "github.com/mslxl/natcatty/ui/theme"
)

// lockProblem identifies the unlock failure the gate must localize.
type lockProblem string

const (
	lockProblemNone      lockProblem = ""
	lockProblemEmpty     lockProblem = "empty"
	lockProblemIncorrect lockProblem = "incorrect"
	lockProblemFailed    lockProblem = "failed"
)

// lockGate owns the phone lock screen's input and wraps every section view.
// The service owns the lock, unlock, idle and attempt state; the gate only
// renders it. It is mutated on the UI thread.
type lockGate struct {
	service  *appapplock.Service
	password string
	problem  lockProblem
	now      func() time.Time
	// refocus puts the keyboard focus back on the password field after a
	// rejected attempt, so the user can correct it without tapping again.
	refocus bool
}

// newLockGate returns a gate over service. now must be the service's clock, so
// an injected clock makes the idle rule deterministic in a test.
func newLockGate(service *appapplock.Service, now func() time.Time) *lockGate {
	if now == nil {
		now = time.Now
	}
	return &lockGate{service: service, now: now}
}

// Locked reports whether the gate is closed. A gate without a service is
// never locked.
func (g *lockGate) Locked() bool {
	if g == nil || g.service == nil {
		return false
	}
	return g.service.Snapshot().Locked
}

// lockForBackground closes the gate before the system snapshots the app, so
// the snapshot shows the lock screen instead of terminal content. An App Lock
// without a verifier is disabled and the call is a no-op.
func (g *lockGate) lockForBackground() {
	if g == nil || g.service == nil {
		return
	}
	_ = g.service.Lock(appapplock.ReasonBackground)
}

// wrap renders content behind the gate. While the gate is closed the section
// is not built at all, so no page, terminal, or Workspace content reaches the
// screen or the system's snapshot.
func (g *lockGate) wrap(content func(*ui.Context)) func(*ui.Context) {
	return func(c *ui.Context) {
		if g == nil || g.service == nil {
			content(c)
			return
		}
		if g.service.LockIfIdle(g.now()) {
			// The composition root's redraw hook covers a device; the
			// invalidate also makes an offscreen test see the idle lock
			// without one.
			c.Invalidate()
		}
		if g.service.Snapshot().Locked {
			g.lockScreen(c)
			return
		}
		// Wake at the idle deadline. After keeps the earliest requested frame,
		// so asking each frame does not accumulate timers.
		if deadline, ok := g.service.IdleDeadline(); ok {
			delay := deadline.Sub(g.now())
			if delay <= 0 {
				delay = time.Millisecond
			}
			c.After(delay)
		}
		// Pointer and scroll input that content widgets do not consume reaches
		// this ancestor and counts as activity, postponing the idle lock. The
		// observe hook sees input the content itself takes, as the terminal
		// takes every key and tap inside its grid: without it a finger or a
		// keystroke in the terminal would never reach this ancestor.
		ui.Column(c).Fill().
			ObserveInput(func(ui.InputEvent) {
				g.service.RecordActivity()
			}).
			HandleInput(func(ui.InputEvent) bool {
				g.service.RecordActivity()
				return false
			}).Children(func() {
			content(c)
		})
	}
}

// lockScreen draws the phone lock page. It is the window's root content, so
// MyGo's root safe-area padding already keeps it clear of the status bar, the
// home indicator, a cutout, and the native keyboard; the scroll container
// lets a short screen or a keyboard-covered one reach the whole form.
func (g *lockGate) lockScreen(c *ui.Context) {
	m := locales.In(c.Locale())
	t := c.Theme()
	width, _ := c.Size()
	// Leave the page's own margin, then cap the form so it stays readable on
	// a tablet or in landscape instead of stretching a line across the screen.
	formWidth := width - 48
	if formWidth > 480 {
		formWidth = 480
	}
	if formWidth < 0 {
		formWidth = 0
	}

	ui.Scroll(c).Fill().Children(func() {
		ui.Column(c).FillWidth().Padding(24).Children(func() {
			ui.Row(c).FillWidth().Children(func() {
				ui.Box(c).Grow(1)
				ui.Column(c).Width(formWidth).Gap(12).Children(func() {
					ui.Text(c, m.AppLockTitle()).FontSize(24).Bold()
					ui.Text(c, m.AppLockBody()).TextColor(t.TextMuted)

					var submitted bool
					kit.FormRow(c, m.AppLockPassword(), func() {
						input := ui.TextInput(c, &g.password).Password().FillWidth()
						switch {
						case g.refocus:
							// A rejected attempt moves focus back here, keeping
							// the native keyboard where the user needs it.
							input = input.Focus()
							g.refocus = false
						default:
							// The first frame claims focus and opens the
							// native keyboard.
							input = input.AutoFocus()
						}
						submitted = input.Submitted()
					})
					unlock := kit.PrimaryButton(c, "", m.AppLockUnlock()).
						Width(formWidth).Clicked()
					if submitted || unlock {
						g.unlock(c)
					}

					if message := lockProblemText(m, g.problem); message != "" {
						ui.Text(c, message).TextColor(uitheme.DangerText(t))
					}
				})
				ui.Box(c).Grow(1)
			})
		})
	})
}

// unlock checks the password on the shared service. A failure stays on the
// lock screen with a localized message; success opens the gate, and the
// section the user last chose renders on the next frame.
func (g *lockGate) unlock(c *ui.Context) {
	err := g.service.Unlock(g.password)
	switch {
	case err == nil:
		g.password = ""
		g.problem = lockProblemNone
		g.refocus = false
	case errors.Is(err, appapplock.ErrEmptyPassword):
		g.problem = lockProblemEmpty
		g.refocus = true
	case errors.Is(err, appapplock.ErrIncorrectPassword):
		g.problem = lockProblemIncorrect
		g.refocus = true
	default:
		g.problem = lockProblemFailed
		g.refocus = true
	}
	// The service's redraw hook covers the app; the invalidate also repaints
	// an offscreen test that has no hook.
	c.Invalidate()
}

func lockProblemText(m locales.Messages, problem lockProblem) string {
	switch problem {
	case lockProblemEmpty:
		return m.AppLockProblemEmpty()
	case lockProblemIncorrect:
		return m.AppLockProblemIncorrect()
	case lockProblemFailed:
		return m.AppLockProblemFailed()
	}
	return ""
}
