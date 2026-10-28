//go:build selftest

package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	appmcp "github.com/mslxl/natcatty/application/mcp"
	"github.com/mslxl/natcatty/ui/selftest"
)

// runTicket58 presses 5 and 8, advances the harness until the background write
// and security scenario completes, and asserts a passing five-step result.
// openMenu presses F11 first.
func runTicket58(t *testing.T, tester *ui.Tester, openMenu bool) selftest.Result {
	t.Helper()
	if openMenu {
		tester.Key(0, ui.KeyF11)
		tester.Frame()
	}
	tester.Key(0, ui.Key5)
	tester.Frame()
	tester.Key(0, ui.Key8)
	tester.Frame()

	// The check itself bounds the scenario at 90s. The loop waits on the harness
	// state, never on a fixed delay.
	deadline := time.Now().Add(4 * time.Minute)
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
		t.Fatalf("ticket 58 self-test failed: %v", result.Errors)
	}
	if len(result.Steps) != 5 {
		t.Fatalf("steps = %d, want 5: %+v", len(result.Steps), result.Steps)
	}
	for _, step := range result.Steps {
		t.Logf("%s: %s (%s)", step.Name, step.Detail, step.Seam)
	}
	return result
}

// TestTicket58CheckRunsThroughTheKeyboard drives F11,5,8. The check composes
// the real local shell, SQLite Vault and secret store, policy, handler, write
// executor, and injected approval clock.
func TestTicket58CheckRunsThroughTheKeyboard(t *testing.T) {
	tester, dir := newTicket16Tester(t)
	tester.Key(0, ui.KeyF11)
	tester.Frame()
	if !tester.HasText("Self-test") {
		t.Fatalf("F11 did not open the overlay: %v", tester.Texts())
	}

	result := runTicket58(t, tester, false)
	for _, step := range result.Steps {
		if step.Detail == "" {
			t.Fatalf("step %q has no detail: %+v", step.Name, result.Steps)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "natcatty-selftest-58.json")); err != nil {
		t.Fatalf("result file: %v", err)
	}
	if !tester.HasText("PASS  58") {
		t.Fatalf("the overlay does not show PASS: %v", tester.Texts())
	}
}

// TestTicket58CheckPassesTwiceInOneProcess proves the reset-at-entry path and
// that the local shell, SQLite Vault, secret store, job manager, and injected
// clock are all released between runs.
func TestTicket58CheckPassesTwiceInOneProcess(t *testing.T) {
	tester, dir := newTicket16Tester(t)
	first := runTicket58(t, tester, true)
	if !first.Pass {
		t.Fatalf("first run failed: %+v", first)
	}

	tester.Key(0, ui.KeyF11)
	tester.Frame()
	tester.Key(0, ui.KeyF11)
	tester.Frame()
	second := runTicket58(t, tester, false)
	for _, step := range second.Steps {
		if !step.Pass {
			t.Fatalf("second-run step %q failed: %+v", step.Name, second.Steps)
		}
	}
	if !tester.HasText("PASS  58") {
		t.Fatalf("the overlay does not show PASS after the second run: %v", tester.Texts())
	}
	if _, err := os.Stat(filepath.Join(dir, "natcatty-selftest-58.json")); err != nil {
		t.Fatalf("result file: %v", err)
	}
}

// TestTicket58CheckPassesTwiceAfterACommandTimeout reproduces the acceptance's
// repeatability failure: a real command timeout happens in the process first,
// and then the check passes twice in a row. Each check still causes its own
// internal timeout; the point is that the second run starts from a reset
// scenario and a shell that answers again, not from the first run's state.
func TestTicket58CheckPassesTwiceAfterACommandTimeout(t *testing.T) {
	fixture := newMCPWriteFixture(t)
	shell, _ := fixture.sessions.ShellKind(fixture.localSession)
	warm := callMCPTool(t, fixture, "terminal_execute", map[string]any{
		"sessionId": string(fixture.localSession),
		"command":   shellReadyCommand(shell, "after-timeout-warm"),
	})
	requireMCPWriteOK(t, "terminal_execute", warm)
	previousTimeout := fixture.executor.toolTimeout
	fixture.executor.toolTimeout = time.Second
	timedOut := callMCPTool(t, fixture, "terminal_execute", map[string]any{
		"sessionId": string(fixture.localSession),
		"command":   shellCommandFor(shell, "before-timeout", selftest58SlowCommand(shell)),
	})
	requireMCPWriteCode(t, "terminal_execute", timedOut, appmcp.CodeCommandTimeout)
	if stdout, _ := timedOut["stdout"].(string); !strings.Contains(stdout, "before-timeout") {
		t.Fatalf("timeout partial stdout = %q", stdout)
	}
	fixture.executor.toolTimeout = previousTimeout
	// The same Session must be reusable immediately after the timeout; the
	// acceptance's repeatability failure started with this state.
	recovered := callMCPTool(t, fixture, "terminal_execute", map[string]any{
		"sessionId": string(fixture.localSession),
		"command":   shellReadyCommand(shell, "after-timeout-recovered"),
	})
	requireMCPWriteOK(t, "terminal_execute", recovered)
	if stdout, _ := recovered["stdout"].(string); !strings.Contains(stdout, "after-timeout-recovered") {
		t.Fatalf("post-timeout stdout = %q", stdout)
	}

	tester, dir := newTicket16Tester(t)
	first := runTicket58(t, tester, true)
	if !first.Pass {
		t.Fatalf("first run after a timeout failed: %+v", first)
	}

	tester.Key(0, ui.KeyF11)
	tester.Frame()
	tester.Key(0, ui.KeyF11)
	tester.Frame()
	second := runTicket58(t, tester, false)
	if !second.Pass {
		t.Fatalf("second run after a timeout failed: %+v", second)
	}
	if !tester.HasText("PASS  58") {
		t.Fatalf("the overlay does not show PASS after the second run: %v", tester.Texts())
	}
	if _, err := os.Stat(filepath.Join(dir, "natcatty-selftest-58.json")); err != nil {
		t.Fatalf("result file: %v", err)
	}
}
