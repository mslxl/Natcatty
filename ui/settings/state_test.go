package settings

import (
	"context"
	"testing"

	applock "github.com/mslxl/natcatty/application/applock"
	domainapplock "github.com/mslxl/natcatty/domain/applock"
)

type fakeAppLock struct {
	snapshot  applock.Snapshot
	settings  domainapplock.Settings
	enabled   string
	disabled  string
	timeout   int
	lockCause applock.Reason
	err       error
}

func (f *fakeAppLock) Snapshot() applock.Snapshot { return f.snapshot }

func (f *fakeAppLock) Settings() domainapplock.Settings { return f.settings }

func (f *fakeAppLock) Enable(_ context.Context, password string) error {
	if f.err != nil {
		return f.err
	}
	f.enabled = password
	f.snapshot.Enabled = true
	return nil
}

func (f *fakeAppLock) Disable(_ context.Context, currentPassword string) error {
	if f.err != nil {
		return f.err
	}
	f.disabled = currentPassword
	f.snapshot.Enabled = false
	return nil
}

func (f *fakeAppLock) SetTimeout(_ context.Context, minutes int) error {
	if f.err != nil {
		return f.err
	}
	f.timeout = minutes
	return nil
}

func (f *fakeAppLock) Lock(reason applock.Reason) error {
	if f.err != nil {
		return f.err
	}
	f.lockCause = reason
	f.snapshot.Locked = true
	return nil
}

func TestEnableValidatesThePasswordAndConfirmation(t *testing.T) {
	lock := &fakeAppLock{}
	state := NewAppLockState(lock)

	if state.Enable() {
		t.Fatal("an empty password enabled App Lock")
	}
	if state.Problem() != AppLockProblemEmpty {
		t.Fatalf("problem = %q, want %q", state.Problem(), AppLockProblemEmpty)
	}

	*state.Password() = "one"
	*state.Confirmation() = "two"
	if state.Enable() {
		t.Fatal("mismatched passwords enabled App Lock")
	}
	if state.Problem() != AppLockProblemMismatch {
		t.Fatalf("problem = %q, want %q", state.Problem(), AppLockProblemMismatch)
	}

	*state.Confirmation() = "one"
	if !state.Enable() {
		t.Fatalf("Enable failed: %q", state.Problem())
	}
	if lock.enabled != "one" {
		t.Fatalf("enabled with %q, want one", lock.enabled)
	}
	if *state.Password() != "" || *state.Confirmation() != "" || state.Problem() != AppLockProblemNone {
		t.Fatal("a successful enable left input or a problem behind")
	}
}

func TestEnableReportsARejectedPassword(t *testing.T) {
	lock := &fakeAppLock{err: applock.ErrIncorrectPassword}
	state := NewAppLockState(lock)
	*state.Password() = "one"
	*state.Confirmation() = "one"

	if state.Enable() {
		t.Fatal("Enable returned true after an error")
	}
	if state.Problem() != AppLockProblemIncorrect {
		t.Fatalf("problem = %q, want %q", state.Problem(), AppLockProblemIncorrect)
	}
}

func TestDisableNeedsTheCurrentPassword(t *testing.T) {
	lock := &fakeAppLock{snapshot: applock.Snapshot{Enabled: true, CanLock: true}}
	state := NewAppLockState(lock)

	if state.Disable() {
		t.Fatal("an empty current password disabled App Lock")
	}
	*state.CurrentPassword() = "secret"
	if !state.Disable() {
		t.Fatalf("Disable failed: %q", state.Problem())
	}
	if lock.disabled != "secret" {
		t.Fatalf("disabled with %q, want secret", lock.disabled)
	}
	if *state.CurrentPassword() != "" {
		t.Fatal("the current password was left in the form")
	}
}

func TestSetTimeoutUsesAnOfferedValue(t *testing.T) {
	lock := &fakeAppLock{}
	state := NewAppLockState(lock)

	if !state.SetTimeout(5) {
		t.Fatalf("SetTimeout failed: %q", state.Problem())
	}
	if lock.timeout != 5 {
		t.Fatalf("timeout = %d, want 5", lock.timeout)
	}

	if !state.SetTimeout(7) {
		t.Fatalf("SetTimeout failed: %q", state.Problem())
	}
	if lock.timeout != domainapplock.DefaultTimeoutMinutes {
		t.Fatalf("timeout = %d, want the default", lock.timeout)
	}
}

func TestLockNowAsksForAManualLock(t *testing.T) {
	lock := &fakeAppLock{snapshot: applock.Snapshot{Enabled: true, CanLock: true}}
	state := NewAppLockState(lock)

	if !state.LockNow() {
		t.Fatalf("LockNow failed: %q", state.Problem())
	}
	if lock.lockCause != applock.ReasonManual {
		t.Fatalf("reason = %q, want manual", lock.lockCause)
	}
}

func TestSnapshotComesFromTheService(t *testing.T) {
	lock := &fakeAppLock{snapshot: applock.Snapshot{Enabled: true, Locked: true, TimeoutMinutes: 30}}
	state := NewAppLockState(lock)
	if got := state.Snapshot(); got.TimeoutMinutes != 30 || !got.Locked {
		t.Fatalf("snapshot = %+v", got)
	}
}
