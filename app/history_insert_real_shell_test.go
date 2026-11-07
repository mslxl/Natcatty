//go:build windows || darwin || linux

package app

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mslxl/natcatty/adapters/localpty"
	appsession "github.com/mslxl/natcatty/application/session"
	sessionui "github.com/mslxl/natcatty/ui/session"
)

// TestHistoryInsertIsSafeInARealShell reproduces the reported marker-file
// repro against a shell that enables bracketed paste: every variant of a
// multi-line history command must remain in the line editor, and a single-line
// command must not be submitted either. A shell without bracketed paste is
// covered by the refusal test below.
func TestHistoryInsertIsSafeInARealShell(t *testing.T) {
	command := bracketedPasteShellCommand(t)
	tests := []struct {
		name string
		text func(marker string) string
	}{
		{"multiline", func(marker string) string {
			return "touch " + marker + "\necho SHOULD_NOT_RUN"
		}},
		{"crlf", func(marker string) string {
			return "touch " + marker + "\r\necho SHOULD_NOT_RUN"
		}},
		{"trailing newline", func(marker string) string {
			return "touch " + marker + "\n"
		}},
		{"imported text", func(marker string) string {
			// A synced row can carry the bracketed-paste end marker itself. It
			// must stay inert text rather than ending the paste and submitting
			// the command that follows it.
			return "touch " + marker + "\x1b[201~\r\necho SHOULD_NOT_RUN"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := realShellDir(t)
			marker := filepath.Join(dir, "natcat-insert-marker")
			text := test.text(marker)
			surface, session := openInsertSurface(t, command, dir, true)
			if err := surface.InsertText(text); err != nil {
				if errors.Is(err, sessionui.ErrInsertUnsafe) {
					t.Skip("this shell did not enable bracketed paste")
				}
				t.Fatalf("InsertText: %v", err)
			}
			if !realShellWait(t, func() bool {
				return strings.Contains(surface.ScreenText(), "touch "+marker)
			}) {
				t.Fatalf("the inserted text is not on the shell line: %q", surface.ScreenText())
			}
			if !strings.Contains(session.Snapshot().Input, "touch "+marker) {
				t.Fatalf("the tracked line = %q, want the inserted command",
					session.Snapshot().Input)
			}
			if markerAppeared(marker, 500*time.Millisecond) {
				t.Fatalf("inserting %q executed the command", text)
			}
		})
	}

	t.Run("single line", func(t *testing.T) {
		dir := realShellDir(t)
		marker := filepath.Join(dir, "natcat-single-marker")
		surface, session := openInsertSurface(t, command, dir, false)
		if err := surface.InsertText("touch " + marker); err != nil {
			if errors.Is(err, sessionui.ErrInsertUnsafe) {
				t.Skip("this shell did not enable bracketed paste")
			}
			t.Fatalf("InsertText: %v", err)
		}
		if !realShellWait(t, func() bool {
			return strings.Contains(surface.ScreenText(), "touch "+marker)
		}) {
			t.Fatalf("the inserted text is not on the shell line: %q", surface.ScreenText())
		}
		if session.Snapshot().Input != "touch "+marker {
			t.Fatalf("the tracked line = %q, want the inserted command",
				session.Snapshot().Input)
		}
		if markerAppeared(marker, 500*time.Millisecond) {
			t.Fatal("inserting a single-line command submitted it")
		}
	})
}

// TestHistoryInsertRefusesMultilineWithoutBracketedPaste runs the exact
// /bin/sh repro from the verification report. The surface must refuse the
// multi-line text rather than typing newlines the shell would execute; a
// single-line insert still works and stays unsubmitted.
func TestHistoryInsertRefusesMultilineWithoutBracketedPaste(t *testing.T) {
	shell := posixShellWithoutBracketedPaste(t)
	dir := realShellDir(t)
	multilineMarker := filepath.Join(dir, "natcat-multiline-marker")
	singleMarker := filepath.Join(dir, "natcat-single-marker")
	surface, session := openInsertSurface(t, shell, dir, false)

	err := surface.InsertText("touch " + multilineMarker + "\necho SHOULD_NOT_RUN")
	if !errors.Is(err, sessionui.ErrInsertUnsafe) {
		t.Fatalf("multi-line InsertText error = %v, want ErrInsertUnsafe", err)
	}
	if markerAppeared(multilineMarker, 500*time.Millisecond) {
		t.Fatal("a refused multi-line insert executed the command")
	}
	if session.Snapshot().Input != "" {
		t.Fatalf("a refused insert changed the tracked line to %q",
			session.Snapshot().Input)
	}

	if err := surface.InsertText("touch " + singleMarker); err != nil {
		t.Fatalf("single-line InsertText: %v", err)
	}
	if !realShellWait(t, func() bool {
		return strings.Contains(surface.ScreenText(), "touch "+singleMarker)
	}) {
		t.Fatalf("the single-line insert is not on the shell line: %q", surface.ScreenText())
	}
	if session.Snapshot().Input != "touch "+singleMarker {
		t.Fatalf("the tracked line = %q, want the inserted command",
			session.Snapshot().Input)
	}
	if markerAppeared(singleMarker, 500*time.Millisecond) {
		t.Fatal("a single-line insert submitted the command")
	}
}

// openInsertSurface starts one real shell and returns its live surface after
// the prompt is visible and the terminal has recorded its modes.
func openInsertSurface(t *testing.T, command []string, dir string, waitBracketed bool) (*sessionui.Surface, *appsession.Session) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	service := appsession.NewService(localpty.Factory{})
	t.Cleanup(service.CloseAll)
	session, err := service.Open(ctx, appsession.StartSpec{
		Kind:    appsession.KindLocal,
		Command: command,
		Dir:     dir,
		Cols:    100,
		Rows:    30,
	})
	if err != nil {
		t.Fatalf("open the real shell: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	surface, err := sessionui.New(session, sessionui.WithRedraw(func() {}))
	if err != nil {
		t.Fatalf("build the Session surface: %v", err)
	}
	t.Cleanup(surface.Close)
	if !realShellWait(t, func() bool { return strings.TrimSpace(surface.ScreenText()) != "" }) {
		t.Fatal("the real shell produced no output")
	}
	if waitBracketed && !realShellWait(t, surface.BracketedPasteEnabled) {
		t.Skip("this shell did not enable bracketed paste")
	}
	return surface, session
}

// bracketedPasteShellCommand prefers a shell whose line editor enables DEC
// private mode 2004. The InsertText call in the test skips if the chosen shell
// still does not.
func bracketedPasteShellCommand(t *testing.T) []string {
	t.Helper()
	if runtime.GOOS == "windows" {
		for _, name := range []string{"pwsh.exe", "powershell.exe"} {
			if path, err := exec.LookPath(name); err == nil {
				return []string{path, "-NoProfile", "-NoLogo"}
			}
		}
		t.Skip("no PowerShell is available to exercise PSReadLine")
	}
	for _, name := range []string{"/bin/zsh", "/usr/bin/zsh", "/bin/bash", "/usr/bin/bash"} {
		if info, err := os.Stat(name); err == nil && !info.IsDir() {
			return []string{name}
		}
	}
	t.Skip("no shell that can enable bracketed paste is available")
	return nil
}

// posixShellWithoutBracketedPaste picks /bin/sh, whose readline on the
// supported platforms does not enable mode 2004. If a platform's /bin/sh does,
// the test skips rather than asserting an unsupported refusal.
func posixShellWithoutBracketedPaste(t *testing.T) []string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("no POSIX shell on Windows")
	}
	if info, err := os.Stat("/bin/sh"); err == nil && !info.IsDir() {
		return []string{"/bin/sh"}
	}
	t.Skip("no /bin/sh is available")
	return nil
}

// markerAppeared polls a file for a bounded window. Absence is the assertion,
// so the loop observes the real condition (the file) and stops at a hard
// deadline; the interval is only a poll cadence.
func markerAppeared(path string, budget time.Duration) bool {
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}
