package connection

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeNetworkProvider is the single dial port's test double. It records the
// network and address it was asked to dial and can script a result.
type fakeNetworkProvider struct {
	mu        sync.Mutex
	networks  []string
	addresses []string
	conn      net.Conn
	err       error
	closes    int
	phase     ProviderPhase
}

func (p *fakeNetworkProvider) DialContext(_ context.Context, network, address string) (net.Conn, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.networks = append(p.networks, network)
	p.addresses = append(p.addresses, address)
	return p.conn, p.err
}

func (p *fakeNetworkProvider) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closes++
	return nil
}

func (p *fakeNetworkProvider) dials() (networks, addresses []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.networks...), append([]string(nil), p.addresses...)
}

func (p *fakeNetworkProvider) closeCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closes
}

// Phase implements the optional lifecycle reporter. The zero value means
// unknown, so providers created by the shared fake keep working.
func (p *fakeNetworkProvider) Phase() ProviderPhase {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.phase
}

func (p *fakeNetworkProvider) setPhase(phase ProviderPhase) {
	p.mu.Lock()
	p.phase = phase
	p.mu.Unlock()
}

// fakeProviderFactory opens a fresh fake provider per call unless openFn
// scripts a different outcome.
type fakeProviderFactory struct {
	mu        sync.Mutex
	opens     int
	routes    []NetworkRoute
	providers []*fakeNetworkProvider
	err       error
	openFn    func(context.Context, NetworkRoute) (NetworkProvider, error)
}

func (f *fakeProviderFactory) Open(ctx context.Context, route NetworkRoute) (NetworkProvider, error) {
	f.mu.Lock()
	f.opens++
	f.routes = append(f.routes, route)
	fn, err := f.openFn, f.err
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, route)
	}
	if err != nil {
		return nil, err
	}
	provider := &fakeNetworkProvider{}
	f.mu.Lock()
	f.providers = append(f.providers, provider)
	f.mu.Unlock()
	return provider, nil
}

func (f *fakeProviderFactory) openCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.opens
}

func (f *fakeProviderFactory) openedProviders() []*fakeNetworkProvider {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*fakeNetworkProvider(nil), f.providers...)
}

func testNetworkRoute(profileID, fingerprint string) NetworkRoute {
	return NetworkRoute{
		ProfileID:   profileID,
		Kind:        "tailscale",
		Label:       "Profile " + profileID,
		Fingerprint: fingerprint,
	}
}

func typedConnectionError(t *testing.T, err error) *Error {
	t.Helper()
	var typed *Error
	if !errors.As(err, &typed) {
		t.Fatalf("error %v is not a typed connection error", err)
	}
	return typed
}

func TestNetworkProviderRegistryOpensLazilyAndReusesOneProviderPerProfile(t *testing.T) {
	factory := &fakeProviderFactory{}
	registry := NewNetworkProviderRegistry(factory)
	ctx := context.Background()
	firstRoute := testNetworkRoute("profile-a", "fp-1")

	if got := factory.openCount(); got != 0 {
		t.Fatalf("factory opened before the first Acquire: %d", got)
	}
	first, err := registry.Acquire(ctx, firstRoute)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if got := factory.openCount(); got != 1 {
		t.Fatalf("open count = %d, want 1", got)
	}
	again, err := registry.Acquire(ctx, firstRoute)
	if err != nil {
		t.Fatalf("second Acquire: %v", err)
	}
	if first != again {
		t.Fatal("a second Acquire opened a different provider for the same profile")
	}
	if got := factory.openCount(); got != 1 {
		t.Fatalf("open count after reuse = %d, want 1", got)
	}

	other, err := registry.Acquire(ctx, testNetworkRoute("profile-b", "fp-1"))
	if err != nil {
		t.Fatalf("Acquire profile-b: %v", err)
	}
	if other == first {
		t.Fatal("two profiles shared one provider")
	}
	if got := factory.openCount(); got != 2 {
		t.Fatalf("open count = %d, want one per profile", got)
	}

	if err := registry.Close("profile-a"); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := first.(*fakeNetworkProvider).closeCount(); got != 1 {
		t.Fatalf("closed providers after Close = %d, want 1", got)
	}
	if err := registry.Close("profile-a"); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if got := first.(*fakeNetworkProvider).closeCount(); got != 1 {
		t.Fatalf("Close is not idempotent: close count = %d", got)
	}

	reopened, err := registry.Acquire(ctx, firstRoute)
	if err != nil {
		t.Fatalf("Acquire after Close: %v", err)
	}
	if reopened == first {
		t.Fatal("Acquire after Close returned the closed provider")
	}
	if got := factory.openCount(); got != 3 {
		t.Fatalf("open count after reopen = %d, want 3", got)
	}

	if err := registry.CloseAll(); err != nil {
		t.Fatalf("CloseAll: %v", err)
	}
	if got := other.(*fakeNetworkProvider).closeCount(); got != 1 {
		t.Fatalf("profile-b close count = %d, want 1", got)
	}
	if err := registry.CloseAll(); err != nil {
		t.Fatalf("second CloseAll: %v", err)
	}
	_, err = registry.Acquire(ctx, firstRoute)
	typed := typedConnectionError(t, err)
	if typed.Kind != KindProvider || !errors.Is(err, ErrNetworkProviderClosed) {
		t.Fatalf("Acquire after CloseAll = %v, want a closed KindProvider error", err)
	}
}

func TestNetworkProviderRegistrySharesOneConcurrentOpenAttempt(t *testing.T) {
	provider := &fakeNetworkProvider{}
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	factory := &fakeProviderFactory{openFn: func(context.Context, NetworkRoute) (NetworkProvider, error) {
		once.Do(func() { close(started) })
		<-release
		return provider, nil
	}}
	registry := NewNetworkProviderRegistry(factory)
	route := testNetworkRoute("profile-a", "fp-1")

	const workers = 8
	results := make([]NetworkProvider, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			results[index], errs[index] = registry.Acquire(context.Background(), route)
		}(i)
	}
	<-started
	close(release)
	wg.Wait()

	if got := factory.openCount(); got != 1 {
		t.Fatalf("concurrent Acquires opened the provider %d times, want 1", got)
	}
	for i := range results {
		if errs[i] != nil {
			t.Fatalf("worker %d: %v", i, errs[i])
		}
		if results[i] != provider {
			t.Fatalf("worker %d received a different provider", i)
		}
	}
}

func TestNetworkProviderRegistryRetriesAFailedOpen(t *testing.T) {
	boom := errors.New("tailnet join failed")
	var calls atomic.Int32
	provider := &fakeNetworkProvider{}
	factory := &fakeProviderFactory{openFn: func(context.Context, NetworkRoute) (NetworkProvider, error) {
		if calls.Add(1) == 1 {
			return nil, boom
		}
		return provider, nil
	}}
	registry := NewNetworkProviderRegistry(factory)
	route := testNetworkRoute("profile-a", "fp-1")

	_, err := registry.Acquire(context.Background(), route)
	typed := typedConnectionError(t, err)
	if typed.Kind != KindProvider {
		t.Fatalf("startup failure kind = %s, want provider", typed.Kind)
	}
	if !errors.Is(err, boom) {
		t.Fatalf("startup failure lost its cause: %v", err)
	}
	if typed.Provider == nil || typed.Provider.ProfileID != route.ProfileID ||
		typed.Provider.Label != route.Label || typed.Provider.Kind != route.Kind ||
		typed.Provider.Phase != ProviderPhaseError {
		t.Fatalf("ProviderInfo = %+v, want the failed profile at phase error", typed.Provider)
	}

	opened, err := registry.Acquire(context.Background(), route)
	if err != nil {
		t.Fatalf("retry after a failed open: %v", err)
	}
	if opened != provider {
		t.Fatal("retry returned a different provider")
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("factory calls = %d, want 2", got)
	}
}

func TestNetworkProviderRegistryClosesAProviderReturnedWithAnError(t *testing.T) {
	provider := &fakeNetworkProvider{}
	factory := &fakeProviderFactory{openFn: func(context.Context, NetworkRoute) (NetworkProvider, error) {
		return provider, errors.New("join failed after allocating sockets")
	}}
	registry := NewNetworkProviderRegistry(factory)

	_, err := registry.Acquire(context.Background(), testNetworkRoute("profile-a", "fp-1"))
	if err == nil {
		t.Fatal("Acquire succeeded with a failing factory")
	}
	typed := typedConnectionError(t, err)
	if typed.Kind != KindProvider {
		t.Fatalf("kind = %s, want provider", typed.Kind)
	}
	if got := provider.closeCount(); got != 1 {
		t.Fatalf("provider returned with an error was closed %d times, want 1", got)
	}
}

func TestNetworkProviderRegistryReplacesAFingerprintAndClosesTheOldProvider(t *testing.T) {
	factory := &fakeProviderFactory{}
	registry := NewNetworkProviderRegistry(factory)
	var (
		mu       sync.Mutex
		profiles []string
	)
	registry.SetCloseHook(func(profileID string) {
		mu.Lock()
		profiles = append(profiles, profileID)
		mu.Unlock()
	})

	first, err := registry.Acquire(context.Background(), testNetworkRoute("profile-a", "fp-1"))
	if err != nil {
		t.Fatalf("Acquire fp-1: %v", err)
	}
	second, err := registry.Acquire(context.Background(), testNetworkRoute("profile-a", "fp-2"))
	if err != nil {
		t.Fatalf("Acquire fp-2: %v", err)
	}
	if first == second {
		t.Fatal("a rotated fingerprint reused the old provider")
	}
	if got := first.(*fakeNetworkProvider).closeCount(); got != 1 {
		t.Fatalf("old provider close count = %d, want 1", got)
	}
	again, err := registry.Acquire(context.Background(), testNetworkRoute("profile-a", "fp-2"))
	if err != nil {
		t.Fatalf("Acquire fp-2 again: %v", err)
	}
	if again != second {
		t.Fatal("the current fingerprint did not reuse its provider")
	}
	if got := factory.openCount(); got != 2 {
		t.Fatalf("factory opens = %d, want 2", got)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(profiles) != 1 || profiles[0] != "profile-a" {
		t.Fatalf("close hook profiles = %v, want [profile-a]", profiles)
	}
}

func TestNetworkProviderRegistryFailsClosedBeforeOpening(t *testing.T) {
	tests := []struct {
		name     string
		registry *NetworkProviderRegistry
		route    NetworkRoute
		sentinel error
	}{
		{
			name:     "nil registry",
			registry: nil,
			route:    testNetworkRoute("profile-a", "fp-1"),
			sentinel: ErrNetworkProvider,
		},
		{
			name:     "nil factory",
			registry: NewNetworkProviderRegistry(nil),
			route:    testNetworkRoute("profile-a", "fp-1"),
			sentinel: ErrNetworkProvider,
		},
		{
			name:     "missing profile",
			registry: NewNetworkProviderRegistry(&fakeProviderFactory{}),
			route:    NetworkRoute{Kind: "tailscale"},
			sentinel: ErrNetworkProviderProfile,
		},
		{
			name:     "empty kind",
			registry: NewNetworkProviderRegistry(&fakeProviderFactory{}),
			route:    NetworkRoute{ProfileID: "profile-a"},
			sentinel: ErrNetworkProviderKind,
		},
		{
			name:     "unknown kind",
			registry: NewNetworkProviderRegistry(&fakeProviderFactory{}),
			route:    NetworkRoute{ProfileID: "profile-a", Kind: "openvpn"},
			sentinel: ErrNetworkProviderKind,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider, err := test.registry.Acquire(context.Background(), test.route)
			if provider != nil {
				t.Fatalf("Acquire returned provider %v", provider)
			}
			typed := typedConnectionError(t, err)
			if typed.Kind != KindProvider || typed.Provider == nil {
				t.Fatalf("error = %v, want KindProvider with ProviderInfo", err)
			}
			if !errors.Is(err, test.sentinel) {
				t.Fatalf("error = %v, want %v", err, test.sentinel)
			}
		})
	}
}

func TestNetworkProviderRegistryPreservesAReportedPhase(t *testing.T) {
	factory := &fakeProviderFactory{err: &Error{
		Kind:  KindProvider,
		Cause: errors.New("login required"),
		Provider: &ProviderInfo{
			Phase: ProviderPhaseNeedsLogin,
		},
	}}
	registry := NewNetworkProviderRegistry(factory)
	route := testNetworkRoute("profile-a", "fp-1")

	_, err := registry.Acquire(context.Background(), route)
	typed := typedConnectionError(t, err)
	if typed.Kind != KindProvider {
		t.Fatalf("kind = %s, want provider", typed.Kind)
	}
	if typed.Provider == nil || typed.Provider.Phase != ProviderPhaseNeedsLogin ||
		typed.Provider.ProfileID != route.ProfileID || typed.Provider.Label != route.Label {
		t.Fatalf("ProviderInfo = %+v, want the route identity and needs_login", typed.Provider)
	}
}

func TestNetworkProviderRegistryCloseWhileOpeningAbandonsTheResult(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	provider := &fakeNetworkProvider{}
	factory := &fakeProviderFactory{openFn: func(context.Context, NetworkRoute) (NetworkProvider, error) {
		once.Do(func() { close(started) })
		<-release
		return provider, nil
	}}
	registry := NewNetworkProviderRegistry(factory)
	route := testNetworkRoute("profile-a", "fp-1")

	done := make(chan error, 1)
	go func() {
		_, err := registry.Acquire(context.Background(), route)
		done <- err
	}()
	<-started
	if err := registry.Close(route.ProfileID); err != nil {
		t.Fatalf("Close: %v", err)
	}
	close(release)
	if err := <-done; err == nil || !errors.Is(err, ErrNetworkProviderClosed) {
		t.Fatalf("Acquire after Close = %v, want a closed provider error", err)
	}
	if got := provider.closeCount(); got != 1 {
		t.Fatalf("abandoned provider close count = %d, want 1", got)
	}
}

// TestNetworkProviderRegistryStoppingDuringReadyPublicationClosesOnce pins the
// ownership handshake that used to race: an opener that has decided it owns the
// entry must publish readiness before releasing the registry lock. Close,
// Cancel, and CloseAll arriving in that window must observe a settled entry and
// release the Provider exactly once.
func TestNetworkProviderRegistryStoppingDuringReadyPublicationClosesOnce(t *testing.T) {
	operations := map[string]func(*NetworkProviderRegistry, string) error{
		"close": func(registry *NetworkProviderRegistry, profileID string) error {
			return registry.Close(profileID)
		},
		"cancel": func(registry *NetworkProviderRegistry, profileID string) error {
			return registry.Cancel(profileID)
		},
		"close all": func(registry *NetworkProviderRegistry, _ string) error {
			return registry.CloseAll()
		},
	}
	for name, stop := range operations {
		t.Run(name, func(t *testing.T) {
			factory := &fakeProviderFactory{}
			registry := NewNetworkProviderRegistry(factory)
			hookEntered := make(chan struct{})
			releaseHook := make(chan struct{})
			registry.afterOwnership = func(entry *providerEntry) {
				select {
				case <-entry.ready:
				default:
					t.Error("readiness was not published while the registry lock was held")
				}
				close(hookEntered)
				<-releaseHook
			}
			route := testNetworkRoute("profile-a", "fp-1")

			type acquisition struct {
				provider NetworkProvider
				err      error
			}
			acquired := make(chan acquisition, 1)
			go func() {
				provider, err := registry.Acquire(context.Background(), route)
				acquired <- acquisition{provider: provider, err: err}
			}()
			<-hookEntered

			stopStarted := make(chan struct{})
			stopped := make(chan error, 1)
			go func() {
				close(stopStarted)
				stopped <- stop(registry, route.ProfileID)
			}()
			<-stopStarted
			close(releaseHook)

			result := <-acquired
			if err := <-stopped; err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if result.err == nil && result.provider == nil {
				t.Fatal("Acquire returned neither a Provider nor an error")
			}
			providers := factory.openedProviders()
			if len(providers) != 1 {
				t.Fatalf("opened Providers = %d, want 1", len(providers))
			}
			if got := providers[0].closeCount(); got != 1 {
				t.Fatalf("Provider close count = %d, want exactly 1", got)
			}
			if result.err == nil {
				stoppedProvider, ok := result.provider.(*fakeNetworkProvider)
				if !ok || stoppedProvider != providers[0] {
					t.Fatalf("Acquire returned %T, want the opened Provider", result.provider)
				}
			}
		})
	}
}

// cancellableFakeProvider is a provider that can stop its current attempt while
// staying reusable, like the Tailscale Net Provider.
type cancellableFakeProvider struct {
	fakeNetworkProvider
	cancels   int
	cancelErr error
}

func (p *cancellableFakeProvider) Cancel() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cancels++
	return p.cancelErr
}

func (p *cancellableFakeProvider) cancelCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cancels
}

func TestNetworkProviderRegistryCancelStopsWithoutClosingAndReopens(t *testing.T) {
	provider := &cancellableFakeProvider{}
	factory := &fakeProviderFactory{}
	factory.openFn = func(context.Context, NetworkRoute) (NetworkProvider, error) {
		if factory.openCount() == 1 {
			return provider, nil
		}
		return &cancellableFakeProvider{}, nil
	}
	registry := NewNetworkProviderRegistry(factory)
	var (
		mu       sync.Mutex
		profiles []string
	)
	registry.SetCloseHook(func(profileID string) {
		mu.Lock()
		profiles = append(profiles, profileID)
		mu.Unlock()
	})
	route := testNetworkRoute("profile-a", "fp-1")

	first, err := registry.Acquire(context.Background(), route)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := registry.Cancel(route.ProfileID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if got := provider.cancelCount(); got != 1 {
		t.Fatalf("provider cancels = %d, want 1", got)
	}
	if got := provider.closeCount(); got != 0 {
		t.Fatalf("provider closes = %d, want the cancelled provider left reusable", got)
	}
	mu.Lock()
	hookProfiles := append([]string(nil), profiles...)
	mu.Unlock()
	if len(hookProfiles) != 1 || hookProfiles[0] != route.ProfileID {
		t.Fatalf("close hook profiles = %v, want one invalidation for the profile", hookProfiles)
	}

	second, err := registry.Acquire(context.Background(), route)
	if err != nil {
		t.Fatalf("Acquire after Cancel: %v", err)
	}
	if second == first {
		t.Fatal("Acquire after Cancel reused the cancelled provider")
	}
	if got := factory.openCount(); got != 2 {
		t.Fatalf("factory opens = %d, want a fresh provider after Cancel", got)
	}

	// Cancelling an unknown profile and an empty registry is a no-op.
	if err := registry.Cancel("profile-missing"); err != nil {
		t.Fatalf("Cancel unknown profile: %v", err)
	}
	var nilRegistry *NetworkProviderRegistry
	if err := nilRegistry.Cancel("profile-a"); err != nil {
		t.Fatalf("nil registry Cancel: %v", err)
	}
}

func TestNetworkProviderRegistryCancelReportsTheCancellerError(t *testing.T) {
	boom := errors.New("cancel failed")
	provider := &cancellableFakeProvider{cancelErr: boom}
	factory := &fakeProviderFactory{openFn: func(context.Context, NetworkRoute) (NetworkProvider, error) {
		return provider, nil
	}}
	registry := NewNetworkProviderRegistry(factory)
	route := testNetworkRoute("profile-a", "fp-1")
	if _, err := registry.Acquire(context.Background(), route); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := registry.Cancel(route.ProfileID); !errors.Is(err, boom) {
		t.Fatalf("Cancel error = %v, want the canceller failure", err)
	}
	if got := provider.closeCount(); got != 0 {
		t.Fatalf("provider closes = %d, want no close on a canceller path", got)
	}
}

func TestNetworkProviderRegistryCancelClosesAProviderWithoutCanceller(t *testing.T) {
	factory := &fakeProviderFactory{}
	registry := NewNetworkProviderRegistry(factory)
	var (
		mu       sync.Mutex
		profiles []string
	)
	registry.SetCloseHook(func(profileID string) {
		mu.Lock()
		profiles = append(profiles, profileID)
		mu.Unlock()
	})
	route := testNetworkRoute("profile-a", "fp-1")
	opened, err := registry.Acquire(context.Background(), route)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := registry.Cancel(route.ProfileID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if got := opened.(*fakeNetworkProvider).closeCount(); got != 1 {
		t.Fatalf("provider closes = %d, want the fallback close exactly once", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(profiles) != 1 || profiles[0] != route.ProfileID {
		t.Fatalf("close hook profiles = %v, want one invalidation for the profile", profiles)
	}
}

func TestNetworkProviderRegistryCancelWhileOpeningCancelsTheOpen(t *testing.T) {
	started := make(chan struct{})
	openCanceled := make(chan struct{})
	provider := &cancellableFakeProvider{}
	factory := &fakeProviderFactory{openFn: func(ctx context.Context, _ NetworkRoute) (NetworkProvider, error) {
		close(started)
		<-ctx.Done()
		close(openCanceled)
		return nil, ctx.Err()
	}}
	registry := NewNetworkProviderRegistry(factory)
	route := testNetworkRoute("profile-a", "fp-1")

	done := make(chan error, 1)
	go func() {
		_, err := registry.Acquire(context.Background(), route)
		done <- err
	}()
	<-started
	if err := registry.Cancel(route.ProfileID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	select {
	case <-openCanceled:
	case <-time.After(10 * time.Second):
		t.Fatal("Cancel did not cancel the in-flight open")
	}
	err := <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Acquire after Cancel = %v, want the cancelled open's error", err)
	}
	if typed := typedConnectionError(t, err); typed.Kind != KindCancelled {
		t.Fatalf("kind = %s, want cancelled", typed.Kind)
	}
	if got := provider.closeCount(); got != 0 {
		t.Fatalf("provider close count = %d, want none when the open produced no provider", got)
	}
	if got := provider.cancelCount(); got != 0 {
		t.Fatalf("provider cancels = %d, want the in-flight attempt cancelled through its context", got)
	}
}

func TestTagProviderAttachesContextWithoutChangingKind(t *testing.T) {
	original := &Error{Kind: KindTimeout, Cause: errors.New("deadline")}
	tagged := TagProvider(original, ProviderInfo{
		ProfileID: "profile-a", Label: "Office", Kind: "tailscale", Phase: ProviderPhaseRunning,
	})
	typed := typedConnectionError(t, tagged)
	if typed.Kind != KindTimeout {
		t.Fatalf("kind = %s, want the transport's timeout", typed.Kind)
	}
	if typed.Provider == nil || typed.Provider.ProfileID != "profile-a" ||
		typed.Provider.Phase != ProviderPhaseRunning {
		t.Fatalf("ProviderInfo = %+v, want the attached profile", typed.Provider)
	}

	plain := TagProvider(errors.New("unclassified"), ProviderInfo{ProfileID: "profile-a"})
	if got := typedConnectionError(t, plain).Kind; got != KindProvider {
		t.Fatalf("plain error kind = %s, want provider", got)
	}
	if TagProvider(nil, ProviderInfo{}) != nil {
		t.Fatal("TagProvider invented an error for nil")
	}
}

func TestManagerFailsClosedWithoutANetworkProviderRegistry(t *testing.T) {
	dialer := &fakeDialer{}
	manager := NewManager(dialer, &fakePasswordSource{password: "pw"}, &fakeKnownHosts{})
	t.Cleanup(manager.CloseAll)

	req := sshRequest()
	req.Network = &NetworkRoute{ProfileID: "profile-a", Kind: "tailscale", Fingerprint: "fp-1"}
	_, err := manager.Acquire(context.Background(), req)
	typed := typedConnectionError(t, err)
	if typed.Kind != KindProvider || !errors.Is(err, ErrNetworkProvider) {
		t.Fatalf("Acquire without a registry = %v, want a closed KindProvider error", err)
	}
	if got := dialer.dialCount(); got != 0 {
		t.Fatalf("dialer calls = %d, want no direct dial", got)
	}
}

func TestManagerOpensTheProviderBeforeDialingAndPassesItThrough(t *testing.T) {
	factory := &fakeProviderFactory{}
	registry := NewNetworkProviderRegistry(factory)
	dialer := &fakeDialer{}
	manager := NewManager(dialer, &fakePasswordSource{password: "pw"}, &fakeKnownHosts{},
		WithNetworkProviders(registry))
	t.Cleanup(manager.CloseAll)

	req := sshRequest()
	req.Network = &NetworkRoute{ProfileID: "profile-a", Kind: "tailscale", Fingerprint: "fp-1"}
	lease, err := manager.Acquire(context.Background(), req)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	_ = lease.Release()

	request := dialer.request()
	if request.Network == nil || request.Network.ProfileID != "profile-a" {
		t.Fatalf("DialRequest.Network = %+v, want the selected profile", request.Network)
	}
	providers := factory.openedProviders()
	if len(providers) != 1 {
		t.Fatalf("factory opened %d providers, want 1", len(providers))
	}
	if request.Provider != providers[0] {
		t.Fatal("DialRequest.Provider is not the provider the registry opened")
	}
}

func TestManagerKeepsTransportKindAndAttachesProviderInfo(t *testing.T) {
	tests := []struct {
		name     string
		dialErr  error
		wantKind Kind
	}{
		{"unreachable target", errors.New("connect: connection refused"), KindNetwork},
		{"deadline", context.DeadlineExceeded, KindTimeout},
		{"provider tunnel", &Error{
			Kind:     KindProvider,
			Cause:    errors.New("tunnel is down"),
			Provider: &ProviderInfo{Phase: ProviderPhaseRunning},
		}, KindProvider},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := NewNetworkProviderRegistry(&fakeProviderFactory{})
			dialer := &fakeDialer{err: test.dialErr}
			manager := NewManager(dialer, &fakePasswordSource{password: "pw"}, &fakeKnownHosts{},
				WithNetworkProviders(registry))
			t.Cleanup(manager.CloseAll)

			req := sshRequest()
			req.Network = &NetworkRoute{ProfileID: "profile-a", Kind: "tailscale", Fingerprint: "fp-1"}
			_, err := manager.Acquire(context.Background(), req)
			typed := typedConnectionError(t, err)
			if typed.Kind != test.wantKind {
				t.Fatalf("kind = %s, want %s (%v)", typed.Kind, test.wantKind, err)
			}
			if typed.Provider == nil || typed.Provider.ProfileID != "profile-a" {
				t.Fatalf("ProviderInfo = %+v, want the selected profile", typed.Provider)
			}
			if got := dialer.dialCount(); got != 1 {
				t.Fatalf("dialer calls = %d, want exactly one attempt and no fallback", got)
			}
		})
	}
}

func TestManagerProviderStartupFailureNeverDials(t *testing.T) {
	registry := NewNetworkProviderRegistry(&fakeProviderFactory{err: errors.New("not logged in")})
	dialer := &fakeDialer{}
	manager := NewManager(dialer, &fakePasswordSource{password: "pw"}, &fakeKnownHosts{},
		WithNetworkProviders(registry))
	t.Cleanup(manager.CloseAll)

	req := sshRequest()
	req.Network = &NetworkRoute{ProfileID: "profile-a", Kind: "tailscale", Fingerprint: "fp-1"}
	_, err := manager.Acquire(context.Background(), req)
	typed := typedConnectionError(t, err)
	if typed.Kind != KindProvider {
		t.Fatalf("kind = %s, want provider", typed.Kind)
	}
	if typed.Provider == nil || typed.Provider.Phase != ProviderPhaseError {
		t.Fatalf("ProviderInfo = %+v, want the startup error phase", typed.Provider)
	}
	if got := dialer.dialCount(); got != 0 {
		t.Fatalf("dialer calls = %d, want no direct dial after a provider failure", got)
	}
}

func TestEndpointKeyIncludesNetworkProfileAndFingerprint(t *testing.T) {
	base := endpointFor(sshRequest())
	if base.networkProfileID != "" || base.networkFingerprint != "" {
		t.Fatalf("plain endpoint has network identity: %+v", base)
	}

	networked := sshRequest()
	networked.Network = &NetworkRoute{
		ProfileID: "profile-a", Kind: "tailscale", Label: "First", Fingerprint: "fp-1",
	}
	rotated := *networked.Network
	rotated.Fingerprint = "fp-2"
	rotatedRequest := networked
	rotatedRequest.Network = &rotated
	renamed := *networked.Network
	renamed.Label = "Renamed"
	renamedRequest := networked
	renamedRequest.Network = &renamed

	networkedEndpoint := endpointFor(networked)
	if networkedEndpoint.key == base.key {
		t.Fatal("adding a Network Profile did not change the pool key")
	}
	if networkedEndpoint.key == endpointFor(rotatedRequest).key {
		t.Fatal("a rotated fingerprint did not change the pool key")
	}
	if networkedEndpoint.key != endpointFor(renamedRequest).key {
		t.Fatal("the display label entered the pool key")
	}
	if networkedEndpoint.networkProfileID != "profile-a" || networkedEndpoint.networkFingerprint != "fp-1" {
		t.Fatalf("endpoint network identity = %+v", networkedEndpoint)
	}
}

func TestProviderCloseInvalidatesThatProfilesIdleConnections(t *testing.T) {
	factory := &fakeProviderFactory{}
	registry := NewNetworkProviderRegistry(factory)
	dialer := &sequenceDialer{}
	manager := poolTestManager(t, dialer, WithNetworkProviders(registry))

	req := sshRequest()
	req.Network = &NetworkRoute{ProfileID: "profile-a", Kind: "tailscale", Label: "A", Fingerprint: "fp-1"}
	lease, err := manager.Acquire(context.Background(), req)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	client := dialer.clients[0]
	if err := lease.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if client.isClosed() {
		t.Fatal("the released transport was not parked before invalidation")
	}

	if err := registry.Close("profile-a"); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !client.isClosed() {
		t.Fatal("the provider close hook did not invalidate the idle transport")
	}

	second, err := manager.Acquire(context.Background(), req)
	if err != nil {
		t.Fatalf("Acquire after invalidation: %v", err)
	}
	_ = second.Release()
	if got := dialer.dialCount(); got != 2 {
		t.Fatalf("dialer calls = %d, want a fresh dial after invalidation", got)
	}
	if got := factory.openCount(); got != 2 {
		t.Fatalf("provider opens = %d, want a fresh provider after close", got)
	}
}

func TestFingerprintReplacementInvalidatesPriorIdleConnections(t *testing.T) {
	factory := &fakeProviderFactory{}
	registry := NewNetworkProviderRegistry(factory)
	dialer := &sequenceDialer{}
	manager := poolTestManager(t, dialer, WithNetworkProviders(registry))

	oldRequest := sshRequest()
	oldRequest.Network = &NetworkRoute{ProfileID: "profile-a", Kind: "tailscale", Fingerprint: "fp-1"}
	lease, err := manager.Acquire(context.Background(), oldRequest)
	if err != nil {
		t.Fatalf("Acquire fp-1: %v", err)
	}
	oldClient := dialer.clients[0]
	if err := lease.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}

	newRequest := oldRequest
	newRequest.Network = &NetworkRoute{ProfileID: "profile-a", Kind: "tailscale", Fingerprint: "fp-2"}
	second, err := manager.Acquire(context.Background(), newRequest)
	if err != nil {
		t.Fatalf("Acquire fp-2: %v", err)
	}
	_ = second.Release()

	if !oldClient.isClosed() {
		t.Fatal("fingerprint replacement did not invalidate the old idle transport")
	}
	if got := dialer.dialCount(); got != 2 {
		t.Fatalf("dialer calls = %d, want a fresh dial for the new fingerprint", got)
	}
	if got := factory.openCount(); got != 2 {
		t.Fatalf("provider opens = %d, want a replacement provider", got)
	}
}

func TestInvalidatingAProfileLeavesActiveLeasesAlone(t *testing.T) {
	registry := NewNetworkProviderRegistry(&fakeProviderFactory{})
	dialer := &sequenceDialer{}
	manager := poolTestManager(t, dialer, WithNetworkProviders(registry))

	req := sshRequest()
	req.Network = &NetworkRoute{ProfileID: "profile-a", Kind: "tailscale", Fingerprint: "fp-1"}
	lease, err := manager.Acquire(context.Background(), req)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	client := dialer.clients[0]
	if err := registry.Close("profile-a"); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if client.isClosed() {
		t.Fatal("an active lease's transport was closed by invalidation")
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
}
