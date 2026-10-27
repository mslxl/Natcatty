//go:build selftest

package app

// Ticket 54 exercises the real sync service through the keyboard shortcut:
// F11, then 5, then 4.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/selftest"
)

// runTicket54 presses F11,5,4 when openMenu is set, advances the harness until
// the headless scenario finishes, and asserts a passing six-step result.
func runTicket54(t *testing.T, tester *ui.Tester, openMenu bool) selftest.Result {
	t.Helper()
	if openMenu {
		tester.Key(0, ui.KeyF11)
		tester.Frame()
	}
	tester.Key(0, ui.Key5)
	tester.Frame()
	tester.Key(0, ui.Key4)
	tester.Frame()

	// The scenario bounds itself at 180 s. The tester loop waits on the
	// harness state, never on a fixed delay.
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
		t.Fatalf("ticket 54 self-test failed: %v", result.Errors)
	}
	if len(result.Steps) != 6 {
		t.Fatalf("steps = %d, want 6: %+v", len(result.Steps), result.Steps)
	}
	for _, step := range result.Steps {
		t.Logf("%s: %s (%s)", step.Name, step.Detail, step.Seam)
	}
	return result
}

func TestTicket54CheckRunsThroughTheKeyboard(t *testing.T) {
	tester, dir := newTicket16Tester(t)
	tester.Key(0, ui.KeyF11)
	tester.Frame()
	if !tester.HasText("Self-test") {
		t.Fatalf("F11 did not open the overlay: %v", tester.Texts())
	}
	result := runTicket54(t, tester, false)

	seams := map[string]bool{}
	for _, step := range result.Steps {
		if step.Seam != "" {
			seams[step.Seam] = true
		}
	}
	for _, want := range []string{
		"application/sync Decide + MergePayloads + anchor v3",
		"application/sync mergeSettingsPayload deep merge",
		"application/sync DetectDestructiveSync + applyGuard + RestoreGuard + ForcePush",
		"application/sync detectTombstone + RestoreGuard + ForcePush",
		"application/sync surfaceConflict + resolveLocked + retainRemoteBackup + LoadBackups/RestoreBackup",
	} {
		if !seams[want] {
			t.Fatalf("seams = %v, missing %q", seams, want)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "natcatty-selftest-54.json")); err != nil {
		t.Fatalf("result file: %v", err)
	}
	if !tester.HasText("PASS  54") {
		t.Fatalf("the overlay does not show PASS: %v", tester.Texts())
	}
}

func TestTicket54CheckPassesTwiceInOneProcess(t *testing.T) {
	tester, _ := newTicket16Tester(t)
	first := runTicket54(t, tester, true)
	if !first.Pass {
		t.Fatalf("first run failed: %+v", first.Errors)
	}
	tester.Key(0, ui.KeyF11)
	tester.Frame()
	tester.Key(0, ui.KeyF11)
	tester.Frame()
	second := runTicket54(t, tester, false)
	if !second.Pass {
		t.Fatalf("second run failed: %+v", second.Errors)
	}
	if second.Ticket != 54 {
		t.Fatalf("second run ticket = %d", second.Ticket)
	}
}
