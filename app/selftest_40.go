//go:build selftest

package app

// Ticket 40 self-test. Press F11, then 4, then 0. The check proves the Telnet
// transport contract against an in-process fake Telnet server:
//
//   - negotiation activates lazily, so raw bytes pass through until the peer
//     sends an IAC;
//   - the client drives DO SUPPRESS-GO-AHEAD / WILL TERMINAL-TYPE / WILL NAWS,
//     answers NAWS, TERMINAL-TYPE SEND, echo changes, and refuses unknown
//     options;
//   - NVT newline normalization and 0xFF escaping are correct on input, and a
//     literal 0xFF in output is unescaped;
//   - commands split across reads and unterminated subnegotiations are
//     reassembled without leaking into the terminal;
//   - a resize reaches NAWS only after the peer enables it, a clean close ends
//     with exit 0, and a transport failure with exit 1;
//   - a connect timeout is a typed timeout failure, and the Session menu's
//     manual reconnect dials the same endpoint again;
//   - a saved Telnet Host opens a Telnet Session through the shell.
//
// Every wait selects on an observable result with a hard deadline; there is no
// sleep and no external network or hardware.
//
// Result: %TEMP%\natcatty-selftest-<pid>\natcatty-selftest-40.json.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"runtime"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	adapterstnet "github.com/mslxl/natcatty/adapters/telnet"
	appconnection "github.com/mslxl/natcatty/application/connection"
	appsession "github.com/mslxl/natcatty/application/session"
	domaintelnet "github.com/mslxl/natcatty/domain/model/telnet"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/selftest"
	shellstate "github.com/mslxl/natcatty/ui/shell/state"
)

const selftest40Ticket = 40

// selftest40Wait bounds every observable wait in the check.
const selftest40Wait = 5 * time.Second

func (t *selftest40) check() selftest.Check {
	return selftest.Check{
		Ticket: selftest40Ticket,
		Name:   "Telnet negotiation, NVT framing, and transport",
		Steps: []selftest.Step{
			{Name: "lazy-negotiation-and-partial-reads", Run: t.lazyStep},
			{Name: "naws-terminal-type-echo-and-refusals", Run: t.optionStep},
			{Name: "nvt-newlines-and-binary-escaping", Run: t.framingStep},
			{Name: "timeout-resize-close-and-manual-reconnect", Run: t.transportStep},
			{Name: "telnet-host-opens-from-the-shell", Run: t.shellStep},
		},
	}
}

type selftest40 struct{}

// selftest40Handshake is the canonical negotiation the client drives.
var selftest40Handshake = []byte{
	domaintelnet.IAC, domaintelnet.DO, domaintelnet.OptionSuppressGoAhead,
	domaintelnet.IAC, domaintelnet.WILL, domaintelnet.OptionTerminalType,
	domaintelnet.IAC, domaintelnet.WILL, domaintelnet.OptionNAWS,
}

func selftest40Naws(cols, rows int) []byte {
	return []byte{
		domaintelnet.IAC, domaintelnet.SB, domaintelnet.OptionNAWS,
		byte(cols >> 8), byte(cols), byte(rows >> 8), byte(rows),
		domaintelnet.IAC, domaintelnet.SE,
	}
}

func selftest40TerminalType(name string) []byte {
	frame := []byte{
		domaintelnet.IAC, domaintelnet.SB, domaintelnet.OptionTerminalType, domaintelnet.SuboptionIS,
	}
	frame = append(frame, name...)
	return append(frame, domaintelnet.IAC, domaintelnet.SE)
}

func selftest40Concat(parts ...[]byte) []byte {
	var out []byte
	for _, part := range parts {
		out = append(out, part...)
	}
	return out
}

// lazyStep proves the protocol is silent until the peer sends an IAC, and
// that commands split across reads are reassembled.
func (t *selftest40) lazyStep(*ui.Context) selftest.Outcome {
	peer, err := newSelftest40Peer(domaintelnet.Config{Hostname: "fake"}, 80, 24)
	if err != nil {
		return selftest.Failed(fmt.Errorf("open the in-process Telnet peer: %w", err))
	}
	defer peer.Close()

	if err := peer.Send([]byte("raw service banner\r\n")); err != nil {
		return selftest.Failed(err)
	}
	if err := peer.ExpectOutput([]byte("raw service banner\r\n")); err != nil {
		return selftest.Failed(err)
	}

	// IAC arrives alone, then the rest of DO NAWS. Negotiation starts only
	// now, and the split command is answered once.
	if err := peer.Send([]byte{domaintelnet.IAC}); err != nil {
		return selftest.Failed(err)
	}
	if err := peer.Send([]byte{domaintelnet.DO, domaintelnet.OptionNAWS}); err != nil {
		return selftest.Failed(err)
	}
	if err := peer.ExpectWire(selftest40Concat(selftest40Handshake, selftest40Naws(80, 24))); err != nil {
		return selftest.Failed(err)
	}

	// IAC IAC split across reads is a literal 0xFF, not a command.
	if err := peer.Send([]byte{'a', domaintelnet.IAC}); err != nil {
		return selftest.Failed(err)
	}
	if err := peer.Send([]byte{domaintelnet.IAC, 'b'}); err != nil {
		return selftest.Failed(err)
	}
	if err := peer.ExpectOutput([]byte{'a', 0xff, 'b'}); err != nil {
		return selftest.Failed(err)
	}

	// An unterminated subnegotiation is buffered, never shown as data.
	if err := peer.Send(selftest40Concat(
		[]byte("x"),
		[]byte{domaintelnet.IAC, domaintelnet.SB, domaintelnet.OptionTerminalType, domaintelnet.SuboptionSEND},
	)); err != nil {
		return selftest.Failed(err)
	}
	if err := peer.ExpectOutput([]byte("x")); err != nil {
		return selftest.Failed(err)
	}
	if err := peer.Send(selftest40Concat(
		[]byte("tail"),
		[]byte{domaintelnet.IAC, domaintelnet.SE},
	)); err != nil {
		return selftest.Failed(err)
	}
	if err := peer.ExpectWire(selftest40TerminalType(domaintelnet.DefaultTermType)); err != nil {
		return selftest.Failed(err)
	}

	return selftest.Passed(
		"raw bytes passed through until the first IAC; split IAC/DO NAWS, split IAC IAC, and a split subnegotiation were reassembled",
		"adapters/telnet.Factory.Dial + in-process net.Pipe Telnet peer")
}

// optionStep proves NAWS, TERMINAL-TYPE, ECHO, and unknown-option refusal.
func (t *selftest40) optionStep(*ui.Context) selftest.Outcome {
	peer, err := newSelftest40Peer(domaintelnet.Config{Hostname: "fake"}, 120, 40)
	if err != nil {
		return selftest.Failed(fmt.Errorf("open the in-process Telnet peer: %w", err))
	}
	defer peer.Close()

	if err := peer.Send([]byte{
		domaintelnet.IAC, domaintelnet.WILL, domaintelnet.OptionEcho,
		domaintelnet.IAC, domaintelnet.DO, domaintelnet.OptionNAWS,
		domaintelnet.IAC, domaintelnet.SB, domaintelnet.OptionTerminalType, domaintelnet.SuboptionSEND,
		domaintelnet.IAC, domaintelnet.SE,
		domaintelnet.IAC, domaintelnet.DO, domaintelnet.OptionLinemode,
		domaintelnet.IAC, domaintelnet.WILL, domaintelnet.OptionNewEnviron,
	}); err != nil {
		return selftest.Failed(err)
	}
	want := selftest40Concat(
		selftest40Handshake,
		[]byte{domaintelnet.IAC, domaintelnet.DO, domaintelnet.OptionEcho},
		selftest40Naws(120, 40),
		selftest40TerminalType(domaintelnet.DefaultTermType),
		[]byte{domaintelnet.IAC, domaintelnet.WONT, domaintelnet.OptionLinemode},
		[]byte{domaintelnet.IAC, domaintelnet.DONT, domaintelnet.OptionNewEnviron},
	)
	if err := peer.ExpectWire(want); err != nil {
		return selftest.Failed(err)
	}
	if echo := peer.Transport.Echo(); !echo.Remote || echo.Local {
		return selftest.Failed(fmt.Errorf("echo after WILL ECHO = %+v, want remote only", echo))
	}

	// WONT ECHO clears remote echo and is answered with DONT ECHO; the
	// trailing data proves the command was applied before the state check.
	if err := peer.Send([]byte{
		domaintelnet.IAC, domaintelnet.WONT, domaintelnet.OptionEcho,
		'z',
	}); err != nil {
		return selftest.Failed(err)
	}
	if err := peer.ExpectWire([]byte{domaintelnet.IAC, domaintelnet.DONT, domaintelnet.OptionEcho}); err != nil {
		return selftest.Failed(err)
	}
	if err := peer.ExpectOutput([]byte("z")); err != nil {
		return selftest.Failed(err)
	}
	if echo := peer.Transport.Echo(); echo.Remote {
		return selftest.Failed(fmt.Errorf("WONT ECHO left remote echo on: %+v", echo))
	}

	// DO ECHO asks the client to echo locally.
	if err := peer.Send(selftest40Concat(
		[]byte{domaintelnet.IAC, domaintelnet.DO, domaintelnet.OptionEcho},
		[]byte("y"),
	)); err != nil {
		return selftest.Failed(err)
	}
	if err := peer.ExpectWire([]byte{domaintelnet.IAC, domaintelnet.WILL, domaintelnet.OptionEcho}); err != nil {
		return selftest.Failed(err)
	}
	if err := peer.ExpectOutput([]byte("y")); err != nil {
		return selftest.Failed(err)
	}
	if echo := peer.Transport.Echo(); !echo.Local {
		return selftest.Failed(fmt.Errorf("DO ECHO did not enable local echo: %+v", echo))
	}

	return selftest.Passed(
		"answered DO/WILL for NAWS, TERMINAL-TYPE, and ECHO; refused LINEMODE and NEW-ENVIRON with WONT/DONT; tracked both echo directions",
		"domain/model/telnet.Negotiator + adapters/telnet transport")
}

// framingStep proves NVT newline normalization and binary IAC handling.
func (t *selftest40) framingStep(*ui.Context) selftest.Outcome {
	peer, err := newSelftest40Peer(domaintelnet.Config{Hostname: "fake"}, 80, 24)
	if err != nil {
		return selftest.Failed(fmt.Errorf("open the in-process Telnet peer: %w", err))
	}
	defer peer.Close()

	if err := peer.Send([]byte{domaintelnet.IAC, domaintelnet.NOP}); err != nil {
		return selftest.Failed(err)
	}
	if err := peer.ExpectWire(selftest40Handshake); err != nil {
		return selftest.Failed(err)
	}

	// Enter (a lone CR) and a lone LF become CR LF, an existing CR LF or CR
	// NUL pair is preserved, and 0xFF is doubled once negotiation is active.
	input := []byte("a\rb\nc\r\nd\r\x00e\xff")
	if _, err := peer.Transport.Write(input); err != nil {
		return selftest.Failed(fmt.Errorf("write terminal input: %w", err))
	}
	if err := peer.ExpectWire([]byte("a\r\nb\r\nc\r\nd\r\x00e\xff\xff")); err != nil {
		return selftest.Failed(err)
	}

	// A doubled IAC from the peer is one literal 0xFF of output data.
	if err := peer.Send([]byte{'p', domaintelnet.IAC, domaintelnet.IAC, 'q'}); err != nil {
		return selftest.Failed(err)
	}
	if err := peer.ExpectOutput([]byte{'p', 0xff, 'q'}); err != nil {
		return selftest.Failed(err)
	}

	return selftest.Passed(
		"input normalized lone CR and LF to CR LF, preserved CR NUL, doubled literal 0xFF on the wire, and unescaped a peer IAC IAC",
		"domain/model/telnet.NormalizeNewlines + EscapeIAC + Parser")
}

// transportStep proves resize gating, both end classifications, the typed
// connect timeout, and the Session menu's manual reconnect.
func (t *selftest40) transportStep(*ui.Context) selftest.Outcome {
	peer, err := newSelftest40Peer(domaintelnet.Config{Hostname: "fake"}, 80, 24)
	if err != nil {
		return selftest.Failed(fmt.Errorf("open the in-process Telnet peer: %w", err))
	}
	if err := peer.Send([]byte{domaintelnet.IAC, domaintelnet.NOP}); err != nil {
		peer.Close()
		return selftest.Failed(err)
	}
	if err := peer.ExpectWire(selftest40Handshake); err != nil {
		peer.Close()
		return selftest.Failed(err)
	}

	// A resize before DO NAWS is silent; the size follows when NAWS is
	// enabled, and later resizes update it.
	if err := peer.Transport.Resize(100, 30); err != nil {
		peer.Close()
		return selftest.Failed(fmt.Errorf("resize before NAWS: %w", err))
	}
	if err := peer.Send([]byte{domaintelnet.IAC, domaintelnet.DO, domaintelnet.OptionNAWS}); err != nil {
		peer.Close()
		return selftest.Failed(err)
	}
	if err := peer.ExpectWire(selftest40Naws(100, 30)); err != nil {
		peer.Close()
		return selftest.Failed(err)
	}
	if err := peer.Transport.Resize(132, 43); err != nil {
		peer.Close()
		return selftest.Failed(fmt.Errorf("resize: %w", err))
	}
	if err := peer.ExpectWire(selftest40Naws(132, 43)); err != nil {
		peer.Close()
		return selftest.Failed(err)
	}

	// A clean close is exit 0.
	if err := peer.CloseServer(); err != nil {
		peer.Close()
		return selftest.Failed(err)
	}
	if err := peer.WaitEnd(); err != nil {
		peer.Close()
		return selftest.Failed(err)
	}
	if end := peer.Transport.End(); end.Reason != appsession.EndReasonClosed || end.ExitCode != 0 {
		peer.Close()
		return selftest.Failed(fmt.Errorf("clean close end = %+v, want closed/0", end))
	}
	peer.Close()

	// A transport read failure is exit 1 and survives Close.
	failure := errors.New("connection reset by peer")
	broken, err := newSelftest40PeerWithConn(&selftest40FailingConn{err: failure}, domaintelnet.Config{Hostname: "fake"})
	if err != nil {
		return selftest.Failed(fmt.Errorf("open the failing Telnet peer: %w", err))
	}
	if _, err := broken.Transport.Read(make([]byte, 4)); !errors.Is(err, failure) {
		broken.Close()
		return selftest.Failed(fmt.Errorf("failing read error = %v, want the transport failure", err))
	}
	if end := broken.Transport.End(); end.Reason != appsession.EndReasonError || end.ExitCode != 1 {
		broken.Close()
		return selftest.Failed(fmt.Errorf("transport failure end = %+v, want error/1", end))
	}
	if err := broken.Transport.Close(); err != nil {
		return selftest.Failed(err)
	}
	if end := broken.Transport.End(); end.Reason != appsession.EndReasonError {
		return selftest.Failed(fmt.Errorf("Close changed the first end to %+v", end))
	}

	// A dial that outlives the connect timeout is a typed timeout.
	timingOut := adapterstnet.Factory{
		ConnectTimeout: 10 * time.Millisecond,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	cfg := domaintelnet.Config{Hostname: "unreachable", Port: 23}
	_, err = timingOut.Open(context.Background(), appsession.StartSpec{
		Kind: appsession.KindTelnet, Telnet: &cfg,
	})
	var typed *appconnection.Error
	if !errors.As(err, &typed) || typed.Kind != appconnection.KindTimeout {
		return selftest.Failed(fmt.Errorf("connect timeout error = %v, want KindTimeout", err))
	}

	// Manual reconnect dials the same endpoint again through the Session
	// service. The policy does not retry automatically.
	factory := &selftest40SessionFactory{}
	service := appsession.NewService(factory)
	defer service.CloseAll()
	spec := appsession.StartSpec{
		Kind: appsession.KindTelnet, HostID: "host-telnet", Telnet: &cfg,
		Reconnect: &appsession.ReconnectPolicy{
			Delay:       time.Hour,
			ShouldRetry: func(appsession.End) bool { return false },
		},
	}
	session, err := service.Open(context.Background(), spec)
	if err != nil {
		return selftest.Failed(fmt.Errorf("open the reconnectable Session: %w", err))
	}
	if !session.CanReconnect() || !session.ReconnectNow() {
		_ = session.Close()
		return selftest.Failed(errors.New("the Telnet Session refused a manual reconnect"))
	}
	if err := selftest40Await("the manual reconnect", func() bool { return factory.Calls() == 2 }); err != nil {
		_ = session.Close()
		return selftest.Failed(err)
	}
	if got := factory.LastSpec(); got.Kind != appsession.KindTelnet || got.Telnet == nil ||
		got.Telnet.Hostname != cfg.Hostname || got.Telnet.Port != cfg.Port {
		_ = session.Close()
		return selftest.Failed(fmt.Errorf("reconnect spec = %+v, want the same endpoint", got))
	}
	_ = session.Close()

	return selftest.Passed(
		"NAWS was silent before DO NAWS and updated on resize; close ended 0, failure ended 1, the connect timeout was typed, and Reconnect dialed the same endpoint",
		"adapters/telnet transport + application/session.ReconnectNow")
}

// shellStep proves a saved Telnet Host opens a Telnet Session through the
// shell's Host connect path.
func (t *selftest40) shellStep(*ui.Context) selftest.Outcome {
	factory := &selftest40ShellFactory{opened: make(chan appsession.StartSpec, 1)}
	service := appsession.NewService(factory)
	defer service.CloseAll()
	state := shellstate.NewState(
		shellstate.WithSessions(service, func(*appsession.Session) (shellstate.Surface, error) {
			return selftest40Surface{}, nil
		}),
	)
	host := domainvault.Host{
		ID: "host-telnet", Label: "console", Hostname: "console.local",
		Port: 23, Username: "admin", Protocol: domainvault.ProtocolTelnet,
	}
	if err := state.OpenHost(host); err != nil {
		return selftest.Failed(fmt.Errorf("OpenHost: %w", err))
	}

	var spec appsession.StartSpec
	select {
	case spec = <-factory.opened:
	case <-time.After(selftest40Wait):
		return selftest.Failed(errors.New("the shell did not open the Telnet transport"))
	}
	if spec.Kind != appsession.KindTelnet || spec.Telnet == nil ||
		spec.Telnet.Hostname != "console.local" || spec.Telnet.Port != 23 {
		return selftest.Failed(fmt.Errorf("shell spec = %+v, want the Telnet endpoint", spec))
	}
	if spec.Reconnect == nil {
		return selftest.Failed(errors.New("the shell dropped the Telnet reconnect policy"))
	}

	if err := selftest40Await("the Telnet Tab", func() bool {
		state.Flush()
		return state.TabCount() == 1
	}); err != nil {
		return selftest.Failed(err)
	}
	if label, _, ok := state.TabLabel(0); !ok || label != "console" {
		return selftest.Failed(fmt.Errorf("Tab label = %q, want console", label))
	}
	snapshot, ok := state.ActiveSnapshot()
	if !ok || snapshot.Kind != appsession.KindTelnet || snapshot.Status != appsession.StatusRunning {
		return selftest.Failed(fmt.Errorf("snapshot = %+v, %v; want a running Telnet Session", snapshot, ok))
	}

	return selftest.Passed(
		"a saved Telnet Host reached the Telnet transport with its reconnect policy and opened a running console Tab",
		"ui/shell/state.OpenHost + appsession.Service")
}

// selftest40Await polls a real condition with a hard deadline. The cadence is
// a scheduler yield, not a synchronization delay.
func selftest40Await(what string, ready func() bool) error {
	deadline := time.Now().Add(selftest40Wait)
	for time.Now().Before(deadline) {
		if ready() {
			return nil
		}
		runtime.Gosched()
	}
	return fmt.Errorf("timed out after %s waiting for %s", selftest40Wait, what)
}

// selftest40Transport is the slice of the real Telnet transport the check
// exercises: the Session byte stream, its end classification, and its echo
// state.
type selftest40Transport interface {
	appsession.Transport
	appsession.EndReporter
	Echo() adapterstnet.EchoState
}

// selftest40Peer is an in-process Telnet endpoint. The peer side is a net.Pipe
// connection; the transport side is the real adapter.
type selftest40Peer struct {
	Transport selftest40Transport
	server    net.Conn
	wire      chan []byte
	output    chan []byte
	readErr   chan error
	closeOnce sync.Once
}

func newSelftest40Peer(cfg domaintelnet.Config, cols, rows int) (*selftest40Peer, error) {
	client, server := net.Pipe()
	peer, err := newSelftest40PeerWithConn(client, cfg)
	if err != nil {
		_ = client.Close()
		_ = server.Close()
		return nil, err
	}
	peer.server = server
	peer.startServerReader()
	peer.setStartingSize(cols, rows)
	return peer, nil
}

func newSelftest40PeerWithConn(conn net.Conn, cfg domaintelnet.Config) (*selftest40Peer, error) {
	factory := adapterstnet.Factory{Dial: func(context.Context, string, string) (net.Conn, error) {
		return conn, nil
	}}
	spec := appsession.StartSpec{Kind: appsession.KindTelnet, Telnet: &cfg}
	transport, err := factory.Open(context.Background(), spec)
	if err != nil {
		return nil, err
	}
	typed, ok := transport.(selftest40Transport)
	if !ok {
		_ = transport.Close()
		return nil, errors.New("the Telnet transport does not report its end and echo state")
	}
	peer := &selftest40Peer{
		Transport: typed,
		wire:      make(chan []byte, 64),
		output:    make(chan []byte, 64),
		readErr:   make(chan error, 1),
	}
	go func() {
		buffer := make([]byte, 4096)
		for {
			n, err := typed.Read(buffer)
			if n > 0 {
				peer.output <- append([]byte(nil), buffer[:n]...)
			}
			if err != nil {
				peer.readErr <- err
				return
			}
		}
	}()
	return peer, nil
}

func (p *selftest40Peer) setStartingSize(cols, rows int) {
	_ = p.Transport.Resize(cols, rows)
}

func (p *selftest40Peer) startServerReader() {
	go func() {
		buffer := make([]byte, 4096)
		for {
			n, err := p.server.Read(buffer)
			if n > 0 {
				p.wire <- append([]byte(nil), buffer[:n]...)
			}
			if err != nil {
				return
			}
		}
	}()
}

func (p *selftest40Peer) Send(data []byte) error {
	if p.server == nil {
		return errors.New("the fake peer has no server side")
	}
	_, err := p.server.Write(data)
	return err
}

func (p *selftest40Peer) ExpectWire(want []byte) error {
	return selftest40Expect("wire bytes", p.wire, want)
}

func (p *selftest40Peer) ExpectOutput(want []byte) error {
	return selftest40Expect("terminal output", p.output, want)
}

func (p *selftest40Peer) CloseServer() error {
	if p.server == nil {
		return nil
	}
	return p.server.Close()
}

func (p *selftest40Peer) WaitEnd() error {
	select {
	case <-p.readErr:
		return nil
	case <-time.After(selftest40Wait):
		return fmt.Errorf("timed out after %s waiting for the Telnet read to end", selftest40Wait)
	}
}

func (p *selftest40Peer) Close() {
	p.closeOnce.Do(func() {
		_ = p.Transport.Close()
		if p.server != nil {
			_ = p.server.Close()
		}
	})
}

func selftest40Expect(what string, chunks <-chan []byte, want []byte) error {
	got := []byte{}
	deadline := time.After(selftest40Wait)
	for !bytes.Equal(got, want) {
		select {
		case chunk := <-chunks:
			got = append(got, chunk...)
		case <-deadline:
			return fmt.Errorf("timed out after %s waiting for %s: got %v, want %v",
				selftest40Wait, what, got, want)
		}
		if len(got) > len(want) || !bytes.HasPrefix(want, got) {
			return fmt.Errorf("%s diverged: got %v, want %v", what, got, want)
		}
	}
	return nil
}

// selftest40FailingConn is a net.Conn whose reads fail as a dead transport.
type selftest40FailingConn struct {
	mu     sync.Mutex
	writes []byte
	err    error
}

func (c *selftest40FailingConn) Read([]byte) (int, error) { return 0, c.err }
func (c *selftest40FailingConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	c.writes = append(c.writes, p...)
	c.mu.Unlock()
	return len(p), nil
}
func (c *selftest40FailingConn) Close() error                     { return nil }
func (c *selftest40FailingConn) LocalAddr() net.Addr              { return selftest40Addr("local") }
func (c *selftest40FailingConn) RemoteAddr() net.Addr             { return selftest40Addr("remote") }
func (c *selftest40FailingConn) SetDeadline(time.Time) error      { return nil }
func (c *selftest40FailingConn) SetReadDeadline(time.Time) error  { return nil }
func (c *selftest40FailingConn) SetWriteDeadline(time.Time) error { return nil }

type selftest40Addr string

func (a selftest40Addr) Network() string { return "stub" }
func (a selftest40Addr) String() string  { return string(a) }

// selftest40SessionFactory opens blocking fake transports and records specs
// for the manual-reconnect step.
type selftest40SessionFactory struct {
	mu     sync.Mutex
	calls  int
	specs  []appsession.StartSpec
	closed []*selftest40BlockingTransport
}

func (f *selftest40SessionFactory) Open(_ context.Context, spec appsession.StartSpec) (appsession.Transport, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.specs = append(f.specs, spec)
	transport := &selftest40BlockingTransport{closed: make(chan struct{})}
	f.closed = append(f.closed, transport)
	return transport, nil
}

func (f *selftest40SessionFactory) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *selftest40SessionFactory) LastSpec() appsession.StartSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.specs) == 0 {
		return appsession.StartSpec{}
	}
	return f.specs[len(f.specs)-1]
}

// selftest40BlockingTransport stays alive until the Session closes it.
type selftest40BlockingTransport struct {
	closed chan struct{}
	once   sync.Once
}

func (t *selftest40BlockingTransport) Read([]byte) (int, error) {
	<-t.closed
	return 0, io.EOF
}
func (t *selftest40BlockingTransport) Write(p []byte) (int, error) { return len(p), nil }
func (t *selftest40BlockingTransport) Resize(int, int) error       { return nil }
func (t *selftest40BlockingTransport) Close() error {
	t.once.Do(func() { close(t.closed) })
	return nil
}

// selftest40ShellFactory records the shell's open request and keeps the
// Session alive.
type selftest40ShellFactory struct {
	opened chan appsession.StartSpec
	once   sync.Once
}

func (f *selftest40ShellFactory) Open(_ context.Context, spec appsession.StartSpec) (appsession.Transport, error) {
	f.once.Do(func() {
		select {
		case f.opened <- spec:
		default:
		}
	})
	return &selftest40BlockingTransport{closed: make(chan struct{})}, nil
}

type selftest40Surface struct{}

func (selftest40Surface) View(c *ui.Context) ui.Element { return ui.Text(c, "telnet-surface").Fill() }
func (selftest40Surface) Close()                        {}
