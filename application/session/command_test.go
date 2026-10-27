package session

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"
)

var markerPattern = regexp.MustCompile(`__NCMCP_[a-z0-9]+_[0-9a-f]+__`)

type commandCase struct {
	result CommandResult
	err    error
}

func startTestCommand(session *Session, ctx context.Context, command string, opts CommandOptions) <-chan commandCase {
	done := make(chan commandCase, 1)
	go func() {
		result, err := session.Command(ctx, command, opts)
		done <- commandCase{result: result, err: err}
	}()
	return done
}

func awaitTestCommand(t *testing.T, done <-chan commandCase) commandCase {
	t.Helper()
	select {
	case got := <-done:
		return got
	case <-time.After(10 * time.Second):
		t.Fatal("command did not finish")
		return commandCase{}
	}
}

// newShellSession opens a fake POSIX shell Session over the fake transport.
func newShellSession(t *testing.T) (*Session, *fakeTransport) {
	t.Helper()
	transport := newFakeTransport()
	service := newTestService(&fakeFactory{transport: transport})
	session, err := service.Open(context.Background(), StartSpec{
		Kind: KindLocal, Command: []string{"sh"}, Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session, transport
}

func TestCommandRemoteWithoutAShellHintUsesTheUnknownStrictKind(t *testing.T) {
	transport := newFakeTransport()
	service := newTestService(&fakeFactory{transport: transport})
	session, err := service.Open(context.Background(), StartSpec{
		Kind: KindSSH, Hostname: "remote.example", Port: 22, Username: "ops",
		Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	if got := session.ShellKind(); got != "" {
		t.Fatalf("remote ShellKind() = %q; want the unknown kind", got)
	}
}

func TestCommandReportsProgressBeforeTheEndMarker(t *testing.T) {
	session, transport := newShellSession(t)
	progress := make(chan CommandProgress, 8)
	done := startTestCommand(session, context.Background(), "echo hello", CommandOptions{
		Timeout: 5 * time.Second,
		OnProgress: func(snapshot CommandProgress) {
			select {
			case progress <- snapshot:
			default:
			}
		},
	})

	var marker string
	eventually(t, "the wrapped command to reach the transport", func() bool {
		marker = markerPattern.FindString(transport.input())
		return marker != "" && strings.Contains(transport.input(), "\n")
	})
	transport.send("\n" + marker + "_S\r\npartial output\r\n")
	select {
	case snapshot := <-progress:
		if !strings.Contains(snapshot.Stdout, "partial output") {
			t.Fatalf("progress stdout = %q; want partial output", snapshot.Stdout)
		}
		if snapshot.TotalOutputChars == 0 {
			t.Fatalf("progress total = %d; want a non-zero count", snapshot.TotalOutputChars)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no progress snapshot arrived before the end marker")
	}
	transport.send(marker + "_E:0\r\n")
	got := awaitTestCommand(t, done)
	if got.err != nil || got.result.Stdout != "partial output" {
		t.Fatalf("Command = %+v (%v)", got.result, got.err)
	}
}

func TestCommandReportsShellKind(t *testing.T) {
	session, _ := newShellSession(t)
	if got := session.ShellKind(); got != "posix" {
		t.Fatalf("ShellKind() = %q; want posix", got)
	}
	network, _ := newNetworkDeviceSession(t)
	if got := network.ShellKind(); got != "raw" {
		t.Fatalf("network ShellKind() = %q; want raw", got)
	}
}

// TestCommandUsesMarkersAndReturnsOutputAndExitCode feeds the marker protocol
// a real PTY would produce: the echoed wrapper (which contains both marker
// strings, but not on their own lines), a start line, the command output, and
// an end line with the exit status.
func TestCommandUsesMarkersAndReturnsOutputAndExitCode(t *testing.T) {
	session, transport := newShellSession(t)
	done := startTestCommand(session, context.Background(), "echo hello", CommandOptions{
		Timeout: 5 * time.Second,
	})

	var marker string
	eventually(t, "the wrapped command to reach the transport", func() bool {
		marker = markerPattern.FindString(transport.input())
		return marker != "" && strings.Contains(transport.input(), "\n")
	})
	// The echo contains both markers inline; only the printf output is on its
	// own line, so an inline end marker must not complete the command early.
	transport.send(" " + marker + "=0; printf '" + marker + "_S'; printf '" + marker + "_E:0'\r\n")
	select {
	case early := <-done:
		t.Fatalf("command completed on the echoed wrapper: %+v", early)
	case <-time.After(80 * time.Millisecond):
	}
	transport.send("\n" + marker + "_S\r\nhello\r\n" + marker + "_E:7\r\n$ ")

	got := awaitTestCommand(t, done)
	if got.err != nil {
		t.Fatalf("Command: %v", got.err)
	}
	if got.result.Stdout != "hello" {
		t.Fatalf("stdout = %q; want hello", got.result.Stdout)
	}
	if !got.result.HasExitCode || got.result.ExitCode != 7 {
		t.Fatalf("exit = (%v, %d); want (true, 7)", got.result.HasExitCode, got.result.ExitCode)
	}
}

func TestCommandTimeoutSendsInterruptAndReturnsPartialOutput(t *testing.T) {
	session, transport := newShellSession(t)
	done := startTestCommand(session, context.Background(), "sleep 100", CommandOptions{
		Timeout: 60 * time.Millisecond,
	})
	var marker string
	eventually(t, "the wrapper", func() bool {
		marker = markerPattern.FindString(transport.input())
		return marker != ""
	})
	transport.send("\n" + marker + "_S\r\npartial\r\n")
	got := awaitTestCommand(t, done)
	if !errors.Is(got.err, ErrCommandTimeout) {
		t.Fatalf("err = %v; want ErrCommandTimeout", got.err)
	}
	if got.result.Stdout != "partial" {
		t.Fatalf("partial stdout = %q; want partial", got.result.Stdout)
	}
	eventually(t, "Ctrl+C after the timeout", func() bool {
		return strings.Contains(transport.input(), "\x03")
	})
}

func TestCommandCancellationSendsInterrupt(t *testing.T) {
	session, transport := newShellSession(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := startTestCommand(session, ctx, "sleep 100", CommandOptions{Timeout: 5 * time.Second})
	eventually(t, "the wrapper", func() bool {
		return markerPattern.MatchString(transport.input())
	})
	cancel()
	got := awaitTestCommand(t, done)
	if !errors.Is(got.err, ErrCommandCancelled) {
		t.Fatalf("err = %v; want ErrCommandCancelled", got.err)
	}
	eventually(t, "Ctrl+C after cancellation", func() bool {
		return strings.Contains(transport.input(), "\x03")
	})
}

func TestCommandOutputIsBoundedAndOffsetIsMonotonic(t *testing.T) {
	session, transport := newShellSession(t)
	done := startTestCommand(session, context.Background(), "yes", CommandOptions{
		Timeout: 5 * time.Second, MaxOutputBytes: 32,
	})
	var marker string
	eventually(t, "the wrapper", func() bool {
		marker = markerPattern.FindString(transport.input())
		return marker != ""
	})
	transport.send("\n" + marker + "_S\r\n0123456789abcdefghij0123456789abcdefghij0123456789abcdefghij\r\n" + marker + "_E:0\r\n")
	got := awaitTestCommand(t, done)
	if got.err != nil {
		t.Fatalf("Command: %v", got.err)
	}
	if !got.result.Truncated {
		t.Fatal("Truncated = false for output over the cap")
	}
	if len(got.result.Stdout) > 32 {
		t.Fatalf("stdout is %d bytes; cap was 32", len(got.result.Stdout))
	}
	if got.result.Stdout != "abcdefghij0123456789abcdefghij" {
		t.Fatalf("bounded stdout = %q; want the newest 32 captured bytes trimmed", got.result.Stdout)
	}
	if got.result.OutputBaseOffset != 30 {
		t.Fatalf("OutputBaseOffset = %d; want 30 dropped bytes", got.result.OutputBaseOffset)
	}
}

func TestCommandFindsMarkersAfterConPTYEscapeSequences(t *testing.T) {
	// ConPTY emits cursor/color escapes immediately before a line that is
	// visually on its own line, without an LF before the marker. PowerShell
	// 7 does exactly this; the collector must still find both markers.
	session, transport := newShellSession(t)
	done := startTestCommand(session, context.Background(), "echo hello", CommandOptions{
		Timeout: 5 * time.Second,
	})
	var marker string
	eventually(t, "the wrapper", func() bool {
		marker = markerPattern.FindString(transport.input())
		return marker != ""
	})
	transport.send("\x1b[?25h\x1b[m" + marker + "_S\r\nhello\r\n" +
		"\x1b[?25l" + marker + "_E:0\r\nPS C:\\> ")
	got := awaitTestCommand(t, done)
	if got.err != nil {
		t.Fatalf("Command: %v", got.err)
	}
	if got.result.Stdout != "hello" || !got.result.HasExitCode || got.result.ExitCode != 0 {
		t.Fatalf("result = %+v; want hello with exit 0", got.result)
	}
}

func TestCommandOutputExactlyAtTheLimitIsNotTruncated(t *testing.T) {
	session, transport := newShellSession(t)
	done := startTestCommand(session, context.Background(), "printf", CommandOptions{
		Timeout: 5 * time.Second, MaxOutputBytes: 20,
	})
	var marker string
	eventually(t, "the wrapper", func() bool {
		marker = markerPattern.FindString(transport.input())
		return marker != ""
	})
	transport.send("\n" + marker + "_S\r\n0123456789abcdefgh\r\n" + marker + "_E:0\r\n")
	got := awaitTestCommand(t, done)
	if got.err != nil {
		t.Fatalf("Command: %v", got.err)
	}
	if got.result.Truncated || got.result.OutputBaseOffset != 0 {
		t.Fatalf("exactly-at-limit result = %+v; want no truncation", got.result)
	}
	if got.result.Stdout != "0123456789abcdefgh" {
		t.Fatalf("stdout = %q", got.result.Stdout)
	}
}

func TestCommandOnNetworkDeviceUsesRawExecution(t *testing.T) {
	session, transport := newNetworkDeviceSession(t)
	done := startTestCommand(session, context.Background(), "show version", CommandOptions{
		Timeout: 2 * time.Second, RawIdle: 30 * time.Millisecond,
	})
	eventually(t, "the bare command", func() bool { return transport.input() == "show version\r" })
	transport.send("VRP\r\n[switch]\r\n")
	got := awaitTestCommand(t, done)
	if got.err != nil {
		t.Fatalf("Command: %v", got.err)
	}
	if got.result.Stdout != "VRP\n[switch]" {
		t.Fatalf("stdout = %q; want raw output", got.result.Stdout)
	}
}

func TestCommandIsSerializedOnOneSession(t *testing.T) {
	session, transport := newShellSession(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := startTestCommand(session, ctx, "sleep 100", CommandOptions{Timeout: 5 * time.Second})
	eventually(t, "the first wrapper", func() bool { return markerPattern.MatchString(transport.input()) })
	if _, err := session.Command(context.Background(), "true", CommandOptions{Timeout: time.Second}); !errors.Is(err, ErrCommandBusy) {
		t.Fatalf("second Command error = %v; want ErrCommandBusy", err)
	}
	cancel()
	<-first
}
