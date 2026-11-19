//go:build darwin || linux || windows

package session_test

import (
	"context"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mslxl/natcatty/adapters/localpty"
	appsession "github.com/mslxl/natcatty/application/session"
)

// TestLocalShellReportsItsCWDWithoutOSC7 runs a real local shell with no
// capture hook, changes directory, and waits for the Directory Picker's source
// (Snapshot.CWD) to catch up. The shell never emits OSC 7: the process probe
// is the only source. The wait is a condition poll with a hard deadline.
func TestLocalShellReportsItsCWDWithoutOSC7(t *testing.T) {
	start := t.TempDir()
	target := t.TempDir()
	spec := appsession.StartSpec{
		Kind: appsession.KindLocal, Label: "Local Term",
		Dir: start, Cols: 90, Rows: 24,
	}
	var changeDir string
	switch runtime.GOOS {
	case "windows":
		spec.Command = []string{"cmd.exe"}
		changeDir = `cd /d "` + target + `"` + "\r"
	default:
		const shell = "/bin/sh"
		if _, err := os.Stat(shell); err != nil {
			t.Skipf("%s is unavailable: %v", shell, err)
		}
		spec.Command = []string{shell}
		changeDir = "cd '" + strings.ReplaceAll(target, "'", `'\''`) + "'\r"
	}

	service := appsession.NewService(localpty.Factory{})
	session, err := service.Open(context.Background(), spec)
	if err != nil {
		t.Fatalf("Open local shell: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	waitForCWD(t, session, start)
	if !session.InsertInput([]byte(changeDir)) {
		t.Fatal("the local shell refused the cd command")
	}
	waitForCWD(t, session, target)
}

// waitForCWD waits for Snapshot.CWD to report want, polling a real condition
// with a hard deadline rather than assuming a delay.
func waitForCWD(t *testing.T, session *appsession.Session, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if session.Snapshot().CWD == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("cwd never became %q; last reported %q", want, session.Snapshot().CWD)
}
