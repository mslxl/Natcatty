//go:build selftest

package app

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/selftest"
)

// TestTicket60CheckPassesTwiceInOneProcess runs the registered release steps
// twice in one process. Every step re-reads the changelog, the parity matrix,
// and the themes after the reset step, so the second pass cannot reuse state.
func TestTicket60CheckPassesTwiceInOneProcess(t *testing.T) {
	run := &selftest60{}
	check := run.check()
	for pass := 1; pass <= 2; pass++ {
		for _, step := range check.Steps {
			outcome := step.Run(nil)
			if outcome.Waiting {
				t.Fatalf("pass %d step %q waited; a ticket 60 step is synchronous", pass, step.Name)
			}
			if !outcome.Pass {
				t.Fatalf("pass %d step %q failed: %v", pass, step.Name, outcome.Err)
			}
		}
		if run.run != pass {
			t.Fatalf("after pass %d the run counter = %d", pass, run.run)
		}
	}
}

// TestTicket60CheckRunsThroughTheKeyboard drives the real F11,6,0 shortcut
// twice in one process and reads the machine-readable result.
func TestTicket60CheckRunsThroughTheKeyboard(t *testing.T) {
	temp := t.TempDir()
	t.Setenv("TEMP", temp)
	t.Setenv("TMP", temp)
	dir := selftest.DefaultResultDir()
	savedDir := selftestResultDir
	selftestResultDir = dir
	t.Cleanup(func() { selftestResultDir = savedDir })

	registry := selftest.NewRegistry()
	registry.Register((&selftest60{}).check())
	harness := selftest.NewHarness(registry, selftest.WithResultDir(dir))
	view := harness.View(func(c *ui.Context) { ui.Text(c, "content") })
	tester := ui.NewTester(func(c *ui.Context) {
		harness.Bind(c)
		view(c)
	}, 1100, 720)
	tester.SetLocale("en")
	tester.Frame()

	for pass := 1; pass <= 2; pass++ {
		// The overlay stays up after a finished run, so a second run first
		// closes it (F11) and then opens the menu again (F11).
		if harness.State() != selftest.StateHidden {
			tester.Key(0, ui.KeyF11)
			tester.Frame()
		}
		tester.Key(0, ui.KeyF11)
		tester.Frame()
		tester.Key(0, ui.Key6)
		tester.Frame()
		tester.Key(0, ui.Key0)
		tester.Frame()

		deadline := time.Now().Add(60 * time.Second)
		for harness.Running() && time.Now().Before(deadline) {
			tester.Advance(selftestStepPoll)
		}
		result, ok := harness.LastResult()
		if !ok || !result.Pass || len(result.Errors) != 0 {
			t.Fatalf("pass %d failed: %+v (%v)", pass, result, ok)
		}
		if len(result.Steps) != 4 {
			t.Fatalf("pass %d ran %d steps, want 4: %+v", pass, len(result.Steps), result.Steps)
		}
		wantReset := fmt.Sprintf("run %d starts a fresh release inspection", pass)
		if got := result.Steps[0].Detail; got != wantReset {
			t.Fatalf("pass %d reset detail = %q, want %q", pass, got, wantReset)
		}
		if _, err := os.Stat(filepath.Join(dir, "natcatty-selftest-60.json")); err != nil {
			t.Fatalf("pass %d did not write its result file: %v", pass, err)
		}
	}
}
