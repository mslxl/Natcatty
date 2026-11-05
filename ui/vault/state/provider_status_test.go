package state

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	appconnection "github.com/mslxl/natcatty/application/connection"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

func TestNetworkProviderStatusIsQueuedUntilFlushed(t *testing.T) {
	var redraws atomic.Int32
	state := NewState(nil, WithRedrawRequest(func() { redraws.Add(1) }))

	ips := []string{"100.64.0.2"}
	state.PostNetworkProviderStatus(NetworkProviderStatus{
		ProfileID:    "net-1",
		Label:        "Office",
		Kind:         "tailscale",
		Phase:        appconnection.ProviderPhaseStarting,
		TailscaleIPs: ips,
	})
	if _, ok := state.NetworkStatus("net-1"); ok {
		t.Fatal("a queued status was visible before the UI thread flushed it")
	}
	if redraws.Load() == 0 {
		t.Fatal("posting a status did not request a redraw")
	}

	// The queue owns a copy: mutating the caller's slice after posting must not
	// change the published status.
	ips[0] = "changed"
	state.FlushNetworkProviderStatus()
	status, ok := state.NetworkStatus("net-1")
	if !ok {
		t.Fatal("the flushed status is not available")
	}
	if status.Phase != appconnection.ProviderPhaseStarting {
		t.Fatalf("phase = %q, want starting", status.Phase)
	}
	if len(status.TailscaleIPs) != 1 || status.TailscaleIPs[0] != "100.64.0.2" {
		t.Fatalf("addresses = %v, want the posted copy", status.TailscaleIPs)
	}

	// A later event for the same profile replaces the row.
	state.PostNetworkProviderStatus(NetworkProviderStatus{
		ProfileID: "net-1",
		Label:     "Office",
		Kind:      "tailscale",
		Phase:     appconnection.ProviderPhaseRunning,
	})
	state.FlushNetworkProviderStatus()
	status, ok = state.NetworkStatus("net-1")
	if !ok || status.Phase != appconnection.ProviderPhaseRunning {
		t.Fatalf("phase = %q, want the latest running status", status.Phase)
	}
}

func TestNetworkProviderStatusAddressPrefersDNSThenFirstIP(t *testing.T) {
	tests := []struct {
		name   string
		status NetworkProviderStatus
		want   string
	}{
		{"dns name", NetworkProviderStatus{
			DNSName: "office.tailnet.ts.net", TailscaleIPs: []string{"100.64.0.2"},
		}, "office.tailnet.ts.net"},
		{"first ip", NetworkProviderStatus{
			TailscaleIPs: []string{"100.64.0.2", "fd7a::2"},
		}, "100.64.0.2"},
		{"wireguard endpoint", NetworkProviderStatus{
			WireGuardEndpoint: "203.0.113.7:51820",
		}, "203.0.113.7:51820"},
		{"dns name beats wireguard endpoint", NetworkProviderStatus{
			DNSName: "office.tailnet.ts.net", WireGuardEndpoint: "203.0.113.7:51820",
		}, "office.tailnet.ts.net"},
		{"none", NetworkProviderStatus{}, ""},
	}
	for _, test := range tests {
		if got := test.status.Address(); got != test.want {
			t.Errorf("%s Address = %q, want %q", test.name, got, test.want)
		}
	}
}

func TestNetworkProviderStatusIgnoresAnEmptyProfile(t *testing.T) {
	state := NewState(nil)
	state.PostNetworkProviderStatus(NetworkProviderStatus{Phase: appconnection.ProviderPhaseRunning})
	state.FlushNetworkProviderStatus()
	if len(state.networkStatuses) != 0 {
		t.Fatalf("statuses = %+v, want none without a profile id", state.networkStatuses)
	}
}

// fakeProviderActions records the interactive-login lifecycle calls the Vault
// surface makes. A test can hold a start open to prove the call runs off the UI
// thread and script a failure.
type fakeProviderActions struct {
	mu       sync.Mutex
	starts   []string
	cancels  []string
	retries  []string
	startFn  func(context.Context, string) error
	cancelFn func(context.Context, string) error
	retryFn  func(context.Context, string) error
}

func (f *fakeProviderActions) record(action, profileID string) {
	f.mu.Lock()
	switch action {
	case "start":
		f.starts = append(f.starts, profileID)
	case "cancel":
		f.cancels = append(f.cancels, profileID)
	case "retry":
		f.retries = append(f.retries, profileID)
	}
	f.mu.Unlock()
}

func (f *fakeProviderActions) StartNetworkProvider(ctx context.Context, profileID string) error {
	f.record("start", profileID)
	f.mu.Lock()
	fn := f.startFn
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, profileID)
	}
	return nil
}

func (f *fakeProviderActions) CancelNetworkProvider(ctx context.Context, profileID string) error {
	f.record("cancel", profileID)
	f.mu.Lock()
	fn := f.cancelFn
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, profileID)
	}
	return nil
}

func (f *fakeProviderActions) RetryNetworkProvider(ctx context.Context, profileID string) error {
	f.record("retry", profileID)
	f.mu.Lock()
	fn := f.retryFn
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, profileID)
	}
	return nil
}

func (f *fakeProviderActions) calls() (starts, cancels, retries []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.starts...),
		append([]string(nil), f.cancels...),
		append([]string(nil), f.retries...)
}

func newProviderActionState(t *testing.T, opts ...Option) (*State, <-chan struct{}) {
	t.Helper()
	profiles := &fakeNetworkProfileService{items: []domainvault.NetworkProfile{{
		ID: "net-1", Label: "Office", Kind: domainvault.NetworkKindTailscale,
	}}}
	redraws := make(chan struct{}, 32)
	all := append([]Option{
		WithNetworkProfiles(profiles),
		WithRedrawRequest(func() {
			select {
			case redraws <- struct{}{}:
			default:
			}
		}),
	}, opts...)
	state := NewState(&fakeHosts{}, all...)
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	for {
		select {
		case <-redraws:
		default:
			return state, redraws
		}
	}
}

func waitForActionSignal(t *testing.T, signal <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func TestNetworkProviderLoginRunsOffTheUIThreadAndPostsFailures(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	actions := &fakeProviderActions{startFn: func(context.Context, string) error {
		close(started)
		<-release
		close(finished)
		return errors.New("tailscale: control server refused the node")
	}}
	state, redraws := newProviderActionState(t, WithNetworkProviderActions(actions))

	if !state.StartNetworkProviderLogin("net-1") {
		t.Fatal("StartNetworkProviderLogin rejected a listed profile")
	}
	waitForActionSignal(t, started, "the blocking start action")
	// The UI call returned while the action is still running, so the failure
	// status cannot be visible yet.
	state.FlushNetworkProviderStatus()
	if _, ok := state.NetworkStatus("net-1"); ok {
		t.Fatal("the login action blocked the UI thread or published a premature status")
	}

	close(release)
	waitForActionSignal(t, finished, "the login action to finish")
	waitForActionSignal(t, redraws, "the failure status to request a redraw")
	state.FlushNetworkProviderStatus()
	status, ok := state.NetworkStatus("net-1")
	if !ok {
		t.Fatal("a failed login action did not publish an error status")
	}
	if status.Phase != appconnection.ProviderPhaseError ||
		!strings.Contains(status.LastError, "control server refused the node") {
		t.Fatalf("failure status = %+v, want phase error with the cause", status)
	}
	starts, _, _ := actions.calls()
	if len(starts) != 1 || starts[0] != "net-1" {
		t.Fatalf("start calls = %v, want [net-1]", starts)
	}
}

func TestNetworkProviderCancelAndRetryDelegateToTheActions(t *testing.T) {
	actions := &fakeProviderActions{}
	state, _ := newProviderActionState(t, WithNetworkProviderActions(actions))

	if !state.CancelNetworkProviderLogin("net-1") {
		t.Fatal("CancelNetworkProviderLogin rejected a listed profile")
	}
	if !state.RetryNetworkProvider("net-1") {
		t.Fatal("RetryNetworkProvider rejected a listed profile")
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		_, cancels, retries := actions.calls()
		if len(cancels) == 1 && len(retries) == 1 {
			if cancels[0] != "net-1" || retries[0] != "net-1" {
				t.Fatalf("cancel/retry calls = %v/%v, want [net-1]", cancels, retries)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("cancel/retry calls = %v/%v, want one each", cancels, retries)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestNetworkProviderLoginActionsFailClosedWithoutAProfileOrSeam(t *testing.T) {
	actions := &fakeProviderActions{}
	state, _ := newProviderActionState(t, WithNetworkProviderActions(actions))
	if state.StartNetworkProviderLogin("net-missing") {
		t.Fatal("the login action accepted an unknown profile")
	}
	if state.problem != ProblemNotFound {
		t.Fatalf("problem = %v, want not found", state.problem)
	}
	if starts, _, _ := actions.calls(); len(starts) != 0 {
		t.Fatalf("start calls = %v, want none for an unknown profile", starts)
	}

	unwired, _ := newProviderActionState(t)
	if unwired.StartNetworkProviderLogin("net-1") {
		t.Fatal("the login action ran without a wired seam")
	}
	if unwired.problem != ProblemInternal {
		t.Fatalf("problem = %v, want internal", unwired.problem)
	}
}
