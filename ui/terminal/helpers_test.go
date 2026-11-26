package terminal

// The helpers in this file are shared by the ported suites. They come from
// MyGo's terminal plugin tests; natcatty keeps them here rather than in a
// framework-internal package, which it cannot import.

import (
	"image/png"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// waitGuard is the longest a wait waits before it fails the test, as
// MyGo's internal/wait.Guard: a hang guard, not a timeout to tune.
const waitGuard = time.Minute

// waitUntil waits until cond returns true, calling it again and again:
// right away, then at intervals growing to 20 ms. It is copied from
// MyGo's internal/wait.Until.
func waitUntil(t testing.TB, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitGuard)
	pause := time.Millisecond
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("%s never happened (waited %v)", what, waitGuard)
		}
		time.Sleep(pause)
		pause = min(2*pause, 20*time.Millisecond)
	}
}

// waitForValue waits for a value from ch. It is copied from MyGo's
// internal/wait.For.
func waitForValue[T any](t testing.TB, what string, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(waitGuard):
		t.Fatalf("%s never happened (waited %v)", what, waitGuard)
		var zero T
		return zero
	}
}

// loadLib loads libghostty-vt, or skips the test where none is named.
// Unlike the upstream helper it never downloads a library: point
// NATCATTY_GHOSTTY_VT at a build to run the terminal suites.
func loadLib(t *testing.T) {
	t.Helper()
	if os.Getenv("NATCATTY_GHOSTTY_VT") == "" && os.Getenv("MYGO_GHOSTTY_VT") == "" {
		t.Skip("set NATCATTY_GHOSTTY_VT to a libghostty-vt build")
	}
	if err := Load(); err != nil {
		t.Fatal(err)
	}
}

// waitFor runs frames until cond holds: what a source writes arrives
// when it does, on the terminal's reader. The deadline is a hang guard.
func waitFor(t *testing.T, tt *ui.Tester, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitGuard)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
		tt.Frame()
	}
}

func save(t *testing.T, tt *ui.Tester, name string) {
	dir := os.Getenv("NATCATTY_TEST_IMAGES")
	if dir == "" {
		return
	}
	f, err := os.Create(dir + "/" + name + ".png")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	png.Encode(f, tt.Image())
}

// pipe is a Conn whose output the test writes with Feed, and which keeps
// what the terminal sends.
type pipe struct {
	closed chan struct{}
	mu     sync.Mutex
	sent   []byte
}

func newPipe() *pipe { return &pipe{closed: make(chan struct{})} }

func (p *pipe) Read([]byte) (int, error) {
	<-p.closed
	return 0, os.ErrClosed
}

func (p *pipe) Write(b []byte) (int, error) {
	p.mu.Lock()
	p.sent = append(p.sent, b...)
	p.mu.Unlock()
	return len(b), nil
}

// take returns what term sent since the last take: all that it queued
// for its Conn before take, which waits until the queue wrote it (see
// written), and nothing queued after.
func (p *pipe) take(term *Terminal) string {
	written(term)
	p.mu.Lock()
	defer p.mu.Unlock()
	s := string(p.sent)
	p.sent = nil
	return s
}

// written returns once the input queue of term, which writes to its Conn
// from a goroutine of its own, wrote what was queued before, and ran what
// was queued to run once that was written, as the statuses of a paste: it
// queues a marker, which the queue reaches in order. A closed queue writes
// nothing more.
func written(term *Terminal) {
	done := make(chan struct{})
	q := &term.in
	q.mu.Lock()
	q.init()
	if q.closed {
		q.mu.Unlock()
		return
	}
	q.queue = append(q.queue, queued{done: func() { close(done) }})
	q.cond.Signal()
	q.mu.Unlock()
	select {
	case <-done:
	case <-time.After(waitGuard): // a hang guard
		panic("the terminal's input queue wrote nothing for a minute")
	}
}

func (p *pipe) Close() error {
	select {
	case <-p.closed:
	default:
		close(p.closed)
	}
	return nil
}
