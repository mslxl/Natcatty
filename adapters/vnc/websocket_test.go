package vnc_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/mslxl/natcatty/adapters/vnc"
	appconnection "github.com/mslxl/natcatty/application/connection"
	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model/rfb"
	"github.com/mslxl/natcatty/domain/model/rfb/rfbtest"
	modelvnc "github.com/mslxl/natcatty/domain/model/vnc"
)

// webSocketRequest records the HTTP handshake a test server saw.
type webSocketRequest struct {
	URI      string
	Origin   string
	Protocol string
}

type webSocketHarness struct {
	factory  *vnc.Factory
	host     string
	port     int
	server   *rfbtest.Server
	requests chan webSocketRequest
}

// startWebSocket serves one RFB Session through a websockify-shaped upgrade:
// the HTTP handler accepts the WebSocket and then hands websocket.NetConn to
// the same scripted RFB server the TCP tests use.
func startWebSocket(t *testing.T, cfg rfbtest.Config) *webSocketHarness {
	t.Helper()
	h := &webSocketHarness{
		factory:  &vnc.Factory{},
		server:   rfbtest.New(cfg),
		requests: make(chan webSocketRequest, 1),
	}
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case h.requests <- webSocketRequest{
			URI:      r.URL.RequestURI(),
			Origin:   r.Header.Get("Origin"),
			Protocol: r.Header.Get("Sec-WebSocket-Protocol"),
		}:
		default:
		}
		socket, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			Subprotocols: []string{"binary"},
		})
		if err != nil {
			return
		}
		defer socket.Close(websocket.StatusNormalClosure, "")
		conn := websocket.NetConn(r.Context(), socket, websocket.MessageBinary)
		_ = h.server.Serve(conn)
	}))
	t.Cleanup(httpServer.Close)
	h.host, h.port = splitServerAddress(t, httpServer.URL)
	return h
}

// splitServerAddress reads the host and port an httptest server listens on.
func splitServerAddress(t *testing.T, raw string) (string, int) {
	t.Helper()
	address := strings.TrimPrefix(strings.TrimPrefix(raw, "http://"), "https://")
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatalf("split %q: %v", raw, err)
	}
	port, err := net.LookupPort("tcp", portText)
	if err != nil {
		t.Fatalf("parse port %q: %v", portText, err)
	}
	return host, port
}

// spec is a VNC start section that asks for the WebSocket transport.
func (h *webSocketHarness) spec() appsession.StartSpec {
	configuration := modelvnc.DefaultConfig()
	configuration.WebSocket = true
	section := modelvnc.SessionConfig{
		Hostname:      h.host,
		Port:          h.port,
		Configuration: configuration,
	}.Normalized()
	return appsession.StartSpec{Kind: appsession.KindVNC, VNC: &section}
}

// TestWebSocketCarriesTheRFBStream proves the adapter speaks standard RFB
// inside the WebSocket in both directions: it receives a frame and the server
// receives the key the display sent.
func TestWebSocketCarriesTheRFBStream(t *testing.T) {
	h := startWebSocket(t, rfbtest.Config{Frame: testFrame(), Encoding: rfb.EncodingRaw})
	transport, err := h.factory.Open(context.Background(), h.spec())
	if err != nil {
		t.Fatalf("Open over WebSocket: %v", err)
	}
	t.Cleanup(func() { _ = transport.Close() })

	select {
	case request := <-h.requests:
		if request.URI != modelvnc.DefaultWebSocketPath {
			t.Fatalf("request URI = %q, want %q", request.URI, modelvnc.DefaultWebSocketPath)
		}
		if want := "http://" + net.JoinHostPort(h.host, strconv.Itoa(h.port)); request.Origin != want {
			t.Fatalf("Origin = %q, want %q", request.Origin, want)
		}
		if request.Protocol != "binary" {
			t.Fatalf("subprotocol = %q, want binary", request.Protocol)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the WebSocket handshake never reached the server")
	}

	display := transport.(appsession.DisplayProvider).Display()
	nextEvent(t, display.Events(), appsession.DisplayEventFrame)
	if err := display.SendKey(0x61, true); err != nil {
		t.Fatalf("SendKey: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(h.server.Received().Keys) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the server never received the key")
		}
		time.Sleep(time.Millisecond)
	}
	if got := h.server.Received().Keys[0]; got != (rfbtest.KeyEvent{Down: true, Keysym: 0x61}) {
		t.Fatalf("key = %+v, want a press of keysym 0x61", got)
	}
}

// TestWebSocketPathIsSentAsTyped proves a custom path and its query reach the
// server unchanged.
func TestWebSocketPathIsSentAsTyped(t *testing.T) {
	h := startWebSocket(t, rfbtest.Config{Frame: testFrame(), Encoding: rfb.EncodingRaw})
	spec := h.spec()
	spec.VNC.Configuration.WebSocketPath = "/vnc?token=s3cret"
	transport, err := h.factory.Open(context.Background(), spec)
	if err != nil {
		t.Fatalf("Open over WebSocket: %v", err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	select {
	case request := <-h.requests:
		if request.URI != "/vnc?token=s3cret" {
			t.Fatalf("request URI = %q, want the typed path and token", request.URI)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the WebSocket handshake never reached the server")
	}
}

// TestWebSocketRejectsAnEndpointThatIsNotAnUpgrade proves an HTTP page at the
// typed port is reported as a transport failure, not as a VNC handshake.
func TestWebSocketRejectsAnEndpointThatIsNotAnUpgrade(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "this is the noVNC page, not the upgrade path", http.StatusOK)
	}))
	t.Cleanup(server.Close)
	host, port := splitServerAddress(t, server.URL)
	configuration := modelvnc.DefaultConfig()
	configuration.WebSocket = true
	section := modelvnc.SessionConfig{
		Hostname: host, Port: port, Configuration: configuration,
	}.Normalized()
	factory := &vnc.Factory{}
	_, err := factory.Open(context.Background(), appsession.StartSpec{
		Kind: appsession.KindVNC, VNC: &section,
	})
	var typed *appconnection.Error
	if !errors.As(err, &typed) || typed.Kind != appconnection.KindNetwork {
		t.Fatalf("Open error = %v, want a network failure", err)
	}
}

// TestSecureWebSocketVerifiesTheCertificate proves wss runs a real TLS
// handshake against the system trust store: a self-signed test certificate is
// rejected as an authentication failure instead of being skipped.
func TestSecureWebSocketVerifiesTheCertificate(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(server.Close)
	host, port := splitServerAddress(t, server.URL)
	configuration := modelvnc.DefaultConfig()
	configuration.WebSocket = true
	configuration.WebSocketSecure = true
	section := modelvnc.SessionConfig{
		Hostname: host, Port: port, Configuration: configuration,
	}.Normalized()
	factory := &vnc.Factory{}
	_, err := factory.Open(context.Background(), appsession.StartSpec{
		Kind: appsession.KindVNC, VNC: &section,
	})
	var typed *appconnection.Error
	if !errors.As(err, &typed) || typed.Kind != appconnection.KindAuth {
		t.Fatalf("Open error = %v, want an authentication failure", err)
	}
}

// TestCancellingTheConnectContextKeepsTheStream proves Open's context bounds
// only the connection attempt: a request-scoped caller that cancels after Open
// returns must not tear down the live Session.
func TestCancellingTheConnectContextKeepsTheStream(t *testing.T) {
	h := startWebSocket(t, rfbtest.Config{Frame: testFrame(), Encoding: rfb.EncodingRaw})
	ctx, cancel := context.WithCancel(context.Background())
	transport, err := h.factory.Open(ctx, h.spec())
	if err != nil {
		t.Fatalf("Open over WebSocket: %v", err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	cancel()

	display := transport.(appsession.DisplayProvider).Display()
	nextEvent(t, display.Events(), appsession.DisplayEventFrame)
	if err := display.SendKey(0x62, true); err != nil {
		t.Fatalf("SendKey after the connect context was cancelled: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(h.server.Received().Keys) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the server never received the key after the connect context was cancelled")
		}
		time.Sleep(time.Millisecond)
	}
}

// TestWebSocketRefusesARedirect proves a redirecting path is reported as the
// wrong endpoint instead of being followed.
func TestWebSocketRefusesARedirect(t *testing.T) {
	var followed atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		followed.Add(1)
	}))
	t.Cleanup(target.Close)
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/websockify", http.StatusFound)
	}))
	t.Cleanup(redirect.Close)

	host, port := splitServerAddress(t, redirect.URL)
	configuration := modelvnc.DefaultConfig()
	configuration.WebSocket = true
	section := modelvnc.SessionConfig{
		Hostname: host, Port: port, Configuration: configuration,
	}.Normalized()
	factory := &vnc.Factory{}
	_, err := factory.Open(context.Background(), appsession.StartSpec{
		Kind: appsession.KindVNC, VNC: &section,
	})
	var typed *appconnection.Error
	if !errors.As(err, &typed) || typed.Kind != appconnection.KindNetwork {
		t.Fatalf("Open error = %v, want a network failure", err)
	}
	if followed.Load() != 0 {
		t.Fatal("the WebSocket client followed the redirect")
	}
}
