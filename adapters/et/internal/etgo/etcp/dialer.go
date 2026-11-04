// SPDX-License-Identifier: Apache-2.0
// Package etcp is a reliable, ordered, encrypted packet connection to an
// etserver that survives reconnects. Upstream calls this layer EternalTCP
// (src/base/BackedReader.cpp, BackedWriter.cpp, Connection.cpp).
package etcp

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"time"

	"github.com/mslxl/natcatty/adapters/et/internal/etgo/protocol"
	"github.com/mslxl/natcatty/adapters/et/internal/etgo/seal"
	"github.com/mslxl/natcatty/adapters/et/internal/etgo/wire"
)

const (
	// defaultKeepAlive is upstream's maximum (src/base/Headers.hpp:180 at
	// et-v7.0.0).
	defaultKeepAlive = 5 * time.Second
	// defaultReplayLimit is upstream's MAX_BACKUP_BYTES
	// (src/base/BackedWriter.hpp:32 at et-v7.0.0).
	defaultReplayLimit = 64 << 20
	dialTimeout        = 10 * time.Second

	// minKeepAlive is the smallest non-zero KeepAlive Dial accepts, a policy
	// floor: much below it the watcher would probe and drop links faster
	// than a typical round trip, and a nanosecond value would spin.
	minKeepAlive = 100 * time.Millisecond
	// maxReplayLimit is the largest ReplayLimit Dial accepts: upstream's own
	// MAX_BACKUP_BYTES. It does not by itself keep a catchup under the
	// message limit (a disconnected ring can hold about twice the limit);
	// writeRecover checks the size and fails with ErrReplayExceeded.
	maxReplayLimit = defaultReplayLimit
)

// ErrSessionEnded reports that the server ended the session, normally because
// the remote shell exited (a redial got INVALID_KEY). It wraps io.EOF, the
// end-of-stream convention of net.Conn, so errors.Is(err, io.EOF) holds.
var ErrSessionEnded = fmt.Errorf("etcp: session ended by server: %w", io.EOF)

var (
	// ErrVersion reports that the server speaks another protocol version
	// (MISMATCHED_PROTOCOL), on the first connect or a redial.
	ErrVersion = errors.New("etcp: protocol version mismatch")
	// ErrRetryLater reports that the server is recovering its session table
	// (RETRY_LATER). It is not fatal: a redial after a backoff can succeed.
	ErrRetryLater = errors.New("etcp: server is recovering; retry later")
	// ErrLegacyReattach reports that a reattach (ResetIntent) reached a server
	// without the v7 challenge capability. Upstream's client fails with this
	// outcome too (ClientConnection.cpp:165-175 at et-v7.1.1).
	ErrLegacyReattach = errors.New("etcp: server does not support session reattach; upgrade etserver")
	// ErrDisplaced reports that another client took over this session or that
	// the server reset the session's history behind this client's back. It is
	// fatal: the old sequence numbers and epoch key can never be used again.
	ErrDisplaced = errors.New("etcp: session was taken over by another client")
	// ErrIntegrity reports a packet that failed authentication or could not
	// be parsed, a frame above wire.MaxFrameSize, or a handshake message
	// that is oversized or does not decode: the stream is out of step or
	// tampered with and cannot be resumed.
	ErrIntegrity = errors.New("etcp: stream integrity failure")
	// ErrReplayExceeded reports that the session cannot be resumed: the
	// peer needs packets no longer retained, is ahead of what was sent, or
	// needs a catchup too large to send in one message, or more packets
	// were received than a SequenceHeader's int32 sequence number can
	// state. A peer position past that range wraps outside the retained
	// window, so the send direction ends the same way.
	ErrReplayExceeded = errors.New("etcp: session cannot be resumed")
	// ErrRejected reports that the server refused the session: INVALID_KEY
	// on the first connect, NEW_CLIENT on a redial, or an unknown status.
	ErrRejected = errors.New("etcp: server rejected the session")
)

// A Dialer contains options for connecting to an etserver. The zero value is usable.
type Dialer struct {
	// NetDialer dials each TCP link. Nil means a net.Dialer with a 10 s timeout
	// and TCP keepalive (KeepAliveConfig: idle 15 s, interval 5 s, count 3).
	NetDialer interface {
		DialContext(ctx context.Context, network, address string) (net.Conn, error)
	}
	// KeepAlive is the quiet period after which a probe is sent; after two
	// quiet periods the link is declared dead. Inbound frames count as proof
	// of life, and so do our writes while a probe waits behind them: the
	// probe queues behind the unsent backlog, so a slow upload while the
	// server sends nothing would otherwise be taken for a dead link. Writes
	// are observed in 4 KiB pieces, so an uplink too slow to send one piece
	// in two periods (under about 400 B/s at the default) still costs a
	// reconnect, and so does data the kernel has already accepted, which
	// drains out of sight: a send buffer holding more than two periods'
	// worth ahead of the probe delays its echo past the deadline. Zero means 5 s
	// (upstream's maximum, src/base/Headers.hpp:180 at et-v7.0.0); Dial
	// refuses a negative value or one below 100 ms. Probing starts only
	// after the first WritePacket, because etserver aborts when a session's
	// first packet is not INITIAL_PAYLOAD
	// (src/terminal/TerminalServer.cpp:429-439 at et-v7.0.0); before that, a
	// dead link is detected only by a read error or by TCP keepalive when
	// the NetDialer enables it (the default one does).
	KeepAlive time.Duration
	// Probe is the packet sent as a liveness probe. The zero value sends header 0
	// with no payload, which is KEEP_ALIVE, echoed by etserver once the session
	// runs (src/terminal/TerminalServer.cpp:389-393 at et-v7.0.0).
	Probe protocol.Packet
	// ResetIntent makes the first connect ask the server for reset recovery:
	// the request carries resetIntent, and a v7 server answers RETURNING_CLIENT
	// with resetRequired and a fresh salt. Both sides then start a new epoch
	// at sequence zero under a key derived from the salt, and the caller must
	// not send INITIAL_PAYLOAD (Conn.Resumed reports this). Use it to attach to
	// an existing session with saved id and passkey, like stock `et --attach`.
	// A server without the v7 challenge capability (6.2.10) answers with a
	// legacy NEW_CLIENT/RETURNING_CLIENT and the dial fails with
	// ErrLegacyReattach, the same outcome as stock. Reconnects made by the Conn
	// itself never set resetIntent; the server selects reset recovery when it
	// has lost the session's history.
	ResetIntent bool
	// OnDisplaced, when non-nil, is called at most once if this client is
	// displaced by another client taking over the session, or if the server
	// resets the session's epoch behind its back. The Conn then fails with
	// ErrDisplaced. It runs on a Conn goroutine and must not block.
	OnDisplaced func()
	// ReplayLimit bounds the sealed bytes kept for replay; Dial refuses a
	// negative value or one above 64 MiB, and zero means 64 MiB. It is a soft
	// bound applied separately to the two kinds of retained packets: packets
	// already written to a socket are trimmed down to it, and WritePacket
	// blocks while the not-yet-written backlog exceeds it (a single packet
	// may take the backlog over, and probes are never blocked). After a
	// reconnect, written packets the server has not acknowledged are kept
	// until it first sends on the new link, up to twice ReplayLimit: the
	// server counts our catchup only once it has decoded all of it, so a
	// link lost before then must be able to replay it again. The ring can
	// therefore hold about three times ReplayLimit plus one packet. Packets
	// count as sent once written to the socket, and written packets are
	// trimmed first, so a window smaller than what the kernel and the network
	// can hold in flight turns a reconnect into ErrReplayExceeded; so does a
	// catchup too large for one handshake message. Small values are for
	// tests only.
	ReplayLimit int
	// Logger receives connection events. Nil discards them.
	Logger *slog.Logger
}

// Dial connects and completes the first handshake. It expects NEW_CLIENT; a
// RETURNING_CLIENT answer (upstream allows it when a first attempt died after
// registering, src/base/ClientConnection.cpp:35-39 at et-v7.0.0) runs the recover exchange
// with empty state. INVALID_KEY yields ErrRejected and MISMATCHED_PROTOCOL
// yields ErrVersion. A passkey that is not 32 bytes, a Probe payload too
// large for one sealed frame, or a KeepAlive or ReplayLimit outside the range
// its field documents is refused before dialing.
func (d *Dialer) Dial(ctx context.Context, addr, id, passkey string) (*Conn, error) {
	if len(passkey) != 32 {
		return nil, fmt.Errorf("etcp: passkey must be 32 bytes, got %d", len(passkey))
	}
	if len(d.Probe.Payload) > maxPayload {
		return nil, fmt.Errorf("etcp: probe payload of %d bytes: %w", len(d.Probe.Payload), wire.ErrTooLarge)
	}
	if d.KeepAlive < 0 || (d.KeepAlive > 0 && d.KeepAlive < minKeepAlive) {
		return nil, fmt.Errorf("etcp: KeepAlive %v: must be 0 (the default) or at least %v", d.KeepAlive, minKeepAlive)
	}
	if d.ReplayLimit < 0 || d.ReplayLimit > maxReplayLimit {
		return nil, fmt.Errorf("etcp: ReplayLimit %d: must be between 0 (the default) and %d", d.ReplayLimit, maxReplayLimit)
	}
	c := d.newConn(addr, id, passkey)
	nc, info, err := c.connect(ctx, true)
	if err != nil {
		c.cancel(err)
		return nil, err
	}
	if info.resumed {
		c.resumed.Store(true)
	}
	c.wg.Go(func() {
		defer close(c.readerDone)
		c.supervise(nc, info.catchup)
	})
	return c, nil
}

// Resumed reports whether Dial attached to an existing session with reset
// recovery (ResetIntent and a v7 server). When true the server has already
// started the shell, so the caller must not send INITIAL_PAYLOAD. It matches
// upstream TerminalClient::attachedToExisting.
func (c *Conn) Resumed() bool { return c.resumed.Load() }

func (d *Dialer) newConn(addr, id, passkey string) *Conn {
	var nd netDialer = d.NetDialer
	if nd == nil {
		nd = &net.Dialer{
			Timeout: dialTimeout,
			KeepAliveConfig: net.KeepAliveConfig{
				Enable:   true,
				Idle:     15 * time.Second,
				Interval: 5 * time.Second,
				Count:    3,
			},
		}
	}
	logger := d.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	c := &Conn{
		netDialer:   nd,
		addr:        addr,
		id:          id,
		keepAlive:   cmp.Or(d.KeepAlive, defaultKeepAlive),
		probe:       d.Probe,
		logger:      logger,
		resetIntent: d.ResetIntent,
		onDisplaced: d.OnDisplaced,
		ctx:         ctx,
		cancel:      cancel,
		inbox:       make(chan protocol.Packet, inboxSize),
		readerDone:  make(chan struct{}),
		wake:        make(chan struct{}, 1),
		space:       make(chan struct{}, 1),
	}
	copy(c.key[:], passkey)
	c.lastProbe = -1
	c.ring.limit = cmp.Or(d.ReplayLimit, defaultReplayLimit)
	var key [32]byte
	copy(key[:], passkey)
	c.out = seal.New(&key, seal.ClientToServer)
	c.in = seal.New(&key, seal.ServerToClient)
	return c
}

type netDialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

// connectResult is the state one successful connect leaves for the caller:
// the peer's catchup entries and whether this was an attach (reset recovery on
// the first connect).
type connectResult struct {
	catchup [][]byte
	resumed bool
}

// errRecoveryExchange marks a failure after the server accepted the connect
// handshake but the recover exchange did not complete. Unlike a dial or
// handshake failure, repeated occurrences mean the two sides disagree about
// the session's sequence history, not that the network is flaky.
var errRecoveryExchange = errors.New("etcp: recovery exchange failed")

// connect dials one TCP link and runs the connect handshake on it (plus the
// recover or reset exchange for a returning client). It returns the ready
// connection. If ctx ends during the dial or the handshake, the error wraps
// context.Cause(ctx), unless a definitive error (one isFatal accepts, such as
// the server's rejection) came first. A handshake message that is oversized or
// does not decode yields ErrIntegrity.
func (c *Conn) connect(ctx context.Context, first bool) (net.Conn, connectResult, error) {
	dctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	nc, err := c.netDialer.DialContext(dctx, "tcp", c.addr)
	if err != nil {
		if ctx.Err() != nil {
			// As for the handshake below: the caller needs its cause, not
			// the dialer's generic context error.
			return nil, connectResult{}, fmt.Errorf("etcp: dial: %w", context.Cause(ctx))
		}
		return nil, connectResult{}, fmt.Errorf("etcp: dial: %w", err)
	}
	stop := context.AfterFunc(ctx, func() { _ = nc.Close() })
	defer stop()
	info, err := c.handshake(idleConn{Conn: nc, timeout: handshakeIdle}, first)
	if err == nil && !stop() {
		// ctx ended as the handshake finished and the AfterFunc has
		// closed nc: the link is unusable.
		err = context.Cause(ctx)
	}
	if err == nil {
		err = nc.SetDeadline(time.Time{})
	}
	if err != nil {
		_ = nc.Close()
		if errors.Is(err, wire.ErrTooLarge) || errors.Is(err, wire.ErrMalformed) {
			// A handshake message from the server that is oversized or
			// does not decode means a broken or hostile peer (a cut stream
			// yields an EOF error instead); as on the stream, the session
			// cannot continue, and redialing would meet it again. Our own
			// catchup cannot reach here: writeRecover checks its size
			// first and fails with ErrReplayExceeded.
			err = fmt.Errorf("%w: %w", ErrIntegrity, err)
		}
		if ctx.Err() != nil && !isFatal(err) {
			// Ending ctx closes nc, so the handshake reports a closed
			// connection; the cause is what the caller needs. A definitive
			// answer that arrived just before (a rejection, a version
			// mismatch) is kept: it says more than the deadline does.
			return nil, connectResult{}, fmt.Errorf("etcp: connect: %w", context.Cause(ctx))
		}
		return nil, connectResult{}, err
	}
	return nc, info, nil
}

// handshake runs the connect exchange. The client always advertises the v7
// challenge capability; a 6.2.10 server ignores the unknown fields and answers
// with the legacy single response. A v7.1.1 server answers a known client with
// a challenge first, then authenticates the ConnectAuth proof before sending
// the final response with a reset-decision proof.
func (c *Conn) handshake(conn net.Conn, first bool) (connectResult, error) {
	resetIntent := first && c.resetIntent
	req := &protocol.ConnectRequest{}
	req.SetClientId(c.id)
	req.SetVersion(protocol.Version)
	req.SetResetIntent(resetIntent)
	req.SetSupportsChallenge(true)
	if err := wire.WriteMessage(conn, req); err != nil {
		return connectResult{}, fmt.Errorf("etcp: connect request: %w", err)
	}
	var firstResp protocol.ConnectResponse
	if err := wire.ReadMessage(conn, &firstResp); err != nil {
		return connectResult{}, fmt.Errorf("etcp: connect response: %w", err)
	}
	resp := &firstResp
	challenge := firstResp.GetAuthChallenge()
	if len(challenge) > 0 {
		if len(challenge) != authChallengeBytes {
			return connectResult{}, fmt.Errorf("%w: server sent a %d-byte auth challenge",
				ErrIntegrity, len(challenge))
		}
		proof, err := connectionProof(c.key[:], c.id, protocol.Version, challenge, resetIntent)
		if err != nil {
			return connectResult{}, err
		}
		auth := &protocol.ConnectAuth{}
		auth.SetProof(proof)
		if err := wire.WriteMessage(conn, auth); err != nil {
			return connectResult{}, fmt.Errorf("etcp: connect auth: %w", err)
		}
		var final protocol.ConnectResponse
		if err := wire.ReadMessage(conn, &final); err != nil {
			return connectResult{}, fmt.Errorf("etcp: connect response after auth: %w", err)
		}
		resp = &final
		switch resp.GetStatus() {
		case protocol.ConnectStatus_NEW_CLIENT, protocol.ConnectStatus_RETURNING_CLIENT:
			if resp.GetResetRequired() && len(resp.GetResetSalt()) != epochSaltBytes {
				return connectResult{}, fmt.Errorf("%w: reset decision salt is %d bytes",
					ErrIntegrity, len(resp.GetResetSalt()))
			}
			if !verifyResetDecisionProof(resp.GetResetProof(), c.key[:], c.id, protocol.Version,
				challenge, resp.GetStatus(), resp.GetResetRequired(), resp.GetResetSalt()) {
				return connectResult{}, fmt.Errorf("%w: connect success has no valid reset decision proof",
					ErrIntegrity)
			}
		}
	} else if resetIntent &&
		(firstResp.GetStatus() == protocol.ConnectStatus_NEW_CLIENT ||
			firstResp.GetStatus() == protocol.ConnectStatus_RETURNING_CLIENT) {
		// A server without the challenge capability cannot authenticate a
		// reattach or reset its sequence and nonce state. Accepting this
		// response would reuse the first connection's keystream.
		return connectResult{}, ErrLegacyReattach
	}

	// The response's error text is never quoted: it is peer-controlled, so
	// it could carry the passkey or terminal escape sequences, and upstream
	// only puts fixed sentences there (src/base/ServerConnection.cpp:55-59,
	// 93 at et-v7.1.1).
	switch resp.GetStatus() {
	case protocol.ConnectStatus_NEW_CLIENT:
		if first {
			return connectResult{}, nil
		}
		// Deliberately stricter than upstream, whose client closes the
		// socket and keeps redialing on any status other than INVALID_KEY
		// and RETURNING_CLIENT (src/base/ClientConnection.cpp:222-258 at
		// et-v7.1.1). This applies to NEW_CLIENT here and equally to
		// MISMATCHED_PROTOCOL and unknown statuses below: a server that
		// has lost the session's state or changed protocol will not answer
		// differently on the next redial.
		return connectResult{}, fmt.Errorf("%w: server answered NEW_CLIENT to a returning client", ErrRejected)
	case protocol.ConnectStatus_RETURNING_CLIENT:
		info := connectResult{}
		if resp.GetResetRequired() {
			if err := c.resetRecover(conn, resp.GetResetSalt()); err != nil {
				return connectResult{}, fmt.Errorf("%w: %w", errRecoveryExchange, err)
			}
			// Stock marks a first connect that ran reset recovery as
			// attached, so the caller skips INITIAL_PAYLOAD.
			info.resumed = first
			return info, nil
		}
		// Also possible on the first connect, when an earlier attempt died
		// after the server registered it. Running the recover exchange with
		// empty state (nothing sent, nothing received) matches what the
		// server expects after RETURNING_CLIENT
		// (src/base/ServerConnection.cpp:192-203 at et-v7.1.1).
		catchup, err := c.recover(conn)
		if err != nil {
			return connectResult{}, fmt.Errorf("%w: %w", errRecoveryExchange, err)
		}
		info.catchup = catchup
		return info, nil
	case protocol.ConnectStatus_INVALID_KEY:
		if first {
			return connectResult{}, fmt.Errorf("%w: server does not know this client", ErrRejected)
		}
		return connectResult{}, ErrSessionEnded
	case protocol.ConnectStatus_MISMATCHED_PROTOCOL:
		return connectResult{}, fmt.Errorf("%w: server does not speak protocol version %d", ErrVersion, protocol.Version)
	case protocol.ConnectStatus_RETRY_LATER:
		return connectResult{}, ErrRetryLater
	default:
		return connectResult{}, fmt.Errorf("%w: unexpected status %v", ErrRejected, resp.GetStatus())
	}
}

// isFatal reports whether a reconnect error ends the Conn instead of
// triggering a retry. Integrity failures on the stream never reach here (the
// reader ends the Conn directly); ones in a handshake message do.
func isFatal(err error) bool {
	for _, target := range []error{ErrSessionEnded, ErrVersion, ErrReplayExceeded, ErrRejected, ErrIntegrity, ErrDisplaced, ErrLegacyReattach} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}
