package vnc_test

import (
	"context"
	"errors"
	"image"
	"image/color"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	adaptervnc "github.com/mslxl/natcatty/adapters/vnc"
	appconnection "github.com/mslxl/natcatty/application/connection"
	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model/rfb"
	"github.com/mslxl/natcatty/domain/model/rfb/rfbtest"
	modelvnc "github.com/mslxl/natcatty/domain/model/vnc"
	"github.com/mslxl/natcatty/ui/locales"
	sessionui "github.com/mslxl/natcatty/ui/session"
	vncui "github.com/mslxl/natcatty/ui/vnc"
)

var green = color.RGBA{R: 20, G: 220, B: 20, A: 255}

func solidFrame(c color.RGBA) *image.RGBA {
	frame := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for i := 0; i < len(frame.Pix); i += 4 {
		frame.Pix[i] = c.R
		frame.Pix[i+1] = c.G
		frame.Pix[i+2] = c.B
		frame.Pix[i+3] = c.A
	}
	return frame
}

// dialScript counts dials and supplies each later connection from next. It is
// the seam the app normally fills with a routed or direct dial.
type dialScript struct {
	mu    sync.Mutex
	first net.Conn
	next  func() (net.Conn, error)
	calls int
}

func (d *dialScript) dial(context.Context, string, string) (net.Conn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls++
	if d.calls == 1 {
		return d.first, nil
	}
	return d.next()
}

func (d *dialScript) callCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

// newReconnectFixture opens a real VNC Session whose second dial is supplied
// by next. The first frame is red; the test controls what the reconnection
// sees.
func newReconnectFixture(t *testing.T, delay time.Duration,
	next func() (net.Conn, error)) (*fixture, *dialScript, net.Conn) {
	t.Helper()
	serverConn, clientConn := net.Pipe()
	server := rfbtest.New(rfbtest.Config{
		Frame:       solidFrame(red),
		DesktopName: "lab-desktop",
		Encoding:    rfb.EncodingRaw,
	})
	go func() { _ = server.Serve(serverConn) }()

	script := &dialScript{first: clientConn, next: next}
	factory := adaptervnc.Factory{Dial: script.dial}
	cfg := modelvnc.SessionConfig{
		Hostname:      "desk.test",
		Port:          5900,
		Configuration: modelvnc.DefaultConfig(),
	}
	session, err := appsession.NewService(factory).Open(context.Background(), appsession.StartSpec{
		Kind:      appsession.KindVNC,
		Label:     "workstation",
		VNC:       &cfg,
		Reconnect: &appsession.ReconnectPolicy{Delay: delay},
	})
	if err != nil {
		t.Fatalf("Open VNC: %v", err)
	}
	surface, err := vncui.New(session)
	if err != nil {
		t.Fatalf("New viewer: %v", err)
	}
	tester := ui.NewTester(func(c *ui.Context) {
		surface.ViewFocused(c, true)
	}, 400, 300)
	t.Cleanup(func() {
		surface.Close()
		_ = session.Close()
		_ = clientConn.Close()
		_ = serverConn.Close()
	})
	return &fixture{
		session: session,
		surface: surface,
		tester:  tester,
		server:  server,
	}, script, serverConn
}

// TestNetworkDropReconnectsAndResumesTheDesktop proves the pane follows the
// Session's automatic reconnect: a closed transport shows the reconnecting
// state and the replacement transport's frame draws without reopening the Tab.
func TestNetworkDropReconnectsAndResumesTheDesktop(t *testing.T) {
	f, script, firstServerConn := newReconnectFixture(t, 200*time.Millisecond, func() (net.Conn, error) {
		serverConn, clientConn := net.Pipe()
		server := rfbtest.New(rfbtest.Config{
			Frame:       solidFrame(green),
			DesktopName: "lab-desktop",
			Encoding:    rfb.EncodingRaw,
		})
		go func() { _ = server.Serve(serverConn) }()
		return clientConn, nil
	})
	m := locales.In("en")
	waitUntil(t, "the first desktop", func() bool {
		f.tester.Frame()
		return f.tester.Image().RGBAAt(80, 60) == red
	})

	// Drop the server side. The Session enters its retry delay and the viewer
	// reports the state over the retained frame.
	if err := firstServerConn.Close(); err != nil {
		t.Fatalf("close the server: %v", err)
	}
	waitUntil(t, "the reconnecting state", func() bool {
		return f.session.Snapshot().Connection == appsession.ConnectionStatusReconnecting
	})
	f.tester.Frame()
	if !f.tester.HasText(m.ConnectionStatusReconnecting()) {
		t.Fatalf("the pane does not show reconnecting: %v", f.tester.Texts())
	}
	if f.session.Snapshot().Status != appsession.StatusRunning {
		t.Fatalf("status while reconnecting = %q", f.session.Snapshot().Status)
	}

	waitUntil(t, "the reconnected desktop", func() bool {
		f.tester.Frame()
		return f.session.Snapshot().Connection == appsession.ConnectionStatusActive &&
			f.tester.Image().RGBAAt(80, 60) == green
	})
	if calls := script.callCount(); calls != 2 {
		t.Fatalf("dials = %d, want one replacement", calls)
	}
}

// TestFailedAuthenticationOffersManualReconnectAndStopsRetrying covers a
// permanent rejection on a reconnect: no further attempts, a clear failed
// state, and one manual Reconnect control that the shell can back with a fresh
// Session.
func TestFailedAuthenticationOffersManualReconnectAndStopsRetrying(t *testing.T) {
	authFailure := &appconnection.Error{Kind: appconnection.KindAuth, Cause: errors.New("wrong VNC password")}
	var manual int
	f, script, firstServerConn := newReconnectFixture(t, 20*time.Millisecond, func() (net.Conn, error) {
		return nil, authFailure
	})
	m := locales.In("en")
	waitUntil(t, "the first desktop", func() bool {
		f.tester.Frame()
		return f.tester.Image().RGBAAt(80, 60) == red
	})
	f.surface.SetPaneActions(sessionui.PaneActions{Reconnect: func() { manual++ }})
	if err := firstServerConn.Close(); err != nil {
		t.Fatalf("close the server: %v", err)
	}

	select {
	case <-f.session.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the auth failure did not end the Session")
	}
	snapshot := f.session.Snapshot()
	if snapshot.Status != appsession.StatusExited ||
		snapshot.Connection != appsession.ConnectionStatusError ||
		snapshot.EndReason != appsession.EndReasonAuthFailed {
		t.Fatalf("final snapshot = %+v, want an authentication failure", snapshot)
	}
	// The Session dialed once to connect and once to reconnect; the permanent
	// rejection must stop there.
	deadline := time.Now().Add(150 * time.Millisecond)
	for time.Now().Before(deadline) {
		if calls := script.callCount(); calls != 2 {
			t.Fatalf("dials = %d, want no retry after the rejected reconnect", calls)
		}
		time.Sleep(5 * time.Millisecond)
	}

	waitUntil(t, "the failed pane", func() bool {
		f.tester.Frame()
		return f.tester.HasText(m.ConnectionErrorAuth()) && f.tester.HasText(m.SessionVNCReconnectHint())
	})
	if err := f.tester.Click(m.SessionMenuReconnect()); err != nil {
		t.Fatalf("click Reconnect: %v", err)
	}
	if manual != 1 {
		t.Fatalf("manual Reconnect calls = %d, want 1", manual)
	}
	// The manual action is user-driven only; it must not start a retry loop.
	if calls := script.callCount(); calls != 2 {
		t.Fatalf("dials after manual Reconnect = %d, want still 2", calls)
	}
}

// TestHibernateKeepsTheConnectionAndLastFrame proves hibernate stops drawing
// but leaves the transport alive and the last frame retained; Resume draws it
// again.
func TestHibernateKeepsTheConnectionAndLastFrame(t *testing.T) {
	f := newFixture(t)
	m := locales.In("en")
	waitUntil(t, "the first desktop", func() bool {
		f.tester.Frame()
		return f.tester.Image().RGBAAt(35, 15) == red
	})

	if !f.surface.Hibernate() {
		t.Fatal("Hibernate returned false")
	}
	if !f.surface.Hibernated() || !f.session.Snapshot().Hibernated {
		t.Fatal("the viewer did not report hibernation")
	}
	f.tester.Frame()
	if !f.tester.HasText(m.SessionHibernatedTitle()) || !f.tester.HasText(m.SessionVNCHibernatedBody()) {
		t.Fatalf("the hibernated placeholder is missing: %v", f.tester.Texts())
	}
	// The transport is still connected: the server has not seen the stream end
	// and the Session is still running.
	select {
	case err := <-f.serve:
		t.Fatalf("the server connection ended while hibernated: %v", err)
	default:
	}
	if snapshot := f.session.Snapshot(); snapshot.Status != appsession.StatusRunning {
		t.Fatalf("status while hibernated = %q, want running", snapshot.Status)
	}

	if err := f.surface.Resume(); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if f.surface.Hibernated() || f.session.Snapshot().Hibernated {
		t.Fatal("Resume left the viewer hibernated")
	}
	waitUntil(t, "the resumed frame", func() bool {
		f.tester.Frame()
		return f.tester.Image().RGBAAt(35, 15) == red
	})
}

// TestReleaseInputLetsGoOfHeldKeyAndPointer is the hidden-Tab seam: when the
// shell stops building this Surface it calls ReleaseInput, which must send the
// guest the key-up and empty pointer mask it never saw.
func TestReleaseInputLetsGoOfHeldKeyAndPointer(t *testing.T) {
	f := newFixture(t)
	waitUntil(t, "the frame", func() bool {
		f.tester.Frame()
		return f.tester.Image().RGBAAt(35, 15) == red
	})

	f.tester.KeyDown(ui.KeyK)
	waitKeys(t, f.server, []rfbtest.KeyEvent{{Down: true, Keysym: 0x6b}})
	f.surface.ReleaseInput()
	waitKeys(t, f.server, []rfbtest.KeyEvent{
		{Down: true, Keysym: 0x6b},
		{Down: false, Keysym: 0x6b},
	})

	f.tester.Press(35, 15)
	waitUntil(t, "the pointer press", func() bool {
		pointers := f.server.Received().Pointers
		return len(pointers) > 0 && pointers[len(pointers)-1].Mask&1 != 0
	})
	f.surface.ReleaseInput()
	waitUntil(t, "the pointer release", func() bool {
		pointers := f.server.Received().Pointers
		return len(pointers) > 0 && pointers[len(pointers)-1].Mask == 0
	})
}
