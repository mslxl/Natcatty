// Package mosh implements the Mosh Session transport: an SSH bootstrap that
// runs the remote mosh-server, the MOSH CONNECT handshake, and a local
// mosh-client launched from the verified bundle.
//
// The adapter never resolves `mosh-client` or `mosh-server` through PATH. The
// client path comes only from adapters/bundle, which verifies the SHA-256 of
// the bundled binary before every launch; the remote server runs on the Host,
// optionally at a user-supplied path. The SSH bootstrap itself is supplied by
// the application through the Bootstrap port, so saved credentials, MFA,
// known-host trust, and jump chains use the same Connection Manager as an SSH
// Session.
package mosh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mslxl/natcatty/adapters/bundle"
	appsession "github.com/mslxl/natcatty/application/session"
	moshmodel "github.com/mslxl/natcatty/domain/model/mosh"
	"github.com/mslxl/natcatty/platform/pty"
)

// DefaultHandshakeTimeout bounds the wait for mosh-server's announcement. The
// SSH login itself may legitimately take longer with MFA, so the application
// may raise it.
const DefaultHandshakeTimeout = 60 * time.Second

// maxBufferedOutput bounds bytes waiting for the terminal. The reader applies
// backpressure once full, so the client is not read faster than the terminal
// drains it.
const maxBufferedOutput = 4 << 20

// maxPendingInput bounds keystrokes typed before the client is ready.
const maxPendingInput = 64 << 10

// viewportReset clears the handshake output before mosh-client's first frame,
// keeping the terminal's primary screen and scrollback.
const viewportReset = "\x1b[2J\x1b[H"

// Bootstrap opens the SSH channel that runs mosh-server. The application backs
// it with the Connection Manager; tests use an in-process fixture.
type Bootstrap interface {
	OpenBootstrap(ctx context.Context, spec appsession.StartSpec, cfg moshmodel.Config) (io.ReadWriteCloser, error)
}

// Process is one local mosh-client pseudo-terminal.
type Process interface {
	io.ReadWriteCloser
	Resize(cols, rows int) error
	Wait() (int, error)
	Exited() <-chan struct{}
	Kill() error
}

// Factory opens Mosh Sessions.
type Factory struct {
	// Spec resolves and verifies the bundled mosh-client.
	Spec bundle.Spec
	// Bootstrap opens the SSH handshake channel. Nil fails every open with a
	// clear error instead of falling back to a system ssh.
	Bootstrap Bootstrap
	// Spawn starts the local mosh-client. Nil uses platform/pty.
	Spawn func(pty.Options) (Process, error)
	// BaseEnv supplies the process environment. Nil uses os.Environ.
	BaseEnv func() []string
	// HomeDir supplies the client's working directory. Nil uses the user's
	// home directory.
	HomeDir func() string
	// HandshakeTimeout overrides DefaultHandshakeTimeout.
	HandshakeTimeout time.Duration
}

// Open performs the handshake and returns a transport already switched to the
// bundled mosh-client. The bootstrap's visible output is buffered for the
// terminal, with the protocol markers removed.
func (f Factory) Open(ctx context.Context, spec appsession.StartSpec) (appsession.Transport, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if spec.Mosh == nil {
		return nil, fmt.Errorf("%w: no Mosh configuration", moshmodel.ErrInvalidConfig)
	}
	cfg := f.config(spec)
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	resolved, err := f.Spec.Resolve()
	if err != nil {
		return nil, translateBundleError(err)
	}
	if f.Bootstrap == nil {
		return nil, errors.New("mosh: no SSH bootstrap is available")
	}

	queue := newOutputQueue(maxBufferedOutput)
	tr := &transport{
		queue:  queue,
		done:   make(chan struct{}),
		reaped: make(chan struct{}),
		cols:   spec.Cols,
		rows:   spec.Rows,
	}

	timeout := f.HandshakeTimeout
	if timeout <= 0 {
		timeout = DefaultHandshakeTimeout
	}
	handshakeCtx, cancel := context.WithTimeout(ctx, timeout)
	stop := context.AfterFunc(handshakeCtx, func() {
		tr.closeBootstrap()
		queue.close(io.ErrClosedPipe)
	})
	defer func() {
		stop()
		cancel()
	}()

	stream, err := f.Bootstrap.OpenBootstrap(handshakeCtx, spec, cfg)
	if err != nil {
		return nil, err
	}
	tr.setBootstrap(stream)
	connect, err := tr.handshake(handshakeCtx, stream)
	if err != nil {
		if ctx.Err() != nil {
			tr.closeBootstrap()
			tr.finish(appsession.End{Reason: appsession.EndReasonClosed}, 0)
			return nil, ctx.Err()
		}
		tr.closeBootstrap()
		if errors.Is(err, context.DeadlineExceeded) {
			tr.finish(appsession.End{Reason: appsession.EndReasonError}, 0)
			return nil, fmt.Errorf("%w: no announcement within %s", moshmodel.ErrHandshake, timeout)
		}
		tr.finish(appsession.End{Reason: appsession.EndReasonError}, 0)
		return nil, err
	}
	tr.closeBootstrap()

	client, err := f.launch(resolved, cfg, spec, connect, tr)
	if err != nil {
		tr.finish(appsession.End{Reason: appsession.EndReasonError}, 0)
		return nil, err
	}
	tr.attach(client)
	return tr, nil
}

// config merges the Session's locale environment into the Mosh configuration.
func (f Factory) config(spec appsession.StartSpec) moshmodel.Config {
	cfg := spec.Mosh.Normalized()
	environment := f.baseEnv(spec.Env)
	locales := make(map[string]string)
	for _, name := range []string{
		"LANG", "LANGUAGE", "LC_CTYPE", "LC_NUMERIC", "LC_TIME", "LC_COLLATE",
		"LC_MONETARY", "LC_MESSAGES", "LC_PAPER", "LC_NAME", "LC_ADDRESS",
		"LC_TELEPHONE", "LC_MEASUREMENT", "LC_IDENTIFICATION", "LC_ALL",
	} {
		if value := envValue(environment, name); value != "" {
			locales[name] = value
		}
	}
	if cfg.Lang == "" || cfg.Lang == moshmodel.DefaultLang {
		if lang := envValue(environment, "LC_ALL"); lang != "" {
			cfg.Lang = lang
		} else if lang := envValue(environment, "LANG"); lang != "" {
			cfg.Lang = lang
		}
	}
	cfg.Locales = locales
	if cfg.Cols == 0 {
		cfg.Cols = spec.Cols
	}
	if cfg.Rows == 0 {
		cfg.Rows = spec.Rows
	}
	return cfg
}

func (f Factory) baseEnv(extra []string) []string {
	base := os.Environ()
	if f.BaseEnv != nil {
		base = f.BaseEnv()
	}
	return mergeEnv(base, extra)
}

func (f Factory) homeDir() string {
	if f.HomeDir != nil {
		return f.HomeDir()
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

// transport is one Mosh Session after the handshake. Read is terminal output,
// Write is terminal input, and Resize reaches mosh-client.
type transport struct {
	queue *outputQueue

	mu        sync.Mutex
	client    Process
	bootstrap io.ReadWriteCloser
	closed    bool
	pending   [][]byte
	// pendingBytes counts buffered input, not output.
	pendingBytes int
	cols, rows   int
	hasEnd       bool
	end          appsession.End
	exitCode     int

	done chan struct{}
	// reaped closes after mosh-client's Wait returned, so Close can wait for
	// the process itself instead of for the Session-facing done channel that
	// Close closes early.
	reaped     chan struct{}
	reapOnce   sync.Once
	closeOnce  sync.Once
	finishOnce sync.Once
}

// handshake reads the bootstrap until the announcement arrives. It returns the
// parsed announcement or a typed error.
func (t *transport) handshake(ctx context.Context, stream io.ReadWriteCloser) (moshmodel.Connect, error) {
	sniffer := moshmodel.NewSniffer()
	buffer := make([]byte, 32<<10)
	for {
		n, readErr := stream.Read(buffer)
		if n > 0 {
			visible, connect := sniffer.Feed(buffer[:n])
			if len(visible) > 0 {
				if err := t.queue.push(visible); err != nil {
					return moshmodel.Connect{}, err
				}
			}
			if connect != nil {
				return *connect, nil
			}
		}
		if readErr != nil {
			visible, connect := sniffer.Flush()
			if len(visible) > 0 {
				if err := t.queue.push(visible); err != nil {
					return moshmodel.Connect{}, err
				}
			}
			if connect != nil {
				return *connect, nil
			}
			if ctx.Err() != nil {
				return moshmodel.Connect{}, ctx.Err()
			}
			if errors.Is(readErr, io.EOF) {
				return moshmodel.Connect{}, mochHandshakeError(nil)
			}
			return moshmodel.Connect{}, mochHandshakeError(readErr)
		}
	}
}

func mochHandshakeError(cause error) error {
	if cause == nil {
		return fmt.Errorf("%w: the SSH channel closed before mosh-server announced itself", moshmodel.ErrHandshake)
	}
	return fmt.Errorf("%w: %v", moshmodel.ErrHandshake, cause)
}

// launch starts the verified client with the server's key.
func (f Factory) launch(resolved bundle.Resolved, cfg moshmodel.Config, spec appsession.StartSpec,
	connect moshmodel.Connect, tr *transport) (Process, error) {

	host := connect.Host
	if host == "" {
		host = cfg.Hostname
	}
	args, err := moshmodel.ClientArgs(host, connect.Port)
	if err != nil {
		return nil, err
	}
	fallback := ""
	if connect.Host != "" && !strings.EqualFold(connect.Host, cfg.Hostname) {
		fallback = cfg.Hostname
	}
	environment := f.baseEnv(spec.Env)
	environment = setEnv(environment, "TERM", moshmodel.DefaultTerm)
	environment = moshmodel.ClientEnv(environment, connect.Key, cfg.Lang, fallback)
	environment = setEnv(environment, "MOSH_NO_TERM_INIT", "1")

	spawn := f.Spawn
	if spawn == nil {
		spawn = func(options pty.Options) (Process, error) {
			terminal, err := pty.Start(options)
			if err != nil {
				return nil, err
			}
			return terminal, nil
		}
	}
	client, err := spawn(pty.Options{
		Path: resolved.Path,
		Args: args,
		Dir:  f.homeDir(),
		Env:  environment,
		Cols: spec.Cols,
		Rows: spec.Rows,
	})
	if err != nil {
		return nil, fmt.Errorf("mosh: launch the bundled mosh-client: %w", err)
	}
	if spec.Cols > 0 && spec.Rows > 0 {
		if resizeErr := client.Resize(spec.Cols, spec.Rows); resizeErr != nil {
			_ = client.Close()
			return nil, fmt.Errorf("mosh: size mosh-client: %w", resizeErr)
		}
	}
	return client, nil
}

// attach switches the transport to the client, preserving buffered output.
func (t *transport) attach(client Process) {
	t.mu.Lock()
	t.client = client
	ready := !t.closed
	pending := t.pending
	t.pending = nil
	t.pendingBytes = 0
	cols, rows := t.cols, t.rows
	t.mu.Unlock()

	if err := t.queue.push([]byte(viewportReset)); err != nil {
		_ = client.Close()
		t.markReaped()
		t.finish(appsession.End{Reason: appsession.EndReasonError}, 0)
		return
	}
	if cols > 0 && rows > 0 {
		_ = client.Resize(cols, rows)
	}
	if ready {
		for _, chunk := range pending {
			if _, err := client.Write(chunk); err != nil {
				break
			}
		}
	}
	go t.supervise(client)
}

// supervise pumps client output and records the exit.
func (t *transport) supervise(client Process) {
	buffer := make([]byte, 32<<10)
	for {
		n, err := client.Read(buffer)
		if n > 0 {
			if pushErr := t.queue.push(buffer[:n]); pushErr != nil {
				break
			}
		}
		if err != nil {
			break
		}
	}
	code, waitErr := client.Wait()
	t.markReaped()
	end := appsession.End{Reason: appsession.EndReasonExited, ExitCode: code, HasExitCode: true}
	switch {
	case t.isClosed():
		end = appsession.End{Reason: appsession.EndReasonClosed}
	case waitErr != nil || code != 0:
		end = appsession.End{Reason: appsession.EndReasonError, ExitCode: code, HasExitCode: true}
	}
	t.finish(end, code)
}

func (t *transport) isClosed() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.closed
}

// finish records the first end and releases readers.
func (t *transport) finish(end appsession.End, exitCode int) {
	t.finishOnce.Do(func() {
		t.mu.Lock()
		if !t.hasEnd {
			t.hasEnd = true
			t.end = end
			t.exitCode = exitCode
		}
		t.mu.Unlock()
		t.queue.close(io.EOF)
		close(t.done)
	})
}

// Read returns terminal output, including handshake output buffered before the
// swap.
func (t *transport) Read(p []byte) (int, error) { return t.queue.Read(p) }

// Write sends input to mosh-client. Input typed before the swap is buffered
// and flushed in order.
func (t *transport) Write(p []byte) (int, error) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return 0, io.ErrClosedPipe
	}
	client := t.client
	ready := client != nil
	if !ready && t.pendingBytes+len(p) > maxPendingInput {
		t.mu.Unlock()
		return 0, errors.New("mosh: too much input before the client is ready")
	}
	if !ready {
		chunk := append([]byte(nil), p...)
		t.pending = append(t.pending, chunk)
		t.pendingBytes += len(chunk)
		t.mu.Unlock()
		return len(p), nil
	}
	t.mu.Unlock()
	return client.Write(p)
}

// Resize forwards the terminal size to mosh-client.
func (t *transport) Resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return nil
	}
	t.mu.Lock()
	t.cols, t.rows = cols, rows
	client := t.client
	t.mu.Unlock()
	if client == nil {
		return nil
	}
	return client.Resize(cols, rows)
}

// Wait reports the mosh-client exit code.
func (t *transport) Wait() (int, error) {
	<-t.done
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.exitCode, nil
}

// End reports the transport's final classification.
func (t *transport) End() appsession.End {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.hasEnd {
		return t.end
	}
	return appsession.End{Reason: appsession.EndReasonError}
}

// Close hangs up the client and reaps it. The bootstrap channel, when still
// open, is closed too.
func (t *transport) Close() error {
	t.closeOnce.Do(func() {
		t.mu.Lock()
		t.closed = true
		client := t.client
		bootstrap := t.bootstrap
		t.mu.Unlock()
		if bootstrap != nil {
			_ = bootstrap.Close()
		}
		if client != nil {
			_ = client.Close()
		}
		t.finish(appsession.End{Reason: appsession.EndReasonClosed}, 0)
	})
	if t.client != nil {
		select {
		case <-t.reaped:
		case <-time.After(2 * time.Second):
			_ = t.client.Kill()
			select {
			case <-t.reaped:
			case <-time.After(2 * time.Second):
			}
		}
	}
	return nil
}

// markReaped records that the client's Wait returned (or that the client was
// closed before it could be supervised).
func (t *transport) markReaped() {
	t.reapOnce.Do(func() { close(t.reaped) })
}

func (t *transport) setBootstrap(stream io.ReadWriteCloser) {
	t.mu.Lock()
	t.bootstrap = stream
	t.mu.Unlock()
}

func (t *transport) closeBootstrap() {
	t.mu.Lock()
	stream := t.bootstrap
	t.bootstrap = nil
	t.mu.Unlock()
	if stream != nil {
		_ = stream.Close()
	}
}

// translateBundleError maps bundle failures onto the Mosh vocabulary. A
// missing or invalid bundle is never replaced by another executable.
func translateBundleError(err error) error {
	var typed *bundle.Error
	if errors.As(err, &typed) {
		switch typed.Kind {
		case bundle.ErrorChecksumMismatch:
			return fmt.Errorf("%w: %v", moshmodel.ErrChecksumMismatch, err)
		case bundle.ErrorUnsupportedPlatform:
			return fmt.Errorf("%w: %v", moshmodel.ErrPlatformUnsupported, err)
		default:
			return fmt.Errorf("%w: %v", moshmodel.ErrBundleUnavailable, err)
		}
	}
	return err
}

// CheckRoute enforces the original restrictions: Mosh bootstraps its own SSH
// session, so a jump chain or proxy cannot be carried into the UDP channel.
func CheckRoute(jumpCount int, proxied bool) error {
	if proxied {
		return moshmodel.ErrProxyUnsupported
	}
	if jumpCount > 0 {
		return moshmodel.ErrJumpUnsupported
	}
	return nil
}

// envValue returns a variable from a KEY=value list.
func envValue(environment []string, name string) string {
	for _, entry := range environment {
		if value, ok := strings.CutPrefix(entry, name+"="); ok {
			return value
		}
	}
	return ""
}

// setEnv replaces a variable, preserving the order and leaving other entries
// untouched. An empty value from the caller is ignored.
func setEnv(environment []string, name, value string) []string {
	if value == "" {
		return environment
	}
	prefix := name + "="
	for index, entry := range environment {
		if strings.HasPrefix(entry, prefix) {
			environment[index] = prefix + value
			return environment
		}
	}
	return append(environment, prefix+value)
}

// mergeEnv overlays extra KEY=value entries on a base environment.
func mergeEnv(base, extra []string) []string {
	result := append([]string(nil), base...)
	for _, entry := range extra {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || name == "" {
			continue
		}
		result = setEnv(result, name, value)
	}
	return result
}

// outputQueue is the bounded handoff between the bootstrap/client readers and
// the terminal. Push blocks when full, which backpressures the process; Read
// blocks when empty.
type outputQueue struct {
	mu     sync.Mutex
	cond   *sync.Cond
	chunks [][]byte
	bytes  int
	max    int
	closed bool
	err    error
}

func newOutputQueue(max int) *outputQueue {
	queue := &outputQueue{max: max}
	queue.cond = sync.NewCond(&queue.mu)
	return queue
}

func (q *outputQueue) push(chunk []byte) error {
	if len(chunk) == 0 {
		return nil
	}
	copied := bytes.Clone(chunk)
	q.mu.Lock()
	defer q.mu.Unlock()
	for q.bytes >= q.max && !q.closed {
		q.cond.Wait()
	}
	if q.closed {
		return io.ErrClosedPipe
	}
	q.chunks = append(q.chunks, copied)
	q.bytes += len(copied)
	q.cond.Broadcast()
	return nil
}

func (q *outputQueue) Read(p []byte) (int, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.chunks) == 0 && !q.closed {
		q.cond.Wait()
	}
	if len(q.chunks) == 0 {
		if q.err != nil {
			return 0, q.err
		}
		return 0, io.EOF
	}
	n := copy(p, q.chunks[0])
	if n == len(q.chunks[0]) {
		q.chunks[0] = nil
		q.chunks = q.chunks[1:]
	} else {
		q.chunks[0] = q.chunks[0][n:]
	}
	q.bytes -= n
	q.cond.Broadcast()
	return n, nil
}

func (q *outputQueue) close(err error) {
	q.mu.Lock()
	if !q.closed {
		q.closed = true
		q.err = err
	}
	q.mu.Unlock()
	q.cond.Broadcast()
}
