// Package vnc adapts the RFB engine to the Session display port. It owns the
// TCP connection, the RFB client, and the single display-event stream the
// viewer consumes.
//
// The Factory dials whichever stream the app hands it and negotiates the
// strongest security type both sides support: the RealVNC RA2 family, VeNCrypt
// (Plain, TLSPlain, X509Plain, TLSVnc, X509Vnc), Apple's Diffie-Hellman type,
// the VNC password challenge, or None. X509 certificates are verified against
// the system trust store and the Host name; no configuration skips that check.
// Jump chains, proxies, and Network Profiles are resolved by the app: it
// supplies a routed dial seam for a jump or proxy, a Provider-backed dial seam
// for a Network Profile, and the plain direct dialer otherwise. A routed stream
// prefers a security type that does not add a second TLS layer. The adapter is
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
	// WriteQueueCapacity bounds the ordered writer's pending messages. A server
	// that floods fences or requests can no longer grow memory without bound:
	// the adapter fails closed with ErrWriteQueueFull instead.
	WriteQueueCapacity = 256
	// InboundHandoffCapacity bounds the reader-to-processing handoff. The bound
	// lets the reader keep draining a pipelined server while the processing loop
	// waits on a blocking fence reply; beyond it the reader fails closed with
	// ErrInboundQueueFull rather than blocking forever (a server that never
	// reads client replies could otherwise deadlock an unbuffered stream).
	InboundHandoffCapacity = 32
	// fenceProbeWriteTimeout bounds the one-shot client-initiated fence request.
	// It only proves the extension is live, so a peer that stops reading after
	// its own fence reply must not hold the ordered writer indefinitely.
	fenceProbeWriteTimeout = 5 * time.Second
)

// ErrWriteQueueFull reports that the bounded outbound writer queue overflowed.
// The adapter ends the session (and closes the connection so a stuck write
// cannot deadlock the writer) instead of growing the queue without bound.
var ErrWriteQueueFull = errors.New("vnc: outbound write queue is full")

// ErrInboundQueueFull reports that the server produced messages faster than
// the display processing loop consumed them, beyond the reader's fixed
// handoff bound. The adapter ends the session rather than dropping a message
// silently or blocking the reader forever.
var ErrInboundQueueFull = errors.New("vnc: inbound message queue is full")

// Factory opens a VNC Session over one TCP connection. Dial is the seam the
// app backs with a routed stream, a Network Provider, or the plain dialer; a
// nil value uses net.Dialer, which is the path for a Host with no route.
type Factory struct {
	// Dial opens the TCP connection. A nil value uses net.Dialer.
	Dial func(ctx context.Context, network, address string) (net.Conn, error)
	// ConnectTimeout overrides DefaultConnectTimeout.
	ConnectTimeout time.Duration
	// Routed reports that the dialled stream is already encrypted by a tunnel.
	// The app sets it from the Host's resolved route (a jump chain, proxy, or
	// Network Profile) before Open. The adapter never derives it from the
	// protocol or from the dial seam, so a routed session can suppress a second
	// TLS layer and the plaintext warning. A direct Host leaves it false.
	Routed bool
	// SecurityGate is consulted after security negotiation and before any
	// credential is sent or TLS handshake starts. It receives the effective
	// plaintext decision of the negotiated type (including the VeNCrypt
	// subtype). Returning an error aborts Open. A nil gate accepts everything.
	SecurityGate func(ctx context.Context, plaintext bool) error
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
	routed := f.Routed
	if cfg.Configuration.WebSocket {
		conn, err = f.upgradeWebSocket(ctx, conn, cfg)
		if err != nil {
			return nil, err
		}
		// A wss stream is already encrypted, so the RFB security negotiation
		// must not add a second TLS layer or warn about plaintext.
		routed = routed || cfg.Configuration.WebSocketSecure
	}
	// Bound the handshake; the deadline applies to the whole exchange below.
	if deadline, ok := conn.(interface{ SetDeadline(time.Time) error }); ok {
		_ = deadline.SetDeadline(time.Now().Add(handshakeTimeout))
	}
	// A one-shot credential on the start spec is the fallback for a target
	// with no saved Host; otherwise the app resolves the Host's VNC secret
	// into the section before Open.
	password := cfg.Password
	if password == "" {
		password = spec.Password
	}
	username := cfg.Username
	if username == "" {
		username = spec.Username
	}
	client, err := rfb.Connect(conn, rfb.ClientConfig{
		Shared:        cfg.Configuration.Shared,
		Password:      password,
		Username:      username,
		ServerName:    cfg.Hostname,
		Routed:        routed,
		Context:       ctx,
		KeygenTimeout: handshakeTimeout,
		SecurityGate: func(choice rfb.SecurityChoice) error {
			if f.SecurityGate == nil {
				return nil
			}
			// A user may take longer than the handshake deadline to answer a
			// plaintext confirmation, so suspend the deadline while the gate
			// runs and restore it afterwards.
			if deadline, ok := conn.(interface{ SetDeadline(time.Time) error }); ok {
				_ = deadline.SetDeadline(time.Time{})
			}
			err := f.SecurityGate(ctx, choice.Plaintext())
			if deadline, ok := conn.(interface{ SetDeadline(time.Time) error }); ok {
				_ = deadline.SetDeadline(time.Now().Add(handshakeTimeout))
			}
			return err
		},
	})
	if deadline, ok := conn.(interface{ SetDeadline(time.Time) error }); ok {
		_ = deadline.SetDeadline(time.Time{})
	}
	if err != nil {
		_ = conn.Close()
		return nil, classifyHandshakeError(ctx, err)
	}
	return newTransport(conn, client, routed), nil
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
// the client cannot speak to. A missing or rejected password is KindAuth (and
// therefore permanent, so the Session never retries it). A server that offers
// only unimplemented security types is KindUnsupported with the SSH-route
// suggestion the VNC specification calls for.
func classifyHandshakeError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return &appconnection.Error{Kind: appconnection.KindCancelled, Cause: ctx.Err()}
	}
	// A caller-declined plaintext session is the caller's own error, not a
	// transport classification. Preserve it so errors.Is still finds the
	// prompt's decision.
	if errors.Is(err, rfb.ErrSecurityDeclined) {
		return err
	}
	if errors.Is(err, rfb.ErrTLSVerification) {
		return &appconnection.Error{Kind: appconnection.KindAuth, Cause: fmt.Errorf(
			"%w: the VNC server certificate did not verify against the system trust store and the Host name", err)}
	}
	if errors.Is(err, rfb.ErrMissingPassword) {
		return &appconnection.Error{Kind: appconnection.KindAuth, Cause: fmt.Errorf(
			"%w: the server requires a VNC password; add it in the Host editor's VNC Password field", err)}
	}
	var security *rfb.SecurityError
	if errors.As(err, &security) {
		if security.Rejected {
			return &appconnection.Error{Kind: appconnection.KindAuth, Cause: fmt.Errorf(
				"vnc: authentication failed: %w; check the saved VNC password", err)}
		}
		return &appconnection.Error{Kind: appconnection.KindUnsupported, Cause: fmt.Errorf(
			"%w: the server offers no security type this client implements; use an SSH route to encrypt the stream", err)}
	}
	if errors.Is(err, rfb.ErrUnsupportedVersion) {
		return &appconnection.Error{Kind: appconnection.KindUnsupported, Cause: err}
	}
	if errors.Is(err, rfb.ErrUnsupportedSecurity) {
		return &appconnection.Error{Kind: appconnection.KindUnsupported, Cause: fmt.Errorf(
			"%w: the server offers no security type this client implements; use an SSH route to encrypt the stream", err)}
	}
	return &appconnection.Error{Kind: appconnection.KindNetwork, Cause: err}
}

// transport adapts one RFB connection to both the Session transport port and
// the display port. The byte-stream methods exist only to satisfy Transport; a
// VNC Session never calls them.
type transport struct {
	conn   net.Conn
	client *rfb.Client
	// routed is the app's tunnel decision, carried unchanged from the Factory.
	// The adapter must not infer it from the protocol state.
	routed bool
	// emitter coalesces frames and closes the one event channel exactly once.
	emitter *appsession.DisplayEmitter

	// writes is the single ordered writer. Every client message, including
	// fence replies, passes through it, so the wire order is the queue order
	// rather than whichever goroutine grabbed a mutex first.
	writes *writeQueue
	// done is closed by Close, unblocking writers and resize waiters.
	done chan struct{}

	// Negotiated extensions. The reader sets these as the server proves
	// support; SetDesktopSize reads resizeSupport from the caller's goroutine,
	// so it is atomic.
	resizeSupport     atomic.Bool
	continuousEnabled atomic.Bool
	requestPending    atomic.Bool
	continuousOnce    sync.Once
	fenceRequestOnce  sync.Once

	// layoutMu guards the last ExtendedDesktopSize layout. The reader
	// remembers it; SetDesktopSize reads it from the Surface goroutine.
	layoutMu sync.Mutex
	layout   desktopLayout
	// resizeMu guards the pending client-initiated resize: the reply channel,
	// the epoch and dimensions that a matching reply must carry, and the
	// bounded set of timed-out requests whose late replies must not answer a
	// newer request.
	resizeMu           sync.Mutex
	resizeReply        chan resizeResult
	resizeWidth        uint16
	resizeHeight       uint16
	resizePendingEpoch uint64
	resizeEpoch        uint64
	staleResizes       []staleResize

	// readerDone closes when the inner decode goroutine exits, so a teardown
	// can join it deterministically. loopDone closes when readLoop returns.
	readerDone chan struct{}
	loopDone   chan struct{}

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

func newTransport(conn net.Conn, client *rfb.Client, routed bool) *transport {
	t := &transport{
		conn:       conn,
		client:     client,
		routed:     routed,
		emitter:    appsession.NewDisplayEmitter(),
		done:       make(chan struct{}),
		readerDone: make(chan struct{}),
		loopDone:   make(chan struct{}),
	}
	// An outbound overflow is terminal: publish the typed error and close the
	// connection so the writer (possibly stuck in a blocked write) unwinds.
	t.writes = newWriteQueue(WriteQueueCapacity, func(err error) {
		t.fail(err)
		_ = t.Close()
	})
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

// Routed reports the app's decision that the stream is already encrypted by a
// tunnel. It is the value the Factory received, never a protocol inference.
func (t *transport) Routed() bool { return t.routed }

// Security returns the security type the RFB handshake negotiated.
func (t *transport) Security() rfb.SecurityType { return t.client.Security() }

// Plaintext reports whether the negotiated security type leaves the RFB wire
// unencrypted. It is independent of Routed: a routed session is encrypted by
// the tunnel even when the inner type is plaintext. The decision is
// subtype-aware for VeNCrypt, so an X509 or TLS subtype is never reported as
// plaintext even though the container type alone cannot say.
func (t *transport) Plaintext() bool { return negotiatedPlaintext(t.client.SecurityChoice()) }

// negotiatedPlaintext is the plaintext decision isolated from the connection.
func negotiatedPlaintext(choice rfb.SecurityChoice) bool { return choice.Plaintext() }

// Events returns the single display-event stream.
func (t *transport) Events() <-chan appsession.DisplayEvent { return t.emitter.Events() }

// inboundMessage is one decoded server message plus the immutable frame
// snapshot taken while the reader still owns the decoder. Copying here keeps
// the processing goroutine off the live framebuffer.
type inboundMessage struct {
	message rfb.ServerMessage
	frame   *appsession.DisplayFrame
}

// readLoop decodes server messages until the stream ends. A dedicated reader
// goroutine is the only ReadMessage caller and the processing loop below
// consumes its messages in order. The split lets the reader keep draining the
// server while a blocking fence reply is in flight, so a peer that pipelines
// an update cannot deadlock against the reply write. The reader also copies
// the frame, because only it may touch the decoder's pixels.
func (t *transport) readLoop() {
	defer close(t.loopDone)
	// The buffer lets the reader drain a server's pipelined writes while the
	// processing loop waits for a blocking fence reply. Without it, a server
	// that sends an update before reading the reply would deadlock on an
	// unbuffered stream. It is bounded because a buffered pixel update carries
	// a full-frame copy: once the processing loop falls InboundHandoffCapacity
	// messages behind, the reader fails the session with ErrInboundQueueFull
	// rather than blocking forever or discarding a message silently. Failing
	// closed closes the connection, which unblocks any in-flight write.
	messages := make(chan inboundMessage, InboundHandoffCapacity)
	failures := make(chan error, 1)
	stop := make(chan struct{})
	go func() {
		defer close(t.readerDone)
		defer close(messages)
		for {
			message, err := t.client.ReadMessage()
			if err != nil {
				failures <- err
				return
			}
			inbound := inboundMessage{message: message, frame: t.snapshot(message)}
			select {
			case messages <- inbound:
			case <-stop:
				return
			default:
				t.fail(ErrInboundQueueFull)
				_ = t.Close()
				failures <- ErrInboundQueueFull
				return
			}
		}
	}()

	for inbound := range messages {
		if err := t.processMessage(inbound); err != nil {
			// A processing error is terminal. Mirror the overflow path: close
			// the connection so a reader blocked in ReadMessage (or a writer
			// stuck on a send) unwinds, then join the reader before returning.
			// Without this the reader goroutine would outlive the session.
			close(stop)
			t.finish(err)
			_ = t.Close()
			<-t.readerDone
			return
		}
	}
	err := <-failures
	t.finish(err)
}

// snapshot copies the framebuffer for a message that changed pixels. It runs
// on the reader goroutine, the only one allowed to read the decoder's buffer.
func (t *transport) snapshot(message rfb.ServerMessage) *appsession.DisplayFrame {
	if message.Kind != rfb.MessageFramebufferUpdate {
		return nil
	}
	changed := false
	for _, rect := range message.Rectangles {
		if isPixelRectangle(rect) {
			changed = true
			break
		}
	}
	if !changed {
		return nil
	}
	return appsession.NewDisplayFrame(t.client.Frame().Image(), updateRect(message.Rectangles))
}

// processMessage applies one decoded server message. It runs on the single
// processing goroutine; every write it starts goes through the ordered writer.
func (t *transport) processMessage(inbound inboundMessage) error {
	message := inbound.message
	switch message.Kind {
	case rfb.MessageFramebufferUpdate:
		t.publishMetadata(message.Rectangles)
		if inbound.frame != nil {
			t.emitter.Publish(appsession.DisplayEvent{Kind: appsession.DisplayEventFrame, Frame: inbound.frame})
		}
		// Continuous updates replace polling: the server keeps the stream
		// flowing once enabled, so the client must stop asking. The request is
		// queued without waiting so a server that writes back-to-back updates
		// can never deadlock with this processing loop.
		if !t.continuousEnabled.Load() && t.requestPending.CompareAndSwap(false, true) {
			t.pollUpdate()
		}
	case rfb.MessageBell:
		t.emitter.Publish(appsession.DisplayEvent{Kind: appsession.DisplayEventBell})
	case rfb.MessageCutText:
		t.emitter.Publish(appsession.DisplayEvent{Kind: appsession.DisplayEventCutText, Text: message.CutText})
	case rfb.MessageFence:
		// Answer inline. The reply goes through the ordered writer and this
		// loop waits for it, so a BlockBefore/BlockAfter fence is honored
		// before any later server message is processed. There is never more
		// than one outstanding fence reply and no goroutine is created.
		if err := t.handleFence(message.Fence); err != nil {
			return err
		}
	case rfb.MessageEndOfContinuousUpdates:
		t.continuousEnabled.Store(true)
		t.enableContinuousUpdates()
	}
	return nil
}

// finish publishes the terminal state and closes the event stream.
func (t *transport) finish(err error) {
	if t.closed.Load() {
		t.remember(appsession.End{Reason: appsession.EndReasonClosed})
	} else {
		t.remember(appsession.End{Reason: appsession.EndReasonError})
		t.emitter.Publish(appsession.DisplayEvent{Kind: appsession.DisplayEventError, Err: err})
	}
	t.emitter.Close()
}

// pollUpdate asks for the next incremental update and clears the coalescing
// flag. At most one request is outstanding; the next delivered update asks
// again.
func (t *transport) pollUpdate() {
	t.writeAsync(func() error {
		err := t.client.RequestFullUpdate(true)
		t.requestPending.Store(false)
		return err
	})
}

// publishMetadata consumes the pseudo-rectangles in one update: it publishes
// resize and cursor events. Pixel rectangles need no handling here; the reader
// has already snapshotted the frame.
func (t *transport) publishMetadata(rectangles []rfb.Rectangle) {
	for _, rect := range rectangles {
		switch {
		case rect.Extended != nil:
			t.resizeSupport.Store(true)
			t.rememberLayout(rect.Extended)
			if rect.Extended.Reason == 1 {
				// A reasoned reply belongs to our outstanding request.
				t.deliverResizeReply(rect.Extended)
			}
			if rect.Extended.Status == 0 && rect.Extended.Width > 0 && rect.Extended.Height > 0 {
				t.emitter.Publish(appsession.DisplayEvent{
					Kind:   appsession.DisplayEventResize,
					Width:  int(rect.Extended.Width),
					Height: int(rect.Extended.Height),
				})
				// Continuous updates are scoped to a region. Re-scope them to
				// the new extents when the extension is live; on a server
				// without it, ask for a non-incremental repaint of the new
				// size so the added area is never left stale.
				if t.continuousEnabled.Load() {
					t.refreshContinuousRegion(int(rect.Extended.Width), int(rect.Extended.Height))
				} else {
					t.requestFullUpdate(int(rect.Extended.Width), int(rect.Extended.Height))
				}
			}
		case rect.Cursor != nil:
			shape := appsession.NewCursorShape(rect.Cursor.Image, rect.Cursor.Hotspot)
			t.emitter.Publish(appsession.DisplayEvent{
				Kind:   appsession.DisplayEventCursorShape,
				Cursor: shape,
			})
		case rect.Encoding == rfb.EncodingFence || rect.Encoding == rfb.EncodingContinuousUpdates ||
			rect.Encoding == rfb.EncodingExtendedKeyEvent:
			// Extension acknowledgements carry no pixels. The RFB client
			// records the ExtendedKeyEvent acknowledgement itself, because it
			// is the party that chooses the wire format for later keys.
		}
	}
}

// enableContinuousUpdates acknowledges the extension with one enable message
// for the whole framebuffer. It runs at most once, because the adapter only
// needs server-driven updates from then on.
func (t *transport) enableContinuousUpdates() {
	t.continuousOnce.Do(func() {
		bounds := t.client.Frame().Bounds()
		t.writeAsync(func() error {
			return t.client.SendContinuousUpdates(true, 0, 0, uint16(bounds.Dx()), uint16(bounds.Dy()))
		})
	})
}

// refreshContinuousRegion re-enables continuous updates for a new framebuffer
// extent. The extent comes from the ExtendedDesktopSize rectangle, not a
// concurrent Bounds read.
func (t *transport) refreshContinuousRegion(width, height int) {
	if !t.continuousEnabled.Load() || width <= 0 || height <= 0 {
		return
	}
	t.writeAsync(func() error {
		return t.client.SendContinuousUpdates(true, 0, 0, uint16(width), uint16(height))
	})
}

// requestFullUpdate asks for a non-incremental repaint of one desktop size.
// The size is the new ExtendedDesktopSize extent, so it cannot race with a
// concurrent resize read.
func (t *transport) requestFullUpdate(width, height int) {
	if width <= 0 || height <= 0 || width > 0xffff || height > 0xffff {
		return
	}
	t.writeAsync(func() error {
		return t.client.RequestUpdate(false, 0, 0, uint16(width), uint16(height))
	})
}

// understoodFenceFlags is the set of Fence flags this client implements. A
// reply echoes only these bits; FenceRequest is deliberately absent because a
// reply is not a request, and unknown server bits are dropped.
const understoodFenceFlags = rfb.FenceBlockBefore | rfb.FenceBlockAfter | rfb.FenceSyncNext

// handleFence answers a server request and sends one client request of its own
// so a successful round trip proves the extension is live. A fence that sets
// BlockBefore or BlockAfter is written synchronously, so the processing loop
// does not consume a later server message before the reply is on the wire. A
// non-blocking fence is queued and the loop keeps draining, so a peer that
// floods fences cannot deadlock or accumulate goroutines. The one-shot client
// request is bounded: it is only a liveness probe, so a peer that stops
// reading cannot stall the ordered writer behind it.
func (t *transport) handleFence(fence rfb.Fence) error {
	if fence.Request() {
		// Echo only the flags this client understands. The Request bit is not
		// part of the mask, so this both clears it and drops unknown high bits
		// the server may have set, as the RFB Fence extension requires.
		response := rfb.Fence{
			Flags:   fence.Flags & understoodFenceFlags,
			Payload: append([]byte(nil), fence.Payload...),
		}
		if fence.Flags&(rfb.FenceBlockBefore|rfb.FenceBlockAfter) != 0 {
			if err := t.write(func() error { return t.client.SendFence(response.Flags, response.Payload) }); err != nil {
				return err
			}
		} else {
			t.writeAsync(func() error { return t.client.SendFence(response.Flags, response.Payload) })
		}
	}
	// The client-initiated request is asynchronous: its BlockBefore flag
	// constrains the server, and waiting for it here would let a flooding
	// server stall the processing loop. It is also bounded, because it is only
	// a liveness probe: a server that stops reading after replying must not
	// leave the single writer blocked forever on an unbuffered pipe and stall
	// every later client message.
	t.fenceRequestOnce.Do(func() {
		request := rfb.Fence{Flags: rfb.FenceRequest | rfb.FenceBlockBefore, Payload: []byte("natcatty")}
		t.writeAsyncBounded(fenceProbeWriteTimeout, func() error {
			return t.client.SendFence(request.Flags, request.Payload)
		})
	})
	return nil
}

// fail ends the display after a write or protocol failure.
func (t *transport) fail(err error) {
	t.remember(appsession.End{Reason: appsession.EndReasonError})
	t.emitter.Publish(appsession.DisplayEvent{Kind: appsession.DisplayEventError, Err: err})
	t.emitter.Close()
}

// isPixelRectangle reports that rect actually changed framebuffer pixels.
// Metadata pseudo-rectangles have their own framing and never touch the image.
func isPixelRectangle(rect rfb.Rectangle) bool {
	if rect.Extended != nil || rect.Cursor != nil {
		return false
	}
	switch rect.Encoding {
	case rfb.EncodingRaw, rfb.EncodingCopyRect, rfb.EncodingRRE, rfb.EncodingHextile, rfb.EncodingZRLE:
		return true
	default:
		return false
	}
}

// updateRect returns the bounding rectangle of one update's pixel changes. The
// metadata pseudo-rectangles do not name framebuffer regions, so they are
// excluded; an update with no pixel rectangle yields an empty rectangle.
func updateRect(rectangles []rfb.Rectangle) image.Rectangle {
	var union image.Rectangle
	have := false
	for _, rect := range rectangles {
		if !isPixelRectangle(rect) {
			continue
		}
		region := image.Rect(
			int(rect.X), int(rect.Y),
			int(rect.X)+int(rect.Width), int(rect.Y)+int(rect.Height),
		)
		if !have {
			union, have = region, true
			continue
		}
		union = union.Union(region)
	}
	return union
}

// SendKey sends one X11 keysym press or release.
func (t *transport) SendKey(keysym uint32, pressed bool) error {
	return t.write(func() error { return t.client.SendKey(keysym, pressed) })
}

// SendPhysicalKey sends one key with its QEMU key number when known. The RFB
// client chooses an Extended Key Event when the server acknowledged the
// pseudo-encoding and falls back to a plain KeyEvent otherwise.
func (t *transport) SendPhysicalKey(keysym uint32, keyNumber uint32, pressed bool) error {
	return t.write(func() error { return t.client.SendPhysicalKey(keysym, keyNumber, pressed) })
}

// SendPointer sends a pointer position and the RFB button mask. Coordinates
// outside the framebuffer are clamped, as a server would do. The bounds read
// is race-safe against an ExtendedDesktopSize resize.
func (t *transport) SendPointer(x, y int, buttonMask uint8) error {
	bounds := t.client.Frame().Bounds()
	if bounds.Dx() <= 0 || bounds.Dy() <= 0 {
		return nil
	}
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

// desktopResizeTimeout bounds how long SetDesktopSize waits for the server's
// ExtendedDesktopSize reply. It is a variable so a test can shorten it.
var desktopResizeTimeout = 5 * time.Second

// staleResizeWindow is how long a timed-out request can still explain a late
// nonzero ExtendedDesktopSize reply. A shorter window would let a very late
// refusal latch a newer request as refused; the window only needs to exceed
// the server's plausible reply latency.
var staleResizeWindow = 30 * time.Second

// maxStaleResizes bounds the timed-out request set. A pathological caller
// cannot grow it without bound; the oldest entry is dropped first.
const maxStaleResizes = 16

// resizeResult is one outcome delivered to a waiting SetDesktopSize. A timed
// out true result is used when a stale reply makes the answer ambiguous: the
// caller must not treat it as a refusal, because that would latch the local
// scaling fallback for a server that may have resized.
type resizeResult struct {
	extended *rfb.ExtendedDesktopSize
	timeout  bool
}

// staleResize is a timed-out SetDesktopSize request. Its epoch and dimensions
// identify the reply that may still arrive for it.
type staleResize struct {
	epoch   uint64
	width   uint16
	height  uint16
	expires time.Time
}

// SetDesktopSize asks the server to match the pane's size and waits for the
// server's ExtendedDesktopSize reply. ExtendedDesktopSize must have been
// negotiated first, which the client learns from the server's initial full
// update; until then the pane keeps its local scaling. The method runs on the
// Surface goroutine, never on the processing goroutine, so waiting does not
// stall reads.
func (t *transport) SetDesktopSize(width, height int) error {
	if t.closed.Load() {
		return appsession.ErrNoDisplay
	}
	if width <= 0 || height <= 0 || width > 0xffff || height > 0xffff {
		return fmt.Errorf("vnc: invalid desktop size %dx%d", width, height)
	}
	if !t.resizeSupport.Load() {
		return appsession.ErrDesktopResizeUnsupported
	}
	screens := t.desktopScreens(uint16(width), uint16(height))

	reply := make(chan resizeResult, 1)
	t.resizeMu.Lock()
	if t.resizeReply != nil {
		t.resizeMu.Unlock()
		return fmt.Errorf("vnc: a desktop resize is already in progress")
	}
	t.resizeEpoch++
	epoch := t.resizeEpoch
	t.resizeReply = reply
	t.resizeWidth = uint16(width)
	t.resizeHeight = uint16(height)
	t.resizePendingEpoch = epoch
	t.resizeMu.Unlock()

	if err := t.write(func() error {
		return t.client.SendSetDesktopSize(uint16(width), uint16(height), screens)
	}); err != nil {
		t.finishResize(reply, false)
		return err
	}

	timer := time.NewTimer(desktopResizeTimeout)
	defer timer.Stop()
	select {
	case result := <-reply:
		if result.timeout {
			return appsession.ErrDesktopResizeTimeout
		}
		extended := result.extended
		switch {
		case extended == nil:
			return appsession.ErrDesktopResizeRefused
		case extended.Status == 0:
			return nil
		case extended.Status == 4:
			// RFC-style status 4 ("request forwarded") is the normal answer
			// while a guest is booting: the server accepted the request and
			// sends the real result asynchronously. It is not a refusal.
			return appsession.ErrDesktopResizePending
		default:
			// Statuses 1-3 are refusals; an unknown nonzero status fails safe
			// as a refusal without ending the session.
			return appsession.ErrDesktopResizeRefused
		}
	case <-timer.C:
		// Keep the timed-out request briefly so its late nonzero reply cannot
		// answer (and latch) a newer request.
		t.finishResize(reply, true)
		return appsession.ErrDesktopResizeTimeout
	case <-t.done:
		t.finishResize(reply, false)
		return appsession.ErrNoDisplay
	}
}

// finishResize clears the pending request if reply still owns it and, when the
// call timed out, records it in the bounded stale set. It runs under resizeMu
// so a concurrent late reply either wins the pending slot or sees the stale
// entry, never both.
func (t *transport) finishResize(reply chan resizeResult, timedOut bool) {
	t.resizeMu.Lock()
	defer t.resizeMu.Unlock()
	if t.resizeReply != reply {
		// A reply already resolved this request.
		return
	}
	if timedOut {
		t.addStaleLocked(staleResize{
			epoch:   t.resizePendingEpoch,
			width:   t.resizeWidth,
			height:  t.resizeHeight,
			expires: time.Now().Add(staleResizeWindow),
		})
	}
	t.clearPendingLocked()
}

// deliverResizeReply hands a client-initiated ExtendedDesktopSize reply to a
// waiting SetDesktopSize. The reader never blocks: an unexpected reply is
// discarded.
//
// Correlation uses the request epoch plus dimensions. A success (status 0) is
// matched by dimensions to the pending request; if it matches a timed-out
// request instead, that stale entry is consumed and the pending waiter is left
// alone. A nonzero status describes the server's current layout rather than
// the requested size, so dimensions cannot identify it: when a timed-out
// request is still live, the reply cannot be attributed to the pending request
// with certainty and the adapter fails safe. A refusal then resolves the
// pending request as a timeout (never latching local scaling), and a forwarded
// status leaves it pending for its own deadline. This prevents a late refusal
// for an older size from latching a fallback on a server that did resize.
func (t *transport) deliverResizeReply(extended *rfb.ExtendedDesktopSize) {
	if extended == nil || extended.Reason != 1 {
		return
	}
	t.resizeMu.Lock()
	t.expireStaleLocked(time.Now())
	reply := t.resizeReply
	var (
		result  resizeResult
		deliver bool
	)
	switch extended.Status {
	case 0:
		switch {
		case reply != nil && extended.Width == t.resizeWidth && extended.Height == t.resizeHeight:
			t.clearPendingLocked()
			result, deliver = resizeResult{extended: extended}, true
		case t.consumeStaleMatchingLocked(extended.Width, extended.Height):
			// A late success for an earlier, timed-out request. The pending
			// waiter must keep waiting for its own reply.
		default:
			// An unmatched success carries no useful correlation; discard it.
		}
	case 4:
		switch {
		case reply == nil:
			t.consumeOldestStaleLocked()
		case len(t.staleResizes) > 0:
			// An older request could have produced this forwarded status.
			// Leave the pending request to its deadline (non-latching).
			t.consumeOldestStaleLocked()
		default:
			t.clearPendingLocked()
			result, deliver = resizeResult{extended: extended}, true
		}
	default:
		switch {
		case reply == nil:
			t.consumeOldestStaleLocked()
		case len(t.staleResizes) > 0:
			// A late refusal (status 1-3 or unknown) for an older request.
			// Resolve the pending request as a timeout: a refusal would latch
			// the fallback even though the server may have resized.
			t.consumeOldestStaleLocked()
			t.clearPendingLocked()
			result, deliver = resizeResult{timeout: true}, true
		default:
			t.clearPendingLocked()
			result, deliver = resizeResult{extended: extended}, true
		}
	}
	t.resizeMu.Unlock()
	if !deliver {
		return
	}
	select {
	case reply <- result:
	default:
	}
}

// clearPendingLocked forgets the outstanding request. The caller holds
// resizeMu.
func (t *transport) clearPendingLocked() {
	t.resizeReply = nil
	t.resizeWidth, t.resizeHeight = 0, 0
	t.resizePendingEpoch = 0
}

// addStaleLocked records a timed-out request, dropping expired entries and the
// oldest entry beyond maxStaleResizes. The caller holds resizeMu.
func (t *transport) addStaleLocked(entry staleResize) {
	t.expireStaleLocked(time.Now())
	t.staleResizes = append(t.staleResizes, entry)
	if len(t.staleResizes) > maxStaleResizes {
		t.staleResizes = t.staleResizes[len(t.staleResizes)-maxStaleResizes:]
	}
}

// expireStaleLocked drops stale entries past their window. The caller holds
// resizeMu.
func (t *transport) expireStaleLocked(now time.Time) {
	kept := t.staleResizes[:0]
	for _, entry := range t.staleResizes {
		if entry.expires.After(now) {
			kept = append(kept, entry)
		}
	}
	t.staleResizes = kept
}

// consumeStaleMatchingLocked removes the oldest stale entry with these
// dimensions and reports whether one matched. The caller holds resizeMu.
func (t *transport) consumeStaleMatchingLocked(width, height uint16) bool {
	for i, entry := range t.staleResizes {
		if entry.width == width && entry.height == height {
			t.removeStaleAtLocked(i)
			return true
		}
	}
	return false
}

// consumeOldestStaleLocked removes the oldest stale entry if one is live. A
// nonzero reply cannot name its size, so any live timed-out request could be
// its source. The caller holds resizeMu.
func (t *transport) consumeOldestStaleLocked() {
	if len(t.staleResizes) > 0 {
		t.removeStaleAtLocked(0)
	}
}

// removeStaleAtLocked removes one stale entry. The caller holds resizeMu.
func (t *transport) removeStaleAtLocked(index int) {
	t.staleResizes = append(t.staleResizes[:index], t.staleResizes[index+1:]...)
}

// desktopLayout is the last ExtendedDesktopSize screen layout. The width and
// height are the desktop size the layout described.
type desktopLayout struct {
	width   uint16
	height  uint16
	screens []rfb.Screen
}

// rememberLayout keeps the newest successful layout so a later resize can
// preserve screen IDs and flags instead of collapsing to one synthetic screen.
func (t *transport) rememberLayout(extended *rfb.ExtendedDesktopSize) {
	if extended == nil || extended.Status != 0 || len(extended.Screens) == 0 {
		return
	}
	width, height := int(extended.Width), int(extended.Height)
	if width <= 0 || height <= 0 {
		width, height = layoutExtent(extended.Screens)
	}
	layout := desktopLayout{
		width:   uint16(width),
		height:  uint16(height),
		screens: append([]rfb.Screen(nil), extended.Screens...),
	}
	t.layoutMu.Lock()
	t.layout = layout
	t.layoutMu.Unlock()
}

// desktopScreens builds the screen list for a SetDesktopSize request from the
// remembered layout. With no layout it falls back to one synthetic screen.
func (t *transport) desktopScreens(width, height uint16) []rfb.Screen {
	t.layoutMu.Lock()
	layout := desktopLayout{
		width:   t.layout.width,
		height:  t.layout.height,
		screens: append([]rfb.Screen(nil), t.layout.screens...),
	}
	t.layoutMu.Unlock()
	return resizeScreens(layout.screens, layout.width, layout.height, width, height)
}

// resizeScreens returns the remembered layout scaled to width by height.
// Every screen keeps its ID and flags; origins and sizes scale proportionally
// and are clamped so the union fits the requested desktop and no screen shrinks
// to zero. The clamping applies to a single remembered screen too: a screen
// remembered at a nonzero origin must not push the union past the desktop.
func resizeScreens(screens []rfb.Screen, oldWidth, oldHeight, width, height uint16) []rfb.Screen {
	if len(screens) == 0 {
		return []rfb.Screen{{Width: width, Height: height}}
	}
	oldW, oldH := int(oldWidth), int(oldHeight)
	if oldW <= 0 || oldH <= 0 {
		oldW, oldH = layoutExtent(screens)
	}
	newW, newH := int(width), int(height)
	scaled := make([]rfb.Screen, len(screens))
	for i, screen := range screens {
		scaled[i] = screen
		x := clamp(scaledExtent(int(screen.X), oldW, newW), 0, newW-1)
		y := clamp(scaledExtent(int(screen.Y), oldH, newH), 0, newH-1)
		w := max(scaledExtent(int(screen.Width), oldW, newW), 1)
		h := max(scaledExtent(int(screen.Height), oldH, newH), 1)
		if x+w > newW {
			w = newW - x
		}
		if y+h > newH {
			h = newH - y
		}
		scaled[i].X = uint16(x)
		scaled[i].Y = uint16(y)
		scaled[i].Width = uint16(max(w, 1))
		scaled[i].Height = uint16(max(h, 1))
	}
	return scaled
}

// scaledExtent scales one coordinate or dimension from an old total to a new
// one. A non-positive old total yields zero. The multiplication uses int64 so
// a 32-bit target cannot overflow on a large screen layout.
func scaledExtent(value, oldTotal, newTotal int) int {
	if oldTotal <= 0 {
		return 0
	}
	return int(int64(value) * int64(newTotal) / int64(oldTotal))
}

// layoutExtent returns the bounding size of a screen layout.
func layoutExtent(screens []rfb.Screen) (int, int) {
	width, height := 1, 1
	for _, screen := range screens {
		if right := int(screen.X) + int(screen.Width); right > width {
			width = right
		}
		if bottom := int(screen.Y) + int(screen.Height); bottom > height {
			height = bottom
		}
	}
	return width, height
}

func clamp(value, low, high int) int {
	if high < low {
		high = low
	}
	return min(max(value, low), high)
}

// write enqueues one client message and waits for the writer goroutine to send
// it. It is safe from any goroutine, including the processing goroutine when a
// blocking fence requires a completed reply.
func (t *transport) write(send func() error) error {
	if t.closed.Load() {
		return appsession.ErrNoDisplay
	}
	done, ok := t.writes.submit(send)
	if !ok {
		return appsession.ErrNoDisplay
	}
	select {
	case err := <-done:
		return err
	case <-t.done:
		return appsession.ErrNoDisplay
	}
}

// writeAsync enqueues one client message without waiting. A failure ends the
// transport, because the ordered writer cannot recover a broken stream.
func (t *transport) writeAsync(send func() error) {
	if t.closed.Load() {
		return
	}
	t.writes.submitAsync(func() error {
		if err := send(); err != nil && !t.closed.Load() {
			t.fail(err)
			_ = t.Close()
		}
		return nil
	})
}

// writeAsyncBounded is writeAsync with a write deadline for one optional
// message. The deadline is set and cleared inside the writer goroutine, so no
// other message is written under it. If the peer stops reading, the write
// times out, the transport ends, and the writer is released instead of
// blocking every later message behind it.
func (t *transport) writeAsyncBounded(timeout time.Duration, send func() error) {
	if t.closed.Load() {
		return
	}
	t.writes.submitAsync(func() error {
		if setter, ok := t.conn.(interface{ SetWriteDeadline(time.Time) error }); ok {
			_ = setter.SetWriteDeadline(time.Now().Add(timeout))
			defer func() { _ = setter.SetWriteDeadline(time.Time{}) }()
		}
		if err := send(); err != nil && !t.closed.Load() {
			t.fail(err)
			_ = t.Close()
		}
		return nil
	})
}

// writeQueue is a bounded single-writer FIFO. A server that floods fence
// requests can enqueue replies without forcing the processing goroutine to
// block, while a single goroutine still serializes the wire. The bound keeps
// memory finite; enqueueing past it fails closed through onOverflow (which the
// transport uses to publish a typed error and close the connection) rather
// than growing the queue without limit.
type writeQueue struct {
	mu         sync.Mutex
	tasks      []writeTask
	stopped    bool
	capacity   int
	wake       chan struct{}
	onOverflow func(error)
	// finished closes when the writer goroutine exits. It exists so a test can
	// join the writer deterministically.
	finished chan struct{}
}

type writeTask struct {
	send func() error
	done chan error
}

func newWriteQueue(capacity int, onOverflow func(error)) *writeQueue {
	if capacity <= 0 {
		capacity = WriteQueueCapacity
	}
	queue := &writeQueue{
		capacity:   capacity,
		wake:       make(chan struct{}, 1),
		onOverflow: onOverflow,
		finished:   make(chan struct{}),
	}
	go queue.run()
	return queue
}

func (q *writeQueue) run() {
	defer close(q.finished)
	for {
		q.mu.Lock()
		if q.stopped {
			// Drop whatever is queued: the session is ending. Waiters also
			// select on the transport's done channel, so signalling them here
			// is best-effort and cannot block.
			pending := q.tasks
			q.tasks = nil
			q.mu.Unlock()
			for _, task := range pending {
				if task.done != nil {
					task.done <- appsession.ErrNoDisplay
				}
			}
			return
		}
		if len(q.tasks) == 0 {
			q.mu.Unlock()
			<-q.wake
			continue
		}
		task := q.tasks[0]
		q.tasks = q.tasks[1:]
		q.mu.Unlock()
		err := task.send()
		if task.done != nil {
			task.done <- err
		}
	}
}

func (q *writeQueue) submit(send func() error) (chan error, bool) {
	done := make(chan error, 1)
	if !q.enqueue(writeTask{send: send, done: done}) {
		return nil, false
	}
	return done, true
}

func (q *writeQueue) submitAsync(send func() error) bool {
	return q.enqueue(writeTask{send: send})
}

// enqueue appends one task, or fails closed when the bound would be exceeded.
// onOverflow runs without q.mu held so it can close the connection (which
// unblocks a stuck writer) without deadlocking against the queue.
func (q *writeQueue) enqueue(task writeTask) bool {
	q.mu.Lock()
	if q.stopped {
		q.mu.Unlock()
		return false
	}
	if len(q.tasks) >= q.capacity {
		q.stopped = true
		q.mu.Unlock()
		select {
		case q.wake <- struct{}{}:
		default:
		}
		if q.onOverflow != nil {
			q.onOverflow(ErrWriteQueueFull)
		}
		return false
	}
	q.tasks = append(q.tasks, task)
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
	return true
}

func (q *writeQueue) close() {
	q.mu.Lock()
	if q.stopped {
		q.mu.Unlock()
		return
	}
	q.stopped = true
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
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
		close(t.done)
		t.writes.close()
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
