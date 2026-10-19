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
