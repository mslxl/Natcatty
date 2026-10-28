package session

import (
	"bytes"
	"errors"
	"io"
	"sync"

	"github.com/mslxl/natcatty/domain/model"
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
)

// Snapshot/Feed bounds. The replay keeps the same 3,000-line history the
// original's hibernate snapshot captured, with a byte cap so a Session that
// never rewraps cannot retain unbounded output. An observer that falls behind
// is bounded the same way; it never backpressures the live Session.
const (
	// SnapshotMaxBytes bounds one screen snapshot.
	SnapshotMaxBytes = 512 << 10
	// SnapshotMaxLines bounds one screen snapshot by lines, matching the
	// original's TERMINAL_HIBERNATE_SNAPSHOT_MAX_LINES.
	SnapshotMaxLines = 3000
	// feedMaxBytes bounds output buffered for one observer.
	feedMaxBytes = 512 << 10
)

var (
	// ErrViewAttached reports that a Session already has its one live view.
	ErrViewAttached = errors.New("session: a live view is already attached")
	// ErrNotHibernated reports a Resume for a Session that is not hibernated.
	ErrNotHibernated = errors.New("session: the Session is not hibernated")
	// ErrObserverReadOnly reports input written to an observer. Observers
	// never route input to the owning Session.
	ErrObserverReadOnly = errors.New("session: an observer is read-only")
)

// ScreenSnapshot is one replayable capture of a Session's rendered output.
// Feeding Data to a terminal of Cols x Rows reproduces the screen the owning
// view rendered. Output produced afterwards arrives through the Observation's
// blocking Read.
type ScreenSnapshot struct {
	SessionID model.SessionID
	Cols      int
	Rows      int
	Encoding  domainterminal.Encoding
	Data      []byte
}

// snapshotBuffer keeps the newest output within the snapshot bounds. It trims
// at line boundaries so a replayed stream does not start mid-line.
type snapshotBuffer struct {
	data  []byte
	lines int
}

func (b *snapshotBuffer) append(p []byte) {
	if len(p) == 0 {
		return
	}
	b.data = append(b.data, p...)
	b.lines += bytes.Count(p, []byte{'\n'})
	b.trim()
}

func (b *snapshotBuffer) snapshot() []byte {
	return append([]byte(nil), b.data...)
}

func (b *snapshotBuffer) trim() {
	if cut := newestBoundary(b.data, SnapshotMaxBytes); cut > 0 {
		b.lines -= bytes.Count(b.data[:cut], []byte{'\n'})
		b.data = append(b.data[:0], b.data[cut:]...)
	}
	for b.lines > SnapshotMaxLines {
		index := bytes.IndexByte(b.data, '\n')
		if index < 0 {
			b.lines = 0
			break
		}
		b.data = b.data[index+1:]
		b.lines--
	}
	// Slicing keeps the original array; compact once most of it is dead.
	if cap(b.data) > 2*SnapshotMaxBytes && len(b.data) < SnapshotMaxBytes {
		b.data = append([]byte(nil), b.data...)
	}
}

// newestBoundary returns where to cut data so at most max bytes remain. It
// prefers the first line boundary inside the dropped region so a replayed
// stream does not start mid-line.
func newestBoundary(data []byte, max int) int {
	if len(data) <= max {
		return 0
	}
	cut := len(data) - max
	if index := bytes.IndexByte(data[cut:], '\n'); index >= 0 {
		return cut + index + 1
	}
	return cut
}

// outputFeed is a bounded, read-only stream of rendered output. Read blocks
// until the Session produces output. Input never travels this way.
type outputFeed struct {
	mu     sync.Mutex
	cond   *sync.Cond
	buffer []byte
	closed bool
}

func newOutputFeed() *outputFeed {
	feed := &outputFeed{}
	feed.cond = sync.NewCond(&feed.mu)
	return feed
}

// Read returns output produced after the observation, blocking until some
// arrives. It returns io.EOF once the observation is closed.
func (f *outputFeed) Read(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for len(f.buffer) == 0 && !f.closed {
		f.cond.Wait()
	}
	if len(f.buffer) == 0 {
		return 0, io.EOF
	}
	n := copy(p, f.buffer)
	f.buffer = f.buffer[n:]
	if len(f.buffer) == 0 {
		f.buffer = nil
	}
	return n, nil
}

func (f *outputFeed) push(p []byte) {
	if len(p) == 0 {
		return
	}
	f.mu.Lock()
	if !f.closed {
		f.buffer = append(f.buffer, p...)
		if cut := newestBoundary(f.buffer, feedMaxBytes); cut > 0 {
			f.buffer = append(f.buffer[:0], f.buffer[cut:]...)
		}
		f.cond.Broadcast()
	}
	f.mu.Unlock()
}

func (f *outputFeed) close() {
	f.mu.Lock()
	f.closed = true
	f.buffer = nil
	f.cond.Broadcast()
	f.mu.Unlock()
}

// Observation is an observer's view of one Session: the screen snapshot the
// Session had when the observation opened, followed by a blocking feed of
// output produced afterwards. It is an io.ReadWriteCloser for MyGo's Conn
// option, but Write is refused: only the owning live view may type.
type Observation struct {
	session *Session
	feed    *outputFeed
	screen  ScreenSnapshot

	mu     sync.Mutex
	head   []byte
	closed bool
}

// Screen returns the snapshot captured when the observation opened.
func (o *Observation) Screen() ScreenSnapshot {
	screen := o.screen
	screen.Data = append([]byte(nil), o.screen.Data...)
	return screen
}

// Read yields the snapshot first and then the feed. It never reads the live
// view's TerminalConn, so an observer cannot consume or backpressure the
// owning screen.
func (o *Observation) Read(p []byte) (int, error) {
	o.mu.Lock()
	if len(o.head) > 0 {
		n := copy(p, o.head)
		o.head = o.head[n:]
		o.mu.Unlock()
		return n, nil
	}
	o.mu.Unlock()
	return o.feed.Read(p)
}

// Write refuses observer input.
func (o *Observation) Write([]byte) (int, error) {
	return 0, ErrObserverReadOnly
}

// Resize is accepted and ignored: the shadow terminal may size itself, but
// resizing an observer must never resize the owning Session's transport.
func (o *Observation) Resize(int, int) error { return nil }

// Close stops the feed. The owning Session and its live view are unaffected.
func (o *Observation) Close() error {
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return nil
	}
	o.closed = true
	o.mu.Unlock()
	o.session.dropObservation(o.feed)
	o.feed.close()
	return nil
}

// Observe atomically captures the Session's screen and subscribes to output
// produced after it. The returned Observation is read-only and independent of
// the Session's single live view.
func (s *Session) Observe() (*Observation, error) {
	s.mu.Lock()
	snapshot := s.snapshot
	s.mu.Unlock()

	s.outputMu.Lock()
	screen := ScreenSnapshot{
		SessionID: snapshot.ID,
		Cols:      snapshot.Cols,
		Rows:      snapshot.Rows,
		Encoding:  snapshot.Encoding,
		Data:      s.replay.snapshot(),
	}
	feed := newOutputFeed()
	if s.feeds == nil {
		s.feeds = make(map[*outputFeed]struct{})
	}
	s.feeds[feed] = struct{}{}
	s.outputMu.Unlock()

	return &Observation{
		session: s,
		feed:    feed,
		screen:  screen,
		head:    append([]byte(nil), screen.Data...),
	}, nil
}

func (s *Session) dropObservation(feed *outputFeed) {
	s.outputMu.Lock()
	delete(s.feeds, feed)
	s.outputMu.Unlock()
}

// emitOutput publishes rendered output to the bounded replay, every open
// observation, and the live view's queue. It is the single point the tap's
// filtered output passes through.
func (s *Session) emitOutput(p []byte) {
	if len(p) == 0 {
		return
	}
	s.outputMu.Lock()
	s.replay.append(p)
	for feed := range s.feeds {
		feed.push(p)
	}
	// A running programmatic command reads transport output through the raw
	// feed. If no view is attached there is no terminal reader to drain this
	// live queue, so appending a megabyte would hit HighWaterMark, pause the
	// transport, and deadlock the command before its end marker arrives. The
	// replay and observers above still receive the output.
	s.rawMu.Lock()
	rawCommand := s.rawFeed != nil
	s.rawMu.Unlock()
	if !rawCommand || s.conn.hasView() {
		s.conn.appendOutput(p)
	}
	s.outputMu.Unlock()
}

// ClearBuffer runs the terminal's clear-scrollback sequence through the same
// rendered-output path as program output. The replay and every observer then
// clear with the owning screen instead of resurrecting cleared scrollback from
// the bounded history.
func (s *Session) ClearBuffer() {
	s.emitOutput([]byte("\x1b[3J"))
}

// closeObservations ends every observer feed. A Session that ends while an
// observation is open reports io.EOF instead of blocking that reader forever.
func (s *Session) closeObservations() {
	s.outputMu.Lock()
	feeds := s.feeds
	s.feeds = nil
	s.outputMu.Unlock()
	for feed := range feeds {
		feed.close()
	}
}

// Hibernate detaches the Session's screen from its resources while leaving the
// transport and every application-level field alive. It reports false for a
// Session that already ended or is already hibernated. Every live view is
// detached; output produced while hibernated is retained in the bounded replay
// and replayed by Resume.
func (s *Session) Hibernate() bool {
	s.mu.Lock()
	if s.snapshot.Status != StatusRunning || s.snapshot.Hibernated {
		s.mu.Unlock()
		return false
	}
	s.snapshot.Hibernated = true
	snapshot := s.snapshot
	s.mu.Unlock()

	s.outputMu.Lock()
	s.conn.discardPending()
	s.conn.detachCurrentView()
	s.outputMu.Unlock()

	if s.onChange != nil {
		s.onChange(snapshot)
	}
	return true
}

// Resume atomically reopens the Session's single live view. The returned
// ResumedView replays the bounded screen snapshot and then follows output
// produced after it; Write and Resize reach the Session normally.
func (s *Session) Resume() (*ResumedView, error) {
	s.mu.Lock()
	if !s.snapshot.Hibernated || s.snapshot.Status != StatusRunning {
		s.mu.Unlock()
		return nil, ErrNotHibernated
	}
	cols, rows := s.snapshot.Cols, s.snapshot.Rows
	s.mu.Unlock()

	s.outputMu.Lock()
	screen := s.replay.snapshot()
	s.conn.discardPending()
	s.outputMu.Unlock()

	token, ok := s.conn.attachView()
	if !ok {
		return nil, ErrViewAttached
	}

	s.mu.Lock()
	if !s.snapshot.Hibernated {
		s.mu.Unlock()
		s.conn.detachView(token)
		return nil, ErrNotHibernated
	}
	s.snapshot.Hibernated = false
	snapshot := s.snapshot
	s.mu.Unlock()
	if s.onChange != nil {
		s.onChange(snapshot)
	}

	return &ResumedView{
		view: &ViewConn{session: s, token: token},
		head: append([]byte(nil), screen...),
		cols: cols,
		rows: rows,
	}, nil
}

// LiveViews reports how many live terminal views are attached. The Session
// allows at most one; observers do not count.
func (s *Session) LiveViews() int {
	if s.conn.hasView() {
		return 1
	}
	return 0
}

// AttachView claims the Session's one live view. A second claim fails with
// ErrViewAttached instead of creating another view of the same Session.
func (s *Session) AttachView() (*ViewConn, error) {
	token, ok := s.conn.attachView()
	if !ok {
		return nil, ErrViewAttached
	}
	return &ViewConn{session: s, token: token}, nil
}

// ViewConn is the live view's connection to a Session. It reads and writes the
// same bounded duplex the terminal always used, but Close only detaches the
// view: the Session and its transport stay alive for hibernate or for the
// shell's explicit Close.
type ViewConn struct {
	session *Session
	token   *viewToken
	once    sync.Once
}

// Read returns the live view's output. A detached view returns io.EOF without
// consuming bytes meant for its replacement.
func (c *ViewConn) Read(p []byte) (int, error) {
	return c.session.conn.readView(c.token, p)
}

// Write sends input to the Session's transport.
func (c *ViewConn) Write(p []byte) (int, error) { return c.session.conn.Write(p) }

// Resize reaches the transport through the Session's command loop.
func (c *ViewConn) Resize(cols, rows int) error { return c.session.Resize(cols, rows) }

// Close detaches the view without ending the Session.
func (c *ViewConn) Close() error {
	c.once.Do(func() { c.session.conn.detachView(c.token) })
	return nil
}

// ResumedView is the Conn for a view created by Resume. Its first reads replay
// the screen snapshot; after that it is the ordinary live ViewConn. Serving
// the snapshot through the connection keeps it ordered before any live output
// the terminal's reader may already have picked up.
type ResumedView struct {
	view *ViewConn

	mu   sync.Mutex
	head []byte
	cols int
	rows int
}

// ScreenSize reports the size the snapshot was captured at.
func (r *ResumedView) ScreenSize() (cols, rows int) { return r.cols, r.rows }

// Read replays the snapshot, then reads live output.
func (r *ResumedView) Read(p []byte) (int, error) {
	r.mu.Lock()
	if len(r.head) > 0 {
		n := copy(p, r.head)
		r.head = r.head[n:]
		r.mu.Unlock()
		return n, nil
	}
	r.mu.Unlock()
	return r.view.Read(p)
}

// Write sends input to the Session's transport.
func (r *ResumedView) Write(p []byte) (int, error) { return r.view.Write(p) }

// Resize reaches the transport.
func (r *ResumedView) Resize(cols, rows int) error { return r.view.Resize(cols, rows) }

// Close detaches the resumed live view.
func (r *ResumedView) Close() error { return r.view.Close() }
