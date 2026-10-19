// Package sessionui owns the MyGo terminal that renders one application
// Session. The application owns the transport; this surface connects MyGo's
// terminal to the Session's bounded duplex and keeps the terminal's lifetime
// to the Tab's. It also renders the Session's command, activity, logging, and
// transfer chrome from application state.
package sessionui

import (
	"sync"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
	"github.com/mslxl/natcatty/ui/locales"
)

// Options configure a Surface. The app supplies logging defaults from Settings
// and the window redraw used when a clipboard request arrives on the UI thread.
type Options struct {
	LogDefaults func() appsession.LogOptions
	Redraw      func()
}

// Option customizes Surface options.
type Option func(*Options)

// WithLogDefaults supplies the logging destination and timestamp policy used
// by the Session's Start log control.
func WithLogDefaults(fn func() appsession.LogOptions) Option {
	return func(options *Options) { options.LogDefaults = fn }
}

// WithRedraw supplies the window invalidation used for asynchronous clipboard
// requests.
func WithRedraw(fn func()) Option {
	return func(options *Options) { options.Redraw = fn }
}

// Surface is one Tab's terminal view.
type Surface struct {
	session   *appsession.Session
	terminal  *terminal.Terminal
	options   Options
	closeOnce sync.Once

	mu               sync.Mutex
	pendingClipboard *terminal.ClipboardRequest

	// UI-thread state.
	logFormat domainterminal.LogFormat
	logError  bool
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

	model := chromeModel{
		Command:          snapshot.Command,
		Busy:             snapshot.Busy,
		Transfer:         snapshot.Transfer,
		Logging:          snapshot.Logging,
		ClipboardPending: clipboardPending,
		LogFormat:        s.logFormat,
		LogDirectory:     directory,
		LogProblem:       s.logError || snapshot.Logging.Problem != "",
	}
	actions := chromeActions{
		ToggleLog: func() { s.toggleLog() },
		SetLogFormat: func(format domainterminal.LogFormat) {
			s.setLogFormat(format)
		},
		AllowClipboard: func() { s.answerClipboard(true) },
		DenyClipboard:  func() { s.answerClipboard(false) },
		CancelTransfer: func() { s.session.CancelTransfer() },
	}

	return ui.Column(c).Fill().Children(func() {
		renderChrome(c, model, m, t, actions)
		terminal.View(c, s.terminal).Grow(1)
	})
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
