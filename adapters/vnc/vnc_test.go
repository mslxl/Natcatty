package vnc_test

import (
	"context"
	"errors"
	"image"
	"image/color"
	"io"
	"net"
	"strings"
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
	return openSpecWithPassword(hostname, port, "")
}

func openSpecWithPassword(hostname string, port int, password string) appsession.StartSpec {
	cfg := modelvnc.SessionConfig{
		Hostname:      hostname,
		Port:          port,
		Password:      password,
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

// TestPhysicalKeysUseExtendedEventsWhenNegotiated proves the adapter's send
// path: after QEMU's acknowledgement, a key with a physical number becomes an
// Extended Key Event, while a key without one falls back to a plain KeyEvent.
// The server's frame is awaited first, which also guarantees the client has
// read the acknowledgement that preceded it.
func TestPhysicalKeysUseExtendedEventsWhenNegotiated(t *testing.T) {
	h := start(t, rfbtest.Config{Frame: testFrame(), Encoding: rfb.EncodingRaw, ExtendedKeyEvent: true})
	transport, err := h.factory.Open(context.Background(), openSpec("desk.test", 5900))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	display := transport.(appsession.DisplayProvider).Display()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case event, ok := <-display.Events():
			if !ok {
				t.Fatal("event stream closed before a frame")
			}
			if event.Kind != appsession.DisplayEventFrame {
				continue
			}
		case <-deadline:
			t.Fatal("timed out waiting for the first frame")
		}
		break
	}

	if err := display.SendPhysicalKey(0x61, 30, true); err != nil {
		t.Fatalf("SendPhysicalKey down: %v", err)
	}
	if err := display.SendPhysicalKey(0x61, 30, false); err != nil {
		t.Fatalf("SendPhysicalKey up: %v", err)
	}
	if err := display.SendPhysicalKey(0xffe1, 0, true); err != nil {
		t.Fatalf("SendPhysicalKey without a key number: %v", err)
	}

	pollDeadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(pollDeadline) {
		received := h.server.Received()
		if len(received.ExtendedKeys) == 2 && len(received.Keys) == 1 {
			want := []rfbtest.ExtendedKeyEvent{
				{Down: true, Keysym: 0x61, Keycode: 30},
				{Down: false, Keysym: 0x61, Keycode: 30},
			}
			for i := range want {
				if received.ExtendedKeys[i] != want[i] {
					t.Fatalf("extended keys = %+v, want %+v", received.ExtendedKeys, want)
				}
			}
			if received.Keys[0] != (rfbtest.KeyEvent{Down: true, Keysym: 0xffe1}) {
				t.Fatalf("plain key = %+v, want the keysym-only fallback", received.Keys[0])
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("extended keys = %+v, plain keys = %+v", h.server.Received().ExtendedKeys, h.server.Received().Keys)
}

// TestPhysicalKeysStayPlainWithoutExtendedEvent proves the same send path
// sends ordinary KeyEvents when the server did not negotiate the extension.
func TestPhysicalKeysStayPlainWithoutExtendedEvent(t *testing.T) {
	h := start(t, rfbtest.Config{Frame: testFrame(), Encoding: rfb.EncodingRaw})
	transport, err := h.factory.Open(context.Background(), openSpec("desk.test", 5900))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	display := transport.(appsession.DisplayProvider).Display()
	if err := display.SendPhysicalKey(0x61, 30, true); err != nil {
		t.Fatalf("SendPhysicalKey: %v", err)
	}
	if err := display.SendPhysicalKey(0x61, 30, false); err != nil {
		t.Fatalf("SendPhysicalKey: %v", err)
	}
	pollDeadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(pollDeadline) {
		received := h.server.Received()
		if len(received.Keys) == 2 {
			if len(received.ExtendedKeys) != 0 {
				t.Fatalf("extended keys = %+v, want none", received.ExtendedKeys)
			}
			if received.Keys[0] != (rfbtest.KeyEvent{Down: true, Keysym: 0x61}) ||
				received.Keys[1] != (rfbtest.KeyEvent{Down: false, Keysym: 0x61}) {
				t.Fatalf("plain keys = %+v, want press and release", received.Keys)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("plain keys = %+v", h.server.Received().Keys)
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
// is reported as unsupported rather than as a rejected password, and that the
// failure suggests an SSH route.
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
		// One offered security type: VeNCrypt, which this slice does not
		// implement yet.
		_, _ = serverConn.Write([]byte{1, byte(rfb.SecurityVeNCrypt)})
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
	if !strings.Contains(err.Error(), "SSH route") {
		t.Fatalf("Open = %v, want an SSH-route suggestion", err)
	}
}

// TestVNCPasswordAuthenticates proves the adapter reads the password from the
// start section, negotiates the password type and reports the wire plaintext.
func TestVNCPasswordAuthenticates(t *testing.T) {
	const password = "desk-secret"
	h := start(t, rfbtest.Config{
		Frame: testFrame(), Encoding: rfb.EncodingRaw, Password: password,
	})
	transport, err := h.factory.Open(context.Background(), openSpecWithPassword("desk.test", 5900, password))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	reporter, ok := transport.(interface{ Security() rfb.SecurityType })
	if !ok || reporter.Security() != rfb.SecurityVNCAuth {
		t.Fatalf("negotiated security = %v, want VNC authentication", reporter.Security())
	}
	if plaintext, ok := transport.(interface{ Plaintext() bool }); !ok || !plaintext.Plaintext() {
		t.Fatal("a VNC password session did not report a plaintext wire")
	}
	if !h.server.Received().VNCAuthAccepted {
		t.Fatal("the server did not verify the VNC password")
	}
}

// TestVNCWrongPasswordIsAPermanentAuthFailure proves a rejected password is a
// typed authentication failure the Session will not retry.
func TestVNCWrongPasswordIsAPermanentAuthFailure(t *testing.T) {
	h := start(t, rfbtest.Config{
		Frame: testFrame(), Encoding: rfb.EncodingRaw, Password: "right-pass",
	})
	_, err := h.factory.Open(context.Background(), openSpecWithPassword("desk.test", 5900, "wrong-pass"))
	if !errors.Is(err, rfb.ErrSecurityRejected) {
		t.Fatalf("Open = %v, want ErrSecurityRejected", err)
	}
	var typed *appconnection.Error
	if !errors.As(err, &typed) || typed.Kind != appconnection.KindAuth {
		t.Fatalf("Open = %v, want a KindAuth failure", err)
	}
	if !typed.Permanent() {
		t.Fatal("a rejected VNC password was not classified as permanent")
	}
}

// TestVNCMissingPasswordPointsAtTheEditorField proves a server that demands a
// password with none saved reports the missing credential and where to set it.
func TestVNCMissingPasswordPointsAtTheEditorField(t *testing.T) {
	h := start(t, rfbtest.Config{
		Frame: testFrame(), Encoding: rfb.EncodingRaw, Password: "required",
	})
	_, err := h.factory.Open(context.Background(), openSpec("desk.test", 5900))
	if !errors.Is(err, rfb.ErrMissingPassword) {
		t.Fatalf("Open = %v, want ErrMissingPassword", err)
	}
	var typed *appconnection.Error
	if !errors.As(err, &typed) || typed.Kind != appconnection.KindAuth {
		t.Fatalf("Open = %v, want a KindAuth failure", err)
	}
	if !typed.Permanent() {
		t.Fatal("a missing VNC password was not classified as permanent")
	}
	if !strings.Contains(err.Error(), "VNC Password field") {
		t.Fatalf("Open = %v, want the editor field in the message", err)
	}
}

// TestVNCReportsNoneAsPlaintext proves the default None handshake reports the
// wire as plaintext too.
func TestVNCReportsNoneAsPlaintext(t *testing.T) {
	h := start(t, rfbtest.Config{Frame: testFrame(), Encoding: rfb.EncodingRaw})
	transport, err := h.factory.Open(context.Background(), openSpec("desk.test", 5900))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	reporter, ok := transport.(interface{ Plaintext() bool })
	if !ok || !reporter.Plaintext() {
		t.Fatal("a None session did not report a plaintext wire")
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

// TestRoutedDecisionIsCarriedFromTheFactory proves the adapter takes the
// tunnel-encryption decision from the Factory and never infers it from the
// negotiated protocol: both a routed and an unrouted Factory run the same None
// handshake, and each transport reports the value it was given.
func TestRoutedDecisionIsCarriedFromTheFactory(t *testing.T) {
	for _, routed := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct", true: "routed"}[routed], func(t *testing.T) {
			h := start(t, rfbtest.Config{Frame: testFrame(), Encoding: rfb.EncodingRaw})
			h.factory.Routed = routed
			transport, err := h.factory.Open(context.Background(), openSpec("desk.test", 5900))
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			t.Cleanup(func() { _ = transport.Close() })
			reporter, ok := transport.(interface{ Routed() bool })
			if !ok {
				t.Fatal("the transport does not report its routed decision")
			}
			if got := reporter.Routed(); got != routed {
				t.Fatalf("Routed() = %v, want %v", got, routed)
			}
		})
	}
}

func hasEncoding(encodings []rfb.Encoding, want rfb.Encoding) bool {
	for _, encoding := range encodings {
		if encoding == want {
			return true
		}
	}
	return false
}

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestZRLEOnlyServerRendersTheGoldenFrame proves the adapter prefers ZRLE and
// composites the decoder's output identically to the server image.
func TestZRLEOnlyServerRendersTheGoldenFrame(t *testing.T) {
	frame := testFrame()
	h := start(t, rfbtest.Config{Frame: frame, Encoding: rfb.EncodingZRLE})
	transport, err := h.factory.Open(context.Background(), openSpec("desk.test", 5900))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	display := transport.(appsession.DisplayProvider).Display()

	event := nextEvent(t, display.Events(), appsession.DisplayEventFrame)
	if event.Frame == nil || event.Frame.Image.Bounds() != frame.Bounds() {
		t.Fatalf("frame = %+v, want %v", event.Frame, frame.Bounds())
	}
	for y := 0; y < frame.Bounds().Dy(); y++ {
		for x := 0; x < frame.Bounds().Dx(); x++ {
			if got, want := event.Frame.Image.RGBAAt(x, y), frame.RGBAAt(x, y); got != want {
				t.Fatalf("ZRLE pixel %d,%d = %+v, want %+v", x, y, got, want)
			}
		}
	}
	waitFor(t, "the client to advertise ZRLE", func() bool {
		return hasEncoding(h.server.Received().Encodings, rfb.EncodingZRLE)
	})
}

// TestExtensionsAreNegotiatedAndKeepUpdatesFlowing proves ContinuousUpdates and
// Fence are advertised, negotiated and used: the server streams a frame after
// the client enables continuous updates, and the client stops polling.
func TestExtensionsAreNegotiatedAndKeepUpdatesFlowing(t *testing.T) {
	h := start(t, rfbtest.Config{
		Frame:               testFrame(),
		Encoding:            rfb.EncodingRaw,
		ExtendedDesktopSize: true,
		ContinuousUpdates:   true,
		Fence:               true,
	})
	transport, err := h.factory.Open(context.Background(), openSpec("desk.test", 5900))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	display := transport.(appsession.DisplayProvider).Display()

	// Wait for the first frame: by then the EDS rectangle has been read and
	// the extension is live.
	frameDeadline := time.After(5 * time.Second)
	frameSeen := false
	for !frameSeen {
		select {
		case event, ok := <-display.Events():
			if !ok {
				t.Fatalf("event stream closed; encodings=%v", h.server.Received().Encodings)
			}
			frameSeen = event.Kind == appsession.DisplayEventFrame
		case err := <-h.serve:
			t.Fatalf("server ended early: %v; encodings=%v", err, h.server.Received().Encodings)
		case <-frameDeadline:
			t.Fatalf("timed out waiting for a frame; encodings=%v received=%+v",
				h.server.Received().Encodings, h.server.Received().Requests)
		}
	}
	negotiationDeadline := time.Now().Add(5 * time.Second)
	for {
		received := h.server.Received()
		negotiated := hasEncoding(received.Encodings, rfb.EncodingContinuousUpdates) &&
			hasEncoding(received.Encodings, rfb.EncodingFence) &&
			hasEncoding(received.Encodings, rfb.EncodingExtendedDesktopSize) &&
			hasEncoding(received.Encodings, rfb.EncodingCursorWithAlpha) &&
			len(received.Continuous) > 0 && received.Continuous[0].Enable &&
			len(received.Fences) > 0
		if negotiated {
			break
		}
		if time.Now().After(negotiationDeadline) {
			t.Fatalf("extensions did not negotiate: encodings=%v continuous=%+v fences=%+v requests=%+v",
				received.Encodings, received.Continuous, received.Fences, received.Requests)
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Polling stopped as soon as continuous updates were enabled: the only
	// update request is the initial non-incremental one.
	received := h.server.Received()
	for _, request := range received.Requests {
		if request.Incremental {
			t.Fatalf("the client polled after enabling continuous updates: %+v", received.Requests)
		}
	}

	if err := display.SetDesktopSize(8, 6); err != nil {
		t.Fatalf("SetDesktopSize: %v", err)
	}
	waitFor(t, "the resize request to reach the server", func() bool {
		for _, request := range h.server.Received().DesktopSizes {
			if request.Width == 8 && request.Height == 6 {
				return true
			}
		}
		return false
	})
	// A successful resize re-scopes server-driven updates to the new extent,
	// so the added area is part of the continuous region.
	waitFor(t, "continuous updates re-scoped to 8x6", func() bool {
		for _, update := range h.server.Received().Continuous {
			if update.Enable && update.Width == 8 && update.Height == 6 {
				return true
			}
		}
		return false
	})
	// The server follows the request and streams a repaint at the new size.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		event := nextEvent(t, display.Events(), appsession.DisplayEventFrame)
		if event.Frame.Image.Bounds().Dx() == 8 && event.Frame.Image.Bounds().Dy() == 6 {
			return
		}
	}
	t.Fatal("the resized desktop never repainted")
}

// TestCursorShapeArrivesAsADisplayEvent proves CursorWithAlpha is parsed and
// surfaced through the display port, including the hidden-cursor form.
func TestCursorShapeArrivesAsADisplayEvent(t *testing.T) {
	cursor := image.NewRGBA(image.Rect(0, 0, 2, 1))
	cursor.SetRGBA(0, 0, color.RGBA{R: 255, A: 255})
	cursor.SetRGBA(1, 0, color.RGBA{G: 255, A: 255})
	h := start(t, rfbtest.Config{
		Frame: testFrame(), Encoding: rfb.EncodingRaw,
		Cursor: cursor, CursorAt: image.Pt(1, 0),
	})
	transport, err := h.factory.Open(context.Background(), openSpec("desk.test", 5900))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	event := nextEvent(t, transport.(appsession.DisplayProvider).Display().Events(),
		appsession.DisplayEventCursorShape)
	if event.Cursor == nil || event.Cursor.Image == nil {
		t.Fatalf("cursor event = %+v", event)
	}
	if event.Cursor.Hotspot != image.Pt(1, 0) {
		t.Fatalf("cursor hotspot = %v, want (1,0)", event.Cursor.Hotspot)
	}
	if got := event.Cursor.Image.RGBAAt(0, 0); got != (color.RGBA{R: 255, A: 255}) {
		t.Fatalf("cursor pixel = %+v", got)
	}
}

// TestHiddenCursorArrivesAsAnEmptyShape proves a zero-size CursorWithAlpha
// clears the local cursor instead of drawing a stale one.
func TestHiddenCursorArrivesAsAnEmptyShape(t *testing.T) {
	cursor := image.NewRGBA(image.Rect(0, 0, 1, 1))
	cursor.SetRGBA(0, 0, color.RGBA{A: 255})
	h := start(t, rfbtest.Config{
		Frame: testFrame(), Encoding: rfb.EncodingRaw,
		Cursor: cursor, HideCursor: true,
	})
	transport, err := h.factory.Open(context.Background(), openSpec("desk.test", 5900))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	event := nextEvent(t, transport.(appsession.DisplayProvider).Display().Events(),
		appsession.DisplayEventCursorShape)
	if event.Cursor != nil {
		t.Fatalf("hidden cursor event = %+v, want an empty shape", event.Cursor)
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
