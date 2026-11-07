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
	"sync"
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
	// Alive reports whether the underlying SSH transport can still serve a
	// channel. A lost network connection reports false, so the ET connection
	// replaces the lease instead of dialing through a dead transport.
	Alive() bool
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
// With Sessions set, a fresh ET session saves its id and passkey in the
// encrypted secret store so a later app process can reattach the same remote
// shell by reset. Persistence is capability-aware: the adapter saves the pair
// only when the server answered the fresh connect with the v7 auth challenge,
// which is what makes a later reattach possible. A pre-v7 server such as
// 6.2.10 stays memory-only for that session, so a restart starts a fresh shell
// instead of leaving an unusable pair that can only fail with the same upgrade
// outcome as stock. A pair saved by an older build is still refused rather
// than silently starting a second shell; the shell clears such a stale record
// and may then explicitly fall back.
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
	// KeepAlive overrides the ET link's liveness period. Zero keeps etcp's
	// default. Tests use a short value to observe a reconnect quickly.
	KeepAlive time.Duration
	// Sessions saves resumable ET session credentials in the encrypted secret
	// store so a new app process can reattach. Nil keeps sessions memory-only.
	Sessions SessionStore
}

// Open starts etterminal through one SSH exec channel, dials etserver through
// the same lease's direct-tcpip channel, completes the connect handshake (the
// v7 challenge when the server offers one, reset recovery when a saved session
// is reattached), and returns the Session transport.
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

	resuming, credentials, err := f.resumeSession(ctx, spec.ResumeKey)
	if err != nil {
		return nil, err
	}
	startingFresh := !resuming
	if !resuming {
		credentials, err = f.bootstrap(ctx, lease)
		if err != nil {
			return nil, err
		}
	}
	dialer, err := newLeaseDialer(lease, func(acquireCtx context.Context) (ConnectionLease, error) {
		return f.acquire(acquireCtx, spec, route, cfg)
	})
	if err != nil {
		return nil, fmt.Errorf("%w: SSH forwarding for etserver: %w", etmodel.ErrHandshakeFailed, err)
	}
	address := net.JoinHostPort(etServerHost, strconv.Itoa(cfg.ETPort))
	handshakeCtx, cancelHandshake := context.WithTimeout(ctx, f.handshakeTimeout())
	defer cancelHandshake()
	notices := make(chan appsession.Notice, 8)
	conn, err := f.dialThrough(handshakeCtx, dialer, address, credentials, resuming, notices)
	if err != nil && resuming && reattachUnavailable(err) {
		// INVALID_KEY means etserver no longer knows the saved id: the remote
		// shell exited while the app was closed. Stock starts a fresh session
		// in that case. An unchallenged legacy server is not this case; stock
		// refuses that reattach with the upgrade outcome.
		f.clearSession(ctx, spec.ResumeKey)
		credentials, err = f.bootstrap(ctx, lease)
		if err == nil {
			startingFresh = true
			conn, err = f.dialThrough(handshakeCtx, dialer, address, credentials, false, notices)
		}
	}
	if err != nil {
		dialer.Close(true)
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
			dialer.Close(true)
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
		dialer.Close(true)
		close(notices)
		return nil, context.Cause(ctx)
	}
	if startingFresh {
		if !conn.ChallengeCapable() {
			// A pre-v7 server cannot authenticate a reattach after a restart.
			// Keep the pair in the live transport only; persisting it would
			// guarantee a permanent ErrReattachUnsupported on the next run.
			f.logger().Info("et: server does not support v7 reattach; session credentials stay memory-only")
		} else if err := f.saveSession(ctx, spec.ResumeKey, credentials); err != nil {
			// Persistence only enables a later reattach; a fully working
			// session must not fail because the secret store is unavailable.
			// The error never contains the credentials.
			f.logger().Warn("et: session credentials not saved for reattach", "err", err)
		}
	}

	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	transport := newInProcessTransport(conn, dialer, newLocalTerminal(spec.Cols, spec.Rows),
		notices, cancel, f.logger(), f.forgetSession(spec.ResumeKey))
	go transport.run(runCtx)
	opened = true
	return transport, nil
}

// reattachUnavailable reports a saved session etserver no longer knows
// (INVALID_KEY on the first connect). Stock falls back to a fresh session
// there. A legacy server that cannot challenge the passkey is a different
// outcome: stock fails with ErrLegacyReattach and tells the user to upgrade
// etserver, so that case must surface instead of starting a second shell.
func reattachUnavailable(err error) bool {
	return errors.Is(err, etcp.ErrRejected)
}

// retryLaterDelay is the first backoff after RETRY_LATER and its cap. The
// server asks for a retry only while it rebuilds its session table after a
// restart; stock et's reconnect loop also waits and retries instead of ending
// the session.
const (
	retryLaterDelay    = 250 * time.Millisecond
	retryLaterMaxDelay = 5 * time.Second
)

// dial connects once through a single forwarding client. It is the
// compatibility path tests and the lab helpers use when there is no lease
// replacement to exercise.
func (f InProcessFactory) dial(ctx context.Context, forwarding appconnection.ForwardingClient,
	address string, credentials bootstrap.Credentials, notices chan appsession.Notice) (*etcp.Conn, error) {
	return f.dialThrough(ctx, noDeadlineDialer{forwarding: forwarding}, address, credentials, false, notices)
}

// dialThrough connects to etserver through netDialer, retrying with backoff
// while the server answers RETRY_LATER. The caller's context bounds the wait.
// The Displaced callback publishes a Session notice without blocking the
// connection goroutine. resetIntent asks the first handshake for a v7 reset
// recovery; the etcp Conn's own redials never set it again.
func (f InProcessFactory) dialThrough(ctx context.Context, netDialer leaseNetDialer,
	address string, credentials bootstrap.Credentials, resetIntent bool,
	notices chan appsession.Notice) (*etcp.Conn, error) {

	onDisplaced := func() {
		select {
		case notices <- appsession.Notice{Code: appsession.NoticeETDisplaced}:
		default:
		}
	}
	dialer := &etcp.Dialer{
		NetDialer:        netDialer,
		KeepAlive:        f.KeepAlive,
		HandshakeTimeout: f.handshakeTimeout(),
		ResetIntent:      resetIntent,
		Logger:           f.logger(),
		OnDisplaced:      onDisplaced,
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
// The in-process path is the only ET path and applies no Mosh-style route
// restriction: a proxy and any number of jump Hosts are ordinary Connection
// Manager routes, shared with SSH Sessions.
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
		HostID:   spec.HostID,
		Hostname: spec.Hostname,
		Port:     spec.Port,
		Username: username,
		Jumps:    route.Jumps,
		Proxy:    route.Proxy,
		// The bootstrap and every replacement lease use the same first-endpoint
		// Network Profile: the Manager opens the Provider from this route and
		// never falls back to a direct dial.
		Network:           route.Network,
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

// permanentError marks an Open failure that retrying cannot change, such as a
// server speaking another protocol or refusing reattach. The Session service
// stops on it instead of looping on the same typed outcome. errors.Is and
// errors.As still see the wrapped error.
type permanentError struct{ err error }

func (e permanentError) Error() string { return e.err.Error() }
func (e permanentError) Unwrap() error { return e.err }
func (permanentError) Permanent() bool { return true }

// classifyDialError maps the vendored ET connection's failures onto the
// domain's typed vocabulary.
func classifyDialError(err error) error {
	switch {
	case errors.Is(err, etcp.ErrVersion):
		return permanentError{fmt.Errorf("%w: %w", etmodel.ErrUnsupportedServer, err)}
	case errors.Is(err, etcp.ErrLegacyReattach):
		return permanentError{fmt.Errorf("%w: %w", etmodel.ErrReattachUnsupported, err)}
	case errors.Is(err, etcp.ErrReplayExceeded):
		// The sequence gap is beyond the retained window: the remote session
		// cannot be continued and the caller needs a typed, actionable error.
		return fmt.Errorf("%w: %w", etmodel.ErrSessionUnavailable, err)
	case errors.Is(err, etcp.ErrRetryLater):
		return fmt.Errorf("%w: %w", etmodel.ErrRetryLater, err)
	case errors.Is(err, etcp.ErrDisplaced):
		// Another client owns ET's single slot; a retry would fight it.
		return permanentError{fmt.Errorf("%w: %w", etmodel.ErrSessionDisplaced, err)}
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

// leaseNetDialer is the etcp dialer's transport seam: it dials one ET link,
// either through the current SSH lease or a replacement acquired after the old
// lease died.
type leaseNetDialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

// leaseDialer adapts the current SSH lease's direct-tcpip forwarding to the
// etcp dialer. It survives a network change: when the leased SSH transport
// dies, the next dial discards it, acquires a fresh lease through the
// Connection Manager, and dials etserver through that lease. The ET session
// credentials and sequence state stay in the etcp.Conn, so reconnecting never
// reruns the etterminal bootstrap or starts a new ET session.
//
// The SSH channel rejects SetDeadline while the handshake sets idle deadlines
// on its conn; noDeadlineConn accepts and ignores them. Open bounds the initial
// handshake with handshakeTimeout, which etcp uses to close the channel, and
// etcp's liveness watcher bounds every later link.
type leaseDialer struct {
	acquire func(context.Context) (ConnectionLease, error)

	// dialMu serializes lease replacement and dials. etcp's supervisor dials
	// one link at a time, but the initial dial may overlap a caller that
	// closes the transport, and keeping it serial means at most one extra
	// lease can exist.
	dialMu sync.Mutex

	mu         sync.Mutex
	lease      ConnectionLease
	forwarding appconnection.ForwardingClient
	closed     bool
}

func newLeaseDialer(lease ConnectionLease, acquire func(context.Context) (ConnectionLease, error)) (*leaseDialer, error) {
	if lease == nil {
		return nil, errors.New("et: no SSH lease")
	}
	forwarding, err := lease.Forwarding()
	if err != nil {
		return nil, err
	}
	return &leaseDialer{lease: lease, forwarding: forwarding, acquire: acquire}, nil
}

// DialContext dials etserver through the current lease. A dead lease, or a
// dial that fails, is dropped so the next attempt acquires a fresh SSH
// connection instead of retrying a transport the network already lost.
func (d *leaseDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	d.dialMu.Lock()
	defer d.dialMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, context.Cause(ctx)
	}

	lease, forwarding := d.current()
	if lease == nil || !lease.Alive() || forwarding == nil {
		var err error
		lease, forwarding, err = d.replace(ctx)
		if err != nil {
			return nil, err
		}
	}
	conn, err := forwarding.DialContext(ctx, network, address)
	if err != nil {
		d.drop(lease)
		return nil, err
	}
	return noDeadlineConn{Conn: conn}, nil
}

// current returns the lease and forwarding client in use, or nil after Close.
func (d *leaseDialer) current() (ConnectionLease, appconnection.ForwardingClient) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil, nil
	}
	return d.lease, d.forwarding
}

// replace discards the current lease, acquires a new one, and installs its
// forwarding client. A Close that races the acquisition wins: the new lease is
// released instead of installed.
func (d *leaseDialer) replace(ctx context.Context) (ConnectionLease, appconnection.ForwardingClient, error) {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil, nil, net.ErrClosed
	}
	old := d.lease
	d.lease, d.forwarding = nil, nil
	d.mu.Unlock()
	discardLease(old)

	if d.acquire == nil {
		return nil, nil, errors.New("et: no SSH lease provider for a reconnect")
	}
	lease, err := d.acquire(ctx)
	if err != nil {
		return nil, nil, err
	}
	if lease == nil {
		return nil, nil, errors.New("et: the SSH lease provider returned no lease")
	}
	forwarding, err := lease.Forwarding()
	if err != nil {
		discardLease(lease)
		return nil, nil, err
	}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		discardLease(lease)
		return nil, nil, net.ErrClosed
	}
	d.lease, d.forwarding = lease, forwarding
	d.mu.Unlock()
	return lease, forwarding, nil
}

// drop forgets lease after its forwarding channel failed. A lease that still
// reports alive is discarded anyway: the SSH connection did not serve the
// channel the ET session needs, and keeping it would retry a broken path
// forever.
func (d *leaseDialer) drop(lease ConnectionLease) {
	d.mu.Lock()
	if d.lease == lease {
		d.lease, d.forwarding = nil, nil
	}
	d.mu.Unlock()
	discardLease(lease)
}

// Close releases the current lease. discard closes it instead of parking it
// for reuse; a failed Session discards, a clean one releases.
func (d *leaseDialer) Close(discard bool) {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	d.closed = true
	lease := d.lease
	d.lease, d.forwarding = nil, nil
	d.mu.Unlock()
	if lease == nil {
		return
	}
	if discard {
		discardLease(lease)
		return
	}
	_ = lease.Release()
}

func discardLease(lease ConnectionLease) {
	if lease != nil {
		_ = lease.Discard()
	}
}

// noDeadlineDialer adapts one direct-tcpip channel to the etcp dialer. The SSH
// channel rejects SetDeadline, while the handshake sets idle deadlines on its
// conn. Those deadlines are accepted and ignored: every connect exchange is
// bounded instead by etcp's handshake watchdog, whose timeout comes from
// InProcessFactory.HandshakeTimeout. Before that watchdog existed, only the
// initial Open was bounded (by its context) and a reconnect to a server that
// accepted the channel and never answered could block forever.
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
