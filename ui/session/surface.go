// Package sessionui owns the MyGo terminal that renders one application
// Session. The application owns the transport; this surface connects MyGo's
// terminal to the Session's bounded duplex and keeps the terminal's lifetime
// to the Tab's. It also renders the Session's command, activity, logging, and
// transfer chrome from application state.
package sessionui

import (
	"errors"
	"image"
	"io"
	"math"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
	"github.com/mslxl/natcatty/ui/terminal"

	"github.com/mslxl/natcatty/application/autocomplete"
	lensmodel "github.com/mslxl/natcatty/application/lens/model"
	appsession "github.com/mslxl/natcatty/application/session"
	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/terminal/appview"
	terminaleffects "github.com/mslxl/natcatty/ui/terminal/effects"
	terminalsurface "github.com/mslxl/natcatty/ui/terminal/surface"
	uitheme "github.com/mslxl/natcatty/ui/theme"
)

// Options configure a Surface. The app supplies logging defaults from Settings
// and the window redraw used when a clipboard request arrives on the UI thread.
type Options struct {
	LogDefaults func() appsession.LogOptions
	// OpenLog hands a Session's current log file to the platform opener. Nil
	// leaves the menu action disabled.
	OpenLog func(path string)
	Redraw  func()
	// PastePolicy reads the live multiline paste setting.
	PastePolicy func() (protection bool, minLines int)
	// DropAction reads what a file drop should do.
	DropAction func() domainterminal.DropAction
	// Upload is the seam for a future SFTP/ZMODEM engine. Nil reports that
	// uploads are unavailable instead of inserting paths.
	Upload UploadFunc
	// SaveScreen writes the terminal's text and returns the saved path. Nil
	// uses SaveScreenFile.
	SaveScreen ScreenWriter
	// Reconnect retries the transport immediately. Nil leaves the Session
	// menu's Reconnect item disabled.
	Reconnect func()
	// Reconnectable reports whether Reconnect can be used now.
	Reconnectable func() bool
	// SendYmodem and ReceiveYmodem are the transfer seam a later ticket
	// implements. Nil leaves the corresponding item disabled.
	SendYmodem    func()
	ReceiveYmodem func()
	// OnEncoding persists a user's live encoding choice.
	OnEncoding func(domainterminal.Encoding)
	// Suggest returns command suggestions for the line being typed. cwd is the
	// working directory the shell last reported with OSC 7, used for the
	// current-directory ranking bonus. Nil leaves autocomplete off.
	Suggest func(input, scope, cwd string) []autocomplete.Suggestion
	// SuggestAtPrompt is Suggest with the tracked prompt kind: program is true
	// when a non-shell program is waiting for ordinary input, where the Vault
	// Snippets join the journal rows. Nil uses Suggest for every prompt.
	SuggestAtPrompt func(input, scope, cwd string, program bool) []autocomplete.Suggestion
	// AutocompleteDisplay reports whether the popup and ghost hint are shown.
	AutocompleteDisplay func() (popup, ghost bool)
	// AutocompleteDelay is the debounce in milliseconds before suggestions are
	// recomputed. Nil means immediately.
	AutocompleteDelay func() int
	// Font reads the live terminal font. The app applies a Settings change to
	// open terminals through SetTerminalFont; this is what a new terminal uses.
	Font func() terminal.Font
	// Cursor reads the live terminal cursor shape and whether it blinks. The
	// app applies a Settings change to open terminals through
	// SetTerminalCursor; a new terminal reads it here.
	Cursor func() (domainpreferences.CursorStyle, bool)
	// ShiftEnterNewline reports whether Shift+Enter sends ESC CR instead of
	// the terminal's own encoding. It is read on each key press, so a Settings
	// change applies to an open terminal.
	ShiftEnterNewline func() bool
	// Theme reads the live terminal color scheme. The app applies a Settings
	// change to open terminals through SetTerminalTheme; a new terminal reads
	// it here.
	Theme func() domaintheme.TerminalTheme
	// Keybinds reads the active application bindings that act inside the
	// terminal. MyGo applies them at construction, and an unbound default chord
	// is cleared so a Settings override is authoritative for new Sessions.
	Keybinds func() []terminal.Keybind
	// RawTerminal reports the global raw terminal display mode. It is read
	// while rendering so a setting change applies to every open Session.
	RawTerminal func() bool
	// View is an already claimed live connection. Resume supplies the
	// ResumedView that replays the hibernated screen; nil claims the
	// Session's one live view with AttachView.
	View io.ReadWriteCloser
	// Browser opens this Session's browser panel, routed through its Host or
	// to this computer. Nil leaves the Session menu's Open Browser item
	// disabled, as for a serial Session.
	Browser func(onClose func()) (BrowserPanel, error)
}

// BrowserPanel is the browser surface a Session shows in place of its chrome
// and terminal. It owns its own dedicated Host tunnel; hiding the terminal
// does not touch the Session's transport. The panel is closed with the
// Session surface, and its Close control calls the onClose callback the
// Session supplied.
type BrowserPanel interface {
	View(*ui.Context) ui.Element
	Close()
}

// ScreenWriter saves text and reports the path it wrote, or an error.
type ScreenWriter func(text string) (string, error)

// ScreenRect is one screen of a desktop a Tab arranges to match its Panes.
// Index is the screen the Pane shows; Rect is the Pane's rectangle in window
// pixels. The Tab normalizes the rectangles before asking the remote.
type ScreenRect struct {
	Index int
	Rect  image.Rectangle
}

// ScreenLayoutHost is implemented by a Surface that shows one screen of a
// desktop shared by the Tab's Panes. The remote has one desktop, so a single
// Pane cannot size it for everyone: the Tab gathers every Pane's rectangle,
// sends one request through one Pane, and the server applies that layout to
// the desktop all Panes share. Driving it per Pane would make two Panes
// compete for the same desktop size.
type ScreenLayoutHost interface {
	// ScreenGeometry reports the screen index this Surface shows and the Pane
	// rectangle of its last draw, in window pixels. ok is false before the
	// first draw.
	ScreenGeometry() (index int, rect image.Rectangle, ok bool)
	// LayoutScreens asks the remote for exactly these screens, one per Pane.
	// The request runs off the UI thread and the Surface records its outcome:
	// a remote that does not adopt the layout falls back to local scaling.
	// It reports whether this Pane took the request: a hibernated, view-only,
	// or not-yet-ready Pane returns false so the Tab can drive through another
	// Pane. A layout the remote already reports, or one already requested, is
	// taken (true) without sending.
	LayoutScreens(screens []ScreenRect) bool
	// SetScreenGeometryListener installs the callback draw invokes, on the UI
	// thread, when the Pane rectangle changes or the remote's layout does.
	SetScreenGeometryListener(fn func())
}

// PaneActions are the Pane commands the Session menu offers. The shell
// supplies them for a Pane in a split-capable Tab; a detached Pane leaves
// them nil and the items show disabled.
type PaneActions struct {
	SplitRight func()
	SplitDown  func()
	Detach     func()
	Close      func()
	// Rename sets the Pane's Tab title to the trimmed, non-empty argument.
	Rename func(title string)
	// Reconnect reopens a failed Pane's Session from its descriptor. It is
	// wired for kinds whose surface offers a manual reconnect; nil leaves the
	// action unavailable.
	Reconnect func()
	// ToggleFiles opens or closes the Tab's side panel on this Pane's Host;
	// FilesOpen reports that it is open. Nil hides the toolbar's side panel
	// button. FilesIcon is that button's glyph, the panel's docked edge; empty
	// draws the left one.
	ToggleFiles func()
	FilesOpen   bool
	FilesIcon   string
	// Title is the Pane's current title. MenuLabel is the accessible name of
	// this Pane's Session menu, unique among the Panes of one window.
	Title     string
	MenuLabel string
}

// Option customizes Surface options.
type Option func(*Options)

// WithLogDefaults supplies the logging destination and timestamp policy used
// by the Session's Start log control.
func WithLogDefaults(fn func() appsession.LogOptions) Option {
	return func(options *Options) { options.LogDefaults = fn }
}

// WithLogOpener supplies the platform opener for a Session's current log file.
func WithLogOpener(fn func(path string)) Option {
	return func(options *Options) { options.OpenLog = fn }
}

// WithRedraw supplies the window invalidation used for asynchronous clipboard
// requests, paste confirmation, and drop feedback.
func WithRedraw(fn func()) Option {
	return func(options *Options) { options.Redraw = fn }
}

// WithPastePolicy supplies the live multiline paste setting.
func WithPastePolicy(fn func() (bool, int)) Option {
	return func(options *Options) { options.PastePolicy = fn }
}

// WithDropAction supplies the live drop action setting.
func WithDropAction(fn func() domainterminal.DropAction) Option {
	return func(options *Options) { options.DropAction = fn }
}

// WithUpload supplies the upload seam a later ticket implements.
func WithUpload(fn UploadFunc) Option {
	return func(options *Options) { options.Upload = fn }
}

// WithSaveScreen supplies the screen writer. The zero value writes to the
// user's Downloads directory (or the temporary directory).
func WithSaveScreen(fn ScreenWriter) Option {
	return func(options *Options) { options.SaveScreen = fn }
}

// WithReconnect supplies the immediate-retry seam and its availability test.
func WithReconnect(attempt func(), available func() bool) Option {
	return func(options *Options) {
		options.Reconnect = attempt
		options.Reconnectable = available
	}
}

// WithYmodem supplies the YMODEM transfer seams. A later transfer ticket
// implements them; nil leaves both menu items disabled.
func WithYmodem(send, receive func()) Option {
	return func(options *Options) {
		options.SendYmodem = send
		options.ReceiveYmodem = receive
	}
}

// WithEncodingChange observes a live encoding choice so the app can persist
// it for the Session's scope.
func WithEncodingChange(fn func(domainterminal.Encoding)) Option {
	return func(options *Options) { options.OnEncoding = fn }
}

// WithSuggestions supplies the autocomplete provider and the live popup/ghost
// switches. The provider receives the Session's scope and its last reported
// working directory.
func WithSuggestions(suggest func(input, scope, cwd string) []autocomplete.Suggestion,
	display func() (popup, ghost bool)) Option {
	return func(options *Options) {
		options.Suggest = suggest
		options.AutocompleteDisplay = display
	}
}

// WithPromptSuggestions supplies the provider used at a tracked prompt: it
// receives whether a non-shell program is waiting for input, so it can add
// Vault Snippets there while leaving shell completions history-only.
func WithPromptSuggestions(suggest func(input, scope, cwd string, program bool) []autocomplete.Suggestion) Option {
	return func(options *Options) {
		options.SuggestAtPrompt = suggest
	}
}

// WithAutocompleteDelay supplies the debounce applied after the typed line
// changes.
func WithAutocompleteDelay(fn func() int) Option {
	return func(options *Options) { options.AutocompleteDelay = fn }
}

// WithTerminalFont supplies the live terminal font a new terminal uses.
func WithTerminalFont(fn func() terminal.Font) Option {
	return func(options *Options) { options.Font = fn }
}

// WithTerminalCursor supplies the live cursor shape and blink preference a new
// terminal uses.
func WithTerminalCursor(fn func() (domainpreferences.CursorStyle, bool)) Option {
	return func(options *Options) { options.Cursor = fn }
}

// WithShiftEnterNewline supplies the live Shift+Enter encoding preference. A
// nil reader leaves Shift+Enter to MyGo's own encoding.
func WithShiftEnterNewline(fn func() bool) Option {
	return func(options *Options) { options.ShiftEnterNewline = fn }
}

// WithTerminalTheme supplies the live terminal color scheme a new terminal
// uses.
func WithTerminalTheme(fn func() domaintheme.TerminalTheme) Option {
	return func(options *Options) { options.Theme = fn }
}

// WithTerminalKeybinds supplies the active bindings that act inside the
// terminal. A change applies to terminals opened after it, because MyGo
// resolves a terminal's bindings when it is created.
func WithTerminalKeybinds(fn func() []terminal.Keybind) Option {
	return func(options *Options) { options.Keybinds = fn }
}

// WithRawTerminal supplies the global raw terminal display mode.
func WithRawTerminal(fn func() bool) Option {
	return func(options *Options) { options.RawTerminal = fn }
}

// WithView binds the Surface to a connection the caller already claimed. A
// resumed Session passes the ResumedView returned by Session.Resume.
func WithView(conn io.ReadWriteCloser) Option {
	return func(options *Options) { options.View = conn }
}

// WithBrowser supplies the opener for this Session's browser panel. A Session
// without a saved Host (or with a transport that cannot forward) omits it, so
// the menu item stays disabled instead of failing after a click.
func WithBrowser(open func(onClose func()) (BrowserPanel, error)) Option {
	return func(options *Options) { options.Browser = open }
}

// terminalScrollback is how much output the terminal keeps above its screen.
// The Shell's boundary mirror keeps the same amount, so its absolute rows and
// the visible terminal's replay rows name the same line: a prompt row recorded
// at OSC 133;B stays valid while the screen scrolls under it.
const terminalScrollback = 256 << 10

// Surface is one Tab's terminal view.
type Surface struct {
	session   *appsession.Session
	terminal  *terminal.Terminal
	flow      *terminalFlow
	options   Options
	closeOnce sync.Once

	// liveSelection is the text selected by dragging over the live screen. It
	// lives here because the Pane is rebuilt every frame; see
	// appview.Selection for why MyGo's paragraph selection cannot serve it.
	liveSelection appview.Selection
	// liveHover is the printed link Command is held over on the live screen.
	// The Pane writes it, because it draws the grid and owns the pointer
	// mapping; it lives here for the same reason the selection does.
	liveHover appview.Hover

	gate             *PasteGate
	drops            *DropHandler
	mu               sync.Mutex
	pendingClipboard *terminal.ClipboardRequest

	// UI-thread state.
	logFormat    domainterminal.LogFormat
	logError     bool
	dropOver     bool
	screenNotice ScreenNotice
	renaming     bool
	renameValue  string
	paneActions  PaneActions
	// terminalKey names this Surface's terminal in the element tree. It is
	// unique per Session so two Tabs never share one node's keyboard, editor,
	// or focus state.
	terminalKey string
	// inputClient is the live screen's connection to the system's input
	// methods. It outlives the frames and the terminal itself: MyGo ends a
	// composition when the client changes, and Hibernate/Resume replaces the
	// terminal the client sends to.
	inputClient *appview.InputClient
	flowScroll  ui.ScrollState
	flowFollow  appview.Follow
	flowRipple  terminaleffects.Ripple
	flowCursor  terminal.CursorStyle
	flowBlink   bool
	flowBells   int64
	// focused is whether the Pane holding this Surface held the keyboard on
	// the last frame. focusPending asks the next terminalView for a one-shot
	// Focus when the Pane gained focus while its terminal was already shown.
	focused          bool
	focusPending     bool
	focusInitialized bool

	focusManaged bool
	focusEpoch   uint64
	// focusRetryFrames covers the short native-window focus transition when a
	// Local Terminal is opened from Vault or a work Tab becomes visible. It is
	// deliberately bounded; steady frames never keep calling Focus.
	focusRetryFrames    uint8
	flowSeen            bool
	flowCount           int
	flowLastID          int
	flowLastExit        bool
	terminalLiveFocused bool
	// terminalOwnView tells that the last frame was drawn by the terminal's own
	// view: that view sizes the terminal, so the pane's grid must not.
	terminalOwnView bool
	// viewport is this Pane's own room for the live screen, reported by
	// appview.Pane from its flow's layout. It is per Surface (one Pane), so a
	// sibling Pane can never overwrite it.
	viewport appview.Viewport
	// grid is the grid for viewport. gridViewport and the cell size record
	// what it was computed from, so a steady frame does no grid math and a
	// font or DPI change still recomputes it.
	grid         appview.Size
	gridViewport appview.Viewport
	gridCellW    float32
	gridCellH    float32
	gridKnown    bool
	// pinViewport is the final room the motion runtime gave this Pane at a
	// layout change (PinViewport). While viewportPinned is set, noteViewport
	// ignores the previous-frame box the Pane's own layout reports, which lags
	// the change, until the reported room matches the pin. The grid is
	// therefore set once at the change and never per animation frame.
	pinViewport    appview.Viewport
	viewportPinned bool
	// terminalSawAlt tells that an alternate screen was drawn since a program
	// placed its pictures; the frame that leaves that screen clears them.
	terminalSawAlt bool
	focusRequests  uint64
	// browser replaces the Session chrome and terminal while open. The
	// Session and its transport keep running underneath.
	browser    BrowserPanel
	browserErr error

	// Hibernate replaces the live terminal with a resumable screen while the
	// application Session and its transport stay alive. The observer is a
	// read-only shadow terminal fed by the application's Snapshot/Feed facade;
	// it never claims a live view.
	hibernated  bool
	observing   bool
	observer    *terminal.Terminal
	observation *appsession.Observation

	// Autocomplete state, refreshed once per frame from the Session's typed
	// line. locale is the frame's language; a change invalidates the cached
	// suggestions so their descriptions follow a live language switch.
	locale              string
	suggestions         []autocomplete.Suggestion
	suggestionIndex     int
	suggestionInput     string
	suggestionLocale    string
	suggestionProgram   bool
	suggestionPending   time.Time
	suggestionDismissed bool
	suggestionTimer     *time.Timer
	showAutocomplete    bool
	showGhost           bool
	// programPrompt reports that the last refresh found a non-shell program
	// waiting for ordinary input. Acceptance uses it to replace only the typed
	// query instead of the whole shell line.
	programPrompt bool
}

// New connects a MyGo terminal to session's TerminalConn. It returns an error
// when the terminal plugin's native library is unavailable.
func New(session *appsession.Session, optionList ...Option) (*Surface, error) {
	options := Options{}
	for _, option := range optionList {
		option(&options)
	}
	surface := &Surface{
		session:     session,
		options:     options,
		logFormat:   domainterminal.DefaultLogFormat,
		terminalKey: "session-terminal-" + string(session.ID()),
	}
	surface.flowCursor, surface.flowBlink = surface.terminalCursor()
	surface.flow = newTerminalFlow(session, options.Redraw)
	// The rows this app draws itself (the live grid, a block's prompt and plain
	// cells, a lens view) are terminal text, so they use the terminal's own
	// configured font. A Surface with no font option reports the empty font,
	// which resets the shared setting to the fallback.
	font := surface.terminalFont()
	terminalsurface.SetTerminalFont(font.Family, font.Size)
	surface.flow.setPromptTheme(surface.terminalTheme())
	if options.LogDefaults != nil {
		if defaults := options.LogDefaults(); validLogFormat(defaults.Format) {
			surface.logFormat = defaults.Format
		}
	}
	snapshot := session.Snapshot()
	if snapshot.Logging.Active && validLogFormat(snapshot.Logging.Format) {
		surface.logFormat = snapshot.Logging.Format
	}
	surface.gate = NewPasteGate(options.PastePolicy, func(text string) {
		if surface.terminal != nil {
			surface.terminal.Paste(text)
		}
	})
	surface.drops = NewDropHandler(options.DropAction, func(text string) {
		_, _ = session.TerminalConn().Write([]byte(text))
	}, options.Upload)

	conn := options.View
	claimed := false
	if conn == nil {
		view, err := session.AttachView()
		if err != nil {
			return nil, err
		}
		conn = view
		claimed = true
	}
	if err := surface.attach(conn); err != nil {
		if claimed {
			_ = conn.Close()
		}
		return nil, err
	}
	return surface, nil
}

// attach builds MyGo's terminal over conn. The application's tap is the single
// authority for OSC 52: it removes rejected writes and reads before the
// terminal sees them, and applies the live Settings policy. MyGo must
// therefore stay permissive here, and this callback applies what the Session
// currently allows. Reading the policy per request is what makes a Settings
// change affect live Sessions.
func (s *Surface) attach(conn io.ReadWriteCloser) error {
	// A mosh Session's peer owns the screen size. ResizeByConn makes the view
	// report the size it fits through Conn without resizing MyGo's screen;
	// the Session then applies the server's authoritative size through
	// SetScreenSizeSink before feeding the bytes encoded for it.
	remoteScreen := s.session.ScreenSizeFromRemote()
	cursor, blink := s.terminalCursor()
	options := terminal.Options{
		Conn:             conn,
		Scrollback:       terminalScrollback,
		Font:             s.terminalFont(),
		Theme:            s.terminalTheme(),
		Keybinds:         s.terminalKeybinds(),
		Cursor:           cursor,
		NoBlink:          !blink,
		ResizeByConn:     remoteScreen,
		NoClipboardWrite: false,
		ClipboardRead:    terminal.ClipboardAsk,
		OnClipboardRequest: func(request terminal.ClipboardRequest) {
			switch access := s.session.ClipboardAccess(); {
			case access.ReadsAllowed():
				request.Allow()
			case access.AsksOnRead():
				s.requestClipboard(request)
			default:
				request.Deny()
			}
		},
		// Every paste path that reaches the terminal passes the policy here.
		// A guarded paste is consumed and stored; nothing reaches the program
		// until the surface's Send action calls Gate.Accept.
		OnPaste: func(data transfer.Data) bool {
			text, err := data.Read(transfer.Text)
			if err != nil || len(text) == 0 {
				return false
			}
			s.requestPaste(string(text))
			return true
		},
		OnKey: s.handleTerminalKey,
		// Printed archive names underline and Command+click fills their
		// extraction command. Returning false leaves URLs to MyGo.
		OpenLink: s.openArchiveLink,
	}
	term, err := terminal.New(options)
	if err != nil {
		return err
	}
	if remoteScreen {
		if sink, ok := conn.(interface{ SetScreenSizeSink(func(cols, rows int)) }); ok {
			sink.SetScreenSizeSink(term.Resize)
		}
	}
	s.terminal = term
	// The selection named cells of the terminal that was showing; Resume (or a
	// reattach) brings a different screen and the rows no longer name it.
	s.liveSelection.Clear()
	s.liveHover = appview.Hover{}
	if s.flow != nil {
		s.flow.bindBellTracker(term)
	}
	return nil
}

// handleTerminalKey runs before MyGo encodes a key. Shift+Enter is taken only
// when the Settings preference asks for the legacy ESC CR newline; every other
// key, including plain Enter and Shift+Enter with the preference off, falls
// through to MyGo's own encoding. The autocomplete popup follows.
func (s *Surface) handleTerminalKey(mods ui.Modifiers, key ui.Key) bool {
	if mods == ui.Shift && key == ui.KeyEnter {
		newline := s.options.ShiftEnterNewline != nil && s.options.ShiftEnterNewline()
		if data := appsession.ShiftEnterNewlineData(newline); data != nil {
			if s.terminal != nil {
				// Send keeps the escape sequence in order with the text
				// typed around it, as the chosen key's encoding would have.
				s.terminal.Send(data)
			}
			return true
		}
	}
	return s.handleAutocompleteKey(mods, key)
}

// HoveredLink reports the link Command (Control elsewhere) is held over in
// the live screen: what it opens, whether it is a file path, and the cells in
// DIPs relative to the terminal view. The pane's cell grid resolves it (see
// appview.Hover); a full-screen program that the plugin's own view draws
// reports its own.
func (s *Surface) HoveredLink() (link string, path bool, at ui.Rect) {
	if s == nil {
		return "", false, ui.Rect{}
	}
	if s.liveHover.Link != "" {
		return s.liveHover.Link, s.liveHover.Path, s.liveHover.At
	}
	if s.terminal != nil {
		return s.terminal.HoveredLink()
	}
	return "", false, ui.Rect{}
}

func (s *Surface) terminalFont() terminal.Font {
	if s.options.Font == nil {
		return terminal.Font{}
	}
	return s.options.Font()
}

// terminalCursor maps the persisted cursor preference to MyGo's vocabulary.
// A missing reader keeps the original's blinking block cursor.
func (s *Surface) terminalCursor() (terminal.CursorStyle, bool) {
	if s.options.Cursor == nil {
		return terminal.CursorBlock, true
	}
	style, blink := s.options.Cursor()
	switch style {
	case domainpreferences.CursorBar:
		return terminal.CursorBar, blink
	case domainpreferences.CursorUnderline:
		return terminal.CursorUnderline, blink
	default:
		return terminal.CursorBlock, blink
	}
}

// cursorSequence is the DECSCUSR sequence for a cursor shape and blink
// preference. Odd shapes blink and even ones are steady.
func cursorSequence(style domainpreferences.CursorStyle, blink bool) string {
	shape := 1
	switch style {
	case domainpreferences.CursorBar:
		shape = 5
	case domainpreferences.CursorUnderline:
		shape = 3
	}
	if !blink {
		shape++
	}
	return "\x1b[" + strconv.Itoa(shape) + " q"
}

func (s *Surface) terminalTheme() *terminal.Theme {
	if s.options.Theme == nil {
		return nil
	}
	preset := s.options.Theme()
	if preset.ID == "" {
		return nil
	}
	return uitheme.Terminal(preset)
}

func (s *Surface) terminalKeybinds() []terminal.Keybind {
	if s.options.Keybinds == nil {
		return nil
	}
	return s.options.Keybinds()
}

// terminalKeybindList is the terminal's effective bindings: MyGo's platform
// defaults overlaid with the app's, in the same order and with the same
// replace-or-unbind rule the terminal plugin applies (ActionNone removes the
// default of its chord).
func terminalKeybindList(binds []terminal.Keybind) []terminal.Keybind {
	merged := terminal.DefaultKeybinds()
	for _, k := range binds {
		kept := merged[:0]
		for _, o := range merged {
			if o.Mods != k.Mods || o.Key != k.Key {
				kept = append(kept, o)
			}
		}
		merged = kept
		if k.Action != terminal.ActionNone {
			merged = append(merged, k)
		}
	}
	return merged
}

// handlePasteChord runs a key bound to ActionPaste before the pane encodes it.
// The drawn grid does not go through the terminal plugin's view, so a paste
// chord used to be sent to the shell as its raw bytes (Ctrl+Shift+V became
// 0x16, literal-next). A performable binding whose action cannot run — an
// empty clipboard — falls through to the program, as the plugin's view does.
func (s *Surface) handlePasteChord(c *ui.Context, mods ui.Modifiers, key ui.Key) bool {
	for _, bind := range terminalKeybindList(s.terminalKeybinds()) {
		if bind.Action != terminal.ActionPaste || bind.Mods != mods || bind.Key != key {
			continue
		}
		if text := c.Services().ReadClipboard(); text != "" {
			s.requestPaste(text)
			return true
		}
		return !bind.Performable
	}
	return false
}

// SetTerminalFont restyles a live terminal without recreating its Session. It
// is what applies the Appearance settings while Sessions are open.
func (s *Surface) SetTerminalFont(family string, size float32) {
	font := terminal.Font{Family: family, Size: size}
	if s.terminal != nil {
		s.terminal.SetFont(font)
	}
	if s.observer != nil {
		s.observer.SetFont(font)
	}
	// The app's own terminal rows (live grid, finished command rows, lens
	// views) follow the same setting, so text does not change font when it
	// moves out of the terminal plugin's grid into a block.
	terminalsurface.SetTerminalFont(family, size)
}

// SetTerminalCursor applies a cursor shape and blink change to a live terminal
// without recreating the Session. MyGo reads the cursor options only when a
// terminal is built, so the running terminal receives the DECSCUSR a program
// would send. A program that sets its own cursor later still wins, as it does
// in MyGo's default-cursor model.
func (s *Surface) SetTerminalCursor(style domainpreferences.CursorStyle, blink bool) {
	sequence := []byte(cursorSequence(style, blink))
	s.flowCursor, s.flowBlink = terminalCursorStyle(style, blink)
	if s.terminal != nil {
		s.terminal.Feed(sequence)
	}
	if s.observer != nil {
		s.observer.Feed(sequence)
	}
}

func terminalCursorStyle(style domainpreferences.CursorStyle, blink bool) (terminal.CursorStyle, bool) {
	switch style {
	case domainpreferences.CursorBar:
		return terminal.CursorBar, blink
	case domainpreferences.CursorUnderline:
		return terminal.CursorUnderline, blink
	default:
		return terminal.CursorBlock, blink
	}
}

// SetTerminalTheme recolors a live terminal and its observer without
// recreating the Session.
func (s *Surface) SetTerminalTheme(preset domaintheme.TerminalTheme) {
	theme := uitheme.Terminal(preset)
	if s.terminal != nil {
		s.terminal.SetTheme(theme, nil)
	}
	if s.observer != nil {
		s.observer.SetTheme(theme, nil)
	}
}

func validLogFormat(format domainterminal.LogFormat) bool {
	switch format {
	case domainterminal.LogFormatText, domainterminal.LogFormatRaw, domainterminal.LogFormatHTML:
		return true
	default:
		return false
	}
}

// View shows the Session chrome above its terminal without asking for the
// keyboard. The shell uses ViewFocused so only the focused Pane's terminal
// takes the keyboard.
func (s *Surface) View(c *ui.Context) ui.Element {
	return s.view(c, false, false)
}

// ViewFocused shows the Session and, when focused, gives its terminal the
// keyboard. Only the shell's focused Pane sets focused, so several Panes in
// one frame do not fight over it: the terminal takes the keyboard in the frame
// its element first appears (a new Tab, a Tab shown again after a switch, or
// Resume after Hibernate) and once when its Pane gains focus while already
// shown. It never reclaims the keyboard on later frames, so Compose, a dialog,
// or another control keeps the focus the user gave it.
func (s *Surface) ViewFocused(c *ui.Context, focused bool) ui.Element {
	return s.view(c, focused, true)
}

// ViewInert draws a still copy of the Session for a Pane-motion overlay. It
// takes no keyboard focus and requests none, does not resize the terminal, and
// leaves every focus field of the live Surface untouched. The real Pane keeps
// the live View under the overlay's opaque mask, so typing and the pointer keep
// reaching it while the copy moves.
func (s *Surface) ViewInert(c *ui.Context) ui.Element {
	if s == nil || s.session == nil {
		return ui.Column(c).Fill()
	}
	t := c.Theme()
	if s.browser != nil {
		// A native page must not be built twice; the moving copy needs only
		// the Pane's shape.
		return ui.Column(c).Fill().Background(t.Background)
	}
	m := locales.In(c.Locale())
	snapshot := s.session.Snapshot()
	model, actions := s.chrome(c, snapshot)
	return ui.Column(c).Fill().MinHeight(0).Children(func() {
		renderChrome(c, model, m, t, actions)
		switch {
		case s.observing && s.observer != nil:
			// The observer's native terminal view stays live in the real
			// Pane; the copy keeps its shape without building it twice.
			ui.Column(c).Fill().Background(t.Background)
		case model.Hibernated || s.terminal == nil:
			s.hibernatedView(c, m, t)
		default:
			s.terminalViewInert(c)
		}
	})
}

// terminalViewInert replays the live terminal into an ordinary element tree
// without the live view's input, focus, selection, scroll, or resize paths.
// It uses a private scroll and follow state so the real Pane's follow and
// scroll position are untouched.
func (s *Surface) terminalViewInert(c *ui.Context) ui.Element {
	if s.flow == nil || s.terminal == nil {
		return ui.Column(c).Fill().MinHeight(0)
	}
	cols, rows := s.terminal.Size()
	size := appview.Size{Cols: cols, Rows: rows}
	promptRow, promptRowKnown := s.flow.promptBoundary(size)
	if s.options.RawTerminal != nil && s.options.RawTerminal() {
		promptRow, promptRowKnown = 0, false
	}
	tail := appview.Tail{
		PromptRow:          promptRow,
		PromptRowKnown:     promptRowKnown,
		PromptAfterCommand: s.flow.promptBoundaryAfterCommand(),
		PromptPending:      s.flow.promptBoundaryPending(),
		ScreenCleared:      s.flow.screenCleared(),
	}
	for _, block := range s.flow.store.Snapshot() {
		if block.Exited {
			continue
		}
		tail.Running = block.Cmd.Typed
		if tail.Running == "" {
			tail.Running = block.Cmd.Line
		}
	}
	terminalTheme := s.terminalTheme()
	cursorColor := ui.Color{}
	if terminalTheme != nil {
		cursorColor = terminalTheme.Cursor
	}
	// A copy of the position, never the live state: laying the copy out must
	// not move the flow the user is reading.
	var scroll ui.ScrollState
	scroll.X, scroll.Y = s.flowScroll.X, s.flowScroll.Y
	var follow appview.Follow
	var ripple terminaleffects.Ripple
	return ui.Column(c).Fill().MinHeight(0).Children(func() {
		appview.Pane(c, s.flow.store, s.flow.engine, s.terminal, size,
			s.flow.imagesSnapshot(), &scroll, &follow, &ripple,
			appview.Bell{Col: -1, Row: -1}, tail,
			appview.Cursor{Style: s.flowCursor, Color: cursorColor, Blink: false},
			terminalTheme,
			appview.Hooks{Key: inertTerminalKey{s}, PromptRows: s.flow.promptRowsFor}, nil)
	})
}

// inertTerminalKey identifies a still copy's live container. It is distinct
// from the real terminal's key so the two builds in one frame never share an
// element identity.
type inertTerminalKey struct{ surface *Surface }

// SetFocusEpoch tells a live Surface that its Tab became visible again. The
// self-drawn terminal uses this display epoch in its retained container key:
// command blocks can be inserted without losing focus, while a Tab switch
// still creates a fresh focus target when the old one is shown again.
func (s *Surface) SetFocusEpoch(epoch uint64) {
	if s != nil && epoch != s.focusEpoch {
		if s.focused || !s.focusInitialized {
			s.focusPending = true
			s.focusRetryFrames = 4
		}
		s.focusEpoch = epoch
	}
}

// NotifyWindowFocus is called once when the native window regains focus. If
// this Surface's terminal logically owned the keyboard before the blur,
// restore it with the same bounded retry used when a Tab becomes visible. The
// logical ownership is tracked independently of native window focus, because
// ui.Element.Focused() is intentionally false during the blur itself.
func (s *Surface) NotifyWindowFocus() {
	if s == nil || !s.focusManaged || !s.focused || !s.terminalLiveFocused {
		return
	}
	s.focusPending = true
	s.focusRetryFrames = 4
}

func (s *Surface) view(c *ui.Context, focused, explicitFocus bool) ui.Element {
	if explicitFocus && !focused {
		s.focusPending = false
	}
	becameFocused := explicitFocus && focused && !s.focused
	s.focusPending = s.focusPending || becameFocused ||
		(!explicitFocus && !s.focusInitialized)
	if becameFocused {
		s.focusRetryFrames = 4
	}
	s.focused = focused
	s.focusInitialized = true
	s.focusManaged = explicitFocus
	// A dismissed observer stops rendering immediately; its feed and shadow
	// terminal are released before the next frame builds anything over them.
	if !s.observing && s.observation != nil {
		s.releaseObserver()
	}
	// The browser owns the whole surface while open: its native page must not
	// sit under trusted chrome, and the terminal keeps running behind it. The
	// Session menu's Close control is the browser panel's own close button.
	if s.browser != nil {
		return s.browser.View(c)
	}
	m := locales.In(c.Locale())
	s.locale = c.Locale()
	t := c.Theme()
	snapshot := s.session.Snapshot()
	s.refreshSuggestions(snapshot.Input, snapshot.CWD, snapshot.Command.Running)
	model, actions := s.chrome(c, snapshot)

	// FillHeight sizes the surface to the room its Pane gives it, but a
	// column's automatic minimum is its content's: with a live grid taller
	// than the window (the flow grows a row per prompt), that minimum became
	// the Pane's whole height and the surface could not shrink by the header
	// above it. The flow then laid out below the window, where neither follow
	// nor the wheel could reach it. MinHeight(0) lets the Pane's header take
	// its row out of the surface, as bodyRow does for the window's body.
	return ui.Column(c).Fill().MinHeight(0).Children(func() {
		if s.browserErr != nil {
			ui.Row(c).FillWidth().Padding(6, 8).Gap(8).AlignItems(ui.Center).
				Background(t.Surface).Children(func() {
				ui.Text(c, m.BrowserOpenFailed()).FontSize(kit.M(c).Font.Caption).Grow(1).
					MinWidth(0).SingleLine().TextColor(uitheme.DangerText(t))
				if ui.Button(c, m.SessionDismiss()).Clicked() {
					s.browserErr = nil
					s.redraw()
				}
			})
		}
		renderChrome(c, model, m, t, actions)
		switch {
		case s.observing && s.observer != nil:
			s.observerView(c, m, t)
		case model.Hibernated || s.terminal == nil:
			s.hibernatedView(c, m, t)
		default:
			// Terminal's finished command rows and inline images come first in the
			// same visual flow; the live terminal is the last element, matching
			// mygo-flow's Pane layout.
			termView := s.terminalView(c)
			// Suggestions float over the terminal at the cursor cell. Building
			// them after the terminal gives the overlay the terminal's bounds,
			// and an overlay row never takes room from the grid.
			if (model.ShowAutocomplete || model.ShowGhost) && s.terminal != nil {
				cols, rows := s.terminal.Size()
				cursor := autocompleteCursorFromText(s.terminal.Text(), model.Input, cols, rows)
				autocompleteLayers(c, termView, model, cursor, cols, rows, m, t, actions)
			}
		}
		if s.renaming {
			s.renameDialog(c, m, t)
		}
	})
}

// chrome builds what the Session's chrome and its Pane toolbar show from one
// snapshot, so the header's menu and the banners under it agree.
func (s *Surface) chrome(c *ui.Context, snapshot appsession.Snapshot) (chromeModel, chromeActions) {
	if snapshot.Logging.Active && validLogFormat(snapshot.Logging.Format) {
		s.logFormat = snapshot.Logging.Format
	}
	hibernated := s.hibernated || snapshot.Hibernated
	directory := ""
	if s.options.LogDefaults != nil {
		directory = s.options.LogDefaults().Directory
	}
	s.mu.Lock()
	clipboardPending := s.pendingClipboard != nil
	s.mu.Unlock()

	var pending *PendingPaste
	if waiting, ok := s.gate.Pending(); ok {
		copy := waiting
		pending = &copy
	}
	dropNotice, dropCount := s.drops.Notice()
	dropAction := domainterminal.DropInsert
	if s.options.DropAction != nil {
		if configured := s.options.DropAction(); configured.Valid() {
			dropAction = configured
		}
	}
	clipboardText := c.ReadClipboard()
	canReconnect := s.options.Reconnect != nil
	if canReconnect && s.options.Reconnectable != nil {
		canReconnect = s.options.Reconnectable()
	}
	ghostText := ""
	if s.showGhost && len(s.suggestions) > 0 {
		ghostText = s.suggestions[0].Text
	}

	var renameAction func()
	if s.paneActions.Rename != nil {
		renameAction = s.beginRename
	}
	model := chromeModel{
		Command:          snapshot.Command,
		Busy:             snapshot.Busy,
		Transfer:         snapshot.Transfer,
		Logging:          snapshot.Logging,
		ClipboardPending: clipboardPending,
		LogFormat:        s.logFormat,
		LogDirectory:     directory,
		LogProblem:       s.logError || snapshot.Logging.Problem != "",
		Encoding:         snapshot.Encoding,
		PendingPaste:     pending,
		DropActive:       s.dropOver,
		DropCount:        dropCount,
		DropAction:       dropAction,
		DropNotice:       dropNotice,
		ScreenNotice:     s.screenNotice,
		MenuLabel:        s.paneActions.MenuLabel,
		CanPaste:         clipboardText != "",
		CanReconnect:     canReconnect,
		Input:            snapshot.Input,
		Suggestions:      append([]autocomplete.Suggestion(nil), s.suggestions...),
		SuggestionIndex:  s.suggestionIndex,
		ShowAutocomplete: s.showAutocomplete,
		ShowGhost:        s.showGhost,
		GhostText:        ghostText,
		Hibernated:       hibernated,
		FilesOpen:        s.paneActions.FilesOpen,
		FilesIcon:        s.paneActions.FilesIcon,
	}
	var saveScreen, clearBuffer func()
	if !hibernated && s.terminal != nil {
		saveScreen = func() { s.saveScreen() }
		clearBuffer = s.clearBuffer
	}
	var openLog func(path string)
	if s.options.OpenLog != nil {
		openLog = s.openLog
	}
	actions := chromeActions{
		ToggleLog: func() { s.toggleLog() },
		OpenLog:   openLog,
		SetLogFormat: func(format domainterminal.LogFormat) {
			s.setLogFormat(format)
		},
		AllowClipboard: func() { s.answerClipboard(true) },
		DenyClipboard:  func() { s.answerClipboard(false) },
		CancelTransfer: func() { s.session.CancelTransfer() },
		SetEncoding:    func(encoding domainterminal.Encoding) { s.setEncoding(encoding) },
		Paste: func() {
			if clipboardText != "" {
				s.requestPaste(clipboardText)
			}
		},
		Reconnect:     s.options.Reconnect,
		SendYmodem:    s.options.SendYmodem,
		ReceiveYmodem: s.options.ReceiveYmodem,
		SaveScreen:    saveScreen,
		ClearBuffer:   clearBuffer,
		Rename:        renameAction,
		AcceptPaste: func() {
			s.gate.Accept()
			s.redraw()
		},
		CancelPaste: func() {
			s.gate.Cancel()
			s.redraw()
		},
		DismissDrop: func() {
			s.drops.Dismiss()
			s.redraw()
		},
		DismissScreen: func() {
			s.screenNotice = ScreenNoticeNone
			s.redraw()
		},
		SplitRight:  s.paneActions.SplitRight,
		SplitDown:   s.paneActions.SplitDown,
		Detach:      s.paneActions.Detach,
		ClosePane:   s.paneActions.Close,
		Hibernate:   func() { s.Hibernate() },
		Resume:      func() { _ = s.Resume() },
		Observe:     func() { _ = s.OpenObserver() },
		OpenBrowser: s.openBrowserAction(),
		AcceptSuggestion: func(index int) {
			s.acceptSuggestion(index)
		},
		ToggleFiles: s.paneActions.ToggleFiles,
	}
	return model, actions

}

// Toolbar renders the Session's controls for its Pane header: Files (SFTP),
// the two splits, and the Session menu. The shell places it at the header's
// right edge, so the terminal itself starts right under the header.
func (s *Surface) Toolbar(c *ui.Context) {
	model, actions := s.chrome(c, s.session.Snapshot())
	toolbar(c, model, locales.In(c.Locale()), c.Theme(), actions)
}

// clearBuffer is the Session menu's "Clear blocks" action. It keeps the
// Session transport alive while matching mygo-flow's visible behavior: clear
// the flow, reset its unified scroll, and clear the self-drawn terminal.
func (s *Surface) clearBuffer() {
	if s == nil {
		return
	}
	if s.flow != nil {
		s.flow.clear()
	}
	if s.terminal != nil {
		appview.ClearScreen(s.terminal)
		// The boundary mirror replays what the visible terminal shows. Clearing
		// the screen here leaves the mirror holding rows the visible replay no
		// longer has, so every later row it records counts from a different
		// first line: the next prompt's boundary landed below the live area and
		// the flow then drew a finished command both as its block and again as
		// raw rows. Seed it from the cleared terminal, as attach does.
		if s.flow != nil {
			s.flow.bindBellTracker(s.terminal)
		}
	}
	s.liveSelection.Clear()
	s.liveHover = appview.Hover{}
	s.flowScroll.Y = math.MaxFloat32
	s.flowFollow = appview.Follow{}
	if s.session != nil {
		s.session.ClearBuffer()
	}
}

// textInputClient is the live screen's one input client, made on first use.
// It sends committed input-method text to the terminal attached now, not to
// the one that existed when it was made: Resume attaches a new terminal.
func (s *Surface) textInputClient() ui.TextInputClient {
	if s.inputClient == nil {
		s.inputClient = appview.NewInputClient(func(b []byte) {
			if s.terminal != nil {
				s.terminal.Send(b)
			}
		})
	}
	return s.inputClient
}

// noteViewport records the room this Pane's live screen was laid out in. The
// Pane reports it from its own flow's scroll, so the grid follows this Pane's
// box and never a sibling's.
//
// The reported box is the previous frame's layout, so after a motion event it
// still names the old room for a frame. While the motion runtime has pinned
// the final room, that stale report is ignored; once the layout catches up
// and reports the pinned room, the pin is spent and later layout changes are
// accepted again.
func (s *Surface) noteViewport(w, h float32) {
	if w <= 0 || h <= 0 {
		return
	}
	if s.viewportPinned {
		if sameViewport(w, h, s.pinViewport) {
			// The report is the pinned room within layout rounding. Keep the
			// pinned values: the cached grid was computed for them, so the
			// arrival frame does no grid math and delivers no extra PTY size.
			s.viewportPinned = false
		}
		return
	}
	if sameViewport(w, h, s.viewport) {
		// Layout rounding alone does not move the room. Keeping the recorded
		// values keeps the cached grid valid, so a repeated report costs no
		// grid math.
		return
	}
	s.viewport = appview.Viewport{W: w, H: h}
}

// sameViewport reports that a reported box is the pinned one, allowing for
// sub-DIP layout rounding. Comparing the boxes keeps the check free of grid
// math, so an animation frame never recomputes a grid.
func sameViewport(w, h float32, want appview.Viewport) bool {
	const epsilon = 0.5
	return absf(w-want.W) <= epsilon && absf(h-want.H) <= epsilon
}

func absf(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

// MotionViewport reports the room this Pane's live screen currently gets, for
// a view that animates a split. It reports false before the Pane's first
// layout, when there is no box to shrink or grow from.
func (s *Surface) MotionViewport() (w, h float32, ok bool) {
	if s == nil || s.viewport.W <= 0 || s.viewport.H <= 0 {
		return 0, 0, false
	}
	return s.viewport.W, s.viewport.H, true
}

// PinViewport sets this Pane's final room and grid at a layout change, before
// the Pane's own layout reports it. The grid is computed once here and the
// PTY receives at most one size change; animation frames then draw the frame
// already painted at that grid (ADR 0026). A Session whose peer owns its
// screen size, or a full-screen program drawing its own view, is left alone:
// neither is sized by the Pane.
func (s *Surface) PinViewport(w, h float32) {
	if s == nil || s.terminal == nil || w <= 0 || h <= 0 {
		return
	}
	if s.session != nil && s.session.ScreenSizeFromRemote() {
		return
	}
	if s.terminalOwnView {
		return
	}
	s.viewport = appview.Viewport{W: w, H: h}
	s.pinViewport = s.viewport
	s.viewportPinned = true
	s.resizeTerminalToGrid()
}

// ReleaseViewport lets the Pane's own layout size the terminal again. It is
// called when a motion run ends, so a later window resize is not ignored.
func (s *Surface) ReleaseViewport() {
	if s == nil {
		return
	}
	s.viewportPinned = false
}

// resizeTerminalToGrid gives the terminal the grid its current viewport has
// room for. It is a no-op when the cached grid already matches, which is what
// keeps a steady frame free of grid math and PTY resizes.
func (s *Surface) resizeTerminalToGrid() {
	if s.terminal == nil {
		return
	}
	grid, ok := s.gridForViewport()
	if !ok {
		return
	}
	if cols, rows := s.terminal.Size(); cols != grid.Cols || rows != grid.Rows {
		s.terminal.Resize(grid.Cols, grid.Rows)
	}
}

// gridForViewport is the cell grid this Pane's terminal fits. The grid is
// recomputed only when the Pane's own viewport or the measured cell changed,
// so a steady frame does no grid math; a font or DPI change still reaches the
// terminal. terminal.Resize is told the same size and does nothing.
func (s *Surface) gridForViewport() (appview.Size, bool) {
	v := s.viewport
	if v.W <= 0 || v.H <= 0 {
		return appview.Size{}, false
	}
	cellW, cellH := appview.CellSize()
	if s.gridKnown && v == s.gridViewport && cellW == s.gridCellW && cellH == s.gridCellH {
		return s.grid, true
	}
	grid, ok := appview.GridFor(v)
	if !ok {
		return appview.Size{}, false
	}
	s.grid, s.gridViewport = grid, v
	s.gridCellW, s.gridCellH, s.gridKnown = cellW, cellH, true
	return grid, true
}

// terminalView shows the live terminal and returns its element, which anchors
// the floating autocomplete layers. It is the Session's one live view.
func (s *Surface) terminalView(c *ui.Context) ui.Element {
	if s.flow != nil && s.terminal != nil {
		if !s.session.ScreenSizeFromRemote() && !s.terminalOwnView {
			// A full-screen program is shown by MyGo's own view (see
			// appview.Live), which sizes the terminal to fit its box, with the
			// font metrics the terminal draws with. Sizing it here too, from the
			// pane's coarser grid, makes the two disagree by a cell and resize
			// the program twice every frame: it then repaints forever and its
			// image placeholders are erased before a frame can show them.
			if grid, ok := s.gridForViewport(); ok {
				if cols, rows := s.terminal.Size(); cols != grid.Cols || rows != grid.Rows {
					s.terminal.Resize(grid.Cols, grid.Rows)
				}
			}
		}
		cols, rows := s.terminal.Size()
		s.flow.setSize(cols, rows)
		blocks := s.flow.store.Snapshot()
		lastID, lastExited := -1, false
		if len(blocks) > 0 {
			last := blocks[len(blocks)-1]
			lastID, lastExited = last.ID, last.Exited
		}
		flowChanged := s.flowSeen &&
			(s.flowCount != len(blocks) ||
				s.flowLastID != lastID ||
				s.flowLastExit != lastExited)
		flowRefocus := flowChanged && s.terminalLiveFocused
		if flowChanged && s.terminalLiveFocused {
			// A finished Lens block changes the live terminal's parent flow.
			// Request one focus pass only when the terminal owned focus before
			// the change; Compose, menus and dialogs therefore keep control.
			s.focusPending = true
		}
		s.flowSeen = true
		s.flowCount, s.flowLastID, s.flowLastExit = len(blocks), lastID, lastExited
		images := s.flow.imagesSnapshot()
		terminalTheme := s.terminalTheme()
		// Prompt rows are captured from the mirror between frames; the scheme
		// in force is the one the shell is drawing with now.
		s.flow.setPromptTheme(terminalTheme)
		promptRow, promptRowKnown := s.flow.promptBoundary(appview.Size{Cols: cols, Rows: rows})
		if s.options.RawTerminal != nil && s.options.RawTerminal() {
			promptRow, promptRowKnown = 0, false
		}
		tail := appview.Tail{
			PromptRow:          promptRow,
			PromptRowKnown:     promptRowKnown,
			PromptAfterCommand: s.flow.promptBoundaryAfterCommand(),
			PromptPending:      s.flow.promptBoundaryPending(),
			ScreenCleared:      s.flow.screenCleared(),
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
		if terminalTheme != nil {
			cursorColor = terminalTheme.Cursor
		}
		bell := appview.Bell{Col: -1, Row: -1}
		if queued, ok := s.flow.takeBell(); ok {
			bell = queued
		}
		// Keep the Session counter as a safety net for output that arrived
		// before the private mirror could be bound (or when the native terminal
		// plugin is unavailable to it). The normal path above is exact-BEL.
		if count := s.session.Snapshot().Stats.Bells; count > s.flowBells {
			s.flowBells = count
			if !bell.Fired {
				bell.Fired = true
				if col, row, ok := appview.CursorCell(s.terminal.Snapshot(), appview.Size{Cols: cols, Rows: rows}); ok {
					bell.Col, bell.Row = col, row
				}
			}
		}
		// A newly created terminal can be built in the same frame that the
		// window is moving focus away from the Vault button.  In that frame
		// Focus() records the request, but Focused() can still be false because
		// the window focus has not settled.  Keep the one-shot request pending
		// until the terminal confirms focus; once it is confirmed this stops
		// immediately and steady frames do not reclaim focus.
		focus := s.focusPending || s.focusRetryFrames > 0 || !s.focusManaged
		focusRequested := focus
		if focus {
			s.focusRequests++
		}
		pane := appview.Pane(
			c,
			s.flow.store,
			s.flow.engine,
			s.terminal,
			appview.Size{Cols: cols, Rows: rows},
			images,
			&s.flowScroll,
			&s.flowFollow,
			&s.flowRipple,
			bell,
			tail,
			appview.Cursor{Style: s.flowCursor, Color: cursorColor, Blink: s.flowBlink},
			terminalTheme,
			appview.Hooks{
				Key:       s.terminalKey + "-" + strconv.FormatUint(s.focusEpoch, 10),
				TextInput: s.textInputClient(),
				Selection: &s.liveSelection,
				// The pane draws the live screen itself, so it resolves and
				// opens links too, with the same policy the plugin's view
				// gets from Options.OpenLink: an archive name fills its
				// extraction command, other file paths open nothing, and a
				// URL opens in the browser.
				OpenLink: s.openPaneLink,
				Hover:    &s.liveHover,
				// A finished block shows the prompt and input the shell drew,
				// captured at the command's OSC 133;C mark: the shell's own
				// prompt, not one this app could only approximate.
				PromptRows: s.flow.promptRowsFor,
				// The Pane reports the box it laid its live screen out in.
				// It is this Surface's own Pane, so the grid computed from it
				// cannot read a sibling's viewport.
				Viewport:  s.noteViewport,
				Focus:     focus,
				Refocus:   flowRefocus,
				KeepFocus: s.focused,
				// A press on a finished command's row or Lens body must not
				// leave the keyboard there. The block still selects text and
				// its Native/Raw switch still works; the Session asks for the
				// terminal's focus on the next frame so typing reaches the
				// shell again.
				Click: func() {
					// A press outside the live screen starts a new selection
					// there: keeping the old highlight over text the user is
					// no longer working with would copy the wrong cells.
					s.liveSelection.Clear()
					// Do not test terminalLiveFocused here: at pointer-down
					// time it is still the previous frame's value, which is
					// true because the terminal owned the keyboard a moment
					// ago, so the guard would skip the one press that needs
					// it. Request the terminal's focus and let the next build
					// (hooks.Focus) put it back on the input box.
					if s.focused {
						s.focusPending = true
						s.redraw()
					}
				},
				Focused: func(focused bool) {
					s.terminalLiveFocused = focused
				},
				Secondary: func() {
					// Right-clicking a block's text opens a text menu, which
					// focuses the read-only paragraph under the pointer. The
					// shell keeps the keyboard, as it does after a primary
					// press; the selection is left alone.
					if s.focused {
						s.focusPending = true
						s.redraw()
					}
				},
				// One menu for the whole terminal: the live grid, blank space,
				// finished blocks, raw cells and Lens bodies. The text views
				// prepend MyGo's editing items and pass no live actions.
				Menu: func(m *ui.Menu, acts appview.MenuActions) {
					model, actions := s.chrome(c, s.session.Snapshot())
					dir := s.Dir()
					var copyDir func()
					if dir != "" {
						copyDir = func() { c.Services().WriteClipboard(dir) }
					}
					buildTerminalMenu(m, model, locales.In(c.Locale()), acts, actions, copyDir)
				},
				OwnView: func(pictures, alt bool) {
					if pictures != s.terminalOwnView && s.focused {
						// The pane changed which element takes the keyboard:
						// a full-screen program that placed pictures is drawn
						// by the terminal's own view instead of the pane's
						// cell grid (and back again). The element that appears
						// starts without focus, so the keys typed after the
						// program opened went nowhere while the mouse still
						// worked. Ask for focus once, as a finished block does.
						s.focusPending = true
						s.focusRetryFrames = 4
					}
					s.terminalOwnView = pictures
					// Pictures are wanted until the program that placed them
					// leaves its alternate screen: a full-screen program sends
					// an image before the replay reports that screen, so a
					// frame in between is not a sign that it is done.
					if alt {
						s.terminalSawAlt = true
						return
					}
					if s.terminalSawAlt {
						s.terminalSawAlt = false
						s.flow.clearPictures()
					}
				},
				Images: s.flow.hasPictures(),
				Input: func(ev ui.InputEvent) bool {
					if ev.Kind == ui.InputCommand && ev.Text == "paste" {
						// The Edit menu's Paste reaches an input handler, outside
						// any frame. Context.ReadClipboard is frame-gated and
						// returns "" there, so the window's clipboard is read
						// through Services, which works at any time. requestPaste
						// (not Terminal.Paste) keeps this path on the same
						// multiline policy and line-ending normalization as the
						// Session menu and the plugin's own view.
						if text := c.Services().ReadClipboard(); text != "" {
							s.requestPaste(text)
						}
						return true
					}
					if ev.Kind == ui.InputKeyDown {
						// The drawn grid bypasses the terminal plugin's view,
						// so its paste chords (Ctrl+Shift+V, Shift+Insert, or a
						// user binding) are taken here; otherwise they reach
						// the shell as raw control bytes.
						if s.handlePasteChord(c, ev.Mods, ev.Key) {
							return true
						}
						// The pane builds the bytes the program reads, so the
						// same key hook the plugin's own view calls (OnKey)
						// runs before the pane's encoder. Shift+Enter's newline
						// and the autocomplete popup are taken here; every other
						// key falls through to the pane.
						return s.handleTerminalKey(ev.Mods, ev.Key)
					}
					return false
				},
				Drag: func(over bool) {
					if over != s.dropOver {
						s.dropOver = over
						s.redraw()
					}
				},
				Drop: func(files []string) {
					s.dropOver = false
					s.drops.Drop(files, s.windowsShell())
					s.redraw()
				},
			},
			// Copy runs from the menu and from handlers, outside the frame,
			// where Context.WriteClipboard is a no-op: Services reaches the
			// window's clipboard at any time.
			func(text string) { c.Services().WriteClipboard(text) },
		).Grow(1)
		if focusRequested && s.focusRetryFrames > 0 {
			// Keep retrying only during the bounded activation window. This
			// handles a native window-focus race without reclaiming focus on
			// steady frames or while the user edits another control.
			s.focusRetryFrames--
			s.focusPending = true
		} else if focusRequested {
			s.focusPending = false
		} else {
			s.focusPending = false
		}
		return pane
	}
	// Key the terminal so its node keeps the same identity when chrome
	// inserts or removes a row above it (statistics and banners). MyGo derives
	// unkeyed node ids from the child position, so an
	// inserted row above the terminal would move the keyboard focus to the new
	// row and the user's next keystroke would go nowhere. The key carries the
	// Session's identity so two Tabs never reuse one terminal node's state.
	//
	// The key must be set on the build context, before terminal.View reads the
	// focus state. Element.Key rekeys only after the terminal has built, so
	// the terminal would build against the unkeyed node every frame and never
	// see the focus it was given: its cursor would draw hollow and never blink,
	// even though input reached the keyed element.
	termView := terminal.View(c.Key(s.terminalKey), s.terminal).Grow(1)
	// AutoFocus fires only in the frame the terminal node appears: a new Tab,
	// a Tab shown again after a switch, and Resume after Hibernate. A Pane
	// that gains focus while its terminal is already shown needs the one-shot
	// Focus instead. Neither runs on a steady frame, so the keyboard the user
	// gave Compose or another control is not taken back.
	if s.focused {
		termView.AutoFocus()
		if s.focusPending {
			termView.Focus()
		}
	}
	s.focusPending = false
	// File drag/drop belongs to the terminal element. Reading it here marks
	// the terminal as the drop target and consumes a completed drop once; the
	// resulting banner appears on the next frame.
	if over := appview.FileDragOver(termView); over != s.dropOver {
		s.dropOver = over
		// Both entering and leaving change the banner, so redraw either way;
		// otherwise the drop hint would stay after the pointer left.
		s.redraw()
	}
	if files := appview.DroppedFiles(termView); len(files) > 0 {
		s.dropOver = false
		s.drops.Drop(files, s.windowsShell())
		s.redraw()
	}
	return termView
}

// hibernatedView replaces the live screen with the Session's resumable state.
// The Session and its transport keep running; Resume reattaches the one live
// view, and the observer can still open a read-only snapshot.
func (s *Surface) hibernatedView(c *ui.Context, m locales.Messages, t *ui.Theme) {
	ui.Column(c).Grow(1).FillWidth().Padding(24).Gap(10).
		Justify(ui.Center).AlignItems(ui.Center).Children(func() {
		ui.Text(c, m.SessionHibernatedTitle()).FontSize(kit.M(c).Font.Title3).Bold().
			TextAlign(ui.Center).TextColor(t.Text)
		ui.Text(c, m.SessionHibernatedBody()).FontSize(kit.M(c).Font.Caption).Wrap().
			TextAlign(ui.Center).MaxWidth(520).TextColor(t.TextMuted)
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			if ui.PrimaryButton(c, m.SessionResume()).Clicked() {
				_ = s.Resume()
			}
			if ui.Button(c, m.SessionMenuObserveSnapshot()).Clicked() {
				_ = s.OpenObserver()
			}
		})
	})
}

// observerView renders the read-only shadow terminal fed by the application's
// Snapshot/Feed facade. It replaces the live terminal in the same Pane and
// never attaches a second live view.
func (s *Surface) observerView(c *ui.Context, m locales.Messages, t *ui.Theme) {
	ui.Column(c).Grow(1).FillWidth().Gap(6).Padding(8).Children(func() {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, m.SessionObserverTitle()).FontSize(kit.M(c).Font.Callout).Bold().SingleLine().
				TextColor(t.Text)
			ui.Text(c, m.SessionObserverHint()).FontSize(kit.M(c).Font.Caption2).SingleLine().
				MinWidth(0).Grow(1).TextColor(t.TextMuted)
			if ui.Button(c, m.SessionObserverClose()).Clicked() {
				s.CloseObserver()
			}
		})
		terminal.View(c, s.observer).Grow(1).Key("session-observer")
	})
}

// renameDialog collects the Pane title. The shell owns the Tab that actually
// carries the name, so Rename hands the trimmed text to PaneActions.Rename.
func (s *Surface) renameDialog(c *ui.Context, m locales.Messages, t *ui.Theme) {
	kit.DialogWith(c, &s.renaming, kit.DialogOptions{Role: kit.DialogSheet}, func() {
		ui.Column(c).Width(320).Padding(14).Gap(10).Background(t.Surface).
			Radius(t.Radius).Border(1, t.Border).Children(func() {
			ui.Text(c, m.SessionMenuRenameTitle()).FontSize(kit.M(c).Font.Callout).Bold().TextColor(t.Text)
			input := ui.TextInput(c, &s.renameValue)
			input.Label(m.SessionRenameField())
			submitted := input.Submitted()
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Column(c).Grow(1)
				confirm := ui.PrimaryButton(c, m.SessionMenuRenameConfirm())
				if (confirm.Clicked() || submitted) && s.applyRename() {
					s.renaming = false
					s.redraw()
				}
				if ui.Button(c, m.SessionPasteCancel()).Clicked() {
					s.renaming = false
					s.redraw()
				}
			})
		})
	}, nil)
}

func (s *Surface) applyRename() bool {
	title := strings.TrimSpace(s.renameValue)
	if title == "" || s.paneActions.Rename == nil {
		return false
	}
	s.paneActions.Rename(title)
	return true
}

// requestPaste applies the multiline paste policy and redraws when the paste
// is waiting for confirmation.
func (s *Surface) requestPaste(text string) {
	if !s.gate.Request(text) {
		s.redraw()
	}
}

// setEncoding applies a live encoding change and reports it to the app for
// persistence. The terminal keeps running; only the Session codec changes.
func (s *Surface) setEncoding(encoding domainterminal.Encoding) {
	if s.session.SetEncoding(encoding) && s.options.OnEncoding != nil {
		s.options.OnEncoding(encoding)
	}
	s.redraw()
}

// SetPaneActions supplies the Pane commands the Session menu offers. The shell
// calls it while rendering the Pane; a detached Pane clears them with the zero
// value.
func (s *Surface) SetPaneActions(actions PaneActions) { s.paneActions = actions }

// openBrowserAction enables the Session menu item. A Session without a Host
// route omits the opener at construction, so the item is disabled rather than
// failing after a click.
func (s *Surface) openBrowserAction() func() {
	if s.options.Browser == nil || s.browser != nil {
		return nil
	}
	return func() { _ = s.OpenBrowser() }
}

// BrowserOpen reports whether the Session is showing its browser panel. The
// panel owns a native page that cannot be mounted in a second window, so a
// follow or topdown renderer checks this before choosing a neutral
// placeholder instead of rendering the Surface elsewhere.
func (s *Surface) BrowserOpen() bool { return s.browser != nil }

// OpenBrowser opens the Session's browser panel. It is idempotent, and the
// terminal keeps running behind the panel. The app supplies the opener, which
// claims a fresh dedicated Host tunnel and creates the window's pages.
func (s *Surface) OpenBrowser() error {
	if s.browser != nil {
		return nil
	}
	if s.options.Browser == nil {
		return errors.New("sessionui: this Session has no browser route")
	}
	panel, err := s.options.Browser(s.closeBrowser)
	if err != nil {
		s.browserErr = err
		s.redraw()
		return err
	}
	s.browserErr = nil
	s.browser = panel
	s.redraw()
	return nil
}

// closeBrowser is the panel's Close control. It clears the panel first so a
// callback from inside it cannot re-enter an open panel.
func (s *Surface) closeBrowser() {
	if s.browser == nil {
		return
	}
	panel := s.browser
	s.browser = nil
	panel.Close()
	s.redraw()
}

// beginRename opens the title dialog with the Pane's current title.
func (s *Surface) beginRename() {
	if s.paneActions.Rename == nil {
		return
	}
	s.renameValue = s.paneActions.Title
	s.renaming = true
	s.redraw()
}

// saveScreen writes the terminal's scrollback and screen and reports the
// result in a dismissible banner.
func (s *Surface) saveScreen() {
	if s.terminal == nil {
		return
	}
	writer := s.options.SaveScreen
	if writer == nil {
		writer = SaveScreenFile
	}
	if _, err := writer(s.terminal.Text()); err != nil {
		s.screenNotice = ScreenNoticeFailed
	} else {
		s.screenNotice = ScreenNoticeSaved
	}
	s.redraw()
}

// Hibernate releases the Session's live view while keeping the transport and
// every application-level field alive. The screen becomes a resumable
// placeholder; output produced now still enters the Session's bounded replay.
func (s *Surface) Hibernate() bool {
	if s.session == nil || s.terminal == nil || s.hibernated {
		return false
	}
	if !s.session.Hibernate() {
		return false
	}
	s.hibernated = true
	s.dropOver = false
	// Closing the terminal closes its ViewConn, which only detaches this
	// view: the Session, the transport, and the observer feeds stay alive.
	_ = s.terminal.Close()
	s.terminal = nil
	s.redraw()
	return true
}

// Resume reattaches the Session's one live view. The resumed connection
// replays the bounded screen snapshot before live output, so the new
// terminal shows the same screen the hibernated Session produced.
func (s *Surface) Resume() error {
	if s.session == nil || !s.hibernated {
		return appsession.ErrNotHibernated
	}
	resumed, err := s.session.Resume()
	if err != nil {
		return err
	}
	if err := s.attach(resumed); err != nil {
		// The Session assumed a view; put it back into its hibernated state
		// so a later Resume starts from a consistent snapshot.
		_ = resumed.Close()
		s.session.Hibernate()
		return err
	}
	// The replay was captured at the Session's last grid size. Give the new
	// terminal that geometry before the Pane's first frame can resize it.
	if cols, rows := resumed.ScreenSize(); cols > 0 && rows > 0 {
		s.terminal.Resize(cols, rows)
	}
	s.hibernated = false
	s.redraw()
	return nil
}

// OpenObserver opens a read-only shadow terminal over the application's
// Snapshot/Feed facade. It is independent of the Session's one live view and
// never receives typed input.
func (s *Surface) OpenObserver() error {
	if s.session == nil || s.observing {
		return nil
	}
	// A closed observer's resources are released before the next frame; drop
	// them now so a quick reopen always starts from a fresh snapshot.
	s.releaseObserver()
	observation, err := s.session.Observe()
	if err != nil {
		return err
	}
	cursor, blink := s.terminalCursor()
	term, err := terminal.New(terminal.Options{
		Conn:             observation,
		Font:             s.terminalFont(),
		Theme:            s.terminalTheme(),
		Keybinds:         s.terminalKeybinds(),
		Cursor:           cursor,
		NoBlink:          !blink,
		NoClipboardWrite: true,
		ClipboardRead:    terminal.ClipboardDeny,
	})
	if err != nil {
		_ = observation.Close()
		return err
	}
	// Capture the owner's geometry before the Pane sizes the view, so the
	// feed renders at the width the snapshot was produced for.
	if screen := observation.Screen(); screen.Cols > 0 && screen.Rows > 0 {
		term.Resize(screen.Cols, screen.Rows)
	}
	s.observation = observation
	s.observer = term
	s.observing = true
	s.redraw()
	return nil
}

// CloseObserver stops rendering the observer. Its resources are released
// before the next frame, when the live terminal takes the Pane again.
func (s *Surface) CloseObserver() {
	if !s.observing {
		return
	}
	s.observing = false
	s.redraw()
}

// releaseObserver closes the observer terminal and its feed. Closing the
// terminal closes the Observation, which only unsubscribes the feed; the
// owning Session and its live view are unaffected.
func (s *Surface) releaseObserver() {
	if s.observer != nil {
		_ = s.observer.Close()
		s.observer = nil
	}
	if s.observation != nil {
		_ = s.observation.Close()
		s.observation = nil
	}
	s.observing = false
}

// Hibernated reports whether the Session has no live screen. It follows both
// the surface's attached state and the Session's own snapshot.
func (s *Surface) Hibernated() bool {
	if s.hibernated {
		return true
	}
	return s.session != nil && s.session.Snapshot().Hibernated
}

// Observing reports whether the observer Pane is showing.
func (s *Surface) Observing() bool { return s.observing }

// ObserverText returns the shadow terminal's text, including its scrollback.
func (s *Surface) ObserverText() string {
	if s.observer == nil {
		return ""
	}
	return s.observer.Text()
}

// ObserverScreen returns the snapshot the observer opened with, and whether
// an observer is open.
func (s *Surface) ObserverScreen() (appsession.ScreenSnapshot, bool) {
	if s.observation == nil {
		return appsession.ScreenSnapshot{}, false
	}
	return s.observation.Screen(), true
}

// Session returns the owning application Session.
func (s *Surface) Session() *appsession.Session { return s.session }

// windowsShell reports whether inserted drop paths should use the Windows
// quoting dialect. Local Windows Sessions run a Windows shell; SSH Sessions are
// POSIX shells regardless of where the client runs. Both cmd.exe and
// PowerShell accept the double-quoted form, which avoids guessing which one
// the local transport selected.
func (s *Surface) windowsShell() bool {
	return runtime.GOOS == "windows" && s.session.Snapshot().Kind == appsession.KindLocal
}

func (s *Surface) toggleLog() {
	snapshot := s.session.Snapshot()
	if snapshot.Logging.Active {
		s.session.StopLogging()
		s.logError = false
		return
	}
	if s.options.LogDefaults == nil {
		s.logError = true
		return
	}
	options := s.options.LogDefaults()
	if options.Directory == "" {
		s.logError = true
		return
	}
	options.Format = s.logFormat
	if err := s.session.StartLogging(options); err != nil {
		s.logError = true
		return
	}
	s.logError = false
}

// openLog hands the file the Session is currently writing to the platform
// opener. It never starts logging: with logging off there is no current file,
// and the menu action is disabled instead.
func (s *Surface) openLog(string) {
	logging := s.session.Snapshot().Logging
	if s.options.OpenLog == nil || !logging.Active || logging.Path == "" {
		return
	}
	s.options.OpenLog(logging.Path)
}

func (s *Surface) setLogFormat(format domainterminal.LogFormat) {
	s.logFormat = format
	if !s.session.Snapshot().Logging.Active || s.options.LogDefaults == nil {
		return
	}
	options := s.options.LogDefaults()
	if options.Directory == "" {
		return
	}
	options.Format = format
	if err := s.session.StartLogging(options); err != nil {
		s.logError = true
		return
	}
	s.logError = false
}

func (s *Surface) requestClipboard(request terminal.ClipboardRequest) {
	s.mu.Lock()
	previous := s.pendingClipboard
	s.pendingClipboard = &request
	s.mu.Unlock()
	if previous != nil {
		previous.Deny()
	}
	s.redraw()
}

func (s *Surface) answerClipboard(allow bool) {
	s.mu.Lock()
	request := s.pendingClipboard
	s.pendingClipboard = nil
	s.mu.Unlock()
	if request == nil {
		return
	}
	if allow {
		request.Allow()
	} else {
		request.Deny()
	}
	s.redraw()
}

func (s *Surface) redraw() {
	if s.options.Redraw != nil {
		s.options.Redraw()
	}
}

// ScreenText returns the text the terminal shows, including scrollback. Tests
// and diagnostics use it to inspect the line a shell actually rendered. A
// hibernated Session has no live screen and returns "".
func (s *Surface) ScreenText() string {
	if s.terminal == nil {
		return ""
	}
	return s.terminal.Text()
}

// LensBlocks returns a snapshot of the command flow currently rendered above
// the live terminal. It is intentionally a copy so diagnostics and integration
// tests cannot mutate the UI-owned Lens store.
func (s *Surface) LensBlocks() []lensmodel.Block {
	if s == nil || s.flow == nil {
		return nil
	}
	return s.flow.store.Snapshot()
}

// Close closes the terminal and the observer. MyGo closes the ViewConn, which
// detaches the live view without ending the Session; the shell closes the
// Session explicitly.
func (s *Surface) Close() {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		request := s.pendingClipboard
		s.pendingClipboard = nil
		s.mu.Unlock()
		if request != nil {
			request.Deny()
		}
		s.releaseObserver()
		if s.browser != nil {
			s.browser.Close()
			s.browser = nil
		}
		s.gate.Cancel()
		s.drops.Dismiss()
		if s.terminal != nil {
			_ = s.terminal.Close()
			s.terminal = nil
		}
		if s.flow != nil {
			s.flow.close()
			s.flow = nil
		}
	})
}

// Dir returns the working directory the shell reported (OSC 7), or "". The
// shell reports it for local and remote Sessions that set the sequence.
func (s *Surface) Dir() string {
	if cwd := s.session.Snapshot().CWD; cwd != "" {
		return cwd
	}
	if s.terminal == nil {
		return ""
	}
	return s.terminal.Dir()
}
