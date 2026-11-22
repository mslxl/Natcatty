package vnc_test

import (
	"errors"
	"image"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/domain/model/rfb/rfbtest"
	modelvnc "github.com/mslxl/natcatty/domain/model/vnc"
	"github.com/mslxl/natcatty/ui/locales"
	vncui "github.com/mslxl/natcatty/ui/vnc"
)

// TestViewOnlyBlocksEverySendAndRestoresThem proves view-only refuses the
// clipboard send and Ctrl-Alt-Del as well as keys, pointer, and wheel, and that
// leaving view-only restores every one of them. Clipboard pull stays local and
// keeps working.
func TestViewOnlyBlocksEverySendAndRestoresThem(t *testing.T) {
	f := newFixtureWithOptions(t, fixtureOptions{configure: func(cfg *rfbtest.Config) {
		cfg.CutText = "remote"
	}})
	waitUntil(t, "the first frame", func() bool {
		f.tester.Frame()
		return f.tester.Image().RGBAAt(35, 15) == red
	})
	if result := f.surface.PullClipboard(f.tester.SetClipboard); result != vncui.ClipboardPulled {
		t.Fatalf("view-only PullClipboard = %v, want pulled", result)
	}

	f.surface.SetViewOnly(true)
	f.tester.Key(0, ui.KeyA)
	f.tester.Press(35, 15)
	f.tester.Release(35, 15)
	f.tester.Wheel(35, 15, 0, 10)
	if result := f.surface.SendClipboard("local"); result != vncui.ClipboardViewOnly {
		t.Fatalf("view-only SendClipboard = %v, want view-only", result)
	}
	if err := f.surface.SendCtrlAltDel(); !errors.Is(err, vncui.ErrViewOnly) {
		t.Fatalf("view-only SendCtrlAltDel = %v, want ErrViewOnly", err)
	}
	f.tester.Frame()
	received := f.server.Received()
	if len(received.Keys) != 0 || len(received.ExtendedKeys) != 0 ||
		len(received.Pointers) != 0 || len(received.CutTexts) != 0 {
		t.Fatalf("view-only wrote keys %+v, extended %+v, pointers %+v, cut texts %q",
			received.Keys, received.ExtendedKeys, received.Pointers, received.CutTexts)
	}

	f.surface.SetViewOnly(false)
	f.tester.Key(0, ui.KeyA)
	f.tester.Press(35, 15)
	f.tester.Release(35, 15)
	f.tester.Wheel(35, 15, 0, 10)
	if result := f.surface.SendClipboard("local"); result != vncui.ClipboardSent {
		t.Fatalf("restored SendClipboard = %v, want sent", result)
	}
	if err := f.surface.SendCtrlAltDel(); err != nil {
		t.Fatalf("restored SendCtrlAltDel: %v", err)
	}
	waitUntil(t, "the restored key and pointer writes", func() bool {
		received := f.server.Received()
		return len(received.Keys) >= 8 && len(received.Pointers) >= 3 && len(received.CutTexts) == 1
	})
}

// TestToolbarViewOnlyRefusalsAreExplicit clicks the clipboard send and
// Ctrl-Alt-Del controls in view-only. Both refuse the wire write and show a
// localized explanation instead of silently doing nothing; leaving view-only
// restores both actions.
func TestToolbarViewOnlyRefusalsAreExplicit(t *testing.T) {
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
	if !f.surface.ViewOnly() {
		t.Fatal("the view-only button did not enter view-only")
	}
	f.tester.SetClipboard("hello")
	click(m.SessionVNCClipboardSend())
	if texts := f.server.Received().CutTexts; len(texts) != 0 {
		t.Fatalf("view-only toolbar wrote clipboard %q", texts)
	}
	if !f.tester.HasText(m.SessionVNCClipboardViewOnly()) {
		t.Fatalf("no localized view-only clipboard result; texts %q", f.tester.Texts())
	}

	click(m.SessionVNCCtrlAltDel())
	if keys := f.server.Received().Keys; len(keys) != 0 {
		t.Fatalf("view-only toolbar wrote keys %+v", keys)
	}
	if !f.tester.HasText(m.SessionVNCViewOnlyAction()) {
		t.Fatalf("no localized view-only input result; texts %q", f.tester.Texts())
	}

	click(m.SessionVNCViewOnly())
	click(m.SessionVNCClipboardSend())
	waitUntil(t, "the restored toolbar clipboard send", func() bool {
		texts := f.server.Received().CutTexts
		return len(texts) == 1 && texts[0] == "hello"
	})
	click(m.SessionVNCCtrlAltDel())
	waitUntil(t, "the restored Ctrl-Alt-Del", func() bool {
		return len(f.server.Received().Keys) == 6
	})
}

// TestClipPlainClickReachesTheGuest proves a primary press and release inside
// clip mode, before the drag threshold, is replayed as a real click. Hover,
// right click, and a drag past the threshold keep their own behavior: a drag
// pans and writes no button.
func TestClipPlainClickReachesTheGuest(t *testing.T) {
	config := modelvnc.DefaultConfig()
	config.FitMode = modelvnc.Clip
	f := newFixtureWithOptions(t, fixtureOptions{frame: clipFrame(), config: &config})
	waitUntil(t, "the centered clip frame", func() bool {
		f.tester.Frame()
		return f.tester.Image().RGBAAt(0, 0) == red
	})

	// A plain click at the pane origin names remote (80,60) and produces the
	// normal primary down and up.
	f.tester.ClickAt(0, 0)
	waitUntil(t, "the click press and release", func() bool {
		pointers := f.server.Received().Pointers
		return len(pointers) == 2 &&
			pointers[0] == (rfbtest.PointerEvent{Mask: 1, X: 80, Y: 60}) &&
			pointers[1] == (rfbtest.PointerEvent{Mask: 0, X: 80, Y: 60})
	})

	// A right click still reaches the guest: only the primary button starts a
	// clip gesture.
	f.surface.SetScaleMode(modelvnc.Clip)
	f.tester.RightClickAt(0, 0)
	waitUntil(t, "the right click", func() bool {
		pointers := f.server.Received().Pointers
		return len(pointers) == 4 &&
			pointers[2] == (rfbtest.PointerEvent{Mask: 4, X: 80, Y: 60}) &&
			pointers[3] == (rfbtest.PointerEvent{Mask: 0, X: 80, Y: 60})
	})

	// Hover keeps sending an empty-mask position.
	f.surface.SetScaleMode(modelvnc.Clip)
	f.tester.Move(40, 30)
	waitUntil(t, "the hover", func() bool {
		event, ok := lastPointer(f.server)
		return ok && event.Mask == 0 && event.X == 120 && event.Y == 90
	})

	// Two pixels of movement stay inside the threshold and remain a click.
	// MyGo delivers the press with its own coordinates, so press away from the
	// last hover: the replayed press must land where the button went down, and
	// the release must land where it came up.
	f.surface.SetScaleMode(modelvnc.Clip)
	f.tester.Move(0, 0)
	f.tester.Press(10, 10)
	f.tester.Move(12, 12)
	f.tester.Release(12, 12)
	waitUntil(t, "the short-move click", func() bool {
		pointers := f.server.Received().Pointers
		n := len(pointers)
		return n >= 2 &&
			pointers[n-2] == (rfbtest.PointerEvent{Mask: 1, X: 90, Y: 70}) &&
			pointers[n-1] == (rfbtest.PointerEvent{Mask: 0, X: 92, Y: 72})
	})

	// Moving past the threshold pans and writes no button at all. Wait for
	// the hover that moves onto the press point, so its write cannot be
	// mistaken for the gesture's.
	f.surface.SetScaleMode(modelvnc.Clip)
	f.tester.Move(0, 0)
	waitUntil(t, "the hover before the drag", func() bool {
		event, ok := lastPointer(f.server)
		return ok && event == (rfbtest.PointerEvent{Mask: 0, X: 80, Y: 60})
	})
	before := len(f.server.Received().Pointers)
	f.tester.Press(0, 0)
	f.tester.Move(4, 0)
	f.tester.Release(4, 0)
	f.tester.Frame()
	if pointers := f.server.Received().Pointers; len(pointers) != before {
		t.Fatalf("a drag wrote pointer events: %+v", pointers[before:])
	}
}

// TestCursorPlacementInOneToOneAndAfterResize proves the local cursor is drawn
// at the remote pointer in 1:1 and follows a pane resize that recenters the
// framebuffer.
func TestCursorPlacementInOneToOneAndAfterResize(t *testing.T) {
	config := modelvnc.DefaultConfig()
	config.FitMode = modelvnc.OneToOne
	f := newFixtureWithOptions(t, fixtureOptions{
		config: &config,
		configure: func(cfg *rfbtest.Config) {
			cfg.Cursor = cursorShape()
			cfg.CursorAt = image.Point{X: 1, Y: 1}
		},
	})
	waitUntil(t, "the 1:1 frame and cursor", func() bool {
		f.tester.Frame()
		return f.surface.Cursor() != nil && f.tester.Image().RGBAAt(78, 58) == red
	})
	// Remote (0,0) at content (78,58); hotspot (1,1) puts the shape at 77,57.
	f.tester.Move(78, 58)
	waitUntil(t, "the 1:1 cursor", func() bool {
		f.tester.Frame()
		shot := f.tester.Image()
		return shot.RGBAAt(77, 57) == green && shot.RGBAAt(78, 58) == green
	})

	f.tester.SetSize(80, 60)
	waitUntil(t, "the recentered 1:1 frame", func() bool {
		f.tester.Frame()
		return f.tester.Image().RGBAAt(38, 28) == red
	})
	f.tester.Move(38, 28)
	waitUntil(t, "the recentered cursor", func() bool {
		f.tester.Frame()
		shot := f.tester.Image()
		return shot.RGBAAt(37, 27) == green && shot.RGBAAt(38, 28) == green
	})
}

// TestCursorPlacementAndClippingInClip proves the local cursor follows the pan
// offset in clip mode and is clipped by the pane edge instead of painting
// outside it.
func TestCursorPlacementAndClippingInClip(t *testing.T) {
	config := modelvnc.DefaultConfig()
	config.FitMode = modelvnc.Clip
	f := newFixtureWithOptions(t, fixtureOptions{
		frame:  clipFrame(),
		config: &config,
		configure: func(cfg *rfbtest.Config) {
			cfg.Cursor = cursorShape()
			cfg.CursorAt = image.Point{X: 1, Y: 1}
		},
	})
	waitUntil(t, "the clip frame and cursor", func() bool {
		f.tester.Frame()
		return f.surface.Cursor() != nil && f.tester.Image().RGBAAt(0, 0) == red
	})

	// Centered content starts at (-80,-60). Pointer (0,0) names remote
	// (80,60), and the 2x2 shape starts at (-1,-1), so only its bottom-right
	// pixel is visible. Move away and back: the tester's report deduplicates
	// an unchanged position, so the second move must be a fresh event.
	f.tester.Move(1, 0)
	f.tester.Move(0, 0)
	waitUntil(t, "the clipped cursor", func() bool {
		f.tester.Frame()
		return f.tester.Image().RGBAAt(0, 0) == green
	})

	// Pan by (20,10). Pointer (0,0) then names remote (60,50), still putting
	// the shape at (-1,-1). The press starts the drag at the same point the
	// pointer hovers over, so the viewport delta is exactly the move.
	f.tester.Move(80, 60)
	f.tester.Press(80, 60)
	f.tester.Move(100, 70)
	f.tester.Release(100, 70)
	f.tester.Move(0, 0)
	waitUntil(t, "the panned clip cursor", func() bool {
		f.tester.Frame()
		return f.tester.Image().RGBAAt(0, 0) == green
	})

	// A pointer well inside the pane shows the whole shape.
	f.tester.Move(40, 30)
	waitUntil(t, "the fully visible cursor", func() bool {
		f.tester.Frame()
		shot := f.tester.Image()
		return shot.RGBAAt(39, 29) == green && shot.RGBAAt(40, 30) == green
	})

	// Resizing recenters the larger framebuffer; pointer (0,0) maps to remote
	// (120,90) and the shape clips at the new top-left corner again.
	f.tester.SetSize(80, 60)
	f.tester.Move(0, 0)
	waitUntil(t, "the resized clip cursor", func() bool {
		f.tester.Frame()
		return f.tester.Image().RGBAAt(0, 0) == green
	})
}

// TestScaleModeSwitchAfterPanningKeepsCoordinates pans in clip mode, then
// switches 1:1 -> clip -> fit and back, proving every pointer position still
// names the remote pixel shown under it and the pan is reset by a mode change.
func TestScaleModeSwitchAfterPanningKeepsCoordinates(t *testing.T) {
	config := modelvnc.DefaultConfig()
	config.FitMode = modelvnc.Clip
	f := newFixtureWithOptions(t, fixtureOptions{frame: clipFrame(), config: &config})
	waitUntil(t, "the centered clip frame", func() bool {
		f.tester.Frame()
		return f.tester.Image().RGBAAt(0, 0) == red
	})

	// The drag starts at the point the pointer hovers over, so the viewport
	// delta is exactly the move.
	f.tester.Move(80, 60)
	f.tester.Press(80, 60)
	f.tester.Move(100, 70)
	f.tester.Release(100, 70)
	f.tester.Move(0, 0)
	waitUntil(t, "the panned pointer", func() bool {
		event, ok := lastPointer(f.server)
		return ok && event.X == 60 && event.Y == 50
	})

	// Each mode change is probed at a fresh position, since the tester
	// deduplicates a move to the position it already reports.
	f.surface.SetScaleMode(modelvnc.OneToOne)
	f.tester.Move(1, 1)
	waitUntil(t, "the 1:1 pointer after panning", func() bool {
		event, ok := lastPointer(f.server)
		return ok && event.X == 81 && event.Y == 61
	})

	f.surface.SetScaleMode(modelvnc.Clip)
	f.tester.Move(2, 2)
	waitUntil(t, "the reset clip pointer", func() bool {
		event, ok := lastPointer(f.server)
		return ok && event.X == 82 && event.Y == 62
	})

	f.surface.SetScaleMode(modelvnc.Fit)
	f.tester.Move(40, 30)
	waitUntil(t, "the fit pointer after panning", func() bool {
		event, ok := lastPointer(f.server)
		return ok && event.X == 80 && event.Y == 60
	})
}

// TestViewOnlyWheelWritesNothing toggles view-only at runtime rather than
// starting in it, proving a wheel notch is still suppressed after the mode
// changes mid-session.
func TestViewOnlyWheelWritesNothing(t *testing.T) {
	f := newFixture(t)
	waitUntil(t, "the first frame", func() bool {
		f.tester.Frame()
		return f.tester.Image().RGBAAt(35, 15) == red
	})
	f.surface.SetViewOnly(true)
	f.tester.Wheel(35, 15, 0, 10)
	f.tester.Wheel(35, 15, 0, -10)
	f.tester.Frame()
	if pointers := f.server.Received().Pointers; len(pointers) != 0 {
		t.Fatalf("view-only wheel wrote %+v", pointers)
	}
}

// TestViewOnlyKeepsClipPanningLocal proves the clip drag stays usable in
// view-only: the viewport moves locally while no pointer write reaches the
// guest.
func TestViewOnlyKeepsClipPanningLocal(t *testing.T) {
	config := modelvnc.DefaultConfig()
	config.FitMode = modelvnc.Clip
	config.ViewOnly = true
	f := newFixtureWithOptions(t, fixtureOptions{frame: clipFrame(), config: &config})
	waitUntil(t, "the centered clip frame", func() bool {
		f.tester.Frame()
		return f.tester.Image().RGBAAt(0, 0) == red
	})

	f.tester.Press(80, 60)
	f.tester.Move(100, 70)
	f.tester.Release(100, 70)
	waitUntil(t, "the view-only pan", func() bool {
		f.tester.Frame()
		return f.tester.Image().RGBAAt(0, 0) == blue
	})
	if pointers := f.server.Received().Pointers; len(pointers) != 0 {
		t.Fatalf("view-only panning wrote %+v", pointers)
	}
}

// TestViewOnlyKeepsClipClicksLocal proves a click in clip mode under view-only
// is consumed as a gesture but never replayed to the guest.
func TestViewOnlyKeepsClipClicksLocal(t *testing.T) {
	config := modelvnc.DefaultConfig()
	config.FitMode = modelvnc.Clip
	config.ViewOnly = true
	f := newFixtureWithOptions(t, fixtureOptions{frame: clipFrame(), config: &config})
	waitUntil(t, "the centered clip frame", func() bool {
		f.tester.Frame()
		return f.tester.Image().RGBAAt(0, 0) == red
	})
	f.tester.ClickAt(0, 0)
	f.tester.Frame()
	if pointers := f.server.Received().Pointers; len(pointers) != 0 {
		t.Fatalf("view-only clip click wrote %+v", pointers)
	}
}

// Ensure a view-only refusal stays distinct from a transport failure, which
// the clipboard notice colors as a warning rather than a success.
func TestClipboardViewOnlyMessageIsLocalized(t *testing.T) {
	f := newFixture(t)
	f.tester.SetLocale("zh-CN")
	f.surface.SetViewOnly(true)
	result := f.surface.SendClipboard("x")
	if result != vncui.ClipboardViewOnly {
		t.Fatalf("SendClipboard = %v, want view-only", result)
	}
	want := locales.In("zh-CN").SessionVNCClipboardViewOnly()
	if got := f.surface.ClipboardMessage(result); got != want {
		t.Fatalf("clipboard message = %q, want %q", got, want)
	}
}

// TestScaleModeSwitchMidGestureResetsThePendingClick proves a clip press that
// never resolved is forgotten when the scale mode changes: releasing it writes
// no button, and the next click in the new mode reaches the guest normally
// instead of being mistaken for the stale gesture.
func TestScaleModeSwitchMidGestureResetsThePendingClick(t *testing.T) {
	config := modelvnc.DefaultConfig()
	config.FitMode = modelvnc.Clip
	f := newFixtureWithOptions(t, fixtureOptions{frame: clipFrame(), config: &config})
	waitUntil(t, "the centered clip frame", func() bool {
		f.tester.Frame()
		return f.tester.Image().RGBAAt(0, 0) == red
	})

	// Begin a clip gesture, then change the mode before it resolves.
	f.tester.Press(0, 0)
	f.surface.SetScaleMode(modelvnc.Clip)
	f.tester.Release(0, 0)
	f.tester.Frame()
	for _, pointer := range f.server.Received().Pointers {
		if pointer.Mask != 0 {
			t.Fatalf("the cancelled gesture wrote button mask %d at %d,%d", pointer.Mask, pointer.X, pointer.Y)
		}
	}

	// A fresh click must now be replayed as a real press and release.
	f.tester.ClickAt(1, 1)
	waitUntil(t, "the fresh clip click", func() bool {
		pointers := f.server.Received().Pointers
		n := len(pointers)
		return n >= 2 &&
			pointers[n-2] == (rfbtest.PointerEvent{Mask: 1, X: 81, Y: 61}) &&
			pointers[n-1] == (rfbtest.PointerEvent{Mask: 0, X: 81, Y: 61})
	})
	// Nothing before the fresh click's press may carry a button, so the
	// cancelled gesture never reached the guest.
	pointers := f.server.Received().Pointers
	for _, pointer := range pointers[:len(pointers)-2] {
		if pointer.Mask != 0 {
			t.Fatalf("the cancelled gesture wrote button mask %d at %d,%d", pointer.Mask, pointer.X, pointer.Y)
		}
	}
}
