package vnc_test

import (
	"context"
	"image"
	"image/color"
	"net"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	adaptervnc "github.com/mslxl/natcatty/adapters/vnc"
	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model/rfb"
	"github.com/mslxl/natcatty/domain/model/rfb/rfbtest"
	modelterminal "github.com/mslxl/natcatty/domain/model/terminal"
	modelvnc "github.com/mslxl/natcatty/domain/model/vnc"
	"github.com/mslxl/natcatty/domain/sessiontitle"
	vncui "github.com/mslxl/natcatty/ui/vnc"
)

var (
	red  = color.RGBA{R: 220, G: 20, B: 20, A: 255}
	blue = color.RGBA{R: 20, G: 20, B: 220, A: 255}
)

func quadFrame() *image.RGBA {
	frame := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			if x < 2 && y < 2 {
				frame.SetRGBA(x, y, red)
			} else {
				frame.SetRGBA(x, y, blue)
			}
		}
	}
	return frame
}

// waitUntil polls a real condition with a hard deadline. It never waits on a
// fixed delay to infer that the app settled.
func waitUntil(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

type fixture struct {
	session *appsession.Session
	surface *vncui.Surface
	tester  *ui.Tester
	server  *rfbtest.Server
	serve   chan error
}

type fixtureOptions struct {
	frame         *image.RGBA
	width, height int
	root          func(*ui.Context, *vncui.Surface) ui.Element
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	return newFixtureWithOptions(t, fixtureOptions{})
}

// newFixtureWithRoot builds the same Session and viewer inside an optional
// wrapper, so a test can give the pane a non-zero window origin.
func newFixtureWithRoot(t *testing.T, root func(*ui.Context, *vncui.Surface) ui.Element) *fixture {
	t.Helper()
	return newFixtureWithOptions(t, fixtureOptions{root: root})
}

func newFixtureWithOptions(t *testing.T, opts fixtureOptions) *fixture {
	t.Helper()
	frame := opts.frame
	if frame == nil {
		frame = quadFrame()
	}
	width, height := opts.width, opts.height
	if width <= 0 || height <= 0 {
		width, height = 160, 120
	}
	serverConn, clientConn := net.Pipe()
	server := rfbtest.New(rfbtest.Config{
		Frame:       frame,
		DesktopName: "lab-desktop",
		Encoding:    rfb.EncodingRaw,
	})
	serve := make(chan error, 1)
	go func() { serve <- server.Serve(serverConn) }()

	factory := adaptervnc.Factory{Dial: func(context.Context, string, string) (net.Conn, error) {
		return clientConn, nil
	}}
	cfg := modelvnc.SessionConfig{
		Hostname:      "desk.test",
		Port:          5900,
		Configuration: modelvnc.DefaultConfig(),
	}
	session, err := appsession.NewService(factory).Open(context.Background(), appsession.StartSpec{
		Kind:  appsession.KindVNC,
		Label: "workstation",
		VNC:   &cfg,
	})
	if err != nil {
		t.Fatalf("Open VNC: %v", err)
	}
	surface, err := vncui.New(session)
	if err != nil {
		t.Fatalf("New viewer: %v", err)
	}
	tester := ui.NewTester(func(c *ui.Context) {
		if opts.root != nil {
			opts.root(c, surface)
			return
		}
		surface.ViewFocused(c, true)
	}, width, height)
	t.Cleanup(func() {
		surface.Close()
		_ = session.Close()
		_ = clientConn.Close()
		_ = serverConn.Close()
	})
	return &fixture{session: session, surface: surface, tester: tester, server: server, serve: serve}
}

// TestFirstDesktopRendersAndInputsEndToEnd is the ticket's whole path: a saved
// VNC configuration opens through the real adapter and the in-process server,
// the headless tester shows the server's frame, typed text and pointer input
// reach the guest, the title follows the desktop name, and closing releases the
// Session.
func TestFirstDesktopRendersAndInputsEndToEnd(t *testing.T) {
	f := newFixture(t)

	// Before the desktop name arrives the title falls back to the Host label;
	// once it arrives the Tab follows the server.
	if title := sessiontitle.Resolve(sessiontitle.Input{HostLabel: "workstation"}, modelterminal.DynamicTabTitleAll); title != "workstation" {
		t.Fatalf("unknown desktop title = %q, want the Host label", title)
	}
	waitUntil(t, "the desktop name", func() bool {
		return f.session.Snapshot().Title == "lab-desktop"
	})
	if title := sessiontitle.Resolve(sessiontitle.Input{
		HostLabel:    "workstation",
		DynamicTitle: f.session.Snapshot().Title,
	}, modelterminal.DynamicTabTitleAll); title != "lab-desktop" {
		t.Fatalf("desktop title = %q, want lab-desktop", title)
	}

	// Fit scaling of a 4x4 desktop in a 160x120 pane: 30x per pixel, 120x120
	// centered, so the red top-left quadrant covers (20,0)-(50,30).
	var shot *image.RGBA
	waitUntil(t, "the server frame to render", func() bool {
		f.tester.Frame()
		shot = f.tester.Image()
		return shot.RGBAAt(35, 15) == red && shot.RGBAAt(130, 100) == blue
	})
	if got := shot.RGBAAt(5, 60); got == red || got == blue {
		t.Fatalf("letterbox pixel = %+v, want the pane background", got)
	}

	// Typed text reaches the element through the platform's text input on
	// Linux as well as through keys; the headless tester reports whether the
	// focused viewer takes text.
	if active, _ := f.tester.TextInputActive(); !active {
		t.Fatal("the VNC viewer does not take text input")
	}

	// Basic pointer and typed-character input reach the guest.
	f.tester.Move(35, 15)
	f.tester.Press(35, 15)
	f.tester.Release(35, 15)
	f.tester.Type("A")

	waitUntil(t, "the guest to record input", func() bool {
		received := f.server.Received()
		if len(received.Keys) < 2 {
			return false
		}
		keys := received.Keys
		return keys[0] == (rfbtest.KeyEvent{Down: true, Keysym: 0x41}) &&
			keys[1] == (rfbtest.KeyEvent{Down: false, Keysym: 0x41}) &&
			hasPress(received.Pointers)
	})
	pointers := f.server.Received().Pointers
	if len(pointers) == 0 || pointers[0].X != 0 || pointers[0].Y != 0 {
		t.Fatalf("pointer events = %+v, want the press at remote 0,0", pointers)
	}

	// A VNC pane has no byte stream, so no terminal resize, capture,
	// scrollback, or broadcast surface behavior applies.
	if f.session.ByteStream() {
		t.Fatal("VNC Session reports a byte stream")
	}
	if err := f.session.Resize(100, 50); err != nil {
		t.Fatalf("Resize = %v, want a no-op", err)
	}
	if _, ok := any(f.surface).(interface{ InsertText(string) error }); ok {
		t.Fatal("the VNC viewer offers text insertion")
	}
	if _, ok := any(f.surface).(interface{ ScreenText() string }); ok {
		t.Fatal("the VNC viewer offers scrollback")
	}

	// Closing the pane (the shell closes the surface, then the Session) ends
	// the connection and the display event stream.
	f.surface.Close()
	if err := f.session.Close(); err != nil {
		t.Fatalf("session Close: %v", err)
	}
	select {
	case <-f.session.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Session did not end after the pane closed")
	}
	if snapshot := f.session.Snapshot(); snapshot.Status != appsession.StatusClosed {
		t.Fatalf("status = %q, want closed", snapshot.Status)
	}
	select {
	case <-f.serve:
	case <-time.After(5 * time.Second):
		t.Fatal("the server connection stayed open after the pane closed")
	}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-f.session.DisplayEvents():
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("display events stayed open after the Session ended")
		}
	}
}

// TestDownscaledDesktopStaysCrisp proves a framebuffer larger than its pane is
// resampled nearest-neighbor, not blended: every sampled pixel is one of the
// server's two colors, with no intermediate shade.
func TestDownscaledDesktopStaysCrisp(t *testing.T) {
	frame := image.NewRGBA(image.Rect(0, 0, 320, 240))
	for y := 0; y < 240; y++ {
		for x := 0; x < 320; x++ {
			if ((x/2)+(y/2))%2 == 0 {
				frame.SetRGBA(x, y, red)
			} else {
				frame.SetRGBA(x, y, blue)
			}
		}
	}
	f := newFixtureWithOptions(t, fixtureOptions{frame: frame})
	waitUntil(t, "the downscaled desktop to render", func() bool {
		f.tester.Frame()
		shot := f.tester.Image()
		// The 320x240 desktop fits a 160x120 pane exactly, so the pattern is
		// one screen pixel per remote 2x2 block.
		for _, point := range [][2]int{{0, 0}, {1, 0}, {0, 1}, {1, 1}, {80, 60}, {159, 119}} {
			pixel := shot.RGBAAt(point[0], point[1])
			if pixel != red && pixel != blue {
				return false
			}
		}
		return true
	})
	shot := f.tester.Image()
	if shot.RGBAAt(0, 0) != red || shot.RGBAAt(1, 0) != blue ||
		shot.RGBAAt(0, 1) != blue || shot.RGBAAt(1, 1) != red {
		t.Fatalf("downscaled tile = %+v %+v %+v %+v, want exactly red/blue",
			shot.RGBAAt(0, 0), shot.RGBAAt(1, 0), shot.RGBAAt(0, 1), shot.RGBAAt(1, 1))
	}
}

func hasPress(pointers []rfbtest.PointerEvent) bool {
	for _, pointer := range pointers {
		if pointer.Mask == 1 && pointer.X == 0 && pointer.Y == 0 {
			return true
		}
	}
	return false
}

// TestPointerUsesPaneLocalCoordinates proves the viewer inverts the fit with
// the pointer position MyGo reports (relative to the element), not its window
// origin. A non-zero padded pane would map the press to the wrong remote pixel
// if the two were confused.
func TestPointerUsesPaneLocalCoordinates(t *testing.T) {
	f := newFixtureWithRoot(t, func(c *ui.Context, surface *vncui.Surface) ui.Element {
		return ui.Column(c).Fill().PaddingX(20).PaddingY(10).Children(func() {
			surface.ViewFocused(c, true)
		})
	})

	// The padded pane is 120x100 at window (20,10). The 4x4 desktop scales by
	// 25 into a 100x100 content box centered at local (10,0), so window (80,60)
	// is local (60,50) and remote (2,2).
	waitUntil(t, "the padded frame to render", func() bool {
		f.tester.Frame()
		return f.tester.Image().RGBAAt(92, 72) == blue
	})
	f.tester.Move(80, 60)
	f.tester.Press(80, 60)
	f.tester.Release(80, 60)

	waitUntil(t, "the guest to record the offset press", func() bool {
		for _, pointer := range f.server.Received().Pointers {
			if pointer.Mask == 1 && pointer.X == 2 && pointer.Y == 2 {
				return true
			}
		}
		return false
	})
}
