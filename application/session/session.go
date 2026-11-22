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
	"os/exec"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	appconnection "github.com/mslxl/natcatty/application/connection"
	"github.com/mslxl/natcatty/application/session/autologin"
	domainhistory "github.com/mslxl/natcatty/domain/history"
	"github.com/mslxl/natcatty/domain/model"
	etmodel "github.com/mslxl/natcatty/domain/model/et"
	moshmodel "github.com/mslxl/natcatty/domain/model/mosh"
	"github.com/mslxl/natcatty/domain/model/serial"
	telnetmodel "github.com/mslxl/natcatty/domain/model/telnet"
	modelterminal "github.com/mslxl/natcatty/domain/model/terminal"
	modelvnc "github.com/mslxl/natcatty/domain/model/vnc"
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
	// KindET is an EternalTerminal Session using the in-process et client.
	KindET Kind = "et"
	// KindVNC is a pixel Session on a VNC Host. It carries a DisplaySession
	// instead of a byte stream.
	KindVNC Kind = "vnc"
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
	// EndReasonDetached is a tmux client detach. The remote session keeps
	// running, so the local Session ended cleanly by design and its Tab closes
	// without an error; the next connection reattaches.
	EndReasonDetached EndReason = "detached"
	// EndReasonDisplaced reports that another client took over an ET session
	// or the server reset its history. ET has one client slot per session, so
	// reconnecting would fight the new owner; the transport reports a notice
	// instead of retrying.
	EndReasonDisplaced EndReason = "displaced"
	// EndReasonAuthFailed reports an authentication or host-key rejection on a
	// live channel, such as an RFB security handshake. A Session never retries
	// it; the user reconnects explicitly after fixing the credential.
	EndReasonAuthFailed EndReason = "auth-failed"
)

// End is a transport's final state.
type End struct {
	Reason      EndReason
	ExitCode    int
	HasExitCode bool
	// NoRetry marks a permanent failure that must not be retried even though
	// Reason is EndReasonError. A display error implements Permanent() to stop
	// reconnecting, but only authentication and host-key failures are allowed
	// to claim EndReasonAuthFailed; an X11 or similar permanent refusal keeps
	// the generic reason and carries the no-retry decision here instead.
	NoRetry bool
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
	if end.NoRetry {
		// A permanent failure the transport already classified never improves
		// by retrying, regardless of the end reason it displays.
		return false
	}
	if end.Reason == EndReasonAuthFailed {
		// A rejected credential never improves by retrying. This check is
		// authoritative even for a caller-supplied predicate.
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
	// EndReason classifies a finished Session. It is empty while the Session
	// runs and while it is reconnecting. A detached tmux client reports
	// EndReasonDetached instead of a shell exit.
	EndReason EndReason
	// Cols and Rows are the last size sent to the transport.
	Cols, Rows int
	// DisplayWidth and DisplayHeight are the remote framebuffer size of a
	// pixel Session. They stay zero for a byte-stream Session.
	DisplayWidth, DisplayHeight int
	// PendingBytes is the output waiting for the terminal to read.
	PendingBytes int
	// Paused reports that output backpressure is engaged.
	Paused bool
	// Notice is the most recent non-fatal runtime problem, such as a local X11
	// display that could not be reached. It is nil when there is none.
	Notice *Notice
	// CWD is the working directory the shell reported with OSC 7, or for a
	// local shell that never reports it, the probed process directory. It is
	// empty when neither source knows.
	CWD string
	// Title is the terminal title from OSC 0/2, or "".
	Title string
	// Command is the command tracked from typed input and OSC 133 marks.
	Command CommandState
	// Input is the line being typed but not yet submitted. Autocomplete reads
	// it without reaching into the terminal's screen buffer.
	Input string
	// InputCursor is the cursor's rune offset within Input. It mirrors the
	// line editor's cursor so an accepted suggestion replaces only the text
	// left of it and leaves the rest of the line intact.
	InputCursor int
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
	// NoticeTmuxRestored reports that a connection attached to an existing
	// tmux session.
	NoticeTmuxRestored NoticeCode = "tmux_session_restored"
	// NoticeTmuxNested reports that the remote already ran inside tmux, so the
	// app connected without nesting.
	NoticeTmuxNested NoticeCode = "tmux_nested"
	// NoticeTmuxUnavailable reports that tmux was missing or could not run, so
	// the app fell back to a plain login shell.
	NoticeTmuxUnavailable NoticeCode = "tmux_unavailable"
	// NoticeETDisplaced reports that another client took over this ET
	// session's single client slot, or that the server reset the session
	// behind this client. ET has no multi-attach: the newest client wins.
	NoticeETDisplaced NoticeCode = "et_session_displaced"
	// NoticeETResumeFallback reports that a saved ET session could not be
	// reattached because the server lacks the v7 reattach capability. The
	// stale secret was cleared and a fresh session was started instead.
	NoticeETResumeFallback NoticeCode = "et_resume_fallback"
	// NoticeCaptureHookUnavailable reports that the session-scoped shell hook
	// could not be installed, so command capture falls back to native OSC
	// 133/7 or typed input. It is never a connection failure.
	NoticeCaptureHookUnavailable NoticeCode = "capture_hook_unavailable"
	// NoticeHostAtuinDetected reports that the Host already runs Atuin.
	// Netcatty imports its history read-only and skips its own capture hook, so
	// the notice explains why commands captured in this Session stay transient.
	NoticeHostAtuinDetected NoticeCode = "host_atuin_detected"
	// NoticeHostAtuinImportFailed reports that the Host runs Atuin but this
	// read-only import attempt failed. The capture hook stays skipped and a
	// later Session retries; the notice makes the failure visible instead of
	// silently leaving the Host without Netcatty capture.
	NoticeHostAtuinImportFailed NoticeCode = "host_atuin_import_failed"
	// NoticeVNCPlaintext reports that an unrouted VNC Session negotiated a
	// security type that leaves the wire unencrypted. The app shows it once per
	// Host and records the acknowledgement; a routed Host never receives it.
	NoticeVNCPlaintext NoticeCode = "vnc_plaintext"
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

// CWDProbe is implemented by a local transport that can read the shell
// process's working directory. It is the fallback when the shell never
// reports OSC 7. A remote transport leaves it unimplemented, so its cwd stays
// unknown rather than being read from this machine.
type CWDProbe interface {
	WorkingDirectory() (string, error)
}

// CaptureHookExpectedReporter is implemented by transports that decide after
// connecting whether the planned capture hook was actually installed. A remote
// peer's shell is only known once its terminal opens: a Windows OpenSSH peer
// with an unwrappable registered shell, an unreadable default-shell setting,
// or a single-channel connection opens a plain login shell instead of the
// planned wrapper. Reporting false makes the Session capture the first typed
// command through the ordinary Tier 2/3 path instead of reserving it for a
// handshake that will never arrive.
type CaptureHookExpectedReporter interface {
	CaptureHookExpected() bool
}

// CaptureHookWithdrawn is implemented by a transport that dropped the planned
// capture hook before the shell started. An SSH transport withdraws the hook
// when it cannot establish that the login shell accepts the POSIX wrapper, for
// example when the login shell is xonsh, csh, or tcsh. The Session must then
// stop reserving its first typed command for a handshake that will never
// arrive and keep the ordinary native or typed fallback.
type CaptureHookWithdrawn interface {
	CaptureHookWithdrawn() bool
}

// ResumePreserver is implemented by transports that can keep their resume
// credentials in the secret store past Close. Close clears them by default; the
// Session service preserves them when it abandons a transport for a reconnect
// and when the whole app shuts down.
type ResumePreserver interface {
	PreserveResume()
}

// preserveTransportResume keeps a resumable transport's credentials for a
// later reattach. Transports without resume state ignore it.
func preserveTransportResume(transport Transport) {
	if preserver, ok := transport.(ResumePreserver); ok {
		preserver.PreserveResume()
	}
}

// ResumeDiscarder is implemented by resumable transports that must clear the
// credentials after an earlier PreserveResume. An explicit Session close uses
// it when the transport was already closed for a reconnect handover.
type ResumeDiscarder interface {
	DiscardResume()
}

// discardTransportResume clears a resumable transport's credentials even if
// PreserveResume was called before. Transports without resume state ignore it.
func discardTransportResume(transport Transport) {
	if discarder, ok := transport.(ResumeDiscarder); ok {
		discarder.DiscardResume()
	}
}

// ScreenSizer is implemented by transports whose remote peer owns the screen
// size (mosh). The handler is called from the transport before it makes bytes
// encoded for that size available to Read, and it returns only after the live
// screen applied the size. Position is the number of output bytes the
// transport produced before the resize, so the Session can deliver every
// earlier byte to the view at the old size first. The Session installs the
// handler that carries the signal to the attached view.
type ScreenSizer interface {
	SetScreenSizeHandler(handler func(cols, rows int, position uint64))
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
	Label string
	// StartupCommand, when non-empty, is typed once after the shell produces
	// its first output on each connection, and replayed after every reconnect.
	// It travels as ordinary input, so the shell's own line editor handles it.
	StartupCommand string
	HostID         model.HostID
	Hostname       string
	Port           int
	Username       string
	// Password is a one-shot credential for an SSH target with no saved Host,
	// such as Quick Connect. It is memory-only: it is never persisted in the
	// Workspace or the Vault, and a reconnect reuses the value the Session
	// already holds. Empty asks the transport to resolve the Host's saved
	// credentials.
	Password string
	// X11Forwarding requests X11 forwarding for this SSH Session when the Host
	// has it enabled and acknowledged. The transport factory turns it into a
	// non-pooled Connection.
	X11Forwarding bool
	// X11Display overrides the local display target. Empty uses DISPLAY or the
	// platform default.
	X11Display string
	// RemoteCommand, when non-empty, makes an SSH transport request a PTY and
	// run this command instead of the login shell. Empty keeps the login-shell
	// behavior. Other transports ignore it.
	RemoteCommand string
	// CaptureHookRemote marks an SSH Session whose interactive shell startup
	// the session capture hook owns. A transport that learns the peer platform
	// after connecting may replace RemoteCommand with the wrapper for that
	// platform; a transport that does not leaves it unchanged. It may also
	// withdraw the wrapper for a login shell that cannot parse a POSIX command,
	// so xonsh, csh, or tcsh never receives one. The flag is only set by the
	// capture hook planner and never accompanies a user-supplied RemoteCommand.
	// Other transports ignore it.
	CaptureHookRemote bool
	// AutoAttachTmux asks an SSH transport to keep the remote shell in the
	// Host's deterministic tmux session. The transport probes the remote and
	// falls back to a plain login shell when tmux is unavailable or already
	// nested, so the flag never makes a connection fail. Other transports
	// ignore it, and an explicit RemoteCommand takes precedence.
	AutoAttachTmux bool
	// NetworkDevice marks an SSH Host whose peer runs a vendor CLI. Programmatic
	// commands then use RawExec: they are written to the existing PTY as-is,
	// without a shell wrapper or completion markers, and report no exit code.
	NetworkDevice bool
	// InputCompatibility translates typed Enter and Backspace keys before
	// they reach the wire. It applies only to KindSSH; every other transport
	// keeps its own input behavior. The zero value is byte-identical to
	// today.
	InputCompatibility modelterminal.InputCompatibility
	// Serial carries a serial Session's port and framing. It is required when
	// Kind is KindSerial and ignored by every other transport.
	Serial *serial.Config
	// Telnet carries a Telnet Session's endpoint and negotiation settings. It
	// is required when Kind is KindTelnet and ignored by every other
	// transport.
	Telnet *telnetmodel.Config
	// AutoLogin, when non-nil, answers a Serial or Telnet console's login and
	// password prompts. It is ignored by every other transport, which
	// authenticates before its shell exists.
	AutoLogin *autologin.Options
	// Mosh carries a Mosh Session's bootstrap settings. It is required when
	// Kind is KindMosh and ignored by every other transport.
	Mosh *moshmodel.Config
	// ET carries an EternalTerminal Session's endpoint and credentials. It is
	// required when Kind is KindET and ignored by every other transport.
	ET *etmodel.Config
	// VNC carries a VNC Session's endpoint, credentials, viewer configuration,
	// and route selection. It is required when Kind is KindVNC and ignored by
	// every other transport.
	VNC *modelvnc.SessionConfig
	// ResumeKey locates a saved ET session in the secret store so a new
	// process can reattach it. It is an opaque, non-secret identifier; the
	// session id and passkey live only in the store. Empty disables reattach.
	ResumeKey string
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

// CaptureHookPlan is the rewrite a session-scoped capture hook applies before
// the transport opens. Spec is the StartSpec to use. HookExpected reports that
// the hook was actually injected, so its first private sequence or prompt
// handshake is still to come; a typed command before that point must stay
// tentative instead of being finalized by the shell's own prompt marks.
// Cleanup releases the plan's session-scoped resources, such as temporary
// startup files, when the Session ends; nil means nothing to release.
type CaptureHookPlan struct {
	Spec         StartSpec
	HookExpected bool
	Cleanup      func()
}

// CaptureHook installs a shell hook that raises capture fidelity to Tier 1.
// Plan runs once per Open on the dialing goroutine, before the transport
// opens. It must never fail an Open: an unmodified Spec falls back to native
// capture. The bool reports that the hook could not be installed, so the app
// announces one capability notice per Host instead of a connection failure.
type CaptureHook interface {
	Plan(spec StartSpec) (CaptureHookPlan, bool)
}

// HostAtuinProbe reports the result of one Host-Atuin detection. Present means
// the Host runs Atuin, so the Netcatty capture hook is skipped and completions
// stay transient; Failed reports that detection or import could not complete
// and the copy retries later.
type HostAtuinProbe struct {
	Present bool
	// Failed reports that the Host has Atuin but this attempt could not read
	// its history. The hook stays skipped and the copy retries later, so the
	// Session publishes a warning instead of failing the Open.
	Failed bool
}

// HostAtuin is the optional Host-Atuin compatibility path. Probe runs on the
// dialing goroutine before the capture hook and the terminal transport, over a
// separate read-only file channel; it must never write the Host or run the
// Atuin binary. MarkNoticed records and reports the one-time Host-Atuin notice
// after the Session opens, so a failed Open does not consume it.
// ImportAfterClose runs once when a Session that skipped the hook ends, to pick
// up rows Atuin wrote during the Session; it must not block teardown.
type HostAtuin interface {
	Probe(ctx context.Context, spec StartSpec) HostAtuinProbe
	MarkNoticed(ctx context.Context, spec StartSpec) bool
	ImportAfterClose(spec StartSpec)
}

// hostAtuinProbeTimeout bounds detection before the terminal opens. A probe
// that times out before any evidence is treated as absence, so the Netcatty
// hook runs; after evidence the Host stays present, the hook stays skipped,
// and the import-failure notice is shown. A later Session retries.
const hostAtuinProbeTimeout = 15 * time.Second

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

	// displayMu guards the current pixel Session. The command loop swaps it on
	// reconnect; the viewer's send methods read it from any goroutine.
	displayMu sync.RWMutex
	display   *SerializedDisplay
	// displayEvents is the Session's own single-consumer event stream. The
	// display loop drains the adapter's channel and forwards every event here,
	// so the adapter still has exactly one consumer. It closes once, at
	// teardown, and never between reconnect generations.
	displayEvents chan DisplayEvent
	// displayDone closes exactly once after displayEvents.
	displayDone sync.Once
	// displayLoops tracks every display-generation goroutine so teardown waits
	// before closing the Session event stream.
	displayLoops sync.WaitGroup

	// generation changes whenever the transport is replaced. The loop is the
	// only writer; readers and writers carry the generation they were started
	// with so a late event from an abandoned transport is ignored.
	generation int
	reopening  bool
	genDone    chan struct{}

	commands chan command
	// closeCh is a one-slot wake-up for an explicit close. It is buffered so
	// Close never waits for a command loop that is inside a blocking transport
	// call; pendingClose carries the request itself.
	closeCh chan struct{}
	writeCh chan []byte
	done    chan struct{}
	// writeMu serializes transport writes between the write loop and the
	// one-shot startup command, so their bytes never interleave inside a
	// chunk.
	writeMu sync.Mutex

	// remoteScreen reports that the transport receives its authoritative
	// screen size from the peer. The UI sets MyGo's ResizeByConn option and
	// registers the inverse resize sink for such a Session.
	remoteScreen atomic.Bool

	closeOnce sync.Once
	// closeMu guards the explicit close record and the ended publication.
	// Resume calls are applied outside it (they may touch the secret store),
	// but taking a record and publishing Done share one critical section so a
	// close racing teardown is never lost.
	closeMu      sync.Mutex
	pendingClose *closeRequest
	ended        bool
	mu           sync.Mutex
	snapshot     Snapshot

	// closedMu guards closedTransports. A transport can be closed by the end
	// handler and again by the final teardown path; recording it here keeps a
	// non-idempotent DisplayProvider from closing its event channel twice.
	closedMu         sync.Mutex
	closedTransports []Transport

	tap             *terminalTap
	clipboardAccess domainterminal.ClipboardAccess
	// captureCleanup releases the capture hook's session-scoped resources. The
	// command loop calls it exactly once during teardown.
	captureCleanup func()
	// hostAtuin is the detection/import policy for a Host that skipped the
	// capture hook, and hostAtuinActive records that this Session chose it.
	hostAtuin       HostAtuin
	hostAtuinActive bool
	codec           Codec
	highlighter     *domainterminal.Highlighter
	inputTranslator *inputTranslator

	// rawExecMu serializes programmatic commands on the one interactive PTY.
	// rawMu guards the single raw-output subscriber that a running command
	// reads; it is nil when no raw command is running.
	rawExecMu sync.Mutex
	rawMu     sync.Mutex
	rawFeed   *rawOutputFeed

	// internalMu guards internalMarkers. Every wrapper this Session types
	// carries an unguessable marker; the shell hook captures the wrapper line
	// exactly like user input, so a completion carrying a marker belongs to
	// the app's execution path and is never published as Command History.
	// A marker is dropped when its own capture is suppressed, so a long
	// Session does not accumulate one entry per command; internalMarkerOrder
	// bounds the set if a capture never arrives, for example when a shell
	// dies mid-command.
	internalMu          sync.Mutex
	internalMarkers     map[string]struct{}
	internalMarkerOrder []string

	// commandReady records that the interactive shell has answered a
	// programmatic wrapper since it started, reconnected, or was interrupted.
	// A remote shell (and any shell after Ctrl+C) may not be at a prompt yet;
	// Command types a no-op probe until it answers, so the real command cannot
	// be swallowed by a line editor that is still aborting. A local shell is
	// ready by construction.
	commandReady atomic.Bool

	// startupMu guards the one-shot Host startup command and the prompt
	// readiness that releases it. It is armed for each transport generation and
	// disarmed before the command is queued, so an output burst can never replay
	// it within one connection. The generation keeps a late read or timer from
	// the abandoned transport from firing the new connection's command.
	startupMu               sync.Mutex
	startupPending          bool
	startupGeneration       int
	startupTransport        Transport
	startupTail             []byte
	startupTimer            *time.Timer
	startupReadinessTimeout time.Duration
	startupSchedule         func(time.Duration, func()) *time.Timer

	// autoLoginMu guards the console detector for the current transport
	// generation. It is replaced on reconnect and disarmed when the transport
	// ends, so a stale prompt can never write to a replacement connection.
	autoLoginMu         sync.Mutex
	autoLogin           *autologin.Engine
	autoLoginGeneration int

	// cwdProbeMu guards the optional local-shell working-directory probe and
	// its cache. A consumer asks for the value; a finished command or a shell
	// prompt marks it stale, so the next ask reads the process again.
	cwdProbeMu    sync.Mutex
	cwdProbe      CWDProbe
	cwdProbeValue string
	cwdProbeValid bool
	cwdProbeStale bool

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

	onNotification      func(model.SessionID, TerminalNotification)
	onTransfer          func(model.SessionID, TransferState)
	onInput             func(model.SessionID, []byte)
	onCommandCompletion func(CommandEvent)
	onBell              func(model.SessionID)

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

// closeRequest is the Session's one explicit close. The command loop or
// teardown takes it under closeMu exactly once, so its preserve/discard effect
// happens once even when Close races the end of the Session.
type closeRequest struct {
	// preserve keeps resumable transport credentials when this close is an
	// app shutdown rather than the user ending the Session.
	preserve bool
}

// permanentFailure is implemented by typed open failures that must not be
// retried, namely authentication and host-key rejections, and an X11
// forwarding refusal. Every permanent failure stops the reconnect loop, but
// only authentication and host-key failures are classified as an authentication
// failure for display; see endReasonFor.
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
	autoLog                 func(StartSpec) *LogOptions
	startupReadinessTimeout time.Duration
	// startupSchedule replaces the readiness fallback timer. It is nil in
	// production; tests inject it to observe the default bound and fire it
	// without waiting.
	startupSchedule     func(time.Duration, func()) *time.Timer
	clipboardPolicy     func() domainterminal.ClipboardAccess
	encodingPolicy      func(StartSpec) domainterminal.Encoding
	codecFactory        CodecFactory
	onNotification      func(model.SessionID, TerminalNotification)
	onTransfer          func(model.SessionID, TransferState)
	onInput             func(model.SessionID, []byte)
	onCommandCompletion func(CommandEvent)
	onBell              func(model.SessionID)
	onConnection        func(ConnectionEvent)
	highlightPolicy     func() (bool, []domainterminal.KeywordHighlightRule)
	captureHook         CaptureHook
	hostAtuin           HostAtuin
	// localShell resolves the configured custom shell for a local Session. It
	// runs before the capture-hook planner so the hook wraps the user's shell
	// instead of the OS login shell; nil keeps the OS default.
	localShell func() []string
	mu         sync.Mutex
	sessions   map[model.SessionID]*Session
	// captureNotices records the Host whose capture-hook failure was already
	// announced, so a reconnect loop does not repeat the capability notice.
	captureNotices map[string]bool
	closing        bool
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

// WithStartupReadinessTimeout bounds how long a Host startup command waits for
// output that looks like a shell prompt. A device that never prints a
// recognized prompt still receives its command after this timeout; zero keeps
// the default. It exists for a slow console and for tests.
func WithStartupReadinessTimeout(timeout time.Duration) Option {
	return func(s *Service) {
		if timeout > 0 {
			s.startupReadinessTimeout = timeout
		}
	}
}

// withStartupReadinessScheduler replaces the fallback timer. Tests use it to
// assert the production default bound and to fire the callback deterministically.
// The callback must not run synchronously: callers hold startupMu while
// scheduling, and the callback locks it.
func withStartupReadinessScheduler(schedule func(time.Duration, func()) *time.Timer) Option {
	return func(s *Service) { s.startupSchedule = schedule }
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

// WithCommandCompletionHook receives each command when it finishes or its
// Session ends, never when Enter is pressed. The app records exactly one
// history row per event. The hook runs after the tap released its lock.
func WithCommandCompletionHook(fn func(CommandEvent)) Option {
	return func(s *Service) { s.onCommandCompletion = fn }
}

// WithBellHook receives a BEL from the transport. The app marks the Session's
// tab as having new activity, as the original did.
func WithBellHook(fn func(model.SessionID)) Option {
	return func(s *Service) { s.onBell = fn }
}

// WithCaptureHook installs a session-scoped shell hook. Plan runs before each
// transport opens and may rewrite the StartSpec; a nil hook leaves every
// Session unchanged. A plan failure never fails the Open.
func WithCaptureHook(hook CaptureHook) Option {
	return func(s *Service) { s.captureHook = hook }
}

// WithHostAtuin installs the Host-Atuin detection and read-only import path.
// Probe runs before the capture hook; a Host where it reports Present skips
// the hook for that Session. A nil policy keeps the normal hook behaviour.
func WithHostAtuin(policy HostAtuin) Option {
	return func(s *Service) { s.hostAtuin = policy }
}

// WithLocalShell resolves the configured custom shell argv for a local
// Session. It is read before the capture-hook planner so a planned hook wraps
// the user's shell rather than the OS login shell. The resolver returns nil
// when no custom shell is configured, or when the configured path is missing
// (the caller keeps the visible fallback warning). An explicit StartSpec
// command always wins; this only fills a local Session that named none, which
// is the workspace restore, split, and retry path.
func WithLocalShell(resolve func() []string) Option {
	return func(s *Service) { s.localShell = resolve }
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
	if spec.Kind == KindLocal && spec.Reconnect == nil {
		// A local shell restarts by hand as a remote Session reconnects, but
		// never on its own: nothing drops between here and the shell.
		spec.Reconnect = &ReconnectPolicy{ShouldRetry: func(End) bool { return false }}
	}
	if spec.Kind == KindVNC {
		// A VNC Session follows the SSH policy: retry a dropped stream every
		// five seconds, but never after an authentication failure.
		if spec.Reconnect == nil {
			spec.Reconnect = &ReconnectPolicy{}
		}
		if spec.VNC == nil {
			return nil, errors.New("session: VNC configuration is required")
		}
		normalized := spec.VNC.Normalized()
		if err := normalized.Validate(); err != nil {
			return nil, err
		}
		spec.VNC = &normalized
	}
	if spec.Cols <= 0 {
		spec.Cols = 80
	}
	if spec.Rows <= 0 {
		spec.Rows = 24
	}
	if spec.EncodingScope == "" {
		if (spec.Kind == KindSSH || spec.Kind == KindTelnet || spec.Kind == KindLocal) && spec.HostID != "" {
			spec.EncodingScope = domainterminal.EncodingScopeHost(string(spec.HostID))
		} else {
			spec.EncodingScope = domainterminal.EncodingScopeLocal
		}
	}
	if spec.Kind == KindVNC {
		// A pixel Session has no wire encoding: pixels cross the display port,
		// and clipboard text is Latin-1 at the RFB layer. Keep the Snapshot's
		// encoding field at its neutral value and never ask for a codec.
		spec.Encoding = domainterminal.EncodingUTF8
	} else {
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
	}
	codec := Codec(identityCodec{})
	if s.codecFactory != nil && spec.Kind != KindVNC {
		codec = s.codecFactory(spec.Encoding)
	}
	if codec == nil {
		return nil, fmt.Errorf("session: codec factory returned nil for %q", spec.Encoding)
	}
	var inputTranslator *inputTranslator
	if spec.Kind == KindSSH {
		inputTranslator = newInputTranslator(spec.InputCompatibility, codec.EncodedWidth)
	}

	// A Host that already runs Atuin is detected before the terminal opens,
	// over a separate read-only channel. Present keeps the Netcatty capture
	// hook out of the startup; MarkNoticed publishes the one-time Host-Atuin
	// notice once the Session is live. The probe never fails an Open.
	hostAtuinActive := false
	hostAtuinFailed := false
	if s.hostAtuin != nil && spec.Kind != KindVNC {
		probeCtx, cancel := context.WithTimeout(ctx, hostAtuinProbeTimeout)
		probe := s.hostAtuin.Probe(probeCtx, spec)
		cancel()
		hostAtuinActive = probe.Present
		hostAtuinFailed = probe.Failed
	}

	// Resolve the custom shell for a local Session before the capture hook
	// plans. The planner rewrites the interactive shell's startup into
	// spec.Command; resolving afterwards (the transport factory's fallback)
	// would see a non-empty command and keep the OS login shell. An explicit
	// StartSpec command, such as the Tab button's, still wins.
	if spec.Kind == KindLocal && len(spec.Command) == 0 && s.localShell != nil {
		if command := s.localShell(); len(command) > 0 {
			spec.Command = command
		}
	}

	// The capture hook rewrites the startup before the transport opens: local
	// temp startup files or a remote wrapper command. It never fails an Open;
	// a failed plan leaves the Session unmodified and reports a capability
	// notice once per Host. A Host-Atuin Session skips the hook entirely.
	var captureCleanup func()
	captureFailed := false
	hookExpected := false
	if s.captureHook != nil && !hostAtuinActive && spec.Kind != KindVNC {
		plan, failed := s.captureHook.Plan(spec)
		spec = plan.Spec
		captureCleanup = plan.Cleanup
		captureFailed = failed
		hookExpected = plan.HookExpected && !failed
	}

	transport, err := s.factory.Open(ctx, spec)
	if err != nil {
		runCaptureCleanup(captureCleanup)
		s.reportConnection(ConnectionEvent{
			HostID: spec.HostID, Label: specLabel(spec), Hostname: spec.Hostname,
			Username: spec.Username, Kind: spec.Kind, StartedAt: startedAt,
			EndedAt: time.Now(), Err: err,
		})
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		_ = transport.Close()
		runCaptureCleanup(captureCleanup)
		s.reportConnection(ConnectionEvent{
			HostID: spec.HostID, Label: specLabel(spec), Hostname: spec.Hostname,
			Username: spec.Username, Kind: spec.Kind, StartedAt: startedAt,
			EndedAt: time.Now(), Err: err,
		})
		return nil, err
	}
	var display *SerializedDisplay
	if spec.Kind == KindVNC {
		display = displayFromTransport(transport)
		if display == nil {
			err := errors.New("session: VNC transport did not provide a display session")
			_ = transport.Close()
			runCaptureCleanup(captureCleanup)
			s.reportConnection(ConnectionEvent{
				HostID: spec.HostID, Label: specLabel(spec), Hostname: spec.Hostname,
				Username: spec.Username, Kind: spec.Kind, StartedAt: startedAt,
				EndedAt: time.Now(), Err: err,
			})
			return nil, err
		}
	}
	if reporter, ok := transport.(CaptureHookExpectedReporter); ok && !reporter.CaptureHookExpected() {
		// The transport learned while connecting that the planned hook was not
		// installed. Do not reserve the first typed command for a handshake
		// that will never come; Tier 2/3 capture starts at the first Enter.
		hookExpected = false
	}
	if hookExpected {
		// The transport may have withdrawn the plan at open time. Do not
		// reserve the first typed command for a handshake that cannot arrive.
		if withdrawn, ok := transport.(CaptureHookWithdrawn); ok && withdrawn.CaptureHookWithdrawn() {
			hookExpected = false
		}
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
	tap.hookExpected = hookExpected
	highlightEnabled, highlightRules := false, []domainterminal.KeywordHighlightRule(nil)
	if s.highlightPolicy != nil {
		highlightEnabled, highlightRules = s.highlightPolicy()
	}
	session := &Session{
		id:                      id,
		ctx:                     sessionCtx,
		cancel:                  cancel,
		transport:               transport,
		display:                 display,
		displayEvents:           newDisplayEvents(spec.Kind),
		factory:                 s.factory,
		spec:                    spec,
		startupReadinessTimeout: s.startupReadinessTimeout,
		startupSchedule:         s.startupSchedule,
		tap:                     tap,
		clipboardAccess:         policy,
		captureCleanup:          captureCleanup,
		hostAtuin:               s.hostAtuin,
		hostAtuinActive:         hostAtuinActive,
		codec:                   codec,
		inputTranslator:         inputTranslator,
		highlighter:             domainterminal.NewHighlighter(highlightEnabled, highlightRules),
		genDone:                 make(chan struct{}),
		writeCh:                 make(chan []byte, s.writeDepth),
		commands:                make(chan command, 16),
		closeCh:                 make(chan struct{}, 1),
		done:                    make(chan struct{}),
		onNotification:          s.onNotification,
		onTransfer:              s.onTransfer,
		onInput:                 s.onInput,
		onCommandCompletion:     s.onCommandCompletion,
		onBell:                  s.onBell,
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
	session.bindScreenSizer(transport)
	session.bindCWDProbe(transport)
	// A local PTY is attached to a live shell at Open; a remote shell has to
	// finish its startup (a ForceCommand bash -i included) before it can read a
	// wrapper, so the first command probes readiness first.
	session.commandReady.Store(spec.Kind == KindLocal)
	if spec.Kind != KindVNC {
		// A Host startup command waits for this connection's shell prompt. A
		// pixel Session has no shell, so neither a startup command nor an
		// auto-login detector applies.
		session.armStartupCommand(session.generation, transport)
		session.armAutoLogin(session.generation, transport)
	}

	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		cancel()
		_ = transport.Close()
		runCaptureCleanup(captureCleanup)
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

	if captureFailed && s.announceCaptureFailure(spec) {
		// The hook could not be installed but the Session opened: capture is
		// simply less precise. The notice is per Host, never per connection.
		session.PublishNotice(Notice{Code: NoticeCaptureHookUnavailable})
	}
	if hostAtuinActive && s.hostAtuin != nil {
		if hostAtuinFailed {
			// The Host runs Atuin but this read failed. The hook stays skipped
			// so Netcatty never double-captures, and the warning says why the
			// Host's history is temporarily unavailable.
			session.PublishNotice(Notice{Code: NoticeHostAtuinImportFailed})
		} else {
			// The Host-Atuin service persists the one-time marker, so a later
			// Session does not repeat this notice. Marking it here, after a
			// successful Open, means a failed terminal start or a failed import
			// does not swallow it.
			noticeCtx, cancel := context.WithTimeout(context.Background(), hostAtuinProbeTimeout)
			first := s.hostAtuin.MarkNoticed(noticeCtx, spec)
			cancel()
			if first {
				session.PublishNotice(Notice{Code: NoticeHostAtuinDetected})
			}
		}
	}

	if s.autoLog != nil && spec.Kind != KindVNC {
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
	return s.OpenLocalCommand(ctx, nil)
}

// OpenLocalCommand opens a local Session with an explicit command line. An
// empty command asks the transport for the platform's login shell. The caller
// resolves a configured custom shell; this method only carries it through.
func (s *Service) OpenLocalCommand(ctx context.Context, command []string) (*Session, error) {
	// The original names local Session logs "Local Terminal".
	return s.Open(ctx, StartSpec{Kind: KindLocal, Label: "Local Terminal", Command: command})
}

// LocalShellExists reports whether path names a program the OS can start. It
// resolves PATH but never runs the program, so a custom-shell preference can
// be checked before a Session opens.
func LocalShellExists(path string) bool {
	_, err := exec.LookPath(path)
	return err == nil
}

// OpenLocalShell opens a named local shell in a new Session. shell is a
// program name resolved on PATH or an executable path; empty falls back to
// the platform default through OpenLocal.
func (s *Service) OpenLocalShell(ctx context.Context, shell string) (*Session, error) {
	if shell == "" {
		return s.OpenLocal(ctx)
	}
	return s.Open(ctx, StartSpec{
		Kind:    KindLocal,
		Label:   "Local Terminal",
		Command: []string{shell},
	})
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
	case KindVNC:
		if spec.Hostname != "" {
			return spec.Hostname
		}
		if spec.VNC != nil && spec.VNC.Hostname != "" {
			return spec.VNC.Hostname
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
// Session. The app uses it for Session-scoped actions that need the live row.
func (s *Service) SessionInfo(id model.SessionID) (model.HostID, string, Kind, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[id]
	if !ok {
		return "", "", "", false
	}
	return session.spec.HostID, session.Label(), session.spec.Kind, true
}

// commandScope returns the history scope a Session's commands are recorded
// under: the local shell, a saved Host, a serial port, or an unsaved Host whose
// scope carries the origin captured at execution. A serial port is not a host
// and is never local. The same value is used for autocomplete's read path, so
// a Session reads the rows it writes.
func commandScope(spec StartSpec) string {
	switch {
	case spec.Kind == KindSerial:
		if spec.Serial != nil {
			return "serial:" + serial.PortName(spec.Serial.Path)
		}
		return "serial"
	case spec.HostID != "":
		return "host:" + string(spec.HostID)
	case spec.Kind == KindLocal || spec.Kind == "":
		return "local"
	default:
		return "host:unsaved:" + domainhistory.CaptureOrigin(
			spec.Hostname, spec.Username, spec.Label)
	}
}

// HistoryScope returns the autocomplete scope this Session's captured commands
// use. It is separate from EncodingScope, which selects a persisted text
// encoding preference rather than a history partition.
func (s *Session) HistoryScope() string {
	if !s.ByteStream() {
		// A pixel Session has no shell and records no command history.
		return ""
	}
	return commandScope(s.spec)
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

// runCaptureCleanup releases a plan's resources exactly once. The plan runs
// before the Session exists, so an Open that fails or is refused cleans up
// here instead of through the command loop.
func runCaptureCleanup(cleanup func()) {
	if cleanup != nil {
		cleanup()
	}
}

// announceCaptureFailure reports whether this Host has not yet shown the
// capture-hook capability notice. A local Session and an unsaved Host have
// their own stable keys.
func (s *Service) announceCaptureFailure(spec StartSpec) bool {
	key := captureNoticeKey(spec)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.captureNotices == nil {
		s.captureNotices = make(map[string]bool)
	}
	if s.captureNotices[key] {
		return false
	}
	s.captureNotices[key] = true
	return true
}

func captureNoticeKey(spec StartSpec) string {
	if spec.HostID != "" {
		return "host:" + string(spec.HostID)
	}
	if spec.Hostname != "" {
		return "host:" + spec.Hostname
	}
	return "local"
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

// Close closes one live Session by ID. It reports whether the Session was
// registered; the close itself is idempotent and asynchronous, matching
// Session.Close.
func (s *Service) Close(id model.SessionID) (bool, error) {
	s.mu.Lock()
	session, ok := s.sessions[id]
	s.mu.Unlock()
	if !ok {
		return false, nil
	}
	return true, session.Close()
}

// CloseAll closes every live Session and stops accepting new ones. It is the
// app-shutdown path, so resumable transports keep their credentials for the
// next run; a user closing one Session clears them.
func (s *Service) CloseAll() {
	s.mu.Lock()
	s.closing = true
	sessions := make([]*Session, 0, len(s.sessions))
	for _, session := range s.sessions {
		sessions = append(sessions, session)
	}
	s.mu.Unlock()

	for _, session := range sessions {
		_ = session.close(true)
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
	session := s.sessions[snapshot.ID]
	s.mu.Unlock()
	if hook == nil {
		return
	}
	if session != nil {
		// Change consumers see the same cwd a Snapshot would, so follow-cwd
		// works for a local shell that never reports OSC 7.
		session.fillProbedCWD(&snapshot)
	}
	hook(snapshot)
}

// ID returns the Session's stable runtime identifier.
func (s *Session) ID() model.SessionID { return s.id }

// HostID returns the Vault Host the Session was opened for. A local Session
// has no Host and reports "".
func (s *Session) HostID() model.HostID { return s.spec.HostID }

// VNCConfiguration returns the normalized viewer configuration a VNC Session
// was opened with. A byte-stream Session has none and reports the zero value.
// The viewer reads it once to start in the Host's fit, cursor, and view-only
// modes.
func (s *Session) VNCConfiguration() modelvnc.Config {
	if s.spec.VNC == nil {
		return modelvnc.Config{}
	}
	return s.spec.VNC.Configuration
}

// BracketedPaste reports whether the peer last enabled DECSET 2004. The
// compose bar uses it to wrap multiline submissions in one paste.
func (s *Session) BracketedPaste() bool {
	if s == nil || s.tap == nil {
		return false
	}
	return s.tap.state(time.Now()).BracketedPaste
}

// Done is closed once the Session has ended, its final snapshot is published,
// and its Service has unregistered it. After Done fires, input addressed to the
// Session by ID reports DeliveryMissing rather than DeliveryFailed.
func (s *Session) Done() <-chan struct{} { return s.done }

// PublishNotice records a non-fatal notice for the Session surface. It is for
// application-level outcomes of opening a Session, such as a saved reattach
// that fell back to a fresh session; transport notices use Notifier instead.
func (s *Session) PublishNotice(notice Notice) {
	s.update(func(snapshot *Snapshot) { snapshot.Notice = &notice })
}

// Snapshot returns the Session's current state.
func (s *Session) Snapshot() Snapshot {
	now := time.Now()
	s.mu.Lock()
	snapshot := s.snapshot
	s.mu.Unlock()
	snapshot.PendingBytes, snapshot.Paused = s.conn.stats()
	state := s.tap.state(now)
	snapshot.CWD = state.CWD
	if snapshot.CWD == "" && snapshot.Kind == KindLocal {
		snapshot.CWD = s.probedCWD()
	}
	if s.ByteStream() {
		// A VNC title is the server's desktop name, stored by the display
		// event path; the terminal tap never sees that stream.
		snapshot.Title = state.Title
	}
	snapshot.Command = state.Command
	snapshot.Input = state.Input
	snapshot.InputCursor = state.InputCursor
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
	if !s.ByteStream() {
		// A pixel Session has no terminal to resize. The viewer scales its own
		// bitmap; a remote desktop resize is SetDesktopSize and is explicit.
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

// Close ends the Session. It is idempotent and never waits for the command
// loop to accept the request: the loop may be inside a blocking transport call,
// and the UI closes a Session synchronously. Done reports when teardown
// finished. An explicit close clears any credentials a resumable transport
// saved, including when the Session had already ended.
func (s *Session) Close() error { return s.close(false) }

// ClosePreservingResume ends the Session like Close, but asks a resumable
// transport to keep its saved credentials for the next app run. The app uses
// it for a graceful window close; a user closing one Session uses Close.
func (s *Session) ClosePreservingResume() error { return s.close(true) }

// close records the Session's one explicit close and wakes the command loop.
// The record is authoritative: the loop applies it when it handles the wake-up,
// and teardown applies it if the Session ends without accepting the wake-up.
// The record is taken under closeMu exactly once, so the preserve/discard call
// happens once even when Close races the end of the Session.
func (s *Session) close(preserve bool) error {
	s.closeOnce.Do(func() {
		request := &closeRequest{preserve: preserve}
		s.closeMu.Lock()
		ended := s.ended
		if !ended {
			s.pendingClose = request
		}
		s.closeMu.Unlock()
		if ended {
			// Teardown already published Done and will not take another
			// record. Apply this close before returning, as an ended Session's
			// explicit close must still clear preserved credentials.
			s.applyClose(request)
			return
		}
		select {
		case s.closeCh <- struct{}{}:
		default:
			// A wake-up is already queued; the record is the same one.
		}
	})
	return nil
}

// consumeCloseRequest applies the pending explicit close, if any. A later
// teardown pass applies a close that is recorded after this take.
func (s *Session) consumeCloseRequest() {
	s.closeMu.Lock()
	request := s.pendingClose
	s.pendingClose = nil
	s.closeMu.Unlock()
	if request != nil {
		s.applyClose(request)
	}
}

// applyClose applies one explicit close to the current transport. It runs
// outside closeMu because a resume call may block in the secret store.
func (s *Session) applyClose(request *closeRequest) {
	if s.transport == nil {
		return
	}
	if request.preserve {
		preserveTransportResume(s.transport)
		return
	}
	discardTransportResume(s.transport)
}

// publishEnded applies a close recorded while teardown was finishing, marks
// the Session ended, and publishes Done. The record is applied outside closeMu
// because a resume call may block in the secret store, so the take and the
// Done close cannot share one section. Instead the last empty take and closing
// Done share one: a Close that stores before it is applied first, and a Close
// that stores after it observes ended and applies its own request. A request is
// therefore never lost and is applied exactly once.
func (s *Session) publishEnded() {
	for {
		s.closeMu.Lock()
		request := s.pendingClose
		if request == nil {
			s.ended = true
			close(s.done)
			s.closeMu.Unlock()
			return
		}
		s.pendingClose = nil
		s.closeMu.Unlock()
		s.applyClose(request)
	}
}

// closeTransport closes one transport at most once for this Session. The end
// handler and final teardown can both reach the same generation, and a
// DisplayProvider's Close owns the display's event channel; closing it twice
// would panic on a non-idempotent adapter. A transport Open returned but never
// handed to the command loop uses the same path.
func (s *Session) closeTransport(transport Transport) {
	if transport == nil {
		return
	}
	if !s.recordTransportClosed(transport) {
		return
	}
	_ = transport.Close()
}

// recordTransportClosed reports whether transport has not been closed yet and
// records it in one step. Interfaces are compared only when their dynamic type
// is comparable, so an exotic non-pointer transport cannot panic the teardown.
func (s *Session) recordTransportClosed(transport Transport) bool {
	s.closedMu.Lock()
	defer s.closedMu.Unlock()
	for _, closed := range s.closedTransports {
		if sameTransport(closed, transport) {
			return false
		}
	}
	s.closedTransports = append(s.closedTransports, transport)
	return true
}

// sameTransport compares two transports by identity. Distinct non-comparable
// values are treated as different, which may close one twice but never panics.
func sameTransport(a, b Transport) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	kind := reflect.TypeOf(a)
	if kind != reflect.TypeOf(b) || !kind.Comparable() {
		return false
	}
	return a == b
}

// bindScreenSizer connects a transport that owns the remote screen size to
// the live view's inverse resize path. A reconnect binds the replacement
// transport the same way.
func (s *Session) bindScreenSizer(transport Transport) {
	sizer, ok := transport.(ScreenSizer)
	if !ok {
		return
	}
	s.remoteScreen.Store(true)
	// Output positions are relative to one transport generation. A reconnect
	// binds a replacement transport whose stream starts at zero again.
	s.conn.resetTransportPosition()
	sizer.SetScreenSizeHandler(s.conn.applyScreenSize)
}

// ScreenSizeFromRemote reports whether the peer's screen size is
// authoritative. The terminal surface then uses MyGo's ResizeByConn and
// applies the inverse resize signal before rendering following output.
func (s *Session) ScreenSizeFromRemote() bool { return s.remoteScreen.Load() }

func (s *Session) start() {
	// Register the display generation before the command loop can observe an
	// end and wait on displayLoops.
	if !s.ByteStream() {
		s.displayLoops.Add(1)
	}
	go s.loop()
	if !s.ByteStream() {
		go s.displayLoop(s.generation, s.transport, s.display, s.genDone)
		// A pixel transport can still report non-fatal notices, such as the
		// one-time unencrypted-VNC warning.
		go s.noticeLoop(s.transport, s.genDone)
		return
	}
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
	reason := EndReason("")
	for {
		select {
		case <-s.ctx.Done():
			goto finished
		case cmd := <-s.commands:
			switch cmd.kind {
			case commandResize:
				if s.reopening || !s.ByteStream() {
					// A pixel Session has no terminal size; the viewer scales
					// its own bitmap and a remote resize is a separate request.
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
					s.closeTransport(cmd.transport)
					continue
				}
				retry := s.spec.Reconnect != nil && s.spec.Reconnect.shouldRetry(cmd.end)
				if retry {
					// The next Open may reattach the same remote session, so keep
					// its credentials across this handover.
					preserveTransportResume(cmd.transport)
				}
				s.setDisplay(nil)
				s.closeTransport(cmd.transport)
				if !retry {
					status = StatusExited
					connection = connectionForEnd(cmd.end)
					code, hasCode = cmd.end.ExitCode, cmd.end.HasExitCode
					reason = cmd.end.Reason
					goto finished
				}
				// Advance the generation before starting the dial so a second
				// end event from the dead transport is ignored.
				s.generation++
				s.reopening = true
				s.commandReady.Store(false)
				s.stopStartupTimer()
				s.cancelAutoLogin()
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
				s.commandReady.Store(false)
				s.stopStartupTimer()
				s.cancelAutoLogin()
				close(s.genDone)
				s.genDone = make(chan struct{})
				if s.transport != nil {
					preserveTransportResume(s.transport)
					s.setDisplay(nil)
					s.closeTransport(s.transport)
				}
				s.update(func(snapshot *Snapshot) {
					snapshot.Connection = ConnectionStatusReconnecting
				})
				go s.reconnect(s.generation, true)
			case commandTransportOpen:
				if cmd.generation != s.generation || !s.reopening {
					s.closeTransport(cmd.transport)
					continue
				}
				s.reopening = false
				s.transport = cmd.transport
				if !s.ByteStream() {
					display := displayFromTransport(cmd.transport)
					if display == nil {
						s.closeTransport(cmd.transport)
						status = StatusExited
						connection = ConnectionStatusError
						reason = EndReasonError
						goto finished
					}
					s.setDisplay(display)
					s.update(func(snapshot *Snapshot) {
						snapshot.Connection = ConnectionStatusActive
						snapshot.Notice = nil
					})
					s.displayLoops.Add(1)
					go s.displayLoop(cmd.generation, cmd.transport, display, s.genDone)
					go s.noticeLoop(cmd.transport, s.genDone)
					continue
				}
				if withdrawn, ok := cmd.transport.(CaptureHookWithdrawn); ok && withdrawn.CaptureHookWithdrawn() {
					// The replacement transport kept the plain login shell. Release
					// any hook reservation left from the previous generation.
					s.tap.withdrawCaptureHook()
				}
				s.bindScreenSizer(cmd.transport)
				s.bindCWDProbe(cmd.transport)
				// Every reconnect gets its own one-shot startup command and a
				// fresh detector for the console's new boot.
				s.armStartupCommand(cmd.generation, cmd.transport)
				s.armAutoLogin(cmd.generation, cmd.transport)
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
				// Only a permanent failure posts this command, from a rejected
				// credential or host key. Naming it lets the pane explain why
				// the reconnect stopped instead of showing a generic error.
				reason = endReasonFor(cmd.err)
				goto finished
			}
		case <-s.closeCh:
			// Apply the explicit close before teardown closes the transport.
			// The current transport may already be closed: a reconnect handover
			// preserves its credentials before closing it, and an explicit close
			// still has to clear them.
			s.consumeCloseRequest()
			status = StatusClosed
			goto finished
		}
	}

finished:
	// Clear the public display handle on every teardown path, including an
	// explicit Close that never saw a transport-end command. Send methods then
	// report ErrNoDisplay instead of reaching an adapter the transport closed
	// below. The normal-end path already did this; this makes the two paths
	// identical.
	s.setDisplay(nil)
	s.cancel()
	close(s.genDone)
	s.stopActivityTimer()
	s.stopPromptTimer()
	s.stopStartupTimer()
	s.cancelAutoLogin()
	s.conn.finish()
	s.closeObservations()
	s.closeTransport(s.transport)
	// The owning transport closes its DisplaySession (see DisplayProvider).
	// Every generation's event loop must stop before the Session stream
	// closes; ctx is already cancelled, so a blocked surface send unwinds.
	s.displayLoops.Wait()
	s.closeDisplayEvents()
	// Temporary startup files are session-scoped. The command loop is the one
	// teardown path, so the cleanup runs exactly once.
	if s.captureCleanup != nil {
		cleanup := s.captureCleanup
		s.captureCleanup = nil
		cleanup()
	}
	// A Host-Atuin Session skipped the hook, so its Atuin history is imported
	// again now that the terminal has ended. The implementation must return
	// without waiting; a failure keeps the previous checkpoint for the next
	// Session to retry.
	if s.hostAtuinActive && s.hostAtuin != nil {
		s.hostAtuin.ImportAfterClose(s.spec)
	}
	// A command still pending when the Session ends is finalized with an
	// unknown exit and no finish time, so no duration is invented. The tap
	// returns it at most once even when output and close race.
	if completion := s.tap.finalizePending(); completion != nil {
		s.publishCommandCompletion(*completion)
	}
	logging := s.finishLogging()
	s.update(func(snapshot *Snapshot) {
		snapshot.Status = status
		snapshot.Connection = connection
		snapshot.ExitCode = code
		snapshot.HasExitCode = hasCode
		snapshot.EndReason = reason
		snapshot.Logging = logging
		// A takeover notice and the end of the displaced transport race:
		// the notice channel closes as the transport ends, and the notice
		// loop may observe the end first. The end reason is authoritative,
		// and a displaced Session never reopens to clear the notice.
		if reason == EndReasonDisplaced {
			snapshot.Notice = &Notice{Code: NoticeETDisplaced}
		}
		// A hibernation only names a live transport whose screen was
		// detached; the transport is gone now.
		snapshot.Hibernated = false
	})
	// A reconnect can finish its dial exactly as the command loop exits,
	// leaving a handoff the loop never consumed. Close it so the transport's
	// display emitter does not leak. closeTransport dedupes the common case
	// where the loop did consume and assign it.
drainHandoffs:
	for {
		select {
		case cmd := <-s.commands:
			if cmd.kind == commandTransportOpen {
				s.closeTransport(cmd.transport)
			}
		default:
			break drainHandoffs
		}
	}
	// Unregister before announcing the end; see Done.
	if s.onEnd != nil {
		s.onEnd(s.id)
	}
	s.publishEnded()
}

// connectionForEnd reports the indicator a final transport end leaves. A
// dropped transport is an error; a deliberate shell end is not.
func connectionForEnd(end End) ConnectionStatus {
	if end.Reason == EndReasonError || end.Reason == EndReasonAuthFailed {
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
			// The dial may outlive the Session: an explicit Close can cancel
			// the context while Open is blocked. The newly opened transport
			// owns a display and its DisplayEmitter goroutine, so close it here
			// rather than leaking it when the command loop has already stopped.
			if s.ctx.Err() != nil {
				s.setDisplay(nil)
				s.closeTransport(transport)
				return
			}
			// A new transport starts a new remote line, so the input
			// translator must not delete bytes from the previous one.
			s.inputTranslator.reset()
			s.post(command{kind: commandTransportOpen, generation: generation, transport: transport})
			// The handoff may still lose to a cancellation between the check
			// above and post. Close the transport unless the command loop
			// accepted it; closeTransport dedupes if both paths reach it.
			if s.ctx.Err() != nil {
				s.closeTransport(transport)
			}
			return
		}
		if s.ctx.Err() != nil {
			return
		}
		if permanentError(err) {
			s.post(command{kind: commandTransportFailed, generation: generation, err: err})
			return
		}
		timer.Reset(delay)
	}
}

// permanentError reports a typed rejection, including an authentication or
// host-key failure, that must not be retried after it is observed.
func permanentError(err error) bool {
	var failure permanentFailure
	return errors.As(err, &failure) && failure.Permanent()
}

// authFailure reports whether err is a rejected credential or host key. It is
// deliberately narrower than Permanent(): an X11 forwarding refusal is
// permanent for retry purposes but is not an authentication failure.
func authFailure(err error) bool {
	var connErr *appconnection.Error
	return errors.As(err, &connErr) &&
		(connErr.Kind == appconnection.KindAuth || connErr.Kind == appconnection.KindHostKey)
}

// endReasonFor classifies an error that stopped the transport. Only an
// authentication or host-key rejection is named as such; every other permanent
// failure, such as an X11 or proxy refusal, stays a generic error and keeps
// its generic user-visible message.
func endReasonFor(err error) EndReason {
	if authFailure(err) {
		return EndReasonAuthFailed
	}
	return EndReasonError
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
			s.feedAutoLogin(generation, string(decoded))
			s.observeStartupOutput(generation, decoded)
			output, effects := s.tap.observe(decoded, now)
			if len(output) > 0 {
				s.writeLog(logOutput(output))
				s.highlightChunk(output)
			}
			if effects.Changed {
				state := s.publishTap(now)
				if len(output) > 0 && !state.Command.Running {
					// A prompt or shell-initiated output can follow a cd.
					// Cache until the next such burst; a running command's
					// output rarely moves the shell's own directory.
					s.invalidateCWDProbe()
				}
				s.scheduleActivityClear()
			}
			// observe returned after releasing the tap lock, so completions are
			// published outside it. Each command is finalized once.
			for _, completion := range effects.Completions {
				s.publishCommandCompletion(completion)
				// Prompt return is the canonical cwd change point.
				s.invalidateCWDProbe()
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
			// The bytes are now in the backlog (or filtered out); a pending
			// remote resize may proceed without overtaking them.
			s.conn.noteTransportRead(n)
			// The shell has produced output for this connection. The startup
			// command is typed once that output ends on a prompt; while
			// auto-login is still answering prompts it would land on a login
			// prompt, so it waits for the detector to finish.
			if s.autoLoginAllowsStartup(generation) {
				s.maybeRunStartupCommand(generation, transport)
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
			s.writeMu.Lock()
			_, err := transport.Write(input)
			s.writeMu.Unlock()
			if err != nil {
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
