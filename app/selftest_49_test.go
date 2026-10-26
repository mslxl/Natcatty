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

// runTicket49 presses 4 and 9, advances the harness until the headless sync
// scenario finishes, and asserts a passing seven-step result. openMenu presses
// F11 first.
func runTicket49(t *testing.T, tester *ui.Tester, openMenu bool) selftest.Result {
	t.Helper()
	if openMenu {
		tester.Key(0, ui.KeyF11)
		tester.Frame()
	}
	tester.Key(0, ui.Key4)
	tester.Frame()
	tester.Key(0, ui.Key9)
	tester.Frame()

	// The scenario bounds itself at 30 s. The tester loop waits on the
	// harness state, never on a fixed delay.
	deadline := time.Now().Add(2 * time.Minute)
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
		t.Fatalf("ticket 49 self-test failed: %v", result.Errors)
	}
	if len(result.Steps) != 7 {
		t.Fatalf("steps = %d, want 7: %+v", len(result.Steps), result.Steps)
	}
	for _, step := range result.Steps {
		t.Logf("%s: %s (%s)", step.Name, step.Detail, step.Seam)
	}
	return result
}

// TestTicket49CheckRunsThroughTheKeyboard drives F11,4,9. The check uses the
// real sync stack, an in-memory credential store, a temporary SQLite Vault,
// and an httptest fake GitHub server; no real network or window is involved.
func TestTicket49CheckRunsThroughTheKeyboard(t *testing.T) {
	tester, dir := newTicket16Tester(t)
	tester.Key(0, ui.KeyF11)
	tester.Frame()
	if !tester.HasText("Self-test") {
		t.Fatalf("F11 did not open the overlay: %v", tester.Texts())
	}
	result := runTicket49(t, tester, false)

	seams := map[string]bool{}
	for _, step := range result.Steps {
		if step.Seam != "" {
			seams[step.Seam] = true
		}
	}
	for _, want := range []string{
		"domain/sync.OriginalFormatVector + EncryptBytes/MarshalFile",
		"adapters/github.Client device flow + httptest fake GitHub",
		"application/sync.WithAppliedSnapshot + ui/vault/state.InvalidateLists/FlushLists",
		"application/sync.NotifyLocalChange debounce + adapters/store change hook",
		"adapters/github conditional PATCH + domain/sync.Decide",
		"application/sync.Service empty-Vault guard before any remote write",
	} {
		if !seams[want] {
			t.Fatalf("seams = %v, missing %q", seams, want)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "natcatty-selftest-49.json")); err != nil {
		t.Fatalf("result file: %v", err)
	}
	if !tester.HasText("PASS  49") {
		t.Fatalf("the overlay does not show PASS: %v", tester.Texts())
	}
}

// TestTicket49CheckPassesTwiceInOneProcess proves the fake server, temporary
// Vault, credential store, and scenario state are all released between runs.
func TestTicket49CheckPassesTwiceInOneProcess(t *testing.T) {
	tester, dir := newTicket16Tester(t)
	first := runTicket49(t, tester, true)
	if !first.Pass {
		t.Fatalf("first run failed: %+v", first)
	}

	tester.Key(0, ui.KeyF11)
	tester.Frame()
	tester.Key(0, ui.KeyF11)
	tester.Frame()
	second := runTicket49(t, tester, false)
	for _, step := range second.Steps {
		if !step.Pass {
			t.Fatalf("second-run step %q failed: %+v", step.Name, second.Steps)
		}
	}
	if !tester.HasText("PASS  49") {
		t.Fatalf("the overlay does not show PASS after the second run: %v", tester.Texts())
	}
	if _, err := os.Stat(filepath.Join(dir, "natcatty-selftest-49.json")); err != nil {
		t.Fatalf("result file: %v", err)
	}
}
