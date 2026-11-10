package mcprpc

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"
)

// startPushServer is startTestServer but also returns the server so a test can
// broadcast without going through a request.
func startPushServer(t *testing.T, token string, handler Handler) (*Server, int) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := NewServer(token, handler)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, listener) }()
	t.Cleanup(func() {
		cancel()
		_ = listener.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("server did not stop")
		}
	})
	return server, listener.Addr().(*net.TCPAddr).Port
}

func verifiedPushClient(t *testing.T, port int, token string) *Client {
	t.Helper()
	client := dialTestClient(t, port)
	if err := client.Verify(context.Background(), token); err != nil {
		t.Fatalf("verify: %v", err)
	}
	return client
}

func TestBroadcastReachesEveryAuthenticatedConnection(t *testing.T) {
	server, port := startPushServer(t, "token", echoHandler())
	notifications := make(chan Notification, 4)
	for index := 0; index < 2; index++ {
		client := verifiedPushClient(t, port, "token")
		client.SetNotificationHandler(func(notification Notification) {
			notifications <- notification
		})
	}

	server.Broadcast(NotificationToolsListChanged, map[string]any{"reason": "lifecycle"})
	seen := 0
	for seen < 2 {
		select {
		case notification := <-notifications:
			if notification.Method != NotificationToolsListChanged {
				t.Fatalf("method = %q", notification.Method)
			}
			var params map[string]any
			if err := json.Unmarshal(notification.Params, &params); err != nil {
				t.Fatalf("decode params: %v", err)
			}
			if params["reason"] != "lifecycle" {
				t.Fatalf("params = %#v", params)
			}
			seen++
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d of 2 clients received the broadcast", seen)
		}
	}

	var nilServer *Server
	nilServer.Broadcast(NotificationProgress, nil)
}

func TestNotifierFromContextPushesToTheRequestingConnection(t *testing.T) {
	handler := HandlerFunc(func(ctx context.Context, method string,
		_ json.RawMessage) (any, error) {
		notifier, ok := NotifierFrom(ctx)
		if !ok {
			return nil, errNotifierMissing
		}
		if err := notifier.Notify(NotificationProgress,
			map[string]any{"progress": 50, "progressToken": "tok-1"}); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, nil
	})
	_, port := startPushServer(t, "token", handler)
	client := verifiedPushClient(t, port, "token")
	notifications := make(chan Notification, 1)
	client.SetNotificationHandler(func(notification Notification) {
		notifications <- notification
	})

	result, err := client.Call(context.Background(), "work", nil)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(result, &body); err != nil || body["ok"] != true {
		t.Fatalf("result = %s, %v", result, err)
	}
	select {
	case notification := <-notifications:
		if notification.Method != NotificationProgress {
			t.Fatalf("method = %q", notification.Method)
		}
		var params map[string]any
		if err := json.Unmarshal(notification.Params, &params); err != nil {
			t.Fatalf("decode params: %v", err)
		}
		if params["progress"] != float64(50) ||
			params["progressToken"] != "tok-1" {
			t.Fatalf("params = %#v", params)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the notification did not arrive")
	}
}

var errNotifierMissing = &RPCError{Code: CodeDispatchFailed, Message: "no notifier"}

func TestDisconnectCancelsTheHandlerContext(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	handler := HandlerFunc(func(ctx context.Context, _ string,
		_ json.RawMessage) (any, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return nil, ctx.Err()
	})
	_, port := startPushServer(t, "token", handler)
	client := verifiedPushClient(t, port, "token")

	callDone := make(chan error, 1)
	go func() {
		_, err := client.Call(context.Background(), "block", nil)
		callDone <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the handler never started")
	}
	if err := client.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("disconnect did not cancel the handler context")
	}
	select {
	case err := <-callDone:
		if err == nil {
			t.Fatal("the pending call returned success after a disconnect")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the pending call did not fail")
	}
}
