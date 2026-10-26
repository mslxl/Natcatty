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

// runTicket51 presses F11,5,1 when openMenu is set, advances the harness until
// the headless OneDrive scenario finishes, and asserts a passing eight-step
// result.
func runTicket51(t *testing.T, tester *ui.Tester, openMenu bool) selftest.Result {
	t.Helper()
	if openMenu {
		tester.Key(0, ui.KeyF11)
		tester.Frame()
	}
	tester.Key(0, ui.Key5)
	tester.Frame()
	tester.Key(0, ui.Key1)
	tester.Frame()

	// The scenario bounds itself at 60 s. The tester loop waits on the
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
		t.Fatalf("ticket 51 self-test failed: %v", result.Errors)
	}
	if len(result.Steps) != 8 {
		t.Fatalf("steps = %d, want 8: %+v", len(result.Steps), result.Steps)
	}
	for _, step := range result.Steps {
		t.Logf("%s: %s (%s)", step.Name, step.Detail, step.Seam)
	}
	return result
}

func TestTicket51CheckRunsThroughTheKeyboard(t *testing.T) {
	tester, dir := newTicket16Tester(t)
	tester.Key(0, ui.KeyF11)
	tester.Frame()
	if !tester.HasText("Self-test") {
		t.Fatalf("F11 did not open the overlay: %v", tester.Texts())
	}
	result := runTicket51(t, tester, false)

	seams := map[string]bool{}
	for _, step := range result.Steps {
		if step.Seam != "" {
			seams[step.Seam] = true
		}
	}
	for _, want := range []string{
		"adapters/onedrive.Authorize + adapters/oauthloopback S256 + loopback http.Server",
		"adapters/onedrive approot simple PUT create/update + downloadUrl",
		"application/sync ensureFreshToken/refreshToken + adapters/onedrive.Refresh rotation",
		"application/sync reauthorize + BrowserAuthorizer + adapters/onedrive invalid_grant mapping",
		"application/sync applyGrant + Config.AccountID + adapters/onedrive/fake per-account approot",
		"application/sync reauthorize error chain + ProblemReauthorization + adapters/onedrive/fake pre/post content",
	} {
		if !seams[want] {
			t.Fatalf("seams = %v, missing %q", seams, want)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "natcatty-selftest-51.json")); err != nil {
		t.Fatalf("result file: %v", err)
	}
	if !tester.HasText("PASS  51") {
		t.Fatalf("the overlay does not show PASS: %v", tester.Texts())
	}
}

func TestTicket51CheckPassesTwiceInOneProcess(t *testing.T) {
	tester, _ := newTicket16Tester(t)
	first := runTicket51(t, tester, true)
	if !first.Pass {
		t.Fatalf("first run failed: %+v", first.Errors)
	}
	tester.Key(0, ui.KeyF11)
	tester.Frame()
	tester.Key(0, ui.KeyF11)
	tester.Frame()
	second := runTicket51(t, tester, false)
	if !second.Pass {
		t.Fatalf("second run failed: %+v", second.Errors)
	}
	if second.Ticket != 51 {
		t.Fatalf("second run ticket = %d", second.Ticket)
	}
}
