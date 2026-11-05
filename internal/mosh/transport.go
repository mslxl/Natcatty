// SPDX-License-Identifier: GPL-3.0-or-later

// Package mosh is the pure-Go mosh protocol core: OCB3 encryption, datagram
// and fragment framing, protobuf instructions, and the client half of the
// State Synchronization Protocol. It never touches the network or a terminal;
// adapters/mosh supplies UDP and the Session transport.
package mosh

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"
)

const (
	// directionClientToServer is the client's direction bit in the nonce.
	directionClientToServer uint64 = 0
	// directionServerToClient is the server's direction bit in the nonce.
	directionServerToClient uint64 = 1 << 63
	sequenceMask                   = math.MaxInt64

	// ShutdownNum is the sentinel state number mosh uses to shut down.
	ShutdownNum = math.MaxUint64

	// Datagram layout: 8-byte sequence/direction, 16-byte OCB tag, 4-byte
	// timestamps, 10-byte fragment header, and at most 1178 bytes of
	// compressed instruction. Stock mosh budgets 1216 bytes of UDP payload
	// for IPv6 (the 1280-byte minimum MTU minus 64 bytes of headers), so
	// 8+16+4+10+1178 = 1216 exactly. The tracer's old 1180 exceeded that
	// budget by two bytes and could be fragmented by IPv6 routers.
	maxFragmentPayload = 1178
	minDatagramSize    = 8 + 16 + 4 + fragmentHeaderSize
	receiveBufferSize  = 2048

	// ACK_DELAY and ACK_INTERVAL match stock mosh's transportsender.h. Data
	// acks are held briefly so a response can piggyback, and an idle peer is
	// probed with an empty ack every three seconds.
	ackDelay    = 100 * time.Millisecond
	ackInterval = 3000 * time.Millisecond
	// SEND_INTERVAL_MIN and SEND_INTERVAL_MAX bound stock's frame pacing.
	// They drive shutdown retransmission, where mosh trades reliability for
	// a prompt exit rather than waiting for the RTO.
	sendIntervalMin = 20 * time.Millisecond
	sendIntervalMax = 250 * time.Millisecond
	minRTO          = 50 * time.Millisecond
	maxRTO          = 1000 * time.Millisecond
	// shutdownRetries and shutdownWindow bound the local shutdown handshake,
	// matching stock's SHUTDOWN_RETRIES and ACTIVE_RETRY_TIMEOUT.
	shutdownRetries = 16
	shutdownWindow  = 10 * time.Second
)

// ErrProtocol reports an authenticated peer that violated the protocol. It is
// distinct from packet loss or a failed authentication check, which are
// ignored.
var ErrProtocol = errors.New("mosh: protocol error")

// ErrClosed reports use of a closed client.
var ErrClosed = errors.New("mosh: client is closed")

// ErrKey reports an unusable mosh key.
var ErrKey = errors.New("mosh: invalid key")

// Update is the result of receiving one datagram: newly available terminal
// output and state-machine flags.
type Update struct {
	// Output is the host bytes not yet delivered, in order. It is a
	// convenience for consumers that only append output; a terminal state
	// machine must use HostStates instead because mosh's prospective resend
	// can rewind the reference state.
	Output []byte
	// HostStates contains every state accepted from this datagram, including
	// out-of-order states retained for a future diff. A terminal model applies
	// each state's Instructions to a copy of the state named by OldNum.
	HostStates []HostState
	// RemoteShutdown reports that the server sent the shutdown sentinel. The
	// caller should send one more datagram carrying the acknowledgement and
	// then end the Session as a normal exit.
	RemoteShutdown bool
}

// HostState is one remote state accepted from a TransportInstruction. It is
// the information a terminal model needs to mirror Transport::received_states
// without inferring state boundaries from concatenated host bytes.
type HostState struct {
	// Num is the new state number.
	Num uint64
	// OldNum is the state this diff applies to.
	OldNum uint64
	// ThrowawayNum is the oldest state the sender still needs. Snapshots below
	// it may be discarded.
	ThrowawayNum uint64
	// Instructions is the diff that produced this state from OldNum, in order.
	Instructions []HostInstruction
	// Newest reports that this state was appended at the end of the received
	// queue. Only a newest state may be drawn; an out-of-order state is kept
	// solely as a reference for a later diff.
	Newest bool
}

type sentState struct {
	num      uint64
	at       time.Time
	inflight bool
}

type receivedState struct {
	num          uint64
	at           time.Time
	instructions []HostInstruction
}

// Client is the client half of one mosh SSP session. All methods are safe for
// concurrent use. Time is supplied by the caller so tests need no clock.
type Client struct {
	mu     sync.Mutex
	ocb    *OCB
	closed bool
	err    error

	// Outgoing (user) state. sent[0] is the acknowledged base; sent[len-1] is
	// the newest state. Each snapshot is the complete instruction list since
	// the initial empty state.
	current []UserInstruction
	sent    []sentStateSnapshot
	acked   uint64

	// Incoming (host) state, sorted by number: received[0] is the retained
	// base, received[len-1] is the highest state.
	received    []receivedState
	ackNum      uint64
	lastAckSent uint64
	pendingAck  bool
	// ackDue is when a delayed data ack becomes due. It is only meaningful
	// while pendingAck is set; the shutdown sentinel bypasses the delay.
	ackDue time.Time
	// delivered counts HostInstructions already forwarded to the terminal from
	// the newest delivered state. This tracer-bullet model assumes host states
	// extend the previously delivered one; mosh's prospective resend can rewind
	// old_num, which needs a terminal-state diff layer to reconstruct output.
	delivered     int
	quenchUntil   time.Time
	lastSend      time.Time
	lastReceive   time.Time
	haveTimestamp bool
	savedTS       uint16
	savedTSAt     time.Time

	// Datagram sequence numbers, independent of SSP state numbers.
	seqOut     uint64
	seqInMax   uint64
	seqInSet   bool
	fragment   fragmentAssembler
	fragmentID uint64

	// RTT estimation.
	srtt    time.Duration
	rttvar  time.Duration
	rto     time.Duration
	rttInit bool

	// Shutdown.
	shutdown              bool
	shutdownStarted       time.Time
	shutdownTries         int
	sentinelSent          bool
	shutdownAcked         bool
	remoteShutdown        bool
	remoteShutdownAckSent bool
}

type sentStateSnapshot struct {
	sentState
	instructions []UserInstruction
}

// NewClient creates a client from a decoded 16-byte mosh key and the initial
// terminal size. The key is copied into the cipher; the caller should zero its
// slice. Close zeroizes the cipher's copy.
func NewClient(key []byte, cols, rows int) (*Client, error) {
	ocb, err := NewOCB(key)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrKey, err)
	}
	now := time.Now()
	client := &Client{
		ocb:      ocb,
		sent:     []sentStateSnapshot{{sentState: sentState{num: 0, at: now}}},
		received: []receivedState{{num: 0, at: now}},
		rto:      maxRTO,
		srtt:     1000 * time.Millisecond,
		rttvar:   500 * time.Millisecond,
		fragment: newFragmentAssembler(),
	}
	if cols > 0 && rows > 0 {
		client.current = append(client.current, UserInstruction{
			Resize: true,
			Width:  int32(cols),
			Height: int32(rows),
		})
	}
	return client, nil
}

// Close zeroizes key material and rejects further use. It is idempotent.
func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	c.ocb.Close()
	c.current = nil
	for i := range c.sent {
		c.sent[i].instructions = nil
	}
	c.received = nil
}

// Input appends keystrokes to the outgoing state. An empty slice is ignored.
func (c *Client) Input(data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.usable(); err != nil {
		return err
	}
	if len(data) == 0 || c.shutdown || c.remoteShutdown {
		return nil
	}
	c.current = append(c.current, UserInstruction{Keys: append([]byte(nil), data...)})
	return nil
}

// Resize appends a terminal resize to the outgoing state.
func (c *Client) Resize(cols, rows int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.usable(); err != nil {
		return err
	}
	if cols <= 0 || rows <= 0 || c.shutdown || c.remoteShutdown {
		return nil
	}
	current := c.current
	if len(current) > 0 && current[len(current)-1].Resize &&
		current[len(current)-1].Width == int32(cols) && current[len(current)-1].Height == int32(rows) {
		return nil
	}
	c.current = append(c.current, UserInstruction{Resize: true, Width: int32(cols), Height: int32(rows)})
	return nil
}

// SendDatagrams returns the datagrams due at now. It may return nil. The
// caller writes each datagram to the server in order.
func (c *Client) SendDatagrams(now time.Time) [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.err != nil {
		return nil
	}

	if c.shutdown {
		if c.shutdownAcked || c.shutdownTimedOutLocked(now) {
			return nil
		}
		if c.remoteShutdown && c.remoteShutdownAckSent {
			return nil
		}
		// Stock retransmits the sentinel at its frame rate rather than the
		// RTO, so a user close has several chances inside the adapter's one
		// second shutdown window.
		if c.sentinelSent && now.Sub(c.lastSend) < c.sendIntervalLocked() {
			return nil
		}
		return c.sendShutdownLocked(now)
	}

	haveNew := !userInstructionsEqual(c.current, c.sent[len(c.sent)-1].instructions)
	if haveNew {
		return c.sendStateLocked(now, false)
	}
	if c.ackDueLocked(now) || now.Sub(c.lastSend) >= ackInterval {
		// Stock sends the diff from the state it assumes the peer has. When
		// that assumption has fallen behind the newest sent state (no ack
		// within the RTO window), an empty ack would reference a state the
		// peer may never have received and its acknowledgement would be
		// dropped. Resend the real diff instead so the ack gets through.
		if c.assumedReceiverIndexLocked(now) != len(c.sent)-1 {
			return c.sendStateLocked(now, false)
		}
		return c.sendStateLocked(now, true)
	}
	newest := c.sent[len(c.sent)-1]
	if newest.num > c.acked && now.Sub(c.lastSend) >= c.rto {
		return c.sendRetransmitLocked(now)
	}
	return nil
}

// ackDueLocked reports whether the peer is waiting for an acknowledgement.
// Stock delays a data ack by ACK_DELAY so the next outgoing diff can carry
// it, but acknowledges the shutdown sentinel immediately and bypasses the
// delay when the ack itself is a shutdown.
func (c *Client) ackDueLocked(now time.Time) bool {
	if c.ackNum == c.lastAckSent {
		return false
	}
	if c.ackNum == ShutdownNum {
		return true
	}
	if !c.pendingAck {
		return false
	}
	return !c.ackDue.IsZero() && !now.Before(c.ackDue)
}

// sendIntervalLocked is stock's clamp(SRTT/2, SEND_INTERVAL_MIN,
// SEND_INTERVAL_MAX).
func (c *Client) sendIntervalLocked() time.Duration {
	interval := c.srtt / 2
	if interval < sendIntervalMin {
		interval = sendIntervalMin
	}
	if interval > sendIntervalMax {
		interval = sendIntervalMax
	}
	return interval
}

// Retransmit sends the newest unacknowledged state immediately, without
// waiting for the RTO. The connection probe uses it when it moves to a
// fallback address, where the shared client may have sent moments before and
// an ordinary SendDatagrams call would return nothing.
func (c *Client) Retransmit(now time.Time) [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.err != nil {
		return nil
	}
	if c.shutdown {
		return c.sendShutdownLocked(now)
	}
	if len(c.sent) > 1 {
		return c.sendRetransmitLocked(now)
	}
	if !userInstructionsEqual(c.current, c.sent[0].instructions) {
		return c.sendStateLocked(now, false)
	}
	return c.sendStateLocked(now, true)
}

// BeginShutdown starts the mosh shutdown handshake and returns the first
// datagram set. Further SendDatagrams calls retransmit until the server
// acknowledges the sentinel or the shutdown times out.
func (c *Client) BeginShutdown(now time.Time) [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.err != nil || c.shutdown {
		return nil
	}
	c.shutdown = true
	c.shutdownStarted = now
	if c.remoteShutdown {
		c.shutdownAcked = true
		return nil
	}
	return c.sendShutdownLocked(now)
}

func (c *Client) sendShutdownLocked(now time.Time) [][]byte {
	if c.sentinelSent {
		// Retransmit the sentinel without advancing the state number.
		oldNum := c.sent[0].num
		return c.finishSendLocked(now, oldNum, ShutdownNum, nil, true)
	}
	c.sentinelSent = true
	c.sent = append(c.sent, sentStateSnapshot{
		sentState:    sentState{num: ShutdownNum, at: now, inflight: true},
		instructions: cloneUserInstructions(c.current),
	})
	oldNum := c.sent[0].num
	return c.finishSendLocked(now, oldNum, ShutdownNum, nil, true)
}

// RemoteShutdown reports whether the server sent the shutdown sentinel.
func (c *Client) RemoteShutdown() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.remoteShutdown
}

// CounterpartyShutdownAcknowledged reports whether a datagram acknowledging
// the server's shutdown sentinel has been sent.
func (c *Client) CounterpartyShutdownAcknowledged() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.remoteShutdownAckSent
}

// SentStateAckedTimestamp returns the send time of the oldest retained sent
// state, exactly as TransportSender::get_sent_state_acked_timestamp() returns
// sent_states.front().timestamp. It advances only when an acknowledgement
// prunes the sent queue, not when a datagram arrives.
func (c *Client) SentStateAckedTimestamp() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.sent) == 0 {
		return time.Time{}
	}
	return c.sent[0].at
}

// ShutdownComplete reports whether the server acknowledged our shutdown
// sentinel.
func (c *Client) ShutdownComplete() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.shutdown && c.shutdownAcked
}

// ShutdownTimedOut reports whether the local shutdown handshake gave up.
func (c *Client) ShutdownTimedOut(now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.shutdown && c.shutdownTimedOutLocked(now)
}

func (c *Client) shutdownTimedOutLocked(now time.Time) bool {
	return c.shutdown && !c.shutdownAcked &&
		(c.shutdownTries >= shutdownRetries || now.Sub(c.shutdownStarted) >= shutdownWindow)
}

// Err returns the fatal protocol error, if any.
func (c *Client) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// sendStateLocked builds one instruction. When emptyAck is true the caller
// has no new user instructions; the state number advances with the current
// contents and the wire diff is empty, matching mosh's send_empty_ack. The
// empty diff is essential: rebuilding a diff from an older base under a new
// state number would replay user input the peer may already have applied when
// an acknowledgement was lost. When emptyAck is false a genuinely new state
// is sent as a diff from the assumed receiver state.
func (c *Client) sendStateLocked(now time.Time, emptyAck bool) [][]byte {
	newest := c.sent[len(c.sent)-1]
	target := c.current

	if emptyAck {
		newNum := newest.num + 1
		c.sent = append(c.sent, sentStateSnapshot{
			sentState:    sentState{num: newNum, at: now, inflight: true},
			instructions: cloneUserInstructions(target),
		})
		c.trimSentLocked()
		return c.finishSendLocked(now, newest.num, newNum, nil, false)
	}

	newNum := newest.num + 1

	// The assumed receiver state must be chosen before the new state joins
	// the history; otherwise the sender would always assume the peer already
	// has the state it is about to send and emit an empty diff.
	baseIndex := c.assumedReceiverIndexLocked(now)
	base := c.sent[baseIndex]

	c.sent = append(c.sent, sentStateSnapshot{
		sentState:    sentState{num: newNum, at: now, inflight: true},
		instructions: cloneUserInstructions(target),
	})
	c.trimSentLocked()

	diff := marshalUserDiff(base.instructions, target)
	return c.finishSendLocked(now, base.num, newNum, diff, false)
}

// sendRetransmitLocked resends the newest state from the oldest retained base
// after a timeout, guaranteeing recovery even if an optimistic base was lost.
func (c *Client) sendRetransmitLocked(now time.Time) [][]byte {
	newest := c.sent[len(c.sent)-1]
	base := c.sent[0]
	diff := marshalUserDiff(base.instructions, newest.instructions)
	return c.finishSendLocked(now, base.num, newest.num, diff, false)
}

func (c *Client) finishSendLocked(now time.Time, oldNum, newNum uint64, diff []byte, shutdown bool) [][]byte {
	instruction := TransportInstruction{
		ProtocolVersion: ProtocolVersion,
		OldNum:          oldNum,
		NewNum:          newNum,
		AckNum:          c.ackNum,
		ThrowawayNum:    c.sent[0].num,
		Diff:            diff,
	}
	datagrams, err := c.encodeInstructionLocked(instruction, now)
	if err != nil {
		c.err = err
		return nil
	}
	c.lastSend = now
	c.lastAckSent = c.ackNum
	c.pendingAck = false
	c.ackDue = time.Time{}
	if shutdown {
		c.shutdownTries++
	}
	if c.remoteShutdown && instruction.AckNum == ShutdownNum {
		c.remoteShutdownAckSent = true
	}
	return datagrams
}

func (c *Client) assumedReceiverIndexLocked(now time.Time) int {
	index := 0
	for i := 1; i < len(c.sent); i++ {
		if c.sent[i].num == ShutdownNum {
			break
		}
		if now.Sub(c.sent[i].at) < c.rto+ackDelay {
			index = i
			continue
		}
		break
	}
	return index
}

func (c *Client) trimSentLocked() {
	if len(c.sent) <= 64 {
		return
	}
	copy(c.sent[1:], c.sent[2:])
	c.sent = c.sent[:len(c.sent)-1]
}

// Receive processes one wire datagram. authenticated reports a well-formed,
// authenticated mosh datagram, even when it carried no new state (a fragment
// of a larger instruction is still proof of life). A nil error with
// authenticated=false means the datagram was empty, belonged to the other
// direction, or failed authentication. Reordered and duplicate datagrams are
// still fed to fragment reassembly and the SSP, which ignores state numbers it
// has already seen.
func (c *Client) Receive(wire []byte, now time.Time) (update Update, authenticated bool, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return Update{}, false, ErrClosed
	}
	if c.err != nil {
		return Update{}, false, c.err
	}
	if len(wire) < minDatagramSize {
		return Update{}, false, nil
	}

	directionSequence := binary.BigEndian.Uint64(wire[:8])
	if directionSequence&directionServerToClient == 0 {
		return Update{}, false, nil
	}
	sequence := directionSequence & sequenceMask
	// Stock mosh still feeds reordered and duplicate datagrams to fragment
	// reassembly and SSP; only timestamps, RTT estimation, and roaming are
	// gated on sequence freshness. Dropping older datagrams outright, as the
	// tracer did, loses fragments whenever UDP reorders an instruction and
	// forces an otherwise unnecessary retransmission.
	newest := !c.seqInSet || sequence > c.seqInMax

	var nonce [12]byte
	binary.BigEndian.PutUint64(nonce[4:], directionSequence)
	plaintext, ok := c.ocb.Decrypt(nonce[:], wire[8:])
	if !ok {
		return Update{}, false, nil
	}
	if newest {
		c.seqInMax = sequence
		c.seqInSet = true
		c.lastReceive = now
	}

	if len(plaintext) < 4+fragmentHeaderSize {
		return c.protocolFailureLocked(fmt.Errorf("%w: datagram too short for timestamps and fragment", ErrProtocol))
	}
	if newest {
		c.processTimestampsLocked(plaintext[:4], now)
	}

	frag, err := unmarshalFragment(plaintext[4:])
	if err != nil {
		return c.protocolFailureLocked(fmt.Errorf("%w: %v", ErrProtocol, err))
	}
	assembled, complete := c.fragment.add(frag)
	if !complete {
		return Update{}, true, nil
	}
	instruction, err := decodeInstruction(assembled)
	if err != nil {
		return c.protocolFailureLocked(fmt.Errorf("%w: %v", ErrProtocol, err))
	}
	update, authenticated, err = c.processInstructionLocked(instruction, now)
	if err != nil {
		c.err = err
	}
	return update, authenticated, err
}

// protocolFailureLocked records a fatal authenticated-protocol violation so
// every later operation fails consistently.
func (c *Client) protocolFailureLocked(err error) (Update, bool, error) {
	c.err = err
	return Update{}, true, err
}

func decodeInstruction(assembled []byte) (TransportInstruction, error) {
	raw, err := decompressInstruction(assembled)
	if err != nil {
		return TransportInstruction{}, err
	}
	instruction, err := UnmarshalTransportInstruction(raw)
	if err != nil {
		return TransportInstruction{}, err
	}
	if instruction.ProtocolVersion != ProtocolVersion {
		return TransportInstruction{}, fmt.Errorf("%w: protocol version %d, want %d",
			ErrProtocol, instruction.ProtocolVersion, ProtocolVersion)
	}
	return instruction, nil
}

func (c *Client) processTimestampsLocked(header []byte, now time.Time) {
	timestamp := binary.BigEndian.Uint16(header[:2])
	reply := binary.BigEndian.Uint16(header[2:4])
	if timestamp != 0xffff {
		c.savedTS = timestamp
		c.savedTSAt = now
		c.haveTimestamp = true
	}
	if reply != 0xffff {
		c.updateRTTLocked(time.Duration(timestampDiff(timestamp16(now), reply)) * time.Millisecond)
	}
}

func (c *Client) updateRTTLocked(sample time.Duration) {
	if sample < 0 || sample > 5*time.Second {
		return
	}
	if !c.rttInit {
		c.srtt = sample
		c.rttvar = sample / 2
		c.rttInit = true
	} else {
		delta := c.srtt - sample
		if delta < 0 {
			delta = -delta
		}
		c.rttvar = 3*c.rttvar/4 + delta/4
		c.srtt = 7*c.srtt/8 + sample/8
	}
	rto := c.srtt + 4*c.rttvar
	if rto < minRTO {
		rto = minRTO
	}
	if rto > maxRTO {
		rto = maxRTO
	}
	c.rto = rto
}

func (c *Client) processInstructionLocked(instruction TransportInstruction, now time.Time) (Update, bool, error) {
	c.processAckLocked(instruction.AckNum)

	for _, state := range c.received {
		if state.num == instruction.NewNum {
			return Update{}, true, nil
		}
	}
	referenceIndex := -1
	for i := range c.received {
		if c.received[i].num == instruction.OldNum {
			referenceIndex = i
			break
		}
	}
	if referenceIndex < 0 {
		return Update{}, true, nil
	}
	// Keep the reference state's contents before pruning: throwaway may drop
	// other entries and change indices, but never the referenced state itself
	// (the sender always keeps old_num at or above its throwaway number).
	instructions := c.received[referenceIndex].instructions
	var hostState HostState

	c.processThrowawayLocked(instruction.ThrowawayNum)

	if len(c.received) > 1024 {
		if now.Before(c.quenchUntil) {
			return Update{}, true, nil
		}
		c.quenchUntil = now.Add(15 * time.Second)
	}

	if len(instruction.Diff) > 0 {
		added, err := UnmarshalHostMessage(instruction.Diff)
		if err != nil {
			return Update{}, true, fmt.Errorf("%w: %v", ErrProtocol, err)
		}
		combined := make([]HostInstruction, 0, len(instructions)+len(added))
		combined = append(combined, instructions...)
		combined = append(combined, added...)
		instructions = combined
		hostState.Instructions = added
	} else {
		hostState.Instructions = nil
	}

	state := receivedState{num: instruction.NewNum, at: now, instructions: instructions}
	hostState.Num = instruction.NewNum
	hostState.OldNum = instruction.OldNum
	hostState.ThrowawayNum = instruction.ThrowawayNum
	insertAt := len(c.received)
	for i := range c.received {
		if c.received[i].num > state.num {
			insertAt = i
			break
		}
	}
	if insertAt < len(c.received) {
		c.received = append(c.received, receivedState{})
		copy(c.received[insertAt+1:], c.received[insertAt:])
		c.received[insertAt] = state
		return Update{HostStates: []HostState{hostState}}, true, nil
	}

	c.received = append(c.received, state)
	hostState.Newest = true
	c.ackNum = state.num
	if len(instruction.Diff) > 0 {
		c.pendingAck = true
		due := now.Add(ackDelay)
		if c.ackDue.IsZero() || due.Before(c.ackDue) {
			c.ackDue = due
		}
	}

	var update Update
	update.HostStates = []HostState{hostState}
	if c.delivered < len(state.instructions) {
		var output []byte
		for _, hostInstruction := range state.instructions[c.delivered:] {
			if len(hostInstruction.HostString) > 0 {
				output = append(output, hostInstruction.HostString...)
			}
		}
		c.delivered = len(state.instructions)
		update.Output = output
	}
	if state.num == ShutdownNum {
		c.remoteShutdown = true
		update.RemoteShutdown = true
	}
	return update, true, nil
}

func (c *Client) processAckLocked(ack uint64) {
	matched := -1
	for i := range c.sent {
		if c.sent[i].num == ack {
			matched = i
			break
		}
	}
	if matched < 0 {
		return
	}
	if matched > 0 {
		c.sent = append(c.sent[:0], c.sent[matched:]...)
	}
	c.acked = ack
	if ack == ShutdownNum && c.shutdown {
		c.shutdownAcked = true
	}
}

func (c *Client) processThrowawayLocked(throwaway uint64) {
	if throwaway == 0 {
		return
	}
	highest := c.received[len(c.received)-1].num
	if throwaway > highest {
		return
	}
	kept := c.received[:0]
	for _, state := range c.received {
		if state.num >= throwaway {
			kept = append(kept, state)
		}
	}
	c.received = kept
}

func (c *Client) encodeInstructionLocked(instruction TransportInstruction, now time.Time) ([][]byte, error) {
	raw := instruction.Marshal()
	compressed, err := compressInstruction(raw)
	if err != nil {
		return nil, err
	}
	c.fragmentID++
	id := c.fragmentID
	var datagrams [][]byte
	number := uint16(0)
	for offset := 0; offset < len(compressed); offset += maxFragmentPayload {
		end := offset + maxFragmentPayload
		if end > len(compressed) {
			end = len(compressed)
		}
		frag := fragment{id: id, number: number, final: end == len(compressed), content: compressed[offset:end]}
		datagram, err := c.encryptDatagramLocked(frag.marshal(), now)
		if err != nil {
			return nil, err
		}
		datagrams = append(datagrams, datagram)
		number++
	}
	return datagrams, nil
}

func (c *Client) encryptDatagramLocked(payload []byte, now time.Time) ([]byte, error) {
	c.seqOut++
	if c.seqOut&directionServerToClient != 0 {
		return nil, fmt.Errorf("%w: datagram sequence exhausted", ErrProtocol)
	}
	sequence := c.seqOut
	var nonce [12]byte
	binary.BigEndian.PutUint64(nonce[4:], directionClientToServer|sequence)

	reply := uint16(0xffff)
	if c.haveTimestamp && now.Sub(c.savedTSAt) < time.Second {
		elapsed := uint16(now.Sub(c.savedTSAt) / time.Millisecond)
		reply = c.savedTS + elapsed
		c.haveTimestamp = false
	}
	plaintext := make([]byte, 4, 4+len(payload))
	binary.BigEndian.PutUint16(plaintext[:2], timestamp16(now))
	binary.BigEndian.PutUint16(plaintext[2:4], reply)
	plaintext = append(plaintext, payload...)

	ciphertext, err := c.ocb.Encrypt(nonce[:], plaintext)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrProtocol, err)
	}
	wire := make([]byte, 8+len(ciphertext))
	binary.BigEndian.PutUint64(wire[:8], directionClientToServer|sequence)
	copy(wire[8:], ciphertext)
	return wire, nil
}

func (c *Client) usable() error {
	if c.closed {
		return ErrClosed
	}
	if c.err != nil {
		return c.err
	}
	return nil
}

func timestamp16(now time.Time) uint16 {
	value := uint16(now.UnixNano() / int64(time.Millisecond))
	if value == 0xffff {
		value++
	}
	return value
}

func timestampDiff(newest, oldest uint16) uint16 {
	difference := int(newest) - int(oldest)
	if difference < 0 {
		difference += 65536
	}
	return uint16(difference)
}

func userInstructionsEqual(a, b []UserInstruction) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Resize != b[i].Resize || a[i].Width != b[i].Width || a[i].Height != b[i].Height {
			return false
		}
		if !bytesEqual(a[i].Keys, b[i].Keys) {
			return false
		}
	}
	return true
}

func cloneUserInstructions(in []UserInstruction) []UserInstruction {
	out := make([]UserInstruction, len(in))
	copy(out, in)
	for i := range out {
		out[i].Keys = append([]byte(nil), in[i].Keys...)
	}
	return out
}

func marshalUserDiff(base, target []UserInstruction) []byte {
	if len(target) < len(base) {
		return MarshalUserMessage(target)
	}
	return MarshalUserMessage(target[len(base):])
}
