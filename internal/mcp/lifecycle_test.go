package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mslxl/natcatty/internal/adapters/mcprpc"
)

// blockingCaller holds one plugin tool call open until its context is
// cancelled. Other methods answer immediately.
type blockingCaller struct {
	started  chan struct{}
	canceled chan struct{}

	startOnce  sync.Once
	cancelOnce sync.Once
}

func newBlockingCaller() *blockingCaller {
	return &blockingCaller{
		started:  make(chan struct{}),
		canceled: make(chan struct{}),
	}
}

func (c *blockingCaller) Call(ctx context.Context, method string, _ map[string]any) (any, error) {
	if method != mcprpc.MethodPluginToolCall {
		return map[string]any{"ok": true}, nil
	}
	c.startOnce.Do(func() { close(c.started) })
	<-ctx.Done()
	c.cancelOnce.Do(func() { close(c.canceled) })
	return nil, ctx.Err()
}

func writeLine(t *testing.T, writer io.Writer, line string) {
	t.Helper()
	if _, err := io.WriteString(writer, line+"\n"); err != nil {
		t.Fatalf("write MCP line: %v", err)
	}
}

func awaitSignal(t *testing.T, signal <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal(what)
	}
}

func decodeOutputLines(t *testing.T, raw string) []map[string]any {
	t.Helper()
	messages := make([]map[string]any, 0, 2)
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var message map[string]any
		if err := json.Unmarshal([]byte(line), &message); err != nil {
			t.Fatalf("decode output line %q: %v", line, err)
		}
		messages = append(messages, message)
	}
	return messages
}

// TestServeCancelsInFlightRequestsOnStdinEOF is the regression for the stdio
// process's disconnect path: a request is running when stdin reaches EOF, and
// Serve must return immediately instead of waiting for the call to finish,
// with the call's context cancelled.
func TestServeCancelsInFlightRequestsOnStdinEOF(t *testing.T) {
	inputReader, inputWriter := io.Pipe()
	var output bytes.Buffer
	caller := newBlockingCaller()
	server := NewServer(caller)
	served := make(chan error, 1)
	go func() { served <- server.Serve(context.Background(), inputReader, &output) }()

	writeLine(t, inputWriter, request(1, "tools/call", map[string]any{
		"name": "plugin.acme.tools.echo",
	}))
	awaitSignal(t, caller.started, "the in-flight call never started")

	if err := inputWriter.Close(); err != nil {
		t.Fatalf("close stdin: %v", err)
	}
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("Serve returned %v after EOF", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return promptly after stdin EOF")
	}
	select {
	case <-caller.canceled:
	default:
		t.Fatal("the in-flight request was not cancelled on EOF")
	}
}

// TestServeObservesEOFWithARequestQueuedBehindTheInFlightCall is the regression
// for a pump that blocked on the request handoff: the client pipelines a second
// request and then disconnects, so the pump must still reach EOF while the
// first call is running instead of waiting behind it.
func TestServeObservesEOFWithARequestQueuedBehindTheInFlightCall(t *testing.T) {
	inputReader, inputWriter := io.Pipe()
	var output bytes.Buffer
	caller := newBlockingCaller()
	server := NewServer(caller)
	served := make(chan error, 1)
	go func() { served <- server.Serve(context.Background(), inputReader, &output) }()

	writeLine(t, inputWriter, request(1, "tools/call", map[string]any{
		"name": "plugin.acme.tools.echo",
	}))
	awaitSignal(t, caller.started, "the in-flight call never started")

	// The second request is queued behind the still-running call. An
	// unbuffered handoff leaves the pump blocked here, so it never reads the
	// EOF below and Serve waits for the tool call's deadline.
	writeLine(t, inputWriter, request(2, "ping", nil))
	if err := inputWriter.Close(); err != nil {
		t.Fatalf("close stdin: %v", err)
	}
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("Serve returned %v after EOF with a queued request", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return promptly after stdin EOF while a request was queued")
	}
	select {
	case <-caller.canceled:
	default:
		t.Fatal("the in-flight request was not cancelled on EOF")
	}
}

// TestServeCancelsARequestWhoseCancellationSharesItsStdinWrite is the regression
// for the lost-cancellation race: the pump handles notifications ahead of the
// request loop, so a cancellation written together with its request can arrive
// before beginRequest registers it.
func TestServeCancelsARequestWhoseCancellationSharesItsStdinWrite(t *testing.T) {
	inputReader, inputWriter := io.Pipe()
	var output bytes.Buffer
	caller := newBlockingCaller()
	server := NewServer(caller)
	served := make(chan error, 1)
	go func() { served <- server.Serve(context.Background(), inputReader, &output) }()

	const callID = 9
	payload := request(callID, "tools/call", map[string]any{
		"name": "plugin.acme.tools.echo",
	}) + "\n" +
		`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":9}}` + "\n"
	if _, err := io.WriteString(inputWriter, payload); err != nil {
		t.Fatalf("write request and cancellation: %v", err)
	}
	awaitSignal(t, caller.canceled, "the same-write cancellation did not cancel the call")

	if err := inputWriter.Close(); err != nil {
		t.Fatalf("close stdin: %v", err)
	}
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("Serve returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after stdin EOF")
	}

	cancelled := findMessage(decodeOutputLines(t, output.String()), callID)
	errorBody, ok := cancelled["error"].(map[string]any)
	if !ok {
		t.Fatalf("cancelled request response = %v, want a JSON-RPC error", cancelled)
	}
	if errorBody["code"] != float64(codeRequestCancelled) {
		t.Fatalf("cancellation error code = %v, want %d", errorBody["code"], codeRequestCancelled)
	}
}

// TestPendingCancellationAppliesToALateRequest pins the implementation of the
// pending-cancel set deterministically, without depending on the pump racing
// ahead of the request loop: a cancellation recorded before registration must
// cancel the request when beginRequest runs.
func TestPendingCancellationAppliesToALateRequest(t *testing.T) {
	server := NewServer(&fakeCaller{})
	id := json.RawMessage("11")

	// This is what pump.handleNotification does when it reads the
	// cancellation from the same stdin write as the request.
	server.cancelRequest(id)

	requestCtx, request := server.beginRequest(context.Background(), id)
	defer server.endRequest(id, request)
	if !request.cancelled.Load() {
		t.Fatal("a cancellation that arrived before registration was lost")
	}
	select {
	case <-requestCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("the request context was not cancelled")
	}
}

// TestServeCancellationNotificationCancelsOneRequest proves a
// notifications/cancelled message reaches the request while it is running,
// produces the JSON-RPC cancellation error, and leaves the session serving.
func TestServeCancellationNotificationCancelsOneRequest(t *testing.T) {
	inputReader, inputWriter := io.Pipe()
	var output bytes.Buffer
	caller := newBlockingCaller()
	server := NewServer(caller)
	served := make(chan error, 1)
	go func() { served <- server.Serve(context.Background(), inputReader, &output) }()

	const callID = 7
	writeLine(t, inputWriter, request(callID, "tools/call", map[string]any{
		"name": "plugin.acme.tools.echo",
	}))
	awaitSignal(t, caller.started, "the in-flight call never started")

	writeLine(t, inputWriter,
		`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":7}}`)
	awaitSignal(t, caller.canceled, "the cancellation notification did not cancel the call")

	// The session keeps serving after one request is cancelled.
	writeLine(t, inputWriter, request(8, "ping", nil))
	if err := inputWriter.Close(); err != nil {
		t.Fatalf("close stdin: %v", err)
	}
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("Serve returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after stdin EOF")
	}

	messages := decodeOutputLines(t, output.String())
	cancelled := findMessage(messages, callID)
	errorBody, ok := cancelled["error"].(map[string]any)
	if !ok {
		t.Fatalf("cancelled request response = %v, want a JSON-RPC error", cancelled)
	}
	if errorBody["code"] != float64(codeRequestCancelled) {
		t.Fatalf("cancellation error code = %v, want %d", errorBody["code"], codeRequestCancelled)
	}
	if _, present := cancelled["result"]; present {
		t.Fatalf("cancelled request also carried a result: %v", cancelled)
	}

	ping := findMessage(messages, 8)
	if _, ok := ping["result"].(map[string]any); !ok {
		t.Fatalf("ping after the cancellation failed: %v", ping)
	}
}
