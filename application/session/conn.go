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
	// viewSink applies an authoritative remote screen size to the live view's
	// terminal. It is registered by the surface after it creates MyGo and is
	// cleared with the view. The reading goroutine applies pending sizes after
	// the output produced before the resize and before output encoded for the
	// new size.
	viewSink func(cols, rows int)
	// pendingScreen is the newest remote size the reader has not applied yet.
	pendingScreen    *screenSizeRequest
	screenGeneration uint64
	// transportRead counts output bytes read from the current ScreenSizer
	// transport and appended to the backlog. It lets the transport's resize
	// handler wait until every earlier byte reached the view at the old size.
	transportRead uint64
}

// screenSizeRequest is one remote resize waiting for the live view's reader.
// drainBefore is the number of unread backlog bytes produced before the
// resize; they are returned to the view before the size is applied.
type screenSizeRequest struct {
	cols, rows  int
	generation  uint64
	drainBefore int
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
//
// A pending authoritative resize splits the backlog: bytes produced before it
// are returned at the old size, then this goroutine calls Terminal.Resize, and
// only then are bytes encoded for the new size returned. That is the other
// half of MyGo's ResizeByConn contract.
func (c *TerminalConn) read(p []byte, token *viewToken) (int, error) {
	c.mu.Lock()
	for {
		if token != nil && c.view != token {
			c.mu.Unlock()
			return 0, io.EOF
		}
		if c.pendingScreen != nil {
			resize := *c.pendingScreen
			// Bytes the transport produced before the resize belong to the
			// old screen. Deliver them before Terminal.Resize so MyGo lays
			// them out at the size the server used.
			if resize.drainBefore > 0 && len(c.buffer) > 0 {
				n := c.consumeLocked(p, resize.drainBefore)
				resize.drainBefore -= n
				c.pendingScreen = &resize
				c.mu.Unlock()
				return n, nil
			}
			sink := c.viewSink
			if sink == nil {
				if c.eof || c.closed {
					c.pendingScreen = nil
					c.cond.Broadcast()
					continue
				}
				if token == nil && c.view == nil {
					// No live screen can apply it; a later view reads the
					// next resize the transport reports.
					c.pendingScreen = nil
					c.cond.Broadcast()
					continue
				}
				c.cond.Wait()
				continue
			}
			c.mu.Unlock()
			sink(resize.cols, resize.rows)
			c.mu.Lock()
			if c.pendingScreen != nil && c.pendingScreen.generation == resize.generation {
				c.pendingScreen = nil
				c.cond.Broadcast()
			}
			continue
		}
		if len(c.buffer) > 0 {
			n := c.consumeLocked(p, len(p))
			c.mu.Unlock()
			return n, nil
		}
		if c.eof || c.closed {
			c.mu.Unlock()
			return 0, io.EOF
		}
		c.cond.Wait()
	}
}

// consumeLocked moves at most limit bytes from the backlog into p. The caller
// holds c.mu.
func (c *TerminalConn) consumeLocked(p []byte, limit int) int {
	if limit > len(p) {
		limit = len(p)
	}
	if limit > len(c.buffer) {
		limit = len(c.buffer)
	}
	n := copy(p, c.buffer[:limit])
	c.buffer = c.buffer[n:]
	if len(c.buffer) == 0 {
		c.buffer = nil
	}
	c.pending -= n
	if c.paused && c.pending <= LowWaterMark {
		c.paused = false
		c.cond.Broadcast()
	}
	return n
}

// noteTransportRead records output bytes read from the current ScreenSizer
// transport. The resize handler waits on this count so a screen size never
// overtakes bytes the server produced before it.
func (c *TerminalConn) noteTransportRead(n int) {
	if n <= 0 {
		return
	}
	c.mu.Lock()
	c.transportRead += uint64(n)
	c.cond.Broadcast()
	c.mu.Unlock()
}

// resetTransportPosition starts a new transport generation at offset zero.
func (c *TerminalConn) resetTransportPosition() {
	c.mu.Lock()
	c.transportRead = 0
	c.mu.Unlock()
}

// applyScreenSize records the peer's authoritative size and waits until the
// live view's reader has applied it. Position is the transport's output offset
// before the resize; with a live view, every byte below it is delivered to the
// view at the old size first. With no live view the size stays pending and the
// next view applies it before reading queued output. The transport calls this
// before making display bytes encoded at that size available.
func (c *TerminalConn) applyScreenSize(cols, rows int, position uint64) {
	if cols <= 0 || rows <= 0 {
		return
	}
	c.mu.Lock()
	if c.closed || c.eof {
		c.mu.Unlock()
		return
	}
	waited := c.view != nil
	for c.view != nil && !c.eof && !c.closed && c.transportRead < position {
		c.cond.Wait()
	}
	if c.closed || c.eof {
		c.mu.Unlock()
		return
	}
	if waited {
		// Keyword highlighting can hold a partial line after the reader has
		// consumed its raw bytes. Flush it now so those bytes join the old-size
		// backlog rather than surfacing after the resize.
		c.mu.Unlock()
		c.session.flushHighlightCarry()
		c.mu.Lock()
		if c.closed || c.eof {
			c.mu.Unlock()
			return
		}
	}
	c.screenGeneration++
	request := &screenSizeRequest{
		cols:        cols,
		rows:        rows,
		generation:  c.screenGeneration,
		drainBefore: len(c.buffer),
	}
	c.pendingScreen = request
	c.cond.Broadcast()
	if c.view == nil {
		c.mu.Unlock()
		return
	}
	for c.pendingScreen != nil && c.pendingScreen.generation == request.generation &&
		c.view != nil && !c.eof && !c.closed {
		c.cond.Wait()
	}
	c.mu.Unlock()
}

// prepareScreen applies a pending authoritative size to the live view through
// its sink. A resumed view calls this before replaying its snapshot: the
// snapshot may already contain output encoded for the new size, and it must
// not reach the terminal before Terminal.Resize. A nil sink waits until the
// surface registers one or the view is detached.
func (c *TerminalConn) prepareScreen(token *viewToken) {
	c.mu.Lock()
	for {
		if token != nil && c.view != token {
			c.mu.Unlock()
			return
		}
		if c.pendingScreen == nil {
			c.mu.Unlock()
			return
		}
		pending := *c.pendingScreen
		sink := c.viewSink
		if sink == nil {
			if c.eof || c.closed {
				c.pendingScreen = nil
				c.cond.Broadcast()
				c.mu.Unlock()
				return
			}
			c.cond.Wait()
			continue
		}
		c.mu.Unlock()
		sink(pending.cols, pending.rows)
		c.mu.Lock()
		if c.pendingScreen != nil && c.pendingScreen.generation == pending.generation {
			c.pendingScreen = nil
			c.cond.Broadcast()
		}
	}
}

// setScreenSizeSink installs the live view's Terminal.Resize. The token check
// keeps a detached view from applying a size meant for its replacement.
func (c *TerminalConn) setScreenSizeSink(token *viewToken, sink func(cols, rows int)) {
	c.mu.Lock()
	if token != nil && c.view == token {
		c.viewSink = sink
		c.cond.Broadcast()
	}
	c.mu.Unlock()
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
	c.pendingScreen = nil
	c.viewSink = nil
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
		c.viewSink = nil
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
	c.viewSink = nil
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
