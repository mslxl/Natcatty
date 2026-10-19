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

func newTicket15Tester(t *testing.T) (*ui.Tester, string) {
	t.Helper()
	dir := t.TempDir()
	savedDir := selftestResultDir
	selftestResultDir = dir
	t.Cleanup(func() { selftestResultDir = savedDir })

	graph := newWindowGraph(t, unlockedGate(t))
	installSelftest(graph.manager, graph.state, nil, func([]string, string) {
		graph.manager.SecondInstance()
	})
	t.Cleanup(func() { activeSelftest = nil })

	graph.manager.OpenMain()
	window := graph.host.windows[0]
	if window == nil {
		t.Fatal("the main window was not created")
	}

	tester := ui.NewTester(window.spec.Content, 1100, 720)
	tester.SetLocale("en")
	tester.Frame()
	return tester, dir
}

// runTicket15 presses 1 and 5, advances the harness to completion, and
// asserts a passing check. openMenu presses F11 first.
func runTicket15(t *testing.T, tester *ui.Tester, openMenu bool) selftest.Result {
	t.Helper()
	if openMenu {
		tester.Key(0, ui.KeyF11)
		tester.Frame()
	}
	tester.Key(0, ui.Key1)
	tester.Frame()
	tester.Key(0, ui.Key5)
	tester.Frame()

	deadline := time.Now().Add(20 * time.Second)
	for activeSelftest.State() == selftest.StateRunning && time.Now().Before(deadline) {
		tester.Advance(selftestStepPoll)
	}
	result, ok := activeSelftest.LastResult()
	if !ok {
		t.Fatalf("the check did not finish: state = %v", activeSelftest.State())
	}
	if !result.Pass {
		t.Fatalf("ticket 15 self-test failed: %+v", result)
	}
	if len(result.Steps) != 7 {
		t.Fatalf("steps = %d, want 7: %+v", len(result.Steps), result.Steps)
	}
	return result
}

// TestTicket15CheckRunsThroughTheKeyboard drives F11,1,5 from the initial
// Vault view through the production App Lock gate. The logging steps use real
// local shells; the OSC and transfer steps feed the real tap through a pipe
// transport.
func TestTicket15CheckRunsThroughTheKeyboard(t *testing.T) {
	tester, dir := newTicket15Tester(t)
	if !tester.HasText("Vault") {
		t.Fatalf("the main window does not start on the Vault view: %v", tester.Texts())
	}
	tester.Key(0, ui.KeyF11)
	tester.Frame()
	if !tester.HasText("Self-test") {
		t.Fatalf("F11 did not open the overlay: %v", tester.Texts())
	}
	result := runTicket15(t, tester, false)

	seams := map[string]bool{}
	for _, step := range result.Steps {
		if step.Seam != "" {
			seams[step.Seam] = true
		}
	}
	for _, want := range []string{"localpty.Factory", "pipe transport",
		"sessionui.RenderSelftestChrome", "shell State"} {
		if !seams[want] {
			t.Fatalf("seams = %v, missing %q", seams, want)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "natcatty-selftest-15.json")); err != nil {
		t.Fatalf("result file: %v", err)
	}
	if !tester.HasText("PASS  15") {
		t.Fatalf("the overlay does not show PASS: %v", tester.Texts())
	}
}

// TestTicket15CheckPassesTwiceInOneProcess is the repeat-run regression: the
// second run must remove the previous log files, Sessions, and demo Tab, or
// the log and OSC steps start from the last run's state.
func TestTicket15CheckPassesTwiceInOneProcess(t *testing.T) {
	tester, dir := newTicket15Tester(t)
	first := runTicket15(t, tester, true)
	if !first.Pass {
		t.Fatalf("first run failed: %+v", first)
	}

	tester.Key(0, ui.KeyF11)
	tester.Frame()
	tester.Key(0, ui.KeyF11)
	tester.Frame()
	second := runTicket15(t, tester, false)

	for _, step := range second.Steps {
		if !step.Pass {
			t.Fatalf("second-run step %q failed: %+v", step.Name, second.Steps)
		}
	}
	if !tester.HasText("PASS  15") {
		t.Fatalf("the overlay does not show PASS after the second run: %v", tester.Texts())
	}
	if _, err := os.Stat(filepath.Join(dir, "natcatty-selftest-15.json")); err != nil {
		t.Fatalf("result file: %v", err)
	}
}
