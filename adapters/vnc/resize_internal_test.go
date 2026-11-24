package vnc

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"io"
	"net"
	"testing"
	"time"

	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model/rfb"
)

// These tests reach into the adapter's resize timeout so a silent server does
// not make the suite wait the production five seconds per case.

type clientMessage struct {
	kind          byte
	width, height uint16
}

func startResizeTransport(t *testing.T, onResize func(conn net.Conn, width, height uint16) error) *transport {
	t.Helper()
	serverConn, clientConn := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- serveResizeServer(serverConn, onResize) }()
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
	deadline := time.Now().Add(5 * time.Second)
	for !tr.resizeSupport.Load() {
		if time.Now().After(deadline) {
			t.Fatal("the server never announced ExtendedDesktopSize support")
		}
		time.Sleep(time.Millisecond)
	}
	return tr
}

func shortResizeTimeout(t *testing.T) {
	t.Helper()
	previous := desktopResizeTimeout
	desktopResizeTimeout = 150 * time.Millisecond
	t.Cleanup(func() { desktopResizeTimeout = previous })
}

// TestReasonZeroResizeDoesNotSatisfyAPendingRequest proves a server-side
// (reason 0) layout change of the same dimensions is not mistaken for the
// reply to the client's outstanding request.
func TestReasonZeroResizeDoesNotSatisfyAPendingRequest(t *testing.T) {
	shortResizeTimeout(t)
	tr := startResizeTransport(t, func(conn net.Conn, width, height uint16) error {
		return writeExtendedRectangle(conn, 0, 0, width, height, []rfb.Screen{{
			Width: width, Height: height,
		}})
	})
	if err := tr.SetScreenLayout([]appsession.DisplayScreen{{Rect: image.Rect(0, 0, 8, 6)}}); !errors.Is(err, appsession.ErrDesktopResizeTimeout) {
		t.Fatalf("SetScreenLayout = %v, want ErrDesktopResizeTimeout", err)
	}
}

// TestStaleSuccessDoesNotSatisfyAPendingRequest proves a reason-1 success for
// a different size (a late reply to an earlier request) is discarded, so the
// current request still waits for its own reply and times out.
func TestStaleSuccessDoesNotSatisfyAPendingRequest(t *testing.T) {
	shortResizeTimeout(t)
	tr := startResizeTransport(t, func(conn net.Conn, width, height uint16) error {
		return writeExtendedRectangle(conn, 1, 0, 4, 3, []rfb.Screen{{
			Width: 4, Height: 3,
		}})
	})
	if err := tr.SetScreenLayout([]appsession.DisplayScreen{{Rect: image.Rect(0, 0, 8, 6)}}); !errors.Is(err, appsession.ErrDesktopResizeTimeout) {
		t.Fatalf("SetScreenLayout = %v, want ErrDesktopResizeTimeout", err)
	}
}

// TestMatchingSuccessCompletesTheRequest proves the dimension match does not
// reject the real reply.
func TestMatchingSuccessCompletesTheRequest(t *testing.T) {
	shortResizeTimeout(t)
	tr := startResizeTransport(t, func(conn net.Conn, width, height uint16) error {
		return writeExtendedRectangle(conn, 1, 0, width, height, []rfb.Screen{{
			Width: width, Height: height,
		}})
	})
	if err := tr.SetScreenLayout([]appsession.DisplayScreen{{Rect: image.Rect(0, 0, 8, 6)}}); err != nil {
		t.Fatalf("SetScreenLayout = %v, want nil", err)
	}
}

// TestTransportWriterOverflowEndsTheSession proves the transport wires the
// bounded writer's overflow to a typed session error rather than growing the
// queue or deadlocking on a stuck send.
func TestTransportWriterOverflowEndsTheSession(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	go func() {
		if serveResizeHandshake(serverConn) != nil {
			return
		}
		if _, err := readClientMessage(serverConn); err != nil {
			return
		}
		// Stop reading so the first queued send blocks the writer.
		_, _ = io.Copy(io.Discard, serverConn)
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

	block := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(block)
		}
	}()
	if !tr.writes.submitAsync(func() error {
		<-block
		return nil
	}) {
		t.Fatal("the writer rejected its first task")
	}
	for i := 0; i < WriteQueueCapacity+10; i++ {
		if !tr.writes.submitAsync(func() error { return nil }) {
			break
		}
	}

	deadline := time.After(5 * time.Second)
	for {
		select {
		case event, ok := <-tr.emitter.Events():
			if !ok {
				t.Fatal("the event stream closed without an overflow error")
			}
			if event.Kind != appsession.DisplayEventError {
				continue
			}
			if !errors.Is(event.Err, ErrWriteQueueFull) {
				t.Fatalf("session error = %v, want ErrWriteQueueFull", event.Err)
			}
			close(block)
			released = true
			select {
			case <-tr.writes.finished:
			case <-time.After(5 * time.Second):
				t.Fatal("the writer goroutine did not exit")
			}
			return
		case <-deadline:
			t.Fatal("no overflow error was published")
		}
	}
}

// serveResizeServer completes the handshake, announces ExtendedDesktopSize on
// the first update request, and answers the first SetDesktopSize through
// onResize. It keeps reading so an asynchronous client request never blocks.
func serveResizeServer(conn net.Conn, onResize func(conn net.Conn, width, height uint16) error) error {
	if err := serveResizeHandshake(conn); err != nil {
		return err
	}
	announced, answered := false, false
	for {
		message, err := readClientMessage(conn)
		if err != nil {
			return err
		}
		switch message.kind {
		case 3: // FramebufferUpdateRequest
			if !announced {
				announced = true
				if err := writeExtendedRectangle(conn, 0, 0, 4, 4, []rfb.Screen{{
					Width: 4, Height: 4,
				}}); err != nil {
					return err
				}
			}
		case 251: // SetDesktopSize
			if !answered {
				answered = true
				if err := onResize(conn, message.width, message.height); err != nil {
					return err
				}
			}
		}
	}
}

func serveResizeHandshake(conn net.Conn) error {
	if err := writeAllBytes(conn, []byte("RFB 003.008\n")); err != nil {
		return err
	}
	var version [12]byte
	if _, err := io.ReadFull(conn, version[:]); err != nil {
		return err
	}
	if err := writeAllBytes(conn, []byte{1, byte(rfb.SecurityNone)}); err != nil {
		return err
	}
	var selection [1]byte
	if _, err := io.ReadFull(conn, selection[:]); err != nil {
		return err
	}
	if err := writeAllBytes(conn, []byte{0, 0, 0, 0}); err != nil {
		return err
	}
	var shared [1]byte
	if _, err := io.ReadFull(conn, shared[:]); err != nil {
		return err
	}
	var init [24]byte
	binary.BigEndian.PutUint16(init[0:2], 4)
	binary.BigEndian.PutUint16(init[2:4], 4)
	var format bytes.Buffer
	if _, err := rfb.DefaultPixelFormat().WriteTo(&format); err != nil {
		return err
	}
	copy(init[4:20], format.Bytes())
	if err := writeAllBytes(conn, init[:]); err != nil {
		return err
	}
	for {
		message, err := readClientMessage(conn)
		if err != nil {
			return err
		}
		if message.kind == 2 {
			return nil
		}
	}
}

func readClientMessage(conn io.Reader) (clientMessage, error) {
	var wire [1]byte
	if _, err := io.ReadFull(conn, wire[:]); err != nil {
		return clientMessage{}, err
	}
	message := clientMessage{kind: wire[0]}
	switch wire[0] {
	case 0:
		var payload [19]byte
		_, err := io.ReadFull(conn, payload[:])
		return message, err
	case 2:
		var header [3]byte
		if _, err := io.ReadFull(conn, header[:]); err != nil {
			return message, err
		}
		count := int(binary.BigEndian.Uint16(header[1:3]))
		payload := make([]byte, 4*count)
		_, err := io.ReadFull(conn, payload)
		return message, err
	case 3:
		var payload [9]byte
		_, err := io.ReadFull(conn, payload[:])
		return message, err
	case 4:
		var payload [7]byte
		_, err := io.ReadFull(conn, payload[:])
		return message, err
	case 5:
		var payload [5]byte
		_, err := io.ReadFull(conn, payload[:])
		return message, err
	case 6:
		var header [7]byte
		if _, err := io.ReadFull(conn, header[:]); err != nil {
			return message, err
		}
		payload := make([]byte, binary.BigEndian.Uint32(header[3:7]))
		_, err := io.ReadFull(conn, payload)
		return message, err
	case 150:
		var payload [9]byte
		_, err := io.ReadFull(conn, payload[:])
		return message, err
	case 248:
		var header [8]byte
		if _, err := io.ReadFull(conn, header[:]); err != nil {
			return message, err
		}
		payload := make([]byte, int(header[7]))
		_, err := io.ReadFull(conn, payload)
		return message, err
	case 251:
		// type (consumed) + pad + width:2 + height:2 + screen-count:1 +
		// pad. Reading all seven bytes keeps the following screen payload
		// aligned; consuming only six leaves the trailing pad byte to shift
		// every screen.
		var header [7]byte
		if _, err := io.ReadFull(conn, header[:]); err != nil {
			return message, err
		}
		message.width = binary.BigEndian.Uint16(header[1:3])
		message.height = binary.BigEndian.Uint16(header[3:5])
		payload := make([]byte, 16*int(header[5]))
		_, err := io.ReadFull(conn, payload)
		return message, err
	default:
		return message, fmt.Errorf("unexpected client message %d", wire[0])
	}
}

func writeExtendedRectangle(w io.Writer, reason, status uint8, width, height uint16, screens []rfb.Screen) error {
	var update bytes.Buffer
	var updateHeader [4]byte
	binary.BigEndian.PutUint16(updateHeader[2:4], 1)
	update.Write(updateHeader[:])
	var rect [12]byte
	binary.BigEndian.PutUint16(rect[0:2], uint16(reason))
	binary.BigEndian.PutUint16(rect[2:4], uint16(status))
	binary.BigEndian.PutUint16(rect[4:6], width)
	binary.BigEndian.PutUint16(rect[6:8], height)
	extendedEncoding := int32(rfb.EncodingExtendedDesktopSize)
	binary.BigEndian.PutUint32(rect[8:12], uint32(extendedEncoding))
	update.Write(rect[:])
	payload := make([]byte, 4+16*len(screens))
	payload[0] = byte(len(screens))
	for i, screen := range screens {
		offset := 4 + i*16
		binary.BigEndian.PutUint32(payload[offset:offset+4], screen.ID)
		binary.BigEndian.PutUint16(payload[offset+4:offset+6], screen.X)
		binary.BigEndian.PutUint16(payload[offset+6:offset+8], screen.Y)
		binary.BigEndian.PutUint16(payload[offset+8:offset+10], screen.Width)
		binary.BigEndian.PutUint16(payload[offset+10:offset+12], screen.Height)
		binary.BigEndian.PutUint32(payload[offset+12:offset+16], screen.Flags)
	}
	update.Write(payload)
	return writeAllBytes(w, update.Bytes())
}

func writeAllBytes(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

// scriptedResizeReply is one ExtendedDesktopSize update the test server writes
// on demand.
type scriptedResizeReply struct {
	reason, status uint8
	width, height  uint16
	screens        []rfb.Screen
}

// startScriptedResizeTransport performs the handshake, announces
// ExtendedDesktopSize on the first update request, records every
// SetDesktopSize request, and writes replies supplied by the test. It never
// answers a resize on its own, so a test controls the reply order precisely.
func startScriptedResizeTransport(t *testing.T) (*transport, <-chan clientMessage, chan<- scriptedResizeReply) {
	t.Helper()
	serverConn, clientConn := net.Pipe()
	requests := make(chan clientMessage, 16)
	replies := make(chan scriptedResizeReply, 16)
	announce := make(chan struct{}, 1)
	writerStop := make(chan struct{})
	serverDone := make(chan struct{})

	go func() {
		defer close(serverDone)
		if err := serveResizeHandshake(serverConn); err != nil {
			return
		}
		// One writer serializes the capability announcement and every
		// test-supplied reply.
		writerDone := make(chan struct{})
		go func() {
			defer close(writerDone)
			for {
				select {
				case <-writerStop:
					return
				case <-announce:
					_ = writeExtendedRectangle(serverConn, 0, 0, 4, 4, []rfb.Screen{{
						Width: 4, Height: 4,
					}})
				case reply := <-replies:
					if err := writeExtendedRectangle(serverConn, reply.reason, reply.status, reply.width, reply.height, reply.screens); err != nil {
						return
					}
				}
			}
		}()
		announced := false
		for {
			message, err := readClientMessage(serverConn)
			if err != nil {
				break
			}
			switch message.kind {
			case 3: // FramebufferUpdateRequest
				if !announced {
					announced = true
					select {
					case announce <- struct{}{}:
					default:
					}
				}
			case 251: // SetDesktopSize
				select {
				case requests <- message:
				default:
				}
			}
		}
		close(writerStop)
		<-writerDone
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
		select {
		case <-serverDone:
		case <-time.After(5 * time.Second):
			t.Error("the scripted resize server did not exit")
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	for !tr.resizeSupport.Load() {
		if time.Now().After(deadline) {
			t.Fatal("the server never announced ExtendedDesktopSize support")
		}
		time.Sleep(time.Millisecond)
	}
	return tr, requests, replies
}

// awaitResizeRequest waits for the SetDesktopSize message with these dimensions
// to reach the server.
func awaitResizeRequest(t *testing.T, requests <-chan clientMessage, width, height uint16) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case message := <-requests:
			if message.kind == 251 && message.width == width && message.height == height {
				return
			}
		case <-deadline:
			t.Fatalf("no SetDesktopSize %dx%d reached the server", width, height)
		}
	}
}

// waitForFrameSize polls the decoder's framebuffer, which is authoritative.
func waitForFrameSize(t *testing.T, tr *transport, width, height int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		bounds := tr.client.Frame().Bounds()
		if bounds.Dx() == width && bounds.Dy() == height {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("framebuffer = %v, want %dx%d", bounds, width, height)
		}
		time.Sleep(time.Millisecond)
	}
}

// resizeTimeouts returns every DisplayEventResize size on the event stream.
// The caller must have started it before the server sends the event.
func resizeTimeouts(tr *transport) <-chan image.Point {
	sizes := make(chan image.Point, 16)
	go func() {
		for event := range tr.emitter.Events() {
			if event.Kind == appsession.DisplayEventResize {
				sizes <- image.Pt(event.Width, event.Height)
			}
		}
		close(sizes)
	}()
	return sizes
}

// TestStaleRefusalDoesNotRefuseANewerRequest reproduces the verifier's scenario:
// request A (40x30) times out, request B (64x48) is sent, and a late refusal for
// A arrives. B must not be resolved as refused (which would latch local
// scaling); the framebuffer's actual size is authoritative.
func TestStaleRefusalDoesNotRefuseANewerRequest(t *testing.T) {
	shortResizeTimeout(t)
	tr, requests, replies := startScriptedResizeTransport(t)

	first := make(chan error, 1)
	go func() { first <- tr.SetScreenLayout([]appsession.DisplayScreen{{Rect: image.Rect(0, 0, 40, 30)}}) }()
	awaitResizeRequest(t, requests, 40, 30)
	if err := <-first; !errors.Is(err, appsession.ErrDesktopResizeTimeout) {
		t.Fatalf("first SetScreenLayout = %v, want ErrDesktopResizeTimeout", err)
	}

	second := make(chan error, 1)
	go func() { second <- tr.SetScreenLayout([]appsession.DisplayScreen{{Rect: image.Rect(0, 0, 64, 48)}}) }()
	awaitResizeRequest(t, requests, 64, 48)

	replies <- scriptedResizeReply{
		reason: 1, status: 1, width: 40, height: 30,
		screens: []rfb.Screen{{Width: 40, Height: 30}},
	}

	err := <-second
	if errors.Is(err, appsession.ErrDesktopResizeRefused) {
		t.Fatal("a late refusal for an older request refused the newer one")
	}
	if !errors.Is(err, appsession.ErrDesktopResizeTimeout) {
		t.Fatalf("second SetScreenLayout = %v, want ErrDesktopResizeTimeout", err)
	}

	// The server actually reached the newer size; a server-initiated success is
	// authoritative and the framebuffer follows it.
	replies <- scriptedResizeReply{
		reason: 0, status: 0, width: 64, height: 48,
		screens: []rfb.Screen{{Width: 64, Height: 48}},
	}
	waitForFrameSize(t, tr, 64, 48)
}

// TestStaleSuccessDoesNotSatisfyANewerRequest proves a late status-0 reply for
// an earlier, timed-out size does not resolve the newer request, even when the
// dimensions are checked.
func TestStaleSuccessDoesNotSatisfyANewerRequest(t *testing.T) {
	shortResizeTimeout(t)
	tr, requests, replies := startScriptedResizeTransport(t)

	first := make(chan error, 1)
	go func() { first <- tr.SetScreenLayout([]appsession.DisplayScreen{{Rect: image.Rect(0, 0, 40, 30)}}) }()
	awaitResizeRequest(t, requests, 40, 30)
	if err := <-first; !errors.Is(err, appsession.ErrDesktopResizeTimeout) {
		t.Fatalf("first SetScreenLayout = %v, want ErrDesktopResizeTimeout", err)
	}

	second := make(chan error, 1)
	go func() { second <- tr.SetScreenLayout([]appsession.DisplayScreen{{Rect: image.Rect(0, 0, 64, 48)}}) }()
	awaitResizeRequest(t, requests, 64, 48)

	// A success for the timed-out A arrives while B is pending. It resizes the
	// framebuffer but must not be mistaken for B's answer.
	replies <- scriptedResizeReply{
		reason: 1, status: 0, width: 40, height: 30,
		screens: []rfb.Screen{{Width: 40, Height: 30}},
	}
	waitForFrameSize(t, tr, 40, 30)
	if err := <-second; !errors.Is(err, appsession.ErrDesktopResizeTimeout) {
		t.Fatalf("second SetScreenLayout = %v, want ErrDesktopResizeTimeout", err)
	}
}

// TestServerInitiatedResizeWhilePendingPublishesAndKeepsTheRequest proves a
// reason-0 ExtendedDesktopSize for a different size neither clears nor answers
// the pending request, but still publishes the resize event.
func TestServerInitiatedResizeWhilePendingPublishesAndKeepsTheRequest(t *testing.T) {
	shortResizeTimeout(t)
	tr, requests, replies := startScriptedResizeTransport(t)
	resizes := resizeTimeouts(tr)

	second := make(chan error, 1)
	go func() { second <- tr.SetScreenLayout([]appsession.DisplayScreen{{Rect: image.Rect(0, 0, 64, 48)}}) }()
	awaitResizeRequest(t, requests, 64, 48)

	replies <- scriptedResizeReply{
		reason: 0, status: 0, width: 80, height: 60,
		screens: []rfb.Screen{{Width: 80, Height: 60}},
	}

	deadline := time.After(5 * time.Second)
	for {
		select {
		case size := <-resizes:
			if size == image.Pt(80, 60) {
				goto published
			}
		case <-deadline:
			t.Fatal("the server-initiated resize was not published")
		}
	}
published:
	waitForFrameSize(t, tr, 80, 60)
	if err := <-second; !errors.Is(err, appsession.ErrDesktopResizeTimeout) {
		t.Fatalf("SetScreenLayout = %v, want ErrDesktopResizeTimeout", err)
	}
}

// TestCloseUnblocksAnOutstandingResize proves Close ends the session while
// SetScreenLayout is waiting and joins the reader and writer goroutines. The
// full production timeout is used, so only Close can unblock the waiter.
func TestCloseUnblocksAnOutstandingResize(t *testing.T) {
	tr, requests, _ := startScriptedResizeTransport(t)

	result := make(chan error, 1)
	go func() { result <- tr.SetScreenLayout([]appsession.DisplayScreen{{Rect: image.Rect(0, 0, 64, 48)}}) }()
	awaitResizeRequest(t, requests, 64, 48)

	if err := tr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, appsession.ErrNoDisplay) {
			t.Fatalf("SetScreenLayout = %v, want ErrNoDisplay", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SetScreenLayout did not unblock on Close")
	}
	waitClosed(t, tr.readerDone, "reader goroutine")
	waitClosed(t, tr.loopDone, "read loop")
	waitClosed(t, tr.writes.finished, "writer goroutine")
}

// TestStaleResizeSetIsBoundedAndExpires proves the timed-out request set cannot
// grow without bound and drops entries past their window.
func TestStaleResizeSetIsBoundedAndExpires(t *testing.T) {
	tr := &transport{}
	for i := 0; i < maxStaleResizes+10; i++ {
		tr.addStaleLocked(staleResize{
			epoch:   uint64(i),
			width:   uint16(i),
			height:  1,
			expires: time.Now().Add(time.Minute),
		})
	}
	if len(tr.staleResizes) != maxStaleResizes {
		t.Fatalf("stale set holds %d entries, want %d", len(tr.staleResizes), maxStaleResizes)
	}
	tr.staleResizes = append(tr.staleResizes, staleResize{expires: time.Now().Add(-time.Second)})
	tr.expireStaleLocked(time.Now())
	for _, entry := range tr.staleResizes {
		if !entry.expires.After(time.Now()) {
			t.Fatalf("an expired stale entry survived: %+v", entry)
		}
	}
}
