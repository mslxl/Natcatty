// Package session owns terminal Sessions: their transport, lifecycle,
// status, and the bounded duplex the terminal surface consumes.
//
// The application owns every transport. A Session reads its Transport on one
// goroutine, sends input through one bounded writer queue, and serializes
// close and resize commands on one command loop. The UI never sees a
// transport type; it connects MyGo's terminal to TerminalConn, whose Read is
// terminal output, Write is terminal input, and Resize reaches the transport.
//
// Resources are bounded by backpressure, not loss: when output pending in
// TerminalConn reaches HighWaterMark the reader stops reading the transport,
// and it resumes once the terminal drains the backlog to LowWaterMark.
package session

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/mslxl/natcatty/domain/model"
)

// Flow-control watermarks, in bytes of output pending in TerminalConn.
const (
	// HighWaterMark is the backlog at which the Session stops reading its
	// transport, so a slow terminal backpressures the PTY or remote shell.
	HighWaterMark = 1 << 20
	// LowWaterMark is the backlog at which the Session resumes reading.
	LowWaterMark = 8 << 10
)

// defaultWriteQueueDepth bounds input chunks queued ahead of the transport
// writer. TerminalConn.Write blocks when the queue is full.
const defaultWriteQueueDepth = 64

// readChunkSize is the size of one transport read.
const readChunkSize = 32 << 10

// Kind is the protocol of a Session. Local shells and password-authenticated
// SSH exist in this slice; later tickets add keys, serial, Telnet, mosh, and
// EternalTerminal.
type Kind string

const (
	// KindLocal is a shell running in a pseudo-terminal on this machine.
	KindLocal Kind = "local"
	// KindSSH is an interactive shell on a remote Host.
	KindSSH Kind = "ssh"
)

// Status is a Session's lifecycle state.
type Status string

const (
	// StatusRunning reports a Session whose transport is open.
	StatusRunning Status = "running"
	// StatusExited reports a Session whose transport ended on its own.
	StatusExited Status = "exited"
	// StatusClosed reports a Session the user or app closed.
	StatusClosed Status = "closed"
)

// ConnectionStatus is the state of the transport beneath a Session. The UI
// shows it beside the lifecycle status.
type ConnectionStatus string

const (
	// ConnectionStatusConnecting is the initial dial. A Session is not
	// published in this state; the shell shows it while Open is pending.
	ConnectionStatusConnecting ConnectionStatus = "connecting"
	// ConnectionStatusActive reports an open transport.
	ConnectionStatusActive ConnectionStatus = "active"
	// ConnectionStatusReconnecting reports that the transport ended under the
	// original retry predicate and a replacement dial is underway.
	ConnectionStatusReconnecting ConnectionStatus = "reconnecting"
	// ConnectionStatusError reports a terminal failure, including a
	// reconnect that stopped on an authentication or host-key failure.
	ConnectionStatusError ConnectionStatus = "error"
)

// EndReason classifies why a transport stopped, matching the original exit
// vocabulary.
type EndReason string

const (
	// EndReasonExited is a normal shell exit.
	EndReasonExited EndReason = "exited"
	// EndReasonSignaled is a server-side signal; reconnecting would fight the
	// server's policy.
	EndReasonSignaled EndReason = "signaled"
	// EndReasonError is a transport failure.
	EndReasonError EndReason = "error"
	// EndReasonClosed is a bare channel close with no exit status.
	EndReasonClosed EndReason = "closed"
	// EndReasonTimeout is an administrative idle logout or connect timeout.
	EndReasonTimeout EndReason = "timeout"
)

// End is a transport's final state.
type End struct {
	Reason      EndReason
	ExitCode    int
	HasExitCode bool
}

// EndReporter is implemented by transports that can classify why they
// stopped. Without it, a transport implementing ExitWaiter reports an exit
// code; every other end is an error.
type EndReporter interface {
	End() End
}

// ReconnectPolicy reopens a dropped SSH transport. The original retries every
// five seconds only after a first successful connection and only for an
// "error" or bare "closed" end. A deliberate exit, a timeout, or a signal
// does not reconnect.
type ReconnectPolicy struct {
	// Delay defaults to DefaultReconnectDelay.
	Delay time.Duration
	// ShouldRetry overrides the default predicate when non-nil.
	ShouldRetry func(End) bool
}

// DefaultReconnectDelay is the original terminal auto-reconnect interval.
const DefaultReconnectDelay = 5 * time.Second

// shouldRetry reports the original reason predicate.
func (p *ReconnectPolicy) shouldRetry(end End) bool {
	if p == nil {
		return false
	}
	if p.ShouldRetry != nil {
		return p.ShouldRetry(end)
	}
	switch end.Reason {
	case EndReasonError:
		return true
	case EndReasonClosed:
		return true
	default:
		return false
	}
}

func (p *ReconnectPolicy) delay() time.Duration {
	if p == nil || p.Delay <= 0 {
		return DefaultReconnectDelay
	}
	return p.Delay
}

// Snapshot is the immutable state the UI consumes.
type Snapshot struct {
	ID     model.SessionID
	Kind   Kind
	Status Status
	// Connection is the transport state beneath the Session.
	Connection ConnectionStatus
	// ExitCode is meaningful when HasExitCode is true: a program that ended
	// normally reports its code, a signalled one reports 128+signal.
	ExitCode    int
	HasExitCode bool
	// Cols and Rows are the last size sent to the transport.
	Cols, Rows int
	// PendingBytes is the output waiting for the terminal to read.
	PendingBytes int
	// Paused reports that output backpressure is engaged.
	Paused bool
}

// Transport is one Session's byte stream. Read is output, Write is input, and
// Resize changes the pseudo-terminal or remote window size.
type Transport interface {
	io.ReadWriteCloser
	Resize(cols, rows int) error
}

// ExitWaiter is implemented by transports that can report a process exit code.
// Network transports need not implement it.
type ExitWaiter interface {
	Wait() (int, error)
}

// StartSpec describes a Session to open. Command empty asks the transport
// adapter for its platform's default shell. SSH Sessions use HostID to read
// the saved credential and Hostname, Port, and Username to reach the endpoint;
// the other fields are ignored by that transport.
type StartSpec struct {
	Kind     Kind
	Command  []string
	Dir      string
	Env      []string
	Cols     int
	Rows     int
	HostID   model.HostID
	Hostname string
	Port     int
	Username string
	// Reconnect reopens the transport after a retryable end. A nil policy
	// disables reconnection, which is the rule for local shells.
	Reconnect *ReconnectPolicy
}

// Factory opens a Transport for a StartSpec. It is the application's port for
// local PTYs, SSH channels, and the other transports.
type Factory interface {
	Open(ctx context.Context, spec StartSpec) (Transport, error)
}

// ErrClosed reports an operation on a Session that has already ended.
var ErrClosed = errors.New("session: closed")

// Session is one terminal Session. Its methods are safe from any goroutine.
type Session struct {
	id        model.SessionID
	ctx       context.Context
	cancel    context.CancelFunc
	transport Transport
	conn      *TerminalConn
	factory   Factory
	spec      StartSpec

	// generation changes whenever the transport is replaced. The loop is the
	// only writer; readers and writers carry the generation they were started
	// with so a late event from an abandoned transport is ignored.
	generation int
	reopening  bool
	genDone    chan struct{}

	commands chan command
	writeCh  chan []byte
	done     chan struct{}

	closeOnce sync.Once
	mu        sync.Mutex
	snapshot  Snapshot

	onEnd    func(model.SessionID)
	onChange func(Snapshot)
}

type commandKind uint8

const (
	commandResize commandKind = iota
	commandTransportEnded
	commandTransportOpen
	commandTransportFailed
	commandClose
)

type command struct {
	kind       commandKind
	cols       int
	rows       int
	generation int
	transport  Transport
	end        End
	err        error
}

// permanentFailure is implemented by typed open failures that must not be
// retried, namely authentication and host-key rejections.
type permanentFailure interface {
	Permanent() bool
}

// Service opens and tracks Sessions for the app.
type Service struct {
	factory    Factory
	newID      func() model.SessionID
	writeDepth int
	onChange   func(Snapshot)
	mu         sync.Mutex
	sessions   map[model.SessionID]*Session
	closing    bool
}

// Option customizes a Service.
type Option func(*Service)

// WithIDGenerator sets how the Service names Sessions. Tests use it to make
// IDs deterministic; the app uses the ULID default.
func WithIDGenerator(fn func() model.SessionID) Option {
	return func(s *Service) {
		if fn != nil {
			s.newID = fn
		}
	}
}

// WithChangeHook sets a function called with a fresh Snapshot whenever a
// Session's state changes. The app wires it to the window's Invalidate, which
// is safe from any goroutine.
func WithChangeHook(fn func(Snapshot)) Option {
	return func(s *Service) { s.onChange = fn }
}

// NewService returns a Service that opens transports through factory.
func NewService(factory Factory, opts ...Option) *Service {
	s := &Service{
		factory:    factory,
		newID:      newSessionID,
		writeDepth: defaultWriteQueueDepth,
		sessions:   make(map[model.SessionID]*Session),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Open opens a Session from spec. The caller's context bounds opening the
// transport; once open, the Session owns a long-lived context of its own.
func (s *Service) Open(ctx context.Context, spec StartSpec) (*Session, error) {
	if s.factory == nil {
		return nil, errors.New("session: no transport factory")
	}
	if spec.Kind == "" {
		spec.Kind = KindLocal
	}
	if spec.Cols <= 0 {
		spec.Cols = 80
	}
	if spec.Rows <= 0 {
		spec.Rows = 24
	}

	transport, err := s.factory.Open(ctx, spec)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		_ = transport.Close()
		return nil, err
	}

	id := s.newID()
	sessionCtx, cancel := context.WithCancel(context.Background())
	session := &Session{
		id:        id,
		ctx:       sessionCtx,
		cancel:    cancel,
		transport: transport,
		factory:   s.factory,
		spec:      spec,
		genDone:   make(chan struct{}),
		writeCh:   make(chan []byte, s.writeDepth),
		commands:  make(chan command, 16),
		done:      make(chan struct{}),
		snapshot: Snapshot{
			ID:         id,
			Kind:       spec.Kind,
			Status:     StatusRunning,
			Connection: ConnectionStatusActive,
			Cols:       spec.Cols,
			Rows:       spec.Rows,
		},
		onEnd:    s.remove,
		onChange: s.changed,
	}
	session.conn = newTerminalConn(session)

	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		cancel()
		_ = transport.Close()
		return nil, ErrClosed
	}
	s.sessions[id] = session
	s.mu.Unlock()

	session.start()
	return session, nil
}

// OpenLocal opens the platform's local shell in a new Session.
func (s *Service) OpenLocal(ctx context.Context) (*Session, error) {
	return s.Open(ctx, StartSpec{Kind: KindLocal})
}

// SetChangeHook replaces the function called when a Session changes.
func (s *Service) SetChangeHook(fn func(Snapshot)) {
	s.mu.Lock()
	s.onChange = fn
	s.mu.Unlock()
}

// CloseAll closes every live Session and stops accepting new ones.
func (s *Service) CloseAll() {
	s.mu.Lock()
	s.closing = true
	sessions := make([]*Session, 0, len(s.sessions))
	for _, session := range s.sessions {
		sessions = append(sessions, session)
	}
	s.mu.Unlock()

	for _, session := range sessions {
		_ = session.Close()
	}
}

func (s *Service) remove(id model.SessionID) {
	s.mu.Lock()
	delete(s.sessions, id)
	s.mu.Unlock()
}

func (s *Service) changed(snapshot Snapshot) {
	s.mu.Lock()
	hook := s.onChange
	s.mu.Unlock()
	if hook != nil {
		hook(snapshot)
	}
}

// ID returns the Session's opaque identifier.
func (s *Session) ID() model.SessionID { return s.id }

// Done is closed once the Session has ended and its final snapshot is
// published.
func (s *Session) Done() <-chan struct{} { return s.done }

// Snapshot returns the Session's current state.
func (s *Session) Snapshot() Snapshot {
	s.mu.Lock()
	snapshot := s.snapshot
	s.mu.Unlock()
	snapshot.PendingBytes, snapshot.Paused = s.conn.stats()
	return snapshot
}

// TerminalConn returns the bounded duplex the terminal surface connects to.
func (s *Session) TerminalConn() *TerminalConn { return s.conn }

// Resize asks the transport to change its window size. The command loop
// serializes it with close and exit.
func (s *Session) Resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return nil
	}
	select {
	case s.commands <- command{kind: commandResize, cols: cols, rows: rows}:
		return nil
	case <-s.done:
		return ErrClosed
	case <-s.ctx.Done():
		return ErrClosed
	}
}

// Close ends the Session. It is idempotent and returns once the command is
// queued; Done reports when teardown finished.
func (s *Session) Close() error {
	s.closeOnce.Do(func() {
		select {
		case s.commands <- command{kind: commandClose}:
		case <-s.done:
		case <-s.ctx.Done():
		}
	})
	return nil
}

func (s *Session) start() {
	go s.loop()
	go s.readLoop(s.generation, s.transport)
	go s.writeLoop(s.generation, s.transport, s.genDone)
}

// loop serializes lifecycle commands and publishes the final snapshot. It is
// the only goroutine that replaces the transport, so generation changes are
// ordered against resize and close commands.
func (s *Session) loop() {
	status := StatusClosed
	connection := ConnectionStatusActive
	code := -1
	hasCode := false
	for {
		select {
		case <-s.ctx.Done():
			goto finished
		case cmd := <-s.commands:
			switch cmd.kind {
			case commandResize:
				if s.reopening {
					continue
				}
				if err := s.transport.Resize(cmd.cols, cmd.rows); err != nil {
					continue
				}
				s.update(func(snapshot *Snapshot) {
					snapshot.Cols, snapshot.Rows = cmd.cols, cmd.rows
				})
			case commandTransportEnded:
				if cmd.generation != s.generation || s.reopening {
					// A late read or write error from a transport this Session
					// already abandoned.
					if cmd.transport != nil {
						_ = cmd.transport.Close()
					}
					continue
				}
				_ = cmd.transport.Close()
				if s.spec.Reconnect == nil || !s.spec.Reconnect.shouldRetry(cmd.end) {
					status = StatusExited
					connection = connectionForEnd(cmd.end)
					code, hasCode = cmd.end.ExitCode, cmd.end.HasExitCode
					goto finished
				}
				// Advance the generation before starting the dial so a second
				// end event from the dead transport is ignored.
				s.generation++
				s.reopening = true
				close(s.genDone)
				s.genDone = make(chan struct{})
				s.update(func(snapshot *Snapshot) {
					snapshot.Connection = ConnectionStatusReconnecting
				})
				go s.reconnect(s.generation)
			case commandTransportOpen:
				if cmd.generation != s.generation || !s.reopening {
					if cmd.transport != nil {
						_ = cmd.transport.Close()
					}
					continue
				}
				s.reopening = false
				s.transport = cmd.transport
				s.update(func(snapshot *Snapshot) {
					snapshot.Connection = ConnectionStatusActive
				})
				go s.readLoop(s.generation, cmd.transport)
				go s.writeLoop(s.generation, cmd.transport, s.genDone)
			case commandTransportFailed:
				if cmd.generation != s.generation || !s.reopening {
					continue
				}
				status = StatusExited
				connection = ConnectionStatusError
				goto finished
			case commandClose:
				status = StatusClosed
				goto finished
			}
		}
	}

finished:
	s.cancel()
	close(s.genDone)
	s.conn.finish()
	if s.transport != nil {
		_ = s.transport.Close()
	}
	s.update(func(snapshot *Snapshot) {
		snapshot.Status = status
		snapshot.Connection = connection
		snapshot.ExitCode = code
		snapshot.HasExitCode = hasCode
	})
	close(s.done)
	if s.onEnd != nil {
		s.onEnd(s.id)
	}
}

// connectionForEnd reports the indicator a final transport end leaves. A
// dropped transport is an error; a deliberate shell end is not.
func connectionForEnd(end End) ConnectionStatus {
	if end.Reason == EndReasonError {
		return ConnectionStatusError
	}
	return ConnectionStatusActive
}

// reconnect retries opening the transport on the policy's interval. It stops
// on Session close and on a permanent authentication or host-key failure.
func (s *Session) reconnect(generation int) {
	delay := s.spec.Reconnect.delay()
	timer := time.NewTimer(delay)
	defer timer.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-timer.C:
		}
		transport, err := s.factory.Open(s.ctx, s.spec)
		if err == nil {
			s.post(command{kind: commandTransportOpen, generation: generation, transport: transport})
			return
		}
		if s.ctx.Err() != nil {
			return
		}
		if permanentOpenError(err) {
			s.post(command{kind: commandTransportFailed, generation: generation, err: err})
			return
		}
		timer.Reset(delay)
	}
}

// permanentOpenError reports failures that must not be retried.
func permanentOpenError(err error) bool {
	var failure permanentFailure
	return errors.As(err, &failure) && failure.Permanent()
}

// readLoop moves transport output into the bounded TerminalConn backlog. Each
// invocation belongs to one transport generation.
func (s *Session) readLoop(generation int, transport Transport) {
	buffer := make([]byte, readChunkSize)
	for {
		if !s.conn.waitReadable() {
			return
		}
		n, err := transport.Read(buffer)
		if n > 0 {
			s.conn.appendOutput(buffer[:n])
		}
		if err != nil {
			s.post(command{
				kind:       commandTransportEnded,
				generation: generation,
				transport:  transport,
				end:        endOf(transport),
			})
			return
		}
	}
}

// endOf classifies a finished transport. EndReporter is authoritative; a
// transport that can only report an exit code maps a normal wait to "exited".
func endOf(transport Transport) End {
	if reporter, ok := transport.(EndReporter); ok {
		end := reporter.End()
		if end.Reason == "" {
			end.Reason = EndReasonError
		}
		return end
	}
	if waiter, ok := transport.(ExitWaiter); ok {
		if code, err := waiter.Wait(); err == nil {
			return End{Reason: EndReasonExited, ExitCode: code, HasExitCode: true}
		}
	}
	return End{Reason: EndReasonError}
}

// writeLoop drains the bounded input queue into one transport generation.
func (s *Session) writeLoop(generation int, transport Transport, genDone <-chan struct{}) {
	for {
		// Prefer leaving over consuming input for a transport this Session
		// has already abandoned.
		select {
		case <-s.ctx.Done():
			return
		case <-genDone:
			return
		default:
		}
		select {
		case <-s.ctx.Done():
			return
		case <-genDone:
			return
		case input := <-s.writeCh:
			if _, err := transport.Write(input); err != nil {
				s.post(command{
					kind:       commandTransportEnded,
					generation: generation,
					transport:  transport,
					end:        End{Reason: EndReasonError},
				})
				return
			}
		}
	}
}

func (s *Session) post(cmd command) {
	select {
	case s.commands <- cmd:
	case <-s.ctx.Done():
	case <-s.done:
	}
}

func (s *Session) update(change func(*Snapshot)) {
	s.mu.Lock()
	change(&s.snapshot)
	snapshot := s.snapshot
	s.mu.Unlock()
	if s.onChange != nil {
		s.onChange(snapshot)
	}
}

// sessionIDEncoding is Crockford's base32, the alphabet ULIDs use.
var sessionIDEncoding = base32.NewEncoding("0123456789ABCDEFGHJKMNPQRSTVWXYZ").WithPadding(base32.NoPadding)

// newSessionID returns a ULID: a 48-bit millisecond timestamp and 80 random
// bits, rendered as 26 Crockford base32 characters. The ID stays opaque to
// callers; only its uniqueness and sortable prefix matter.
func newSessionID() model.SessionID {
	var id [16]byte
	milliseconds := uint64(time.Now().UnixMilli())
	id[0] = byte(milliseconds >> 40)
	id[1] = byte(milliseconds >> 32)
	id[2] = byte(milliseconds >> 24)
	id[3] = byte(milliseconds >> 16)
	id[4] = byte(milliseconds >> 8)
	id[5] = byte(milliseconds)
	if _, err := rand.Read(id[6:]); err != nil {
		// crypto/rand does not fail on supported platforms, but keep IDs
		// distinct rather than panic if it ever does.
		binary.BigEndian.PutUint64(id[8:], uint64(time.Now().UnixNano()))
	}
	return model.SessionID(sessionIDEncoding.EncodeToString(id[:]))
}
