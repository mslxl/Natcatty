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
	term, err := terminal.New(terminal.Options{Conn: conn})
	if err != nil {
		return nil, err
	}
	return &ghosttyScreen{term: term}, nil
}

// ghosttyScreen adapts the libghostty-vt terminal to the screen seam.
type ghosttyScreen struct {
	term *terminal.Terminal
}

func (s *ghosttyScreen) View(c *ui.Context) ui.Element { return terminal.View(c, s.term) }

func (s *ghosttyScreen) Close() error { return s.term.Close() }
