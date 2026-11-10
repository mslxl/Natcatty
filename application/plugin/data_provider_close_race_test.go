package plugin_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	domainpluginabi "github.com/mslxl/natcatty/domain/plugin/abi"
)

// dataCloseRecorder counts guest Close calls per connection token.
type dataCloseRecorder struct {
	mu     sync.Mutex
	counts map[uint64]int
}

func newDataCloseRecorder() *dataCloseRecorder {
	return &dataCloseRecorder{counts: make(map[uint64]int)}
}

func (r *dataCloseRecorder) handle(request appplugin.DataProviderRequest) ([]byte, error) {
	if request.Method != domainpluginabi.DataProviderMethodClose {
		return dataOK(nil), nil
	}
	var payload struct {
		Connection uint64 `json:"connection"`
	}
	_ = json.Unmarshal(request.Payload, &payload)
	r.mu.Lock()
	r.counts[payload.Connection]++
	r.mu.Unlock()
	return dataOK(nil), nil
}

func (r *dataCloseRecorder) count(token uint64) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.counts[token]
}

func (r *dataCloseRecorder) total() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	total := 0
	for _, count := range r.counts {
		total += count
	}
	return total
}

// dataTestSecondProvider registers a second provider on the environment's
// fiber. Both registrations share one data-provider instance, so a teardown of
// one can mark the instance inactive while the other still owns connections.
func dataTestSecondProvider(t *testing.T, env *dataEnv,
	id string) *appplugin.DataProvider {

	t.Helper()
	contribution := dataTestContribution()
	contribution.ID = id
	provider, err := env.host.Register(dataTestFiber,
		appplugin.PluginRef{ID: "acme.data"}, contribution,
		domainplugin.DataModeReadOnly, env.server)
	if err != nil {
		t.Fatalf("register second provider: %v", err)
	}
	return provider
}

func dataTestCloseAuditRows(env *dataEnv) []string {
	var reasons []string
	for _, event := range env.audit.snapshot() {
		if event.Event == domainplugin.AuditData &&
			strings.HasPrefix(event.Reason, "data.close:") {
			reasons = append(reasons, event.Reason)
		}
	}
	return reasons
}

func dataTestProfileRecord(id, provider string) domainplugin.DataProfile {
	return domainplugin.DataProfile{
		ID: id, Provider: provider, Label: id,
		Mode: domainplugin.DataModeReadOnly,
	}
}

// A fatal guest call marks the shared instance inactive while the provider
// registration and its live connection remain. Every teardown path must still
// reach the guest exactly once and write exactly one close audit row; the
// guest close may not depend on the active flag.
func TestDataProviderTeardownClosesInactiveInstanceOnce(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name       string
		teardown   func(t *testing.T, env *dataEnv, lease *appplugin.DataLease)
		wantReason string
	}{
		{
			name: "unregister",
			teardown: func(t *testing.T, env *dataEnv, _ *appplugin.DataLease) {
				if err := env.host.Unregister(env.provider); err != nil {
					t.Fatalf("unregister: %v", err)
				}
			},
			wantReason: "data.close:unloaded",
		},
		{
			name: "lease close",
			teardown: func(t *testing.T, _ *dataEnv, lease *appplugin.DataLease) {
				if err := lease.Close(ctx); err != nil {
					t.Fatalf("lease close: %v", err)
				}
			},
			wantReason: "data.close:ok",
		},
		{
			name: "idle expiry",
			teardown: func(t *testing.T, env *dataEnv, lease *appplugin.DataLease) {
				lease.Release()
				env.schedule.Fire()
			},
			wantReason: "data.close:idle",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newDataEnv(t, domainplugin.DataModeReadOnly, appplugin.DataLimits{})
			env.saveProfile(dataTestDraft())
			_ = dataTestSecondProvider(t, env, "warehouse-b")
			lease := env.acquire(domainplugin.DataModeReadOnly)
			token := lease.ConnectionID()

			recorder := newDataCloseRecorder()
			env.server.handler = func(request appplugin.DataProviderRequest,
				_ context.Context) ([]byte, error) {

				if request.Method == domainpluginabi.DataProviderMethodPing {
					return nil, appplugin.ErrClosed
				}
				return recorder.handle(request)
			}
			// The fatal ping marks the shared instance inactive; the
			// registration and connection stay live.
			err := lease.Ping(ctx)
			if err == nil {
				t.Fatal("the fatal ping succeeded")
			}
			wantDataKind(t, err, appplugin.DataErrorFaulted)

			tc.teardown(t, env, lease)

			if got := recorder.count(token); got != 1 {
				t.Fatalf("guest close calls = %d, want 1", got)
			}
			if got := recorder.total(); got != 1 {
				t.Fatalf("total guest close calls = %d, want 1", got)
			}
			reasons := dataTestCloseAuditRows(env)
			if len(reasons) != 1 || reasons[0] != tc.wantReason {
				t.Fatalf("close audit rows = %v, want [%s]", reasons, tc.wantReason)
			}
			if count := env.host.DataConnectionCount(env.profile.ID); count != 0 {
				t.Fatalf("connections after teardown = %d, want 0", count)
			}
		})
	}
}

// Unregister of one provider can race host.Close on a shared multi-provider
// fiber. Whichever path claims the connection must still attempt the guest
// close through the forced path, giving exactly one close and one close audit
// row for the token even when the shared instance was marked inactive first.
func TestDataProviderUnregisterRacingHostCloseClosesExactlyOnce(t *testing.T) {
	const iterations = 200
	for iteration := 0; iteration < iterations; iteration++ {
		func() {
			env := newDataEnv(t, domainplugin.DataModeReadOnly, appplugin.DataLimits{})
			env.saveProfile(dataTestDraft())
			_ = dataTestSecondProvider(t, env, "warehouse-b")
			lease := env.acquire(domainplugin.DataModeReadOnly)
			token := lease.ConnectionID()

			recorder := newDataCloseRecorder()
			env.server.handler = func(request appplugin.DataProviderRequest,
				_ context.Context) ([]byte, error) {

				return recorder.handle(request)
			}

			start := make(chan struct{})
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				<-start
				_ = env.host.Unregister(env.provider)
			}()
			go func() {
				defer wg.Done()
				<-start
				env.host.Close()
			}()
			close(start)
			wg.Wait()

			if got := recorder.count(token); got != 1 {
				t.Fatalf("iteration %d: guest close calls = %d, want 1", iteration, got)
			}
			if got := recorder.total(); got != 1 {
				t.Fatalf("iteration %d: total guest close calls = %d, want 1",
					iteration, got)
			}
			if reasons := dataTestCloseAuditRows(env); len(reasons) != 1 {
				t.Fatalf("iteration %d: close audit rows = %v, want 1", iteration, reasons)
			}
			if count := env.host.DataConnectionCount(env.profile.ID); count != 0 {
				t.Fatalf("iteration %d: connections = %d, want 0", iteration, count)
			}
		}()
	}
}

// Two concurrent Unregisters on one shared fiber each close their own tokens
// exactly once, even though one of them may mark the instance inactive before
// the other reaches its guest close.
func TestDataProviderConcurrentUnregisterClosesEachTokenOnce(t *testing.T) {
	ctx := context.Background()
	env := newDataEnv(t, domainplugin.DataModeReadOnly, appplugin.DataLimits{})
	second := dataTestSecondProvider(t, env, "warehouse-b")

	for _, profile := range []domainplugin.DataProfile{
		dataTestProfileRecord("profile-a", env.provider.ID()),
		dataTestProfileRecord("profile-b", second.ID()),
	} {
		if err := env.profiles.Save(ctx, profile); err != nil {
			t.Fatalf("save profile %q: %v", profile.ID, err)
		}
	}
	first, err := env.host.Acquire(ctx, "profile-a", domainplugin.DataModeReadOnly)
	if err != nil {
		t.Fatalf("acquire first: %v", err)
	}
	other, err := env.host.Acquire(ctx, "profile-b", domainplugin.DataModeReadOnly)
	if err != nil {
		t.Fatalf("acquire second: %v", err)
	}
	tokens := []uint64{first.ConnectionID(), other.ConnectionID()}

	recorder := newDataCloseRecorder()
	env.server.handler = func(request appplugin.DataProviderRequest,
		_ context.Context) ([]byte, error) {

		return recorder.handle(request)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_ = env.host.Unregister(env.provider)
	}()
	go func() {
		defer wg.Done()
		<-start
		_ = env.host.Unregister(second)
	}()
	close(start)
	wg.Wait()

	for _, token := range tokens {
		if got := recorder.count(token); got != 1 {
			t.Fatalf("token %d: guest close calls = %d, want 1", token, got)
		}
	}
	if got := recorder.total(); got != len(tokens) {
		t.Fatalf("total guest close calls = %d, want %d", got, len(tokens))
	}
	if reasons := dataTestCloseAuditRows(env); len(reasons) != len(tokens) {
		t.Fatalf("close audit rows = %v, want %d", reasons, len(tokens))
	}
	for _, profile := range []string{"profile-a", "profile-b"} {
		if count := env.host.DataConnectionCount(profile); count != 0 {
			t.Fatalf("%s connections after unregister = %d, want 0", profile, count)
		}
	}
}
