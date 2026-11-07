package session

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	domainhistory "github.com/mslxl/natcatty/domain/history"
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
)

// netcattyExec builds the private sequence the session hook emits.
func netcattyExec(shell, cwd, command string, padded bool) string {
	encoding := base64.RawURLEncoding
	if padded {
		encoding = base64.URLEncoding
	}
	return "\x1b]1337;NetcattyExec;" + shell + ";" +
		encoding.EncodeToString([]byte(cwd)) + ";" +
		encoding.EncodeToString([]byte(command)) + "\x07"
}

func TestTapNetcattyExecIsAuthoritativeAndStartsTier1(t *testing.T) {
	for _, padded := range []bool{false, true} {
		padded := padded
		name := "unpadded"
		if padded {
			name = "padded"
		}
		t.Run(name, func(t *testing.T) {
			tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
			sequence := netcattyExec("zsh", "/run/current", "git status --short", padded)

			// Split reads: the parser holds the partial OSC and consumes the
			// whole sequence without forwarding a byte to the renderer.
			first := sequence[:len(sequence)/2]
			output, effects := tap.observe([]byte(first), tapAt(0))
			if len(output) != 0 {
				t.Fatalf("partial sequence leaked: %q", output)
			}
			output, effects = tap.observe([]byte(sequence[len(sequence)/2:]), tapAt(time.Second))
			if len(output) != 0 {
				t.Fatalf("private sequence reached the renderer: %q", output)
			}
			if !effects.Changed {
				t.Fatal("the private sequence did not report a state change")
			}
			state := tap.state(tapAt(time.Second))
			if !state.HookActive {
				t.Fatal("the tap did not mark the Session hook-active")
			}
			if state.Command.Text != "git status --short" || !state.Command.Running ||
				!state.Command.StartedAt.Equal(tapAt(time.Second)) {
				t.Fatalf("command state = %+v", state.Command)
			}
			_, effects = tap.observe([]byte("\x1b]133;D;7\x07"), tapAt(2*time.Second))
			if len(effects.Completions) != 1 {
				t.Fatalf("completions = %+v", effects.Completions)
			}
			completion := effects.Completions[0]
			if completion.command != "git status --short" || completion.cwd != "/run/current" ||
				completion.shell != "zsh" || completion.captureTier != domainhistory.CaptureTierHook {
				t.Fatalf("completion = %+v", completion)
			}
			if !completion.hasExitCode || completion.exitCode != 7 {
				t.Fatalf("exit = %d/%t", completion.exitCode, completion.hasExitCode)
			}
			if !completion.startedAt.Equal(tapAt(time.Second)) ||
				!completion.finishedAt.Equal(tapAt(2*time.Second)) {
				t.Fatalf("times = %v..%v", completion.startedAt, completion.finishedAt)
			}
		})
	}
}

func TestTapNetcattyExecReplacesTypedPending(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	_, _, _, _, _ = tap.handleInput([]byte("echo typed\r"), tapAt(0))

	sequence := netcattyExec("bash", "/srv/app", "echo exact", false)
	output, effects := tap.observe([]byte(sequence), tapAt(time.Second))
	if len(output) != 0 || !effects.Changed {
		t.Fatalf("output = %q, changed = %t", output, effects.Changed)
	}
	_, effects = tap.observe([]byte("\x1b]133;D;0\x07"), tapAt(2*time.Second))
	if len(effects.Completions) != 1 {
		t.Fatalf("completions = %+v", effects.Completions)
	}
	completion := effects.Completions[0]
	if completion.command != "echo exact" || completion.cwd != "/srv/app" ||
		completion.shell != "bash" || completion.captureTier != domainhistory.CaptureTierHook {
		t.Fatalf("completion = %+v", completion)
	}
	// The Enter time is replaced by the hook time, which is authoritative.
	if !completion.startedAt.Equal(tapAt(time.Second)) {
		t.Fatalf("start = %v, want the hook time", completion.startedAt)
	}
}

func TestTapNetcattyExecMalformedIsConsumedAndIgnored(t *testing.T) {
	cases := map[string]string{
		"too few fields":   "\x1b]1337;NetcattyExec;zsh;/tmp\x07",
		"too many fields":  "\x1b]1337;NetcattyExec;zsh;YQ;Yg;Yw\x07",
		"empty shell":      "\x1b]1337;NetcattyExec;;YQ;Yg\x07",
		"bad shell":        "\x1b]1337;NetcattyExec;zs h;YQ;Yg\x07",
		"bad cwd encoding": "\x1b]1337;NetcattyExec;zsh;!!!!;Yg\x07",
		"bad command enc":  "\x1b]1337;NetcattyExec;zsh;YQ;!!!!\x07",
		"empty command":    "\x1b]1337;NetcattyExec;zsh;YQ;\x07",
	}
	for name, sequence := range cases {
		t.Run(name, func(t *testing.T) {
			tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
			output, effects := tap.observe([]byte(sequence), tapAt(0))
			if len(output) != 0 {
				t.Fatalf("malformed sequence reached the renderer: %q", output)
			}
			if effects.Changed || tap.state(tapAt(0)).HookActive {
				t.Fatal("malformed sequence changed Session state")
			}
			// The malformed sequence must not break a later valid one.
			tap.observe([]byte(netcattyExec("fish", "/tmp", "ls", false)), tapAt(time.Second))
			if !tap.state(tapAt(time.Second)).HookActive {
				t.Fatal("parsing did not recover after a malformed sequence")
			}
		})
	}
}

func TestTapNetcattyExecLeavesOtherOSC1337Alone(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	other := "\x1b]1337;File=inline=1:Zm9v\x07"
	output, _ := tap.observe([]byte(other), tapAt(0))
	if string(output) != other {
		t.Fatalf("foreign OSC 1337 was filtered: %q", output)
	}
}

func TestTapNetcattyExecTruncatesOversizedCommand(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	command := strings.Repeat("x", domainhistory.MaxCommandLength+100)
	tap.observe([]byte(netcattyExec("zsh", "/tmp", command, false)), tapAt(0))
	if got := len(tap.state(tapAt(0)).Command.Text); got != domainhistory.MaxCommandLength {
		t.Fatalf("command length = %d, want %d", got, domainhistory.MaxCommandLength)
	}
}

func TestTapNetcattyExecTier1SurvivesLateMarksAndClosesOnce(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	tap.observe([]byte(netcattyExec("zsh", "/tmp", "make", false)), tapAt(0))
	_, effects := tap.observe([]byte("\x1b]133;D;0\x07"), tapAt(time.Second))
	if len(effects.Completions) != 1 {
		t.Fatalf("completions = %+v", effects.Completions)
	}
	if completion := effects.Completions[0]; completion.captureTier != domainhistory.CaptureTierHook {
		t.Fatalf("tier = %d", completion.captureTier)
	}
	_, effects = tap.observe([]byte("\x1b]133;D;0\x07"), tapAt(2*time.Second))
	if len(effects.Completions) != 0 {
		t.Fatalf("a late mark emitted again: %+v", effects.Completions)
	}
	if completion := tap.finalizePending(); completion != nil {
		t.Fatalf("close finalized twice: %+v", completion)
	}
}
