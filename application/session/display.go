package session

import (
	"errors"
	"image"
	"sync"
	"time"
)

// The display-session port carries a pixel Session beside the byte-stream
// Transport port. It is deliberately free of UI types: pixels cross the
// boundary as stdlib image snapshots, and the viewer owns every widget.
//
// One consumer drains Events. Every send method is safe from any goroutine;
// SerializedDisplay is the wrapper that enforces it for an adapter. Frames are
// immutable snapshots and are coalesced by DisplayEmitter to at most
// MaxFramesPerSecond.

// MaxFramesPerSecond bounds how many frame events a display Session delivers.
// The receive side never sees the decode buffer, so intermediate frames may be
// replaced by a newer snapshot; the newest frame is always delivered.
const MaxFramesPerSecond = 30

// FrameInterval is the minimum time between delivered frame events.
const FrameInterval = time.Second / MaxFramesPerSecond

// displayEventBuffer bounds events queued toward the single consumer. It lets
// Close deposit the final frame without a consumer and lets a short UI stall
// absorb a burst of non-frame events.
const displayEventBuffer = 64

// ErrNoDisplay reports an operation that needs a pixel Session while none is
// open, for example after a Session ended or while a reconnect is pending.
var ErrNoDisplay = errors.New("session: no display session")

// ErrDesktopResizeUnsupported reports a desktop resize request the server did
// not negotiate. It is explicit so the viewer can keep letterboxing instead of
// treating the server's silence as a failure.
var ErrDesktopResizeUnsupported = errors.New("session: desktop resize was not negotiated")

// ErrNoByteStream reports an operation that only exists for a terminal
// byte-stream Session. A VNC Session exposes pixels, not a terminal.
var ErrNoByteStream = errors.New("session: display session has no byte stream")

// DisplayEventKind discriminates DisplayEvent values.
type DisplayEventKind uint8

const (
	// DisplayEventFrame carries an immutable framebuffer snapshot.
	DisplayEventFrame DisplayEventKind = iota
	// DisplayEventResize reports a new remote framebuffer size.
	DisplayEventResize
	// DisplayEventBell reports a bell from the remote desktop.
	DisplayEventBell
	// DisplayEventCutText reports the server's clipboard text in Text.
	DisplayEventCutText
	// DisplayEventCursorShape reports the server cursor shape in Cursor.
	DisplayEventCursorShape
	// DisplayEventDesktopName reports the server's desktop name in Text.
	DisplayEventDesktopName
	// DisplayEventError reports a fatal display failure in Err. A failure that
	// implements Permanent() bool ends the Session without a retry.
	DisplayEventError
)

func (k DisplayEventKind) String() string {
	switch k {
	case DisplayEventFrame:
		return "frame"
	case DisplayEventResize:
		return "resize"
	case DisplayEventBell:
		return "bell"
	case DisplayEventCutText:
		return "cut-text"
	case DisplayEventCursorShape:
		return "cursor-shape"
	case DisplayEventDesktopName:
		return "desktop-name"
	case DisplayEventError:
		return "error"
	default:
		return "unknown"
	}
}

// DisplayFrame is one immutable framebuffer snapshot. Image is a deep copy of
// the decoder's buffer, so later decoding can never change a frame a consumer
// already received. Rect is the region this update changed; a full update
// covers Image.Bounds().
type DisplayFrame struct {
	Image *image.RGBA
	Rect  image.Rectangle
}

// NewDisplayFrame snapshots source into a frame that never aliases it. A nil
// source returns nil. An empty rect defaults to the whole framebuffer.
func NewDisplayFrame(source *image.RGBA, rect image.Rectangle) *DisplayFrame {
	if source == nil {
		return nil
	}
	rect = rect.Intersect(source.Bounds())
	if rect.Empty() {
		rect = source.Bounds()
	}
	clone := image.NewRGBA(source.Bounds())
	copy(clone.Pix, source.Pix)
	return &DisplayFrame{Image: clone, Rect: rect}
}

// Clone returns an independent copy of the frame.
func (f *DisplayFrame) Clone() *DisplayFrame {
	if f == nil {
		return nil
	}
	clone := image.NewRGBA(f.Image.Bounds())
	copy(clone.Pix, f.Image.Pix)
	return &DisplayFrame{Image: clone, Rect: f.Rect}
}

// CursorShape is an immutable cursor bitmap and its hotspot. Image is a deep
// copy of the decoder's buffer.
type CursorShape struct {
	Image   *image.RGBA
	Hotspot image.Point
}

// NewCursorShape snapshots a cursor bitmap. A nil source returns nil.
func NewCursorShape(source *image.RGBA, hotspot image.Point) *CursorShape {
	if source == nil {
		return nil
	}
	clone := image.NewRGBA(source.Bounds())
	copy(clone.Pix, source.Pix)
	return &CursorShape{Image: clone, Hotspot: hotspot}
}

// DisplayEvent is one event from a pixel Session's receive direction. Kind
// selects which fields are meaningful:
//
//	Frame         Frame
//	Resize        Width, Height
//	Bell          -
//	CutText       Text
//	DesktopName   Text
//	CursorShape   Cursor
//	Error         Err
type DisplayEvent struct {
	Kind   DisplayEventKind
	Frame  *DisplayFrame
	Width  int
	Height int
	Text   string
	Cursor *CursorShape
	Err    error
}

// DisplaySession is one pixel Session. Events is drained by one consumer and
// closed exactly once when the Session ends. Every send method is safe from
// any goroutine and serializes with the others; an adapter may use
// NewSerializedDisplay to obtain that guarantee.
type DisplaySession interface {
	// Events returns the typed event stream.
	Events() <-chan DisplayEvent
	// SendKey sends one X11 keysym press or release.
	SendKey(keysym uint32, pressed bool) error
	// SendPhysicalKey sends one X11 keysym press or release with the physical
	// key number when the backend knows it. keyNumber 0 means unknown. A VNC
	// backend uses it for QEMU Extended Key Events; a backend without the
	// extension must behave exactly like SendKey.
	SendPhysicalKey(keysym uint32, keyNumber uint32, pressed bool) error
	// SendPointer sends pointer position and the RFB button mask.
	SendPointer(x, y int, buttonMask uint8) error
	// SendCutText sends clipboard text to the server.
	SendCutText(text string) error
	// RequestUpdate asks the server for a framebuffer update. An incremental
	// request asks only for changed pixels.
	RequestUpdate(incremental bool) error
	// SetDesktopSize asks the server to resize the desktop. It returns
	// ErrDesktopResizeUnsupported when the server did not negotiate the
	// extension.
	SetDesktopSize(width, height int) error
	// Close ends the Session and closes the event channel exactly once.
	Close() error
}

// DisplayProvider is implemented by a Transport that carries pixels through a
// DisplaySession instead of a byte stream. A VNC transport implements it; the
// Session then consumes the display and never starts the byte-stream loops.
// The transport owns the display: its Close must close the display, including
// the event channel, exactly once.
type DisplayProvider interface {
	Display() DisplaySession
}

// SerializedDisplay wraps an adapter so every send happens one at a time.
// Adapters need not be goroutine-safe; callers may send from any goroutine and
// the calls apply in a serialized order.
type SerializedDisplay struct {
	inner DisplaySession

	mu        sync.Mutex
	closed    bool
	closeOnce sync.Once
	closeErr  error

	// resizeMu serializes desktop-resize calls and, in Close, lets the
	// wrapper wait for a call that passed the open check before closing the
	// adapter. It is never held across the adapter's blocking wait by any
	// other send, so input keeps flowing during a resize.
	resizeMu sync.Mutex
}

// NewSerializedDisplay wraps inner. A nil inner returns nil.
func NewSerializedDisplay(inner DisplaySession) *SerializedDisplay {
	if inner == nil {
		return nil
	}
	return &SerializedDisplay{inner: inner}
}

// newDisplayEvents allocates the Session-level event stream for a pixel
// Session. A byte-stream Session has none.
func newDisplayEvents(kind Kind) chan DisplayEvent {
	if kind != KindVNC {
		return nil
	}
	return make(chan DisplayEvent, displayEventBuffer)
}

// DisplayEvents returns the Session's typed event stream for a pixel Session,
// or nil for a byte-stream Session. A surface drains this channel; it closes
// exactly once when the Session ends and stays open across reconnects.
func (s *Session) DisplayEvents() <-chan DisplayEvent {
	return s.displayEvents
}

// displayFromTransport extracts a display from a pixel transport and wraps its
// sends. A transport without a display reports nil.
func displayFromTransport(transport Transport) *SerializedDisplay {
	provider, ok := transport.(DisplayProvider)
	if !ok {
		return nil
	}
	return NewSerializedDisplay(provider.Display())
}

// Display returns the Session's pixel stream, or nil for a byte-stream
// Session. The returned value is safe to use from any goroutine. Its Events
// channel is drained by the Session; a surface reads DisplayEvents instead and
// uses this value only for the send methods.
func (s *Session) Display() DisplaySession {
	s.displayMu.RLock()
	defer s.displayMu.RUnlock()
	if s.display == nil {
		return nil
	}
	return s.display
}

// SendKey sends one X11 keysym press or release to a pixel Session.
func (s *Session) SendKey(keysym uint32, pressed bool) error {
	display := s.Display()
	if display == nil {
		return ErrNoDisplay
	}
	return display.SendKey(keysym, pressed)
}

// SendPhysicalKey sends one X11 keysym press or release with the physical key
// number when the pixel backend knows it.
func (s *Session) SendPhysicalKey(keysym uint32, keyNumber uint32, pressed bool) error {
	display := s.Display()
	if display == nil {
		return ErrNoDisplay
	}
	return display.SendPhysicalKey(keysym, keyNumber, pressed)
}

// SendPointer sends pointer position and the RFB button mask.
func (s *Session) SendPointer(x, y int, buttonMask uint8) error {
	display := s.Display()
	if display == nil {
		return ErrNoDisplay
	}
	return display.SendPointer(x, y, buttonMask)
}

// SendCutText sends clipboard text to a pixel Session.
func (s *Session) SendCutText(text string) error {
	display := s.Display()
	if display == nil {
		return ErrNoDisplay
	}
	return display.SendCutText(text)
}

// RequestUpdate asks a pixel Session's server for a framebuffer update.
func (s *Session) RequestUpdate(incremental bool) error {
	display := s.Display()
	if display == nil {
		return ErrNoDisplay
	}
	return display.RequestUpdate(incremental)
}

// SetDesktopSize asks a pixel Session's server to resize the desktop. It
// returns ErrDesktopResizeUnsupported when the extension was not negotiated.
func (s *Session) SetDesktopSize(width, height int) error {
	display := s.Display()
	if display == nil {
		return ErrNoDisplay
	}
	return display.SetDesktopSize(width, height)
}

// ByteStream reports whether the Session carries a terminal byte stream. A
// VNC Session is pixel-only: capture, broadcast, scrollback, command history,
// and terminal resize do not apply.
func (s *Session) ByteStream() bool {
	return s.spec.Kind != KindVNC
}

// setDisplay replaces the current pixel stream. The command loop is the only
// caller, on open and on reconnect.
func (s *Session) setDisplay(display *SerializedDisplay) {
	s.displayMu.Lock()
	s.display = display
	s.displayMu.Unlock()
}

// displayLoop drains one display generation's event stream. It is the adapter
// channel's only consumer: every event is forwarded to the Session stream and
// applied to the Snapshot. A closed adapter channel ends this transport
// generation; the Session stream stays open for a reconnect.
func (s *Session) displayLoop(generation int, transport Transport, display DisplaySession, genDone <-chan struct{}) {
	defer s.displayLoops.Done()
	if display == nil || display.Events() == nil {
		s.post(command{
			kind:       commandTransportEnded,
			generation: generation,
			transport:  transport,
			end:        End{Reason: EndReasonError},
		})
		return
	}
	events := display.Events()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-genDone:
			return
		case event, ok := <-events:
			if !ok {
				s.post(command{
					kind:       commandTransportEnded,
					generation: generation,
					transport:  transport,
					end:        End{Reason: EndReasonClosed},
				})
				return
			}
			s.forwardDisplayEvent(event, genDone)
			if event.Kind == DisplayEventError {
				end := End{Reason: EndReasonError}
				if event.Err != nil && permanentError(event.Err) {
					end.Reason = EndReasonAuthFailed
				}
				s.post(command{
					kind:       commandTransportEnded,
					generation: generation,
					transport:  transport,
					end:        end,
				})
				return
			}
		}
	}
}

// forwardDisplayEvent publishes one event to the Surface and applies the
// Session-visible effects. It runs on the display loop; a slow Surface blocks
// this consumer, which leaves the adapter's emitter coalescing frames, but the
// generation ending releases it so a stalled Surface cannot delay a reconnect.
func (s *Session) forwardDisplayEvent(event DisplayEvent, genDone <-chan struct{}) {
	switch event.Kind {
	case DisplayEventResize:
		s.update(func(snapshot *Snapshot) {
			snapshot.DisplayWidth, snapshot.DisplayHeight = event.Width, event.Height
		})
	case DisplayEventBell:
		if s.onBell != nil {
			s.onBell(s.id)
		}
	case DisplayEventDesktopName:
		s.update(func(snapshot *Snapshot) { snapshot.Title = event.Text })
	}
	if s.displayEvents == nil {
		return
	}
	select {
	case s.displayEvents <- event:
	case <-genDone:
		// This generation is ending; the event stream stays open for the
		// reconnect and a surface that stopped reading must not hold it up.
	case <-s.ctx.Done():
	}
}

// closeDisplayEvents closes the Session stream once, at teardown.
func (s *Session) closeDisplayEvents() {
	if s.displayEvents == nil {
		return
	}
	s.displayDone.Do(func() { close(s.displayEvents) })
}

// Events returns the adapter's event stream.
func (d *SerializedDisplay) Events() <-chan DisplayEvent { return d.inner.Events() }

// checkOpen reports ErrNoDisplay once Close has run. Callers hold d.mu, so a
// send either happens before Close or observes the closed flag, never on a
// closed adapter.
func (d *SerializedDisplay) checkOpen() error {
	if d.closed {
		return ErrNoDisplay
	}
	return nil
}

// SendKey serializes one keysym event.
func (d *SerializedDisplay) SendKey(keysym uint32, pressed bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.checkOpen(); err != nil {
		return err
	}
	return d.inner.SendKey(keysym, pressed)
}

// SendPhysicalKey serializes one keysym-with-physical-key event.
func (d *SerializedDisplay) SendPhysicalKey(keysym uint32, keyNumber uint32, pressed bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.checkOpen(); err != nil {
		return err
	}
	return d.inner.SendPhysicalKey(keysym, keyNumber, pressed)
}

// SendPointer serializes one pointer event.
func (d *SerializedDisplay) SendPointer(x, y int, buttonMask uint8) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.checkOpen(); err != nil {
		return err
	}
	return d.inner.SendPointer(x, y, buttonMask)
}

// SendCutText serializes one clipboard event.
func (d *SerializedDisplay) SendCutText(text string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.checkOpen(); err != nil {
		return err
	}
	return d.inner.SendCutText(text)
}

// RequestUpdate serializes one framebuffer-update request.
func (d *SerializedDisplay) RequestUpdate(incremental bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.checkOpen(); err != nil {
		return err
	}
	return d.inner.RequestUpdate(incremental)
}

// SetDesktopSize sends one desktop resize request. The adapter may block for
// its reply bound (several seconds) while a server is slow or silent, so the
// send mutex is deliberately NOT held across the wait: key and pointer input
// must keep flowing while a resize is outstanding. The mutex is acquired only
// to check closed; concurrent resize calls are serialized by resizeMu. Close
// marks the wrapper closed, closes the adapter (which must unblock an in-flight
// adapter wait), and then waits for any call that passed the open check to
// return before Close itself returns. A SetDesktopSize that starts after the
// closed flag is set therefore never reaches the closed adapter.
func (d *SerializedDisplay) SetDesktopSize(width, height int) error {
	d.resizeMu.Lock()
	defer d.resizeMu.Unlock()

	d.mu.Lock()
	if err := d.checkOpen(); err != nil {
		d.mu.Unlock()
		return err
	}
	d.mu.Unlock()

	return d.inner.SetDesktopSize(width, height)
}

// Close closes the adapter exactly once. Once it returns, every send reports
// ErrNoDisplay instead of reaching the closed adapter.
//
// It first sets the closed flag and closes the adapter under the send mutex, so
// no other send can overlap the adapter's Close. An in-flight SetDesktopSize
// does not hold the send mutex; the adapter's Close must unblock that wait (the
// VNC adapter closes its done channel). Close then joins the resize mutex, so
// a call that passed the open check cannot call the adapter after Close returns.
// Adapters whose blocking SetDesktopSize does not observe Close would make
// Close wait for its own timeout; that is the adapter's contract to uphold.
func (d *SerializedDisplay) Close() error {
	d.closeOnce.Do(func() {
		d.mu.Lock()
		d.closed = true
		d.closeErr = d.inner.Close()
		d.mu.Unlock()

		d.resizeMu.Lock()
		d.resizeMu.Unlock()
	})
	return d.closeErr
}

// DisplayEmitter coalesces frames and delivers typed events to one consumer.
// Frames published between ticks replace one another, so the consumer sees at
// most MaxFramesPerSecond frames but always the newest. Non-frame events pass
// through in arrival order. Close closes the event channel exactly once and
// deposits the newest pending frame first.
type DisplayEmitter struct {
	interval time.Duration
	out      chan DisplayEvent
	wake     chan struct{}
	closing  chan struct{}
	finished chan struct{}

	closeOnce sync.Once
	mu        sync.Mutex
	// closed is set under mu before closing signals run to flush, so a Publish
	// either appends its event before flush captures the queue or is rejected.
	// It closes the race where Publish checked closing, appended after flush,
	// and the loop had already exited.
	closed bool
	frame  *DisplayEvent
	events []DisplayEvent
}

// finalFlushTimeout bounds how long Close waits to deposit the newest pending
// frame into a full buffer. A consumer that is merely stalled receives it; one
// that never drains cannot deadlock Close.
const finalFlushTimeout = 100 * time.Millisecond

// NewDisplayEmitter returns an emitter at the production frame interval.
func NewDisplayEmitter() *DisplayEmitter {
	e := &DisplayEmitter{
		interval: FrameInterval,
		out:      make(chan DisplayEvent, displayEventBuffer),
		wake:     make(chan struct{}, 1),
		closing:  make(chan struct{}),
		finished: make(chan struct{}),
	}
	go e.run()
	return e
}

// Events returns the event channel the single consumer drains.
func (e *DisplayEmitter) Events() <-chan DisplayEvent { return e.out }

// Publish queues an event. A frame replaces the previous pending frame; the
// call never blocks, so the decoder goroutine is never stalled by a slow
// consumer. A Publish that races Close is either queued before the final flush
// observes it or rejected once Close has marked the emitter closed.
func (e *DisplayEmitter) Publish(event DisplayEvent) {
	if event.Kind == DisplayEventFrame && event.Frame == nil {
		return
	}
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return
	}
	if event.Kind == DisplayEventFrame {
		copied := event
		e.frame = &copied
	} else {
		e.events = append(e.events, event)
	}
	e.mu.Unlock()
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

// Close stops the emitter, deposits the newest pending frame and queued
// events, and closes the event channel. It may be called more than once.
func (e *DisplayEmitter) Close() {
	e.closeOnce.Do(func() {
		// Mark closed and signal under the queue lock. A concurrent Publish
		// therefore either appends before this point (and flush sees it) or
		// observes closed and returns without leaving an undrained event.
		e.mu.Lock()
		e.closed = true
		close(e.closing)
		e.mu.Unlock()
	})
	<-e.finished
}

func (e *DisplayEmitter) run() {
	defer close(e.finished)
	defer close(e.out)
	ticker := time.NewTicker(e.interval)
	defer ticker.Stop()
	for {
		select {
		case <-e.closing:
			e.flush()
			return
		case <-e.wake:
			e.deliverEvents()
		case <-ticker.C:
			e.deliverEvents()
			e.deliverFrame()
		}
	}
}

// deliverEvents sends queued non-frame events. A slow consumer holds the loop
// here; frames meanwhile keep being replaced by the newest snapshot.
func (e *DisplayEmitter) deliverEvents() {
	for {
		e.mu.Lock()
		if len(e.events) == 0 {
			e.mu.Unlock()
			return
		}
		event := e.events[0]
		e.events = e.events[1:]
		e.mu.Unlock()
		select {
		case e.out <- event:
		case <-e.closing:
			// Deposit it back for flush, then leave.
			e.mu.Lock()
			e.events = append([]DisplayEvent{event}, e.events...)
			e.mu.Unlock()
			return
		}
	}
}

func (e *DisplayEmitter) deliverFrame() {
	e.mu.Lock()
	event := e.frame
	e.frame = nil
	e.mu.Unlock()
	if event == nil {
		return
	}
	select {
	case e.out <- *event:
	case <-e.closing:
		e.mu.Lock()
		if e.frame == nil {
			e.frame = event
		}
		e.mu.Unlock()
	}
}

// flush deposits the latest events. The newest frame goes first: Close is
// final, so the consumer must see the last framebuffer even when the buffer is
// too full to hold everything, and a pre-frame resize or bell has already been
// applied to the Session snapshot. The final frame tries a non-blocking send
// first and then waits at most finalFlushTimeout for a slot, so a stalled
// consumer still receives the newest frame while one that never drains cannot
// deadlock Close. Non-frame events remain best-effort because frames carry the
// visual state.
func (e *DisplayEmitter) flush() {
	e.mu.Lock()
	events := e.events
	frame := e.frame
	e.events = nil
	e.frame = nil
	e.mu.Unlock()
	if frame != nil {
		select {
		case e.out <- *frame:
		default:
			timer := time.NewTimer(finalFlushTimeout)
			select {
			case e.out <- *frame:
			case <-timer.C:
			}
			timer.Stop()
		}
	}
	for _, event := range events {
		select {
		case e.out <- event:
		default:
		}
	}
}
