// SPDX-License-Identifier: Apache-2.0
package etcp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"sync"
	"time"

	"github.com/mslxl/natcatty/adapters/et/internal/etgo/protocol"
	"github.com/mslxl/natcatty/adapters/et/internal/etgo/wire"
	"google.golang.org/protobuf/proto"
)

// supervise owns reconnects: it runs a link until it dies, then redials with
// backoff and runs the next one, until the Conn ends.
func (c *Conn) supervise(nc net.Conn, catchup [][]byte) {
	var b backoff
	for {
		started := time.Now()
		cause := c.runLink(nc, catchup)
		if c.ctx.Err() != nil {
			return
		}
		c.logger.Info("etcp: link lost", "cause", cause)
		if time.Since(started) >= backoffReset {
			b.reset()
		}
		var err error
		nc, catchup, err = c.reconnect(&b)
		if err != nil {
			if errors.Is(err, ErrDisplaced) {
				c.notifyDisplaced()
			}
			c.cancel(err)
			return
		}
		c.logger.Info("etcp: link restored")
	}
}

// notifyDisplaced runs the configured callback once. It must never block: it
// runs on the supervisor or reader goroutine that is ending the Conn.
func (c *Conn) notifyDisplaced() {
	c.displacedOnce.Do(func() {
		if c.onDisplaced != nil {
			c.onDisplaced()
		}
	})
}

// failDisplaced reports displacement and ends the Conn.
func (c *Conn) failDisplaced() error {
	c.notifyDisplaced()
	return c.fail(ErrDisplaced)
}

// maxRecoveryFailures is how many consecutive authenticated recover exchanges
// may fail before a live session is treated as displaced. A server that
// accepted the handshake and then cannot serve either side's sequence history
// is not a transient network fault; another client reset the session, and its
// new keystream is why every exchange fails. It equals a few reconnect
// attempts, so a takeover by a client that has already typed enough input to
// hide the backwards sequence still surfaces.
const maxRecoveryFailures = 6

// reconnect dials until a link is recovered, the error is fatal, or the Conn
// ends. A RETRY_LATER answer is not fatal; it falls through to the next
// backoff, which is how a session survives an etserver restart while its
// terminals re-register. A server sequence outside this client's own stream,
// backwards or past what it sent (both detected in writeRecover), or
// repeated recovery-exchange failures on a live session, is fatal with
// ErrDisplaced: another client reset the session.
func (c *Conn) reconnect(b *backoff) (net.Conn, [][]byte, error) {
	recoveryFailures := 0
	for {
		if d := b.next(); d > 0 {
			t := time.NewTimer(d)
			select {
			case <-t.C:
			case <-c.ctx.Done():
				t.Stop()
				return nil, nil, context.Cause(c.ctx)
			}
		}
		nc, info, err := c.connect(c.ctx, false)
		if err == nil {
			return nc, info.catchup, nil
		}
		if isFatal(err) || c.ctx.Err() != nil {
			return nil, nil, err
		}
		if errors.Is(err, errRecoveryExchange) && c.peerSeq > 0 {
			recoveryFailures++
			if recoveryFailures >= maxRecoveryFailures {
				return nil, nil, fmt.Errorf("%w: %d consecutive recovery exchanges failed: %w",
					ErrDisplaced, recoveryFailures, err)
			}
		} else {
			recoveryFailures = 0
		}
		c.logger.Debug("etcp: reconnect attempt failed", "err", err)
	}
}

// recover runs the client side of the recover exchange and returns the peer's
// catchup entries. first reports whether this is Dial's connect rather than a
// reconnect of a session that was already running; writeRecover uses it to
// tell a first-attempt resume from a takeover of a live session. Upstream has
// each side write its SequenceHeader, read the peer's, write its
// CatchupBuffer, then read the peer's (src/base/Connection.cpp:105-143).
// Because both sides write before they read at every step, this client reads
// the peer's two messages concurrently with writing its own: done in
// sequence, the exchange deadlocks as soon as the connection cannot buffer
// what both sides write (at once over net.Pipe, and over TCP once both
// catchups exceed the socket buffers). Whichever side fails first closes
// conn to unblock the other; readerErrWins decides which error is reported.
// On success the ring holds our catchup until the server first speaks on the
// new link (see releaseHold).
func (c *Conn) recover(conn net.Conn, first bool) ([][]byte, error) {
	var (
		peer   protocol.SequenceHeader
		theirs protocol.CatchupBuffer
		gotSeq = make(chan error, 1)
		gotAll = make(chan error, 1)
		wg     sync.WaitGroup
	)
	wg.Go(func() {
		err := wire.ReadMessage(conn, &peer)
		gotSeq <- err
		if err == nil {
			err = wire.ReadMessage(conn, &theirs)
		}
		if err != nil {
			// Unblock the writer: upstream reads our messages only after
			// writing its own, so ours may be stuck until the idle timeout.
			_ = conn.Close()
		}
		gotAll <- err
	})

	snap, err := c.writeRecover(conn, gotSeq, &peer, first)
	if err != nil {
		_ = conn.Close() // unblock the reader
	}
	rerr := <-gotAll
	wg.Wait()
	if rerr != nil && (err == nil || readerErrWins(err, rerr)) {
		return nil, fmt.Errorf("etcp: read recover message: %w", rerr)
	}
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.unsent -= c.ring.bytesBetween(c.flushed, snap)
	c.flushed = snap
	// The server counts our catchup only once it has decoded the whole
	// message, so until it speaks on this link the next SequenceHeader may
	// ask for it again: hold everything from the position it acknowledged.
	c.ring.held, c.ring.hold = true, int64(peer.GetSequenceNumber())
	// Trim as writeLoop does after a Write: a link that dies before its
	// first Write would otherwise leave the catchup in the ring while
	// WritePacket admits another ReplayLimit.
	room := c.releaseLocked()
	c.mu.Unlock()
	if room {
		signal(c.space)
	}
	c.afterRecovery.Store(true)
	return theirs.GetBuffer(), nil
}

// resetRecover runs the v7 reset exchange after the authenticated handshake
// selected it. Both sides echo the salted decision in their SequenceHeader,
// exchange empty catchup buffers, then start a new epoch at sequence zero under
// BLAKE2b-256(baseKey, salt) (src/base/Connection.cpp:128-168 and
// src/base/ClientConnection.cpp:96-104 at et-v7.1.1). Upstream writes each
// message before reading the peer's; the messages here are empty, so a
// sequential exchange cannot deadlock.
func (c *Conn) resetRecover(conn net.Conn, salt []byte) error {
	if len(salt) != epochSaltBytes {
		return fmt.Errorf("%w: reset salt is %d bytes", ErrIntegrity, len(salt))
	}
	mine := &protocol.SequenceHeader{}
	mine.SetSequenceNumber(0)
	mine.SetReset(true)
	mine.SetResetSalt(salt)
	if err := wire.WriteMessage(conn, mine); err != nil {
		return fmt.Errorf("etcp: write reset sequence: %w", err)
	}
	var peer protocol.SequenceHeader
	if err := wire.ReadMessage(conn, &peer); err != nil {
		return fmt.Errorf("etcp: read reset sequence: %w", err)
	}
	if !peer.GetReset() || !bytes.Equal(peer.GetResetSalt(), salt) {
		return fmt.Errorf("%w: server did not echo the negotiated reset salt", ErrIntegrity)
	}
	if err := wire.WriteMessage(conn, &protocol.CatchupBuffer{}); err != nil {
		return fmt.Errorf("etcp: write empty catchup: %w", err)
	}
	var theirs protocol.CatchupBuffer
	if err := wire.ReadMessage(conn, &theirs); err != nil {
		return fmt.Errorf("etcp: read empty catchup: %w", err)
	}
	// Reset recovery promises empty catchups. A non-empty one contradicts the
	// authenticated decision and must not be replayed under the new epoch.
	if len(theirs.GetBuffer()) != 0 {
		return fmt.Errorf("%w: server sent %d catchup packets during reset recovery",
			ErrIntegrity, len(theirs.GetBuffer()))
	}

	c.mu.Lock()
	if err := c.out.Rekey(salt); err != nil {
		c.mu.Unlock()
		return fmt.Errorf("etcp: reset outbound epoch: %w", err)
	}
	limit := c.ring.limit
	c.ring = ring{limit: limit}
	c.flushed, c.unsent, c.lastProbe = 0, 0, -1
	c.mu.Unlock()
	signal(c.space) // writers may have been waiting on the old backlog

	if err := c.in.Rekey(salt); err != nil {
		return fmt.Errorf("etcp: reset inbound epoch: %w", err)
	}
	c.recvSeq = 0
	c.peerSeq = 0
	c.afterRecovery.Store(true)
	c.logger.Info("etcp: reset recovery complete")
	return nil
}

// readerErrWins reports whether the recover reader's error rerr, rather than
// the writer's err, explains a failed exchange. It does when rerr marks a
// bad message from the server (which connect turns into ErrIntegrity): over
// TCP the same bad peer may also reset the connection, and the writer's
// reset error can arrive before the reader's close takes effect. It also
// does when the writer merely hit the conn that the reader's failure
// closed. Otherwise the writer failed on its own (an I/O error, or
// ErrReplayExceeded, which is fatal either way) and its error stands.
func readerErrWins(err, rerr error) bool {
	if errors.Is(rerr, wire.ErrTooLarge) || errors.Is(rerr, wire.ErrMalformed) {
		return true
	}
	return errors.Is(err, net.ErrClosed) || errors.Is(err, io.ErrClosedPipe)
}

// maxCatchupSize is the largest CatchupBuffer we send: etserver refuses
// handshake messages above 128 MiB (src/base/SocketHandler.hpp:60 at
// et-v7.0.0), the same bound as wire.MaxMessageSize. It is a variable only
// so a test can lower it.
var maxCatchupSize = wire.MaxMessageSize

// writeRecover writes our half of the recover exchange and returns the
// sequence number the new link starts sending from. It fails with
// ErrReplayExceeded when the peer's position is outside the retained window
// or our catchup is too large for one message, and also when we have
// received more packets than SequenceHeader's int32 sequence_number can
// state (internal/protocol ET.pb.go), rather than send a wrapped negative
// count. On a reconnect, a peer position past our own send count is
// ErrDisplaced: another client's reset made the server's reader count its
// packets. The peer's position needs no int32 guard: a position truncated to
// int32 lies at least 2^31 packets below its true value, while the retained
// window holds at most a few ReplayLimits of bytes, far fewer packets, so
// ring.since refuses it.
func (c *Conn) writeRecover(conn net.Conn, gotSeq <-chan error, peer *protocol.SequenceHeader, first bool) (int64, error) {
	if c.recvSeq > math.MaxInt32 {
		return 0, fmt.Errorf("%w: received %d packets, more than the protocol's int32 sequence number can express",
			ErrReplayExceeded, c.recvSeq)
	}
	mine := &protocol.SequenceHeader{}
	mine.SetSequenceNumber(int32(c.recvSeq))
	if err := wire.WriteMessage(conn, mine); err != nil {
		return 0, fmt.Errorf("etcp: write sequence: %w", err)
	}
	if err := <-gotSeq; err != nil {
		return 0, fmt.Errorf("etcp: read sequence: %w", err)
	}
	if peer.GetReset() || len(peer.GetResetSalt()) != 0 {
		// The server may send a reset header only when the authenticated
		// handshake selected reset recovery, which this exchange is not.
		return 0, fmt.Errorf("%w: unexpected reset request during recovery", ErrIntegrity)
	}

	// Read ring.next(), the send sequence, exactly once. Packets written
	// after this snapshot are not in our catchup; the new link sends them
	// because it starts at snap.
	c.mu.Lock()
	snap := c.ring.next()
	peerSeq := int64(peer.GetSequenceNumber())
	if peerSeq < c.peerSeq {
		c.mu.Unlock()
		// The ordered stream never un-receives a packet, so a lower count
		// means the server's reader was reset without telling this client.
		// Only another client's reset recovery does that.
		return 0, fmt.Errorf("%w: server sequence went backwards from %d to %d",
			ErrDisplaced, c.peerSeq, peerSeq)
	}
	c.peerSeq = peerSeq
	if peerSeq > snap {
		c.mu.Unlock()
		// The server has received more packets than this client ever sent.
		// On a reconnect that is impossible for this client's own stream, so
		// the reader was reset and advanced by another client's reset
		// recovery: the sleeping-victim takeover. Report it as displacement
		// so the Session stops instead of reconnecting over the new owner.
		// On Dial's first connect there is no running session to lose, and
		// an earlier attempt cannot have sent anything for this Conn, so keep
		// the replay error: Dial reports it as a failed handshake.
		//
		// One takeover shape remains invisible to these counters: a foreign
		// reset whose reader has not counted a packet yet reports zero, and
		// a client whose own peerSeq is also zero sees no backwards move.
		// peerSeq is zero before INITIAL_RESPONSE and stays zero for the
		// whole epoch opened by resetRecover, since only this exchange
		// records the peer's count; a second reset that counts nothing is
		// therefore invisible to a reset-attached victim. No sequence field
		// distinguishes it: the reconnect is attempted, and once the server
		// speaks, the first frame after recovery fails to open under the old
		// epoch and ends as displaced (see afterRecovery). If it never
		// speaks, the replay cannot be decrypted and the link is retried.
		if !first {
			return 0, fmt.Errorf("%w: server received %d packets, this client sent %d",
				ErrDisplaced, peerSeq, snap)
		}
		return 0, fmt.Errorf("%w: server received %d packets, this client sent %d",
			ErrReplayExceeded, peerSeq, snap)
	}
	ours, ok := c.ring.since(peerSeq, snap)
	windowStart := c.ring.first
	c.mu.Unlock()
	if !ok {
		return 0, fmt.Errorf("%w: peer is at %d, retained window is [%d, %d]",
			ErrReplayExceeded, peer.GetSequenceNumber(), windowStart, snap)
	}
	cb := &protocol.CatchupBuffer{}
	cb.SetBuffer(ours)
	// A catchup the server cannot accept would fail the same way on every
	// redial, so it ends the Conn instead.
	if size := proto.Size(cb); size > maxCatchupSize {
		return 0, fmt.Errorf("%w: catchup of %d bytes exceeds the %d-byte message limit",
			ErrReplayExceeded, size, maxCatchupSize)
	}
	if err := wire.WriteMessage(conn, cb); err != nil {
		return 0, fmt.Errorf("etcp: write catchup: %w", err)
	}
	return snap, nil
}
