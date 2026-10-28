//go:build windows || darwin || linux

package app

import (
	"context"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/adapters/localpty"
	"github.com/mslxl/natcatty/application/autocomplete"
	appsession "github.com/mslxl/natcatty/application/session"
	sessionui "github.com/mslxl/natcatty/ui/session"
)

// TestSurfaceAutocompleteReplacesTheRealShellLine drives a real shell through
// the Surface: the accepted command must be the line the shell holds, with no
// control bytes left behind. PSReadLine binds Ctrl+U to Undo, so the earlier
// Ctrl+U accept left "git s^Ugit stash" on the PowerShell line; this test
// presses Tab and clicks a popup row against a real shell, then runs the
// accepted command. Its exact output line proves the shell received the
// suggestion as the whole line and nothing else.
func TestSurfaceAutocompleteReplacesTheRealShellLine(t *testing.T) {
	command := realShellCommand(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	service := appsession.NewService(localpty.Factory{})
	t.Cleanup(service.CloseAll)
	session, err := service.Open(ctx, appsession.StartSpec{
		Kind:    appsession.KindLocal,
		Command: command,
		Dir:     realShellDir(t),
		Cols:    100,
		Rows:    30,
	})
	if err != nil {
		t.Fatalf("open the real shell: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	// The two rounds use distinct commands so the status bar, which remembers
	// the previous command, cannot be mistaken for the popup row by its text.
	const (
		tabPrefix     = "echo NATCAT_TAB_"
		tabSuggestion = "echo NATCAT_TAB_OK"
		tabMarker     = "NATCAT_TAB_OK"
		clickPrefix   = "echo NATCAT_CLICK_"
		clickSuggest  = "echo NATCAT_CLICK_OK"
		clickMarker   = "NATCAT_CLICK_OK"
	)
	providers := map[string]string{tabPrefix: tabSuggestion, clickPrefix: clickSuggest}
	surface, err := sessionui.New(session,
		sessionui.WithRedraw(func() {}),
		sessionui.WithSuggestions(func(input, _ string) []autocomplete.Suggestion {
			suggestion, ok := providers[input]
			if !ok {
				return nil
			}
			return []autocomplete.Suggestion{{
				Text: suggestion, Display: suggestion, Source: autocomplete.SourceHistory,
			}}
		}, func() (bool, bool) { return true, false }),
	)
	if err != nil {
		t.Fatalf("build the Session surface: %v", err)
	}
	t.Cleanup(surface.Close)

	tester := ui.NewTester(func(c *ui.Context) { surface.View(c) }, 900, 620)
	tester.SetLocale("en")
	tester.Frame()
	if !realShellWait(t, func() bool { return strings.TrimSpace(surface.ScreenText()) != "" }) {
		t.Fatal("the real shell produced no output")
	}
	tester.ClickAt(450, 560)

	acceptAndRun := func(name, prefix, suggestion, marker string, waitPrompt bool, accept func()) {
		t.Helper()
		tester.Type(prefix)
		if !realShellWait(t, func() bool { return session.Snapshot().Input == prefix }) {
			t.Fatalf("%s: the typed line is %q, want %q", name, session.Snapshot().Input, prefix)
		}
		tester.Frame()
		if !tester.HasText(suggestion) {
			t.Fatalf("%s: the popup does not show the suggestion: %v", name, tester.Texts())
		}
		accept()
		if !realShellWait(t, func() bool { return session.Snapshot().Input == suggestion }) {
			t.Fatalf("%s: the shell line after accepting is %q, want %q",
				name, session.Snapshot().Input, suggestion)
		}
		if !realShellWait(t, func() bool { return strings.Contains(surface.ScreenText(), suggestion) }) {
			t.Fatalf("%s: the shell line on screen is %q", name, surface.ScreenText())
		}
		if screen := surface.ScreenText(); strings.Contains(screen, "^U") ||
			strings.Contains(screen, "NATCAT_echo") {
			t.Fatalf("%s: control bytes or a duplicated prefix stayed on the shell line: %q", name, screen)
		}
		// Run the accepted command. The submitted command the Session's input
		// tap saw must be exactly the suggestion, and the screen must show
		// the suggestion followed only by whitespace before its marker
		// output. The pairing is deliberate: PowerShell clears the rest of
		// the input line with spaces on Enter, the last column auto-wraps,
		// and MyGo's
		// terminal joins soft-wrapped rows, so the echo, its output, and the
		// next prompt can share one logical line. A line-equality check would
		// then fail even though the shell ran the command; requiring the
		// output marker to follow the suggestion still rejects a different
		// line (whose output would not be the marker) without depending on
		// where the terminal wrapped. Enter comes straight from the keyboard:
		// accepting must leave the terminal focused.
		tester.Key(0, ui.KeyEnter)
		if !realShellWait(t, func() bool {
			return session.Snapshot().Command.Text == suggestion &&
				commandRanOnScreen(surface.ScreenText(), suggestion, marker)
		}) {
			t.Fatalf("%s: the shell did not run the accepted command: %q (submitted %q)",
				name, surface.ScreenText(), session.Snapshot().Command.Text)
		}
		if waitPrompt {
			if !realShellWait(t, func() bool { return !session.Snapshot().Command.Running }) {
				t.Fatalf("%s: the command did not finish: %+v screen %q",
					name, session.Snapshot().Command, surface.ScreenText())
			}
		}
	}

	// Tab takes the production key path through the terminal's OnKey hook.
	acceptAndRun("Tab", tabPrefix, tabSuggestion, tabMarker, true,
		func() { tester.Key(0, ui.KeyTab) })

	// Clicking a popup row must take the same verified write path.
	acceptAndRun("click", clickPrefix, clickSuggest, clickMarker, false, func() {
		if err := tester.Click(clickSuggest); err != nil {
			t.Errorf("click the suggestion: %v", err)
		}
	})

	screen := surface.ScreenText()
	if !commandRanOnScreen(screen, tabSuggestion, tabMarker) ||
		!commandRanOnScreen(screen, clickSuggest, clickMarker) {
		t.Fatalf("the shell did not run both accepted commands: %q", screen)
	}
}

// TestSurfaceAutocompleteAfterAFastRealCommand guards the command-tracking
// state autocomplete depends on. A fast command's prompt can arrive inside the
// fallback's guard window; once that happened with no further output, Running
// stayed true and autocomplete stayed suppressed until Ctrl+C. Running a real
// command must clear Running and the next typed prefix must open the popup
// without any interrupt.
func TestSurfaceAutocompleteAfterAFastRealCommand(t *testing.T) {
	command := realShellCommand(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	service := appsession.NewService(localpty.Factory{})
	t.Cleanup(service.CloseAll)
	session, err := service.Open(ctx, appsession.StartSpec{
		Kind:    appsession.KindLocal,
		Command: command,
		Dir:     realShellDir(t),
		Cols:    100,
		Rows:    30,
	})
	if err != nil {
		t.Fatalf("open the real shell: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	const (
		prefix     = "echo NATCAT_FAST_"
		suggestion = "echo NATCAT_FAST_OK"
		runCommand = "echo NATCAT_FAST_RUN"
		marker     = "NATCAT_FAST_RUN"
	)
	surface, err := sessionui.New(session,
		sessionui.WithRedraw(func() {}),
		sessionui.WithSuggestions(func(input, _ string) []autocomplete.Suggestion {
			if input != prefix {
				return nil
			}
			return []autocomplete.Suggestion{{
				Text: suggestion, Display: suggestion, Source: autocomplete.SourceHistory,
			}}
		}, func() (bool, bool) { return true, false }),
	)
	if err != nil {
		t.Fatalf("build the Session surface: %v", err)
	}
	t.Cleanup(surface.Close)

	tester := ui.NewTester(func(c *ui.Context) { surface.View(c) }, 900, 620)
	tester.SetLocale("en")
	tester.Frame()
	if !realShellWait(t, func() bool { return strings.TrimSpace(surface.ScreenText()) != "" }) {
		t.Fatal("the real shell produced no output")
	}
	tester.ClickAt(450, 560)

	// Warm the shell and PSReadLine first: a first command pays interpreter and
	// line-editor startup, so its prompt lands later than the guard window and
	// would not exercise the fast-command path this test guards.
	tester.Type("echo NATCAT_WARM")
	if !realShellWait(t, func() bool { return session.Snapshot().Input == "echo NATCAT_WARM" }) {
		t.Fatalf("the typed line is %q", session.Snapshot().Input)
	}
	tester.Key(0, ui.KeyEnter)
	if !realShellWait(t, func() bool {
		return commandRanOnScreen(surface.ScreenText(), "echo NATCAT_WARM", "NATCAT_WARM")
	}) {
		t.Fatalf("the shell did not run the warm-up command: %q", surface.ScreenText())
	}

	tester.Type(runCommand)
	if !realShellWait(t, func() bool { return session.Snapshot().Input == runCommand }) {
		t.Fatalf("the typed line is %q, want %q", session.Snapshot().Input, runCommand)
	}
	tester.Key(0, ui.KeyEnter)
	if !realShellWait(t, func() bool {
		return commandRanOnScreen(surface.ScreenText(), runCommand, marker)
	}) {
		t.Fatalf("the shell did not run the fast command: %q", surface.ScreenText())
	}

	// The prompt returned; command tracking must follow it on its own. No
	// Ctrl+C, no extra output, and no further input is allowed to help.
	if !realShellWait(t, func() bool { return !session.Snapshot().Command.Running }) {
		t.Fatalf("Running stuck after the fast command: %+v screen %q",
			session.Snapshot().Command, surface.ScreenText())
	}

	tester.Type(prefix)
	if !realShellWait(t, func() bool { return session.Snapshot().Input == prefix }) {
		t.Fatalf("the typed line is %q, want %q", session.Snapshot().Input, prefix)
	}
	tester.Frame()
	if !tester.HasText(suggestion) {
		t.Fatalf("autocomplete stayed suppressed after a fast command: %v", tester.Texts())
	}
}

// realShellDir returns a short working directory for the real shell.
// t.TempDir() embeds the whole test name, and with a long private TEMP the
// PowerShell prompt then fills the hundred-column terminal: the wrapped prompt
// merges the command echo and its output into one logical screen line, which
// the wrap-independent proof below has to tolerate. The length of a temporary
// path must not decide whether the accepted line is observable, so keep the
// prompt short and the proof strict.
func realShellDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ncat-auto-")
	if err != nil {
		t.Fatalf("create the shell's working directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// realShellCommand picks a real interactive shell: PowerShell (PSReadLine) on
// Windows, and a POSIX shell elsewhere.
func realShellCommand(t *testing.T) []string {
	t.Helper()
	if runtime.GOOS == "windows" {
		for _, name := range []string{"pwsh.exe", "powershell.exe"} {
			if path, err := exec.LookPath(name); err == nil {
				return []string{path, "-NoProfile", "-NoLogo"}
			}
		}
		t.Skip("no PowerShell is available to exercise PSReadLine")
	}
	for _, name := range []string{"/bin/sh", "/bin/bash", "/bin/zsh"} {
		if info, err := os.Stat(name); err == nil && !info.IsDir() {
			return []string{name}
		}
	}
	t.Skip("no POSIX shell is available")
	return nil
}

// realShellWait polls a condition for a real shell's asynchronous output. It
// keeps its own five-second budget instead of reusing the UI tests' waitFor,
// whose two seconds fit the fake shell but are tight for a real one starting.
// Every iteration observes real shell state, and the ten-millisecond interval
// is only the poll cadence; the loop always stops at the hard deadline.
func realShellWait(t *testing.T, condition func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// commandRanOnScreen reports whether the flattened terminal text shows the
// accepted command's echo followed by its marker output. The terminal joins
// soft-wrapped rows, so the echo and its output may share one logical line
// separated only by the spaces PowerShell used to clear the input line; the
// whole suggestion must still be followed by the marker as a complete word.
// A line holding anything else (an appended word, for example) cannot match:
// its output would carry that word after the marker, so the marker would not
// be the first complete word after the suggestion.
func commandRanOnScreen(screen, suggestion, marker string) bool {
	pattern := regexp.MustCompile(
		regexp.QuoteMeta(suggestion) + `\s*` + regexp.QuoteMeta(marker) + `\b`)
	return pattern.MatchString(screen)
}

// TestCommandRanOnScreenAcceptsJoinedLinesAndRejectsChangedCommands pins the
// proof used above. A soft-wrapped screen joins the echo and its output with
// spaces or a newline; a line that differs from the suggestion carries the
// difference into its output, so the marker no longer directly follows the
// suggestion.
func TestCommandRanOnScreenAcceptsJoinedLinesAndRejectsChangedCommands(t *testing.T) {
	const (
		suggestion = "echo NATCAT_TAB_OK"
		marker     = "NATCAT_TAB_OK"
	)
	for _, test := range []struct {
		name   string
		screen string
		want   bool
	}{
		{"separate lines", "PS C:\\tmp> echo NATCAT_TAB_OK\r\nNATCAT_TAB_OK\r\nPS C:\\tmp>", true},
		{"soft-wrapped rows", "PS C:\\tmp> echo NATCAT_TAB_OK        NATCAT_TAB_OK        PS C:\\tmp>", true},
		{"appended character", "PS C:\\tmp> echo NATCAT_TAB_OKX\r\nNATCAT_TAB_OKX\r\nPS C:\\tmp>", false},
		{"appended word", "PS C:\\tmp> echo NATCAT_TAB_OK extra\r\nNATCAT_TAB_OK extra\r\nPS C:\\tmp>", false},
	} {
		if got := commandRanOnScreen(test.screen, suggestion, marker); got != test.want {
			t.Errorf("%s: commandRanOnScreen(%q) = %v, want %v", test.name, test.screen, got, test.want)
		}
	}
}
