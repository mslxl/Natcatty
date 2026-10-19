package shell

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/shell/state"
)

// newInputGraph opens three real shell-State Tabs on fake Sessions.
func newInputGraph(t *testing.T) *testGraph {
	t.Helper()
	graph := newTestGraph(t)
	for index := 0; index < 3; index++ {
		if err := graph.state.NewTab(); err != nil {
			t.Fatalf("open tab %d: %v", index, err)
		}
	}
	graph.state.Flush()
	return graph
}

// prepareBroadcastAndCompose drives the production state into an active
// broadcast with two delivered targets and an open compose draft that
// exercises trimming and escape decoding.
func prepareBroadcastAndCompose(t *testing.T, graph *testGraph) {
	t.Helper()
	graph.state.OpenBroadcastPanel()
	if !graph.state.StartBroadcast() {
		t.Fatal("broadcast did not start")
	}
	if len(graph.sessions) != 3 {
		t.Fatalf("sessions = %d", len(graph.sessions))
	}
	graph.state.NoteSessionInput(graph.sessions[2].ID(), []byte("marker\r"))
	graph.state.OpenCompose()
	*graph.state.ComposeTrim() = true
	*graph.state.ComposeDecodeEscapes() = true
	graph.state.SetComposeDraft("  echo one\\necho two  ")
}

func assertInputInside(t *testing.T, tester *ui.Tester, width, height int, text string) ui.Rect {
	t.Helper()
	rect, ok := tester.Find(text)
	if !ok {
		t.Fatalf("%dx%d does not show %q: %v", width, height, text, tester.Texts())
	}
	if rect.X < -0.5 || rect.Y < -0.5 ||
		rect.X+rect.W > float32(width)+0.5 || rect.Y+rect.H > float32(height)+0.5 {
		t.Fatalf("%dx%d clips %q: %+v", width, height, text, rect)
	}
	return rect
}

// TestBroadcastAndComposeFitAtDefaultAndMinimumSizes proves the production
// shell renders the active broadcast banner, target rows with per-Session
// status, and the compose preview inside 1100x720 and 720x480 without row
// overlap.
func TestBroadcastAndComposeFitAtDefaultAndMinimumSizes(t *testing.T) {
	m := locales.In("en")
	for _, size := range [][2]int{{1100, 720}, {720, 480}} {
		graph := newInputGraph(t)
		prepareBroadcastAndCompose(t, graph)

		tester := ui.NewTester(View(graph.state), size[0], size[1])
		tester.SetLocale("en")
		tester.Frame()

		// Minimum heights catch a control that is present but cut off at the
		// bottom edge; a clipped row reports only its remaining height.
		wanted := []struct {
			text string
			minH float32
		}{
			{m.BroadcastPanelTitle(), 15},
			{m.BroadcastActive(2), 12},
			{m.BroadcastTargetsTitle(), 12},
			{m.BroadcastDelivered(), 12},
			{m.BroadcastSummary(2, 2), 12},
			{m.BroadcastFocused(), 8},
			{m.BroadcastStop(), 16},
			{m.ComposeTitle(), 15},
			{m.ComposeTargetBroadcast(2), 12},
			{m.ComposePreview(), 11},
			{"echo one\\necho two", 12},
			{m.ComposeTrim(), 16},
			{m.ComposeDecodeEscapes(), 16},
			{m.ComposeSend(), 16},
			{m.ComposeCancel(), 16},
		}
		rects := make(map[string]ui.Rect, len(wanted))
		for _, want := range wanted {
			rects[want.text] = assertInputInside(t, tester, size[0], size[1], want.text)
			if rects[want.text].H < want.minH {
				t.Fatalf("%dx%d clips %q to height %.1f, want at least %.1f: %+v",
					size[0], size[1], want.text, rects[want.text].H, want.minH, rects[want.text])
			}
		}

		tabTitle := assertInputInside(t, tester, size[0], size[1], "Local Session 1")
		panel := rects[m.BroadcastPanelTitle()]
		compose := rects[m.ComposeTitle()]
		if tabTitle.Y+tabTitle.H > panel.Y+0.5 {
			t.Fatalf("%dx%d panel overlaps the tab strip: tab %+v panel %+v",
				size[0], size[1], tabTitle, panel)
		}
		if panel.Y+panel.H > compose.Y+0.5 {
			t.Fatalf("%dx%d compose overlaps the broadcast panel: panel %+v compose %+v",
				size[0], size[1], panel, compose)
		}
		summary := rects[m.BroadcastSummary(2, 2)]
		if summary.Y < panel.Y || summary.Y > compose.Y {
			t.Fatalf("%dx%d per-Session summary is outside the panel: %+v", size[0], size[1], summary)
		}
		send := rects[m.ComposeSend()]
		if send.Y < compose.Y {
			t.Fatalf("%dx%d Send is above the compose header: send %+v title %+v",
				size[0], size[1], send, compose)
		}
	}
}

// TestBroadcastControlsShowStoppedState proves the active state disappears
// when broadcast stops while the target panel stays usable.
func TestBroadcastControlsShowStoppedState(t *testing.T) {
	m := locales.In("en")
	for _, size := range [][2]int{{1100, 720}, {720, 480}} {
		graph := newInputGraph(t)
		graph.state.OpenBroadcastPanel()
		if !graph.state.StartBroadcast() {
			t.Fatal("broadcast did not start")
		}
		graph.state.StopBroadcast()

		tester := ui.NewTester(View(graph.state), size[0], size[1])
		tester.SetLocale("en")
		tester.Frame()

		assertInputInside(t, tester, size[0], size[1], m.BroadcastStopped())
		assertInputInside(t, tester, size[0], size[1], m.BroadcastStart())
		if tester.HasText(m.BroadcastActive(2)) {
			t.Fatalf("%dx%d still shows an active broadcast: %v", size[0], size[1], tester.Texts())
		}
		if tester.HasText(m.BroadcastStop()) {
			t.Fatalf("%dx%d still offers Stop after it stopped", size[0], size[1])
		}
	}
}

// TestComposeFocusedTargetIndication proves the bar says where Send goes and
// shows the transformed preview before the user sends.
func TestComposeFocusedTargetIndication(t *testing.T) {
	m := locales.In("en")
	graph := newInputGraph(t)
	graph.state.OpenCompose()
	graph.state.SetComposeDraft("echo hi")

	tester := ui.NewTester(View(graph.state), 720, 480)
	tester.SetLocale("en")
	tester.Frame()

	assertInputInside(t, tester, 720, 480,
		m.ComposeTargetFocusedNamed(m.LocalSessionTab(3)))
	assertInputInside(t, tester, 720, 480, m.ComposePreview())
	assertInputInside(t, tester, 720, 480, "echo hi")
	assertInputInside(t, tester, 720, 480, m.ComposeSend())
	// The focused Session is the last Tab opened, so its target label is the
	// one the bar names.
	if !tester.HasText(m.ComposeTargetFocused()) {
		t.Fatalf("the compose bar does not identify its target: %v", tester.Texts())
	}
}

// recordingTransport keeps the input every composed send reaches the shell
// transport with, so the keyboard path can be checked end to end.
type recordingTransport struct {
	fakeTransport
	mu    sync.Mutex
	input []byte
}

func newRecordingTransport() *recordingTransport {
	return &recordingTransport{fakeTransport: fakeTransport{
		output: make(chan []byte),
		closed: make(chan struct{}),
	}}
}

func (t *recordingTransport) Write(p []byte) (int, error) {
	t.mu.Lock()
	t.input = append(t.input, p...)
	t.mu.Unlock()
	return len(p), nil
}

func (t *recordingTransport) inputString() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.input)
}

type recordingFactory struct{ transport *recordingTransport }

func (f recordingFactory) Open(context.Context, appsession.StartSpec) (appsession.Transport, error) {
	return f.transport, nil
}

func newComposeKeyboardGraph(t *testing.T) (*state.State, *recordingTransport) {
	t.Helper()
	transport := newRecordingTransport()
	service := appsession.NewService(recordingFactory{transport: transport})
	t.Cleanup(service.CloseAll)
	shellState := state.NewState(
		state.WithSessions(service, func(*appsession.Session) (state.Surface, error) {
			return fakeSurface{}, nil
		}),
	)
	if err := shellState.NewTab(); err != nil {
		t.Fatalf("open tab: %v", err)
	}
	shellState.Flush()
	return shellState, transport
}

func waitComposeInput(t *testing.T, transport *recordingTransport, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if transport.inputString() == want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("transport input = %q, want %q", transport.inputString(), want)
}

// TestComposeKeyboardSendsEnterAndBreaksShiftEnter proves the placeholder's
// promise: Shift+Enter inserts a line, Enter sends the text typed between
// frames, and Escape closes the bar without sending.
func TestComposeKeyboardSendsEnterAndBreaksShiftEnter(t *testing.T) {
	m := locales.In("en")
	shellState, transport := newComposeKeyboardGraph(t)
	shellState.OpenCompose()
	tester := ui.NewTester(View(shellState), 900, 600)
	tester.SetLocale("en")
	tester.Frame()

	if err := tester.Click(m.ComposePlaceholder()); err != nil {
		t.Fatalf("focus the compose draft: %v", err)
	}
	tester.Type("echo one")
	tester.Key(ui.Shift, ui.KeyEnter)
	tester.Type("echo two")
	tester.Frame()
	if got := *shellState.ComposeDraft(); got != "echo one\necho two" {
		t.Fatalf("draft after Shift+Enter = %q", got)
	}
	if got := transport.inputString(); got != "" {
		t.Fatalf("Shift+Enter sent %q", got)
	}

	tester.Key(0, ui.KeyEnter)
	tester.Frame()
	waitComposeInput(t, transport, "echo one\necho two\r")
	if got := *shellState.ComposeDraft(); got != "" {
		t.Fatalf("draft after Enter = %q", got)
	}
	if !shellState.ComposeOpen() {
		t.Fatal("Enter closed the compose bar")
	}

	shellState.SetComposeDraft("not sent")
	tester.Frame()
	if err := tester.Click(m.ComposePlaceholder()); err != nil {
		t.Fatal(err)
	}
	tester.Key(0, ui.KeyEscape)
	tester.Frame()
	if shellState.ComposeOpen() {
		t.Fatal("Escape did not close the compose bar")
	}
	if got := *shellState.ComposeDraft(); got != "" {
		t.Fatalf("Escape kept draft %q", got)
	}
}

// TestBroadcastPanelDoesNotStartWithOnlyTheFocusedSource proves the start
// control is disabled and explained when the selection would reach nobody.
func TestBroadcastPanelDoesNotStartWithOnlyTheFocusedSource(t *testing.T) {
	m := locales.In("en")
	graph := newTestGraph(t)
	if err := graph.state.NewTab(); err != nil {
		t.Fatal(err)
	}
	graph.state.Flush()
	focused, ok := graph.state.FocusedSessionID()
	if !ok {
		t.Fatal("no focused Session")
	}
	graph.state.OpenBroadcastPanel()
	graph.state.SetBroadcastTarget(focused, true)
	if graph.state.BroadcastCanStart() {
		t.Fatal("a selection of only the focused source can start")
	}
	if graph.state.StartBroadcast() {
		t.Fatal("broadcast started with only the focused source")
	}
	if graph.state.Broadcasting() {
		t.Fatal("broadcast is active with only the focused source")
	}

	tester := ui.NewTester(View(graph.state), 720, 480)
	tester.SetLocale("en")
	tester.Frame()
	assertInputInside(t, tester, 720, 480, m.BroadcastSelectHint())
}
