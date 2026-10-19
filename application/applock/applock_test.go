package applock

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	domainapplock "github.com/mslxl/natcatty/domain/applock"
)

type fakeRepository struct {
	settings domainapplock.Settings
	loads    int
	saves    int
	loadErr  error
	saveErr  error
}

func (f *fakeRepository) Load(context.Context) (domainapplock.Settings, error) {
	f.loads++
	if f.loadErr != nil {
		return domainapplock.Settings{}, f.loadErr
	}
	return f.settings, nil
}

func (f *fakeRepository) Save(_ context.Context, settings domainapplock.Settings) error {
	f.saves++
	if f.saveErr != nil {
		return f.saveErr
	}
	f.settings = settings
	return nil
}

func newFakeRepository() *fakeRepository {
	return &fakeRepository{settings: domainapplock.DefaultSettings()}
}

type testClock struct{ now time.Time }

func (c *testClock) Now() time.Time { return c.now }
func (c *testClock) Advance(d time.Duration) {
	c.now = c.now.Add(d)
}

func newTestService(t *testing.T, repo *fakeRepository) (*Service, *testClock) {
	t.Helper()
	clock := &testClock{now: time.Unix(1700000000, 0).UTC()}
	service := NewService(repo,
		WithClock(clock.Now),
		WithRandom(bytes.NewReader(make([]byte, 4096))),
	)
	if err := service.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	return service, clock
}

func TestLoadStartsUnlockedWithoutASavedPassword(t *testing.T) {
	service, _ := newTestService(t, newFakeRepository())

	snapshot := service.Snapshot()
	if snapshot.Locked {
		t.Error("a fresh App Lock starts locked")
	}
	if snapshot.Enabled {
		t.Error("a fresh App Lock is enabled")
	}
	if snapshot.TimeoutMinutes != domainapplock.DefaultTimeoutMinutes {
		t.Errorf("timeout = %d, want %d", snapshot.TimeoutMinutes, domainapplock.DefaultTimeoutMinutes)
	}
}

func TestEnabledAppLockStartsLockedOnTheNextLoad(t *testing.T) {
	repo := newFakeRepository()
	first, _ := newTestService(t, repo)
	if err := first.Enable(context.Background(), "s3cret"); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if first.Snapshot().Locked {
		t.Error("enabling App Lock locked the current session immediately")
	}
	if !repo.settings.Enabled || repo.settings.PasswordVerifier == nil {
		t.Fatalf("saved settings = %+v, want enabled with a verifier", repo.settings)
	}

	second, _ := newTestService(t, repo)
	snapshot := second.Snapshot()
	if !snapshot.Locked || snapshot.Reason != ReasonStartup {
		t.Fatalf("snapshot = %+v, want a startup lock", snapshot)
	}
}

func TestUnlockAcceptsOnlyTheCorrectPassword(t *testing.T) {
	repo := newFakeRepository()
	service, _ := newTestService(t, repo)
	if err := service.Enable(context.Background(), "s3cret"); err != nil {
		t.Fatal(err)
	}
	reopened, _ := newTestService(t, repo)

	if err := reopened.Unlock("wrong"); !errors.Is(err, ErrIncorrectPassword) {
		t.Fatalf("Unlock(wrong) = %v, want ErrIncorrectPassword", err)
	}
	if !reopened.Snapshot().Locked {
		t.Fatal("a wrong password unlocked the app")
	}
	if err := reopened.Unlock(""); !errors.Is(err, ErrEmptyPassword) {
		t.Fatalf("Unlock(empty) = %v, want ErrEmptyPassword", err)
	}
	if err := reopened.Unlock("s3cret"); err != nil {
		t.Fatalf("Unlock(correct): %v", err)
	}
	if snapshot := reopened.Snapshot(); snapshot.Locked || snapshot.Reason != "" {
		t.Fatalf("snapshot = %+v, want unlocked", snapshot)
	}
}

func TestDisableRequiresTheCurrentPasswordAndForgetsTheVerifier(t *testing.T) {
	repo := newFakeRepository()
	service, _ := newTestService(t, repo)
	if err := service.Enable(context.Background(), "s3cret"); err != nil {
		t.Fatal(err)
	}

	if err := service.Disable(context.Background(), "wrong"); !errors.Is(err, ErrIncorrectPassword) {
		t.Fatalf("Disable(wrong) = %v, want ErrIncorrectPassword", err)
	}
	if !repo.settings.Enabled {
		t.Fatal("a wrong password disabled App Lock")
	}
	if err := service.Disable(context.Background(), "s3cret"); err != nil {
		t.Fatalf("Disable(correct): %v", err)
	}
	if repo.settings.Enabled || repo.settings.PasswordVerifier != nil {
		t.Fatalf("saved settings = %+v, want disabled without a verifier", repo.settings)
	}
	if service.Snapshot().Locked {
		t.Fatal("disabling App Lock left the app locked")
	}
}

func TestSetTimeoutNormalizesAndPersists(t *testing.T) {
	repo := newFakeRepository()
	service, _ := newTestService(t, repo)
	if err := service.Enable(context.Background(), "s3cret"); err != nil {
		t.Fatal(err)
	}

	for _, minutes := range domainapplock.TimeoutOptions {
		if err := service.SetTimeout(context.Background(), minutes); err != nil {
			t.Fatalf("SetTimeout(%d): %v", minutes, err)
		}
		if repo.settings.TimeoutMinutes != minutes {
			t.Errorf("saved timeout = %d, want %d", repo.settings.TimeoutMinutes, minutes)
		}
	}
	if err := service.SetTimeout(context.Background(), 7); err != nil {
		t.Fatalf("SetTimeout(7): %v", err)
	}
	if repo.settings.TimeoutMinutes != domainapplock.DefaultTimeoutMinutes {
		t.Errorf("saved timeout = %d, want the default %d", repo.settings.TimeoutMinutes, domainapplock.DefaultTimeoutMinutes)
	}
}

func TestIdleTimeoutLocksAtTheConfiguredMinute(t *testing.T) {
	tests := []struct {
		name    string
		timeout int
		idle    time.Duration
		want    bool
	}{
		{"never", 0, 24 * time.Hour, false},
		{"one minute before", 1, 59 * time.Second, false},
		{"one minute", 1, time.Minute, true},
		{"five minutes", 5, 5 * time.Minute, true},
		{"fifteen minutes", 15, 15 * time.Minute, true},
		{"thirty minutes", 30, 30 * time.Minute, true},
		{"sixty minutes", 60, 60 * time.Minute, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := newFakeRepository()
			service, clock := newTestService(t, repo)
			if err := service.Enable(context.Background(), "s3cret"); err != nil {
				t.Fatal(err)
			}
			if err := service.SetTimeout(context.Background(), test.timeout); err != nil {
				t.Fatal(err)
			}
			clock.Advance(test.idle)

			locked := service.LockIfIdle(clock.Now())
			if locked != test.want {
				t.Fatalf("LockIfIdle after %v = %v, want %v", test.idle, locked, test.want)
			}
			if got := service.Snapshot().Locked; got != test.want {
				t.Fatalf("Locked = %v, want %v", got, test.want)
			}
			if test.want && service.Snapshot().Reason != ReasonIdle {
				t.Fatalf("reason = %q, want %q", service.Snapshot().Reason, ReasonIdle)
			}
		})
	}
}

func TestActivityPostponesTheIdleLock(t *testing.T) {
	repo := newFakeRepository()
	service, clock := newTestService(t, repo)
	if err := service.Enable(context.Background(), "s3cret"); err != nil {
		t.Fatal(err)
	}
	if err := service.SetTimeout(context.Background(), 1); err != nil {
		t.Fatal(err)
	}

	clock.Advance(50 * time.Second)
	service.RecordActivity()
	clock.Advance(50 * time.Second)
	if service.LockIfIdle(clock.Now()) {
		t.Fatal("activity did not postpone the idle lock")
	}
	clock.Advance(10 * time.Second)
	if !service.LockIfIdle(clock.Now()) {
		t.Fatal("the lock did not engage one minute after the last activity")
	}
}

func TestManualLockNeedsEnabledAppLock(t *testing.T) {
	service, _ := newTestService(t, newFakeRepository())
	if err := service.Lock(ReasonManual); !errors.Is(err, ErrLockDisabled) {
		t.Fatalf("Lock = %v, want ErrLockDisabled", err)
	}

	repo := newFakeRepository()
	enabled, _ := newTestService(t, repo)
	if err := enabled.Enable(context.Background(), "s3cret"); err != nil {
		t.Fatal(err)
	}
	if err := enabled.Lock(ReasonManual); err != nil {
		t.Fatalf("Lock: %v", err)
	}
	if snapshot := enabled.Snapshot(); !snapshot.Locked || snapshot.Reason != ReasonManual {
		t.Fatalf("snapshot = %+v, want a manual lock", snapshot)
	}
}

func TestIdleDeadlineIsReportedOnlyWhenItCanLock(t *testing.T) {
	service, clock := newTestService(t, newFakeRepository())
	if _, ok := service.IdleDeadline(); ok {
		t.Error("a disabled App Lock reported a deadline")
	}
	if err := service.Enable(context.Background(), "s3cret"); err != nil {
		t.Fatal(err)
	}
	deadline, ok := service.IdleDeadline()
	if !ok {
		t.Fatal("an enabled App Lock reported no deadline")
	}
	if want := clock.Now().Add(15 * time.Minute); !deadline.Equal(want) {
		t.Errorf("deadline = %v, want %v", deadline, want)
	}
	if err := service.SetTimeout(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	if _, ok := service.IdleDeadline(); ok {
		t.Error("the never timeout reported a deadline")
	}
}

func TestLoadReportsAStorageFailure(t *testing.T) {
	repo := &fakeRepository{loadErr: errors.New("disk")}
	service := NewService(repo)
	if err := service.Load(context.Background()); err == nil {
		t.Fatal("Load ignored a storage failure")
	}
}
