// SPDX-License-Identifier: GPL-3.0-or-later

// In-process Mosh transport. It reuses the SSH bootstrap from the bundled
// adapter, then speaks mosh's UDP protocol directly through internal/mosh
// instead of launching a bundled mosh-client.
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
	"time"

	appsession "github.com/mslxl/natcatty/application/session"
	moshmodel "github.com/mslxl/natcatty/domain/model/mosh"
	moshcore "github.com/mslxl/natcatty/internal/mosh"
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
)

// InProcessFactory opens Mosh Sessions without a bundled client binary. The
// SSH bootstrap, handshake, route checks, and credential handling are shared
// with the bundled adapter; the UDP protocol is pure Go.
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

	probeTimeout := f.ProbeTimeout
	if probeTimeout <= 0 {
		probeTimeout = DefaultProbeTimeout
	}
	// The handshake banner is already queued. Reset the viewport before the
	// probe can surface the first server frame, so that frame survives.
	if err := queue.push([]byte(viewportReset)); err != nil {
		client.Close()
		return nil, err
	}
	conn, err := probe(ctx, client, cfg, connect, probeTimeout, queue)
	if err != nil {
		client.Close()
		return nil, err
	}

	transport := &inProcessTransport{
		queue:  queue,
		client: client,
		conn:   conn,
		cols:   cfg.Cols,
		rows:   cfg.Rows,
		done:   make(chan struct{}),
	}
	go transport.readLoop()
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

// candidateAddress is one resolved UDP endpoint to probe.
type candidateAddress struct {
	host string
	addr *net.UDPAddr
	err  error
}

// probe sends the initial SSP datagram to each candidate and keeps the first
// endpoint that returns an authenticated mosh packet. Packets are accepted
// only from the connected endpoint; the cryptographic check authenticates the
// server.
func probe(ctx context.Context, client *moshcore.Client, cfg moshmodel.Config,
	connect moshmodel.Connect, timeout time.Duration, queue *outputQueue) (*net.UDPConn, error) {

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
		address, err := net.ResolveUDPAddr("udp", net.JoinHostPort(host, strconv.Itoa(connect.Port)))
		candidates = append(candidates, candidateAddress{host: host, addr: address, err: err})
	}

	initial := client.SendDatagrams(time.Now())
	var lastErr error
	resolved := false
	for _, candidate := range candidates {
		if candidate.err != nil {
			lastErr = candidate.err
			continue
		}
		resolved = true
		network := "udp4"
		if candidate.addr.IP.To4() == nil {
			network = "udp6"
		}
		conn, err := net.DialUDP(network, nil, candidate.addr)
		if err != nil {
			lastErr = err
			continue
		}
		writeFailed := false
		for _, datagram := range initial {
			if _, err := conn.Write(datagram); err != nil {
				lastErr = err
				writeFailed = true
				break
			}
		}
		if writeFailed {
			_ = conn.Close()
			continue
		}

		connected, err := awaitResponse(ctx, client, conn, timeout, queue, candidate.host)
		if connected != nil {
			return connected, nil
		}
		_ = conn.Close()
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
	if resolved {
		if lastErr != nil {
			return nil, fmt.Errorf("%w: no response from %s: %v", moshmodel.ErrTimeout,
				strings.Join(hosts, ", "), lastErr)
		}
		return nil, fmt.Errorf("%w: no response from %s", moshmodel.ErrTimeout, strings.Join(hosts, ", "))
	}
	if lastErr != nil {
		return nil, fmt.Errorf("%w: resolve mosh-server address: %v", moshmodel.ErrNetwork, lastErr)
	}
	return nil, fmt.Errorf("%w: resolve mosh-server address", moshmodel.ErrNetwork)
}

// awaitResponse waits for one authenticated datagram from the connected
// server. A non-nil conn means the probe succeeded. A non-nil error is either
// terminal (protocol/cancellation) or a candidate timeout.
func awaitResponse(ctx context.Context, client *moshcore.Client, conn *net.UDPConn,
	timeout time.Duration, queue *outputQueue, host string) (*net.UDPConn, error) {

	deadline := time.Now().Add(timeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	buffer := make([]byte, receiveBuffer)
	for {
		if err := conn.SetReadDeadline(deadline); err != nil {
			return nil, err
		}
		n, err := conn.Read(buffer)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			var networkError net.Error
			if errors.As(err, &networkError) && networkError.Timeout() {
				return nil, fmt.Errorf("%w: %s did not answer", moshmodel.ErrTimeout, host)
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
		if len(update.Output) > 0 {
			if err := queue.push(update.Output); err != nil {
				return nil, err
			}
		}
		return conn, nil
	}
}

// inProcessTransport is one Mosh Session. Read is terminal output, Write is
// terminal input, Resize reaches mosh-server, and Close performs the mosh
// shutdown handshake.
type inProcessTransport struct {
	queue  *outputQueue
	client *moshcore.Client
	conn   *net.UDPConn

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
	t.sendMu.Lock()
	defer t.sendMu.Unlock()
	if t.isClosing() {
		return 0, io.ErrClosedPipe
	}
	if err := t.client.Input(p); err != nil {
		return 0, err
	}
	datagrams := t.client.SendDatagrams(time.Now())
	if err := t.writeDatagrams(datagrams); err != nil {
		return 0, err
	}
	return len(p), nil
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
	return t.writeDatagrams(t.client.SendDatagrams(time.Now()))
}

func (t *inProcessTransport) writeDatagrams(datagrams [][]byte) error {
	for _, datagram := range datagrams {
		if _, err := t.conn.Write(datagram); err != nil {
			return err
		}
	}
	return nil
}

func (t *inProcessTransport) flush(now time.Time) error {
	t.sendMu.Lock()
	defer t.sendMu.Unlock()
	// Sends continue while closing: this is what retransmits the shutdown
	// sentinel and acknowledges a server-initiated shutdown.
	return t.writeDatagrams(t.client.SendDatagrams(now))
}

func (t *inProcessTransport) readLoop() {
	buffer := make([]byte, receiveBuffer)
	for {
		n, err := t.conn.Read(buffer)
		if err != nil {
			if !t.isFinished() && !t.isClosing() {
				t.finish(appsession.End{Reason: appsession.EndReasonError}, fmt.Errorf("%w: %v", moshmodel.ErrNetwork, err))
			}
			return
		}
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
		if len(update.Output) > 0 {
			if err := t.queue.push(update.Output); err != nil {
				t.finish(appsession.End{Reason: appsession.EndReasonError}, err)
				return
			}
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
		_ = t.conn.Close()
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
