package session

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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

// conptyWrap splits a token the way ConPTY rewrites output that reaches the
// right margin: a CRLF, a cursor-position escape, and a re-rendered copy of
// the cell at the margin are inserted at the wrap point. A narrow real
// PowerShell terminal wraps the marker this way.
func conptyWrap(token string, columns int) string {
	var wrapped strings.Builder
	for index := 0; index < len(token); index += columns {
		end := index + columns
		if end > len(token) {
			end = len(token)
		}
		if index > 0 {
			wrapped.WriteString(fmt.Sprintf("\r\n\x1b[9;%dH", columns))
			wrapped.WriteByte(token[index-1])
		}
		wrapped.WriteString(token[index:end])
	}
	return wrapped.String()
}

// TestCommandToleratesConPTYHardWrappedMarkers proves the marker protocol
// survives a narrow real terminal. ConPTY inserts CRLF and a cursor escape
// inside a long marker line, so the start and end markers arrive split; the
// echoed wrapper, which is also wrapped, must still not start the command.
func TestCommandToleratesConPTYHardWrappedMarkers(t *testing.T) {
	session, transport := newShellSession(t)
	done := startTestCommand(session, context.Background(), "echo hello", CommandOptions{
		Timeout: 5 * time.Second,
	})
	var marker string
	eventually(t, "the wrapped command to reach the transport", func() bool {
		marker = markerPattern.FindString(transport.input())
		return marker != ""
	})
	// The echo of the start marker is quoted and hard-wrapped; it must not be
	// mistaken for the marker line.
	echo := "\r\n\x1b[36m'" + conptyWrap(marker+"_S", 20) + "'\x1b[37m; " + marker + "=0\r\n"
	transport.send(echo)
	transport.send(conptyWrap(marker+"_S", 20) + "\r\nhello\r\n" + conptyWrap(marker+"_E:7", 20) + "\r\n")

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

// TestCommandCollectorScansTheEndMarkerIncrementally feeds a full rolling
// window one byte at a time, the shape a terminal uses when it dribbles a
// megabyte of output. A full rescan of the window per chunk is quadratic and
// can outlast the command deadline; the incremental scanner must stay linear.
func TestCommandCollectorScansTheEndMarkerIncrementally(t *testing.T) {
	const limit = 1 << 20
	marker := "__NCMCP_linear_0123456789abcdef__"
	collector := &commandCollector{marker: marker, limit: limit}
	feedBytes := func(text []byte) {
		for _, character := range text {
			collector.feed([]byte{character})
		}
	}
	feedBytes([]byte(marker + "_S\r\n"))
	if !collector.started {
		t.Fatal("the start marker was not found in single-byte chunks")
	}
	payload := bytes.Repeat([]byte("x"), limit)
	begin := time.Now()
	feedBytes(payload)
	// The end marker is wrapped exactly as a narrow ConPTY splits it, still
	// one byte per chunk, so the resume path is exercised as well.
	feedBytes([]byte(conptyWrap(marker+"_E:7", 20) + "\r\n"))
	elapsed := time.Since(begin)
	if !collector.finished || collector.exitCode != 7 {
		t.Fatalf("collector = finished:%v exit:%d", collector.finished, collector.exitCode)
	}
	result := collector.result()
	if len(result.Stdout) > limit {
		t.Fatalf("stdout = %d bytes, cap %d", len(result.Stdout), limit)
	}
	if result.TotalOutputChars < limit {
		t.Fatalf("total output = %d, want at least %d", result.TotalOutputChars, limit)
	}
	// The quadratic scan would have to examine hundreds of gigabytes here and
	// cannot finish in this window; the linear scan is well under a second.
	if elapsed > 60*time.Second {
		t.Fatalf("single-byte scan took %v; the end-marker search is not incremental", elapsed)
	}
}

// TestCommandHeadlessOutputDoesNotDeadlockOnTerminalBackpressure proves a
// programmatic command on a Session with no attached view can read past the
// terminal's high watermark. Without the fix, the undrained live queue pauses
// the transport at HighWaterMark and the command never sees its end marker.
func TestCommandHeadlessOutputDoesNotDeadlockOnTerminalBackpressure(t *testing.T) {
	session, transport := newShellSession(t)
	done := startTestCommand(session, context.Background(), "generate", CommandOptions{
		Timeout: 5 * time.Second, MaxOutputBytes: 1 << 20,
	})
	var marker string
	eventually(t, "the wrapper", func() bool {
		marker = markerPattern.FindString(transport.input())
		return marker != ""
	})
	transport.send("\n" + marker + "_S\r\n")
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		payload := bytes.Repeat([]byte("x"), (1<<20)+(64<<10))
		for len(payload) > 0 {
			chunk := 16 << 10
			if chunk > len(payload) {
				chunk = len(payload)
			}
			select {
			case transport.output <- append([]byte(nil), payload[:chunk]...):
			case <-stop:
				return
			}
			payload = payload[chunk:]
		}
		select {
		case transport.output <- []byte("\r\n" + marker + "_E:0\r\n"):
		case <-stop:
		}
	}()

	got := awaitTestCommand(t, done)
	if got.err != nil {
		t.Fatalf("Command: %v; the headless terminal backlog stalled the transport", got.err)
	}
	if !got.result.Truncated {
		t.Fatal("Truncated = false for output over the cap")
	}
	if got.result.TotalOutputChars < 1<<20 {
		t.Fatalf("total output = %d; want at least %d", got.result.TotalOutputChars, 1<<20)
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
