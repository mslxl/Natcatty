package session

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/mslxl/natcatty/domain/model"
)

// scriptedTransport is a Transport with a test-chosen end classification.
type scriptedTransport struct {
	output chan []byte
	closed chan struct{}
	once   sync.Once
	end    End

	mu      sync.Mutex
	written bytes.Buffer
	resizes [][2]int
}

func newScriptedTransport() *scriptedTransport {
	return &scriptedTransport{output: make(chan []byte, 8), closed: make(chan struct{})}
}

func (t *scriptedTransport) Read(p []byte) (int, error) {
	select {
	case chunk := <-t.output:
		return copy(p, chunk), nil
	case <-t.closed:
		return 0, io.EOF
	}
}

func (t *scriptedTransport) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.written.Write(p)
}

func (t *scriptedTransport) Resize(cols, rows int) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.resizes = append(t.resizes, [2]int{cols, rows})
	return nil
}

func (t *scriptedTransport) End() End { return t.end }

func (t *scriptedTransport) Close() error {
	t.once.Do(func() { close(t.closed) })
	return nil
}

func (t *scriptedTransport) fail(end End) {
	t.end = end
	t.once.Do(func() { close(t.closed) })
}

func (t *scriptedTransport) input() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.written.String()
}

// scriptedFactory returns a scripted transport or error for each Open.
type scriptedFactory struct {
	mu         sync.Mutex
	transports []*scriptedTransport
	errs       []error
	calls      int
	specs      []StartSpec
}

func (f *scriptedFactory) Open(_ context.Context, spec StartSpec) (Transport, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	index := f.calls
	f.calls++
	f.specs = append(f.specs, spec)
	if index < len(f.errs) && f.errs[index] != nil {
		return nil, f.errs[index]
	}
	if index < len(f.transports) {
		return f.transports[index], nil
	}
	return nil, io.EOF
}

func (f *scriptedFactory) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type permanentTestError struct{}

func (permanentTestError) Error() string   { return "authentication rejected" }
func (permanentTestError) Permanent() bool { return true }

func reconnectTestService(factory Factory) *Service {
	return NewService(factory, WithIDGenerator(func() model.SessionID { return "session-reconnect" }))
}

func waitForCondition(t *testing.T, what string, ready func() bool) {
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

func TestReconnectReasonPredicate(t *testing.T) {
	policy := &ReconnectPolicy{}
	cases := map[EndReason]bool{
		EndReasonError:    true,
		EndReasonClosed:   true,
		EndReasonExited:   false,
		EndReasonTimeout:  false,
		EndReasonSignaled: false,
	}
	for reason, want := range cases {
		if got := policy.shouldRetry(End{Reason: reason}); got != want {
			t.Errorf("shouldRetry(%q) = %t, want %t", reason, got, want)
		}
	}
	if (*ReconnectPolicy)(nil).shouldRetry(End{Reason: EndReasonError}) {
		t.Fatal("a nil policy retried")
	}
}

func TestReconnectDelayDefaultsToFiveSeconds(t *testing.T) {
	if got := (&ReconnectPolicy{}).delay(); got != 5*time.Second {
		t.Fatalf("default reconnect delay = %v, want 5s", got)
	}
}

func TestSessionReconnectsAfterATransportError(t *testing.T) {
	first := newScriptedTransport()
	second := newScriptedTransport()
	factory := &scriptedFactory{transports: []*scriptedTransport{first, second}}
	service := reconnectTestService(factory)

	session, err := service.Open(context.Background(), StartSpec{
		Kind: KindSSH, Hostname: "example.com", Port: 22, Username: "root",
		Reconnect: &ReconnectPolicy{Delay: 50 * time.Millisecond},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	first.fail(End{Reason: EndReasonError})
	waitForCondition(t, "the reconnecting indicator", func() bool {
		return session.Snapshot().Connection == ConnectionStatusReconnecting
	})
	waitForCondition(t, "the replacement transport", func() bool {
		return factory.callCount() == 2 && session.Snapshot().Connection == ConnectionStatusActive
	})
	if snapshot := session.Snapshot(); snapshot.Status != StatusRunning {
		t.Fatalf("status = %q during reconnect, want running", snapshot.Status)
	}

	if _, err := session.TerminalConn().Write([]byte("ls\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	waitForCondition(t, "input on the replacement transport", func() bool {
		return second.input() == "ls\n"
	})
}

func TestSessionDoesNotReconnectAfterACleanExit(t *testing.T) {
	first := newScriptedTransport()
	factory := &scriptedFactory{transports: []*scriptedTransport{first}}
	service := reconnectTestService(factory)

	session, err := service.Open(context.Background(), StartSpec{
		Kind: KindSSH, Reconnect: &ReconnectPolicy{Delay: time.Millisecond},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	first.fail(End{Reason: EndReasonExited, ExitCode: 7, HasExitCode: true})
	waitForCondition(t, "the exited Session", func() bool {
		select {
		case <-session.Done():
			return true
		default:
			return false
		}
	})
	snapshot := session.Snapshot()
	if snapshot.Status != StatusExited || !snapshot.HasExitCode || snapshot.ExitCode != 7 {
		t.Fatalf("snapshot = %+v, want exited with code 7", snapshot)
	}
	time.Sleep(20 * time.Millisecond)
	if calls := factory.callCount(); calls != 1 {
		t.Fatalf("factory calls = %d after a clean exit, want 1", calls)
	}
}

func TestSessionDoesNotReconnectAfterUserClose(t *testing.T) {
	first := newScriptedTransport()
	factory := &scriptedFactory{transports: []*scriptedTransport{first}}
	service := reconnectTestService(factory)

	session, err := service.Open(context.Background(), StartSpec{
		Kind: KindSSH, Reconnect: &ReconnectPolicy{Delay: time.Millisecond},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	waitForCondition(t, "the closed Session", func() bool {
		select {
		case <-session.Done():
			return true
		default:
			return false
		}
	})
	if snapshot := session.Snapshot(); snapshot.Status != StatusClosed {
		t.Fatalf("status = %q, want closed", snapshot.Status)
	}
	time.Sleep(20 * time.Millisecond)
	if calls := factory.callCount(); calls != 1 {
		t.Fatalf("factory calls = %d after a user close, want 1", calls)
	}
}

func TestSessionStopsReconnectingOnAPermanentFailure(t *testing.T) {
	first := newScriptedTransport()
	factory := &scriptedFactory{
		transports: []*scriptedTransport{first},
		errs:       []error{nil, permanentTestError{}},
	}
	service := reconnectTestService(factory)

	session, err := service.Open(context.Background(), StartSpec{
		Kind: KindSSH, Reconnect: &ReconnectPolicy{Delay: time.Millisecond},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	first.fail(End{Reason: EndReasonError})
	waitForCondition(t, "the permanent-failure end", func() bool {
		select {
		case <-session.Done():
			return true
		default:
			return false
		}
	})
	snapshot := session.Snapshot()
	if snapshot.Status != StatusExited || snapshot.Connection != ConnectionStatusError {
		t.Fatalf("snapshot = %+v, want an exited Session with a connection error", snapshot)
	}
	if calls := factory.callCount(); calls != 2 {
		t.Fatalf("factory calls = %d, want one retry then a permanent stop", calls)
	}
}

func TestSessionRetriesTransientReconnectFailures(t *testing.T) {
	first := newScriptedTransport()
	second := newScriptedTransport()
	factory := &scriptedFactory{
		transports: []*scriptedTransport{first, second, second},
		errs:       []error{nil, io.ErrUnexpectedEOF, nil},
	}
	service := reconnectTestService(factory)

	session, err := service.Open(context.Background(), StartSpec{
		Kind: KindSSH, Reconnect: &ReconnectPolicy{Delay: time.Millisecond},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	first.fail(End{Reason: EndReasonClosed})
	waitForCondition(t, "the retried connection", func() bool {
		return factory.callCount() == 3 && session.Snapshot().Connection == ConnectionStatusActive
	})
}

func TestStaleTransportEndIsIgnoredAfterReconnect(t *testing.T) {
	first := newScriptedTransport()
	second := newScriptedTransport()
	factory := &scriptedFactory{transports: []*scriptedTransport{first, second}}
	service := reconnectTestService(factory)

	session, err := service.Open(context.Background(), StartSpec{
		Kind: KindSSH, Reconnect: &ReconnectPolicy{Delay: time.Millisecond},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	first.fail(End{Reason: EndReasonError})
	waitForCondition(t, "the replacement transport", func() bool {
		return factory.callCount() == 2 && session.Snapshot().Connection == ConnectionStatusActive
	})
	// A late event from the abandoned generation must not end the Session.
	session.post(command{
		kind: commandTransportEnded, generation: 0, transport: first,
		end: End{Reason: EndReasonError},
	})
	time.Sleep(20 * time.Millisecond)
	if snapshot := session.Snapshot(); snapshot.Status != StatusRunning || snapshot.Connection != ConnectionStatusActive {
		t.Fatalf("snapshot = %+v after a stale end, want a running active Session", snapshot)
	}
}
