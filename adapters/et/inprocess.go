// SPDX-License-Identifier: GPL-3.0-or-later

package et

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"time"

	"github.com/mslxl/natcatty/adapters/et/internal/etgo/bootstrap"
	"github.com/mslxl/natcatty/adapters/et/internal/etgo/etcp"
	etsession "github.com/mslxl/natcatty/adapters/et/internal/etgo/session"
	appconnection "github.com/mslxl/natcatty/application/connection"
	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
	etmodel "github.com/mslxl/natcatty/domain/model/et"
)

const (
	// bootstrapTimeout bounds how long the etterminal bootstrap may take to
	// print IDPASSKEY. It covers a slow login shell without letting a broken
	// server hold the Session open forever.
	bootstrapTimeout = 30 * time.Second
	// maxBootstrapOutput bounds how much etterminal stdout is retained while
	// looking for IDPASSKEY. Banner noise is far smaller; a marker after the
	// cap is ignored.
	maxBootstrapOutput = 1 << 20
	// handshakeTimeout bounds the protocol-6 connect exchange. The SSH
	// forwarding channel cannot carry net.Conn deadlines, so this context is
	// what interrupts a server that accepts the channel and never answers:
	// etcp closes the channel when it ends.
	handshakeTimeout = 30 * time.Second
	// etServerHost is the address the direct-tcpip channel targets from the
	// SSH server's perspective. The ET server runs beside the SSH endpoint.
	etServerHost = "127.0.0.1"
	// defaultCols and defaultRows replace a Session size that was never set.
	defaultCols = 80
	defaultRows = 24
)

// RouteSource resolves the non-secret jump chain and proxy for a Host. The app
// backs it with the route resolver used by SSH Sessions.
type RouteSource interface {
	Route(ctx context.Context, id model.HostID) (appconnection.Route, error)
}

// ConnectionLease is the slice of connection.Lease the in-process client
// needs. *appconnection.Lease implements it; tests provide a fake.
type ConnectionLease interface {
	OpenExec(ctx context.Context, command string) (appconnection.ExecStream, error)
	Forwarding() (appconnection.ForwardingClient, error)
	Release() error
	Discard() error
	Done() <-chan struct{}
}

var _ ConnectionLease = (*appconnection.Lease)(nil)

// InProcessFactory opens EternalTerminal Sessions with the pure-Go client
// vendored in adapters/et/internal/etgo. The SSH bootstrap runs through the
// Connection Manager's exec capability and the ET socket is reached through
// the same lease's direct-tcpip forwarding, so saved credentials, host-key
// trust, timeouts, proxy, and jump chains behave like an SSH Session.
//
// The zero value is not usable: Manager or Acquire must be set.
type InProcessFactory struct {
	// Manager acquires the Connection lease. Acquire, when non-nil, replaces
	// it for tests.
	Manager *appconnection.Manager
	// Routes resolves proxy and jump chains. Nil means a direct connection.
	Routes RouteSource
	// Logger receives connection events. Nil discards them. It must never
	// receive the ET session id or passkey.
	Logger *slog.Logger
	// Acquire overrides Manager; tests use it to inject a fake lease.
	Acquire func(ctx context.Context, req appconnection.Request) (ConnectionLease, error)
	// HandshakeTimeout overrides handshakeTimeout. Zero keeps the default.
	HandshakeTimeout time.Duration
}

// Open starts etterminal through one SSH exec channel, dials etserver through
// the same lease's direct-tcpip channel, completes the protocol-6 handshake,
// and returns the Session transport.
func (f InProcessFactory) Open(ctx context.Context, spec appsession.StartSpec) (appsession.Transport, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if spec.ET == nil {
		return nil, fmt.Errorf("%w: no EternalTerminal configuration", etmodel.ErrInvalidConfig)
	}
	cfg := spec.ET.Normalized()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	route, err := f.route(ctx, spec.HostID)
	if err != nil {
		return nil, err
	}
	lease, err := f.acquire(ctx, spec, route, cfg)
	if err != nil {
		// connection.Manager errors are already typed (auth, host key,
		// timeout, network) and must keep their kind for the UI.
		return nil, err
	}
	opened := false
	defer func() {
		if !opened {
			_ = lease.Discard()
		}
	}()

	credentials, err := f.bootstrap(ctx, lease)
	if err != nil {
		return nil, err
	}
	forwarding, err := lease.Forwarding()
	if err != nil {
		return nil, fmt.Errorf("%w: SSH forwarding for etserver: %w", etmodel.ErrHandshakeFailed, err)
	}
	address := net.JoinHostPort(etServerHost, strconv.Itoa(cfg.ETPort))
	handshakeCtx, cancelHandshake := context.WithTimeout(ctx, f.handshakeTimeout())
	defer cancelHandshake()
	notices := make(chan appsession.Notice, 8)
	conn, err := f.dial(handshakeCtx, forwarding, address, credentials, notices)
	if err != nil {
		close(notices)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, context.Cause(ctx)
		}
		// A server that answered RETRY_LATER until the handshake deadline is
		// still a retry-later outcome; the generic timeout sentence would hide
		// that the server was recovering.
		if errors.Is(err, etcp.ErrRetryLater) {
			return nil, classifyDialError(err)
		}
		if errors.Is(context.Cause(handshakeCtx), context.DeadlineExceeded) {
			return nil, fmt.Errorf("%w: connect handshake timed out after %v: %w",
				etmodel.ErrHandshakeFailed, f.handshakeTimeout(), err)
		}
		return nil, classifyDialError(err)
	}
	// A reset-recovery connect attached to a session whose shell is already
	// running, so stock et skips INITIAL_PAYLOAD (ClientConnection's
	// wasRecovered path). The terminal then starts blank: ET has no
	// server-side screen snapshot and reset recovery exchanges empty catchup
	// buffers, exactly like stock `et --attach`.
	if !conn.Resumed() {
		if err := etsession.Start(handshakeCtx, conn, etsession.Options{Logger: f.logger()}); err != nil {
			_ = conn.Close()
			close(notices)
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, context.Cause(ctx)
			}
			if errors.Is(context.Cause(handshakeCtx), context.DeadlineExceeded) {
				return nil, fmt.Errorf("%w: INITIAL_PAYLOAD timed out: %w", etmodel.ErrHandshakeFailed, err)
			}
			return nil, classifyStartError(err)
		}
	}
	cancelHandshake()
	if err := ctx.Err(); err != nil {
		_ = conn.Close()
		close(notices)
		return nil, context.Cause(ctx)
	}

	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	transport := newInProcessTransport(conn, lease, newLocalTerminal(spec.Cols, spec.Rows), notices, cancel, f.logger())
	go transport.run(runCtx)
	go transport.watchLease(runCtx)
	opened = true
	return transport, nil
}

// retryLaterDelay is the first backoff after RETRY_LATER and its cap. The
// server asks for a retry only while it rebuilds its session table after a
// restart; stock et's reconnect loop also waits and retries instead of ending
// the session.
const (
	retryLaterDelay    = 250 * time.Millisecond
	retryLaterMaxDelay = 5 * time.Second
)

// dial connects to etserver, retrying with backoff while the server answers
// RETRY_LATER. The caller's context bounds the wait. The Displaced callback
// publishes a Session notice without blocking the connection goroutine.
func (f InProcessFactory) dial(ctx context.Context, forwarding appconnection.ForwardingClient,
	address string, credentials bootstrap.Credentials, notices chan appsession.Notice) (*etcp.Conn, error) {

	onDisplaced := func() {
		select {
		case notices <- appsession.Notice{Code: appsession.NoticeETDisplaced}:
		default:
		}
	}
	dialer := &etcp.Dialer{
		NetDialer:   noDeadlineDialer{forwarding: forwarding},
		Logger:      f.logger(),
		OnDisplaced: onDisplaced,
	}
	delay := retryLaterDelay
	for {
		conn, err := dialer.Dial(ctx, address, credentials.ID, credentials.Passkey())
		if err == nil {
			return conn, nil
		}
		if !errors.Is(err, etcp.ErrRetryLater) {
			return nil, err
		}
		f.logger().Info("et: server is recovering; retrying", "delay", delay)
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return nil, fmt.Errorf("%w: %w", err, context.Cause(ctx))
		}
		delay = min(2*delay, retryLaterMaxDelay)
	}
}

// route resolves the Host's non-secret path. A nil resolver is a direct
// connection, as it is for the SSH factory.
//
// The in-process path deliberately does not call the bundled client's
// CheckRoute: a proxy and any number of jump Hosts are ordinary Connection
// Manager routes, shared with SSH Sessions. Only the bundled factory keeps the
// original one-jump/no-proxy restriction.
func (f InProcessFactory) route(ctx context.Context, id model.HostID) (appconnection.Route, error) {
	if f.Routes == nil {
		return appconnection.Route{}, nil
	}
	return f.Routes.Route(ctx, id)
}

// acquire takes the lease that carries both the bootstrap exec and the ET
// forwarding channels.
func (f InProcessFactory) acquire(ctx context.Context, spec appsession.StartSpec,
	route appconnection.Route, cfg etmodel.Config) (ConnectionLease, error) {

	username := spec.Username
	if username == "" {
		username = cfg.Username
	}
	request := appconnection.Request{
		HostID:            spec.HostID,
		Hostname:          spec.Hostname,
		Port:              spec.Port,
		Username:          username,
		Jumps:             route.Jumps,
		Proxy:             route.Proxy,
		Algorithms:        route.Algorithms,
		ConfigFingerprint: route.ConfigFingerprint,
		LeaseKind:         appconnection.LeaseShell,
		Keepalive:         &appconnection.DefaultKeepalive,
	}
	if f.Acquire != nil {
		return f.Acquire(ctx, request)
	}
	if f.Manager == nil {
		return nil, errors.New("et: no SSH connection manager for the in-process client")
	}
	return f.Manager.Acquire(ctx, request)
}

// bootstrap runs the etterminal helper over the lease's exec capability and
// returns the credentials it prints. The placeholder passkey the command
// carries is not secret; a modern server regenerates both values.
func (f InProcessFactory) bootstrap(ctx context.Context, lease ConnectionLease) (bootstrap.Credentials, error) {
	id, passkey := bootstrap.Placeholder()
	command, err := bootstrap.RemoteCommand(id, passkey, bootstrap.DefaultTerm, "")
	if err != nil {
		return bootstrap.Credentials{}, fmt.Errorf("%w: %w", etmodel.ErrBootstrapFailed, err)
	}
	stream, err := lease.OpenExec(ctx, command)
	if err != nil {
		return bootstrap.Credentials{}, fmt.Errorf("%w: start etterminal: %w", etmodel.ErrBootstrapFailed, err)
	}
	defer stream.Close()

	bootstrapCtx, cancel := context.WithTimeout(ctx, bootstrapTimeout)
	defer cancel()
	credentials, err := readBootstrap(bootstrapCtx, stream)
	if err != nil {
		return bootstrap.Credentials{}, err
	}
	return credentials, nil
}

// readBootstrap scans etterminal's stdout for the first complete IDPASSKEY
// line. It never includes the passkey in an error.
func readBootstrap(ctx context.Context, stream appconnection.ExecStream) (bootstrap.Credentials, error) {
	type result struct {
		credentials bootstrap.Credentials
		err         error
	}
	done := make(chan result, 1)
	go func() {
		buffer := make([]byte, 4096)
		var output []byte
		marked := false
		for {
			n, err := stream.Read(buffer)
			if n > 0 {
				if room := maxBootstrapOutput - len(output); room > 0 {
					output = append(output, buffer[:min(n, room)]...)
				}
				if !marked && bootstrap.HasMarker(output) {
					marked = true
				}
				if marked {
					credentials, parseErr := bootstrap.ParseCredentials(output)
					if parseErr == nil {
						done <- result{credentials: credentials}
						return
					}
					// A marker split across reads can look malformed until
					// enough of the id and passkey have arrived. Only an
					// end-of-stream turns a still-incomplete value into a
					// failure.
					if err == nil && incompleteCredentials(output) {
						continue
					}
					done <- result{err: fmt.Errorf("%w: %w", etmodel.ErrBootstrapFailed, parseErr)}
					return
				}
			}
			if err != nil {
				done <- result{err: bootstrapEndError(stream, output, err)}
				return
			}
		}
	}()
	select {
	case got := <-done:
		return got.credentials, got.err
	case <-ctx.Done():
		return bootstrap.Credentials{}, fmt.Errorf("%w: %w", etmodel.ErrBootstrapFailed, context.Cause(ctx))
	}
}

// incompleteCredentials reports whether the IDPASSKEY value after the first
// marker is still shorter than a complete line, so more output may finish it.
func incompleteCredentials(output []byte) bool {
	index := bytes.Index(output, []byte(bootstrap.Marker))
	if index < 0 {
		return true
	}
	rest := output[index+len(bootstrap.Marker):]
	// 16-byte id, '/', 32-byte passkey.
	return len(rest) < 16+1+32
}

// bootstrapEndError builds the failure for an etterminal stream that ended
// without usable credentials. It reports the remote exit status but never the
// output, which may contain a real passkey.
func bootstrapEndError(stream appconnection.ExecStream, output []byte, readErr error) error {
	if bootstrap.HasMarker(output) {
		if _, parseErr := bootstrap.ParseCredentials(output); parseErr != nil {
			return fmt.Errorf("%w: %w", etmodel.ErrBootstrapFailed, parseErr)
		}
	}
	if errors.Is(readErr, io.EOF) {
		if code, err := stream.ExitCode(); err == nil {
			return fmt.Errorf("%w: etterminal ended with status %d", etmodel.ErrBootstrapFailed, code)
		}
		return fmt.Errorf("%w: etterminal ended without IDPASSKEY", etmodel.ErrBootstrapFailed)
	}
	return fmt.Errorf("%w: %w", etmodel.ErrBootstrapFailed, readErr)
}

// classifyDialError maps the vendored ET connection's failures onto the
// domain's typed vocabulary.
func classifyDialError(err error) error {
	switch {
	case errors.Is(err, etcp.ErrVersion):
		return fmt.Errorf("%w: %w", etmodel.ErrUnsupportedServer, err)
	case errors.Is(err, etcp.ErrLegacyReattach):
		return fmt.Errorf("%w: %w", etmodel.ErrReattachUnsupported, err)
	case errors.Is(err, etcp.ErrRetryLater):
		return fmt.Errorf("%w: %w", etmodel.ErrRetryLater, err)
	case errors.Is(err, etcp.ErrDisplaced):
		return fmt.Errorf("%w: %w", etmodel.ErrSessionDisplaced, err)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	default:
		return fmt.Errorf("%w: %w", etmodel.ErrHandshakeFailed, err)
	}
}

// classifyStartError maps INITIAL_PAYLOAD failures onto the domain vocabulary.
func classifyStartError(err error) error {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	case errors.Is(err, etsession.ErrStartRejected):
		return fmt.Errorf("%w: server rejected the session: %w", etmodel.ErrHandshakeFailed, err)
	default:
		return fmt.Errorf("%w: %w", etmodel.ErrHandshakeFailed, err)
	}
}

// classifyRun maps the session's end onto the Session transport vocabulary. A
// server end without an exit status is a normal shell exit: ET 6.2.10 has no
// exit-status packet, and treating it as a bare close would make the Session
// service reconnect after every deliberate logout.
func classifyRun(err error) appsession.End {
	if err == nil {
		return appsession.End{Reason: appsession.EndReasonExited}
	}
	var exit *etsession.ExitError
	if errors.As(err, &exit) {
		return appsession.End{Reason: appsession.EndReasonExited, ExitCode: exit.Code, HasExitCode: true}
	}
	if errors.Is(err, etsession.ErrDetached) {
		// The local escape deliberately ends the client, like stock et.
		return appsession.End{Reason: appsession.EndReasonExited}
	}
	if errors.Is(err, etcp.ErrDisplaced) {
		// Another client owns ET's single session slot. Reconnecting would
		// fight it, so the Session ends with the takeover notice instead of
		// entering the reconnect loop.
		return appsession.End{Reason: appsession.EndReasonDisplaced}
	}
	return appsession.End{Reason: appsession.EndReasonError}
}

// noDeadlineDialer adapts a direct-tcpip channel to the etcp dialer. The SSH
// channel rejects SetDeadline, while the handshake sets idle deadlines on its
// conn. Those deadlines are accepted and ignored: Open bounds the initial
// handshake with handshakeTimeout, which etcp uses to close the channel.
// Reconnect handshakes are the vendored supervisor's and belong to a later
// ticket.
type noDeadlineDialer struct {
	forwarding appconnection.ForwardingClient
}

func (d noDeadlineDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	conn, err := d.forwarding.DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}
	return noDeadlineConn{Conn: conn}, nil
}

// noDeadlineConn hides the SSH channel's missing deadline support.
type noDeadlineConn struct {
	net.Conn
}

func (noDeadlineConn) SetDeadline(time.Time) error      { return nil }
func (noDeadlineConn) SetReadDeadline(time.Time) error  { return nil }
func (noDeadlineConn) SetWriteDeadline(time.Time) error { return nil }

func (f InProcessFactory) handshakeTimeout() time.Duration {
	if f.HandshakeTimeout > 0 {
		return f.HandshakeTimeout
	}
	return handshakeTimeout
}

func (f InProcessFactory) logger() *slog.Logger {
	if f.Logger != nil {
		return f.Logger
	}
	return slog.New(slog.DiscardHandler)
}
