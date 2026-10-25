//go:build selftest

package app

// Ticket 41 self-test. Press F11, then 4, then 1. The check proves network
// device mode against an in-process fake SSH server reached through the real
// SSH adapter:
//
//   - a Host marked as a network device reaches the shell's Session as raw
//     execution, and the mode never travels to another protocol;
//   - a command is written to the device as-is, without a shell wrapper or
//     completion markers, and the result has no exit code;
//   - idle completion and the overall timeout work, and the overall timeout
//     writes Ctrl+C so the device abandons the command;
//   - output is capped exactly at the configured limit and marked truncated;
//   - a single-channel device banner is detected and only the one session
//     channel is opened: no exec or subsystem channel is requested.
//
// Every wait selects on an observable result with a hard deadline; there is no
// sleep and no external network or hardware.
//
// Result: %TEMP%\natcatty-selftest-41.json.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	xssh "golang.org/x/crypto/ssh"

	"github.com/egoist/mygo/ui"

	sshadapter "github.com/mslxl/natcatty/adapters/ssh"
	"github.com/mslxl/natcatty/adapters/sshtest/fakessh"
	appconnection "github.com/mslxl/natcatty/application/connection"
	appsession "github.com/mslxl/natcatty/application/session"
	domainconn "github.com/mslxl/natcatty/domain/connection"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/selftest"
	shellstate "github.com/mslxl/natcatty/ui/shell/state"
)

const (
	selftest41Ticket = 41
	// selftest41Wait bounds every observable wait in the check.
	selftest41Wait = 5 * time.Second
	// selftest41Banner is a device software token the original treated as
	// single-channel.
	selftest41Banner = "SSH-2.0-BHostSSH_7.0"
)

func (t *selftest41) check() selftest.Check {
	return selftest.Check{
		Ticket: selftest41Ticket,
		Name:   "Network-device raw execution over SSH",
		Steps: []selftest.Step{
			{Name: "host-mode-reaches-the-session", Run: t.hostModeStep},
			{Name: "bare-command-and-no-exit-code", Run: t.commandStep},
			{Name: "idle-and-overall-timeouts", Run: t.timeoutStep},
			{Name: "output-cap-and-single-channel", Run: t.capStep},
		},
	}
}

type selftest41 struct{}

// hostModeStep proves the Host setting reaches the Session only over SSH and
// that raw execution is refused for a shell Session.
func (t *selftest41) hostModeStep(*ui.Context) selftest.Outcome {
	factory := &selftest41ShellFactory{opened: make(chan appsession.StartSpec, 4)}
	service := appsession.NewService(factory)
	defer service.CloseAll()
	state := shellstate.NewState(
		shellstate.WithSessions(service, func(*appsession.Session) (shellstate.Surface, error) {
			return selftest41Surface{}, nil
		}),
	)

	switchHost := domainvault.Host{
		ID: "host-switch", Label: "switch", Hostname: "switch.example.com", Port: 22,
		Username: "admin", Protocol: domainvault.ProtocolSSH,
		DeviceType: domainvault.DeviceTypeNetwork,
	}
	if !switchHost.IsNetworkDevice() {
		return selftest.Failed(errors.New("an SSH Host with network device mode is not recognized"))
	}
	if err := state.OpenHost(switchHost); err != nil {
		return selftest.Failed(fmt.Errorf("OpenHost switch: %w", err))
	}
	var sshSpec appsession.StartSpec
	select {
	case sshSpec = <-factory.opened:
	case <-time.After(selftest41Wait):
		return selftest.Failed(errors.New("the shell did not open the network device Session"))
	}
	if sshSpec.Kind != appsession.KindSSH || !sshSpec.NetworkDevice {
		return selftest.Failed(fmt.Errorf("shell spec = %+v, want SSH raw execution", sshSpec))
	}

	// A hand-edited record pairing the mode with another transport must not
	// put that Session into raw execution.
	console := domainvault.Host{
		ID: "host-console", Label: "console", Hostname: "console.local", Port: 23,
		Username: "admin", Protocol: domainvault.ProtocolTelnet,
		DeviceType: domainvault.DeviceTypeNetwork,
	}
	if console.IsNetworkDevice() {
		return selftest.Failed(errors.New("a Telnet Host was treated as a network device"))
	}
	if err := state.OpenHost(console); err != nil {
		return selftest.Failed(fmt.Errorf("OpenHost console: %w", err))
	}
	var telnetSpec appsession.StartSpec
	select {
	case telnetSpec = <-factory.opened:
	case <-time.After(selftest41Wait):
		return selftest.Failed(errors.New("the shell did not open the Telnet Session"))
	}
	if telnetSpec.Kind != appsession.KindTelnet || telnetSpec.NetworkDevice {
		return selftest.Failed(fmt.Errorf("Telnet spec = %+v, want no raw execution", telnetSpec))
	}

	// The raw executor is an application capability, not a property of every
	// Session; a local shell must refuse it without writing anything.
	shellTransport := &selftest41BlockingTransport{closed: make(chan struct{})}
	shellService := appsession.NewService(&selftest41SessionFactory{transport: shellTransport})
	defer shellService.CloseAll()
	local, err := shellService.OpenLocal(context.Background())
	if err != nil {
		return selftest.Failed(fmt.Errorf("OpenLocal: %w", err))
	}
	if _, err := local.RawExec(context.Background(), "echo hi", appsession.RawExecOptions{}); !errors.Is(err, appsession.ErrRawExecUnsupported) {
		return selftest.Failed(fmt.Errorf("RawExec on a shell Session = %v, want ErrRawExecUnsupported", err))
	}

	return selftest.Passed(
		"an SSH network-device Host reached the Session as raw execution, a Telnet Host never did, and a local shell refused RawExec",
		"domain/vault.Host.IsNetworkDevice + ui/shell/state.OpenHost + application/session.RawExec")
}

// commandStep proves one command is written bare and the result carries no exit
// code, while a second command is refused as busy and an empty one is never
// sent.
func (t *selftest41) commandStep(*ui.Context) selftest.Outcome {
	received := make(chan string, 1)
	release := make(chan struct{})
	fixture, err := selftest41OpenFixture(selftest41Banner, func(channel xssh.Channel) {
		line, err := selftest41ReadCommand(channel)
		if err != nil {
			return
		}
		received <- line
		<-release
		_, _ = io.WriteString(channel, "show version\r\nVRP (R) software\r\n[switch]\r\n")
		_, _ = io.Copy(io.Discard, channel)
	})
	if err != nil {
		return selftest.Failed(err)
	}
	defer fixture.Close()

	done := selftest41StartRaw(fixture.session, context.Background(), "show version",
		appsession.RawExecOptions{Idle: 500 * time.Millisecond, Timeout: selftest41Wait})
	select {
	case line := <-received:
		if line != "show version\r" {
			return selftest.Failed(fmt.Errorf("device received %q, want the bare command and CR", line))
		}
	case <-time.After(selftest41Wait):
		return selftest.Failed(errors.New("the command did not reach the device"))
	}

	if _, err := fixture.session.RawExec(context.Background(), "display clock", appsession.RawExecOptions{}); !errors.Is(err, appsession.ErrRawExecBusy) {
		return selftest.Failed(fmt.Errorf("concurrent RawExec = %v, want ErrRawExecBusy", err))
	}
	if _, err := fixture.session.RawExec(context.Background(), "   ", appsession.RawExecOptions{}); !errors.Is(err, appsession.ErrRawExecEmptyCommand) {
		return selftest.Failed(fmt.Errorf("empty RawExec = %v, want ErrRawExecEmptyCommand", err))
	}
	close(release)

	outcome, err := selftest41WaitRaw("the raw command", done)
	if err != nil {
		return selftest.Failed(err)
	}
	if outcome.err != nil {
		return selftest.Failed(fmt.Errorf("RawExec: %w", outcome.err))
	}
	if want := "VRP (R) software\n[switch]"; outcome.result.Stdout != want {
		return selftest.Failed(fmt.Errorf("stdout = %q, want %q", outcome.result.Stdout, want))
	}
	if outcome.result.HasExitCode {
		return selftest.Failed(errors.New("raw execution reported an exit code"))
	}
	if !fixture.session.NetworkDevice() {
		return selftest.Failed(errors.New("the Session does not report network device mode"))
	}
	if version := fixture.remoteVersion(); version != selftest41Banner {
		return selftest.Failed(fmt.Errorf("remote version = %q, want %q", version, selftest41Banner))
	}

	return selftest.Passed(
		"the device received exactly \"show version\\r\", the echoed line and ANSI were stripped, and the result carried no exit code",
		"application/session.RawExec + adapters/ssh interactive channel")
}

// timeoutStep proves idle completion and the overall timeout, including the
// Ctrl+C interrupt on timeout.
func (t *selftest41) timeoutStep(*ui.Context) selftest.Outcome {
	commands := make(chan string, 4)
	interrupted := make(chan struct{})
	handlerDone := make(chan struct{})
	fixture, err := selftest41OpenFixture(selftest41Banner, func(channel xssh.Channel) {
		defer close(handlerDone)
		reader := selftest41NewChannelReader(channel)
		for {
			line, ok := reader.command()
			if !ok {
				return
			}
			commands <- line
			switch line {
			case "display clock\r":
				_, _ = io.WriteString(channel, "display clock\r\n12:00:00\r\n[switch]\r\n")
			case "ping 203.0.113.1\r":
				_, _ = io.WriteString(channel, "PING 203.0.113.1 ...\r\n")
				if reader.interrupt() {
					close(interrupted)
				}
				return
			}
		}
	})
	if err != nil {
		return selftest.Failed(err)
	}
	defer fixture.Close()

	// Silence after the answer ends the command: no marker is needed.
	idleDone := selftest41StartRaw(fixture.session, context.Background(), "display clock",
		appsession.RawExecOptions{Idle: 200 * time.Millisecond, Timeout: selftest41Wait})
	select {
	case line := <-commands:
		if line != "display clock\r" {
			return selftest.Failed(fmt.Errorf("device received %q", line))
		}
	case <-time.After(selftest41Wait):
		return selftest.Failed(errors.New("the idle command did not reach the device"))
	}
	idle, err := selftest41WaitRaw("the idle command", idleDone)
	if err != nil {
		return selftest.Failed(err)
	}
	if idle.err != nil {
		return selftest.Failed(fmt.Errorf("idle RawExec: %w", idle.err))
	}
	if want := "12:00:00\n[switch]"; idle.result.Stdout != want {
		return selftest.Failed(fmt.Errorf("idle stdout = %q, want %q", idle.result.Stdout, want))
	}
	if idle.result.HasExitCode {
		return selftest.Failed(errors.New("the idle result reported an exit code"))
	}

	// The device keeps working; the overall budget interrupts it.
	timeoutDone := selftest41StartRaw(fixture.session, context.Background(), "ping 203.0.113.1",
		appsession.RawExecOptions{Idle: 10 * time.Second, Timeout: 800 * time.Millisecond})
	select {
	case line := <-commands:
		if line != "ping 203.0.113.1\r" {
			return selftest.Failed(fmt.Errorf("device received %q", line))
		}
	case <-time.After(selftest41Wait):
		return selftest.Failed(errors.New("the long command did not reach the device"))
	}
	timedOut, err := selftest41WaitRaw("the overall timeout", timeoutDone)
	if err != nil {
		return selftest.Failed(err)
	}
	if !errors.Is(timedOut.err, appsession.ErrRawExecTimeout) {
		return selftest.Failed(fmt.Errorf("overall timeout error = %v, want ErrRawExecTimeout", timedOut.err))
	}
	if timedOut.result.Stdout != "PING 203.0.113.1 ..." {
		return selftest.Failed(fmt.Errorf("partial stdout = %q", timedOut.result.Stdout))
	}
	select {
	case <-interrupted:
	case <-time.After(selftest41Wait):
		return selftest.Failed(errors.New("the timeout did not write Ctrl+C"))
	}
	if timedOut.result.HasExitCode {
		return selftest.Failed(errors.New("the timed-out result reported an exit code"))
	}

	return selftest.Passed(
		"device silence finished the command without markers; the overall budget interrupted a running command with Ctrl+C",
		"application/session.RawExec idle and overall timers")
}

// capStep proves the output cap and that a single-channel banner is driven
// through the one interactive channel.
func (t *selftest41) capStep(*ui.Context) selftest.Outcome {
	fixture, err := selftest41OpenFixture(selftest41Banner, func(channel xssh.Channel) {
		if _, err := selftest41ReadCommand(channel); err != nil {
			return
		}
		_, _ = channel.Write(bytes.Repeat([]byte("A"), 8192))
		_, _ = io.Copy(io.Discard, channel)
	})
	if err != nil {
		return selftest.Failed(err)
	}
	defer fixture.Close()

	done := selftest41StartRaw(fixture.session, context.Background(), "display logbuffer",
		appsession.RawExecOptions{
			Idle: 250 * time.Millisecond, Timeout: selftest41Wait, MaxOutputBytes: 4096,
		})
	outcome, err := selftest41WaitRaw("the capped command", done)
	if err != nil {
		return selftest.Failed(err)
	}
	if outcome.err != nil {
		return selftest.Failed(fmt.Errorf("RawExec: %w", outcome.err))
	}
	if !outcome.result.Truncated {
		return selftest.Failed(fmt.Errorf("Truncated = false: %+v", outcome.result))
	}
	if got, want := len(outcome.result.Stdout), 4096; got != want {
		return selftest.Failed(fmt.Errorf("stdout length = %d, want %d", got, want))
	}
	if outcome.result.Stdout != string(bytes.Repeat([]byte("A"), 4096)) {
		return selftest.Failed(errors.New("the capped output is not the first 4096 bytes"))
	}

	version := fixture.remoteVersion()
	if !appconnection.RemoteRequiresSingleChannel(version) {
		return selftest.Failed(fmt.Errorf("banner %q was not recognized as single-channel", version))
	}
	if channels := fixture.server.SessionChannels(); channels != 1 {
		return selftest.Failed(fmt.Errorf("session channels = %d, want exactly 1", channels))
	}
	if requests := fixture.server.ExecRequests(); len(requests) != 0 {
		return selftest.Failed(fmt.Errorf("exec requests = %q; raw mode must not open another channel", requests))
	}
	if requests := fixture.server.SubsystemRequests(); len(requests) != 0 {
		return selftest.Failed(fmt.Errorf("subsystem requests = %q; raw mode must not open another channel", requests))
	}
	if !fixture.server.ShellOpened() {
		return selftest.Failed(errors.New("the Session did not request an interactive shell"))
	}
	if size, ok := fixture.server.Pty(); !ok || size.Cols != 80 || size.Rows != 24 {
		return selftest.Failed(fmt.Errorf("PTY = %+v, %v; want 80x24", size, ok))
	}

	return selftest.Passed(
		"output was capped at exactly 4096 bytes and marked truncated; a BHostSSH banner used exactly one session channel with no exec or subsystem request",
		"application/session.RawExec cap + adapters/ssh single-channel banner")
}

// selftest41RawOutcome is one finished RawExec call.
type selftest41RawOutcome struct {
	result appsession.RawExecResult
	err    error
}

func selftest41StartRaw(session *appsession.Session, ctx context.Context, command string, opts appsession.RawExecOptions) <-chan selftest41RawOutcome {
	done := make(chan selftest41RawOutcome, 1)
	go func() {
		result, err := session.RawExec(ctx, command, opts)
		done <- selftest41RawOutcome{result: result, err: err}
	}()
	return done
}

func selftest41WaitRaw(what string, done <-chan selftest41RawOutcome) (selftest41RawOutcome, error) {
	select {
	case outcome := <-done:
		return outcome, nil
	case <-time.After(selftest41Wait):
		return selftest41RawOutcome{}, fmt.Errorf("timed out after %s waiting for %s", selftest41Wait, what)
	}
}

// selftest41Fixture is one real SSH adapter Session against an in-process fake
// device.
type selftest41Fixture struct {
	service    *appsession.Service
	session    *appsession.Session
	client     appconnection.Client
	server     *fakessh.Server
	clientConn net.Conn
	serverConn net.Conn
}

func selftest41OpenFixture(version string, shell func(xssh.Channel)) (*selftest41Fixture, error) {
	clientConn, serverConn := fakessh.Pipe()
	server, err := fakessh.Start(serverConn, fakessh.Options{
		Password: "s3cret", ServerVersion: version, Shell: shell,
	})
	if err != nil {
		_ = clientConn.Close()
		_ = serverConn.Close()
		return nil, fmt.Errorf("start the fake SSH device: %w", err)
	}
	fixture := &selftest41Fixture{server: server, clientConn: clientConn, serverConn: serverConn}

	dialer := sshadapter.NewDialer(
		sshadapter.WithDialFunc(func(context.Context, string, string) (net.Conn, error) {
			return clientConn, nil
		}),
		sshadapter.WithTimeouts(selftest41Wait, selftest41Wait),
	)
	client, err := dialer.Dial(context.Background(), appconnection.DialRequest{
		Hostname: "switch.example.com", Port: 22, Username: "admin", Password: "s3cret",
		VerifyHostKey: func(context.Context, domainconn.Key) error { return nil },
	})
	if err != nil {
		fixture.Close()
		return nil, fmt.Errorf("dial the fake SSH device: %w", err)
	}
	fixture.client = client

	terminal, err := client.OpenTerminal(context.Background(), appconnection.TerminalSpec{
		Cols: 80, Rows: 24, Term: "xterm-256color",
	})
	if err != nil {
		fixture.Close()
		return nil, fmt.Errorf("open the device shell: %w", err)
	}

	service := appsession.NewService(&selftest41SessionFactory{transport: terminal})
	session, err := service.Open(context.Background(), appsession.StartSpec{
		Kind: appsession.KindSSH, HostID: "host-switch", Hostname: "switch.example.com",
		Port: 22, Username: "admin", Cols: 80, Rows: 24, NetworkDevice: true,
	})
	if err != nil {
		service.CloseAll()
		fixture.Close()
		return nil, fmt.Errorf("open the raw Session: %w", err)
	}
	fixture.service = service
	fixture.session = session
	return fixture, nil
}

func (f *selftest41Fixture) remoteVersion() string {
	versioned, ok := f.client.(interface{ RemoteVersion() string })
	if !ok {
		return ""
	}
	return versioned.RemoteVersion()
}

func (f *selftest41Fixture) Close() {
	if f.service != nil {
		f.service.CloseAll()
	}
	if f.client != nil {
		_ = f.client.Close()
	}
	if f.server != nil {
		_ = f.server.Close()
	}
	if f.clientConn != nil {
		_ = f.clientConn.Close()
	}
	if f.serverConn != nil {
		_ = f.serverConn.Close()
	}
}

// selftest41SessionFactory hands the real SSH terminal to one Session.
type selftest41SessionFactory struct {
	transport appsession.Transport
}

func (f *selftest41SessionFactory) Open(context.Context, appsession.StartSpec) (appsession.Transport, error) {
	return f.transport, nil
}

// selftest41BlockingTransport stays alive until the Session closes it.
type selftest41BlockingTransport struct {
	closed chan struct{}
}

func (t *selftest41BlockingTransport) Read(p []byte) (int, error) {
	<-t.closed
	return 0, io.EOF
}
func (t *selftest41BlockingTransport) Write(p []byte) (int, error) { return len(p), nil }
func (t *selftest41BlockingTransport) Resize(int, int) error       { return nil }
func (t *selftest41BlockingTransport) Close() error {
	select {
	case <-t.closed:
	default:
		close(t.closed)
	}
	return nil
}

// selftest41ShellFactory records the shell's open request and keeps the
// Session alive.
type selftest41ShellFactory struct {
	opened chan appsession.StartSpec
}

func (f *selftest41ShellFactory) Open(_ context.Context, spec appsession.StartSpec) (appsession.Transport, error) {
	select {
	case f.opened <- spec:
	default:
	}
	return &selftest41BlockingTransport{closed: make(chan struct{})}, nil
}

type selftest41Surface struct{}

func (selftest41Surface) View(c *ui.Context) ui.Element {
	return ui.Text(c, "network-device-surface").Fill()
}
func (selftest41Surface) Close() {}

// selftest41ReadCommand reads one CR-terminated command from a device channel.
func selftest41ReadCommand(channel xssh.Channel) (string, error) {
	reader := selftest41NewChannelReader(channel)
	line, ok := reader.command()
	if !ok {
		return "", errors.New("the device channel closed before a command arrived")
	}
	return line, nil
}

// selftest41ChannelReader splits device input into CR-terminated commands and
// exposes Ctrl+C separately.
type selftest41ChannelReader struct {
	bytes chan byte
}

func selftest41NewChannelReader(channel xssh.Channel) *selftest41ChannelReader {
	reader := &selftest41ChannelReader{bytes: make(chan byte, 4096)}
	go func() {
		defer close(reader.bytes)
		buffer := make([]byte, 1024)
		for {
			n, err := channel.Read(buffer)
			for _, value := range buffer[:n] {
				reader.bytes <- value
			}
			if err != nil {
				return
			}
		}
	}()
	return reader
}

// command returns the next CR-terminated line, including the CR.
func (r *selftest41ChannelReader) command() (string, bool) {
	line := []byte{}
	for value := range r.bytes {
		if value == 0x03 {
			continue
		}
		line = append(line, value)
		if value == '\r' {
			return string(line), true
		}
	}
	return "", false
}

// interrupt reports whether Ctrl+C arrived before the channel ended.
func (r *selftest41ChannelReader) interrupt() bool {
	for value := range r.bytes {
		if value == 0x03 {
			return true
		}
	}
	return false
}
