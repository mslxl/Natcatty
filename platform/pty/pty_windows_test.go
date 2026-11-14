//go:build windows

package pty

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestNatcattyConPTYSmokeEcho(t *testing.T) {
	sess, err := Start(Options{
		Path: "cmd.exe",
		Args: []string{"cmd.exe", "/c", "echo natcatty-ok"},
		Cols: 80,
		Rows: 24,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	buf := make([]byte, 4096)
	var tapped []byte
	readDone := make(chan struct{})
	go func() {
		for {
			n, readErr := sess.Read(buf)
			if n > 0 {
				tapped = append(tapped, buf[:n]...)
			}
			if readErr != nil {
				close(readDone)
				return
			}
		}
	}()
	waitDone := make(chan int, 1)
	go func() {
		code, _ := sess.Wait()
		waitDone <- code
	}()
	var code int
	select {
	case code = <-waitDone:
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for ConPTY child")
	}
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	select {
	case <-readDone:
	case <-time.After(3 * time.Second):
		t.Fatal("ConPTY reader did not stop after close")
	}
	text := string(tapped)
	if !strings.Contains(text, "natcatty-ok") {
		t.Fatalf("captured output = %q", text)
	}
}

func TestNatcattyConPTYResizeAndClose(t *testing.T) {
	sess, err := Start(Options{Path: "cmd.exe", Args: []string{"cmd.exe", "/c", "pause"}})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if err := sess.Resize(120, 40); err != nil {
		t.Fatal(err)
	}
	if err := sess.Resize(0, 0); err != nil {
		t.Fatal(err)
	}
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	if err := sess.Resize(80, 24); !errors.Is(err, ErrClosed) {
		t.Fatalf("Resize after close = %v", err)
	}
	if _, err := sess.Write([]byte("x")); !errors.Is(err, ErrClosed) {
		t.Fatalf("Write after close = %v", err)
	}
}

// TestNatcattyConPTYPwshInput exercises the same interactive child used by a
// Local Terminal. A ConPTY can report a successful CreateProcess while
// PowerShell has already failed during startup, so an echo-only smoke test is
// not enough to cover the input path.
func TestNatcattyConPTYPwshInput(t *testing.T) {
	pwsh, err := exec.LookPath("pwsh.exe")
	if err != nil {
		t.Skip("PowerShell is not installed")
	}
	sess, err := Start(Options{
		Path: pwsh,
		Args: []string{"pwsh.exe", "-NoLogo", "-NoProfile"},
		Cols: 120,
		Rows: 40,
	})
	if err != nil {
		t.Fatalf("Start PowerShell: %v", err)
	}
	defer sess.Close()

	output := make(chan string, 1)
	go func() {
		var all strings.Builder
		buf := make([]byte, 4096)
		for {
			n, readErr := sess.Read(buf)
			if n > 0 {
				all.Write(buf[:n])
				if strings.Contains(all.String(), "NATCATTTY_PWSH_INPUT") {
					output <- all.String()
					return
				}
			}
			if readErr != nil {
				output <- all.String()
				return
			}
		}
	}()

	time.Sleep(750 * time.Millisecond)
	if _, err := sess.Write([]byte("Write-Output NATCATTTY_PWSH_INPUT\r")); err != nil {
		t.Fatalf("write PowerShell input: %v", err)
	}
	select {
	case text := <-output:
		if !strings.Contains(text, "NATCATTTY_PWSH_INPUT") {
			t.Fatalf("PowerShell output = %q", text)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("PowerShell did not echo interactive input through ConPTY")
	}
}
