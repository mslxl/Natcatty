package sessionui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/application/autocomplete"
	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/ui/locales"
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

// openSuggestionPopup performs the first Smart Tab: the keypress that asks for
// the completion list. Under the request-driven popup, typing never opens it,
// so a test that exercises acceptance, navigation, or layout starts here. It
// returns false when Tab was not taken — the same failure the caller would
// report, with the setup named instead.
func openSuggestionPopup(t *testing.T, surface *Surface) {
	t.Helper()
	if !surface.handleAutocompleteKey(0, ui.KeyTab) {
		t.Fatalf("the first Tab did not open the popup: %+v", surface.SuggestionRows())
	}
}


// TestInvalidateSuggestionsDropsADeletedCommand proves an open popup cannot
// keep offering a row the shared journal no longer has: invalidating it makes
// the next frame recompute from the reloaded source, and Tab cannot accept the
// vanished text.
func TestInvalidateSuggestionsDropsADeletedCommand(t *testing.T) {
	transport := newRecordingTransport()
	service := appsession.NewService(recordingFactory{transport: transport})
	t.Cleanup(service.CloseAll)
	session, err := service.Open(context.Background(), appsession.StartSpec{Kind: appsession.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	var available atomic.Bool
	available.Store(true)
	surface, err := New(session,
		WithRedraw(func() {}),
		WithSuggestions(func(input, scope, _ string) []autocomplete.Suggestion {
			if !available.Load() {
				return nil
			}
			return []autocomplete.Suggestion{
				{Text: "git status", Display: "git status", Source: "history"},
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
	tester.ClickAt(360, 420)
	tester.Type("gi")
	if !waitFor(t, func() bool { return session.Snapshot().Input == "gi" }) {
		t.Fatalf("typed input = %q", session.Snapshot().Input)
	}
	tester.Frame()
	// Typing never opens the popup on its own; the first Tab asks for it.
	if !surface.handleAutocompleteKey(0, ui.KeyTab) {
		t.Fatal("Tab was not taken with a suggestion available")
	}
	tester.Frame()
	if len(surface.SuggestionRows()) != 1 {
		t.Fatalf("popup rows = %+v, want the suggestion", surface.SuggestionRows())
	}

	// The History mutation reloads the journal and invalidates open popups.
	available.Store(false)
	surface.InvalidateSuggestions()
	tester.Frame()
	if len(surface.SuggestionRows()) != 0 {
		t.Fatalf("popup rows after invalidation = %+v, want none", surface.SuggestionRows())
	}
	tester.Key(0, ui.KeyTab)
	tester.Frame()
	if bytes.Contains([]byte(transport.input()), []byte("git status")) {
		t.Fatalf("Tab inserted the deleted command: %q", transport.input())
	}
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
		WithSuggestions(func(input, scope, _ string) []autocomplete.Suggestion {
			suggested <- input + "|" + scope
			return []autocomplete.Suggestion{
				{Text: "git status", Display: "git status", Source: "history"},
				{Text: "git stash", Display: "git stash", Source: "history"},
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
	// Typing alone must not open the popup: it answers an explicit request.
	if tester.HasText("git status") {
		t.Fatalf("the popup opened on typing: %v", tester.Texts())
	}
	if !surface.handleAutocompleteKey(0, ui.KeyTab) {
		t.Fatal("Tab was not taken with suggestions available")
	}
	tester.Frame()
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

// TestTabCompletesAfterThePopupWasDismissed guards the key path against a
// dismissal that outlives the popup. Escape hides the rows until the input
// changes, but Tab is an explicit request for completion: it must recompute
// from the typed line, accept the row, and keep the literal tab away from the
// shell.
func TestTabCompletesAfterThePopupWasDismissed(t *testing.T) {
	transport := newRecordingTransport()
	service := appsession.NewService(recordingFactory{transport: transport})
	t.Cleanup(service.CloseAll)
	session, err := service.Open(context.Background(), appsession.StartSpec{Kind: appsession.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	surface, err := New(session,
		WithRedraw(func() {}),
		WithSuggestions(func(input, _, _ string) []autocomplete.Suggestion {
			if input != "gi" {
				return nil
			}
			return []autocomplete.Suggestion{{Text: "git status", Display: "git status", Source: "history"}}
		}, func() (bool, bool) { return true, false }),
	)
	if err != nil {
		t.Fatalf("New surface: %v", err)
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
	// Open the popup with the first Tab.
	if !surface.handleAutocompleteKey(0, ui.KeyTab) {
		t.Fatal("Tab was not taken with a suggestion available")
	}
	tester.Frame()
	if !tester.HasText("git status") {
		t.Fatalf("the popup does not show the suggestion: %v", tester.Texts())
	}

	// Escape closes the popup and the rows stay hidden for the same input.
	tester.Key(0, ui.KeyEscape)
	tester.Frame()
	if tester.HasText("git status") {
		t.Fatalf("Escape left the popup open: %v", tester.Texts())
	}

	// Tab asks for completion again: the first press re-opens the list rather
	// than accepting a row the user is no longer looking at, and the second
	// accepts it. Neither reaches the shell as a tab byte.
	tester.Key(0, ui.KeyTab)
	tester.Frame()
	if !tester.HasText("git status") {
		t.Fatalf("the first Tab after dismissal did not re-open the popup: %v", tester.Texts())
	}
	tester.Key(0, ui.KeyTab)
	if !waitFor(t, func() bool {
		return bytes.Contains([]byte(transport.input()), []byte("\x7f\x7fgit status"))
	}) {
		t.Fatalf("Tab after dismissing the popup typed %q, want the accepted line",
			transport.input())
	}
	if bytes.Contains([]byte(transport.input()), []byte("\t")) {
		t.Fatalf("Tab reached the transport after the popup was dismissed: %q", transport.input())
	}
}

// TestSurfaceAutocompleteRefusesMultilineSuggestionWithoutBracketedPaste
// proves the acceptance path uses the safe insertion seam: without DEC 2004 a
// multi-line suggestion cannot be typed into the editor, so nothing is sent
// and the typed prefix stays. The Tab and popup-click paths share the seam.
func TestSurfaceAutocompleteRefusesMultilineSuggestionWithoutBracketedPaste(t *testing.T) {
	transport := newRecordingTransport()
	service := appsession.NewService(recordingFactory{transport: transport})
	t.Cleanup(service.CloseAll)
	session, err := service.Open(context.Background(), appsession.StartSpec{Kind: appsession.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	const command = "git status\ntouch /tmp/never-run"
	surface, err := New(session,
		WithRedraw(func() {}),
		WithSuggestions(func(input, _, _ string) []autocomplete.Suggestion {
			if input == "gi" {
				return []autocomplete.Suggestion{{
					Text: command, Display: "git status (multiline)", Source: "history",
				}}
			}
			return nil
		}, func() (bool, bool) { return true, false }),
	)
	if err != nil {
		t.Fatalf("New surface: %v", err)
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
	openSuggestionPopup(t, surface)
	if len(surface.SuggestionRows()) != 1 {
		t.Fatalf("popup rows = %+v, want the multiline suggestion", surface.SuggestionRows())
	}
	before := transport.input()

	if !surface.handleAutocompleteKey(0, ui.KeyTab) {
		t.Fatal("Tab was not taken with the popup open")
	}
	if got := transport.input(); got != before {
		t.Fatalf("Tab sent unsafe bytes: %q", got)
	}
	if session.Snapshot().Input != "gi" {
		t.Fatalf("Tab changed the tracked line to %q", session.Snapshot().Input)
	}

	// The popup stays available after the refusal, and the click path uses the
	// same seam and is refused the same way.
	tester.Frame()
	if err := tester.Click("git status (multiline)"); err != nil {
		t.Fatalf("click the multiline suggestion: %v", err)
	}
	if got := transport.input(); got != before {
		t.Fatalf("click sent unsafe bytes: %q", got)
	}
	if session.Snapshot().Input != "gi" {
		t.Fatalf("click changed the tracked line to %q", session.Snapshot().Input)
	}
	if err := surface.insertTextReplacing(2, command); !errors.Is(err, ErrInsertUnsafe) {
		t.Fatalf("insertTextReplacing error = %v, want ErrInsertUnsafe", err)
	}
}

// TestSurfaceAutocompleteInsertsMultilineSuggestionAsBracketedPaste proves the
// same acceptance path sanitizes and wraps a multi-line suggestion when the
// shell enabled DEC 2004: the prefix is erased, the text arrives as one paste,
// and no newline is submitted.
func TestSurfaceAutocompleteInsertsMultilineSuggestionAsBracketedPaste(t *testing.T) {
	transport := newRecordingTransport()
	service := appsession.NewService(recordingFactory{transport: transport})
	t.Cleanup(service.CloseAll)
	session, err := service.Open(context.Background(), appsession.StartSpec{Kind: appsession.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	const command = "git status\ntouch marker"
	surface, err := New(session,
		WithRedraw(func() {}),
		WithSuggestions(func(input, _, _ string) []autocomplete.Suggestion {
			if input == "gi" {
				return []autocomplete.Suggestion{{
					Text: command, Display: "git status (multiline)", Source: "history",
				}}
			}
			return nil
		}, func() (bool, bool) { return true, false }),
	)
	if err != nil {
		t.Fatalf("New surface: %v", err)
	}
	t.Cleanup(surface.Close)
	surface.terminal.Feed([]byte("\x1b[?2004h"))
	if !surface.BracketedPasteEnabled() {
		t.Fatal("bracketed paste mode was not read from the terminal")
	}
	tester := ui.NewTester(func(c *ui.Context) { surfaceFrame(c, surface, false) }, 720, 480)
	tester.SetLocale("en")
	tester.Frame()
	tester.ClickAt(360, 420)
	tester.Type("gi")
	if !waitFor(t, func() bool { return session.Snapshot().Input == "gi" }) {
		t.Fatalf("typed input = %q", session.Snapshot().Input)
	}
	tester.Frame()
	openSuggestionPopup(t, surface)
	if !surface.handleAutocompleteKey(0, ui.KeyTab) {
		t.Fatal("Tab was not taken with the popup open")
	}
	want := "\x7f\x7f\x1b[200~" + command + "\x1b[201~"
	if !waitFor(t, func() bool { return bytes.Contains([]byte(transport.input()), []byte(want)) }) {
		t.Fatalf("accepted input = %q, want %q", transport.input(), want)
	}
	if bytes.HasSuffix([]byte(transport.input()), []byte("\n")) {
		t.Fatalf("acceptance submitted the line: %q", transport.input())
	}
	if !waitFor(t, func() bool { return session.Snapshot().Input == command }) {
		t.Fatalf("the shell line tracked after accepting = %q", session.Snapshot().Input)
	}
}

// An Unsaved Host Session reads autocomplete under the same scope its captured
// commands are recorded with, not the local shell's scope.
func TestSurfaceAutocompleteReadsTheUnsavedHostHistoryScope(t *testing.T) {
	transport := newRecordingTransport()
	service := appsession.NewService(recordingFactory{transport: transport})
	t.Cleanup(service.CloseAll)
	session, err := service.Open(context.Background(), appsession.StartSpec{
		Kind: appsession.KindSSH, Label: "deploy@typed.example",
		Hostname: "typed.example", Username: "deploy",
	})
	if err != nil {
		t.Fatal(err)
	}
	scopes := make(chan string, 4)
	surface, err := New(session,
		WithRedraw(func() {}),
		WithSuggestions(func(input, scope, _ string) []autocomplete.Suggestion {
			scopes <- scope
			return []autocomplete.Suggestion{{Text: input + " status", Source: "history"}}
		}, func() (bool, bool) { return true, false }),
	)
	if err != nil {
		t.Fatalf("New surface: %v", err)
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
	select {
	case scope := <-scopes:
		if scope != "host:unsaved:typed.example:deploy" {
			t.Fatalf("suggestion scope = %q", scope)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the suggestion provider was not called")
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
		WithSuggestions(func(input, _, _ string) []autocomplete.Suggestion {
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
	// Typing alone never opens the list; the arrow keys are the shell's own
	// until the user asks. Rows exist for every typed prefix, so a closed
	// popup that consumed them would break readline's history recall on every
	// line.
	if tester.HasText("gi status") {
		t.Fatalf("the popup opened on typing: %v", tester.Texts())
	}
	if surface.handleAutocompleteKey(0, ui.KeyUp) {
		t.Fatal("Up was consumed while the popup was closed")
	}
	if surface.handleAutocompleteKey(0, ui.KeyDown) {
		t.Fatal("Down was consumed while the popup was closed")
	}
	if surface.handleAutocompleteKey(0, ui.KeyEscape) {
		t.Fatal("Escape was consumed while the popup was closed")
	}
	openSuggestionPopup(t, surface)
	tester.Frame()
	if !tester.HasText("gi status") {
		t.Fatalf("popup missing after Tab: %v", tester.Texts())
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

	// Typing again leaves the popup closed — the dismissal held until the line
	// changed, and the new line was never asked about. The first Tab opens the
	// list for the new input and the second accepts the selected command,
	// neither sending a tab character.
	tester.Type("t")
	if !waitFor(t, func() bool { return session.Snapshot().Input == "git" }) {
		t.Fatalf("typed input = %q", session.Snapshot().Input)
	}
	tester.Frame()
	if tester.HasText("git status") {
		t.Fatalf("the popup re-opened on typing: %v", tester.Texts())
	}
	if !surface.handleAutocompleteKey(0, ui.KeyTab) {
		t.Fatalf("the first Tab was not taken: %v", surface.SuggestionRows())
	}
	tester.Frame()
	if !tester.HasText("git status") {
		t.Fatalf("the first Tab did not open the popup: %v", tester.Texts())
	}
	if !surface.handleAutocompleteKey(0, ui.KeyTab) {
		t.Fatalf("the second Tab was not taken: %v", surface.SuggestionRows())
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
		WithSuggestions(func(input, _, _ string) []autocomplete.Suggestion {
			mu.Lock()
			calls++
			mu.Unlock()
			return []autocomplete.Suggestion{{Text: input + " status", Source: "history"}}
		}, func() (bool, bool) { return true, false }),
		// The pre-delay assertion below samples immediately after typing, so
		// leave enough headroom that a slow frame cannot cross the deadline.
		WithAutocompleteDelay(func() int { return 500 }),
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
	// The debounce gates the rows' refresh, and even after it the list stays
	// closed: only an explicit Tab asks for it. The Tab key path skips the
	// delay (see TestSurfaceTabTakesASuggestionInsideTheDebounceWindow), so
	// this frame waits the delay out before asking.
	if !waitFor(t, func() bool {
		tester.Frame()
		return len(surface.SuggestionRows()) > 0
	}) {
		t.Fatal("the rows were never computed after the delay")
	}
	if tester.HasText("gi status") {
		t.Fatalf("the popup opened without a request: %v", tester.Texts())
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

// TestSurfaceTabTakesASuggestionInsideTheDebounceWindow is the regression for
// a fast Tab: the key handler used to run the frame path's debounce, so a Tab
// pressed before the delay elapsed saw no rows and was left to the Pane, which
// sent a literal tab to the shell. The delay belongs to the popup's appearance,
// not to the key that acts on its rows.
func TestSurfaceTabTakesASuggestionInsideTheDebounceWindow(t *testing.T) {
	transport := newRecordingTransport()
	service := appsession.NewService(recordingFactory{transport: transport})
	t.Cleanup(service.CloseAll)
	session, err := service.Open(context.Background(), appsession.StartSpec{Kind: appsession.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	surface, err := New(session,
		WithRedraw(func() {}),
		WithSuggestions(func(input, _, _ string) []autocomplete.Suggestion {
			return []autocomplete.Suggestion{{Text: input + " status", Source: "history"}}
		}, func() (bool, bool) { return true, false }),
		// Long enough that the test cannot accidentally cross the window.
		WithAutocompleteDelay(func() int { return 60000 }),
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
		t.Fatalf("the popup appeared before the delay: %v", tester.Texts())
	}
	// The first Tab — pressed inside the debounce window — must still open the
	// list rather than fall through to the shell, and the second accepts.
	if !surface.handleAutocompleteKey(0, ui.KeyTab) {
		t.Fatalf("Tab was not taken inside the debounce window: %v", surface.SuggestionRows())
	}
	tester.Frame()
	if !tester.HasText("gi status") {
		t.Fatalf("the first Tab did not open the popup: %v", tester.Texts())
	}
	if !surface.handleAutocompleteKey(0, ui.KeyTab) {
		t.Fatalf("the accepting Tab was not taken: %v", surface.SuggestionRows())
	}
	if !waitFor(t, func() bool {
		return bytes.Contains([]byte(transport.input()), []byte("\x7f\x7fgi status"))
	}) {
		t.Fatalf("accepted input = %q", transport.input())
	}
	if bytes.Contains([]byte(transport.input()), []byte("\t")) {
		t.Fatalf("a literal tab reached the shell: %q", transport.input())
	}
	if !waitFor(t, func() bool { return session.Snapshot().Input == "gi status" }) {
		t.Fatalf("the shell line tracked after accepting = %q", session.Snapshot().Input)
	}
}

// TestSurfaceAutocompleteDoesNotResizeTheTerminal is the regression this fix
// exists for: the popup used to be a chrome row above the terminal, so showing
// it shrank the terminal grid and the Session resized the transport, making the
// shell repaint the whole screen on each keystroke. The floating layers must
// leave the terminal's box untouched.
func TestSurfaceAutocompleteDoesNotResizeTheTerminal(t *testing.T) {
	transport := newRecordingTransport()
	service := appsession.NewService(recordingFactory{transport: transport})
	t.Cleanup(service.CloseAll)
	session, err := service.Open(context.Background(), appsession.StartSpec{Kind: appsession.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	surface, err := New(session,
		WithRedraw(func() {}),
		WithSuggestions(func(input, _, _ string) []autocomplete.Suggestion {
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
	before, ok := tester.Find("Terminal")
	if !ok {
		t.Fatalf("the terminal is not in the frame: %v", tester.Texts())
	}

	tester.Type("gi")
	if !waitFor(t, func() bool { return session.Snapshot().Input == "gi" }) {
		t.Fatalf("typed input = %q", session.Snapshot().Input)
	}
	tester.Frame()
	tester.Frame()
	openSuggestionPopup(t, surface)
	tester.Frame()
	if !tester.HasText("gi status") {
		t.Fatalf("the popup did not show: %v", tester.Texts())
	}
	after, ok := tester.Find("Terminal")
	if !ok {
		t.Fatal("the terminal left the frame when the popup appeared")
	}
	if before != after {
		t.Fatalf("the popup resized the terminal: %+v -> %+v", before, after)
	}

	// Dismissing the popup must not resize it back either.
	if !surface.handleAutocompleteKey(0, ui.KeyEscape) {
		t.Fatal("Escape was not taken while the popup was visible")
	}
	tester.Frame()
	if tester.HasText("gi status") {
		t.Fatalf("Escape did not dismiss the popup: %v", tester.Texts())
	}
	dismissed, ok := tester.Find("Terminal")
	if !ok {
		t.Fatal("the terminal left the frame when the popup closed")
	}
	if before != dismissed {
		t.Fatalf("dismissing the popup resized the terminal: %+v -> %+v", before, dismissed)
	}
}

// TestSurfaceAutocompletePopupFloatsAboveThePrompt feeds the terminal a screen
// of output and a shell prompt, then checks the popup appears above the input
// row at the bottom of the terminal, not in the chrome above the terminal and
// not over the line being completed.
func TestSurfaceAutocompletePopupFloatsAboveThePrompt(t *testing.T) {
	transport := newRecordingTransport()
	service := appsession.NewService(recordingFactory{transport: transport})
	t.Cleanup(service.CloseAll)
	session, err := service.Open(context.Background(), appsession.StartSpec{Kind: appsession.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	surface, err := New(session,
		WithRedraw(func() {}),
		WithSuggestions(func(input, _, _ string) []autocomplete.Suggestion {
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

	// A shell keeps the prompt on the last visible row after output scrolls.
	surface.terminal.Feed([]byte(strings.Repeat("output\r\n", 40) + "$ gi"))
	tester.Frame()
	tester.Type("gi")
	if !waitFor(t, func() bool { return session.Snapshot().Input == "gi" }) {
		t.Fatalf("typed input = %q", session.Snapshot().Input)
	}
	tester.Frame()
	tester.Frame()
	openSuggestionPopup(t, surface)
	tester.Frame()
	if !tester.HasText("gi status") {
		t.Fatalf("the popup did not show: %v", tester.Texts())
	}

	terminal, ok := tester.Find("Terminal")
	if !ok {
		t.Fatalf("the terminal is not in the frame: %v", tester.Texts())
	}
	_, rows := surface.terminal.Size()
	cellHeight := (terminal.H - 2*terminalPaddingY) / float32(rows)
	inputTop := terminal.Y + terminal.H - terminalPaddingY - cellHeight
	m := locales.In("en")
	header, ok := tester.Find(m.SessionAutocompleteLabel())
	if !ok {
		t.Fatalf("the popup header is missing: %v", tester.Texts())
	}
	last, ok := tester.Find("gi stash")
	if !ok {
		t.Fatalf("the last suggestion is missing: %v", tester.Texts())
	}
	// The popup floats over the terminal's lower half and ends above the
	// input row, so the list does not cover the line being completed.
	if header.Y <= terminal.Y+terminal.H/2 {
		t.Fatalf("the popup stayed at the top of the terminal: header %+v terminal %+v",
			header, terminal)
	}
	if last.Y+last.H > inputTop+0.5 {
		t.Fatalf("the popup covers the input line: last row %+v input top %v",
			last, inputTop)
	}
}

// TestAutocompletePopupShowsAndRelocalizesDescriptions proves the catalog
// annotation reaches the popup, and that a language switch invalidates the
// cached rows so the description follows the rest of the UI without reopening
// the Session.
func TestAutocompletePopupShowsAndRelocalizesDescriptions(t *testing.T) {
	transport := newRecordingTransport()
	service := appsession.NewService(recordingFactory{transport: transport})
	t.Cleanup(service.CloseAll)
	session, err := service.Open(context.Background(), appsession.StartSpec{Kind: appsession.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	description := locales.In("en").CommandDescriptionLs()
	surface, err := New(session,
		WithRedraw(func() {}),
		WithSuggestions(func(input, _, _ string) []autocomplete.Suggestion {
			return []autocomplete.Suggestion{
				{Text: input + " -la", Description: description, Source: "history"},
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
	tester.Type("ls")
	if !waitFor(t, func() bool { return session.Snapshot().Input == "ls" }) {
		t.Fatalf("typed input = %q", session.Snapshot().Input)
	}
	tester.Frame()
	tester.Frame()
	openSuggestionPopup(t, surface)
	tester.Frame()
	if !tester.HasText(description) {
		t.Fatalf("the popup did not show %q: %v", description, tester.Texts())
	}

	// A live language switch re-asks the service, whose callback reads the new
	// locale; the popup shows the translated text without a new keystroke.
	description = locales.In("zh-TW").CommandDescriptionLs()
	tester.SetLocale("zh-TW")
	tester.Frame()
	tester.Frame()
	rows := surface.SuggestionRows()
	if len(rows) != 1 || rows[0].Description != description {
		t.Fatalf("cached rows after the language switch = %+v, want %q", rows, description)
	}
	if !tester.HasText(description) {
		t.Fatalf("the popup did not relocalize to %q: %v", description, tester.Texts())
	}
}

// startProgramPromptSession opens a local Session, submits a command so the
// command is running, and waits for output that looks like a program's input
// prompt. The terminal has no program echo in these tests, so the prompt and
// the typed query are all the popup has to go on.
func startProgramPromptSession(t *testing.T, prompt string, optionList ...Option) (*Surface, *appsession.Session, *scriptedTransport, *ui.Tester) {
	t.Helper()
	transport := newScriptedTransport()
	service := appsession.NewService(scriptedFactory{transport: transport})
	t.Cleanup(service.CloseAll)
	session, err := service.Open(context.Background(), appsession.StartSpec{Kind: appsession.KindLocal})
	if err != nil {
		t.Fatalf("open Session: %v", err)
	}
	options := append([]Option{WithRedraw(func() {})}, optionList...)
	surface, err := New(session, options...)
	if err != nil {
		t.Fatalf("New surface: %v", err)
	}
	t.Cleanup(surface.Close)
	tester := surfaceTester(surface)
	tester.Frame()
	tester.ClickAt(450, 520)
	tester.Type("read input")
	tester.Key(0, ui.KeyEnter)
	if !waitFor(t, func() bool {
		snapshot := session.Snapshot()
		return snapshot.Command.Running && snapshot.Input == ""
	}) {
		t.Fatalf("the command did not start: %+v", session.Snapshot().Command)
	}
	transport.emit(prompt)
	if !waitFor(t, func() bool { return strings.Contains(surface.ScreenText(), strings.TrimSpace(prompt)) }) {
		t.Fatalf("the program prompt did not reach the screen: %q", surface.ScreenText())
	}
	return surface, session, transport, tester
}

// TestAutocompleteOffersSnippetsAtProgramPrompt proves a Vault Snippet joins
// the popup while a program's read prompt is waiting, and Tab inserts its
// command into the line editor at the cursor.
func TestAutocompleteOffersSnippetsAtProgramPrompt(t *testing.T) {
	requests := make(chan bool, 8)
	surface, session, transport, tester := startProgramPromptSession(t, "Email: ",
		WithSuggestions(func(input, scope, cwd string) []autocomplete.Suggestion {
			return nil
		}, func() (bool, bool) { return true, false }),
		WithPromptSuggestions(func(input, scope, cwd string, program bool) []autocomplete.Suggestion {
			requests <- program
			if !program || input != "wo" {
				return nil
			}
			return []autocomplete.Suggestion{{
				Text: "work@example.com", Display: "Work email",
				Source: autocomplete.SourceSnippet,
			}}
		}),
	)
	// Frames while the command was being typed asked the shell provider; only
	// the request after the query is the program-prompt one under test.
	for len(requests) > 0 {
		<-requests
	}
	tester.Type("wo")
	if !waitFor(t, func() bool { return session.Snapshot().Input == "wo" }) {
		t.Fatalf("typed input = %q", session.Snapshot().Input)
	}
	tester.Frame()
	select {
	case program := <-requests:
		if !program {
			t.Fatal("the provider was not told this is a program prompt")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the prompt suggestion provider was not called")
	}
	rows := surface.SuggestionRows()
	if len(rows) != 1 || rows[0].Source != autocomplete.SourceSnippet ||
		rows[0].Text != "work@example.com" {
		t.Fatalf("popup rows = %+v, want the snippet", rows)
	}
	openSuggestionPopup(t, surface)
	tester.Frame()
	if !tester.HasText("Work email") {
		t.Fatalf("the popup does not show the snippet label: %v", tester.Texts())
	}

	if !surface.handleAutocompleteKey(0, ui.KeyTab) {
		t.Fatal("Tab was not taken with the popup open")
	}
	if !waitFor(t, func() bool {
		return strings.Contains(transport.Input(), "\x7f\x7fwork@example.com")
	}) {
		t.Fatalf("accepted input = %q", transport.Input())
	}
	if strings.Contains(transport.Input(), "\x15") {
		t.Fatalf("Ctrl+U reached the transport: %q", transport.Input())
	}
}

// TestAutocompleteMidLineAcceptanceKeepsTheSuffix proves the acceptance erase
// stops at the tracked cursor. With the cursor between "wo" and "r", only the
// two characters left of it are erased; the suffix stays on the line.
func TestAutocompleteMidLineAcceptanceKeepsTheSuffix(t *testing.T) {
	surface, session, transport, tester := startProgramPromptSession(t, "Email: ",
		WithSuggestions(func(input, scope, cwd string) []autocomplete.Suggestion {
			return nil
		}, func() (bool, bool) { return true, false }),
		WithPromptSuggestions(func(input, scope, cwd string, program bool) []autocomplete.Suggestion {
			return []autocomplete.Suggestion{{
				Text: "work@example.com", Display: "Work email",
				Source: autocomplete.SourceSnippet,
			}}
		}),
	)
	tester.Type("wor")
	if !waitFor(t, func() bool { return session.Snapshot().Input == "wor" }) {
		t.Fatalf("typed input = %q", session.Snapshot().Input)
	}
	// The left arrow moves the line editor's cursor between "wo" and "r".
	if !session.InsertInput([]byte("\x1b[D")) {
		t.Fatal("the left arrow was refused")
	}
	if !waitFor(t, func() bool { return session.Snapshot().InputCursor == 2 }) {
		t.Fatalf("tracked cursor = %d, want 2", session.Snapshot().InputCursor)
	}
	tester.Frame()
	openSuggestionPopup(t, surface)
	before := strings.Count(transport.Input(), "\x7f")
	if !surface.handleAutocompleteKey(0, ui.KeyTab) {
		t.Fatal("Tab was not taken with the popup open")
	}
	if !waitFor(t, func() bool {
		return strings.Count(transport.Input(), "\x7f") == before+2
	}) {
		t.Fatalf("acceptance erased %d characters, want only the 2 left of the cursor: %q",
			strings.Count(transport.Input(), "\x7f")-before, transport.Input())
	}
}

// TestAutocompleteRefusesUnsafeMultilineSnippetAtProgramPrompt keeps the
// existing bracket-paste safeguard at a program prompt: without DEC 2004 the
// multi-line Snippet is not typed, so nothing is sent and the typed query
// stays in the editor.
func TestAutocompleteRefusesUnsafeMultilineSnippetAtProgramPrompt(t *testing.T) {
	const command = "first line\nsecond line"
	surface, session, transport, tester := startProgramPromptSession(t, "Email: ",
		WithSuggestions(func(input, scope, cwd string) []autocomplete.Suggestion {
			return nil
		}, func() (bool, bool) { return true, false }),
		WithPromptSuggestions(func(input, scope, cwd string, program bool) []autocomplete.Suggestion {
			return []autocomplete.Suggestion{{
				Text: command, Display: "Two lines", Source: autocomplete.SourceSnippet,
			}}
		}),
	)
	tester.Type("wo")
	if !waitFor(t, func() bool { return session.Snapshot().Input == "wo" }) {
		t.Fatalf("typed input = %q", session.Snapshot().Input)
	}
	tester.Frame()
	openSuggestionPopup(t, surface)
	if len(surface.SuggestionRows()) != 1 {
		t.Fatalf("popup rows = %+v, want the multiline snippet", surface.SuggestionRows())
	}
	before := transport.Input()
	if !surface.handleAutocompleteKey(0, ui.KeyTab) {
		t.Fatal("Tab was not taken with the popup open")
	}
	if got := transport.Input(); got != before {
		t.Fatalf("Tab sent unsafe bytes: %q", got)
	}
	if session.Snapshot().Input != "wo" {
		t.Fatalf("Tab changed the tracked line to %q", session.Snapshot().Input)
	}
	if err := surface.insertTextReplacing(2, command); !errors.Is(err, ErrInsertUnsafe) {
		t.Fatalf("insertTextReplacing error = %v, want ErrInsertUnsafe", err)
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
