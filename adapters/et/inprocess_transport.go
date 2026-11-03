// SPDX-License-Identifier: GPL-3.0-or-later

package et

import (
	"context"
	"errors"
	"io"
	"iter"
	"log/slog"
	"sync"

	"github.com/mslxl/natcatty/adapters/et/internal/etgo/console"
	"github.com/mslxl/natcatty/adapters/et/internal/etgo/etcp"
	etsession "github.com/mslxl/natcatty/adapters/et/internal/etgo/session"
	appsession "github.com/mslxl/natcatty/application/session"
)

// inProcessTransport is one live pure-Go ET Session. It owns the encrypted
// packet connection, the SSH lease that carries it, and the terminal queues
// between the ET session service and the Session.
type inProcessTransport struct {
	conn     *etcp.Conn
	lease    ConnectionLease
	terminal *localTerminal
	logger   *slog.Logger
	cancel   context.CancelFunc

	mu     sync.Mutex
	closed bool
	hasEnd bool
	end    appsession.End

	done       chan struct{}
	finishOnce sync.Once
	closeOnce  sync.Once
}

var (
	_ appsession.Transport   = (*inProcessTransport)(nil)
	_ appsession.ExitWaiter  = (*inProcessTransport)(nil)
	_ appsession.EndReporter = (*inProcessTransport)(nil)
)

func newInProcessTransport(conn *etcp.Conn, lease ConnectionLease, terminal *localTerminal,
	cancel context.CancelFunc, logger *slog.Logger) *inProcessTransport {
	return &inProcessTransport{
		conn:     conn,
		lease:    lease,
		terminal: terminal,
		logger:   logger,
		cancel:   cancel,
		done:     make(chan struct{}),
	}
}

// run owns the ET session service for the transport's lifetime.
func (t *inProcessTransport) run(ctx context.Context) {
	err := etsession.Run(ctx, t.conn, etsession.Options{Terminal: t.terminal, Logger: t.logger})
	t.cancel()
	_ = t.conn.Close()

	t.mu.Lock()
	closed := t.closed
	t.mu.Unlock()
	end := classifyRun(err)
	if closed {
		end = appsession.End{Reason: appsession.EndReasonClosed}
	}
	t.finish(end)
}

// watchLease ends the ET connection when the SSH transport disappears. A
// reconnect would have no forwarding path, so waiting for an error is better
// than blocking until the Session's idle timeout.
func (t *inProcessTransport) watchLease(ctx context.Context) {
	select {
	case <-t.lease.Done():
		_ = t.conn.Close()
	case <-t.done:
	case <-ctx.Done():
	}
}

// Read returns remote terminal output. The ET session service closes the
// terminal just before Run returns, so a read error means the end is being
// classified right now. Waiting for it here means a caller that asks End as
// soon as Read fails (the Session read loop does) cannot see the unclassified
// error default and reconnect a Session that deliberately exited.
func (t *inProcessTransport) Read(p []byte) (int, error) {
	n, err := t.terminal.ReadOutput(p)
	if err != nil {
		<-t.done
	}
	return n, err
}

// Write sends terminal input.
func (t *inProcessTransport) Write(p []byte) (int, error) { return t.terminal.WriteInput(p) }

// Resize records a window size; the ET session service sends the latest value
// as TERMINAL_INFO.
func (t *inProcessTransport) Resize(cols, rows int) error {
	if t.isClosed() {
		return appsession.ErrClosed
	}
	t.terminal.SetSize(cols, rows)
	return nil
}

// Wait waits for the Session to end and reports its exit status. ET 6.2.10
// sends no exit status, so the value is zero with no code reported by End.
func (t *inProcessTransport) Wait() (int, error) {
	<-t.done
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.end.ExitCode, nil
}

// End reports why the Session stopped. It is safe after Close.
func (t *inProcessTransport) End() appsession.End {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.hasEnd {
		return t.end
	}
	return appsession.End{Reason: appsession.EndReasonError}
}

// Close ends the Session and waits for its goroutines. Closing a live
// Session is a deliberate stop, so the SSH connection stays reusable; a
// failed transport is discarded by finish.
func (t *inProcessTransport) Close() error {
	t.closeOnce.Do(func() {
		t.mu.Lock()
		t.closed = true
		t.mu.Unlock()
		t.cancel()
		_ = t.conn.Close()
		_ = t.terminal.Close()
	})
	<-t.done
	return nil
}

func (t *inProcessTransport) finish(end appsession.End) {
	t.finishOnce.Do(func() {
		t.mu.Lock()
		if !t.hasEnd {
			t.hasEnd = true
			t.end = end
		}
		t.mu.Unlock()
		if end.Reason == appsession.EndReasonError {
			_ = t.lease.Discard()
		} else {
			_ = t.lease.Release()
		}
		close(t.done)
	})
}

func (t *inProcessTransport) isClosed() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.closed
}

// localTerminal adapts the Session transport's byte directions to the ET
// session service's console contract. The service reads keyboard input and
// writes remote output; the Session transport reads output and writes input.
type localTerminal struct {
	inR  *io.PipeReader
	inW  *io.PipeWriter
	outR *io.PipeReader
	outW *io.PipeWriter

	mu       sync.Mutex
	size     console.Size
	updated  chan struct{}
	shutdown chan struct{}
	once     sync.Once
}

func newLocalTerminal(cols, rows int) *localTerminal {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	terminal := &localTerminal{
		inR:      inR,
		inW:      inW,
		outR:     outR,
		outW:     outW,
		updated:  make(chan struct{}, 1),
		shutdown: make(chan struct{}),
	}
	setConsoleSize(&terminal.size, cols, rows)
	return terminal
}

// Read reads keyboard input from the Session.
func (t *localTerminal) Read(p []byte) (int, error) { return t.inR.Read(p) }

// Write writes remote output to the Session.
func (t *localTerminal) Write(p []byte) (int, error) { return t.outW.Write(p) }

// Size returns the current window size.
func (t *localTerminal) Size() (console.Size, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.size, nil
}

// Resizes yields each changed size until ctx ends or the terminal closes.
func (t *localTerminal) Resizes(ctx context.Context) iter.Seq[console.Size] {
	return func(yield func(console.Size) bool) {
		last, _ := t.Size()
		for {
			select {
			case <-t.updated:
				current, _ := t.Size()
				if current == last {
					continue
				}
				last = current
				if !yield(current) {
					return
				}
			case <-t.shutdown:
				return
			case <-ctx.Done():
				return
			}
		}
	}
}

// SetSize records a new window size. Zero rows or columns are replaced by a
// usable default when the size is sent.
func (t *localTerminal) SetSize(cols, rows int) {
	t.mu.Lock()
	setConsoleSize(&t.size, cols, rows)
	t.mu.Unlock()
	signal(t.updated)
}

// Close unblocks both directions. The ET session service calls it when Run
// ends, so it must be idempotent.
func (t *localTerminal) Close() error {
	t.once.Do(func() {
		close(t.shutdown)
		_ = t.inR.Close()
		_ = t.inW.Close()
		_ = t.outR.Close()
		_ = t.outW.Close()
	})
	return nil
}

// ReadOutput is the Session's output read.
func (t *localTerminal) ReadOutput(p []byte) (int, error) { return t.outR.Read(p) }

// WriteInput is the Session's input write.
func (t *localTerminal) WriteInput(p []byte) (int, error) {
	n, err := t.inW.Write(p)
	if err != nil && errors.Is(err, io.ErrClosedPipe) {
		return n, appsession.ErrClosed
	}
	return n, err
}

// set applies a size from the Session, replacing a never-set dimension with
// the default the ET session service would send anyway.
func setConsoleSize(s *console.Size, cols, rows int) {
	if cols <= 0 {
		cols = defaultCols
	}
	if rows <= 0 {
		rows = defaultRows
	}
	s.Cols, s.Rows = cols, rows
}

func signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}
