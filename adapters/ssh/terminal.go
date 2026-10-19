package ssh

import (
	"context"
	"io"
	"sync"

	xssh "golang.org/x/crypto/ssh"

	appconnection "github.com/mslxl/natcatty/application/connection"
)

// sshClient is the adapter's authenticated transport. It opens interactive
// shell channels and closes the underlying SSH connection once.
type sshClient struct {
	client *xssh.Client
	mu     sync.Mutex
	closed bool
}

var _ appconnection.Client = (*sshClient)(nil)

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

// Close closes the SSH transport. Open channels are closed with it.
func (c *sshClient) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()
	return c.client.Close()
}

// channelTerminal is one interactive shell channel. Read and Write are the
// remote stdout and stdin; Resize sends a window-change request.
type channelTerminal struct {
	session *xssh.Session
	stdin   io.WriteCloser
	stdout  io.Reader

	mu     sync.Mutex
	closed bool
}

var _ appconnection.Terminal = (*channelTerminal)(nil)

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
