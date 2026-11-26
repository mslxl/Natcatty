//go:build darwin || linux

package app

// Regressions for "Tab inserts a literal tab". The reported flow was a local
// zsh Session: the planned capture hook was replaced by the Lens OSC 133
// integration, so the tap kept reserving every typed command for a handshake
// that never came. The command reached history only when the Session closed,
// the autocomplete service stayed empty while the user typed, and Tab had no
// suggestion to accept, so the Pane forwarded the byte to the shell.
//
// These tests open the same local shell through the production adapters and
// capture hook, wire the production completion hook into a real
// autocomplete.Service, and drive the Surface keys: type a command, run it,
// type its prefix, press Tab. The Tab must replace the line while the Session
// is still open — not after a closer that these tests never perform.

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

// captureShell is a real zsh Session wired to the production capture hook, the
// production completion hook and a real autocomplete service, rendered through
// the Surface.
type captureShell struct {
	session *appsession.Session
	surface *sessionui.Surface
	tester  *ui.Tester
	service *autocomplete.Service
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

	return &captureShell{session: session, surface: surface, tester: tester, service: autocompleteService}
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

// typePrefixWithoutPopup types the prefix and verifies the popup has not drawn
// the full command: the debounce is still running, so a suggestion row cannot
// be how Tab found the line.
//
// The count of the command's text is the measure: the finished block's header
// already shows the command that ran, so the popup is one more occurrence.
func (s *captureShell) typePrefixWithoutPopup(t *testing.T) {
	t.Helper()
	before := s.waitForCommandHeader(t)
	s.tester.Type(capturePrefix)
	if !realShellWait(t, func() bool { return s.session.Snapshot().Input == capturePrefix }) {
		t.Fatalf("the typed line is %q, want %q", s.session.Snapshot().Input, capturePrefix)
	}
	pumpTUIFrames(s.tester, 300*time.Millisecond)
	if got := s.commandTextCount(); got != before {
		t.Fatalf("the suggestion popup drew before the debounce elapsed (%d → %d occurrences): %v",
			before, got, s.tester.Texts())
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
// second time; the marker count is the count of executions.
func (s *captureShell) runAcceptedLine(t *testing.T, markerCount int) {
	t.Helper()
	s.tester.Key(0, ui.KeyEnter)
	if !realShellWait(t, func() bool {
		return strings.Count(s.surface.ScreenText(), captureMarker) >= markerCount &&
			!s.session.Snapshot().Command.Running
	}) {
		t.Fatalf("the accepted command did not run: screen %q command %+v",
			s.surface.ScreenText(), s.session.Snapshot().Command)
	}
}

// TestTabAcceptsACapturedCommandWhileTheSessionIsOpen is the end-to-end guard
// for the reported literal tab.
func TestTabAcceptsACapturedCommandWhileTheSessionIsOpen(t *testing.T) {
	shell := openCaptureShell(t, 0)
	shell.runCaptureCommand(t)

	// The prefix opens the popup from the journal row alone; nothing in this
	// test supplies a suggestion by hand.
	beforePopup := shell.waitForCommandHeader(t)
	shell.tester.Type(capturePrefix)
	if !realShellWait(t, func() bool { return shell.session.Snapshot().Input == capturePrefix }) {
		t.Fatalf("the typed line is %q, want %q", shell.session.Snapshot().Input, capturePrefix)
	}
	pumpTUIFrames(shell.tester, 300*time.Millisecond)
	// Typing never opens the list: the count stays at the header alone, and the
	// first Tab — not a keystroke — asks for completion. The popup row is the
	// one extra occurrence.
	if got := shell.commandTextCount(); got != beforePopup {
		t.Fatalf("the popup opened on typing (%d → %d occurrences): %v",
			beforePopup, got, shell.tester.Texts())
	}
	shell.tester.Key(0, ui.KeyTab)
	pumpTUIFrames(shell.tester, 100*time.Millisecond)
	if got := shell.commandTextCount(); got <= beforePopup {
		t.Fatalf("the first Tab did not open the popup (%d → %d occurrences): %v",
			beforePopup, got, shell.tester.Texts())
	}

	before := strings.Count(shell.surface.ScreenText(), captureMarker)
	shell.tester.Key(0, ui.KeyTab)
	if !realShellWait(t, func() bool { return shell.session.Snapshot().Input == captureCommand }) {
		t.Fatalf("the accepting Tab left the line as %q, want %q — the byte went to the shell",
			shell.session.Snapshot().Input, captureCommand)
	}
	shell.runAcceptedLine(t, before+2)
}

// TestTabInsideTheDebounceWindowCompletesInARealShell guards the second half of
// the literal-tab bug: the autocomplete popup is deliberately delayed after
// typing, and a Tab pressed inside that window used to see no rows, decline
// the key, and send a literal tab to the shell. The key path must recompute the
// rows immediately; only the popup waits.
//
// The delay is large (a minute) so the test is deterministic: the popup cannot
// have appeared, yet Tab must still complete from the capture.
func TestTabInsideTheDebounceWindowCompletesInARealShell(t *testing.T) {
	shell := openCaptureShell(t, 60000)
	shell.runCaptureCommand(t)
	shell.typePrefixWithoutPopup(t)

	// The first Tab inside the debounce window opens the list — the key path
	// never rides the delay — and the second accepts.
	shell.tester.Key(0, ui.KeyTab)
	pumpTUIFrames(shell.tester, 100*time.Millisecond)
	if got := shell.commandTextCount(); got == 0 {
		t.Fatalf("the first Tab inside the debounce window opened nothing: %v", shell.tester.Texts())
	}

	before := strings.Count(shell.surface.ScreenText(), captureMarker)
	shell.tester.Key(0, ui.KeyTab)
	if !realShellWait(t, func() bool { return shell.session.Snapshot().Input == captureCommand }) {
		t.Fatalf("the accepting Tab inside the debounce window left the line as %q, want %q — the byte went to the shell",
			shell.session.Snapshot().Input, captureCommand)
	}
	shell.runAcceptedLine(t, before+2)
}

// TestTabAcceptsACapturedCommandInARealBash runs the same end-to-end flow
// against bash, the shell the reported Session ran: bash hands the typed line
// to the capture hook from its history inside the DEBUG trap, a different path
// than zsh's zle, and Tab must still replace the line instead of leaving a tab.
func TestTabAcceptsACapturedCommandInARealBash(t *testing.T) {
	shell := openCaptureShellNamed(t, "bash", 0)
	shell.runCaptureCommand(t)

	beforePopup := shell.waitForCommandHeader(t)
	shell.tester.Type(capturePrefix)
	if !realShellWait(t, func() bool { return shell.session.Snapshot().Input == capturePrefix }) {
		t.Fatalf("the typed line is %q, want %q", shell.session.Snapshot().Input, capturePrefix)
	}
	pumpTUIFrames(shell.tester, 300*time.Millisecond)
	// Typing never opens the list: the count stays at the header alone, and the
	// first Tab — not a keystroke — asks for completion. The popup row is the
	// one extra occurrence.
	if got := shell.commandTextCount(); got != beforePopup {
		t.Fatalf("the popup opened on typing (%d → %d occurrences): %v",
			beforePopup, got, shell.tester.Texts())
	}
	shell.tester.Key(0, ui.KeyTab)
	pumpTUIFrames(shell.tester, 100*time.Millisecond)
	if got := shell.commandTextCount(); got <= beforePopup {
		t.Fatalf("the first Tab did not open the popup (%d → %d occurrences): %v",
			beforePopup, got, shell.tester.Texts())
	}

	before := strings.Count(shell.surface.ScreenText(), captureMarker)
	shell.tester.Key(0, ui.KeyTab)
	if !realShellWait(t, func() bool { return shell.session.Snapshot().Input == captureCommand }) {
		t.Fatalf("the accepting Tab left the line as %q, want %q — the byte went to the shell",
			shell.session.Snapshot().Input, captureCommand)
	}
	shell.runAcceptedLine(t, before+2)
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
