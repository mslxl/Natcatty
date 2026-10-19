package state

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
)

// inputFactory opens a fresh fake transport per Session so a test can observe
// each Tab's input.
type inputFactory struct {
	mu         sync.Mutex
	transports []*fakeTransport
}

func (f *inputFactory) Open(context.Context, appsession.StartSpec) (appsession.Transport, error) {
	transport := newFakeTransport()
	f.mu.Lock()
	f.transports = append(f.transports, transport)
	f.mu.Unlock()
	return transport, nil
}

func (f *inputFactory) transport(index int) *fakeTransport {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.transports[index]
}

type inputGraph struct {
	state   *State
	factory *inputFactory
}

func newInputGraph(t *testing.T, tabs int) *inputGraph {
	t.Helper()
	graph := &inputGraph{factory: &inputFactory{}}
	service := appsession.NewService(graph.factory)
	t.Cleanup(service.CloseAll)
	graph.state = NewState(
		WithSessions(service, func(*appsession.Session) (Surface, error) {
			return &fakeSurface{}, nil
		}),
	)
	for index := 0; index < tabs; index++ {
		if err := graph.state.NewTab(); err != nil {
			t.Fatalf("open tab %d: %v", index, err)
		}
	}
	graph.state.Flush()
	return graph
}

func (g *inputGraph) sessionID(t *testing.T, tab int) model.SessionID {
	t.Helper()
	return g.session(t, tab).ID()
}

func (g *inputGraph) session(t *testing.T, tab int) *appsession.Session {
	t.Helper()
	tabValue, _ := g.state.tabByID(mustTabID(t, g.state, tab))
	if tabValue == nil {
		t.Fatalf("tab %d missing", tab)
	}
	value, _ := tabValue.focusedPane()
	if value == nil || value.session == nil {
		t.Fatalf("tab %d has no Session", tab)
	}
	return value.session
}

func mustTabID(t *testing.T, s *State, index int) model.TabID {
	t.Helper()
	id, ok := s.TabID(index)
	if !ok {
		t.Fatalf("tab %d has no ID", index)
	}
	return id
}

func (f *fakeTransport) inputString() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return string(f.input)
}

func waitTransportInput(t *testing.T, transport *fakeTransport, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		transport.mu.Lock()
		got := string(transport.input)
		transport.mu.Unlock()
		if got == want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	transport.mu.Lock()
	got := string(transport.input)
	transport.mu.Unlock()
	t.Fatalf("transport input = %q, want %q", got, want)
}

func TestBroadcastReachesOnlySelectedSessionsAndStopsImmediately(t *testing.T) {
	graph := newInputGraph(t, 3)
	state := graph.state
	source := graph.sessionID(t, 2)

	state.OpenBroadcastPanel()
	if state.BroadcastTargetCount() != 2 {
		t.Fatalf("default targets = %d, want the two non-focused Sessions: %+v",
			state.BroadcastTargetCount(), state.BroadcastSessions())
	}
	for _, info := range state.BroadcastSessions() {
		if info.Focused && info.Selected {
			t.Fatal("the focused Session was selected by default")
		}
	}
	if !state.StartBroadcast() || !state.Broadcasting() {
		t.Fatal("broadcast did not start")
	}

	state.NoteSessionInput(source, []byte("marker\r"))
	waitTransportInput(t, graph.factory.transport(0), "marker\r")
	waitTransportInput(t, graph.factory.transport(1), "marker\r")

	result, ok := state.BroadcastResult()
	if !ok || result.DeliveredCount() != 2 || !result.Complete() {
		t.Fatalf("broadcast result = %+v, ok = %v", result, ok)
	}
	statuses := make(map[model.SessionID]appsession.DeliveryStatus)
	for _, info := range state.BroadcastSessions() {
		if info.HasStatus {
			statuses[info.ID] = info.Status
		}
	}
	if len(statuses) != 2 {
		t.Fatalf("per-Session statuses = %+v", statuses)
	}
	for _, status := range statuses {
		if status != appsession.DeliveryDelivered {
			t.Fatalf("per-Session status = %q", status)
		}
	}

	// Stop affects only the fan-out; typing stays on the focused Session.
	state.StopBroadcast()
	if state.Broadcasting() {
		t.Fatal("broadcast did not stop")
	}
	state.NoteSessionInput(source, []byte("not-broadcast\r"))
	time.Sleep(20 * time.Millisecond)
	if got := graph.factory.transport(0).inputString(); got != "marker\r" {
		t.Fatalf("a target received input after Stop: %q", got)
	}
	if _, err := graph.session(t, 2).TerminalConn().Write([]byte("focused-only\r")); err != nil {
		t.Fatal(err)
	}
	waitTransportInput(t, graph.factory.transport(2), "focused-only\r")
	if got := graph.factory.transport(1).inputString(); got != "marker\r" {
		t.Fatalf("normal typing reached a peer: %q", got)
	}
}

func TestBroadcastReportsAPeerThatClosedMidSession(t *testing.T) {
	graph := newInputGraph(t, 3)
	state := graph.state
	source := graph.sessionID(t, 2)
	closedID := graph.sessionID(t, 0)

	state.OpenBroadcastPanel()
	if !state.StartBroadcast() {
		t.Fatal("broadcast did not start")
	}
	tab, _ := state.tabByID(mustTabID(t, state, 0))
	value, _ := tab.focusedPane()
	_ = value.session.Close()
	select {
	case <-value.session.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("the peer did not close")
	}

	state.NoteSessionInput(source, []byte("partial\r"))
	result, ok := state.BroadcastResult()
	if !ok {
		t.Fatal("no broadcast result")
	}
	statuses := make(map[model.SessionID]appsession.DeliveryStatus)
	for _, delivery := range result.Deliveries {
		statuses[delivery.SessionID] = delivery.Status
	}
	if statuses[closedID] != appsession.DeliveryMissing {
		t.Fatalf("closed peer = %q, want missing: %+v", statuses[closedID], result.Deliveries)
	}
	if result.DeliveredCount() != 1 {
		t.Fatalf("partial result = %+v", result.Deliveries)
	}
	waitTransportInput(t, graph.factory.transport(1), "partial\r")
}

func TestComposeTransformsTheDraftAndSendsToTheFocusedSession(t *testing.T) {
	graph := newInputGraph(t, 1)
	state := graph.state
	state.OpenCompose()
	state.SetComposeDraft("  echo one\r\necho two  ")

	preview := state.ComposePreview()
	if preview.Text != "  echo one\necho two  " {
		t.Fatalf("preview text = %q", preview.Text)
	}
	if !state.SendCompose() {
		t.Fatal("compose send failed")
	}
	waitTransportInput(t, graph.factory.transport(0), "  echo one\necho two  \r")
	if draft := *state.ComposeDraft(); draft != "" {
		t.Fatalf("draft after send = %q", draft)
	}
	result, ok := state.ComposeResult()
	if !ok || result.DeliveredCount() != 1 {
		t.Fatalf("compose result = %+v", result)
	}
}

func TestComposeSendsToTheBroadcastSetAndAppliesEscapesAndTrim(t *testing.T) {
	graph := newInputGraph(t, 3)
	state := graph.state
	state.OpenBroadcastPanel()
	state.SetBroadcastTarget(graph.sessionID(t, 2), false)
	if !state.StartBroadcast() {
		t.Fatal("broadcast did not start")
	}
	state.OpenCompose()
	*state.ComposeTrim() = true
	*state.ComposeDecodeEscapes() = true
	state.SetComposeDraft("  echo a\\techo b  ")

	preview := state.ComposePreview()
	if preview.Text != "echo a\techo b" {
		t.Fatalf("preview = %q", preview.Text)
	}
	targets, broadcast := state.ComposeTargets()
	if !broadcast || len(targets) != 2 {
		t.Fatalf("compose targets = %v broadcast = %v", targets, broadcast)
	}
	if !state.SendCompose() {
		t.Fatal("compose send failed")
	}
	waitTransportInput(t, graph.factory.transport(0), "echo a\techo b\r")
	waitTransportInput(t, graph.factory.transport(1), "echo a\techo b\r")
	time.Sleep(20 * time.Millisecond)
	if got := graph.factory.transport(2).inputString(); got != "" {
		t.Fatalf("the focused peer received the broadcast draft: %q", got)
	}
	// A compose send to the broadcast set is broadcast input: the target panel
	// must show the same per-Session delivery status as typed broadcast input.
	result, ok := state.BroadcastResult()
	if !ok || len(result.Deliveries) != 2 || result.DeliveredCount() != 2 {
		t.Fatalf("broadcast result after compose = %+v", result)
	}
	statuses := map[model.SessionID]appsession.DeliveryStatus{}
	for _, delivery := range result.Deliveries {
		statuses[delivery.SessionID] = delivery.Status
	}
	for _, index := range []int{0, 1} {
		if status := statuses[graph.sessionID(t, index)]; status != appsession.DeliveryDelivered {
			t.Fatalf("compose delivery status for Session %d = %q", index, status)
		}
	}
	if _, exists := statuses[graph.sessionID(t, 2)]; exists {
		t.Fatal("the focused peer acquired a broadcast delivery status")
	}
}

func TestComposeSendsTheEditorDraftAndClearsItOnAcceptance(t *testing.T) {
	graph := newInputGraph(t, 1)
	state := graph.state
	state.OpenCompose()
	state.SetComposeDraft("stale bound value")
	if !state.SendComposeDraft("echo fresh") {
		t.Fatal("compose rejected the editor draft")
	}
	if draft := *state.ComposeDraft(); draft != "" {
		t.Fatalf("draft after acceptance = %q, want it cleared at send time", draft)
	}
	waitTransportInput(t, graph.factory.transport(0), "echo fresh\r")
}

func TestClosingComposeCancelsTheRemainingLinesAndKeepsNewText(t *testing.T) {
	graph := newInputGraph(t, 1)
	state := graph.state
	state.OpenCompose()
	state.SetComposeLineMode(appsession.ComposeLineByLine)
	state.SetComposeDraft("one\ntwo")
	if !state.SendCompose() {
		t.Fatal("compose send failed")
	}
	if draft := *state.ComposeDraft(); draft != "" {
		t.Fatalf("draft after acceptance = %q, want it cleared immediately", draft)
	}
	waitTransportInput(t, graph.factory.transport(0), "one\r")

	state.CloseCompose()
	state.SetComposeDraft("typed after close")
	// The canceled sender returns during this window. A stale completion must
	// not report a result or wipe the new draft.
	time.Sleep(2 * appsession.ComposeLineDelay)
	if state.ComposeSending() {
		t.Fatal("compose is still sending after Close")
	}
	if draft := *state.ComposeDraft(); draft != "typed after close" {
		t.Fatalf("draft after the stale completion = %q", draft)
	}
	if got := graph.factory.transport(0).inputString(); strings.Contains(got, "two\r") {
		t.Fatalf("a canceled compose delivered its remaining line: %q", got)
	}
}

func TestStoppingBroadcastCancelsTheRemainingComposedLines(t *testing.T) {
	graph := newInputGraph(t, 3)
	state := graph.state
	state.OpenBroadcastPanel()
	if !state.StartBroadcast() {
		t.Fatal("broadcast did not start")
	}
	state.OpenCompose()
	state.SetComposeLineMode(appsession.ComposeLineByLine)
	state.SetComposeDraft("one\ntwo")
	if !state.SendCompose() {
		t.Fatal("broadcast compose send failed")
	}
	waitTransportInput(t, graph.factory.transport(0), "one\r")
	state.StopBroadcast()
	time.Sleep(2 * appsession.ComposeLineDelay)

	if state.ComposeSending() {
		t.Fatal("compose is still sending after Stop")
	}
	for _, index := range []int{0, 1} {
		if got := graph.factory.transport(index).inputString(); strings.Contains(got, "two\r") {
			t.Fatalf("target %d received the remaining line after Stop: %q", index, got)
		}
	}
	if got := graph.factory.transport(2).inputString(); got != "" {
		t.Fatalf("the unselected focused Session received %q", got)
	}
}

func TestBroadcastCannotStartWithOnlyTheFocusedSource(t *testing.T) {
	graph := newInputGraph(t, 2)
	state := graph.state
	focused, ok := state.FocusedSessionID()
	if !ok {
		t.Fatal("no focused Session")
	}
	state.OpenBroadcastPanel()
	for _, info := range state.BroadcastSessions() {
		state.SetBroadcastTarget(info.ID, false)
	}
	state.SetBroadcastTarget(focused, true)
	if state.BroadcastCanStart() {
		t.Fatal("a selection of only the focused source can start")
	}
	if state.StartBroadcast() {
		t.Fatal("broadcast started with only the focused source")
	}
	if state.Broadcasting() {
		t.Fatal("broadcast is active with only the focused source")
	}
}
