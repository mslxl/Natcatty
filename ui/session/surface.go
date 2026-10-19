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

// New connects a MyGo terminal to session's TerminalConn. It returns an error
// when the terminal plugin's native library is unavailable.
func New(session *appsession.Session) (*Surface, error) {
	term, err := terminal.New(terminal.Options{Conn: session.TerminalConn()})
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
