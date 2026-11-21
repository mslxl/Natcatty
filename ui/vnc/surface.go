// Package vnc renders a VNC display Session: it owns a bitmap updated from the
// application's display-event stream and sends basic pointer and typed-key
// input back through the Session. It implements the shell's Surface interface,
// so a VNC Host opens in an ordinary Tab.
//
// This first desktop offers the fit scaling, letterboxing, crisp pixels, and
// basic input the ticket specifies. The per-kind chrome (scale cycle, view-only
// toggle, clipboard actions, fullscreen, keyboard grab) arrives with a later
// ticket.
package vnc

import (
	"errors"
	"image"
	"sync"

	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"
)

// unicodeKeysymBase prefixes a Unicode code point as an X11 keysym. Latins 1
// and printable ASCII are their own keysyms.
const unicodeKeysymBase uint32 = 0x01000000

// Options configure a viewer Surface.
type Options struct {
	// Redraw asks for a new frame after an asynchronous display event.
	Redraw func()
}

// Option customizes Options.
type Option func(*Options)

// WithRedraw supplies the window invalidation used when a frame arrives.
func WithRedraw(fn func()) Option {
	return func(options *Options) { options.Redraw = fn }
}

// Surface is one VNC Tab's viewer. It drains the Session's display events on
// one goroutine and updates an owned bitmap on the UI thread.
type Surface struct {
	session *appsession.Session
	opts    Options

	mu sync.Mutex
	// pending is the newest frame not yet copied into the owned bitmap.
	pending *appsession.DisplayFrame
	// pixels is the viewer's own RGBA buffer. The bitmap draws it, so a later
	// decoder write can never change what is on screen.
	pixels *image.RGBA
	bitmap *ui.Bitmap
	// scaled is the nearest-neighbor image at the pane's pixel size. MyGo only
	// draws a Pixelated bitmap crisply when it grows, so the viewer resamples
	// every non-1:1 frame itself and the desktop stays sharp instead of
	// blurred.
	scaled      *ui.Bitmap
	scaledPix   *image.RGBA
	scaledSize  image.Point
	frameNumber uint64
	scaledFor   uint64
	frame       image.Rectangle
	pane        ui.Rect

	// UI-thread state.
	focused      bool
	focusPending bool
	pointerX     float32
	pointerY     float32

	done      chan struct{}
	closeOnce sync.Once
}

// New starts a viewer for session. It reports an error when the Session is not
// a pixel Session.
func New(session *appsession.Session, options ...Option) (*Surface, error) {
	if session == nil {
		return nil, errors.New("vnc viewer: session is required")
	}
	events := session.DisplayEvents()
	if events == nil {
		return nil, errors.New("vnc viewer: session has no display stream")
	}
	opts := Options{}
	for _, option := range options {
		option(&opts)
	}
	surface := &Surface{session: session, opts: opts, done: make(chan struct{})}
	go surface.consume(events)
	return surface, nil
}

// consume drains the Session's typed events. It is the stream's only consumer.
func (s *Surface) consume(events <-chan appsession.DisplayEvent) {
	for {
		select {
		case <-s.done:
			return
		case event, ok := <-events:
			if !ok {
				return
			}
			switch event.Kind {
			case appsession.DisplayEventFrame:
				if event.Frame == nil {
					continue
				}
				s.mu.Lock()
				s.pending = event.Frame
				s.mu.Unlock()
				s.requestRedraw()
			case appsession.DisplayEventResize:
				if event.Width > 0 && event.Height > 0 {
					s.requestRedraw()
				}
			}
		}
	}
}

// View shows the remote desktop without asking for the keyboard.
func (s *Surface) View(c *ui.Context) ui.Element {
	return s.view(c, false)
}

// ViewFocused shows the desktop and takes the keyboard while its Pane is the
// focused one, mirroring the terminal surface's focus behavior.
func (s *Surface) ViewFocused(c *ui.Context, focused bool) ui.Element {
	return s.view(c, focused)
}

func (s *Surface) view(c *ui.Context, focused bool) ui.Element {
	s.applyPendingFrame()
	t := c.Theme()
	caret := ui.Rect{X: s.pointerX, Y: s.pointerY, W: 1, H: 1}
	element := ui.Column(c).Fill().Clip().Background(t.Background).
		Focusable().HandleInput(s.handleInput).TextCaret(caret).
		Draw(func(p *ui.Painter, r ui.Rect) {
			s.draw(p, r)
		})
	// Marking the element as watching the pointer makes moves over it arrive
	// as InputPointerMove events, not only the presses it takes.
	_, _, _ = element.PointerPosition()
	s.focusPending = focused && !s.focused
	s.focused = focused
	if focused {
		element.AutoFocus()
		if s.focusPending {
			element.Focus()
		}
	}
	s.focusPending = false
	return element
}

// applyPendingFrame copies the newest snapshot into the owned bitmap. It runs
// on the UI thread, as drawing does.
func (s *Surface) applyPendingFrame() {
	s.mu.Lock()
	frame := s.pending
	s.pending = nil
	s.mu.Unlock()
	if frame == nil || frame.Image == nil {
		return
	}
	bounds := frame.Image.Bounds()
	size := image.Rect(0, 0, bounds.Dx(), bounds.Dy())
	if s.pixels == nil || s.pixels.Bounds() != size {
		s.pixels = image.NewRGBA(size)
		s.bitmap = ui.NewBitmap(s.pixels).Pixelated()
	}
	copy(s.pixels.Pix, frame.Image.Pix)
	s.bitmap.Update(s.pixels)
	s.frame = size
	s.frameNumber++
	s.scaled = nil
	s.scaledPix = nil
}

// draw paints the letterbox background and the bitmap in its fit rectangle.
// The pane is kept in element-local coordinates: MyGo reports pointer input
// relative to the element's box, while Draw receives its window rectangle.
func (s *Surface) draw(p *ui.Painter, r ui.Rect) {
	s.mu.Lock()
	bitmap := s.bitmap
	frame := s.frame
	s.pane = ui.Rect{W: r.W, H: r.H}
	s.mu.Unlock()
	if bitmap == nil || frame.Empty() {
		return
	}
	dest := fitRect(r, frame)
	width, height := int(dest.W+0.5), int(dest.H+0.5)
	if width <= 0 || height <= 0 {
		return
	}
	if width == frame.Dx() && height == frame.Dy() {
		p.Image(bitmap, dest, ui.FillBox)
		return
	}
	s.mu.Lock()
	if s.pixels == nil {
		s.mu.Unlock()
		return
	}
	if s.scaled == nil || s.scaledSize != image.Pt(width, height) || s.scaledFor != s.frameNumber {
		s.scaledPix = scaleNearest(s.pixels, width, height)
		s.scaled = ui.NewBitmap(s.scaledPix).Pixelated()
		s.scaledSize = image.Pt(width, height)
		s.scaledFor = s.frameNumber
	}
	scaled := s.scaled
	s.mu.Unlock()
	p.Image(scaled, ui.Rect{X: dest.X, Y: dest.Y, W: float32(width), H: float32(height)}, ui.FillBox)
}

// handleInput sends basic pointer and typed-character input. It never claims a
// key it does not map, so shortcuts and focus traversal still work.
func (s *Surface) handleInput(event ui.InputEvent) bool {
	if s.session == nil {
		return false
	}
	switch event.Kind {
	case ui.InputPointerDown:
		s.pointerX, s.pointerY = event.X, event.Y
		s.sendPointer(event.X, event.Y, buttonMask(event.Button))
		return true
	case ui.InputPointerUp:
		s.pointerX, s.pointerY = event.X, event.Y
		s.sendPointer(event.X, event.Y, 0)
		return true
	case ui.InputPointerMove:
		s.pointerX, s.pointerY = event.X, event.Y
		mask := uint8(0)
		if event.Button >= 0 {
			mask = buttonMask(event.Button)
		}
		s.sendPointer(event.X, event.Y, mask)
		return true
	case ui.InputText:
		if event.Text == "" {
			return false
		}
		for _, r := range event.Text {
			_ = s.session.SendKey(KeysymForRune(r), true)
			_ = s.session.SendKey(KeysymForRune(r), false)
		}
		return true
	}
	return false
}

// sendPointer maps a local pixel position onto the framebuffer and sends it.
func (s *Surface) sendPointer(x, y float32, mask uint8) {
	s.mu.Lock()
	pane, frame := s.pane, s.frame
	s.mu.Unlock()
	if frame.Empty() {
		return
	}
	rx, ry := remotePoint(x, y, pane, frame)
	_ = s.session.SendPointer(rx, ry, mask)
}

// buttonMask maps a MyGo pointer button onto the RFB mask. Primary is bit 0,
// secondary bit 1, middle bit 2.
func buttonMask(button int) uint8 {
	switch button {
	case 0:
		return 1
	case 1:
		return 2
	case 2:
		return 4
	default:
		return 0
	}
}

// KeysymForRune returns the X11 keysym for a typed character. Printable
// Latin-1 characters are their own keysyms; everything else uses the Unicode
// keysym range, which servers use for input-method text.
func KeysymForRune(r rune) uint32 {
	if (r >= 0x20 && r <= 0x7e) || (r >= 0xa0 && r <= 0xff) {
		return uint32(r)
	}
	return unicodeKeysymBase | uint32(r)
}

// close stops the event consumer and releases the bitmap. The shell closes the
// application Session separately, which closes the transport.
func (s *Surface) Close() {
	s.closeOnce.Do(func() {
		close(s.done)
		s.mu.Lock()
		s.pending = nil
		s.bitmap = nil
		s.pixels = nil
		s.scaled = nil
		s.scaledPix = nil
		s.mu.Unlock()
	})
}

// requestRedraw asks for the frame that applies a new display event.
func (s *Surface) requestRedraw() {
	if s.opts.Redraw != nil {
		s.opts.Redraw()
	}
}

type surface interface {
	View(c *ui.Context) ui.Element
	Close()
}

var _ surface = (*Surface)(nil)
