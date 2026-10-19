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

// Snapshot is the immutable state the UI consumes.
type Snapshot struct {
	ID     model.SessionID
	Kind   Kind
	Status Status
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
	commandExit
	commandClose
)

type command struct {
	kind    commandKind
	cols    int
	rows    int
	code    int
	hasCode bool
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
		writeCh:   make(chan []byte, s.writeDepth),
		commands:  make(chan command, 16),
		done:      make(chan struct{}),
		snapshot: Snapshot{
			ID:     id,
			Kind:   spec.Kind,
			Status: StatusRunning,
			Cols:   spec.Cols,
			Rows:   spec.Rows,
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
	go s.readLoop()
	go s.writeLoop()
}

// loop serializes lifecycle commands and publishes the final snapshot.
func (s *Session) loop() {
	status := StatusClosed
	code := -1
	hasCode := false
	for {
		select {
		case <-s.ctx.Done():
			goto finished
		case cmd := <-s.commands:
			switch cmd.kind {
			case commandResize:
				if err := s.transport.Resize(cmd.cols, cmd.rows); err != nil {
					continue
				}
				s.update(func(snapshot *Snapshot) {
					snapshot.Cols, snapshot.Rows = cmd.cols, cmd.rows
				})
			case commandExit:
				status, code, hasCode = StatusExited, cmd.code, cmd.hasCode
				goto finished
			case commandClose:
				status = StatusClosed
				goto finished
			}
		}
	}

finished:
	s.cancel()
	s.conn.finish()
	_ = s.transport.Close()
	s.update(func(snapshot *Snapshot) {
		snapshot.Status = status
		snapshot.ExitCode = code
		snapshot.HasExitCode = hasCode
	})
	close(s.done)
	if s.onEnd != nil {
		s.onEnd(s.id)
	}
}

// readLoop moves transport output into the bounded TerminalConn backlog.
func (s *Session) readLoop() {
	buffer := make([]byte, readChunkSize)
	for {
		if !s.conn.waitReadable() {
			return
		}
		n, err := s.transport.Read(buffer)
		if n > 0 {
			s.conn.appendOutput(buffer[:n])
		}
		if err != nil {
			code := -1
			hasCode := false
			if waiter, ok := s.transport.(ExitWaiter); ok {
				if waited, waitErr := waiter.Wait(); waitErr == nil {
					code, hasCode = waited, true
				}
			}
			s.post(command{kind: commandExit, code: code, hasCode: hasCode})
			return
		}
	}
}

// writeLoop drains the bounded input queue into the transport.
func (s *Session) writeLoop() {
	for {
		select {
		case <-s.ctx.Done():
			return
		case input := <-s.writeCh:
			if _, err := s.transport.Write(input); err != nil {
				s.post(command{kind: commandExit, code: -1})
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
