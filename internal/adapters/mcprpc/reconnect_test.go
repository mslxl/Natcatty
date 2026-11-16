package mcprpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// trackingListener closes every connection it accepted when the test stops it,
// so "the app restarted" is observable synchronously instead of by a delay.
type trackingListener struct {
	net.Listener
	mu    sync.Mutex
	conns map[net.Conn]struct{}
}

func newTrackingListener(t *testing.T) *trackingListener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	return &trackingListener{Listener: listener, conns: map[net.Conn]struct{}{}}
}

func (l *trackingListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	tracked := &trackedConn{Conn: conn, owner: l}
	l.mu.Lock()
	l.conns[conn] = struct{}{}
	l.mu.Unlock()
	return tracked, nil
}

func (l *trackingListener) closeAll() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for conn := range l.conns {
		_ = conn.Close()
	}
}

type trackedConn struct {
	net.Conn
	owner *trackingListener
}

func (c *trackedConn) Close() error {
	c.owner.mu.Lock()
	delete(c.owner.conns, c.Conn)
	c.owner.mu.Unlock()
	return c.Conn.Close()
}

// startRestartableServer runs a real bridge whose stop closes live sockets,
// letting a test simulate an app restart deterministically.
func startRestartableServer(t *testing.T, token string, handler Handler) (port int, stop func()) {
	t.Helper()
	listener := newTrackingListener(t)
	server := NewServer(token, handler)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, listener) }()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			_ = listener.Close()
			listener.closeAll()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("bridge did not stop")
			}
		})
	}
	t.Cleanup(stop)
	return listener.Addr().(*net.TCPAddr).Port, stop
}

// dialAndVerify is the Connector body the command uses: dial, then authenticate.
func dialAndVerify(ctx context.Context, port int, token string) (*Client, error) {
	client, err := Dial(ctx, "127.0.0.1", port)
	if err != nil {
		return nil, err
	}
	if err := client.Verify(ctx, token); err != nil {
		_ = client.Close()
		return nil, err
	}
	return client, nil
}

func callWithTimeout(t *testing.T, client *ReconnectingClient, method string) (json.RawMessage, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return client.Call(ctx, method, map[string]any{"value": "hello"})
}

// TestReconnectingClientRedialsAfterTheAppRestarts is the core acceptance
// bullet: the discovery target changes, the old socket is gone, and the next
// call reconnects and succeeds without restarting the stdio process.
func TestReconnectingClientRedialsAfterTheAppRestarts(t *testing.T) {
	firstPort, stopFirst := startRestartableServer(t, "first-token", echoHandler())

	type target struct {
		port  int
		token string
	}
	var mu sync.Mutex
	current := target{port: firstPort, token: "first-token"}
	connects := 0
	connect := func(ctx context.Context) (*Client, error) {
		mu.Lock()
		chosen := current
		mu.Unlock()
		client, err := dialAndVerify(ctx, chosen.port, chosen.token)
		if err != nil {
			return nil, err
		}
		mu.Lock()
		connects++
		mu.Unlock()
		return client, nil
	}

	client := NewReconnectingClient(connect)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer client.Close()

	if _, err := callWithTimeout(t, client, "echo"); err != nil {
		t.Fatalf("first call: %v", err)
	}

	// Restart the app: the old socket closes and the discovery pointer changes
	// port and token.
	stopFirst()
	secondPort, _ := startRestartableServer(t, "second-token", echoHandler())
	mu.Lock()
	current = target{port: secondPort, token: "second-token"}
	mu.Unlock()

	result, err := callWithTimeout(t, client, "echo")
	if err != nil {
		t.Fatalf("call after restart: %v", err)
	}
	var decoded struct {
		OK   bool   `json:"ok"`
		Echo string `json:"echo"`
	}
	if err := json.Unmarshal(result, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !decoded.OK || decoded.Echo != "hello" {
		t.Fatalf("result = %+v", decoded)
	}

	mu.Lock()
	got := connects
	mu.Unlock()
	if got != 2 {
		t.Fatalf("connects = %d, want 2 (initial + one redial)", got)
	}
}

// eofAfterWriteConn accepts a request and reports EOF on the response, which is
// exactly a call that raced teardown: the app may have executed it, but the
// socket is closed.
type eofAfterWriteConn struct{}

func (eofAfterWriteConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (eofAfterWriteConn) Write(p []byte) (int, error)      { return len(p), nil }
func (eofAfterWriteConn) Close() error                     { return nil }
func (eofAfterWriteConn) LocalAddr() net.Addr              { return dummyAddr("local") }
func (eofAfterWriteConn) RemoteAddr() net.Addr             { return dummyAddr("remote") }
func (eofAfterWriteConn) SetDeadline(time.Time) error      { return nil }
func (eofAfterWriteConn) SetReadDeadline(time.Time) error  { return nil }
func (eofAfterWriteConn) SetWriteDeadline(time.Time) error { return nil }

type dummyAddr string

func (a dummyAddr) Network() string { return "tcp" }
func (a dummyAddr) String() string  { return string(a) }

// TestReconnectingClientRetriesACallThatRacedTeardown proves a call whose
// socket died after the write is retried once on a fresh authenticated
// connection instead of surfacing "closed".
func TestReconnectingClientRetriesACallThatRacedTeardown(t *testing.T) {
	port, _ := startRestartableServer(t, "token", echoHandler())

	connects := 0
	connect := func(ctx context.Context) (*Client, error) {
		connects++
		if connects == 1 {
			// The app accepts the request and then the socket disappears.
			return snoopClient(&eofAfterWriteConn{}), nil
		}
		return dialAndVerify(ctx, port, "token")
	}

	client := NewReconnectingClient(connect)
	defer client.Close()
	if _, err := callWithTimeout(t, client, "echo"); err != nil {
		t.Fatalf("call after teardown race: %v", err)
	}
	if connects != 2 {
		t.Fatalf("connects = %d, want 2", connects)
	}
}

// snoopClient builds a Client whose transport is the supplied fake. It is a
// test-only escape hatch around Dial.
func snoopClient(conn net.Conn) *Client {
	return &Client{conn: conn, reader: bufio.NewReader(conn)}
}

// TestReconnectingClientRetriesOnlyOnce stops the retry after a second closed
// socket, so a broken app cannot make one MCP call loop forever.
func TestReconnectingClientRetriesOnlyOnce(t *testing.T) {
	connects := 0
	connect := func(context.Context) (*Client, error) {
		connects++
		return snoopClient(&eofAfterWriteConn{}), nil
	}
	client := NewReconnectingClient(connect)
	defer client.Close()

	_, err := callWithTimeout(t, client, "echo")
	if err == nil {
		t.Fatal("call over permanently closed sockets succeeded")
	}
	if !IsConnectionClosed(err) {
		t.Fatalf("error = %v, want a closed-connection error", err)
	}
	if connects != 2 {
		t.Fatalf("connects = %d, want 2 (initial + exactly one retry)", connects)
	}
}

// TestReconnectingClientDoesNotRetryDispatchFailures pins that an app-side
// error is returned as-is: retrying could execute the call twice.
func TestReconnectingClientDoesNotRetryDispatchFailures(t *testing.T) {
	port, _ := startRestartableServer(t, "token", echoHandler())
	connects := 0
	connect := func(ctx context.Context) (*Client, error) {
		connects++
		return dialAndVerify(ctx, port, "token")
	}
	client := NewReconnectingClient(connect)
	defer client.Close()
	if _, err := callWithTimeout(t, client, "fail"); err == nil || ErrorCode(err) != CodeDispatchFailed {
		t.Fatalf("dispatch error = %v, code = %d", err, ErrorCode(err))
	}
	if connects != 1 {
		t.Fatalf("connects = %d, want 1", connects)
	}
}

// TestClientRemainsFailFast proves the managed fail-fast behavior is intact:
// the bare Client never redials by itself.
func TestClientRemainsFailFast(t *testing.T) {
	port, stop := startRestartableServer(t, "token", echoHandler())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := dialAndVerify(ctx, port, "token")
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer client.Close()

	stop()
	_, err = client.Call(ctx, "echo", nil)
	if !IsConnectionClosed(err) {
		t.Fatalf("error = %v, want a closed-connection error", err)
	}
	// A second call fails immediately on the same dead client; nothing dialed.
	if _, err := client.Call(ctx, "echo", nil); !errors.Is(err, ErrConnectionClosed) {
		t.Fatalf("second error = %v, want ErrConnectionClosed", err)
	}
}
