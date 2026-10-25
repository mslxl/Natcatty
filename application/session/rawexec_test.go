package session

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// rawExecCase is one finished RawExec call observed from a test goroutine.
type rawExecCase struct {
	result RawExecResult
	err    error
}

// startRawExec runs RawExec in the background so the test can feed the
// transport while the command is in flight.
func startRawExec(session *Session, ctx context.Context, command string, opts RawExecOptions) <-chan rawExecCase {
	done := make(chan rawExecCase, 1)
	go func() {
		result, err := session.RawExec(ctx, command, opts)
		done <- rawExecCase{result: result, err: err}
	}()
	return done
}

// awaitRawExec waits for a raw command to finish, with a hard deadline.
func awaitRawExec(t *testing.T, done <-chan rawExecCase) rawExecCase {
	t.Helper()
	select {
	case got := <-done:
		return got
	case <-time.After(10 * time.Second):
		t.Fatal("raw command did not finish")
		return rawExecCase{}
	}
}

// newNetworkDeviceSession opens an SSH Session in network-device mode over the
// fake transport.
func newNetworkDeviceSession(t *testing.T) (*Session, *fakeTransport) {
	t.Helper()
	transport := newFakeTransport()
	service := newTestService(&fakeFactory{transport: transport})
	session, err := service.Open(context.Background(), StartSpec{
		Kind: KindSSH, HostID: "switch-1", Hostname: "switch.example.com", Port: 22,
		Username: "admin", Cols: 80, Rows: 24, NetworkDevice: true,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session, transport
}

func TestNetworkDeviceSessionReportsItsMode(t *testing.T) {
	session, _ := newNetworkDeviceSession(t)
	if !session.NetworkDevice() {
		t.Fatal("NetworkDevice() = false for a network-device Session")
	}
	localTransport := newFakeTransport()
	service := newTestService(&fakeFactory{transport: localTransport})
	local, err := service.OpenLocal(context.Background())
	if err != nil {
		t.Fatalf("OpenLocal: %v", err)
	}
	t.Cleanup(func() { _ = local.Close() })
	if local.NetworkDevice() {
		t.Fatal("NetworkDevice() = true for a local Session")
	}
}

func TestRawExecWritesTheBareCommandAndReportsNoExitCode(t *testing.T) {
	session, transport := newNetworkDeviceSession(t)
	done := startRawExec(session, context.Background(), "show version",
		RawExecOptions{Idle: 40 * time.Millisecond, Timeout: 2 * time.Second})

	// The command reaches the transport as-is: no wrapper, no marker, and a
	// trailing carriage return, which is what a vendor CLI expects.
	eventually(t, "the raw command to reach the transport", func() bool {
		return transport.input() == "show version\r"
	})
	transport.send("\x1b[1;32mshow version\x1b[0m\r\nHuawei VRP\r\n[switch]\r\n")

	got := awaitRawExec(t, done)
	if got.err != nil {
		t.Fatalf("RawExec: %v", got.err)
	}
	if want := "Huawei VRP\n[switch]"; got.result.Stdout != want {
		t.Fatalf("stdout = %q, want %q", got.result.Stdout, want)
	}
	if got.result.HasExitCode {
		t.Fatal("raw execution reported an exit code, but vendor CLIs expose none")
	}
	if got.result.ExitCode != 0 {
		t.Fatalf("exit code = %d, want the zero value", got.result.ExitCode)
	}
	if got.result.Truncated || got.result.NoResponse {
		t.Fatalf("unexpected flags: %+v", got.result)
	}
}

func TestRawExecStripsOscAndCarriageReturns(t *testing.T) {
	session, transport := newNetworkDeviceSession(t)
	done := startRawExec(session, context.Background(), "display clock",
		RawExecOptions{Idle: 30 * time.Millisecond, Timeout: 2 * time.Second})

	eventually(t, "the command to reach the transport", func() bool {
		return transport.input() == "display clock\r"
	})
	transport.send("\x1b]0;switch\x07display clock\r\n2026-10-25 12:00:00\r\n[switch]\r\n")

	got := awaitRawExec(t, done)
	if got.err != nil {
		t.Fatalf("RawExec: %v", got.err)
	}
	if want := "2026-10-25 12:00:00\n[switch]"; got.result.Stdout != want {
		t.Fatalf("stdout = %q, want %q", got.result.Stdout, want)
	}
	if strings.ContainsAny(got.result.Stdout, "\r\x1b") {
		t.Fatalf("stdout still contains control characters: %q", got.result.Stdout)
	}
}

func TestRawExecFinishesAfterTheIdleWindow(t *testing.T) {
	session, transport := newNetworkDeviceSession(t)
	done := startRawExec(session, context.Background(), "display version",
		RawExecOptions{Idle: 60 * time.Millisecond, Timeout: 5 * time.Second})

	eventually(t, "the command to reach the transport", func() bool {
		return transport.input() == "display version\r"
	})
	transport.send("VRP (R) software\r\n")

	got := awaitRawExec(t, done)
	if got.err != nil {
		t.Fatalf("RawExec: %v", got.err)
	}
	if got.result.Stdout != "VRP (R) software" {
		t.Fatalf("stdout = %q", got.result.Stdout)
	}
	if got.result.NoResponse {
		t.Fatal("NoResponse = true after the device answered")
	}
}

func TestRawExecOverallTimeoutInterruptsTheCommand(t *testing.T) {
	session, transport := newNetworkDeviceSession(t)
	done := startRawExec(session, context.Background(), "ping 192.0.2.1",
		RawExecOptions{Idle: 10 * time.Second, Timeout: 2 * time.Second})

	eventually(t, "the command to reach the transport", func() bool {
		return transport.input() == "ping 192.0.2.1\r"
	})
	transport.send("PING 192.0.2.1 ...\r\n")

	got := awaitRawExec(t, done)
	if !errors.Is(got.err, ErrRawExecTimeout) {
		t.Fatalf("error = %v, want ErrRawExecTimeout", got.err)
	}
	if got.result.Stdout != "PING 192.0.2.1 ..." {
		t.Fatalf("partial stdout = %q", got.result.Stdout)
	}
	eventually(t, "Ctrl+C to reach the transport", func() bool {
		return strings.HasSuffix(transport.input(), "\x03")
	})
}

func TestRawExecCancellationInterruptsTheCommand(t *testing.T) {
	session, transport := newNetworkDeviceSession(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := startRawExec(session, ctx, "display interface",
		RawExecOptions{Idle: 10 * time.Second, Timeout: 30 * time.Second})

	eventually(t, "the command to reach the transport", func() bool {
		return transport.input() == "display interface\r"
	})
	cancel()

	got := awaitRawExec(t, done)
	if !errors.Is(got.err, ErrRawExecCancelled) {
		t.Fatalf("error = %v, want ErrRawExecCancelled", got.err)
	}
	eventually(t, "Ctrl+C to reach the transport", func() bool {
		return strings.HasSuffix(transport.input(), "\x03")
	})
}

func TestRawExecCapsTheOutputAndMarksItTruncated(t *testing.T) {
	session, transport := newNetworkDeviceSession(t)
	done := startRawExec(session, context.Background(), "display logbuffer",
		RawExecOptions{Idle: 60 * time.Millisecond, Timeout: 3 * time.Second, MaxOutputBytes: 16})

	eventually(t, "the command to reach the transport", func() bool {
		return transport.input() == "display logbuffer\r"
	})
	transport.send(strings.Repeat("A", 40))

	got := awaitRawExec(t, done)
	if got.err != nil {
		t.Fatalf("RawExec: %v", got.err)
	}
	if !got.result.Truncated {
		t.Fatalf("Truncated = false, result = %+v", got.result)
	}
	if got.result.Stdout != strings.Repeat("A", 16) {
		t.Fatalf("stdout = %q, want exactly the first 16 bytes", got.result.Stdout)
	}
}

func TestRawExecReportsNoResponseInsteadOfHanging(t *testing.T) {
	session, transport := newNetworkDeviceSession(t)
	done := startRawExec(session, context.Background(), "terminal length 0",
		RawExecOptions{Idle: 100 * time.Millisecond, Timeout: 2 * time.Second})

	eventually(t, "the command to reach the transport", func() bool {
		return transport.input() == "terminal length 0\r"
	})
	// A mode change can complete silently. The no-response fallback resolves
	// well before the overall timeout instead of waiting for silence that
	// never starts.
	got := awaitRawExec(t, done)
	if got.err != nil {
		t.Fatalf("RawExec: %v", got.err)
	}
	if !got.result.NoResponse {
		t.Fatal("NoResponse = false without device output")
	}
	if got.result.Stdout != "" {
		t.Fatalf("stdout = %q, want empty", got.result.Stdout)
	}
}

func TestRawExecRejectsAConcurrentCommand(t *testing.T) {
	session, transport := newNetworkDeviceSession(t)
	done := startRawExec(session, context.Background(), "display version",
		RawExecOptions{Idle: 60 * time.Millisecond, Timeout: 2 * time.Second})

	eventually(t, "the first command to reach the transport", func() bool {
		return transport.input() == "display version\r"
	})
	if _, err := session.RawExec(context.Background(), "display clock", RawExecOptions{}); !errors.Is(err, ErrRawExecBusy) {
		t.Fatalf("second RawExec error = %v, want ErrRawExecBusy", err)
	}

	transport.send("VRP\r\n")
	if got := awaitRawExec(t, done); got.err != nil {
		t.Fatalf("first RawExec: %v", got.err)
	}
	if got := transport.input(); got != "display version\r" {
		t.Fatalf("transport input = %q; the busy command must not be written", got)
	}
}

func TestRawExecIsUnavailableOnAShellSession(t *testing.T) {
	transport := newFakeTransport()
	service := newTestService(&fakeFactory{transport: transport})
	session, err := service.OpenLocal(context.Background())
	if err != nil {
		t.Fatalf("OpenLocal: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	if _, err := session.RawExec(context.Background(), "echo hi", RawExecOptions{}); !errors.Is(err, ErrRawExecUnsupported) {
		t.Fatalf("error = %v, want ErrRawExecUnsupported", err)
	}
	if transport.input() != "" {
		t.Fatalf("unsupported RawExec wrote %q", transport.input())
	}
}

func TestRawExecRejectsAnEmptyCommand(t *testing.T) {
	session, transport := newNetworkDeviceSession(t)
	if _, err := session.RawExec(context.Background(), "   ", RawExecOptions{}); !errors.Is(err, ErrRawExecEmptyCommand) {
		t.Fatalf("error = %v, want ErrRawExecEmptyCommand", err)
	}
	if transport.input() != "" {
		t.Fatalf("empty command wrote %q", transport.input())
	}
}

func TestSerialSessionSupportsRawExec(t *testing.T) {
	transport := newFakeTransport()
	service := newTestService(&fakeFactory{transport: transport})
	session, err := service.Open(context.Background(), StartSpec{Kind: KindSerial})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	done := startRawExec(session, context.Background(), "AT",
		RawExecOptions{Idle: 40 * time.Millisecond, Timeout: 2 * time.Second})
	eventually(t, "the AT command to reach the serial transport", func() bool {
		return transport.input() == "AT\r"
	})
	transport.send("OK\r\n")

	got := awaitRawExec(t, done)
	if got.err != nil {
		t.Fatalf("RawExec: %v", got.err)
	}
	if got.result.Stdout != "OK" {
		t.Fatalf("stdout = %q, want OK", got.result.Stdout)
	}
	if got.result.HasExitCode {
		t.Fatal("serial raw execution reported an exit code")
	}
}

func TestCleanRawOutputDropsAnEchoedFirstLineOnly(t *testing.T) {
	cases := []struct {
		name    string
		output  string
		command string
		want    string
	}{
		{"echo", "show run\r\nhostname sw\r\n", "show run", "hostname sw"},
		{"echo after prompt", "sw1#show run\r\nhostname sw\r\n", "show run", "hostname sw"},
		{"no echo", "hostname sw\r\n", "show run", "hostname sw"},
		{"single line", "OK", "AT", "OK"},
		{"ansi", "\x1b[32mOK\x1b[0m\r\n", "AT", "OK"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := cleanRawOutput(testCase.output, testCase.command); got != testCase.want {
				t.Fatalf("cleanRawOutput(%q) = %q, want %q", testCase.output, got, testCase.want)
			}
		})
	}
}
