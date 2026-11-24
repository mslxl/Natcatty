package vnc

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model/rfb"
)

// waitClosed joins a goroutine-signal channel with a hard deadline, so a leak
// is reported deterministically instead of through a goroutine count.
func waitClosed(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not exit", what)
	}
}

// nextEventKind drains events until one of kind arrives or the stream closes.
// It reports the event and whether the stream was still open.
func nextEventKind(t *testing.T, events <-chan appsession.DisplayEvent, kind appsession.DisplayEventKind) (appsession.DisplayEvent, bool) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case event, ok := <-events:
			if !ok {
				return appsession.DisplayEvent{}, false
			}
			if event.Kind == kind {
				return event, true
			}
		case <-deadline:
			t.Fatalf("no %v event arrived", kind)
		}
	}
}

// drainUntilClosed consumes the rest of the event stream and reports whether it
// closed within the deadline.
func drainUntilClosed(t *testing.T, events <-chan appsession.DisplayEvent) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-events:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("the display event stream did not close")
		}
	}
}

// TestInboundOverflowClosesEverything proves the reader's bounded handoff
// fails closed with the typed error, closes the event stream, closes the
// connection and joins both the reader and writer goroutines.
func TestInboundOverflowClosesEverything(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		if err := serveResizeHandshake(serverConn); err != nil {
			return
		}
		// Consume the initial FramebufferUpdateRequest so the script below
		// starts from a known point.
		if _, err := readClientMessage(serverConn); err != nil {
			return
		}
		// A blocking fence the server never answers keeps the processing loop
		// waiting on the reply write. Flooding then overflows the handoff.
		if err := writeServerFenceRaw(serverConn, rfb.FenceBlockBefore|rfb.FenceRequest, []byte("x")); err != nil {
			return
		}
		for i := 0; i < InboundHandoffCapacity+100; i++ {
			if err := writeServerCutTextRaw(serverConn, "x"); err != nil {
				return
			}
		}
	}()

	client, err := rfb.Connect(clientConn, rfb.ClientConfig{Shared: true})
	if err != nil {
		_ = serverConn.Close()
		_ = clientConn.Close()
		t.Fatalf("Connect: %v", err)
	}
	tr := newTransport(clientConn, client, false)
	t.Cleanup(func() {
		_ = tr.Close()
		_ = clientConn.Close()
		_ = serverConn.Close()
	})

	event, ok := nextEventKind(t, tr.emitter.Events(), appsession.DisplayEventError)
	if !ok {
		t.Fatal("the event stream closed without an overflow error")
	}
	if !errors.Is(event.Err, ErrInboundQueueFull) {
		t.Fatalf("session error = %v, want ErrInboundQueueFull", event.Err)
	}
	drainUntilClosed(t, tr.emitter.Events())
	// The overflow path publishes the error before it calls Close, so join the
	// goroutines before asserting the connection is closed.
	waitClosed(t, tr.readerDone, "reader goroutine")
	waitClosed(t, tr.loopDone, "read loop")
	waitClosed(t, tr.writes.finished, "writer goroutine")
	if !tr.closed.Load() {
		t.Fatal("the transport did not close the connection")
	}

	_ = serverConn.Close()
	waitClosed(t, serverDone, "flood server")
}

// TestProcessingErrorTearsDownTheSession proves a processMessage failure closes
// the connection and joins the reader and writer instead of leaving the reader
// blocked in ReadMessage. The server closes after sending a blocking fence
// without reading its reply, so the reply write fails.
func TestProcessingErrorTearsDownTheSession(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		if err := serveResizeHandshake(serverConn); err != nil {
			return
		}
		if _, err := readClientMessage(serverConn); err != nil {
			return
		}
		if err := writeServerFenceRaw(serverConn, rfb.FenceBlockBefore|rfb.FenceRequest, []byte("x")); err != nil {
			return
		}
		// Close without reading the reply: the client's writer gets a broken
		// pipe, processMessage returns an error, and teardown must join it.
		_ = serverConn.Close()
	}()

	client, err := rfb.Connect(clientConn, rfb.ClientConfig{Shared: true})
	if err != nil {
		_ = serverConn.Close()
		_ = clientConn.Close()
		t.Fatalf("Connect: %v", err)
	}
	tr := newTransport(clientConn, client, false)
	t.Cleanup(func() {
		_ = tr.Close()
		_ = clientConn.Close()
		_ = serverConn.Close()
	})

	event, ok := nextEventKind(t, tr.emitter.Events(), appsession.DisplayEventError)
	if !ok {
		t.Fatal("the event stream closed without an error")
	}
	if event.Err == nil {
		t.Fatal("the processing error was not reported")
	}
	waitClosed(t, tr.readerDone, "reader goroutine")
	waitClosed(t, tr.loopDone, "read loop")
	waitClosed(t, tr.writes.finished, "writer goroutine")
	if !tr.closed.Load() {
		t.Fatal("the transport did not close the connection after a processing error")
	}
	if end := tr.End(); end.Reason != appsession.EndReasonError {
		t.Fatalf("session end = %v, want the error reason", end.Reason)
	}
	drainUntilClosed(t, tr.emitter.Events())
	_ = serverConn.Close()
	waitClosed(t, serverDone, "fence server")
}

func writeServerFenceRaw(w io.Writer, flags uint32, payload []byte) error {
	var header [9]byte
	header[0] = byte(rfb.ServerFenceMessage)
	binary.BigEndian.PutUint32(header[4:8], flags)
	header[8] = byte(len(payload))
	if err := writeAllBytes(w, header[:]); err != nil {
		return err
	}
	return writeAllBytes(w, payload)
}

func writeServerCutTextRaw(w io.Writer, text string) error {
	var header [8]byte
	header[0] = byte(rfb.ServerCutText)
	binary.BigEndian.PutUint32(header[4:8], uint32(len(text)))
	if err := writeAllBytes(w, header[:]); err != nil {
		return err
	}
	return writeAllBytes(w, []byte(text))
}
