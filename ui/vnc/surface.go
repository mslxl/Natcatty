// Package vnc renders a VNC display Session: it owns a bitmap updated from the
// application's display-event stream and sends keysym-mode keyboard input and
// pointer input back through the Session. It implements the shell's Surface
// interface, so a VNC Host opens in an ordinary Tab.
//
// Keyboard input follows the mapping research: the physical key identity is
// preferred, the logical key is the fallback, shifted keysyms are sent at
// send-time, and modifiers are synthesized because MyGo reports them as bits
// rather than key events. The local input method is disabled and text-input
// events are ignored, so only the guest's own layout composes characters.
// When the server negotiates QEMU Extended Key Events the Session uses them;
// otherwise it sends plain KeyEvents with identical guest behavior.
//
// This desktop offers fit scaling, letterboxing, crisp pixels, and the input
// mapping the ticket specifies. Its per-kind chrome adds the scale cycle,
// view-only toggle, bidirectional clipboard, Ctrl+Alt+Del, fullscreen, and
// keyboard grab; the shell renders that row in the main window, a detached
// window, and the Quake panel wherever a VNC Pane appears.
package vnc

import (
	"errors"
	"image"
	"runtime"
	"sync"

	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model/rfb"
	modelvnc "github.com/mslxl/natcatty/domain/model/vnc"
	"github.com/mslxl/natcatty/ui/locales"
	sessionui "github.com/mslxl/natcatty/ui/session"
)

// unicodeKeysymBase prefixes a Unicode code point as an X11 keysym. Latins 1
// and printable ASCII are their own keysyms.
const unicodeKeysymBase uint32 = 0x01000000

// clipDragThreshold is how far the pointer must move while held in clip mode
// before a primary press becomes a viewport drag. A shorter gesture is a
// click, which still reaches the guest.
const clipDragThreshold float32 = 3

// ErrViewOnly reports that a guest-directed write was refused because the
// viewer is in view-only mode. The chrome turns it into an explicit localized
// notice instead of silently dropping the action.
var ErrViewOnly = errors.New("vnc viewer: view-only")

// Options configure a viewer Surface.
type Options struct {
	// Redraw asks for a new frame after an asynchronous display event.
	Redraw func()
	// Fullscreen toggles the hosting window's fullscreen state. It is an
	// injectable callback so offscreen tests never open a real window.
	Fullscreen func()
	// Bell receives a remote bell. A nil callback leaves the app's existing
	// Session bell plumbing as the only cue.
	Bell func()
}

// Option customizes Options.
type Option func(*Options)

// WithRedraw supplies the window invalidation used when a frame arrives.
func WithRedraw(fn func()) Option {
	return func(options *Options) { options.Redraw = fn }
}

// WithFullscreen supplies the window fullscreen toggle the toolbar uses.
func WithFullscreen(fn func()) Option {
	return func(options *Options) { options.Fullscreen = fn }
}

// WithBell supplies a callback for a remote bell. The Session's own bell hook
// still marks the Tab active; this callback is for a viewer-owned cue.
func WithBell(fn func()) Option {
	return func(options *Options) { options.Bell = fn }
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
	// cursor is the newest CursorWithAlpha shape. A nil shape is a hidden
	// cursor or one the server has not sent yet; both draw nothing.
	cursor *appsession.CursorShape
	// cursorFor and cursorBitmap cache the drawn shape. A CursorWithAlpha
	// event allocates a new shape, so pointer identity is the cache key.
	cursorFor    *appsession.CursorShape
	cursorBitmap *ui.Bitmap
	// cursorScaled caches a scaled cursor for the current fit factor, so a
	// pointer move does not resample the shape every frame.
	cursorScaled     *ui.Bitmap
	cursorScaledFor  *appsession.CursorShape
	cursorScaledSize image.Point
	cursorMode       modelvnc.CursorMode
	scaleMode        modelvnc.FitMode
	// panX and panY are the clip viewport's offset from the centered 1:1
	// position. They are clamped against the pane and frame on every use.
	panX, panY float32
	// remoteText is the last ServerCutText decoded from Latin-1, and
	// clipboardPending asks the UI thread to put it on the local clipboard.
	remoteText       string
	hasRemoteText    bool
	clipboardPending bool
	// clipboardResult is the most recent toolbar clipboard outcome, shown as
	// a localized notice. A nil value means no action has run.
	clipboardResult *ClipboardResult
	// viewOnlyNotice is the localized explanation for a chrome action that
	// view-only refused. It is cleared when the viewer leaves view-only.
	viewOnlyNotice string
	// resizeUnsupported latches after the server refuses or never negotiates
	// desktop resizing, so the viewer stops asking and keeps local scaling.
	resizeUnsupported bool
	resizeInFlight    bool
	// layoutRequested is the screen layout of the outstanding request and
	// layoutDesktop its desktop size. The pair suppresses duplicate requests:
	// it stays set while a forwarded (pending) request may still complete, and
	// after a timeout it acts as a backoff for that layout. A resize event of
	// the same desktop size, or a non-retryable reply, clears it.
	layoutRequested []appsession.DisplayScreen
	layoutDesktop   image.Point
	// layoutPending is the newest layout the Tab asked for while a request was
	// in flight. Bursts of layout passes (a window resize draws one Pane after
	// another) collapse into one follow-up request instead of a queue.
	layoutPending        []appsession.DisplayScreen
	layoutPendingDesktop image.Point
	// paneRect is the Pane rectangle of the last draw, in window pixels, and
	// geometryDirty asks the next draw to tell the Tab about it even when the
	// rectangle did not move (the remote's own layout changed, or the first
	// frame arrived after the last report).
	paneRect      ui.Rect
	geometryDirty bool
	// geometryFn is the Tab's listener, installed by the shell. It runs on the
	// UI thread during draw.
	geometryFn func()

	// screens is the newest server-reported layout and desktop is the full
	// framebuffer extent it describes. A Pane with a screen index crops its
	// screen out of every frame, so the bitmap, the scale modes, and pointer
	// mapping all treat that screen as the desktop. An empty layout means the
	// whole framebuffer is the one screen.
	screens []appsession.DisplayScreen
	desktop image.Point
	// screenIndex is the remote screen this Pane shows: 0 is the whole desktop
	// on a single-screen server and the first screen of a multi-screen layout.
	screenIndex int
	// screenOrigin is the framebuffer origin of the screen drawn last; pointer
	// positions are offset by it before they reach the guest.
	screenOrigin image.Point
	// multiScreenRefused latches after the server answered a split request
	// without adopting the layout, so the Pane stops offering Split and the
	// chrome explains why. A Pane never shows the same desktop twice.
	multiScreenRefused bool

	// hibernated replaces the live desktop with a resumable placeholder while
	// the Session and its transport stay alive. pixels is kept: it is the
	// last frame Resume shows before any new update arrives.
	hibernated bool

	// paneActions are the Pane commands the shell supplies for this Pane.
	// They carry split, detach, rename, close, and manual reconnect, so the
	// VNC pane behaves like a terminal pane.
	paneActions sessionui.PaneActions
	// renaming and renameValue drive the Pane rename dialog.
	renaming    bool
	renameValue string

	// UI-thread state.
	focused      bool
	focusPending bool
	// viewOnly suppresses keys and pointer writes while frames and clipboard
	// pull keep working. keyboardGrab lets the guest keep chords the app would
	// otherwise dispatch as accelerators.
	viewOnly     bool
	keyboardGrab bool
	// panning, panStartX/Y, panStartPanX/Y track a clip-mode drag. The pointer
	// position always updates for the local cursor even while panning.
	panning                    bool
	panStartX, panStartY       float32
	panStartPanX, panStartPanY float32
	// panPending is a primary press in clip mode that has not yet moved past
	// clipDragThreshold. Inside the threshold the gesture is a click and is
	// replayed as a press and release; past it the gesture becomes panning and
	// the guest sees no button at all. panPressX/Y is where the press landed,
	// because MyGo delivers it with its own coordinates, not the last hover.
	// panMods carries the modifiers the click was pressed with, because the
	// press is deferred until release.
	panPending           bool
	panPressX, panPressY float32
	panMods              ui.Modifiers
	// pointerSeen keeps the local cursor hidden until the pointer has moved,
	// so it cannot appear at the pane origin on the first frame.
	pointerSeen bool
	// locale is the language of the last frame, used for clipboard results.
	locale string
	// windowFocused is the window's keyboard focus as observed last frame.
	// MyGo clears it on SurfaceBlur without delivering key-up events, so the
	// viewer watches the true-to-false transition and releases held input.
	windowFocused bool
	pointerX      float32
	pointerY      float32

	// Keyboard state, touched only from HandleInput on the UI thread.
	//
	// heldKeys is the noVNC down-list: a key-up sends the keysym and physical
	// number recorded at key-down, so the pair always matches even if the
	// layout or modifier state changed in between. heldMods is the modifier
	// state the guest has been told about; MyGo reports modifiers as bits and
	// never as key events, so the viewer synthesizes the keysym presses and
	// releases itself.
	heldKeys map[rfb.KeyCode]heldKey
	heldMods ui.Modifiers
	// downMask combines every pointer button currently held. RFB pointer
	// events carry the whole mask, so a drag with two buttons held must keep
	// both bits set.
	downMask uint8
	// macModifiers applies noVNC's macOS swap: Command reaches the guest as
	// Alt and Option as AltGr (ISO_Level3_Shift).
	macModifiers bool

	done      chan struct{}
	closeOnce sync.Once
}

// heldKey is one key's wire identity, recorded at key-down for its key-up.
type heldKey struct {
	keysym    uint32
	keyNumber uint32
}

// modifierOrder is the deterministic order modifier keysyms are pressed and
// the reverse order they are released.
var modifierOrder = []ui.Modifiers{ui.Shift, ui.Ctrl, ui.Alt, ui.Super}

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
	config := session.VNCConfiguration()
	surface := &Surface{
		session:      session,
		opts:         opts,
		macModifiers: runtime.GOOS == "darwin",
		scaleMode:    modelvnc.NormalizeFitMode(config.FitMode),
		cursorMode:   modelvnc.NormalizeCursorMode(config.CursorMode),
		viewOnly:     config.ViewOnly,
		screenIndex:  session.VNCScreen(),
		locale:       "en",
		done:         make(chan struct{}),
	}
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
				// The Session ended. The pane may now have to show its
				// failed state; ask for the frame that draws it.
				s.requestRedraw()
				return
			}
			switch event.Kind {
			case appsession.DisplayEventFrame:
				if event.Frame == nil {
					continue
				}
				s.mu.Lock()
				s.pending = event.Frame
				hibernated := s.hibernated
				s.mu.Unlock()
				if !hibernated {
					s.requestRedraw()
				}
			case appsession.DisplayEventResize:
				if event.Width > 0 && event.Height > 0 {
					s.mu.Lock()
					s.desktop = image.Pt(event.Width, event.Height)
					s.screens = append([]appsession.DisplayScreen(nil), event.Screens...)
					// The server reached the desktop size we last asked for. A
					// pending (status 4) request is complete and must not be
					// re-sent.
					if s.layoutDesktop == image.Pt(event.Width, event.Height) {
						s.layoutRequested = nil
						s.layoutDesktop = image.Point{}
					}
					// The remote's layout changed, so the Tab may need to ask
					// for it again: its Panes' rectangles are unchanged, but a
					// re-drive is cheap and converges when another client moved
					// the desktop.
					s.geometryDirty = true
					s.mu.Unlock()
					s.requestRedraw()
				}
			case appsession.DisplayEventCursorShape:
				s.mu.Lock()
				s.cursor = event.Cursor
				// The shape changed, so the cached bitmaps describe the old
				// one and must not be drawn again.
				s.cursorFor = nil
				s.cursorBitmap = nil
				s.cursorScaled = nil
				s.cursorScaledFor = nil
				s.mu.Unlock()
				s.requestRedraw()
			case appsession.DisplayEventCutText:
				// The wire text is Latin-1; decode it once so both the local
				// clipboard and the toolbar's pull action see the same text.
				s.mu.Lock()
				s.remoteText = DecodeLatin1(event.Text)
				s.hasRemoteText = true
				s.clipboardPending = true
				s.mu.Unlock()
				s.requestRedraw()
			case appsession.DisplayEventBell:
				if s.opts.Bell != nil {
					s.opts.Bell()
				}
				// The Session's own bell hook marks the Tab; a redraw makes
				// the new badge visible on the next frame.
				s.requestRedraw()
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
	s.locale = c.Locale()
	m := locales.In(c.Locale())
	t := c.Theme()
	snapshot := s.session.Snapshot()
	// A stopped Session and a hibernated one render no desktop, so neither can
	// own the keyboard. Release whatever the guest still believes held before
	// hiding the live surface.
	switch {
	case connectionFailed(snapshot):
		s.dropFocus()
		element := s.failedView(c, m, snapshot)
		if s.renaming {
			s.renameDialog(c, m, t)
		}
		return element
	case s.Hibernated():
		s.dropFocus()
		element := s.hibernatedView(c, m)
		if s.renaming {
			s.renameDialog(c, m, t)
		}
		return element
	}
	s.applyPendingFrame()
	s.syncRemoteClipboard(c)
	caret := ui.Rect{X: s.pointerX, Y: s.pointerY, W: 1, H: 1}
	element := ui.Column(c).Fill().Clip().Background(t.Background).
		Focusable().HandleInput(s.handleInput).TextCaret(caret).
		// Pure keysym mode: the guest's own layout composes characters, so
		// the local input method must not intercept keys. The viewer ignores
		// the InputText events that follow.
		InputMethod(false).
		Draw(func(p *ui.Painter, r ui.Rect) {
			s.draw(p, r)
		})
	// Marking the element as watching the pointer makes moves over it arrive
	// as InputPointerMove events, not only the presses it takes.
	_, _, _ = element.PointerPosition()
	if s.focused && !focused {
		// The pane lost the keyboard; the guest must not keep keys held.
		s.releaseHeldKeys()
	}
	s.focusPending = focused && !s.focused
	s.focused = focused
	if focused {
		element.AutoFocus()
		if s.focusPending {
			element.Focus()
		}
	}
	s.focusPending = false
	// MyGo's SurfaceBlur clears the window focus but delivers no key-up events,
	// so Cmd-Tab away with a key held would leave it down in the guest. Focused
	// reports focused id && window focus; reading it here both observes the
	// window focus (independent of whether the Pane itself is focused) and makes
	// MyGo request the frame that reports a change. On a true-to-false
	// transition, release every held key, synthesized modifier and pointer
	// button. Tracking the previous state keeps startup (false to false) from
	// sending a burst of key-ups.
	windowFocused := element.Focused()
	if s.windowFocused && !windowFocused {
		s.releaseHeldKeys()
	}
	s.windowFocused = windowFocused
	s.connectionOverlay(c, m, t, snapshot, element)
	if s.renaming {
		s.renameDialog(c, m, t)
	}
	return element
}

// applyPendingFrame copies the newest snapshot's screen into the owned bitmap.
// It runs on the UI thread, as drawing does. A Pane that shows one screen of a
// multi-screen desktop copies only that screen, so every later stage — the
// bitmap, the scale modes, the cursor, and pointer mapping — works in screen
// coordinates and the Pane never shows a neighbour's pixels.
func (s *Surface) applyPendingFrame() {
	s.mu.Lock()
	frame := s.pending
	s.pending = nil
	s.mu.Unlock()
	if frame == nil || frame.Image == nil {
		return
	}
	bounds := frame.Image.Bounds()
	s.mu.Lock()
	screen := s.screenRectLocked(bounds)
	s.mu.Unlock()
	size := image.Rect(0, 0, screen.Dx(), screen.Dy())
	// Hibernation keeps pixels but drops the drawn bitmap, so a pending frame
	// that arrived while hibernated must recreate it before Update.
	if s.pixels == nil || s.pixels.Bounds() != size || s.bitmap == nil {
		s.pixels = image.NewRGBA(size)
		s.bitmap = ui.NewBitmap(s.pixels).Pixelated()
	}
	copyScreenPixels(s.pixels, frame.Image, screen)
	s.bitmap.Update(s.pixels)
	s.mu.Lock()
	s.frame = size
	s.screenOrigin = screen.Min
	s.frameNumber++
	if s.frameNumber == 1 {
		// The first applied frame is what makes a layout request meaningful:
		// an earlier draw had no frame to show, and the Tab may already have
		// asked while this Pane could not answer. Report the geometry again.
		s.geometryDirty = true
	}
	s.mu.Unlock()
	s.scaled = nil
	s.scaledPix = nil
}

// screenRectLocked returns the framebuffer rectangle this Pane displays,
// clamped to bounds. A Pane whose index does not name a reported screen — a
// restored Pane after the server shrank its layout, or a single-screen desktop
// — shows the whole framebuffer. The caller holds s.mu.
func (s *Surface) screenRectLocked(bounds image.Rectangle) image.Rectangle {
	if s.screenIndex < 0 || s.screenIndex >= len(s.screens) {
		return bounds
	}
	rect := s.screens[s.screenIndex].Rect.Intersect(bounds)
	if rect.Empty() {
		return bounds
	}
	return rect
}

// copyScreenPixels copies the screen rectangle of src into dst, whose origin is
// (0,0). Both images use the RGBA layout, so a row is one contiguous copy.
func copyScreenPixels(dst, src *image.RGBA, screen image.Rectangle) {
	width := min(screen.Dx(), dst.Bounds().Dx())
	for y := 0; y < screen.Dy() && y < dst.Bounds().Dy(); y++ {
		srcOffset := src.PixOffset(screen.Min.X, screen.Min.Y+y)
		dstOffset := dst.PixOffset(0, y)
		copy(dst.Pix[dstOffset:dstOffset+width*4], src.Pix[srcOffset:srcOffset+width*4])
	}
}

// ScreenGeometry reports the screen this Pane shows and its rectangle from the
// last draw, in window pixels. The Tab uses it to lay the desktop's screens out
// to match the Panes. ok is false before the first draw.
func (s *Surface) ScreenGeometry() (int, image.Rectangle, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rect := s.paneRect
	width, height := int(rect.W+0.5), int(rect.H+0.5)
	if width <= 0 || height <= 0 {
		return s.screenIndex, image.Rectangle{}, false
	}
	x, y := int(rect.X+0.5), int(rect.Y+0.5)
	return s.screenIndex, image.Rect(x, y, x+width, y+height), true
}

// SetScreenGeometryListener installs the Tab's callback. Draw invokes it on the
// UI thread when the Pane rectangle changes or the remote reports a layout, so
// the Tab can re-drive the desktop from every Pane's geometry.
func (s *Surface) SetScreenGeometryListener(fn func()) {
	s.mu.Lock()
	s.geometryFn = fn
	s.mu.Unlock()
}

// LayoutScreens asks the server for the screen layout the Tab derived from its
// Panes. The Tab calls it on the UI thread when a Pane moves or resizes; the
// request runs off the UI thread so drawing never waits on the network.
//
// The remote has one desktop, so the Tab is the only driver: a Pane never asks
// for a size of its own, which is what would make two Panes compete for the
// same desktop size.
func (s *Surface) LayoutScreens(screens []sessionui.ScreenRect) bool {
	layout, desktop, ok := displayLayout(screens)
	if !ok {
		return false
	}
	s.mu.Lock()
	if s.viewOnly || s.hibernated || s.resizeUnsupported || s.frameNumber == 0 {
		s.mu.Unlock()
		return false
	}
	if s.resizeInFlight {
		// Coalesce: the request in flight describes an older layout, and this
		// one is newer than anything queued. Replacing the pending target keeps
		// one follow-up request per burst. A repeat of the outstanding layout
		// needs no follow-up at all.
		if !s.layoutIsRequestedLocked(layout, desktop) {
			s.layoutPending, s.layoutPendingDesktop = layout, desktop
		}
		s.mu.Unlock()
		return true
	}
	launch := s.startLayoutLocked(layout, desktop)
	s.mu.Unlock()
	if launch {
		go s.requestScreenLayout(layout)
	}
	return true
}

// displayLayout converts the Tab's screen rectangles into the display port's
// layout and its desktop size. ok is false for a rectangle the wire cannot
// carry, so a malformed layout never reaches the server.
func displayLayout(screens []sessionui.ScreenRect) ([]appsession.DisplayScreen, image.Point, bool) {
	if len(screens) == 0 {
		return nil, image.Point{}, false
	}
	layout := make([]appsession.DisplayScreen, 0, len(screens))
	desktop := image.Point{}
	for _, screen := range screens {
		rect := screen.Rect
		if rect.Empty() || rect.Min.X < 0 || rect.Min.Y < 0 ||
			rect.Max.X > 0xffff || rect.Max.Y > 0xffff {
			return nil, image.Point{}, false
		}
		layout = append(layout, appsession.DisplayScreen{Rect: rect})
		desktop.X = max(desktop.X, rect.Max.X)
		desktop.Y = max(desktop.Y, rect.Max.Y)
	}
	return layout, desktop, true
}

// startLayoutLocked records layout as the outstanding request and reports
// whether the caller must launch it. The caller holds s.mu. Nothing is launched
// before the first frame: ExtendedDesktopSize support is learned from the
// server's initial full update, and asking earlier would mistake "not
// negotiated yet" for unsupported.
func (s *Surface) startLayoutLocked(layout []appsession.DisplayScreen, desktop image.Point) bool {
	if s.viewOnly || s.hibernated || s.resizeUnsupported || s.resizeInFlight || s.frameNumber == 0 {
		return false
	}
	if s.layoutMatchesLocked(layout, desktop) || s.layoutIsRequestedLocked(layout, desktop) {
		return false
	}
	s.layoutRequested, s.layoutDesktop = layout, desktop
	s.resizeInFlight = true
	return true
}

// layoutMatchesLocked reports whether the server already reports this layout.
// The caller holds s.mu.
func (s *Surface) layoutMatchesLocked(layout []appsession.DisplayScreen, desktop image.Point) bool {
	if s.desktop != desktop || len(s.screens) != len(layout) {
		return false
	}
	for i := range layout {
		if s.screens[i].Rect != layout[i].Rect {
			return false
		}
	}
	return true
}

// layoutIsRequestedLocked reports whether this exact layout is already the one
// outstanding or last asked for. The caller holds s.mu.
func (s *Surface) layoutIsRequestedLocked(layout []appsession.DisplayScreen, desktop image.Point) bool {
	if s.layoutDesktop != desktop || len(s.layoutRequested) != len(layout) {
		return false
	}
	for i := range layout {
		if s.layoutRequested[i].Rect != layout[i].Rect {
			return false
		}
	}
	return true
}

// requestScreenLayout sends one layout and folds its outcome into the viewer
// state. Any other error is left to the Session, which owns reconnect and
// teardown.
func (s *Surface) requestScreenLayout(layout []appsession.DisplayScreen) {
	err := s.session.SetScreenLayout(layout)
	s.applyLayoutResult(err)
	s.requestRedraw()
}

// applyLayoutResult records the result of one layout request.
//
//   - Success clears the request; the server adopted the layout and its next
//     report makes the Panes crop their new screens.
//   - A pending (forwarded) status is not a failure: the server may complete it
//     later, so the layout stays requested to avoid spamming, and local scaling
//     is not latched.
//   - A refusal, or a server that did not adopt the layout, latches the
//     local-scaling fallback: the Panes keep their screens and letterbox.
//   - A timeout keeps the layout as a backoff, so a silent server cannot make
//     every layout pass wait again.
func (s *Surface) applyLayoutResult(err error) {
	s.mu.Lock()
	s.resizeInFlight = false
	switch {
	case errors.Is(err, appsession.ErrDesktopResizeTimeout):
		// Keep the layout as the backoff.
	case errors.Is(err, appsession.ErrDesktopResizePending):
		// Keep the layout; the server may complete it asynchronously.
	case errors.Is(err, appsession.ErrDesktopResizeUnsupported),
		errors.Is(err, appsession.ErrDesktopResizeRefused),
		errors.Is(err, appsession.ErrMultiScreenUnsupported):
		s.layoutRequested, s.layoutDesktop = nil, image.Point{}
		s.resizeUnsupported = true
	default:
		// Success, and anything else that did not adopt the layout yet: the
		// server's next report decides whether it is in place.
		s.layoutRequested, s.layoutDesktop = nil, image.Point{}
	}
	pending, pendingDesktop := s.layoutPending, s.layoutPendingDesktop
	s.layoutPending, s.layoutPendingDesktop = nil, image.Point{}
	launch := pending != nil && s.startLayoutLocked(pending, pendingDesktop)
	s.mu.Unlock()
	if launch {
		go s.requestScreenLayout(pending)
	}
}

// LocalScaling reports that the server cannot resize its desktop, so the
// viewer letterboxes and scales the framebuffer locally.
func (s *Surface) LocalScaling() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.resizeUnsupported
}

// ResizeFallbackNotice is the user-facing message for LocalScaling, or "" when
// remote resizing works (or has not been attempted yet). The Pane chrome shows
// it as a badge beside the viewer controls.
func (s *Surface) ResizeFallbackNotice() string {
	if !s.LocalScaling() {
		return ""
	}
	return locales.SessionVNCResizeFallback()
}

// Cursor returns the latest cursor shape the server sent, or nil when no shape
// has arrived. The viewer draws it in local cursor mode.
func (s *Surface) Cursor() *appsession.CursorShape {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cursor
}

// draw paints the desktop in its scale mode and, when the Host uses the local
// cursor, the server's cursor shape at the pointer. The pane is kept in
// element-local coordinates: MyGo reports pointer input relative to the
// element's box, while Draw receives its window rectangle.
func (s *Surface) draw(p *ui.Painter, r ui.Rect) {
	s.mu.Lock()
	bitmap := s.bitmap
	frame := s.frame
	if s.paneRect != r {
		s.paneRect = r
		s.geometryDirty = true
	}
	s.pane = ui.Rect{W: r.W, H: r.H}
	dirty, listener := s.geometryDirty, s.geometryFn
	s.geometryDirty = false
	s.mu.Unlock()
	// The Tab lays the desktop's screens out from every Pane's rectangle, so a
	// moved or resized Pane, or a new remote layout, is what starts a request.
	if dirty && listener != nil {
		listener()
	}
	if bitmap == nil || frame.Empty() {
		return
	}
	mode := s.scaleMode
	// Draw uses the element's window rectangle; input and the cursor's remote
	// position use the element-local pane kept above.
	dest := contentRect(r, frame, mode, s.panX, s.panY)
	width, height := int(dest.W+0.5), int(dest.H+0.5)
	if width <= 0 || height <= 0 {
		return
	}
	switch {
	case width == frame.Dx() && height == frame.Dy():
		// 1:1, or a fit that happens to be exact: the owned bitmap is drawn
		// directly and the pane's Clip keeps any 1:1 overflow out of view.
		p.Image(bitmap, dest, ui.FillBox)
	case mode != modelvnc.Fit:
		// A non-fit mode never resamples: one remote pixel is one pane pixel.
		p.Image(bitmap, dest, ui.FillBox)
	default:
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
	s.drawCursor(p, frame, dest)
}

// drawCursor paints the server's cursor shape at the pointer's framebuffer
// position. It draws nothing for a server cursor, a hidden cursor, a shape
// that has not arrived, or before the pointer has moved.
func (s *Surface) drawCursor(p *ui.Painter, frame image.Rectangle, content ui.Rect) {
	if s.cursorMode == modelvnc.CursorServer || !s.pointerSeen {
		return
	}
	s.mu.Lock()
	shape := s.cursor
	s.mu.Unlock()
	if shape == nil || shape.Image == nil || shape.Image.Bounds().Empty() {
		return
	}
	// The hotspot sits at the remote pixel under the pointer, so the shape's
	// origin is the pointer's framebuffer pixel minus the hotspot.
	rx, ry := remotePointFor(s.pointerX, s.pointerY, s.pane, frame, s.scaleMode, s.panX, s.panY)
	scaleX := content.W / float32(frame.Dx())
	scaleY := content.H / float32(frame.Dy())
	if scaleX <= 0 || scaleY <= 0 {
		return
	}
	originX := content.X + (float32(rx)-float32(shape.Hotspot.X))*scaleX
	originY := content.Y + (float32(ry)-float32(shape.Hotspot.Y))*scaleY
	width := float32(shape.Image.Bounds().Dx()) * scaleX
	height := float32(shape.Image.Bounds().Dy()) * scaleY
	if width <= 0 || height <= 0 {
		return
	}
	bitmap := s.cursorBitmapFor(shape, int(width+0.5), int(height+0.5), scaleX == 1 && scaleY == 1)
	if bitmap == nil {
		return
	}
	p.Image(bitmap, ui.Rect{X: originX, Y: originY, W: width, H: height}, ui.FillBox)
}

// cursorBitmapFor returns a cached drawn cursor at the requested size. A 1:1
// cursor uses the server bitmap directly; a scaled one is resampled nearest
// like the desktop so it stays crisp.
func (s *Surface) cursorBitmapFor(shape *appsession.CursorShape, width, height int, exact bool) *ui.Bitmap {
	if width <= 0 || height <= 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if exact {
		if s.cursorFor != shape || s.cursorBitmap == nil {
			s.cursorBitmap = ui.NewBitmap(shape.Image).Pixelated()
			s.cursorFor = shape
		}
		return s.cursorBitmap
	}
	if s.cursorScaledFor != shape || s.cursorScaled == nil || s.cursorScaledSize != image.Pt(width, height) {
		s.cursorScaled = ui.NewBitmap(scaleNearest(shape.Image, width, height)).Pixelated()
		s.cursorScaledFor = shape
		s.cursorScaledSize = image.Pt(width, height)
	}
	return s.cursorScaled
}

// handleInput sends keyboard and pointer input in pure keysym mode. It never
// claims an event it does not map, so shortcuts and focus traversal still work.
// Text-input and composition events are deliberately ignored: their text comes
// from the local layout or input method, and forwarding it would type twice
// over the guest's own handling.
//
// View-only writes nothing. It still tracks the pointer locally so the cursor
// and a clip pan keep working, and it lets key and text events fall through to
// the app's normal handling. In clip mode a primary drag pans the viewport
// instead of reaching the guest; every other pointer event is inverted with
// the pan offset.
func (s *Surface) handleInput(event ui.InputEvent) bool {
	if s.session == nil {
		return false
	}
	switch event.Kind {
	case ui.InputKeyDown:
		if s.viewOnly {
			return false
		}
		return s.handleKeyDown(event)
	case ui.InputKeyUp:
		if s.viewOnly {
			return false
		}
		return s.handleKeyUp(event)
	case ui.InputPointerDown:
		s.pointerX, s.pointerY, s.pointerSeen = event.X, event.Y, true
		// In clip mode a primary press starts a gesture that is either a
		// viewport drag or a click; neither is written until it is resolved.
		// Panning stays local even in view-only mode.
		if s.beginClipGesture(event) {
			s.requestRedraw()
			return true
		}
		// A second button during a pending clip gesture means the user is
		// clicking, not panning. Drop the unrealized primary press so the
		// button that follows is handled normally.
		s.cancelPendingPan()
		if s.viewOnly {
			return true
		}
		next, ok := applyButton(s.downMask, event.Button, true)
		if !ok {
			return false
		}
		// A Shift- or Ctrl-click carries its modifier to the guest. The
		// modifiers are scoped to the click (released on button-up) because
		// MyGo never reports their own key-up.
		s.syncModifiers(event.Mods)
		s.downMask = next
		s.sendPointer(event.X, event.Y, s.downMask)
		s.requestRedraw()
		return true
	case ui.InputPointerUp:
		s.pointerX, s.pointerY, s.pointerSeen = event.X, event.Y, true
		if s.panPending || s.panning {
			s.finishClipGesture(event)
			s.requestRedraw()
			return true
		}
		if s.viewOnly {
			return true
		}
		next, ok := applyButton(s.downMask, event.Button, false)
		if !ok {
			return false
		}
		s.downMask = next
		s.sendPointer(event.X, event.Y, s.downMask)
		s.releaseModifiers()
		s.requestRedraw()
		return true
	case ui.InputPointerMove:
		s.pointerX, s.pointerY, s.pointerSeen = event.X, event.Y, true
		if s.panPending || s.panning {
			s.moveClipGesture(event.X, event.Y)
			s.requestRedraw()
			return true
		}
		if s.viewOnly {
			s.requestRedraw()
			return true
		}
		// A move during a drag carries the held button; the mask is the full
		// set of buttons down, so a second button pressed mid-drag stays set.
		if event.Button >= 0 {
			if next, ok := applyButton(s.downMask, event.Button, true); ok {
				s.downMask = next
			}
		}
		// Hover still sends a position update with an empty mask so the
		// remote cursor tracks the pointer.
		s.sendPointer(event.X, event.Y, s.downMask)
		// The local cursor must follow a move even when no pointer event
		// changes anything the guest shows.
		s.requestRedraw()
		return true
	case ui.InputScroll:
		if s.viewOnly {
			return true
		}
		return s.handleScroll(event)
	case ui.InputText, ui.InputCompose, ui.InputCommand:
		return false
	}
	return false
}

// beginClipGesture claims a clip-mode primary press. It records the press
// point and the pan origin but writes nothing: the gesture resolves into
// either a click (released inside clipDragThreshold) or a viewport drag
// (moved past it). It reports whether the event became that gesture.
func (s *Surface) beginClipGesture(event ui.InputEvent) bool {
	if s.scaleMode != modelvnc.Clip || event.Kind != ui.InputPointerDown || event.Button != 0 {
		return false
	}
	s.mu.Lock()
	frame := s.frame
	s.mu.Unlock()
	if frame.Empty() {
		return false
	}
	s.panPending = true
	s.panning = false
	s.panStartX, s.panStartY = event.X, event.Y
	s.panPressX, s.panPressY = event.X, event.Y
	s.panStartPanX, s.panStartPanY = s.panX, s.panY
	s.panMods = event.Mods
	return true
}

// cancelPendingPan forgets an unresolved clip press without writing anything.
func (s *Surface) cancelPendingPan() {
	s.panPending = false
	s.panning = false
}

// moveClipGesture advances a clip gesture. Before the threshold it only
// updates the local cursor; past it the gesture becomes a drag and moves the
// viewport by the total delta from the press. It never sends a pointer button.
func (s *Surface) moveClipGesture(x, y float32) {
	if s.panPending {
		dx, dy := x-s.panStartX, y-s.panStartY
		distance := dx*dx + dy*dy
		if distance > clipDragThreshold*clipDragThreshold {
			s.panPending = false
			s.panning = true
		}
	}
	if s.panning {
		s.panViewport(x, y)
	}
}

// finishClipGesture resolves a released clip gesture. A drag ends without any
// wire write. A press that never passed the threshold is a click: the viewer
// now sends the ordinary primary press where it actually landed and the release
// at the release point (and any modifiers), so a plain click in clip mode still
// reaches the guest. View-only keeps the click local and writes nothing.
func (s *Surface) finishClipGesture(event ui.InputEvent) {
	wasPanning := s.panning
	s.panPending = false
	s.panning = false
	if wasPanning || s.viewOnly {
		return
	}
	s.syncModifiers(s.panMods)
	s.sendPointer(s.panPressX, s.panPressY, 1)
	s.sendPointer(event.X, event.Y, 0)
	s.releaseModifiers()
}

// panViewport moves the clip viewport by the drag delta and clamps it so no
// edge shows a gap.
func (s *Surface) panViewport(x, y float32) {
	s.mu.Lock()
	frame, pane := s.frame, s.pane
	s.mu.Unlock()
	if frame.Empty() {
		return
	}
	s.panX = clampPan(s.panStartPanX+(x-s.panStartX), float32(frame.Dx()), pane.W)
	s.panY = clampPan(s.panStartPanY+(y-s.panStartY), float32(frame.Dy()), pane.H)
}

// handleKeyDown resolves the physical key, tells the guest about modifier
// changes, then sends the key. Auto-repeat reuses the recorded identity.
func (s *Surface) handleKeyDown(event ui.InputEvent) bool {
	if event.Repeat {
		if _, held, ok := s.held(event); ok {
			s.sendKeyEvent(held.keysym, held.keyNumber, true)
			return true
		}
	}
	resolved, ok := resolveEvent(event)
	if !ok {
		// No tier has a keysym: macOS Fn is a hardware toggle with no guest
		// meaning, so the event is dropped rather than guessed.
		return false
	}
	s.syncModifiers(event.Mods)
	s.sendKeyEvent(resolved.Keysym, resolved.QEMUKeyNumber, true)
	if s.heldKeys == nil {
		s.heldKeys = make(map[rfb.KeyCode]heldKey)
	}
	s.heldKeys[resolved.Code] = heldKey{keysym: resolved.Keysym, keyNumber: resolved.QEMUKeyNumber}
	return true
}

// handleKeyUp sends the key-down's recorded keysym, then lets go of every
// synthesized modifier. MyGo never reports a modifier's own key-up (it reports
// modifier changes as bits only), so a modifier held for a chord is scoped to
// that key press: it is released with the key. The next key press re-presses
// it. This avoids a modifier that stays stuck in the guest after the user lets
// go, while every chord still reaches the guest with its modifiers down. A
// key-up with no matching key-down is ignored, as noVNC does.
func (s *Surface) handleKeyUp(event ui.InputEvent) bool {
	code, held, ok := s.held(event)
	if !ok {
		return false
	}
	s.sendKeyEvent(held.keysym, held.keyNumber, false)
	delete(s.heldKeys, code)
	s.releaseModifiers()
	return true
}

// held returns the recorded key-down for an event. Resolution is physical
// first, so a key-up pairs with its down even when the shift state changed.
func (s *Surface) held(event ui.InputEvent) (rfb.KeyCode, heldKey, bool) {
	resolved, ok := resolveEvent(event)
	if !ok {
		return rfb.CodeUnknown, heldKey{}, false
	}
	key, found := s.heldKeys[resolved.Code]
	if !found {
		return rfb.CodeUnknown, heldKey{}, false
	}
	return resolved.Code, key, true
}

// resolveEvent maps MyGo's key identities onto the RFB resolver's tiers.
//
// MyGo v0.10.0 (the vendored version) exposes InputEvent.Code, a USB HID usage
// ID, but this resolver does not read it yet: the physical ANSI-US position is
// still the first tier. Filling the Code tier from it is a separate change.
//
// MyGo's Key is also reduced to a fixed US-key enum (KeyForRune), so a layout
// character outside that set, such as ISO '#' or a non-US punctuation mark,
// arrives as KeyUnknown and resolves by physical position. The shifted keysym
// is therefore the US table's for that position; the platform API cannot give
// the layout's actual character on a key event.
func resolveEvent(event ui.InputEvent) (rfb.ResolvedKey, bool) {
	return rfb.ResolveKey(rfb.KeyStroke{
		Code:     rfb.CodeUnknown,
		Physical: rfbKeyCode(event.PhysicalKey),
		Logical:  rfbKeyCode(event.Key),
		Shift:    event.Mods&ui.Shift != 0,
	})
}

// syncModifiers makes the guest's modifier state match want, pressing and
// releasing the left-side keysyms in a deterministic order. MyGo reports
// modifiers only as bits, so their keysym events have to be synthesized.
func (s *Surface) syncModifiers(want ui.Modifiers) {
	want &= ui.Shift | ui.Ctrl | ui.Alt | ui.Super
	for i := len(modifierOrder) - 1; i >= 0; i-- {
		mod := modifierOrder[i]
		if s.heldMods&mod != 0 && want&mod == 0 {
			s.sendModifier(mod, false)
		}
	}
	for _, mod := range modifierOrder {
		if want&mod != 0 && s.heldMods&mod == 0 {
			s.sendModifier(mod, true)
		}
	}
	s.heldMods = want
}

// releaseModifiers lets go of every synthesized modifier.
func (s *Surface) releaseModifiers() { s.syncModifiers(0) }

// sendModifier presses or releases one synthesized modifier keysym.
func (s *Surface) sendModifier(mod ui.Modifiers, down bool) {
	if keysym, ok := s.modifierKeysym(mod); ok {
		_ = s.sendKey(keysym, down)
	}
}

// modifierKeysym maps a modifier bit onto its keysym. macOS follows noVNC:
// Command is the guest's Alt and Option is AltGr, because macOS's Option types
// special characters instead of acting as a plain Alt.
func (s *Surface) modifierKeysym(mod ui.Modifiers) (uint32, bool) {
	switch mod {
	case ui.Shift:
		return rfb.KeysymShiftL, true
	case ui.Ctrl:
		return rfb.KeysymControlL, true
	case ui.Alt:
		if s.macModifiers {
			return rfb.KeysymISOLevel3Shift, true
		}
		return rfb.KeysymAltL, true
	case ui.Super:
		if s.macModifiers {
			return rfb.KeysymAltL, true
		}
		return rfb.KeysymSuperL, true
	}
	return 0, false
}

// canSendInput reports whether guest-directed input writes are permitted.
// Every send path goes through it, so view-only cannot be bypassed by an
// action the toolbar, a test, or a later caller invokes directly.
func (s *Surface) canSendInput() bool { return !s.viewOnly }

// sendKey writes one keysym when input is allowed.
func (s *Surface) sendKey(keysym uint32, down bool) error {
	if !s.canSendInput() {
		return ErrViewOnly
	}
	if s.session == nil {
		return appsession.ErrNoDisplay
	}
	return s.session.SendKey(keysym, down)
}

// sendKeyEvent sends one key with its physical number when known. The Session
// chooses an Extended Key Event or a plain KeyEvent; either is the same guest
// behavior. View-only refuses the write before it reaches the wire.
func (s *Surface) sendKeyEvent(keysym, keyNumber uint32, down bool) error {
	if !s.canSendInput() {
		return ErrViewOnly
	}
	if keyNumber != 0 {
		if s.session == nil {
			return appsession.ErrNoDisplay
		}
		return s.session.SendPhysicalKey(keysym, keyNumber, down)
	}
	return s.sendKey(keysym, down)
}

// releaseHeldKeys lets go of every key, synthesized modifier and pointer
// button the guest was told about, so losing the Pane or the window's focus
// cannot leave input stuck. MyGo reports neither a key-up on window blur nor a
// modifier's own key-up, so the viewer owns both.
func (s *Surface) releaseHeldKeys() {
	if s.session == nil {
		return
	}
	// A viewport drag is local, but stopping the Pane must not leave the
	// gesture half-applied either.
	s.panPending = false
	s.panning = false
	for code, held := range s.heldKeys {
		s.sendKeyEvent(held.keysym, held.keyNumber, false)
		delete(s.heldKeys, code)
	}
	s.releaseModifiers()
	if s.downMask != 0 {
		s.downMask = 0
		s.sendPointer(s.pointerX, s.pointerY, 0)
	}
}

// handleScroll sends the wheel as momentary RFB buttons: 4 (up) and 5 (down).
// Each notch is a press followed by a release carrying the rest of the mask.
//
// Direction follows X11 and noVNC: positive DY moves the view down the content,
// which is wheel-down or RFB button 5. The mapping research's table pairs
// positive DY with button 4, but that contradicts X11 (4=up, 5=down) and the
// ticket's "4 (up) and 5 (down)"; noVNC also sends 5 for a positive delta.
func (s *Surface) handleScroll(event ui.InputEvent) bool {
	if event.DY == 0 {
		return false
	}
	s.pointerX, s.pointerY = event.X, event.Y
	button := uint8(0x10) // button 5: wheel down
	if event.DY < 0 {
		button = 0x08 // button 4: wheel up
	}
	s.sendPointer(event.X, event.Y, s.downMask|button)
	s.sendPointer(event.X, event.Y, s.downMask)
	return true
}

// SendCtrlAltDel injects the Ctrl+Alt+Del chord as three keysyms, pressed in
// order and released in reverse. The host OS usually grabs the real chord, so
// the viewer chrome calls this from its toolbar. View-only refuses the whole
// chord instead of writing a partial one.
func (s *Surface) SendCtrlAltDel() error {
	if !s.canSendInput() {
		return ErrViewOnly
	}
	if s.session == nil {
		return appsession.ErrNoDisplay
	}
	sequence := []struct {
		keysym uint32
		down   bool
	}{
		{rfb.KeysymControlL, true},
		{rfb.KeysymAltL, true},
		{rfb.KeysymDelete, true},
		{rfb.KeysymDelete, false},
		{rfb.KeysymAltL, false},
		{rfb.KeysymControlL, false},
	}
	for _, step := range sequence {
		if err := s.sendKey(step.keysym, step.down); err != nil {
			return err
		}
	}
	return nil
}

// sendPointer maps a local pixel position onto the framebuffer and sends it.
// The mapping inverts the active scale mode and clip pan, so 1:1 and a panned
// viewport name the same remote pixels they show. A Pane that shows one screen
// of a multi-screen desktop offsets the result by that screen's origin, because
// the guest expects desktop coordinates. View-only writes nothing.
func (s *Surface) sendPointer(x, y float32, mask uint8) {
	if !s.canSendInput() {
		return
	}
	s.mu.Lock()
	pane, frame, origin := s.pane, s.frame, s.screenOrigin
	s.mu.Unlock()
	if frame.Empty() {
		return
	}
	rx, ry := remotePointFor(x, y, pane, frame, s.scaleMode, s.panX, s.panY)
	_ = s.session.SendPointer(rx+origin.X, ry+origin.Y, mask)
}

// applyButton sets or clears one button's bit in a pointer mask, returning the
// combined mask. ok is false for a button RFB has no bit for.
func applyButton(mask uint8, button int, down bool) (next uint8, ok bool) {
	bit := buttonMask(button)
	if bit == 0 {
		return mask, false
	}
	if down {
		return mask | bit, true
	}
	return mask &^ bit, true
}

// buttonMask maps a MyGo pointer button onto the RFB mask. The protocol's bits
// are 0 primary, 1 middle, 2 secondary; MyGo numbers the secondary button 1
// and the middle button 2, so those two are swapped on the way out.
func buttonMask(button int) uint8 {
	switch button {
	case 0:
		return 1 // primary
	case 1:
		return 4 // secondary
	case 2:
		return 2 // middle
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
		// The Pane is going away; let go of every key and pointer button the
		// guest still believes held before the transport closes.
		s.releaseHeldKeys()
		close(s.done)
		s.mu.Lock()
		s.pending = nil
		s.bitmap = nil
		s.pixels = nil
		s.scaled = nil
		s.scaledPix = nil
		s.cursor = nil
		s.cursorFor = nil
		s.cursorBitmap = nil
		s.cursorScaled = nil
		s.cursorScaledFor = nil
		s.hibernated = false
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
