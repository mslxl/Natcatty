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

// runTicket31 presses 3 and 1, advances the harness to completion, and
// asserts a passing nine-step result. openMenu presses F11 first.
func runTicket31(t *testing.T, tester *ui.Tester, openMenu bool) selftest.Result {
	t.Helper()
	if openMenu {
		tester.Key(0, ui.KeyF11)
		tester.Frame()
	}
	tester.Key(0, ui.Key3)
	tester.Frame()
	tester.Key(0, ui.Key1)
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
		t.Fatalf("ticket 31 self-test failed: %v", result.Errors)
	}
	if len(result.Steps) != 9 {
		t.Fatalf("steps = %d, want 9: %+v", len(result.Steps), result.Steps)
	}
	for _, step := range result.Steps {
		t.Logf("%s: %s (%s)", step.Name, step.Detail, step.Seam)
	}
	return result
}

// TestTicket31CheckRunsThroughTheKeyboard drives F11,3,1 from the initial
// view. The steps use the real queue, local adapter, saved history, surface
// state, and surface view.
func TestTicket31CheckRunsThroughTheKeyboard(t *testing.T) {
	tester, dir := newTicket16Tester(t)
	tester.Key(0, ui.KeyF11)
	tester.Frame()
	if !tester.HasText("Self-test") {
		t.Fatalf("F11 did not open the overlay: %v", tester.Texts())
	}
	result := runTicket31(t, tester, false)

	seams := map[string]bool{}
	for _, step := range result.Steps {
		if step.Seam != "" {
			seams[step.Seam] = true
		}
	}
	for _, want := range []string{
		"apptransfer.Queue + adaptersftp.Local",
		"apptransfer.Queue + transferstate.State",
		"apptransfer.Queue + domainsftp.TaskStatus.CanRetry",
		"apptransfer.Queue.Stats + domainsftp.LocalResourceKey",
		"apptransfer.Queue + domainsftp.DowngradeInterrupted",
		"transferstate.State + apptransfer.Queue",
		"apptransfer.Queue + domainsftp.ErrTaskUnavailable",
		"transferui.View + transferstate.State + apptransfer.Queue",
	} {
		if !seams[want] {
			t.Fatalf("seams = %v, missing %q", seams, want)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "natcatty-selftest-31.json")); err != nil {
		t.Fatalf("result file: %v", err)
	}
	if !tester.HasText("PASS  31") {
		t.Fatalf("the overlay does not show PASS: %v", tester.Texts())
	}
}

// TestTicket31CheckPassesTwiceInOneProcess proves the queue workers and the
// temporary source tree are cleaned up, so a repeated F11,3,1 starts clean.
func TestTicket31CheckPassesTwiceInOneProcess(t *testing.T) {
	tester, dir := newTicket16Tester(t)
	first := runTicket31(t, tester, true)
	if !first.Pass {
		t.Fatalf("first run failed: %+v", first)
	}

	tester.Key(0, ui.KeyF11)
	tester.Frame()
	tester.Key(0, ui.KeyF11)
	tester.Frame()
	second := runTicket31(t, tester, false)
	for _, step := range second.Steps {
		if !step.Pass {
			t.Fatalf("second-run step %q failed: %+v", step.Name, second.Steps)
		}
	}
	if !tester.HasText("PASS  31") {
		t.Fatalf("the overlay does not show PASS after the second run: %v", tester.Texts())
	}
	if _, err := os.Stat(filepath.Join(dir, "natcatty-selftest-31.json")); err != nil {
		t.Fatalf("result file: %v", err)
	}
}
