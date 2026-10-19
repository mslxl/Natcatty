package session

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mslxl/natcatty/domain/model"
)

// fakeTransport is a Transport whose output the test writes and whose input
// and resizes the test reads. Wait returns exitCode once the output ends.
type fakeTransport struct {
	output    chan []byte
	closed    chan struct{}
	closeOnce sync.Once

	mu       sync.Mutex
	written  bytes.Buffer
	resizes  [][2]int
	exitCode int
}

func newFakeTransport() *fakeTransport {
	return &fakeTransport{output: make(chan []byte, 1), closed: make(chan struct{})}
}

func (f *fakeTransport) Read(p []byte) (int, error) {
	select {
	case chunk := <-f.output:
		return copy(p, chunk), nil
	case <-f.closed:
		return 0, io.EOF
	}
}

func (f *fakeTransport) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.written.Write(p)
}

func (f *fakeTransport) Resize(cols, rows int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resizes = append(f.resizes, [2]int{cols, rows})
	return nil
}

func (f *fakeTransport) Wait() (int, error) { return f.exitCode, nil }

func (f *fakeTransport) Close() error {
	f.closeOnce.Do(func() { close(f.closed) })
	return nil
}

func (f *fakeTransport) send(p string) {
	f.output <- []byte(p)
}

func (f *fakeTransport) end() {
	f.closeOnce.Do(func() { close(f.closed) })
}

func (f *fakeTransport) input() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.written.String()
}

func (f *fakeTransport) resizeCalls() [][2]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][2]int(nil), f.resizes...)
}

type fakeFactory struct {
	transport *fakeTransport
	spec      StartSpec
}

func (f *fakeFactory) Open(_ context.Context, spec StartSpec) (Transport, error) {
	f.spec = spec
	return f.transport, nil
}

func newTestService(factory Factory, opts ...Option) *Service {
	return NewService(factory, opts...)
}

func TestOpenLocalStartsARunningLocalSession(t *testing.T) {
	factory := &fakeFactory{transport: newFakeTransport()}
	service := newTestService(factory, WithIDGenerator(func() model.SessionID { return "session-1" }))

	session, err := service.OpenLocal(context.Background())
	if err != nil {
		t.Fatalf("OpenLocal: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	if factory.spec.Kind != KindLocal {
		t.Errorf("factory spec kind = %q, want %q", factory.spec.Kind, KindLocal)
	}
	if factory.spec.Cols != 80 || factory.spec.Rows != 24 {
		t.Errorf("factory spec size = %dx%d, want 80x24", factory.spec.Cols, factory.spec.Rows)
	}

	snapshot := session.Snapshot()
	if snapshot.ID != "session-1" {
		t.Errorf("snapshot ID = %q, want session-1", snapshot.ID)
	}
	if snapshot.Kind != KindLocal {
		t.Errorf("snapshot kind = %q, want %q", snapshot.Kind, KindLocal)
	}
	if snapshot.Status != StatusRunning {
		t.Errorf("snapshot status = %q, want %q", snapshot.Status, StatusRunning)
	}
	if snapshot.Cols != 80 || snapshot.Rows != 24 {
		t.Errorf("snapshot size = %dx%d, want 80x24", snapshot.Cols, snapshot.Rows)
	}
}

func TestOpenSSHPassesTheConnectionTargetToTheFactory(t *testing.T) {
	factory := &fakeFactory{transport: newFakeTransport()}
	service := newTestService(factory, WithIDGenerator(func() model.SessionID { return "session-ssh" }))

	session, err := service.Open(context.Background(), StartSpec{
		Kind: KindSSH, HostID: "host-1", Hostname: "example.com", Port: 2222,
		Username: "root", Cols: 120, Rows: 40,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	spec := factory.spec
	if spec.Kind != KindSSH || spec.HostID != "host-1" || spec.Hostname != "example.com" ||
		spec.Port != 2222 || spec.Username != "root" {
		t.Fatalf("factory spec = %+v, want the SSH connection target", spec)
	}
	if snapshot := session.Snapshot(); snapshot.Kind != KindSSH {
		t.Fatalf("snapshot kind = %q, want %q", snapshot.Kind, KindSSH)
	}
}

func TestSessionsHaveUniqueULIDIdentifiers(t *testing.T) {
	service := newTestService(&fakeFactory{transport: newFakeTransport()})
	t.Cleanup(service.CloseAll)

	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	ids := make(map[model.SessionID]bool)
	for range 2 {
		session, err := service.OpenLocal(context.Background())
		if err != nil {
			t.Fatalf("OpenLocal: %v", err)
		}
		id := session.Snapshot().ID
		if len(id) != 26 {
			t.Fatalf("session ID %q has length %d, want 26", id, len(id))
		}
		for _, character := range id {
			if !strings.ContainsRune(alphabet, character) {
				t.Fatalf("session ID %q contains %q, which is not Crockford base32", id, character)
			}
		}
		if ids[id] {
			t.Fatalf("session ID %q was generated twice", id)
		}
		ids[id] = true
	}
}

func TestTerminalConnCarriesOutputAndInput(t *testing.T) {
	for _, kind := range []Kind{KindLocal, KindSSH} {
		t.Run(string(kind), func(t *testing.T) {
			factory := &fakeFactory{transport: newFakeTransport()}
			service := newTestService(factory)
			session, err := service.Open(context.Background(), StartSpec{
				Kind: kind, HostID: "host-1", Hostname: "example.com", Username: "root",
			})
			if err != nil {
				t.Fatalf("Open(%s): %v", kind, err)
			}
			t.Cleanup(func() { _ = session.Close() })

			conn := session.TerminalConn()
			factory.transport.send("hello")

			got := make([]byte, 5)
			if _, err := readWithTimeout(t, conn, got); err != nil {
				t.Fatalf("Read: %v", err)
			}
			if string(got) != "hello" {
				t.Fatalf("output = %q, want hello", got)
			}

			if _, err := conn.Write([]byte("ls\n")); err != nil {
				t.Fatalf("Write: %v", err)
			}
			eventually(t, "input to reach the transport", func() bool {
				return factory.transport.input() == "ls\n"
			})
		})
	}
}

func TestResizeReachesTheTransportAndTheSnapshot(t *testing.T) {
	for _, kind := range []Kind{KindLocal, KindSSH} {
		t.Run(string(kind), func(t *testing.T) {
			factory := &fakeFactory{transport: newFakeTransport()}
			service := newTestService(factory)
			session, err := service.Open(context.Background(), StartSpec{
				Kind: kind, HostID: "host-1", Hostname: "example.com", Username: "root",
			})
			if err != nil {
				t.Fatalf("Open(%s): %v", kind, err)
			}
			t.Cleanup(func() { _ = session.Close() })

			if err := session.TerminalConn().Resize(120, 40); err != nil {
				t.Fatalf("Resize: %v", err)
			}

			eventually(t, "the transport to be resized", func() bool {
				calls := factory.transport.resizeCalls()
				return len(calls) == 1 && calls[0] == [2]int{120, 40}
			})
			eventually(t, "the snapshot to carry the new size", func() bool {
				snapshot := session.Snapshot()
				return snapshot.Cols == 120 && snapshot.Rows == 40
			})
		})
	}
}

func TestNaturalExitPublishesTheStatusAndExitCode(t *testing.T) {
	factory := &fakeFactory{transport: newFakeTransport()}
	factory.transport.exitCode = 7
	service := newTestService(factory)
	session, err := service.OpenLocal(context.Background())
	if err != nil {
		t.Fatalf("OpenLocal: %v", err)
	}

	factory.transport.end()
	select {
	case <-session.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("the session did not end after its transport did")
	}

	snapshot := session.Snapshot()
	if snapshot.Status != StatusExited {
		t.Errorf("snapshot status = %q, want %q", snapshot.Status, StatusExited)
	}
	if !snapshot.HasExitCode || snapshot.ExitCode != 7 {
		t.Errorf("snapshot exit code = (%v, %d), want (true, 7)", snapshot.HasExitCode, snapshot.ExitCode)
	}
}

func TestClosePublishesTheClosedStatus(t *testing.T) {
	factory := &fakeFactory{transport: newFakeTransport()}
	service := newTestService(factory)
	session, err := service.OpenLocal(context.Background())
	if err != nil {
		t.Fatalf("OpenLocal: %v", err)
	}

	if err := session.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case <-session.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("the session did not end after Close")
	}

	snapshot := session.Snapshot()
	if snapshot.Status != StatusClosed {
		t.Errorf("snapshot status = %q, want %q", snapshot.Status, StatusClosed)
	}
	if snapshot.HasExitCode {
		t.Errorf("a closed session reported an exit code: %d", snapshot.ExitCode)
	}
}

// floodTransport produces output without a bound until its output is
// exhausted, recording how far the session read.
type floodTransport struct {
	data       []byte
	chunk      int
	mu         sync.Mutex
	readOffset int
	closed     chan struct{}
	closeOnce  sync.Once
}

func newFloodTransport(size int) *floodTransport {
	return &floodTransport{
		data:   bytes.Repeat([]byte("x"), size),
		chunk:  32 << 10,
		closed: make(chan struct{}),
	}
}

func (f *floodTransport) Read(p []byte) (int, error) {
	select {
	case <-f.closed:
		return 0, io.EOF
	default:
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.readOffset >= len(f.data) {
		return 0, io.EOF
	}
	n := copy(p, f.data[f.readOffset:f.readOffset+min(f.chunk, len(f.data)-f.readOffset)])
	f.readOffset += n
	return n, nil
}

func (f *floodTransport) Write([]byte) (int, error) { return 0, nil }
func (f *floodTransport) Resize(int, int) error     { return nil }
func (f *floodTransport) Wait() (int, error)        { return 0, nil }
func (f *floodTransport) Close() error {
	f.closeOnce.Do(func() { close(f.closed) })
	return nil
}

func (f *floodTransport) offset() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.readOffset
}

func TestFlowControlPausesAtTheHighWatermarkAndResumesAtTheLow(t *testing.T) {
	transport := newFloodTransport(4 << 20)
	service := NewService(&floodFactory{transport: transport})
	session, err := service.OpenLocal(context.Background())
	if err != nil {
		t.Fatalf("OpenLocal: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	eventually(t, "the backlog to reach the high watermark", func() bool {
		return session.Snapshot().PendingBytes >= HighWaterMark
	})
	if !session.Snapshot().Paused {
		t.Fatal("the session is not paused at the high watermark")
	}

	conn := session.TerminalConn()
	buf := make([]byte, 32<<10)

	// Drain part of the backlog but stay above the low watermark: the reader
	// must remain paused, so the transport offset cannot advance.
	pausedAt := transport.offset()
	drainTo := LowWaterMark + 1
	for pending := session.Snapshot().PendingBytes; pending > drainTo; pending = session.Snapshot().PendingBytes {
		n := min(len(buf), pending-drainTo)
		if _, err := conn.Read(buf[:n]); err != nil {
			t.Fatalf("draining output: %v", err)
		}
	}
	if !session.Snapshot().Paused {
		t.Fatal("the session resumed above the low watermark")
	}
	if got := transport.offset(); got != pausedAt {
		t.Fatalf("the transport kept being read while paused: offset %d -> %d", pausedAt, got)
	}

	for session.Snapshot().PendingBytes > LowWaterMark {
		if _, err := conn.Read(buf); err != nil {
			t.Fatalf("draining output: %v", err)
		}
	}

	eventually(t, "the session to resume below the low watermark", func() bool {
		return !session.Snapshot().Paused
	})
	eventually(t, "the transport to be read again", func() bool {
		return transport.offset() > pausedAt
	})
}

type floodFactory struct{ transport *floodTransport }

func (f *floodFactory) Open(context.Context, StartSpec) (Transport, error) {
	return f.transport, nil
}

func readWithTimeout(t *testing.T, r io.Reader, p []byte) (int, error) {
	t.Helper()
	type result struct {
		n   int
		err error
	}
	done := make(chan result, 1)
	go func() {
		n, err := r.Read(p)
		done <- result{n, err}
	}()
	select {
	case got := <-done:
		return got.n, got.err
	case <-time.After(2 * time.Second):
		t.Fatal("read timed out")
		return 0, nil
	}
}

// notifyingTransport is a fake transport that also reports runtime notices.
type notifyingTransport struct {
	*fakeTransport
	notices chan Notice
}

func (t *notifyingTransport) Notices() <-chan Notice { return t.notices }

type notifyingFactory struct{ transport Transport }

func (f *notifyingFactory) Open(context.Context, StartSpec) (Transport, error) {
	return f.transport, nil
}

func TestNoticesReachTheSnapshotWithoutEndingTheSession(t *testing.T) {
	transport := &notifyingTransport{fakeTransport: newFakeTransport(), notices: make(chan Notice, 1)}
	service := newTestService(&notifyingFactory{transport: transport},
		WithIDGenerator(func() model.SessionID { return "session-notice" }))

	session, err := service.OpenLocal(context.Background())
	if err != nil {
		t.Fatalf("OpenLocal: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	transport.notices <- Notice{Code: NoticeX11LocalDisplay, Target: "localhost:6000"}
	eventually(t, "the notice to reach the snapshot", func() bool {
		snapshot := session.Snapshot()
		return snapshot.Notice != nil &&
			snapshot.Notice.Code == NoticeX11LocalDisplay &&
			snapshot.Notice.Target == "localhost:6000"
	})

	if snapshot := session.Snapshot(); snapshot.Status != StatusRunning {
		t.Fatalf("notice ended the Session: status = %q", snapshot.Status)
	}
}

func eventually(t *testing.T, what string, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
