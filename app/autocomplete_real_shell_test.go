//go:build windows || darwin || linux

package app

import (
	"context"
	"os"
	"os/exec"
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
		Dir:     t.TempDir(),
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
		// Run the accepted command; only a line that is exactly it prints
		// the marker on a line of its own. Enter comes straight from the
		// keyboard: accepting must leave the terminal focused.
		tester.Key(0, ui.KeyEnter)
		if !realShellWait(t, func() bool { return exactScreenLineCount(surface.ScreenText(), marker) >= 1 }) {
			t.Fatalf("%s: the shell did not run the accepted command: %q", name, surface.ScreenText())
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

	if got := exactScreenLineCount(surface.ScreenText(), tabMarker) +
		exactScreenLineCount(surface.ScreenText(), clickMarker); got < 2 {
		t.Fatalf("the shell ran %d accepted commands, want 2: %q", got, surface.ScreenText())
	}
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

// exactScreenLineCount counts screen lines equal to want, ignoring the CR some
// shells leave at a line end. The command's echo line carries the prompt, so
// only a line that is exactly the marker proves the shell ran it.
func exactScreenLineCount(screen, want string) int {
	count := 0
	for _, line := range strings.Split(screen, "\n") {
		if strings.TrimSpace(strings.TrimSuffix(line, "\r")) == want {
			count++
		}
	}
	return count
}
