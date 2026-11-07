// SPDX-License-Identifier: GPL-3.0-or-later

package tailscale

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	appconnection "github.com/mslxl/natcatty/application/connection"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

const testAuthKey = "tskey-auth-test-secret-do-not-log"

// fakeBackend is the Network Provider's test double. It scripts start, status,
// and dial outcomes and records every call, so the state machine can be tested
// without a tailnet or a socket.
type fakeBackend struct {
	mu       sync.Mutex
	startFn  func(context.Context) (backendStatus, error)
	statusFn func(context.Context) (backendStatus, error)
	dialFn   func(context.Context, string, string) (net.Conn, error)
	started  chan struct{}
	startOne sync.Once

	networks  []string
	addresses []string
	dials     int
	closes    int
}

func (b *fakeBackend) Start(ctx context.Context) (backendStatus, error) {
	if b.started != nil {
		b.startOne.Do(func() { close(b.started) })
	}
	if b.startFn != nil {
		return b.startFn(ctx)
	}
	return backendStatus{Phase: appconnection.ProviderPhaseRunning, TailscaleIPs: []string{"100.64.0.2"}}, nil
}

func (b *fakeBackend) Dial(ctx context.Context, network, address string) (net.Conn, error) {
	b.mu.Lock()
	b.dials++
	b.networks = append(b.networks, network)
	b.addresses = append(b.addresses, address)
	fn := b.dialFn
	b.mu.Unlock()
	if fn != nil {
		return fn(ctx, network, address)
	}
	server, client := net.Pipe()
	_ = server.Close()
	return client, nil
}

func (b *fakeBackend) Status(ctx context.Context) (backendStatus, error) {
	if b.statusFn != nil {
		return b.statusFn(ctx)
	}
	return backendStatus{Phase: appconnection.ProviderPhaseRunning, TailscaleIPs: []string{"100.64.0.2"}}, nil
}

func (b *fakeBackend) Close() error {
	b.mu.Lock()
	b.closes++
	b.mu.Unlock()
	return nil
}

func (b *fakeBackend) closeCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closes
}

func (b *fakeBackend) dialCalls() (int, []string, []string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.dials, append([]string(nil), b.networks...), append([]string(nil), b.addresses...)
}

// statusRecorder captures the status events a Provider emits.
type statusRecorder struct {
	mu       sync.Mutex
	statuses []Status
}

func (r *statusRecorder) observe(status Status) {
	r.mu.Lock()
	r.statuses = append(r.statuses, status)
	r.mu.Unlock()
}

func (r *statusRecorder) all() []Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Status(nil), r.statuses...)
}

func testConfig(t *testing.T) Config {
	t.Helper()
	return Config{
		ProfileID:    "net-1",
		Label:        "Office",
		Kind:         domainvault.NetworkKindTailscale,
		StateBaseDir: t.TempDir(),
		AuthKey:      testAuthKey,
	}
}

func fakeProvider(t *testing.T, cfg Config, fake *fakeBackend) (*Provider, *statusRecorder) {
	t.Helper()
	recorder := &statusRecorder{}
	provider, err := NewProvider(cfg,
		WithStatusObserver(recorder.observe),
		withBackendFactory(func(Config) (backend, error) { return fake, nil }),
		// The background poll is exercised explicitly; keep it out of the
		// timing of unrelated state-machine tests.
		withPollInterval(time.Hour),
	)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	return provider, recorder
}

// pollingProvider builds a Provider whose live status poll runs at interval and
// reports through a channel, so a phase transition is awaited as a condition
// rather than a fixed delay.
func pollingProvider(t *testing.T, cfg Config, fake *fakeBackend,
	interval time.Duration) (*Provider, <-chan Status) {
	t.Helper()
	observed := make(chan Status, 64)
	provider, err := NewProvider(cfg,
		WithStatusObserver(func(status Status) {
			select {
			case observed <- status:
			default:
			}
		}),
		withBackendFactory(func(Config) (backend, error) { return fake, nil }),
		withPollInterval(interval),
	)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	return provider, observed
}

// waitForStatus reads observations until want arrives or a hard deadline
// expires.
func waitForStatus(t *testing.T, observed <-chan Status,
	want appconnection.ProviderPhase) Status {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case status := <-observed:
			if status.Phase == want {
				return status
			}
		case <-deadline:
			t.Fatalf("no %q status was observed", want)
		}
	}
}

// waitForProviderPhase polls the live phase until it reaches want or a hard
// deadline expires.
func waitForProviderPhase(t *testing.T, provider *Provider, want appconnection.ProviderPhase) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if provider.Phase() == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("phase = %q, want %q", provider.Phase(), want)
}

// waitForBackendCloses waits until the backend's Close has been called want
// times or a hard deadline expires. failRuntime publishes the error phase
// before releasing the backend, so observing the phase alone is not proof that
// the close finished.
func waitForBackendCloses(t *testing.T, fake *fakeBackend, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if fake.closeCount() == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("backend closes = %d, want %d", fake.closeCount(), want)
}

func typedProviderError(t *testing.T, err error) *appconnection.Error {
	t.Helper()
	var typed *appconnection.Error
	if !errors.As(err, &typed) {
		t.Fatalf("error %v is not a typed connection error", err)
	}
	if typed.Provider == nil {
		t.Fatalf("typed error %v carries no ProviderInfo", typed)
	}
	return typed
}

func TestAuthKeyStartReachesRunningAndDialUsesTheBackend(t *testing.T) {
	fake := &fakeBackend{
		startFn: func(context.Context) (backendStatus, error) {
			return backendStatus{
				Phase:        appconnection.ProviderPhaseRunning,
				TailscaleIPs: []string{"100.64.0.2", "fd7a:115c:a1e0::2"},
				DNSName:      "natcatty-office.tailnet.ts.net",
			}, nil
		},
	}
	provider, recorder := fakeProvider(t, testConfig(t), fake)

	if err := provider.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := provider.Phase(); got != appconnection.ProviderPhaseRunning {
		t.Fatalf("Phase = %q, want running", got)
	}
	last := recorder.all()
	if len(last) == 0 || last[len(last)-1].Phase != appconnection.ProviderPhaseRunning {
		t.Fatalf("status events = %+v, want a final running event", last)
	}
	if last[len(last)-1].DNSName != "natcatty-office.tailnet.ts.net" {
		t.Fatalf("DNSName = %q", last[len(last)-1].DNSName)
	}

	conn, err := provider.DialContext(context.Background(), "tcp", "peer.tailnet.ts.net:22")
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	_ = conn.Close()
	calls, networks, addresses := fake.dialCalls()
	if calls != 1 || networks[0] != "tcp" || addresses[0] != "peer.tailnet.ts.net:22" {
		t.Fatalf("backend dials = %d %v %v", calls, networks, addresses)
	}
}

func TestMissingAuthKeyWaitsForInteractiveLogin(t *testing.T) {
	cfg := testConfig(t)
	cfg.AuthKey = ""
	authURL := "https://login.tailscale.com/a/example"
	fake := &fakeBackend{
		startFn: func(context.Context) (backendStatus, error) {
			return backendStatus{
				Phase:   appconnection.ProviderPhaseNeedsLogin,
				AuthURL: authURL,
			}, nil
		},
	}
	provider, recorder := fakeProvider(t, cfg, fake)

	// A login-needing start is not a failure: the node stays running so the
	// browser login can complete without an application restart.
	if err := provider.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := provider.Phase(); got != appconnection.ProviderPhaseNeedsLogin {
		t.Fatalf("Phase = %q, want needs_login", got)
	}
	status := provider.Status()
	if status.AuthURL != authURL {
		t.Fatalf("Status.AuthURL = %q, want the login URL", status.AuthURL)
	}

	events := recorder.all()
	found := false
	for _, event := range events {
		if event.Phase == appconnection.ProviderPhaseNeedsLogin && event.AuthURL == authURL {
			found = true
		}
	}
	if !found {
		t.Fatalf("status events = %+v, want needs_login with the Auth URL", events)
	}
	if fake.closeCount() != 0 {
		t.Fatalf("backend closes = %d, want the node alive while login is pending", fake.closeCount())
	}

	// Dialing while logged out fails closed with the typed login error and
	// never falls back to a direct connection.
	_, err := provider.DialContext(context.Background(), "tcp", "peer.tailnet.ts.net:22")
	if !errors.Is(err, ErrNeedsLogin) {
		t.Fatalf("DialContext error = %v, want ErrNeedsLogin", err)
	}
	typed := typedProviderError(t, err)
	if typed.Kind != appconnection.KindProvider {
		t.Fatalf("Kind = %q, want provider", typed.Kind)
	}
	if typed.Provider.Phase != appconnection.ProviderPhaseNeedsLogin ||
		typed.Provider.ProfileID != "net-1" {
		t.Fatalf("ProviderInfo = %+v", typed.Provider)
	}
	if strings.Contains(err.Error(), authURL) {
		t.Fatalf("dial error leaked the Auth URL: %q", err)
	}
	if calls, _, _ := fake.dialCalls(); calls != 0 {
		t.Fatalf("backend dials = %d, want none while login is pending", calls)
	}

	if err := provider.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if fake.closeCount() != 1 {
		t.Fatalf("backend closes after Close = %d, want exactly one", fake.closeCount())
	}
}

// phaseEvents forwards every status event the provider emits to a buffered
// channel. The observer must never block the provider goroutine.
func phaseEvents() (func(Status), <-chan Status) {
	events := make(chan Status, 32)
	return func(status Status) {
		select {
		case events <- status:
		default:
		}
	}, events
}

func waitForPhase(t *testing.T, events <-chan Status, phase appconnection.ProviderPhase) Status {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case status := <-events:
			if status.Phase == phase {
				return status
			}
		case <-timeout:
			t.Fatalf("timed out waiting for a %q status event", phase)
		}
	}
}

func TestInteractiveLoginCompletionPublishesRunning(t *testing.T) {
	authURL := "https://login.tailscale.com/a/example"
	var loggedIn atomic.Bool
	fake := &fakeBackend{
		startFn: func(context.Context) (backendStatus, error) {
			return backendStatus{Phase: appconnection.ProviderPhaseNeedsLogin, AuthURL: authURL}, nil
		},
		statusFn: func(context.Context) (backendStatus, error) {
			if loggedIn.Load() {
				return backendStatus{
					Phase:        appconnection.ProviderPhaseRunning,
					TailscaleIPs: []string{"100.64.0.2"},
					DNSName:      "natcatty-office.tailnet.ts.net",
				}, nil
			}
			return backendStatus{Phase: appconnection.ProviderPhaseNeedsLogin, AuthURL: authURL}, nil
		},
	}
	observe, events := phaseEvents()
	provider, err := NewProvider(testConfig(t),
		WithStatusObserver(observe),
		withBackendFactory(func(Config) (backend, error) { return fake, nil }),
		withLoginWatchInterval(time.Millisecond),
	)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	if err := provider.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Simulate the user finishing the browser login. The provider-owned watcher
	// must publish Running without another Start or an application restart.
	loggedIn.Store(true)
	status := waitForPhase(t, events, appconnection.ProviderPhaseRunning)
	if status.DNSName != "natcatty-office.tailnet.ts.net" ||
		len(status.TailscaleIPs) != 1 || status.AuthURL != "" {
		t.Fatalf("running status = %+v", status)
	}
	if got := provider.Phase(); got != appconnection.ProviderPhaseRunning {
		t.Fatalf("Phase = %q, want running", got)
	}

	conn, err := provider.DialContext(context.Background(), "tcp", "peer.tailnet.ts.net:22")
	if err != nil {
		t.Fatalf("DialContext after login: %v", err)
	}
	_ = conn.Close()
	if calls, _, _ := fake.dialCalls(); calls != 1 {
		t.Fatalf("backend dials = %d, want the completed login to dial through the node", calls)
	}
	if err := provider.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if fake.closeCount() != 1 {
		t.Fatalf("backend closes = %d, want exactly one", fake.closeCount())
	}
}

// TestLoginWatcherPromotedNodeKeepsTheLiveStatusPoll proves a node that reaches
// Running through an interactive login is still audited for a later
// authorization loss or stop, not left unmonitored once the watcher exits.
func TestLoginWatcherPromotedNodeKeepsTheLiveStatusPoll(t *testing.T) {
	const authURL = "https://login.tailscale.com/a/promoted"
	var loggedIn atomic.Bool
	var lost atomic.Bool
	fake := &fakeBackend{
		startFn: func(context.Context) (backendStatus, error) {
			return backendStatus{Phase: appconnection.ProviderPhaseNeedsLogin, AuthURL: authURL}, nil
		},
		statusFn: func(context.Context) (backendStatus, error) {
			switch {
			case lost.Load():
				return backendStatus{}, errors.New("tailscale: read live state: LocalClient unavailable")
			case loggedIn.Load():
				return backendStatus{
					Phase:        appconnection.ProviderPhaseRunning,
					TailscaleIPs: []string{"100.64.0.2"},
				}, nil
			default:
				return backendStatus{Phase: appconnection.ProviderPhaseNeedsLogin, AuthURL: authURL}, nil
			}
		},
	}
	observe, events := phaseEvents()
	provider, err := NewProvider(testConfig(t),
		WithStatusObserver(observe),
		withBackendFactory(func(Config) (backend, error) { return fake, nil }),
		withLoginWatchInterval(time.Millisecond),
		withPollInterval(time.Millisecond),
	)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	if err := provider.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	loggedIn.Store(true)
	waitForPhase(t, events, appconnection.ProviderPhaseRunning)

	lost.Store(true)
	waitForPhase(t, events, appconnection.ProviderPhaseError)
	waitForProviderPhase(t, provider, appconnection.ProviderPhaseError)
	waitForBackendCloses(t, fake, 1)
	if err := provider.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestCancelStopsPendingLoginAndTheNextStartRuns(t *testing.T) {
	authURL := "https://login.tailscale.com/a/example"
	var mu sync.Mutex
	var fakes []*fakeBackend
	factory := func(Config) (backend, error) {
		mu.Lock()
		defer mu.Unlock()
		fake := &fakeBackend{}
		if len(fakes) == 0 {
			fake.startFn = func(context.Context) (backendStatus, error) {
				return backendStatus{Phase: appconnection.ProviderPhaseNeedsLogin, AuthURL: authURL}, nil
			}
		}
		fakes = append(fakes, fake)
		return fake, nil
	}
	observe, events := phaseEvents()
	provider, err := NewProvider(testConfig(t),
		WithStatusObserver(observe),
		withBackendFactory(factory),
		withLoginWatchInterval(time.Hour),
	)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	if err := provider.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	first := fakes[0]

	if err := provider.Cancel(); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if got := provider.Phase(); got != appconnection.ProviderPhaseStopped {
		t.Fatalf("Phase after Cancel = %q, want stopped", got)
	}
	if status := provider.Status(); status.AuthURL != "" || status.Phase != appconnection.ProviderPhaseStopped {
		t.Fatalf("status after Cancel = %+v, want a cleared stopped status", status)
	}
	if first.closeCount() != 1 {
		t.Fatalf("backend closes after Cancel = %d, want exactly one", first.closeCount())
	}
	waitForPhase(t, events, appconnection.ProviderPhaseStopped)

	// Cancel leaves the Provider reusable: the next Start opens a fresh node.
	if err := provider.Start(context.Background()); err != nil {
		t.Fatalf("restart after Cancel: %v", err)
	}
	if got := provider.Phase(); got != appconnection.ProviderPhaseRunning {
		t.Fatalf("Phase after restart = %q, want running", got)
	}
	if len(fakes) != 2 {
		t.Fatalf("backends opened = %d, want a fresh node for the retry", len(fakes))
	}
	if err := provider.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if first.closeCount() != 1 || fakes[1].closeCount() != 1 {
		t.Fatalf("backend closes = %d/%d, want one close per node",
			first.closeCount(), fakes[1].closeCount())
	}
}

func TestCancelWhileJoiningClosesTheServerOnce(t *testing.T) {
	entered := make(chan struct{})
	fake := &fakeBackend{
		started: entered,
		startFn: func(ctx context.Context) (backendStatus, error) {
			<-ctx.Done()
			return backendStatus{}, ctx.Err()
		},
	}
	provider, recorder := fakeProvider(t, testConfig(t), fake)

	startErr := make(chan error, 1)
	go func() { startErr <- provider.Start(context.Background()) }()
	<-entered
	if err := provider.Cancel(); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if err := <-startErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Start error = %v, want context.Canceled", err)
	}
	events := recorder.all()
	if len(events) == 0 {
		t.Fatal("Cancel published no status")
	}
	if last := events[len(events)-1]; last.Phase != appconnection.ProviderPhaseStopped || last.Err != nil {
		t.Fatalf("last status after Cancel = %+v, want a clean stopped status", last)
	}
	if fake.closeCount() != 1 {
		t.Fatalf("backend closes = %d, want the cancelled join released exactly once", fake.closeCount())
	}
	if got := provider.Phase(); got != appconnection.ProviderPhaseStopped {
		t.Fatalf("Phase = %q, want stopped", got)
	}
	if err := provider.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if fake.closeCount() != 1 {
		t.Fatalf("backend closes after Close = %d, want exactly one", fake.closeCount())
	}
}

func TestLoginWatcherFailureRetiresNodeAndRetryStartsFresh(t *testing.T) {
	authURL := "https://login.tailscale.com/a/example"
	var fail atomic.Bool
	var mu sync.Mutex
	var fakes []*fakeBackend
	factory := func(Config) (backend, error) {
		mu.Lock()
		defer mu.Unlock()
		fake := &fakeBackend{}
		if len(fakes) == 0 {
			fake.startFn = func(context.Context) (backendStatus, error) {
				return backendStatus{Phase: appconnection.ProviderPhaseNeedsLogin, AuthURL: authURL}, nil
			}
			fake.statusFn = func(context.Context) (backendStatus, error) {
				if fail.Load() {
					return backendStatus{Phase: appconnection.ProviderPhaseError}, nil
				}
				return backendStatus{Phase: appconnection.ProviderPhaseNeedsLogin, AuthURL: authURL}, nil
			}
		}
		fakes = append(fakes, fake)
		return fake, nil
	}
	observe, events := phaseEvents()
	provider, err := NewProvider(testConfig(t),
		WithStatusObserver(observe),
		withBackendFactory(factory),
		withLoginWatchInterval(time.Millisecond),
	)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	if err := provider.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	fail.Store(true)
	waitForPhase(t, events, appconnection.ProviderPhaseError)
	if got := provider.Phase(); got != appconnection.ProviderPhaseError {
		t.Fatalf("Phase = %q, want error", got)
	}
	waitForBackendCloses(t, fakes[0], 1)
	if status := provider.Status(); status.AuthURL != "" || status.Err == nil {
		t.Fatalf("status after node failure = %+v, want a cleared login URL and a cause", status)
	}

	// The Provider stays retryable after a node failure.
	if err := provider.Start(context.Background()); err != nil {
		t.Fatalf("retry after node failure: %v", err)
	}
	if got := provider.Phase(); got != appconnection.ProviderPhaseRunning {
		t.Fatalf("Phase after retry = %q, want running", got)
	}
	if len(fakes) != 2 {
		t.Fatalf("backends opened = %d, want a fresh node for the retry", len(fakes))
	}
	if err := provider.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if fakes[0].closeCount() != 1 || fakes[1].closeCount() != 1 {
		t.Fatalf("backend closes = %d/%d, want one close per node",
			fakes[0].closeCount(), fakes[1].closeCount())
	}
}

func TestCloseWhileWaitingForLoginReleasesTheNodeOnce(t *testing.T) {
	authURL := "https://login.tailscale.com/a/example"
	fake := &fakeBackend{
		startFn: func(context.Context) (backendStatus, error) {
			return backendStatus{Phase: appconnection.ProviderPhaseNeedsLogin, AuthURL: authURL}, nil
		},
	}
	provider, _ := fakeProvider(t, testConfig(t), fake)
	if err := provider.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := provider.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if fake.closeCount() != 1 {
		t.Fatalf("backend closes = %d, want exactly one", fake.closeCount())
	}
	status := provider.Status()
	if status.Phase != appconnection.ProviderPhaseStopped || status.AuthURL != "" {
		t.Fatalf("status after Close = %+v, want stopped without a login URL", status)
	}
	if err := provider.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if fake.closeCount() != 1 {
		t.Fatalf("backend closes after a second Close = %d, want exactly one", fake.closeCount())
	}
}

func TestStartIsIdempotentWhileRunningOrWaitingForLogin(t *testing.T) {
	tests := []struct {
		name  string
		phase appconnection.ProviderPhase
	}{
		{"running", appconnection.ProviderPhaseRunning},
		{"needs login", appconnection.ProviderPhaseNeedsLogin},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			opened := 0
			fake := &fakeBackend{
				startFn: func(context.Context) (backendStatus, error) {
					return backendStatus{
						Phase:        test.phase,
						TailscaleIPs: []string{"100.64.0.2"},
						AuthURL:      "https://login.tailscale.com/a/example",
					}, nil
				},
			}
			provider, err := NewProvider(testConfig(t),
				withBackendFactory(func(Config) (backend, error) {
					opened++
					return fake, nil
				}),
				withLoginWatchInterval(time.Hour),
			)
			if err != nil {
				t.Fatalf("NewProvider: %v", err)
			}
			if err := provider.Start(context.Background()); err != nil {
				t.Fatalf("Start: %v", err)
			}
			if err := provider.Start(context.Background()); err != nil {
				t.Fatalf("second Start: %v", err)
			}
			if opened != 1 {
				t.Fatalf("backends opened = %d, want one node for an active Provider", opened)
			}
			if fake.closeCount() != 0 {
				t.Fatalf("backend closes = %d, want no replacement server", fake.closeCount())
			}
			if got := provider.Phase(); got != test.phase {
				t.Fatalf("Phase = %q, want %q", got, test.phase)
			}
			if err := provider.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if fake.closeCount() != 1 {
				t.Fatalf("backend closes = %d, want exactly one", fake.closeCount())
			}
		})
	}
}

func TestProfilesKeepIndependentNodesAndLoginState(t *testing.T) {
	base := t.TempDir()
	authURL := "https://login.tailscale.com/a/example"
	firstFake := &fakeBackend{
		startFn: func(context.Context) (backendStatus, error) {
			return backendStatus{Phase: appconnection.ProviderPhaseNeedsLogin, AuthURL: authURL}, nil
		},
	}
	secondFake := &fakeBackend{}
	first, err := NewProvider(Config{
		ProfileID: "net-one", Label: "One",
		Kind: domainvault.NetworkKindTailscale, StateBaseDir: base,
	}, withBackendFactory(func(Config) (backend, error) { return firstFake, nil }),
		withLoginWatchInterval(time.Hour))
	if err != nil {
		t.Fatalf("NewProvider first: %v", err)
	}
	second, err := NewProvider(Config{
		ProfileID: "net-two", Label: "Two",
		Kind: domainvault.NetworkKindTailscale, StateBaseDir: base,
	}, withBackendFactory(func(Config) (backend, error) { return secondFake, nil }))
	if err != nil {
		t.Fatalf("NewProvider second: %v", err)
	}
	if first.cfg.stateDir == second.cfg.stateDir {
		t.Fatalf("profiles share the state directory %q", first.cfg.stateDir)
	}
	for _, dir := range []string{first.cfg.stateDir, second.cfg.stateDir} {
		if !within(base, dir) {
			t.Fatalf("state directory %q escapes %q", dir, base)
		}
	}

	if err := first.Start(context.Background()); err != nil {
		t.Fatalf("start first: %v", err)
	}
	if err := second.Start(context.Background()); err != nil {
		t.Fatalf("start second: %v", err)
	}
	if first.Phase() != appconnection.ProviderPhaseNeedsLogin ||
		second.Phase() != appconnection.ProviderPhaseRunning {
		t.Fatalf("phases = %q/%q, want independent login states", first.Phase(), second.Phase())
	}
	if status := first.Status(); status.AuthURL != authURL {
		t.Fatalf("first AuthURL = %q, want the login URL", status.AuthURL)
	}
	if status := second.Status(); status.AuthURL != "" {
		t.Fatalf("second AuthURL = %q, want none", status.AuthURL)
	}

	// Cancelling the waiting profile must not disturb the running one.
	if err := first.Cancel(); err != nil {
		t.Fatalf("cancel first: %v", err)
	}
	if firstFake.closeCount() != 1 {
		t.Fatalf("first backend closes = %d, want exactly one", firstFake.closeCount())
	}
	if second.Phase() != appconnection.ProviderPhaseRunning {
		t.Fatalf("second Phase = %q, want running after the other profile was cancelled", second.Phase())
	}
	if secondFake.closeCount() != 0 {
		t.Fatalf("second backend closes = %d, want the other profile untouched", secondFake.closeCount())
	}
	conn, err := second.DialContext(context.Background(), "tcp", "peer.tailnet.ts.net:22")
	if err != nil {
		t.Fatalf("second DialContext: %v", err)
	}
	_ = conn.Close()

	if err := second.Close(); err != nil {
		t.Fatalf("close second: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first: %v", err)
	}
	if secondFake.closeCount() != 1 || firstFake.closeCount() != 1 {
		t.Fatalf("backend closes = %d/%d, want one close per node",
			firstFake.closeCount(), secondFake.closeCount())
	}
}

func TestStartFailureIsTypedWithProfileContext(t *testing.T) {
	fake := &fakeBackend{
		startFn: func(context.Context) (backendStatus, error) {
			return backendStatus{}, errors.New("control server refused the node")
		},
	}
	provider, _ := fakeProvider(t, testConfig(t), fake)

	err := provider.Start(context.Background())
	typed := typedProviderError(t, err)
	if typed.Kind != appconnection.KindProvider {
		t.Fatalf("Kind = %q, want provider", typed.Kind)
	}
	if typed.Provider.ProfileID != "net-1" || typed.Provider.Label != "Office" ||
		typed.Provider.Kind != string(domainvault.NetworkKindTailscale) {
		t.Fatalf("ProviderInfo = %+v", typed.Provider)
	}
	if typed.Provider.Phase != appconnection.ProviderPhaseError {
		t.Fatalf("Phase = %q, want error", typed.Provider.Phase)
	}
	if fake.closeCount() != 1 {
		t.Fatalf("backend closes = %d, want the failed node released", fake.closeCount())
	}
}

func TestCanceledStartLeavesNoLiveServer(t *testing.T) {
	entered := make(chan struct{})
	fake := &fakeBackend{
		started: entered,
		startFn: func(ctx context.Context) (backendStatus, error) {
			<-ctx.Done()
			return backendStatus{}, ctx.Err()
		},
	}
	provider, recorder := fakeProvider(t, testConfig(t), fake)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- provider.Start(ctx) }()
	<-entered
	cancel()

	err := <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Start error = %v, want context.Canceled", err)
	}
	// A cancelled start is a normal stop for the Vault surface: the caller still
	// gets the cancellation, but no error row is published.
	events := recorder.all()
	if len(events) == 0 {
		t.Fatal("the cancelled start published no status")
	}
	if last := events[len(events)-1]; last.Phase != appconnection.ProviderPhaseStopped || last.Err != nil {
		t.Fatalf("last status after a cancelled start = %+v, want a clean stopped status", last)
	}
	if fake.closeCount() != 1 {
		t.Fatalf("backend closes = %d, want the canceled start released", fake.closeCount())
	}
	if got := provider.Phase(); got != appconnection.ProviderPhaseStopped {
		t.Fatalf("Phase = %q, want stopped", got)
	}
	if err := provider.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if fake.closeCount() != 1 {
		t.Fatalf("backend closes after Close = %d, want exactly one", fake.closeCount())
	}
}

// TestExpiredLoginWaitPublishesACleanStoppedStatus covers a caller context that
// expires while a start is waiting on the login path. The backend can still
// report the last NeedsLogin snapshot it read, but that node is closed and no
// interactive login is live: the Provider must publish a stopped status with
// neither a dead Auth URL nor an error, and the next Start must open a fresh
// node.
func TestExpiredLoginWaitPublishesACleanStoppedStatus(t *testing.T) {
	const authURL = "https://login.tailscale.com/a/expired"
	tests := map[string]struct {
		expired error
		ctx     func(*testing.T) context.Context
	}{
		"canceled": {
			expired: context.Canceled,
			ctx: func(*testing.T) context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
		},
		"deadline": {
			expired: context.DeadlineExceeded,
			ctx: func(t *testing.T) context.Context {
				ctx, cancel := context.WithDeadline(context.Background(),
					time.Now().Add(-time.Second))
				t.Cleanup(cancel)
				return ctx
			},
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			var mu sync.Mutex
			var fakes []*fakeBackend
			factory := func(Config) (backend, error) {
				mu.Lock()
				defer mu.Unlock()
				fake := &fakeBackend{}
				if len(fakes) == 0 {
					fake.startFn = func(ctx context.Context) (backendStatus, error) {
						// This is the snapshot a backend returns when the caller's
						// context ends while the keyed login wait is pending.
						return backendStatus{
							Phase:   appconnection.ProviderPhaseNeedsLogin,
							AuthURL: authURL,
						}, test.expired
					}
				}
				fakes = append(fakes, fake)
				return fake, nil
			}
			recorder := &statusRecorder{}
			provider, err := NewProvider(testConfig(t),
				WithStatusObserver(recorder.observe),
				withBackendFactory(factory),
				withLoginWatchInterval(time.Hour),
				withPollInterval(time.Hour),
			)
			if err != nil {
				t.Fatalf("NewProvider: %v", err)
			}
			t.Cleanup(func() { _ = provider.Close() })

			err = provider.Start(test.ctx(t))
			if !errors.Is(err, test.expired) {
				t.Fatalf("Start error = %v, want %v", err, test.expired)
			}
			typed := typedProviderError(t, err)
			wantKind := appconnection.KindCancelled
			if errors.Is(test.expired, context.DeadlineExceeded) {
				wantKind = appconnection.KindTimeout
			}
			if typed.Kind != wantKind ||
				typed.Provider.Phase != appconnection.ProviderPhaseStopped {
				t.Fatalf("typed error = %+v, want kind %q and phase stopped",
					typed, wantKind)
			}
			if got := provider.Phase(); got != appconnection.ProviderPhaseStopped {
				t.Fatalf("Phase = %q, want stopped", got)
			}
			status := provider.Status()
			if status.Phase != appconnection.ProviderPhaseStopped ||
				status.AuthURL != "" || status.Err != nil {
				t.Fatalf("status after an expired login wait = %+v, want a clean stopped status", status)
			}
			events := recorder.all()
			if len(events) == 0 {
				t.Fatal("the expired start published no status")
			}
			if last := events[len(events)-1]; last.Phase != appconnection.ProviderPhaseStopped ||
				last.AuthURL != "" || last.Err != nil {
				t.Fatalf("last status = %+v, want a clean stopped status", last)
			}
			if fakes[0].closeCount() != 1 {
				t.Fatalf("backend closes = %d, want the expired node released exactly once",
					fakes[0].closeCount())
			}

			// The expired start stays retryable: a fresh Start opens a new node
			// and never reuses the dead login.
			if retryErr := provider.Start(context.Background()); retryErr != nil {
				t.Fatalf("retry after an expired login wait: %v", retryErr)
			}
			if got := provider.Phase(); got != appconnection.ProviderPhaseRunning {
				t.Fatalf("Phase after retry = %q, want running", got)
			}
			if len(fakes) != 2 {
				t.Fatalf("backends opened = %d, want a fresh node for the retry", len(fakes))
			}
			if fakes[0].closeCount() != 1 || fakes[1].closeCount() != 0 {
				t.Fatalf("backend closes = %d/%d, want the retry node still open",
					fakes[0].closeCount(), fakes[1].closeCount())
			}
		})
	}
}

func TestCloseIsIdempotentAndClosesTheServerOnce(t *testing.T) {
	fake := &fakeBackend{}
	provider, _ := fakeProvider(t, testConfig(t), fake)
	if err := provider.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := provider.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := provider.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if fake.closeCount() != 1 {
		t.Fatalf("backend closes = %d, want exactly one", fake.closeCount())
	}
	if _, err := provider.DialContext(context.Background(), "tcp", "peer:22"); err == nil {
		t.Fatal("DialContext succeeded on a closed provider")
	}
}

func TestDialRejectsNetworksOtherThanTCP(t *testing.T) {
	fake := &fakeBackend{}
	provider, _ := fakeProvider(t, testConfig(t), fake)
	if err := provider.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	for _, network := range []string{"udp", "udp4", "udp6", "unix"} {
		_, err := provider.DialContext(context.Background(), network, "peer:22")
		if err == nil {
			t.Fatalf("DialContext(%q) succeeded", network)
		}
		typed := typedProviderError(t, err)
		if typed.Kind != appconnection.KindUnsupported {
			t.Fatalf("DialContext(%q) Kind = %q, want unsupported", network, typed.Kind)
		}
	}
	if calls, _, _ := fake.dialCalls(); calls != 0 {
		t.Fatalf("backend dials = %d, want none", calls)
	}
}

func TestDialDatagramAcceptsUDPOnlyAndUsesTheSameNode(t *testing.T) {
	fake := &fakeBackend{}
	provider, _ := fakeProvider(t, testConfig(t), fake)
	if err := provider.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	for _, network := range []string{"tcp", "tcp4", "tcp6", "unix", ""} {
		_, err := provider.DialDatagram(context.Background(), network, "peer:60001")
		if err == nil {
			t.Fatalf("DialDatagram(%q) succeeded", network)
		}
		typed := typedProviderError(t, err)
		if typed.Kind != appconnection.KindUnsupported {
			t.Fatalf("DialDatagram(%q) Kind = %q, want unsupported", network, typed.Kind)
		}
	}
	if calls, _, _ := fake.dialCalls(); calls != 0 {
		t.Fatalf("backend dials = %d, want none for non-UDP networks", calls)
	}

	conn, err := provider.DialDatagram(context.Background(), "udp", "peer.example:60001")
	if err != nil {
		t.Fatalf("DialDatagram: %v", err)
	}
	_ = conn.Close()
	calls, networks, addresses := fake.dialCalls()
	if calls != 1 || networks[0] != "udp" || addresses[0] != "peer.example:60001" {
		t.Fatalf("backend dials = %d %v %v, want one udp dial", calls, networks, addresses)
	}
}

func TestAuthKeyNeverAppearsInErrorsLogsOrTheStateDirectory(t *testing.T) {
	cfg := testConfig(t)
	var logMu sync.Mutex
	var logs []string
	logf := func(format string, args ...any) {
		logMu.Lock()
		logs = append(logs, fmt.Sprintf(format, args...))
		logMu.Unlock()
	}
	fake := &fakeBackend{
		startFn: func(context.Context) (backendStatus, error) {
			return backendStatus{}, fmt.Errorf("control rejected key %s", testAuthKey)
		},
	}
	recorder := &statusRecorder{}
	provider, err := NewProvider(cfg,
		WithLogf(logf),
		WithStatusObserver(recorder.observe),
		withBackendFactory(func(Config) (backend, error) { return fake, nil }),
	)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}

	startErr := provider.Start(context.Background())
	if startErr == nil {
		t.Fatal("Start succeeded with a failing backend")
	}
	if strings.Contains(startErr.Error(), testAuthKey) {
		t.Fatalf("start error leaked the Auth Key: %q", startErr)
	}
	for _, event := range recorder.all() {
		if event.Err != nil && strings.Contains(event.Err.Error(), testAuthKey) {
			t.Fatalf("status event leaked the Auth Key: %q", event.Err)
		}
	}

	redactingLogf(logf, testAuthKey)("control rejected key %s", testAuthKey)
	logMu.Lock()
	for _, line := range logs {
		if strings.Contains(line, testAuthKey) {
			t.Fatalf("log line leaked the Auth Key: %q", line)
		}
	}
	logMu.Unlock()

	stateDir := provider.cfg.stateDir
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		t.Fatalf("read state dir: %v", err)
	}
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(stateDir, entry.Name()))
		if err != nil {
			t.Fatalf("read state file: %v", err)
		}
		if strings.Contains(string(data), testAuthKey) {
			t.Fatalf("state file %q contains the Auth Key", entry.Name())
		}
	}
	if strings.Contains(stateDir, testAuthKey) {
		t.Fatalf("state path contains the Auth Key: %q", stateDir)
	}
}

func TestBackendConfigurationIsDerivedFromTheProfile(t *testing.T) {
	cfg := testConfig(t)
	cfg.Hostname = "My Node!"
	cfg.ControlURL = "https://headscale.example.com"
	cfg.Ephemeral = true
	cfg.AdvertiseTags = []string{"tag:prod", " tag:prod ", "tag:ops"}

	provider, err := NewProvider(cfg)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	raw, err := newTSNetBackend(provider.cfg)
	if err != nil {
		t.Fatalf("newTSNetBackend: %v", err)
	}
	backend := raw.(*tsnetBackend)
	if backend.server.Dir != provider.cfg.stateDir {
		t.Fatalf("server.Dir = %q, want %q", backend.server.Dir, provider.cfg.stateDir)
	}
	if backend.server.AuthKey != testAuthKey {
		t.Fatalf("server.AuthKey = %q, want the profile key", backend.server.AuthKey)
	}
	if backend.server.Hostname != "my-node" {
		t.Fatalf("server.Hostname = %q, want my-node", backend.server.Hostname)
	}
	if backend.server.ControlURL != cfg.ControlURL || !backend.server.Ephemeral {
		t.Fatalf("server ControlURL/Ephemeral = %q/%t", backend.server.ControlURL, backend.server.Ephemeral)
	}
	if len(backend.server.AdvertiseTags) != 2 ||
		backend.server.AdvertiseTags[0] != "tag:prod" || backend.server.AdvertiseTags[1] != "tag:ops" {
		t.Fatalf("AdvertiseTags = %v", backend.server.AdvertiseTags)
	}
	if isSystemTailscaleDir(backend.server.Dir) {
		t.Fatalf("server.Dir points at a system Tailscale directory: %q", backend.server.Dir)
	}
}

func TestStateDirectoryIsIsolatedPerProfileAndPrivate(t *testing.T) {
	base := t.TempDir()
	first, err := prepareStateDir(base, "net-one")
	if err != nil {
		t.Fatalf("prepareStateDir first: %v", err)
	}
	second, err := prepareStateDir(base, "net-two")
	if err != nil {
		t.Fatalf("prepareStateDir second: %v", err)
	}
	if first == second {
		t.Fatalf("two profiles share the state directory %q", first)
	}
	wantBase := canonicalTestPath(t, base)
	for _, dir := range []string{first, second} {
		if !within(wantBase, dir) {
			t.Fatalf("state directory %q escapes %q", dir, wantBase)
		}
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("stat %q: %v", dir, err)
		}
		if !info.IsDir() {
			t.Fatalf("%q is not a directory", dir)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
			t.Fatalf("%q mode = %o, want 700", dir, info.Mode().Perm())
		}
	}
}

func TestStateDirectoryRejectsPathTraversalProfileIDs(t *testing.T) {
	base := t.TempDir()
	dir, err := prepareStateDir(base, "../../escape")
	if err != nil {
		t.Fatalf("prepareStateDir: %v", err)
	}
	wantBase := canonicalTestPath(t, base)
	if !within(wantBase, dir) {
		t.Fatalf("state directory %q escapes %q", dir, wantBase)
	}
	if strings.Contains(filepath.Base(dir), string(filepath.Separator)) {
		t.Fatalf("state directory name still contains a separator: %q", dir)
	}
	if filepath.Dir(dir) != filepath.Join(wantBase, "tailscale") {
		t.Fatalf("state directory parent = %q", filepath.Dir(dir))
	}
}

func TestSystemTailscaleDirectoriesAreRecognized(t *testing.T) {
	if isSystemTailscaleDir(t.TempDir()) {
		t.Fatal("a temporary directory was classified as system Tailscale state")
	}
	roots := systemTailscaleDirs()
	if len(roots) == 0 {
		t.Skip("this platform exposes no known system Tailscale directory")
	}
	for _, root := range roots {
		if !isSystemTailscaleDir(root) || !isSystemTailscaleDir(filepath.Join(root, "tailscaled.state")) {
			t.Fatalf("%q is not classified as system Tailscale state", root)
		}
	}
}

func TestNodeHostnameSanitization(t *testing.T) {
	tests := map[string]string{
		"My Node!":               "my-node",
		"  office.tailnet  ":     "office-tailnet",
		"___":                    "",
		"UPPER_case-123":         "upper-case-123",
		"控制台":                    "",
		strings.Repeat("a", 100): strings.Repeat("a", 63),
		"a..b__c":                "a-b-c",
		"nodeé":                  "node",
	}
	for input, want := range tests {
		if got := sanitizeHostname(input); got != want {
			t.Errorf("sanitizeHostname(%q) = %q, want %q", input, got, want)
		}
	}

	cfg := testConfig(t)
	cfg.Hostname = ""
	cfg.Label = "My Office"
	provider, err := NewProvider(cfg)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	if provider.cfg.nodeHostname != "natcatty-my-office" {
		t.Fatalf("derived hostname = %q", provider.cfg.nodeHostname)
	}
}

// TestDialChecksALivePhaseEvenWhenBackendStatusFails covers the same race as
// the WireGuard regression: a backend that cannot read its live state (and
// therefore reports no phase) must fail closed as KindProvider, never as an
// ordinary network failure with a stale running phase.
func TestDialChecksALivePhaseEvenWhenBackendStatusFails(t *testing.T) {
	fake := &fakeBackend{
		dialFn: func(context.Context, string, string) (net.Conn, error) {
			return nil, errors.New("tailscale: node is not running")
		},
		statusFn: func(context.Context) (backendStatus, error) {
			return backendStatus{}, errors.New("tailscale: read live state: LocalClient unavailable")
		},
	}
	provider, _ := fakeProvider(t, testConfig(t), fake)
	if err := provider.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	conn, err := provider.DialContext(context.Background(), "tcp", "peer.tailnet.ts.net:22")
	if conn != nil {
		t.Fatal("a dial with an unreadable live state returned a connection")
	}
	typed := typedProviderError(t, err)
	if typed.Kind != appconnection.KindProvider {
		t.Fatalf("Kind = %q, want provider: %v", typed.Kind, err)
	}
	if typed.Provider.Phase != appconnection.ProviderPhaseError {
		t.Fatalf("Phase = %q, want error", typed.Provider.Phase)
	}
	if typed.Provider.ProfileID != "net-1" {
		t.Fatalf("ProfileID = %q, want net-1", typed.Provider.ProfileID)
	}
	if provider.Phase() != appconnection.ProviderPhaseError {
		t.Fatalf("Provider phase = %q, want error", provider.Phase())
	}
}

// TestDialChecksAStoppedPhaseReportedWithAnError covers a status that carries
// both a definitive phase and an error.
func TestDialChecksAStoppedPhaseReportedWithAnError(t *testing.T) {
	fake := &fakeBackend{
		dialFn: func(context.Context, string, string) (net.Conn, error) {
			return nil, errors.New("tailscale: node is not running")
		},
		statusFn: func(context.Context) (backendStatus, error) {
			return backendStatus{Phase: appconnection.ProviderPhaseStopped},
				errors.New("tsnet: server closed")
		},
	}
	provider, _ := fakeProvider(t, testConfig(t), fake)
	if err := provider.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	_, err := provider.DialContext(context.Background(), "tcp", "peer.tailnet.ts.net:22")
	typed := typedProviderError(t, err)
	if typed.Kind != appconnection.KindProvider ||
		typed.Provider.Phase != appconnection.ProviderPhaseStopped {
		t.Fatalf("error = %v, want KindProvider/stopped", err)
	}
}

// TestClosePublishesStoppingAndStopped proves shutdown reaches the Vault row
// and the Connection Log instead of leaving the last running status in place.
func TestClosePublishesStoppingAndStopped(t *testing.T) {
	fake := &fakeBackend{}
	provider, recorder := fakeProvider(t, testConfig(t), fake)
	if err := provider.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := provider.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	statuses := recorder.all()
	sawStopping := false
	for _, status := range statuses {
		if status.Phase == appconnection.ProviderPhaseStopping {
			sawStopping = true
		}
	}
	if !sawStopping {
		t.Fatalf("status events = %+v, want a stopping event", statuses)
	}
	if len(statuses) == 0 || statuses[len(statuses)-1].Phase != appconnection.ProviderPhaseStopped {
		t.Fatalf("last status = %+v, want stopped", statuses)
	}
}

// TestStatusPollReportsAuthorizationLossAndTypesTheNextIO proves losing the
// tailnet authorization after startup reaches the status surface without
// another dial, keeps the node for a possible login, and turns an active
// connection's next failed I/O into a typed KindProvider error.
func TestStatusPollReportsAuthorizationLossAndTypesTheNextIO(t *testing.T) {
	const authURL = "https://login.tailscale.com/a/lost"
	var lost atomic.Bool
	fake := &fakeBackend{
		statusFn: func(context.Context) (backendStatus, error) {
			if lost.Load() {
				return backendStatus{
					Phase:   appconnection.ProviderPhaseNeedsLogin,
					AuthURL: authURL,
				}, nil
			}
			return backendStatus{
				Phase:        appconnection.ProviderPhaseRunning,
				TailscaleIPs: []string{"100.64.0.2"},
			}, nil
		},
	}
	provider, observed := pollingProvider(t, testConfig(t), fake, 2*time.Millisecond)
	if err := provider.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	conn, err := provider.DialContext(context.Background(), "tcp", "peer.tailnet.ts.net:22")
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer func() { _ = conn.Close() }()

	lost.Store(true)
	status := waitForStatus(t, observed, appconnection.ProviderPhaseNeedsLogin)
	if status.AuthURL != authURL {
		t.Fatalf("needs-login status AuthURL = %q, want %q", status.AuthURL, authURL)
	}
	if got := provider.Phase(); got != appconnection.ProviderPhaseNeedsLogin {
		t.Fatalf("phase = %q, want needs_login", got)
	}
	if fake.closeCount() != 0 {
		t.Fatalf("backend closes = %d, want the node kept for a login", fake.closeCount())
	}

	_, err = conn.Read(make([]byte, 1))
	typed := typedProviderError(t, err)
	if typed.Kind != appconnection.KindProvider {
		t.Fatalf("next I/O Kind = %q, want provider", typed.Kind)
	}
	if typed.Provider.Phase != appconnection.ProviderPhaseNeedsLogin {
		t.Fatalf("next I/O phase = %q, want needs_login", typed.Provider.Phase)
	}

	// The authorization can return while the same node stays alive; the poll
	// must publish the recovery and the next I/O is an ordinary transport error
	// again.
	lost.Store(false)
	waitForStatus(t, observed, appconnection.ProviderPhaseRunning)
	if got := provider.Phase(); got != appconnection.ProviderPhaseRunning {
		t.Fatalf("phase after recovery = %q, want running", got)
	}
	if fake.closeCount() != 0 {
		t.Fatalf("backend closes after recovery = %d, want the same node", fake.closeCount())
	}
}

// TestStatusPollFailsClosedWhenTheNodeDies proves an unexpected stop or an
// unreadable live state releases the node and publishes an error phase, so a
// retry can never silently reuse a dead tunnel.
func TestStatusPollFailsClosedWhenTheNodeDies(t *testing.T) {
	tests := map[string]func(context.Context) (backendStatus, error){
		"stopped": func(context.Context) (backendStatus, error) {
			return backendStatus{Phase: appconnection.ProviderPhaseStopped}, nil
		},
		"unreadable": func(context.Context) (backendStatus, error) {
			return backendStatus{}, errors.New("local client unavailable")
		},
	}
	for name, statusFn := range tests {
		t.Run(name, func(t *testing.T) {
			fake := &fakeBackend{statusFn: statusFn}
			provider, _ := pollingProvider(t, testConfig(t), fake, 2*time.Millisecond)
			if err := provider.Start(context.Background()); err != nil {
				t.Fatalf("Start: %v", err)
			}

			waitForProviderPhase(t, provider, appconnection.ProviderPhaseError)
			waitForBackendCloses(t, fake, 1)
			if provider.Status().Err == nil {
				t.Fatal("the error phase carries no failure reason")
			}
			provider.mu.Lock()
			attached := provider.backend
			provider.mu.Unlock()
			if attached != nil {
				t.Fatal("the dead backend is still attached")
			}
		})
	}
}

// TestCloseKeepsATerminalStartFailureStatus proves releasing a Provider after a
// failed start does not overwrite the reason (or the login URL) the Vault row
// and the Connection Log need. The opener closes the adapter on a Start error,
// so a clean stopped status would otherwise erase every failure.
func TestCloseKeepsATerminalStartFailureStatus(t *testing.T) {
	const authURL = "https://login.tailscale.com/a/example"
	tests := map[string]struct {
		startFn func(context.Context) (backendStatus, error)
		phase   appconnection.ProviderPhase
		authURL string
		wantErr bool
	}{
		"error": {
			startFn: func(context.Context) (backendStatus, error) {
				return backendStatus{}, errors.New("control server refused the node")
			},
			phase:   appconnection.ProviderPhaseError,
			wantErr: true,
		},
		"needs login": {
			// A login-needing node is normally kept alive for the browser
			// login; a NeedsLogin status that ends a start is the terminal case
			// where the backend also reports an error (for example, an Auth Key
			// that did not complete the login within its grace period).
			startFn: func(context.Context) (backendStatus, error) {
				return backendStatus{
					Phase:   appconnection.ProviderPhaseNeedsLogin,
					AuthURL: authURL,
				}, errors.New("interactive login did not complete")
			},
			phase:   appconnection.ProviderPhaseNeedsLogin,
			authURL: authURL,
			wantErr: true,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			fake := &fakeBackend{startFn: test.startFn}
			provider, recorder := fakeProvider(t, testConfig(t), fake)
			if err := provider.Start(context.Background()); err == nil {
				t.Fatal("Start succeeded with a failing start")
			}
			events := len(recorder.all())
			if err := provider.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			statuses := recorder.all()
			if len(statuses) != events {
				t.Fatalf("Close published %d extra statuses: %+v", len(statuses)-events, statuses[events:])
			}
			last := statuses[len(statuses)-1]
			if last.Phase != test.phase || last.AuthURL != test.authURL {
				t.Fatalf("last status = %+v, want phase %q and AuthURL %q",
					last, test.phase, test.authURL)
			}
			if test.wantErr != (last.Err != nil) {
				t.Fatalf("last status error = %v, wantErr %v", last.Err, test.wantErr)
			}
		})
	}
}

// TestProviderConnClassifiesIOFailuresAfterClose proves an active connection
// reports KindProvider on its next I/O after the node was closed.
func TestProviderConnClassifiesIOFailuresAfterClose(t *testing.T) {
	fake := &fakeBackend{}
	provider, _ := fakeProvider(t, testConfig(t), fake)
	if err := provider.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	conn, err := provider.DialContext(context.Background(), "tcp", "peer.tailnet.ts.net:22")
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	if err := provider.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_, err = conn.Read(make([]byte, 1))
	typed := typedProviderError(t, err)
	if typed.Kind != appconnection.KindProvider {
		t.Fatalf("Kind = %q, want provider", typed.Kind)
	}
	if typed.Provider.Phase != appconnection.ProviderPhaseStopped {
		t.Fatalf("Phase = %q, want stopped", typed.Provider.Phase)
	}
}
