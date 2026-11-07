package session

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mslxl/natcatty/domain/model"
)

type captureHookFunc func(StartSpec) (CaptureHookPlan, bool)

func (f captureHookFunc) Plan(spec StartSpec) (CaptureHookPlan, bool) { return f(spec) }

// The plan rewrites the StartSpec before the transport opens: the factory must
// see the rewritten command, environment, and remote command.
func TestCaptureHookPlanReachesTheTransport(t *testing.T) {
	factory := &fakeFactory{transport: newFakeTransport()}
	hook := captureHookFunc(func(spec StartSpec) (CaptureHookPlan, bool) {
		spec.Command = []string{"/bin/zsh"}
		spec.Env = append(spec.Env, "NETCATTY_HOOK=1")
		return CaptureHookPlan{Spec: spec}, false
	})
	service := newTestService(factory, WithCaptureHook(hook))
	session, err := service.Open(context.Background(), StartSpec{Kind: KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	if len(factory.spec.Command) != 1 || factory.spec.Command[0] != "/bin/zsh" {
		t.Fatalf("factory command = %v", factory.spec.Command)
	}
	if len(factory.spec.Env) != 1 || factory.spec.Env[0] != "NETCATTY_HOOK=1" {
		t.Fatalf("factory env = %v", factory.spec.Env)
	}
}

// A plan's cleanup runs when the Session ends, exactly once.
func TestCaptureHookCleanupRunsOnSessionClose(t *testing.T) {
	factory := &fakeFactory{transport: newFakeTransport()}
	var mu sync.Mutex
	cleaned := 0
	hook := captureHookFunc(func(spec StartSpec) (CaptureHookPlan, bool) {
		return CaptureHookPlan{
			Spec: spec,
			Cleanup: func() {
				mu.Lock()
				cleaned++
				mu.Unlock()
			},
		}, false
	})
	service := newTestService(factory, WithCaptureHook(hook))
	session, err := service.Open(context.Background(), StartSpec{Kind: KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	_ = session.Close()
	select {
	case <-session.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("the Session did not end")
	}
	mu.Lock()
	got := cleaned
	mu.Unlock()
	if got != 1 {
		t.Fatalf("cleanup calls = %d, want 1", got)
	}
}

// A failed Open releases the plan's resources instead of leaking them.
func TestCaptureHookCleanupRunsWhenOpenFails(t *testing.T) {
	cleaned := 0
	hook := captureHookFunc(func(spec StartSpec) (CaptureHookPlan, bool) {
		return CaptureHookPlan{
			Spec:    spec,
			Cleanup: func() { cleaned++ },
		}, false
	})
	service := newTestService(failingFactory{err: errors.New("dial failed")}, WithCaptureHook(hook))
	if _, err := service.Open(context.Background(), StartSpec{Kind: KindLocal}); err == nil {
		t.Fatal("Open succeeded despite a failing factory")
	}
	if cleaned != 1 {
		t.Fatalf("cleanup calls = %d, want 1", cleaned)
	}
}

// A hook that cannot be installed is a capability notice, never a connection
// failure, and it is reported once per Host.
func TestCaptureHookFailureAnnouncesOncePerHost(t *testing.T) {
	factory := &fakeFactory{transport: newFakeTransport()}
	hook := captureHookFunc(func(spec StartSpec) (CaptureHookPlan, bool) {
		return CaptureHookPlan{Spec: spec}, true
	})
	service := newTestService(factory, WithCaptureHook(hook))

	open := func(hostID string) Snapshot {
		t.Helper()
		session, err := service.Open(context.Background(), StartSpec{
			Kind: KindSSH, HostID: model.HostID(hostID), Hostname: "example.test",
		})
		if err != nil {
			t.Fatalf("Open %s: %v", hostID, err)
		}
		t.Cleanup(func() { _ = session.Close() })
		return session.Snapshot()
	}

	first := open("host-1")
	if first.Notice == nil || first.Notice.Code != NoticeCaptureHookUnavailable {
		t.Fatalf("first notice = %+v", first.Notice)
	}
	if first.Status != StatusRunning || first.Connection != ConnectionStatusActive {
		t.Fatalf("a hook failure changed the connection state: %+v", first)
	}
	if second := open("host-1"); second.Notice != nil {
		t.Fatalf("the same Host announced the notice twice: %+v", second.Notice)
	}
	if other := open("host-2"); other.Notice == nil ||
		other.Notice.Code != NoticeCaptureHookUnavailable {
		t.Fatalf("a different Host did not announce the notice: %+v", other.Notice)
	}
}

// A nil capture hook leaves the Open path exactly as it was.
func TestNoCaptureHookLeavesSpecAlone(t *testing.T) {
	factory := &fakeFactory{transport: newFakeTransport()}
	service := newTestService(factory)
	session, err := service.Open(context.Background(), StartSpec{
		Kind: KindLocal, Command: []string{"/bin/sh"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	if len(factory.spec.Command) != 1 || factory.spec.Command[0] != "/bin/sh" {
		t.Fatalf("factory command = %v", factory.spec.Command)
	}
	if session.Snapshot().Notice != nil {
		t.Fatal("a nil hook produced a notice")
	}
}
