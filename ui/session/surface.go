// Package sessionui owns the MyGo terminal that renders one application
// Session. The application owns the transport; this surface connects MyGo's
// terminal to the Session's bounded duplex and keeps the terminal's lifetime
// to the Tab's. It also renders the Session's command, activity, logging, and
// transfer chrome from application state.
package sessionui

import (
	"errors"
	"io"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/application/autocomplete"
	appsession "github.com/mslxl/natcatty/application/session"
	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
	"github.com/mslxl/natcatty/ui/locales"
	uitheme "github.com/mslxl/natcatty/ui/theme"
)

// Options configure a Surface. The app supplies logging defaults from Settings
// and the window redraw used when a clipboard request arrives on the UI thread.
type Options struct {
	LogDefaults func() appsession.LogOptions
	Redraw      func()
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
	// Suggest returns command suggestions for the line being typed. Nil leaves
	// autocomplete off.
	Suggest func(input, scope string) []autocomplete.Suggestion
	// AutocompleteDisplay reports whether the popup and ghost hint are shown.
	AutocompleteDisplay func() (popup, ghost bool)
	// AutocompleteDelay is the debounce in milliseconds before suggestions are
	// recomputed. Nil means immediately.
	AutocompleteDelay func() int
	// ShowStats reports whether the Session counters are drawn.
	ShowStats func() bool
	// Font reads the live terminal font. The app applies a Settings change to
	// open terminals through SetTerminalFont; this is what a new terminal uses.
	Font func() terminal.Font
	// Theme reads the live terminal color scheme. The app applies a Settings
	// change to open terminals through SetTerminalTheme; a new terminal reads
	// it here.
	Theme func() domaintheme.TerminalTheme
	// Keybinds reads the active application bindings that act inside the
	// terminal. MyGo applies them at construction, and an unbound default chord
	// is cleared so a Settings override is authoritative for new Sessions.
	Keybinds func() []terminal.Keybind
	// View is an already claimed live connection. Resume supplies the
	// ResumedView that replays the hibernated screen; nil claims the
	// Session's one live view with AttachView.
	View io.ReadWriteCloser
	// Browser opens this Session's host-routed browser panel. Nil leaves the
	// Session menu's Open Browser item disabled, as for a local Session with
	// no saved Host route.
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
// switches.
func WithSuggestions(suggest func(input, scope string) []autocomplete.Suggestion,
	display func() (popup, ghost bool)) Option {
	return func(options *Options) {
		options.Suggest = suggest
		options.AutocompleteDisplay = display
	}
}

// WithAutocompleteDelay supplies the debounce applied after the typed line
// changes.
func WithAutocompleteDelay(fn func() int) Option {
	return func(options *Options) { options.AutocompleteDelay = fn }
}

// WithSessionStats supplies the live statistics visibility setting.
func WithSessionStats(show func() bool) Option {
	return func(options *Options) { options.ShowStats = show }
}

// WithTerminalFont supplies the live terminal font a new terminal uses.
func WithTerminalFont(fn func() terminal.Font) Option {
	return func(options *Options) { options.Font = fn }
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

// Surface is one Tab's terminal view.
type Surface struct {
	session   *appsession.Session
	terminal  *terminal.Terminal
	options   Options
	closeOnce sync.Once

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
	// focused is whether the Pane holding this Surface held the keyboard on
	// the last frame. focusPending asks the next terminalView for a one-shot
	// Focus when the Pane gained focus while its terminal was already shown.
	focused      bool
	focusPending bool
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
	// line.
	suggestions         []autocomplete.Suggestion
	suggestionIndex     int
	suggestionInput     string
	suggestionPending   time.Time
	suggestionDismissed bool
	suggestionTimer     *time.Timer
	showAutocomplete    bool
	showGhost           bool
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
	options := terminal.Options{
		Conn:             conn,
		Font:             s.terminalFont(),
		Theme:            s.terminalTheme(),
		Keybinds:         s.terminalKeybinds(),
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
		OnKey: s.handleAutocompleteKey,
	}
	term, err := terminal.New(options)
	if err != nil {
		return err
	}
	s.terminal = term
	return nil
}

func (s *Surface) terminalFont() terminal.Font {
	if s.options.Font == nil {
		return terminal.Font{}
	}
	return s.options.Font()
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
	return s.view(c, false)
}

// ViewFocused shows the Session and, when focused, gives its terminal the
// keyboard. Only the shell's focused Pane sets focused, so several Panes in
// one frame do not fight over it: the terminal takes the keyboard in the frame
// its element first appears (a new Tab, a Tab shown again after a switch, or
// Resume after Hibernate) and once when its Pane gains focus while already
// shown. It never reclaims the keyboard on later frames, so Compose, a dialog,
// or another control keeps the focus the user gave it.
func (s *Surface) ViewFocused(c *ui.Context, focused bool) ui.Element {
	return s.view(c, focused)
}

func (s *Surface) view(c *ui.Context, focused bool) ui.Element {
	s.focusPending = focused && !s.focused
	s.focused = focused
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
	t := c.Theme()
	snapshot := s.session.Snapshot()
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
	s.refreshSuggestions(snapshot.Input, snapshot.Command.Running)
	showStats := s.options.ShowStats != nil && s.options.ShowStats()
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
		ShowStats:        showStats,
		Stats:            snapshot.Stats,
		Hibernated:       hibernated,
	}
	var saveScreen, clearBuffer func()
	if !hibernated && s.terminal != nil {
		saveScreen = func() { s.saveScreen() }
		clearBuffer = func() { s.session.ClearBuffer() }
	}
	actions := chromeActions{
		ToggleLog: func() { s.toggleLog() },
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
	}

	return ui.Column(c).Fill().Children(func() {
		if s.browserErr != nil {
			ui.Row(c).FillWidth().Padding(6, 8).Gap(8).AlignItems(ui.Center).
				Background(t.Surface).Children(func() {
				ui.Text(c, m.BrowserOpenFailed()).FontSize(11).Grow(1).
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
		case hibernated || s.terminal == nil:
			s.hibernatedView(c, m, t)
		default:
			s.terminalView(c)
		}
		if s.renaming {
			s.renameDialog(c, m, t)
		}
	})
}

// terminalView shows the live terminal. It is the Session's one live view.
func (s *Surface) terminalView(c *ui.Context) {
	// Key the terminal so its node keeps the same identity when chrome
	// inserts or removes a row above it (autocomplete popup, statistics,
	// banners). MyGo derives unkeyed node ids from the child position, so an
	// inserted row above the terminal would move the keyboard focus to the new
	// row and the user's next keystroke would go nowhere. The key carries the
	// Session's identity so two Tabs never reuse one terminal node's state.
	termView := terminal.View(c, s.terminal).Grow(1).Key(s.terminalKey)
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
	if over := termView.FileDragOver(); over != s.dropOver {
		s.dropOver = over
		// Both entering and leaving change the banner, so redraw either way;
		// otherwise the drop hint would stay after the pointer left.
		s.redraw()
	}
	if files := termView.DroppedFiles(); len(files) > 0 {
		s.dropOver = false
		s.drops.Drop(files, s.windowsShell())
		s.redraw()
	}
}

// hibernatedView replaces the live screen with the Session's resumable state.
// The Session and its transport keep running; Resume reattaches the one live
// view, and the observer can still open a read-only snapshot.
func (s *Surface) hibernatedView(c *ui.Context, m locales.Messages, t *ui.Theme) {
	ui.Column(c).Grow(1).FillWidth().Padding(24).Gap(10).
		Justify(ui.Center).AlignItems(ui.Center).Children(func() {
		ui.Text(c, m.SessionHibernatedTitle()).FontSize(16).Bold().
			TextAlign(ui.Center).TextColor(t.Text)
		ui.Text(c, m.SessionHibernatedBody()).FontSize(11).Wrap().
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
			ui.Text(c, m.SessionObserverTitle()).FontSize(13).Bold().SingleLine().
				TextColor(t.Text)
			ui.Text(c, m.SessionObserverHint()).FontSize(10).SingleLine().
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
	ui.Modal(c, &s.renaming, func() {
		ui.Column(c).Width(320).Padding(14).Gap(10).Background(t.Surface).
			Radius(t.Radius).Border(1, t.Border).Children(func() {
			ui.Text(c, m.SessionMenuRenameTitle()).FontSize(13).Bold().TextColor(t.Text)
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
	})
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
	term, err := terminal.New(terminal.Options{
		Conn:             observation,
		Font:             s.terminalFont(),
		Theme:            s.terminalTheme(),
		Keybinds:         s.terminalKeybinds(),
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
