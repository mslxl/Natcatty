//go:build windows

package ssh

import (
	"context"
	"errors"
	"net"
	"os"
	"time"
)

// ErrAgentUnavailable reports that no local SSH agent is reachable.
var ErrAgentUnavailable = errors.New("ssh: agent unavailable")

// openSSHAgentPipe is the named pipe the OpenSSH client uses for its agent on
// Windows.
const openSSHAgentPipe = `\\.\pipe\openssh-ssh-agent`

// AgentDialFunc opens the local SSH agent's transport. Tests replace it with
// an in-memory pipe running agent.ServeAgent.
type AgentDialFunc func(ctx context.Context) (net.Conn, error)

// defaultAgentDial connects to the OpenSSH named pipe. Windows has no
// SSH_AUTH_SOCK convention in the OpenSSH client, so the well-known pipe is
// the only supported location.
func defaultAgentDial(ctx context.Context) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(openSSHAgentPipe, os.O_RDWR, 0)
	if err != nil {
		return nil, errors.Join(ErrAgentUnavailable, err)
	}
	return &pipeConn{file: file}, nil
}

// pipeConn adapts an os.File opened on a named pipe to net.Conn. It has no
// meaningful addresses; the SSH agent protocol does not need them.
type pipeConn struct {
	file *os.File
}

func (c *pipeConn) Read(p []byte) (int, error)  { return c.file.Read(p) }
func (c *pipeConn) Write(p []byte) (int, error) { return c.file.Write(p) }
func (c *pipeConn) Close() error                { return c.file.Close() }

func (c *pipeConn) LocalAddr() net.Addr  { return pipeAddr("local") }
func (c *pipeConn) RemoteAddr() net.Addr { return pipeAddr("agent") }

func (c *pipeConn) SetDeadline(t time.Time) error      { return c.file.SetDeadline(t) }
func (c *pipeConn) SetReadDeadline(t time.Time) error  { return c.file.SetReadDeadline(t) }
func (c *pipeConn) SetWriteDeadline(t time.Time) error { return c.file.SetWriteDeadline(t) }

type pipeAddr string

func (pipeAddr) Network() string  { return "pipe" }
func (a pipeAddr) String() string { return string(a) }
