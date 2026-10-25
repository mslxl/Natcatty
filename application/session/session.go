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
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/mslxl/natcatty/domain/model"
	etmodel "github.com/mslxl/natcatty/domain/model/et"
	moshmodel "github.com/mslxl/natcatty/domain/model/mosh"
	"github.com/mslxl/natcatty/domain/model/serial"
	telnetmodel "github.com/mslxl/natcatty/domain/model/telnet"
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
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

// highlightIdleFlush bounds how long a partial line waits for its newline. A
// prompt has no trailing newline, so it is flushed after this quiet period.
const highlightIdleFlush = 30 * time.Millisecond

// maxHighlightCarry bounds the partial line held for highlighting.
const maxHighlightCarry = 64 << 10

// Kind is the protocol of a Session. Local shells, SSH, serial, Telnet, Mosh,
// and EternalTerminal exist in this slice.
type Kind string

const (
	// KindLocal is a shell running in a pseudo-terminal on this machine.
	KindLocal Kind = "local"
	// KindSSH is an interactive shell on a remote Host.
	KindSSH Kind = "ssh"
	// KindSerial is a device on a serial port.
	KindSerial Kind = "serial"
	// KindTelnet is an interactive terminal on a Telnet Host.
	KindTelnet Kind = "telnet"
	// KindMosh is a UDP Mosh Session bootstrapped over SSH.
	KindMosh Kind = "mosh"
	// KindET is an EternalTerminal Session using the bundled et client.
	KindET Kind = "et"
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
	// Notice is the most recent non-fatal runtime problem, such as a local X11
	// display that could not be reached. It is nil when there is none.
	Notice *Notice
	// CWD is the working directory the shell reported with OSC 7, or "".
	CWD string
	// Title is the terminal title from OSC 0/2, or "".
	Title string
	// Command is the command tracked from typed input and OSC 133 marks.
	Command CommandState
	// Input is the line being typed but not yet submitted. Autocomplete reads
	// it without reaching into the terminal's screen buffer.
	Input string
	// Busy reports recent output, a running command, or an active transfer.
	Busy bool
	// LastActivity is when terminal output last arrived.
	LastActivity time.Time
	// Transfer is the current or most recent file-transfer gate.
	Transfer TransferState
	// Notification is the most recent accepted program notification.
	Notification *TerminalNotification
	// Clipboard is the most recent OSC 52 request decision, without its data.
	Clipboard *ClipboardEvent
	// Logging is the current Session log state.
	Logging LogStatus
	// Encoding is the character encoding this Session decodes output and
	// encodes input with.
	Encoding domainterminal.Encoding
	// Stats are the byte, command, bell, notification, and transfer counters.
	Stats Stats
	// Hibernated reports that the Session's live view was released while the
	// transport and application state stayed alive. Resume reattaches one
	// view with the captured screen.
	Hibernated bool
}

// NoticeCode classifies a non-fatal runtime notice.
type NoticeCode string

const (
	// NoticeX11LocalDisplay reports that an X11 channel could not reach the
	// local X server.
	NoticeX11LocalDisplay NoticeCode = "x11_local_display"
	// NoticeX11Cookie reports that no local xauth cookie was available, so the
	// forwarded setup packet kept the fake cookie.
	NoticeX11Cookie NoticeCode = "x11_cookie"
	// NoticeX11Channel reports that an X11 channel could not be accepted.
	NoticeX11Channel NoticeCode = "x11_channel"
)

// Notice is a non-fatal problem reported while a Session keeps running.
type Notice struct {
	Code   NoticeCode
	Target string
}

// Notifier is implemented by transports that report non-fatal runtime notices.
// The channel closes when the transport stops reporting; a transport without a
// channel simply does not implement Notifier.
type Notifier interface {
	Notices() <-chan Notice
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
	Kind    Kind
	Command []string
	Dir     string
	Env     []string
	Cols    int
	Rows    int
	// Label names the Session for logging (usually the Host label). Empty
	// falls back to "session".
	Label    string
	HostID   model.HostID
	Hostname string
	Port     int
	Username string
	// X11Forwarding requests X11 forwarding for this SSH Session when the Host
	// has it enabled and acknowledged. The transport factory turns it into a
	// non-pooled Connection.
	X11Forwarding bool
	// X11Display overrides the local display target. Empty uses DISPLAY or the
	// platform default.
	X11Display string
	// NetworkDevice marks an SSH Host whose peer runs a vendor CLI. Programmatic
	// commands then use RawExec: they are written to the existing PTY as-is,
	// without a shell wrapper or completion markers, and report no exit code.
	NetworkDevice bool
	// Serial carries a serial Session's port and framing. It is required when
	// Kind is KindSerial and ignored by every other transport.
	Serial *serial.Config
	// Telnet carries a Telnet Session's endpoint and negotiation settings. It
	// is required when Kind is KindTelnet and ignored by every other
	// transport.
	Telnet *telnetmodel.Config
	// Mosh carries a Mosh Session's bootstrap settings. It is required when
	// Kind is KindMosh and ignored by every other transport.
	Mosh *moshmodel.Config
	// ET carries an EternalTerminal Session's endpoint and credentials. It is
	// required when Kind is KindET and ignored by every other transport.
	ET *etmodel.Config
	// Reconnect reopens the transport after a retryable end. A nil policy
	// disables reconnection, which is the rule for local shells.
	Reconnect *ReconnectPolicy
	// Encoding is the wire encoding for this Session. Empty asks the service's
	// resolver; an unresolved value is UTF-8.
	Encoding domainterminal.Encoding
	// EncodingScope is the persisted preference key. Empty derives "local" or
	// "host:<id>" from Kind and HostID.
	EncodingScope string
}

// ConnectionEvent reports one completed connection attempt. Err is nil when a
// Session opened; otherwise the attempt failed and SessionID is empty. The app
// records it as a connection log.
type ConnectionEvent struct {
	SessionID model.SessionID
	HostID    model.HostID
	Label     string
	Hostname  string
	Username  string
	// UnsavedHost marks a Host the user typed without a saved Vault record.
	// Such an attempt has no HostID but is not a local shell: it keeps the
	// typed label and address for the recorded row.
	UnsavedHost bool
	Kind        Kind
	StartedAt   time.Time
	EndedAt     time.Time
	Err         error
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

	tap             *terminalTap
	clipboardAccess domainterminal.ClipboardAccess
	codec           Codec
	highlighter     *domainterminal.Highlighter

	// rawExecMu serializes programmatic commands on the one interactive PTY.
	// rawMu guards the single raw-output subscriber that a running command
	// reads; it is nil when no raw command is running.
	rawExecMu sync.Mutex
	rawMu     sync.Mutex
	rawFeed   *rawOutputFeed

	// outputMu orders rendered output against hibernate/resume. replay holds
	// the bounded screen snapshot, feeds holds the observers, and the live
	// view's queue is appended under the same lock.
	outputMu sync.Mutex
	replay   snapshotBuffer
	feeds    map[*outputFeed]struct{}

	logMu   sync.Mutex
	logger  *sessionLogger
	lastLog LogStatus

	activitySeq   uint64
	activityTimer *time.Timer
	promptSeq     uint64
	promptTimer   *time.Timer

	// highlightMu guards a partial output line held so a keyword split across
	// two transport reads is still highlighted. Only the read-loop goroutine,
	// its idle timer, and transport end touch it.
	highlightMu    sync.Mutex
	highlightCarry []byte
	highlightTimer *time.Timer

	onNotification func(model.SessionID, TerminalNotification)
	onTransfer     func(model.SessionID, TransferState)
	onInput        func(model.SessionID, []byte)
	onCommand      func(model.SessionID, string, string)
	onBell         func(model.SessionID)

	onEnd    func(model.SessionID)
	onChange func(Snapshot)
}

type commandKind uint8

const (
	commandResize commandKind = iota
	commandTransportEnded
	commandTransportOpen
	commandTransportFailed
	commandReconnectNow
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
	// autoLog decides whether a new Session starts logging. Nil disables
	// automatic logs; the user can still start one from the Session surface.
	autoLog         func(StartSpec) *LogOptions
	clipboardPolicy func() domainterminal.ClipboardAccess
	encodingPolicy  func(StartSpec) domainterminal.Encoding
	codecFactory    CodecFactory
	onNotification  func(model.SessionID, TerminalNotification)
	onTransfer      func(model.SessionID, TransferState)
	onInput         func(model.SessionID, []byte)
	onCommand       func(model.SessionID, string, string)
	onBell          func(model.SessionID)
	onConnection    func(ConnectionEvent)
	highlightPolicy func() (bool, []domainterminal.KeywordHighlightRule)
	mu              sync.Mutex
	sessions        map[model.SessionID]*Session
	closing         bool
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

// WithAutoLogging decides the log options for each new Session. Returning nil
// leaves logging off. The policy comes from Settings, read when the Session
// opens.
func WithAutoLogging(fn func(StartSpec) *LogOptions) Option {
	return func(s *Service) { s.autoLog = fn }
}

// WithClipboardPolicy supplies the OSC 52 policy read when a Session opens.
// SetClipboardPolicy applies later Settings changes to live Sessions. Nil
// keeps the original write-only default.
func WithClipboardPolicy(fn func() domainterminal.ClipboardAccess) Option {
	return func(s *Service) { s.clipboardPolicy = fn }
}

// WithEncodingResolver supplies the initial encoding when a StartSpec does
// not name one. The app reads the per-Host preference here; nil means UTF-8.
func WithEncodingResolver(fn func(StartSpec) domainterminal.Encoding) Option {
	return func(s *Service) { s.encodingPolicy = fn }
}

// WithCodecFactory supplies the codec that converts a Session's legacy wire
// encoding. Without one a Session is UTF-8 only. The app injects the
// adapters/textcodec implementation.
func WithCodecFactory(factory CodecFactory) Option {
	return func(s *Service) { s.codecFactory = factory }
}

// WithNotificationHook receives accepted program notifications. The app applies
// the user's notification mode and window focus before showing a desktop
// notification.
func WithNotificationHook(fn func(model.SessionID, TerminalNotification)) Option {
	return func(s *Service) { s.onNotification = fn }
}

// WithTransferHook observes file-transfer detection. It is the injectable seam
// for a future transfer engine; the gate itself is always active.
func WithTransferHook(fn func(model.SessionID, TransferState)) Option {
	return func(s *Service) { s.onTransfer = fn }
}

// WithCommandHook receives each non-empty command submitted in a Session,
// together with its history scope. The app records it for autocomplete.
func WithCommandHook(fn func(model.SessionID, string, string)) Option {
	return func(s *Service) { s.onCommand = fn }
}

// WithBellHook receives a BEL from the transport. The app marks the Session's
// tab as having new activity, as the original did.
func WithBellHook(fn func(model.SessionID)) Option {
	return func(s *Service) { s.onBell = fn }
}

// WithConnectionHook receives one event per completed connection attempt. The
// app records it as a connection log. The hook runs on the dialing goroutine,
// before a successful Session joins the Service.
func WithConnectionHook(fn func(ConnectionEvent)) Option {
	return func(s *Service) { s.onConnection = fn }
}

// WithHighlightPolicy supplies the keyword highlight switch and rules read
// when a Session opens. SetHighlightPolicy applies later Settings changes to
// live Sessions.
func WithHighlightPolicy(fn func() (bool, []domainterminal.KeywordHighlightRule)) Option {
	return func(s *Service) { s.highlightPolicy = fn }
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
	startedAt := time.Now()
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
	if spec.EncodingScope == "" {
		if (spec.Kind == KindSSH || spec.Kind == KindTelnet) && spec.HostID != "" {
			spec.EncodingScope = domainterminal.EncodingScopeHost(string(spec.HostID))
		} else {
			spec.EncodingScope = domainterminal.EncodingScopeLocal
		}
	}
	if !spec.Encoding.Valid() {
		spec.Encoding = domainterminal.EncodingUTF8
		if s.encodingPolicy != nil {
			if candidate := s.encodingPolicy(spec); candidate.Valid() {
				spec.Encoding = candidate
			}
		}
	}
	spec.Encoding = domainterminal.NormalizeEncoding(spec.Encoding)
	if spec.Encoding != domainterminal.EncodingUTF8 && s.codecFactory == nil {
		return nil, fmt.Errorf("session: no codec factory for encoding %q", spec.Encoding)
	}
	codec := Codec(identityCodec{})
	if s.codecFactory != nil {
		codec = s.codecFactory(spec.Encoding)
	}
	if codec == nil {
		return nil, fmt.Errorf("session: codec factory returned nil for %q", spec.Encoding)
	}

	transport, err := s.factory.Open(ctx, spec)
	if err != nil {
		s.reportConnection(ConnectionEvent{
			HostID: spec.HostID, Label: specLabel(spec), Hostname: spec.Hostname,
			Username: spec.Username, Kind: spec.Kind, StartedAt: startedAt,
			EndedAt: time.Now(), Err: err,
		})
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		_ = transport.Close()
		s.reportConnection(ConnectionEvent{
			HostID: spec.HostID, Label: specLabel(spec), Hostname: spec.Hostname,
			Username: spec.Username, Kind: spec.Kind, StartedAt: startedAt,
			EndedAt: time.Now(), Err: err,
		})
		return nil, err
	}

	id := s.newID()
	sessionCtx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	policyProvider := s.clipboardPolicy
	s.mu.Unlock()
	policy := domainterminal.ClipboardWriteOnly
	if policyProvider != nil {
		if candidate := policyProvider(); candidate.Valid() {
			policy = candidate
		}
	}
	tap := newTerminalTap(policy)
	highlightEnabled, highlightRules := false, []domainterminal.KeywordHighlightRule(nil)
	if s.highlightPolicy != nil {
		highlightEnabled, highlightRules = s.highlightPolicy()
	}
	session := &Session{
		id:              id,
		ctx:             sessionCtx,
		cancel:          cancel,
		transport:       transport,
		factory:         s.factory,
		spec:            spec,
		tap:             tap,
		clipboardAccess: policy,
		codec:           codec,
		highlighter:     domainterminal.NewHighlighter(highlightEnabled, highlightRules),
		genDone:         make(chan struct{}),
		writeCh:         make(chan []byte, s.writeDepth),
		commands:        make(chan command, 16),
		done:            make(chan struct{}),
		onNotification:  s.onNotification,
		onTransfer:      s.onTransfer,
		onInput:         s.onInput,
		onCommand:       s.onCommand,
		onBell:          s.onBell,
		snapshot: Snapshot{
			ID:         id,
			Kind:       spec.Kind,
			Status:     StatusRunning,
			Connection: ConnectionStatusActive,
			Cols:       spec.Cols,
			Rows:       spec.Rows,
			Encoding:   spec.Encoding,
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

	s.reportConnection(ConnectionEvent{
		SessionID: id,
		HostID:    spec.HostID,
		Label:     specLabel(spec),
		Hostname:  spec.Hostname,
		Username:  spec.Username,
		Kind:      spec.Kind,
		StartedAt: startedAt,
		EndedAt:   time.Now(),
	})

	if s.autoLog != nil {
		if options := s.autoLog(spec); options != nil {
			if options.Label == "" {
				options.Label = spec.Label
			}
			session.startAutoLog(*options)
		}
	}

	session.start()
	return session, nil
}

// OpenLocal opens the platform's local shell in a new Session.
func (s *Service) OpenLocal(ctx context.Context) (*Session, error) {
	// The original names local Session logs "Local Terminal".
	return s.Open(ctx, StartSpec{Kind: KindLocal, Label: "Local Terminal"})
}

// Label names the Session for logging. It is the Host label the shell opened,
// or "Local Terminal" for a local shell.
func (s *Session) Label() string { return specLabel(s.spec) }

// specLabel resolves the display label a Session and its connection log use.
func specLabel(spec StartSpec) string {
	if spec.Label != "" {
		return spec.Label
	}
	switch spec.Kind {
	case KindSSH, KindTelnet:
		if spec.Hostname != "" {
			return spec.Hostname
		}
		if spec.Telnet != nil && spec.Telnet.Hostname != "" {
			return spec.Telnet.Hostname
		}
	case KindLocal:
		return "Local Terminal"
	case KindSerial:
		if spec.Serial != nil {
			return SerialLabel(*spec.Serial)
		}
		return "Serial"
	}
	return "session"
}

// SerialLabel is the Tab label for a serial configuration, matching the
// original's "Serial: <port>" title.
func SerialLabel(cfg serial.Config) string {
	return "Serial: " + serial.PortName(cfg.Path)
}

// reportConnection delivers one attempt outcome to the app's hook. It is safe
// on the dialing goroutine.
func (s *Service) reportConnection(event ConnectionEvent) {
	if s.onConnection == nil {
		return
	}
	if event.Kind == "" {
		event.Kind = KindLocal
	}
	// A Host attempt without a saved identity is a typed (unsaved) Host. A
	// local shell and a serial device legitimately have no Host identity.
	event.UnsavedHost = event.HostID == "" && event.Kind != KindLocal && event.Kind != KindSerial
	s.onConnection(event)
}

// SessionInfo returns the Host identity, display label, and kind of a live
// Session. The command hook uses it to associate a recorded command with the
// terminal it ran in.
func (s *Service) SessionInfo(id model.SessionID) (model.HostID, string, Kind, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[id]
	if !ok {
		return "", "", "", false
	}
	return session.spec.HostID, session.Label(), session.spec.Kind, true
}

// SetClipboardPolicy applies an OSC 52 policy change to every live Session
// and to Sessions opened later. The app calls it from the Settings change
// hook, matching the original's live policy lookup.
func (s *Service) SetClipboardPolicy(policy domainterminal.ClipboardAccess) {
	if !policy.Valid() {
		return
	}
	s.mu.Lock()
	s.clipboardPolicy = func() domainterminal.ClipboardAccess { return policy }
	live := make([]*Session, 0, len(s.sessions))
	for _, session := range s.sessions {
		live = append(live, session)
	}
	s.mu.Unlock()
	for _, session := range live {
		session.setClipboardAccess(policy)
	}
}

// SetHighlightPolicy applies a keyword highlight change to every live Session
// and to Sessions opened later. Rules are normalized by the domain highlighter.
func (s *Service) SetHighlightPolicy(enabled bool, rules []domainterminal.KeywordHighlightRule) {
	highlighter := domainterminal.NewHighlighter(enabled, rules)
	storedRules := append([]domainterminal.KeywordHighlightRule(nil), rules...)
	s.mu.Lock()
	s.highlightPolicy = func() (bool, []domainterminal.KeywordHighlightRule) {
		return enabled, storedRules
	}
	live := make([]*Session, 0, len(s.sessions))
	for _, session := range s.sessions {
		live = append(live, session)
	}
	s.mu.Unlock()
	for _, session := range live {
		session.setHighlighter(highlighter)
	}
}

func (s *Session) setHighlighter(highlighter *domainterminal.Highlighter) {
	if highlighter == nil {
		return
	}
	s.mu.Lock()
	s.highlighter = highlighter
	s.mu.Unlock()
}

// highlightChunk appends output to the highlight carry and emits complete
// lines. Holding the trailing partial line keeps a keyword split across two
// reads intact; a short idle flush emits it when the program wrote no newline,
// as a prompt does.
func (s *Session) highlightChunk(p []byte) {
	if !s.highlightActive() {
		s.emitOutput(p)
		return
	}
	s.highlightMu.Lock()
	defer s.highlightMu.Unlock()
	s.highlightCarry = append(s.highlightCarry, p...)
	flushed := 0
	if index := bytes.LastIndexByte(s.highlightCarry, '\n'); index >= 0 {
		flushed = index + 1
	}
	if len(s.highlightCarry) > maxHighlightCarry || !endsInHighlightWord(s.highlightCarry) {
		flushed = len(s.highlightCarry)
	}
	if flushed > 0 {
		part := append([]byte(nil), s.highlightCarry[:flushed]...)
		s.highlightCarry = s.highlightCarry[flushed:]
		s.emitOutput(s.highlightOutput(part))
	}
	if len(s.highlightCarry) > 0 {
		if s.highlightTimer == nil {
			s.highlightTimer = time.AfterFunc(highlightIdleFlush, s.flushHighlightCarry)
		}
		return
	}
	if s.highlightTimer != nil {
		s.highlightTimer.Stop()
		s.highlightTimer = nil
	}
}

// highlightActive reports whether a highlighter is installed. With none, output
// takes the old path and is never delayed.
func (s *Session) highlightActive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.highlighter != nil
}

// endsInHighlightWord reports whether data ends in a character a keyword could
// continue through. Only then is holding it worth a possible delay; a newline,
// prompt marker, or other punctuation can never complete a split match.
func endsInHighlightWord(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	last := data[len(data)-1]
	return last >= 0x80 || last == '_' || last == '-' || last == '.' ||
		last == '/' || last == ':' ||
		(last >= '0' && last <= '9') ||
		(last >= 'A' && last <= 'Z') ||
		(last >= 'a' && last <= 'z')
}

// flushHighlightCarry emits the partial line and stops the idle timer.
func (s *Session) flushHighlightCarry() {
	s.highlightMu.Lock()
	defer s.highlightMu.Unlock()
	s.highlightTimer = nil
	if len(s.highlightCarry) == 0 {
		return
	}
	part := append([]byte(nil), s.highlightCarry...)
	s.highlightCarry = nil
	s.emitOutput(s.highlightOutput(part))
}

func (s *Session) highlightOutput(p []byte) []byte {
	s.mu.Lock()
	highlighter := s.highlighter
	s.mu.Unlock()
	if highlighter == nil {
		return p
	}
	return highlighter.Apply(p)
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

// ID returns the Session's stable runtime identifier.
func (s *Session) ID() model.SessionID { return s.id }

// HostID returns the Vault Host the Session was opened for. A local Session
// has no Host and reports "".
func (s *Session) HostID() model.HostID { return s.spec.HostID }

// Done is closed once the Session has ended, its final snapshot is published,
// and its Service has unregistered it. After Done fires, input addressed to the
// Session by ID reports DeliveryMissing rather than DeliveryFailed.
func (s *Session) Done() <-chan struct{} { return s.done }

// Snapshot returns the Session's current state.
func (s *Session) Snapshot() Snapshot {
	now := time.Now()
	s.mu.Lock()
	snapshot := s.snapshot
	s.mu.Unlock()
	snapshot.PendingBytes, snapshot.Paused = s.conn.stats()
	state := s.tap.state(now)
	snapshot.CWD = state.CWD
	snapshot.Title = state.Title
	snapshot.Command = state.Command
	snapshot.Input = state.Input
	snapshot.Busy = state.Busy
	snapshot.LastActivity = state.LastActivity
	snapshot.Transfer = state.Transfer
	snapshot.Notification = state.Notification
	snapshot.Clipboard = state.Clipboard
	snapshot.Stats = state.Stats
	snapshot.Logging = s.logStatus()
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

// CanReconnect reports whether ReconnectNow can reopen the transport: the
// Session has a reconnect policy and has not ended.
func (s *Session) CanReconnect() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.spec.Reconnect != nil && s.ctx.Err() == nil &&
		s.snapshot.Status == StatusRunning &&
		s.snapshot.Connection != ConnectionStatusReconnecting
}

// ReconnectNow abandons the current transport and dials again immediately
// instead of waiting for the policy's delay. It reports whether the request
// was accepted. The Session's shell restarts, as any reconnect does.
func (s *Session) ReconnectNow() bool {
	if !s.CanReconnect() {
		return false
	}
	select {
	case s.commands <- command{kind: commandReconnectNow}:
		return true
	default:
		return false
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
	go s.noticeLoop(s.transport, s.genDone)
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
				go s.reconnect(s.generation, false)
			case commandReconnectNow:
				if s.reopening || s.spec.Reconnect == nil {
					continue
				}
				// Advance the generation before closing the live transport so
				// its end event is ignored, as an automatic reconnect does.
				s.generation++
				s.reopening = true
				close(s.genDone)
				s.genDone = make(chan struct{})
				if s.transport != nil {
					_ = s.transport.Close()
				}
				s.update(func(snapshot *Snapshot) {
					snapshot.Connection = ConnectionStatusReconnecting
				})
				go s.reconnect(s.generation, true)
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
					snapshot.Notice = nil
				})
				go s.readLoop(s.generation, cmd.transport)
				go s.writeLoop(s.generation, cmd.transport, s.genDone)
				go s.noticeLoop(cmd.transport, s.genDone)
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
	s.stopActivityTimer()
	s.stopPromptTimer()
	s.conn.finish()
	s.closeObservations()
	if s.transport != nil {
		_ = s.transport.Close()
	}
	logging := s.finishLogging()
	s.update(func(snapshot *Snapshot) {
		snapshot.Status = status
		snapshot.Connection = connection
		snapshot.ExitCode = code
		snapshot.HasExitCode = hasCode
		snapshot.Logging = logging
		// A hibernation only names a live transport whose screen was
		// detached; the transport is gone now.
		snapshot.Hibernated = false
	})
	// Unregister before announcing the end; see Done.
	if s.onEnd != nil {
		s.onEnd(s.id)
	}
	close(s.done)
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
func (s *Session) reconnect(generation int, immediate bool) {
	delay := s.spec.Reconnect.delay()
	initial := delay
	if immediate {
		// Only the first attempt skips the policy delay. A failed manual
		// attempt falls back to the same interval as an automatic retry.
		initial = 0
	}
	timer := time.NewTimer(initial)
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
			now := time.Now()
			decoded := s.decodeOutput(buffer[:n])
			// A running raw command sees the transport output before the tap:
			// its completion detection must not depend on prompt tracking or
			// history effects.
			s.publishRawOutput(decoded)
			output, effects := s.tap.observe(decoded, now)
			if len(output) > 0 {
				s.writeLog(logOutput(output))
				s.highlightChunk(output)
			}
			if effects.Changed {
				s.publishTap(now)
				s.scheduleActivityClear()
			}
			if effects.PromptRecheck {
				s.schedulePromptRecheck()
			}
			if effects.Bell && s.onBell != nil {
				s.onBell(s.id)
			}
			if effects.Notification != nil && s.onNotification != nil {
				s.onNotification(s.id, *effects.Notification)
			}
			if effects.Transfer != nil && s.onTransfer != nil {
				s.onTransfer(s.id, *effects.Transfer)
			}
		}
		if err != nil {
			s.flushHighlightCarry()
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

// noticeLoop publishes non-fatal notices from one transport generation. It
// stops when the Session ends or the generation is abandoned.
func (s *Session) noticeLoop(transport Transport, genDone <-chan struct{}) {
	notifier, ok := transport.(Notifier)
	if !ok {
		return
	}
	notices := notifier.Notices()
	if notices == nil {
		return
	}
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-genDone:
			return
		case notice, ok := <-notices:
			if !ok {
				return
			}
			s.update(func(snapshot *Snapshot) {
				snapshot.Notice = &notice
			})
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
