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

// runTicket38 presses 3 and 8, advances the harness to completion, and asserts
// a passing six-step result. openMenu presses F11 first.
func runTicket38(t *testing.T, tester *ui.Tester, openMenu bool) selftest.Result {
	t.Helper()
	if openMenu {
		tester.Key(0, ui.KeyF11)
		tester.Frame()
	}
	tester.Key(0, ui.Key3)
	tester.Frame()
	tester.Key(0, ui.Key8)
	tester.Frame()

	deadline := time.Now().Add(120 * time.Second)
	for activeSelftest.State() == selftest.StateRunning && time.Now().Before(deadline) {
		tester.Advance(selftestStepPoll)
	}
	result, ok := activeSelftest.LastResult()
	if !ok {
		t.Fatalf("the check did not finish: state = %v", activeSelftest.State())
	}
	if !result.Pass {
		for _, step := range result.Steps {
			t.Logf("step %s pass=%v detail=%s err=%s", step.Name, step.Pass, step.Detail, step.Error)
		}
		t.Fatalf("ticket 38 self-test failed: %v", result.Errors)
	}
	if len(result.Steps) != 6 {
		t.Fatalf("steps = %d, want 6: %+v", len(result.Steps), result.Steps)
	}
	for _, step := range result.Steps {
		t.Logf("%s: %s (%s)", step.Name, step.Detail, step.Seam)
	}
	return result
}

// TestTicket38CheckRunsThroughTheKeyboard drives F11,3,8 from the initial
// view. The real transfer surface, the real file-open service, the real
// editor view (through a headless tester), and the real staged write run;
// only the launcher and the remote transport's Create failure are scripted.
func TestTicket38CheckRunsThroughTheKeyboard(t *testing.T) {
	tester, dir := newTicket16Tester(t)
	tester.Key(0, ui.KeyF11)
	tester.Frame()
	if !tester.HasText("Self-test") {
		t.Fatalf("F11 did not open the overlay: %v", tester.Texts())
	}
	result := runTicket38(t, tester, false)

	seams := map[string]bool{}
	for _, step := range result.Steps {
		if step.Seam != "" {
			seams[step.Seam] = true
		}
	}
	for _, want := range []string{
		"application/fileopen.Service.SweepTemp (the call app/app.go makes at startup)",
		"transferstate.State.Open -> application/fileopen.Service -> ui/editor.State -> application/transfer.Service.WriteFile",
		"application/fileopen.Service watcher (real launcher seam) -> application/transfer.Service.WriteFile",
		"application/fileopen.Service.Save conflict check -> ui/editor.State prompt -> application/transfer.Service.WriteFile",
		"application/fileopen.Service error classification -> ui/editor.State retry -> application/transfer.Service.WriteFile",
		"application/fileopen.Service.SweepTemp + Close/Shutdown",
	} {
		if !seams[want] {
			t.Fatalf("seams = %v, missing %q", seams, want)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "natcatty-selftest-38.json")); err != nil {
		t.Fatalf("result file: %v", err)
	}
	if !tester.HasText("PASS  38") {
		t.Fatalf("the overlay does not show PASS: %v", tester.Texts())
	}
}

// TestTicket38CheckPassesTwiceInOneProcess proves the fixtures, the editing
// sessions, and the transfer surface are reset, so a repeated F11,3,8 starts
// clean.
func TestTicket38CheckPassesTwiceInOneProcess(t *testing.T) {
	tester, dir := newTicket16Tester(t)
	first := runTicket38(t, tester, true)
	if !first.Pass {
		t.Fatalf("first run failed: %+v", first)
	}

	tester.Key(0, ui.KeyF11)
	tester.Frame()
	tester.Key(0, ui.KeyF11)
	tester.Frame()
	second := runTicket38(t, tester, false)
	for _, step := range second.Steps {
		if !step.Pass {
			t.Fatalf("second-run step %q failed: %+v", step.Name, second.Steps)
		}
	}
	if !tester.HasText("PASS  38") {
		t.Fatalf("the overlay does not show PASS after the second run: %v", tester.Texts())
	}
	if _, err := os.Stat(filepath.Join(dir, "natcatty-selftest-38.json")); err != nil {
		t.Fatalf("result file: %v", err)
	}
}
