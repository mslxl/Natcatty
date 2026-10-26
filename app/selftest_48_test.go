//go:build selftest

package app

import (
	"image"
	_ "image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/selftest"
)

// runTicket48 presses 4 and 8, advances the harness to completion, and
// asserts a passing eight-step result. openMenu presses F11 first.
func runTicket48(t *testing.T, tester *ui.Tester, openMenu bool) selftest.Result {
	t.Helper()
	if openMenu {
		tester.Key(0, ui.KeyF11)
		tester.Frame()
	}
	tester.Key(0, ui.Key4)
	tester.Frame()
	tester.Key(0, ui.Key8)
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
		t.Fatalf("ticket 48 self-test failed: %v", result.Errors)
	}
	if len(result.Steps) != 8 {
		t.Fatalf("steps = %d, want 8: %+v", len(result.Steps), result.Steps)
	}
	return result
}

// TestTicket48CheckRunsThroughTheKeyboard drives F11,4,8 from the production
// window wiring. The origin, foreign server, tunnel, bookmarks, icons, and
// downloads are all in-process; no internet, WebView, Vault record, or real
// Downloads directory is touched.
func TestTicket48CheckRunsThroughTheKeyboard(t *testing.T) {
	tester, dir := newTicket16Tester(t)
	tester.Key(0, ui.KeyF11)
	tester.Frame()
	if !tester.HasText("Self-test") {
		t.Fatalf("F11 did not open the overlay: %v", tester.Texts())
	}
	result := runTicket48(t, tester, false)
	for _, step := range result.Steps {
		t.Logf("%s: %s (%s)", step.Name, step.Detail, step.Seam)
		if step.Detail == "" {
			t.Errorf("step %s recorded no detail", step.Name)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "natcatty-selftest-48.json")); err != nil {
		t.Fatalf("result file: %v", err)
	}
	shots := filepath.Join(dir, "natcatty-selftest-48-shots")
	entries, err := os.ReadDir(shots)
	if err != nil {
		t.Fatalf("screenshot directory: %v", err)
	}
	if len(entries) != 10 {
		t.Fatalf("screenshots = %d, want 10", len(entries))
	}
	for _, entry := range entries {
		file, err := os.Open(filepath.Join(shots, entry.Name()))
		if err != nil {
			t.Fatalf("open %s: %v", entry.Name(), err)
		}
		config, format, err := image.DecodeConfig(file)
		_ = file.Close()
		if err != nil {
			t.Fatalf("decode %s: %v", entry.Name(), err)
		}
		wantWidth, wantHeight := 868, 688
		if filepath.Base(entry.Name()) == "bookmarks-long-en-440x380.png" ||
			filepath.Base(entry.Name()) == "bookmarks-long-zh-CN-440x380.png" ||
			filepath.Base(entry.Name()) == "blocked-typed-error-en-440x380.png" {
			wantWidth, wantHeight = 440, 380
		}
		if format != "png" || config.Width != wantWidth || config.Height != wantHeight {
			t.Fatalf("%s = %s %dx%d, want %dx%d", entry.Name(), format,
				config.Width, config.Height, wantWidth, wantHeight)
		}
	}
}

// TestTicket48CheckPassesTwiceInOneProcess proves resetRunState clears the
// servers, service instances, tunnel counters, page set, captured screenshots,
// and stage flags, so the same registered steps run a second time.
func TestTicket48CheckPassesTwiceInOneProcess(t *testing.T) {
	tester, _ := newTicket16Tester(t)
	first := runTicket48(t, tester, true)
	if !first.Pass {
		t.Fatalf("first run failed: %+v", first)
	}

	// Match the real shortcut: F11 closes the finished overlay, F11 opens the
	// menu again, then 4 and 8 run the check a second time.
	tester.Key(0, ui.KeyF11)
	tester.Frame()
	tester.Key(0, ui.KeyF11)
	tester.Frame()
	second := runTicket48(t, tester, false)
	if !second.Pass || len(second.Errors) != 0 {
		t.Fatalf("second run failed: pass=%v errors=%v steps=%+v",
			second.Pass, second.Errors, second.Steps)
	}
	seen := map[string]bool{}
	for _, step := range second.Steps {
		seen[step.Name] = step.Pass
	}
	for _, name := range []string{"open-panel", "bookmarks-add-edit",
		"bookmarks-persist-and-scope", "favicon", "downloads", "sandbox",
		"render-and-capture", "close-and-release"} {
		if !seen[name] {
			t.Fatalf("second run did not pass %q: %+v", name, second.Steps)
		}
	}
	if !tester.HasText("PASS  48") {
		t.Fatalf("the overlay does not show PASS 48 after the second run: %v", tester.Texts())
	}
}
