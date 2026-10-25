//go:build selftest

package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/selftest"
)

// runTicket42 presses 4 and 2, advances the harness to completion, and asserts
// a passing four-step result. openMenu presses F11 first. The device-deadline
// step waits the real 30 seconds, so the frame loop yields between advances.
func runTicket42(t *testing.T, tester *ui.Tester, openMenu bool) selftest.Result {
	t.Helper()
	if openMenu {
		tester.Key(0, ui.KeyF11)
		tester.Frame()
	}
	tester.Key(0, ui.Key4)
	tester.Frame()
	tester.Key(0, ui.Key2)
	tester.Frame()

	deadline := time.Now().Add(90 * time.Second)
	for activeSelftest.State() == selftest.StateRunning && time.Now().Before(deadline) {
		tester.Advance(selftestStepPoll)
		if activeSelftest.State() == selftest.StateRunning {
			time.Sleep(2 * time.Millisecond)
		}
	}
	result, ok := activeSelftest.LastResult()
	if !ok {
		t.Fatalf("the check did not finish: state = %v", activeSelftest.State())
	}
	if !result.Pass {
		for _, step := range result.Steps {
			t.Logf("step %s pass=%v detail=%s err=%s", step.Name, step.Pass, step.Detail, step.Error)
		}
		t.Fatalf("ticket 42 self-test failed: %v", result.Errors)
	}
	if len(result.Steps) != 4 {
		t.Fatalf("steps = %d, want 4: %+v", len(result.Steps), result.Steps)
	}
	for _, step := range result.Steps {
		t.Logf("%s: %s (%s)", step.Name, step.Detail, step.Seam)
	}
	return result
}

// TestTicket42CheckRunsThroughTheKeyboard drives F11,4,2 from the initial Vault
// view. Every step uses an in-process agent and SSH server; no hardware or
// network is required.
func TestTicket42CheckRunsThroughTheKeyboard(t *testing.T) {
	tester, dir := newTicket16Tester(t)
	tester.Key(0, ui.KeyF11)
	tester.Frame()
	if !tester.HasText("Self-test") {
		t.Fatalf("F11 did not open the overlay: %v", tester.Texts())
	}
	result := runTicket42(t, tester, false)

	seams := map[string]bool{}
	for _, step := range result.Steps {
		if step.Seam != "" {
			seams[step.Seam] = true
		}
	}
	for _, want := range []string{
		"adapters/ssh.AgentSecurityKeyAgent + application/connection.SelectSecurityKeys",
		"adapters/ssh.ParseSecurityKeySignature + PROTOCOL.u2f framing + in-process fake agent",
		"adapters/ssh.Dialer + authMachine + adapters/sshtest/fakessh",
		"application/connection.SecurityKeyDeviceDeadline",
	} {
		if !seams[want] {
			t.Fatalf("seams = %v, missing %q", seams, want)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "natcatty-selftest-42.json")); err != nil {
		t.Fatalf("result file: %v", err)
	}
	if !tester.HasText("PASS  42") {
		t.Fatalf("the overlay does not show PASS: %v", tester.Texts())
	}
}

// TestTicket42CheckPassesTwiceInOneProcess proves the fake agent connections
// and SSH server are closed, so a repeated F11,4,2 starts clean.
func TestTicket42CheckPassesTwiceInOneProcess(t *testing.T) {
	tester, dir := newTicket16Tester(t)
	first := runTicket42(t, tester, true)
	if !first.Pass {
		t.Fatalf("first run failed: %+v", first)
	}

	tester.Key(0, ui.KeyF11)
	tester.Frame()
	tester.Key(0, ui.KeyF11)
	tester.Frame()
	second := runTicket42(t, tester, false)
	for _, step := range second.Steps {
		if !step.Pass {
			t.Fatalf("second-run step %q failed: %+v", step.Name, second.Steps)
		}
	}
	if !tester.HasText("PASS  42") {
		t.Fatalf("the overlay does not show PASS after the second run: %v", tester.Texts())
	}
	if _, err := os.Stat(filepath.Join(dir, "natcatty-selftest-42.json")); err != nil {
		t.Fatalf("result file: %v", err)
	}
}
