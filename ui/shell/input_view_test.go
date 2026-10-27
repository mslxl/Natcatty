package shell

import (
	"context"
	"strings"
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
	for _, size := range [][2]int{{1084, 661}, {704, 421}} {
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

		// The compose body above the pinned option row scrolls. At the
		// minimum client the preview sits below the body viewport at rest;
		// scrolling must bring it fully above the pinned Send row, and the
		// row itself must not move.
		if rect, ok := tester.Find(m.ComposePreview()); !ok || rect.H < 11 {
			tester.Scroll(352, 330, 0, 400)
			tester.Frame()
		}
		preview := assertInputInside(t, tester, size[0], size[1], m.ComposePreview())
		if preview.H < 11 {
			t.Fatalf("%dx%d clips the scrolled preview label: %+v", size[0], size[1], preview)
		}
		if !strings.Contains(strings.Join(tester.Texts(), "\n"), "echo one\\necho two") {
			t.Fatalf("%dx%d lost the preview text after scrolling: %q", size[0], size[1], tester.Texts())
		}
		moved := rects[m.ComposeSend()]
		if moved != send {
			t.Fatalf("%dx%d moved the pinned Send row while scrolling the body: %+v -> %+v",
				size[0], size[1], send, moved)
		}
	}
}

// TestInputControlsKeepTheirTextLineUnderShortFontMetrics pins the text line
// of the broadcast and compose controls to a height of their own. macOS lays
// the same labels out at 15.3 DIPs, below the 16-DIP minimum a control needs
// to stay readable; a scaled default theme reproduces shorter metrics on any
// platform, so a control that leaves its line height to the font is caught
// everywhere.
func TestInputControlsKeepTheirTextLineUnderShortFontMetrics(t *testing.T) {
	m := locales.In("en")
	for _, size := range [][2]int{{1100, 720}, {720, 480}} {
		graph := newInputGraph(t)
		prepareBroadcastAndCompose(t, graph)

		tester := ui.NewTester(View(graph.state), size[0], size[1])
		tester.SetLocale("en")
		tester.SetPreferences(ui.Preferences{TextScale: 0.8})
		tester.Frame()

		for _, want := range []string{
			m.BroadcastStop(),
			m.ComposeTrim(),
			m.ComposeDecodeEscapes(),
			m.ComposeSend(),
			m.ComposeCancel(),
		} {
			rect := assertInputInside(t, tester, size[0], size[1], want)
			if rect.H < 16 {
				t.Fatalf("%dx%d with short font metrics %q is %.1f DIPs, want at least 16: %+v",
					size[0], size[1], want, rect.H, rect)
			}
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
	// one the bar names, not the generic or broadcast label.
	if tester.HasText(m.ComposeTargetFocused()) {
		t.Fatalf("the compose bar still shows the unnamed target label: %v", tester.Texts())
	}
	if tester.HasText(m.ComposeTargetBroadcast(1)) {
		t.Fatalf("the compose bar shows a broadcast target without an active broadcast: %v", tester.Texts())
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

// TestDetachedTargetLeavesThePanelAndCountsAgree is the UI half of the
// detached-target regression: after a selected Tab is detached, the panel and
// the compose header report the same one remaining target, a visible notice
// explains the removal, and the detached Session has no row.
func TestDetachedTargetLeavesThePanelAndCountsAgree(t *testing.T) {
	m := locales.In("en")
	graph := newInputGraph(t)
	prepareBroadcastAndCompose(t, graph)
	if !graph.state.DetachTab(0) {
		t.Fatal("detach failed")
	}

	tester := ui.NewTester(View(graph.state), 720, 480)
	tester.SetLocale("en")
	tester.Frame()

	assertInputInside(t, tester, 720, 480, m.BroadcastActive(1))
	assertInputInside(t, tester, 720, 480, m.ComposeTargetBroadcast(1))
	assertInputInside(t, tester, 720, 480, m.BroadcastTargetsRemoved(1))
	if tester.HasText(m.BroadcastActive(2)) {
		t.Fatalf("the panel still counts the detached Session: %v", tester.Texts())
	}
	if tester.HasText(m.ComposeTargetBroadcast(2)) {
		t.Fatalf("compose still counts the detached Session: %v", tester.Texts())
	}
	if tester.HasText("Local Session 1") {
		t.Fatalf("the detached Session still has a tab or target row: %v", tester.Texts())
	}
	if tester.HasText(m.BroadcastDelivered()) {
		t.Fatalf("a status row survived the detach: %v", tester.Texts())
	}
}

// TestComposeHeaderStaysOnOneLineAtMinimumWidth keeps the target and result
// beside the title instead of wrapping into a ragged block above Cancel.
func TestComposeHeaderStaysOnOneLineAtMinimumWidth(t *testing.T) {
	m := locales.In("en")
	graph := newInputGraph(t)
	prepareBroadcastAndCompose(t, graph)
	if !graph.state.SendCompose() {
		t.Fatal("compose send failed")
	}

	tester := ui.NewTester(View(graph.state), 720, 480)
	tester.SetLocale("en")
	tester.Frame()

	title := assertInputInside(t, tester, 720, 480, m.ComposeTitle())
	target := assertInputInside(t, tester, 720, 480, m.ComposeTargetBroadcast(2))
	result := assertInputInside(t, tester, 720, 480, m.ComposeSent(2))
	cancel := assertInputInside(t, tester, 720, 480, m.ComposeCancel())
	titleCenter := title.Y + title.H/2
	for name, rect := range map[string]ui.Rect{"target": target, "result": result} {
		if center := rect.Y + rect.H/2; center < titleCenter-0.5 || center > titleCenter+0.5 {
			t.Fatalf("compose %s wrapped above or below the title: %s %+v title %+v",
				name, name, rect, title)
		}
		if rect.H > 20 {
			t.Fatalf("compose %s is a %0.1f DIP block, want one text line: %+v",
				name, rect.H, rect)
		}
	}
	if target.W < 20 || result.W < 20 {
		t.Fatalf("header text is squeezed away: target %+v result %+v", target, result)
	}
	if cancelCenter := cancel.Y + cancel.H/2; cancelCenter < titleCenter-0.5 || cancelCenter > titleCenter+0.5 {
		t.Fatalf("Cancel was pushed below the header row: %+v", cancel)
	}
}

// TestComposePreviewShowsTheFullWrappedResult proves a long draft is not cut
// at the first line: the preview holds the complete transformed text, wraps
// it over several lines, and scrolls the region.
func TestComposePreviewShowsTheFullWrappedResult(t *testing.T) {
	m := locales.In("en")
	graph := newInputGraph(t)
	graph.state.OpenCompose()
	draft := "line 01 with a fairly long command argument\n" +
		"line 02 with a fairly long command argument\n" +
		"line 03 with a fairly long command argument\n" +
		"line 04 with a fairly long command argument\n" +
		"line 05 with a fairly long command argument\n" +
		"line 06 with a fairly long command argument"
	graph.state.SetComposeDraft(draft)
	preview := graph.state.ComposePreview().PreviewText()

	tester := ui.NewTester(View(graph.state), 720, 480)
	tester.SetLocale("en")
	tester.Frame()

	if !tester.HasText(preview) {
		t.Fatalf("the preview does not hold the full transformed draft: %v", tester.Texts())
	}
	rect, ok := tester.Find(preview)
	if !ok {
		t.Fatalf("the preview text is not rendered: %v", tester.Texts())
	}
	if rect.W < 200 || rect.H < 30 {
		t.Fatalf("the wrapped preview is only %0.1fx%0.1f: %+v", rect.W, rect.H, rect)
	}
	// The preview sits in a short scroll region; scrolling it must leave the
	// controls and the full text in place.
	tester.Scroll(rect.X+rect.W/2, rect.Y+rect.H/2, 0, 200)
	tester.Frame()
	if !tester.HasText(preview) {
		t.Fatal("scrolling dropped the preview text")
	}
	assertInputInside(t, tester, 720, 480, m.ComposeSend())
	assertInputInside(t, tester, 720, 480, m.ComposePreview())
}

// TestTabStripScrollsToReachEveryTabAtMinimumWidth covers the overflow of the
// Tab strip: the active Tab of a wide strip is scrolled into view when it is
// opened, every Tab stays reachable by scrolling, and selecting a Tab that is
// scrolled out of view brings it back.
func TestTabStripScrollsToReachEveryTabAtMinimumWidth(t *testing.T) {
	m := locales.In("en")
	graph := newTestGraph(t)
	for index := 0; index < 6; index++ {
		if err := graph.state.NewTab(); err != nil {
			t.Fatal(err)
		}
	}
	graph.state.Flush()

	tester := ui.NewTester(View(graph.state), 704, 421)
	tester.SetLocale("en")
	tester.Frame()

	strip := assertInputInside(t, tester, 704, 421, m.WorkTabsLabel())
	// The sixth Tab is active and was opened while the strip overflowed; it
	// must be whole inside the strip without the user scrolling.
	last := assertInputInside(t, tester, 704, 421, "Local Session 6")
	if last.W < 40 {
		t.Fatalf("the active last Tab is clipped at rest: %+v", last)
	}
	if last.X < strip.X-0.5 || last.X+last.W > strip.X+strip.W+0.5 {
		t.Fatalf("the active last Tab is outside the strip at rest: tab %+v strip %+v", last, strip)
	}
	if first, ok := tester.Find("Local Session 1"); ok {
		// The first Tab is allowed to start off-screen, but it must not be
		// half cut inside the viewport: either it is whole or fully out.
		if first.X < strip.X+strip.W && first.X+first.W > strip.X {
			t.Fatalf("the first Tab is half cut at rest: tab %+v strip %+v", first, strip)
		}
	}

	tester.Scroll(strip.X+strip.W/2, strip.Y+strip.H/2, -2000, 0)
	tester.Frame()
	first := assertInputInside(t, tester, 704, 421, "Local Session 1")
	if first.W < 40 {
		t.Fatalf("the first Tab is still clipped after scrolling back: %+v", first)
	}
	if first.X < strip.X-0.5 || first.X+first.W > strip.X+strip.W+0.5 {
		t.Fatalf("the first Tab is outside the strip: tab %+v strip %+v", first, strip)
	}

	tester.Scroll(strip.X+strip.W/2, strip.Y+strip.H/2, 2000, 0)
	tester.Frame()
	last = assertInputInside(t, tester, 704, 421, "Local Session 6")
	if last.W < 40 {
		t.Fatalf("the last Tab is still clipped after scrolling: %+v", last)
	}
	if last.X < strip.X-0.5 || last.X+last.W > strip.X+strip.W+0.5 {
		t.Fatalf("the last Tab is outside the strip: tab %+v strip %+v", last, strip)
	}

	// Selecting the first Tab while the strip is scrolled to the end must
	// reveal it, so a Tab can never be selected yet invisible.
	graph.state.SelectTab(0)
	tester.Frame()
	first = assertInputInside(t, tester, 704, 421, "Local Session 1")
	if first.W < 40 || first.X < strip.X-0.5 || first.X+first.W > strip.X+strip.W+0.5 {
		t.Fatalf("selecting the first Tab did not reveal it: tab %+v strip %+v", first, strip)
	}
}

// TestDenseBroadcastComposeKeepsTabStripAtMinimumSize is the layout pressure
// regression: the active panel, target rows, compose controls, and a long
// preview together must not shrink the Tab strip away at 720x480.
func TestDenseBroadcastComposeKeepsTabStripAtMinimumSize(t *testing.T) {
	m := locales.In("en")
	graph := newInputGraph(t)
	prepareBroadcastAndCompose(t, graph)
	// A long draft makes the compose panel taller, as the demo state does.
	graph.state.SetComposeDraft("echo one long argument\n" +
		"echo two long argument\n" +
		"echo three long argument\n" +
		"echo four long argument\n" +
		"echo five long argument")
	// End one selected Session so its row reports Missing beside Delivered.
	_ = graph.sessions[1].Close()

	tester := ui.NewTester(View(graph.state), 704, 421)
	tester.SetLocale("en")
	tester.Frame()

	strip := assertInputInside(t, tester, 704, 421, m.WorkTabsLabel())
	if strip.H < 25 {
		t.Fatalf("the Tab strip shrank to %.1f DIPs: %+v", strip.H, strip)
	}
	// The third Tab is active; the strip reveals it when it overflows.
	first := assertInputInside(t, tester, 704, 421, "Local Session 3")
	if first.W < 40 {
		t.Fatalf("the active Tab is clipped: %+v", first)
	}
	if first.Y+first.H > strip.Y+strip.H+0.5 {
		t.Fatalf("the Tab leaves the strip: tab %+v strip %+v", first, strip)
	}
	assertInputInside(t, tester, 704, 421, m.ComposeSend())
	assertInputInside(t, tester, 704, 421, m.ComposeCancel())
}
