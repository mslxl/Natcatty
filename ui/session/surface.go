// Package sessionui owns the MyGo terminal that renders one application
// Session. The application owns the transport; this surface connects MyGo's
// terminal to the Session's bounded duplex and keeps the terminal's lifetime
// to the Tab's.
package sessionui

import (
	"sync"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"
)

// Surface is one Tab's terminal view.
type Surface struct {
	terminal  *terminal.Terminal
	closeOnce sync.Once
}

// options are the terminal choices a Surface accepts.
type options struct {
	onKey func(ui.Modifiers, ui.Key) bool
}

// Option customizes a Surface.
type Option func(*options)

// WithKeyInterceptor installs a handler that sees key presses before the
// terminal does, as the selftest build uses to keep its plain F12 prefix from
// being sent to the program. Returning true consumes the key.
func WithKeyInterceptor(fn func(ui.Modifiers, ui.Key) bool) Option {
	return func(o *options) { o.onKey = fn }
}

// New connects a MyGo terminal to session's TerminalConn. It returns an error
// when the terminal plugin's native library is unavailable.
func New(session *appsession.Session, opts ...Option) (*Surface, error) {
	var options options
	for _, opt := range opts {
		opt(&options)
	}
	term, err := terminal.New(terminal.Options{
		Conn:  session.TerminalConn(),
		OnKey: options.onKey,
	})
	if err != nil {
		return nil, err
	}
	return &Surface{terminal: term}, nil
}

// View shows the terminal and gives it the keyboard focus.
func (s *Surface) View(c *ui.Context) ui.Element {
	return terminal.View(c, s.terminal).Fill().AutoFocus()
}

// Close closes the terminal. MyGo closes the Conn, which closes the Session.
func (s *Surface) Close() {
	s.closeOnce.Do(func() {
		_ = s.terminal.Close()
	})
}

// Dir returns the working directory the shell reported (OSC 7), or "". The
// shell reports it for local and remote Sessions that set the sequence.
func (s *Surface) Dir() string { return s.terminal.Dir() }
