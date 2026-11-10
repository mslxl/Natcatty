package app

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	appsession "github.com/mslxl/natcatty/application/session"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	domainpluginabi "github.com/mslxl/natcatty/domain/plugin/abi"
)

const (
	trapProviderID       = "acme.trapper"
	trapProviderProtocol = "x-acme-trapper"
	trapProviderFiber    = domainplugin.FiberID("fiber/acme.trapper")
)

// trapProviderInvoker answers the provider ABI in-process. Open succeeds and
// schedules the first pump; that pump blocks until the test has input in
// flight and then reports a fatal typed fault, the way a trapping guest does.
type trapProviderInvoker struct {
	release  <-chan struct{}
	pumped   chan struct{}
	pumpOnce sync.Once
}

func newTrapProviderInvoker(release <-chan struct{}) *trapProviderInvoker {
	return &trapProviderInvoker{release: release, pumped: make(chan struct{})}
}

func (i *trapProviderInvoker) SupportsProvider() bool { return true }

func (i *trapProviderInvoker) InvokeProvider(ctx context.Context,
	request appplugin.ProviderRequest) ([]byte, error) {

	if request.Method != domainpluginabi.ProviderMethodPump {
		return []byte(`{"status":"ok"}`), nil
	}
	i.pumpOnce.Do(func() { close(i.pumped) })
	select {
	case <-i.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return nil, &domainplugin.ProviderError{
		Kind:     domainplugin.ProviderErrorFaulted,
		Provider: trapProviderID, Protocol: trapProviderProtocol,
		Message: "provider trapped",
	}
}

// TestConnectionProviderTrapDuringInputKeepsTypedProviderFailure stresses the
// provider end race: a provider traps on the pump scheduled by Open while the
// Session is writing input. The read and write loops both observe the failure,
// and whichever reports first must surface the typed provider status instead
// of a bare transport error. Iterations matter here: the original defect only
// appeared when the write path won the race.
func TestConnectionProviderTrapDuringInputKeepsTypedProviderFailure(t *testing.T) {
	const iterations = 60
	for iteration := 0; iteration < iterations; iteration++ {
		t.Run(fmt.Sprintf("run-%02d", iteration), func(t *testing.T) {
			host := appplugin.NewProviderHost(appplugin.ProviderHostOptions{
				// A small input queue makes the write loop reach its blocking
				// write quickly, so the trap lands while a write is in flight.
				Limits: appplugin.ProviderLimits{QueueBytes: 2048},
			})
			t.Cleanup(host.Close)

			release := make(chan struct{})
			var releaseOnce sync.Once
			releaseTrap := func() { releaseOnce.Do(func() { close(release) }) }
			t.Cleanup(releaseTrap)

			invoker := newTrapProviderInvoker(release)
			ref := appplugin.PluginRef{ID: trapProviderID}
			contribution := domainplugin.ConnectionProvider{
				Protocol:     trapProviderProtocol,
				Name:         "Acme Trapper",
				Capabilities: domainplugin.ConnectionCapabilities{MaxSessions: 1},
			}
			provider, err := host.Register(trapProviderFiber, ref, contribution, invoker)
			if err != nil {
				t.Fatalf("Register: %v", err)
			}
			registry := appsession.NewProviderRegistry()
			registrar := &connectionProviderRegistrar{registry: registry}
			if _, err := registrar.RegisterConnectionProvider(ref, contribution, provider); err != nil {
				t.Fatalf("RegisterConnectionProvider: %v", err)
			}
			factory := newTransportFactory(&factoryLocal{}, nil, nil, nil, nil).
				withProviders(registry)
			service := appsession.NewService(factory)
			t.Cleanup(service.CloseAll)

			session, err := service.Open(context.Background(), appsession.StartSpec{
				Kind:     appsession.KindPlugin,
				Protocol: trapProviderProtocol,
				Provider: trapProviderID,
				Cols:     80,
				Rows:     24,
			})
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			t.Cleanup(func() { _ = session.Close() })

			// Wait until Open's scheduled pump is blocked in the guest, then
			// put more input in flight than the bounded stream queue holds.
			select {
			case <-invoker.pumped:
			case <-time.After(5 * time.Second):
				t.Fatal("the provider pump was never scheduled")
			}
			input := bytes.Repeat([]byte{'x'}, 16<<10)
			if _, err := session.TerminalConn().Write(input); err != nil {
				t.Fatalf("TerminalConn().Write: %v", err)
			}
			// Let the write loop fill the 2 KiB provider queue and block in
			// transport.Write before the trap wakes it with an error.
			time.Sleep(10 * time.Millisecond)
			releaseTrap()

			waitForApp(t, "the trapped Session to end", func() bool {
				select {
				case <-session.Done():
					return true
				default:
					return false
				}
			})
			snapshot := session.Snapshot()
			if snapshot.Status != appsession.StatusExited ||
				snapshot.EndReason != appsession.EndReasonError {
				t.Fatalf("snapshot status/reason = %q/%q, want exited/error",
					snapshot.Status, snapshot.EndReason)
			}
			if snapshot.ProviderFailure == nil ||
				snapshot.ProviderFailure.Status != appsession.ProviderStatusFaulted ||
				snapshot.ProviderFailure.Provider != trapProviderID ||
				snapshot.ProviderFailure.Protocol != trapProviderProtocol {
				t.Fatalf("ProviderFailure = %+v, want provider-faulted for %s/%s",
					snapshot.ProviderFailure, trapProviderID, trapProviderProtocol)
			}
		})
	}
}
