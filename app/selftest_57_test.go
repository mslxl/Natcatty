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

// runTicket57 presses 5 and 7, advances the harness until the background policy
// and stdio scenario completes, and asserts a passing four-step result.
// openMenu presses F11 first.
func runTicket57(t *testing.T, tester *ui.Tester, openMenu bool) selftest.Result {
	t.Helper()
	if openMenu {
		tester.Key(0, ui.KeyF11)
		tester.Frame()
	}
	tester.Key(0, ui.Key5)
	tester.Frame()
	tester.Key(0, ui.Key7)
	tester.Frame()

	// The check itself bounds the scenario at 180s. Give the tester extra time
	// for the first go build of cmd/natcatty-mcp. The loop waits on the harness
	// state, never on a fixed delay.
	deadline := time.Now().Add(4 * time.Minute)
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
		t.Fatalf("ticket 57 self-test failed: %v", result.Errors)
	}
	if len(result.Steps) != 4 {
		t.Fatalf("steps = %d, want 4: %+v", len(result.Steps), result.Steps)
	}
	for _, step := range result.Steps {
		t.Logf("%s: %s (%s)", step.Name, step.Detail, step.Seam)
	}
	return result
}

// TestTicket57CheckRunsThroughTheKeyboard drives F11,5,7. The check runs the
// real policy matrices, the Settings seam, and a real cmd/natcatty-mcp process
// over stdio against a dedicated authenticated loopback listener.
func TestTicket57CheckRunsThroughTheKeyboard(t *testing.T) {
	tester, dir := newTicket16Tester(t)
	tester.Key(0, ui.KeyF11)
	tester.Frame()
	if !tester.HasText("Self-test") {
		t.Fatalf("F11 did not open the overlay: %v", tester.Texts())
	}

	result := runTicket57(t, tester, false)
	for _, step := range result.Steps {
		if step.Detail == "" {
			t.Fatalf("step %q has no detail: %+v", step.Name, result.Steps)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "natcatty-selftest-57.json")); err != nil {
		t.Fatalf("result file: %v", err)
	}
	if !tester.HasText("PASS  57") {
		t.Fatalf("the overlay does not show PASS: %v", tester.Texts())
	}
}

// TestTicket57CheckPassesTwiceInOneProcess proves the reset-at-entry path and
// that the built client, pipes, temporary binary, listener, and injected clock
// are all released between runs.
func TestTicket57CheckPassesTwiceInOneProcess(t *testing.T) {
	tester, dir := newTicket16Tester(t)
	first := runTicket57(t, tester, true)
	if !first.Pass {
		t.Fatalf("first run failed: %+v", first)
	}

	tester.Key(0, ui.KeyF11)
	tester.Frame()
	tester.Key(0, ui.KeyF11)
	tester.Frame()
	second := runTicket57(t, tester, false)
	for _, step := range second.Steps {
		if !step.Pass {
			t.Fatalf("second-run step %q failed: %+v", step.Name, second.Steps)
		}
	}
	if !tester.HasText("PASS  57") {
		t.Fatalf("the overlay does not show PASS after the second run: %v", tester.Texts())
	}
	if _, err := os.Stat(filepath.Join(dir, "natcatty-selftest-57.json")); err != nil {
		t.Fatalf("result file: %v", err)
	}
}
