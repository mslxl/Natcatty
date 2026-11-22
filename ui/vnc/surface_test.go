package vnc_test

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"net"
	"runtime"
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
	configure     func(*rfbtest.Config)
	// config overrides the Host's viewer configuration; nil uses defaults.
	config *modelvnc.Config
	// serviceOptions and surfaceOptions pass test seams through to the
	// Session service and the viewer.
	serviceOptions []appsession.Option
	surfaceOptions []vncui.Option
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
	serverCfg := rfbtest.Config{
		Frame:       frame,
		DesktopName: "lab-desktop",
		Encoding:    rfb.EncodingRaw,
	}
	if opts.configure != nil {
		opts.configure(&serverCfg)
	}
	server := rfbtest.New(serverCfg)
	serve := make(chan error, 1)
	go func() { serve <- server.Serve(serverConn) }()

	factory := adaptervnc.Factory{Dial: func(context.Context, string, string) (net.Conn, error) {
		return clientConn, nil
	}}
	configuration := modelvnc.DefaultConfig()
	if opts.config != nil {
		configuration = *modelvnc.NormalizeConfig(opts.config)
	}
	cfg := modelvnc.SessionConfig{
		Hostname:      "desk.test",
		Port:          5900,
		Configuration: configuration,
	}
	session, err := appsession.NewService(factory, opts.serviceOptions...).Open(context.Background(), appsession.StartSpec{
		Kind:  appsession.KindVNC,
		Label: "workstation",
		VNC:   &cfg,
	})
	if err != nil {
		t.Fatalf("Open VNC: %v", err)
	}
	surface, err := vncui.New(session, opts.surfaceOptions...)
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

	// The viewer takes text focus so it can disable the input method; in pure
	// keysym mode it must have done so, because the guest composes characters
	// from keysyms and forwarding local text would type twice.
	active, noIME := f.tester.TextInputActive()
	if !active || !noIME {
		t.Fatalf("text input = active %v, IME off %v; want both true", active, noIME)
	}

	// Basic pointer and key input reach the guest.
	f.tester.Move(35, 15)
	f.tester.Press(35, 15)
	f.tester.Release(35, 15)
	f.tester.Key(0, ui.KeyA)

	waitUntil(t, "the guest to record input", func() bool {
		received := f.server.Received()
		if len(received.Keys) < 2 {
			return false
		}
		keys := received.Keys
		return keys[0] == (rfbtest.KeyEvent{Down: true, Keysym: 0x61}) &&
			keys[1] == (rfbtest.KeyEvent{Down: false, Keysym: 0x61}) &&
			hasPress(received.Pointers)
	})
	pointers := f.server.Received().Pointers
	if len(pointers) == 0 || pointers[0].X != 0 || pointers[0].Y != 0 {
		t.Fatalf("pointer events = %+v, want the press at remote 0,0", pointers)
	}
	// Hover sends an empty button mask at the pointer's remote position.
	if pointers[0].Mask != 0 {
		t.Fatalf("hover mask = %d, want 0", pointers[0].Mask)
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

// waitKeys waits for exactly want key events at the front of the guest's
// record. Each test starts from a fresh server, so the prefix is the whole
// exchange it sent.
func waitKeys(t *testing.T, server *rfbtest.Server, want []rfbtest.KeyEvent) {
	t.Helper()
	waitUntil(t, "the guest's key sequence", func() bool {
		received := server.Received().Keys
		if len(received) != len(want) {
			return false
		}
		for i := range want {
			if received[i] != want[i] {
				return false
			}
		}
		return true
	})
}

// TestKeyboardShiftedKeysymsAndModifiers proves the send-time rules: Shift is
// sent as its own keysym around the key, the shifted keysym is sent instead of
// the unshifted one, and the modifier is released with the key so it cannot
// stay stuck (MyGo never reports a modifier's own key-up).
func TestKeyboardShiftedKeysymsAndModifiers(t *testing.T) {
	f := newFixture(t)
	f.tester.Frame()
	f.tester.Key(ui.Shift, ui.Key2)
	f.tester.Key(0, ui.KeyA)
	waitKeys(t, f.server, []rfbtest.KeyEvent{
		{Down: true, Keysym: rfb.KeysymShiftL},
		{Down: true, Keysym: 0x40}, // @, the shifted keysym for position 2
		{Down: false, Keysym: 0x40},
		{Down: false, Keysym: rfb.KeysymShiftL},
		{Down: true, Keysym: 0x61},
		{Down: false, Keysym: 0x61},
	})
}

// TestKeyboardSpecialCombinations covers Ctrl+A, Shift+Tab (never
// ISO_Left_Tab) and Alt+Tab, and the toolbar's Ctrl+Alt+Del injection.
func TestKeyboardSpecialCombinations(t *testing.T) {
	f := newFixture(t)
	f.tester.Frame()

	f.tester.Key(ui.Ctrl, ui.KeyA)
	waitKeys(t, f.server, []rfbtest.KeyEvent{
		{Down: true, Keysym: rfb.KeysymControlL},
		{Down: true, Keysym: 0x61},
		{Down: false, Keysym: 0x61},
		{Down: false, Keysym: rfb.KeysymControlL},
	})

	f.tester.Key(ui.Shift, ui.KeyTab)
	waitUntil(t, "Shift+Tab", func() bool {
		keys := f.server.Received().Keys
		if len(keys) != 8 {
			return false
		}
		for _, event := range keys[4:] {
			if event.Keysym == 0xfe20 { // ISO_Left_Tab
				t.Fatalf("Shift+Tab sent ISO_Left_Tab: %+v", keys[4:])
			}
		}
		return keys[4] == (rfbtest.KeyEvent{Down: true, Keysym: rfb.KeysymShiftL}) &&
			keys[5] == (rfbtest.KeyEvent{Down: true, Keysym: rfb.KeysymTab}) &&
			keys[6] == (rfbtest.KeyEvent{Down: false, Keysym: rfb.KeysymTab}) &&
			keys[7] == (rfbtest.KeyEvent{Down: false, Keysym: rfb.KeysymShiftL})
	})

	f.tester.Key(ui.Alt, ui.KeyTab)
	alt := rfb.KeysymAltL
	if runtime.GOOS == "darwin" {
		alt = rfb.KeysymISOLevel3Shift
	}
	waitUntil(t, "Alt+Tab", func() bool {
		keys := f.server.Received().Keys
		if len(keys) != 12 {
			return false
		}
		return keys[8] == (rfbtest.KeyEvent{Down: true, Keysym: alt}) &&
			keys[9] == (rfbtest.KeyEvent{Down: true, Keysym: rfb.KeysymTab}) &&
			keys[10] == (rfbtest.KeyEvent{Down: false, Keysym: rfb.KeysymTab}) &&
			keys[11] == (rfbtest.KeyEvent{Down: false, Keysym: alt})
	})

	if err := f.surface.SendCtrlAltDel(); err != nil {
		t.Fatalf("SendCtrlAltDel: %v", err)
	}
	waitKeys(t, f.server, append(allKeys(), []rfbtest.KeyEvent{
		{Down: true, Keysym: rfb.KeysymControlL},
		{Down: true, Keysym: rfb.KeysymAltL},
		{Down: true, Keysym: rfb.KeysymDelete},
		{Down: false, Keysym: rfb.KeysymDelete},
		{Down: false, Keysym: rfb.KeysymAltL},
		{Down: false, Keysym: rfb.KeysymControlL},
	}...))
}

// allKeys is the prefix the previous steps in TestKeyboardSpecialCombinations
// sent: Ctrl+A, Shift+Tab and Alt+Tab.
func allKeys() []rfbtest.KeyEvent {
	alt := rfb.KeysymAltL
	if runtime.GOOS == "darwin" {
		alt = rfb.KeysymISOLevel3Shift
	}
	return []rfbtest.KeyEvent{
		{Down: true, Keysym: rfb.KeysymControlL},
		{Down: true, Keysym: 0x61},
		{Down: false, Keysym: 0x61},
		{Down: false, Keysym: rfb.KeysymControlL},
		{Down: true, Keysym: rfb.KeysymShiftL},
		{Down: true, Keysym: rfb.KeysymTab},
		{Down: false, Keysym: rfb.KeysymTab},
		{Down: false, Keysym: rfb.KeysymShiftL},
		{Down: true, Keysym: alt},
		{Down: true, Keysym: rfb.KeysymTab},
		{Down: false, Keysym: rfb.KeysymTab},
		{Down: false, Keysym: alt},
	}
}

// TestKeyboardPhysicalFallbackAndDrop proves a key reported only by its
// physical position still maps (a dead key), while a key with no identity such
// as macOS Fn is dropped rather than guessed.
func TestKeyboardPhysicalFallbackAndDrop(t *testing.T) {
	f := newFixture(t)
	f.tester.Frame()
	f.tester.Key(0, ui.KeyUnknown)
	f.tester.KeyAt(0, ui.KeyQuote)
	waitKeys(t, f.server, []rfbtest.KeyEvent{
		{Down: true, Keysym: 0x27},
		{Down: false, Keysym: 0x27},
	})
}

// TestPureKeysymModeIgnoresTextInput proves the input method is disabled and
// typed or composed text never reaches the guest, so the guest's own handling
// is not doubled.
func TestPureKeysymModeIgnoresTextInput(t *testing.T) {
	f := newFixture(t)
	if _, noIME := f.tester.TextInputActive(); !noIME {
		t.Fatal("the viewer did not disable the input method")
	}
	f.tester.Type("héllo")
	f.tester.Compose("にほん", 1)
	f.tester.Type("世界")
	// A later real key proves the text events are not merely queued behind a
	// slow path: only the key's two events are recorded.
	f.tester.Key(0, ui.KeyB)
	waitKeys(t, f.server, []rfbtest.KeyEvent{
		{Down: true, Keysym: 0x62},
		{Down: false, Keysym: 0x62},
	})
}

// TestWindowBlurReleasesHeldKey proves switching away from the window while a
// key is held releases it in the guest. MyGo's SurfaceBlur clears window focus
// without delivering key-up events, so the viewer must synthesize the release
// from the focus transition it observes while building the frame.
func TestWindowBlurReleasesHeldKey(t *testing.T) {
	f := newFixture(t)
	f.tester.Frame()
	f.tester.KeyDown(ui.KeyK)
	waitKeys(t, f.server, []rfbtest.KeyEvent{{Down: true, Keysym: 0x6b}})

	f.tester.SetFocused(false)
	f.tester.Frame()
	waitKeys(t, f.server, []rfbtest.KeyEvent{
		{Down: true, Keysym: 0x6b},
		{Down: false, Keysym: 0x6b},
	})
}

// TestWindowBlurBeforeFocusSendsNoReleases proves startup does not storm the
// guest with releases: a window that was never observed focused, or one that
// blurs with nothing held, sends no key events at all.
func TestWindowBlurBeforeFocusSendsNoReleases(t *testing.T) {
	f := newFixture(t)
	f.tester.SetFocused(false)
	f.tester.Frame()
	if keys := f.server.Received().Keys; len(keys) != 0 {
		t.Fatalf("startup sent %+v, want no releases", keys)
	}
	if events := f.server.Received().ExtendedKeys; len(events) != 0 {
		t.Fatalf("startup sent extended key events %+v, want none", events)
	}
}

// TestPaneFocusLossReleasesHeldKey keeps the other release path covered: when
// the Pane itself loses focus (not the window), a held key is released too.
func TestPaneFocusLossReleasesHeldKey(t *testing.T) {
	focused := true
	f := newFixtureWithRoot(t, func(c *ui.Context, surface *vncui.Surface) ui.Element {
		return surface.ViewFocused(c, focused)
	})
	f.tester.Frame()
	f.tester.KeyDown(ui.KeyK)
	waitKeys(t, f.server, []rfbtest.KeyEvent{{Down: true, Keysym: 0x6b}})

	focused = false
	f.tester.Frame()
	waitKeys(t, f.server, []rfbtest.KeyEvent{
		{Down: true, Keysym: 0x6b},
		{Down: false, Keysym: 0x6b},
	})
}

// TestWindowBlurReleasesHeldPointerButton proves window blur leaves no pointer
// button held: the guest sees the press followed by an empty mask.
func TestWindowBlurReleasesHeldPointerButton(t *testing.T) {
	f := newFixture(t)
	waitUntil(t, "the frame", func() bool {
		f.tester.Frame()
		return f.tester.Image().RGBAAt(35, 15) == red
	})
	f.tester.Press(35, 15)
	waitUntil(t, "the pointer press", func() bool {
		pointers := f.server.Received().Pointers
		return len(pointers) > 0 && pointers[len(pointers)-1].Mask&1 != 0
	})

	f.tester.SetFocused(false)
	f.tester.Frame()
	waitUntil(t, "the pointer release", func() bool {
		pointers := f.server.Received().Pointers
		return len(pointers) > 0 && pointers[len(pointers)-1].Mask == 0
	})
}

// TestExtendedKeyEventsWhenNegotiated proves the viewer sends a QEMU Extended
// Key Event with the physical key number once the server acknowledged the
// pseudo-encoding. The tester's KeyAt reports a physical position, as a real
// platform does.
func TestExtendedKeyEventsWhenNegotiated(t *testing.T) {
	f := newFixtureWithOptions(t, fixtureOptions{configure: func(cfg *rfbtest.Config) {
		cfg.ExtendedKeyEvent = true
	}})
	// The acknowledgement is written before the first framebuffer update, so
	// rendering that update means the client has already read the ack. Waiting
	// only for the client to send SetEncodings would race the reader under
	// -race -count=20 and could send plain KeyEvents before the ack arrived.
	waitUntil(t, "the first rendered frame", func() bool {
		f.tester.Frame()
		return f.tester.Image().RGBAAt(35, 15) == red
	})
	f.tester.KeyAt(0, ui.KeyA)
	waitUntil(t, "the extended key events", func() bool {
		events := f.server.Received().ExtendedKeys
		return len(events) == 2 &&
			events[0] == (rfbtest.ExtendedKeyEvent{Down: true, Keysym: 0x61, Keycode: 30}) &&
			events[1] == (rfbtest.ExtendedKeyEvent{Down: false, Keysym: 0x61, Keycode: 30})
	})
	if keys := f.server.Received().Keys; len(keys) != 0 {
		t.Fatalf("plain key events = %+v, want only extended events", keys)
	}
}

// TestPointerMasksWheelAndFitCoordinates proves hover sends an empty mask,
// every held button combines in the mask, the wheel is buttons 5/4 pressed and
// released, and all coordinates pass through the fit rectangle (including the
// letterbox offset and clamping).
func TestPointerMasksWheelAndFitCoordinates(t *testing.T) {
	f := newFixture(t)
	waitUntil(t, "the frame", func() bool {
		f.tester.Frame()
		return f.tester.Image().RGBAAt(35, 15) == red
	})

	f.tester.Move(35, 15)          // local (35,15) -> remote (0,0), hover
	f.tester.Press(35, 15)         // primary down
	f.tester.Move(80, 60)          // drag to remote (2,2), primary still held
	f.tester.Release(80, 60)       // primary up at remote (2,2)
	f.tester.RightClickAt(140, 15) // secondary at letterbox-clamped (3,0)
	f.tester.Wheel(140, 15, 0, 1)  // wheel down
	f.tester.Wheel(140, 15, 0, -1) // wheel up

	want := []rfbtest.PointerEvent{
		{Mask: 0x00, X: 0, Y: 0}, // hover
		{Mask: 0x01, X: 0, Y: 0}, // primary down
		{Mask: 0x01, X: 2, Y: 2}, // drag keeps the held button
		{Mask: 0x00, X: 2, Y: 2}, // primary up
		{Mask: 0x00, X: 3, Y: 0}, // hover before the press
		{Mask: 0x04, X: 3, Y: 0}, // secondary (right) down
		{Mask: 0x00, X: 3, Y: 0}, // secondary up
		{Mask: 0x10, X: 3, Y: 0}, // button 5 down
		{Mask: 0x00, X: 3, Y: 0},
		{Mask: 0x08, X: 3, Y: 0}, // button 4 down
		{Mask: 0x00, X: 3, Y: 0},
	}
	waitUntil(t, "some pointer events", func() bool {
		return len(f.server.Received().Pointers) >= 11
	})
	got := f.server.Received().Pointers
	if len(got) != len(want) {
		t.Fatalf("pointer events = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("pointer events = %+v, want %+v", got, want)
		}
	}
}

// TestPointerClickCarriesModifiers proves a Shift-click reaches the guest with
// Shift held: MyGo reports the modifier as a bit on the pointer event, not as
// a key event, so the viewer synthesizes the keysyms around the click.
func TestPointerClickCarriesModifiers(t *testing.T) {
	f := newFixture(t)
	waitUntil(t, "the frame", func() bool {
		f.tester.Frame()
		return f.tester.Image().RGBAAt(35, 15) == red
	})
	f.tester.ClickAtWith(ui.Shift, 35, 15)
	waitKeys(t, f.server, []rfbtest.KeyEvent{
		{Down: true, Keysym: rfb.KeysymShiftL},
		{Down: false, Keysym: rfb.KeysymShiftL},
	})
	pointers := f.server.Received().Pointers
	if len(pointers) < 2 || pointers[len(pointers)-2].Mask != 1 || pointers[len(pointers)-1].Mask != 0 {
		t.Fatalf("pointer events = %+v, want primary down and up", pointers)
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

func desktopSizes(server *rfbtest.Server) []rfbtest.DesktopSizeRequest {
	return server.Received().DesktopSizes
}

func requestedSize(server *rfbtest.Server, width, height uint16) bool {
	for _, request := range desktopSizes(server) {
		if request.Width == width && request.Height == height {
			return true
		}
	}
	return false
}

// TestPaneResizeAsksTheServerForTheMatchingDesktopSize proves ExtendedDesktopSize
// is used for both the initial pane fit and a later pane resize, and that the
// server's follow-up resize reaches the Session snapshot.
func TestPaneResizeAsksTheServerForTheMatchingDesktopSize(t *testing.T) {
	f := newFixtureWithOptions(t, fixtureOptions{configure: func(cfg *rfbtest.Config) {
		cfg.ExtendedDesktopSize = true
	}})
	f.tester.Frame()
	waitUntil(t, "the initial 160x120 resize", func() bool {
		f.tester.Frame()
		return requestedSize(f.server, 160, 120)
	})

	f.tester.SetSize(80, 60)
	waitUntil(t, "the 80x60 resize", func() bool {
		f.tester.Frame()
		return requestedSize(f.server, 80, 60)
	})
	waitUntil(t, "the Session to adopt the server's size", func() bool {
		f.tester.Frame()
		snapshot := f.session.Snapshot()
		return snapshot.DisplayWidth == 80 && snapshot.DisplayHeight == 60
	})
	if f.surface.LocalScaling() {
		t.Fatal("a supporting server was reported as local scaling")
	}
}

// TestServerWithoutResizeKeepsLocalScaling proves the fallback: when the server
// never negotiates ExtendedDesktopSize the viewer stops asking, keeps fitting
// locally and exposes a notice the chrome can show.
func TestServerWithoutResizeKeepsLocalScaling(t *testing.T) {
	f := newFixture(t)
	waitUntil(t, "the local scaling fallback", func() bool {
		f.tester.Frame()
		return f.surface.LocalScaling()
	})
	if notice := f.surface.ResizeFallbackNotice(); notice == "" {
		t.Fatal("the local scaling fallback has no notice")
	}
	if len(desktopSizes(f.server)) != 0 {
		t.Fatalf("the test server received %+v despite not supporting resize", desktopSizes(f.server))
	}

	// The pane still renders the letterboxed desktop after a local resize.
	f.tester.SetSize(80, 60)
	var shot *image.RGBA
	waitUntil(t, "the local-scaled frame", func() bool {
		f.tester.Frame()
		shot = f.tester.Image()
		return shot.RGBAAt(20, 15) == red && shot.RGBAAt(60, 45) == blue
	})
}

// TestServerRefusingResizeShowsTheFallback proves a nonzero ExtendedDesktopSize
// status leaves local scaling in place and exposes the notice, rather than
// silently caching the rejected size.
func TestServerRefusingResizeShowsTheFallback(t *testing.T) {
	f := newFixtureWithOptions(t, fixtureOptions{configure: func(cfg *rfbtest.Config) {
		cfg.ExtendedDesktopSize = true
		cfg.DesktopSizeStatus = 1
	}})
	f.tester.Frame()
	waitUntil(t, "the resize request to reach the refusing server", func() bool {
		f.tester.Frame()
		return len(desktopSizes(f.server)) > 0
	})
	waitUntil(t, "the local scaling fallback", func() bool {
		f.tester.Frame()
		return f.surface.LocalScaling()
	})
	if notice := f.surface.ResizeFallbackNotice(); notice == "" {
		t.Fatal("a refused resize left no fallback notice")
	}

	// The refused size is not retried forever: a later pane change leaves the
	// latched fallback in place and still renders the letterboxed desktop.
	f.tester.SetSize(80, 60)
	var shot *image.RGBA
	waitUntil(t, "the fallback frame after a pane change", func() bool {
		f.tester.Frame()
		shot = f.tester.Image()
		return shot.RGBAAt(20, 15) == red && shot.RGBAAt(60, 45) == blue
	})
	if !f.surface.LocalScaling() {
		t.Fatal("the fallback was cleared after a refused resize")
	}
}

// TestForwardedResizeStaysPendingThenCompletes proves ExtendedDesktopSize
// status 4 ("request forwarded") does not latch local scaling: the pane keeps
// the same size pending, and a later server-initiated resize to that size
// completes it and clears the request.
func TestForwardedResizeStaysPendingThenCompletes(t *testing.T) {
	f := newFixtureWithOptions(t, fixtureOptions{configure: func(cfg *rfbtest.Config) {
		cfg.ExtendedDesktopSize = true
		cfg.DesktopSizeStatus = 4
		cfg.DesktopSizeFollowUp = true
	}})
	f.tester.Frame()
	waitUntil(t, "the pane-size request", func() bool {
		f.tester.Frame()
		return requestedSize(f.server, 160, 120)
	})
	waitUntil(t, "the forwarded resize to complete", func() bool {
		f.tester.Frame()
		snapshot := f.session.Snapshot()
		return snapshot.DisplayWidth == 160 && snapshot.DisplayHeight == 120
	})
	// Let redraws run at the new size; the pending request must not be re-sent
	// and the fallback must never latch.
	for i := 0; i < 5; i++ {
		f.tester.Frame()
	}
	if f.surface.LocalScaling() {
		t.Fatal("a forwarded (status 4) resize latched local scaling")
	}
	if notice := f.surface.ResizeFallbackNotice(); notice != "" {
		t.Fatalf("a forwarded resize showed the fallback notice: %q", notice)
	}
	if sizes := desktopSizes(f.server); len(sizes) != 1 {
		t.Fatalf("desktop size requests = %+v, want one pending request", sizes)
	}
}

// TestForwardedResizeWithoutFollowUpKeepsTheRequest proves a status-4 answer
// with no follow-up is not retried at the same pane size, a different pane
// size still retries, and the fallback never latches.
func TestForwardedResizeWithoutFollowUpKeepsTheRequest(t *testing.T) {
	f := newFixtureWithOptions(t, fixtureOptions{configure: func(cfg *rfbtest.Config) {
		cfg.ExtendedDesktopSize = true
		cfg.DesktopSizeStatus = 4
		cfg.DesktopSizeFollowUp = false
	}})
	f.tester.Frame()
	waitUntil(t, "the first pane-size request", func() bool {
		f.tester.Frame()
		return requestedSize(f.server, 160, 120)
	})
	// Redraws at the same pane size must not spam the silent server.
	for i := 0; i < 10; i++ {
		f.tester.Frame()
	}
	if sizes := desktopSizes(f.server); len(sizes) != 1 {
		t.Fatalf("desktop size requests = %+v, want exactly one pending request", sizes)
	}
	if f.surface.LocalScaling() {
		t.Fatal("a forwarded (status 4) resize latched local scaling")
	}

	// A different pane size is allowed to retry.
	f.tester.SetSize(80, 60)
	waitUntil(t, "a retry at the new pane size", func() bool {
		f.tester.Frame()
		return requestedSize(f.server, 80, 60)
	})
	if f.surface.LocalScaling() {
		t.Fatal("a retry after a forwarded resize latched local scaling")
	}
	if notice := f.surface.ResizeFallbackNotice(); notice != "" {
		t.Fatalf("a forwarded resize showed the fallback notice: %q", notice)
	}
}

// TestRefusedResizeStatusesLatchTheFallback proves statuses 1, 2 and 3 (and an
// unknown nonzero status, which fails safe) leave local scaling in place.
func TestRefusedResizeStatusesLatchTheFallback(t *testing.T) {
	for _, status := range []uint8{1, 2, 3, 9} {
		t.Run(fmt.Sprintf("status-%d", status), func(t *testing.T) {
			f := newFixtureWithOptions(t, fixtureOptions{configure: func(cfg *rfbtest.Config) {
				cfg.ExtendedDesktopSize = true
				cfg.DesktopSizeStatus = status
			}})
			f.tester.Frame()
			waitUntil(t, "the resize request", func() bool {
				f.tester.Frame()
				return len(desktopSizes(f.server)) > 0
			})
			waitUntil(t, "the local scaling fallback", func() bool {
				f.tester.Frame()
				return f.surface.LocalScaling()
			})
		})
	}
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
