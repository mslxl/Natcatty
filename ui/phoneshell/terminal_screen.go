package phoneshell

import (
	"io"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/terminal"
)

// newTerminalScreen builds the phone's real terminal over a Session handle.
// libghostty-vt is linked into the app on phones, so terminal.New succeeds once
// the root has called terminal.Load; a test injects its own ScreenFactory so
// the shell's navigation tests do not depend on the native library.
func newTerminalScreen(conn io.ReadWriteCloser) (TerminalScreen, error) {
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
	term, err := terminal.New(terminal.Options{Conn: conn, ResizeByConn: remoteScreen})
	if err != nil {
		return nil, err
	}
	if remoteScreen {
		if sink, ok := conn.(interface{ SetScreenSizeSink(func(cols, rows int)) }); ok {
			sink.SetScreenSizeSink(term.Resize)
		}
	}
	return &ghosttyScreen{term: term}, nil
}

// ghosttyScreen adapts the libghostty-vt terminal to the screen seam.
type ghosttyScreen struct {
	term *terminal.Terminal
}

func (s *ghosttyScreen) View(c *ui.Context) ui.Element { return terminal.View(c, s.term) }

// SendKey types an accessory-bar key through the terminal's own encoder.
func (s *ghosttyScreen) SendKey(key ui.Key, mods ui.Modifiers) { s.term.SendKey(key, mods) }

func (s *ghosttyScreen) Close() error { return s.term.Close() }
