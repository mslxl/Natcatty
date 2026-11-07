// SPDX-License-Identifier: GPL-3.0-or-later

// In-process Mosh transport. It shares the SSH bootstrap contract, the
// MOSH CONNECT announcement parser, and the route checks in this package, then
// speaks mosh's UDP protocol directly through internal/mosh. No local
// mosh-client process is launched.
package mosh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	appsession "github.com/mslxl/natcatty/application/session"
	moshmodel "github.com/mslxl/natcatty/domain/model/mosh"
	moshcore "github.com/mslxl/natcatty/internal/mosh"
	"github.com/mslxl/natcatty/internal/mosh/terminal"
)

const (
	// DefaultProbeTimeout bounds one UDP endpoint probe. When mosh-server
	// announces an address that is not routable from the client (for example a
	// container-internal address behind a published port), the client falls
	// back to the SSH hostname after this timeout.
	DefaultProbeTimeout = 1200 * time.Millisecond
	// shutdownTimeout bounds the graceful sentinel exchange on Close. The
	// local mosh-server acknowledges in milliseconds; the cap keeps a user
	// close snappy when the peer is gone.
	shutdownTimeout = time.Second
	// tickInterval drives acks, retransmissions, and shutdown retries.
	tickInterval = 25 * time.Millisecond
	// receiveBuffer matches mosh's Session::RECEIVE_MTU.
	receiveBuffer = 2048
	// readRetryInterval and maxReadFailures bound transient read errors on
	// the unconnected UDP socket. A local address change or an ICMP error
	// must not end the Session; a persistently unusable socket should reach
	// the reconnect policy.
	readRetryInterval = 100 * time.Millisecond
	maxReadFailures   = 50
	// probeRetransmitInterval paces the initial SSP state during the bounded
	// connection probe. The probe cannot wait for the full SSP RTO: with the
	// default 1200ms window, one lost initial datagram would consume almost
	// all of it. Stock mosh-client has no such fixed probe deadline and keeps
	// sending until the server answers.
	probeRetransmitInterval = 250 * time.Millisecond
)

// InProcessFactory opens Mosh Sessions without a local client binary. The SSH
// bootstrap, handshake, route checks, and credential handling are shared in
// this package; the UDP protocol is pure Go.
type InProcessFactory struct {
	// Bootstrap opens the SSH channel that runs mosh-server.
	Bootstrap Bootstrap
	// BaseEnv supplies the process environment used for locale handoff. Nil
	// uses os.Environ.
	BaseEnv func() []string
	// HandshakeTimeout overrides DefaultHandshakeTimeout.
	HandshakeTimeout time.Duration
	// ProbeTimeout overrides DefaultProbeTimeout.
	ProbeTimeout time.Duration
	// DialDatagram, when non-nil, opens the UDP flow inside a Network
	// Provider's virtual network. The address is handed to the Provider
	// unresolved, so the adapter never uses the host resolver or a host UDP
	// socket. Nil keeps stock mosh's unconnected host socket.
	DialDatagram func(ctx context.Context, network, address string) (net.Conn, error)
	// clock overrides the overlay clock in tests. Production uses the
	// process monotonic clock.
	clock terminal.Clock
}

// Open performs the SSH handshake, probes the announced UDP endpoint (with a
// fallback to the SSH hostname), and returns a Session transport.
func (f InProcessFactory) Open(ctx context.Context, spec appsession.StartSpec) (appsession.Transport, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if spec.Mosh == nil {
		return nil, fmt.Errorf("%w: no Mosh configuration", moshmodel.ErrInvalidConfig)
	}
	environment := os.Environ()
	if f.BaseEnv != nil {
		environment = f.BaseEnv()
	}
	cfg := configFromEnvironment(spec, mergeEnv(environment, spec.Env))
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if f.Bootstrap == nil {
		return nil, errors.New("mosh: no SSH bootstrap is available")
	}

	queue := newOutputQueue(maxBufferedOutput)
	timeout := f.HandshakeTimeout
	if timeout <= 0 {
		timeout = DefaultHandshakeTimeout
	}
	handshakeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	stream, err := f.Bootstrap.OpenBootstrap(handshakeCtx, spec, cfg)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(handshakeCtx, func() { _ = stream.Close() })
	connect, err := announce(handshakeCtx, stream, queue)
	stop()
	_ = stream.Close()
	if err != nil {
		if errors.Is(err, moshmodel.ErrAnnouncement) {
			return nil, err
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("%w: %w: no announcement within %s", moshmodel.ErrHandshake, moshmodel.ErrTimeout, timeout)
		}
		return nil, err
	}

	key, err := moshmodel.DecodeKey(connect.Key)
	connect.Key = ""
	if err != nil {
		return nil, err
	}
	client, err := moshcore.NewClient(key, cfg.Cols, cfg.Rows)
	clear(key)
	if err != nil {
		return nil, err
	}

	// The renderer owns the remote Complete snapshots from the initial state
	// onward. The display's first frame clears the handshake banner, so the
	// old viewport-reset escape is not needed and no raw host bytes are queued.
	rendererOptions := defaultTerminalRendererOptions()
	if f.clock != nil {
		rendererOptions.Clock = f.clock
	}
	prediction := predictionPreference(cfg.PredictionDisplay)
	rendererOptions.PredictionDisplay = &prediction
	renderer := newTerminalRendererWithOptions(queue, cfg.Cols, cfg.Rows, rendererOptions)
	renderer.setUDPPort(connect.Port)

	probeTimeout := f.ProbeTimeout
	if probeTimeout <= 0 {
		probeTimeout = DefaultProbeTimeout
	}
	result, err := probe(ctx, client, cfg, connect, probeTimeout, renderer, f.DialDatagram)
	if err != nil {
		client.Close()
		return nil, err
	}

	transport := &inProcessTransport{
		queue:    queue,
		client:   client,
		link:     result.link,
		renderer: renderer,
		cols:     cfg.Cols,
		rows:     cfg.Rows,
		done:     make(chan struct{}),
	}
	// A sentinel consumed by the probe must still render its final frame, so
	// the read loop owns the finish rather than Open racing it synchronously.
	go transport.readLoop(result.remoteShutdown)
	go transport.tickLoop()
	return transport, nil
}

// announce reads the SSH channel until the MOSH CONNECT marker appears. Any
// visible bootstrap output is buffered for the terminal.
func announce(ctx context.Context, stream io.ReadWriteCloser, queue *outputQueue) (moshmodel.Connect, error) {
	connect, invalid, err := readAnnouncement(ctx, stream, queue)
	if err != nil {
		if invalid {
			return moshmodel.Connect{}, fmt.Errorf("%w: the MOSH CONNECT port or key is unusable", moshmodel.ErrAnnouncement)
		}
		if ctx.Err() != nil {
			return moshmodel.Connect{}, ctx.Err()
		}
		return moshmodel.Connect{}, mochHandshakeError(err)
	}
	return connect, nil
}

// candidateAddress is one UDP endpoint to probe. In host mode addr is the
// resolved address; in Provider mode resolution is left to the Provider and
// addr is nil.
type candidateAddress struct {
	host string
	addr *net.UDPAddr
	err  error
}

// udpLink is one client-side UDP channel to mosh-server. The socket is
// deliberately unconnected, like stock mosh: the OCB key authenticates the
// peer, and an unconnected socket keeps working when the local address
// changes, avoids ICMP-induced read errors, and accepts server replies after
// a NAT rebinding.
type udpLink struct {
	conn   *net.UDPConn
	remote *net.UDPAddr
}

func (l *udpLink) writeOne(datagram []byte) error {
	_, err := l.conn.WriteToUDP(datagram, l.remote)
	return err
}

func (l *udpLink) readFrom(buffer []byte) (int, error) {
	n, _, err := l.conn.ReadFromUDP(buffer)
	return n, err
}

func (l *udpLink) setReadDeadline(deadline time.Time) error {
	return l.conn.SetReadDeadline(deadline)
}

func (l *udpLink) close() error { return l.conn.Close() }

// connLink adapts a connected datagram flow, such as the one a Network
// Provider opens inside its virtual network, to datagramLink. The Provider
// owns resolution and family selection; this side only reads and writes
// datagrams.
type connLink struct {
	conn net.Conn
}

func (l *connLink) writeOne(datagram []byte) error {
	_, err := l.conn.Write(datagram)
	return err
}

func (l *connLink) readFrom(buffer []byte) (int, error) {
	return l.conn.Read(buffer)
}

func (l *connLink) setReadDeadline(deadline time.Time) error {
	return l.conn.SetReadDeadline(deadline)
}

func (l *connLink) close() error { return l.conn.Close() }

// datagramLink is the UDP surface the transport uses. Tests substitute a link
// whose writes fail so the network-error notification path is deterministic.
type datagramLink interface {
	writeOne(datagram []byte) error
	readFrom(buffer []byte) (int, error)
	setReadDeadline(deadline time.Time) error
	close() error
}

// probeResult is a usable UDP link plus any state the first authenticated
// datagram already carried. A server whose shell exits immediately can send
// its shutdown sentinel before the probe returns.
type probeResult struct {
	link           datagramLink
	remoteShutdown bool
}

// isClosedNetworkError reports the only send/read error that cannot be
// recovered by retransmission.
func isClosedNetworkError(err error) bool {
	return errors.Is(err, net.ErrClosed) || errors.Is(err, os.ErrClosed)
}

// probe sends the initial SSP datagram to each candidate and keeps the first
// endpoint that returns an authenticated mosh packet. The cryptographic check
// authenticates the server; the source address of a reply is not pinned,
// matching stock mosh-client. A nil dialDatagram uses an unconnected host UDP
// socket; a non-nil one opens a connected flow through a Network Provider and
// leaves resolution to it.
func probe(ctx context.Context, client *moshcore.Client, cfg moshmodel.Config,
	connect moshmodel.Connect, timeout time.Duration, renderer *terminalRenderer,
	dialDatagram func(context.Context, string, string) (net.Conn, error)) (*probeResult, error) {

	hosts := make([]string, 0, 2)
	if connect.Host != "" {
		hosts = append(hosts, connect.Host)
	}
	if cfg.Hostname != "" && !strings.EqualFold(cfg.Hostname, connect.Host) {
		hosts = append(hosts, cfg.Hostname)
	}
	if len(hosts) == 0 {
		return nil, fmt.Errorf("%w: no host to reach mosh-server", moshmodel.ErrNetwork)
	}

	candidates := make([]candidateAddress, 0, len(hosts))
	for _, host := range hosts {
		if dialDatagram != nil {
			// The Provider resolves the announcement inside its virtual
			// network, where MagicDNS and tunnel DNS names only exist.
			// Resolving on the host here would pick the wrong address or fail
			// entirely.
			candidates = append(candidates, candidateAddress{host: host})
			continue
		}
		address, err := net.ResolveUDPAddr("udp", net.JoinHostPort(host, strconv.Itoa(connect.Port)))
		candidates = append(candidates, candidateAddress{host: host, addr: address, err: err})
	}

	var lastErr error
	sent := false
	for _, candidate := range candidates {
		if candidate.err != nil {
			lastErr = candidate.err
			continue
		}
		var link datagramLink
		if dialDatagram != nil {
			// Bound the Provider dial by the same window as one endpoint
			// probe, so an unreachable tunnel cannot hold Open open and the
			// fallback address still gets its turn. The context only bounds
			// the dial; the response window is awaitResponse's own deadline.
			dialCtx, cancel := context.WithTimeout(ctx, timeout)
			conn, err := dialDatagram(dialCtx, "udp",
				net.JoinHostPort(candidate.host, strconv.Itoa(connect.Port)))
			cancel()
			if err != nil {
				lastErr = err
				continue
			}
			if conn == nil {
				lastErr = errors.New("mosh: the Network Provider returned no datagram flow")
				continue
			}
			link = &connLink{conn: conn}
		} else {
			network := "udp4"
			if candidate.addr.IP.To4() == nil {
				network = "udp6"
			}
			socket, err := net.ListenUDP(network, nil)
			if err != nil {
				lastErr = err
				continue
			}
			link = &udpLink{conn: socket, remote: candidate.addr}
		}
		sent = true
		// The first candidate sends the initial state; a fallback candidate
		// needs an immediate retransmission because the shared client may
		// have sent moments ago and its RTO may not be due yet.
		initial := client.SendDatagrams(time.Now())
		if len(initial) == 0 {
			initial = client.Retransmit(time.Now())
		}
		writeFailed := false
		for _, datagram := range initial {
			if err := link.writeOne(datagram); err != nil {
				lastErr = err
				writeFailed = true
				break
			}
		}
		if writeFailed {
			_ = link.close()
			continue
		}

		connected, err := awaitResponse(ctx, client, link, timeout, renderer, candidate.host)
		if connected != nil {
			return connected, nil
		}
		_ = link.close()
		if err != nil {
			// A protocol failure or cancellation is terminal; a candidate
			// timeout moves on to the fallback address.
			if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			if errors.Is(err, moshmodel.ErrProtocol) {
				return nil, err
			}
			lastErr = err
		}
	}
	if sent {
		if lastErr != nil {
			return nil, fmt.Errorf("%w: no response from %s: %v", moshmodel.ErrTimeout,
				strings.Join(hosts, ", "), lastErr)
		}
		return nil, fmt.Errorf("%w: no response from %s", moshmodel.ErrTimeout, strings.Join(hosts, ", "))
	}
	// Nothing was sent: every candidate failed to resolve or to open a flow.
	// That is a network failure, never a silent fall back to another path.
	if lastErr != nil {
		// Keep the Provider's typed failure in the chain: a login requirement
		// or a stopped tunnel must not degrade into a generic network error.
		return nil, fmt.Errorf("%w: reach mosh-server address: %w", moshmodel.ErrNetwork, lastErr)
	}
	return nil, fmt.Errorf("%w: resolve mosh-server address", moshmodel.ErrNetwork)
}

// awaitResponse waits for one authenticated datagram from the server. While
// waiting for each candidate it retransmits the initial SSP state every
// probeRetransmitInterval, so lost initial datagrams do not turn a reachable
// server into ErrTimeout. A non-nil result means the probe succeeded; its
// remoteShutdown flag reports a sentinel that arrived before the probe
// finished. A non-nil error is either terminal (protocol/cancellation) or a
// candidate timeout.
func awaitResponse(ctx context.Context, client *moshcore.Client, link datagramLink,
	timeout time.Duration, renderer *terminalRenderer, host string) (*probeResult, error) {

	lastRetransmit := time.Now()

	deadline := time.Now().Add(timeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	buffer := make([]byte, receiveBuffer)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, fmt.Errorf("%w: %s did not answer", moshmodel.ErrTimeout, host)
		}
		wait := remaining
		if wait > tickInterval {
			wait = tickInterval
		}
		if err := link.setReadDeadline(time.Now().Add(wait)); err != nil {
			return nil, err
		}
		n, err := link.readFrom(buffer)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			var networkError net.Error
			if errors.As(err, &networkError) && networkError.Timeout() {
				// Keep probing on a fixed cadence instead of the SSP RTO so
				// a lost initial datagram does not consume the probe window.
				now := time.Now()
				if now.Sub(lastRetransmit) >= probeRetransmitInterval {
					lastRetransmit = now
					for _, datagram := range client.Retransmit(now) {
						_ = link.writeOne(datagram)
					}
				}
				continue
			}
			return nil, fmt.Errorf("%w: %s: %v", moshmodel.ErrNetwork, host, err)
		}
		update, authenticated, receiveErr := client.Receive(buffer[:n], time.Now())
		if receiveErr != nil {
			if errors.Is(receiveErr, moshcore.ErrProtocol) {
				return nil, fmt.Errorf("%w: %w", moshmodel.ErrProtocol, receiveErr)
			}
			return nil, receiveErr
		}
		if !authenticated {
			continue
		}
		if err := renderer.applyUpdate(update); err != nil {
			return nil, fmt.Errorf("%w: %w", moshmodel.ErrProtocol, err)
		}
		if update.RemoteShutdown {
			// Acknowledge immediately; the probe caller will classify this
			// as a normal remote exit rather than discarding the sentinel.
			for _, datagram := range client.SendDatagrams(time.Now()) {
				_ = link.writeOne(datagram)
			}
		}
		// The probe polls with short read deadlines. Clear the deadline
		// before handing the socket to the long-lived read loop, which
		// otherwise observes an immediate i/o timeout.
		_ = link.setReadDeadline(time.Time{})
		return &probeResult{link: link, remoteShutdown: update.RemoteShutdown}, nil
	}
}

// inProcessTransport is one Mosh Session. Read is terminal output, Write is
// terminal input, Resize reaches mosh-server, and Close performs the mosh
// shutdown handshake.
type inProcessTransport struct {
	queue    *outputQueue
	client   *moshcore.Client
	link     datagramLink
	renderer *terminalRenderer

	// sendMu serializes SSP state changes with their UDP datagrams, so
	// fragments of different instructions never interleave.
	sendMu sync.Mutex

	mu      sync.Mutex
	cols    int
	rows    int
	closing bool
	hasEnd  bool
	end     appsession.End

	done      chan struct{}
	closeOnce sync.Once
	endOnce   sync.Once
}

func (t *inProcessTransport) Read(p []byte) (int, error) { return t.queue.Read(p) }

func (t *inProcessTransport) Write(p []byte) (int, error) {
	forwarded := t.renderer.translateInput(p)
	t.sendMu.Lock()
	if t.isClosing() {
		t.sendMu.Unlock()
		return 0, io.ErrClosedPipe
	}
	// Capture the frame the incoming bytes will travel in before the state is
	// extended. Stock's prediction overlay records this as the expiration
	// frame; it is a hint, never part of the outgoing message.
	localFrameSent := t.client.SentStateLast()
	// Record the prediction before the state carrying these bytes is sent, as
	// STMClient::process_user_input does before network->tick() sends it. If
	// the socket reader were allowed to process the server's echo first, the
	// EchoAck could reach this frame before the prediction exists and the
	// overlay would cull it without ever drawing it. Holding sendMu keeps the
	// expiration frame stable; predictUserInput takes the renderer lock, the
	// same sendMu-then-renderer order Resize and the send-error path use.
	t.renderer.predictUserInput(p, localFrameSent)
	if len(forwarded) > 0 {
		if err := t.client.Input(forwarded); err != nil {
			t.sendMu.Unlock()
			return 0, err
		}
		// A transient send failure is not a write failure: the client retains
		// the state and retransmits it, exactly as stock mosh ignores sendto()
		// errors and keeps the input in its SSP state.
		if err := t.writeDatagrams(t.client.SendDatagrams(time.Now())); err != nil {
			t.sendMu.Unlock()
			return 0, err
		}
	}
	t.sendMu.Unlock()

	// Prediction consumed the raw keystrokes stock feeds Parser::UTF8Parser;
	// the send path above used UserInput's translation and is already complete,
	// so nothing here can change the bytes on the wire.
	if err := t.renderer.emitLatest(); err != nil {
		return 0, err
	}
	return len(p), nil
}

// SetScreenSizeHandler installs the inverse resize signal the Session uses to
// resize MyGo before display bytes encoded for the new size arrive. Position
// is the display-stream offset of the resize.
func (t *inProcessTransport) SetScreenSizeHandler(handler func(cols, rows int, position uint64)) {
	t.renderer.setScreenSizeHandler(handler)
}

func (t *inProcessTransport) Resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return nil
	}
	t.mu.Lock()
	t.cols, t.rows = cols, rows
	t.mu.Unlock()

	t.sendMu.Lock()
	defer t.sendMu.Unlock()
	if t.isClosing() {
		return nil
	}
	if err := t.client.Resize(cols, rows); err != nil {
		return err
	}
	// STMClient::process_resize tells the remote emulator and then resets the
	// prediction engine, because the predicted cells' geometry just changed.
	t.renderer.resetPredictions()
	return t.writeDatagrams(t.client.SendDatagrams(time.Now()))
}

// networkErrorMessage formats a socket failure the way stock mosh does:
// Network::send stores "sendto: " + strerror(errno), and recvmsg failures are
// NetworkException("recvmsg", errno). Go's net.OpError adds the operation,
// address, and a nested syscall prefix that stock never displays.
func networkErrorMessage(operation string, err error) string {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return operation + ": " + errno.Error()
	}
	return operation + ": " + err.Error()
}

// writeDatagrams sends datagrams to the announced server address. Transient
// send failures are ignored; the client keeps the corresponding state and
// retransmits it. Only a closed socket is a transport failure.
func (t *inProcessTransport) writeDatagrams(datagrams [][]byte) error {
	for _, datagram := range datagrams {
		if err := t.link.writeOne(datagram); err != nil {
			if isClosedNetworkError(err) {
				return err
			}
			// The route is temporarily unavailable (network switch, ICMP,
			// ENOBUFS). Sending the remaining fragments would likely fail
			// too; retransmission will resend the whole instruction with a
			// fresh fragment ID. Stock stores the sendto error in
			// Network::send_error, which the main loop turns into the
			// network-error notification.
			t.renderer.setNetworkError(networkErrorMessage("sendto", err))
			return nil
		}
	}
	// Stock clears send_error whenever a send pass reports none.
	t.renderer.clearNetworkError()
	return nil
}

func (t *inProcessTransport) flush(now time.Time) error {
	t.sendMu.Lock()
	defer t.sendMu.Unlock()
	// Sends continue while closing: this is what retransmits the shutdown
	// sentinel and acknowledges a server-initiated shutdown.
	return t.writeDatagrams(t.client.SendDatagrams(now))
}

func (t *inProcessTransport) readLoop(probeShutdown bool) {
	// Emit once the transport is live. A remote resize waits here until the
	// Session registers its inverse resize signal, so display bytes encoded
	// for the new size can never overtake the resize.
	if err := t.renderer.emitInitial(); err != nil {
		t.finish(appsession.End{Reason: appsession.EndReasonError}, err)
		return
	}
	// Feed the prediction engine the same hints STMClient::process_network_input
	// gives it on every authenticated receive. The probe's state was applied
	// before this goroutine started, so without this first update a fast link
	// would keep stock's conservative initial 250ms pacing until the next
	// server state arrived.
	t.renderer.predictionNetworkHints(t.client.SentStateAcked(), t.client.SendInterval())
	// Render the state the probe already accepted, if any. This includes the
	// final frame of a shutdown sentinel consumed during the probe, which must
	// reach the queue before finish closes it.
	if err := t.renderer.emitLatest(); err != nil {
		t.finish(appsession.End{Reason: appsession.EndReasonError}, err)
		return
	}
	if probeShutdown {
		// The probe acknowledged the sentinel and captured its final state. A
		// user-initiated Close keeps its classification when the two race.
		if t.isClosing() {
			t.finish(appsession.End{Reason: appsession.EndReasonClosed}, nil)
		} else {
			t.finish(appsession.End{Reason: appsession.EndReasonExited}, nil)
		}
		return
	}

	buffer := make([]byte, receiveBuffer)
	failures := 0
	for {
		n, err := t.link.readFrom(buffer)
		if err != nil {
			if t.isFinished() || t.isClosing() {
				return
			}
			if !isClosedNetworkError(err) && failures+1 < maxReadFailures {
				// A network switch can surface a transient read error.
				// Keep the SSP state and socket alive; the tick loop keeps
				// sending until the path recovers. Stock reports the same
				// condition through its network-error notification.
				t.renderer.setNetworkError(networkErrorMessage("recvmsg", err))
				failures++
				select {
				case <-t.done:
					return
				case <-time.After(readRetryInterval):
				}
				continue
			}
			t.finish(appsession.End{Reason: appsession.EndReasonError},
				fmt.Errorf("%w: %v", moshmodel.ErrNetwork, err))
			return
		}
		failures = 0
		t.renderer.clearNetworkError()
		update, authenticated, receiveErr := t.client.Receive(buffer[:n], time.Now())
		if receiveErr != nil {
			if t.isClosing() {
				return
			}
			t.finish(appsession.End{Reason: appsession.EndReasonError},
				fmt.Errorf("%w: %w", moshmodel.ErrProtocol, receiveErr))
			return
		}
		if !authenticated {
			continue
		}
		// STMClient::process_network_input reports the send time of the oldest
		// retained sent state after every receive. Like stock, this timestamp
		// advances only when an ack prunes the sent queue.
		t.renderer.serverAcked(t.client.SentStateAckedTimestamp())
		if err := t.renderer.applyUpdate(update); err != nil {
			t.finish(appsession.End{Reason: appsession.EndReasonError},
				fmt.Errorf("%w: %w", moshmodel.ErrProtocol, err))
			return
		}
		// STMClient::process_network_input feeds the prediction engine the
		// acknowledged frame, current pacing, and EchoAck before redrawing.
		t.renderer.predictionNetworkHints(t.client.SentStateAcked(), t.client.SendInterval())
		if err := t.renderer.emitLatest(); err != nil {
			t.finish(appsession.End{Reason: appsession.EndReasonError}, err)
			return
		}
		if err := t.flush(time.Now()); err != nil {
			if t.isClosing() {
				return
			}
			t.finish(appsession.End{Reason: appsession.EndReasonError}, err)
			return
		}
		if update.RemoteShutdown {
			// The acknowledgement was just sent. mosh-server exits after
			// receiving it. A user-initiated Close keeps its own
			// classification when the two happen to race.
			if t.isClosing() {
				t.finish(appsession.End{Reason: appsession.EndReasonClosed}, nil)
			} else {
				t.finish(appsession.End{Reason: appsession.EndReasonExited}, nil)
			}
			return
		}
	}
}

func (t *inProcessTransport) tickLoop() {
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-t.done:
			return
		case now := <-ticker.C:
			if t.isFinished() {
				return
			}
			if err := t.flush(now); err != nil {
				if t.isClosing() {
					return
				}
				t.finish(appsession.End{Reason: appsession.EndReasonError}, err)
				return
			}
			// Stock interleaves output_new_frame with network ticks using
			// min(network->wait_time(), overlays.wait_time()), and caps the
			// wait at 250ms while still connecting.
			if err := t.renderer.maintainOverlays(); err != nil {
				if t.isFinished() {
					return
				}
				t.finish(appsession.End{Reason: appsession.EndReasonError}, err)
				return
			}
			if t.isClosing() && (t.client.ShutdownComplete() || t.client.ShutdownTimedOut(now)) {
				t.finish(appsession.End{Reason: appsession.EndReasonClosed}, nil)
				return
			}
		}
	}
}

// Close performs a best-effort mosh shutdown (so mosh-server notices promptly)
// and then releases the UDP socket and key material.
func (t *inProcessTransport) Close() error {
	t.closeOnce.Do(func() {
		t.mu.Lock()
		t.closing = true
		finished := t.hasEnd
		t.mu.Unlock()
		if !finished {
			t.sendMu.Lock()
			datagrams := t.client.BeginShutdown(time.Now())
			if len(datagrams) == 0 {
				datagrams = t.client.SendDatagrams(time.Now())
			}
			_ = t.writeDatagrams(datagrams)
			t.sendMu.Unlock()
			select {
			case <-t.done:
			case <-time.After(shutdownTimeout):
			}
		}
		_ = t.link.close()
		t.client.Close()
		t.finish(appsession.End{Reason: appsession.EndReasonClosed}, nil)
	})
	return nil
}

// Wait reports when the Session ended. Mosh carries no exit status; End is
// authoritative.
func (t *inProcessTransport) Wait() (int, error) {
	<-t.done
	return 0, nil
}

// End reports the first classification recorded by the transport.
func (t *inProcessTransport) End() appsession.End {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.hasEnd {
		return t.end
	}
	return appsession.End{Reason: appsession.EndReasonError}
}

// finish records the first end and releases readers and Wait. A nil cause
// closes Read with EOF; a cause is returned by Read after buffered output.
func (t *inProcessTransport) finish(end appsession.End, cause error) {
	t.endOnce.Do(func() {
		t.mu.Lock()
		t.hasEnd = true
		t.end = end
		t.mu.Unlock()
		t.renderer.close()
		t.queue.close(cause)
		close(t.done)
	})
}

func (t *inProcessTransport) isClosing() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.closing
}

func (t *inProcessTransport) isFinished() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.hasEnd
}
