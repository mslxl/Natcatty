package session

import (
	"testing"
	"time"

	domainhistory "github.com/mslxl/natcatty/domain/history"
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
)

// A line typed while a hook-owned command is still running only waits in the
// terminal input queue. It must not become a fallback capture, must not steal
// the running command's exit code, and the hook's later private sequence must
// produce exactly one real Tier 1 row for it.
func TestTapHookTypeAheadWaitsForTheRunningCommand(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	tap.observe([]byte(hookHandshake("zsh")), tapAt(0))

	tap.handleInput([]byte("sleep 3\r"), tapAt(time.Second))
	tap.observe([]byte(netcattyExec("zsh", "/work", "sleep 3", false)),
		tapAt(time.Second+time.Millisecond))

	// Type-ahead while sleep runs: Enter is observed but starts nothing.
	_, _, started, completions, _ := tap.handleInput([]byte("echo x\r"),
		tapAt(1300*time.Millisecond))
	if started || len(completions) != 0 {
		t.Fatalf("type-ahead started or finished a capture: %t/%+v", started, completions)
	}

	// The running command reports its own exit at its D.
	_, effects := tap.observe([]byte("\x1b]133;D;0\x07"), tapAt(4*time.Second))
	if len(effects.Completions) != 1 {
		t.Fatalf("sleep completions = %+v, want one", effects.Completions)
	}
	if completion := effects.Completions[0]; completion.command != "sleep 3" ||
		completion.captureTier != domainhistory.CaptureTierHook ||
		!completion.hasExitCode || completion.exitCode != 0 {
		t.Fatalf("sleep completion = %+v", completion)
	}

	// The queued line runs only after sleep ends; the hook reports it then.
	tap.observe([]byte(netcattyExec("zsh", "/work", "echo x", false)),
		tapAt(4*time.Second+time.Millisecond))
	_, effects = tap.observe([]byte("\x1b]133;D;0\x07"), tapAt(5*time.Second))
	if len(effects.Completions) != 1 {
		t.Fatalf("type-ahead completions = %+v, want one", effects.Completions)
	}
	completion := effects.Completions[0]
	if completion.command != "echo x" || completion.captureTier != domainhistory.CaptureTierHook ||
		!completion.hasExitCode || completion.exitCode != 0 {
		t.Fatalf("type-ahead completion = %+v", completion)
	}
}

// An interactive shell started from a hooked prompt takes over capture. The
// outer command is finalized once with unknown finish when the inner primary
// prompt appears; the inner shell then uses typed/prompt capture, including
// back-to-back commands, and the next handshake restores Tier 1.
func TestTapHookNestedShellHandsCaptureToTheInnerShell(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	tap.observe([]byte(hookHandshake("zsh")), tapAt(0))

	tap.handleInput([]byte("/bin/bash\r"), tapAt(time.Second))
	tap.observe([]byte(netcattyExec("zsh", "/work", "/bin/bash", false)),
		tapAt(time.Second+time.Millisecond))

	// The inner shell's primary prompt is not a continuation prompt.
	_, effects := tap.observe([]byte("bash-5.3$ "), tapAt(1200*time.Millisecond))
	if len(effects.Completions) != 1 {
		t.Fatalf("outer completions = %+v, want one", effects.Completions)
	}
	outer := effects.Completions[0]
	if outer.command != "/bin/bash" || outer.captureTier != domainhistory.CaptureTierHook ||
		!outer.finishedAt.IsZero() || outer.hasExitCode {
		t.Fatalf("outer completion = %+v", outer)
	}
	if tap.state(tapAt(1200 * time.Millisecond)).HookActive {
		t.Fatal("the outer hook stayed authoritative over the inner shell")
	}

	// Back-to-back inner commands are both captured by the ordinary fallback.
	tap.handleInput([]byte("echo one\r"), tapAt(1300*time.Millisecond))
	_, _, _, firstCompletions, _ := tap.handleInput([]byte("echo two\r"),
		tapAt(1300*time.Millisecond+time.Millisecond))
	if len(firstCompletions) != 1 || firstCompletions[0].command != "echo one" ||
		firstCompletions[0].captureTier != domainhistory.CaptureTierFallback {
		t.Fatalf("first inner command was not retired as Tier 3: %+v", firstCompletions)
	}
	_, effects = tap.observe([]byte("one\r\ntwo\r\nbash-5.3$ "), tapAt(1500*time.Millisecond))
	if len(effects.Completions) != 1 {
		t.Fatalf("second inner completions = %+v, want one", effects.Completions)
	}
	second := effects.Completions[0]
	if second.command != "echo two" || second.captureTier != domainhistory.CaptureTierFallback {
		t.Fatalf("second inner completion = %+v, want Tier 3", second)
	}

	// Returning to the hooked prompt restores Tier 1 capture.
	tap.observe([]byte(hookHandshake("zsh")), tapAt(1600*time.Millisecond))
	if !tap.state(tapAt(1600 * time.Millisecond)).HookActive {
		t.Fatal("the handshake did not restore the hook")
	}
	tap.handleInput([]byte("echo back\r"), tapAt(1700*time.Millisecond))
	tap.observe([]byte(netcattyExec("zsh", "/work", "echo back", false)),
		tapAt(1700*time.Millisecond+time.Millisecond))
	_, effects = tap.observe([]byte("\x1b]133;D;0\x07"), tapAt(2*time.Second))
	if len(effects.Completions) != 1 {
		t.Fatalf("restored completions = %+v, want one", effects.Completions)
	}
	if completion := effects.Completions[0]; completion.command != "echo back" ||
		completion.captureTier != domainhistory.CaptureTierHook {
		t.Fatalf("restored completion = %+v", completion)
	}
}

// A nested shell launched from a Tier 2 or Tier 3 capture must reset the
// parent's tier like the hook-owned path does. Before this branch existed,
// promptNestedShellLocked only ran while a hook owned the capture, so a Tier 2
// parent's nativeSeen stayed set and the child's commands were published as
// Tier 2 even though the child itself emitted no OSC 133/7 at all.
func TestTapNestedShellFromAFallbackCaptureClearsTheParentTier(t *testing.T) {
	cases := []struct {
		name  string
		setup func(tap *terminalTap)
		tier  int
	}{
		{
			name: "native OSC parent is Tier 2",
			setup: func(tap *terminalTap) {
				tap.observe([]byte("\x1b]7;file://host/work\x07\x1b]133;A\x07$ "), tapAt(0))
			},
			tier: domainhistory.CaptureTierNativeOSC,
		},
		{
			name:  "typed parent is Tier 3",
			setup: func(tap *terminalTap) {},
			tier:  domainhistory.CaptureTierFallback,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
			testCase.setup(tap)

			tap.handleInput([]byte("/bin/bash\r"), tapAt(time.Second))
			if testCase.tier == domainhistory.CaptureTierNativeOSC {
				tap.observe([]byte("\x1b]133;C\x07"), tapAt(time.Second+time.Millisecond))
			}

			// The child's primary prompt finalizes the parent with an unknown
			// finish and must clear the parent's native marks.
			_, effects := tap.observe([]byte("bash-5.3$ "), tapAt(1200*time.Millisecond))
			if len(effects.Completions) != 1 {
				t.Fatalf("outer completions = %+v, want one", effects.Completions)
			}
			outer := effects.Completions[0]
			if outer.command != "/bin/bash" || outer.captureTier != testCase.tier ||
				!outer.finishedAt.IsZero() || outer.hasExitCode {
				t.Fatalf("outer completion = %+v, want tier %d with an unknown finish",
					outer, testCase.tier)
			}
			if tap.nativeSeen {
				t.Fatal("the child inherited the parent's native marks")
			}

			// The child emits no OSC: both back-to-back commands are Tier 3.
			tap.handleInput([]byte("echo one\r"), tapAt(1300*time.Millisecond))
			_, _, _, firstCompletions, _ := tap.handleInput([]byte("echo two\r"),
				tapAt(1300*time.Millisecond+time.Millisecond))
			if len(firstCompletions) != 1 ||
				firstCompletions[0].captureTier != domainhistory.CaptureTierFallback {
				t.Fatalf("first inner command was not Tier 3: %+v", firstCompletions)
			}
			_, effects = tap.observe([]byte("one\r\ntwo\r\nbash-5.3$ "), tapAt(1500*time.Millisecond))
			if len(effects.Completions) != 1 {
				t.Fatalf("second inner completions = %+v, want one", effects.Completions)
			}
			if second := effects.Completions[0]; second.captureTier != domainhistory.CaptureTierFallback {
				t.Fatalf("second inner completion = %+v, want Tier 3", second)
			}
		})
	}
}

// A nested PowerShell started from a hooked prompt takes over capture just
// like a POSIX shell. The parent command is finalized once when the child's
// primary prompt appears. The child then refuses to install its own hook (an
// existing integration, or no PSReadLine): its skip marker must release the
// reservation so both back-to-back inner commands use Tier 2/3 capture.
func TestTapHookNestedPowerShellFallsBackAfterTheChildSkip(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	tap.observe([]byte(hookHandshake("powershell")), tapAt(0))

	const inner = `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`
	tap.handleInput([]byte(inner+"\r"), tapAt(time.Second))
	tap.observe([]byte(netcattyExec("powershell", `C:\work`, inner, false)),
		tapAt(time.Second+time.Millisecond))

	// The inner PowerShell declines the hook before its first prompt. The
	// outer hook still owns the outer command, but the plan must stop
	// expecting a child hook.
	output, effects := tap.observe([]byte("\x1b]1337;NetcattyHookSkip;powershell\x07"),
		tapAt(1100*time.Millisecond))
	if len(output) != 0 {
		t.Fatalf("the child skip marker reached the renderer: %q", output)
	}
	if len(effects.Completions) != 0 {
		t.Fatalf("the child skip marker completed a command: %+v", effects.Completions)
	}
	if tap.hookExpected {
		t.Fatal("the tap still expects a hook the inner shell refused")
	}
	if !tap.state(tapAt(1100 * time.Millisecond)).HookActive {
		t.Fatal("the child skip cleared the outer hook before its prompt")
	}

	// The inner primary prompt is not a continuation prompt. It finalizes the
	// outer command and hands capture to the child.
	_, effects = tap.observe([]byte(`PS C:\work> `), tapAt(1200*time.Millisecond))
	if len(effects.Completions) != 1 {
		t.Fatalf("outer completions = %+v, want one", effects.Completions)
	}
	outer := effects.Completions[0]
	if outer.command != inner ||
		outer.captureTier != domainhistory.CaptureTierHook ||
		!outer.finishedAt.IsZero() || outer.hasExitCode {
		t.Fatalf("outer completion = %+v", outer)
	}
	if tap.state(tapAt(1200 * time.Millisecond)).HookActive {
		t.Fatal("the outer hook stayed authoritative over the inner PowerShell")
	}

	// Back-to-back inner commands are both captured before close.
	tap.handleInput([]byte("Get-Date\r"), tapAt(1400*time.Millisecond))
	_, _, _, firstCompletions, _ := tap.handleInput([]byte("Get-Location\r"),
		tapAt(1400*time.Millisecond+time.Millisecond))
	if len(firstCompletions) != 1 || firstCompletions[0].command != "Get-Date" ||
		firstCompletions[0].captureTier != domainhistory.CaptureTierFallback {
		t.Fatalf("first inner command was not retired as Tier 3: %+v", firstCompletions)
	}
	_, effects = tap.observe([]byte("Friday\r\nPS C:\\work> "), tapAt(1600*time.Millisecond))
	if len(effects.Completions) != 1 {
		t.Fatalf("second inner completions = %+v, want one", effects.Completions)
	}
	second := effects.Completions[0]
	if second.command != "Get-Location" || second.captureTier != domainhistory.CaptureTierFallback {
		t.Fatalf("second inner completion = %+v, want Tier 3", second)
	}

	// Returning to the hooked prompt restores Tier 1 capture.
	tap.observe([]byte(hookHandshake("powershell")), tapAt(1700*time.Millisecond))
	tap.handleInput([]byte("Get-Process\r"), tapAt(1800*time.Millisecond))
	tap.observe([]byte(netcattyExec("powershell", `C:\work`, "Get-Process", false)),
		tapAt(1800*time.Millisecond+time.Millisecond))
	_, effects = tap.observe([]byte("\x1b]133;D;0\x07"), tapAt(2*time.Second))
	if len(effects.Completions) != 1 {
		t.Fatalf("restored completions = %+v, want one", effects.Completions)
	}
	if completion := effects.Completions[0]; completion.command != "Get-Process" ||
		completion.captureTier != domainhistory.CaptureTierHook {
		t.Fatalf("restored completion = %+v", completion)
	}
}

// A multiline command may pause at a continuation prompt for longer than any
// timer. Nothing may finalize the first line; the hook reports the whole
// logical command once at preexec and its D finishes it.
func TestTapHookSlowMultilineWaitsAtContinuationPrompt(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	tap.observe([]byte(hookHandshake("zsh")), tapAt(0))

	tap.handleInput([]byte("echo \"one\r"), tapAt(time.Second))
	_, effects := tap.observe([]byte("dquote> "), tapAt(1100*time.Millisecond))
	if len(effects.Completions) != 0 {
		t.Fatalf("the continuation prompt finished a capture: %+v", effects.Completions)
	}
	if line := lastVisibleLine([]byte("echo \"one\r\ndquote> ")); line != "dquote>" {
		t.Fatalf("the continuation prompt was not the last line: %q", line)
	}
	if tap.pending != nil {
		t.Fatalf("an unfinished multiline produced a pending capture: %+v", tap.pending)
	}

	// The user waits at PS2 for 1.5s, then finishes the line. No timer exists
	// to finalize the first line.
	tap.handleInput([]byte("two\"\r"), tapAt(2600*time.Millisecond))
	full := "echo \"one\ntwo\""
	tap.observe([]byte(netcattyExec("zsh", "/work", full, false)), tapAt(2700*time.Millisecond))
	_, effects = tap.observe([]byte("\x1b]133;D;0\x07"), tapAt(3*time.Second))
	if len(effects.Completions) != 1 {
		t.Fatalf("completions = %+v, want one full command", effects.Completions)
	}
	completion := effects.Completions[0]
	if completion.command != full || completion.captureTier != domainhistory.CaptureTierHook ||
		!completion.hasExitCode || completion.exitCode != 0 {
		t.Fatalf("completion = %+v", completion)
	}
}

// A command typed before the planned hook's first handshake is tentative. The
// pre-hook prompt's native A/D and the prompt heuristic must not finalize it;
// the private sequence replaces it and its D publishes exactly one Tier 1 row.
func TestTapHookExpectedHoldsAnEarlyCommandUntilItsPrivateSequence(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	tap.hookExpected = true

	_, _, _, completions, _ := tap.handleInput([]byte("echo early\r"), tapAt(0))
	if len(completions) != 0 {
		t.Fatalf("the early command published before the hook spoke: %+v", completions)
	}
	if tap.pending == nil || !tap.pending.awaitingHook {
		t.Fatalf("the early command is not tentative: %+v", tap.pending)
	}

	// The shell's own prompt marks belong to its pre-hook prompt.
	_, effects := tap.observe([]byte("\x1b]133;A\x07"), tapAt(10*time.Millisecond))
	if len(effects.Completions) != 0 {
		t.Fatalf("a native prompt start finalized the early command: %+v", effects.Completions)
	}
	_, effects = tap.observe([]byte("\x1b]133;D;0\x07"), tapAt(20*time.Millisecond))
	if len(effects.Completions) != 0 {
		t.Fatalf("a native D finalized the early command: %+v", effects.Completions)
	}

	// The first handshake proves the hook exists, but not yet which command
	// it saw; the tentative capture stays held.
	tap.observe([]byte(hookHandshake("zsh")), tapAt(30*time.Millisecond))
	if !tap.state(tapAt(30 * time.Millisecond)).HookActive {
		t.Fatal("the handshake did not mark the hook active")
	}
	_, effects = tap.observe([]byte("\x1b]133;D;0\x07"), tapAt(40*time.Millisecond))
	if len(effects.Completions) != 0 {
		t.Fatalf("a D after the handshake finalized the early command: %+v", effects.Completions)
	}
	if completion := tap.resolvePrompt(tapAt(100 * time.Millisecond)); completion != nil {
		t.Fatalf("the prompt recheck finalized the early command: %+v", completion)
	}

	// The private sequence replaces the tentative capture; its D emits the
	// single authoritative row with the real exit.
	tap.observe([]byte(netcattyExec("zsh", "/work", "echo early", false)),
		tapAt(110*time.Millisecond))
	_, effects = tap.observe([]byte("\x1b]133;D;0\x07"), tapAt(time.Second))
	if len(effects.Completions) != 1 {
		t.Fatalf("early completions = %+v, want exactly one", effects.Completions)
	}
	completion := effects.Completions[0]
	if completion.command != "echo early" ||
		completion.captureTier != domainhistory.CaptureTierHook ||
		!completion.hasExitCode || completion.exitCode != 0 {
		t.Fatalf("early completion = %+v", completion)
	}
	if duration := completion.finishedAt.Sub(completion.startedAt); duration < 500*time.Millisecond {
		t.Fatalf("duration = %v, want the private sequence's real duration", duration)
	}
}

// If the planned hook never speaks, the tentative early capture is not lost:
// Session close publishes it once with an unknown finish.
func TestTapHookExpectedFinalizesTheEarlyCommandAtClose(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	tap.hookExpected = true
	tap.handleInput([]byte("echo early\r"), tapAt(0))
	tap.observe([]byte("\x1b]133;A\x07\x1b]133;D;0\x07"), tapAt(10*time.Millisecond))

	completion := tap.finalizePending()
	if completion == nil {
		t.Fatal("Session close lost the early tentative capture")
	}
	if completion.command != "echo early" || !completion.finishedAt.IsZero() ||
		completion.hasExitCode {
		t.Fatalf("close completion = %+v", completion)
	}
	if again := tap.finalizePending(); again != nil {
		t.Fatalf("Session close published the early capture twice: %+v", again)
	}
}

// A startup script that deliberately installs nothing sends the private skip
// marker instead of the handshake. The tap must stop reserving the held
// command for a hook that will never speak: the ordinary prompt path finishes
// it and every later Enter starts a normal Tier 2/3 capture.
func TestTapHookSkipEndsTheWaitForThePlannedHook(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	tap.hookExpected = true

	_, _, _, completions, _ := tap.handleInput([]byte("echo first\r"), tapAt(0))
	if len(completions) != 0 {
		t.Fatalf("the held command published early: %+v", completions)
	}
	if tap.pending == nil || !tap.pending.awaitingHook {
		t.Fatalf("the first command is not tentative: %+v", tap.pending)
	}

	// The remote wrapper form of the marker has no shell field.
	output, effects := tap.observe([]byte("\x1b]1337;NetcattyHookSkip\x07"), tapAt(10*time.Millisecond))
	if len(output) != 0 {
		t.Fatalf("the skip marker reached the renderer: %q", output)
	}
	if len(effects.Completions) != 0 {
		t.Fatalf("the skip marker completed a command: %+v", effects.Completions)
	}
	if tap.hookExpected {
		t.Fatal("the tap still expects a hook that announced its skip")
	}
	if tap.pending == nil || tap.pending.awaitingHook {
		t.Fatalf("the held command was not released: %+v", tap.pending)
	}

	// The shell's next prompt finishes the released command like any typed
	// fallback capture.
	_, effects = tap.observe([]byte("first\r\nfirst\r\rnokiylax@host% "), tapAt(200*time.Millisecond))
	if len(effects.Completions) != 1 {
		t.Fatalf("completions = %+v, want the released command once", effects.Completions)
	}
	first := effects.Completions[0]
	if first.command != "echo first" || first.captureTier != domainhistory.CaptureTierFallback ||
		first.finishedAt.IsZero() || first.hasExitCode {
		t.Fatalf("released completion = %+v", first)
	}

	// A later command is no longer tentative: it is captured once too.
	if _, _, _, completions, _ := tap.handleInput([]byte("echo second\r"), tapAt(300*time.Millisecond)); len(completions) != 0 {
		t.Fatalf("the second Enter completed early: %+v", completions)
	}
	_, effects = tap.observe([]byte("second\r\rnokiylax@host% "), tapAt(400*time.Millisecond))
	if len(effects.Completions) != 1 {
		t.Fatalf("second completions = %+v, want one", effects.Completions)
	}
	second := effects.Completions[0]
	if second.command != "echo second" || second.captureTier != domainhistory.CaptureTierFallback ||
		second.finishedAt.IsZero() {
		t.Fatalf("second completion = %+v", second)
	}
}

// A skip that arrives after a real handshake must not disturb the installed
// hook: its private sequence and D remain authoritative.
func TestTapHookSkipAfterTheHandshakeIsIgnored(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	tap.observe([]byte(hookHandshake("zsh")), tapAt(0))
	tap.handleInput([]byte("sleep 3\r"), tapAt(time.Second))
	tap.observe([]byte(netcattyExec("zsh", "/work", "sleep 3", false)), tapAt(time.Second+time.Millisecond))

	_, effects := tap.observe([]byte("\x1b]1337;NetcattyHookSkip;zsh\x07"), tapAt(2*time.Second))
	if len(effects.Completions) != 0 {
		t.Fatalf("a late skip completed the hooked command: %+v", effects.Completions)
	}
	if !tap.state(tapAt(2 * time.Second)).HookActive {
		t.Fatal("a late skip cleared the installed hook")
	}
	_, effects = tap.observe([]byte("\x1b]133;D;0\x07"), tapAt(3*time.Second))
	if len(effects.Completions) != 1 {
		t.Fatalf("completions = %+v, want the hook's one", effects.Completions)
	}
	if completion := effects.Completions[0]; completion.command != "sleep 3" ||
		completion.captureTier != domainhistory.CaptureTierHook {
		t.Fatalf("completion = %+v", completion)
	}
}

// A command whose output happens to end in a prompt-like line is not a nested
// shell even when its first word resembles one.
func TestTapHookPromptLikeOutputIsNotANestedShell(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	tap.observe([]byte(hookHandshake("bash")), tapAt(0))

	command := "bash -c 'printf 12%'"
	tap.handleInput([]byte(command+"\r"), tapAt(time.Second))
	tap.observe([]byte(netcattyExec("bash", "/work", command, false)), tapAt(1*time.Second))
	_, effects := tap.observe([]byte("12%"), tapAt(1100*time.Millisecond))
	if len(effects.Completions) != 0 {
		t.Fatalf("a non-interactive shell output finished the command: %+v", effects.Completions)
	}
	if !tap.state(tapAt(1100 * time.Millisecond)).HookActive {
		t.Fatal("the hook was cleared for a non-interactive shell")
	}
	_, effects = tap.observe([]byte("\x1b]133;D;0\x07"), tapAt(2*time.Second))
	if len(effects.Completions) != 1 {
		t.Fatalf("completions = %+v, want one", effects.Completions)
	}
}

func TestNestedShellCommand(t *testing.T) {
	cases := map[string]bool{
		"/bin/bash":          true,
		"bash":               true,
		"zsh -l":             true,
		"fish --private":     true,
		"sh -i":              true,
		"ssh host":           true,
		"tmux attach":        true,
		"tmux attach -t dev": true,
		"tmux a":             true,
		"pwsh":               true,
		"powershell.exe":     true,
		`C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`: true,
		`"C:\Program Files\PowerShell\7\pwsh.exe"`:                  true,
		"pwsh -NoLogo -NoExit":                                      true,
		"pwsh -ExecutionPolicy:Bypass":                              true,
		"powershell -NoProfile":                                     true,
		"pwsh -ExecutionPolicy Bypass":                              true,
		"powershell -ExecutionPolicy Bypass":                        true,
		"pwsh -NoLogo -ExecutionPolicy Bypass -NoExit":              true,
		"pwsh -WorkingDirectory /tmp":                               true,
		"pwsh -WindowStyle Hidden":                                  true,
		"powershell -ConfigurationName foo":                         true,
		"pwsh -ExecutionPolicy Bypass -NoLogo":                      true,
		"pwsh -Login":                                               true,
		"pwsh -l":                                                   true,
		"& pwsh -Login":                                             true,
		`& pwsh`:                                                    true,
		`&pwsh -NoLogo`:                                             true,
		`& "C:\Program Files\PowerShell\7\pwsh.exe"`:                true,
		`& 'C:\Program Files\PowerShell\7\pwsh.exe' -NoLogo`:        true,
		`&"C:\Program Files\PowerShell\7\pwsh.exe" -NoExit`:         true,
		`& pwsh -ExecutionPolicy Bypass -NoExit`:                    true,
		`& bash`:                                                    true,
		`& tmux attach`:                                             true,
		// A quoted option value keeps its spaces as one word.
		`pwsh -WorkingDirectory 'C:\dir with a space'`:             true,
		`pwsh -WorkingDirectory "C:\Program Files\PowerShell\7"`:   true,
		`& pwsh -NoLogo -WorkingDirectory '/tmp/dir with a space'`: true,
		`pwsh -WorkingDirectory 'C:\it''s here' -NoLogo`:           true,
		// -NoExit keeps the prompt after -Command/-File/-EncodedCommand,
		// but only before them: after, it is the command's own argument.
		"pwsh -NoExit -Command 'echo hi'":                           true,
		"pwsh -NoExit -File script.ps1":                             true,
		"pwsh -NoExit -EncodedCommand AAAA":                         true,
		"pwsh -NoExit -WorkingDirectory '/tmp/dir with a space'":    true,
		"pwsh -NoExit -NonInteractive -Command 'echo hi'":           false,
		"pwsh -Command 'echo hi' -NoExit":                           false,
		"pwsh -File script.ps1 -NoExit":                             false,
		"pwsh -Command 'echo hi'":                                   false,
		"pwsh -c 'echo hi'":                                         false,
		"pwsh -File script.ps1":                                     false,
		"pwsh -NoLogo script.ps1":                                   false,
		"pwsh -NonInteractive":                                      false,
		"pwsh -nonin":                                               false,
		"pwsh -ExecutionPolicy":                                     false,
		"pwsh -ExecutionPolicy -NoLogo":                             false,
		"pwsh -ExecutionPolicy Bypass script.ps1":                   false,
		`& pwsh -Command 'echo hi'`:                                 false,
		`& bash -c 'echo hi'`:                                       false,
		`& "C:\Program Files\PowerShell\7\pwsh.exe" -File hook.ps1`: false,
		"&":                  false,
		"&& pwsh":            false,
		"bash -c 'echo hi'":  false,
		"bash -lc 'echo hi'": false,
		"sh script.sh":       false,
		"tmux ls":            false,
		"vim":                false,
		"sleep 3":            false,
		"":                   false,
	}
	for command, want := range cases {
		if got := nestedShellCommand(command); got != want {
			t.Errorf("nestedShellCommand(%q) = %t, want %t", command, got, want)
		}
	}
}

// The tail is split into lines before control bytes are stripped; stripping
// first removed CR/LF and concatenated every line.
func TestLastVisibleLineSplitsRawLines(t *testing.T) {
	cases := []struct {
		name string
		tail string
		want string
	}{
		{name: "crlf continuation", tail: "echo \"one\r\ndquote> ", want: "dquote>"},
		{name: "cr continuation", tail: "one\r> ", want: ">"},
		{name: "prompt after output", tail: "result" + "\r" + "nokiylax@host$ ", want: "nokiylax@host$"},
		{name: "osc before prompt", tail: "\x1b]133;A\x07prompt% ", want: "prompt%"},
		{name: "blank tail", tail: "one\r\n\r\n", want: "one"},
		{name: "empty", tail: "", want: ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := lastVisibleLine([]byte(testCase.tail)); got != testCase.want {
				t.Fatalf("lastVisibleLine(%q) = %q, want %q", testCase.tail, got, testCase.want)
			}
		})
	}
}
