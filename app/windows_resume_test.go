package app

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/shell/state"
)

// resumeTransport is a Session transport that records the resume handover the
// way the in-process ET transport does: PreserveResume keeps the credentials,
// an explicit Close or DiscardResume clears them.
type resumeTransport struct {
	done   chan struct{}
	failed chan struct{}
	once   sync.Once
	fail   sync.Once

	mu            sync.Mutex
	preserveCalls int
	discardCalls  int
}

func newResumeTransport() *resumeTransport {
	return &resumeTransport{done: make(chan struct{}), failed: make(chan struct{})}
}

func (t *resumeTransport) Read([]byte) (int, error) {
	select {
	case <-t.done:
		return 0, io.EOF
	case <-t.failed:
		return 0, errors.New("et link lost")
	}
}

func (t *resumeTransport) Write(p []byte) (int, error) { return len(p), nil }
func (t *resumeTransport) Resize(int, int) error       { return nil }

// End classifies the only end this fake produces besides a deliberate close.
func (t *resumeTransport) End() appsession.End {
	return appsession.End{Reason: appsession.EndReasonError}
}

func (t *resumeTransport) PreserveResume() {
	t.mu.Lock()
	t.preserveCalls++
	t.mu.Unlock()
}

func (t *resumeTransport) DiscardResume() {
	t.mu.Lock()
	t.discardCalls++
	t.mu.Unlock()
}

func (t *resumeTransport) Close() error {
	t.once.Do(func() {
		close(t.done)
		t.mu.Lock()
		preserved := t.preserveCalls > 0
		t.mu.Unlock()
		if !preserved {
			t.DiscardResume()
		}
	})
	return nil
}

// drop ends the link like a network failure and starts the reconnect path.
func (t *resumeTransport) drop() {
	t.fail.Do(func() { close(t.failed) })
}

func (t *resumeTransport) preserves() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.preserveCalls
}

func (t *resumeTransport) discards() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.discardCalls
}

func (t *resumeTransport) closed() bool {
	select {
	case <-t.done:
		return true
	default:
		return false
	}
}

// resumeFactory hands out the scripted transports. An unexpected extra Open
// (a reconnect timer firing) blocks until the Session is closed instead of
// introducing a transport the test did not plan.
type resumeFactory struct {
	mu         sync.Mutex
	transports []*resumeTransport
	calls      int
}

func (f *resumeFactory) Open(ctx context.Context, spec appsession.StartSpec) (appsession.Transport, error) {
	f.mu.Lock()
	index := f.calls
	f.calls++
	var transport *resumeTransport
	if index < len(f.transports) {
		transport = f.transports[index]
	}
	f.mu.Unlock()
	if transport != nil {
		return transport, nil
	}
	<-ctx.Done()
	return nil, context.Cause(ctx)
}

// newResumeWindowGraph mirrors the composition root with a factory the test
// controls, so the real window hooks and the real shutdown helper run against
// a resume-aware transport.
func newResumeWindowGraph(t *testing.T, factory appsession.Factory) *windowGraph {
	t.Helper()
	service := appsession.NewService(factory)
	t.Cleanup(service.CloseAll)
	graph := &windowGraph{
		host:      &fakeWindowHost{},
		service:   service,
		confirmer: &recordingConfirmer{answer: true},
	}
	graph.state = state.NewState(
		state.WithSessions(service, func(*appsession.Session) (state.Surface, error) {
			return &wiringSurface{}, nil
		}),
		state.WithDetachHandler(func(id model.TabID) {
			graph.manager.OpenDetached(id)
		}),
		state.WithDetachReattachHandler(func(id model.TabID) {
			graph.manager.ReattachClose(id)
		}),
		state.WithDetachedCloseRequest(func(id model.TabID) {
			graph.manager.RequestDetachedClose(id)
		}),
	)
	graph.manager = newWindowManager(
		graph.host,
		graph.confirmer,
		nil,
		graph.state,
		func() func(*ui.Context) { return func(*ui.Context) {} },
		func() string { return "en" },
	)
	return graph
}

func etResumeHost() domainvault.Host {
	return domainvault.Host{
		ID: "host-et", Label: "et", Hostname: "et.example.com",
		Port: 2022, Username: "bob", Protocol: domainvault.ProtocolET, ETPort: 8737,
	}
}

// openResumeSession opens one ET Host through the shell and waits until its
// Session surface is attached.
func openResumeSession(t *testing.T, graph *windowGraph) {
	t.Helper()
	if err := graph.state.OpenHost(etResumeHost()); err != nil {
		t.Fatalf("OpenHost: %v", err)
	}
	waitFor(t, "the ET Session surface", func() bool {
		graph.state.Flush()
		return graph.state.TabCount() == 1 && graph.state.ActiveSurface() != nil
	})
}

func waitForTransportDone(t *testing.T, transport *resumeTransport) {
	t.Helper()
	waitFor(t, "the Session transport to close", transport.closed)
}

// TestGracefulQuitPreservesETResumeThroughWindowClose replays MyGo's quit
// ordering: ConfirmQuit, every window OnClosed, then OnWillQuit. The main
// window hook must preserve the credentials before the one-shot Session close
// is consumed, or the later CloseAll path cannot.
func TestGracefulQuitPreservesETResumeThroughWindowClose(t *testing.T) {
	transport := newResumeTransport()
	graph := newResumeWindowGraph(t, &resumeFactory{transports: []*resumeTransport{transport}})
	graph.manager.OpenMain()
	openResumeSession(t, graph)

	if !graph.manager.ConfirmQuit() {
		t.Fatal("ConfirmQuit refused after the confirmer approved")
	}
	main := graph.manager.Main()
	main.Close()
	waitForTransportDone(t, transport)
	shutdownSessions(graph.state, graph.service)

	if got := transport.preserves(); got == 0 {
		t.Fatal("a graceful quit discarded the ET resume credentials")
	}
	if got := transport.discards(); got != 0 {
		t.Fatalf("a graceful quit called DiscardResume %d times", got)
	}
}

// TestLastMainWindowClosePreservesETResume covers closing the main window
// without Cmd+Q: that close is the quit trigger, and OnWillQuit still runs
// afterwards.
func TestLastMainWindowClosePreservesETResume(t *testing.T) {
	transport := newResumeTransport()
	graph := newResumeWindowGraph(t, &resumeFactory{transports: []*resumeTransport{transport}})
	graph.manager.OpenMain()
	openResumeSession(t, graph)

	graph.manager.Main().Close()
	waitForTransportDone(t, transport)
	shutdownSessions(graph.state, graph.service)

	if got := transport.preserves(); got == 0 {
		t.Fatal("closing the last main window discarded the ET resume credentials")
	}
	if got := transport.discards(); got != 0 {
		t.Fatalf("closing the last main window called DiscardResume %d times", got)
	}
}

// TestQuitDuringReconnectDelayPreservesETResume closes the window while the
// Session has already preserved and closed its old transport for a reconnect
// handover and is waiting for the replacement dial.
func TestQuitDuringReconnectDelayPreservesETResume(t *testing.T) {
	transport := newResumeTransport()
	factory := &resumeFactory{transports: []*resumeTransport{transport}}
	graph := newResumeWindowGraph(t, factory)
	graph.manager.OpenMain()
	openResumeSession(t, graph)

	transport.drop()
	waitFor(t, "the reconnect-delay state", func() bool {
		graph.state.Flush()
		snapshot, ok := graph.state.ActiveSnapshot()
		return ok && snapshot.Connection == appsession.ConnectionStatusReconnecting
	})
	if got := transport.preserves(); got == 0 {
		t.Fatal("the reconnect handover did not preserve the ET credentials")
	}

	if !graph.manager.ConfirmQuit() {
		t.Fatal("ConfirmQuit refused during a reconnect")
	}
	graph.manager.Main().Close()
	waitForTransportDone(t, transport)
	shutdownSessions(graph.state, graph.service)

	if got := transport.preserves(); got == 0 {
		t.Fatal("quitting during the reconnect delay discarded the ET resume credentials")
	}
	if got := transport.discards(); got != 0 {
		t.Fatalf("quitting during the reconnect delay called DiscardResume %d times", got)
	}
}

// TestQuitPreservesMainButDiscardsDetachedETResume handles the detached
// windows the quit sequence closes before OnWillQuit. Main-window Tabs are
// part of the persisted Workspace, so their credentials survive for the next
// run; detached Tabs are never persisted, so their credentials must be cleared
// instead of orphaned in the secret store.
func TestQuitPreservesMainButDiscardsDetachedETResume(t *testing.T) {
	mainTransport := newResumeTransport()
	detachedTransport := newResumeTransport()
	graph := newResumeWindowGraph(t, &resumeFactory{transports: []*resumeTransport{mainTransport, detachedTransport}})
	graph.manager.OpenMain()

	if err := graph.state.OpenHost(etResumeHost()); err != nil {
		t.Fatalf("OpenHost: %v", err)
	}
	waitFor(t, "the first ET Session", func() bool {
		graph.state.Flush()
		return graph.state.TabCount() == 1 && graph.state.ActiveSurface() != nil
	})
	if err := graph.state.OpenHost(etResumeHost()); err != nil {
		t.Fatalf("second OpenHost: %v", err)
	}
	waitFor(t, "the second ET Session", func() bool {
		graph.state.Flush()
		if graph.state.TabCount() != 2 {
			return false
		}
		pane, ok := graph.state.FocusedPaneID(1)
		if !ok {
			return false
		}
		info, ok := graph.state.PaneInfoAt(1, pane)
		return ok && info.Surface != nil
	})
	if !graph.state.DetachTab(1) {
		t.Fatal("DetachTab returned false")
	}
	detachedIDs := graph.state.DetachedTabIDs()
	if len(detachedIDs) != 1 {
		t.Fatalf("detached Tabs = %v, want one", detachedIDs)
	}
	waitForDetachedSurface(t, graph.state, detachedIDs[0])

	if !graph.manager.ConfirmQuit() {
		t.Fatal("ConfirmQuit refused with a detached window open")
	}
	detached := graph.manager.detached[detachedIDs[0]]
	if detached == nil {
		t.Fatal("the detached window was not created")
	}
	detached.Close()
	graph.manager.Main().Close()
	waitForTransportDone(t, mainTransport)
	waitForTransportDone(t, detachedTransport)
	shutdownSessions(graph.state, graph.service)

	if got := mainTransport.preserves(); got == 0 {
		t.Fatal("the main window quit discarded the ET resume credentials")
	}
	if got := mainTransport.discards(); got != 0 {
		t.Fatalf("the main window quit called DiscardResume %d times", got)
	}
	// A detached Tab cannot be restored, so its credentials must not be left
	// behind in the secret store.
	if got := detachedTransport.preserves(); got != 0 {
		t.Fatalf("a detached window quit preserved credentials %d times", got)
	}
	if got := detachedTransport.discards(); got == 0 {
		t.Fatal("a detached window quit left the ET credentials in the secret store")
	}
}

// TestExplicitTabCloseDiscardsETResume keeps the other half of the contract:
// a user closing a Tab is an explicit close, not a quit.
func TestExplicitTabCloseDiscardsETResume(t *testing.T) {
	transport := newResumeTransport()
	graph := newResumeWindowGraph(t, &resumeFactory{transports: []*resumeTransport{transport}})
	graph.manager.OpenMain()
	openResumeSession(t, graph)

	if !graph.state.CloseTab(0) {
		t.Fatal("CloseTab returned false")
	}
	waitForTransportDone(t, transport)
	if got := transport.discards(); got == 0 {
		t.Fatal("an explicit Tab close kept the ET resume credentials")
	}
	if got := transport.preserves(); got != 0 {
		t.Fatalf("an explicit Tab close preserved the transport %d times", got)
	}
}
