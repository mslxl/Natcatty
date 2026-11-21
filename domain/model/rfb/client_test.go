package rfb_test

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mslxl/natcatty/domain/model/rfb"
	"github.com/mslxl/natcatty/domain/model/rfb/rfbtest"
)

var testColors = []color.RGBA{
	{R: 255, A: 255},
	{G: 255, A: 255},
	{B: 255, A: 255},
	{R: 255, G: 255, B: 255, A: 255},
}

func newTestFrame(width, height int) *image.RGBA {
	frame := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			frame.SetRGBA(x, y, testColors[(x+y)%len(testColors)])
		}
	}
	return frame
}

func mirroredFrame() *image.RGBA {
	frame := image.NewRGBA(image.Rect(0, 0, 8, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			pixel := testColors[(x+y)%len(testColors)]
			frame.SetRGBA(x, y, pixel)
			frame.SetRGBA(x+4, y, pixel)
		}
	}
	return frame
}

func startServer(t *testing.T, cfg rfbtest.Config) (*rfbtest.Server, net.Conn, <-chan error) {
	t.Helper()
	serverConn, clientConn := net.Pipe()
	if err := clientConn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("SetDeadline: %v", err)
	}
	server := rfbtest.New(cfg)
	done := make(chan error, 1)
	go func() { done <- server.Serve(serverConn) }()
	t.Cleanup(func() {
		_ = clientConn.Close()
		_ = serverConn.Close()
	})
	return server, clientConn, done
}

func waitServer(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("server: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("server did not finish")
	}
}

func checkFrame(t *testing.T, client *rfb.Client, want *image.RGBA) {
	t.Helper()
	got := client.Frame().Image()
	if got.Bounds() != want.Bounds() {
		t.Fatalf("frame bounds = %v, want %v", got.Bounds(), want.Bounds())
	}
	if !bytes.Equal(got.Pix, want.Pix) {
		t.Fatalf("frame pixels differ from the reference")
	}
}

func TestClientHandshakeVersions(t *testing.T) {
	for _, version := range []rfb.Version{rfb.Version3_3, rfb.Version3_7, rfb.Version3_8} {
		t.Run(version.String(), func(t *testing.T) {
			frame := newTestFrame(4, 3)
			server, conn, done := startServer(t, rfbtest.Config{
				Version:     version,
				Frame:       frame,
				DesktopName: "test desktop",
			})
			client, err := rfb.Connect(conn, rfb.ClientConfig{Shared: true})
			if err != nil {
				t.Fatalf("Connect: %v", err)
			}
			if client.Version() != version {
				t.Fatalf("version = %v, want %v", client.Version(), version)
			}
			info := client.ServerInfo()
			if info.Width != 4 || info.Height != 3 || info.DesktopName != "test desktop" {
				t.Fatalf("ServerInfo = %+v", info)
			}
			if info.PixelFormat != rfb.DefaultPixelFormat() {
				t.Fatalf("ServerInfo pixel format = %+v", info.PixelFormat)
			}

			if err := client.RequestFullUpdate(false); err != nil {
				t.Fatalf("RequestFullUpdate: %v", err)
			}
			message, err := client.ReadMessage()
			if err != nil {
				t.Fatalf("ReadMessage: %v", err)
			}
			if message.Kind != rfb.MessageFramebufferUpdate {
				t.Fatalf("message = %+v, want a framebuffer update", message)
			}
			checkFrame(t, client, frame)

			if err := client.SetEncodings([]rfb.Encoding{rfb.EncodingTight}); !errors.Is(err, rfb.ErrUnsupportedEncoding) {
				t.Fatalf("SetEncodings(Tight) = %v, want ErrUnsupportedEncoding", err)
			}
			if err := client.SendKey(0x61, true); err != nil {
				t.Fatalf("SendKey down: %v", err)
			}
			if err := client.SendKey(0x61, false); err != nil {
				t.Fatalf("SendKey up: %v", err)
			}
			if err := client.SendPointer(1, 2, 3); err != nil {
				t.Fatalf("SendPointer: %v", err)
			}
			if err := client.SendCutText("clip"); err != nil {
				t.Fatalf("SendCutText: %v", err)
			}
			_ = conn.Close()
			waitServer(t, done)

			received := server.Received()
			if received.ClientVersion != version {
				t.Fatalf("server saw client version %v, want %v", received.ClientVersion, version)
			}
			if !received.Shared {
				t.Fatal("server did not see the shared flag")
			}
			if received.PixelFormat != rfb.DefaultPixelFormat() {
				t.Fatalf("server saw pixel format %+v", received.PixelFormat)
			}
			if !reflect.DeepEqual(received.Encodings, rfb.ClientEncodings()) {
				t.Fatalf("server saw encodings %v, want %v", received.Encodings, rfb.ClientEncodings())
			}
			if len(received.Requests) != 1 {
				t.Fatalf("requests = %+v", received.Requests)
			}
			if received.Requests[0].Incremental || received.Requests[0].Width != 4 || received.Requests[0].Height != 3 {
				t.Fatalf("request = %+v", received.Requests[0])
			}
			if len(received.Keys) != 2 || received.Keys[0] != (rfbtest.KeyEvent{Down: true, Keysym: 0x61}) ||
				received.Keys[1] != (rfbtest.KeyEvent{Down: false, Keysym: 0x61}) {
				t.Fatalf("keys = %+v", received.Keys)
			}
			if len(received.Pointers) != 1 || received.Pointers[0] != (rfbtest.PointerEvent{Mask: 1, X: 2, Y: 3}) {
				t.Fatalf("pointers = %+v", received.Pointers)
			}
			if len(received.CutTexts) != 1 || received.CutTexts[0] != "clip" {
				t.Fatalf("cut texts = %+v", received.CutTexts)
			}
		})
	}
}

// TestClientVNCPasswordHandshake proves the client completes VNC password
// authentication against the in-process server for every protocol version the
// engine speaks.
func TestClientVNCPasswordHandshake(t *testing.T) {
	for _, version := range []rfb.Version{rfb.Version3_3, rfb.Version3_7, rfb.Version3_8} {
		t.Run(version.String(), func(t *testing.T) {
			server, conn, done := startServer(t, rfbtest.Config{
				Version:  version,
				Frame:    newTestFrame(2, 2),
				Password: "correct-horse",
			})
			client, err := rfb.Connect(conn, rfb.ClientConfig{Shared: true, Password: "correct-horse"})
			if err != nil {
				t.Fatalf("Connect: %v", err)
			}
			if client.Security() != rfb.SecurityVNCAuth {
				t.Fatalf("Security() = %v, want VNC authentication", client.Security())
			}
			_ = conn.Close()
			waitServer(t, done)
			received := server.Received()
			if received.Security != rfb.SecurityVNCAuth {
				t.Fatalf("server saw security type %v, want VNC authentication", received.Security)
			}
			if !received.VNCAuthAccepted {
				t.Fatal("server did not verify the VNC password response")
			}
		})
	}
}

// TestTestServerRejectsAWrongVNCPassword proves the in-process server rejects
// a response that does not match its configured password, and the client
// reports the rejection.
func TestTestServerRejectsAWrongVNCPassword(t *testing.T) {
	_, conn, done := startServer(t, rfbtest.Config{Width: 2, Height: 2, Password: "right-pass"})
	_, err := rfb.Connect(conn, rfb.ClientConfig{Password: "wrong-pass"})
	if !errors.Is(err, rfb.ErrSecurityRejected) {
		t.Fatalf("Connect = %v, want ErrSecurityRejected", err)
	}
	select {
	case serverErr := <-done:
		if serverErr == nil || !strings.Contains(serverErr.Error(), "authentication failed") {
			t.Fatalf("server error = %v, want an authentication failure", serverErr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("server did not finish after rejecting a password")
	}
}

// TestClientMissingVNCPasswordFailsClosed proves a server that offers only VNC
// password authentication is not satisfied with an empty credential.
func TestClientMissingVNCPasswordFailsClosed(t *testing.T) {
	_, conn, _ := startServer(t, rfbtest.Config{Width: 2, Height: 2, Password: "required"})
	_, err := rfb.Connect(conn, rfb.ClientConfig{})
	if !errors.Is(err, rfb.ErrMissingPassword) {
		t.Fatalf("Connect = %v, want ErrMissingPassword", err)
	}
}

// TestClientSelectsVNCPasswordOverNone proves the server's stronger offer wins
// even when None is also available.
func TestClientSelectsVNCPasswordOverNone(t *testing.T) {
	server, conn, done := startServer(t, rfbtest.Config{
		Width: 2, Height: 2,
		Password:      "preferred",
		SecurityTypes: []rfb.SecurityType{rfb.SecurityNone, rfb.SecurityVNCAuth},
	})
	client, err := rfb.Connect(conn, rfb.ClientConfig{Password: "preferred"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if client.Security() != rfb.SecurityVNCAuth {
		t.Fatalf("Security() = %v, want VNC authentication", client.Security())
	}
	_ = conn.Close()
	waitServer(t, done)
	if received := server.Received(); received.Security != rfb.SecurityVNCAuth {
		t.Fatalf("server saw security type %v, want VNC authentication", received.Security)
	}
}

// TestClientUnsupportedOnlySecurityNamesTheOffer proves an offer the client
// cannot speak fails closed with the offered types recorded.
func TestClientUnsupportedOnlySecurityNamesTheOffer(t *testing.T) {
	_, conn, _ := startServer(t, rfbtest.Config{
		Width: 2, Height: 2,
		SecurityTypes: []rfb.SecurityType{rfb.SecurityVeNCrypt},
	})
	_, err := rfb.Connect(conn, rfb.ClientConfig{})
	if !errors.Is(err, rfb.ErrUnsupportedSecurity) {
		t.Fatalf("Connect = %v, want ErrUnsupportedSecurity", err)
	}
	var securityErr *rfb.SecurityError
	if !errors.As(err, &securityErr) || !containsSecurityType(securityErr.Offered, rfb.SecurityVeNCrypt) {
		t.Fatalf("Connect = %v, want the offered VeNCrypt type", err)
	}
}

func containsSecurityType(offered []rfb.SecurityType, want rfb.SecurityType) bool {
	for _, security := range offered {
		if security == want {
			return true
		}
	}
	return false
}

func TestClientServerMessages(t *testing.T) {
	frame := newTestFrame(2, 2)
	_, conn, done := startServer(t, rfbtest.Config{
		Frame:    frame,
		SendBell: true,
		CutText:  "remote clipboard",
	})
	client, err := rfb.Connect(conn, rfb.ClientConfig{Shared: false})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := client.RequestFullUpdate(false); err != nil {
		t.Fatalf("RequestFullUpdate: %v", err)
	}
	bell, err := client.ReadMessage()
	if err != nil {
		t.Fatalf("Bell: %v", err)
	}
	if bell.Kind != rfb.MessageBell {
		t.Fatalf("first message = %+v, want Bell", bell)
	}
	cut, err := client.ReadMessage()
	if err != nil {
		t.Fatalf("CutText: %v", err)
	}
	if cut.Kind != rfb.MessageCutText || cut.CutText != "remote clipboard" {
		t.Fatalf("second message = %+v", cut)
	}
	update, err := client.ReadMessage()
	if err != nil {
		t.Fatalf("FramebufferUpdate: %v", err)
	}
	if update.Kind != rfb.MessageFramebufferUpdate {
		t.Fatalf("third message = %+v", update)
	}
	checkFrame(t, client, frame)
	_ = conn.Close()
	waitServer(t, done)
}

func TestTestServerServesEveryAdvertisedEncoding(t *testing.T) {
	frame := mirroredFrame()
	cases := []struct {
		name string
		cfg  rfbtest.Config
	}{
		{
			name: "Raw",
			cfg:  rfbtest.Config{Frame: frame, Encoding: rfb.EncodingRaw},
		},
		{
			name: "RRE",
			cfg:  rfbtest.Config{Frame: frame, Encoding: rfb.EncodingRRE},
		},
		{
			name: "Hextile",
			cfg:  rfbtest.Config{Frame: frame, Encoding: rfb.EncodingHextile},
		},
		{
			name: "CopyRect",
			cfg: rfbtest.Config{
				Frame: frame,
				Rectangles: []rfbtest.Rectangle{
					{X: 0, Y: 0, Width: 4, Height: 4, Encoding: rfb.EncodingRaw},
					{X: 4, Y: 0, Width: 4, Height: 4, Encoding: rfb.EncodingCopyRect, SrcX: 0, SrcY: 0},
				},
			},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, conn, done := startServer(t, test.cfg)
			client, err := rfb.Connect(conn, rfb.ClientConfig{Shared: true})
			if err != nil {
				t.Fatalf("Connect: %v", err)
			}
			if err := client.RequestFullUpdate(false); err != nil {
				t.Fatalf("RequestFullUpdate: %v", err)
			}
			message, err := client.ReadMessage()
			if err != nil {
				t.Fatalf("ReadMessage: %v", err)
			}
			if message.Kind != rfb.MessageFramebufferUpdate {
				t.Fatalf("message = %+v", message)
			}
			checkFrame(t, client, frame)
			_ = conn.Close()
			waitServer(t, done)
		})
	}
}

func TestClientNormalisesNon32BitServerPixelFormat(t *testing.T) {
	format := rfb.PixelFormat{
		BitsPerPixel: 16,
		Depth:        16,
		TrueColour:   true,
		RedMax:       31,
		GreenMax:     63,
		BlueMax:      31,
		RedShift:     11,
		GreenShift:   5,
		BlueShift:    0,
	}
	frame := newTestFrame(2, 2)
	_, conn, done := startServer(t, rfbtest.Config{
		Frame:            frame,
		ForcePixelFormat: true,
		PixelFormat:      format,
	})
	client, err := rfb.Connect(conn, rfb.ClientConfig{Shared: true})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if client.ServerInfo().PixelFormat != format {
		t.Fatalf("ServerInfo pixel format = %+v, want %+v", client.ServerInfo().PixelFormat, format)
	}
	if err := client.SetPixelFormat(format); err != nil {
		t.Fatalf("SetPixelFormat: %v", err)
	}
	if err := client.RequestFullUpdate(false); err != nil {
		t.Fatalf("RequestFullUpdate: %v", err)
	}
	message, err := client.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if message.Kind != rfb.MessageFramebufferUpdate {
		t.Fatalf("message = %+v", message)
	}
	checkFrame(t, client, frame)
	_ = conn.Close()
	waitServer(t, done)
}

func TestClientEncodingsNeverIncludeTight(t *testing.T) {
	encodings := rfb.ClientEncodings()
	want := []rfb.Encoding{rfb.EncodingRaw, rfb.EncodingCopyRect, rfb.EncodingRRE, rfb.EncodingHextile}
	if !reflect.DeepEqual(encodings, want) {
		t.Fatalf("ClientEncodings = %v, want %v", encodings, want)
	}
	for _, encoding := range encodings {
		if encoding == rfb.EncodingTight || encoding == rfb.EncodingTightPNG {
			t.Fatalf("ClientEncodings advertises %d", encoding)
		}
	}
}

func TestSetEncodingsRefusesUndecodableEncodings(t *testing.T) {
	undecodable := []struct {
		name     string
		encoding rfb.Encoding
	}{
		{"Tight", rfb.EncodingTight},
		{"TightPNG", rfb.EncodingTightPNG},
		{"ZRLE", rfb.Encoding(16)},
		{"DesktopSize", rfb.Encoding(-223)},
	}
	for _, test := range undecodable {
		t.Run(test.name, func(t *testing.T) {
			frame := newTestFrame(2, 2)
			server, conn, done := startServer(t, rfbtest.Config{Frame: frame})
			client, err := rfb.Connect(conn, rfb.ClientConfig{Shared: true})
			if err != nil {
				t.Fatalf("Connect: %v", err)
			}
			err = client.SetEncodings([]rfb.Encoding{rfb.EncodingRaw, test.encoding})
			if !errors.Is(err, rfb.ErrUnsupportedEncoding) {
				t.Fatalf("SetEncodings(%d) = %v, want ErrUnsupportedEncoding", test.encoding, err)
			}
			// Drive one more message so the server has consumed every byte the
			// client sent before the connection closes.
			if err := client.SendKey(0x61, true); err != nil {
				t.Fatalf("SendKey: %v", err)
			}
			_ = conn.Close()
			waitServer(t, done)

			received := server.Received()
			if want := rfb.ClientEncodings(); !reflect.DeepEqual(received.Encodings, want) {
				t.Fatalf("server saw encodings %v after a refused SetEncodings, want %v", received.Encodings, want)
			}
		})
	}
}

func TestSetEncodingsWritesSupportedList(t *testing.T) {
	frame := newTestFrame(2, 2)
	server, conn, done := startServer(t, rfbtest.Config{Frame: frame})
	client, err := rfb.Connect(conn, rfb.ClientConfig{Shared: true})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	want := []rfb.Encoding{rfb.EncodingHextile, rfb.EncodingRaw}
	if err := client.SetEncodings(want); err != nil {
		t.Fatalf("SetEncodings: %v", err)
	}
	if err := client.SendKey(0x61, true); err != nil {
		t.Fatalf("SendKey: %v", err)
	}
	_ = conn.Close()
	waitServer(t, done)
	if received := server.Received(); !reflect.DeepEqual(received.Encodings, want) {
		t.Fatalf("server saw encodings %v, want %v", received.Encodings, want)
	}
}
