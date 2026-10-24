package ssh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"

	xssh "golang.org/x/crypto/ssh"

	appconnection "github.com/mslxl/natcatty/application/connection"
)

// sshClient is the adapter's authenticated transport. It opens interactive
// shell channels on the final endpoint and keeps the earlier hops of a jump
// chain alive until the transport closes: each hop is the carrier for the next
// hop's direct-tcpip channel.
type sshClient struct {
	client        *xssh.Client
	ancestors     []*xssh.Client
	remoteVersion string
	x11           *x11Bridge
	mu            sync.Mutex
	closed        bool
}

var _ appconnection.Client = (*sshClient)(nil)
var _ appconnection.KeepaliveClient = (*sshClient)(nil)
var _ appconnection.VersionedClient = (*sshClient)(nil)
var _ appconnection.ForwardingClient = (*sshClient)(nil)
var _ appconnection.SubsystemClient = (*sshClient)(nil)
var _ appconnection.Executor = (*sshClient)(nil)

// DialContext opens a direct-tcpip channel through the SSH Host. It is the
// transport behind a local or dynamic Port Forwarding Rule.
func (c *sshClient) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return nil, &appconnection.Error{Kind: appconnection.KindNetwork, Cause: appconnection.ErrClosed}
	}
	conn, err := c.client.DialContext(ctx, network, address)
	if err != nil {
		return nil, classifyDialFailure(ctx, ctx, err)
	}
	return conn, nil
}

// ListenTCP asks the SSH Host to listen for remote forwarding. Closing the
// returned listener sends cancel-tcpip-forward; the caller bounds that wait.
func (c *sshClient) ListenTCP(address string) (net.Listener, error) {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return nil, &appconnection.Error{Kind: appconnection.KindNetwork, Cause: appconnection.ErrClosed}
	}
	listener, err := c.client.Listen("tcp", address)
	if err != nil {
		// A denied tcpip-forward request is how a remote bind conflict reaches
		// the client; there is no local errno to inspect.
		return nil, bindForwardFailure(err)
	}
	return listener, nil
}

// bindForwardFailure classifies a server-side forward denial. The SSH protocol
// does not carry a structured errno, so the server's reason text is the only
// signal available.
func bindForwardFailure(err error) error {
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "address already in use") ||
		strings.Contains(message, "permission denied") ||
		strings.Contains(message, "cannot assign requested address") {
		return &appconnection.Error{
			Kind:  appconnection.KindNetwork,
			Cause: fmt.Errorf("%w: %v", appconnection.ErrBindConflict, err),
		}
	}
	return &appconnection.Error{Kind: appconnection.KindNetwork, Cause: err}
}

// OpenSubsystem requests a named SSH subsystem on a new session channel.
// The SFTP adapter owns the stream's protocol; this method only opens and
// closes it.
func (c *sshClient) OpenSubsystem(ctx context.Context, name string) (appconnection.Subsystem, error) {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return nil, &appconnection.Error{Kind: appconnection.KindNetwork, Cause: appconnection.ErrClosed}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	session, err := c.client.NewSession()
	if err != nil {
		return nil, err
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		_ = session.Close()
		return nil, err
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		_ = session.Close()
		return nil, err
	}
	if err := session.RequestSubsystem(name); err != nil {
		_ = session.Close()
		// The server refused this subsystem, not the transport: a Host may
		// serve shell sessions without SFTP. Reporting a typed unsupported
		// failure keeps the healthy Connection parked for other leases instead
		// of discarding it as a network loss.
		return nil, &appconnection.Error{
			Kind:  appconnection.KindUnsupported,
			Cause: fmt.Errorf("%w: %v", appconnection.ErrSubsystemUnavailable, err),
		}
	}
	if err := ctx.Err(); err != nil {
		_ = session.Close()
		return nil, err
	}
	return &subsystemChannel{session: session, stdin: stdin, stdout: stdout}, nil
}

// Exec runs one non-interactive command to completion. The context bounds the
// command: cancellation closes the channel, and the caller sees the context
// error rather than a remote exit status.
func (c *sshClient) Exec(ctx context.Context, command string) (appconnection.ExecResult, error) {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return appconnection.ExecResult{}, &appconnection.Error{Kind: appconnection.KindNetwork, Cause: appconnection.ErrClosed}
	}
	if err := ctx.Err(); err != nil {
		return appconnection.ExecResult{}, err
	}
	session, err := c.client.NewSession()
	if err != nil {
		return appconnection.ExecResult{}, err
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr
	if err := session.Start(command); err != nil {
		return appconnection.ExecResult{}, err
	}
	done := make(chan error, 1)
	go func() { done <- session.Wait() }()

	select {
	case <-ctx.Done():
		_ = session.Close()
		<-done
		return appconnection.ExecResult{}, ctx.Err()
	case err := <-done:
		result := appconnection.ExecResult{Stdout: stdout.String(), Stderr: stderr.String()}
		var exit *xssh.ExitError
		switch {
		case err == nil:
			result.ExitCode, result.HasExitCode = 0, true
		case errors.As(err, &exit):
			result.ExitCode, result.HasExitCode = exit.ExitStatus(), true
		default:
			// The command may have flushed output before the transport
			// dropped. Return what arrived together with the failure so the
			// caller can tell an empty channel from a real answer.
			return result, err
		}
		return result, nil
	}
}

// OpenExec starts one non-interactive command and returns its raw stream. The
// context bounds opening only; the caller owns the stream and closes it when
// its protocol ends.
func (c *sshClient) OpenExec(ctx context.Context, command string) (appconnection.ExecStream, error) {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return nil, &appconnection.Error{Kind: appconnection.KindNetwork, Cause: appconnection.ErrClosed}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	session, err := c.client.NewSession()
	if err != nil {
		return nil, err
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		_ = session.Close()
		return nil, err
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		_ = session.Close()
		return nil, err
	}
	stderr, err := session.StderrPipe()
	if err != nil {
		_ = session.Close()
		return nil, err
	}
	if err := session.Start(command); err != nil {
		_ = session.Close()
		return nil, err
	}
	return &execChannel{session: session, stdin: stdin, stdout: stdout, stderr: stderr}, nil
}

// Keepalive sends an OpenSSH keepalive global request. A failed request means
// the transport is no longer answering.
func (c *sshClient) Keepalive() error {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return appconnection.ErrClosed
	}
	_, _, err := c.client.SendRequest("keepalive@openssh.com", true, nil)
	return err
}

// RemoteVersion returns the server's full SSH identification line (for
// example "SSH-2.0-OpenSSH_9.6") when the handshake captured it.
func (c *sshClient) RemoteVersion() string { return c.remoteVersion }

// OpenTerminal requests a PTY and an interactive shell.
func (c *sshClient) OpenTerminal(ctx context.Context, spec appconnection.TerminalSpec) (appconnection.Terminal, error) {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return nil, &appconnection.Error{Kind: appconnection.KindNetwork, Cause: appconnection.ErrClosed}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	session, err := c.client.NewSession()
	if err != nil {
		return nil, err
	}
	if err := c.x11.RequestX11(session); err != nil {
		_ = session.Close()
		return nil, err
	}

	cols, rows := spec.Cols, spec.Rows
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	term := spec.Term
	if term == "" {
		term = "xterm-256color"
	}

	modes := xssh.TerminalModes{
		xssh.ECHO:          1,
		xssh.TTY_OP_ISPEED: 14400,
		xssh.TTY_OP_OSPEED: 14400,
	}
	if err := session.RequestPty(term, rows, cols, modes); err != nil {
		_ = session.Close()
		return nil, err
	}

	stdin, err := session.StdinPipe()
	if err != nil {
		_ = session.Close()
		return nil, err
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		_ = session.Close()
		return nil, err
	}
	if err := session.Shell(); err != nil {
		_ = session.Close()
		return nil, err
	}

	return &channelTerminal{session: session, stdin: stdin, stdout: stdout}, nil
}

// Close closes the final transport and then the hops that carried it. Open
// channels are closed with the final transport.
func (c *sshClient) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()

	c.x11.Close()
	err := c.client.Close()
	for i := len(c.ancestors) - 1; i >= 0; i-- {
		if closeErr := c.ancestors[i].Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}
	return err
}

// channelTerminal is one interactive shell channel. Read and Write are the
// remote stdout and stdin; Resize sends a window-change request.
type channelTerminal struct {
	session *xssh.Session
	stdin   io.WriteCloser
	stdout  io.Reader

	mu       sync.Mutex
	closed   bool
	waitOnce sync.Once
	end      appconnection.TerminalEnd
}

var _ appconnection.Terminal = (*channelTerminal)(nil)
var _ appconnection.EndingTerminal = (*channelTerminal)(nil)

// End waits for the remote exit status and classifies the end. A normal exit
// (including a non-zero status) is "exited"; a signal is "signaled"; a
// dropped transport reports an error because no exit status arrived.
func (t *channelTerminal) End() appconnection.TerminalEnd {
	t.waitOnce.Do(func() {
		err := t.session.Wait()
		switch {
		case err == nil:
			t.end = appconnection.TerminalEnd{Reason: appconnection.TerminalEndExited, ExitCode: 0, HasExitCode: true}
		default:
			var exit *xssh.ExitError
			if errors.As(err, &exit) {
				if signal := exit.Signal(); signal != "" {
					t.end = appconnection.TerminalEnd{Reason: appconnection.TerminalEndSignaled}
					return
				}
				t.end = appconnection.TerminalEnd{
					Reason:   appconnection.TerminalEndExited,
					ExitCode: exit.ExitStatus(), HasExitCode: true,
				}
				return
			}
			var missing *xssh.ExitMissingError
			if errors.As(err, &missing) {
				t.end = appconnection.TerminalEnd{Reason: appconnection.TerminalEndError}
				return
			}
			t.end = appconnection.TerminalEnd{Reason: appconnection.TerminalEndError}
		}
	})
	return t.end
}

// Read returns remote output.
func (t *channelTerminal) Read(p []byte) (int, error) { return t.stdout.Read(p) }

// Write sends terminal input.
func (t *channelTerminal) Write(p []byte) (int, error) { return t.stdin.Write(p) }

// Resize changes the remote PTY size. SSH exchanges window-change as
// (columns, rows), so the argument order turns around here.
func (t *channelTerminal) Resize(cols, rows int) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return appconnection.ErrClosed
	}
	return t.session.WindowChange(rows, cols)
}

// Close hangs up the shell channel. It is safe to call more than once.
func (t *channelTerminal) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	t.mu.Unlock()
	_ = t.stdin.Close()
	return t.session.Close()
}

// execChannel is one open exec channel. Read and Write are the remote stdout
// and stdin; Stderr exposes diagnostics for a password handshake.
type execChannel struct {
	session *xssh.Session
	stdin   io.WriteCloser
	stdout  io.Reader
	stderr  io.Reader
	once    sync.Once
}

var _ appconnection.ExecStream = (*execChannel)(nil)

// Read returns the command's stdout.
func (e *execChannel) Read(p []byte) (int, error) { return e.stdout.Read(p) }

// Write sends the command's stdin.
func (e *execChannel) Write(p []byte) (int, error) { return e.stdin.Write(p) }

// Stderr returns the command's stderr stream.
func (e *execChannel) Stderr() io.Reader { return e.stderr }

// ExitCode waits for the remote command and reports its status. A command
// terminated by a signal, or one whose status never arrived, reports an
// error.
func (e *execChannel) ExitCode() (int, error) {
	err := e.session.Wait()
	if err == nil {
		return 0, nil
	}
	var exit *xssh.ExitError
	if errors.As(err, &exit) {
		if signal := exit.Signal(); signal != "" {
			return 0, err
		}
		return exit.ExitStatus(), nil
	}
	return 0, err
}

// Close closes stdin first so the peer sees end-of-file, then the channel.
func (e *execChannel) Close() (err error) {
	e.once.Do(func() {
		_ = e.stdin.Close()
		err = e.session.Close()
	})
	return err
}

// subsystemChannel is one open SSH subsystem. Read is the server's stdout,
// Write is its stdin, and Close hangs up the session channel.
type subsystemChannel struct {
	session *xssh.Session
	stdin   io.WriteCloser
	stdout  io.Reader
	once    sync.Once
}

var _ appconnection.Subsystem = (*subsystemChannel)(nil)

// Read returns subsystem output.
func (s *subsystemChannel) Read(p []byte) (int, error) { return s.stdout.Read(p) }

// Write sends subsystem input.
func (s *subsystemChannel) Write(p []byte) (int, error) { return s.stdin.Write(p) }

// Close closes the input stream and the channel. It is safe to call more
// than once.
func (s *subsystemChannel) Close() error {
	var err error
	s.once.Do(func() {
		_ = s.stdin.Close()
		err = s.session.Close()
	})
	return err
}
