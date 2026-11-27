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

// openSuggestionPopup waits out the debounce and forces the frame that opens
// the automatic popup. Under the pass-through-Tab model the list opens on its
// own once rows exist and the debounce has passed; a test that exercises
// navigation, preview, or layout starts here.
func openSuggestionPopup(t *testing.T, surface *Surface, tester *ui.Tester) {
	t.Helper()
	surface.refreshSuggestionsFor(surface.session.Snapshot().Input, surface.session.Snapshot().CWD, surface.session.Snapshot().Command.Running, false, true)
	tester.Frame()
	if len(surface.SuggestionRows()) == 0 {
		t.Fatalf("the popup did not open after the debounce: %+v", surface.SuggestionRows())
	}
}

// waitForTransportInput waits for needle to appear in the transport's recorded
// bytes. Input travels the Session's bounded write queue before reaching the
// transport, so a byte pattern is never visible in the same instant the key
// handler ran — asserting without the wait would flake on a loaded machine.
func waitForTransportInput(t *testing.T, transport *recordingTransport, needle string) bool {
	t.Helper()
	return waitFor(t, func() bool { return strings.Contains(transport.input(), needle) })
}

// newAutocompleteSurface opens a local Session behind a recording transport
// and a surface whose suggestion provider is the test's own.
func newAutocompleteSurface(t *testing.T, suggest func(input, scope, cwd string) []autocomplete.Suggestion, display func() (bool, bool), extra ...Option) (*Surface, *appsession.Session, *recordingTransport, *ui.Tester) {
	t.Helper()
	transport := newRecordingTransport()
	service := appsession.NewService(recordingFactory{transport: transport})
	t.Cleanup(service.CloseAll)
	session, err := service.Open(context.Background(), appsession.StartSpec{Kind: appsession.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	options := append([]Option{
		WithRedraw(func() {}),
		WithSuggestions(suggest, display),
	}, extra...)
	surface, err := New(session, options...)
	if err != nil {
		t.Fatalf("New surface: %v", err)
	}
	t.Cleanup(surface.Close)
	tester := ui.NewTester(func(c *ui.Context) { surfaceFrame(c, surface, false) }, 720, 480)
	tester.SetLocale("en")
	tester.Frame()
	tester.ClickAt(360, 420)
	return surface, session, transport, tester
}

// typeIntoTerminal types text into the surface's terminal and waits for the
// tap to mirror it.
func typeIntoTerminal(t *testing.T, tester *ui.Tester, session *appsession.Session, text string) {
	t.Helper()
	tester.Type(text)
	if !waitFor(t, func() bool { return session.Snapshot().Input == text }) {
		t.Fatalf("typed input = %q, want %q", session.Snapshot().Input, text)
	}
	tester.Frame()
}

// TestInvalidateSuggestionsDropsADeletedCommand proves an open popup cannot
// keep offering a row the shared journal no longer has: invalidating it makes
// the next frame recompute from the reloaded source, so the vanished text can
// never be previewed into the line.
func TestInvalidateSuggestionsDropsADeletedCommand(t *testing.T) {
	var available atomic.Bool
	available.Store(true)
	surface, session, transport, tester := newAutocompleteSurface(t,
		func(input, scope, _ string) []autocomplete.Suggestion {
			if !available.Load() {
				return nil
			}
			return []autocomplete.Suggestion{
				{Text: "git status", Display: "git status", Source: "history"},
			}
		}, func() (bool, bool) { return true, false })

	typeIntoTerminal(t, tester, session, "gi")
	openSuggestionPopup(t, surface, tester)
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

	// Navigation cannot put the deleted command into the line: the rows are
	// gone, so the arrows fall through to the shell as its own keys.
	if surface.handleAutocompleteKey(0, ui.KeyDown) {
		t.Fatal("Down was consumed with no rows")
	}
	tester.Frame()
	if bytes.Contains([]byte(transport.input()), []byte("git status")) {
		t.Fatalf("the deleted command reached the line: %q", transport.input())
	}
}

// TestSurfaceAutocompleteSuggestsFromTheTypedLine proves the popup's rows
// answer the line the user is typing — the Session's tap records the
// keystrokes, the next frame asks the provider with that text and scope — and
// that clicking a row commits it through the erase-and-rewrite seam.
func TestSurfaceAutocompleteSuggestsFromTheTypedLineAndClickCommits(t *testing.T) {
	suggested := make(chan string, 4)
	surface, session, transport, tester := newAutocompleteSurface(t,
		func(input, scope, _ string) []autocomplete.Suggestion {
			suggested <- input + "|" + scope
			return []autocomplete.Suggestion{
				{Text: "git status", Display: "git status", Source: "history"},
				{Text: "git stash", Display: "git stash", Source: "history"},
			}
		}, func() (bool, bool) { return true, false })

	typeIntoTerminal(t, tester, session, "gi")
	openSuggestionPopup(t, surface, tester)
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

	// Clicking a row is a decisive pick: the whole typed prefix is erased and
	// the candidate written whole — never a revertable preview.
	if err := tester.Click("git status"); err != nil {
		t.Fatalf("click the suggestion: %v", err)
	}
	if !waitForTransportInput(t, transport, "\x7f\x7fgit status") {
		t.Fatalf("accepted input = %q, want the erase+rewrite of the clicked row", transport.input())
	}
	if bytes.Contains([]byte(transport.input()), []byte{0x15}) {
		t.Fatalf("Ctrl+U reached the transport: %q", transport.input())
	}
	if !waitFor(t, func() bool { return session.Snapshot().Input == "git status" }) {
		t.Fatalf("the shell line tracked after accepting = %q", session.Snapshot().Input)
	}
	// The popup closes on the commit: the rows are dropped at once, and even
	// after the committed line lands in the mirror and the rows recompute
	// against it, the dismissal keeps the list hidden until the input changes
	// again.
	tester.Frame()
	tester.Frame()
	if surface.showAutocomplete {
		t.Fatalf("the click commit left the popup open: rows %+v", surface.SuggestionRows())
	}
	if tester.HasText("git stash") {
		t.Fatalf("the popup stayed visible after the click commit: %v", tester.Texts())
	}
}

// TestTabPassesThroughWithThePopupOpen is the core of the pass-through model:
// Tab is never intercepted. With the popup open and a preview in flight, the
// key handler reverts the line to the typed baseline, closes the popup, and
// returns false — the 0x09 byte reaches the shell, whose own completion is the
// one the user meant.
func TestTabPassesThroughWithThePopupOpen(t *testing.T) {
	surface, session, transport, tester := newAutocompleteSurface(t,
		func(input, _, _ string) []autocomplete.Suggestion {
			return []autocomplete.Suggestion{
				{Text: "git status", Display: "git status", Source: "history"},
			}
		}, func() (bool, bool) { return true, false })

	typeIntoTerminal(t, tester, session, "git")
	openSuggestionPopup(t, surface, tester)

	// Steer onto the row so a live preview is in flight when Tab lands.
	if !surface.handleAutocompleteKey(0, ui.KeyDown) {
		t.Fatal("Down was not taken while the popup was visible")
	}
	if !waitForTransportInput(t, transport, "git status") {
		t.Fatalf("the preview never reached the transport: %q", transport.input())
	}
	if !waitFor(t, func() bool { return session.Snapshot().Input == "git status" }) {
		t.Fatalf("preview input = %q", session.Snapshot().Input)
	}

	// Tab is not consumed in any state.
	if surface.handleAutocompleteKey(0, ui.KeyTab) {
		t.Fatal("Tab was consumed with the popup open")
	}
	tester.Key(0, ui.KeyTab)
	// The preview is reverted before the tab byte leaves: the shell completes
	// against the typed baseline, not a candidate the user never committed to.
	if !waitFor(t, func() bool { return session.Snapshot().Input == "git" }) {
		t.Fatalf("Tab left the previewed line in place: %q", session.Snapshot().Input)
	}
	if !waitForTransportInput(t, transport, "\x7f\x7f\x7f\x7f\x7f\x7f\x7f\x7f\x7f\x7fgit") {
		t.Fatalf("the revert never reached the transport: %q", transport.input())
	}
	if !waitForTransportInput(t, transport, "\t") {
		t.Fatalf("the Tab byte never reached the transport: %q", transport.input())
	}
	tester.Frame()
	if tester.HasText("git status") {
		t.Fatalf("the popup stayed open through Tab: %v", tester.Texts())
	}
	if len(surface.SuggestionRows()) != 0 {
		t.Fatalf("the rows survived Tab: %+v", surface.SuggestionRows())
	}
}

// TestTabPassesThroughWithOnlyTheGhost covers the other passive layer: the
// ghost hint hides on Tab and the key still reaches the shell.
func TestTabPassesThroughWithOnlyTheGhost(t *testing.T) {
	surface, session, transport, tester := newAutocompleteSurface(t,
		func(input, _, _ string) []autocomplete.Suggestion {
			return []autocomplete.Suggestion{
				{Text: "git status", Display: "git status", Source: "history"},
			}
		}, func() (bool, bool) { return false, true })

	typeIntoTerminal(t, tester, session, "git")
	surface.refreshSuggestionsFor("git", "", false, false, true)
	tester.Frame()
	if !surface.showGhost {
		t.Fatal("the ghost hint did not appear")
	}
	if surface.handleAutocompleteKey(0, ui.KeyTab) {
		t.Fatal("Tab was consumed with the ghost visible")
	}
	if surface.showGhost {
		t.Fatal("the ghost stayed visible through Tab")
	}
	tester.Key(0, ui.KeyTab)
	if !waitForTransportInput(t, transport, "\t") {
		t.Fatalf("the Tab byte never reached the transport: %q", transport.input())
	}
}

// TestArrowNavigationLivePreviewsTheRows proves the steering model: moving
// onto a row writes the candidate into the real shell line — only the tail
// when it extends what is typed, erases plus the whole text when it does
// not — and cycling back off the end restores the typed baseline exactly.
func TestArrowNavigationLivePreviewsTheRows(t *testing.T) {
	surface, session, transport, tester := newAutocompleteSurface(t,
		func(input, _, _ string) []autocomplete.Suggestion {
			return []autocomplete.Suggestion{
				{Text: "git status", Display: "git status", Source: "history"},
				{Text: "gzip archive", Display: "gzip archive", Source: "history"},
			}
		}, func() (bool, bool) { return true, false })

	typeIntoTerminal(t, tester, session, "git")
	openSuggestionPopup(t, surface, tester)
	if surface.suggestionIndex != noSelection {
		t.Fatalf("the selection started at %d, want noSelection", surface.suggestionIndex)
	}

	// Row 0 extends the typed line, so the preview is the tail alone: no erase
	// byte is sent, and the tracked line becomes the candidate.
	if !surface.handleAutocompleteKey(0, ui.KeyDown) {
		t.Fatal("Down was not taken while the popup was visible")
	}
	if surface.suggestionIndex != 0 {
		t.Fatalf("suggestion index = %d, want 0", surface.suggestionIndex)
	}
	if !waitForTransportInput(t, transport, "git status") {
		t.Fatalf("the previewed tail never reached the transport: %q", transport.input())
	}
	if bytes.Contains([]byte(transport.input()), []byte{0x7f}) {
		t.Fatalf("a prefix preview erased text it only needed to extend: %q", transport.input())
	}
	if !waitFor(t, func() bool { return session.Snapshot().Input == "git status" }) {
		t.Fatalf("preview input = %q, want the candidate", session.Snapshot().Input)
	}

	// Row 1 does not extend the line in place: the previewed text is erased
	// whole and the candidate written in full.
	if !surface.handleAutocompleteKey(0, ui.KeyDown) {
		t.Fatal("the second Down was not taken")
	}
	if !waitForTransportInput(t, transport, "\x7f\x7f\x7f\x7f\x7f\x7f\x7f\x7f\x7f\x7fgzip archive") {
		t.Fatalf("the non-prefix preview never rewrote the line: %q", transport.input())
	}
	if !waitFor(t, func() bool { return session.Snapshot().Input == "gzip archive" }) {
		t.Fatalf("preview input = %q, want gzip archive", session.Snapshot().Input)
	}

	// Stepping past the last row lands on the no-selection slot: the line
	// returns to exactly what was typed, erases plus the baseline.
	if !surface.handleAutocompleteKey(0, ui.KeyDown) {
		t.Fatal("the wrapping Down was not taken")
	}
	if surface.suggestionIndex != noSelection {
		t.Fatalf("the selection did not wrap to noSelection: %d", surface.suggestionIndex)
	}
	if !waitForTransportInput(t, transport, "\x7f\x7f\x7f\x7f\x7f\x7f\x7f\x7f\x7f\x7f\x7f\x7fgit") {
		t.Fatalf("the baseline revert never reached the transport: %q", transport.input())
	}
	if !waitFor(t, func() bool { return session.Snapshot().Input == "git" }) {
		t.Fatalf("the line after cycling closed = %q, want the typed baseline", session.Snapshot().Input)
	}

	// Up from the no-selection slot wraps to the last row: the cycle runs
	// [-1, 0, …, count-1] in both directions.
	if !surface.handleAutocompleteKey(0, ui.KeyUp) {
		t.Fatal("Up was not taken while the popup was visible")
	}
	if surface.suggestionIndex != 1 {
		t.Fatalf("suggestion index after the wrapping Up = %d, want 1", surface.suggestionIndex)
	}
	if !waitFor(t, func() bool { return session.Snapshot().Input == "gzip archive" }) {
		t.Fatalf("preview input after the wrapping Up = %q", session.Snapshot().Input)
	}
}

// TestEscapeRevertsThePreviewAndDismisses proves Escape restores the typed
// baseline over whatever the live preview wrote, then closes the list until
// the input changes.
func TestEscapeRevertsThePreviewAndDismisses(t *testing.T) {
	surface, session, transport, tester := newAutocompleteSurface(t,
		func(input, _, _ string) []autocomplete.Suggestion {
			return []autocomplete.Suggestion{
				{Text: input + " status", Source: "history"},
			}
		}, func() (bool, bool) { return true, false })

	typeIntoTerminal(t, tester, session, "git")
	openSuggestionPopup(t, surface, tester)
	if !surface.handleAutocompleteKey(0, ui.KeyDown) {
		t.Fatal("Down was not taken while the popup was visible")
	}
	if !waitFor(t, func() bool { return session.Snapshot().Input == "git status" }) {
		t.Fatalf("preview input = %q", session.Snapshot().Input)
	}
	if !waitForTransportInput(t, transport, "git status") {
		t.Fatalf("the preview never reached the transport: %q", transport.input())
	}

	if !surface.handleAutocompleteKey(0, ui.KeyEscape) {
		t.Fatal("Escape was not taken while the popup was visible")
	}
	if !waitFor(t, func() bool { return session.Snapshot().Input == "git" }) {
		t.Fatalf("Escape left the previewed line in place: %q", session.Snapshot().Input)
	}
	if !waitForTransportInput(t, transport, "\x7f\x7f\x7f\x7f\x7f\x7f\x7f\x7f\x7f\x7fgit") {
		t.Fatalf("the revert never reached the transport: %q", transport.input())
	}
	tester.Frame()
	if tester.HasText("git status") {
		t.Fatalf("Escape did not dismiss the popup: %v", tester.Texts())
	}
}

// TestTypingAfterAPreviewKeepsTheUsersEdit proves the line belongs to the user
// once they edit under a preview: the preview state drops without a revert,
// and Escape after that must not drag their text back to the stale baseline.
func TestTypingAfterAPreviewKeepsTheUsersEdit(t *testing.T) {
	surface, session, transport, tester := newAutocompleteSurface(t,
		func(input, _, _ string) []autocomplete.Suggestion {
			return []autocomplete.Suggestion{
				{Text: "git status", Display: "git status", Source: "history"},
			}
		}, func() (bool, bool) { return true, false })

	typeIntoTerminal(t, tester, session, "git")
	openSuggestionPopup(t, surface, tester)
	if !surface.handleAutocompleteKey(0, ui.KeyDown) {
		t.Fatal("Down was not taken while the popup was visible")
	}
	if !waitForTransportInput(t, transport, "git status") {
		t.Fatalf("the preview never reached the transport: %q", transport.input())
	}
	if !waitFor(t, func() bool { return session.Snapshot().Input == "git status" }) {
		t.Fatalf("preview input = %q", session.Snapshot().Input)
	}

	// The user continues typing on top of the previewed text. The echo frame
	// sees the change is not the preview's own write, so the preview state
	// drops: this text is theirs now.
	tester.Type("s")
	if !waitFor(t, func() bool { return session.Snapshot().Input == "git statuss" }) {
		t.Fatalf("typed input = %q", session.Snapshot().Input)
	}
	tester.Frame()
	if surface.previewActive {
		t.Fatal("the preview state survived the user's own edit")
	}

	// Escape reopens nothing and reverts nothing: the line stays exactly what
	// the user typed. (The popup itself re-opened for the new input, so the
	// key is consumed as a dismissal — but no erase may follow it.)
	before := transport.input()
	surface.handleAutocompleteKey(0, ui.KeyEscape)
	time.Sleep(100 * time.Millisecond)
	if session.Snapshot().Input != "git statuss" {
		t.Fatalf("Escape reverted the user's edit: %q", session.Snapshot().Input)
	}
	if strings.Contains(transport.input()[len(before):], "\x7f") {
		t.Fatalf("Escape erased the user's text: %q", transport.input()[len(before):])
	}
}

// TestEnterPassesThroughThePopup proves the popup never consumes Enter: the
// key is the shell's own submit, and the line it submits already holds
// whatever the user previewed.
func TestEnterPassesThroughThePopup(t *testing.T) {
	surface, session, transport, tester := newAutocompleteSurface(t,
		func(input, _, _ string) []autocomplete.Suggestion {
			return []autocomplete.Suggestion{
				{Text: "git status", Display: "git status", Source: "history"},
			}
		}, func() (bool, bool) { return true, false })

	typeIntoTerminal(t, tester, session, "git")
	openSuggestionPopup(t, surface, tester)
	if !surface.handleAutocompleteKey(0, ui.KeyDown) {
		t.Fatal("Down was not taken while the popup was visible")
	}
	if !waitFor(t, func() bool { return session.Snapshot().Input == "git status" }) {
		t.Fatalf("preview input = %q", session.Snapshot().Input)
	}

	if surface.handleAutocompleteKey(0, ui.KeyEnter) {
		t.Fatal("Enter was consumed by the popup")
	}
	tester.Key(0, ui.KeyEnter)
	if !waitFor(t, func() bool { return session.Snapshot().Command.Running }) {
		t.Fatal("the previewed line never submitted")
	}
	if !waitForTransportInput(t, transport, "git status\r") {
		t.Fatalf("the submitted line never reached the transport: %q", transport.input())
	}
}

// TestRightArrowAcceptsTheGhostTail proves the ghost hint's acceptance: when
// the first row continues the typed line and the cursor is at its end, Right
// writes only the tail and is consumed; mid-line there is no tail to write and
// the key falls through to the shell's cursor motion.
func TestRightArrowAcceptsTheGhostTail(t *testing.T) {
	surface, session, transport, tester := newAutocompleteSurface(t,
		func(input, _, _ string) []autocomplete.Suggestion {
			if input != "git" {
				return nil
			}
			return []autocomplete.Suggestion{
				{Text: "git status", Display: "git status", Source: "history"},
			}
		}, func() (bool, bool) { return false, true })

	typeIntoTerminal(t, tester, session, "git")
	surface.refreshSuggestionsFor("git", "", false, false, true)
	tester.Frame()
	if !surface.showGhost {
		t.Fatal("the ghost hint did not appear")
	}

	if !surface.handleAutocompleteKey(0, ui.KeyRight) {
		t.Fatal("Right was not taken with the ghost hinting a completion")
	}
	if !waitForTransportInput(t, transport, "git status") {
		t.Fatalf("the ghost tail never reached the transport: %q", transport.input())
	}
	if bytes.Contains([]byte(transport.input()), []byte{0x7f}) {
		t.Fatalf("the ghost acceptance erased text it only needed to extend: %q", transport.input())
	}
	if !waitFor(t, func() bool { return session.Snapshot().Input == "git status" }) {
		t.Fatalf("input after accepting the ghost = %q", session.Snapshot().Input)
	}
}

// TestRightArrowMidLineIsTheShellsOwnKey proves a mid-line cursor leaves the
// ghost nothing to continue: Right is not consumed and no byte is written.
func TestRightArrowMidLineIsTheShellsOwnKey(t *testing.T) {
	surface, session, transport, tester := newAutocompleteSurface(t,
		func(input, _, _ string) []autocomplete.Suggestion {
			return []autocomplete.Suggestion{
				{Text: "git status", Display: "git status", Source: "history"},
			}
		}, func() (bool, bool) { return false, true })

	typeIntoTerminal(t, tester, session, "git")
	// Move the cursor between "gi" and "t".
	if !session.InsertInput([]byte("\x1b[D")) {
		t.Fatal("the left arrow was refused")
	}
	if !waitFor(t, func() bool { return session.Snapshot().InputCursor == 2 }) {
		t.Fatalf("tracked cursor = %d, want 2", session.Snapshot().InputCursor)
	}
	tester.Frame()
	surface.refreshSuggestionsFor("git", "", false, false, true)
	tester.Frame()
	if !surface.showGhost {
		t.Fatal("the ghost hint did not appear")
	}
	before := transport.input()
	// The filter declines the key outright: mid-line the hint does not
	// continue the input, so Right is the shell's cursor motion — the
	// original's guard — and the popup must not eat cursor keys. acceptGhost
	// keeps the same check as a belt-and-braces guard.
	if surface.handleAutocompleteKey(0, ui.KeyRight) {
		t.Fatal("a mid-line Right was consumed by the popup")
	}
	time.Sleep(100 * time.Millisecond)
	if transport.input() != before {
		t.Fatalf("a mid-line Right wrote bytes: %q", transport.input()[len(before):])
	}
	if session.Snapshot().Input != "git" {
		t.Fatalf("a mid-line Right changed the line: %q", session.Snapshot().Input)
	}
}

// TestClosedPopupLeavesTheArrowsToTheShell covers the key filter's first
// rule: with the list closed the arrow keys are the shell's own — history
// recall, a menu's selection — even when rows exist for the typed prefix,
// because the user never asked to see them.
func TestClosedPopupLeavesTheArrowsToTheShell(t *testing.T) {
	surface, session, _, tester := newAutocompleteSurface(t,
		func(input, _, _ string) []autocomplete.Suggestion {
			return []autocomplete.Suggestion{
				{Text: input + " status", Source: "history"},
			}
		}, func() (bool, bool) { return true, false },
		// The long debounce keeps the automatic popup closed for the whole
		// test, so the arrows are tested while rows are genuinely absent.
		WithAutocompleteDelay(func() int { return 60000 }),
	)

	typeIntoTerminal(t, tester, session, "gi")
	if tester.HasText("gi status") {
		t.Fatalf("the popup opened before the debounce: %v", tester.Texts())
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
	if surface.handleAutocompleteKey(0, ui.KeyRight) {
		t.Fatal("Right was consumed while the popup was closed")
	}
}

// TestDismissalHoldsUntilTheInputChanges proves Escape's dismissal is scoped
// to the line it dismissed: the popup stays hidden while the same text is
// edited around, and typing new text re-arms the automatic popup.
func TestDismissalHoldsUntilTheInputChanges(t *testing.T) {
	surface, session, _, tester := newAutocompleteSurface(t,
		func(input, _, _ string) []autocomplete.Suggestion {
			return []autocomplete.Suggestion{
				{Text: input + " status", Source: "history"},
			}
		}, func() (bool, bool) { return true, false })

	typeIntoTerminal(t, tester, session, "git")
	openSuggestionPopup(t, surface, tester)
	if !tester.HasText("git status") {
		t.Fatalf("the popup did not open: %v", tester.Texts())
	}
	if !surface.handleAutocompleteKey(0, ui.KeyEscape) {
		t.Fatal("Escape was not taken while the popup was visible")
	}
	tester.Frame()
	if tester.HasText("git status") {
		t.Fatalf("Escape did not dismiss the popup: %v", tester.Texts())
	}
	// The forced refresh cannot resurrect a dismissed popup for the same input.
	surface.refreshSuggestionsFor("git", "", false, false, true)
	tester.Frame()
	if tester.HasText("git status") {
		t.Fatalf("the dismissal did not hold for the same input: %v", tester.Texts())
	}

	// New text is a new question: the popup re-arms for it.
	tester.Type("u")
	if !waitFor(t, func() bool { return session.Snapshot().Input == "gitu" }) {
		t.Fatalf("typed input = %q", session.Snapshot().Input)
	}
	tester.Frame()
	openSuggestionPopup(t, surface, tester)
	if !tester.HasText("gitu status") {
		t.Fatalf("the popup did not re-arm for the new input: %v", tester.Texts())
	}
}

// TestScreenSourcedShorteningKeepsTheDismissal covers the echo-lag guard:
// once Tab has invalidated the mirror the popup's input is the rendered line,
// and echo that has not caught up reads as a strict prefix of the real line.
// Treating that lag as the user's edit would reopen the popup Tab just
// closed, so a screen-sourced shortening must not expire the dismissal —
// while a lengthening (the shell's completion landing, or typing) must.
func TestScreenSourcedShorteningKeepsTheDismissal(t *testing.T) {
	surface, session, _, tester := newAutocompleteSurface(t,
		func(input, _, _ string) []autocomplete.Suggestion {
			return []autocomplete.Suggestion{
				{Text: "echo NATCAT_CAPTURE_OK", Display: "echo NATCAT_CAPTURE_OK", Source: "history"},
			}
		}, func() (bool, bool) { return true, false })

	typeIntoTerminal(t, tester, session, "echo NATCAT_CAPTURE")
	openSuggestionPopup(t, surface, tester)
	if !tester.HasText("echo NATCAT_CAPTURE_OK") {
		t.Fatalf("the popup did not open: %v", tester.Texts())
	}
	// Tab passes through but dismisses the popup for the line as typed.
	surface.handleAutocompleteKey(0, ui.KeyTab)
	tester.Frame()
	if tester.HasText("echo NATCAT_CAPTURE_OK") {
		t.Fatalf("Tab did not step the popup aside: %v", tester.Texts())
	}

	// Echo lag: the screen-sourced input transiently reads as a strict prefix
	// of the dismissed line. Neither frame may reopen the popup, and the
	// recovery to the full prefix must be a non-event.
	surface.refreshSuggestionsFor("echo", "", false, true, true)
	tester.Frame()
	if surface.showAutocomplete {
		t.Fatal("the echo-lag collapse expired the dismissal")
	}
	surface.refreshSuggestionsFor("echo NATCAT_CAPTURE", "", false, true, true)
	tester.Frame()
	if surface.showAutocomplete {
		t.Fatal("the echo's recovery reopened the popup")
	}

	// The shell's completion landing — the line growing past the dismissed
	// text — is a new question: the dismissal expires and the rows recompute.
	surface.refreshSuggestionsFor("echo NATCAT_CAPTURE_OK", "", false, true, true)
	tester.Frame()
	if surface.suggestionDismissed {
		t.Fatal("a lengthening of the line did not expire the dismissal")
	}
}

// TestMirrorSourcedShorteningExpiresTheDismissal is the mirror-reliable
// counterpart: while the keystroke mirror answers, every change is a real
// keystroke, so an erasure expires the dismissal exactly like typing does.
func TestMirrorSourcedShorteningExpiresTheDismissal(t *testing.T) {
	surface, session, _, tester := newAutocompleteSurface(t,
		func(input, _, _ string) []autocomplete.Suggestion {
			return []autocomplete.Suggestion{
				{Text: input + "x", Source: "history"},
			}
		}, func() (bool, bool) { return true, false })

	typeIntoTerminal(t, tester, session, "git")
	openSuggestionPopup(t, surface, tester)
	if !surface.handleAutocompleteKey(0, ui.KeyEscape) {
		t.Fatal("Escape was not taken while the popup was visible")
	}
	tester.Frame()
	if !surface.suggestionDismissed {
		t.Fatal("Escape did not dismiss")
	}

	// screenSourced=false — the mirror is the witness — so even a pure
	// shortening is the user's own erasure, and the dismissal expires.
	surface.refreshSuggestionsFor("gi", "", false, false, true)
	if surface.suggestionDismissed {
		t.Fatal("a mirror-sourced erasure did not expire the dismissal")
	}
}

// TestSurfaceAutocompleteRefusesMultilineSuggestionWithoutBracketedPaste
// proves the live-preview path uses the safe insertion seam: without DEC 2004
// a multi-line candidate cannot be typed into the editor, so nothing is sent
// and the typed prefix stays. Navigation and popup-click share the seam.
func TestSurfaceAutocompleteRefusesMultilineSuggestionWithoutBracketedPaste(t *testing.T) {
	const command = "git status\ntouch /tmp/never-run"
	surface, session, transport, tester := newAutocompleteSurface(t,
		func(input, _, _ string) []autocomplete.Suggestion {
			if input == "gi" {
				return []autocomplete.Suggestion{{
					Text: command, Display: "git status (multiline)", Source: "history",
				}}
			}
			return nil
		}, func() (bool, bool) { return true, false })

	typeIntoTerminal(t, tester, session, "gi")
	openSuggestionPopup(t, surface, tester)
	if len(surface.SuggestionRows()) != 1 {
		t.Fatalf("popup rows = %+v, want the multiline suggestion", surface.SuggestionRows())
	}
	before := transport.input()

	if !surface.handleAutocompleteKey(0, ui.KeyDown) {
		t.Fatal("Down was not taken while the popup was visible")
	}
	time.Sleep(100 * time.Millisecond)
	if got := transport.input(); got != before {
		t.Fatalf("the refused preview sent unsafe bytes: %q", got)
	}
	if session.Snapshot().Input != "gi" {
		t.Fatalf("the refused preview changed the tracked line to %q", session.Snapshot().Input)
	}

	// The popup stays available after the refusal, and the click path uses the
	// same seam and is refused the same way.
	tester.Frame()
	if err := tester.Click("git status (multiline)"); err != nil {
		t.Fatalf("click the multiline suggestion: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
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
// shell enabled DEC 2004: the preview arrives as one paste and no newline is
// submitted.
func TestSurfaceAutocompleteInsertsMultilineSuggestionAsBracketedPaste(t *testing.T) {
	const command = "git status\ntouch marker"
	surface, session, transport, tester := newAutocompleteSurface(t,
		func(input, _, _ string) []autocomplete.Suggestion {
			if input == "gi" {
				return []autocomplete.Suggestion{{
					Text: command, Display: "git status (multiline)", Source: "history",
				}}
			}
			return nil
		}, func() (bool, bool) { return true, false })
	surface.terminal.Feed([]byte("\x1b[?2004h"))
	if !surface.BracketedPasteEnabled() {
		t.Fatal("bracketed paste mode was not read from the terminal")
	}

	typeIntoTerminal(t, tester, session, "gi")
	openSuggestionPopup(t, surface, tester)
	// The candidate extends the typed prefix, so the preview write is the tail
	// alone — paste-wrapped, because the tail itself carries the newline.
	tail := command[len("gi"):]
	want := "\x1b[200~" + tail + "\x1b[201~"
	if !surface.handleAutocompleteKey(0, ui.KeyDown) {
		t.Fatal("Down was not taken while the popup was visible")
	}
	if !waitForTransportInput(t, transport, want) {
		t.Fatalf("previewed input = %q, want the paste-wrapped tail %q", transport.input(), want)
	}
	if bytes.HasSuffix([]byte(transport.input()), []byte("\n")) {
		t.Fatalf("the preview submitted the line: %q", transport.input())
	}
	if !waitFor(t, func() bool { return session.Snapshot().Input == command }) {
		t.Fatalf("the shell line tracked after previewing = %q", session.Snapshot().Input)
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

// TestSurfaceAutocompleteHonorsTheDebounceAndRunningCommand covers the two
// policies that keep autocomplete out of the way: a configured delay before
// the rows appear, and a command that is still running (vim, less) where every
// key — Tab included — must reach the program.
func TestSurfaceAutocompleteHonorsTheDebounceAndRunningCommand(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	surface, session, _, tester := newAutocompleteSurface(t,
		func(input, _, _ string) []autocomplete.Suggestion {
			mu.Lock()
			calls++
			mu.Unlock()
			return []autocomplete.Suggestion{{Text: input + " status", Source: "history"}}
		}, func() (bool, bool) { return true, false },
		// The pre-delay assertion below samples immediately after typing, so
		// leave enough headroom that a slow frame cannot cross the deadline.
		WithAutocompleteDelay(func() int { return 500 }),
	)

	typeIntoTerminal(t, tester, session, "gi")
	if tester.HasText("gi status") {
		t.Fatalf("suggestions appeared before the delay: %v", tester.Texts())
	}
	// Once the delay passes the automatic popup opens on its own — no key is
	// ever needed, because accepting is an arrow, not a Tab.
	if !waitFor(t, func() bool {
		tester.Frame()
		return len(surface.SuggestionRows()) > 0 && surface.showAutocomplete
	}) {
		t.Fatal("the popup never opened after the delay")
	}
	if !tester.HasText("gi status") {
		t.Fatalf("the popup did not appear after the delay: %v", tester.Texts())
	}

	// Submit a command: while it runs, suggestions close and no autocomplete
	// key is consumed, so everything reaches the running program.
	if _, err := session.TerminalConn().Write([]byte("less\r")); err != nil {
		t.Fatal(err)
	}
	if !waitFor(t, func() bool { return session.Snapshot().Command.Running }) {
		t.Fatalf("the command did not start: %+v", session.Snapshot().Command)
	}
	tester.Frame()
	if tester.HasText("gi status") {
		t.Fatalf("suggestions stayed visible while a command was running: %v", tester.Texts())
	}
	for _, key := range []ui.Key{ui.KeyTab, ui.KeyUp, ui.KeyDown, ui.KeyEscape, ui.KeyRight} {
		if surface.handleAutocompleteKey(0, key) {
			t.Fatalf("key %v was consumed while a command was running", key)
		}
	}
	mu.Lock()
	afterRunning := calls
	mu.Unlock()
	if afterRunning == 0 {
		t.Fatal("the provider was never called")
	}
}

// TestArrowsInsideTheDebounceWindowSteerTheRows is the regression for fast
// navigation: the debounce gates the passive layers, not the user's steering.
// An arrow pressed before the delay elapsed must still move the selection —
// the key path skips the delay — or it falls through to the shell as a
// history recall the user never meant.
func TestArrowsInsideTheDebounceWindowSteerTheRows(t *testing.T) {
	surface, session, transport, tester := newAutocompleteSurface(t,
		func(input, _, _ string) []autocomplete.Suggestion {
			return []autocomplete.Suggestion{{Text: input + " status", Source: "history"}}
		}, func() (bool, bool) { return true, false },
		// Long enough that the test cannot accidentally cross the window.
		WithAutocompleteDelay(func() int { return 60000 }),
	)

	typeIntoTerminal(t, tester, session, "gi")
	if tester.HasText("gi status") {
		t.Fatalf("the popup appeared before the delay: %v", tester.Texts())
	}

	// The rows do not exist yet inside the window, so the first arrows are the
	// shell's own.
	if surface.handleAutocompleteKey(0, ui.KeyDown) {
		t.Fatal("Down was consumed before any rows existed")
	}
	// skipDelay computes the rows, but the window still gates the passive
	// layers: the list stays hidden until the timer fires, because rows and
	// ghost that tracked every keystroke would flicker with the echo. The
	// steering guarantee the regression guards is the visible one — a live
	// preview is outside the debounce, so once the user is steering, the list
	// cannot close under their arrows. Rows that never showed were never the
	// user's to steer.
	surface.refreshSuggestionsFor("gi", "", false, false, true)
	tester.Frame()
	if len(surface.SuggestionRows()) != 1 {
		t.Fatalf("skipDelay did not compute the rows: %+v", surface.SuggestionRows())
	}
	if surface.showAutocomplete {
		t.Fatal("the debounce window did not gate the passive popup")
	}
	if surface.handleAutocompleteKey(0, ui.KeyDown) {
		t.Fatal("Down was consumed for rows the user cannot see")
	}

	// The frame inside the window with a live preview active is the other
	// half of the regression: steering must not be answered with the shell's
	// history keys because the last keystroke is still inside the window.
	// Simulate the state the debounce timer produces at the window's end, then
	// start the preview and prove a frame inside the window keeps the list.
	surface.suggestionPending = time.Now().Add(-time.Minute)
	surface.refreshSuggestionsFor("gi", "", false, false, false)
	tester.Frame()
	if !surface.showAutocomplete {
		t.Fatal("the popup did not open once the debounce passed")
	}
	if !surface.handleAutocompleteKey(0, ui.KeyDown) {
		t.Fatal("Down was not taken with the popup open")
	}
	if surface.suggestionIndex != 0 {
		t.Fatalf("suggestion index = %d, want 0", surface.suggestionIndex)
	}
	if !waitForTransportInput(t, transport, "gi status") {
		t.Fatalf("the preview never reached the transport: %q", transport.input())
	}
	if bytes.Contains([]byte(transport.input()), []byte("\t")) {
		t.Fatalf("a literal tab reached the shell: %q", transport.input())
	}
	if !waitFor(t, func() bool { return session.Snapshot().Input == "gi status" }) {
		t.Fatalf("preview input = %q", session.Snapshot().Input)
	}
	// Now the active half of the regression: rewind the clock into the window
	// and run a frame while the preview is live. The debounce must not close
	// the rows under the user's arrows — the preview is outside the passive
	// gate, or steering would be answered with the shell's history keys.
	surface.suggestionPending = time.Now()
	surface.previewActive = true
	surface.refreshSuggestionsFor("gi status", "", false, false, false)
	tester.Frame()
	if !surface.showAutocomplete {
		t.Fatal("the debounce window closed the rows under a live preview")
	}
}

// TestSurfaceAutocompleteDoesNotResizeTheTerminal is the regression this fix
// exists for: the popup used to be a chrome row above the terminal, so showing
// it shrank the terminal grid and the Session resized the transport, making the
// shell repaint the whole screen on each keystroke. The floating layers must
// leave the terminal's box untouched.
func TestSurfaceAutocompleteDoesNotResizeTheTerminal(t *testing.T) {
	surface, session, _, tester := newAutocompleteSurface(t,
		func(input, _, _ string) []autocomplete.Suggestion {
			return []autocomplete.Suggestion{
				{Text: input + " status", Source: "history"},
				{Text: input + " stash", Source: "history"},
			}
		}, func() (bool, bool) { return true, false })

	before, ok := tester.Find("Terminal")
	if !ok {
		t.Fatalf("the terminal is not in the frame: %v", tester.Texts())
	}

	typeIntoTerminal(t, tester, session, "gi")
	tester.Frame()
	openSuggestionPopup(t, surface, tester)
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
	surface, session, _, tester := newAutocompleteSurface(t,
		func(input, _, _ string) []autocomplete.Suggestion {
			return []autocomplete.Suggestion{
				{Text: input + " status", Source: "history"},
				{Text: input + " stash", Source: "history"},
			}
		}, func() (bool, bool) { return true, false })

	// A shell keeps the prompt on the last visible row after output scrolls.
	surface.terminal.Feed([]byte(strings.Repeat("output\r\n", 40) + "$ gi"))
	tester.Frame()
	typeIntoTerminal(t, tester, session, "gi")
	tester.Frame()
	openSuggestionPopup(t, surface, tester)
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
	description := locales.In("en").CommandDescriptionLs()
	surface, session, _, tester := newAutocompleteSurface(t,
		func(input, _, _ string) []autocomplete.Suggestion {
			return []autocomplete.Suggestion{
				{Text: input + " -la", Description: description, Source: "history"},
			}
		}, func() (bool, bool) { return true, false })

	typeIntoTerminal(t, tester, session, "ls")
	tester.Frame()
	openSuggestionPopup(t, surface, tester)
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

// waitForScriptedInput is waitForTransportInput for the scripted transport the
// program-prompt tests share.
func waitForScriptedInput(t *testing.T, transport *scriptedTransport, needle string) bool {
	t.Helper()
	return waitFor(t, func() bool { return strings.Contains(transport.Input(), needle) })
}

// TestAutocompleteOffersSnippetsAtProgramPrompt proves a Vault Snippet joins
// the popup while a program's read prompt is waiting, and steering onto it
// live-previews its command into the line editor at the cursor.
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
	openSuggestionPopup(t, surface, tester)
	tester.Frame()
	if !tester.HasText("Work email") {
		t.Fatalf("the popup does not show the snippet label: %v", tester.Texts())
	}

	// At a program input prompt the typed text is a query, so accepting
	// replaces only the current word — but at the end of the line this
	// candidate continues the query, so the preview is the tail alone.
	if !surface.handleAutocompleteKey(0, ui.KeyDown) {
		t.Fatal("Down was not taken while the popup was visible")
	}
	if !waitForScriptedInput(t, transport, "work@example.com") {
		t.Fatalf("the snippet preview never reached the transport: %q", transport.Input())
	}
	if strings.Contains(transport.Input(), "\x15") {
		t.Fatalf("Ctrl+U reached the transport: %q", transport.Input())
	}
	if !waitFor(t, func() bool { return session.Snapshot().Input == "work@example.com" }) {
		t.Fatalf("the line tracked after previewing the snippet = %q", session.Snapshot().Input)
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
	openSuggestionPopup(t, surface, tester)
	before := strings.Count(transport.Input(), "\x7f")

	// Mid-line there is no prefix shortcut: steering onto the row erases the
	// two characters left of the cursor and writes the candidate whole, and
	// the "r" right of the cursor is never touched.
	if !surface.handleAutocompleteKey(0, ui.KeyDown) {
		t.Fatal("Down was not taken while the popup was visible")
	}
	if !waitFor(t, func() bool {
		return strings.Count(transport.Input(), "\x7f") == before+2
	}) {
		t.Fatalf("the preview erased %d characters, want only the 2 left of the cursor: %q",
			strings.Count(transport.Input(), "\x7f")-before, transport.Input())
	}
	if !waitForScriptedInput(t, transport, "\x7f\x7fwork@example.com") {
		t.Fatalf("the preview bytes = %q, want two erases plus the candidate", transport.Input())
	}
	if !waitFor(t, func() bool { return session.Snapshot().Input == "work@example.comr" }) {
		t.Fatalf("the line after the mid-line preview = %q, want the suffix kept",
			session.Snapshot().Input)
	}
}

// TestEscapeRevertsAPreviewAfterFramesIntervene is the regression guard for
// the review's reopen defect: real usage renders frames while the preview is
// live, so the rows' input tracking has already followed the preview write
// when Escape's revert lands. The revert is the surface's own write exactly
// like the preview was: it must not read as the user's edit, or the popup
// would recompute rows for the baseline and reopen the list Escape just
// closed.
func TestEscapeRevertsAPreviewAfterFramesIntervene(t *testing.T) {
	surface, session, _, tester := newAutocompleteSurface(t,
		func(input, _, _ string) []autocomplete.Suggestion {
			return []autocomplete.Suggestion{
				{Text: "git status", Display: "git status", Source: "history"},
			}
		}, func() (bool, bool) { return true, false })

	typeIntoTerminal(t, tester, session, "git")
	openSuggestionPopup(t, surface, tester)
	if !surface.handleAutocompleteKey(0, ui.KeyDown) {
		t.Fatal("Down was not taken while the popup was visible")
	}
	if !waitFor(t, func() bool { return session.Snapshot().Input == "git status" }) {
		t.Fatalf("preview input = %q", session.Snapshot().Input)
	}
	// The frame is the difference from the simpler Escape test: it lets the
	// refresh loop observe the preview write before the revert begins, exactly
	// as real rendering does between the keystroke and the Escape.
	tester.Frame()
	tester.Frame()

	if !surface.handleAutocompleteKey(0, ui.KeyEscape) {
		t.Fatal("Escape was not taken while the popup was visible")
	}
	if !waitFor(t, func() bool { return session.Snapshot().Input == "git" }) {
		t.Fatalf("Escape left the previewed line in place: %q", session.Snapshot().Input)
	}
	// The revert write lands in the mirror a frame later. The popup must stay
	// closed through it: the write is the surface's own, not the user's.
	tester.Frame()
	tester.Frame()
	if surface.showAutocomplete {
		t.Fatalf("the popup reopened after the revert landed: rows %+v", surface.SuggestionRows())
	}
	if tester.HasText("git status") {
		t.Fatalf("the popup reopened after the revert landed: %v", tester.Texts())
	}
}

// TestEscapeRevertsAMidLinePreviewExactly is the regression guard for the
// review's mid-line revert defect: the baseline is restored whole, suffix and
// cursor position included, because the revert erases exactly what the preview
// chain put left of the cursor and writes back the baseline's head — never a
// re-derived acceptance erasure.
func TestEscapeRevertsAMidLinePreviewExactly(t *testing.T) {
	surface, session, _, tester := startProgramPromptSession(t, "Email: ",
		WithSuggestions(func(input, scope, cwd string) []autocomplete.Suggestion {
			return nil
		}, func() (bool, bool) { return true, false }),
		WithPromptSuggestions(func(input, scope, cwd string, program bool) []autocomplete.Suggestion {
			return []autocomplete.Suggestion{{
				Text: "worked example", Display: "worked example", Source: autocomplete.SourceSnippet,
			}}
		}),
	)
	tester.Type("wor")
	if !waitFor(t, func() bool { return session.Snapshot().Input == "wor" }) {
		t.Fatalf("typed input = %q", session.Snapshot().Input)
	}
	// Cursor between "wo" and "r": the preview writes candidate+suffix.
	if !session.InsertInput([]byte("\x1b[D")) {
		t.Fatal("the left arrow was refused")
	}
	if !waitFor(t, func() bool { return session.Snapshot().InputCursor == 2 }) {
		t.Fatalf("tracked cursor = %d, want 2", session.Snapshot().InputCursor)
	}
	tester.Frame()
	openSuggestionPopup(t, surface, tester)

	if !surface.handleAutocompleteKey(0, ui.KeyDown) {
		t.Fatal("Down was not taken while the popup was visible")
	}
	if !waitFor(t, func() bool { return session.Snapshot().Input == "worked exampler" }) {
		t.Fatalf("the mid-line preview left %q, want candidate+suffix", session.Snapshot().Input)
	}
	// Frames intervene, as in real rendering: the recognition of the preview's
	// own write must be what keeps the preview state alive, or Escape has
	// nothing to revert.
	tester.Frame()
	tester.Frame()
	if !surface.previewActive {
		t.Fatal("the preview state self-destructed on the frame after the write")
	}

	if !surface.handleAutocompleteKey(0, ui.KeyEscape) {
		t.Fatal("Escape was not taken while the popup was visible")
	}
	if !waitFor(t, func() bool { return session.Snapshot().Input == "wor" }) {
		t.Fatalf("Escape left the line as %q, want the typed baseline whole — suffix included",
			session.Snapshot().Input)
	}
	if !waitFor(t, func() bool { return session.Snapshot().InputCursor == 2 }) {
		t.Fatalf("the cursor after the revert = %d, want the baseline's 2",
			session.Snapshot().InputCursor)
	}
}

// TestEscapeRevertsAMultiWordCandidateAtAProgramPrompt is the regression
// guard for the program-prompt revert defect: acceptance erases only the
// query word, but the revert must undo what the preview actually wrote — the
// whole candidate, spaces included. Re-deriving the acceptance erasure at
// revert time would erase only the candidate's last word and leave the rest
// merged into the baseline.
func TestEscapeRevertsAMultiWordCandidateAtAProgramPrompt(t *testing.T) {
	surface, session, _, tester := startProgramPromptSession(t, "Email: ",
		WithSuggestions(func(input, scope, cwd string) []autocomplete.Suggestion {
			return nil
		}, func() (bool, bool) { return true, false }),
		WithPromptSuggestions(func(input, scope, cwd string, program bool) []autocomplete.Suggestion {
			return []autocomplete.Suggestion{{
				Text: "git commit --amend", Display: "git commit --amend", Source: autocomplete.SourceSnippet,
			}}
		}),
	)
	tester.Type("wo")
	if !waitFor(t, func() bool { return session.Snapshot().Input == "wo" }) {
		t.Fatalf("typed input = %q", session.Snapshot().Input)
	}
	tester.Frame()
	openSuggestionPopup(t, surface, tester)

	if !surface.handleAutocompleteKey(0, ui.KeyDown) {
		t.Fatal("Down was not taken while the popup was visible")
	}
	if !waitFor(t, func() bool { return session.Snapshot().Input == "git commit --amend" }) {
		t.Fatalf("the preview left %q, want the multi-word candidate", session.Snapshot().Input)
	}
	tester.Frame()

	if !surface.handleAutocompleteKey(0, ui.KeyEscape) {
		t.Fatal("Escape was not taken while the popup was visible")
	}
	if !waitFor(t, func() bool { return session.Snapshot().Input == "wo" }) {
		t.Fatalf("Escape left the line as %q, want the typed query whole — a word-boundary revert leaves candidate residue",
			session.Snapshot().Input)
	}
}

// TestUnwritableRowKeepsThePreviousPreviewRevertable is the regression guard
// for the refused-write defect: steering onto a row the insertion seam
// refuses strands nothing — the line keeps the last successful preview, the
// bookkeeping stays valid for it, and the no-selection slot still restores
// the typed baseline exactly.
func TestUnwritableRowKeepsThePreviousPreviewRevertable(t *testing.T) {
	surface, session, _, tester := newAutocompleteSurface(t,
		func(input, _, _ string) []autocomplete.Suggestion {
			return []autocomplete.Suggestion{
				{Text: "git status", Display: "git status", Source: "history"},
				{Text: "git log\ntouch /tmp/x", Display: "two lines", Source: "history"},
			}
		}, func() (bool, bool) { return true, false })

	typeIntoTerminal(t, tester, session, "git")
	openSuggestionPopup(t, surface, tester)

	// Row 0 previews fine.
	if !surface.handleAutocompleteKey(0, ui.KeyDown) {
		t.Fatal("Down was not taken while the popup was visible")
	}
	if !waitFor(t, func() bool { return session.Snapshot().Input == "git status" }) {
		t.Fatalf("preview input = %q", session.Snapshot().Input)
	}

	// Row 1 is multi-line and the fake shell never enabled bracketed paste:
	// the write is refused. The selection lands on the row, but the line —
	// and the preview bookkeeping — still belong to row 0's write.
	if !surface.handleAutocompleteKey(0, ui.KeyDown) {
		t.Fatal("the second Down was not taken")
	}
	if surface.suggestionIndex != 1 {
		t.Fatalf("the selection did not land on the refused row: %d", surface.suggestionIndex)
	}
	if !surface.previewActive {
		t.Fatal("the refused write destroyed the live preview state")
	}
	time.Sleep(100 * time.Millisecond)
	if got := session.Snapshot().Input; got != "git status" {
		t.Fatalf("the refused write changed the line to %q, want the previous preview kept", got)
	}

	// Stepping off the end restores the typed baseline exactly: the line was
	// never stranded on a candidate.
	if !surface.handleAutocompleteKey(0, ui.KeyDown) {
		t.Fatal("the wrapping Down was not taken")
	}
	if surface.suggestionIndex != noSelection {
		t.Fatalf("the selection did not wrap to noSelection: %d", surface.suggestionIndex)
	}
	if !waitFor(t, func() bool { return session.Snapshot().Input == "git" }) {
		t.Fatalf("the line after cycling closed = %q, want the typed baseline", session.Snapshot().Input)
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
	openSuggestionPopup(t, surface, tester)
	if len(surface.SuggestionRows()) != 1 {
		t.Fatalf("popup rows = %+v, want the multiline snippet", surface.SuggestionRows())
	}
	before := transport.Input()
	if !surface.handleAutocompleteKey(0, ui.KeyDown) {
		t.Fatal("Down was not taken while the popup was visible")
	}
	time.Sleep(100 * time.Millisecond)
	if got := transport.Input(); got != before {
		t.Fatalf("the refused preview sent unsafe bytes: %q", got)
	}
	if session.Snapshot().Input != "wo" {
		t.Fatalf("the refused preview changed the tracked line to %q", session.Snapshot().Input)
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
