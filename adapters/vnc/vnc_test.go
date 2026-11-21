package vnc_test

import (
	"context"
	"errors"
	"image"
	"image/color"
	"io"
	"net"
	"testing"
	"time"

	"github.com/mslxl/natcatty/adapters/vnc"
	appconnection "github.com/mslxl/natcatty/application/connection"
	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model/rfb"
	"github.com/mslxl/natcatty/domain/model/rfb/rfbtest"
	modelvnc "github.com/mslxl/natcatty/domain/model/vnc"
)

func testFrame() *image.RGBA {
	frame := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			pixel := color.RGBA{A: 255}
			switch {
			case x < 2 && y < 2:
				pixel = color.RGBA{R: 200, G: 30, B: 10, A: 255}
			case x >= 2 && y >= 2:
				pixel = color.RGBA{R: 10, G: 30, B: 200, A: 255}
			}
			frame.SetRGBA(x, y, pixel)
		}
	}
	return frame
}

type harness struct {
	factory *vnc.Factory
	server  *rfbtest.Server
	address string
	serve   chan error
	closed  chan struct{}
}

func start(t *testing.T, cfg rfbtest.Config) *harness {
	t.Helper()
	serverConn, clientConn := net.Pipe()
	h := &harness{server: rfbtest.New(cfg), closed: make(chan struct{})}
	h.serve = make(chan error, 1)
	go func() { h.serve <- h.server.Serve(serverConn) }()
	h.factory = &vnc.Factory{Dial: func(_ context.Context, network, address string) (net.Conn, error) {
		h.address = address
		return clientConn, nil
	}}
	t.Cleanup(func() {
		_ = clientConn.Close()
		_ = serverConn.Close()
	})
	return h
}

func openSpec(hostname string, port int) appsession.StartSpec {
	cfg := modelvnc.SessionConfig{
		Hostname:      hostname,
		Port:          port,
		Configuration: modelvnc.DefaultConfig(),
	}
	return appsession.StartSpec{Kind: appsession.KindVNC, VNC: &cfg}
}

func nextEvent(t *testing.T, events <-chan appsession.DisplayEvent, kind appsession.DisplayEventKind) appsession.DisplayEvent {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case event, ok := <-events:
			if !ok {
				t.Fatalf("event stream closed before %v", kind)
			}
			if event.Kind == kind {
				return event
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %v", kind)
		}
	}
}

// TestOpenRendersTheServerFrameAndSendsInput is the adapter's end-to-end
// contract against the in-process RFB server: a None handshake, a delivered
// immutable frame, recorded key and pointer messages, and one clean close.
func TestOpenRendersTheServerFrameAndSendsInput(t *testing.T) {
	frame := testFrame()
	h := start(t, rfbtest.Config{
		Frame:       frame,
		DesktopName: "lab-desktop",
		Encoding:    rfb.EncodingRaw,
		SendBell:    true,
		CutText:     "from-server",
	})

	transport, err := h.factory.Open(context.Background(), openSpec("desk.test", 5900))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	display := transport.(appsession.DisplayProvider).Display()
	if display == nil {
		t.Fatal("Open returned no display")
	}
	if h.address != "desk.test:5900" {
		t.Fatalf("dial address = %q, want desk.test:5900", h.address)
	}

	// Non-frame events travel ahead of the coalesced frame, so drain until the
	// first frame and remember what came with it.
	var resize *appsession.DisplayEvent
	var bell, cutText bool
	var update appsession.DisplayEvent
	deadline := time.After(5 * time.Second)
	for update.Frame == nil {
		select {
		case event, ok := <-display.Events():
			if !ok {
				t.Fatal("event stream closed before a frame")
			}
			switch event.Kind {
			case appsession.DisplayEventDesktopName:
				if event.Text != "lab-desktop" {
					t.Fatalf("desktop name = %q, want lab-desktop", event.Text)
				}
			case appsession.DisplayEventResize:
				resize = &event
			case appsession.DisplayEventBell:
				bell = true
			case appsession.DisplayEventCutText:
				cutText = event.Text == "from-server"
			case appsession.DisplayEventFrame:
				update = event
			}
		case <-deadline:
			t.Fatal("timed out waiting for a frame")
		}
	}
	if resize == nil || resize.Width != 4 || resize.Height != 4 {
		t.Fatalf("resize = %+v, want 4x4", resize)
	}
	if update.Frame.Image.Bounds() != frame.Bounds() {
		t.Fatalf("frame bounds = %v, want %v", update.Frame.Image.Bounds(), frame.Bounds())
	}
	if red := update.Frame.Image.RGBAAt(0, 0); red != (color.RGBA{R: 200, G: 30, B: 10, A: 255}) {
		t.Fatalf("pixel (0,0) = %+v, want the server's red", red)
	}
	if !bell {
		t.Fatal("no bell event")
	}
	if !cutText {
		t.Fatal("no server cut-text event")
	}

	// The update loop is live: a delivered frame asks for the next
	// incremental one, so later changes reach the viewer.
	incremental := false
	requestDeadline := time.Now().Add(5 * time.Second)
	for !incremental && time.Now().Before(requestDeadline) {
		for _, request := range h.server.Received().Requests {
			incremental = incremental || request.Incremental
		}
		if !incremental {
			time.Sleep(5 * time.Millisecond)
		}
	}
	if !incremental {
		t.Fatalf("update requests = %+v, want an incremental request after the frame", h.server.Received().Requests)
	}

	if err := display.SendKey(0x61, true); err != nil {
		t.Fatalf("SendKey down: %v", err)
	}
	if err := display.SendKey(0x61, false); err != nil {
		t.Fatalf("SendKey up: %v", err)
	}
	if err := display.SendPointer(2, 3, 1); err != nil {
		t.Fatalf("SendPointer: %v", err)
	}
	if err := display.SendCutText("from-client"); err != nil {
		t.Fatalf("SendCutText: %v", err)
	}
	if err := display.SetDesktopSize(1024, 768); !errors.Is(err, appsession.ErrDesktopResizeUnsupported) {
		t.Fatalf("SetDesktopSize = %v, want ErrDesktopResizeUnsupported", err)
	}

	pollUntil := time.Now().Add(5 * time.Second)
	for time.Now().Before(pollUntil) {
		received := h.server.Received()
		if len(received.Keys) >= 2 && len(received.Pointers) >= 1 && len(received.CutTexts) >= 1 {
			if received.Keys[0] != (rfbtest.KeyEvent{Down: true, Keysym: 0x61}) ||
				received.Keys[1] != (rfbtest.KeyEvent{Down: false, Keysym: 0x61}) {
				t.Fatalf("keys = %+v, want press and release of 0x61", received.Keys)
			}
			if received.Pointers[0] != (rfbtest.PointerEvent{Mask: 1, X: 2, Y: 3}) {
				t.Fatalf("pointer = %+v, want mask 1 at 2,3", received.Pointers[0])
			}
			if received.CutTexts[0] != "from-client" {
				t.Fatalf("cut texts = %q, want from-client", received.CutTexts)
			}
			if len(received.Encodings) == 0 {
				t.Fatal("client advertised no encodings")
			}
			for _, encoding := range []rfb.Encoding{
				rfb.EncodingRaw, rfb.EncodingCopyRect, rfb.EncodingRRE, rfb.EncodingHextile,
			} {
				found := false
				for _, advertised := range received.Encodings {
					found = found || advertised == encoding
				}
				if !found {
					t.Fatalf("encodings = %v, missing %d", received.Encodings, encoding)
				}
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("server received %+v", h.server.Received())
}

// TestMalformedConfigurationDialsNothing proves validation happens before any
// connection, as the ticket requires.
func TestMalformedConfigurationDialsNothing(t *testing.T) {
	dialed := false
	factory := vnc.Factory{Dial: func(context.Context, string, string) (net.Conn, error) {
		dialed = true
		return nil, errors.New("unexpected dial")
	}}
	_, err := factory.Open(context.Background(), openSpec("", 5900))
	if !errors.Is(err, modelvnc.ErrInvalidConfig) {
		t.Fatalf("Open = %v, want ErrInvalidConfig", err)
	}
	if dialed {
		t.Fatal("a malformed configuration opened a connection")
	}
}

// TestUnsupportedSecurityFailsTyped proves an offer the client cannot negotiate
// is reported as unsupported rather than as a rejected password.
func TestUnsupportedSecurityFailsTyped(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	go func() {
		defer serverConn.Close()
		var clientVersion [12]byte
		_, _ = serverConn.Write([]byte("RFB 003.008\n"))
		if _, err := io.ReadFull(serverConn, clientVersion[:]); err != nil {
			return
		}
		// One offered security type: VNC authentication, which this slice does
		// not implement.
		_, _ = serverConn.Write([]byte{1, byte(rfb.SecurityVNCAuth)})
	}()
	factory := vnc.Factory{Dial: func(context.Context, string, string) (net.Conn, error) {
		return clientConn, nil
	}}
	_, err := factory.Open(context.Background(), openSpec("desk.test", 5900))
	if err == nil {
		t.Fatal("Open succeeded against an unsupported security type")
	}
	var typed *appconnection.Error
	if !errors.As(err, &typed) || typed.Kind != appconnection.KindUnsupported {
		t.Fatalf("Open = %v, want KindUnsupported", err)
	}
}

// TestCloseIsIdempotentAndClosesTheEventStream proves the adapter owns the
// display lifetime: one close, and the channel closes exactly once.
func TestCloseIsIdempotentAndClosesTheEventStream(t *testing.T) {
	h := start(t, rfbtest.Config{Frame: testFrame(), Encoding: rfb.EncodingRaw})
	transport, err := h.factory.Open(context.Background(), openSpec("desk.test", 5900))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	events := transport.(appsession.DisplayProvider).Display().Events()
	if err := transport.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := transport.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-events:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("event stream did not close")
		}
	}
}

// TestByteStreamMethodsAreUnavailable keeps a VNC transport from pretending to
// carry a terminal.
func TestByteStreamMethodsAreUnavailable(t *testing.T) {
	h := start(t, rfbtest.Config{Frame: testFrame(), Encoding: rfb.EncodingRaw})
	transport, err := h.factory.Open(context.Background(), openSpec("desk.test", 5900))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	if _, err := transport.Read(make([]byte, 1)); !errors.Is(err, appsession.ErrNoByteStream) {
		t.Fatalf("Read = %v, want ErrNoByteStream", err)
	}
	if _, err := transport.Write([]byte("x")); !errors.Is(err, appsession.ErrNoByteStream) {
		t.Fatalf("Write = %v, want ErrNoByteStream", err)
	}
	if err := transport.Resize(80, 24); !errors.Is(err, appsession.ErrNoByteStream) {
		t.Fatalf("Resize = %v, want ErrNoByteStream", err)
	}
}
