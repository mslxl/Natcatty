package phoneshell

import (
	"io"
	"strings"
	"sync"

	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"

	domainterminal "github.com/mslxl/natcatty/domain/terminal"
	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/terminal"
	"github.com/mslxl/natcatty/ui/terminal/appview"
)

// newTerminalScreen builds the phone's real terminal over a Session handle.
// libghostty-vt is linked into the app on phones, so terminal.New succeeds once
// the root has called terminal.Load; a test injects its own ScreenFactory so
// the shell's navigation tests do not depend on the native library.
//
// appearance is the shared Appearance document's terminal settings. Passing
// it here is what makes the phone draw the same font, cursor and scheme as the
// desktop; SetAppearance applies later changes to the live screen.
func newTerminalScreen(conn io.ReadWriteCloser, appearance TerminalAppearance) (TerminalScreen, error) {
	// A Mosh or Eternal Terminal Session's peer owns the screen size. On a
	// phone that is the only size signal there is: the peer has no
	// window-change channel of its own, so the view must not resize the
	// screen it draws, and must hand the peer's size to the terminal instead.
	// MyGo's ResizeByConn asks Conn for the view's size, and the Session's
	// sink applies the peer's authoritative size before feeding its bytes.
	remoteScreen := false
	if remote, ok := conn.(interface{ ScreenSizeFromRemote() bool }); ok {
		remoteScreen = remote.ScreenSizeFromRemote()
	}
	// The flow observes the same bytes the terminal reads, and it must do so
	// before the terminal consumes them. The tap wraps the view; the view's
	// optional methods (Resize) stay reachable through it.
	flow := newPhoneLensFlow(nil)
	// The screen exists before the terminal because the terminal's OnKey hook
	// dispatches through it: an external keyboard's application bindings are
	// resolved before the terminal encodes the key.
	screen := &ghosttyScreen{
		appearance:   appearance,
		flow:         flow,
		lensMode:     true,
		focusNext:    true,
		remoteScreen: remoteScreen,
		viewConn:     conn,
		pasteKeys:    phonePasteKeys(appearance.Keybinds),
	}
	term, err := terminal.New(terminal.Options{
		Conn:         &lensTapConn{conn: conn, flow: flow},
		ResizeByConn: remoteScreen,
		Scrollback:   phoneLensScrollback,
		Font:         appearance.Font,
		Theme:        appearance.Theme,
		Cursor:       appearance.Cursor,
		NoBlink:      !appearance.Blink,
		Keybinds:     appearance.Keybinds,
		OnKey:        screen.onKey,
		OnPaste:      screen.onPaste,
	})
	if err != nil {
		flow.close()
		return nil, err
	}
	// The terminal's reader waits for this bind, so a marker can never be
	// recorded without the mirror that names its row.
	flow.bind(term)
	if remoteScreen {
		if sink, ok := conn.(interface{ SetScreenSizeSink(func(cols, rows int)) }); ok {
			sink.SetScreenSizeSink(term.Resize)
		}
	}
	screen.term = term
	screen.input = appview.NewInputClient(func(p []byte) { term.Send(p) })
	if remoteScreen {
		screen.viewSizeCh = make(chan appview.Size, 1)
		screen.viewSizeQuit = make(chan struct{})
		go screen.watchViewSize(screen.viewSizeQuit)
	}
	return screen, nil
}

// ghosttyScreen adapts the libghostty-vt terminal to the screen seam. In the
// global Lens terminal mode it draws the shared appview flow: finished
// commands and the live screen in one scroll space. With the global raw mode
// on, it draws the terminal's own view instead, exactly as before.
type ghosttyScreen struct {
	term       *terminal.Terminal
	appearance TerminalAppearance
	flow       *phoneLensFlow
	// lensMode follows the shared terminal settings' RawTerminalMode inverse.
	// Only the UI thread reads or writes it.
	lensMode     bool
	remoteScreen bool
	// viewConn is the Session view the screen reads. With a peer-owned screen
	// it is told the grid the phone has room for, as terminal.View reports the
	// size it fits; the screen itself keeps the peer's size.
	viewConn     io.ReadWriteCloser
	viewSizeCh   chan appview.Size
	viewSizeQuit chan struct{}
	// viewRect is the Pane's laid-out box in the last paint. The grid divides
	// that room, but the measurement is only readable on the build after it
	// (appview.GridFor reads the previous layout), so a changed box asks for
	// the frame that can report it.
	viewRect     ui.Rect
	viewRectOK   bool
	reportedSize appview.Size
	reportedOK   bool
	// focusNext asks the pane for one frame of keyboard focus: when the pane
	// first appears, when Lens mode turns back on, and after a press on a
	// finished block.
	focusNext bool
	selection appview.Selection
	scroll    ui.ScrollState
	follow    appview.Follow
	input     *appview.InputClient
	// keyboard is the external-keyboard dispatcher the Sessions page installs
	// on every frame the Pane is visible. It is nil when the Shell has no
	// binding document, and the terminal then keeps its own keys alone.
	keyboard *phoneKeyboard
	// pasteKeys are the chords the terminal's key table treats as paste. They
	// are intercepted here so every entry point — touch, hardware key and the
	// system's Paste command — reads the clipboard and asks for confirmation
	// through the one phone paste path.
	pasteKeys map[phoneChordKey]bool
	// pasteTarget is the Sessions page's unified paste decision. It reports
	// whether it took the data (by sending it or by holding it for the
	// confirmation row).
	pasteTarget func(c *ui.Context, d transfer.Data) bool
	// echoKey marks that the last key was handled here — a binding ran, or
	// the screen typed the composed text the key carried — so the text the
	// platform commits for it is not typed as well.
	echoKey bool
	// frame is the last build's context. A tap arrives between frames, and
	// the URL outcome has to repaint the same window.
	frame *ui.Context
	// tapGeom is the live grid's geometry as the last frame laid it out.
	tapGeom tapGeometry
	// tapX and tapY are where the finger went down, so a drag is a selection
	// rather than a link.
	tapX, tapY   float32
	tapCandidate bool
	noticeMu     sync.Mutex
	linkNotice   string
	// pendingTap is a clean tap waiting for the next frame. A pointer event
	// arrives between frames, where the Context's OpenURL has no frame to
	// act on, so the tap is resolved when the frame that follows it builds.
	pendingTap  bool
	pendingTapX float32
	pendingTapY float32
}

// View draws the screen for this frame. The appview flow needs a frame after
// output arrives; the Context's invalidate is safe from the reader goroutine.
func (s *ghosttyScreen) View(c *ui.Context) ui.Element {
	s.frame = c
	s.runPendingTap(c)
	s.flow.setRedraw(c.Invalidate)
	if !s.lensMode {
		return terminal.View(c, s.term)
	}
	return s.lensView(c)
}

// SetLensMode follows the global Lens terminal mode. Turning it on requests
// keyboard focus for the replaced input element; the appview flow's rows are
// new nodes, so without the request the keyboard would stay on none of them.
func (s *ghosttyScreen) SetLensMode(on bool) {
	if s.lensMode == on {
		return
	}
	s.lensMode = on
	s.selection.Clear()
	if on {
		s.focusNext = true
		return
	}
	s.setNotice("")
}

// lensView lays the shared flow into the space the phone gives the screen. It
// is the same one-scroll layout the desktop uses (ADR-0022), with the tap
// path added for a finger: a clean tap on a printed link opens it, and a drag
// stays a selection.
func (s *ghosttyScreen) lensView(c *ui.Context) ui.Element {
	if !s.remoteScreen {
		// The pane sizes the terminal to the room it has; the peer-owned
		// screen size is already authoritative for a remote TUI.
		if grid, ok := appview.GridFor(); ok {
			if cols, rows := s.term.Size(); cols != grid.Cols || rows != grid.Rows {
				s.term.Resize(grid.Cols, grid.Rows)
			}
		}
	}
	return ui.Column(c).Fill().Children(func() {
		if notice := s.notice(); notice != "" {
			kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, notice, func() {
				if kit.GhostButton(c, icons.X, "Dismiss").Clicked() {
					s.setNotice("")
				}
			})
		}
		s.lensPane(c).Fill()
	})
}

// lensPane builds the appview Pane with the phone's hooks. It is one frame's
// element; the flow, its scroll, follow and selection outlive it.
func (s *ghosttyScreen) lensPane(c *ui.Context) ui.Element {
	cols, rows := s.term.Size()
	size := appview.Size{Cols: cols, Rows: rows}
	s.flow.setSize(cols, rows)
	blocks := s.flow.store.Snapshot()
	tail := appview.Tail{}
	if row, known := s.flow.promptBoundary(); known {
		tail.PromptRow, tail.PromptRowKnown = row, true
		tail.PromptAfterCommand = s.flow.promptFollowsCommand()
	}
	for _, block := range blocks {
		if block.Exited {
			continue
		}
		tail.Running = block.Cmd.Typed
		if tail.Running == "" {
			tail.Running = block.Cmd.Line
		}
	}
	cursorColor := ui.Color{}
	if s.appearance.Theme != nil {
		cursorColor = s.appearance.Theme.Cursor
	}
	focus := s.focusNext
	s.focusNext = false
	pane := appview.Pane(
		c,
		s.flow.store,
		s.flow.engine,
		s.term,
		size,
		nil,
		&s.scroll,
		&s.follow,
		nil,
		appview.Bell{Col: -1, Row: -1},
		tail,
		appview.Cursor{
			Style: s.appearance.Cursor,
			Color: cursorColor,
			Blink: s.appearance.Blink,
		},
		s.appearance.Theme,
		appview.Hooks{
			Key:       s.term,
			TextInput: s.input,
			// An external keyboard's application bindings and the shared
			// paste path run before the terminal encodes a key.
			Input:     s.handleInput,
			Selection: &s.selection,
			Focus:     focus,
			KeepFocus: true,
			Click: func() {
				// A press on a finished block takes the keyboard back to the
				// live screen, as the desktop Session does.
				s.selection.Clear()
				s.focusNext = true
				c.Invalidate()
			},
		},
		func(text string) { c.Services().WriteClipboard(text) },
	)
	// The pointer's coordinates are relative to the Pane. The live grid sits
	// flowPadY inside the flow and livePad inside the live box; the Pane's
	// content is pinned to the flow's end while the reader follows, so the
	// rows are measured from the bottom.
	cellW, cellH := appview.CellSize()
	bounds := pane.Bounds()
	s.tapGeom = tapGeometry{
		width: bounds.W, height: bounds.H,
		cellW: cellW, cellH: cellH,
		padX: 16, padY: 16, bottomInset: 16,
		cols: cols, rows: rows,
		lens: true,
	}
	s.reportViewSize()
	element := pane.ObserveInput(s.observeTap)
	if s.remoteScreen {
		// The Pane's painted box is the room the grid fits. Its changed box
		// asks for another frame, the first that can measure the new grid.
		element = element.DrawOver(func(_ *ui.Painter, r ui.Rect) {
			if !s.viewRectOK || r != s.viewRect {
				s.viewRect, s.viewRectOK = r, true
				c.Invalidate()
			}
		})
	}
	return element
}

// reportViewSize tells a peer-owned Session the grid the phone has room for.
// terminal.View reports the size it fits from its paint; the Lens flow draws
// through appview instead, so it reports the same thing itself while keeping
// the screen at the peer's size, which the peer has authority over.
func (s *ghosttyScreen) reportViewSize() {
	if !s.remoteScreen || s.viewSizeCh == nil {
		return
	}
	grid, ok := appview.GridFor()
	if !ok {
		return
	}
	if s.reportedOK && grid == s.reportedSize {
		return
	}
	s.reportedSize, s.reportedOK = grid, true
	// The connection may be a network session: report from its own goroutine,
	// as terminal.Terminal does, and keep only the latest size queued.
	for {
		select {
		case s.viewSizeCh <- grid:
			return
		default:
		}
		select {
		case <-s.viewSizeCh:
		default:
		}
	}
}

// watchViewSize reports the queued sizes one at a time, so a slow connection
// sees them in order and only the newest of a burst.
func (s *ghosttyScreen) watchViewSize(quit <-chan struct{}) {
	for {
		select {
		case grid := <-s.viewSizeCh:
			s.tellViewSize(grid)
		case <-quit:
			return
		}
	}
}

func (s *ghosttyScreen) tellViewSize(grid appview.Size) {
	if resizable, ok := s.viewConn.(interface {
		ResizePixels(cols, rows, cellWidth, cellHeight int) error
	}); ok {
		cellW, cellH := appview.CellSize()
		_ = resizable.ResizePixels(grid.Cols, grid.Rows, int(cellW), int(cellH))
		return
	}
	if resizable, ok := s.viewConn.(interface{ Resize(int, int) error }); ok {
		_ = resizable.Resize(grid.Cols, grid.Rows)
	}
}

// observeTap is the capture-phase listener. It never consumes the event: the
// Pane still selects the cells under the finger and reports a program's mouse
// input. A clean tap (no drag) additionally opens the link under it.
func (s *ghosttyScreen) observeTap(ev ui.InputEvent) {
	switch ev.Kind {
	case ui.InputPointerDown:
		s.tapCandidate = ev.Button == 0
		s.tapX, s.tapY = ev.X, ev.Y
	case ui.InputPointerMove:
		if !s.tapCandidate {
			return
		}
		if absFloat(ev.X-s.tapX) > 12 || absFloat(ev.Y-s.tapY) > 12 {
			s.tapCandidate = false
		}
	case ui.InputPointerUp:
		candidate := s.tapCandidate && ev.Button == 0 && !ev.Canceled && s.lensMode
		s.tapCandidate = false
		if candidate {
			// Resolve on the next frame: the pointer event runs outside one,
			// where a Context's OpenURLThen has no frame to act on.
			s.pendingTap = true
			s.pendingTapX, s.pendingTapY = ev.X, ev.Y
			if s.frame != nil {
				s.frame.Invalidate()
			}
		}
	}
}

// runPendingTap performs the tap the last pointer event queued, on the frame
// that followed it. The Context is valid here, so a browser launch and a
// failed open's notice both reach the window.
func (s *ghosttyScreen) runPendingTap(c *ui.Context) {
	if !s.pendingTap {
		return
	}
	s.pendingTap = false
	s.handleTap(c, s.pendingTapX, s.pendingTapY)
}

func absFloat(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

// handleTap opens the target under a finger. A URL goes to the system browser
// with its outcome watched, so a refusal becomes a notice and not a lost tap.
// An archive name fills its extraction command at the prompt, unsubmitted, and
// the live screen takes the keyboard back. Every other target is not a link
// and does nothing (see linkAtRow).
func (s *ghosttyScreen) handleTap(c *ui.Context, x, y float32) {
	// The live grid is under the finger only while the flow follows its end.
	// Scrolled back, the rows are finished blocks, and reading them as live
	// rows would open a link the reader never touched.
	if !s.follow.Pinned() {
		return
	}
	target, kind, ok := linkAtPoint(s.term, s.tapGeom, x, y)
	if !ok {
		return
	}
	switch kind {
	case linkURL:
		if c == nil {
			return
		}
		c.OpenURLThen(target, func(err error) {
			if err == nil {
				return
			}
			s.setNotice(linkNoticeText(target, err))
			c.Invalidate()
		})
	case linkArchive:
		command, ok := domainterminal.ArchiveExtractCommand(target)
		if !ok {
			return
		}
		// The desktop's archive handler declines while a command runs or a
		// program takes the pointer, and so does this one: an archive path
		// must never be typed into a program that did not ask for it.
		if s.flow.running() || s.mouseReporting() {
			return
		}
		// The command lands at the line editor's cursor as typed text; it is
		// never submitted, and the terminal's own state is untouched.
		s.term.Send([]byte(command))
		s.focusNext = true
		if c != nil {
			c.Invalidate()
		}
	}
}

// mouseReporting reports whether the screen's program asked for pointer
// events (DECSET 1000/1002/1003). The replay carries the modes, as it does the
// rest of the VT state.
func (s *ghosttyScreen) mouseReporting() bool {
	cols, rows := s.term.Size()
	if cols <= 0 || rows <= 0 {
		return false
	}
	return appview.ScreenFrame(s.term, appview.Size{Cols: cols, Rows: rows}, false).Mouse.Enabled()
}

func (s *ghosttyScreen) notice() string {
	s.noticeMu.Lock()
	defer s.noticeMu.Unlock()
	return s.linkNotice
}

func (s *ghosttyScreen) setNotice(text string) {
	s.noticeMu.Lock()
	s.linkNotice = text
	s.noticeMu.Unlock()
}

// SendKey types an accessory-bar key through the terminal's own encoder.
func (s *ghosttyScreen) SendKey(key ui.Key, mods ui.Modifiers) { s.term.SendKey(key, mods) }

// SetKeyboard installs the external-keyboard dispatcher. The Sessions page
// calls it for every frame the Pane is visible; a nil table leaves the
// terminal's own keys alone.
func (s *ghosttyScreen) SetKeyboard(keyboard *phoneKeyboard) { s.keyboard = keyboard }

// SetPasteTarget installs the Sessions page's unified paste decision. Every
// paste entry point on this screen hands the clipboard data to it.
func (s *ghosttyScreen) SetPasteTarget(target func(c *ui.Context, d transfer.Data) bool) {
	s.pasteTarget = target
}

// handleInput runs before the terminal encodes a key. It gives an external
// keyboard's application bindings their chance, intercepts the paste keys and
// the system's Paste command so they share the touch path's confirmation,
// types the composed text a key carries where the terminal's own encoding
// would drop it, and leaves every other key to the terminal untouched.
func (s *ghosttyScreen) handleInput(ev ui.InputEvent) bool {
	switch ev.Kind {
	case ui.InputText:
		if s.echoKey {
			// The platform committed the text of a key this screen already
			// handled; typing it too would run the action twice.
			s.echoKey = false
			return true
		}
		if ev.Text == "\u0000" {
			// Android's modifier artifact, never typed.
			return true
		}
		return false
	case ui.InputCommand:
		if ev.Text == "paste" {
			return s.pasteFromClipboard()
		}
		return false
	case ui.InputKeyDown:
		s.echoKey = false
		mods, key, text, ok := phoneKeyInput(ev)
		if ok {
			if s.isPasteChord(mods, key) {
				return s.pasteFromClipboard()
			}
			if s.keyboard.run(mods, key) {
				s.echoKey = true
				return true
			}
		}
		// The platform composed text the key's own encoding does not type:
		// punctuation whose name suppresses Android's text fallback, a key
		// with no name, or an Alt-composed character. Type it, and swallow
		// the commit the platform may send after the key, as a handled
		// chord does.
		if s.term != nil && phoneTypesText(mods, key, text) {
			s.term.Send([]byte(text))
			s.echoKey = true
			return true
		}
		return false
	case ui.InputKeyUp:
		// The text a handled key commits arrives before its release. Clearing
		// here keeps the flag from swallowing the first character the user
		// later types on the on-screen keyboard.
		s.echoKey = false
		return false
	default:
		return false
	}
}

// onKey is the raw terminal view's hook, which runs before the terminal's
// keybinds and before it encodes the key. The Lens flow does not use it: its
// Pane calls handleInput, which sees the event's text as well.
func (s *ghosttyScreen) onKey(mods ui.Modifiers, key ui.Key) bool {
	if s.isPasteChord(mods, key) {
		return s.pasteFromClipboard()
	}
	return s.keyboard.run(mods, key)
}

// onPaste is the raw view's paste hook: a Paste menu command, a secondary
// click with RightClickPaste, and any paste key the terminal resolves itself
// all arrive here. Taking every one of them keeps the view from falling back to
// its own unfiltered read when the phone can paste none of the formats, and
// sends what it can through the one paste path.
func (s *ghosttyScreen) onPaste(transfer.Data) bool {
	s.pasteFromClipboard()
	return true
}

// isPasteChord reports whether the terminal's own key table binds the chord to
// paste, so this screen can route it through the shared confirmation.
func (s *ghosttyScreen) isPasteChord(mods ui.Modifiers, key ui.Key) bool {
	return s.pasteKeys[phoneChordKey{mods: mods, key: key}]
}

// pasteFromClipboard reads the clipboard explicitly as text and PNG and hands
// it to the Sessions page's one paste path. It reports whether anything was
// pasted: an empty clipboard takes nothing, as MyGo returns empty data rather
// than an error for it.
func (s *ghosttyScreen) pasteFromClipboard() bool {
	if s.term == nil {
		return false
	}
	data := readPhoneClipboard(s.frame)
	if len(data.Items()) == 0 {
		return false
	}
	if s.pasteTarget != nil {
		return s.pasteTarget(s.frame, data)
	}
	s.term.PasteData(data)
	return true
}

// phonePasteKeys computes the chords the terminal's key table binds to paste,
// merging the installed bindings over MyGo's defaults exactly as
// terminal.keybinds does. The touch control and the hardware keys then agree on
// which chords paste.
func phonePasteKeys(binds []terminal.Keybind) map[phoneChordKey]bool {
	effective := terminal.DefaultKeybinds()
	for _, bind := range binds {
		kept := effective[:0]
		for _, existing := range effective {
			if existing.Mods != bind.Mods || existing.Key != bind.Key {
				kept = append(kept, existing)
			}
		}
		effective = kept
		if bind.Action != terminal.ActionNone {
			effective = append(effective, bind)
		}
	}
	keys := make(map[phoneChordKey]bool)
	for _, bind := range effective {
		if bind.Action == terminal.ActionPaste {
			keys[phoneChordKey{mods: bind.Mods, key: bind.Key}] = true
		}
	}
	return keys
}

// SelectionText reads the selection the long press and drag made. The raw
// view keeps the terminal's own selection; the Lens flow selects replay rows
// with the shared Pane selection, so its text comes from the same replay.
func (s *ghosttyScreen) SelectionText() (string, bool) {
	if !s.lensMode {
		return s.term.SelectionText()
	}
	if s.selection.Empty() {
		return "", false
	}
	cols, rows := s.term.Size()
	if cols <= 0 || rows <= 0 {
		return "", false
	}
	frame := appview.ScreenFrame(s.term, appview.Size{Cols: cols, Rows: rows}, false)
	text := frame.SelectionText(s.selection)
	text = strings.TrimRight(text, "\n")
	if text == "" {
		return "", false
	}
	return text, true
}

// PasteData uses the terminal's paste path, so bracketed paste mode applies:
// the program that asked for one gets the payload wrapped and does not run a
// multi-line paste as commands.
func (s *ghosttyScreen) PasteData(d transfer.Data) { s.term.PasteData(d) }

// SetAppearance restyles the live terminal without recreating the view. It is
// what applies an Appearance change while a Session is open: the font and
// colors are set directly, and the cursor is sent the DECSCUSR sequence a
// program would use.
func (s *ghosttyScreen) SetAppearance(a TerminalAppearance) {
	s.appearance = a
	s.term.SetFont(a.Font)
	s.term.SetTheme(a.Theme, nil)
	s.term.SetCursor(a.Cursor, a.Blink)
}

// InScrollback reports whether the viewport is above the live screen. The
// Lens flow scrolls its own flow; the raw view uses the terminal's viewport.
func (s *ghosttyScreen) InScrollback() bool {
	if !s.lensMode {
		return s.term.ScrolledBack()
	}
	return s.scroll.Y < s.scroll.MaxY-1
}

// Dir is the working directory the shell reported through OSC 7. The phone
// persists it with the shared split tree so a restored Pane reopens where it
// left off, exactly as the desktop does.
func (s *ghosttyScreen) Dir() string { return s.term.Dir() }

// ToggleScrollback uses the terminal's own viewport API rather than a MyGo
// scroll widget. The scrollback lives in libghostty-vt and reflows with the
// grid, so a scroll container around the view could not show the rows above
// it without duplicating the screen, and a finger drag there would claim the
// gesture the grid needs for selection and mouse reporting. A button the
// thumb reaches asks for neither and works whether a program takes the mouse.
func (s *ghosttyScreen) ToggleScrollback() {
	if !s.lensMode {
		s.term.ToggleScrollback()
		return
	}
	if s.InScrollback() {
		// Back to the end: Follow sees the flow at its end and pins again.
		s.scroll.Y = s.scroll.MaxY
	} else {
		s.follow.Detach()
		page := s.tapGeom.height
		if page <= 0 {
			page = 300
		}
		s.scroll.Y = max(0, s.scroll.Y-page)
	}
	if s.frame != nil {
		s.frame.Invalidate()
	}
}

// Close detaches the screen. Closing the tap flow first stops the reader's
// frame requests from outliving the view.
func (s *ghosttyScreen) Close() error {
	if s.flow != nil {
		s.flow.close()
	}
	if s.viewSizeQuit != nil {
		close(s.viewSizeQuit)
		s.viewSizeQuit = nil
	}
	return s.term.Close()
}
