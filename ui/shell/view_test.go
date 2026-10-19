package shell

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/shell/state"
)

// fakeTransport keeps a Session alive until the test ends it.
type fakeTransport struct {
	output    chan []byte
	closed    chan struct{}
	closeOnce sync.Once
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

func (f *fakeTransport) Write(p []byte) (int, error) { return len(p), nil }
func (f *fakeTransport) Resize(int, int) error       { return nil }
func (f *fakeTransport) Wait() (int, error)          { return 6, nil }

func (f *fakeTransport) Close() error {
	f.closeOnce.Do(func() { close(f.closed) })
	return nil
}

func (f *fakeTransport) end() {
	f.closeOnce.Do(func() { close(f.closed) })
}

type fakeFactory struct{ transport *fakeTransport }

func (f *fakeFactory) Open(context.Context, appsession.StartSpec) (appsession.Transport, error) {
	return f.transport, nil
}

type fakeSurface struct{}

func (fakeSurface) View(c *ui.Context) ui.Element {
	return ui.Text(c, "terminal-surface").Fill()
}

func (fakeSurface) Close() {}

type testGraph struct {
	state     *state.State
	transport *fakeTransport
	sessions  []*appsession.Session
}

func newTestGraph(t *testing.T) *testGraph {
	t.Helper()
	graph := &testGraph{transport: newFakeTransport()}
	service := appsession.NewService(&fakeFactory{transport: graph.transport})
	t.Cleanup(service.CloseAll)
	graph.state = state.NewState(state.WithSessions(service, func(session *appsession.Session) (state.Surface, error) {
		graph.sessions = append(graph.sessions, session)
		return fakeSurface{}, nil
	}))
	return graph
}

func TestShellShowsVaultAndSettingsNavigation(t *testing.T) {
	tt := ui.NewTester(View(state.NewState()), 900, 600)

	if !tt.HasText("Vault") {
		t.Errorf("shell does not show Vault navigation, texts %q", tt.Texts())
	}
	if !tt.HasText("Settings") {
		t.Errorf("shell does not show Settings navigation, texts %q", tt.Texts())
	}
	if !tt.HasText("Your Hosts, keys, and notes will appear here.") {
		t.Errorf("shell does not show the Vault surface, texts %q", tt.Texts())
	}
}

func TestShellSettingsNavigationRequestsTheSettingsWindow(t *testing.T) {
	var requests int
	shellState := state.NewState(state.WithSettingsRequest(func() { requests++ }))
	tt := ui.NewTester(View(shellState), 900, 600)

	if err := tt.Click("Settings"); err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("settings requests = %d after clicking Settings, want 1", requests)
	}
}

func TestShellSwitchesAllFiveLocalesWithoutRestart(t *testing.T) {
	tt := ui.NewTester(View(state.NewState()), 900, 600)

	tests := []struct {
		tag      string
		settings string
	}{
		{"en", "Settings"},
		{"es", "Configuración"},
		{"ru", "Настройки"},
		{"zh-CN", "设置"},
		{"zh-TW", "設定"},
	}
	for _, test := range tests {
		tt.SetLocale(test.tag)
		if !tt.HasText(test.settings) {
			t.Errorf("locale %s does not show %q, texts %q", test.tag, test.settings, tt.Texts())
		}
	}
}

func TestShellShowsOpenLocalSessionTabsAndTheirTerminal(t *testing.T) {
	graph := newTestGraph(t)
	tt := ui.NewTester(View(graph.state), 900, 600)

	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	tt.Frame()

	for _, want := range []string{"Local Session 1", "Local Session 2", "Running", "terminal-surface"} {
		if !tt.HasText(want) {
			t.Errorf("shell does not show %q, texts %q", want, tt.Texts())
		}
	}

	if !graph.state.CloseActiveTab() {
		t.Fatal("CloseActiveTab returned false")
	}
	tt.Frame()
	if tt.HasText("Local Session 2") {
		t.Errorf("shell still shows closed Local Session 2, texts %q", tt.Texts())
	}
	if !tt.HasText("Local Session 1") {
		t.Errorf("shell does not show Local Session 1 after closing Local Session 2, texts %q", tt.Texts())
	}
}

func TestShellShowsExitStatusAndCode(t *testing.T) {
	graph := newTestGraph(t)
	tt := ui.NewTester(View(graph.state), 900, 600)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("Running") {
		t.Fatalf("shell does not show the running status, texts %q", tt.Texts())
	}

	graph.transport.end()
	select {
	case <-graph.sessions[0].Done():
	case <-time.After(2 * time.Second):
		t.Fatal("the session did not exit")
	}
	tt.Frame()

	want := locales.In("en").SessionExitedWithCode(6)
	if !tt.HasText(want) {
		t.Errorf("shell does not show %q, texts %q", want, tt.Texts())
	}
	if tt.HasText("Running") {
		t.Errorf("shell still shows Running after the session exited, texts %q", tt.Texts())
	}
}

func TestShellLanguageControlRequestsLocale(t *testing.T) {
	var requested []string
	shellState := state.NewState(state.WithLocaleRequest(func(tag string) {
		requested = append(requested, tag)
	}))
	tt := ui.NewTester(View(shellState), 900, 600)

	if err := tt.Click("español"); err != nil {
		t.Fatal(err)
	}
	if len(requested) != 1 || requested[0] != "es" {
		t.Fatalf("requested locales = %v, want [es]", requested)
	}
}
