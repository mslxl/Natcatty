package rfb_test

import (
	"testing"
	"time"

	"github.com/mslxl/natcatty/domain/model/rfb"
	"github.com/mslxl/natcatty/domain/model/rfb/rfbtest"
)

func readUntil(t *testing.T, client *rfb.Client, match func(rfb.ServerMessage) bool) rfb.ServerMessage {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		message, err := client.ReadMessage()
		if err != nil {
			t.Fatalf("ReadMessage: %v", err)
		}
		if match(message) {
			return message
		}
	}
	t.Fatal("timed out waiting for a server message")
	return rfb.ServerMessage{}
}

func TestClientFenceAndContinuousExtensions(t *testing.T) {
	server, conn, done := startServer(t, rfbtest.Config{
		Frame:             newTestFrame(2, 2),
		Encoding:          rfb.EncodingRaw,
		Fence:             true,
		ContinuousUpdates: true,
	})
	client, err := rfb.Connect(conn, rfb.ClientConfig{Shared: true})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	var fence rfb.Fence
	sawEnd := false
	deadline := time.Now().Add(10 * time.Second)
	for (!sawEnd || fence.Flags == 0) && time.Now().Before(deadline) {
		message, err := client.ReadMessage()
		if err != nil {
			t.Fatalf("ReadMessage: %v", err)
		}
		switch message.Kind {
		case rfb.MessageEndOfContinuousUpdates:
			sawEnd = true
		case rfb.MessageFence:
			fence = message.Fence
		}
	}
	if !sawEnd {
		t.Fatal("the client never learned ContinuousUpdates support")
	}
	if !fence.Request() {
		t.Fatalf("ServerFence = %+v, want a request", fence)
	}

	response := rfb.Fence{
		Flags:   fence.Flags &^ rfb.FenceRequest,
		Payload: fence.Payload,
	}
	if err := client.SendFence(response.Flags, response.Payload); err != nil {
		t.Fatalf("SendFence: %v", err)
	}
	if err := client.SendContinuousUpdates(true, 0, 0, 2, 2); err != nil {
		t.Fatalf("SendContinuousUpdates: %v", err)
	}
	// The server streams a frame once continuous updates are enabled, without
	// a further request.
	readUntil(t, client, func(message rfb.ServerMessage) bool {
		return message.Kind == rfb.MessageFramebufferUpdate
	})

	_ = conn.Close()
	waitServer(t, done)
	received := server.Received()
	if len(received.Continuous) != 1 || !received.Continuous[0].Enable {
		t.Fatalf("continuous updates = %+v, want one enable", received.Continuous)
	}
	if len(received.Fences) != 1 {
		t.Fatalf("fences = %+v, want the client response", received.Fences)
	}
	if got := received.Fences[0]; got.Request() || got.Flags != rfb.FenceBlockBefore {
		t.Fatalf("client fence = %+v, want the Request bit cleared", got)
	}
}

func TestClientQEMUExtendedKeyEvents(t *testing.T) {
	server, conn, done := startServer(t, rfbtest.Config{
		Frame:            newTestFrame(2, 2),
		Encoding:         rfb.EncodingRaw,
		ExtendedKeyEvent: true,
	})
	client, err := rfb.Connect(conn, rfb.ClientConfig{Shared: true})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if client.ExtendedKeySupported() {
		t.Fatal("ExtendedKeyEvent support was set before the server acknowledged it")
	}
	readUntil(t, client, func(message rfb.ServerMessage) bool {
		for _, rect := range message.Rectangles {
			if rect.Encoding == rfb.EncodingExtendedKeyEvent {
				return true
			}
		}
		return false
	})
	if !client.ExtendedKeySupported() {
		t.Fatal("the server's pseudo-rectangle did not negotiate ExtendedKeyEvent")
	}

	// A known physical key number takes the QEMU message; an unknown one falls
	// back to a plain KeyEvent instead, as the spec requires.
	if err := client.SendPhysicalKey(0x61, 30, true); err != nil {
		t.Fatalf("SendPhysicalKey extended down: %v", err)
	}
	if err := client.SendPhysicalKey(0x61, 30, false); err != nil {
		t.Fatalf("SendPhysicalKey extended up: %v", err)
	}
	if err := client.SendPhysicalKey(0x41, 0, true); err != nil {
		t.Fatalf("SendPhysicalKey plain fallback: %v", err)
	}

	_ = conn.Close()
	waitServer(t, done)
	received := server.Received()
	wantExtended := []rfbtest.ExtendedKeyEvent{
		{Down: true, Keysym: 0x61, Keycode: 30},
		{Down: false, Keysym: 0x61, Keycode: 30},
	}
	if len(received.ExtendedKeys) != len(wantExtended) {
		t.Fatalf("extended key events = %+v, want %+v", received.ExtendedKeys, wantExtended)
	}
	for i := range wantExtended {
		if received.ExtendedKeys[i] != wantExtended[i] {
			t.Fatalf("extended key events = %+v, want %+v", received.ExtendedKeys, wantExtended)
		}
	}
	if len(received.Keys) != 1 || received.Keys[0] != (rfbtest.KeyEvent{Down: true, Keysym: 0x41}) {
		t.Fatalf("plain key events = %+v, want the keysym-only fallback", received.Keys)
	}
}

func TestClientExtendedKeyEventNotNegotiatedFallsBackToKeysym(t *testing.T) {
	server, conn, done := startServer(t, rfbtest.Config{
		Frame:    newTestFrame(2, 2),
		Encoding: rfb.EncodingRaw,
	})
	client, err := rfb.Connect(conn, rfb.ClientConfig{Shared: true})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if client.ExtendedKeySupported() {
		t.Fatal("ExtendedKeyEvent support was set without a server acknowledgement")
	}
	if err := client.SendPhysicalKey(0x62, 48, true); err != nil {
		t.Fatalf("SendPhysicalKey: %v", err)
	}
	if err := client.SendPhysicalKey(0x62, 48, false); err != nil {
		t.Fatalf("SendPhysicalKey: %v", err)
	}
	_ = conn.Close()
	waitServer(t, done)
	received := server.Received()
	if len(received.ExtendedKeys) != 0 {
		t.Fatalf("extended key events = %+v, want none before negotiation", received.ExtendedKeys)
	}
	want := []rfbtest.KeyEvent{{Down: true, Keysym: 0x62}, {Down: false, Keysym: 0x62}}
	if len(received.Keys) != len(want) {
		t.Fatalf("plain key events = %+v, want %+v", received.Keys, want)
	}
	for i := range want {
		if received.Keys[i] != want[i] {
			t.Fatalf("plain key events = %+v, want %+v", received.Keys, want)
		}
	}
}

func TestClientSetDesktopSizeRoundTrip(t *testing.T) {
	server, conn, done := startServer(t, rfbtest.Config{
		Frame:               newTestFrame(2, 2),
		Encoding:            rfb.EncodingRaw,
		ExtendedDesktopSize: true,
	})
	client, err := rfb.Connect(conn, rfb.ClientConfig{Shared: true})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := client.RequestFullUpdate(false); err != nil {
		t.Fatalf("RequestFullUpdate: %v", err)
	}
	initial := readUntil(t, client, func(message rfb.ServerMessage) bool {
		for _, rect := range message.Rectangles {
			if rect.Extended != nil {
				return true
			}
		}
		return false
	})
	if extended := initial.Rectangles[0].Extended; extended == nil || extended.Width != 2 || extended.Height != 2 {
		t.Fatalf("initial ExtendedDesktopSize = %+v", initial.Rectangles)
	}
	// Drain the data update that followed the layout in the same response, so
	// the server is back in its read loop before the client writes.
	readUntil(t, client, func(message rfb.ServerMessage) bool {
		for _, rect := range message.Rectangles {
			if rect.Extended == nil && rect.Cursor == nil {
				return true
			}
		}
		return false
	})

	if err := client.SendSetDesktopSize(8, 6, []rfb.Screen{{Width: 8, Height: 6}}); err != nil {
		t.Fatalf("SendSetDesktopSize: %v", err)
	}
	response := readUntil(t, client, func(message rfb.ServerMessage) bool {
		for _, rect := range message.Rectangles {
			if rect.Extended != nil && rect.Extended.Width == 8 {
				return true
			}
		}
		return false
	})
	extended := response.Rectangles[0].Extended
	if extended.Reason != 1 || extended.Status != 0 {
		t.Fatalf("ExtendedDesktopSize response = %+v, want a successful client-initiated change", extended)
	}
	if got := client.Frame().Bounds(); got.Dx() != 8 || got.Dy() != 6 {
		t.Fatalf("framebuffer bounds = %v, want 8x6", got)
	}
	// Drain the repaint the server sent at the new size before closing.
	readUntil(t, client, func(message rfb.ServerMessage) bool {
		return message.Kind == rfb.MessageFramebufferUpdate
	})

	_ = conn.Close()
	waitServer(t, done)
	defer func() { _ = conn.Close() }()
	if requests := server.Received().DesktopSizes; len(requests) != 1 || requests[0].Width != 8 || requests[0].Height != 6 {
		t.Fatalf("desktop size requests = %+v", requests)
	}
	_ = server
}
