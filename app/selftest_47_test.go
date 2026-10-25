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
