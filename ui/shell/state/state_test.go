package state

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"
)

// fakeTransport keeps a Session alive until the test closes it.
type fakeTransport struct {
	output    chan []byte
	closed    chan struct{}
	closeOnce sync.Once

	mu      sync.Mutex
	input   []byte
	resizes [][2]int
}

func newFakeTransport() *fakeTransport {
	return &fakeTransport{output: make(chan []byte), closed: make(chan struct{})}
}

func (f *fakeTransport) Read(p []byte) (int, error) {
	select {
	case data := <-f.output:
		return copy(p, data), nil
	case <-f.closed:
		return 0, io.EOF
	}
}

func (f *fakeTransport) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.input = append(f.input, p...)
	return len(p), nil
}

func (f *fakeTransport) Resize(cols, rows int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resizes = append(f.resizes, [2]int{cols, rows})
	return nil
}

func (f *fakeTransport) Wait() (int, error) { return 0, nil }

func (f *fakeTransport) Close() error {
	f.closeOnce.Do(func() { close(f.closed) })
	return nil
}

type fakeFactory struct{ transport *fakeTransport }

func (f *fakeFactory) Open(context.Context, appsession.StartSpec) (appsession.Transport, error) {
	return f.transport, nil
}

// fakeSurface stands in for the MyGo terminal the UI owns.
type fakeSurface struct {
	closed bool
}

func (f *fakeSurface) View(c *ui.Context) ui.Element {
	return ui.Text(c, "terminal-surface").Fill()
}

func (f *fakeSurface) Close() { f.closed = true }

// testGraph wires a shell State to fake Sessions and surfaces.
type testGraph struct {
	state    *State
	surface  *fakeSurface
	sessions []*appsession.Session
	changes  int
}

func newTestGraph(t *testing.T) *testGraph {
	t.Helper()
	graph := &testGraph{}
	service := appsession.NewService(&fakeFactory{transport: newFakeTransport()})
	t.Cleanup(service.CloseAll)
	graph.surface = &fakeSurface{}
	graph.state = NewState(
		WithRedrawRequest(func() { graph.changes++ }),
		WithSessions(service, func(session *appsession.Session) (Surface, error) {
			graph.sessions = append(graph.sessions, session)
			return graph.surface, nil
		}),
	)
	return graph
}

func TestStateStartsWithoutTabs(t *testing.T) {
	graph := newTestGraph(t)

	if graph.state.TabCount() != 0 || graph.state.ActiveTab() != -1 {
		t.Fatalf("tabs = %d, active = %d, want 0 and -1", graph.state.TabCount(), graph.state.ActiveTab())
	}
}

func TestNewTabOpensALocalShellSessionAndSurface(t *testing.T) {
	graph := newTestGraph(t)

	if err := graph.state.NewTab(); err != nil {
		t.Fatalf("NewTab: %v", err)
	}
	if graph.state.TabCount() != 1 || graph.state.ActiveTab() != 0 {
		t.Fatalf("tabs = %d, active = %d, want 1 and 0", graph.state.TabCount(), graph.state.ActiveTab())
	}
	if got := graph.state.ActiveSurface(); got != graph.surface {
		t.Fatalf("active surface = %v, want the created surface", got)
	}
	if len(graph.sessions) != 1 {
		t.Fatalf("created sessions = %d, want 1", len(graph.sessions))
	}

	snapshot, ok := graph.state.ActiveSnapshot()
	if !ok {
		t.Fatal("the active tab has no Session snapshot")
	}
	if snapshot.Kind != appsession.KindLocal {
		t.Errorf("session kind = %q, want %q", snapshot.Kind, appsession.KindLocal)
	}
	if snapshot.Status != appsession.StatusRunning {
		t.Errorf("session status = %q, want %q", snapshot.Status, appsession.StatusRunning)
	}
}

func TestNewTabAppendsAndClosesActiveTab(t *testing.T) {
	graph := newTestGraph(t)

	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if graph.state.TabCount() != 2 || graph.state.ActiveTab() != 1 {
		t.Fatalf("tabs = %d, active = %d, want 2 and 1", graph.state.TabCount(), graph.state.ActiveTab())
	}
	if got := graph.state.TabNumbers(); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("tab numbers = %v, want [1 2]", got)
	}

	if !graph.state.CloseActiveTab() {
		t.Fatal("CloseActiveTab returned false with an active tab")
	}
	if graph.state.TabCount() != 1 || graph.state.ActiveTab() != 0 {
		t.Fatalf("tabs = %d, active = %d, want 1 and 0", graph.state.TabCount(), graph.state.ActiveTab())
	}

	if !graph.state.CloseActiveTab() {
		t.Fatal("CloseActiveTab returned false with one tab left")
	}
	if graph.state.TabCount() != 0 || graph.state.ActiveTab() != -1 {
		t.Fatalf("tabs = %d, active = %d, want 0 and -1", graph.state.TabCount(), graph.state.ActiveTab())
	}
	if graph.state.CloseActiveTab() {
		t.Fatal("CloseActiveTab returned true with no tabs")
	}
}

func TestCloseActiveTabClosesItsSessionAndSurface(t *testing.T) {
	graph := newTestGraph(t)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	session := graph.sessions[0]

	if !graph.state.CloseActiveTab() {
		t.Fatal("CloseActiveTab returned false")
	}
	if !graph.surface.closed {
		t.Error("the tab's surface was not closed")
	}
	select {
	case <-session.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("the session did not close")
	}
	if got := session.Snapshot().Status; got != appsession.StatusClosed {
		t.Errorf("session status = %q, want %q", got, appsession.StatusClosed)
	}
}

func TestTabChangesRequestARedraw(t *testing.T) {
	graph := newTestGraph(t)

	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if graph.changes != 1 {
		t.Fatalf("change requests after NewTab = %d, want 1", graph.changes)
	}
	if !graph.state.CloseActiveTab() {
		t.Fatal("CloseActiveTab returned false")
	}
	if graph.changes != 2 {
		t.Fatalf("change requests after CloseActiveTab = %d, want 2", graph.changes)
	}
}

func TestCloseActiveTabSelectsPreviousNeighbour(t *testing.T) {
	graph := newTestGraph(t)
	for range 3 {
		if err := graph.state.NewTab(); err != nil {
			t.Fatal(err)
		}
	}
	graph.state.SelectTab(2)

	if !graph.state.CloseActiveTab() {
		t.Fatal("CloseActiveTab returned false")
	}
	if graph.state.TabCount() != 2 || graph.state.ActiveTab() != 1 {
		t.Fatalf("tabs = %d, active = %d, want 2 and 1", graph.state.TabCount(), graph.state.ActiveTab())
	}
}

func TestRequestLocaleReportsTheRequestedTag(t *testing.T) {
	var requested []string
	state := NewState(WithLocaleRequest(func(tag string) {
		requested = append(requested, tag)
	}))

	state.RequestLocale("es")
	state.RequestLocale("zh-TW")

	if len(requested) != 2 || requested[0] != "es" || requested[1] != "zh-TW" {
		t.Fatalf("requested locales = %v, want [es zh-TW]", requested)
	}
}

func TestRequestSettingsReportsTheRequest(t *testing.T) {
	requests := 0
	state := NewState(WithSettingsRequest(func() { requests++ }))

	state.RequestSettings()
	state.RequestSettings()

	if requests != 2 {
		t.Fatalf("settings requests = %d, want 2", requests)
	}
}
