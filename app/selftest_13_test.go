//go:build selftest

package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/selftest"
)

// TestTicket13CheckRunsThroughTheKeyboard drives the whole ticket 13 check
// through F12 1 3. It uses the fake window host from the app tests but the
// real State, check steps, seams, and result file.
func TestTicket13CheckRunsThroughTheKeyboard(t *testing.T) {
	dir := t.TempDir()
	savedDir := selftestResultDir
	selftestResultDir = dir
	t.Cleanup(func() { selftestResultDir = savedDir })

	graph := newWindowGraph(t, nil)
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
	tester.Key(0, ui.KeyF12)
	tester.Frame()
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
