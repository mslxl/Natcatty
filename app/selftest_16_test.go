//go:build selftest

package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/adapters/localpty"
	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
	"github.com/mslxl/natcatty/ui/selftest"
	"github.com/mslxl/natcatty/ui/shell/state"
)

// newTicket16Tester builds the production window wiring with the real local
// shell transport. The terminal surfaces are the test double because the check
// observes delivery through marker files, not through the rendered terminal.
func newTicket16Tester(t *testing.T) (*ui.Tester, string) {
	t.Helper()
	return newTicket16TesterInDir(t, t.TempDir())
}

// newTicket16TesterInDir is newTicket16Tester with dir as the process result
// directory. A test passes a path that does not exist yet to prove the first
// check in a fresh process creates it before a checker writes into it.
func newTicket16TesterInDir(t *testing.T, dir string) (*ui.Tester, string) {
	t.Helper()
	savedDir := selftestResultDir
	selftestResultDir = dir
	t.Cleanup(func() { selftestResultDir = savedDir })

	var shellState *state.State
	service := appsession.NewService(localpty.Factory{},
		// Mirror the composition root: typed input fans out through the shell
		// State's broadcast selection.
		appsession.WithInputHook(func(id model.SessionID, data []byte) {
			if shellState != nil {
				shellState.NoteSessionInput(id, data)
			}
		}),
	)
	t.Cleanup(service.CloseAll)
	shellState = state.NewState(
		state.WithSessions(service, func(*appsession.Session) (state.Surface, error) {
			return &wiringSurface{}, nil
		}),
	)
	host := &fakeWindowHost{}
	manager := newWindowManager(
		host,
		&recordingConfirmer{},
		unlockedGate(t),
		shellState,
		func() func(*ui.Context) {
			return func(c *ui.Context) { ui.Text(c, "settings-content") }
		},
		func() string { return "en" },
	)
	installSelftest(manager, shellState, nil, func([]string, string) {
		manager.SecondInstance()
	})
	t.Cleanup(func() {
		activeSelftest = nil
		shellState.CloseAllSessions()
		service.CloseAll()
	})

	manager.OpenMain()
	window := host.windows[0]
	if window == nil {
		t.Fatal("the main window was not created")
	}

	tester := ui.NewTester(window.spec.Content, 1100, 720)
	tester.SetLocale("en")
	tester.Frame()
	return tester, dir
}

// runTicket16 presses 1 and 6, advances the harness to completion, and asserts
// a passing check. openMenu presses F11 first.
func runTicket16(t *testing.T, tester *ui.Tester, openMenu bool) selftest.Result {
	t.Helper()
	if openMenu {
		tester.Key(0, ui.KeyF11)
		tester.Frame()
	}
	tester.Key(0, ui.Key1)
	tester.Frame()
	tester.Key(0, ui.Key6)
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
		t.Fatalf("ticket 16 self-test failed: %+v", result)
	}
	if len(result.Steps) != 8 {
		t.Fatalf("steps = %d, want 8: %+v", len(result.Steps), result.Steps)
	}
	return result
}

// TestTicket16CheckRunsThroughTheKeyboard drives F11,1,6 from the initial
// Vault view through the production App Lock gate. The broadcast and compose
// steps use three real local shells; the layout step uses the production
// renderer seam.
func TestTicket16CheckRunsThroughTheKeyboard(t *testing.T) {
	tester, dir := newTicket16Tester(t)
	if !tester.HasText("Vault") {
		t.Fatalf("the main window does not start on the Vault view: %v", tester.Texts())
	}
	tester.Key(0, ui.KeyF11)
	tester.Frame()
	if !tester.HasText("Self-test") {
		t.Fatalf("F11 did not open the overlay: %v", tester.Texts())
	}
	result := runTicket16(t, tester, false)

	seams := map[string]bool{}
	for _, step := range result.Steps {
		if step.Seam != "" {
			seams[step.Seam] = true
		}
	}
	for _, want := range []string{"localpty.Factory", "shell State", "shell.RenderSelftestInputs"} {
		if !seams[want] {
			t.Fatalf("seams = %v, missing %q", seams, want)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "natcatty-selftest-16.json")); err != nil {
		t.Fatalf("result file: %v", err)
	}
	if !tester.HasText("PASS  16") {
		t.Fatalf("the overlay does not show PASS: %v", tester.Texts())
	}
}

// TestTicket16CheckPassesTwiceInOneProcess is the repeat-run regression: the
// second run must stop broadcast, close the previous compose state, remove the
// marker files, and close the previous three Tabs before reopening shells.
func TestTicket16CheckPassesTwiceInOneProcess(t *testing.T) {
	tester, dir := newTicket16Tester(t)
	first := runTicket16(t, tester, true)
	if !first.Pass {
		t.Fatalf("first run failed: %+v", first)
	}

	tester.Key(0, ui.KeyF11)
	tester.Frame()
	tester.Key(0, ui.KeyF11)
	tester.Frame()
	second := runTicket16(t, tester, false)

	for _, step := range second.Steps {
		if !step.Pass {
			t.Fatalf("second-run step %q failed: %+v", step.Name, second.Steps)
		}
	}
	if !tester.HasText("PASS  16") {
		t.Fatalf("the overlay does not show PASS after the second run: %v", tester.Texts())
	}
	if _, err := os.Stat(filepath.Join(dir, "natcatty-selftest-16.json")); err != nil {
		t.Fatalf("result file: %v", err)
	}
}
