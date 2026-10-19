// Package process is the platform seam for spawning a shell command whose
// stdin and stdout carry a byte stream. The SSH adapter uses it for
// ProxyCommand; keeping os/exec here leaves application and adapter code free
// of platform process plumbing.
package process

import (
	"bytes"
	"errors"
	"io"
	"net"
	"os/exec"
	"sync"
	"time"
)

// maxStderr keeps a failing command's diagnostic bounded.
const maxStderr = 4 << 10

// Start runs command through the platform shell and returns a net.Conn whose
// Read is the process's stdout and whose Write is its stdin. Closing the
// connection closes the pipes and kills the process.
func Start(command string) (net.Conn, error) {
	if command == "" {
		return nil, errors.New("process: empty command")
	}
	cmd := shellCommand(command)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr := &limitedBuffer{limit: maxStderr}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, err
	}
	return &commandConn{
		cmd:    cmd,
		stdin:  stdin,
		stdout: stdout,
		stderr: stderr,
	}, nil
}

// commandConn adapts a child process's pipes to net.Conn. Deadlines are
// accepted but not enforced: cancellation reaches the process through Close,
// which the caller watches on its context.
type commandConn struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr *limitedBuffer

	closeOnce sync.Once
}

func (c *commandConn) Read(p []byte) (int, error)  { return c.stdout.Read(p) }
func (c *commandConn) Write(p []byte) (int, error) { return c.stdin.Write(p) }

func (c *commandConn) Close() error {
	c.closeOnce.Do(func() {
		_ = c.stdin.Close()
		_ = c.stdout.Close()
		terminate(c.cmd)
		// Reaping must not block Close. A shell can outlive its own child's
		// inherited stderr, and the transport must still close promptly.
		go func() { _ = c.cmd.Wait() }()
	})
	return nil
}

// Stderr returns the bounded diagnostic output collected so far.
func (c *commandConn) Stderr() string { return c.stderr.String() }

func (c *commandConn) LocalAddr() net.Addr              { return commandAddr("process") }
func (c *commandConn) RemoteAddr() net.Addr             { return commandAddr("process") }
func (c *commandConn) SetDeadline(time.Time) error      { return nil }
func (c *commandConn) SetReadDeadline(time.Time) error  { return nil }
func (c *commandConn) SetWriteDeadline(time.Time) error { return nil }

type commandAddr string

func (a commandAddr) Network() string { return string(a) }
func (a commandAddr) String() string  { return string(a) }

// limitedBuffer keeps the first limit bytes written to it.
type limitedBuffer struct {
	mu    sync.Mutex
	limit int
	data  bytes.Buffer
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if remaining := b.limit - b.data.Len(); remaining > 0 {
		if len(p) > remaining {
			b.data.Write(p[:remaining])
		} else {
			b.data.Write(p)
		}
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.String()
}
