// Package vnc adapts the RFB engine to the Session display port. It owns the
// TCP connection, the RFB client, and the single display-event stream the
// viewer consumes.
//
// This slice dials the Host directly and performs the baseline None security
// handshake. Jump chains, proxies, Network Profiles, password authentication,
// TLS, and the optional encodings arrive in later tickets. The adapter is
// deliberately free of UI types: frames cross the port as stdlib images and
// input arrives as X11 keysyms and RFB button masks.
package vnc

import (
	"context"
	"errors"
	"fmt"
	"image"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	appconnection "github.com/mslxl/natcatty/application/connection"
	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model/rfb"
	modelvnc "github.com/mslxl/natcatty/domain/model/vnc"
)

const (
	// DefaultConnectTimeout bounds the TCP dial when the Factory does not set
	// one.
	DefaultConnectTimeout = 10 * time.Second
	// handshakeTimeout bounds version negotiation, security, ServerInit, and
	// the initial client setup messages, so a peer that accepts TCP and then
	// goes silent cannot hold Open forever.
	handshakeTimeout = 15 * time.Second
)

// Factory opens a VNC Session over a direct TCP connection. Dial is the seam
// tests replace with an in-process stream; a nil value uses net.Dialer, which
// is the path for a Host with no route.
type Factory struct {
	// Dial opens the TCP connection. A nil value uses net.Dialer.
	Dial func(ctx context.Context, network, address string) (net.Conn, error)
	// ConnectTimeout overrides DefaultConnectTimeout.
	ConnectTimeout time.Duration
}

var _ appsession.Factory = Factory{}

// Open validates the VNC start section, dials the endpoint, performs the RFB
// handshake, and returns the Session transport. A malformed configuration
// fails before any connection is opened.
func (f Factory) Open(ctx context.Context, spec appsession.StartSpec) (appsession.Transport, error) {
	if err := ctx.Err(); err != nil {
		return nil, &appconnection.Error{Kind: appconnection.KindCancelled, Cause: err}
	}
	if spec.VNC == nil {
		return nil, &modelvnc.ValidationError{Field: "vnc", Reason: "is required"}
	}
	cfg := spec.VNC.Normalized()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	timeout := f.ConnectTimeout
	if timeout <= 0 {
		timeout = DefaultConnectTimeout
	}
	address := net.JoinHostPort(cfg.Hostname, strconv.Itoa(cfg.Port))
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	conn, err := f.dial(dialCtx, "tcp", address)
	dialCtxErr := dialCtx.Err()
	cancel()
	if err != nil {
		return nil, classifyDialError(ctx, dialCtxErr, address, err)
	}
	if ctx.Err() != nil {
		_ = conn.Close()
		return nil, &appconnection.Error{Kind: appconnection.KindCancelled, Cause: ctx.Err()}
	}
	// Bound the handshake; the deadline applies to the whole exchange below.
	if deadline, ok := conn.(interface{ SetDeadline(time.Time) error }); ok {
		_ = deadline.SetDeadline(time.Now().Add(handshakeTimeout))
	}
	client, err := rfb.Connect(conn, rfb.ClientConfig{Shared: cfg.Configuration.Shared})
	if deadline, ok := conn.(interface{ SetDeadline(time.Time) error }); ok {
		_ = deadline.SetDeadline(time.Time{})
	}
	if err != nil {
		_ = conn.Close()
		return nil, classifyHandshakeError(ctx, err)
	}
	return newTransport(conn, client), nil
}

func (f Factory) dial(ctx context.Context, network, address string) (net.Conn, error) {
	if f.Dial != nil {
		return f.Dial(ctx, network, address)
	}
	var dialer net.Dialer
	return dialer.DialContext(ctx, network, address)
}

// classifyDialError maps a direct dial failure onto the UI's connection
// vocabulary. A typed failure from an injected seam keeps its classification.
func classifyDialError(ctx context.Context, dialCtxErr error, address string, err error) error {
	var typed *appconnection.Error
	if errors.As(err, &typed) {
		return err
	}
	if ctx.Err() != nil {
		return &appconnection.Error{Kind: appconnection.KindCancelled, Cause: ctx.Err()}
	}
	var netErr net.Error
	if errors.Is(dialCtxErr, context.DeadlineExceeded) ||
		errors.Is(err, context.DeadlineExceeded) ||
		(errors.As(err, &netErr) && netErr.Timeout()) {
		return &appconnection.Error{
			Kind:  appconnection.KindTimeout,
			Cause: fmt.Errorf("%w: %s", appconnection.ErrDialTimeout, address),
		}
	}
	return &appconnection.Error{Kind: appconnection.KindNetwork, Cause: err}
}

// classifyHandshakeError distinguishes a refused credential from an endpoint
// the client cannot speak to. None is the only supported type in this slice, so
// a server that offers anything else fails as unsupported rather than as a
// rejected password.
func classifyHandshakeError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return &appconnection.Error{Kind: appconnection.KindCancelled, Cause: ctx.Err()}
	}
	var security *rfb.SecurityError
	if errors.As(err, &security) {
		kind := appconnection.KindUnsupported
		if security.Rejected {
			kind = appconnection.KindAuth
		}
		return &appconnection.Error{Kind: kind, Cause: err}
	}
	if errors.Is(err, rfb.ErrUnsupportedVersion) || errors.Is(err, rfb.ErrUnsupportedSecurity) {
		return &appconnection.Error{Kind: appconnection.KindUnsupported, Cause: err}
	}
	return &appconnection.Error{Kind: appconnection.KindNetwork, Cause: err}
}

// transport adapts one RFB connection to both the Session transport port and
// the display port. The byte-stream methods exist only to satisfy Transport; a
// VNC Session never calls them.
type transport struct {
	conn   net.Conn
	client *rfb.Client
	// emitter coalesces frames and closes the one event channel exactly once.
	emitter *appsession.DisplayEmitter

	// writeMu serializes every client message. The RFB engine owns no lock and
	// the viewer may send from any goroutine.
	writeMu sync.Mutex

	closed atomic.Bool

	endMu sync.Mutex
	ended bool
	end   appsession.End
	close sync.Once
	err   error
}

var (
	_ appsession.Transport       = (*transport)(nil)
	_ appsession.EndReporter     = (*transport)(nil)
	_ appsession.DisplayProvider = (*transport)(nil)
	_ appsession.DisplaySession  = (*transport)(nil)
)

func newTransport(conn net.Conn, client *rfb.Client) *transport {
	t := &transport{conn: conn, client: client, emitter: appsession.NewDisplayEmitter()}
	info := client.ServerInfo()
	// The desktop name and size are known before the first update. Publishing
	// them now means the Tab title is right as soon as the Session is visible.
	t.emitter.Publish(appsession.DisplayEvent{Kind: appsession.DisplayEventDesktopName, Text: info.DesktopName})
	t.emitter.Publish(appsession.DisplayEvent{Kind: appsession.DisplayEventResize,
		Width: int(info.Width), Height: int(info.Height)})
	go t.readLoop()
	// Ask for the first frame immediately; every delivered update requests the
	// next incremental one.
	if err := t.RequestUpdate(false); err != nil {
		t.fail(err)
	}
	return t
}

// Display returns the adapter's display session. The transport owns its
// lifetime and closes it exactly once.
func (t *transport) Display() appsession.DisplaySession { return t }

// Events returns the single display-event stream.
func (t *transport) Events() <-chan appsession.DisplayEvent { return t.emitter.Events() }

// readLoop decodes server messages until the stream ends. It is the only
// reader of the connection.
func (t *transport) readLoop() {
	for {
		message, err := t.client.ReadMessage()
		if err != nil {
			if t.closed.Load() {
				t.remember(appsession.End{Reason: appsession.EndReasonClosed})
			} else {
				t.remember(appsession.End{Reason: appsession.EndReasonError})
				t.emitter.Publish(appsession.DisplayEvent{Kind: appsession.DisplayEventError, Err: err})
			}
			t.emitter.Close()
			return
		}
		switch message.Kind {
		case rfb.MessageFramebufferUpdate:
			if frame := appsession.NewDisplayFrame(t.client.Frame().Image(), updateRect(message.Rectangles)); frame != nil {
				t.emitter.Publish(appsession.DisplayEvent{Kind: appsession.DisplayEventFrame, Frame: frame})
			}
			if err := t.RequestUpdate(true); err != nil {
				t.fail(err)
				return
			}
		case rfb.MessageBell:
			t.emitter.Publish(appsession.DisplayEvent{Kind: appsession.DisplayEventBell})
		case rfb.MessageCutText:
			t.emitter.Publish(appsession.DisplayEvent{Kind: appsession.DisplayEventCutText, Text: message.CutText})
		}
	}
}

// fail ends the display after a write or protocol failure.
func (t *transport) fail(err error) {
	t.remember(appsession.End{Reason: appsession.EndReasonError})
	t.emitter.Publish(appsession.DisplayEvent{Kind: appsession.DisplayEventError, Err: err})
	t.emitter.Close()
}

// updateRect returns the bounding rectangle of one update. An empty update
// covers the whole framebuffer.
func updateRect(rectangles []rfb.Rectangle) image.Rectangle {
	if len(rectangles) == 0 {
		return image.Rectangle{}
	}
	union := image.Rect(
		int(rectangles[0].X), int(rectangles[0].Y),
		int(rectangles[0].X)+int(rectangles[0].Width),
		int(rectangles[0].Y)+int(rectangles[0].Height),
	)
	for _, rect := range rectangles[1:] {
		union = union.Union(image.Rect(
			int(rect.X), int(rect.Y),
			int(rect.X)+int(rect.Width), int(rect.Y)+int(rect.Height),
		))
	}
	return union
}

// SendKey sends one X11 keysym press or release.
func (t *transport) SendKey(keysym uint32, pressed bool) error {
	return t.write(func() error { return t.client.SendKey(keysym, pressed) })
}

// SendPointer sends a pointer position and the RFB button mask. Coordinates
// outside the framebuffer are clamped, as a server would do. The bounds are
// immutable, so this read is safe alongside the reader goroutine.
func (t *transport) SendPointer(x, y int, buttonMask uint8) error {
	bounds := t.client.Frame().Bounds()
	x = min(max(x, 0), bounds.Dx()-1)
	y = min(max(y, 0), bounds.Dy()-1)
	return t.write(func() error { return t.client.SendPointer(buttonMask, uint16(x), uint16(y)) })
}

// SendCutText sends clipboard text to the server.
func (t *transport) SendCutText(text string) error {
	return t.write(func() error { return t.client.SendCutText(text) })
}

// RequestUpdate asks for the whole framebuffer.
func (t *transport) RequestUpdate(incremental bool) error {
	return t.write(func() error { return t.client.RequestFullUpdate(incremental) })
}

// SetDesktopSize reports that this slice did not negotiate the extension.
func (t *transport) SetDesktopSize(width, height int) error {
	if t.closed.Load() {
		return appsession.ErrNoDisplay
	}
	return appsession.ErrDesktopResizeUnsupported
}

// write serializes one client message and rejects sends after Close.
func (t *transport) write(send func() error) error {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	if t.closed.Load() {
		return appsession.ErrNoDisplay
	}
	return send()
}

// Read reports that a VNC Session has no byte stream.
func (t *transport) Read([]byte) (int, error) { return 0, appsession.ErrNoByteStream }

// Write reports that a VNC Session has no byte stream.
func (t *transport) Write([]byte) (int, error) { return 0, appsession.ErrNoByteStream }

// Resize reports that a VNC Session has no terminal grid to resize.
func (t *transport) Resize(int, int) error { return appsession.ErrNoByteStream }

// End classifies why the transport stopped.
func (t *transport) End() appsession.End {
	t.endMu.Lock()
	defer t.endMu.Unlock()
	if t.ended {
		return t.end
	}
	if t.closed.Load() {
		return appsession.End{Reason: appsession.EndReasonClosed}
	}
	return appsession.End{Reason: appsession.EndReasonError}
}

func (t *transport) remember(end appsession.End) {
	t.endMu.Lock()
	if !t.ended {
		t.ended = true
		t.end = end
	}
	t.endMu.Unlock()
}

// Close hangs up the connection and closes the display event stream exactly
// once. Sends after Close report ErrNoDisplay.
func (t *transport) Close() error {
	t.close.Do(func() {
		t.closed.Store(true)
		err := t.conn.Close()
		t.emitter.Close()
		t.remember(appsession.End{Reason: appsession.EndReasonClosed})
		t.endMu.Lock()
		t.err = err
		t.endMu.Unlock()
	})
	t.endMu.Lock()
	defer t.endMu.Unlock()
	return t.err
}
