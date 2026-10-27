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

// runTicket53 presses F11,5,3 when openMenu is set, advances the harness until
// the headless S3 scenario finishes, and asserts a passing eight-step result.
func runTicket53(t *testing.T, tester *ui.Tester, openMenu bool) selftest.Result {
	t.Helper()
	if openMenu {
		tester.Key(0, ui.KeyF11)
		tester.Frame()
	}
	tester.Key(0, ui.Key5)
	tester.Frame()
	tester.Key(0, ui.Key3)
	tester.Frame()

	// The scenario bounds itself at 90 s. The tester loop waits on the harness
	// state, never on a fixed delay.
	deadline := time.Now().Add(3 * time.Minute)
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
		t.Fatalf("ticket 53 self-test failed: %v", result.Errors)
	}
	if len(result.Steps) != 8 {
		t.Fatalf("steps = %d, want 8: %+v", len(result.Steps), result.Steps)
	}
	for _, step := range result.Steps {
		t.Logf("%s: %s (%s)", step.Name, step.Detail, step.Seam)
	}
	return result
}

func TestTicket53CheckRunsThroughTheKeyboard(t *testing.T) {
	tester, dir := newTicket16Tester(t)
	tester.Key(0, ui.KeyF11)
	tester.Frame()
	if !tester.HasText("Self-test") {
		t.Fatalf("F11 did not open the overlay: %v", tester.Texts())
	}
	result := runTicket53(t, tester, false)

	seams := map[string]bool{}
	for _, step := range result.Steps {
		if step.Seam != "" {
			seams[step.Seam] = true
		}
	}
	for _, want := range []string{
		"application/sync ConnectS3 gate + syncLocked insecure branch",
		"adapters/s3 Sign + adapters/s3/fake independent canonical request/signature recomputation",
		"adapters/s3 objectAddress + adapters/s3/fake path-style routing",
		"adapters/s3 mapStatus + adapters/s3/fake x-amz-bucket-region",
		"adapters/s3 Sign x-amz-security-token + adapters/s3/fake token enforcement",
		"adapters/s3 Read/Write conditional If-Match/If-None-Match + mapStatus",
		"adapters/syncstore.ConfigStore + domain/sync.Token.S3 + adapters/s3/fake redacted traces",
	} {
		if !seams[want] {
			t.Fatalf("seams = %v, missing %q", seams, want)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "natcatty-selftest-53.json")); err != nil {
		t.Fatalf("result file: %v", err)
	}
	if !tester.HasText("PASS  53") {
		t.Fatalf("the overlay does not show PASS: %v", tester.Texts())
	}
}

func TestTicket53CheckPassesTwiceInOneProcess(t *testing.T) {
	tester, _ := newTicket16Tester(t)
	first := runTicket53(t, tester, true)
	if !first.Pass {
		t.Fatalf("first run failed: %+v", first.Errors)
	}
	tester.Key(0, ui.KeyF11)
	tester.Frame()
	tester.Key(0, ui.KeyF11)
	tester.Frame()
	second := runTicket53(t, tester, false)
	if !second.Pass {
		t.Fatalf("second run failed: %+v", second.Errors)
	}
	if second.Ticket != 53 {
		t.Fatalf("second run ticket = %d", second.Ticket)
	}
}
