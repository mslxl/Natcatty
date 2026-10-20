package session

import (
	"io"
	"sync"
)

// TerminalConn is the bounded duplex between a Session's transport and the
// terminal surface. Read yields output produced by the transport, Write
// queues input for it, and Resize reaches the transport through the Session's
// command loop.
//
// Output is never dropped. When the backlog reaches HighWaterMark the reader
// stops reading the transport, so the PTY or remote shell applies its own
// backpressure; Read resumes it once the backlog falls to LowWaterMark.
// Write blocks when the bounded writer queue is full, then unblocks with
// io.ErrClosedPipe when the Session ends.
type TerminalConn struct {
	session *Session

	mu       sync.Mutex
	cond     *sync.Cond
	buffer   []byte
	pending  int
	paused   bool
	eof      bool
	closed   bool
	finished bool
	// view is the token of the Session's one live view. A view created by
	// Resume or AttachView reads through readView, which stops returning
	// output as soon as it is detached, so a hibernated view can never steal
	// bytes from its replacement.
	view *viewToken
}

// viewToken identifies one claimed live view.
type viewToken struct{}

func newTerminalConn(session *Session) *TerminalConn {
	conn := &TerminalConn{session: session}
	conn.cond = sync.NewCond(&conn.mu)
	return conn
}

// Read returns terminal output, blocking until some arrives. It returns io.EOF
// once the transport ended and the backlog is drained.
func (c *TerminalConn) Read(p []byte) (int, error) {
	return c.read(p, nil)
}

// read drains the backlog for the owning view when token is non-nil, or for
// the legacy direct reader when it is nil. A view whose token is no longer
// current gets io.EOF without consuming bytes meant for its replacement.
func (c *TerminalConn) read(p []byte, token *viewToken) (int, error) {
	c.mu.Lock()
	for len(c.buffer) == 0 && (token == nil || c.view == token) && !c.eof && !c.closed {
		c.cond.Wait()
	}
	if token != nil && c.view != token {
		c.mu.Unlock()
		return 0, io.EOF
	}
	if len(c.buffer) > 0 {
		n := copy(p, c.buffer)
		c.buffer = c.buffer[n:]
		if len(c.buffer) == 0 {
			c.buffer = nil
		}
		c.pending -= n
		if c.paused && c.pending <= LowWaterMark {
			c.paused = false
			c.cond.Broadcast()
		}
		c.mu.Unlock()
		return n, nil
	}
	c.mu.Unlock()
	return 0, io.EOF
}

// Write queues input for the transport. It blocks while the bounded writer
// queue is full and returns io.ErrClosedPipe once the Session ended. Input is
// observed first so command tracking sees the keys the user typed and a file
// transfer gate can drop normal input.
func (c *TerminalConn) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	forwarded, _, _ := c.session.handleInput(p, true)
	if len(forwarded) == 0 {
		select {
		case <-c.session.done:
			return 0, io.ErrClosedPipe
		case <-c.session.ctx.Done():
			return 0, io.ErrClosedPipe
		default:
			return len(p), nil
		}
	}
	copied := append([]byte(nil), forwarded...)
	select {
	case c.session.writeCh <- copied:
		c.session.tap.addInputBytes(len(copied))
		return len(p), nil
	case <-c.session.done:
		return 0, io.ErrClosedPipe
	}
}

// Resize asks the transport to change its window size, as MyGo's terminal
// does when its view changes size.
func (c *TerminalConn) Resize(cols, rows int) error {
	return c.session.Resize(cols, rows)
}

// Close ends the Session. It is safe to call more than once.
func (c *TerminalConn) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.buffer = nil
	c.pending = 0
	c.cond.Broadcast()
	c.mu.Unlock()
	return c.session.Close()
}

// appendOutput adds transport output to the backlog and engages backpressure
// at the high watermark. The reader goroutine calls it.
func (c *TerminalConn) appendOutput(p []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.eof {
		return
	}
	c.buffer = append(c.buffer, p...)
	c.pending += len(p)
	if c.pending >= HighWaterMark {
		c.paused = true
	}
	c.cond.Broadcast()
}

// waitReadable blocks while backpressure is engaged. It reports false when the
// reader should stop.
func (c *TerminalConn) waitReadable() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for c.paused && !c.eof && !c.closed {
		c.cond.Wait()
	}
	return !c.eof && !c.closed
}

// finish marks the transport's output complete and wakes both directions.
func (c *TerminalConn) finish() {
	c.mu.Lock()
	if c.finished {
		c.mu.Unlock()
		return
	}
	c.finished = true
	c.eof = true
	c.paused = false
	c.cond.Broadcast()
	c.mu.Unlock()
}

func (c *TerminalConn) stats() (pending int, paused bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pending, c.paused
}

// attachView claims the Session's one live view. It reports false when a view
// already owns the connection.
func (c *TerminalConn) attachView() (*viewToken, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.view != nil {
		return nil, false
	}
	token := &viewToken{}
	c.view = token
	c.cond.Broadcast()
	return token, true
}

// detachView detaches a view and wakes its blocked Read, which returns io.EOF
// without consuming output meant for a later view.
func (c *TerminalConn) detachView(token *viewToken) {
	c.mu.Lock()
	if c.view == token {
		c.view = nil
		c.cond.Broadcast()
	}
	c.mu.Unlock()
}

func (c *TerminalConn) hasView() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.view != nil
}

// detachCurrentView drops whichever view is current. Hibernate uses it to
// release the Session's one live view; the old ViewConn's Read then returns
// io.EOF.
func (c *TerminalConn) detachCurrentView() {
	c.mu.Lock()
	c.view = nil
	c.cond.Broadcast()
	c.mu.Unlock()
}

// readView is Read for the Session's one live view.
func (c *TerminalConn) readView(token *viewToken, p []byte) (int, error) {
	return c.read(p, token)
}

// discardPending drops output queued for a view that is being replaced. The
// Session's replay keeps the same bytes for the resumed or observed screen, so
// the new view cannot render them twice. It also releases transport
// backpressure, so a hibernated Session that was paused resumes reading.
func (c *TerminalConn) discardPending() {
	c.mu.Lock()
	c.buffer = nil
	c.pending = 0
	if c.paused {
		c.paused = false
		c.cond.Broadcast()
	}
	c.mu.Unlock()
}
