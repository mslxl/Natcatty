package vnc_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/mslxl/natcatty/adapters/vnc"
	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model/rfb"
	"github.com/mslxl/natcatty/domain/model/rfb/rfbtest"
)

// TestDesktopResizeRefusalIsTyped proves a nonzero ExtendedDesktopSize status
// is surfaced as a distinct refusal instead of masquerading as success.
func TestDesktopResizeRefusalIsTyped(t *testing.T) {
	h := start(t, rfbtest.Config{
		Frame:               testFrame(),
		Encoding:            rfb.EncodingRaw,
		ExtendedDesktopSize: true,
		DesktopSizeStatus:   1,
	})
	transport, err := h.factory.Open(context.Background(), openSpec("desk.test", 5900))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	display := transport.(appsession.DisplayProvider).Display()
	// The first update carries the ExtendedDesktopSize capability.
	nextEvent(t, display.Events(), appsession.DisplayEventFrame)

	if err := display.SetDesktopSize(8, 6); !errors.Is(err, appsession.ErrDesktopResizeRefused) {
		t.Fatalf("SetDesktopSize = %v, want ErrDesktopResizeRefused", err)
	}
}

// TestDesktopResizeStatusesAreClassified proves status 4 ("request forwarded")
// is pending, while statuses 1-3 and an unknown nonzero status are refusals.
func TestDesktopResizeStatusesAreClassified(t *testing.T) {
	cases := []struct {
		status uint8
		want   error
	}{
		{4, appsession.ErrDesktopResizePending},
		{1, appsession.ErrDesktopResizeRefused},
		{2, appsession.ErrDesktopResizeRefused},
		{3, appsession.ErrDesktopResizeRefused},
		{9, appsession.ErrDesktopResizeRefused},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("status-%d", tc.status), func(t *testing.T) {
			h := start(t, rfbtest.Config{
				Frame:               testFrame(),
				Encoding:            rfb.EncodingRaw,
				ExtendedDesktopSize: true,
				DesktopSizeStatus:   tc.status,
			})
			transport, err := h.factory.Open(context.Background(), openSpec("desk.test", 5900))
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			t.Cleanup(func() { _ = transport.Close() })
			display := transport.(appsession.DisplayProvider).Display()
			nextEvent(t, display.Events(), appsession.DisplayEventFrame)

			if err := display.SetDesktopSize(8, 6); !errors.Is(err, tc.want) {
				t.Fatalf("SetDesktopSize = %v, want %v", err, tc.want)
			}
			// Even a refusal or an unknown status must not kill the session.
			if err := display.SendKey(0x62, true); err != nil {
				t.Fatalf("SendKey after the resize answer: %v", err)
			}
			waitFor(t, "the session to stay alive", func() bool {
				for _, key := range h.server.Received().Keys {
					if key.Keysym == 0x62 {
						return true
					}
				}
				return false
			})
		})
	}
}

// TestFenceReplyClearsUnknownFlagBits proves the client echoes only the flags
// it understands, dropping unknown high bits and the Request bit.
func TestFenceReplyClearsUnknownFlagBits(t *testing.T) {
	const unknown = uint32(1 << 20)
	h := start(t, rfbtest.Config{
		Frame:      testFrame(),
		Encoding:   rfb.EncodingRaw,
		Fence:      true,
		FenceFlags: rfb.FenceBlockBefore | unknown,
	})
	transport, err := h.factory.Open(context.Background(), openSpec("desk.test", 5900))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = transport.Close() })

	var response *rfb.Fence
	waitFor(t, "the fence reply", func() bool {
		for _, fence := range h.server.Received().Fences {
			if !fence.Request() {
				reply := fence
				response = &reply
				return true
			}
		}
		return false
	})
	if response.Flags != rfb.FenceBlockBefore {
		t.Fatalf("fence reply flags = %#x, want only BlockBefore", response.Flags)
	}
}

// TestPostResizeRequestsTheNewExtent proves a server without ContinuousUpdates
// receives a non-incremental request that covers the new desktop, so the added
// area is repainted.
func TestPostResizeRequestsTheNewExtent(t *testing.T) {
	h := start(t, rfbtest.Config{
		Frame:               testFrame(),
		Encoding:            rfb.EncodingRaw,
		ExtendedDesktopSize: true,
	})
	transport, err := h.factory.Open(context.Background(), openSpec("desk.test", 5900))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	display := transport.(appsession.DisplayProvider).Display()
	nextEvent(t, display.Events(), appsession.DisplayEventFrame)

	if err := display.SetDesktopSize(8, 6); err != nil {
		t.Fatalf("SetDesktopSize: %v", err)
	}
	waitFor(t, "a full update request for the new size", func() bool {
		for _, request := range h.server.Received().Requests {
			if !request.Incremental && request.Width == 8 && request.Height == 6 {
				return true
			}
		}
		return false
	})
}

// TestUnknownEncodingAfterValidRectanglesDiscardsSession proves that a valid
// rectangle followed by an undecodable one ends the session with a typed error.
// The framebuffer may hold the first rectangle's pixels, but no frame event is
// published, and the connection is discarded, so those pixels are never shown.
func TestUnknownEncodingAfterValidRectanglesDiscardsSession(t *testing.T) {
	h := start(t, rfbtest.Config{
		Frame:    testFrame(),
		Encoding: rfb.EncodingRaw,
		Rectangles: []rfbtest.Rectangle{
			{X: 0, Y: 0, Width: 4, Height: 4, Encoding: rfb.EncodingRaw},
		},
		ExtraEncoding: rfb.Encoding(9999),
	})
	transport, err := h.factory.Open(context.Background(), openSpec("desk.test", 5900))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	events := transport.(appsession.DisplayProvider).Display().Events()

	sawFrame := false
	deadline := time.After(5 * time.Second)
	for {
		select {
		case event, ok := <-events:
			if !ok {
				t.Fatal("event stream closed without an error")
			}
			switch event.Kind {
			case appsession.DisplayEventFrame:
				sawFrame = true
			case appsession.DisplayEventError:
				if !errors.Is(event.Err, rfb.ErrUnsupportedEncoding) {
					t.Fatalf("error = %v, want ErrUnsupportedEncoding", event.Err)
				}
				if sawFrame {
					t.Fatal("a partially applied frame was published before the decoder failed")
				}
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for the decoder error")
		}
	}
}

// TestFenceBurstDoesNotStallTheWriter proves a fence burst is answered through
// the bounded, shared writer rather than one goroutine per request: every reply
// arrives, and the writer still accepts and sends a later client message. This
// assertion is deterministic, unlike a goroutine-count delta.
func TestFenceBurstDoesNotStallTheWriter(t *testing.T) {
	const burst = 200
	h := start(t, rfbtest.Config{
		Frame:      testFrame(),
		Encoding:   rfb.EncodingRaw,
		Fence:      true,
		FenceBurst: burst,
	})
	transport, err := h.factory.Open(context.Background(), openSpec("desk.test", 5900))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	display := transport.(appsession.DisplayProvider).Display()

	waitFor(t, "the fence burst to be answered", func() bool {
		return len(h.server.Received().Fences) >= burst
	})
	if err := display.SendKey(0x62, true); err != nil {
		t.Fatalf("SendKey after the burst: %v", err)
	}
	waitFor(t, "the post-burst key on the wire", func() bool {
		for _, key := range h.server.Received().Keys {
			if key.Keysym == 0x62 {
				return true
			}
		}
		return false
	})
}

// openRawTransport opens a VNC transport over a client conn served by script.
func openRawTransport(t *testing.T, script func(serverConn net.Conn) error) (*harness, appsession.DisplaySession, net.Conn, net.Conn, chan error) {
	t.Helper()
	serverConn, clientConn := net.Pipe()
	h := &harness{closed: make(chan struct{})}
	done := make(chan error, 1)
	go func() { done <- script(serverConn) }()
	h.factory = &vnc.Factory{Dial: func(context.Context, string, string) (net.Conn, error) {
		return clientConn, nil
	}}
	t.Cleanup(func() {
		_ = clientConn.Close()
		_ = serverConn.Close()
	})
	transport, err := h.factory.Open(context.Background(), openSpec("desk.test", 5900))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	return h, transport.(appsession.DisplayProvider).Display(), clientConn, serverConn, done
}

// TestInboundHandoffOverflowFailsClosed proves the reader's bounded handoff
// fails the session with a typed error instead of blocking forever when a
// server stops reading a blocking fence reply and then floods messages.
func TestInboundHandoffOverflowFailsClosed(t *testing.T) {
	_, display, clientConn, serverConn, done := openRawTransport(t, func(serverConn net.Conn) error {
		if err := serveNoneHandshake(serverConn); err != nil {
			return err
		}
		if err := waitForUpdateRequest(serverConn); err != nil {
			return err
		}
		if err := writeServerFence(serverConn, rfb.FenceBlockBefore|rfb.FenceRequest, []byte("x")); err != nil {
			return err
		}
		for i := 0; i < vnc.InboundHandoffCapacity+100; i++ {
			if err := writeServerCutText(serverConn, "x"); err != nil {
				return err
			}
		}
		return nil
	})

	event := nextEvent(t, display.Events(), appsession.DisplayEventError)
	if !errors.Is(event.Err, vnc.ErrInboundQueueFull) {
		t.Fatalf("session error = %v, want ErrInboundQueueFull", event.Err)
	}
	_ = clientConn.Close()
	_ = serverConn.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the flood server did not exit after the connection closed")
	}
}

// fenceOrderingConn records the first byte of every completed client write.
type fenceOrderingConn struct {
	net.Conn
	mu     sync.Mutex
	writes []byte
}

func (c *fenceOrderingConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if n > 0 {
		c.mu.Lock()
		c.writes = append(c.writes, p[0])
		c.mu.Unlock()
	}
	return n, err
}

func (c *fenceOrderingConn) wroteFence() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, kind := range c.writes {
		if kind == byte(rfb.ServerFenceMessage) {
			return true
		}
	}
	return false
}

// TestBlockingFenceIsAnsweredBeforeTheNextMessage proves a ServerFence with
// BlockBefore is written before the processing loop consumes the message the
// server pipelines after it.
func TestBlockingFenceIsAnsweredBeforeTheNextMessage(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	ordering := &fenceOrderingConn{Conn: clientConn}
	done := make(chan error, 1)
	go func() { done <- serveBlockingFence(serverConn) }()
	t.Cleanup(func() {
		_ = clientConn.Close()
		_ = serverConn.Close()
	})

	factory := vnc.Factory{Dial: func(context.Context, string, string) (net.Conn, error) {
		return ordering, nil
	}}
	transport, err := factory.Open(context.Background(), openSpec("desk.test", 5900))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	display := transport.(appsession.DisplayProvider).Display()

	cut := nextEvent(t, display.Events(), appsession.DisplayEventCutText)
	if cut.Text != "after-fence" {
		t.Fatalf("cut text = %q, want after-fence", cut.Text)
	}
	if !ordering.wroteFence() {
		t.Fatal("the cut text was processed before the fence reply was written")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("server: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the server never read the fence reply")
	}
}

// serveNoneHandshake performs the RFB 3.8 None handshake and consumes client
// setup through SetEncodings. It leaves the connection ready for a script.
func serveNoneHandshake(conn net.Conn) error {
	if err := writeAll(conn, []byte("RFB 003.008\n")); err != nil {
		return err
	}
	var version [12]byte
	if _, err := io.ReadFull(conn, version[:]); err != nil {
		return err
	}
	if err := writeAll(conn, []byte{1, byte(rfb.SecurityNone)}); err != nil {
		return err
	}
	var selection [1]byte
	if _, err := io.ReadFull(conn, selection[:]); err != nil {
		return err
	}
	if err := writeAll(conn, []byte{0, 0, 0, 0}); err != nil {
		return err
	}
	var shared [1]byte
	if _, err := io.ReadFull(conn, shared[:]); err != nil {
		return err
	}
	var init [24]byte
	binary.BigEndian.PutUint16(init[0:2], 2)
	binary.BigEndian.PutUint16(init[2:4], 2)
	var format bytes.Buffer
	if _, err := rfb.DefaultPixelFormat().WriteTo(&format); err != nil {
		return err
	}
	copy(init[4:20], format.Bytes())
	if err := writeAll(conn, init[:]); err != nil {
		return err
	}

	for {
		var messageType [1]byte
		if _, err := io.ReadFull(conn, messageType[:]); err != nil {
			return err
		}
		switch messageType[0] {
		case 0: // SetPixelFormat
			var payload [19]byte
			if _, err := io.ReadFull(conn, payload[:]); err != nil {
				return err
			}
		case 2: // SetEncodings
			var header [3]byte
			if _, err := io.ReadFull(conn, header[:]); err != nil {
				return err
			}
			count := int(binary.BigEndian.Uint16(header[1:3]))
			payload := make([]byte, 4*count)
			if _, err := io.ReadFull(conn, payload); err != nil {
				return err
			}
			return nil
		default:
			return errors.New("unexpected client message during setup")
		}
	}
}

// waitForUpdateRequest consumes the next client message, which must be the
// initial FramebufferUpdateRequest, so Open's synchronous first request can
// complete before a script stops reading client messages.
func waitForUpdateRequest(conn net.Conn) error {
	var messageType [1]byte
	if _, err := io.ReadFull(conn, messageType[:]); err != nil {
		return err
	}
	if messageType[0] != 3 {
		return errors.New("expected a FramebufferUpdateRequest")
	}
	var payload [9]byte
	_, err := io.ReadFull(conn, payload[:])
	return err
}

// serveBlockingFence performs the RFB 3.8 None handshake, then sends a
// BlockBefore ServerFence followed immediately by a CutText. The client must
// write its fence reply before processing the cut text. The server keeps
// reading after the reply: the client also sends one client-initiated fence
// request, and returning before consuming it would leave that write blocked on
// the unbuffered pipe and stall every later client message.
func serveBlockingFence(conn net.Conn) error {
	if err := serveNoneHandshake(conn); err != nil {
		return err
	}
	if err := writeServerFence(conn, rfb.FenceBlockBefore|rfb.FenceRequest, []byte("x")); err != nil {
		return err
	}
	if err := writeServerCutText(conn, "after-fence"); err != nil {
		return err
	}
	for {
		var messageType [1]byte
		if _, err := io.ReadFull(conn, messageType[:]); err != nil {
			return err
		}
		switch messageType[0] {
		case 3: // FramebufferUpdateRequest
			var payload [9]byte
			if _, err := io.ReadFull(conn, payload[:]); err != nil {
				return err
			}
		case byte(rfb.ServerFenceMessage):
			var header [8]byte
			if _, err := io.ReadFull(conn, header[:]); err != nil {
				return err
			}
			flags := binary.BigEndian.Uint32(header[3:7])
			payload := make([]byte, int(header[7]))
			if _, err := io.ReadFull(conn, payload); err != nil {
				return err
			}
			// A fence without Request is the reply to our BlockBefore fence;
			// keep reading until the client's own one-shot request arrives.
			if flags&rfb.FenceRequest != 0 {
				return nil
			}
		default:
			return errors.New("unexpected client message after the fence")
		}
	}
}

func writeServerFence(w io.Writer, flags uint32, payload []byte) error {
	var header [9]byte
	header[0] = byte(rfb.ServerFenceMessage)
	binary.BigEndian.PutUint32(header[4:8], flags)
	header[8] = byte(len(payload))
	if err := writeAll(w, header[:]); err != nil {
		return err
	}
	return writeAll(w, payload)
}

func writeServerCutText(w io.Writer, text string) error {
	var header [8]byte
	header[0] = byte(rfb.ServerCutText)
	binary.BigEndian.PutUint32(header[4:8], uint32(len(text)))
	if err := writeAll(w, header[:]); err != nil {
		return err
	}
	return writeAll(w, []byte(text))
}

func writeAll(w io.Writer, data []byte) error {
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
