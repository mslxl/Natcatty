package sessionui

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	appsession "github.com/mslxl/natcatty/application/session"
)

// remoteScreenTransport is a Session transport whose peer owns the screen
// size, like the in-process mosh adapter. It records the Session's inverse
// resize handler so a test can drive a server-authoritative resize.
type remoteScreenTransport struct {
	*scriptedTransport
	mu      sync.Mutex
	handler func(cols, rows int, position uint64)
	ready   chan struct{}
	once    sync.Once
}

func newRemoteScreenTransport() *remoteScreenTransport {
	return &remoteScreenTransport{scriptedTransport: newScriptedTransport(), ready: make(chan struct{})}
}

func (t *remoteScreenTransport) SetScreenSizeHandler(handler func(cols, rows int, position uint64)) {
	t.mu.Lock()
	t.handler = handler
	t.mu.Unlock()
	t.once.Do(func() { close(t.ready) })
}

func (t *remoteScreenTransport) remoteResize(tst *testing.T, cols, rows int) {
	tst.Helper()
	select {
	case <-t.ready:
	case <-time.After(2 * time.Second):
		tst.Error("Session never registered the inverse resize handler")
		return
	}
	t.mu.Lock()
	handler := t.handler
	t.mu.Unlock()
	handler(cols, rows, 0)
}

type remoteScreenFactory struct{ transport *remoteScreenTransport }

func (f remoteScreenFactory) Open(context.Context, appsession.StartSpec) (appsession.Transport, error) {
	return f.transport, nil
}

// TestMoshScreenSizeReachesMyGo proves the desktop integration: a Session
// whose peer owns the size turns on MyGo's ResizeByConn, and the inverse
// resize signal changes MyGo's screen before the following output.
func TestMoshScreenSizeReachesMyGo(t *testing.T) {
	transport := newRemoteScreenTransport()
	service := appsession.NewService(remoteScreenFactory{transport: transport})
	t.Cleanup(service.CloseAll)
	session, err := service.Open(context.Background(), appsession.StartSpec{
		Kind: appsession.KindMosh, Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatalf("open Session: %v", err)
	}
	if !session.ScreenSizeFromRemote() {
		t.Fatal("Session did not detect a server-authoritative screen size")
	}
	surface, err := New(session, WithRedraw(func() {}))
	if err != nil {
		t.Fatalf("New surface: %v", err)
	}
	t.Cleanup(surface.Close)

	tester := surfaceTester(surface)
	tester.Frame()

	resized := make(chan struct{})
	go func() {
		transport.remoteResize(t, 101, 37)
		close(resized)
	}()
	eventually(t, func() bool {
		tester.Frame()
		cols, rows := surface.terminal.Size()
		return cols == 101 && rows == 37
	})
	select {
	case <-resized:
	case <-time.After(2 * time.Second):
		t.Fatal("the resize handler did not return after MyGo resized")
	}

	transport.emit("\x1b[Hframe-at-101x37")
	eventually(t, func() bool {
		tester.Frame()
		cols, rows := surface.terminal.Size()
		return cols == 101 && rows == 37 &&
			strings.Contains(surface.terminal.Text(), "frame-at-101x37")
	})
}
