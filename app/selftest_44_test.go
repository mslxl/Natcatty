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

// runTicket44 presses 4 and 4, advances the harness to completion, and returns
// the result. openMenu presses F11 first.
func runTicket44(t *testing.T, tester *ui.Tester, openMenu bool) selftest.Result {
	t.Helper()
	if openMenu {
		tester.Key(0, ui.KeyF11)
		tester.Frame()
	}
	tester.Key(0, ui.Key4)
	tester.Frame()
	tester.Key(0, ui.Key4)
	tester.Frame()

	deadline := time.Now().Add(45 * time.Second)
	for activeSelftest.State() == selftest.StateRunning && time.Now().Before(deadline) {
		tester.Advance(selftestStepPoll)
	}
	result, ok := activeSelftest.LastResult()
	if !ok {
		t.Fatalf("the check did not finish: state = %v", activeSelftest.State())
	}
	return result
}

// TestTicket44CheckRunsThroughTheKeyboard drives F11,4,4 from the production
// window wiring. Every Host is in-process; no network or hardware is needed.
func TestTicket44CheckRunsThroughTheKeyboard(t *testing.T) {
	tester, dir := newTicket16Tester(t)
	tester.Key(0, ui.KeyF11)
	tester.Frame()
	if !tester.HasText("Self-test") {
		t.Fatalf("F11 did not open the overlay: %v", tester.Texts())
	}
	result := runTicket44(t, tester, false)
	if !result.Pass {
		for _, step := range result.Steps {
			t.Logf("step %s pass=%v detail=%s err=%s", step.Name, step.Pass, step.Detail, step.Error)
		}
		t.Fatalf("ticket 44 self-test failed: %v", result.Errors)
	}
	if len(result.Steps) != 5 {
		t.Fatalf("steps = %d, want 5: %+v", len(result.Steps), result.Steps)
	}
	for _, step := range result.Steps {
		t.Logf("%s: %s (%s)", step.Name, step.Detail, step.Seam)
		if step.Detail == "" {
			t.Errorf("step %s recorded no detail", step.Name)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "natcatty-selftest-44.json")); err != nil {
		t.Fatalf("result file: %v", err)
	}
	shots := filepath.Join(dir, "natcatty-selftest-44-shots")
	entries, err := os.ReadDir(shots)
	if err != nil {
		t.Fatalf("screenshot directory: %v", err)
	}
	if len(entries) != 16 {
		t.Fatalf("screenshots = %d, want 16", len(entries))
	}
	var names []string
	for _, variant := range []string{"1100x720-en", "1100x720-zh-CN", "720x480-en", "720x480-zh-CN"} {
		for _, kind := range []string{"list", "editor", "columns", "charts"} {
			names = append(names, "monitor-"+variant+"-"+kind+".png")
		}
	}
	for _, name := range names {
		file, err := os.Open(filepath.Join(shots, name))
		if err != nil {
			t.Fatalf("open %s: %v", name, err)
		}
		config, format, err := image.DecodeConfig(file)
		_ = file.Close()
		if err != nil {
			t.Fatalf("decode %s: %v", name, err)
		}
		if format != "png" || config.Width <= 0 || config.Height <= 0 {
			t.Fatalf("%s = %s %+v", name, format, config)
		}
		wantWidth := 1100
		if strings.Contains(name, "720x480") {
			wantWidth = 720
		}
		if config.Width != wantWidth {
			t.Fatalf("%s width = %d, want %d", name, config.Width, wantWidth)
		}
	}
}

// TestTicket44CheckPassesTwiceInOneProcess is the repeat-run regression: the
// harness registers one set of steps, so the second F11,4,4 run must reset the
// checker's configuration flag, service, surface, deadlines, and the fake
// transport's sample counters. Acceptance at 9f233a32 saw run 1 PASS and run 2
// FAIL: configure-panel reported the run-1 text without configuring, then
// collect-metrics reused the past run-1 deadline against the closed service and
// timed out waiting for live samples.
func TestTicket44CheckPassesTwiceInOneProcess(t *testing.T) {
	tester, _ := newTicket16Tester(t)
	first := runTicket44(t, tester, true)
	if !first.Pass || len(first.Errors) != 0 {
		t.Fatalf("first run failed: pass=%v errors=%v steps=%+v",
			first.Pass, first.Errors, first.Steps)
	}
	if len(first.Steps) != 5 {
		t.Fatalf("first run steps = %d, want 5: %+v", len(first.Steps), first.Steps)
	}

	// Match the real shortcut: F11 closes the finished overlay, F11 opens the
	// menu again, then 4 and 4 run the check a second time.
	tester.Key(0, ui.KeyF11)
	tester.Frame()
	tester.Key(0, ui.KeyF11)
	tester.Frame()
	second := runTicket44(t, tester, false)
	if !second.Pass || len(second.Errors) != 0 {
		t.Fatalf("second run failed: pass=%v errors=%v steps=%+v",
			second.Pass, second.Errors, second.Steps)
	}
	seen := map[string]bool{}
	for _, step := range second.Steps {
		seen[step.Name] = step.Pass
	}
	for _, name := range []string{"configure-panel", "collect-metrics", "render-and-capture",
		"host-key-gate", "stop-and-release"} {
		if !seen[name] {
			t.Fatalf("second run did not pass %q: %+v", name, second.Steps)
		}
	}
	if !tester.HasText("PASS  44") {
		t.Fatalf("the overlay does not show PASS 44 after the second run: %v", tester.Texts())
	}
}
