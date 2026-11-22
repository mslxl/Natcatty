package vnc_test

import (
	"image"
	"image/color"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
	"github.com/mslxl/natcatty/domain/model/rfb"
	"github.com/mslxl/natcatty/domain/model/rfb/rfbtest"
	modelvnc "github.com/mslxl/natcatty/domain/model/vnc"
	"github.com/mslxl/natcatty/ui/locales"
	vncui "github.com/mslxl/natcatty/ui/vnc"
)

var (
	ink = color.RGBA{R: 10, G: 20, B: 30, A: 255}
)

// vncChromeRoot draws the Pane chrome above the desktop, as the shell does.
func vncChromeRoot(c *ui.Context, surface *vncui.Surface) ui.Element {
	return ui.Column(c).Fill().Children(func() {
		ui.Row(c).FillWidth().Height(30).Gap(4).AlignItems(ui.Center).Shrink(0).Children(func() {
			ui.Column(c).Grow(1)
			surface.Toolbar(c)
		})
		ui.Column(c).Grow(1).FillWidth().MinHeight(0).Children(func() {
			surface.ViewFocused(c, true)
		})
	})
}

func clipFrame() *image.RGBA {
	frame := image.NewRGBA(image.Rect(0, 0, 320, 240))
	for y := 0; y < 240; y++ {
		for x := 0; x < 320; x++ {
			frame.SetRGBA(x, y, ink)
		}
	}
	frame.SetRGBA(80, 60, red)
	frame.SetRGBA(60, 50, blue)
	return frame
}

func cursorShape() *image.RGBA {
	cursor := image.NewRGBA(image.Rect(0, 0, 2, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 2; x++ {
			cursor.SetRGBA(x, y, green)
		}
	}
	return cursor
}

func lastPointer(server *rfbtest.Server) (rfbtest.PointerEvent, bool) {
	pointers := server.Received().Pointers
	if len(pointers) == 0 {
		return rfbtest.PointerEvent{}, false
	}
	return pointers[len(pointers)-1], true
}

// TestScaleCycleDrawsFitOneToOneAndClip drives the three modes the chrome
// cycles through. Fit letterboxes and scales, 1:1 and clip draw the owned
// bitmap one remote pixel per pane pixel, and each mode's pointer inversion
// names the pixel actually shown under the pointer.
func TestScaleCycleDrawsFitOneToOneAndClip(t *testing.T) {
	f := newFixture(t)
	waitUntil(t, "the fitted frame", func() bool {
		f.tester.Frame()
		shot := f.tester.Image()
		return shot.RGBAAt(35, 15) == red && shot.RGBAAt(5, 60) != red && shot.RGBAAt(5, 60) != blue
	})
	if f.surface.ScaleMode() != modelvnc.Fit {
		t.Fatalf("initial scale = %q, want fit", f.surface.ScaleMode())
	}

	if mode := f.surface.CycleScale(); mode != modelvnc.OneToOne {
		t.Fatalf("first cycle = %q, want 1:1", mode)
	}
	waitUntil(t, "the one-to-one frame", func() bool {
		f.tester.Frame()
		shot := f.tester.Image()
		// A 4x4 desktop centered in 160x120 starts at (78,58), unscaled.
		return shot.RGBAAt(78, 58) == red && shot.RGBAAt(80, 58) == blue
	})
	shot := f.tester.Image()
	if pixel := shot.RGBAAt(5, 5); pixel == red || pixel == blue {
		t.Fatalf("1:1 pixel outside the desktop = %+v, want the pane background", pixel)
	}
	f.tester.Move(78, 58)
	waitUntil(t, "the 1:1 pointer", func() bool {
		event, ok := lastPointer(f.server)
		return ok && event.X == 0 && event.Y == 0
	})

	if mode := f.surface.CycleScale(); mode != modelvnc.Clip {
		t.Fatalf("second cycle = %q, want clip", mode)
	}
	waitUntil(t, "the clip frame", func() bool {
		f.tester.Frame()
		shot := f.tester.Image()
		return shot.RGBAAt(78, 58) == red && shot.RGBAAt(80, 58) == blue
	})
	f.tester.Move(78, 58)
	waitUntil(t, "the clip pointer", func() bool {
		event, ok := lastPointer(f.server)
		return ok && event.X == 0 && event.Y == 0
	})

	if mode := f.surface.CycleScale(); mode != modelvnc.Fit {
		t.Fatalf("third cycle = %q, want fit", mode)
	}
	waitUntil(t, "the fitted frame to return", func() bool {
		f.tester.Frame()
		return f.tester.Image().RGBAAt(35, 15) == red
	})
}

// TestOneToOneRecentersAndInvertsPointerAfterResize proves a pane resize in
// 1:1 mode recenters the framed desktop and keeps the pointer mapping in step:
// the bitmap is never stretched and a pixel outside it is not claimed.
func TestOneToOneRecentersAndInvertsPointerAfterResize(t *testing.T) {
	config := modelvnc.DefaultConfig()
	config.FitMode = modelvnc.OneToOne
	f := newFixtureWithOptions(t, fixtureOptions{config: &config})
	waitUntil(t, "the one-to-one frame", func() bool {
		f.tester.Frame()
		return f.tester.Image().RGBAAt(78, 58) == red
	})

	f.tester.SetSize(80, 60)
	waitUntil(t, "the recentered frame", func() bool {
		f.tester.Frame()
		return f.tester.Image().RGBAAt(38, 28) == red
	})
	f.tester.Move(38, 28)
	waitUntil(t, "the recentered pointer", func() bool {
		event, ok := lastPointer(f.server)
		return ok && event.X == 0 && event.Y == 0
	})
	f.tester.Move(79, 59)
	waitUntil(t, "the clamped corner pointer", func() bool {
		event, ok := lastPointer(f.server)
		return ok && event.X == 3 && event.Y == 3
	})
}

// TestClipDragPansAndClampsTheViewport drags a larger-than-pane desktop. The
// first drag reveals a different remote pixel, the second stops flush at the
// far edge, and no drag writes a pointer button to the guest.
func TestClipDragPansAndClampsTheViewport(t *testing.T) {
	config := modelvnc.DefaultConfig()
	config.FitMode = modelvnc.Clip
	f := newFixtureWithOptions(t, fixtureOptions{frame: clipFrame(), config: &config})
	// Centered, the pane shows remote (80,60) at its origin.
	waitUntil(t, "the centered clip frame", func() bool {
		f.tester.Frame()
		return f.tester.Image().RGBAAt(0, 0) == red
	})

	f.tester.Press(80, 60)
	f.tester.Move(100, 70)
	f.tester.Release(100, 70)
	waitUntil(t, "the panned client", func() bool {
		f.tester.Frame()
		return f.tester.Image().RGBAAt(0, 0) == blue
	})

	// No press, move, or release during the drag reached the guest. MyGo
	// reports a hover move with the press, which carries no button; what must
	// never appear is a button mask.
	for _, pointer := range f.server.Received().Pointers {
		if pointer.Mask != 0 {
			t.Fatalf("panning wrote button mask %d at %d,%d, want none",
				pointer.Mask, pointer.X, pointer.Y)
		}
	}

	// The pointer mapping follows the pan offset.
	f.tester.Move(0, 0)
	waitUntil(t, "the panned pointer", func() bool {
		event, ok := lastPointer(f.server)
		return ok && event.X == 60 && event.Y == 50
	})

	// A drag past the edge clamps flush with the desktop's origin.
	f.tester.Press(0, 0)
	f.tester.Move(159, 119)
	f.tester.Release(159, 119)
	// Move away and back: the tester's report deduplicates an unchanged
	// position, and this hover must be a fresh event.
	f.tester.Move(0, 0)
	f.tester.Move(159, 119)
	waitUntil(t, "the clamped pointer", func() bool {
		event, ok := lastPointer(f.server)
		return ok && event.X == 159 && event.Y == 119
	})
	for _, pointer := range f.server.Received().Pointers {
		if pointer.Mask != 0 {
			t.Fatalf("the clamped drag wrote button mask %d at %d,%d", pointer.Mask, pointer.X, pointer.Y)
		}
	}
}

// TestViewOnlySuppressesInputAndKeepsFrames starts view-only from the Host's
// saved configuration. Frames still arrive and render, remote clipboard text
// still reaches the local clipboard, and no key, pointer, or wheel event is
// written to the guest.
func TestViewOnlySuppressesInputAndKeepsFrames(t *testing.T) {
	config := modelvnc.DefaultConfig()
	config.ViewOnly = true
	f := newFixtureWithOptions(t, fixtureOptions{
		config: &config,
		configure: func(cfg *rfbtest.Config) {
			cfg.CutText = "caf\xe9"
		},
	})
	if !f.surface.ViewOnly() {
		t.Fatal("the viewer did not start view-only")
	}
	waitUntil(t, "the view-only frame and remote clipboard", func() bool {
		f.tester.Frame()
		return f.tester.Image().RGBAAt(35, 15) == red &&
			f.tester.Clipboard() == "café"
	})
	if got := f.surface.LastRemoteText(); got != "café" {
		t.Fatalf("last remote text = %q, want café", got)
	}

	f.tester.KeyDown(ui.KeyK)
	f.tester.KeyUp(ui.KeyK)
	f.tester.Move(35, 15)
	f.tester.Press(35, 15)
	f.tester.Release(35, 15)
	f.tester.Wheel(35, 15, 0, 10)
	f.tester.Frame()

	received := f.server.Received()
	if len(received.Keys) != 0 || len(received.ExtendedKeys) != 0 || len(received.Pointers) != 0 {
		t.Fatalf("view-only wrote keys %+v, extended %+v, pointers %+v",
			received.Keys, received.ExtendedKeys, received.Pointers)
	}
	// Frames still render while view-only.
	if f.tester.Image().RGBAAt(35, 15) != red {
		t.Fatal("the view-only frame stopped rendering")
	}

	// Pulling the remote clipboard is a local action and still works.
	f.tester.SetClipboard("")
	if result := f.surface.PullClipboard(f.tester.SetClipboard); result != vncui.ClipboardPulled {
		t.Fatalf("PullClipboard = %v, want pulled", result)
	}
	if got := f.tester.Clipboard(); got != "café" {
		t.Fatalf("pulled clipboard = %q, want café", got)
	}
}

// TestEnteringViewOnlyReleasesHeldInput proves a held key and pointer button
// are released on the wire when view-only begins, so nothing stays stuck, and
// later input writes nothing.
func TestEnteringViewOnlyReleasesHeldInput(t *testing.T) {
	f := newFixture(t)
	waitUntil(t, "the first frame", func() bool {
		f.tester.Frame()
		return f.tester.Image().RGBAAt(35, 15) == red
	})

	f.tester.KeyDown(ui.KeyK)
	waitUntil(t, "the held key", func() bool {
		return len(f.server.Received().Keys) == 1
	})
	f.tester.Press(35, 15)
	waitUntil(t, "the held pointer button", func() bool {
		event, ok := lastPointer(f.server)
		return ok && event.Mask == 1
	})

	f.surface.SetViewOnly(true)
	waitUntil(t, "the released key and button", func() bool {
		keys := f.server.Received().Keys
		event, ok := lastPointer(f.server)
		return len(keys) == 2 && !keys[1].Down && ok && event.Mask == 0
	})

	f.tester.KeyDown(ui.KeyJ)
	f.tester.KeyUp(ui.KeyJ)
	f.tester.Press(35, 15)
	f.tester.Release(35, 15)
	f.tester.Frame()
	if keys := f.server.Received().Keys; len(keys) != 2 {
		t.Fatalf("view-only keys = %+v, want only the two release events", keys)
	}
}

// TestClipboardLatin1ConversionAndResults covers the encoding rules directly:
// empty text, Latin-1 text, a non-Latin-1 rune, and the exact size limit, with
// no truncation and no oversized result.
func TestClipboardLatin1ConversionAndResults(t *testing.T) {
	if encoded, result := vncui.EncodeLatin1(""); result != vncui.ClipboardEmpty || encoded != nil {
		t.Fatalf("EncodeLatin1(empty) = %v, %v; want nil, empty", encoded, result)
	}
	encoded, result := vncui.EncodeLatin1("café")
	if result != vncui.ClipboardSent || string(encoded) != "caf\xe9" {
		t.Fatalf("EncodeLatin1(café) = %q, %v; want caf\\xe9, sent", encoded, result)
	}
	if got := vncui.DecodeLatin1(string(encoded)); got != "café" {
		t.Fatalf("DecodeLatin1 round trip = %q, want café", got)
	}
	if _, result := vncui.EncodeLatin1("中"); result != vncui.ClipboardNotLatin1 {
		t.Fatalf("EncodeLatin1(中) = %v, want not Latin-1", result)
	}
	if _, result := vncui.EncodeLatin1("Ā"); result != vncui.ClipboardNotLatin1 {
		t.Fatalf("EncodeLatin1(U+0100) = %v, want not Latin-1", result)
	}
	if encoded, result := vncui.EncodeLatin1("ÿ"); result != vncui.ClipboardSent || len(encoded) != 1 || encoded[0] != 0xff {
		t.Fatalf("EncodeLatin1(U+00FF) = %v, %v; want one 0xFF byte", encoded, result)
	}

	limit := strings.Repeat("a", vncui.MaxClipboardBytes)
	encoded, result = vncui.EncodeLatin1(limit)
	if result != vncui.ClipboardSent || len(encoded) != vncui.MaxClipboardBytes {
		t.Fatalf("EncodeLatin1(limit) = %d bytes, %v; want sent at the limit", len(encoded), result)
	}
	if encoded, result := vncui.EncodeLatin1(limit + "a"); result != vncui.ClipboardTooLarge || encoded != nil {
		t.Fatalf("EncodeLatin1(limit+1) = %d bytes, %v; want rejected", len(encoded), result)
	}
	// A much larger paste is rejected with the same explicit result and must
	// not size a buffer from its full length.
	large := strings.Repeat("a", 8*vncui.MaxClipboardBytes)
	if encoded, result := vncui.EncodeLatin1(large); result != vncui.ClipboardTooLarge || encoded != nil {
		t.Fatalf("EncodeLatin1(8*limit) = %d bytes, %v; want rejected", len(encoded), result)
	}
}

// TestClipboardBothDirections drives the real adapter: a ServerCutText becomes
// the local clipboard in Latin-1, a local send reaches the server, and empty,
// oversized, and non-Latin-1 pastes are rejected without writing anything. The
// notice sentence follows the active locale.
func TestClipboardBothDirections(t *testing.T) {
	f := newFixtureWithOptions(t, fixtureOptions{configure: func(cfg *rfbtest.Config) {
		cfg.CutText = "caf\xe9 \xff"
	}})
	f.tester.SetLocale("zh-CN")
	waitUntil(t, "the remote clipboard", func() bool {
		f.tester.Frame()
		return f.tester.Clipboard() == "café ÿ"
	})
	if got := f.surface.LastRemoteText(); got != "café ÿ" {
		t.Fatalf("last remote text = %q, want café ÿ", got)
	}
	if !strings.Contains(f.surface.ClipboardMessage(vncui.ClipboardEmpty),
		locales.In("zh-CN").SessionVNCClipboardEmpty()) {
		t.Fatalf("empty message = %q, want the zh-CN sentence",
			f.surface.ClipboardMessage(vncui.ClipboardEmpty))
	}

	if result := f.surface.SendClipboard("héllo"); result != vncui.ClipboardSent {
		t.Fatalf("SendClipboard = %v, want sent", result)
	}
	waitUntil(t, "the server clipboard", func() bool {
		texts := f.server.Received().CutTexts
		return len(texts) == 1 && texts[0] == "h\xe9llo"
	})

	before := len(f.server.Received().CutTexts)
	for _, test := range []struct {
		name   string
		text   string
		result vncui.ClipboardResult
	}{
		{name: "empty", text: "", result: vncui.ClipboardEmpty},
		{name: "oversized", text: strings.Repeat("a", vncui.MaxClipboardBytes+1), result: vncui.ClipboardTooLarge},
		{name: "not Latin-1", text: "中", result: vncui.ClipboardNotLatin1},
	} {
		if result := f.surface.SendClipboard(test.text); result != test.result {
			t.Errorf("%s SendClipboard = %v, want %v", test.name, result, test.result)
		}
	}
	if texts := f.server.Received().CutTexts; len(texts) != before {
		t.Fatalf("rejected pastes wrote %q", texts[before:])
	}

	// Pull overwrites the local clipboard with the last remote text.
	f.tester.SetClipboard("local")
	if result := f.surface.PullClipboard(f.tester.SetClipboard); result != vncui.ClipboardPulled {
		t.Fatalf("PullClipboard = %v, want pulled", result)
	}
	if got := f.tester.Clipboard(); got != "café ÿ" {
		t.Fatalf("pulled clipboard = %q, want café ÿ", got)
	}
	if result := f.surface.PullClipboard(nil); result != vncui.ClipboardFailed {
		t.Fatalf("PullClipboard(nil) = %v, want failed", result)
	}
}

// TestClipboardPullBeforeRemoteTextIsExplicit covers the never-silent result
// when the server has not shared anything yet.
func TestClipboardPullBeforeRemoteTextIsExplicit(t *testing.T) {
	f := newFixture(t)
	f.tester.Frame()
	f.tester.SetClipboard("local")
	if result := f.surface.PullClipboard(f.tester.SetClipboard); result != vncui.ClipboardNoRemoteText {
		t.Fatalf("PullClipboard = %v, want no remote text", result)
	}
	if got := f.tester.Clipboard(); got != "local" {
		t.Fatalf("clipboard = %q, want the local text untouched", got)
	}
}

// TestClipboardToolbarSendsAndPulls clicks the per-kind chrome rather than
// calling the methods, proving the buttons are wired to the same actions and
// report their localized outcome.
func TestClipboardToolbarSendsAndPulls(t *testing.T) {
	f := newFixtureWithOptions(t, fixtureOptions{
		root: vncChromeRoot,
		configure: func(cfg *rfbtest.Config) {
			cfg.CutText = "desde"
		},
	})
	f.tester.SetSize(1000, 240)
	f.tester.SetLocale("en")
	f.tester.Frame()
	f.tester.Frame()
	m := locales.In("en")

	// The first ServerCutText already reached the local clipboard; the send
	// below overwrites it with the user's own text.
	waitUntil(t, "the automatic remote clipboard", func() bool {
		f.tester.Frame()
		return f.tester.Clipboard() == "desde"
	})
	f.tester.SetClipboard("hola")
	if err := f.tester.Click(m.SessionVNCClipboardSend()); err != nil {
		t.Fatalf("click send: %v", err)
	}
	waitUntil(t, "the toolbar send", func() bool {
		texts := f.server.Received().CutTexts
		return len(texts) == 1 && texts[0] == "hola"
	})
	f.tester.Frame()
	if !f.tester.HasText(m.SessionVNCClipboardSent()) {
		t.Fatalf("sent notice is not shown; texts %q", f.tester.Texts())
	}

	f.tester.SetClipboard("")
	if err := f.tester.Click(m.SessionVNCClipboardPull()); err != nil {
		t.Fatalf("click pull: %v", err)
	}
	if got := f.tester.Clipboard(); got != "desde" {
		t.Fatalf("pulled clipboard = %q, want desde", got)
	}
	f.tester.Frame()
	if !f.tester.HasText(m.SessionVNCClipboardPulled()) {
		t.Fatalf("pulled notice is not shown; texts %q", f.tester.Texts())
	}
}

// TestLocalCursorFollowsThePointer proves the local cursor is drawn at the
// remote pointer with the server's hotspot, moves on the next pointer move,
// and is suppressed in server-cursor mode, for a hidden shape, and when no
// shape has arrived.
func TestLocalCursorFollowsThePointer(t *testing.T) {
	f := newFixtureWithOptions(t, fixtureOptions{configure: func(cfg *rfbtest.Config) {
		cfg.Cursor = cursorShape()
		cfg.CursorAt = image.Point{X: 1, Y: 1}
	}})
	if f.surface.CursorMode() != modelvnc.CursorLocal {
		t.Fatalf("cursor mode = %q, want local", f.surface.CursorMode())
	}
	waitUntil(t, "the cursor shape and frame", func() bool {
		f.tester.Frame()
		return f.surface.Cursor() != nil && f.tester.Image().RGBAAt(35, 15) == red
	})

	f.tester.Move(35, 15) // remote (0,0): hotspot (1,1) puts the shape at (-10,-30)
	waitUntil(t, "the cursor at the pointer", func() bool {
		f.tester.Frame()
		shot := f.tester.Image()
		return shot.RGBAAt(25, 5) == green && shot.RGBAAt(15, 5) == green
	})

	f.tester.Move(80, 60) // remote (2,2): the 60x60 shape moves to (50,30)
	waitUntil(t, "the moved cursor", func() bool {
		f.tester.Frame()
		shot := f.tester.Image()
		return shot.RGBAAt(55, 35) == green && shot.RGBAAt(25, 5) == red
	})

	hidden := newFixtureWithOptions(t, fixtureOptions{configure: func(cfg *rfbtest.Config) {
		cfg.HideCursor = true
	}})
	waitUntil(t, "the hidden frame", func() bool {
		hidden.tester.Frame()
		return hidden.tester.Image().RGBAAt(35, 15) == red
	})
	hidden.tester.Move(35, 15)
	hidden.tester.Frame()
	if hidden.tester.Image().RGBAAt(25, 5) == green {
		t.Fatal("a hidden cursor was drawn locally")
	}

	noShape := newFixture(t)
	waitUntil(t, "the shape-less frame", func() bool {
		noShape.tester.Frame()
		return noShape.tester.Image().RGBAAt(35, 15) == red
	})
	noShape.tester.Move(35, 15)
	noShape.tester.Frame()
	if noShape.tester.Image().RGBAAt(25, 5) == green {
		t.Fatal("a cursor was drawn before any shape arrived")
	}

	config := modelvnc.DefaultConfig()
	config.CursorMode = modelvnc.CursorServer
	serverCursor := newFixtureWithOptions(t, fixtureOptions{
		config: &config,
		configure: func(cfg *rfbtest.Config) {
			cfg.Cursor = cursorShape()
			cfg.CursorAt = image.Point{X: 1, Y: 1}
		},
	})
	if serverCursor.surface.CursorMode() != modelvnc.CursorServer {
		t.Fatalf("cursor mode = %q, want server", serverCursor.surface.CursorMode())
	}
	waitUntil(t, "the server-cursor frame", func() bool {
		serverCursor.tester.Frame()
		return serverCursor.tester.Image().RGBAAt(35, 15) == red
	})
	serverCursor.tester.Move(35, 15)
	serverCursor.tester.Frame()
	if serverCursor.tester.Image().RGBAAt(25, 5) == green {
		t.Fatal("server cursor mode drew a local cursor")
	}
}

// TestBellUsesTheSessionHookAndOptionalCue checks the display event reaches the
// existing Session bell hook and the viewer's injected cue, so a bell is
// visible or audible without changing focus.
func TestBellUsesTheSessionHookAndOptionalCue(t *testing.T) {
	hookBells := make(chan struct{}, 1)
	cues := make(chan struct{}, 1)
	f := newFixtureWithOptions(t, fixtureOptions{
		configure: func(cfg *rfbtest.Config) { cfg.SendBell = true },
		serviceOptions: []appsession.Option{appsession.WithBellHook(func(model.SessionID) {
			select {
			case hookBells <- struct{}{}:
			default:
			}
		})},
		surfaceOptions: []vncui.Option{vncui.WithBell(func() {
			select {
			case cues <- struct{}{}:
			default:
			}
		})},
	})
	ensure := func(name string, ch chan struct{}) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for the %s", name)
		}
	}
	ensure("Session bell hook", hookBells)
	ensure("viewer bell cue", cues)
	waitUntil(t, "the desktop after the bell", func() bool {
		f.tester.Frame()
		return f.tester.Image().RGBAAt(35, 15) == red
	})
}

// TestToolbarViewOnlyFullscreenAndGrab clicks the per-kind controls: view-only
// toggles, fullscreen reaches the injected callback instead of opening a
// window, keyboard grab toggles, and Ctrl-Alt-Del injects the chord.
func TestToolbarViewOnlyFullscreenAndGrab(t *testing.T) {
	fullscreen := 0
	f := newFixtureWithOptions(t, fixtureOptions{
		root: vncChromeRoot,
		surfaceOptions: []vncui.Option{vncui.WithFullscreen(func() {
			fullscreen++
		})},
	})
	f.tester.SetSize(1000, 240)
	f.tester.SetLocale("en")
	f.tester.Frame()
	f.tester.Frame()
	m := locales.In("en")

	click := func(label string) {
		t.Helper()
		if err := f.tester.Click(label); err != nil {
			t.Fatalf("click %q: %v", label, err)
		}
		f.tester.Frame()
	}

	click(m.SessionVNCViewOnly())
	if !f.surface.ViewOnly() {
		t.Fatal("the view-only button did not enter view-only")
	}
	click(m.SessionVNCViewOnly())
	if f.surface.ViewOnly() {
		t.Fatal("the view-only button did not leave view-only")
	}

	click(m.SessionVNCFullscreen())
	if fullscreen != 1 {
		t.Fatalf("fullscreen callback calls = %d, want 1", fullscreen)
	}

	click(m.SessionVNCKeyboardGrab())
	if !f.surface.KeyboardGrab() {
		t.Fatal("the keyboard grab button did not enable the grab")
	}
	click(m.SessionVNCKeyboardGrab())
	if f.surface.KeyboardGrab() {
		t.Fatal("the keyboard grab button did not disable the grab")
	}

	click(m.SessionVNCCtrlAltDel())
	waitUntil(t, "the Ctrl-Alt-Del chord", func() bool {
		keys := f.server.Received().Keys
		return len(keys) == 6 &&
			keys[0] == rfbtest.KeyEvent{Down: true, Keysym: rfb.KeysymControlL} &&
			keys[2] == rfbtest.KeyEvent{Down: true, Keysym: rfb.KeysymDelete} &&
			keys[5] == rfbtest.KeyEvent{Down: false, Keysym: rfb.KeysymControlL}
	})
}

// TestLeavingViewOnlyClearsTheClipboardRefusal clicks the real toolbar: a
// view-only clipboard refusal disappears when the user leaves view-only, while
// an unrelated clipboard outcome from before the toggle stays visible.
func TestLeavingViewOnlyClearsTheClipboardRefusal(t *testing.T) {
	f := newFixtureWithOptions(t, fixtureOptions{root: vncChromeRoot})
	f.tester.SetSize(1000, 240)
	f.tester.SetLocale("en")
	f.tester.Frame()
	f.tester.Frame()
	m := locales.In("en")

	click := func(label string) {
		t.Helper()
		if err := f.tester.Click(label); err != nil {
			t.Fatalf("click %q: %v", label, err)
		}
		f.tester.Frame()
	}

	click(m.SessionVNCViewOnly())
	f.tester.SetClipboard("hello")
	click(m.SessionVNCClipboardSend())
	if !f.tester.HasText(m.SessionVNCClipboardViewOnly()) {
		t.Fatalf("no view-only clipboard refusal; texts %q", f.tester.Texts())
	}

	click(m.SessionVNCViewOnly())
	if f.surface.ViewOnly() {
		t.Fatal("the view-only button did not leave view-only")
	}
	if f.tester.HasText(m.SessionVNCClipboardViewOnly()) {
		t.Fatalf("the view-only clipboard refusal lingered after leaving; texts %q", f.tester.Texts())
	}

	// A clipboard result unrelated to view-only describes an action that still
	// stands, so the toggle leaves it in place.
	click(m.SessionVNCViewOnly())
	click(m.SessionVNCClipboardPull()) // no remote text yet
	if !f.tester.HasText(m.SessionVNCClipboardNoRemoteText()) {
		t.Fatalf("no pull result; texts %q", f.tester.Texts())
	}
	click(m.SessionVNCViewOnly())
	if !f.tester.HasText(m.SessionVNCClipboardNoRemoteText()) {
		t.Fatalf("leaving view-only cleared an unrelated clipboard result; texts %q", f.tester.Texts())
	}
}

// TestToolbarCyclesScaleAndRendersTheFallbackNotice proves the toolbar's scale
// button advances the label and mode together, and that a server which cannot
// resize puts its explicit local-scaling notice in the chrome, not over the
// desktop.
func TestToolbarCyclesScaleAndRendersTheFallbackNotice(t *testing.T) {
	f := newFixtureWithOptions(t, fixtureOptions{root: vncChromeRoot})
	f.tester.SetSize(1000, 240)
	f.tester.SetLocale("en")
	f.tester.Frame()
	f.tester.Frame()
	m := locales.In("en")

	for _, step := range []struct {
		from string
		want modelvnc.FitMode
		to   string
	}{
		{from: m.SessionVNCScaleFit(), want: modelvnc.OneToOne, to: m.SessionVNCScaleOneToOne()},
		{from: m.SessionVNCScaleOneToOne(), want: modelvnc.Clip, to: m.SessionVNCScaleClip()},
		{from: m.SessionVNCScaleClip(), want: modelvnc.Fit, to: m.SessionVNCScaleFit()},
	} {
		if err := f.tester.Click(step.from); err != nil {
			t.Fatalf("click %q: %v", step.from, err)
		}
		f.tester.Frame()
		if f.surface.ScaleMode() != step.want {
			t.Fatalf("scale = %q, want %q", f.surface.ScaleMode(), step.want)
		}
		if !f.tester.HasText(step.to) {
			t.Fatalf("scale label %q is not shown; texts %q", step.to, f.tester.Texts())
		}
	}

	waitUntil(t, "the local-scaling fallback", func() bool {
		f.tester.Frame()
		return f.surface.LocalScaling()
	})
	f.tester.Frame()
	if !f.tester.HasText(f.surface.ResizeFallbackNotice()) {
		t.Fatalf("resize fallback %q is not in the chrome; texts %q",
			f.surface.ResizeFallbackNotice(), f.tester.Texts())
	}
}

// TestHostConfigurationSeedsTheViewer proves the viewer starts in the Host's
// saved scale, cursor, and view-only modes, reading them through the Session's
// configuration accessor.
func TestHostConfigurationSeedsTheViewer(t *testing.T) {
	config := modelvnc.DefaultConfig()
	config.FitMode = modelvnc.OneToOne
	config.CursorMode = modelvnc.CursorServer
	config.ViewOnly = true
	f := newFixtureWithOptions(t, fixtureOptions{config: &config})

	fromSession := f.session.VNCConfiguration()
	if fromSession.FitMode != modelvnc.OneToOne || fromSession.CursorMode != modelvnc.CursorServer || !fromSession.ViewOnly {
		t.Fatalf("Session configuration = %+v, want the Host's modes", fromSession)
	}
	if f.surface.ScaleMode() != modelvnc.OneToOne {
		t.Fatalf("scale = %q, want 1:1", f.surface.ScaleMode())
	}
	if f.surface.CursorMode() != modelvnc.CursorServer {
		t.Fatalf("cursor = %q, want server", f.surface.CursorMode())
	}
	if !f.surface.ViewOnly() {
		t.Fatal("viewer did not start view-only")
	}
}

// vncEmbeddedChromeRoot draws the chrome a renderer that is not a host window
// uses: the same row without fullscreen.
func vncEmbeddedChromeRoot(c *ui.Context, surface *vncui.Surface) ui.Element {
	return ui.Column(c).Fill().Children(func() {
		ui.Row(c).FillWidth().Height(30).Gap(4).AlignItems(ui.Center).Shrink(0).Children(func() {
			ui.Column(c).Grow(1)
			surface.ToolbarEmbedded(c)
		})
		ui.Column(c).Grow(1).FillWidth().MinHeight(0).Children(func() {
			surface.ViewFocused(c, true)
		})
	})
}

// TestToolbarEmbeddedOmitsFullscreen proves the two toolbar forms differ only
// in the fullscreen control: a host window offers it, while the embedded form
// used by the Dropdown Panel keeps every other viewer control.
func TestToolbarEmbeddedOmitsFullscreen(t *testing.T) {
	m := locales.In("en")

	host := newFixtureWithOptions(t, fixtureOptions{root: vncChromeRoot})
	host.tester.SetSize(1000, 240)
	host.tester.SetLocale("en")
	host.tester.Frame()
	host.tester.Frame()
	if !host.tester.HasText(m.SessionVNCFullscreen()) {
		t.Fatalf("the host toolbar lost fullscreen: %v", host.tester.Texts())
	}

	embedded := newFixtureWithOptions(t, fixtureOptions{root: vncEmbeddedChromeRoot})
	embedded.tester.SetSize(1000, 240)
	embedded.tester.SetLocale("en")
	embedded.tester.Frame()
	embedded.tester.Frame()
	if embedded.tester.HasText(m.SessionVNCFullscreen()) {
		t.Fatalf("the embedded toolbar offered fullscreen: %v", embedded.tester.Texts())
	}
	for _, want := range []string{
		m.SessionVNCScaleFit(),
		m.SessionVNCViewOnly(),
		m.SessionVNCClipboardSend(),
		m.SessionVNCClipboardPull(),
		m.SessionVNCCtrlAltDel(),
		m.SessionVNCKeyboardGrab(),
	} {
		if !embedded.tester.HasText(want) {
			t.Fatalf("the embedded toolbar lost %q: %v", want, embedded.tester.Texts())
		}
	}
}

// TestViewOnlyToolbarClicksWriteNothing covers the local chrome actions in
// view-only: cycling the scale and toggling the mode write nothing, a host
// fullscreen click still reaches the window callback without touching the
// guest, and the embedded toolbar's click path is equally silent.
func TestViewOnlyToolbarClicksWriteNothing(t *testing.T) {
	m := locales.In("en")
	run := func(name string, root func(*ui.Context, *vncui.Surface) ui.Element, hostWindow bool) {
		t.Run(name, func(t *testing.T) {
			fullscreen := 0
			f := newFixtureWithOptions(t, fixtureOptions{
				root: root,
				surfaceOptions: []vncui.Option{vncui.WithFullscreen(func() {
					fullscreen++
				})},
			})
			f.tester.SetSize(1000, 240)
			f.tester.SetLocale("en")
			f.tester.Frame()
			f.tester.Frame()
			click := func(label string) {
				t.Helper()
				if err := f.tester.Click(label); err != nil {
					t.Fatalf("click %q: %v", label, err)
				}
				f.tester.Frame()
			}

			click(m.SessionVNCViewOnly())
			if !f.surface.ViewOnly() {
				t.Fatal("the view-only button did not enter view-only")
			}
			click(m.SessionVNCScaleFit())
			if hostWindow {
				click(m.SessionVNCFullscreen())
				if fullscreen != 1 {
					t.Fatalf("fullscreen callback calls = %d, want 1", fullscreen)
				}
			} else {
				if f.tester.HasText(m.SessionVNCFullscreen()) {
					t.Fatalf("the embedded toolbar offered fullscreen: %v", f.tester.Texts())
				}
				if fullscreen != 0 {
					t.Fatalf("an embedded control reached fullscreen %d times", fullscreen)
				}
			}
			f.tester.Frame()

			received := f.server.Received()
			if len(received.Keys) != 0 || len(received.ExtendedKeys) != 0 ||
				len(received.Pointers) != 0 || len(received.CutTexts) != 0 {
				t.Fatalf("view-only chrome wrote keys %+v, extended %+v, pointers %+v, cut texts %q",
					received.Keys, received.ExtendedKeys, received.Pointers, received.CutTexts)
			}
		})
	}
	run("host toolbar", vncChromeRoot, true)
	run("embedded toolbar", vncEmbeddedChromeRoot, false)
}

// TestViewOnlySendsNoDesktopResize proves view-only is a strict read-only
// promise: the control fixture asks the guest to resize, while a view-only
// viewer keeps receiving frames and asks for nothing.
func TestViewOnlySendsNoDesktopResize(t *testing.T) {
	enableResize := func(cfg *rfbtest.Config) { cfg.ExtendedDesktopSize = true }

	control := newFixtureWithOptions(t, fixtureOptions{configure: enableResize})
	waitUntil(t, "the control desktop-resize request", func() bool {
		control.tester.Frame()
		return len(control.server.Received().DesktopSizes) > 0
	})

	config := modelvnc.DefaultConfig()
	config.ViewOnly = true
	f := newFixtureWithOptions(t, fixtureOptions{config: &config, configure: enableResize})
	waitUntil(t, "the view-only frame", func() bool {
		f.tester.Frame()
		return f.tester.Image().RGBAAt(35, 15) == red
	})
	f.tester.SetSize(200, 150)
	f.tester.Frame()

	// The control proves a resize is recorded promptly. Render frames for a
	// bounded observation window; any resize in this window is a defect.
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		f.tester.Frame()
		if sizes := f.server.Received().DesktopSizes; len(sizes) != 0 {
			t.Fatalf("view-only requested desktop resize %+v", sizes)
		}
		time.Sleep(time.Millisecond)
	}
	if f.tester.Image().RGBAAt(35, 15) != red {
		t.Fatal("the view-only viewport stopped rendering frames")
	}
}
