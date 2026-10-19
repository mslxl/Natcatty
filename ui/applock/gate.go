// Package applock renders the App Lock gate: a password prompt that hides the
// app's content while the gate is closed and closes again after the configured
// idle timeout. App Lock is a UI gate; it does not encrypt the database.
package applock

import (
	"errors"
	"time"

	"github.com/egoist/mygo/ui"

	appaplock "github.com/mslxl/natcatty/application/applock"
	"github.com/mslxl/natcatty/ui/locales"
)

// Problem identifies the unlock failure the view must localize.
type Problem string

const (
	ProblemNone      Problem = ""
	ProblemEmpty     Problem = "empty"
	ProblemIncorrect Problem = "incorrect"
	ProblemFailed    Problem = "failed"
)

// Gate owns the lock screen's input and wraps a content view. It is mutated
// only on the UI thread.
type Gate struct {
	service  *appaplock.Service
	password string
	problem  Problem
	now      func() time.Time
	redraw   func()
}

// Option customizes a Gate.
type Option func(*Gate)

// WithClock sets the clock that compares activity with the idle deadline.
// Tests use it to make timeouts deterministic.
func WithClock(fn func() time.Time) Option {
	return func(g *Gate) {
		if fn != nil {
			g.now = fn
		}
	}
}

// WithRedrawRequest sets the function called after the gate changes. The app
// uses it to invalidate the window after an idle lock.
func WithRedrawRequest(fn func()) Option {
	return func(g *Gate) { g.redraw = fn }
}

// NewGate returns a gate for service. A nil service renders content without a
// gate.
func NewGate(service *appaplock.Service, opts ...Option) *Gate {
	gate := &Gate{service: service, now: time.Now}
	for _, opt := range opts {
		opt(gate)
	}
	return gate
}

// RecordActivity postpones the idle lock. It is for input the content views
// handle without passing through the gate, such as a window gaining focus.
func (g *Gate) RecordActivity() {
	if g.service != nil {
		g.service.RecordActivity()
	}
}

// Lock closes the gate now, as the Lock Now command asks. A disabled App Lock
// makes locking a no-op.
func (g *Gate) Lock() error {
	if g.service == nil {
		return nil
	}
	err := g.service.Lock(appaplock.ReasonManual)
	if errors.Is(err, appaplock.ErrLockDisabled) {
		return nil
	}
	return err
}

// Wrap renders content behind the gate. While the gate is closed, content is
// not built at all.
func (g *Gate) Wrap(content func(*ui.Context)) func(*ui.Context) {
	return func(c *ui.Context) {
		if g.service != nil {
			if g.service.LockIfIdle(g.now()) {
				g.requestRedraw()
			}
			if g.service.Snapshot().Locked {
				g.lockScreen(c)
				return
			}
			// Wake at the idle deadline. After keeps the earliest requested
			// frame, so asking each frame does not accumulate timers.
			if deadline, ok := g.service.IdleDeadline(); ok {
				delay := deadline.Sub(g.now())
				if delay <= 0 {
					delay = time.Millisecond
				}
				c.After(delay)
			}
		}

		// Pointer and scroll input that content widgets do not consume
		// reaches this ancestor and counts as activity. The event continues
		// to the widget that would handle it.
		ui.Column(c).Fill().HandleInput(func(ui.InputEvent) bool {
			g.RecordActivity()
			return false
		}).Children(func() {
			content(c)
		})
	}
}

func (g *Gate) lockScreen(c *ui.Context) {
	m := locales.In(c.Locale())
	t := c.Theme()

	ui.Column(c).Fill().Padding(48).Gap(12).Justify(ui.Center).AlignItems(ui.Center).Children(func() {
		ui.Text(c, m.AppLockTitle()).FontSize(24).Bold()
		ui.Text(c, m.AppLockBody()).TextColor(t.TextMuted)

		ui.Column(c).Width(320).Gap(8).Children(func() {
			var submitted bool
			ui.Field(c, m.AppLockPassword(), func() {
				submitted = ui.TextInput(c, &g.password).Password().AutoFocus().Submitted()
			}).Width(320)
			unlock := ui.PrimaryButton(c, m.AppLockUnlock()).Width(320).Clicked()
			if submitted || unlock {
				g.unlock(c)
			}
		})

		if message := problemText(m, g.problem); message != "" {
			ui.Text(c, message).TextColor(t.Danger)
		}
	})
}

func (g *Gate) unlock(c *ui.Context) {
	err := g.service.Unlock(g.password)
	switch {
	case err == nil:
		g.password = ""
		g.problem = ProblemNone
	case errors.Is(err, appaplock.ErrEmptyPassword):
		g.problem = ProblemEmpty
	case errors.Is(err, appaplock.ErrIncorrectPassword):
		g.problem = ProblemIncorrect
	default:
		g.problem = ProblemFailed
	}
	c.Invalidate()
}

func problemText(m locales.Messages, problem Problem) string {
	switch problem {
	case ProblemEmpty:
		return m.AppLockProblemEmpty()
	case ProblemIncorrect:
		return m.AppLockProblemIncorrect()
	case ProblemFailed:
		return m.AppLockProblemFailed()
	}
	return ""
}

func (g *Gate) requestRedraw() {
	if g.redraw != nil {
		g.redraw()
	}
}
