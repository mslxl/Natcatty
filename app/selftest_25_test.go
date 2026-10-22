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

// runTicket25 presses 2 and 5, advances the harness to completion, and asserts
// a passing four-step result. openMenu presses F11 first.
func runTicket25(t *testing.T, tester *ui.Tester, openMenu bool) selftest.Result {
	t.Helper()
	if openMenu {
		tester.Key(0, ui.KeyF11)
		tester.Frame()
	}
	tester.Key(0, ui.Key2)
	tester.Frame()
	tester.Key(0, ui.Key5)
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
		t.Fatalf("ticket 25 self-test failed: %v", result.Errors)
	}
	if len(result.Steps) != 4 {
		t.Fatalf("steps = %d, want 4: %+v", len(result.Steps), result.Steps)
	}
	for _, step := range result.Steps {
		t.Logf("%s: %s (%s)", step.Name, step.Detail, step.Seam)
	}
	return result
}

// TestTicket25CheckRunsThroughTheKeyboard drives F11,2,5 from the initial
// Vault view. Every step uses the real serializers, SQLite store,
// field-secret store, Vault service, Vault state, and Vault view.
func TestTicket25CheckRunsThroughTheKeyboard(t *testing.T) {
	tester, dir := newTicket16Tester(t)
	if !tester.HasText("Vault") {
		t.Fatalf("the main window does not start on the Vault view: %v", tester.Texts())
	}
	tester.Key(0, ui.KeyF11)
	tester.Frame()
	if !tester.HasText("Self-test") {
		t.Fatalf("F11 did not open the overlay: %v", tester.Texts())
	}
	result := runTicket25(t, tester, false)

	seams := map[string]bool{}
	for _, step := range result.Steps {
		if step.Seam != "" {
			seams[step.Seam] = true
		}
	}
	for _, want := range []string{
		"appvault.ExportVault + vaultexport.Export + store.SecretStore",
		"vaultstate.Export/ConfirmExportSecrets + vaultui.View",
		"appvault.ExportVault + appvault.PreviewImport/CommitImport + family importers",
		"vaultui.View + vaultstate.BeginExport + ui.Tester",
	} {
		if !seams[want] {
			t.Fatalf("seams = %v, missing %q", seams, want)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "natcatty-selftest-25.json")); err != nil {
		t.Fatalf("result file: %v", err)
	}
	if !tester.HasText("PASS  25") {
		t.Fatalf("the overlay does not show PASS: %v", tester.Texts())
	}
}

// TestTicket25CheckPassesTwiceInOneProcess proves every temporary store,
// secret store, second handle, and directory is closed, so a repeated
// F11,2,5 starts clean.
func TestTicket25CheckPassesTwiceInOneProcess(t *testing.T) {
	tester, dir := newTicket16Tester(t)
	first := runTicket25(t, tester, true)
	if !first.Pass {
		t.Fatalf("first run failed: %+v", first)
	}

	tester.Key(0, ui.KeyF11)
	tester.Frame()
	tester.Key(0, ui.KeyF11)
	tester.Frame()
	second := runTicket25(t, tester, false)
	for _, step := range second.Steps {
		if !step.Pass {
			t.Fatalf("second-run step %q failed: %+v", step.Name, second.Steps)
		}
	}
	if !tester.HasText("PASS  25") {
		t.Fatalf("the overlay does not show PASS after the second run: %v", tester.Texts())
	}
	if _, err := os.Stat(filepath.Join(dir, "natcatty-selftest-25.json")); err != nil {
		t.Fatalf("result file: %v", err)
	}
}
