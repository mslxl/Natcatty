package sessionui

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/application/autocomplete"
	appsession "github.com/mslxl/natcatty/application/session"
)

type recordingTransport struct {
	mu      sync.Mutex
	written bytes.Buffer
	closed  chan struct{}
	once    sync.Once
}

func newRecordingTransport() *recordingTransport {
	return &recordingTransport{closed: make(chan struct{})}
}

func (t *recordingTransport) Read([]byte) (int, error) { <-t.closed; return 0, io.EOF }
func (t *recordingTransport) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.written.Write(p)
}
func (t *recordingTransport) Resize(int, int) error { return nil }
func (t *recordingTransport) Close() error {
	t.once.Do(func() { close(t.closed) })
	return nil
}
func (t *recordingTransport) input() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.written.String()
}

type recordingFactory struct{ transport *recordingTransport }

func (f recordingFactory) Open(context.Context, appsession.StartSpec) (appsession.Transport, error) {
	return f.transport, nil
}

func TestSurfaceAutocompleteSuggestsFromTheTypedLineAndTabAccepts(t *testing.T) {
	transport := newRecordingTransport()
	service := appsession.NewService(recordingFactory{transport: transport})
	t.Cleanup(service.CloseAll)
	session, err := service.Open(context.Background(), appsession.StartSpec{Kind: appsession.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	suggested := make(chan string, 4)
	surface, err := New(session,
		WithRedraw(func() {}),
		WithSuggestions(func(input, scope string) []autocomplete.Suggestion {
			suggested <- input + "|" + scope
			return []autocomplete.Suggestion{
				{Text: "git status", Display: "git status", Source: "history"},
				{Text: "git stash", Display: "git stash", Source: "shell-history"},
			}
		}, func() (bool, bool) { return true, false }),
	)
	if err != nil {
		t.Fatalf("New surface: %v", err)
	}
	t.Cleanup(surface.Close)
	tester := ui.NewTester(func(c *ui.Context) { surfaceFrame(c, surface, false) }, 720, 480)
	tester.SetLocale("en")
	tester.Frame()

	// Type into the real terminal element. The Session's tap records the line,
	// and the next frame asks the suggestion provider for it.
	tester.ClickAt(360, 420)
	tester.Type("gi")
	if !waitFor(t, func() bool { return session.Snapshot().Input == "gi" }) {
		t.Fatalf("typed input = %q", session.Snapshot().Input)
	}
	tester.Frame()
	select {
	case request := <-suggested:
		if request != "gi|local" {
			t.Fatalf("suggestion request = %q, want gi|local", request)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the suggestion provider was not called")
	}
	if !tester.HasText("git status") || !tester.HasText("git stash") {
		t.Fatalf("the popup does not show the suggestions: %v", tester.Texts())
	}

	// The popup rows are plain rows with a click action: clicking one accepts
	// it through the same path Tab uses.
	if err := tester.Click("git status"); err != nil {
		t.Fatalf("click the suggestion: %v", err)
	}
	if !waitFor(t, func() bool {
		return bytes.Contains([]byte(transport.input()), []byte("\x7f\x7fgit status"))
	}) {
		t.Fatalf("accepted input = %q", transport.input())
	}
	if bytes.Contains([]byte(transport.input()), []byte("\t")) {
		t.Fatalf("Tab reached the transport: %q", transport.input())
	}
	if bytes.Contains([]byte(transport.input()), []byte{0x15}) {
		t.Fatalf("Ctrl+U reached the transport: %q", transport.input())
	}
	if !waitFor(t, func() bool { return session.Snapshot().Input == "git status" }) {
		t.Fatalf("the shell line tracked after accepting = %q", session.Snapshot().Input)
	}
}

func TestSurfaceAutocompleteKeysDismissSelectAndAccept(t *testing.T) {
	transport := newRecordingTransport()
	service := appsession.NewService(recordingFactory{transport: transport})
	t.Cleanup(service.CloseAll)
	session, err := service.Open(context.Background(), appsession.StartSpec{Kind: appsession.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	surface, err := New(session,
		WithRedraw(func() {}),
		WithSuggestions(func(input, _ string) []autocomplete.Suggestion {
			return []autocomplete.Suggestion{
				{Text: input + " status", Source: "history"},
				{Text: input + " stash", Source: "history"},
			}
		}, func() (bool, bool) { return true, false }),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(surface.Close)
	tester := ui.NewTester(func(c *ui.Context) { surfaceFrame(c, surface, false) }, 720, 480)
	tester.SetLocale("en")
	tester.Frame()
	tester.ClickAt(360, 420)
	tester.Type("gi")
	if !waitFor(t, func() bool { return session.Snapshot().Input == "gi" }) {
		t.Fatalf("typed input = %q", session.Snapshot().Input)
	}
	tester.Frame()
	if !tester.HasText("gi status") {
		t.Fatalf("popup missing: %v", tester.Texts())
	}

	// Down selects the second row; Escape dismisses the popup but leaves the
	// typed line alone.
	if !surface.handleAutocompleteKey(0, ui.KeyDown) {
		t.Fatal("Down was not taken while the popup was visible")
	}
	if surface.suggestionIndex != 1 {
		t.Fatalf("suggestion index = %d, want 1", surface.suggestionIndex)
	}
	if !surface.handleAutocompleteKey(0, ui.KeyEscape) {
		t.Fatal("Escape was not taken while the popup was visible")
	}
	tester.Frame()
	if tester.HasText("gi status") {
		t.Fatalf("Escape did not dismiss the popup: %v", tester.Texts())
	}
	if session.Snapshot().Input != "gi" {
		t.Fatalf("Escape changed the typed line to %q", session.Snapshot().Input)
	}

	// Typing again brings the popup back; Tab accepts the selected command
	// instead of sending a tab character.
	tester.Type("t")
	if !waitFor(t, func() bool { return session.Snapshot().Input == "git" }) {
		t.Fatalf("typed input = %q", session.Snapshot().Input)
	}
	tester.Frame()
	if !surface.handleAutocompleteKey(0, ui.KeyTab) {
		t.Fatalf("Tab was not taken with suggestions: %v", surface.SuggestionRows())
	}
	if !waitFor(t, func() bool {
		return bytes.Contains([]byte(transport.input()), []byte("\x7f\x7f\x7fgit status"))
	}) {
		t.Fatalf("accepted input = %q", transport.input())
	}
	if !waitFor(t, func() bool { return session.Snapshot().Input == "git status" }) {
		t.Fatalf("the shell line tracked after accepting = %q", session.Snapshot().Input)
	}
}

// TestSurfaceAutocompleteHonorsTheDebounceAndRunningCommand covers the two
// policies that keep autocomplete out of the way: a configured delay, and a
// command that is still running (vim, less) where Tab must reach the program.
func TestSurfaceAutocompleteHonorsTheDebounceAndRunningCommand(t *testing.T) {
	transport := newRecordingTransport()
	service := appsession.NewService(recordingFactory{transport: transport})
	t.Cleanup(service.CloseAll)
	session, err := service.Open(context.Background(), appsession.StartSpec{Kind: appsession.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	calls := 0
	surface, err := New(session,
		WithRedraw(func() {}),
		WithSuggestions(func(input, _ string) []autocomplete.Suggestion {
			mu.Lock()
			calls++
			mu.Unlock()
			return []autocomplete.Suggestion{{Text: input + " status", Source: "history"}}
		}, func() (bool, bool) { return true, false }),
		WithAutocompleteDelay(func() int { return 40 }),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(surface.Close)
	tester := ui.NewTester(func(c *ui.Context) { surfaceFrame(c, surface, false) }, 720, 480)
	tester.SetLocale("en")
	tester.Frame()
	tester.ClickAt(360, 420)
	tester.Type("gi")
	if !waitFor(t, func() bool { return session.Snapshot().Input == "gi" }) {
		t.Fatalf("typed input = %q", session.Snapshot().Input)
	}
	tester.Frame()
	if tester.HasText("gi status") {
		t.Fatalf("suggestions appeared before the delay: %v", tester.Texts())
	}
	if !waitFor(t, func() bool {
		tester.Frame()
		return tester.HasText("gi status")
	}) {
		t.Fatalf("suggestions never appeared after the delay: %v", tester.Texts())
	}

	// Submit a command: while it runs, suggestions stay closed and Tab is not
	// consumed, so it reaches the running program.
	if _, err := session.TerminalConn().Write([]byte("less\r")); err != nil {
		t.Fatal(err)
	}
	if !waitFor(t, func() bool { return session.Snapshot().Command.Running }) {
		t.Fatalf("the command did not start: %+v", session.Snapshot().Command)
	}
	tester.Frame()
	if tester.HasText("git status") {
		t.Fatalf("suggestions appeared while a command was running: %v", tester.Texts())
	}
	if surface.handleAutocompleteKey(0, ui.KeyTab) {
		t.Fatal("Tab was consumed while a command was running")
	}
	mu.Lock()
	afterRunning := calls
	mu.Unlock()
	if afterRunning == 0 {
		t.Fatal("the provider was never called")
	}
}

func waitFor(t *testing.T, condition func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}
