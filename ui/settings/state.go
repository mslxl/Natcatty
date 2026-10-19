package settings

import (
	"context"
	"errors"

	applock "github.com/mslxl/natcatty/application/applock"
	domainapplock "github.com/mslxl/natcatty/domain/applock"
)

// AppLock is the App Lock use-case seam. The application Service implements
// it; tests provide a fake.
type AppLock interface {
	Snapshot() applock.Snapshot
	Settings() domainapplock.Settings
	Enable(ctx context.Context, password string) error
	Disable(ctx context.Context, currentPassword string) error
	SetTimeout(ctx context.Context, minutes int) error
	Lock(reason applock.Reason) error
}

// AppLockProblem identifies a Settings failure the view must localize.
type AppLockProblem string

const (
	AppLockProblemNone      AppLockProblem = ""
	AppLockProblemEmpty     AppLockProblem = "empty"
	AppLockProblemMismatch  AppLockProblem = "mismatch"
	AppLockProblemIncorrect AppLockProblem = "incorrect"
	AppLockProblemFailed    AppLockProblem = "failed"
)

// AppLockState owns the Settings window's App Lock form. It is mutated only
// on the UI thread.
type AppLockState struct {
	lock          AppLock
	password      string
	confirm       string
	current       string
	problem       AppLockProblem
	requestRedraw func()
}

// AppLockOption customizes an AppLockState.
type AppLockOption func(*AppLockState)

// WithAppLockRedrawRequest sets the function called after the form changes.
func WithAppLockRedrawRequest(fn func()) AppLockOption {
	return func(s *AppLockState) { s.requestRedraw = fn }
}

// NewAppLockState returns the App Lock form for lock.
func NewAppLockState(lock AppLock, opts ...AppLockOption) *AppLockState {
	state := &AppLockState{lock: lock}
	for _, opt := range opts {
		opt(state)
	}
	return state
}

// Snapshot reports the App Lock state the view renders.
func (s *AppLockState) Snapshot() applock.Snapshot {
	return s.lock.Snapshot()
}

// Password returns the new-password input.
func (s *AppLockState) Password() *string { return &s.password }

// Confirmation returns the new-password confirmation input.
func (s *AppLockState) Confirmation() *string { return &s.confirm }

// CurrentPassword returns the password that disables App Lock.
func (s *AppLockState) CurrentPassword() *string { return &s.current }

// Problem returns the current form failure.
func (s *AppLockState) Problem() AppLockProblem { return s.problem }

// Enable turns App Lock on with a new password. The password is never stored,
// only its verifier.
func (s *AppLockState) Enable() bool {
	if s.password == "" {
		s.problem = AppLockProblemEmpty
		s.redraw()
		return false
	}
	if s.password != s.confirm {
		s.problem = AppLockProblemMismatch
		s.redraw()
		return false
	}
	if err := s.lock.Enable(context.Background(), s.password); err != nil {
		s.problem = appLockProblemFor(err)
		s.redraw()
		return false
	}
	s.password = ""
	s.confirm = ""
	s.problem = AppLockProblemNone
	s.redraw()
	return true
}

// Disable turns App Lock off after checking the current password.
func (s *AppLockState) Disable() bool {
	if s.current == "" {
		s.problem = AppLockProblemEmpty
		s.redraw()
		return false
	}
	if err := s.lock.Disable(context.Background(), s.current); err != nil {
		s.problem = appLockProblemFor(err)
		s.redraw()
		return false
	}
	s.current = ""
	s.problem = AppLockProblemNone
	s.redraw()
	return true
}

// SetTimeout stores an offered idle timeout. An unsupported value falls back
// to the default.
func (s *AppLockState) SetTimeout(minutes int) bool {
	if err := s.lock.SetTimeout(context.Background(), domainapplock.NormalizeTimeoutMinutes(minutes)); err != nil {
		s.problem = appLockProblemFor(err)
		s.redraw()
		return false
	}
	s.problem = AppLockProblemNone
	s.redraw()
	return true
}

// LockNow closes the gate immediately.
func (s *AppLockState) LockNow() bool {
	if err := s.lock.Lock(applock.ReasonManual); err != nil {
		s.problem = appLockProblemFor(err)
		s.redraw()
		return false
	}
	s.problem = AppLockProblemNone
	s.redraw()
	return true
}

func appLockProblemFor(err error) AppLockProblem {
	switch {
	case errors.Is(err, applock.ErrEmptyPassword):
		return AppLockProblemEmpty
	case errors.Is(err, applock.ErrIncorrectPassword):
		return AppLockProblemIncorrect
	default:
		return AppLockProblemFailed
	}
}

func (s *AppLockState) redraw() {
	if s.requestRedraw != nil {
		s.requestRedraw()
	}
}
