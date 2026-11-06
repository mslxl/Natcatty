package session

import (
	"bytes"
	"encoding/base64"
	"strings"
	"sync"
	"testing"
	"time"

	domainhistory "github.com/mslxl/natcatty/domain/history"
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
)

var tapTestStart = time.Date(2026, 10, 20, 10, 0, 0, 0, time.Local)

func tapAt(offset time.Duration) time.Time { return tapTestStart.Add(offset) }

func TestTapOSC7TracksCWDAcrossChunks(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	output, _ := tap.observe([]byte("prompt\x1b]7;file://host/tmp/a%20b"), tapAt(0))
	if string(output) != "prompt" {
		t.Fatalf("incomplete OSC 7 leaked into the terminal: %q", output)
	}
	if got := tap.state(tapAt(0)).CWD; got != "" {
		t.Fatalf("incomplete OSC 7 changed cwd to %q", got)
	}
	output, _ = tap.observe([]byte("\x07more"), tapAt(time.Second))
	if got := tap.state(tapAt(time.Second)).CWD; got != "/tmp/a b" {
		t.Fatalf("cwd = %q", got)
	}
	if string(output) != "\x1b]7;file://host/tmp/a%20b\x07more" {
		t.Fatalf("forwarded OSC 7 = %q", output)
	}
}

func TestTapOSC52PolicyMatrix(t *testing.T) {
	write := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte("secret")) + "\x07"
	read := "\x1b]52;c;?\x07"
	cases := []struct {
		name           string
		policy         domainterminal.ClipboardAccess
		writeForwarded bool
		readForwarded  bool
		readAllowed    bool
	}{
		{"off", domainterminal.ClipboardOff, false, false, false},
		{"write-only", domainterminal.ClipboardWriteOnly, true, false, false},
		{"read-write", domainterminal.ClipboardReadWrite, true, true, true},
		{"prompt", domainterminal.ClipboardPrompt, true, true, true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			tap := newTerminalTap(test.policy)
			writeOutput, _ := tap.observe([]byte(write), tapAt(0))
			if got := strings.Contains(string(writeOutput), "]52;"); got != test.writeForwarded {
				t.Fatalf("write forwarded = %v, want %v (%q)", got, test.writeForwarded, writeOutput)
			}
			event := tap.state(tapAt(0)).Clipboard
			if event == nil || event.Operation != ClipboardWrite || event.Allowed != test.writeForwarded {
				t.Fatalf("write event = %+v", event)
			}
			if strings.Contains(string(writeOutput), "secret") {
				t.Fatal("clipboard payload leaked into terminal output")
			}

			readOutput, _ := tap.observe([]byte(read), tapAt(time.Second))
			if got := strings.Contains(string(readOutput), "]52;"); got != test.readForwarded {
				t.Fatalf("read forwarded = %v, want %v (%q)", got, test.readForwarded, readOutput)
			}
			event = tap.state(tapAt(time.Second)).Clipboard
			if event == nil || event.Operation != ClipboardRead || event.Allowed != test.readAllowed {
				t.Fatalf("read event = %+v", event)
			}
		})
	}
}

func TestTapOSC99AssemblesPartsAndSanitizes(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	first, effects := tap.observe([]byte("\x1b]99;i=job:d=0;Build \x1b\\"), tapAt(0))
	if len(first) != 0 || effects.Notification != nil {
		t.Fatalf("first OSC 99 part: output=%q notification=%+v", first, effects.Notification)
	}
	_, effects = tap.observe([]byte("\x1b]99;i=job:p=body;finished\nok\x07"), tapAt(time.Second))
	if effects.Notification == nil {
		t.Fatal("completed OSC 99 did not produce a notification")
	}
	if effects.Notification.Title != "Build" || effects.Notification.Body != "finished ok" {
		t.Fatalf("notification = %+v", effects.Notification)
	}
	if effects.Notification.Protocol != "osc99" {
		t.Fatalf("protocol = %q", effects.Notification.Protocol)
	}
	if got := tap.state(tapAt(time.Second)).Notification; got == nil || got.Body != "finished ok" {
		t.Fatalf("stored notification = %+v", got)
	}
}

func TestTapOSC99Base64AndClose(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	encoded := base64.StdEncoding.EncodeToString([]byte("binary <title>"))
	_, effects := tap.observe([]byte("\x1b]99;i=x:e=1;"+encoded+"\x07"), tapAt(0))
	if effects.Notification == nil || effects.Notification.Title != "Netcatty" || effects.Notification.Body != "" {
		// With no body, resolveNotification moves the title into the body.
	}
	if effects.Notification != nil {
		if effects.Notification.Body != "binary <title>" {
			t.Fatalf("base64 notification = %+v", effects.Notification)
		}
	} else {
		t.Fatal("base64 OSC 99 produced no notification")
	}

	tap.observe([]byte("\x1b]99;i=y:d=0;pending\x07"), tapAt(time.Second))
	_, effects = tap.observe([]byte("\x1b]99;i=y:p=close;\x07"), tapAt(2*time.Second))
	if effects.Notification != nil {
		t.Fatalf("close produced a notification: %+v", effects.Notification)
	}
	_, effects = tap.observe([]byte("\x1b]99;i=y;done\x07"), tapAt(3*time.Second))
	if effects.Notification == nil || effects.Notification.Body != "done" {
		t.Fatalf("pending state survived close: %+v", effects.Notification)
	}
}

func TestTapOSC99RateLimits(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	allowed := 0
	for index := 0; index < 6; index++ {
		_, effects := tap.observe([]byte("\x1b]99;message "+string(rune('0'+index))+"\x07"), tapAt(time.Duration(index)*time.Second))
		if effects.Notification != nil {
			allowed++
		}
	}
	if allowed != oscNotificationMax {
		t.Fatalf("allowed %d notifications, want %d", allowed, oscNotificationMax)
	}
}

func TestTapOSC9And777Notifications(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	output, effects := tap.observe([]byte("\x1b]9;hello there\x07"), tapAt(0))
	if len(output) != 0 || effects.Notification == nil || effects.Notification.Body != "hello there" {
		t.Fatalf("OSC 9: output=%q notification=%+v", output, effects.Notification)
	}
	output, effects = tap.observe([]byte("\x1b]777;notify;Title;Body\x07"), tapAt(time.Second))
	if len(output) != 0 || effects.Notification == nil ||
		effects.Notification.Title != "Title" || effects.Notification.Body != "Body" {
		t.Fatalf("OSC 777: output=%q notification=%+v", output, effects.Notification)
	}
	_, effects = tap.observe([]byte("\x1b]9;4;1;50\x07"), tapAt(2*time.Second))
	if effects.Notification != nil {
		t.Fatalf("ConEmu OSC 9 became a notification: %+v", effects.Notification)
	}
}

func TestTapOSC133TracksCommands(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	if _, changed, _, completions, _ := tap.handleInput([]byte("echo hi\r"), tapAt(0)); !changed {
		t.Fatal("submitting a command did not change state")
	} else if len(completions) != 0 {
		t.Fatalf("Enter emitted a completion: %+v", completions)
	}
	state := tap.state(tapAt(0))
	if !state.Command.Running || state.Command.Text != "echo hi" {
		t.Fatalf("command after Enter = %+v", state.Command)
	}

	output, effects := tap.observe([]byte("\x1b]133;D;0\x07hi\r\n"), tapAt(time.Second))
	if !effects.Changed {
		t.Fatal("OSC 133 D did not change state")
	}
	if len(effects.Completions) != 1 {
		t.Fatalf("OSC 133 D completions = %+v", effects.Completions)
	}
	completion := effects.Completions[0]
	if completion.command != "echo hi" || !completion.hasExitCode || completion.exitCode != 0 {
		t.Fatalf("completion = %+v", completion)
	}
	if completion.captureTier != domainhistory.CaptureTierNativeOSC {
		t.Fatalf("capture tier = %d, want native OSC", completion.captureTier)
	}
	if string(output) != "\x1b]133;D;0\x07hi\r\n" {
		t.Fatalf("OSC 133 should reach the terminal: %q", output)
	}
	state = tap.state(tapAt(time.Second))
	if state.Command.Running {
		t.Fatal("command still running after OSC 133 D")
	}
	if !state.Command.HasExitCode || state.Command.ExitCode != 0 {
		t.Fatalf("exit = %+v", state.Command)
	}

	// Late marks for the finalized command are ignored until a new Enter.
	tap.observe([]byte("\x1b]133;A\x07$ "), tapAt(2*time.Second))
	_, effects = tap.observe([]byte("\x1b]133;C\x07output\x07"), tapAt(3*time.Second))
	if len(effects.Completions) != 0 {
		t.Fatalf("late marks emitted a completion: %+v", effects.Completions)
	}
	if tap.state(tapAt(3 * time.Second)).Command.Running {
		t.Fatal("OSC 133 C started a command without a new Enter")
	}

	// A new Enter starts the next pending command, and C marks it running.
	tap.handleInput([]byte("echo next\r"), tapAt(4*time.Second))
	tap.observe([]byte("\x1b]133;C\x07"), tapAt(5*time.Second))
	if !tap.state(tapAt(5 * time.Second)).Command.Running {
		t.Fatal("OSC 133 C did not mark the new command running")
	}
}

func TestTapCommandTracksMultibyteInputAcrossWrites(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	tap.handleInput([]byte("echo \xc3"), tapAt(0))
	tap.handleInput([]byte("\xa9\r"), tapAt(time.Second))
	state := tap.state(tapAt(time.Second))
	if state.Command.Text != "echo é" {
		t.Fatalf("command text = %q, want %q", state.Command.Text, "echo é")
	}
	if !state.Command.Running {
		t.Fatalf("command did not start: %+v", state.Command)
	}
}

func TestTapCommandBackspaceRemovesOneMultibyteRune(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	tap.handleInput([]byte("éx"), tapAt(0))
	tap.handleInput([]byte{0x7f}, tapAt(time.Second))
	tap.handleInput([]byte("\r"), tapAt(2*time.Second))
	if got := tap.state(tapAt(2 * time.Second)).Command.Text; got != "é" {
		t.Fatalf("command text = %q, want %q", got, "é")
	}
}

func TestTapCommandFallbackPrompt(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	tap.handleInput([]byte("sleep 1\r"), tapAt(0))
	tap.observe([]byte("working\r\nuser@host:~$ "), tapAt(200*time.Millisecond))
	state := tap.state(tapAt(200 * time.Millisecond))
	if state.Command.Running {
		t.Fatalf("prompt fallback did not finish the command: %+v", state.Command)
	}
}

// A fast command's prompt can arrive inside the guard window. Nothing follows
// it, so an output-driven check would never run again and the command would
// stay Running forever; the deferred check must settle it once the window
// expires.
func TestTapPromptFallbackSettlesAFastPromptAfterTheGuardWindow(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	tap.handleInput([]byte("echo world\r"), tapAt(0))
	_, effects := tap.observe([]byte("world\r\nPS C:\\work> "), tapAt(20*time.Millisecond))
	if !effects.PromptRecheck {
		t.Fatal("the prompt inside the guard window did not request a deferred check")
	}
	if !tap.state(tapAt(20 * time.Millisecond)).Command.Running {
		t.Fatal("a prompt inside the guard window finished the command")
	}
	if tap.resolvePrompt(tapAt(40*time.Millisecond)) != nil {
		t.Fatal("the deferred check finished the command inside the guard window")
	}
	if tap.resolvePrompt(tapAt(80*time.Millisecond)) == nil {
		t.Fatal("the deferred check did not finish the command")
	}
	state := tap.state(tapAt(80 * time.Millisecond))
	if state.Command.Running || !state.Command.FinishedAt.Equal(tapAt(80*time.Millisecond)) {
		t.Fatalf("command after the deferred check = %+v", state.Command)
	}
}

// The prompt from the previous command is already in the tail when Enter is
// pressed. It says nothing about the new command and must not finish it, even
// after the guard window, when no output for the new command arrived.
func TestTapPromptFallbackIgnoresThePreviousPrompt(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	tap.observe([]byte("PS C:\\work> "), tapAt(0))
	tap.handleInput([]byte("sleep 10\r"), tapAt(time.Second))
	if tap.resolvePrompt(tapAt(time.Second+500*time.Millisecond)) != nil {
		t.Fatal("the previous prompt finished the new command")
	}
	if state := tap.state(tapAt(time.Second + 500*time.Millisecond)); !state.Command.Running {
		t.Fatalf("a stale prompt finished the running command: %+v", state.Command)
	}
}

// stripTerminalControls must drop a whole CSI sequence, not just its ESC [.
// ConPTY appends cursor controls such as ESC [?25h after a prompt, and their
// parameter bytes must not survive as visible text.
func TestStripTerminalControlsRemovesControlSequences(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"cursor show", "PS C:\\work> \x1b[?25h", "PS C:\\work> "},
		{"cursor hide and color", "\x1b[?25l\x1b[93mecho\x1b[m hi", "echo hi"},
		{"cursor position", "out\x1b[3;1H\x1b[?25h", "out"},
		{"erase screen", "\x1b[2J\x1b[Hdone", "done"},
		{"osc title", "\x1b]0;title\x07done", "done"},
		{"split csi is dropped", "prompt> \x1b[?25", "prompt> "},
	}
	for _, test := range cases {
		if got := stripTerminalControls([]byte(test.in)); got != test.want {
			t.Errorf("%s: stripTerminalControls(%q) = %q, want %q", test.name, test.in, got, test.want)
		}
	}
}

// An escape sequence is forwarded whole and its bytes never count as visible
// output. Before, only the OSC branch did this: a CSI's ESC was forwarded
// alone and its parameter bytes ("?25h") landed in the tail as text.
func TestTapForwardsEscapeSequencesWithoutNotingTheirBytes(t *testing.T) {
	input := []byte("\x1b[?25lPS C:\\work> \x1b[?25h\x1b(B\x1bOP")
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	output, _ := tap.observe(input, tapAt(0))
	if string(output) != string(input) {
		t.Fatalf("output = %q, want the sequences forwarded whole", output)
	}
	if got := lastVisibleLine(tap.tail); got != "PS C:\\work>" {
		t.Fatalf("visible tail = %q, want %q", got, "PS C:\\work>")
	}
}

// A CSI split across two reads is held until its final byte, then forwarded
// whole, with none of its parameter bytes in the visible tail.
func TestTapHoldsASplitEscapeSequence(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	output, _ := tap.observe([]byte("prompt> \x1b[?25"), tapAt(0))
	if string(output) != "prompt> " {
		t.Fatalf("visible output before the split sequence = %q", output)
	}
	output, _ = tap.observe([]byte("h"), tapAt(time.Second))
	if string(output) != "\x1b[?25h" {
		t.Fatalf("the completed sequence = %q", output)
	}
	if got := lastVisibleLine(tap.tail); got != "prompt>" {
		t.Fatalf("visible tail = %q, want %q", got, "prompt>")
	}
}

// PowerShell on the Windows CI runner ends its final chunk with ESC [?25h, and
// no further output follows, so a miss here leaves Running set forever.
func TestTapPromptFallbackSeesAPromptBehindTrailingCursorControls(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	tap.handleInput([]byte("echo world\r"), tapAt(0))
	_, effects := tap.observe([]byte("world\r\nPS C:\\work> \x1b[?25h"), tapAt(20*time.Millisecond))
	if !effects.PromptRecheck {
		t.Fatal("a prompt behind cursor controls did not request a deferred check")
	}
	if !tap.state(tapAt(20 * time.Millisecond)).Command.Running {
		t.Fatal("a prompt inside the guard window finished the command")
	}
	if tap.resolvePrompt(tapAt(80*time.Millisecond)) == nil {
		t.Fatal("the deferred check did not finish the command behind cursor controls")
	}
}

// Output that does not end in a prompt must still request a bounded recheck:
// the deferred check is the only look a quiet session gets after its last
// chunk. It must not finish the command, because no prompt was seen.
func TestTapRunningOutputRequestsABoundedRecheckWithoutAPrompt(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	tap.handleInput([]byte("sleep 10\r"), tapAt(0))
	_, effects := tap.observe([]byte("still working\r\n"), tapAt(time.Second))
	if !effects.PromptRecheck {
		t.Fatal("running output did not request a bounded recheck")
	}
	if tap.resolvePrompt(tapAt(time.Second+500*time.Millisecond)) != nil {
		t.Fatal("the bounded recheck finished a command without a prompt")
	}
	if state := tap.state(tapAt(time.Second + 500*time.Millisecond)); !state.Command.Running {
		t.Fatalf("the bounded recheck cleared a running command: %+v", state.Command)
	}
}

func TestTapActivityTracksOutput(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	tap.observe([]byte("output"), tapAt(0))
	state := tap.state(tapAt(time.Second))
	if !state.Busy || !state.LastActivity.Equal(tapTestStart) {
		t.Fatalf("activity = %+v", state)
	}
	if tap.state(tapAt(3 * time.Second)).Busy {
		t.Fatal("activity stayed busy after the hold window")
	}
}

func TestTapGatesZMODEMAndDropsProtocolBytes(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	output, effects := tap.observe([]byte("before**\x18BhexpayloadOOafter"), tapAt(0))
	if string(output) != "beforeafter" {
		t.Fatalf("terminal output = %q", output)
	}
	state := tap.state(tapAt(0))
	if state.Transfer.Kind != TransferZMODEM || state.Transfer.Active {
		t.Fatalf("transfer = %+v", state.Transfer)
	}
	if state.Transfer.End != TransferEndCompleted || state.Transfer.Suppressed == 0 {
		t.Fatalf("transfer end = %+v", state.Transfer)
	}
	if effects.Transfer == nil {
		t.Fatal("transfer detection was not reported to the Session")
	}
}

func TestTapGatesZMODEMSplitAcrossChunks(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	if output, _ := tap.observe([]byte("**\x18"), tapAt(0)); len(output) != 0 {
		t.Fatalf("partial ZMODEM prefix leaked: %q", output)
	}
	if tap.state(tapAt(0)).Transfer.Active {
		t.Fatal("incomplete prefix started a transfer")
	}
	output, effects := tap.observe([]byte("BpayloadOOdone"), tapAt(time.Second))
	if string(output) != "done" {
		t.Fatalf("output after split detection = %q", output)
	}
	if effects.Transfer == nil || effects.Transfer.End != TransferEndCompleted {
		t.Fatalf("transfer = %+v", effects.Transfer)
	}
}

func TestTapGatesYMODEM(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	output, effects := tap.observe([]byte("\x01\x00\xfffile\x04prompt$ "), tapAt(0))
	if string(output) != "prompt$ " {
		t.Fatalf("terminal output = %q", output)
	}
	if effects.Transfer == nil || effects.Transfer.Kind != TransferYMODEM ||
		effects.Transfer.End != TransferEndCompleted {
		t.Fatalf("transfer = %+v", effects.Transfer)
	}
}

func TestTapTransferInputGateAndCtrlC(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	tap.observe([]byte("**\x18Bdata"), tapAt(0))
	forwarded, changed, _, _, _ := tap.handleInput([]byte("rm -rf /\r"), tapAt(time.Second))
	if forwarded != nil || changed {
		t.Fatalf("normal input passed through the gate: %q", forwarded)
	}
	if !tap.state(tapAt(time.Second)).Transfer.Active {
		t.Fatal("gate closed on normal input")
	}
	forwarded, changed, _, _, transfer := tap.handleInput([]byte{0x03}, tapAt(2*time.Second))
	if !changed || !bytes.Equal(forwarded, []byte{0x18, 0x18, 0x18, 0x18, 0x18}) {
		t.Fatalf("Ctrl+C abort = %q changed=%v", forwarded, changed)
	}
	if transfer == nil || transfer.Active || transfer.End != TransferEndCancelled {
		t.Fatalf("cancel transfer effect = %+v", transfer)
	}
	state := tap.state(tapAt(2 * time.Second))
	if state.Transfer.Active || state.Transfer.End != TransferEndCancelled {
		t.Fatalf("transfer after abort = %+v", state.Transfer)
	}
}

func TestTapCancelTransferFromUI(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	tap.observe([]byte("**\x18Bdata"), tapAt(0))
	if !tap.cancelTransfer(tapAt(time.Second)) {
		t.Fatal("cancelTransfer did nothing")
	}
	output, _ := tap.observe([]byte("visible"), tapAt(2*time.Second))
	if string(output) != "visible" {
		t.Fatalf("output after cancel = %q", output)
	}
}

func TestTapTitleFromOSC0And2(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	tap.observe([]byte("\x1b]0;window\x07"), tapAt(0))
	if got := tap.state(tapAt(0)).Title; got != "window" {
		t.Fatalf("title = %q", got)
	}
	tap.observe([]byte("\x1b]2;second\x1b\\"), tapAt(time.Second))
	if got := tap.state(tapAt(time.Second)).Title; got != "second" {
		t.Fatalf("title = %q", got)
	}
}

func TestTapSanitizesNotificationLimits(t *testing.T) {
	long := strings.Repeat("x", 200)
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	_, effects := tap.observe([]byte("\x1b]777;notify;"+long+";"+long+"\x07"), tapAt(0))
	if effects.Notification == nil {
		t.Fatal("no notification")
	}
	if got := len([]rune(effects.Notification.Title)); got > oscNotificationTitleMax {
		t.Fatalf("title length = %d", got)
	}
	if got := len([]rune(effects.Notification.Body)); got > oscNotificationBodyMax {
		t.Fatalf("body length = %d", got)
	}
}

func TestLogOutputStripsClipboardPayloadsButKeepsScreenText(t *testing.T) {
	secret := base64.StdEncoding.EncodeToString([]byte("password"))
	output := []byte("before\x1b]52;c;" + secret + "\x07after" +
		"\x1b]52;c;?" + "\x1b" + `\` + "tail")
	got := string(logOutput(output))
	if got != "beforeaftertail" {
		t.Fatalf("log output = %q", got)
	}
	if strings.Contains(got, secret) || strings.Contains(got, "password") {
		t.Fatalf("clipboard payload reached the log: %q", got)
	}
	if plain := string(logOutput([]byte("plain output\r\n"))); plain != "plain output\r\n" {
		t.Fatalf("plain output changed: %q", plain)
	}
}

// A command is pending from the Enter that submitted it and never emitted
// early. OSC 133 C marks it running; D finalizes it with the shell's exit.
func TestTapCompletionStartsAtEnterAndFinalizesOnD(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	_, changed, started, completions, _ := tap.handleInput([]byte("ls -la\r"), tapAt(0))
	if !changed || !started || len(completions) != 0 {
		t.Fatalf("Enter: changed=%v started=%v completions=%+v", changed, started, completions)
	}
	if tap.pending == nil || tap.pending.command != "ls -la" ||
		!tap.pending.startedAt.Equal(tapAt(0)) {
		t.Fatalf("pending after Enter = %+v", tap.pending)
	}
	if !tap.state(tapAt(0)).Command.Running {
		t.Fatal("a submitted command is not running")
	}

	_, effects := tap.observe([]byte("\x1b]133;C\x07"), tapAt(time.Second))
	if len(effects.Completions) != 0 {
		t.Fatalf("C emitted a completion: %+v", effects.Completions)
	}
	if !tap.pending.startedAt.Equal(tapAt(0)) {
		t.Fatalf("C replaced the Enter start time: %v", tap.pending.startedAt)
	}

	_, effects = tap.observe([]byte("\x1b]133;D;2\x07"), tapAt(2*time.Second))
	if len(effects.Completions) != 1 {
		t.Fatalf("D completions = %+v", effects.Completions)
	}
	completion := effects.Completions[0]
	if completion.command != "ls -la" ||
		!completion.startedAt.Equal(tapAt(0)) ||
		!completion.finishedAt.Equal(tapAt(2*time.Second)) ||
		!completion.hasExitCode || completion.exitCode != 2 ||
		completion.captureTier != domainhistory.CaptureTierNativeOSC {
		t.Fatalf("completion = %+v", completion)
	}
	if tap.pending != nil {
		t.Fatalf("pending survived D: %+v", tap.pending)
	}
}

// The cwd is the last OSC 7 seen at Enter, not a later one; an OSC 133 D split
// across two transport reads still finalizes exactly once.
func TestTapCompletionUsesCWDAtEnterAndHandlesSplitOSC(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	tap.observe([]byte("\x1b]7;file://host/tmp/work\x07"), tapAt(0))
	tap.handleInput([]byte("pwd\r"), tapAt(time.Second))
	tap.observe([]byte("\x1b]7;file://host/tmp/later\x07"), tapAt(2*time.Second))

	output, effects := tap.observe([]byte("\x1b]133;D;0"), tapAt(3*time.Second))
	if len(output) != 0 || len(effects.Completions) != 0 {
		t.Fatalf("an incomplete OSC finalized the command: output=%q effects=%+v", output, effects)
	}
	output, effects = tap.observe([]byte("\x07"), tapAt(4*time.Second))
	if string(output) != "\x1b]133;D;0\x07" || len(effects.Completions) != 1 {
		t.Fatalf("split OSC completion: output=%q effects=%+v", output, effects)
	}
	completion := effects.Completions[0]
	if completion.cwd != "/tmp/work" {
		t.Fatalf("cwd = %q, want the OSC 7 at Enter", completion.cwd)
	}
	if !completion.finishedAt.Equal(tapAt(4 * time.Second)) {
		t.Fatalf("finishedAt = %v", completion.finishedAt)
	}
}

// Malformed OSC 133 sequences must not break output parsing. A D with an
// unparsable or out-of-range exit still finishes the command, with the exit
// unknown; unknown marks are ignored.
func TestTapCompletionMalformedOSC133(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	tap.handleInput([]byte("first\r"), tapAt(0))
	output, effects := tap.observe([]byte("\x1b]133;Z\x07"), tapAt(time.Second))
	if string(output) != "\x1b]133;Z\x07" || len(effects.Completions) != 0 {
		t.Fatalf("unknown mark: output=%q effects=%+v", output, effects)
	}
	_, effects = tap.observe([]byte("\x1b]133;D;not-a-number\x07"), tapAt(2*time.Second))
	if len(effects.Completions) != 1 {
		t.Fatalf("malformed D completions = %+v", effects.Completions)
	}
	if effects.Completions[0].hasExitCode {
		t.Fatalf("malformed exit was accepted: %+v", effects.Completions[0])
	}

	tap.handleInput([]byte("second\r"), tapAt(3*time.Second))
	_, effects = tap.observe([]byte("\x1b]133;D;999999999999999999999999999999\x07"), tapAt(4*time.Second))
	if len(effects.Completions) != 1 || effects.Completions[0].hasExitCode {
		t.Fatalf("out-of-range exit: %+v", effects.Completions)
	}

	tap.handleInput([]byte("third\r"), tapAt(5*time.Second))
	output, effects = tap.observe([]byte("\x1b]133\x07"), tapAt(6*time.Second))
	if string(output) != "\x1b]133\x07" || len(effects.Completions) != 0 {
		t.Fatalf("bare OSC 133: output=%q effects=%+v", output, effects)
	}
	if tap.pending == nil {
		t.Fatal("a malformed mark discarded the pending command")
	}
}

// A prompt-start mark without D finishes the pending command with an unknown
// exit. A later D for it is ignored.
func TestTapCompletionOSC133AWithoutDFinalizesOnce(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	tap.handleInput([]byte("make\r"), tapAt(0))
	_, effects := tap.observe([]byte("\x1b]133;C\x07building\r\n\x1b]133;A\x07$ "), tapAt(2*time.Second))
	if len(effects.Completions) != 1 {
		t.Fatalf("A completions = %+v", effects.Completions)
	}
	completion := effects.Completions[0]
	if completion.hasExitCode || !completion.finishedAt.Equal(tapAt(2*time.Second)) {
		t.Fatalf("completion = %+v", completion)
	}
	_, effects = tap.observe([]byte("\x1b]133;D;1\x07"), tapAt(3*time.Second))
	if len(effects.Completions) != 0 {
		t.Fatalf("late D emitted a completion: %+v", effects.Completions)
	}
}

// B marks the prompt state but does not finish a command.
func TestTapCompletionOSC133BDoesNotFinalize(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	tap.handleInput([]byte("sleep 1\r"), tapAt(0))
	_, effects := tap.observe([]byte("\x1b]133;B\x07"), tapAt(time.Second))
	if len(effects.Completions) != 0 {
		t.Fatalf("B emitted a completion: %+v", effects.Completions)
	}
	if tap.pending == nil {
		t.Fatal("B discarded the pending command")
	}
}

// A recognizable prompt after output finishes a command with an unknown exit
// and exactly one completion.
func TestTapCompletionPromptFallbackFinalizesOnce(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	tap.handleInput([]byte("echo world\r"), tapAt(0))
	_, effects := tap.observe([]byte("world\r\n$ "), tapAt(100*time.Millisecond))
	if len(effects.Completions) != 1 {
		t.Fatalf("prompt fallback completions = %+v", effects.Completions)
	}
	completion := effects.Completions[0]
	if completion.command != "echo world" || completion.hasExitCode ||
		!completion.finishedAt.Equal(tapAt(100*time.Millisecond)) ||
		completion.captureTier != domainhistory.CaptureTierFallback {
		t.Fatalf("completion = %+v", completion)
	}
	_, effects = tap.observe([]byte("\x1b]133;A\x07$ "), tapAt(2*time.Second))
	if len(effects.Completions) != 0 {
		t.Fatalf("a late prompt emitted a second completion: %+v", effects.Completions)
	}
}

// OSC 7 alone marks the shell as natively integrated, so a prompt fallback is
// Tier 2 and uses the OSC 7 cwd.
func TestTapCompletionOSC7MakesPromptFallbackTier2(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	tap.observe([]byte("\x1b]7;file://host/srv\x07"), tapAt(0))
	tap.handleInput([]byte("deploy\r"), tapAt(time.Second))
	_, effects := tap.observe([]byte("done\r\n$ "), tapAt(2*time.Second))
	if len(effects.Completions) != 1 {
		t.Fatalf("completions = %+v", effects.Completions)
	}
	completion := effects.Completions[0]
	if completion.captureTier != domainhistory.CaptureTierNativeOSC || completion.cwd != "/srv" {
		t.Fatalf("completion = %+v", completion)
	}
}

// Ctrl+C never emits by itself; a following D finalizes with the shell's exit
// code and a finish time.
func TestTapCompletionCtrlCWaitsForD(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	tap.handleInput([]byte("sleep 30\r"), tapAt(0))
	_, _, _, completions, _ := tap.handleInput([]byte{0x03}, tapAt(time.Second))
	if len(completions) != 0 {
		t.Fatalf("Ctrl+C emitted a completion: %+v", completions)
	}
	if tap.pending == nil || !tap.pending.interrupted {
		t.Fatalf("pending after Ctrl+C = %+v", tap.pending)
	}
	_, effects := tap.observe([]byte("\x1b]133;D;130\x07"), tapAt(2*time.Second))
	if len(effects.Completions) != 1 {
		t.Fatalf("D completions = %+v", effects.Completions)
	}
	completion := effects.Completions[0]
	if !completion.hasExitCode || completion.exitCode != 130 {
		t.Fatalf("completion = %+v", completion)
	}
	if !completion.finishedAt.Equal(tapAt(2 * time.Second)) {
		t.Fatalf("finishedAt = %v", completion.finishedAt)
	}
}

// Without D, Ctrl+C falls back to a recognizable prompt, with an unknown exit.
func TestTapCompletionCtrlCThenPromptFallback(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	tap.handleInput([]byte("tail -f log\r"), tapAt(0))
	tap.handleInput([]byte{0x03}, tapAt(time.Second))
	_, effects := tap.observe([]byte("^C\r\n$ "), tapAt(2*time.Second))
	if len(effects.Completions) != 1 {
		t.Fatalf("completions = %+v", effects.Completions)
	}
	completion := effects.Completions[0]
	if completion.hasExitCode || !completion.finishedAt.Equal(tapAt(2*time.Second)) {
		t.Fatalf("completion = %+v", completion)
	}
}

// A new Enter finalizes the previous command with an unknown exit and no
// finish time, then starts the next one.
func TestTapCompletionNewEnterFinalizesPreviousWithoutDuration(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	tap.handleInput([]byte("first\r"), tapAt(0))
	_, _, started, completions, _ := tap.handleInput([]byte("second\r"), tapAt(time.Second))
	if !started || len(completions) != 1 {
		t.Fatalf("new Enter: started=%v completions=%+v", started, completions)
	}
	previous := completions[0]
	if previous.command != "first" || previous.hasExitCode ||
		!previous.finishedAt.IsZero() {
		t.Fatalf("previous completion = %+v", previous)
	}
	if tap.pending == nil || tap.pending.command != "second" {
		t.Fatalf("pending after the new Enter = %+v", tap.pending)
	}
	_, effects := tap.observe([]byte("\x1b]133;D;0\x07"), tapAt(2*time.Second))
	if len(effects.Completions) != 1 || effects.Completions[0].command != "second" {
		t.Fatalf("second completion = %+v", effects.Completions)
	}
}

// Session close finalizes a pending command once, with an unknown exit and no
// finish time.
func TestTapSessionCloseFinalizesPendingWithoutDuration(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	tap.handleInput([]byte("long-job\r"), tapAt(0))
	completion := tap.finalizePending()
	if completion == nil {
		t.Fatal("Session close did not finalize the pending command")
	}
	if completion.command != "long-job" || completion.hasExitCode ||
		!completion.finishedAt.IsZero() {
		t.Fatalf("completion = %+v", completion)
	}
	if again := tap.finalizePending(); again != nil {
		t.Fatalf("Session close finalized twice: %+v", again)
	}
}

// OSC 133 marks with no command text never produce a row.
func TestTapCompletionNoCommandTextNeverEmits(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	for _, mark := range []string{
		"\x1b]133;C\x07", "\x1b]133;D;0\x07", "\x1b]133;A\x07", "\x1b]133;B\x07",
	} {
		output, effects := tap.observe([]byte(mark), tapAt(0))
		if string(output) != mark {
			t.Fatalf("%q was not forwarded: %q", mark, output)
		}
		if len(effects.Completions) != 0 {
			t.Fatalf("%q emitted a completion without command text: %+v", mark, effects.Completions)
		}
	}
	if tap.finalizePending() != nil {
		t.Fatal("Session close emitted a completion without command text")
	}
}

// Concurrent finalizers must not produce two events for one command.
func TestTapFinalizePendingIsExactlyOnceUnderConcurrency(t *testing.T) {
	tap := newTerminalTap(domainterminal.ClipboardWriteOnly)
	tap.handleInput([]byte("racy\r"), tapAt(0))
	const workers = 8
	results := make(chan *capturedCommand, workers)
	var group sync.WaitGroup
	for index := 0; index < workers; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			results <- tap.finalizePending()
		}()
	}
	group.Wait()
	close(results)
	completions := 0
	for completion := range results {
		if completion != nil {
			completions++
		}
	}
	if completions != 1 {
		t.Fatalf("concurrent finalizers produced %d completions, want 1", completions)
	}
}
