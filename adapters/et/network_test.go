// SPDX-License-Identifier: GPL-3.0-or-later

package et

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	sshadapter "github.com/mslxl/natcatty/adapters/ssh"
	"github.com/mslxl/natcatty/adapters/sshtest/fakessh"
	appconnection "github.com/mslxl/natcatty/application/connection"
	"github.com/mslxl/natcatty/domain/model"
)

// etRecordingProvider is the fake NetworkProvider for the ET route tests. It
// records the network and address of every dial, and either scripts a
// transport or returns a typed provider failure.
type etRecordingProvider struct {
	mu        sync.Mutex
	networks  []string
	addresses []string
	dialFn    func(ctx context.Context, network, address string) (net.Conn, error)
	err       error
}

func (p *etRecordingProvider) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	p.mu.Lock()
	p.networks = append(p.networks, network)
	p.addresses = append(p.addresses, address)
	fn, err := p.dialFn, p.err
	p.mu.Unlock()
	if fn != nil {
		return fn(ctx, network, address)
	}
	if err != nil {
		return nil, err
	}
	return nil, errors.New("et network provider has no scripted transport")
}

func (p *etRecordingProvider) recorded() ([]string, []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.networks...), append([]string(nil), p.addresses...)
}

// etStaticNetworkFactory opens the one scripted provider for every profile.
type etStaticNetworkFactory struct {
	provider appconnection.NetworkProvider
	err      error
}

func (f etStaticNetworkFactory) Open(context.Context, appconnection.NetworkRoute) (appconnection.NetworkProvider, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.provider, nil
}

func etTestNetworkRoute() *appconnection.NetworkRoute {
	return &appconnection.NetworkRoute{
		ProfileID: "profile-a", Kind: "tailscale", Label: "Office", Fingerprint: "fp-1",
	}
}

// etNetworkManager wires the real SSH adapter and Connection Manager with the
// scripted Provider registry. The direct dialer fails the test if a route with
// a Network Profile ever falls back to it.
func etNetworkManager(t *testing.T, provider appconnection.NetworkProvider,
	passwords appconnection.PasswordSource, known appconnection.KnownHostRepository) *appconnection.Manager {

	t.Helper()
	dialer := sshadapter.NewDialer(
		sshadapter.WithDialFunc(func(context.Context, string, string) (net.Conn, error) {
			t.Error("the direct dialer was called with a Network Profile selected")
			return nil, errors.New("direct dial is forbidden")
		}),
		sshadapter.WithTimeouts(5*time.Second, 5*time.Second),
	)
	return newRouteManager(t, dialer, passwords, known,
		appconnection.WithNetworkProviders(appconnection.NewNetworkProviderRegistry(
			etStaticNetworkFactory{provider: provider})))
}

// TestInProcessETBootstrapAndDataUseTheNetworkProvider proves the SSH
// bootstrap and the ET data path both travel over a connection the Provider
// opened. The bootstrap runs etterminal over that SSH transport, and the ET
// protocol (INITIAL_PAYLOAD plus forwarded etserver channel) uses the same
// lease, so one Provider dial carries both.
func TestInProcessETBootstrapAndDataUseTheNetworkProvider(t *testing.T) {
	target := startETFixture(t, fakessh.Options{Password: "target-password"}, testID, testPasskey)
	known := &knownHostsFixture{}
	known.trust(target.server, "target.internal", 2222)

	provider := &etRecordingProvider{dialFn: func(context.Context, string, string) (net.Conn, error) {
		return target.client, nil
	}}
	manager := etNetworkManager(t, provider, passwordMap{"target": "target-password"}, known)
	respondToInitialPayload(t, target.etServer)

	route := appconnection.Route{Network: etTestNetworkRoute()}
	transport := openETThroughManager(t, manager, route, routeSpec("target.internal", 2222, "target-user"))
	expectETOutput(t, transport, target.etServer, "et-through-a-provider")

	networks, addresses := provider.recorded()
	if len(networks) != 1 || networks[0] != "tcp" {
		t.Fatalf("provider networks = %v, want one tcp dial", networks)
	}
	if len(addresses) != 1 || addresses[0] != "target.internal:2222" {
		t.Fatalf("provider addresses = %v, want the target Host", addresses)
	}
	if target.execCount() != 1 {
		t.Fatalf("bootstrap exec calls = %d, want 1 over the provider transport", target.execCount())
	}
	forwards := target.recordedForwards()
	if len(forwards) != 1 || forwards[0].Host != etServerHost {
		t.Fatalf("ET forwards = %+v, want the direct-tcpip data channel", forwards)
	}
}

// TestInProcessETNetworkProfileWithoutAFactoryFailsClosed covers the
// no-factory case: selecting a Network Profile with no registry must return
// KindProvider before any direct dial, bootstrap, or data channel starts.
func TestInProcessETNetworkProfileWithoutAFactoryFailsClosed(t *testing.T) {
	target := startETFixture(t, fakessh.Options{Password: "target-password"}, testID, testPasskey)
	known := &knownHostsFixture{}
	known.trust(target.server, "target.internal", 2222)
	directCalls := 0
	dialer := sshadapter.NewDialer(
		sshadapter.WithDialFunc(func(context.Context, string, string) (net.Conn, error) {
			directCalls++
			return nil, errors.New("direct dial is forbidden")
		}),
		sshadapter.WithTimeouts(time.Second, time.Second),
	)
	manager := newRouteManager(t, dialer, passwordMap{"target": "target-password"}, known)
	route := appconnection.Route{Network: etTestNetworkRoute()}
	factory := InProcessFactory{
		Manager: manager,
		Routes: routeSourceFunc(func(context.Context, model.HostID) (appconnection.Route, error) {
			return route, nil
		}),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := factory.Open(ctx, routeSpec("target.internal", 2222, "target-user"))
	var typed *appconnection.Error
	if !errors.As(err, &typed) || typed.Kind != appconnection.KindProvider {
		t.Fatalf("Open error = %v, want a typed provider failure", err)
	}
	if directCalls != 0 {
		t.Fatalf("direct dials = %d, want no fallback", directCalls)
	}
	if target.execCount() != 0 || len(target.recordedForwards()) != 0 {
		t.Fatal("a missing Provider registry still started the ET bootstrap or data channel")
	}
}

// TestInProcessETProviderFailureNeverFallsBackToADirectDial proves a Provider
// failure stays typed and that the ET bootstrap never runs on a direct
// connection behind the user's back.
func TestInProcessETProviderFailureNeverFallsBackToADirectDial(t *testing.T) {
	target := startETFixture(t, fakessh.Options{Password: "target-password"}, testID, testPasskey)
	known := &knownHostsFixture{}
	known.trust(target.server, "target.internal", 2222)

	provider := &etRecordingProvider{err: &appconnection.Error{
		Kind:  appconnection.KindProvider,
		Cause: errors.New("tunnel is down"),
	}}
	manager := etNetworkManager(t, provider, passwordMap{"target": "target-password"}, known)
	route := appconnection.Route{Network: etTestNetworkRoute()}
	factory := InProcessFactory{
		Manager: manager,
		Routes: routeSourceFunc(func(context.Context, model.HostID) (appconnection.Route, error) {
			return route, nil
		}),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := factory.Open(ctx, routeSpec("target.internal", 2222, "target-user"))
	var typed *appconnection.Error
	if !errors.As(err, &typed) || typed.Kind != appconnection.KindProvider {
		t.Fatalf("Open error = %v, want a typed provider failure", err)
	}
	if typed.Provider == nil || typed.Provider.ProfileID != "profile-a" {
		t.Fatalf("Provider info = %+v, want the selected profile", typed.Provider)
	}
	networks, addresses := provider.recorded()
	if len(networks) != 1 || len(addresses) != 1 || addresses[0] != "target.internal:2222" {
		t.Fatalf("provider dials = %v %v, want one target attempt", networks, addresses)
	}
	if target.execCount() != 0 || len(target.recordedForwards()) != 0 {
		t.Fatal("a failed Provider still started the ET bootstrap or data channel")
	}
}
