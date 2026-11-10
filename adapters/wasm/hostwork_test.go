package wasm_test

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mslxl/natcatty/adapters/wasm"
	appplugin "github.com/mslxl/natcatty/application/plugin"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// hostWorkLimits use the production compute deadline so the tests prove host
// work longer than 2 s succeeds. The fixtures are tiny, so a real 2.2 s wait is
// the cheapest honest way to cross the boundary.
var hostWorkLimits = appplugin.Limits{
	InitTimeout:   2 * time.Second,
	CallTimeout:   2 * time.Second,
	UnloadTimeout: 2 * time.Second,
	WatchdogGrace: 100 * time.Millisecond,
}

const hostWorkCommand = "plugin.acme.slow.run"

func slowServiceFixture(t *testing.T) appplugin.ModuleSpec {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "slow-service.wasm"))
	if err != nil {
		t.Fatalf("read slow-service fixture: %v", err)
	}
	return appplugin.ModuleSpec{
		Name:           "acme.slow",
		WASM:           data,
		MemoryMaxBytes: 64 << 20,
	}
}

// blockingDispatcher holds the first host-service call until the test releases
// it, ignoring context cancellation the way a host confirmation UI may.
type blockingDispatcher struct {
	entered chan struct{}
	release chan struct{}

	mu    sync.Mutex
	calls int
}

func (d *blockingDispatcher) Call(_ context.Context, _ appplugin.ServiceCall) []byte {
	d.mu.Lock()
	d.calls++
	first := d.calls == 1
	d.mu.Unlock()
	if first {
		close(d.entered)
		<-d.release
	}
	return []byte(`{"code":"ok"}`)
}

func TestHostServiceWorkOutlivesTheGuestComputeBudget(t *testing.T) {
	rt := wasm.NewRuntime(wasm.WithLimits(hostWorkLimits))
	t.Cleanup(func() { _ = rt.Close(context.Background()) })
	dispatcher := &blockingDispatcher{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	var outputs []string
	instance, err := rt.Instantiate(context.Background(), slowServiceFixture(t),
		appplugin.HostSink{
			Services: dispatcher,
			Output:   func(data []byte) { outputs = append(outputs, string(data)) },
		})
	if err != nil {
		t.Fatalf("Instantiate: %v", err)
	}
	defer func() { _ = instance.Close(context.Background()) }()
	if err := instance.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- instance.Dispatch(context.Background(), hostWorkCommand)
	}()
	select {
	case <-dispatcher.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the service import did not reach the host")
	}
	// One in-flight call per instance still holds while host work runs.
	if err := instance.Dispatch(context.Background(), hostWorkCommand); !errors.Is(err, appplugin.ErrCallInFlight) {
		t.Fatalf("concurrent dispatch error = %v, want ErrCallInFlight", err)
	}

	// Hold the host import past the 2 s guest compute deadline, then release
	// it. The call must still complete and the guest must run to the end.
	time.Sleep(hostWorkLimits.CallTimeout + 200*time.Millisecond)
	close(dispatcher.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("dispatch across host work: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("dispatch did not return after host work completed")
	}
	if len(outputs) != 1 || outputs[0] != "slow ok" {
		t.Fatalf("visible output = %v, want slow ok", outputs)
	}

	// The budget is re-armed for the next call after host work.
	if err := instance.Dispatch(context.Background(), hostWorkCommand); err != nil {
		t.Fatalf("dispatch after host work: %v", err)
	}
}

// methodDispatcher runs a real host-service method for the guest's service
// call, so the adapter test exercises the application path end to end.
type methodDispatcher struct {
	run func(ctx context.Context) error

	mu  sync.Mutex
	err error
}

func (d *methodDispatcher) Call(ctx context.Context, _ appplugin.ServiceCall) []byte {
	err := d.run(ctx)
	d.mu.Lock()
	d.err = err
	d.mu.Unlock()
	return []byte(`{"code":"ok"}`)
}

func (d *methodDispatcher) dispatchError() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.err
}

func hostWorkInstance(t *testing.T,
	dispatcher appplugin.ServiceDispatcher) appplugin.Instance {
	t.Helper()
	rt := wasm.NewRuntime(wasm.WithLimits(hostWorkLimits))
	t.Cleanup(func() { _ = rt.Close(context.Background()) })
	instance, err := rt.Instantiate(context.Background(), slowServiceFixture(t),
		appplugin.HostSink{Services: dispatcher})
	if err != nil {
		t.Fatalf("Instantiate: %v", err)
	}
	t.Cleanup(func() { _ = instance.Close(context.Background()) })
	return instance
}

func slowSSHMaterial(fill byte) []byte {
	const keyType = "ssh-hostwork"
	blob := make([]byte, 4+len(keyType)+16)
	binary.BigEndian.PutUint32(blob, uint32(len(keyType)))
	copy(blob[4:], keyType)
	for i := 4 + len(keyType); i < len(blob); i++ {
		blob[i] = fill
	}
	return []byte(base64.StdEncoding.EncodeToString(blob))
}

func TestTrustConfirmationOutlivesTheGuestComputeBudget(t *testing.T) {
	authority := appplugin.NewAuthority()
	pins := &appplugin.MemoryTrustPinStore{}
	confirmer := &appplugin.MemoryTrustConfirmer{}
	entered := make(chan struct{})
	release := make(chan struct{})
	confirmer.ConfirmFn = func(appplugin.TrustRequest) (bool, error) {
		close(entered)
		<-release
		return true, nil
	}
	host := appplugin.NewHostServices(appplugin.ServiceDeps{
		Authority:      authority,
		TrustPins:      pins,
		TrustConfirmer: confirmer,
	})
	t.Cleanup(host.CloseAll)
	ref := appplugin.PluginRef{
		ID: "acme.slow", Name: "Acme Slow", Version: "1.0.0",
		Source: domainplugin.SourceLocalFile,
	}
	authority.Grant(appplugin.SubjectForRef(ref), domainplugin.PermissionGrant{
		Permission: domainplugin.PermissionTrustVerify,
		Scope:      domainplugin.Scope{Kind: domainplugin.ScopeHost, Host: "db"},
	})
	session := host.Open(ref, domainplugin.NewFiberID("installed", ref.ID, 1), nil)
	if session == nil {
		t.Fatal("HostServices.Open returned nil")
	}
	dispatcher := &methodDispatcher{run: func(ctx context.Context) error {
		_, err := session.TrustVerify(ctx, "db", appplugin.TrustKindSSHHostKey, slowSSHMaterial(0x71))
		return err
	}}
	instance := hostWorkInstance(t, dispatcher)
	session.BindHandles(instance.Handles())
	if err := instance.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- instance.Dispatch(context.Background(), hostWorkCommand) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the trust confirmation did not reach the host")
	}
	time.Sleep(hostWorkLimits.CallTimeout + 200*time.Millisecond)
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("dispatch across trust confirmation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("dispatch did not return after the confirmation")
	}
	if err := dispatcher.dispatchError(); err != nil {
		t.Fatalf("trust verification across the compute deadline: %v", err)
	}
	if records := pins.Records(); len(records) != 1 {
		t.Fatalf("trust pins = %d, want the confirmed pin", len(records))
	}
}

// stubSocket is a minimal ExternalSocket for the delayed dial test.
type stubSocket struct {
	closed bool
}

func (s *stubSocket) Read([]byte) (int, error)       { return 0, io.EOF }
func (s *stubSocket) Write(data []byte) (int, error) { return len(data), nil }
func (s *stubSocket) SetDeadline(time.Time) error    { return nil }
func (s *stubSocket) CloseWrite() error              { return nil }
func (s *stubSocket) CloseRead() error               { return nil }
func (s *stubSocket) Close() error                   { s.closed = true; return nil }

func TestNetworkDialOutlivesTheGuestComputeBudget(t *testing.T) {
	authority := appplugin.NewAuthority()
	broker := &appplugin.MemoryNetworkBroker{Addresses: map[string][]netip.Addr{}}
	entered := make(chan struct{})
	release := make(chan struct{})
	broker.DialFunc = func(appplugin.NetworkDialTarget) (appplugin.ExternalSocket, error) {
		close(entered)
		<-release
		return &stubSocket{}, nil
	}
	host := appplugin.NewHostServices(appplugin.ServiceDeps{
		Authority: authority,
		Network:   broker,
	})
	t.Cleanup(host.CloseAll)
	ref := appplugin.PluginRef{
		ID: "acme.slow", Name: "Acme Slow", Version: "1.0.0",
		Source: domainplugin.SourceLocalFile,
	}
	authority.Grant(appplugin.SubjectForRef(ref), domainplugin.PermissionGrant{
		Permission: domainplugin.PermissionNetworkDial,
		Scope: domainplugin.Scope{
			Kind: domainplugin.ScopeNetwork, Host: "10.0.0.7",
			Ports: []int{5432}, Protocol: domainplugin.NetworkProtocolTCP,
		},
	})
	session := host.Open(ref, domainplugin.NewFiberID("installed", ref.ID, 1), nil)
	if session == nil {
		t.Fatal("HostServices.Open returned nil")
	}
	dispatcher := &methodDispatcher{run: func(ctx context.Context) error {
		_, err := session.NetworkDial(ctx, appplugin.NetworkDialSpec{
			Host: "10.0.0.7", Port: 5432, Protocol: domainplugin.NetworkProtocolTCP,
		})
		return err
	}}
	instance := hostWorkInstance(t, dispatcher)
	session.BindHandles(instance.Handles())
	if err := instance.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- instance.Dispatch(context.Background(), hostWorkCommand) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the dial did not reach the host transport")
	}
	time.Sleep(hostWorkLimits.CallTimeout + 200*time.Millisecond)
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("dispatch across network dial: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("dispatch did not return after the dial")
	}
	if err := dispatcher.dispatchError(); err != nil {
		t.Fatalf("dial across the compute deadline: %v", err)
	}
	if dials := broker.Dials(); len(dials) != 1 {
		t.Fatalf("dial attempts = %d, want 1", len(dials))
	}
}

func TestGuestComputeStillBoundedAfterHostWork(t *testing.T) {
	rt := wasm.NewRuntime(wasm.WithLimits(hostWorkLimits))
	t.Cleanup(func() { _ = rt.Close(context.Background()) })
	dispatcher := &blockingDispatcher{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	close(dispatcher.release)
	instance, err := rt.Instantiate(context.Background(), slowServiceFixture(t),
		appplugin.HostSink{Services: dispatcher})
	if err != nil {
		t.Fatalf("Instantiate: %v", err)
	}
	defer func() { _ = instance.Close(context.Background()) }()
	if err := instance.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}

	err = instance.Dispatch(context.Background(), hostWorkCommand+"-loop")
	var deadline *appplugin.DeadlineError
	if !errors.As(err, &deadline) {
		t.Fatalf("dispatch error = %v, want DeadlineError", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("dispatch error = %v, want context.DeadlineExceeded", err)
	}
}
