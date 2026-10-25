// Package telnet implements the Session transport port over a Telnet TCP
// connection. It owns the socket, the lazy activation policy, and the
// direction of negotiation bytes; the RFC state machines live in
// domain/model/telnet.
//
// Negotiation activates lazily: until the peer sends an IAC byte the stream is
// passed through untouched, so a legacy raw-TCP service on port 23 is not
// corrupted by a client that assumes Telnet framing. Once an IAC arrives the
// client drives the canonical handshake and answers WILL/WONT/DO/DONT and
// subnegotiations.
package telnet

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	appconnection "github.com/mslxl/natcatty/application/connection"
	appsession "github.com/mslxl/natcatty/application/session"
	domaintelnet "github.com/mslxl/natcatty/domain/model/telnet"
)

// readChunkSize is the size of one socket read.
const readChunkSize = 32 << 10

// Factory opens Telnet Sessions. The zero value dials the network directly.
// Dial is the test seam; it is never set in the running app.
type Factory struct {
	// Dial opens the TCP connection. A nil value uses net.Dialer.
	Dial func(ctx context.Context, network, address string) (net.Conn, error)
	// ConnectTimeout overrides the configuration's dial deadline. Zero keeps
	// the configured value, which defaults to ten seconds.
	ConnectTimeout time.Duration
}

var _ appsession.Factory = Factory{}

// Open validates a Telnet configuration, dials the endpoint, and returns the
// Session transport. A dial that exceeds the connect timeout is a typed
// timeout failure; an unreachable endpoint is a typed network failure.
func (f Factory) Open(ctx context.Context, spec appsession.StartSpec) (appsession.Transport, error) {
	if err := ctx.Err(); err != nil {
		return nil, &appconnection.Error{Kind: appconnection.KindCancelled, Cause: err}
	}
	if spec.Telnet == nil {
		return nil, &domaintelnet.ValidationError{Field: "telnet", Reason: "is required"}
	}
	cfg := spec.Telnet.Normalized()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	timeout := cfg.ConnectTimeout
	if f.ConnectTimeout > 0 {
		timeout = f.ConnectTimeout
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
	return newTransport(conn, cfg, spec.Cols, spec.Rows), nil
}

func (f Factory) dial(ctx context.Context, network, address string) (net.Conn, error) {
	if f.Dial != nil {
		return f.Dial(ctx, network, address)
	}
	var dialer net.Dialer
	return dialer.DialContext(ctx, network, address)
}

// classifyDialError maps a dial failure onto the UI's connection vocabulary.
func classifyDialError(ctx context.Context, dialCtxErr error, address string, err error) error {
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

// EchoState is the client's view of both echo directions. Remote echo means
// the server echoes typed input; local echo means the server asked the client
// to echo it.
type EchoState struct {
	Remote bool
	Local  bool
}

// transport adapts one TCP connection to the Session transport port.
type transport struct {
	conn   net.Conn
	parser *domaintelnet.Parser

	// negMu serializes negotiation, activation, window size, and writes that
	// depend on activation. Lock order is negMu then writeMu.
	negMu      sync.Mutex
	negotiator *domaintelnet.Negotiator
	active     bool
	cols, rows int
	clean      []byte

	writeMu sync.Mutex

	// pending and buffer belong to the single Read goroutine.
	pending []byte
	buffer  []byte

	endMu  sync.Mutex
	ended  bool
	end    appsession.End
	closed atomic.Bool

	closeOnce sync.Once
	closeErr  error
}

var (
	_ appsession.Transport   = (*transport)(nil)
	_ appsession.EndReporter = (*transport)(nil)
)

func newTransport(conn net.Conn, cfg domaintelnet.Config, cols, rows int) *transport {
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	t := &transport{
		conn:   conn,
		cols:   cols,
		rows:   rows,
		buffer: make([]byte, readChunkSize),
	}
	t.parser = domaintelnet.NewParser(domaintelnet.Callbacks{
		Data: func(chunk []byte) {
			t.clean = append(t.clean, chunk...)
		},
		Command: func(cmd, opt byte) {
			t.negotiator.HandleCommand(cmd, opt)
		},
		Subnegotiation: func(opt byte, payload []byte) {
			t.negotiator.HandleSubnegotiation(opt, payload)
		},
	})
	t.negotiator = domaintelnet.NewNegotiator(domaintelnet.NegotiatorOptions{
		WriteCommand:        t.writeCommand,
		WriteSubnegotiation: t.writeSubnegotiation,
		WindowSize: func() (int, int) {
			return t.cols, t.rows
		},
		TermType: cfg.TermType,
	})
	return t
}

// Echo reports the current negotiation's echo state.
func (t *transport) Echo() EchoState {
	t.negMu.Lock()
	defer t.negMu.Unlock()
	return EchoState{
		Remote: t.negotiator.RemoteEcho(),
		Local:  t.negotiator.LocalEcho(),
	}
}

// process activates negotiation when the peer speaks it and returns the clean
// data from one chunk. Negotiation takes precedence over the data on the same
// read, as in the original.
func (t *transport) process(chunk []byte) []byte {
	t.negMu.Lock()
	defer t.negMu.Unlock()
	if !t.active {
		if bytes.IndexByte(chunk, domaintelnet.IAC) < 0 {
			return chunk
		}
		t.active = true
		t.negotiator.Start()
	}
	t.clean = t.clean[:0]
	t.parser.Feed(chunk)
	if len(t.clean) == 0 {
		return nil
	}
	out := make([]byte, len(t.clean))
	copy(out, t.clean)
	return out
}

// Read returns clean stream bytes. Commands consumed without producing data
// keep reading instead of returning a zero-length read.
func (t *transport) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for {
		if len(t.pending) > 0 {
			n := copy(p, t.pending)
			t.pending = t.pending[n:]
			return n, nil
		}
		n, err := t.conn.Read(t.buffer)
		if n > 0 {
			if clean := t.process(t.buffer[:n]); len(clean) > 0 {
				t.pending = clean
				continue
			}
		}
		if err != nil {
			t.classifyReadError(err)
			return 0, err
		}
	}
}

func (t *transport) classifyReadError(err error) {
	if t.closed.Load() || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		t.remember(appsession.End{Reason: appsession.EndReasonClosed, ExitCode: 0, HasExitCode: true})
		return
	}
	t.remember(appsession.End{Reason: appsession.EndReasonError, ExitCode: 1, HasExitCode: true})
}

// Write normalizes terminal newlines to NVT form and, once negotiation is
// active, doubles literal 0xFF bytes.
func (t *transport) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	wire := []byte(domaintelnet.NormalizeNewlines(string(p)))
	t.negMu.Lock()
	if t.active {
		wire = domaintelnet.EscapeIAC(wire)
	}
	t.writeMu.Lock()
	_, err := t.conn.Write(wire)
	t.writeMu.Unlock()
	t.negMu.Unlock()
	if err != nil {
		// A write that fails because Close is hanging up is the user's own
		// close, not a transport failure. Check the flag before recording so a
		// close racing a pending write still ends as closed/0.
		if t.closed.Load() {
			t.remember(appsession.End{Reason: appsession.EndReasonClosed, ExitCode: 0, HasExitCode: true})
		} else {
			t.remember(appsession.End{Reason: appsession.EndReasonError, ExitCode: 1, HasExitCode: true})
		}
		return 0, err
	}
	return len(p), nil
}

// Resize records the terminal size and reports it when the peer has enabled
// NAWS. A resize before DO NAWS is silent, and the size is sent when NAWS
// becomes active.
func (t *transport) Resize(cols, rows int) error {
	t.negMu.Lock()
	defer t.negMu.Unlock()
	t.cols, t.rows = cols, rows
	if t.active {
		t.negotiator.SendWindowSize()
	}
	return nil
}

func (t *transport) writeCommand(cmd, opt byte) {
	t.writeRaw([]byte{domaintelnet.IAC, cmd, opt})
}

func (t *transport) writeSubnegotiation(opt byte, payload []byte) {
	frame := make([]byte, 0, len(payload)+5)
	frame = append(frame, domaintelnet.IAC, domaintelnet.SB, opt)
	frame = append(frame, payload...)
	frame = append(frame, domaintelnet.IAC, domaintelnet.SE)
	t.writeRaw(frame)
}

// writeRaw is called only from negotiation paths that already hold negMu.
func (t *transport) writeRaw(data []byte) {
	t.writeMu.Lock()
	_, err := t.conn.Write(data)
	t.writeMu.Unlock()
	if err != nil && !t.closed.Load() {
		t.remember(appsession.End{Reason: appsession.EndReasonError, ExitCode: 1, HasExitCode: true})
	}
}

// End reports the first classified end. A user close is a clean end with exit
// 0; a socket failure is an error end with exit 1.
func (t *transport) End() appsession.End {
	t.endMu.Lock()
	defer t.endMu.Unlock()
	if t.ended {
		return t.end
	}
	if t.closed.Load() {
		return appsession.End{Reason: appsession.EndReasonClosed, ExitCode: 0, HasExitCode: true}
	}
	return appsession.End{Reason: appsession.EndReasonError, ExitCode: 1, HasExitCode: true}
}

// Close hangs up the socket. The first end classification wins.
func (t *transport) Close() error {
	t.closeOnce.Do(func() {
		t.closed.Store(true)
		err := t.conn.Close()
		t.endMu.Lock()
		t.closeErr = err
		t.endMu.Unlock()
		if err != nil {
			t.remember(appsession.End{Reason: appsession.EndReasonError, ExitCode: 1, HasExitCode: true})
			return
		}
		t.remember(appsession.End{Reason: appsession.EndReasonClosed, ExitCode: 0, HasExitCode: true})
	})
	t.endMu.Lock()
	defer t.endMu.Unlock()
	return t.closeErr
}

func (t *transport) remember(end appsession.End) {
	t.endMu.Lock()
	if !t.ended {
		t.ended = true
		t.end = end
	}
	t.endMu.Unlock()
}
