package app

import "github.com/egoist/mygo"

// instanceLock is the inter-process single-instance seam. The real
// implementation is MyGo's per-user Unix-socket lock, which works on macOS,
// Windows, and Linux without a native dependency and safely reclaims a stale
// socket left by a crash. Tests inject a fake.
type instanceLock interface {
	// Acquire reports true in the first instance. In a later instance it
	// hands the launch over to the running instance and reports false.
	Acquire() (primary bool, err error)
	// OnSecondInstance registers the callback the first instance runs when a
	// later launch hands over. args excludes the program name; workingDir is
	// where the later launch started.
	OnSecondInstance(func(args []string, workingDir string)) func()
}

// mygoInstanceLock adapts MyGo's single-instance lock.
type mygoInstanceLock struct{}

func (mygoInstanceLock) Acquire() (bool, error) {
	return mygo.App.RequestSingleInstanceLock(), nil
}

func (mygoInstanceLock) OnSecondInstance(fn func([]string, string)) func() {
	return mygo.App.OnSecondInstance(fn)
}

// singleInstancePrimary acquires the single-instance lock and configures the
// second-launch callback. It returns false when another instance owns the
// lock; the caller must then exit without creating a window. A nil seam or an
// acquisition error falls back to running as the only instance, which keeps a
// launch working on a host where locking is unavailable.
func singleInstancePrimary(lock instanceLock, onSecond func([]string, string)) bool {
	if lock == nil {
		return true
	}
	primary, err := lock.Acquire()
	if err != nil {
		return true
	}
	if !primary {
		return false
	}
	if onSecond != nil {
		lock.OnSecondInstance(onSecond)
	}
	return true
}
