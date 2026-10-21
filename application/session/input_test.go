package session

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/mslxl/natcatty/domain/model"
)

// targetFactory opens one transport per StartSpec.Label so a test can observe
// each Session's input independently.
type targetFactory struct {
	mu         sync.Mutex
	transports map[string]*fakeTransport
}

func newTargetFactory() *targetFactory {
	return &targetFactory{transports: make(map[string]*fakeTransport)}
}

func (f *targetFactory) Open(_ context.Context, spec StartSpec) (Transport, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	transport := newFakeTransport()
	f.transports[spec.Label] = transport
	return transport, nil
}

func (f *targetFactory) transport(label string) *fakeTransport {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.transports[label]
}

func openTargets(t *testing.T, count int) (*Service, *targetFactory, []*Session, []model.SessionID) {
	t.Helper()
	factory := newTargetFactory()
	counter := 0
	service := NewService(factory, WithIDGenerator(func() model.SessionID {
		counter++
		return model.SessionID(fmt.Sprintf("session-%d", counter))
	}))
	t.Cleanup(service.CloseAll)
	sessions := make([]*Session, 0, count)
	ids := make([]model.SessionID, 0, count)
	for index := 0; index < count; index++ {
		session, err := service.Open(context.Background(), StartSpec{
			Kind:  KindLocal,
			Label: fmt.Sprintf("target-%d", index),
		})
		if err != nil {
			t.Fatalf("open target %d: %v", index, err)
		}
		sessions = append(sessions, session)
		ids = append(ids, session.ID())
	}
	return service, factory, sessions, ids
}

func waitForInput(t *testing.T, transport *fakeTransport, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if transport.input() == want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	if got := transport.input(); got != want {
		t.Fatalf("transport input = %q, want %q", got, want)
	}
}

func TestSendInputReachesExactlyTheSelectedSessions(t *testing.T) {
	service, factory, _, ids := openTargets(t, 3)

	result := service.SendInput([]model.SessionID{ids[0], ids[1]}, []byte("echo marker\r"))
	if got := result.DeliveredCount(); got != 2 {
		t.Fatalf("delivered = %d, want 2: %+v", got, result.Deliveries)
	}
	if !result.Complete() {
		t.Fatalf("result = %+v, want complete", result)
	}
	waitForInput(t, factory.transport("target-0"), "echo marker\r")
	waitForInput(t, factory.transport("target-1"), "echo marker\r")
	time.Sleep(20 * time.Millisecond)
	if got := factory.transport("target-2").input(); got != "" {
		t.Fatalf("the unselected Session received %q", got)
	}
}

func TestSendInputDeduplicatesTargetsAndKeepsTheirOrder(t *testing.T) {
	service, factory, _, ids := openTargets(t, 2)

	result := service.SendInput([]model.SessionID{ids[1], ids[1], ids[0]}, []byte("x\r"))
	if len(result.Deliveries) != 2 {
		t.Fatalf("deliveries = %+v, want two", result.Deliveries)
	}
	if result.Deliveries[0].SessionID != ids[1] || result.Deliveries[1].SessionID != ids[0] {
		t.Fatalf("delivery order = %+v", result.Deliveries)
	}
	waitForInput(t, factory.transport("target-0"), "x\r")
	waitForInput(t, factory.transport("target-1"), "x\r")
}

func TestSendInputReportsMissingAndClosedTargetsAsPartialFailure(t *testing.T) {
	service, factory, sessions, ids := openTargets(t, 2)
	_ = sessions[1].Close()
	select {
	case <-sessions[1].Done():
	case <-time.After(2 * time.Second):
		t.Fatal("the target did not close")
	}

	result := service.SendInput(
		[]model.SessionID{ids[0], ids[1], "session-missing"},
		[]byte("still works\r"))
	if result.DeliveredCount() != 1 || result.FailedCount() != 2 {
		t.Fatalf("result = %+v, want one delivered and two failed", result.Deliveries)
	}
	if result.Deliveries[0].Status != DeliveryDelivered {
		t.Fatalf("live target status = %q", result.Deliveries[0].Status)
	}
	if result.Deliveries[1].Status != DeliveryMissing {
		t.Fatalf("closed target = %+v", result.Deliveries[1])
	}
	if result.Deliveries[2].Status != DeliveryMissing {
		t.Fatalf("missing target = %+v", result.Deliveries[2])
	}
	waitForInput(t, factory.transport("target-0"), "still works\r")
}

func TestDoneFiresOnlyAfterTheServiceUnregistersTheSession(t *testing.T) {
	// A Session's end must be observable only once it is gone from the
	// registry. Otherwise SendInput races the teardown and reports a closed
	// Session as DeliveryFailed instead of DeliveryMissing. Repeat so the
	// window, if it reopens, is actually hit.
	for iteration := 0; iteration < 200; iteration++ {
		service, _, sessions, ids := openTargets(t, 1)
		if err := sessions[0].Close(); err != nil {
			t.Fatalf("iteration %d: Close: %v", iteration, err)
		}
		select {
		case <-sessions[0].Done():
		case <-time.After(2 * time.Second):
			t.Fatalf("iteration %d: the session did not close", iteration)
		}

		service.mu.Lock()
		registered := service.sessions[ids[0]]
		service.mu.Unlock()
		if registered != nil {
			t.Fatalf("iteration %d: the session is still registered after Done()", iteration)
		}

		result := service.SendInput([]model.SessionID{ids[0]}, []byte("late\r"))
		if result.Deliveries[0].Status != DeliveryMissing {
			t.Fatalf("iteration %d: delivery after Done() = %+v, want missing",
				iteration, result.Deliveries[0])
		}
	}
}

func TestSendInputReportsTheTransferGateWithoutCorruptingTheTransport(t *testing.T) {
	service, factory, sessions, ids := openTargets(t, 2)
	factory.transport("target-0").send("**\x18Bzmodem")
	deadline := time.Now().Add(2 * time.Second)
	for !sessions[0].Snapshot().Transfer.Active && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if !sessions[0].Snapshot().Transfer.Active {
		t.Fatal("the transfer gate did not open")
	}

	result := service.SendInput(ids, []byte("blocked\r"))
	if result.Deliveries[0].Status != DeliveryGated {
		t.Fatalf("gated target = %+v", result.Deliveries[0])
	}
	if result.Deliveries[1].Status != DeliveryDelivered {
		t.Fatalf("normal target = %+v", result.Deliveries[1])
	}
	time.Sleep(20 * time.Millisecond)
	if got := factory.transport("target-0").input(); got != "" {
		t.Fatalf("gated input reached the transport: %q", got)
	}
	waitForInput(t, factory.transport("target-1"), "blocked\r")
}

func TestInputHookObservesTypedInputButNotDeliveredBroadcastInput(t *testing.T) {
	factory := newTargetFactory()
	counter := 0
	var mu sync.Mutex
	var observed []string
	service := NewService(factory,
		WithIDGenerator(func() model.SessionID {
			counter++
			return model.SessionID(fmt.Sprintf("session-%d", counter))
		}),
		WithInputHook(func(id model.SessionID, data []byte) {
			mu.Lock()
			observed = append(observed, string(id)+":"+string(data))
			mu.Unlock()
		}),
	)
	t.Cleanup(service.CloseAll)
	first, err := service.Open(context.Background(), StartSpec{Kind: KindLocal, Label: "first"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Open(context.Background(), StartSpec{Kind: KindLocal, Label: "second"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := first.TerminalConn().Write([]byte("typed\r")); err != nil {
		t.Fatal(err)
	}
	if _, err := second.TerminalConn().Write([]byte("own\r")); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	got := append([]string(nil), observed...)
	mu.Unlock()
	if len(got) != 2 || got[0] != string(first.ID())+":typed\r" ||
		got[1] != string(second.ID())+":own\r" {
		t.Fatalf("typed hook = %v", got)
	}

	service.SendInput([]model.SessionID{second.ID()}, []byte("broadcast\r"))
	waitForInput(t, factory.transport("second"), "own\rbroadcast\r")
	mu.Lock()
	after := len(observed)
	mu.Unlock()
	if after != 2 {
		t.Fatalf("broadcast delivery re-entered the input hook: %d observations", after)
	}
}

func TestSendChunksDeliversEveryLineInOrder(t *testing.T) {
	service, factory, _, ids := openTargets(t, 1)
	result := service.SendChunks(ids, [][]byte{[]byte("one\r"), []byte("two\r")}, 0)
	if !result.Complete() || result.Bytes != len("one\rtwo\r") {
		t.Fatalf("result = %+v", result)
	}
	waitForInput(t, factory.transport("target-0"), "one\rtwo\r")
}

func TestSendChunksUntilStopsBeforeTheNextChunk(t *testing.T) {
	service, factory, _, ids := openTargets(t, 1)
	ctx, cancel := context.WithCancel(context.Background())
	results := make(chan SendResult, 1)
	go func() {
		results <- service.SendChunksUntil(ctx, ids,
			[][]byte{[]byte("one\r"), []byte("two\r")}, 200*time.Millisecond)
	}()
	waitForInput(t, factory.transport("target-0"), "one\r")
	cancel()
	select {
	case result := <-results:
		if result.Bytes != len("one\r") || result.DeliveredCount() != 1 {
			t.Fatalf("canceled result = %+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("SendChunksUntil did not stop after cancellation")
	}
	time.Sleep(250 * time.Millisecond)
	waitForInput(t, factory.transport("target-0"), "one\r")
}

func TestSendChunksUntilCanceledBeforeTheFirstChunkSendsNothing(t *testing.T) {
	service, factory, _, ids := openTargets(t, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := service.SendChunksUntil(ctx, ids,
		[][]byte{[]byte("one\r"), []byte("two\r")}, 0)
	if len(result.Deliveries) != 0 || result.Bytes != 0 {
		t.Fatalf("canceled result = %+v", result)
	}
	time.Sleep(20 * time.Millisecond)
	waitForInput(t, factory.transport("target-0"), "")
}
