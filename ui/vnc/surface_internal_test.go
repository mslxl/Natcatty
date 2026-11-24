package vnc

import (
	"context"
	"image"
	"net"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	adaptervnc "github.com/mslxl/natcatty/adapters/vnc"
	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model/rfb"
	"github.com/mslxl/natcatty/domain/model/rfb/rfbtest"
	modelvnc "github.com/mslxl/natcatty/domain/model/vnc"
	sessionui "github.com/mslxl/natcatty/ui/session"
)

// TestApplyButtonCombinesMasks proves press, drag and release combine every
// held button: a second press adds its bit without clearing the first, and a
// release clears only its own bit.
func TestApplyButtonCombinesMasks(t *testing.T) {
	tests := []struct {
		name   string
		mask   uint8
		button int
		down   bool
		want   uint8
		ok     bool
	}{
		{"primary down", 0, 0, true, 0x01, true},
		{"secondary down over primary", 0x01, 1, true, 0x05, true},
		{"middle down over both", 0x05, 2, true, 0x07, true},
		{"primary up leaves others", 0x07, 0, false, 0x06, true},
		{"secondary up leaves middle", 0x06, 1, false, 0x02, true},
		{"middle up clears", 0x02, 2, false, 0x00, true},
		{"unknown button is ignored", 0x01, 9, true, 0x01, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := applyButton(tt.mask, tt.button, tt.down)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("applyButton(%#x, %d, %v) = %#x, %v; want %#x, %v",
					tt.mask, tt.button, tt.down, got, ok, tt.want, tt.ok)
			}
		})
	}
}

// TestApplyLayoutResultRecordsEachOutcome proves the viewer's layout state: a
// success and a refusal clear the requested layout, a refusal latches the local
// scaling fallback, and a forwarded (pending) status keeps the request without
// latching.
func TestApplyLayoutResultRecordsEachOutcome(t *testing.T) {
	layout := []appsession.DisplayScreen{{Rect: image.Rect(0, 0, 160, 120)}}
	desktop := image.Pt(160, 120)

	succeeded := &Surface{resizeInFlight: true, layoutRequested: layout, layoutDesktop: desktop}
	succeeded.applyLayoutResult(nil)
	if succeeded.resizeInFlight {
		t.Fatal("success left the layout in flight")
	}
	if succeeded.layoutRequested != nil {
		t.Fatalf("successful layout still cached as %v", succeeded.layoutRequested)
	}
	if succeeded.resizeUnsupported {
		t.Fatal("a success latched the local scaling fallback")
	}

	refused := &Surface{resizeInFlight: true, layoutRequested: layout, layoutDesktop: desktop}
	refused.applyLayoutResult(appsession.ErrDesktopResizeRefused)
	if refused.resizeInFlight {
		t.Fatal("a refusal left the layout in flight")
	}
	if refused.layoutRequested != nil {
		t.Fatalf("refused layout still cached as %v", refused.layoutRequested)
	}
	if !refused.resizeUnsupported {
		t.Fatal("a refusal did not latch the local scaling fallback")
	}

	multi := &Surface{resizeInFlight: true, layoutRequested: layout, layoutDesktop: desktop}
	multi.applyLayoutResult(appsession.ErrMultiScreenUnsupported)
	if !multi.resizeUnsupported {
		t.Fatal("a server that ignored the screen layout did not latch the fallback")
	}

	pending := &Surface{resizeInFlight: true, layoutRequested: layout, layoutDesktop: desktop}
	pending.applyLayoutResult(appsession.ErrDesktopResizePending)
	if pending.resizeInFlight {
		t.Fatal("a pending reply left the layout in flight")
	}
	if len(pending.layoutRequested) != 1 || pending.layoutDesktop != desktop {
		t.Fatal("a pending reply dropped the requested layout")
	}
	if pending.resizeUnsupported {
		t.Fatal("a forwarded (status 4) reply latched the local scaling fallback")
	}

	timedOut := &Surface{resizeInFlight: true, layoutRequested: layout, layoutDesktop: desktop}
	timedOut.applyLayoutResult(appsession.ErrDesktopResizeTimeout)
	if len(timedOut.layoutRequested) != 1 || timedOut.layoutDesktop != desktop {
		t.Fatal("a timeout dropped the layout instead of keeping it as a backoff")
	}
	if timedOut.resizeUnsupported {
		t.Fatal("a timeout latched the fallback instead of allowing a retry")
	}
}

// TestStartLayoutSkipsRepeatsAndUnreadyPanes proves the launch gate: nothing is
// sent before the first frame, while hibernated or view-only, after the fallback
// latched, while a request is outstanding, or for a layout the server already
// reports or already received.
func TestStartLayoutSkipsRepeatsAndUnreadyPanes(t *testing.T) {
	layout := []appsession.DisplayScreen{{Rect: image.Rect(0, 0, 160, 120)}}
	desktop := image.Pt(160, 120)
	cases := []struct {
		name    string
		surface *Surface
	}{
		{"before the first frame", &Surface{}},
		{"view only", &Surface{frameNumber: 1, viewOnly: true}},
		{"hibernated", &Surface{frameNumber: 1, hibernated: true}},
		{"local scaling latched", &Surface{frameNumber: 1, resizeUnsupported: true}},
		{"already in flight", &Surface{frameNumber: 1, resizeInFlight: true}},
		{"already reported", &Surface{frameNumber: 1, desktop: desktop, screens: layout}},
		{"already requested", &Surface{frameNumber: 1, layoutDesktop: desktop, layoutRequested: layout}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.surface.startLayoutLocked(layout, desktop) {
				t.Fatal("startLayoutLocked launched a request")
			}
		})
	}

	surface := &Surface{frameNumber: 1}
	if !surface.startLayoutLocked(layout, desktop) {
		t.Fatal("a ready surface did not launch its first layout")
	}
	if !surface.resizeInFlight {
		t.Fatal("the launched layout is not marked in flight")
	}
	if surface.layoutDesktop != desktop || len(surface.layoutRequested) != 1 {
		t.Fatalf("requested layout = %v at %v", surface.layoutRequested, surface.layoutDesktop)
	}
}

// TestLayoutScreensCoalescesWhileARequestIsInFlight proves a burst of layout
// passes keeps only the newest target as a follow-up, instead of sending every
// intermediate pane arrangement.
func TestLayoutScreensCoalescesWhileARequestIsInFlight(t *testing.T) {
	surface := &Surface{frameNumber: 1, resizeInFlight: true}
	first := []sessionui.ScreenRect{{Index: 0, Rect: image.Rect(0, 0, 100, 80)}}
	second := []sessionui.ScreenRect{{Index: 0, Rect: image.Rect(0, 0, 120, 90)}}
	surface.LayoutScreens(first)
	surface.LayoutScreens(second)
	surface.mu.Lock()
	defer surface.mu.Unlock()
	if len(surface.layoutPending) != 1 || surface.layoutPending[0].Rect != second[0].Rect {
		t.Fatalf("pending layout = %v, want the newest target", surface.layoutPending)
	}
}

// TestDisplayLayoutReportsTheBoundingDesktop proves the viewer hands the port
// the Tab's rectangles unchanged and computes the desktop as their bounding
// box, and that a rectangle the wire cannot carry never reaches the server.
func TestDisplayLayoutReportsTheBoundingDesktop(t *testing.T) {
	screens := []sessionui.ScreenRect{
		{Index: 0, Rect: image.Rect(0, 0, 100, 50)},
		{Index: 1, Rect: image.Rect(100, 0, 180, 50)},
	}
	layout, desktop, ok := displayLayout(screens)
	if !ok {
		t.Fatal("displayLayout rejected a valid layout")
	}
	if desktop != image.Pt(180, 50) {
		t.Fatalf("desktop = %v, want the bounding box 180x50", desktop)
	}
	if len(layout) != 2 || layout[1].Rect != screens[1].Rect {
		t.Fatalf("layout = %v, want both rectangles unchanged", layout)
	}

	for _, bad := range []sessionui.ScreenRect{
		{Index: 0, Rect: image.Rectangle{}},
		{Index: 0, Rect: image.Rect(-1, 0, 10, 10)},
		{Index: 0, Rect: image.Rect(0, 0, 0x10000, 10)},
	} {
		if _, _, ok := displayLayout([]sessionui.ScreenRect{bad}); ok {
			t.Fatalf("displayLayout accepted %v", bad.Rect)
		}
	}
	if _, _, ok := displayLayout(nil); ok {
		t.Fatal("displayLayout accepted an empty layout")
	}
}

// TestScreenGeometryReportsTheDrawnPane proves the Tab sees the Pane rectangle
// from the last draw and the screen index the Session was opened with.
func TestScreenGeometryReportsTheDrawnPane(t *testing.T) {
	surface := &Surface{screenIndex: 2}
	if _, _, ok := surface.ScreenGeometry(); ok {
		t.Fatal("a surface that never drew reported geometry")
	}
	surface.paneRect = ui.Rect{X: 8, Y: 16, W: 100, H: 80}
	index, rect, ok := surface.ScreenGeometry()
	if !ok {
		t.Fatal("a drawn surface reported no geometry")
	}
	if index != 2 {
		t.Fatalf("screen index = %d, want 2", index)
	}
	if rect != image.Rect(8, 16, 108, 96) {
		t.Fatalf("rect = %v, want the drawn window rectangle", rect)
	}
}

// inputFixture is the smallest real Session + viewer + headless window, for
// tests that must hand the surface an InputEvent the Tester cannot synthesize
// (a key-down carrying a modifier bit, as a platform sends while Shift is
// held).
type inputFixture struct {
	surface *Surface
	tester  *ui.Tester
	server  *rfbtest.Server
}

func newInputFixture(t *testing.T) *inputFixture {
	t.Helper()
	serverConn, clientConn := net.Pipe()
	server := rfbtest.New(rfbtest.Config{
		Frame:       image.NewRGBA(image.Rect(0, 0, 4, 4)),
		DesktopName: "lab-desktop",
		Encoding:    rfb.EncodingRaw,
	})
	go func() { _ = server.Serve(serverConn) }()

	factory := adaptervnc.Factory{Dial: func(context.Context, string, string) (net.Conn, error) {
		return clientConn, nil
	}}
	session, err := appsession.NewService(factory).Open(context.Background(), appsession.StartSpec{
		Kind:  appsession.KindVNC,
		Label: "workstation",
		VNC: &modelvnc.SessionConfig{
			Hostname:      "desk.test",
			Port:          5900,
			Configuration: modelvnc.DefaultConfig(),
		},
	})
	if err != nil {
		t.Fatalf("Open VNC: %v", err)
	}
	surface, err := New(session)
	if err != nil {
		t.Fatalf("New viewer: %v", err)
	}
	tester := ui.NewTester(func(c *ui.Context) { surface.ViewFocused(c, true) }, 160, 120)
	t.Cleanup(func() {
		surface.Close()
		_ = session.Close()
		_ = clientConn.Close()
		_ = serverConn.Close()
	})
	return &inputFixture{surface: surface, tester: tester, server: server}
}

// waitReceivedKeys waits for exactly want key events at the guest.
func waitReceivedKeys(t *testing.T, server *rfbtest.Server, want []rfbtest.KeyEvent) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got := server.Received().Keys
		if len(got) == len(want) {
			match := true
			for i := range want {
				if got[i] != want[i] {
					match = false
					break
				}
			}
			if match {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("guest key record = %+v, want %+v", server.Received().Keys, want)
}

// TestWindowBlurReleasesHeldSynthModifier proves a held synthesized modifier is
// released when the window loses focus. The Tester's KeyDown carries no
// modifier bits, while a real platform reports Shift as a bit on the key event,
// so this delivers that same InputEvent to the surface's handler directly and
// then drives the blur through the real Tester focus API.
func TestWindowBlurReleasesHeldSynthModifier(t *testing.T) {
	f := newInputFixture(t)
	f.tester.Frame()

	if !f.surface.handleInput(ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.KeyA, Mods: ui.Shift}) {
		t.Fatal("the Shift+A key-down was not handled")
	}
	waitReceivedKeys(t, f.server, []rfbtest.KeyEvent{
		{Down: true, Keysym: rfb.KeysymShiftL},
		{Down: true, Keysym: 0x41}, // A, the shifted keysym
	})

	f.tester.SetFocused(false)
	f.tester.Frame()
	waitReceivedKeys(t, f.server, []rfbtest.KeyEvent{
		{Down: true, Keysym: rfb.KeysymShiftL},
		{Down: true, Keysym: 0x41},
		{Down: false, Keysym: 0x41},
		{Down: false, Keysym: rfb.KeysymShiftL},
	})
	if f.surface.heldMods != 0 {
		t.Fatalf("heldMods = %#x after blur, want 0", f.surface.heldMods)
	}
	if len(f.surface.heldKeys) != 0 {
		t.Fatalf("heldKeys = %+v after blur, want none", f.surface.heldKeys)
	}
}

// TestInputComposeTextAndCommandAreIgnoredDirectly reaches the text/composition
// branch the Tester cannot: Compose early-returns while the input method is
// off, so this constructs the InputEvents and calls the handler. None may
// reach the guest, and a later real key must still work.
func TestInputComposeTextAndCommandAreIgnoredDirectly(t *testing.T) {
	f := newInputFixture(t)
	f.tester.Frame()

	for _, event := range []ui.InputEvent{
		{Kind: ui.InputText, Text: "héllo"},
		{Kind: ui.InputCompose, Text: "にほん", Caret: 1},
		{Kind: ui.InputCommand, Text: "copy"},
	} {
		if f.surface.handleInput(event) {
			t.Fatalf("handleInput claimed kind %v", event.Kind)
		}
	}
	received := f.server.Received()
	if len(received.Keys) != 0 || len(received.ExtendedKeys) != 0 || len(received.Pointers) != 0 {
		t.Fatalf("text/composition reached the guest: %+v", received)
	}

	if !f.surface.handleInput(ui.InputEvent{Kind: ui.InputKeyDown, Key: ui.KeyB}) {
		t.Fatal("the key-down was not handled")
	}
	if !f.surface.handleInput(ui.InputEvent{Kind: ui.InputKeyUp, Key: ui.KeyB}) {
		t.Fatal("the key-up was not handled")
	}
	waitReceivedKeys(t, f.server, []rfbtest.KeyEvent{
		{Down: true, Keysym: 0x62},
		{Down: false, Keysym: 0x62},
	})
}

// TestLayoutScreensDeclinesWhenAPaneCannotDrive proves the Tab is told when
// this Pane cannot take the request, so it can drive through another Pane: a
// view-only, hibernated, or not-yet-drawn Pane declines instead of swallowing
// the layout.
func TestLayoutScreensDeclinesWhenAPaneCannotDrive(t *testing.T) {
	screens := []sessionui.ScreenRect{{Index: 0, Rect: image.Rect(0, 0, 100, 80)}}
	cases := []struct {
		name    string
		surface *Surface
	}{
		{"before the first frame", &Surface{}},
		{"view only", &Surface{frameNumber: 1, viewOnly: true}},
		{"hibernated", &Surface{frameNumber: 1, hibernated: true}},
		{"local scaling latched", &Surface{frameNumber: 1, resizeUnsupported: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.surface.LayoutScreens(screens) {
				t.Fatal("the Pane took a layout it cannot drive")
			}
		})
	}
}
