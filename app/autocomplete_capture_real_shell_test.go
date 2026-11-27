//go:build darwin || linux

package app

// Regressions for the reported completion flow. The original bug was a local
// zsh Session whose planned capture hook was replaced by the Lens OSC 133
// integration, so the tap kept reserving every typed command for a handshake
// that never came: the command reached history only when the Session closed,
// the autocomplete service stayed empty while the user typed, and pressing Tab
// found nothing to offer. The capture pipeline now publishes the command while
// the Session stays open, and the keymap is the original's: the popup opens on
// its own once the debounce passes, arrowing onto a row live-renders the
// candidate into the shell's real line, Enter — never intercepted — runs it,
// and Tab always belongs to the shell's own completion.
//
// These tests open the same local shell through the production adapters and
// capture hook, wire the production completion hook into a real
// autocomplete.Service, and drive the Surface keys: type a command, run it,
// type its prefix, steer the popup — all while the Session is still open, not
// after a closer that these tests never perform.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/adapters/localpty"
	"github.com/mslxl/natcatty/adapters/shellhook"
	"github.com/mslxl/natcatty/application/autocomplete"
	appsession "github.com/mslxl/natcatty/application/session"
	domainhistory "github.com/mslxl/natcatty/domain/history"
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
	sessionui "github.com/mslxl/natcatty/ui/session"
)

const (
	captureCommand = "echo NATCAT_CAPTURE_OK"
	capturePrefix  = "echo NATCAT_CAPTURE"
	captureMarker  = "NATCAT_CAPTURE_OK"
)

// captureJournal is the autocomplete journal port in memory. The tests never
// reload it; Record is what the production service aggregates from.
type captureJournal struct {
	mu      sync.Mutex
	entries []domainhistory.Entry
}

func (j *captureJournal) Load(context.Context) ([]domainhistory.Entry, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]domainhistory.Entry(nil), j.entries...), nil
}

func (j *captureJournal) Record(_ context.Context, entry domainhistory.Entry) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.entries = append(j.entries, entry)
	return nil
}

// countCommand is how many journal rows hold exactly this command text.
func (j *captureJournal) countCommand(command string) int {
	j.mu.Lock()
	defer j.mu.Unlock()
	n := 0
	for _, entry := range j.entries {
		if entry.Command == command {
			n++
		}
	}
	return n
}

// commands is the journal's command texts, for diagnostics.
func (j *captureJournal) commands() []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]string, len(j.entries))
	for i, entry := range j.entries {
		out[i] = entry.Command
	}
	return out
}

// captureShell is a real zsh Session wired to the production capture hook, the
// production completion hook and a real autocomplete service, rendered through
// the Surface.
type captureShell struct {
	session *appsession.Session
	surface *sessionui.Surface
	tester  *ui.Tester
	service *autocomplete.Service
	journal *captureJournal
}

// openCaptureShell starts a zsh Session and focuses the terminal. delayMs is
// the Surface's suggestion debounce: the popup waits this long after typing,
// while a key that acts on the rows must not.
func openCaptureShell(t *testing.T, delayMs int) *captureShell {
	t.Helper()
	return openCaptureShellNamed(t, "zsh", delayMs)
}

// openCaptureShellNamed is openCaptureShell for a chosen shell: the reported
// literal tab came from a bash Session, and bash captures the typed line from
// its own history rather than from zle.
func openCaptureShellNamed(t *testing.T, name string, delayMs int) *captureShell {
	t.Helper()
	shellPath, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s is not installed here", name)
	}
	home := t.TempDir()
	shareUserCache(t, home)
	// The Lens integration reads the user's own startup file from $HOME: zsh
	// through a replaced ZDOTDIR, bash by sourcing .bashrc from its rcfile.
	startup := map[string][2]string{
		"zsh": {".zshrc", "setopt prompt_subst\n" +
			"PROMPT_EOL_MARK=''\n" +
			"unsetopt correct\n" +
			"HISTFILE=/dev/null\n" +
			"SAVEHIST=0\n" +
			"PS1='%% '\n"},
		"bash": {".bashrc", "HISTFILE=/dev/null\n" +
			"PS1='PROMPT> '\n"},
	}[name]
	if startup[0] == "" {
		t.Fatalf("no startup file for %q", name)
	}
	if err := os.WriteFile(filepath.Join(home, startup[0]), []byte(startup[1]), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("ZDOTDIR", "")
	t.Setenv("NETCATTY_USER_ZDOTDIR", "")
	t.Setenv("NETCATTY_HOOK", "")
	t.Setenv("SHELL", shellPath)
	t.Setenv("PS1", "")
	t.Setenv("PROMPT_EOL_MARK", "")
	t.Setenv("PROMPT_COMMAND", "")
	// Keep other integrations out of the startup so the only OSC 133 marks
	// come from the Lens integration, exactly like the reported Session.
	t.Setenv("STARSHIP_SHELL", name)
	t.Setenv("STARSHIP_SESSION_KEY", "")
	t.Setenv("ATUIN_SESSION", "")
	t.Setenv("ATUIN_SHLVL", "")
	t.Setenv("ITERM_SHELL_INTEGRATION_INSTALLED", "")

	journal := &captureJournal{}
	autocompleteService := autocomplete.NewService(journal, func() autocomplete.Config {
		return autocomplete.Config{
			Enabled:        true,
			MinChars:       1,
			MaxSuggestions: 8,
			DebounceMs:     60,
			// The production default: only this Session's scope suggests.
			Scope: domainterminal.AutocompleteHistoryHost,
		}
	})
	service := appsession.NewService(localpty.Factory{},
		appsession.WithCaptureHook(shellhook.NewPlanner(
			shellhook.WithEnabled(func() bool { return true }),
		)),
		appsession.WithCommandCompletionHook(
			commandCompletionHook(autocompleteService, commandLocalIdentity{Host: "tabcapture", User: "testuser"}, nil)),
	)
	t.Cleanup(service.CloseAll)

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	session, err := service.Open(ctx, appsession.StartSpec{
		Kind: appsession.KindLocal,
		Dir:  realShellDir(t),
		Cols: 100,
		Rows: 30,
	})
	if err != nil {
		t.Fatalf("open the real shell: %v", err)
	}
	// Nothing closes the Session before the assertions: the whole point is
	// that Tab works while it stays open.
	t.Cleanup(func() { _ = session.Close() })

	surface, err := sessionui.New(session,
		sessionui.WithRedraw(func() {}),
		sessionui.WithAutocompleteDelay(func() int { return delayMs }),
		sessionui.WithSuggestions(func(input, scope, cwd string) []autocomplete.Suggestion {
			return autocompleteService.SuggestionsInWith(input, scope, cwd, nil)
		}, func() (bool, bool) { return true, false }),
	)
	if err != nil {
		t.Fatalf("build the Session surface: %v", err)
	}
	t.Cleanup(surface.Close)

	tester := ui.NewTester(func(c *ui.Context) { surface.View(c) }, 900, 620)
	tester.SetLocale("en")
	tester.Frame()
	if !realShellStartWait(t, func() bool { return strings.TrimSpace(surface.ScreenText()) != "" }) {
		t.Fatal("the real shell produced no output")
	}
	// Focus the terminal so the Pane takes the production key path.
	tester.ClickAt(450, 560)

	return &captureShell{session: session, surface: surface, tester: tester, service: autocompleteService, journal: journal}
}

// runCaptureCommand types the command, runs it and waits until the capture is
// published to the autocomplete service, while the Session stays open. That
// publication was the regression: the suggestion exists only if the hook and
// the journal did their job.
func (s *captureShell) runCaptureCommand(t *testing.T) {
	t.Helper()
	s.tester.Type(captureCommand)
	if !realShellWait(t, func() bool { return s.session.Snapshot().Input == captureCommand }) {
		t.Fatalf("the typed line is %q, want %q", s.session.Snapshot().Input, captureCommand)
	}
	s.tester.Key(0, ui.KeyEnter)
	if !realShellWait(t, func() bool {
		return commandRanOnScreen(s.surface.ScreenText(), captureCommand, captureMarker) &&
			!s.session.Snapshot().Command.Running
	}) {
		t.Fatalf("the shell did not run the command: screen %q command %+v",
			s.surface.ScreenText(), s.session.Snapshot().Command)
	}
	if !realShellWait(t, func() bool {
		return len(s.service.Suggestions(capturePrefix, s.session.HistoryScope())) > 0
	}) {
		t.Fatalf("the finished command never reached autocomplete while the Session was open; screen %q",
			s.surface.ScreenText())
	}
}

// commandTextCount is how many times the captured command appears in the
// rendered text: once in its block header, once more for a popup row.
func (s *captureShell) commandTextCount() int {
	return strings.Count(strings.Join(s.tester.Texts(), "\n"), captureCommand)
}

// waitForCommandHeader reads the baseline count after the finished command's
// header has been drawn. Measuring before it appears made the later frame look
// like a popup row: the header, not a suggestion, was the extra occurrence.
func (s *captureShell) waitForCommandHeader(t *testing.T) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s.commandTextCount() > 0 {
			return s.commandTextCount()
		}
		// The frame is what turns a finished block into tester.Texts(); the
		// redraw hook is a no-op, so the wait must pump frames itself.
		s.tester.Frame()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the finished command never reached the rendered text: %v", s.tester.Texts())
	return 0
}

// runAcceptedLine proves the shell received the completed line by running it a
// second time; the marker-output count is the count of executions.
func (s *captureShell) runAcceptedLine(t *testing.T, markerCount int) {
	t.Helper()
	s.tester.Key(0, ui.KeyEnter)
	if !realShellWait(t, func() bool {
		return markerOutputRuns(s.surface.ScreenText()) >= markerCount &&
			!s.session.Snapshot().Command.Running
	}) {
		t.Fatalf("the accepted command did not run: screen %q command %+v",
			s.surface.ScreenText(), s.session.Snapshot().Command)
	}
}

// markerOutputRuns counts executions by their output lines: a line that is the
// bare marker. A substring count cannot do the job — the echoed command also
// contains the marker text, and whether the echo of the line being typed has
// landed yet is a race the test must not depend on. The output row is the one
// line that holds the marker alone.
func markerOutputRuns(screen string) int {
	runs := 0
	for line := range strings.Lines(screen) {
		if strings.TrimRight(strings.TrimRight(line, "\n"), " ") == captureMarker {
			runs++
		}
	}
	return runs
}

// TestPopupPreviewAcceptsACapturedCommandWhileTheSessionIsOpen is the
// end-to-end guard for accepting a journal row in a real shell: the popup
// opens on its own once the debounce passes, arrowing into the row live-
// renders the candidate into the real command line, and Enter — never
// intercepted — runs it.
func TestPopupPreviewAcceptsACapturedCommandWhileTheSessionIsOpen(t *testing.T) {
	shell := openCaptureShell(t, 0)
	shell.runCaptureCommand(t)

	beforePopup := shell.waitForCommandHeader(t)
	shell.tester.Type(capturePrefix)
	if !realShellWait(t, func() bool { return shell.session.Snapshot().Input == capturePrefix }) {
		t.Fatalf("the typed line is %q, want %q", shell.session.Snapshot().Input, capturePrefix)
	}
	// The popup is automatic: once the debounce passes the row appears without
	// any key — one more occurrence of the command's text than the header.
	if !realShellWait(t, func() bool { return shell.commandTextCount() > beforePopup }) {
		pumpTUIFrames(shell.tester, 100*time.Millisecond)
		if !realShellWait(t, func() bool { return shell.commandTextCount() > beforePopup }) {
			t.Fatalf("the popup never opened after the debounce (%d occurrences): %v",
				shell.commandTextCount(), shell.tester.Texts())
		}
	}

	// Arrow down onto the row: the live preview writes the candidate into the
	// shell's real line — no acceptance key, no submit, the line simply becomes
	// the command.
	shell.tester.Key(0, ui.KeyDown)
	if !realShellWait(t, func() bool { return shell.session.Snapshot().Input == captureCommand }) {
		t.Fatalf("the live preview left the line as %q, want %q",
			shell.session.Snapshot().Input, captureCommand)
	}

	before := markerOutputRuns(shell.surface.ScreenText())
	shell.runAcceptedLine(t, before+1)
}

// TestTabPassesThroughToTheShellInARealShell guards the original's #1005
// parity: natcatty's popup never intercepts Tab. With the popup open, Tab
// closes the list and the byte reaches the shell — whose own completion, not
// natcatty's rows, answers it. zsh cannot complete the capture prefix (no such
// command or file), so the line keeps the typed text: the proof the byte went
// to the shell is that the popup closed and nothing was inserted from the
// rows.
func TestTabPassesThroughToTheShellInARealShell(t *testing.T) {
	shell := openCaptureShell(t, 0)
	shell.runCaptureCommand(t)

	beforePopup := shell.waitForCommandHeader(t)
	shell.tester.Type(capturePrefix)
	if !realShellWait(t, func() bool { return shell.session.Snapshot().Input == capturePrefix }) {
		t.Fatalf("the typed line is %q, want %q", shell.session.Snapshot().Input, capturePrefix)
	}
	if !realShellWait(t, func() bool { return shell.commandTextCount() > beforePopup }) {
		pumpTUIFrames(shell.tester, 100*time.Millisecond)
		if !realShellWait(t, func() bool { return shell.commandTextCount() > beforePopup }) {
			t.Fatalf("the popup never opened after the debounce: %v", shell.tester.Texts())
		}
	}

	shell.tester.Key(0, ui.KeyTab)
	pumpTUIFrames(shell.tester, 200*time.Millisecond)
	// The popup yields to the shell's completion: the list closes, and the line
	// is the typed prefix still — zsh has nothing to complete it to, and
	// natcatty's row must not have been inserted either way.
	if got := shell.commandTextCount(); got != beforePopup {
		t.Fatalf("Tab did not close the popup for the shell (%d → %d occurrences): %v",
			beforePopup+1, got, shell.tester.Texts())
	}
	if !realShellWait(t, func() bool { return shell.session.Snapshot().Input == capturePrefix }) {
		t.Fatalf("Tab left the line as %q — either natcatty's row was inserted instead of the shell answering, or the line was clobbered",
			shell.session.Snapshot().Input)
	}
}

// TestPopupPreviewAcceptsACapturedCommandInARealBash runs the same end-to-end
// flow against bash, the shell the reported Session ran: bash hands the typed
// line to the capture hook from its history inside the DEBUG trap, a different
// path than zsh's zle, and the live preview must still land the candidate in
// the line.
func TestPopupPreviewAcceptsACapturedCommandInARealBash(t *testing.T) {

	shell := openCaptureShellNamed(t, "bash", 0)
	shell.runCaptureCommand(t)

	beforePopup := shell.waitForCommandHeader(t)
	shell.tester.Type(capturePrefix)
	if !realShellWait(t, func() bool { return shell.session.Snapshot().Input == capturePrefix }) {
		t.Fatalf("the typed line is %q, want %q", shell.session.Snapshot().Input, capturePrefix)
	}
	if !realShellWait(t, func() bool { return shell.commandTextCount() > beforePopup }) {
		pumpTUIFrames(shell.tester, 100*time.Millisecond)
		if !realShellWait(t, func() bool { return shell.commandTextCount() > beforePopup }) {
			t.Fatalf("the popup never opened after the debounce in bash: %v", shell.tester.Texts())
		}
	}

	shell.tester.Key(0, ui.KeyDown)
	if !realShellWait(t, func() bool { return shell.session.Snapshot().Input == captureCommand }) {
		t.Fatalf("the live preview left the line as %q, want %q",
			shell.session.Snapshot().Input, captureCommand)
	}

	before := markerOutputRuns(shell.surface.ScreenText())
	shell.runAcceptedLine(t, before+1)
}

// TestRecallRecordsTheRenderedCommand is the end-to-end guard for the screen
// capture fallback. A history recall rewrites the line invisibly to the
// keystroke mirror: the user types a command, runs it, presses Up so zsh
// repaints it on the prompt, and runs it again. The mirror saw only the Up
// arrow, so before the fallback the second execution captured nothing (the
// hybrid discard). Now the tap reads the rendered line at Enter, and the
// recalled command reaches the journal a second time — correct, not dropped,
// not a hybrid.
func TestRecallRecordsTheRenderedCommand(t *testing.T) {
	testRecallRecordsTheRenderedCommand(t, openCaptureShell(t, 0))
}

// TestRecallRecordsTheRenderedCommandInBash runs the recall flow against bash,
// whose readline repaints the recalled line just as zsh's zle does. The capture
// fallback must read the rendered line in either shell.
//
// It is skipped: a real bash session's prompt rows are not marked with the OSC
// 133 prompt semantics ghostty exposes, so there is no trustworthy line to read
// (Terminal.SubmittedLine reports not-ok). zsh rows are marked; bash's are not,
// even though the integration emits the same A/B marks in both. That marking
// gap is independent of the recall fallback — it decides whether any screen
// read is possible at all — so it is tracked separately (ticket 04), and the
// recall capture in bash degrades to the safe discard exactly as it did before
// this feature. The skip lifts when the marking gap closes.
func TestRecallRecordsTheRenderedCommandInBash(t *testing.T) {
	t.Skip("bash prompt rows are not OSC 133-marked, so no trustworthy line exists to read; the recall degrades to the discard")
	testRecallRecordsTheRenderedCommand(t, openCaptureShellNamed(t, "bash", 0))
}

func testRecallRecordsTheRenderedCommand(t *testing.T, shell *captureShell) {
	t.Helper()
	shell.runCaptureCommand(t) // first execution: journal count 1

	if got := shell.journal.countCommand(captureCommand); got != 1 {
		t.Fatalf("after the first run the journal holds %d rows of %q, want 1: %v",
			got, captureCommand, shell.journal.commands())
	}

	// Press Up once: the shell recalls the last command onto the line. The
	// keystroke mirror sees only the arrow; the line editor swaps the buffer
	// invisibly.
	shell.tester.Key(0, ui.KeyUp)
	pumpTUIFrames(shell.tester, 300*time.Millisecond)
	// Run the recalled line. If the fallback reads the screen, this records the
	// same correct command again; if it still discards, the count stays 1.
	shell.tester.Key(0, ui.KeyEnter)
	if !realShellWait(t, func() bool {
		return shell.journal.countCommand(captureCommand) >= 2
	}) {
		t.Fatalf("the recalled execution never reached the journal: count %d, commands %v, screen %q",
			shell.journal.countCommand(captureCommand), shell.journal.commands(), shell.surface.ScreenText())
	}
	// The captured text must be the verbatim command, not a hybrid fragment or
	// control-byte residue.
	for _, command := range shell.journal.commands() {
		if strings.ContainsRune(command, '\x1b') || strings.ContainsRune(command, '\x03') {
			t.Fatalf("the journal holds a garbled row %q: %v", command, shell.journal.commands())
		}
	}
}

// TestEmptyRecallDoesNotCaptureThePrompt is the negative guard: when the
// recall leaves nothing on the line, or the user backs out of a reverse search
// to a bare prompt, Enter must not record the prompt chrome as a command. The
// journal must keep only the one real execution.
func TestEmptyRecallDoesNotCaptureThePrompt(t *testing.T) {
	shell := openCaptureShell(t, 0)
	shell.runCaptureCommand(t)

	if got := shell.journal.countCommand(captureCommand); got != 1 {
		t.Fatalf("after the first run the journal holds %d rows of %q, want 1: %v",
			got, captureCommand, shell.journal.commands())
	}

	// Recall then erase the line with Ctrl+U, so Enter submits a bare prompt.
	// No command ran, so the journal must not grow — and especially must not
	// grow with a row that is only the prompt text.
	shell.tester.Key(0, ui.KeyUp)
	pumpTUIFrames(shell.tester, 200*time.Millisecond)
	shell.tester.Key(ui.Ctrl, ui.KeyU)
	pumpTUIFrames(shell.tester, 200*time.Millisecond)
	before := len(shell.journal.commands())
	shell.tester.Key(0, ui.KeyEnter)
	pumpTUIFrames(shell.tester, 400*time.Millisecond)
	if got := len(shell.journal.commands()); got != before {
		t.Fatalf("a bare-prompt Enter grew the journal to %v", shell.journal.commands())
	}
}

// shareUserCache points an isolated HOME at the developer's real cache
// directory. MyGo fetches its native rendering library into $HOME's cache, so
// without this every test with a temporary home downloaded it again, and a
// slow download looked exactly like a shell that never produced output.
func shareUserCache(t *testing.T, home string) {
	t.Helper()
	cache, err := os.UserCacheDir()
	if err != nil {
		return
	}
	if info, statErr := os.Stat(cache); statErr != nil || !info.IsDir() {
		return
	}
	link := filepath.Join(home, ".cache")
	if runtime.GOOS == "darwin" {
		link = filepath.Join(home, "Library", "Caches")
	}
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(cache, link); err != nil {
		t.Fatal(err)
	}
}

// realShellStartWait is realShellWait with room for the shell's own startup.
// zsh runs the system files and the user's before its first prompt, which took
// about two seconds on this machine, so the five seconds the state checks use
// are too tight for the first output when several real shells run in one
// process.
func realShellStartWait(t *testing.T, condition func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}
