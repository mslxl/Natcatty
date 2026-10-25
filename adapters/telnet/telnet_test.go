package telnet

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"runtime"
	"sync"
	"testing"
	"time"

	appconnection "github.com/mslxl/natcatty/application/connection"
	appsession "github.com/mslxl/natcatty/application/session"
	domaintelnet "github.com/mslxl/natcatty/domain/model/telnet"
)

const waitTimeout = 2 * time.Second

// harness wires a transport to a net.Pipe endpoint. The server side is the
// fake Telnet peer; output and wire channels collect what each direction
// produced.
type harness struct {
	transport *transport
	server    net.Conn
	output    chan []byte
	wire      chan []byte
	readErr   chan error
}

func newHarness(t *testing.T, cfg domaintelnet.Config, cols, rows int) *harness {
	t.Helper()
	client, server := net.Pipe()
	factory := Factory{Dial: func(context.Context, string, string) (net.Conn, error) {
		return client, nil
	}}
	tr, err := factory.Open(context.Background(), telnetSpec(cfg, cols, rows))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	h := &harness{
		transport: tr.(*transport),
		server:    server,
		output:    make(chan []byte, 64),
		wire:      make(chan []byte, 64),
		readErr:   make(chan error, 1),
	}
	go func() {
		buffer := make([]byte, 4096)
		for {
			n, err := h.transport.Read(buffer)
			if n > 0 {
				h.output <- append([]byte(nil), buffer[:n]...)
			}
			if err != nil {
				h.readErr <- err
				return
			}
		}
	}()
	go func() {
		buffer := make([]byte, 4096)
		for {
			n, err := server.Read(buffer)
			if n > 0 {
				h.wire <- append([]byte(nil), buffer[:n]...)
			}
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		_ = tr.Close()
		_ = server.Close()
	})
	return h
}

func telnetSpec(cfg domaintelnet.Config, cols, rows int) appsession.StartSpec {
	return appsession.StartSpec{Kind: appsession.KindTelnet, Telnet: &cfg, Cols: cols, Rows: rows}
}

func (h *harness) send(t *testing.T, data []byte) {
	t.Helper()
	if _, err := h.server.Write(data); err != nil {
		t.Fatalf("fake server write: %v", err)
	}
}

// expect collects chunks until they equal want. Any divergence or timeout
// fails the test.
func expect(t *testing.T, what string, chunks <-chan []byte, want []byte) []byte {
	t.Helper()
	got := []byte{}
	deadline := time.After(waitTimeout)
	for !bytes.Equal(got, want) {
		select {
		case chunk := <-chunks:
			got = append(got, chunk...)
		case <-deadline:
			t.Fatalf("timed out waiting for %s: got %v, want %v", what, got, want)
		}
		if len(got) > len(want) || !bytes.HasPrefix(want, got) {
			t.Fatalf("%s diverged: got %v, want %v", what, got, want)
		}
	}
	return got
}

func (h *harness) expectWire(t *testing.T, want []byte) { expect(t, "wire bytes", h.wire, want) }
func (h *harness) expectOutput(t *testing.T, want []byte) {
	expect(t, "terminal output", h.output, want)
}

var handshake = []byte{
	domaintelnet.IAC, domaintelnet.DO, domaintelnet.OptionSuppressGoAhead,
	domaintelnet.IAC, domaintelnet.WILL, domaintelnet.OptionTerminalType,
	domaintelnet.IAC, domaintelnet.WILL, domaintelnet.OptionNAWS,
}

func nawsFrame(cols, rows int) []byte {
	return []byte{
		domaintelnet.IAC, domaintelnet.SB, domaintelnet.OptionNAWS,
		byte(cols >> 8), byte(cols), byte(rows >> 8), byte(rows),
		domaintelnet.IAC, domaintelnet.SE,
	}
}

func concat(parts ...[]byte) []byte {
	var out []byte
	for _, part := range parts {
		out = append(out, part...)
	}
	return out
}

func TestFactoryDialsTheConfiguredEndpoint(t *testing.T) {
	var network, address string
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	factory := Factory{Dial: func(_ context.Context, gotNetwork, gotAddress string) (net.Conn, error) {
		network, address = gotNetwork, gotAddress
		return client, nil
	}}
	transport, err := factory.Open(context.Background(), telnetSpec(
		domaintelnet.Config{Hostname: " router.local ", Port: 2323}, 0, 0))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer transport.Close()
	if network != "tcp" || address != "router.local:2323" {
		t.Fatalf("dial = %q %q, want tcp router.local:2323", network, address)
	}
}

func TestFactoryRejectsBadConfiguration(t *testing.T) {
	called := 0
	factory := Factory{Dial: func(context.Context, string, string) (net.Conn, error) {
		called++
		return nil, errors.New("unexpected dial")
	}}
	_, err := factory.Open(context.Background(), appsession.StartSpec{Kind: appsession.KindTelnet})
	if !errors.Is(err, domaintelnet.ErrInvalidConfig) {
		t.Fatalf("missing config error = %v, want ErrInvalidConfig", err)
	}
	_, err = factory.Open(context.Background(), telnetSpec(
		domaintelnet.Config{Hostname: "host", Port: 70000}, 0, 0))
	if !errors.Is(err, domaintelnet.ErrInvalidConfig) {
		t.Fatalf("bad port error = %v, want ErrInvalidConfig", err)
	}
	if called != 0 {
		t.Fatalf("a malformed configuration still dialed (%d times)", called)
	}
}

func TestFactoryClassifiesTimeoutNetworkAndCancellation(t *testing.T) {
	// A dial that blocks until its context expires is the connect timeout.
	timingOut := Factory{
		ConnectTimeout: 10 * time.Millisecond,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	_, err := timingOut.Open(context.Background(), telnetSpec(domaintelnet.Config{Hostname: "host"}, 0, 0))
	var typed *appconnection.Error
	if !errors.As(err, &typed) || typed.Kind != appconnection.KindTimeout {
		t.Fatalf("timeout error = %#v, want KindTimeout", err)
	}
	if !errors.Is(err, appconnection.ErrDialTimeout) {
		t.Fatalf("timeout error does not wrap ErrDialTimeout: %v", err)
	}

	refused := Factory{Dial: func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("connection refused")
	}}
	_, err = refused.Open(context.Background(), telnetSpec(domaintelnet.Config{Hostname: "host"}, 0, 0))
	if !errors.As(err, &typed) || typed.Kind != appconnection.KindNetwork {
		t.Fatalf("refused error = %#v, want KindNetwork", err)
	}

	called := 0
	cancelled := Factory{Dial: func(context.Context, string, string) (net.Conn, error) {
		called++
		return nil, errors.New("unexpected")
	}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = cancelled.Open(ctx, telnetSpec(domaintelnet.Config{Hostname: "host"}, 0, 0))
	if !errors.As(err, &typed) || typed.Kind != appconnection.KindCancelled {
		t.Fatalf("cancelled error = %#v, want KindCancelled", err)
	}
	if called != 0 {
		t.Fatalf("a cancelled context still dialed (%d times)", called)
	}
}

func TestNegotiationActivatesLazilyAndLeavesRawDataAlone(t *testing.T) {
	h := newHarness(t, domaintelnet.Config{Hostname: "host"}, 80, 24)
	h.send(t, []byte("hello raw service"))
	h.expectOutput(t, []byte("hello raw service"))

	// The first IAC activates the protocol: exactly one handshake, then the
	// reply to WILL ECHO.
	h.send(t, []byte{domaintelnet.IAC, domaintelnet.WILL, domaintelnet.OptionEcho})
	h.expectWire(t, concat(handshake, []byte{
		domaintelnet.IAC, domaintelnet.DO, domaintelnet.OptionEcho,
	}))
	if echo := h.transport.Echo(); !echo.Remote || echo.Local {
		t.Fatalf("echo after WILL = %+v, want remote only", echo)
	}
}

func TestTransportAnswersNawsTerminalTypeEchoAndRefusals(t *testing.T) {
	h := newHarness(t, domaintelnet.Config{Hostname: "host", TermType: "XTERM-256COLOR"}, 120, 40)
	h.send(t, []byte{
		domaintelnet.IAC, domaintelnet.WILL, domaintelnet.OptionEcho,
		domaintelnet.IAC, domaintelnet.DO, domaintelnet.OptionNAWS,
		domaintelnet.IAC, domaintelnet.SB, domaintelnet.OptionTerminalType, domaintelnet.SuboptionSEND,
		domaintelnet.IAC, domaintelnet.SE,
		domaintelnet.IAC, domaintelnet.DO, domaintelnet.OptionLinemode,
		domaintelnet.IAC, domaintelnet.WILL, domaintelnet.OptionNewEnviron,
	})
	terminalType := concat(
		[]byte{domaintelnet.IAC, domaintelnet.SB, domaintelnet.OptionTerminalType, domaintelnet.SuboptionIS},
		[]byte("XTERM-256COLOR"),
		[]byte{domaintelnet.IAC, domaintelnet.SE},
	)
	h.expectWire(t, concat(
		handshake,
		[]byte{domaintelnet.IAC, domaintelnet.DO, domaintelnet.OptionEcho},
		nawsFrame(120, 40),
		terminalType,
		[]byte{domaintelnet.IAC, domaintelnet.WONT, domaintelnet.OptionLinemode},
		[]byte{domaintelnet.IAC, domaintelnet.DONT, domaintelnet.OptionNewEnviron},
	))

	// WONT ECHO clears remote echo and is not answered further.
	h.send(t, []byte{domaintelnet.IAC, domaintelnet.WONT, domaintelnet.OptionEcho})
	deadline := time.After(waitTimeout)
	for h.transport.Echo().Remote {
		select {
		case <-deadline:
			t.Fatal("WONT ECHO did not clear remote echo")
		default:
			runtime.Gosched()
		}
	}
}

func TestTransportReassemblesCommandsSplitAcrossReads(t *testing.T) {
	h := newHarness(t, domaintelnet.Config{Hostname: "host"}, 80, 24)
	// IAC alone, then the rest of DO NAWS.
	h.send(t, []byte{domaintelnet.IAC})
	h.send(t, []byte{domaintelnet.DO, domaintelnet.OptionNAWS})
	h.expectWire(t, concat(handshake, nawsFrame(80, 24)))

	// An escaped IAC split across reads must not leak as a command.
	h.send(t, []byte{'a', domaintelnet.IAC})
	h.send(t, []byte{domaintelnet.IAC, 'b'})
	h.expectOutput(t, []byte{'a', 0xff, 'b'})

	// An unterminated subnegotiation is buffered, never shown as data.
	h.send(t, append([]byte("x"), domaintelnet.IAC, domaintelnet.SB, domaintelnet.OptionTerminalType, domaintelnet.SuboptionSEND))
	h.expectOutput(t, []byte("x"))
	h.send(t, concat([]byte("Y"), []byte{domaintelnet.IAC, domaintelnet.SE}))
	h.expectWire(t, concat(
		[]byte{domaintelnet.IAC, domaintelnet.SB, domaintelnet.OptionTerminalType, domaintelnet.SuboptionIS},
		[]byte("XTERM-256COLOR"),
		[]byte{domaintelnet.IAC, domaintelnet.SE},
	))
}

func TestTransportNormalizesNewlinesAndEscapesIAC(t *testing.T) {
	h := newHarness(t, domaintelnet.Config{Hostname: "host"}, 80, 24)
	h.send(t, []byte{domaintelnet.IAC, domaintelnet.NOP})
	h.expectWire(t, handshake)

	input := []byte("a\rb\nc\r\nd\r\x00e\xff")
	if _, err := h.transport.Write(input); err != nil {
		t.Fatalf("Write: %v", err)
	}
	h.expectWire(t, []byte("a\r\nb\r\nc\r\nd\r\x00e\xff\xff"))
}

func TestTransportSendsNawsOnlyAfterThePeerEnablesIt(t *testing.T) {
	h := newHarness(t, domaintelnet.Config{Hostname: "host"}, 80, 24)
	h.send(t, []byte{domaintelnet.IAC, domaintelnet.NOP})
	h.expectWire(t, handshake)

	// A resize before DO NAWS emits nothing. If it did, the next expected
	// frame would not match exactly.
	if err := h.transport.Resize(100, 30); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	h.send(t, []byte{domaintelnet.IAC, domaintelnet.DO, domaintelnet.OptionNAWS})
	h.expectWire(t, nawsFrame(100, 30))

	if err := h.transport.Resize(132, 43); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	h.expectWire(t, nawsFrame(132, 43))
}

func TestCleanCloseReportsClosedAndZero(t *testing.T) {
	h := newHarness(t, domaintelnet.Config{Hostname: "host"}, 80, 24)
	if err := h.server.Close(); err != nil {
		t.Fatalf("close server: %v", err)
	}
	select {
	case err := <-h.readErr:
		if err == nil {
			t.Fatal("the read ended without an error")
		}
	case <-time.After(waitTimeout):
		t.Fatal("the read did not end after the peer closed")
	}
	end := h.transport.End()
	if end.Reason != appsession.EndReasonClosed || !end.HasExitCode || end.ExitCode != 0 {
		t.Fatalf("clean end = %+v, want closed/0", end)
	}
	if err := h.transport.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// failingConn is a net.Conn whose reads fail with a transport error.
type failingConn struct {
	mu     sync.Mutex
	writes []byte
	err    error
}

func (c *failingConn) Read([]byte) (int, error) { return 0, c.err }
func (c *failingConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	c.writes = append(c.writes, p...)
	c.mu.Unlock()
	return len(p), nil
}
func (c *failingConn) Close() error                     { return nil }
func (c *failingConn) LocalAddr() net.Addr              { return stubAddr("local") }
func (c *failingConn) RemoteAddr() net.Addr             { return stubAddr("remote") }
func (c *failingConn) SetDeadline(time.Time) error      { return nil }
func (c *failingConn) SetReadDeadline(time.Time) error  { return nil }
func (c *failingConn) SetWriteDeadline(time.Time) error { return nil }

// closeWriteConn blocks a write until the transport is closed and only then
// fails it, so a user close racing a pending write is deterministic.
type closeWriteConn struct {
	started chan struct{}
	release chan struct{}
	closed  chan struct{}
	done    chan struct{}
	once    sync.Once
}

func newCloseWriteConn() *closeWriteConn {
	return &closeWriteConn{
		started: make(chan struct{}),
		release: make(chan struct{}),
		closed:  make(chan struct{}),
		done:    make(chan struct{}),
	}
}

var errWriteDuringClose = errors.New("use of closed network connection")

func (c *closeWriteConn) Read([]byte) (int, error) {
	<-c.closed
	return 0, io.EOF
}
func (c *closeWriteConn) Write([]byte) (int, error) {
	c.once.Do(func() { close(c.started) })
	<-c.release
	return 0, errWriteDuringClose
}
func (c *closeWriteConn) Close() error {
	close(c.release)
	<-c.done
	close(c.closed)
	return nil
}
func (c *closeWriteConn) LocalAddr() net.Addr              { return stubAddr("local") }
func (c *closeWriteConn) RemoteAddr() net.Addr             { return stubAddr("remote") }
func (c *closeWriteConn) SetDeadline(time.Time) error      { return nil }
func (c *closeWriteConn) SetReadDeadline(time.Time) error  { return nil }
func (c *closeWriteConn) SetWriteDeadline(time.Time) error { return nil }

func TestWriteFailingDuringCloseKeepsTheCleanEnd(t *testing.T) {
	conn := newCloseWriteConn()
	factory := Factory{Dial: func(context.Context, string, string) (net.Conn, error) {
		return conn, nil
	}}
	transport, err := factory.Open(context.Background(), telnetSpec(
		domaintelnet.Config{Hostname: "host"}, 80, 24))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	writeDone := make(chan struct{})
	conn.done = writeDone
	go func() {
		_, _ = transport.Write([]byte("x"))
		close(writeDone)
	}()
	select {
	case <-conn.started:
	case <-time.After(waitTimeout):
		t.Fatal("the write did not reach the connection")
	}
	closeDone := make(chan struct{})
	go func() {
		_ = transport.Close()
		close(closeDone)
	}()
	select {
	case <-closeDone:
	case <-time.After(waitTimeout):
		t.Fatal("Close did not return")
	}
	select {
	case <-writeDone:
	case <-time.After(waitTimeout):
		t.Fatal("the racing write did not return")
	}
	if end := transport.(appsession.EndReporter).End(); end.Reason != appsession.EndReasonClosed ||
		!end.HasExitCode || end.ExitCode != 0 {
		t.Fatalf("close racing a write ended as %+v, want closed/0", end)
	}
}

type stubAddr string

func (a stubAddr) Network() string { return "stub" }
func (a stubAddr) String() string  { return string(a) }

func TestTransportFailureEndsAsAnErrorAndClosePreservesIt(t *testing.T) {
	failure := errors.New("connection reset by peer")
	conn := &failingConn{err: failure}
	factory := Factory{Dial: func(context.Context, string, string) (net.Conn, error) {
		return conn, nil
	}}
	transport, err := factory.Open(context.Background(), telnetSpec(
		domaintelnet.Config{Hostname: "host"}, 80, 24))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := transport.Read(make([]byte, 8)); !errors.Is(err, failure) {
		t.Fatalf("Read error = %v, want the transport failure", err)
	}
	end := transport.(appsession.EndReporter).End()
	if end.Reason != appsession.EndReasonError || !end.HasExitCode || end.ExitCode != 1 {
		t.Fatalf("failure end = %+v, want error/1", end)
	}
	if err := transport.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if end := transport.(appsession.EndReporter).End(); end.Reason != appsession.EndReasonError {
		t.Fatalf("Close changed the first end to %+v", end)
	}
}

func TestCloseWhileConnectedIsACleanEnd(t *testing.T) {
	h := newHarness(t, domaintelnet.Config{Hostname: "host"}, 80, 24)
	if err := h.transport.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case <-h.readErr:
	case <-time.After(waitTimeout):
		t.Fatal("the read did not end after the transport closed")
	}
	end := h.transport.End()
	if end.Reason != appsession.EndReasonClosed || end.ExitCode != 0 {
		t.Fatalf("close end = %+v, want closed/0", end)
	}
}

func TestReadHandlesABufferSmallerThanOneChunk(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	factory := Factory{Dial: func(context.Context, string, string) (net.Conn, error) {
		return client, nil
	}}
	transport, err := factory.Open(context.Background(), telnetSpec(
		domaintelnet.Config{Hostname: "host"}, 80, 24))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer transport.Close()
	payload := []byte("0123456789")
	go func() { _, _ = server.Write(payload) }()

	got := make([]byte, 0, len(payload))
	buffer := make([]byte, 3)
	for len(got) < len(payload) {
		if err := setReadDeadline(client, waitTimeout); err != nil {
			t.Fatalf("deadline: %v", err)
		}
		n, err := transport.Read(buffer)
		if err != nil && !errors.Is(err, io.EOF) {
			t.Fatalf("Read: %v", err)
		}
		got = append(got, buffer[:n]...)
	}
	if string(got) != string(payload) {
		t.Fatalf("read %q, want %q", got, payload)
	}
}

func setReadDeadline(conn net.Conn, timeout time.Duration) error {
	return conn.SetReadDeadline(time.Now().Add(timeout))
}
