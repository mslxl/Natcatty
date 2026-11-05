// SPDX-License-Identifier: GPL-3.0-or-later

package connection

import (
	"context"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mslxl/natcatty/domain/model"
)

// TestNetworkProviderRegistryRetiresAStoppedProviderAndReopens proves a cached
// Provider that died after its successful open is not handed to a new dial.
func TestNetworkProviderRegistryRetiresAStoppedProviderAndReopens(t *testing.T) {
	first := &fakeNetworkProvider{}
	first.setPhase(ProviderPhaseStopped)
	second := &fakeNetworkProvider{}
	var calls atomic.Int32
	factory := &fakeProviderFactory{openFn: func(context.Context, NetworkRoute) (NetworkProvider, error) {
		if calls.Add(1) == 1 {
			return first, nil
		}
		return second, nil
	}}
	registry := NewNetworkProviderRegistry(factory)
	t.Cleanup(func() { _ = registry.CloseAll() })

	provider, err := registry.Acquire(context.Background(), testNetworkRoute("profile-a", "fp-1"))
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if provider != second {
		t.Fatalf("Acquire returned %p, want the reopened provider %p", provider, second)
	}
	if first.closeCount() != 1 {
		t.Fatalf("dead provider closes = %d, want exactly 1", first.closeCount())
	}
	if factory.openCount() != 2 {
		t.Fatalf("opens = %d, want one retry after the dead provider", factory.openCount())
	}

	// A login prompt is a live lifecycle, not a dead provider: it is returned
	// unchanged so the Manager can report the accurate phase.
	second.setPhase(ProviderPhaseNeedsLogin)
	provider, err = registry.Acquire(context.Background(), testNetworkRoute("profile-a", "fp-1"))
	if err != nil {
		t.Fatalf("Acquire with needs-login: %v", err)
	}
	if provider != second {
		t.Fatal("a needs-login provider was replaced instead of reported")
	}
	if factory.openCount() != 2 || second.closeCount() != 0 {
		t.Fatalf("needs-login caused opens=%d closes=%d, want no replacement",
			factory.openCount(), second.closeCount())
	}
}

// TestManagerFailsClosedWhenTheProviderNeedsLogin proves a selected profile
// never falls back to a direct dial while the provider is not running.
func TestManagerFailsClosedWhenTheProviderNeedsLogin(t *testing.T) {
	provider := &fakeNetworkProvider{}
	provider.setPhase(ProviderPhaseNeedsLogin)
	factory := &fakeProviderFactory{openFn: func(context.Context, NetworkRoute) (NetworkProvider, error) {
		return provider, nil
	}}
	registry := NewNetworkProviderRegistry(factory)
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
	if typed.Provider == nil || typed.Provider.Phase != ProviderPhaseNeedsLogin {
		t.Fatalf("ProviderInfo = %+v, want needs_login", typed.Provider)
	}
	if !errors.Is(err, ErrNetworkProviderNotRunning) {
		t.Fatalf("error %v does not wrap ErrNetworkProviderNotRunning", err)
	}
	if dialer.dialCount() != 0 {
		t.Fatalf("dialer calls = %d, want no direct dial", dialer.dialCount())
	}
}

// TestManagerRefusesToReuseAParkedTransportWhileTheProviderIsNotRunning proves
// the fail-closed check happens before the pool, not only on a fresh dial.
func TestManagerRefusesToReuseAParkedTransportWhileTheProviderIsNotRunning(t *testing.T) {
	provider := &fakeNetworkProvider{}
	registry := NewNetworkProviderRegistry(&fakeProviderFactory{openFn: func(
		context.Context, NetworkRoute) (NetworkProvider, error) {
		return provider, nil
	}})
	dialer := &sequenceDialer{}
	manager := poolTestManager(t, dialer, WithNetworkProviders(registry))
	req := sshRequest()
	req.Network = &NetworkRoute{ProfileID: "profile-a", Kind: "tailscale", Fingerprint: "fp-1"}

	first, err := manager.Acquire(context.Background(), req)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if dialer.dialCount() != 1 {
		t.Fatalf("dials = %d, want 1", dialer.dialCount())
	}

	// NeedsLogin is not a dead provider (the login can still complete), so the
	// registry keeps it and the Manager must refuse the parked transport.
	provider.setPhase(ProviderPhaseNeedsLogin)
	_, err = manager.Acquire(context.Background(), req)
	typed := typedConnectionError(t, err)
	if typed.Kind != KindProvider || typed.Provider.Phase != ProviderPhaseNeedsLogin {
		t.Fatalf("Acquire while needs-login = %v, want KindProvider/needs_login", err)
	}
	if dialer.dialCount() != 1 {
		t.Fatalf("dials = %d, the parked transport was reused while the provider was not running",
			dialer.dialCount())
	}

	// Once the login completes, the same profile reuses its parked transport
	// instead of discarding it.
	provider.setPhase(ProviderPhaseRunning)
	second, err := manager.Acquire(context.Background(), req)
	if err != nil {
		t.Fatalf("Acquire after recovery: %v", err)
	}
	_ = second.Release()
	if dialer.dialCount() != 1 || second.conn != first.conn {
		t.Fatalf("dials = %d, want the recovered profile's parked transport reused", dialer.dialCount())
	}
}

// TestActiveNetworkProfileLeasesCountsLiveTransports proves the Vault's delete
// warning sees both leases on a shared transport and nothing for idle ones.
func TestActiveNetworkProfileLeasesCountsLiveTransports(t *testing.T) {
	factory := &fakeProviderFactory{}
	registry := NewNetworkProviderRegistry(factory)
	dialer := &sequenceDialer{}
	manager := poolTestManager(t, dialer, WithNetworkProviders(registry))
	req := sshRequest()
	req.Network = &NetworkRoute{ProfileID: "profile-a", Kind: "tailscale", Fingerprint: "fp-1"}

	first, err := manager.Acquire(context.Background(), req)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	second, err := manager.Acquire(context.Background(), req)
	if err != nil {
		t.Fatalf("second Acquire: %v", err)
	}
	if got := manager.ActiveNetworkProfileLeases("profile-a"); got != 2 {
		t.Fatalf("active leases = %d, want 2", got)
	}
	if got := manager.ActiveNetworkProfileLeases("profile-b"); got != 0 {
		t.Fatalf("other profile leases = %d, want 0", got)
	}
	_ = first.Release()
	if got := manager.ActiveNetworkProfileLeases("profile-a"); got != 1 {
		t.Fatalf("active leases after one release = %d, want 1", got)
	}
	_ = second.Release()
	if got := manager.ActiveNetworkProfileLeases("profile-a"); got != 0 {
		t.Fatalf("active leases after release = %d, want 0", got)
	}
	if dialer.dialCount() != 1 {
		t.Fatalf("dials = %d, want one shared transport", dialer.dialCount())
	}
	if got := factory.openCount(); got != 1 {
		t.Fatalf("provider opens = %d, want 1", got)
	}
}

// TestManagerRetryStaysOnTheSameProfileAfterAProviderFailure proves a manual
// retry resolves the same Network Profile and never falls back to a direct
// dial.
func TestManagerRetryStaysOnTheSameProfileAfterAProviderFailure(t *testing.T) {
	var calls atomic.Int32
	provider := &fakeNetworkProvider{}
	factory := &fakeProviderFactory{openFn: func(_ context.Context,
		route NetworkRoute) (NetworkProvider, error) {
		if calls.Add(1) == 1 {
			return nil, errors.New("not logged in")
		}
		return provider, nil
	}}
	registry := NewNetworkProviderRegistry(factory)
	dialer := &fakeDialer{}
	manager := NewManager(dialer, &fakePasswordSource{password: "pw"}, &fakeKnownHosts{},
		WithNetworkProviders(registry))
	t.Cleanup(manager.CloseAll)
	req := sshRequest()
	req.Network = &NetworkRoute{ProfileID: "profile-a", Kind: "tailscale", Fingerprint: "fp-1"}

	if _, err := manager.Acquire(context.Background(), req); err == nil {
		t.Fatal("the first Acquire succeeded with a failing Provider")
	}
	if dialer.dialCount() != 0 {
		t.Fatalf("dials after a Provider failure = %d, want no direct fallback", dialer.dialCount())
	}

	lease, err := manager.Acquire(context.Background(), req)
	if err != nil {
		t.Fatalf("retry Acquire: %v", err)
	}
	_ = lease.Release()
	if dialer.dialCount() != 1 {
		t.Fatalf("dials after retry = %d, want 1", dialer.dialCount())
	}
	request := dialer.request()
	if request.Network == nil || request.Network.ProfileID != "profile-a" {
		t.Fatalf("retry Network = %+v, want the same profile", request.Network)
	}
	if request.Provider != provider {
		t.Fatal("the retry did not use the reopened Provider")
	}
	opened := factory.routes
	if len(opened) != 2 || opened[0].ProfileID != "profile-a" || opened[1].ProfileID != "profile-a" {
		t.Fatalf("factory routes = %+v, want two attempts on profile-a", opened)
	}
}

// TestOneProviderFailureDoesNotAffectAnotherProfile proves profiles are
// isolated at runtime.
func TestOneProviderFailureDoesNotAffectAnotherProfile(t *testing.T) {
	good := &fakeNetworkProvider{}
	factory := &fakeProviderFactory{openFn: func(_ context.Context,
		route NetworkRoute) (NetworkProvider, error) {
		if route.ProfileID == "profile-bad" {
			return nil, errors.New("auth expired")
		}
		return good, nil
	}}
	registry := NewNetworkProviderRegistry(factory)
	dialer := &fakeDialer{}
	manager := NewManager(dialer, &fakePasswordSource{password: "pw"}, &fakeKnownHosts{},
		WithNetworkProviders(registry))
	t.Cleanup(manager.CloseAll)

	bad := sshRequest()
	bad.Network = &NetworkRoute{ProfileID: "profile-bad", Kind: "tailscale", Fingerprint: "fp-1"}
	if _, err := manager.Acquire(context.Background(), bad); err == nil {
		t.Fatal("the failing profile did not fail")
	}
	goodRequest := sshRequest()
	goodRequest.HostID = "host-2"
	goodRequest.Network = &NetworkRoute{ProfileID: "profile-good", Kind: "tailscale", Fingerprint: "fp-1"}
	lease, err := manager.Acquire(context.Background(), goodRequest)
	if err != nil {
		t.Fatalf("Acquire through the healthy profile: %v", err)
	}
	_ = lease.Release()
	request := dialer.request()
	if request.Provider != good || request.Network.ProfileID != "profile-good" {
		t.Fatalf("healthy profile dial = %+v, want its own Provider", request.Network)
	}
	if dialer.dialCount() != 1 {
		t.Fatalf("dials = %d, want one through the healthy profile", dialer.dialCount())
	}
}

// TestManagerCloseAllClosesEveryProviderExactlyOnce covers application exit.
func TestManagerCloseAllClosesEveryProviderExactlyOnce(t *testing.T) {
	factory := &fakeProviderFactory{}
	registry := NewNetworkProviderRegistry(factory)
	manager := poolTestManager(t, &sequenceDialer{}, WithNetworkProviders(registry))

	for _, profile := range []string{"profile-a", "profile-b"} {
		req := sshRequest()
		req.HostID = modelHostIDForTest(profile)
		req.Network = &NetworkRoute{ProfileID: profile, Kind: "tailscale", Fingerprint: "fp-1"}
		lease, err := manager.Acquire(context.Background(), req)
		if err != nil {
			t.Fatalf("Acquire %s: %v", profile, err)
		}
		_ = lease.Release()
	}

	manager.CloseAll()
	manager.CloseAll()
	providers := factory.openedProviders()
	if len(providers) != 2 {
		t.Fatalf("opened providers = %d, want 2", len(providers))
	}
	for i, provider := range providers {
		if got := provider.closeCount(); got != 1 {
			t.Fatalf("provider %d closes = %d, want exactly 1", i, got)
		}
	}
}

func modelHostIDForTest(profile string) model.HostID {
	return model.HostID("host-" + profile)
}

// failingConn returns a scripted error for Read and Write.
type failingConn struct{ err error }

func (c failingConn) Read([]byte) (int, error)         { return 0, c.err }
func (c failingConn) Write([]byte) (int, error)        { return 0, c.err }
func (c failingConn) Close() error                     { return nil }
func (c failingConn) LocalAddr() net.Addr              { return stubTestAddr("local") }
func (c failingConn) RemoteAddr() net.Addr             { return stubTestAddr("remote") }
func (c failingConn) SetDeadline(time.Time) error      { return nil }
func (c failingConn) SetReadDeadline(time.Time) error  { return nil }
func (c failingConn) SetWriteDeadline(time.Time) error { return nil }

type stubTestAddr string

func (a stubTestAddr) Network() string { return "stub" }
func (a stubTestAddr) String() string  { return string(a) }

func TestWrapProviderConnClassifiesIOFailuresAgainstTheLivePhase(t *testing.T) {
	transportErr := errors.New("transport reset")
	info := &ProviderInfo{ProfileID: "profile-a", Kind: "tailscale", Phase: ProviderPhaseError}
	wrapped := WrapProviderConn(failingConn{err: transportErr}, func() (ProviderPhase, *ProviderInfo) {
		return ProviderPhaseError, info
	})
	_, err := wrapped.Read(make([]byte, 1))
	typed := typedConnectionError(t, err)
	if typed.Kind != KindProvider || typed.Provider != info {
		t.Fatalf("Read error = %#v, want KindProvider with the supplied info", typed)
	}
	_, err = wrapped.Write([]byte("x"))
	if typed := typedConnectionError(t, err); typed.Kind != KindProvider {
		t.Fatalf("Write error = %v, want KindProvider", err)
	}

	// A running provider preserves the ordinary transport error, including EOF.
	pipe, peer := net.Pipe()
	_ = peer.Close()
	running := WrapProviderConn(pipe, func() (ProviderPhase, *ProviderInfo) {
		return ProviderPhaseRunning, info
	})
	_, err = running.Read(make([]byte, 1))
	if !errors.Is(err, io.EOF) {
		t.Fatalf("read from a closed pipe = %v, want io.EOF preserved", err)
	}
	var untyped *Error
	if errors.As(err, &untyped) {
		t.Fatalf("a healthy provider classified EOF: %v", err)
	}
	_ = running.Close()

	// An already typed error keeps its classification, and nil inputs are safe.
	existing := &Error{Kind: KindNetwork, Cause: transportErr}
	typedErr := WrapProviderConn(failingConn{err: existing}, func() (ProviderPhase, *ProviderInfo) {
		return ProviderPhaseStopped, info
	})
	if _, err := typedErr.Read(make([]byte, 1)); err != existing {
		t.Fatalf("an existing typed error was rewritten: %v", err)
	}
	if WrapProviderConn(nil, func() (ProviderPhase, *ProviderInfo) { return "", nil }) != nil {
		t.Fatal("a nil connection was wrapped")
	}
}
