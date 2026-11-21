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

// TestApplyResizeResultRecordsEachOutcome proves the viewer's resize state:
// success clears the requested size, a refusal latches the fallback, and a
// forwarded (pending) status neither latches nor clears the pending request.
func TestApplyResizeResultRecordsEachOutcome(t *testing.T) {
	succeeded := &Surface{resizeInFlight: true, resizeRequested: image.Pt(160, 120)}
	succeeded.applyResizeResult(nil)
	if succeeded.resizeInFlight {
		t.Fatal("success left the resize in flight")
	}
	if succeeded.resizeRequested != (image.Point{}) {
		t.Fatalf("successful size still cached as %v", succeeded.resizeRequested)
	}
	if succeeded.resizeUnsupported {
		t.Fatal("a success latched the local scaling fallback")
	}

	refused := &Surface{resizeInFlight: true, resizeRequested: image.Pt(160, 120)}
	refused.applyResizeResult(appsession.ErrDesktopResizeRefused)
	if refused.resizeInFlight {
		t.Fatal("a refusal left the resize in flight")
	}
	if refused.resizeRequested != (image.Point{}) {
		t.Fatalf("refused size still cached as %v", refused.resizeRequested)
	}
	if !refused.resizeUnsupported {
		t.Fatal("a refusal did not latch the local scaling fallback")
	}

	pending := &Surface{resizeInFlight: true, resizeRequested: image.Pt(160, 120)}
	pending.applyResizeResult(appsession.ErrDesktopResizePending)
	if pending.resizeInFlight {
		t.Fatal("a pending reply left the resize in flight")
	}
	if pending.resizeRequested != image.Pt(160, 120) {
		t.Fatalf("pending size = %v, want the request kept", pending.resizeRequested)
	}
	if pending.resizeUnsupported {
		t.Fatal("a forwarded (status 4) reply latched the local scaling fallback")
	}
}

// TestTimeoutKeepsTheFailedSizeButAllowsADifferentOne proves the per-size
// backoff: after a timeout, redraws at the same pane size do not start another
// slow wait, while a different pane size may retry.
func TestTimeoutKeepsTheFailedSizeButAllowsADifferentOne(t *testing.T) {
	surface := &Surface{
		frameNumber:     1,
		frame:           image.Rect(0, 0, 200, 150),
		resizeInFlight:  true,
		resizeRequested: image.Pt(160, 120),
	}
	surface.applyResizeResult(appsession.ErrDesktopResizeTimeout)
	if surface.resizeInFlight {
		t.Fatal("a timeout left the resize in flight")
	}
	if surface.resizeRequested != image.Pt(160, 120) {
		t.Fatalf("timed-out size = %v, want the failed size kept as backoff", surface.resizeRequested)
	}
	if surface.resizeUnsupported {
		t.Fatal("a timeout latched the fallback instead of allowing a retry")
	}

	surface.mu.Lock()
	same := surface.shouldRequestDesktopSize(160, 120)
	different := surface.shouldRequestDesktopSize(80, 60)
	surface.mu.Unlock()
	if same {
		t.Fatal("the timed-out size was retried on a redraw")
	}
	if !different {
		t.Fatal("a different pane size was not allowed to retry")
	}
}

// TestStaleNonzeroReplyDoesNotLatchTheFallback proves the viewer's half of the
// stale-reply rule: a late nonzero answer for an older request is surfaced by
// the adapter as a timeout, and a timeout must never latch local scaling.
func TestStaleNonzeroReplyDoesNotLatchTheFallback(t *testing.T) {
	surface := &Surface{
		frameNumber:     1,
		frame:           image.Rect(0, 0, 64, 48),
		resizeInFlight:  true,
		resizeRequested: image.Pt(64, 48),
	}
	surface.applyResizeResult(appsession.ErrDesktopResizeTimeout)
	if surface.resizeUnsupported {
		t.Fatal("a stale nonzero reply latched the local scaling fallback")
	}
	if surface.resizeRequested != image.Pt(64, 48) {
		t.Fatalf("timed-out size = %v, want the per-size backoff kept", surface.resizeRequested)
	}
}

// TestStageFourWithoutFollowUpSuppressesTheSameSizeButRetries proves a
// forwarded (status 4) request with no follow-up keeps the requested size: a
// redraw at that size is suppressed, a different pane size retries, and the
// fallback never latches.
func TestStageFourWithoutFollowUpSuppressesTheSameSizeButRetries(t *testing.T) {
	surface := &Surface{
		frameNumber:     1,
		frame:           image.Rect(0, 0, 200, 150),
		resizeInFlight:  true,
		resizeRequested: image.Pt(160, 120),
	}
	surface.applyResizeResult(appsession.ErrDesktopResizePending)
	if surface.resizeInFlight {
		t.Fatal("a pending reply left the resize in flight")
	}
	if surface.resizeUnsupported {
		t.Fatal("a forwarded (status 4) reply latched the local scaling fallback")
	}
	surface.mu.Lock()
	same := surface.shouldRequestDesktopSize(160, 120)
	different := surface.shouldRequestDesktopSize(80, 60)
	surface.mu.Unlock()
	if same {
		t.Fatal("the pending size was re-requested on a redraw")
	}
	if !different {
		t.Fatal("a different pane size was not allowed to retry")
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
