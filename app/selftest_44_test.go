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

// TestTicket44CheckRunsThroughTheKeyboard drives F11,4,4 from the production
// window wiring. Every Host is in-process; no network or hardware is needed.
func TestTicket44CheckRunsThroughTheKeyboard(t *testing.T) {
	tester, dir := newTicket16Tester(t)
	tester.Key(0, ui.KeyF11)
	tester.Frame()
	if !tester.HasText("Self-test") {
		t.Fatalf("F11 did not open the overlay: %v", tester.Texts())
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
	if len(entries) != 4 {
		t.Fatalf("screenshots = %d, want 4", len(entries))
	}
	for _, name := range []string{
		"monitor-1100x720-en.png",
		"monitor-1100x720-zh-CN.png",
		"monitor-720x480-en.png",
		"monitor-720x480-zh-CN.png",
	} {
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
