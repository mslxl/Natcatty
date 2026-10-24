package connection

import (
	"context"
	"errors"
	"io"
)

// ErrExecUnavailable reports a transport that cannot run a non-interactive
// command, such as a test fake or a vendor client without exec support.
var ErrExecUnavailable = errors.New("connection: exec unavailable")

// ExecResult is one finished non-interactive command. HasExitCode reports
// whether the peer sent a status; a dropped transport has none.
type ExecResult struct {
	Stdout      string
	Stderr      string
	ExitCode    int
	HasExitCode bool
}

// ExecStream is one open exec channel. Read and Write are the remote stdout
// and stdin; Stderr is the remote stderr. The caller closes the stream when
// the protocol ends.
type ExecStream interface {
	io.ReadWriteCloser
	// Stderr returns the remote stderr stream. It is valid until the stream
	// is closed.
	Stderr() io.Reader
	// ExitCode waits for the remote command to finish and returns its status.
	// It reports an error when no status arrived, for example after a dropped
	// transport.
	ExitCode() (int, error)
}

// HalfCloser is an optional ExecStream capability that closes the command's
// stdin while stdout stays readable. A streaming consumer such as tar needs
// end-of-file on its input to finish and report an exit status before the
// whole channel is closed. A stream without this capability is closed instead,
// which aborts the command.
type HalfCloser interface {
	CloseWrite() error
}

// Executor is the non-interactive command capability of an authenticated
// transport. The SCP backend runs shell commands and its own scp -t/-f
// protocol over it; the connection layer never parses either.
type Executor interface {
	// Exec runs one command to completion and returns its bounded output.
	// The context bounds the command: cancellation closes the channel.
	Exec(ctx context.Context, command string) (ExecResult, error)
	// OpenExec starts one command and returns its raw stream. The context
	// bounds only opening; the caller owns the stream afterwards.
	OpenExec(ctx context.Context, command string) (ExecStream, error)
}

// Executor returns the leased Connection's exec capability. A released lease,
// a closed Connection, and a transport without exec support fail closed.
func (l *Lease) Executor() (Executor, error) {
	if l == nil {
		return nil, ErrClosed
	}
	l.mu.Lock()
	released := l.released
	l.mu.Unlock()
	if released || l.conn == nil {
		return nil, ErrClosed
	}
	l.conn.mu.Lock()
	client := l.conn.client
	closed := l.conn.closed
	l.conn.mu.Unlock()
	if closed || client == nil {
		return nil, ErrClosed
	}
	executor, ok := client.(Executor)
	if !ok {
		return nil, unsupportedError(ErrExecUnavailable)
	}
	return executor, nil
}

// Exec runs one non-interactive command on the leased Connection.
func (l *Lease) Exec(ctx context.Context, command string) (ExecResult, error) {
	executor, err := l.Executor()
	if err != nil {
		return ExecResult{}, err
	}
	return executor.Exec(ctx, command)
}

// OpenExec opens one exec channel on the leased Connection.
func (l *Lease) OpenExec(ctx context.Context, command string) (ExecStream, error) {
	executor, err := l.Executor()
	if err != nil {
		return nil, err
	}
	return executor.OpenExec(ctx, command)
}

// RemoteVersion returns the peer's SSH identification line when the transport
// captured it.
func (l *Lease) RemoteVersion() string {
	if l == nil || l.conn == nil {
		return ""
	}
	l.conn.mu.Lock()
	client := l.conn.client
	l.conn.mu.Unlock()
	return remoteVersion(client)
}

// SingleChannel reports whether this peer accepts only one session channel per
// TCP connection. Such a peer cannot host a second channel, so SFTP on a
// shared transport and SCP fallback are both refused.
func (l *Lease) SingleChannel() bool {
	if l == nil || l.conn == nil {
		return false
	}
	l.conn.mu.Lock()
	client := l.conn.client
	endpoint := l.conn.endpoint
	l.conn.mu.Unlock()
	return endpoint.singleChannel || RemoteRequiresSingleChannel(remoteVersion(client))
}

// Reused reports whether this lease shares a transport that was already open
// before the Acquire call. A reused single-channel transport must not host an
// SFTP channel.
func (l *Lease) Reused() bool { return l != nil && l.reused }
