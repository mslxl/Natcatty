package phoneshell

// Resize on a phone: the terminal page lives inside the window's safe area,
// whose bottom is the keyboard, so the keyboard and rotation change the room
// the terminal has; and a transport whose peer owns the screen size (Mosh,
// Eternal Terminal) has no window-change signal of its own, so the peer's
// size is applied to the screen while the view only reports the size it
// fits.

import (
	"io"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestPhoneTerminalRespectsKeyboardInsetAndRotation(t *testing.T) {
	fixture, _ := openTerminal(t)

	terminal, ok := fixture.tester.Find("Terminal")
	if !ok {
		t.Fatal("no terminal element")
	}
	// The soft keyboard covers the bottom of an 844-high phone. The window
	// root lays the page out inside that inset, so neither the terminal nor
	// the bar may run under it.
	fixture.tester.SetInsets(0, 0, 300, 0)
	fixture.tester.Frame()
	terminal, ok = fixture.tester.Find("Terminal")
	if !ok {
		t.Fatal("no terminal element after the keyboard came up")
	}
	if bottom := terminal.Y + terminal.H; bottom > 844-300+1 {
		t.Errorf("the terminal at y=%v h=%v reaches %v, under the keyboard's inset", terminal.Y, terminal.H, bottom)
	}
	bar, ok := fixture.tester.Find("Esc")
	if !ok {
		t.Fatal("the accessory bar is gone with the keyboard up")
	}
	if bar.Y+bar.H > terminal.Y+1 {
		t.Errorf("the bar at y=%v h=%v is not above the terminal at y=%v", bar.Y, bar.H, terminal.Y)
	}

	// Rotation gives the keyboard back and swaps the window's sides.
	fixture.tester.SetInsets(0, 0, 0, 0)
	fixture.tester.SetSize(844, 390)
	fixture.tester.Frame()
	rotated, ok := fixture.tester.Find("Terminal")
	if !ok {
		t.Fatal("no terminal element after rotation")
	}
	if rotated.W <= terminal.W {
		t.Errorf("rotation left the terminal %v wide, not wider than %v", rotated.W, terminal.W)
	}
	if bottom := rotated.Y + rotated.H; bottom > 390+1 {
		t.Errorf("the rotated terminal at y=%v h=%v reaches %v, outside the window", rotated.Y, rotated.H, bottom)
	}
}

// screenConn is a Session handle's connection for the real terminal. It
// records the sizes the view reports and can stand for a transport whose peer
// owns the screen size.
type screenConn struct {
	mu     sync.Mutex
	sizes  [][2]int
	sink   func(cols, rows int)
	remote bool
	done   chan struct{}
	closed sync.Once
}

func newScreenConn(remote bool) *screenConn {
	return &screenConn{remote: remote, done: make(chan struct{})}
}

func (c *screenConn) Read([]byte) (int, error) {
	<-c.done
	return 0, io.EOF
}

func (c *screenConn) Write(p []byte) (int, error) { return len(p), nil }

func (c *screenConn) Close() error {
	c.closed.Do(func() { close(c.done) })
	return nil
}

func (c *screenConn) Resize(cols, rows int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sizes = append(c.sizes, [2]int{cols, rows})
	return nil
}

func (c *screenConn) ScreenSizeFromRemote() bool { return c.remote }

func (c *screenConn) SetScreenSizeSink(sink func(cols, rows int)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sink = sink
}

func (c *screenConn) sizeSink() func(cols, rows int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sink
}

func (c *screenConn) reported() [][2]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([][2]int(nil), c.sizes...)
}

// awaitReport waits for the view to report a size to the Session.
func awaitReport(t *testing.T, conn *screenConn) [2]int {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for len(conn.reported()) == 0 {
		select {
		case <-deadline:
			t.Fatal("the view never reported its size to the Session")
		case <-time.After(time.Millisecond):
		}
	}
	return conn.reported()[0]
}

func TestPhoneRemoteScreenTakesThePeersSize(t *testing.T) {
	conn := newScreenConn(true)
	screen, err := newTerminalScreen(conn)
	if err != nil {
		t.Skipf("libghostty-vt unavailable: %v", err)
	}
	defer screen.Close()

	sink := conn.sizeSink()
	if sink == nil {
		t.Fatal("a remote-authoritative Session installed no screen-size sink")
	}
	// The peer says the session is 132x43. That is the screen's size, since
	// there is no window-change signal to ask it for another.
	sink(132, 43)
	term := screen.(*ghosttyScreen).term
	if cols, rows := term.Size(); cols != 132 || rows != 43 {
		t.Fatalf("the screen is %dx%d, want the peer's 132x43", cols, rows)
	}

	// The view fits the phone's room, reports that size to the Session, and
	// leaves the screen at the peer's size for the Session to apply.
	tt := ui.NewTester(func(c *ui.Context) { screen.View(c).Fill() }, 400, 300)
	tt.Frame()
	reported := awaitReport(t, conn)
	if reported == [2]int{132, 43} {
		t.Fatalf("the view reported the screen's size %v, not the room it fits", reported)
	}
	if cols, rows := term.Size(); cols != 132 || rows != 43 {
		t.Errorf("the view resized the peer's screen to %dx%d", cols, rows)
	}
}

func TestPhonePlainScreenTakesTheViewsSize(t *testing.T) {
	conn := newScreenConn(false)
	screen, err := newTerminalScreen(conn)
	if err != nil {
		t.Skipf("libghostty-vt unavailable: %v", err)
	}
	defer screen.Close()

	if sink := conn.sizeSink(); sink != nil {
		t.Fatal("a plain Session installed a peer-size sink")
	}
	tt := ui.NewTester(func(c *ui.Context) { screen.View(c).Fill() }, 400, 300)
	tt.Frame()
	reported := awaitReport(t, conn)
	term := screen.(*ghosttyScreen).term
	if cols, rows := term.Size(); cols != reported[0] || rows != reported[1] {
		t.Errorf("the screen is %dx%d, want the view's %v", cols, rows, reported)
	}
}
