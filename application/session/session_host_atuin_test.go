package session

import (
	"context"
	"sync"
	"testing"
	"time"
)

type fakeHostAtuin struct {
	mu         sync.Mutex
	probe      HostAtuinProbe
	mark       bool
	probes     []StartSpec
	marks      []StartSpec
	afterClose []StartSpec
}

func (f *fakeHostAtuin) Probe(_ context.Context, spec StartSpec) HostAtuinProbe {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.probes = append(f.probes, spec)
	return f.probe
}

func (f *fakeHostAtuin) MarkNoticed(_ context.Context, spec StartSpec) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.marks = append(f.marks, spec)
	return f.mark
}

func (f *fakeHostAtuin) markCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.marks)
}

func (f *fakeHostAtuin) ImportAfterClose(spec StartSpec) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.afterClose = append(f.afterClose, spec)
}

func (f *fakeHostAtuin) probeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.probes)
}

func (f *fakeHostAtuin) afterCloseSpecs() []StartSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]StartSpec(nil), f.afterClose...)
}

func waitForSessionEnd(t *testing.T, session *Session) {
	t.Helper()
	select {
	case <-session.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the Session did not end")
	}
}

// The Host-Atuin probe runs before the capture hook is planned, so a Host with
// Atuin skips the Netcatty hook entirely and the terminal spec is untouched.
func TestHostAtuinPresentSkipsTheCaptureHookAndAnnouncesOnce(t *testing.T) {
	factory := &fakeFactory{transport: newFakeTransport()}
	hookCalls := 0
	hook := captureHookFunc(func(spec StartSpec) (CaptureHookPlan, bool) {
		hookCalls++
		spec.Command = []string{"/bin/hooked"}
		return CaptureHookPlan{Spec: spec}, false
	})
	policy := &fakeHostAtuin{probe: HostAtuinProbe{Present: true}, mark: true}
	service := newTestService(factory, WithCaptureHook(hook), WithHostAtuin(policy))

	session, err := service.Open(context.Background(), StartSpec{
		Kind: KindSSH, HostID: "host-1", Hostname: "web.example.test",
		Username: "deploy", Command: []string{"/bin/sh"},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if hookCalls != 0 {
		t.Fatalf("the capture hook ran %d times for a Host Atuin Session", hookCalls)
	}
	if len(factory.spec.Command) != 1 || factory.spec.Command[0] != "/bin/sh" {
		t.Fatalf("factory command = %v, want the unhooked command", factory.spec.Command)
	}
	if policy.probeCount() != 1 {
		t.Fatalf("probes = %d, want 1", policy.probeCount())
	}
	if policy.markCount() != 1 {
		t.Fatalf("notice marks = %d, want 1", policy.markCount())
	}
	probed := policy.probes[0]
	if probed.HostID != "host-1" || probed.Hostname != "web.example.test" {
		t.Fatalf("probed spec = %+v", probed)
	}
	snapshot := session.Snapshot()
	if snapshot.Notice == nil || snapshot.Notice.Code != NoticeHostAtuinDetected {
		t.Fatalf("notice = %+v, want the Host Atuin notice", snapshot.Notice)
	}

	if err := session.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	waitForSessionEnd(t, session)
	closed := policy.afterCloseSpecs()
	if len(closed) != 1 || closed[0].HostID != "host-1" {
		t.Fatalf("post-Session imports = %+v, want one for host-1", closed)
	}
}

func TestHostAtuinAbsentKeepsTheCaptureHookAndPublishesNoNotice(t *testing.T) {
	factory := &fakeFactory{transport: newFakeTransport()}
	hookCalls := 0
	hook := captureHookFunc(func(spec StartSpec) (CaptureHookPlan, bool) {
		hookCalls++
		spec.Command = []string{"/bin/hooked"}
		return CaptureHookPlan{Spec: spec}, false
	})
	policy := &fakeHostAtuin{}
	service := newTestService(factory, WithCaptureHook(hook), WithHostAtuin(policy))

	session, err := service.Open(context.Background(), StartSpec{
		Kind: KindSSH, HostID: "host-1", Command: []string{"/bin/sh"},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	if hookCalls != 1 {
		t.Fatalf("the capture hook ran %d times, want 1", hookCalls)
	}
	if len(factory.spec.Command) != 1 || factory.spec.Command[0] != "/bin/hooked" {
		t.Fatalf("factory command = %v, want the hooked command", factory.spec.Command)
	}
	if session.Snapshot().Notice != nil {
		t.Fatalf("an absent Host produced a notice: %+v", session.Snapshot().Notice)
	}
	if policy.markCount() != 0 {
		t.Fatalf("an absent Host consumed a notice mark: %d", policy.markCount())
	}
}

// A Host that skipped the hook does not import again on teardown, so the
// Netcatty hook is never installed next to a Host Atuin session.
func TestHostAtuinAbsentHasNoPostSessionImport(t *testing.T) {
	factory := &fakeFactory{transport: newFakeTransport()}
	policy := &fakeHostAtuin{}
	service := newTestService(factory, WithHostAtuin(policy))

	session, err := service.Open(context.Background(), StartSpec{Kind: KindSSH, HostID: "host-1"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	waitForSessionEnd(t, session)
	if got := policy.afterCloseSpecs(); len(got) != 0 {
		t.Fatalf("an absent Host imported after close: %+v", got)
	}
}

// A Host whose Atuin history could not be read keeps the hook skipped, shows
// a warning, and does not consume the one-time preference notice.
func TestHostAtuinImportFailurePublishesAWarningWithoutConsumingTheNotice(t *testing.T) {
	factory := &fakeFactory{transport: newFakeTransport()}
	hookCalls := 0
	hook := captureHookFunc(func(spec StartSpec) (CaptureHookPlan, bool) {
		hookCalls++
		return CaptureHookPlan{Spec: spec}, false
	})
	policy := &fakeHostAtuin{
		probe: HostAtuinProbe{Present: true, Failed: true},
		mark:  true,
	}
	service := newTestService(factory, WithCaptureHook(hook), WithHostAtuin(policy))

	session, err := service.Open(context.Background(), StartSpec{
		Kind: KindSSH, HostID: "host-1", Label: "web",
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	if hookCalls != 0 {
		t.Fatalf("the capture hook ran %d times, want 0", hookCalls)
	}
	snapshot := session.Snapshot()
	if snapshot.Notice == nil || snapshot.Notice.Code != NoticeHostAtuinImportFailed {
		t.Fatalf("notice = %+v, want the import failure warning", snapshot.Notice)
	}
	if policy.markCount() != 0 {
		t.Fatalf("a failed import consumed the one-time notice: %d marks", policy.markCount())
	}
}

func TestHostAtuinPresentAlreadyNoticedStillSkipsTheHook(t *testing.T) {
	factory := &fakeFactory{transport: newFakeTransport()}
	hookCalls := 0
	hook := captureHookFunc(func(spec StartSpec) (CaptureHookPlan, bool) {
		hookCalls++
		return CaptureHookPlan{Spec: spec}, false
	})
	policy := &fakeHostAtuin{probe: HostAtuinProbe{Present: true}, mark: false}
	service := newTestService(factory, WithCaptureHook(hook), WithHostAtuin(policy))

	session, err := service.Open(context.Background(), StartSpec{
		Kind: KindSSH, HostID: "host-1", Label: "web",
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	if hookCalls != 0 {
		t.Fatalf("the capture hook ran %d times, want 0", hookCalls)
	}
	if session.Snapshot().Notice != nil {
		t.Fatalf("a previously announced Host repeated its notice: %+v", session.Snapshot().Notice)
	}
}

// The probe is still bounded when the caller's context has no deadline.
func TestHostAtuinProbeHasABoundedContext(t *testing.T) {
	var hasDeadline bool
	policy := &deadlinePolicy{seen: &hasDeadline}
	service := newTestService(&fakeFactory{transport: newFakeTransport()}, WithHostAtuin(policy))

	session, err := service.Open(context.Background(), StartSpec{Kind: KindSSH, HostID: "host-1"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	if !hasDeadline {
		t.Fatal("the probe received an unbounded context")
	}
}

type deadlinePolicy struct {
	seen *bool
}

func (p *deadlinePolicy) Probe(ctx context.Context, _ StartSpec) HostAtuinProbe {
	_, ok := ctx.Deadline()
	*p.seen = ok
	return HostAtuinProbe{}
}

func (p *deadlinePolicy) MarkNoticed(context.Context, StartSpec) bool { return false }

func (p *deadlinePolicy) ImportAfterClose(StartSpec) {}
