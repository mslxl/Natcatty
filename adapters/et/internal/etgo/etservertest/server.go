// SPDX-License-Identifier: Apache-2.0
// Package etservertest provides an in-process fake etserver and an in-memory
// network, so etcp can be tested without sockets and under testing/synctest.
//
// The server is written independently of etcp, from upstream EternalTerminal
// semantics (tag et-v7.0.0), so the two implementations check each other.
// In particular it writes its whole catchup before reading the client's,
// exactly like upstream's Connection::recover (src/base/Connection.cpp:105-143).
package etservertest

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"sync"

	"github.com/mslxl/natcatty/adapters/et/internal/etgo/etcp"
	"github.com/mslxl/natcatty/adapters/et/internal/etgo/protocol"
	"github.com/mslxl/natcatty/adapters/et/internal/etgo/seal"
	"github.com/mslxl/natcatty/adapters/et/internal/etgo/wire"
)

var errSessionEnded = errors.New("etservertest: session ended")

// Server is an in-process fake etserver for one session.
type Server struct {
	id  string
	key [32]byte

	mu         sync.Mutex
	registered bool // false after EndSession: connects get INVALID_KEY
	known      bool // a client connected before: the next connect is RETURNING_CLIENT
	legacy     bool // answer without the v7 challenge, like etserver 6.2.10
	echo       bool
	out        *seal.Stream // server to client
	sent       [][]byte     // every sealed packet sent; index is the sequence number
	in         *seal.Stream // client to server
	recvSeq    int64
	queue      []protocol.Packet // received, not yet returned by Recv
	cur        *serverLink

	notify chan struct{} // cap 1: queue grew
	wake   chan struct{} // cap 1: sent grew or the session ended
}

type serverLink struct {
	conn net.Conn
	done chan struct{} // closed when Serve for this link returns
}

// NewServer returns a server that accepts the session id with passkey. It
// panics if passkey is not 32 bytes, which is a bug in the test.
func NewServer(id, passkey string) *Server {
	if len(passkey) != 32 {
		panic("etservertest: passkey must be 32 bytes")
	}
	s := &Server{
		id:         id,
		registered: true,
		echo:       true,
		notify:     make(chan struct{}, 1),
		wake:       make(chan struct{}, 1),
	}
	copy(s.key[:], passkey)
	s.out = seal.New(&s.key, seal.ServerToClient)
	s.in = seal.New(&s.key, seal.ClientToServer)
	return s
}

// Serve runs one link on c: the connect handshake (and the recover exchange
// for a returning client), then the encrypted stream, until c fails, the
// session ends or ctx ends. A link replaces the one before it when it takes
// over, as upstream closes the old socket before recovering on the new one
// (src/base/ServerClientConnection.cpp:27-35 at et-v7.0.0). Takeover follows
// the order links reach it, not the order they connected, so a test that
// connects twice waits for the first link to settle.
func (s *Server) Serve(ctx context.Context, c net.Conn) error {
	defer func() { _ = c.Close() }()
	// lctx ends with ctx or when either stream loop fails; closing c then
	// unblocks the handshake, the recover exchange or the other loop.
	lctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	stop := context.AfterFunc(lctx, func() { _ = c.Close() })
	defer stop()

	var req protocol.ConnectRequest
	if err := wire.ReadMessage(c, &req); err != nil {
		return fmt.Errorf("etservertest: connect request: %w", err)
	}
	if req.GetVersion() != protocol.Version {
		// Upstream's text (src/base/ServerConnection.cpp:55-59 at et-v7.1.1).
		return s.reject(c, protocol.ConnectStatus_MISMATCHED_PROTOCOL, fmt.Sprintf(
			"Mismatched protocol versions.  Your client & server must be on the same version of ET.  Client: %d != Server: %d",
			req.GetVersion(), protocol.Version))
	}
	s.mu.Lock()
	registered := req.GetClientId() == s.id && s.registered
	legacy := s.legacy || !req.GetSupportsChallenge()
	s.mu.Unlock()
	if !registered {
		// MEASURED against etserver 7.0.0: the error text for an unknown or
		// ended session is "Client is not registered".
		return s.reject(c, protocol.ConnectStatus_INVALID_KEY, "Client is not registered")
	}

	status, resetRequired, salt, err := s.authenticate(c, &req, legacy)
	if err != nil {
		return err
	}
	if status != protocol.ConnectStatus_NEW_CLIENT && status != protocol.ConnectStatus_RETURNING_CLIENT {
		return fmt.Errorf("etservertest: rejected: status %v", status)
	}

	done := s.takeOver(c)
	defer close(done)

	var flushed int64
	switch {
	case resetRequired:
		if err := s.resetRecover(c, salt); err != nil {
			return err
		}
	case status == protocol.ConnectStatus_RETURNING_CLIENT:
		f, err := s.recover(c)
		if err != nil {
			return err
		}
		flushed = f
	}

	var wg sync.WaitGroup
	wg.Go(func() { cancel(s.readLoop(c)) })
	wg.Go(func() { cancel(s.writeLoop(lctx, c, flushed)) })
	wg.Wait()
	return context.Cause(lctx)
}

// reject writes a status-only response, as upstream does before any
// challenge, and reports the fixed rejection text.
func (s *Server) reject(c net.Conn, status protocol.ConnectStatus, text string) error {
	resp := &protocol.ConnectResponse{}
	resp.SetStatus(status)
	if text != "" {
		resp.SetError(text)
	}
	if err := wire.WriteMessage(c, resp); err != nil {
		return fmt.Errorf("etservertest: connect response: %w", err)
	}
	return fmt.Errorf("etservertest: rejected: %s", text)
}

// authenticate runs the v7 challenge and decides the final status, or, for a
// legacy peer, decides directly from the known-client state. It returns
// resetRequired and salt for an authenticated reset recovery.
func (s *Server) authenticate(c net.Conn, req *protocol.ConnectRequest, legacy bool) (
	status protocol.ConnectStatus, resetRequired bool, salt []byte, err error) {

	if legacy {
		s.mu.Lock()
		status = protocol.ConnectStatus_RETURNING_CLIENT
		if !s.known {
			status = protocol.ConnectStatus_NEW_CLIENT
			s.known = true
		}
		s.mu.Unlock()
		resp := &protocol.ConnectResponse{}
		resp.SetStatus(status)
		if err := wire.WriteMessage(c, resp); err != nil {
			return 0, false, nil, fmt.Errorf("etservertest: connect response: %w", err)
		}
		return status, false, nil, nil
	}

	challenge := randomBytes()
	challengeResponse := &protocol.ConnectResponse{}
	challengeResponse.SetAuthChallenge(challenge)
	if err := wire.WriteMessage(c, challengeResponse); err != nil {
		return 0, false, nil, fmt.Errorf("etservertest: auth challenge: %w", err)
	}
	var auth protocol.ConnectAuth
	if err := wire.ReadMessage(c, &auth); err != nil {
		return 0, false, nil, fmt.Errorf("etservertest: connect auth: %w", err)
	}
	expected, proofErr := etcp.ConnectionProof(s.key[:], req.GetClientId(), req.GetVersion(),
		challenge, req.GetResetIntent())
	if proofErr != nil || subtle.ConstantTimeCompare(auth.GetProof(), expected) != 1 {
		rejected := &protocol.ConnectResponse{}
		rejected.SetStatus(protocol.ConnectStatus_INVALID_KEY)
		rejected.SetError("Client authentication failed")
		if err := wire.WriteMessage(c, rejected); err != nil {
			return 0, false, nil, fmt.Errorf("etservertest: connect response: %w", err)
		}
		return 0, false, nil, errors.New("etservertest: rejected: client authentication failed")
	}

	s.mu.Lock()
	known := s.known
	if !known {
		s.known = true
	}
	s.mu.Unlock()
	if known {
		status = protocol.ConnectStatus_RETURNING_CLIENT
		resetRequired = req.GetResetIntent()
		if resetRequired {
			salt = randomBytes()
		}
	} else {
		status = protocol.ConnectStatus_NEW_CLIENT
	}
	proof, err := etcp.ResetDecisionProof(s.key[:], req.GetClientId(), req.GetVersion(),
		challenge, status, resetRequired, salt)
	if err != nil {
		return 0, false, nil, fmt.Errorf("etservertest: reset decision proof: %w", err)
	}
	resp := &protocol.ConnectResponse{}
	resp.SetStatus(status)
	resp.SetResetRequired(resetRequired)
	if resetRequired {
		resp.SetResetSalt(salt)
	}
	resp.SetResetProof(proof)
	if err := wire.WriteMessage(c, resp); err != nil {
		return 0, false, nil, fmt.Errorf("etservertest: connect response: %w", err)
	}
	return status, resetRequired, salt, nil
}

// resetRecover runs upstream's forced-reset exchange: both sides write a
// zero sequence header carrying the negotiated salt, exchange empty catchup
// buffers, rekey, and restart the stream at sequence zero
// (src/base/Connection.cpp:120-177 at et-v7.1.1).
func (s *Server) resetRecover(c io.ReadWriter, salt []byte) error {
	// Upstream writes each message before reading the peer's. Over net.Pipe
	// there is no buffer, so both sides would block on their first write;
	// write concurrently with the read, as the client's recover does.
	sh := &protocol.SequenceHeader{}
	sh.SetSequenceNumber(0)
	sh.SetReset(true)
	sh.SetResetSalt(salt)
	writeErr := make(chan error, 1)
	go func() { writeErr <- wire.WriteMessage(c, sh) }()
	var peer protocol.SequenceHeader
	readErr := wire.ReadMessage(c, &peer)
	if err := <-writeErr; err != nil {
		return fmt.Errorf("etservertest: write reset sequence: %w", err)
	}
	if readErr != nil {
		return fmt.Errorf("etservertest: read reset sequence: %w", readErr)
	}
	if !peer.GetReset() || !bytes.Equal(peer.GetResetSalt(), salt) {
		return errors.New("etservertest: reset request does not match the negotiated salt")
	}
	go func() { writeErr <- wire.WriteMessage(c, &protocol.CatchupBuffer{}) }()
	var theirs protocol.CatchupBuffer
	readErr = wire.ReadMessage(c, &theirs)
	if err := <-writeErr; err != nil {
		return fmt.Errorf("etservertest: write empty catchup: %w", err)
	}
	if readErr != nil {
		return fmt.Errorf("etservertest: read reset catchup: %w", readErr)
	}
	if len(theirs.GetBuffer()) != 0 {
		return errors.New("etservertest: reset catchup is not empty")
	}

	out := seal.New(&s.key, seal.ServerToClient)
	if err := out.Rekey(salt); err != nil {
		return fmt.Errorf("etservertest: rekey server stream: %w", err)
	}
	in := seal.New(&s.key, seal.ClientToServer)
	if err := in.Rekey(salt); err != nil {
		return fmt.Errorf("etservertest: rekey client stream: %w", err)
	}
	s.mu.Lock()
	s.out = out
	s.in = in
	s.sent = nil
	s.recvSeq = 0
	s.queue = nil
	s.mu.Unlock()
	return nil
}

// LegacyHandshake makes the server answer without the v7 challenge, as
// etserver 6.2.10 does with unknown ConnectRequest fields.
func (s *Server) LegacyHandshake(on bool) {
	s.mu.Lock()
	s.legacy = on
	s.mu.Unlock()
}

func randomBytes() []byte {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("etservertest: crypto/rand: " + err.Error())
	}
	return b
}

// takeOver makes c the active link, closing the previous one and waiting for
// its Serve to finish so the two never touch the stream state at once.
func (s *Server) takeOver(c net.Conn) chan struct{} {
	done := make(chan struct{})
	s.mu.Lock()
	prev := s.cur
	s.cur = &serverLink{conn: c, done: done}
	s.mu.Unlock()
	if prev != nil {
		_ = prev.conn.Close()
		<-prev.done
	}
	return done
}

// recover runs the server side of the recover exchange in upstream order and
// returns the sequence number the new link's writer starts from.
func (s *Server) recover(c io.ReadWriter) (int64, error) {
	s.mu.Lock()
	mine := s.recvSeq
	s.mu.Unlock()

	sh := &protocol.SequenceHeader{}
	sh.SetSequenceNumber(int32(mine))
	if err := wire.WriteMessage(c, sh); err != nil {
		return 0, fmt.Errorf("etservertest: write sequence: %w", err)
	}
	var peer protocol.SequenceHeader
	if err := wire.ReadMessage(c, &peer); err != nil {
		return 0, fmt.Errorf("etservertest: read sequence: %w", err)
	}

	s.mu.Lock()
	from := int64(peer.GetSequenceNumber())
	if from < 0 || from > int64(len(s.sent)) {
		s.mu.Unlock()
		return 0, fmt.Errorf("etservertest: client sequence %d outside [0, %d]", from, len(s.sent))
	}
	catchup := slices.Clone(s.sent[from:])
	flushed := int64(len(s.sent))
	s.mu.Unlock()

	// Upstream writes its whole catchup before reading ours
	// (src/base/Connection.cpp:125-135). Over net.Pipe, which has no buffer,
	// this deadlocks unless the client reads concurrently.
	cb := &protocol.CatchupBuffer{}
	cb.SetBuffer(catchup)
	if err := wire.WriteMessage(c, cb); err != nil {
		return 0, fmt.Errorf("etservertest: write catchup: %w", err)
	}
	var theirs protocol.CatchupBuffer
	if err := wire.ReadMessage(c, &theirs); err != nil {
		return 0, fmt.Errorf("etservertest: read catchup: %w", err)
	}
	for _, b := range theirs.GetBuffer() {
		if err := s.accept(b); err != nil {
			return 0, err
		}
	}
	return flushed, nil
}

func (s *Server) readLoop(r io.Reader) error {
	br := bufio.NewReader(r)
	var buf []byte
	for {
		frame, err := wire.ReadFrame(br, buf)
		if err != nil {
			return fmt.Errorf("etservertest: read: %w", err)
		}
		buf = frame
		if err := s.accept(frame); err != nil {
			return err
		}
	}
}

// accept opens one sealed packet from the client and queues it for Recv.
func (s *Server) accept(b []byte) error {
	encrypted, h, payload, err := wire.ParsePacket(b)
	if err != nil {
		return fmt.Errorf("etservertest: %w", err)
	}
	if !encrypted {
		return errors.New("etservertest: unencrypted packet")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	plain, err := s.in.Open(nil, payload)
	if err != nil {
		return fmt.Errorf("etservertest: %w", err)
	}
	s.recvSeq++
	s.queue = append(s.queue, protocol.Packet{Header: h, Payload: plain})
	signal(s.notify)
	// Upstream echoes KEEP_ALIVE (src/terminal/TerminalServer.cpp:389-393), but
	// only once the session runs: a first packet other than INITIAL_PAYLOAD
	// aborts etserver (TerminalServer.cpp:429-439). This fake echoes at any
	// time and does not model that abort; etcp's TestNoProbeBeforeFirstPacket
	// pins the client side instead.
	if h == protocol.HeaderKeepAlive && s.echo {
		s.enqueueLocked(protocol.Packet{Header: protocol.HeaderKeepAlive})
		signal(s.wake)
	}
	return nil
}

func (s *Server) writeLoop(ctx context.Context, w io.Writer, flushed int64) error {
	for {
		s.mu.Lock()
		pending := s.sent[flushed:]
		ended := !s.registered
		s.mu.Unlock()
		if len(pending) == 0 {
			if ended {
				return errSessionEnded
			}
			select {
			case <-s.wake:
				continue
			case <-ctx.Done():
				return context.Cause(ctx)
			}
		}
		for _, f := range pending {
			if err := wire.WriteFrame(w, f); err != nil {
				return fmt.Errorf("etservertest: write: %w", err)
			}
		}
		flushed += int64(len(pending))
	}
}

func (s *Server) enqueueLocked(p protocol.Packet) {
	sealed := s.out.Seal(nil, p.Payload)
	s.sent = append(s.sent, wire.AppendPacket(nil, true, p.Header, sealed))
}

// Recv returns the next packet the client sent, exactly once and in order,
// including KEEP_ALIVE probes.
func (s *Server) Recv(ctx context.Context) (protocol.Packet, error) {
	for {
		s.mu.Lock()
		if len(s.queue) > 0 {
			p := s.queue[0]
			s.queue[0] = protocol.Packet{}
			s.queue = s.queue[1:]
			s.mu.Unlock()
			return p, nil
		}
		s.mu.Unlock()
		select {
		case <-s.notify:
		case <-ctx.Done():
			return protocol.Packet{}, context.Cause(ctx)
		}
	}
}

// Send queues a packet to the client. It is sealed at once and replayed
// across reconnects, like upstream's BackedWriter.
func (s *Server) Send(ctx context.Context, p protocol.Packet) error {
	if err := ctx.Err(); err != nil {
		return context.Cause(ctx)
	}
	s.mu.Lock()
	s.enqueueLocked(p)
	s.mu.Unlock()
	signal(s.wake)
	return nil
}

// EndSession drops the session like a shell exit on etserver 7.0.0: the live
// link flushes what was already sent and closes, and later connects get
// INVALID_KEY (src/terminal/TerminalServer.cpp:329-332,422-426).
func (s *Server) EndSession() {
	s.mu.Lock()
	s.registered = false
	s.mu.Unlock()
	signal(s.wake)
}

// EchoKeepAlive controls whether KEEP_ALIVE packets are echoed (default true).
func (s *Server) EchoKeepAlive(on bool) {
	s.mu.Lock()
	s.echo = on
	s.mu.Unlock()
}

func signal(c chan struct{}) {
	select {
	case c <- struct{}{}:
	default:
	}
}
