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

// runTicket56 presses 5 and 6, advances the harness until the background stdio
// exchange completes, and asserts a passing two-step result. openMenu presses
// F11 first.
func runTicket56(t *testing.T, tester *ui.Tester, openMenu bool) selftest.Result {
	t.Helper()
	if openMenu {
		tester.Key(0, ui.KeyF11)
		tester.Frame()
	}
	tester.Key(0, ui.Key5)
	tester.Frame()
	tester.Key(0, ui.Key6)
	tester.Frame()

	// The step itself bounds the exchange at 180s; give the tester extra time
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
		t.Fatalf("ticket 56 self-test failed: %v", result.Errors)
	}
	if len(result.Steps) != 2 {
		t.Fatalf("steps = %d, want 2: %+v", len(result.Steps), result.Steps)
	}
	for _, step := range result.Steps {
		t.Logf("%s: %s (%s)", step.Name, step.Detail, step.Seam)
	}
	return result
}

// TestTicket56CheckRunsThroughTheKeyboard drives F11,5,6. The check builds and
// launches cmd/natcatty-mcp, authenticates to a real loopback listener, lists
// the progressive catalog, loads a toolset and then all, and calls read tools.
func TestTicket56CheckRunsThroughTheKeyboard(t *testing.T) {
	tester, dir := newTicket16Tester(t)
	tester.Key(0, ui.KeyF11)
	tester.Frame()
	if !tester.HasText("Self-test") {
		t.Fatalf("F11 did not open the overlay: %v", tester.Texts())
	}

	result := runTicket56(t, tester, false)
	if result.Steps[0].Detail == "" || result.Steps[1].Detail == "" {
		t.Fatalf("steps have no detail: %+v", result.Steps)
	}
	if _, err := os.Stat(filepath.Join(dir, "natcatty-selftest-56.json")); err != nil {
		t.Fatalf("result file: %v", err)
	}
	if !tester.HasText("PASS  56") {
		t.Fatalf("the overlay does not show PASS: %v", tester.Texts())
	}
}

// TestTicket56CheckPassesTwiceInOneProcess proves the launched process, pipes,
// temporary binary, and fallback listener are all released between runs.
func TestTicket56CheckPassesTwiceInOneProcess(t *testing.T) {
	tester, dir := newTicket16Tester(t)
	first := runTicket56(t, tester, true)
	if !first.Pass {
		t.Fatalf("first run failed: %+v", first)
	}

	tester.Key(0, ui.KeyF11)
	tester.Frame()
	tester.Key(0, ui.KeyF11)
	tester.Frame()
	second := runTicket56(t, tester, false)
	for _, step := range second.Steps {
		if !step.Pass {
			t.Fatalf("second-run step %q failed: %+v", step.Name, second.Steps)
		}
	}
	if !tester.HasText("PASS  56") {
		t.Fatalf("the overlay does not show PASS after the second run: %v", tester.Texts())
	}
	if _, err := os.Stat(filepath.Join(dir, "natcatty-selftest-56.json")); err != nil {
		t.Fatalf("result file: %v", err)
	}
}
