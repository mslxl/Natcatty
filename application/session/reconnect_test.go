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

	mu            sync.Mutex
	written       bytes.Buffer
	resizes       [][2]int
	preserveCalls int
	discardCalls  int
}

// PreserveResume records the handover decision; the transport itself has no
// secret store.
func (t *scriptedTransport) PreserveResume() {
	t.mu.Lock()
	t.preserveCalls++
	t.mu.Unlock()
}

// DiscardResume records the explicit-close cleanup.
func (t *scriptedTransport) DiscardResume() {
	t.mu.Lock()
	t.discardCalls++
	t.mu.Unlock()
}

func (t *scriptedTransport) preserves() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.preserveCalls
}

func (t *scriptedTransport) discards() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.discardCalls
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

func (t *scriptedTransport) End() End {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.end
}

func (t *scriptedTransport) Close() error {
	t.once.Do(func() { close(t.closed) })
	return nil
}

func (t *scriptedTransport) fail(end End) {
	t.mu.Lock()
	t.end = end
	t.mu.Unlock()
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

// resizeBlockingTransport models a transport whose Resize can wedge, as SSH's
// window-change request can while its socket is blocked.
type resizeBlockingTransport struct {
	*scriptedTransport
	entered   chan struct{}
	release   chan struct{}
	enterOnce sync.Once
}

func newResizeBlockingTransport() *resizeBlockingTransport {
	return &resizeBlockingTransport{
		scriptedTransport: newScriptedTransport(),
		entered:           make(chan struct{}),
		release:           make(chan struct{}),
	}
}

func (t *resizeBlockingTransport) Resize(cols, rows int) error {
	t.enterOnce.Do(func() { close(t.entered) })
	<-t.release
	return t.scriptedTransport.Resize(cols, rows)
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

func waitForDone(t *testing.T, session *Session) {
	t.Helper()
	waitForCondition(t, "the closed Session", func() bool {
		select {
		case <-session.Done():
			return true
		default:
			return false
		}
	})
}

// TestReconnectNowDialsImmediately backs the Session menu's Reconnect action:
// it must be available only while a reconnectable Session is running, and it
// must open a new transport without waiting for the policy delay.
func TestReconnectNowDialsImmediately(t *testing.T) {
	first := newScriptedTransport()
	second := newScriptedTransport()
	factory := &scriptedFactory{transports: []*scriptedTransport{first, second}}
	service := reconnectTestService(factory)
	session, err := service.Open(context.Background(), StartSpec{
		Kind:      KindSSH,
		Reconnect: &ReconnectPolicy{Delay: time.Hour},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	if !session.CanReconnect() {
		t.Fatal("CanReconnect = false for a running SSH Session with a policy")
	}
	if !session.ReconnectNow() {
		t.Fatal("ReconnectNow was refused")
	}
	waitForCondition(t, "the immediate reconnect", func() bool { return factory.callCount() == 2 })
	if snapshot := session.Snapshot(); snapshot.Status != StatusRunning {
		t.Fatalf("status after reconnect = %q", snapshot.Status)
	}
	_ = session.Close()

	// A local shell restarts by hand as a remote one reconnects.
	localFactory := &scriptedFactory{transports: []*scriptedTransport{newScriptedTransport(), newScriptedTransport()}}
	local, err := reconnectTestService(localFactory).Open(context.Background(), StartSpec{Kind: KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = local.Close() })
	if !local.CanReconnect() || !local.ReconnectNow() {
		t.Fatal("a running local Session refused Reconnect")
	}
	waitForCondition(t, "the restarted local shell", func() bool { return localFactory.callCount() == 2 })

	// A failed manual attempt must not turn into a zero-delay retry loop; it
	// falls back to the policy interval for the next attempt.
	third := newScriptedTransport()
	retrying := &scriptedFactory{
		transports: []*scriptedTransport{newScriptedTransport(), third, third},
		errs:       []error{nil, io.ErrUnexpectedEOF, nil},
	}
	retryService := reconnectTestService(retrying)
	retrySession, err := retryService.Open(context.Background(), StartSpec{
		Kind: KindSSH, Reconnect: &ReconnectPolicy{Delay: 100 * time.Millisecond},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = retrySession.Close() })
	if !retrySession.ReconnectNow() {
		t.Fatal("the second ReconnectNow was refused")
	}
	waitForCondition(t, "the failed immediate attempt", func() bool { return retrying.callCount() == 2 })
	time.Sleep(20 * time.Millisecond)
	if calls := retrying.callCount(); calls != 2 {
		t.Fatalf("factory calls = %d, want the policy delay before the third attempt", calls)
	}
	waitForCondition(t, "the delayed retry", func() bool {
		return retrying.callCount() == 3 && retrySession.Snapshot().Connection == ConnectionStatusActive
	})
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

	// The abandoned transport's resume credentials must survive the handover,
	// or the replacement Open could not reattach a remote ET session.
	if got := first.preserves(); got != 1 {
		t.Fatalf("abandoned transport PreserveResume calls = %d, want 1", got)
	}
	if got := second.preserves(); got != 0 {
		t.Fatalf("replacement transport was preserved before close: %d calls", got)
	}

	if _, err := session.TerminalConn().Write([]byte("ls\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	waitForCondition(t, "input on the replacement transport", func() bool {
		return second.input() == "ls\n"
	})

	if err := session.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	waitForCondition(t, "the explicitly closed Session", func() bool {
		select {
		case <-session.Done():
			return true
		default:
			return false
		}
	})
	if got := second.preserves(); got != 0 {
		t.Fatalf("an explicit close preserved the replacement transport %d times", got)
	}
	if got := second.discards(); got != 1 {
		t.Fatalf("an explicit close discarded the replacement credentials %d times, want 1", got)
	}
}

// TestUserCloseDuringReconnectDelayClearsSavedCredentials covers the window
// between preserving the old transport for a handover and the replacement
// Open: the old transport is already closed, but a user close must still clear
// the saved ET credentials.
func TestUserCloseDuringReconnectDelayClearsSavedCredentials(t *testing.T) {
	first := newScriptedTransport()
	factory := &scriptedFactory{transports: []*scriptedTransport{first}}
	service := reconnectTestService(factory)

	session, err := service.Open(context.Background(), StartSpec{
		Kind: KindET, ResumeKey: "resume-test",
		Reconnect: &ReconnectPolicy{Delay: time.Hour},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	first.fail(End{Reason: EndReasonError})
	waitForCondition(t, "the preserved reconnect handover", func() bool {
		return first.preserves() == 1 &&
			session.Snapshot().Connection == ConnectionStatusReconnecting
	})
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
	if got := first.discards(); got != 1 {
		t.Fatalf("an explicit close during the reconnect delay discarded credentials %d times, want 1", got)
	}
}

// TestCloseDoesNotBlockOnABlockedTransportResize pins the Close contract the
// UI depends on: Close is called synchronously from the window close path, so a
// loop stuck in a transport call (an SSH window-change write to a wedged socket,
// for example) must not wedge the caller. The close stays pending and its
// credential cleanup runs when teardown reaches it.
func TestCloseDoesNotBlockOnABlockedTransportResize(t *testing.T) {
	transport := newResizeBlockingTransport()
	service := reconnectTestService(&staticFactory{transport: transport})
	session, err := service.Open(context.Background(), StartSpec{
		Kind: KindET, ResumeKey: "resume-test",
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(transport.release) }) }
	t.Cleanup(release)

	if err := session.Resize(120, 40); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	select {
	case <-transport.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the transport never entered Resize")
	}

	closed := make(chan struct{})
	started := time.Now()
	go func() {
		_ = session.Close()
		close(closed)
	}()
	select {
	case <-closed:
		t.Logf("Close returned in %v while transport Resize was blocked", time.Since(started))
	case <-time.After(time.Second):
		t.Fatal("Close blocked while the command loop was inside transport Resize")
	}

	select {
	case <-session.Done():
		t.Fatal("the Session finished while Resize was still blocked")
	default:
	}

	release()
	waitForDone(t, session)
	if got := transport.discards(); got != 1 {
		t.Fatalf("explicit close discarded credentials %d times, want 1", got)
	}
	if got := transport.preserves(); got != 0 {
		t.Fatalf("explicit close preserved credentials %d times, want 0", got)
	}
}

// TestCloseRacingTheEndAppliesTheResumeDecisionOnce: a close that races the
// natural end of the Session must apply its preserve/discard decision exactly
// once. The close record is consumed by the loop or teardown, never both.
func TestCloseRacingTheEndAppliesTheResumeDecisionOnce(t *testing.T) {
	cases := []struct {
		name         string
		close        func(*Session) error
		wantPreserve int
		wantDiscard  int
	}{
		{"explicit close discards", (*Session).Close, 0, 1},
		{"app close preserves", (*Session).ClosePreservingResume, 1, 0},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			for i := 0; i < 100; i++ {
				first := newScriptedTransport()
				service := reconnectTestService(&staticFactory{transport: first})
				session, err := service.Open(context.Background(), StartSpec{
					Kind: KindET, ResumeKey: "resume-test",
				})
				if err != nil {
					t.Fatalf("iteration %d: Open: %v", i, err)
				}
				var wg sync.WaitGroup
				wg.Add(2)
				go func() {
					defer wg.Done()
					_ = testCase.close(session)
				}()
				go func() {
					defer wg.Done()
					first.fail(End{Reason: EndReasonExited})
				}()
				wg.Wait()
				waitForDone(t, session)
				if got := first.preserves(); got != testCase.wantPreserve {
					t.Fatalf("iteration %d: PreserveResume calls = %d, want %d", i, got, testCase.wantPreserve)
				}
				if got := first.discards(); got != testCase.wantDiscard {
					t.Fatalf("iteration %d: DiscardResume calls = %d, want %d", i, got, testCase.wantDiscard)
				}
			}
		})
	}
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
	if got := first.preserves(); got != 0 {
		t.Fatalf("a user close preserved the transport %d times", got)
	}
}

// TestCloseAllPreservesResumableTransports: app shutdown is not a user close.
// The saved ET credentials must survive so the next process can reattach.
func TestCloseAllPreservesResumableTransports(t *testing.T) {
	first := newScriptedTransport()
	factory := &scriptedFactory{transports: []*scriptedTransport{first}}
	service := reconnectTestService(factory)

	session, err := service.Open(context.Background(), StartSpec{Kind: KindET})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	service.CloseAll()
	waitForCondition(t, "the app-shutdown close", func() bool {
		select {
		case <-session.Done():
			return true
		default:
			return false
		}
	})
	if got := first.preserves(); got != 1 {
		t.Fatalf("CloseAll PreserveResume calls = %d, want 1", got)
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
	// The reconnect handover preserved the pair before the replacement Open
	// failed. The user then closes the ended Pane: Close finds the loop already
	// finished, so it must clear the preserved pair itself. Otherwise the
	// secret store keeps orphaned ET credentials forever.
	if got := first.preserves(); got == 0 {
		t.Fatal("the reconnect handover did not preserve the credentials")
	}
	if err := session.Close(); err != nil {
		t.Fatalf("Close after the permanent failure: %v", err)
	}
	if got := first.discards(); got != 1 {
		t.Fatalf("an explicit Close after the permanent reconnect failure discarded credentials %d times, want 1", got)
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

// A local shell that ends is not restarted on its own: only a remote
// transport drops and comes back.
func TestLocalSessionNeverRestartsOnItsOwn(t *testing.T) {
	first := newScriptedTransport()
	factory := &scriptedFactory{transports: []*scriptedTransport{first, newScriptedTransport()}}
	session, err := reconnectTestService(factory).Open(context.Background(), StartSpec{Kind: KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	first.fail(End{Reason: EndReasonError})
	waitForCondition(t, "the ended local Session", func() bool {
		select {
		case <-session.Done():
			return true
		default:
			return false
		}
	})
	if calls := factory.callCount(); calls != 1 {
		t.Fatalf("factory calls = %d after the shell ended, want 1", calls)
	}
}
