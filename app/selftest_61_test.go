//go:build selftest

package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/selftest"
)

// TestSelftest61PassesTwiceInOneProcess is the ticket 61 repeatability check.
// The harness keeps one registered set of steps, so a second F11,6,1 in the
// same process must rebuild every fixture after the reset step rather than
// reuse a closed service or a consumed deadline. Running the same step slice
// twice here catches that regression without opening a real window.
func TestSelftest61PassesTwiceInOneProcess(t *testing.T) {
	run := &selftest61{}
	check := run.check()
	for pass := 1; pass <= 2; pass++ {
		for _, step := range check.Steps {
			outcome := step.Run(nil)
			if outcome.Waiting {
				t.Fatalf("pass %d step %q waited; every ticket 61 step is synchronous", pass, step.Name)
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

// TestForwardingEditorFieldsStayInsideTheRealClient is the ticket 61 rework
// regression for the Port Forwarding editor. It renders the production shell
// (with its 236-DIP sidebar) at the real client sizes and asserts every text
// input's label and box (Label, Bind address, Local port, Destination host,
// Destination port) is inside the client, no input covers its label, and
// 1084x661 keeps the two-column look. The pre-fix fixed-width rows put the en
// Local port input at x781..901 and the zh-CN Destination port at x612..732
// against a 704-wide client.
func TestForwardingEditorFieldsStayInsideTheRealClient(t *testing.T) {
	for _, size := range []struct {
		width, height int
		pairedRows    bool
	}{
		{704, 421, false},
		{1084, 661, true},
	} {
		for _, locale := range []string{"en", "zh-CN"} {
			tester, context, forwardingState, cleanup, err := renderSelftest61ForwardingEditor(
				size.width, size.height, locale)
			if err != nil {
				t.Fatalf("%dx%d %s: build the fixture: %v", size.width, size.height, locale, err)
			}
			err = selftest61ForwardingWidth(tester, context, forwardingState,
				size.width, size.height, size.pairedRows, locales.In(locale))
			cleanup()
			if err != nil {
				t.Fatalf("%dx%d %s: %v", size.width, size.height, locale, err)
			}
		}
	}
}

// TestTicket61FirstCheckInAFreshProcess covers ticket 61 as the first keyboard
// check of a brand-new harness whose per-process result directory does not
// exist yet: F11,6,1 runs all nine steps before any result write can create
// the directory. The default directory keeps its documented per-PID suffix
// because the isolation step also asserts it.
func TestTicket61FirstCheckInAFreshProcess(t *testing.T) {
	temp := t.TempDir()
	t.Setenv("TEMP", temp)
	t.Setenv("TMP", temp)
	dir := selftest.DefaultResultDir()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the fixture result directory %q already exists: %v", dir, err)
	}
	savedDir := selftestResultDir
	selftestResultDir = dir
	t.Cleanup(func() { selftestResultDir = savedDir })

	registry := selftest.NewRegistry()
	registry.Register((&selftest61{}).check())
	harness := selftest.NewHarness(registry, selftest.WithResultDir(dir))
	view := harness.View(func(c *ui.Context) { ui.Text(c, "content") })
	tester := ui.NewTester(func(c *ui.Context) {
		harness.Bind(c)
		view(c)
	}, 1100, 720)
	tester.SetLocale("en")
	tester.Frame()

	tester.Key(0, ui.KeyF11)
	tester.Frame()
	tester.Key(0, ui.Key6)
	tester.Frame()
	tester.Key(0, ui.Key1)
	tester.Frame()

	deadline := time.Now().Add(60 * time.Second)
	for harness.Running() && time.Now().Before(deadline) {
		tester.Advance(selftestStepPoll)
	}
	result, ok := harness.LastResult()
	if !ok || !result.Pass || len(result.Errors) != 0 {
		t.Fatalf("the first ticket 61 check in a fresh process failed: %+v (%v)", result, ok)
	}
	if len(result.Steps) != 9 {
		t.Fatalf("steps = %d, want 9: %+v", len(result.Steps), result.Steps)
	}
	if _, err := os.Stat(filepath.Join(dir, "natcatty-selftest-61.json")); err != nil {
		t.Fatalf("the first check did not write its result below %q: %v", dir, err)
	}
}
