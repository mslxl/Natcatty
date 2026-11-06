// SPDX-License-Identifier: GPL-3.0-or-later

package et

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/mslxl/natcatty/adapters/et/internal/etgo/bootstrap"
	"github.com/mslxl/natcatty/adapters/et/internal/etgo/etcp"
	"github.com/mslxl/natcatty/adapters/et/internal/etgo/etservertest"
	"github.com/mslxl/natcatty/adapters/et/internal/etgo/protocol"
	"github.com/mslxl/natcatty/adapters/et/internal/etgo/wire"
	appconnection "github.com/mslxl/natcatty/application/connection"
	appsession "github.com/mslxl/natcatty/application/session"
	etmodel "github.com/mslxl/natcatty/domain/model/et"
	"google.golang.org/protobuf/proto"
)

const (
	testID      = "abcdEFGH12345678"
	testPasskey = "0123456789abcdefABCDEF0123456789"
)

// fakeLease is one Connection lease with in-memory exec and forwarding.
type fakeLease struct {
	mu         sync.Mutex
	stream     appconnection.ExecStream
	forwarding appconnection.ForwardingClient
	done       chan struct{}
	command    string
	execErr    error
	fwdErr     error
	released   bool
	discarded  bool
}

func (l *fakeLease) OpenExec(_ context.Context, command string) (appconnection.ExecStream, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.command = command
	return l.stream, l.execErr
}

func (l *fakeLease) Forwarding() (appconnection.ForwardingClient, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.forwarding, l.fwdErr
}

func (l *fakeLease) Release() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.released = true
	return nil
}

func (l *fakeLease) Discard() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.discarded = true
	return nil
}

func (l *fakeLease) Done() <-chan struct{} { return l.done }

func (l *fakeLease) Alive() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	select {
	case <-l.done:
		return false
	default:
		return true
	}
}

func (l *fakeLease) state() (command string, released, discarded bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.command, l.released, l.discarded
}

// memoryExec is an etterminal stdout stream fed from memory.
type memoryExec struct {
	mu     sync.Mutex
	data   []byte
	chunks [][]byte
	closed bool
	code   int
}

func (s *memoryExec) Read(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.chunks) > 0 {
		chunk := s.chunks[0]
		s.chunks = s.chunks[1:]
		return copy(p, chunk), nil
	}
	if len(s.data) == 0 {
		return 0, io.EOF
	}
	n := copy(p, s.data)
	s.data = s.data[n:]
	return n, nil
}

func (s *memoryExec) Write(p []byte) (int, error) { return len(p), nil }
func (s *memoryExec) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}
func (s *memoryExec) Stderr() io.Reader      { return strings.NewReader("") }
func (s *memoryExec) ExitCode() (int, error) { return s.code, nil }

// recordingForwarder records the ET address and dials the fake network.
type recordingForwarder struct {
	mu        sync.Mutex
	addresses []string
	dial      func(ctx context.Context) (net.Conn, error)
}

func (f *recordingForwarder) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	f.mu.Lock()
	f.addresses = append(f.addresses, network+" "+address)
	f.mu.Unlock()
	return f.dial(ctx)
}

func (f *recordingForwarder) ListenTCP(string) (net.Listener, error) {
	return nil, errors.New("not used")
}

func (f *recordingForwarder) firstAddress() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.addresses) == 0 {
		return ""
	}
	return f.addresses[0]
}

func baseETConfig() *etmodel.Config {
	cfg := etmodel.Config{
		Hostname:  "127.0.0.1",
		Port:      2222,
		Username:  "tester",
		ETPort:    2022,
		Auth:      etmodel.Auth{Method: etmodel.AuthPassword, Password: "testpass"},
		SessionID: "inprocess-test",
		Cols:      100,
		Rows:      40,
	}.Normalized()
	return &cfg
}

func inProcessStartSpec() appsession.StartSpec {
	return appsession.StartSpec{
		Kind:     appsession.KindET,
		HostID:   "lab",
		Hostname: "127.0.0.1",
		Port:     2222,
		Username: "tester",
		Cols:     100,
		Rows:     40,
		ET:       baseETConfig(),
	}
}

func newFakeLease(stream appconnection.ExecStream, forwarding appconnection.ForwardingClient) *fakeLease {
	return &fakeLease{stream: stream, forwarding: forwarding, done: make(chan struct{})}
}

func credentialsExec() *memoryExec {
	return &memoryExec{data: []byte("login banner\r\nIDPASSKEY:" + testID + "/" + testPasskey + "\r\n")}
}

func testLogger(buffer *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buffer, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// openFake opens the adapter against the in-process etserver fake. The fake
// server does not answer INITIAL_PAYLOAD itself, so a goroutine records the
// packet (when initial is non-nil) and sends INITIAL_RESPONSE to let Open
// finish.
func openFake(t *testing.T, factory InProcessFactory, lease *fakeLease,
	server *etservertest.Server, initial chan protocol.Packet) appsession.Transport {
	t.Helper()
	return openFakeSpec(t, factory, lease, server, initial, inProcessStartSpec())
}

func openFakeSpec(t *testing.T, factory InProcessFactory, lease *fakeLease,
	server *etservertest.Server, initial chan protocol.Packet, spec appsession.StartSpec) appsession.Transport {
	t.Helper()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		packet, err := server.Recv(ctx)
		if err != nil {
			return
		}
		if initial != nil {
			initial <- packet
		}
		response := &protocol.InitialResponse{}
		encoded, err := proto.Marshal(response)
		if err != nil {
			return
		}
		_ = server.Send(context.Background(), protocol.Packet{
			Header: protocol.HeaderInitialResponse, Payload: encoded,
		})
	}()
	factory.Acquire = func(context.Context, appconnection.Request) (ConnectionLease, error) {
		return lease, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	transport, err := factory.Open(ctx, spec)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	return transport
}

func recvPacket(t *testing.T, server *etservertest.Server) protocol.Packet {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	packet, err := server.Recv(ctx)
	if err != nil {
		t.Fatalf("server Recv: %v", err)
	}
	return packet
}

func readTransport(t *testing.T, transport appsession.Transport, want string) string {
	t.Helper()
	type result struct {
		text string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		var text strings.Builder
		buffer := make([]byte, 1024)
		for {
			n, err := transport.Read(buffer)
			text.Write(buffer[:n])
			if strings.Contains(text.String(), want) {
				done <- result{text: text.String()}
				return
			}
			if err != nil {
				done <- result{text: text.String(), err: err}
				return
			}
		}
	}()
	select {
	case got := <-done:
		if !strings.Contains(got.text, want) {
			t.Fatalf("read %q (err %v), want %q", got.text, got.err, want)
		}
		return got.text
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %q", want)
		return ""
	}
}

func terminalInfo(t *testing.T, packet protocol.Packet) (rows, cols int32) {
	t.Helper()
	if packet.Header != protocol.HeaderTerminalInfo {
		t.Fatalf("packet header = %v, want TERMINAL_INFO", packet.Header)
	}
	info := &protocol.TerminalInfo{}
	if err := proto.Unmarshal(packet.Payload, info); err != nil {
		t.Fatalf("decode TerminalInfo: %v", err)
	}
	return info.GetRow(), info.GetColumn()
}

func TestInProcessLifecycle(t *testing.T) {
	server := etservertest.NewServer(testID, testPasskey)
	network := etservertest.NewNetwork(server)
	t.Cleanup(network.Close)
	forwarding := &recordingForwarder{dial: func(context.Context) (net.Conn, error) {
		return network.DialContext(context.Background(), "tcp", "127.0.0.1:2022")
	}}
	var logs bytes.Buffer
	lease := newFakeLease(credentialsExec(), forwarding)
	initialPackets := make(chan protocol.Packet, 1)
	transport := openFake(t, InProcessFactory{Logger: testLogger(&logs)}, lease, server, initialPackets)

	if got := forwarding.firstAddress(); got != "tcp 127.0.0.1:2022" {
		t.Fatalf("ET dial = %q, want the direct-tcpip route to 127.0.0.1:2022", got)
	}
	command, _, _ := lease.state()
	if !strings.Contains(command, "| etterminal --verbose=0") ||
		!strings.Contains(command, "_xterm-256color") {
		t.Fatalf("bootstrap command = %q", command)
	}

	initial := <-initialPackets
	if initial.Header != protocol.HeaderInitialPayload {
		t.Fatalf("first packet = %v, want INITIAL_PAYLOAD", initial.Header)
	}
	payload := &protocol.InitialPayload{}
	if err := proto.Unmarshal(initial.Payload, payload); err != nil {
		t.Fatalf("decode InitialPayload: %v", err)
	}
	if !payload.GetSupportsExitStatus() {
		t.Fatal("INITIAL_PAYLOAD did not request exit status")
	}
	if rows, cols := terminalInfo(t, recvPacket(t, server)); rows != 40 || cols != 100 {
		t.Fatalf("initial terminal info = %dx%d, want 40x100", rows, cols)
	}

	if err := server.Send(context.Background(), protocol.Packet{
		Header: protocol.HeaderTerminalBuffer, Payload: mustMarshal(t, &protocol.TerminalBuffer{}, func(m *protocol.TerminalBuffer) {
			m.SetBuffer([]byte("hello from etserver"))
		}),
	}); err != nil {
		t.Fatalf("Send output: %v", err)
	}
	readTransport(t, transport, "hello from etserver")

	if _, err := transport.Write([]byte("typing\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	input := recvPacket(t, server)
	if input.Header != protocol.HeaderTerminalBuffer {
		t.Fatalf("input packet header = %v, want TERMINAL_BUFFER", input.Header)
	}
	buffer := &protocol.TerminalBuffer{}
	if err := proto.Unmarshal(input.Payload, buffer); err != nil {
		t.Fatalf("decode input TerminalBuffer: %v", err)
	}
	if string(buffer.GetBuffer()) != "typing\r" {
		t.Fatalf("input = %q, want %q", buffer.GetBuffer(), "typing\r")
	}

	if err := transport.Resize(120, 40); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	if rows, cols := terminalInfo(t, recvPacket(t, server)); rows != 40 || cols != 120 {
		t.Fatalf("resized terminal info = %dx%d, want 40x120", rows, cols)
	}

	if err := server.Send(context.Background(), protocol.Packet{
		Header: protocol.HeaderTerminalExitStatus, Payload: mustMarshal(t, &protocol.TerminalExitStatus{}, func(m *protocol.TerminalExitStatus) {
			m.SetExitcode(7)
		}),
	}); err != nil {
		t.Fatalf("Send exit status: %v", err)
	}
	server.EndSession()

	waiter := transport.(appsession.ExitWaiter)
	if code, err := waiter.Wait(); err != nil || code != 7 {
		t.Fatalf("Wait = (%d, %v), want (7, nil)", code, err)
	}
	end := transport.(appsession.EndReporter).End()
	if end.Reason != appsession.EndReasonExited || !end.HasExitCode || end.ExitCode != 7 {
		t.Fatalf("End = %+v, want a normal exit with status 7", end)
	}
	_, released, discarded := lease.state()
	if !released || discarded {
		t.Fatalf("lease released=%v discarded=%v, want a reusable lease", released, discarded)
	}
	if strings.Contains(logs.String(), testID) {
		t.Fatal("the ET session id appeared in logs")
	}
	if strings.Contains(logs.String(), testPasskey) {
		t.Fatal("the session passkey appeared in logs")
	}
}

// A resize that lands after Resizes is called but before the iterator's first
// step must still be reported. The session service takes the baseline by
// calling Resizes before it reads the initial size; if the baseline were read
// lazily inside the iterator, a resize in that window would be adopted as the
// baseline and the remote pty would keep the initial size.
func TestLocalTerminalReportsAResizeBeforeTheIteratorStarts(t *testing.T) {
	terminal := newLocalTerminal(100, 40)
	t.Cleanup(func() { _ = terminal.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	resizes := terminal.Resizes(ctx)
	terminal.SetSize(120, 40)

	got := make(chan [2]int, 1)
	go func() {
		for size := range resizes {
			got <- [2]int{size.Cols, size.Rows}
			return
		}
	}()
	select {
	case size := <-got:
		if size != [2]int{120, 40} {
			t.Fatalf("resize = %v, want [120 40]", size)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a resize between Resizes and the iterator start was lost")
	}
}

func TestReadErrorSeesTheClassifiedEnd(t *testing.T) {
	server := etservertest.NewServer(testID, testPasskey)
	network := etservertest.NewNetwork(server)
	t.Cleanup(network.Close)
	lease := newFakeLease(credentialsExec(), &recordingForwarder{
		dial: func(context.Context) (net.Conn, error) {
			return network.DialContext(context.Background(), "tcp", "127.0.0.1:2022")
		},
	})
	transport := openFake(t, InProcessFactory{}, lease, server, nil)

	// Consume the initial TERMINAL_INFO, then end the session without an exit
	// status, as a deliberate logout does.
	_ = recvPacket(t, server)
	server.EndSession()

	readDone := make(chan error, 1)
	go func() {
		buffer := make([]byte, 256)
		for {
			if _, err := transport.Read(buffer); err != nil {
				readDone <- err
				return
			}
		}
	}()
	select {
	case err := <-readDone:
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("Read error = %v, want the closed terminal", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Read did not return after the session ended")
	}
	// The Session read loop asks End immediately after Read fails; it must
	// already see the deliberate exit, not the unclassified error default.
	end := transport.(appsession.EndReporter).End()
	if end.Reason != appsession.EndReasonExited {
		t.Fatalf("End = %+v, want exited", end)
	}
}

func TestInProcessCloseClassifiesClosed(t *testing.T) {
	server := etservertest.NewServer(testID, testPasskey)
	network := etservertest.NewNetwork(server)
	t.Cleanup(network.Close)
	lease := newFakeLease(credentialsExec(), &recordingForwarder{
		dial: func(context.Context) (net.Conn, error) {
			return network.DialContext(context.Background(), "tcp", "127.0.0.1:2022")
		},
	})
	transport := openFake(t, InProcessFactory{}, lease, server, nil)

	// Drain the initial terminal info so Close cannot block behind an unread
	// terminal.
	_ = recvPacket(t, server) // TERMINAL_INFO

	if err := transport.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	end := transport.(appsession.EndReporter).End()
	if end.Reason != appsession.EndReasonClosed {
		t.Fatalf("End = %+v, want closed", end)
	}
	_, released, discarded := lease.state()
	if !released || discarded {
		t.Fatalf("lease released=%v discarded=%v, want a reusable lease", released, discarded)
	}
}

func TestBootstrapCommandUsesTheLeaseNotSSH(t *testing.T) {
	server := etservertest.NewServer(testID, testPasskey)
	network := etservertest.NewNetwork(server)
	t.Cleanup(network.Close)
	lease := newFakeLease(credentialsExec(), &recordingForwarder{
		dial: func(context.Context) (net.Conn, error) {
			return network.DialContext(context.Background(), "tcp", "127.0.0.1:2022")
		},
	})
	transport := openFake(t, InProcessFactory{}, lease, server, nil)
	defer transport.Close()

	command, _, _ := lease.state()
	if strings.Contains(command, "ssh ") || strings.Contains(command, "-o") {
		t.Fatalf("bootstrap shelled out to ssh: %q", command)
	}
	if !strings.Contains(command, "echo 'XXX") {
		t.Fatalf("bootstrap did not send a regenerable placeholder: %q", command)
	}
}

func TestBootstrapFailureIsTypedAndRedacted(t *testing.T) {
	exec := &memoryExec{data: []byte("IDPASSKEY:" + testID + "/short/short")}
	factory := InProcessFactory{}
	factory.Acquire = func(context.Context, appconnection.Request) (ConnectionLease, error) {
		return newFakeLease(exec, nil), nil
	}
	_, err := factory.Open(context.Background(), inProcessStartSpec())
	if !errors.Is(err, etmodel.ErrBootstrapFailed) {
		t.Fatalf("Open error = %v, want ErrBootstrapFailed", err)
	}
	if strings.Contains(err.Error(), testID) || strings.Contains(err.Error(), "short/short") {
		t.Fatalf("bootstrap error leaked the printed credentials: %v", err)
	}
}

func TestBootstrapSplitMarkerIsParsed(t *testing.T) {
	exec := &memoryExec{chunks: [][]byte{
		[]byte("banner\r\nIDPASS"),
		[]byte("KEY:" + testID + "/"),
		[]byte(testPasskey[:8]),
		[]byte(testPasskey[8:] + "\r\n"),
	}}
	credentials, err := readBootstrap(context.Background(), exec)
	if err != nil {
		t.Fatalf("readBootstrap: %v", err)
	}
	if credentials.ID != testID || credentials.Passkey() != testPasskey {
		t.Fatalf("credentials = %+v", credentials)
	}
}

func TestUnsupportedServerIsTyped(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var request protocol.ConnectRequest
		if err := wire.ReadMessage(conn, &request); err != nil {
			return
		}
		response := &protocol.ConnectResponse{}
		response.SetStatus(protocol.ConnectStatus_MISMATCHED_PROTOCOL)
		_ = wire.WriteMessage(conn, response)
	}()
	forwarding := &recordingForwarder{dial: func(context.Context) (net.Conn, error) {
		return net.Dial("tcp", listener.Addr().String())
	}}
	lease := newFakeLease(credentialsExec(), forwarding)
	factory := InProcessFactory{}
	factory.Acquire = func(context.Context, appconnection.Request) (ConnectionLease, error) {
		return lease, nil
	}
	_, err = factory.Open(context.Background(), inProcessStartSpec())
	if !errors.Is(err, etmodel.ErrUnsupportedServer) {
		t.Fatalf("Open error = %v, want ErrUnsupportedServer", err)
	}
	if _, _, discarded := lease.state(); !discarded {
		t.Fatal("a failed handshake did not discard the lease")
	}
}

func TestForwardingFailureIsATypedHandshakeFailure(t *testing.T) {
	lease := newFakeLease(credentialsExec(), nil)
	lease.fwdErr = errors.New("forwarding unavailable")
	factory := InProcessFactory{}
	factory.Acquire = func(context.Context, appconnection.Request) (ConnectionLease, error) {
		return lease, nil
	}
	if _, err := factory.Open(context.Background(), inProcessStartSpec()); !errors.Is(err, etmodel.ErrHandshakeFailed) {
		t.Fatalf("Open error = %v, want ErrHandshakeFailed", err)
	}
}

// TestHandshakeTimeoutIsTyped proves a server that accepts the forwarding
// channel and never answers cannot hang Open: the handshake context closes
// the channel and the adapter reports a typed handshake failure.
func TestHandshakeTimeoutIsTyped(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	release := make(chan struct{})
	defer close(release)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		<-release
	}()
	lease := newFakeLease(credentialsExec(), &recordingForwarder{dial: func(context.Context) (net.Conn, error) {
		return net.Dial("tcp", listener.Addr().String())
	}})
	factory := InProcessFactory{HandshakeTimeout: 200 * time.Millisecond}
	factory.Acquire = func(context.Context, appconnection.Request) (ConnectionLease, error) {
		return lease, nil
	}
	start := time.Now()
	_, err = factory.Open(context.Background(), inProcessStartSpec())
	if !errors.Is(err, etmodel.ErrHandshakeFailed) {
		t.Fatalf("Open error = %v, want ErrHandshakeFailed", err)
	}
	if errors.Is(err, etmodel.ErrUnsupportedServer) {
		t.Fatalf("a silent server was classified as an unsupported server: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Open took %v; the handshake timeout did not bound it", elapsed)
	}
	if _, _, discarded := lease.state(); !discarded {
		t.Fatal("a timed-out handshake did not discard the lease")
	}
}

func TestDialFailureIsATypedHandshakeFailure(t *testing.T) {
	lease := newFakeLease(credentialsExec(), &recordingForwarder{dial: func(context.Context) (net.Conn, error) {
		return nil, errors.New("connection refused")
	}})
	factory := InProcessFactory{}
	factory.Acquire = func(context.Context, appconnection.Request) (ConnectionLease, error) {
		return lease, nil
	}
	if _, err := factory.Open(context.Background(), inProcessStartSpec()); !errors.Is(err, etmodel.ErrHandshakeFailed) {
		t.Fatalf("Open error = %v, want ErrHandshakeFailed", err)
	}
}

// TestRetryLaterRetriesWithBackoff proves the adapter's initial connect
// treats RETRY_LATER as a wait, not a fatal handshake: it redials after 250 ms
// and then 500 ms, and accepts the session when the server stops recovering.
func TestRetryLaterRetriesWithBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var dials atomic.Int32
		var servers sync.WaitGroup
		forwarding := &recordingForwarder{dial: func(context.Context) (net.Conn, error) {
			n := int(dials.Add(1)) - 1
			client, server := net.Pipe()
			servers.Go(func() {
				defer server.Close()
				var request protocol.ConnectRequest
				if wire.ReadMessage(server, &request) != nil {
					return
				}
				response := &protocol.ConnectResponse{}
				if n < 2 {
					response.SetStatus(protocol.ConnectStatus_RETRY_LATER)
					_ = wire.WriteMessage(server, response)
					return
				}
				response.SetStatus(protocol.ConnectStatus_NEW_CLIENT)
				if wire.WriteMessage(server, response) != nil {
					return
				}
				_, _ = io.Copy(io.Discard, server)
			})
			return client, nil
		}}
		notices := make(chan appsession.Notice, 8)
		factory := InProcessFactory{}
		type result struct {
			conn *etcp.Conn
			err  error
		}
		done := make(chan result, 1)
		start := time.Now()
		go func() {
			conn, err := factory.dial(t.Context(), forwarding, "et.example:2022",
				bootstrap.NewCredentials(testID, testPasskey), notices)
			done <- result{conn: conn, err: err}
		}()
		got := <-done
		if got.err != nil {
			t.Fatalf("dial after RETRY_LATER: %v", got.err)
		}
		if count := dials.Load(); count != 3 {
			t.Fatalf("dials = %d, want 3 (two RETRY_LATER answers)", count)
		}
		if elapsed := time.Since(start); elapsed < retryLaterDelay+2*retryLaterDelay {
			t.Fatalf("RETRY_LATER retries took %v, want at least %v of backoff",
				elapsed, retryLaterDelay+2*retryLaterDelay)
		}
		if err := got.conn.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		servers.Wait()
	})
}

// TestRetryLaterIsTypedWhenTheWaitEnds: if the server never stops recovering,
// the last answer is still reported as the typed retry-later outcome rather
// than a generic handshake failure.
func TestRetryLaterIsTypedWhenTheWaitEnds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		forwarding := &recordingForwarder{dial: func(context.Context) (net.Conn, error) {
			client, server := net.Pipe()
			go func() {
				defer server.Close()
				var request protocol.ConnectRequest
				if wire.ReadMessage(server, &request) != nil {
					return
				}
				response := &protocol.ConnectResponse{}
				response.SetStatus(protocol.ConnectStatus_RETRY_LATER)
				_ = wire.WriteMessage(server, response)
			}()
			return client, nil
		}}
		factory := InProcessFactory{HandshakeTimeout: 2 * time.Second}
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()
		_, err := factory.dial(ctx, forwarding, "et.example:2022",
			bootstrap.NewCredentials(testID, testPasskey), make(chan appsession.Notice, 8))
		if !errors.Is(err, etcp.ErrRetryLater) {
			t.Fatalf("dial = %v, want ErrRetryLater", err)
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("dial = %v, want the context deadline too", err)
		}
	})
}

// TestOpenRetryLaterIsTyped proves the public Open path keeps the retry-later
// vocabulary when the wait ends. Reporting the generic handshake-timeout
// sentence instead would hide that the server was recovering and asking the
// client to retry.
func TestOpenRetryLaterIsTyped(t *testing.T) {
	var servers sync.WaitGroup
	forwarding := &recordingForwarder{dial: func(context.Context) (net.Conn, error) {
		client, server := net.Pipe()
		servers.Go(func() {
			defer server.Close()
			var request protocol.ConnectRequest
			if wire.ReadMessage(server, &request) != nil {
				return
			}
			response := &protocol.ConnectResponse{}
			response.SetStatus(protocol.ConnectStatus_RETRY_LATER)
			_ = wire.WriteMessage(server, response)
		})
		return client, nil
	}}
	lease := newFakeLease(credentialsExec(), forwarding)
	factory := InProcessFactory{HandshakeTimeout: 300 * time.Millisecond}
	factory.Acquire = func(context.Context, appconnection.Request) (ConnectionLease, error) {
		return lease, nil
	}
	_, err := factory.Open(context.Background(), inProcessStartSpec())
	if !errors.Is(err, etmodel.ErrRetryLater) {
		t.Fatalf("Open error = %v, want ErrRetryLater", err)
	}
	if errors.Is(err, etmodel.ErrHandshakeFailed) {
		t.Fatalf("a recovering server was classified as a handshake failure: %v", err)
	}
	if _, _, discarded := lease.state(); !discarded {
		t.Fatal("a retry-later open did not discard the lease")
	}
	servers.Wait()
}

// TestClassifyDialErrorV7 pins the typed vocabulary for the v7 outcomes.
func TestClassifyDialErrorV7(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		want      error
		permanent bool
	}{
		{"version", etcp.ErrVersion, etmodel.ErrUnsupportedServer, true},
		{"legacy reattach", etcp.ErrLegacyReattach, etmodel.ErrReattachUnsupported, true},
		{"retry later", etcp.ErrRetryLater, etmodel.ErrRetryLater, false},
		{"displaced", etcp.ErrDisplaced, etmodel.ErrSessionDisplaced, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := classifyDialError(fmt.Errorf("wrapped: %w", tt.err))
			if !errors.Is(err, tt.want) {
				t.Fatalf("classifyDialError(%v) = %v, want %v", tt.err, err, tt.want)
			}
			if !errors.Is(err, tt.err) {
				t.Fatalf("classifyDialError(%v) lost the underlying error: %v", tt.err, err)
			}
			var failure interface{ Permanent() bool }
			if got := errors.As(err, &failure); got != tt.permanent {
				t.Fatalf("classifyDialError(%v) permanent = %v, want %v", tt.err, got, tt.permanent)
			}
		})
	}
}

// TestClassifyRunDisplaced proves the Session ends as displaced, not as a
// generic error, so the reconnect policy will not fight the new client.
func TestClassifyRunDisplaced(t *testing.T) {
	end := classifyRun(fmt.Errorf("session: %w", etcp.ErrDisplaced))
	if end.Reason != appsession.EndReasonDisplaced {
		t.Fatalf("End = %+v, want displaced", end)
	}
}

func mustMarshal[T proto.Message](t *testing.T, message T, fill func(T)) []byte {
	t.Helper()
	fill(message)
	encoded, err := proto.Marshal(message)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return encoded
}
