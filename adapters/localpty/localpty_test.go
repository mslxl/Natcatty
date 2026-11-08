//go:build windows || darwin || linux

package localpty

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	appsession "github.com/mslxl/natcatty/application/session"
)

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
