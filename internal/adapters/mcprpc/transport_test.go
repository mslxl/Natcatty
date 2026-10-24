package mcprpc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// startTestServer serves one Handler on a loopback listener and returns its
// port plus a stop function. Readiness is awaited by dialing rather than a
// fixed delay: net.Listen has already bound the port, so Dial cannot race a
// listener that does not exist yet.
func startTestServer(t *testing.T, token string, handler Handler) (port int, stop func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := NewServer(token, handler)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, listener) }()
	stop = func() {
		cancel()
		_ = listener.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("server did not stop")
		}
	}
	t.Cleanup(stop)
	return listener.Addr().(*net.TCPAddr).Port, stop
}

func dialTestClient(t *testing.T, port int) *Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := Dial(ctx, "127.0.0.1", port)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func echoHandler() HandlerFunc {
	return func(_ context.Context, method string, params json.RawMessage) (any, error) {
		switch method {
		case "echo":
			var body struct {
				Value any `json:"value"`
			}
			_ = json.Unmarshal(params, &body)
			return map[string]any{"ok": true, "echo": body.Value}, nil
		case "fail":
			return nil, errors.New("boom")
		default:
			return nil, errors.New("unknown method " + method)
		}
	}
}

func TestClientAuthenticatesBeforeAnyRequest(t *testing.T) {
	port, _ := startTestServer(t, "right-token", echoHandler())
	client := dialTestClient(t, port)

	ctx := context.Background()
	if _, err := client.Call(ctx, "echo", map[string]any{"value": 1}); err == nil {
		t.Fatal("an unauthenticated request succeeded")
	} else if code := ErrorCode(err); code != CodeAuthRequired {
		t.Fatalf("code = %d, want %d (%v)", code, CodeAuthRequired, err)
	}
}

func TestClientRejectsAWrongToken(t *testing.T) {
	port, _ := startTestServer(t, "right-token", echoHandler())
	client := dialTestClient(t, port)
	err := client.Verify(context.Background(), "wrong-token")
	if err == nil {
		t.Fatal("a wrong token was accepted")
	}
	if code := ErrorCode(err); code != CodeAuthRequired {
		t.Fatalf("code = %d, want %d (%v)", code, CodeAuthRequired, err)
	}
}

func TestClientVerifiesThenDispatchesAndMapsErrors(t *testing.T) {
	port, _ := startTestServer(t, "token", echoHandler())
	client := dialTestClient(t, port)
	ctx := context.Background()
	if err := client.Verify(ctx, "token"); err != nil {
		t.Fatalf("verify: %v", err)
	}
	result, err := client.Call(ctx, "echo", map[string]any{"value": "hello"})
	if err != nil {
		t.Fatalf("echo: %v", err)
	}
	var decoded struct {
		OK   bool   `json:"ok"`
		Echo string `json:"echo"`
	}
	if err := json.Unmarshal(result, &decoded); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if !decoded.OK || decoded.Echo != "hello" {
		t.Fatalf("result = %+v", decoded)
	}

	_, err = client.Call(ctx, "fail", nil)
	if err == nil || ErrorCode(err) != CodeDispatchFailed {
		t.Fatalf("dispatch error = %v, code = %d", err, ErrorCode(err))
	}
}

// TestASecondConnectionCannotSkipAuth proves authentication is per socket,
// not per server.
func TestASecondConnectionCannotSkipAuth(t *testing.T) {
	port, _ := startTestServer(t, "token", echoHandler())
	first := dialTestClient(t, port)
	if err := first.Verify(context.Background(), "token"); err != nil {
		t.Fatalf("first verify: %v", err)
	}
	second := dialTestClient(t, port)
	if _, err := second.Call(context.Background(), "echo", nil); ErrorCode(err) != CodeAuthRequired {
		t.Fatalf("second connection was not challenged: %v", err)
	}
}

// TestConcurrentClientsShareTheHandler proves the server accepts more than
// one authenticated loopback connection.
func TestConcurrentClientsShareTheHandler(t *testing.T) {
	port, _ := startTestServer(t, "token", echoHandler())
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for index := 0; index < 4; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			client, err := Dial(ctx, "127.0.0.1", port)
			if err != nil {
				errs <- err
				return
			}
			defer client.Close()
			if err := client.Verify(ctx, "token"); err != nil {
				errs <- err
				return
			}
			if _, err := client.Call(ctx, "echo", map[string]any{"value": index}); err != nil {
				errs <- err
			}
		}(index)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent call: %v", err)
	}
}

// TestOversizedLineClosesTheConnection uses a small configured cap so the
// production 10 MiB limit is not allocated in a unit test.
func TestOversizedLineClosesTheConnection(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	server := NewServer("token", echoHandler())
	server.MaxLineBytes = 128
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = server.Serve(ctx, listener) }()

	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, strings.Repeat("x", 1024)+"\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buffer := make([]byte, 1)
	if _, err := conn.Read(buffer); err == nil {
		t.Fatal("the server left an oversized connection open")
	}
}

func TestServeStopsWhenTheContextIsCancelled(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer("token", echoHandler())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, listener) }()
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, net.ErrClosed) {
			t.Fatalf("serve returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after cancellation")
	}
}
