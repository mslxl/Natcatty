package sessionui

import (
	"io"
	"sync"
	"testing"

	"github.com/mslxl/natcatty/ui/terminal"
	"github.com/mslxl/natcatty/ui/terminal/appview"
)

// pinConn is the fake transport for a Surface's terminal: it records the sizes
// the terminal delivers and blocks Read until the terminal closes.
type pinConn struct {
	mu      sync.Mutex
	resizes []appview.Size
	closed  chan struct{}
	once    sync.Once
}

func newPinConn() *pinConn { return &pinConn{closed: make(chan struct{})} }

func (c *pinConn) Read([]byte) (int, error) {
	<-c.closed
	return 0, io.EOF
}

func (c *pinConn) Write(p []byte) (int, error) { return len(p), nil }

func (c *pinConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

func (c *pinConn) Resize(cols, rows int) error {
	c.mu.Lock()
	c.resizes = append(c.resizes, appview.Size{Cols: cols, Rows: rows})
	c.mu.Unlock()
	return nil
}

func (c *pinConn) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.resizes)
}

// TestPinViewportGuardsTheFinalRoom checks the no-reflow push from ADR 0026:
// the change sets the final room once, the previous-frame report the Pane's
// layout still gives is ignored until it names the final room, and a release
// lets the layout size the terminal again. The grid computed at the change and
// the one PTY resize it delivers are asserted by the shell's real-surface
// tests, which run inside a frame and so have a measured cell.
func TestPinViewportGuardsTheFinalRoom(t *testing.T) {
	conn := newPinConn()
	term, err := terminal.New(terminal.Options{Conn: conn, Scrollback: -1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = term.Close() })
	surface := &Surface{terminal: term}
	surface.noteViewport(892, 506)
	if w, h, ok := surface.MotionViewport(); !ok || w != 892 || h != 506 {
		t.Fatalf("MotionViewport before the change = %v, %v, %v, want 892, 506, true", w, h, ok)
	}

	const finalW, finalH = 439.5, 504
	surface.PinViewport(finalW, finalH)
	if w, h, ok := surface.MotionViewport(); !ok || w != finalW || h != finalH {
		t.Fatalf("MotionViewport after the pin = %v, %v, %v, want %v, %v, true",
			w, h, ok, finalW, finalH)
	}
	atChange := conn.count()

	// The Pane's own layout still reports the room from the frame before the
	// change for a frame or two. The pin ignores it, without a resize.
	surface.noteViewport(892, 506)
	if w, h, _ := surface.MotionViewport(); w != finalW || h != finalH {
		t.Fatalf("a stale layout report moved the pinned room to %vx%v", w, h)
	}
	if got := conn.count(); got != atChange {
		t.Fatalf("a stale layout report delivered %d extra resizes", got-atChange)
	}

	// Once the layout reports the final room, the pin is spent and later
	// reports size the terminal again.
	surface.noteViewport(finalW, finalH)
	surface.noteViewport(500, 400)
	if w, h, _ := surface.MotionViewport(); w != 500 || h != 400 {
		t.Fatalf("after confirmation the layout report was ignored: %vx%v", w, h)
	}

	// A release also lets a report through that the pin never confirmed.
	surface.PinViewport(finalW, finalH)
	surface.ReleaseViewport()
	surface.noteViewport(500, 400)
	if w, h, _ := surface.MotionViewport(); w != 500 || h != 400 {
		t.Fatalf("after release the layout report was ignored: %vx%v", w, h)
	}
}
