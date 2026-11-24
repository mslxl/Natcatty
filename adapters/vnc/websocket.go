package vnc

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/coder/websocket"

	appconnection "github.com/mslxl/natcatty/application/connection"
	modelvnc "github.com/mslxl/natcatty/domain/model/vnc"
)

// maxWebSocketMessage bounds one inbound WebSocket message. websockify forwards
// the RFB byte stream in small chunks, so this is generous for a framebuffer
// update while still failing closed on a peer that claims a huge message.
const maxWebSocketMessage = 16 << 20

// upgradeWebSocket carries the RFB byte stream inside a WebSocket, the
// transport a noVNC page gets from websockify. The stream has already been
// dialled through the Host's route, so a jump chain, proxy, or Network Profile
// still applies; only the framing changes.
func (f Factory) upgradeWebSocket(ctx context.Context, conn net.Conn, cfg modelvnc.SessionConfig) (net.Conn, error) {
	websocketConfig := cfg.Configuration
	scheme, originScheme := "ws", "http"
	if websocketConfig.WebSocketSecure {
		scheme, originScheme = "wss", "https"
	}
	authority := net.JoinHostPort(cfg.Hostname, strconv.Itoa(cfg.Port))
	path := websocketConfig.WebSocketPath
	if path == "" {
		path = modelvnc.DefaultWebSocketPath
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	// The request rides the connection the Factory already dialled, so the
	// transport must neither dial again nor use an environment proxy.
	transport := &http.Transport{
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			return conn, nil
		},
		ForceAttemptHTTP2: false,
		TLSNextProto:      map[string]func(string, *tls.Conn) http.RoundTripper{},
	}
	if websocketConfig.WebSocketSecure {
		transport.TLSClientConfig = &tls.Config{
			ServerName: cfg.Hostname,
			MinVersion: tls.VersionTLS12,
		}
	}
	header := http.Header{}
	header.Set("Origin", originScheme+"://"+authority)

	handshakeCtx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	socket, _, err := websocket.Dial(handshakeCtx, scheme+"://"+authority+path, &websocket.DialOptions{
		HTTPClient: &http.Client{
			Transport: transport,
			// A websockify path that redirects is a wrong endpoint, not a
			// login page to follow; report the response instead.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		HTTPHeader:   header,
		Subprotocols: []string{"binary"},
	})
	if err != nil {
		_ = conn.Close()
		return nil, classifyWebSocketError(ctx, err)
	}
	// NetConn ties the stream's lifetime to the context it is given. Open's
	// context only bounds the connection attempt (the Session keeps its own
	// lifetime context), so detach it: Close is what ends the stream, and a
	// caller that cancels its connect request must not kill a live Session.
	// NetConn disables the library's read limit, so restore a bound after it:
	// the stream is consumed as an io.Reader, and one unbounded message from a
	// hostile peer must fail closed instead of growing without limit.
	stream := websocket.NetConn(context.WithoutCancel(ctx), socket, websocket.MessageBinary)
	socket.SetReadLimit(maxWebSocketMessage)
	return stream, nil
}

// classifyWebSocketError maps a failed WebSocket handshake onto the UI's
// connection vocabulary. The RFB stream is never reached, so this is a
// transport failure rather than an authentication one, except for a
// certificate the system trust store rejects.
func classifyWebSocketError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return &appconnection.Error{Kind: appconnection.KindCancelled, Cause: ctx.Err()}
	}
	var certificateError *tls.CertificateVerificationError
	if errors.As(err, &certificateError) {
		return &appconnection.Error{Kind: appconnection.KindAuth, Cause: fmt.Errorf(
			"%w: the WebSocket certificate did not verify against the system trust store and the Host name", err)}
	}
	var netError net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netError) && netError.Timeout()) {
		return &appconnection.Error{Kind: appconnection.KindTimeout, Cause: fmt.Errorf(
			"%w: the WebSocket endpoint did not accept the upgrade in time", err)}
	}
	return &appconnection.Error{Kind: appconnection.KindNetwork, Cause: fmt.Errorf(
		"vnc: WebSocket handshake failed: %w", err)}
}
