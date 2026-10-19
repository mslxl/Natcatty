//go:build selftest

package app

import (
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	appapplock "github.com/mslxl/natcatty/application/applock"
	domainapplock "github.com/mslxl/natcatty/domain/applock"
	uiapplock "github.com/mslxl/natcatty/ui/applock"
	"github.com/mslxl/natcatty/ui/selftest"
)

// unlockedGate returns the production App Lock gate with its service enabled,
// loaded, and unlocked, so the test exercises the same gate wrapper as the
// composition root rather than bypassing it with a nil gate.
func unlockedGate(t *testing.T) *uiapplock.Gate {
	t.Helper()
	verifier, err := domainapplock.NewVerifier("secret", rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	service := appapplock.NewService(&windowSettingsRepository{settings: domainapplock.Settings{
		Enabled:          true,
		TimeoutMinutes:   15,
		PasswordVerifier: verifier,
	}})
	if err := service.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !service.Snapshot().Locked {
		t.Fatal("an enabled App Lock did not start locked")
	}
	if err := service.Unlock("secret"); err != nil {
		t.Fatal(err)
	}
	if service.Snapshot().Locked {
		t.Fatal("the App Lock stayed locked")
	}
	return uiapplock.NewGate(service)
}

// TestTicket13CheckRunsThroughTheKeyboard drives the whole ticket 13 check
// through F11 1 3 from the initial Vault view, through the production App Lock
// gate. It uses the fake window host from the app tests but the real State,
// check steps, seams, and result file.
func TestTicket13CheckRunsThroughTheKeyboard(t *testing.T) {
	dir := t.TempDir()
	savedDir := selftestResultDir
	selftestResultDir = dir
	t.Cleanup(func() { selftestResultDir = savedDir })

	graph := newWindowGraph(t, unlockedGate(t))
	installSelftest(graph.manager, graph.state, func() {
		graph.manager.SecondInstance()
	})
	t.Cleanup(func() { activeSelftest = nil })

	main := graph.manager.OpenMain()
	window := graph.host.windows[0]
	if window == nil || main == nil {
		t.Fatal("the main window was not created")
	}

	tester := ui.NewTester(window.spec.Content, 1100, 720)
	tester.SetLocale("en")
	tester.Frame()
	// The check starts from the Vault view, with no terminal holding focus,
	// and the window's content is inside the App Lock gate.
	if !tester.HasText("Vault") {
		t.Fatalf("the main window does not start on the Vault view: %v", tester.Texts())
	}
	tester.Key(0, ui.KeyF11)
	tester.Frame()
	if !tester.HasText("Self-test") {
		t.Fatalf("F11 did not open the overlay through the gate: %v", tester.Texts())
	}
	tester.Key(0, ui.Key1)
	tester.Frame()
	tester.Key(0, ui.Key3)
	tester.Frame()

	// Waiting steps ask for frames with Context.After; advance the tester's
	// clock until the check settles.
	deadline := time.Now().Add(10 * time.Second)
	for activeSelftest.State() == selftest.StateRunning && time.Now().Before(deadline) {
		tester.Advance(selftestStepPoll)
	}
	result, ok := activeSelftest.LastResult()
	if !ok {
		t.Fatalf("the check did not finish: state = %v", activeSelftest.State())
	}
	if !result.Pass {
		t.Fatalf("ticket 13 self-test failed: %+v", result)
	}
	if len(result.Steps) != 7 {
		t.Fatalf("steps = %d, want 7: %+v", len(result.Steps), result.Steps)
	}
	// Steps that substituted a seam say which one.
	seams := map[string]bool{}
	for _, step := range result.Steps {
		if step.Seam != "" {
			seams[step.Seam] = true
		}
	}
	if !seams["dialogConfirmer"] || !seams["uiapplock.Gate"] {
		t.Fatalf("seams = %v, want dialogConfirmer and uiapplock.Gate", seams)
	}
	if _, err := os.Stat(filepath.Join(dir, "natcatty-selftest-13.json")); err != nil {
		t.Fatalf("result file: %v", err)
	}
	if !tester.HasText("PASS  13") {
		t.Fatalf("the overlay does not show PASS: %v", tester.Texts())
	}
}

// selftestStepPoll is longer than the harness's 25 ms poll so one Advance
// crosses at most one due frame.
const selftestStepPoll = 40 * time.Millisecond
