package session

import (
	"strings"
	"testing"
	"time"

	domainhistory "github.com/mslxl/natcatty/domain/history"
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
)

// hookHandshake builds the prompt sequence the hook emits before OSC 133 A.
func hookHandshake(shell string) string {
	return "\x1b]1337;NetcattyHook;" + shell + "\x07"
}

// A shell that emits its own OSC 133 prompt mark (fish does) must not finalize
// the currently typed line while the hook owns capture: the hook's private
// preexec sequence is the authoritative start.
func TestTapHookHandshakeSuppressesATypedPromptFinalize(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	output, effects := tap.observe([]byte(hookHandshake("fish")), tapAt(0))
	if len(output) != 0 {
		t.Fatalf("the handshake reached the renderer: %q", output)
	}
	if len(effects.Completions) != 0 {
		t.Fatalf("the handshake completed a command: %+v", effects.Completions)
	}
	if !tap.state(tapAt(0)).HookActive {
		t.Fatal("the tap did not mark the hook active")
	}

	// The user types a command. The shell's own prompt mark can arrive before
	// the command executes; it must not produce a fallback row.
	if _, _, _, completions, _ := tap.handleInput([]byte("echo once\r"), tapAt(time.Second)); len(completions) != 0 {
		t.Fatalf("typing completed a command: %+v", completions)
	}
	_, effects = tap.observe([]byte("\x1b]133;A\x07"), tapAt(1500*time.Millisecond))
	if len(effects.Completions) != 0 {
		t.Fatalf("a native prompt mark finalized the typed line: %+v", effects.Completions)
	}

	// The hook's own preexec sequence starts the command, and its D finishes it
	// exactly once as Tier 1.
	_, effects = tap.observe([]byte(netcattyExec("fish", "/work", "echo once", false)), tapAt(2*time.Second))
	if len(effects.Completions) != 0 {
		t.Fatalf("the private sequence completed early: %+v", effects.Completions)
	}
	_, effects = tap.observe([]byte("\x1b]133;D;0\x07"), tapAt(3*time.Second))
	if len(effects.Completions) != 1 {
		t.Fatalf("completions = %+v, want one", effects.Completions)
	}
	if completion := effects.Completions[0]; completion.command != "echo once" ||
		completion.captureTier != domainhistory.CaptureTierHook {
		t.Fatalf("completion = %+v", completion)
	}
}

// A multiline command is one shell execution. Typed Enter between its lines
// must not emit a partial row; the private sequence carries the whole command
// and D emits it once.
func TestTapHookHandshakeSuppressesMultilinePartials(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	tap.observe([]byte(hookHandshake("bash")), tapAt(0))

	if _, _, _, completions, _ := tap.handleInput([]byte("echo one \\\r"), tapAt(time.Second)); len(completions) != 0 {
		t.Fatalf("the first line completed a command: %+v", completions)
	}
	if _, _, _, completions, _ := tap.handleInput([]byte("echo two\r"), tapAt(2*time.Second)); len(completions) != 0 {
		t.Fatalf("the second line completed a partial command: %+v", completions)
	}

	full := "echo one echo two"
	_, effects := tap.observe([]byte(netcattyExec("bash", "/work", full, false)), tapAt(3*time.Second))
	if len(effects.Completions) != 0 {
		t.Fatalf("the private sequence completed early: %+v", effects.Completions)
	}
	_, effects = tap.observe([]byte("\x1b]133;D;0\x07"), tapAt(4*time.Second))
	if len(effects.Completions) != 1 {
		t.Fatalf("completions = %+v, want one full command", effects.Completions)
	}
	completion := effects.Completions[0]
	if completion.command != full || completion.captureTier != domainhistory.CaptureTierHook {
		t.Fatalf("completion = %+v", completion)
	}
	if strings.Contains(completion.command, "\\") {
		t.Fatalf("the captured command kept a line continuation: %q", completion.command)
	}
}

// A transport that withdraws the planned hook must release the first typed
// command. Otherwise the tap would keep it tentative until Session close and
// ignore the shell's own prompt marks.
func TestTapWithdrawCaptureHookReleasesTheReservedCommand(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	tap.hookExpected = true

	if _, _, _, completions, _ := tap.handleInput([]byte("echo plain\r"), tapAt(0)); len(completions) != 0 {
		t.Fatalf("Enter completed the command early: %+v", completions)
	}
	if tap.pending == nil || !tap.pending.awaitingHook {
		t.Fatalf("the planned hook did not reserve the command: %+v", tap.pending)
	}

	tap.withdrawCaptureHook()
	if tap.pending.awaitingHook {
		t.Fatal("the withdrawn hook still reserves the command")
	}
	_, effects := tap.observe([]byte("echo plain\r\nresult\r\n$ "), tapAt(time.Second))
	if len(effects.Completions) != 1 {
		t.Fatalf("completions = %+v, want the typed fallback", effects.Completions)
	}
	if completion := effects.Completions[0]; completion.command != "echo plain" ||
		completion.captureTier != domainhistory.CaptureTierFallback {
		t.Fatalf("completion = %+v", completion)
	}
}

// A shell whose startup is not the planned capture hook still records its
// commands. The Lens zsh integration draws native marks (D, A, OSC 7, B) but
// never the hook's handshake, because it replaces the planned ZDOTDIR; when the
// tap kept reserving every typed command for a handshake that could not come,
// the command reached history only when the Session closed, so the Tab
// completion popup never had a row to accept while the user was typing.
func TestTapPromptWithoutAHandshakeStopsWaitingForTheHook(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	tap.hookExpected = true

	prompt := "\x1b]133;D;0\x07\x1b]133;A\x07\x1b]7;file://host/work\x07\x1b]133;B\x07"
	_, effects := tap.observe([]byte(prompt), tapAt(0))
	if len(effects.Completions) != 0 {
		t.Fatalf("the startup prompt completed a command: %+v", effects.Completions)
	}
	if tap.hookExpected || tap.hookActive {
		t.Fatalf("the tap still waits for the hook: expected=%v active=%v", tap.hookExpected, tap.hookActive)
	}

	if _, _, _, completions, _ := tap.handleInput([]byte("git status\r"), tapAt(time.Second)); len(completions) != 0 {
		t.Fatalf("Enter published the command: %+v", completions)
	}
	execution := "\x1b]133;C;git%20status;git%20status\x07"
	if _, effects = tap.observe([]byte(execution), tapAt(1500*time.Millisecond)); len(effects.Completions) != 0 {
		t.Fatalf("the execution mark completed the command: %+v", effects.Completions)
	}
	_, effects = tap.observe([]byte("\x1b]133;D;0\x07"), tapAt(2*time.Second))
	if len(effects.Completions) != 1 {
		t.Fatalf("completions = %+v, want one at the next prompt", effects.Completions)
	}
	completion := effects.Completions[0]
	if completion.command != "git status" ||
		completion.captureTier != domainhistory.CaptureTierNativeOSC ||
		!completion.hasExitCode || completion.exitCode != 0 {
		t.Fatalf("completion = %+v", completion)
	}
}

// The handshake comes before the prompt mark in every installed hook, so an
// announced hook keeps its authority: a prompt after the handshake must not
// clear the expectation or fall back to native capture.
func TestTapKeepsTheHookAnnouncedBeforeThePrompt(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	tap.hookExpected = true

	if _, effects := tap.observe([]byte(hookHandshake("zsh")), tapAt(0)); len(effects.Completions) != 0 {
		t.Fatalf("the handshake completed a command: %+v", effects.Completions)
	}
	tap.observe([]byte("\x1b]133;A\x07"), tapAt(time.Second))
	if !tap.hookActive || !tap.hookSeen {
		t.Fatalf("the announced hook was dropped: active=%v seen=%v", tap.hookActive, tap.hookSeen)
	}
}
