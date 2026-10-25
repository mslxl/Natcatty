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

// runTicket45 presses 4 and 5, advances the harness to completion, and
// asserts a passing six-step result. openMenu presses F11 first.
func runTicket45(t *testing.T, tester *ui.Tester, openMenu bool) selftest.Result {
	t.Helper()
	if openMenu {
		tester.Key(0, ui.KeyF11)
		tester.Frame()
	}
	tester.Key(0, ui.Key4)
	tester.Frame()
	tester.Key(0, ui.Key5)
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
		t.Fatalf("ticket 45 self-test failed: %v", result.Errors)
	}
	if len(result.Steps) != 6 {
		t.Fatalf("steps = %d, want 6: %+v", len(result.Steps), result.Steps)
	}
	return result
}

// TestTicket45CheckRunsThroughTheKeyboard drives F11,4,5 from the production
// window wiring. Every Host is in-process; no network or hardware is needed.
func TestTicket45CheckRunsThroughTheKeyboard(t *testing.T) {
	tester, dir := newTicket16Tester(t)
	tester.Key(0, ui.KeyF11)
	tester.Frame()
	if !tester.HasText("Self-test") {
		t.Fatalf("F11 did not open the overlay: %v", tester.Texts())
	}
	result := runTicket45(t, tester, false)
	for _, step := range result.Steps {
		t.Logf("%s: %s (%s)", step.Name, step.Detail, step.Seam)
		if step.Detail == "" {
			t.Errorf("step %s recorded no detail", step.Name)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "natcatty-selftest-45.json")); err != nil {
		t.Fatalf("result file: %v", err)
	}
	shots := filepath.Join(dir, "natcatty-selftest-45-shots")
	entries, err := os.ReadDir(shots)
	if err != nil {
		t.Fatalf("screenshot directory: %v", err)
	}
	if len(entries) != 16 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("screenshots = %d, want 16: %v", len(entries), names)
	}
	for _, tab := range []string{"overview", "processes", "ports", "services"} {
		for _, size := range []struct {
			name       string
			wantWidth  int
			wantHeight int
		}{
			{"1100x720", 1100, 720},
			{"720x480", 720, 480},
		} {
			for _, locale := range []string{"en", "zh-CN"} {
				name := "sysmanager-" + tab + "-" + size.name + "-" + locale + ".png"
				file, err := os.Open(filepath.Join(shots, name))
				if err != nil {
					t.Fatalf("open %s: %v", name, err)
				}
				config, format, err := image.DecodeConfig(file)
				_ = file.Close()
				if err != nil {
					t.Fatalf("decode %s: %v", name, err)
				}
				if format != "png" || config.Width != size.wantWidth ||
					config.Height != size.wantHeight {
					t.Fatalf("%s = %s %+v, want %dx%d", name, format, config,
						size.wantWidth, size.wantHeight)
				}
				if !strings.HasSuffix(name, ".png") {
					t.Fatalf("unexpected screenshot name %s", name)
				}
			}
		}
	}
}

// TestTicket45CheckPassesTwiceInOneProcess is the repeat-run regression: the
// harness registers one set of steps, so the second F11,4,5 run must reset the
// checker's service, surface, stages, and deadlines. Acceptance at a2d30004
// saw run 1 PASS and run 2 FAIL with "confirmation-gate: timed out ... waiting
// for the confirmed restart to finish" because actionStage survived at 3
// against the closed service.
func TestTicket45CheckPassesTwiceInOneProcess(t *testing.T) {
	tester, _ := newTicket16Tester(t)
	first := runTicket45(t, tester, true)
	if !first.Pass {
		t.Fatalf("first run failed: %+v", first)
	}

	// Match the real shortcut: F11 closes the finished overlay, F11 opens the
	// menu again, then 4 and 5 run the check a second time.
	tester.Key(0, ui.KeyF11)
	tester.Frame()
	tester.Key(0, ui.KeyF11)
	tester.Frame()
	second := runTicket45(t, tester, false)
	if !second.Pass || len(second.Errors) != 0 {
		t.Fatalf("second run failed: pass=%v errors=%v steps=%+v",
			second.Pass, second.Errors, second.Steps)
	}
	seen := map[string]bool{}
	for _, step := range second.Steps {
		seen[step.Name] = step.Pass
	}
	for _, name := range []string{"sample-tabs", "confirmation-gate", "degradation",
		"host-key-gate", "render-and-capture", "stop-and-release"} {
		if !seen[name] {
			t.Fatalf("second run did not pass %q: %+v", name, second.Steps)
		}
	}
	if !tester.HasText("PASS  45") {
		t.Fatalf("the overlay does not show PASS 45 after the second run: %v", tester.Texts())
	}
}
