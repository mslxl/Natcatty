package session

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/mslxl/natcatty/domain/model"
)

// probeTransport is a fake local shell that can report a process cwd. Remote
// transports simply leave the capability off.
type probeTransport struct {
	*fakeTransport

	mu    sync.Mutex
	cwd   string
	err   error
	calls int
}

func (p *probeTransport) WorkingDirectory() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.err != nil {
		return "", p.err
	}
	return p.cwd, nil
}

func (p *probeTransport) setCWD(cwd string) {
	p.mu.Lock()
	p.cwd = cwd
	p.mu.Unlock()
}

func (p *probeTransport) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

type probeFactory struct {
	transport Transport
}

func (f probeFactory) Open(context.Context, StartSpec) (Transport, error) {
	return f.transport, nil
}

// TestLocalSessionProbesCWDWhenOSC7IsAbsent is the acceptance path: with no
// OSC 7 the local shell's process directory answers a consumer, stays cached
// across repeated asks, and is refreshed after a command finishes.
func TestLocalSessionProbesCWDWhenOSC7IsAbsent(t *testing.T) {
	transport := &probeTransport{fakeTransport: newFakeTransport(), cwd: "/work/one"}
	service := NewService(probeFactory{transport: transport},
		WithIDGenerator(func() model.SessionID { return "session-cwd" }))
	session, err := service.Open(context.Background(), StartSpec{Kind: KindLocal, Label: "Local"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	if got := session.Snapshot().CWD; got != "/work/one" {
		t.Fatalf("snapshot cwd = %q, want /work/one", got)
	}
	if calls := transport.callCount(); calls != 1 {
		t.Fatalf("probe calls = %d, want 1", calls)
	}
	if got := session.Snapshot().CWD; got != "/work/one" {
		t.Fatalf("cached snapshot cwd = %q, want /work/one", got)
	}
	if calls := transport.callCount(); calls != 1 {
		t.Fatalf("a repeated ask probed again: %d calls", calls)
	}

	// A finished command may run cd. The next consumer must see the new
	// directory without waiting for a timer.
	transport.setCWD("/work/two")
	service.SendInput([]model.SessionID{session.ID()}, []byte("cd /work/two\r"))
	transport.send("\x1b]133;D;0\x07")
	eventually(t, "the refreshed local cwd", func() bool {
		return session.Snapshot().CWD == "/work/two"
	})
	if calls := transport.callCount(); calls < 2 {
		t.Fatalf("probe calls = %d after a finished command, want a refresh", calls)
	}
}

// TestRemoteSessionKeepsUnknownCWDWithoutOSC7 proves the fallback is local
// only, even when the transport happens to implement the capability.
func TestRemoteSessionKeepsUnknownCWDWithoutOSC7(t *testing.T) {
	transport := &probeTransport{fakeTransport: newFakeTransport(), cwd: `C:\local`}
	service := NewService(probeFactory{transport: transport},
		WithIDGenerator(func() model.SessionID { return "session-remote" }))
	session, err := service.Open(context.Background(), StartSpec{
		Kind: KindSSH, HostID: "host-1", Hostname: "example.com", Username: "me",
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	if got := session.Snapshot().CWD; got != "" {
		t.Fatalf("remote snapshot cwd = %q, want unknown", got)
	}
	if calls := transport.callCount(); calls != 0 {
		t.Fatalf("a remote Session probed this machine's process: %d calls", calls)
	}
}

// TestFailedCWDProbeIsCached keeps a missing process from being looked up on
// every frame; the next prompt retries.
func TestFailedCWDProbeIsCached(t *testing.T) {
	transport := &probeTransport{
		fakeTransport: newFakeTransport(),
		err:           errors.New("no such process"),
	}
	service := NewService(probeFactory{transport: transport},
		WithIDGenerator(func() model.SessionID { return "session-gone" }))
	session, err := service.Open(context.Background(), StartSpec{Kind: KindLocal, Label: "Local"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	if got := session.Snapshot().CWD; got != "" {
		t.Fatalf("snapshot cwd = %q, want unknown", got)
	}
	if got := session.Snapshot().CWD; got != "" {
		t.Fatalf("second snapshot cwd = %q, want unknown", got)
	}
	if calls := transport.callCount(); calls != 1 {
		t.Fatalf("probe calls = %d, want one cached failure", calls)
	}
}
