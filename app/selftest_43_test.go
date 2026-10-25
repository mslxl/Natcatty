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

// runTicket43 presses 4 and 3 and advances the harness until the three
// in-process steps finish. openMenu presses F11 first.
func runTicket43(t *testing.T, tester *ui.Tester, openMenu bool) selftest.Result {
	t.Helper()
	if openMenu {
		tester.Key(0, ui.KeyF11)
		tester.Frame()
	}
	tester.Key(0, ui.Key4)
	tester.Frame()
	tester.Key(0, ui.Key3)
	tester.Frame()

	deadline := time.Now().Add(60 * time.Second)
	for activeSelftest.State() == selftest.StateRunning && time.Now().Before(deadline) {
		tester.Advance(selftestStepPoll)
		if activeSelftest.State() == selftest.StateRunning {
			time.Sleep(2 * time.Millisecond)
		}
	}
	result, ok := activeSelftest.LastResult()
	if !ok {
		t.Fatalf("the check did not finish: state = %v", activeSelftest.State())
	}
	if !result.Pass {
		for _, step := range result.Steps {
			t.Logf("step %s pass=%v detail=%s err=%s", step.Name, step.Pass, step.Detail, step.Error)
		}
		t.Fatalf("ticket 43 self-test failed: %v", result.Errors)
	}
	if len(result.Steps) != 3 {
		t.Fatalf("steps = %d, want 3: %+v", len(result.Steps), result.Steps)
	}
	for _, step := range result.Steps {
		t.Logf("%s: %s (%s)", step.Name, step.Detail, step.Seam)
	}
	return result
}

// TestTicket43CheckRunsThroughTheKeyboard drives F11,4,3 from the initial Vault
// view. Every step uses in-process fixtures and the committed Windows bundle.
func TestTicket43CheckRunsThroughTheKeyboard(t *testing.T) {
	t.Setenv("NATCATTY_RESOURCES", filepath.Join("..", "resources"))
	tester, dir := newTicket16Tester(t)
	tester.Key(0, ui.KeyF11)
	tester.Frame()
	if !tester.HasText("Self-test") {
		t.Fatalf("F11 did not open the overlay: %v", tester.Texts())
	}
	result := runTicket43(t, tester, false)

	seams := map[string]bool{}
	for _, step := range result.Steps {
		if step.Seam != "" {
			seams[step.Seam] = true
		}
	}
	for _, want := range []string{
		"adapters/bundle.Resolve + adapters/mosh.Factory + adapters/et.Factory",
		"adapters/mosh.Factory + domain/model/mosh.Sniffer + in-process SSH/UDP/PTY fixtures",
		"adapters/et.Factory.Prepare + domain/model/et.CheckRoute + AskpassEntries",
	} {
		if !seams[want] {
			t.Fatalf("seams = %v, missing %q", seams, want)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "natcatty-selftest-43.json")); err != nil {
		t.Fatalf("result file: %v", err)
	}
	if !tester.HasText("PASS  43") {
		t.Fatalf("the overlay does not show PASS: %v", tester.Texts())
	}
}

// TestTicket43CheckPassesTwiceInOneProcess proves the fixtures close their
// goroutines and files, so a repeated F11,4,3 starts clean.
func TestTicket43CheckPassesTwiceInOneProcess(t *testing.T) {
	t.Setenv("NATCATTY_RESOURCES", filepath.Join("..", "resources"))
	tester, dir := newTicket16Tester(t)
	first := runTicket43(t, tester, true)
	if !first.Pass {
		t.Fatalf("first run failed: %+v", first)
	}

	tester.Key(0, ui.KeyF11)
	tester.Frame()
	tester.Key(0, ui.KeyF11)
	tester.Frame()
	runTicket43(t, tester, false)
	if !tester.HasText("PASS  43") {
		t.Fatalf("the overlay does not show PASS after the second run: %v", tester.Texts())
	}
	if _, err := os.Stat(filepath.Join(dir, "natcatty-selftest-43.json")); err != nil {
		t.Fatalf("result file: %v", err)
	}
}
