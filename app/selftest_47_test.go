//go:build selftest

package app

import (
	"image"
	_ "image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/selftest"
)

// runTicket47 presses 4 and 7, advances the harness to completion, and
// asserts a passing eight-step result. openMenu presses F11 first.
func runTicket47(t *testing.T, tester *ui.Tester, openMenu bool) selftest.Result {
	t.Helper()
	if openMenu {
		tester.Key(0, ui.KeyF11)
		tester.Frame()
	}
	tester.Key(0, ui.Key4)
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
		for _, step := range result.Steps {
			t.Logf("step %s pass=%v detail=%s err=%s", step.Name, step.Pass, step.Detail, step.Error)
		}
		t.Fatalf("ticket 47 self-test failed: %v", result.Errors)
	}
	if len(result.Steps) != 8 {
		t.Fatalf("steps = %d, want 8: %+v", len(result.Steps), result.Steps)
	}
	return result
}

// TestTicket47CheckRunsThroughTheKeyboard drives F11,4,7 from the production
// window wiring. The Host, its transport, and every page are in-process; no
// network, web view, or saved Vault record is needed.
func TestTicket47CheckRunsThroughTheKeyboard(t *testing.T) {
	tester, dir := newTicket16Tester(t)
	tester.Key(0, ui.KeyF11)
	tester.Frame()
	if !tester.HasText("Self-test") {
		t.Fatalf("F11 did not open the overlay: %v", tester.Texts())
	}
	result := runTicket47(t, tester, false)
	for _, step := range result.Steps {
		t.Logf("%s: %s (%s)", step.Name, step.Detail, step.Seam)
		if step.Detail == "" {
			t.Errorf("step %s recorded no detail", step.Name)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "natcatty-selftest-47.json")); err != nil {
		t.Fatalf("result file: %v", err)
	}
	shots := filepath.Join(dir, "natcatty-selftest-47-shots")
	entries, err := os.ReadDir(shots)
	if err != nil {
		t.Fatalf("screenshot directory: %v", err)
	}
	if len(entries) != 8 {
		t.Fatalf("screenshots = %d, want 8", len(entries))
	}
	for _, variant := range []string{"1100x720-en", "1100x720-zh-CN", "720x480-en", "720x480-zh-CN"} {
		for _, kind := range []string{"connected", "disconnected"} {
			name := "browser-" + variant + "-" + kind + ".png"
			file, err := os.Open(filepath.Join(shots, name))
			if err != nil {
				t.Fatalf("open %s: %v", name, err)
			}
			config, format, err := image.DecodeConfig(file)
			_ = file.Close()
			if err != nil {
				t.Fatalf("decode %s: %v", name, err)
			}
			wantWidth := 1100
			if strings.HasPrefix(variant, "720x480") {
				wantWidth = 720
			}
			if format != "png" || config.Width != wantWidth || config.Height <= 0 {
				t.Fatalf("%s = %s %+v, want width %d", name, format, config, wantWidth)
			}
		}
	}
}

// TestTicket47CheckPassesTwiceInOneProcess is the repeat-run regression:
// acceptance at 960f22b1 saw run 1 PASS and run 2 FAIL at
// disconnect-and-retry, because the registered steps kept run 1's opened
// flags, tunnel counters, and a closed panel, so Retry never opened a fresh
// tunnel. Every run now rebuilds its seams from resetRunState.
func TestTicket47CheckPassesTwiceInOneProcess(t *testing.T) {
	tester, _ := newTicket16Tester(t)
	first := runTicket47(t, tester, true)
	if !first.Pass {
		t.Fatalf("first run failed: %+v", first)
	}

	// Match the real shortcut: F11 closes the finished overlay, F11 opens the
	// menu again, then 4 and 7 run the check a second time.
	tester.Key(0, ui.KeyF11)
	tester.Frame()
	tester.Key(0, ui.KeyF11)
	tester.Frame()
	second := runTicket47(t, tester, false)
	if !second.Pass || len(second.Errors) != 0 {
		t.Fatalf("second run failed: pass=%v errors=%v steps=%+v",
			second.Pass, second.Errors, second.Steps)
	}
	seen := map[string]bool{}
	for _, step := range second.Steps {
		seen[step.Name] = step.Pass
	}
	for _, name := range []string{"open-session", "open-browser", "navigate-local",
		"navigate-public-and-controls", "tabs", "disconnect-and-retry",
		"render-and-capture", "close-and-release"} {
		if !seen[name] {
			t.Fatalf("second run did not pass %q: %+v", name, second.Steps)
		}
	}
	if !tester.HasText("PASS  47") {
		t.Fatalf("the overlay does not show PASS 47 after the second run: %v", tester.Texts())
	}
}
