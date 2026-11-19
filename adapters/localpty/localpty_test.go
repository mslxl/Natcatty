//go:build windows || darwin || linux

package localpty

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	appsession "github.com/mslxl/natcatty/application/session"
)

func TestEnvironmentIncludesTerminalLensHints(t *testing.T) {
	values := make(map[string]string)
	for _, entry := range environment(nil) {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			values[key] = value
		}
	}
	for key, want := range map[string]string{
		"TERM":              "xterm-256color",
		"COLORTERM":         "truecolor",
		"TERM_PROGRAM":      "Natcatty",
		"NATCATTY_LENSES":   "1",
		"PAGER":             "cat",
		"GIT_PAGER":         "cat",
		"GH_PAGER":          "cat",
		"MANPAGER":          "cat",
		"SYSTEMD_PAGER":     "cat",
		"AWS_PAGER":         "",
		"BAT_PAGING":        "never",
		"PSQL_PAGER":        "cat",
		"BUILDKIT_PROGRESS": "plain",
		"CLICOLOR":          "1",
	} {
		if got := values[key]; got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

// TestLocalShellProducesOutputAndExitStatus is the adapter integration seam:
// it proves a real pseudo-terminal carries output and reports exit.
func TestLocalShellProducesOutputAndExitStatus(t *testing.T) {
	command := []string{"/bin/sh", "-c", "printf hello"}
	if runtime.GOOS == "windows" {
		command = []string{"cmd.exe", "/c", "echo hello"}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	terminal, err := (Factory{}).Open(ctx, appsession.StartSpec{
		Kind:    appsession.KindLocal,
		Command: command,
		Cols:    80,
		Rows:    24,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer terminal.Close()

	var mu sync.Mutex
	var output strings.Builder
	go func() {
		buffer := make([]byte, 4096)
		for {
			n, err := terminal.Read(buffer)
			if n > 0 {
				mu.Lock()
				output.Write(buffer[:n])
				mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		seen := strings.Contains(output.String(), "hello")
		mu.Unlock()
		if seen {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	got := output.String()
	mu.Unlock()
	if !strings.Contains(got, "hello") {
		t.Fatalf("output = %q, want it to contain hello", got)
	}

	waiter, ok := terminal.(appsession.ExitWaiter)
	if !ok {
		t.Fatal("the local transport does not report an exit code")
	}
	waited := make(chan struct{})
	var code int
	var waitErr error
	go func() {
		code, waitErr = waiter.Wait()
		close(waited)
	}()
	select {
	case <-waited:
	case <-time.After(10 * time.Second):
		t.Fatal("the shell did not exit")
	}
	if waitErr != nil {
		t.Fatalf("Wait: %v", waitErr)
	}
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
}

// TestLocalShellAcceptsInputAndResize drives a real interactive shell through
// the adapter: typed input must execute, and Resize must reach the PTY.
func TestLocalShellAcceptsInputAndResize(t *testing.T) {
	command := []string{"/bin/sh"}
	marker := `printf 'got:%s\n' "$((6*7))"`
	newline := "\n"
	if runtime.GOOS == "windows" {
		command = []string{"cmd.exe", "/q", "/v:on"}
		marker = `set /a x=6*7 & echo got:!x!`
		newline = "\r\n"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	terminal, err := (Factory{}).Open(ctx, appsession.StartSpec{
		Kind:    appsession.KindLocal,
		Command: command,
		Cols:    80,
		Rows:    24,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer terminal.Close()

	var mu sync.Mutex
	var output strings.Builder
	go func() {
		buffer := make([]byte, 4096)
		for {
			n, err := terminal.Read(buffer)
			if n > 0 {
				mu.Lock()
				output.Write(buffer[:n])
				mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()

	if _, err := terminal.Write([]byte(marker + newline)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	waitForOutput(t, &mu, &output, "got:42")

	if err := terminal.Resize(100, 30); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	if runtime.GOOS != "windows" {
		// stty reads the size back from the PTY, proving the ioctl landed.
		if _, err := terminal.Write([]byte("stty size" + newline)); err != nil {
			t.Fatalf("Write: %v", err)
		}
		waitForOutput(t, &mu, &output, "30 100")
	}
}

// TestLocalShellCloseKillsAndReapsAHungUpShell proves Close's bounded wait is
// real: a shell that ignores the hangup is killed and its exit is observed
// before Close returns, so a caller cannot remove files the shell still owns.
func TestLocalShellCloseKillsAndReapsAHungUpShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fixture uses a POSIX shell's signal handling")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	terminal, err := (Factory{}).Open(ctx, appsession.StartSpec{
		Kind: appsession.KindLocal,
		// The shell announces that the ignored disposition is installed, so the
		// test never bets on a delay.
		Command: []string{"/bin/sh", "-c", `trap "" HUP; printf ready; while :; do sleep 1; done`},
		Cols:    80,
		Rows:    24,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ready := make(chan string, 1)
	go func() {
		var seen strings.Builder
		buffer := make([]byte, 256)
		for {
			count, readErr := terminal.Read(buffer)
			if count > 0 {
				seen.Write(buffer[:count])
				if strings.Contains(seen.String(), "ready") {
					ready <- seen.String()
					return
				}
			}
			if readErr != nil {
				ready <- seen.String()
				return
			}
		}
	}()
	select {
	case seen := <-ready:
		if !strings.Contains(seen, "ready") {
			t.Fatalf("the shell exited before installing the ignored hangup: %q", seen)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the shell never signalled readiness")
	}

	started := time.Now()
	if err := terminal.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if elapsed, bound := time.Since(started), closeGrace+closeReapTimeout; elapsed > bound {
		t.Fatalf("Close took %v, want at most the bounded %v", elapsed, bound)
	}
	waiter, ok := terminal.(appsession.ExitWaiter)
	if !ok {
		t.Fatal("the local transport does not report an exit code")
	}
	code, waitErr := waiter.Wait()
	if waitErr != nil {
		t.Fatalf("Wait after Close: %v", waitErr)
	}
	const killedBySIGKILL = 128 + 9
	if code != killedBySIGKILL {
		t.Fatalf("exit code = %d, want %d from the bounded kill", code, killedBySIGKILL)
	}
}

func waitForOutput(t *testing.T, mu *sync.Mutex, output *strings.Builder, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		seen := strings.Contains(output.String(), want)
		mu.Unlock()
		if seen {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	got := output.String()
	mu.Unlock()
	t.Fatalf("output = %q, want it to contain %q", got, want)
}

// writeFakeBash installs a shell at dir/bash whose only job is to answer the
// line-editing probe: exit says whether it has readline.
func writeFakeBash(t *testing.T, dir string, exit int) string {
	t.Helper()
	path := filepath.Join(dir, "bash")
	body := fmt.Sprintf("#!/bin/sh\nexit %d\n", exit)
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestBrokenBashIsReplacedWithAnEditableOne is the regression for a terminal
// that spawned a shell nobody can type into: `nix develop` exports a bash built
// without readline as $SHELL, and such a shell cannot complete a word, decode
// the \[ \] prompt markers, or redraw the prompt, so Tab is a literal tab in
// every terminal. The Session must run a bash with line editing instead.
func TestBrokenBashIsReplacedWithAnEditableOne(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fixture models a POSIX shell build")
	}
	good := writeFakeBash(t, t.TempDir(), 0)
	broken := writeFakeBash(t, t.TempDir(), 1)
	t.Setenv("PATH", filepath.Dir(good))

	if got := editableShell(broken); got != good {
		t.Fatalf("editableShell(%q) = %q, want the editable %q", broken, got, good)
	}
	if got := editableShell(good); got != good {
		t.Fatalf("an editable bash was replaced: %q", got)
	}
}

// TestOnlyBashIsProbedForLineEditing keeps the probe off shells whose builds do
// not have the option at all: a zsh, fish or sh must be started as given.
func TestOnlyBashIsProbedForLineEditing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fixture models a POSIX shell build")
	}
	dir := t.TempDir()
	// A shell that fails every probe stands in for a non-bash program.
	zsh := filepath.Join(dir, "zsh")
	if err := os.WriteFile(zsh, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	if got := editableShell(zsh); got != zsh {
		t.Fatalf("editableShell(%q) = %q, want it unchanged", zsh, got)
	}
}

// TestIntegratedShellStartsWithItsProgramName is the regression for a default
// Local Terminal that closed the moment it opened: integration.Shell returns
// the program and its options separately, and passing those options as argv
// made bash read --rcfile as the name of a script, run it, and exit. The
// integrated shell must stay interactive and answer Tab with a completion.
func TestIntegratedShellStartsWithItsProgramName(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fixture drives a POSIX shell")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed here")
	}
	home := t.TempDir()
	bashrc := "PS1='PROMPT> '\nHISTFILE=/dev/null\n"
	if err := os.WriteFile(filepath.Join(home, ".bashrc"), []byte(bashrc), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("SHELL", bash)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	terminal, err := (Factory{}).Open(ctx, appsession.StartSpec{
		Kind: appsession.KindLocal,
		Env:  []string{"HOME=" + home},
		Cols: 80,
		Rows: 24,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer terminal.Close()

	var mu sync.Mutex
	var output strings.Builder
	go func() {
		buffer := make([]byte, 4096)
		for {
			count, readErr := terminal.Read(buffer)
			if count > 0 {
				mu.Lock()
				output.Write(buffer[:count])
				mu.Unlock()
			}
			if readErr != nil {
				return
			}
		}
	}()
	waitForOutput(t, &mu, &output, "PROMPT> ")
	mu.Lock()
	started := output.String()
	mu.Unlock()
	if !strings.Contains(started, "\x1b]133;A\x07") {
		t.Fatalf("the integrated shell did not start with its marks: %q", started)
	}

	if _, err := terminal.Write([]byte("ech\t")); err != nil {
		t.Fatal(err)
	}
	waitForOutput(t, &mu, &output, "PROMPT> echo ")
}
