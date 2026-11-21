package phoneshell

import (
	"io"

	"github.com/egoist/mygo/transfer"
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

// SelectionText reads the terminal's own selection, the one the long press
// and drag made.
func (s *ghosttyScreen) SelectionText() (string, bool) { return s.term.SelectionText() }

// PasteData uses the terminal's paste path, so bracketed paste mode applies:
// the program that asked for one gets the payload wrapped and does not run a
// multi-line paste as commands.
func (s *ghosttyScreen) PasteData(d transfer.Data) { s.term.PasteData(d) }

// InScrollback reports whether the viewport is above the live screen.
func (s *ghosttyScreen) InScrollback() bool { return s.term.ScrolledBack() }

// ToggleScrollback uses the terminal's own viewport API rather than a MyGo
// scroll widget. The scrollback lives in libghostty-vt and reflows with the
// grid, so a scroll container around the view could not show the rows above
// it without duplicating the screen, and a finger drag there would claim the
// gesture the grid needs for selection and mouse reporting. A button the
// thumb reaches asks for neither and works whether a program takes the mouse.
func (s *ghosttyScreen) ToggleScrollback() { s.term.ToggleScrollback() }

func (s *ghosttyScreen) Close() error { return s.term.Close() }
