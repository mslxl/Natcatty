package session

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// screenSizerFake is a Transport whose peer owns the screen size. remoteResize
// runs the Session's inverse-resize handler exactly as the mosh adapter does
// before it makes bytes encoded at the new size readable.
type screenSizerFake struct {
	*fakeTransport
	mu      sync.Mutex
	handler func(cols, rows int, position uint64)
	sent    uint64
	ready   chan struct{}
	once    sync.Once
}

func newScreenSizerFake() *screenSizerFake {
	return &screenSizerFake{fakeTransport: newFakeTransport(), ready: make(chan struct{})}
}

func (s *screenSizerFake) SetScreenSizeHandler(handler func(cols, rows int, position uint64)) {
	s.mu.Lock()
	s.handler = handler
	s.mu.Unlock()
	s.once.Do(func() { close(s.ready) })
}

// send overrides fakeTransport.send so remoteResize knows how many output
// bytes precede the resize, exactly as the mosh adapter reports its stream
// position.
func (s *screenSizerFake) send(p string) {
	s.mu.Lock()
	s.sent += uint64(len(p))
	s.mu.Unlock()
	s.fakeTransport.send(p)
}

func (s *screenSizerFake) remoteResize(t *testing.T, cols, rows int) {
	t.Helper()
	select {
	case <-s.ready:
	case <-time.After(2 * time.Second):
		t.Fatal("Session never registered its screen-size handler")
	}
	s.mu.Lock()
	handler := s.handler
	position := s.sent
	s.mu.Unlock()
	handler(cols, rows, position)
}

type fixedFactory struct {
	transport Transport
}

func (f fixedFactory) Open(context.Context, StartSpec) (Transport, error) {
	return f.transport, nil
}

func openScreenSizerSession(t *testing.T, transport Transport) *Session {
	t.Helper()
	service := newTestService(fixedFactory{transport: transport})
	session, err := service.Open(context.Background(), StartSpec{
		Kind: KindMosh, Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// watchView reads until EOF and reports each chunk. It has no fixed delay:
// tests wait on the channel or a hard timeout.
func watchView(view io.Reader, chunks chan<- string) {
	go func() {
		buffer := make([]byte, 256)
		for {
			n, err := view.Read(buffer)
			if n > 0 {
				chunks <- string(buffer[:n])
			}
			if err != nil {
				close(chunks)
				return
			}
		}
	}()
}

func waitChunk(t *testing.T, chunks <-chan string) string {
	t.Helper()
	select {
	case chunk, ok := <-chunks:
		if !ok {
			t.Fatal("view ended before the expected output")
		}
		return chunk
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for view output")
		return ""
	}
}

// TestScreenSizeHandlerBeforeOutput is the transport-seam contract: while the
// inverse resize is blocked, no output may become readable; after it applies
// the sink, the bytes encoded for that size follow.
func TestScreenSizeHandlerBeforeOutput(t *testing.T) {
	transport := newScreenSizerFake()
	session := openScreenSizerSession(t, transport)
	if !session.ScreenSizeFromRemote() {
		t.Fatal("ScreenSizeFromRemote = false for a ScreenSizer transport")
	}
	view, err := session.AttachView()
	if err != nil {
		t.Fatalf("AttachView: %v", err)
	}
	t.Cleanup(func() { _ = view.Close() })

	resized := make(chan [2]int, 1)
	view.SetScreenSizeSink(func(cols, rows int) {
		resized <- [2]int{cols, rows}
	})
	chunks := make(chan string, 4)
	watchView(view, chunks)

	handlerReturned := make(chan struct{})
	go func() {
		transport.remoteResize(t, 91, 33)
		close(handlerReturned)
	}()
	select {
	case size := <-resized:
		if size != [2]int{91, 33} {
			t.Fatalf("sink size = %v, want [91 33]", size)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the resize sink was never called")
	}
	// The transport handler returns only once the Session reader has applied
	// the size, so output sent now is ordered after it.
	select {
	case <-handlerReturned:
	case <-time.After(2 * time.Second):
		t.Fatal("the transport resize handler did not return after the sink ran")
	}
	transport.send("frame-91x33")
	if got := waitChunk(t, chunks); got != "frame-91x33" {
		t.Fatalf("view output = %q", got)
	}
}

// TestScreenSizeKeepsEarlierOutputAtOldSize proves the position-aware
// ordering behind MyGo's ResizeByConn: bytes the server produced before the
// resize reach the view before Terminal.Resize, and bytes produced after it
// wait until the new size is applied.
func TestScreenSizeKeepsEarlierOutputAtOldSize(t *testing.T) {
	transport := newScreenSizerFake()
	session := openScreenSizerSession(t, transport)
	view, err := session.AttachView()
	if err != nil {
		t.Fatalf("AttachView: %v", err)
	}
	t.Cleanup(func() { _ = view.Close() })

	resized := make(chan [2]int, 1)
	view.SetScreenSizeSink(func(cols, rows int) {
		resized <- [2]int{cols, rows}
	})

	transport.send("old-size-frame")
	handlerReturned := make(chan struct{})
	go func() {
		transport.remoteResize(t, 120, 40)
		close(handlerReturned)
	}()
	eventually(t, "the resize to become pending behind the old frame", func() bool {
		session.conn.mu.Lock()
		defer session.conn.mu.Unlock()
		return session.conn.pendingScreen != nil
	})

	buffer := make([]byte, 64)
	n, err := view.Read(buffer)
	if err != nil || string(buffer[:n]) != "old-size-frame" {
		t.Fatalf("first read = %q, %v; want the old-size frame", buffer[:n], err)
	}
	select {
	case size := <-resized:
		t.Fatalf("resize %v was applied before the old-size frame was delivered", size)
	default:
	}

	// These bytes were produced after the resize. They stay behind the
	// pending sink even though the transport makes them readable.
	transport.send("frame-120x40")
	n, err = view.Read(buffer)
	if err != nil || string(buffer[:n]) != "frame-120x40" {
		t.Fatalf("second read = %q, %v; want the new-size frame", buffer[:n], err)
	}
	select {
	case size := <-resized:
		if size != [2]int{120, 40} {
			t.Fatalf("resize sink = %v, want [120 40]", size)
		}
	default:
		t.Fatal("new-size bytes were returned before the resize was applied")
	}
	select {
	case <-handlerReturned:
	case <-time.After(2 * time.Second):
		t.Fatal("the transport resize handler did not return after applying the size")
	}
}

// TestScreenSizePendingBeforeView covers a remote resize that arrives before
// the surface attaches its terminal: the size stays pending and is applied by
// the first live read, before the queued frame.
func TestScreenSizePendingBeforeView(t *testing.T) {
	transport := newScreenSizerFake()
	session := openScreenSizerSession(t, transport)

	done := make(chan struct{})
	go func() {
		transport.remoteResize(t, 47, 19)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("remote resize blocked with no live view")
	}
	transport.send("frame-47x19")

	view, err := session.AttachView()
	if err != nil {
		t.Fatalf("AttachView: %v", err)
	}
	t.Cleanup(func() { _ = view.Close() })
	resized := make(chan [2]int, 1)
	view.SetScreenSizeSink(func(cols, rows int) { resized <- [2]int{cols, rows} })
	chunks := make(chan string, 4)
	watchView(view, chunks)

	select {
	case size := <-resized:
		if size != [2]int{47, 19} {
			t.Fatalf("pending sink size = %v, want [47 19]", size)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pending size was not applied by the first read")
	}
	if got := waitChunk(t, chunks); got != "frame-47x19" {
		t.Fatalf("view output = %q", got)
	}
}

// TestScreenSizePendingThroughHibernation proves a size that arrived while the
// view was detached is applied before the resumed snapshot replay, which may
// already contain bytes encoded for the new size.
func TestScreenSizePendingThroughHibernation(t *testing.T) {
	transport := newScreenSizerFake()
	session := openScreenSizerSession(t, transport)
	first, err := session.AttachView()
	if err != nil {
		t.Fatalf("AttachView: %v", err)
	}
	firstResize := make(chan [2]int, 1)
	first.SetScreenSizeSink(func(cols, rows int) { firstResize <- [2]int{cols, rows} })
	firstChunks := make(chan string, 4)
	watchView(first, firstChunks)
	transport.send("before-hibernate")
	if got := waitChunk(t, firstChunks); got != "before-hibernate" {
		t.Fatalf("first view output = %q", got)
	}

	if !session.Hibernate() {
		t.Fatal("Hibernate returned false")
	}
	// The detached view reports EOF.
	select {
	case _, ok := <-firstChunks:
		if ok {
			t.Fatal("hibernated view returned more output instead of EOF")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("hibernated view did not end")
	}

	done := make(chan struct{})
	go func() {
		transport.remoteResize(t, 120, 41)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("remote resize blocked while hibernated")
	}
	transport.send("frame-120x41")

	resumed, err := session.Resume()
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	t.Cleanup(func() { _ = resumed.Close() })
	resumedResize := make(chan [2]int, 1)
	resumed.SetScreenSizeSink(func(cols, rows int) { resumedResize <- [2]int{cols, rows} })
	resumedChunks := make(chan string, 8)
	watchView(resumed, resumedChunks)

	// The snapshot replay contains "before-hibernate" and the new frame. The
	// pending sink must run before either is readable.
	select {
	case size := <-resumedResize:
		if size != [2]int{120, 41} {
			t.Fatalf("resumed sink size = %v, want [120 41]", size)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("resumed view read output before applying the pending size")
	}
	var replayed string
	for {
		chunk := waitChunk(t, resumedChunks)
		replayed += chunk
		if len(replayed) >= len("before-hibernate")+len("frame-120x41") {
			break
		}
	}
	if !strings.Contains(replayed, "before-hibernate") ||
		!strings.Contains(replayed, "frame-120x41") {
		t.Fatalf("resumed replay = %q", replayed)
	}
}

// TestPlainTransportIsNotScreenSized keeps the MyGo ResizeByConn selection
// honest: only a ScreenSizer transport opts into server-authoritative sizing.
func TestPlainTransportIsNotScreenSized(t *testing.T) {
	session := openScreenSizerSession(t, newFakeTransport())
	if session.ScreenSizeFromRemote() {
		t.Fatal("plain transport reported a remote screen size")
	}
}
