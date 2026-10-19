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

func runTicket17(t *testing.T, tester *ui.Tester, openMenu bool) selftest.Result {
	t.Helper()
	if openMenu {
		tester.Key(0, ui.KeyF11)
		tester.Frame()
	}
	tester.Key(0, ui.Key1)
	tester.Frame()
	tester.Key(0, ui.Key7)
	tester.Frame()

	deadline := time.Now().Add(60 * time.Second)
	for activeSelftest.State() == selftest.StateRunning && time.Now().Before(deadline) {
		tester.Advance(selftestStepPoll)
	}
	result, ok := activeSelftest.LastResult()
	if !ok {
		t.Fatalf("the check did not finish: state = %v", activeSelftest.State())
	}
	if !result.Pass {
		t.Fatalf("ticket 17 self-test failed: %+v", result)
	}
	if len(result.Steps) != 6 {
		t.Fatalf("steps = %d, want 6: %+v", len(result.Steps), result.Steps)
	}
	for _, step := range result.Steps {
		t.Logf("%s: %s (%s)", step.Name, step.Detail, step.Seam)
	}
	return result
}

// TestTicket17CheckRunsThroughTheKeyboard drives F11,1,7 from the initial
// Vault view. The encoding step uses a recording transport through the real
// Session codec; the paste step uses a real local shell; the menu and layout
// steps use the production renderer and real MyGo menus.
func TestTicket17CheckRunsThroughTheKeyboard(t *testing.T) {
	tester, dir := newTicket16Tester(t)
	if !tester.HasText("Vault") {
		t.Fatalf("the main window does not start on the Vault view: %v", tester.Texts())
	}
	tester.Key(0, ui.KeyF11)
	tester.Frame()
	if !tester.HasText("Self-test") {
		t.Fatalf("F11 did not open the overlay: %v", tester.Texts())
	}
	result := runTicket17(t, tester, false)

	seams := map[string]bool{}
	for _, step := range result.Steps {
		if step.Seam != "" {
			seams[step.Seam] = true
		}
	}
	for _, want := range []string{
		"appsession.Session codec and TerminalConn",
		"sessionui.PasteGate + localpty.Factory",
		"sessionui.DropHandler",
		"sessionui.RenderSelftestInteraction + ui.Tester",
		"sessionui.RenderSelftestInteraction",
		"state.SurfaceFactory",
	} {
		if !seams[want] {
			t.Fatalf("seams = %v, missing %q", seams, want)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "natcatty-selftest-17.json")); err != nil {
		t.Fatalf("result file: %v", err)
	}
	if !tester.HasText("PASS  17") {
		t.Fatalf("the overlay does not show PASS: %v", tester.Texts())
	}
}

// TestTicket17CheckPassesTwiceInOneProcess proves the encoding stage, paste
// stage, and demo Tab reset so a repeated F11,1,7 does not inherit pending
// state or leak a shell.
func TestTicket17CheckPassesTwiceInOneProcess(t *testing.T) {
	tester, dir := newTicket16Tester(t)
	first := runTicket17(t, tester, true)
	if !first.Pass {
		t.Fatalf("first run failed: %+v", first)
	}

	tester.Key(0, ui.KeyF11)
	tester.Frame()
	tester.Key(0, ui.KeyF11)
	tester.Frame()
	second := runTicket17(t, tester, false)
	for _, step := range second.Steps {
		if !step.Pass {
			t.Fatalf("second-run step %q failed: %+v", step.Name, second.Steps)
		}
	}
	if !tester.HasText("PASS  17") {
		t.Fatalf("the overlay does not show PASS after the second run: %v", tester.Texts())
	}
	if _, err := os.Stat(filepath.Join(dir, "natcatty-selftest-17.json")); err != nil {
		t.Fatalf("result file: %v", err)
	}
}
