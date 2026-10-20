// Package sessionui owns the MyGo terminal that renders one application
// Session. The application owns the transport; this surface connects MyGo's
// terminal to the Session's bounded duplex and keeps the terminal's lifetime
// to the Tab's. It also renders the Session's command, activity, logging, and
// transfer chrome from application state.
package sessionui

import (
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/application/autocomplete"
	appsession "github.com/mslxl/natcatty/application/session"
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
	"github.com/mslxl/natcatty/ui/locales"
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
		session:   session,
		options:   options,
		logFormat: domainterminal.DefaultLogFormat,
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

	// The application's tap is the single authority for OSC 52: it removes
	// rejected writes and reads before the terminal sees them, and applies the
	// live Settings policy. MyGo must therefore stay permissive here, and this
	// callback applies what the Session currently allows. Reading the policy
	// per request is what makes a Settings change affect live Sessions.
	term, err := terminal.New(terminal.Options{
		Conn:             session.TerminalConn(),
		NoClipboardWrite: false,
		ClipboardRead:    terminal.ClipboardAsk,
		OnClipboardRequest: func(request terminal.ClipboardRequest) {
			switch access := session.ClipboardAccess(); {
			case access.ReadsAllowed():
				request.Allow()
			case access.AsksOnRead():
				surface.requestClipboard(request)
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
			surface.requestPaste(string(text))
			return true
		},
		OnKey: surface.handleAutocompleteKey,
	})
	if err != nil {
		return nil, err
	}
	surface.terminal = term
	return surface, nil
}

func validLogFormat(format domainterminal.LogFormat) bool {
	switch format {
	case domainterminal.LogFormatText, domainterminal.LogFormatRaw, domainterminal.LogFormatHTML:
		return true
	default:
		return false
	}
}

// View shows the Session chrome above the terminal and gives the terminal the
// keyboard focus.
func (s *Surface) View(c *ui.Context) ui.Element {
	m := locales.In(c.Locale())
	t := c.Theme()
	snapshot := s.session.Snapshot()
	if snapshot.Logging.Active && validLogFormat(snapshot.Logging.Format) {
		s.logFormat = snapshot.Logging.Format
	}
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
		SaveScreen:    func() { s.saveScreen() },
		ClearBuffer:   func() { s.terminal.Feed([]byte("\x1b[3J")) },
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
		SplitRight: s.paneActions.SplitRight,
		SplitDown:  s.paneActions.SplitDown,
		Detach:     s.paneActions.Detach,
		ClosePane:  s.paneActions.Close,
		AcceptSuggestion: func(index int) {
			s.acceptSuggestion(index)
		},
	}

	return ui.Column(c).Fill().Children(func() {
		renderChrome(c, model, m, t, actions)
		// Key the terminal so its node keeps the same identity when chrome
		// inserts or removes a row above it (autocomplete popup, statistics,
		// banners). MyGo derives unkeyed node ids from the child position, so
		// an inserted row above the terminal would move the keyboard focus to
		// the new row and the user's next keystroke would go nowhere.
		termView := terminal.View(c, s.terminal).Grow(1).Key("session-terminal")
		// File drag/drop belongs to the terminal element. Reading it here
		// marks the terminal as the drop target and consumes a completed
		// drop once; the resulting banner appears on the next frame.
		if over := termView.FileDragOver(); over != s.dropOver {
			s.dropOver = over
			// Both entering and leaving change the banner, so redraw either
			// way; otherwise the drop hint would stay after the pointer left.
			s.redraw()
		}
		if files := termView.DroppedFiles(); len(files) > 0 {
			s.dropOver = false
			s.drops.Drop(files, s.windowsShell())
			s.redraw()
		}
		if s.renaming {
			s.renameDialog(c, m, t)
		}
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
// and diagnostics use it to inspect the line a shell actually rendered.
func (s *Surface) ScreenText() string {
	return s.terminal.Text()
}

// Close closes the terminal. MyGo closes the Conn, which closes the Session.
func (s *Surface) Close() {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		request := s.pendingClipboard
		s.pendingClipboard = nil
		s.mu.Unlock()
		if request != nil {
			request.Deny()
		}
		s.gate.Cancel()
		s.drops.Dismiss()
		_ = s.terminal.Close()
	})
}

// Dir returns the working directory the shell reported (OSC 7), or "". The
// shell reports it for local and remote Sessions that set the sequence.
func (s *Surface) Dir() string {
	if cwd := s.session.Snapshot().CWD; cwd != "" {
		return cwd
	}
	return s.terminal.Dir()
}
