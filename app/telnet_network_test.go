package app

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	adapterstnet "github.com/mslxl/natcatty/adapters/telnet"
	appconnection "github.com/mslxl/natcatty/application/connection"
	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
	domaintelnet "github.com/mslxl/natcatty/domain/model/telnet"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// fakeHostNetworkRoutes resolves only the target Host's profile and records
// the identity it was asked about.
type fakeHostNetworkRoutes struct {
	route *appconnection.NetworkRoute
	err   error
	id    model.HostID
	calls int
}

func (f *fakeHostNetworkRoutes) NetworkRouteForHost(_ context.Context,
	id model.HostID) (*appconnection.NetworkRoute, error) {

	f.calls++
	f.id = id
	return f.route, f.err
}

// telnetTestProvider records each dial and scripts its transport.
type telnetTestProvider struct {
	mu        sync.Mutex
	networks  []string
	addresses []string
	dialFn    func(ctx context.Context, network, address string) (net.Conn, error)
	err       error
}

func (p *telnetTestProvider) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
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
	return nil, errors.New("telnet test provider has no scripted transport")
}

func (p *telnetTestProvider) recorded() ([]string, []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.networks...), append([]string(nil), p.addresses...)
}

// telnetTestNetworkFactory counts provider opens so a test can prove one
// registry entry serves repeated Session opens.
type telnetTestNetworkFactory struct {
	mu       sync.Mutex
	opens    int
	provider appconnection.NetworkProvider
}

func (f *telnetTestNetworkFactory) Open(context.Context, appconnection.NetworkRoute) (appconnection.NetworkProvider, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opens++
	return f.provider, nil
}

func (f *telnetTestNetworkFactory) openCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.opens
}

func telnetStartSpec() appsession.StartSpec {
	cfg := domaintelnet.Config{Hostname: "console.local", Port: 2323}.Normalized()
	return appsession.StartSpec{
		Kind: appsession.KindTelnet, HostID: "console", Hostname: "console.local", Port: 2323,
		Telnet: &cfg,
	}
}

// TestTelnetWithoutANetworkProfileKeepsTheDirectDial checks the no-profile
// path is the adapter's original one: it dials the target itself and never
// touches a Provider.
func TestTelnetWithoutANetworkProfileKeepsTheDirectDial(t *testing.T) {
	client, server := net.Pipe()
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})
	directCalls := 0
	inner := adapterstnet.Factory{Dial: func(_ context.Context, network, address string) (net.Conn, error) {
		directCalls++
		if network != "tcp" || address != "console.local:2323" {
			t.Errorf("direct dial = %s %s, want tcp console.local:2323", network, address)
		}
		return client, nil
	}}
	networkFactory := &telnetTestNetworkFactory{provider: &telnetTestProvider{
		dialFn: func(context.Context, string, string) (net.Conn, error) {
			t.Error("a Provider was used without a Network Profile")
			return nil, errors.New("unexpected provider dial")
		},
	}}
	factory := newTelnetFactory(inner, &fakeHostNetworkRoutes{},
		appconnection.NewNetworkProviderRegistry(networkFactory))

	transport, err := factory.Open(context.Background(), telnetStartSpec())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = transport.Close()
	if directCalls != 1 {
		t.Fatalf("direct dials = %d, want 1", directCalls)
	}
	if opens := networkFactory.openCount(); opens != 0 {
		t.Fatalf("provider opens = %d, want none without a Network Profile", opens)
	}
}

// TestTelnetUsesTheHostNetworkProviderForItsFirstDial proves the target Host's
// effective profile supplies the one Telnet dial, over the existing
// Factory.Dial seam.
func TestTelnetUsesTheHostNetworkProviderForItsFirstDial(t *testing.T) {
	client, server := net.Pipe()
	server.Close()
	t.Cleanup(func() { _ = client.Close() })
	provider := &telnetTestProvider{dialFn: func(context.Context, string, string) (net.Conn, error) {
		return client, nil
	}}
	directCalls := 0
	inner := adapterstnet.Factory{Dial: func(context.Context, string, string) (net.Conn, error) {
		directCalls++
		return nil, errors.New("direct dial is forbidden")
	}}
	network := &appconnection.NetworkRoute{
		ProfileID: "net-console", Kind: "wireguard", Label: "Lab", Fingerprint: "fp-telnet",
	}
	routes := &fakeHostNetworkRoutes{route: network}
	registry := appconnection.NewNetworkProviderRegistry(&telnetTestNetworkFactory{provider: provider})
	factory := newTelnetFactory(inner, routes, registry)

	transport, err := factory.Open(context.Background(), telnetStartSpec())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = transport.Close()

	if routes.calls != 1 || routes.id != "console" {
		t.Fatalf("route resolution = %d for %q, want the target Host", routes.calls, routes.id)
	}
	networks, addresses := provider.recorded()
	if len(networks) != 1 || networks[0] != "tcp" {
		t.Fatalf("provider networks = %v, want one tcp dial", networks)
	}
	if len(addresses) != 1 || addresses[0] != "console.local:2323" {
		t.Fatalf("provider addresses = %v, want the target endpoint", addresses)
	}
	if directCalls != 0 {
		t.Fatalf("direct dials = %d, want no fallback", directCalls)
	}
}

// TestTelnetProviderFailureStaysTypedAndNeverFallsBack proves a Provider-level
// failure is returned as-is and the adapter's direct dialer is never tried.
func TestTelnetProviderFailureStaysTypedAndNeverFallsBack(t *testing.T) {
	provider := &telnetTestProvider{err: &appconnection.Error{
		Kind:  appconnection.KindProvider,
		Cause: errors.New("tunnel is down"),
	}}
	directCalls := 0
	inner := adapterstnet.Factory{Dial: func(context.Context, string, string) (net.Conn, error) {
		directCalls++
		return nil, errors.New("direct dial is forbidden")
	}}
	registry := appconnection.NewNetworkProviderRegistry(&telnetTestNetworkFactory{provider: provider})
	factory := newTelnetFactory(inner, &fakeHostNetworkRoutes{route: &appconnection.NetworkRoute{
		ProfileID: "net-console", Kind: "tailscale", Fingerprint: "fp-telnet",
	}}, registry)

	_, err := factory.Open(context.Background(), telnetStartSpec())
	var typed *appconnection.Error
	if !errors.As(err, &typed) || typed.Kind != appconnection.KindProvider {
		t.Fatalf("Open error = %v, want a typed provider failure", err)
	}
	if typed.Provider == nil || typed.Provider.ProfileID != "net-console" {
		t.Fatalf("Provider info = %+v, want the selected profile", typed.Provider)
	}
	if directCalls != 0 {
		t.Fatalf("direct dials = %d, want no fallback", directCalls)
	}
}

// TestTelnetConfigErrorIsNotBlamedOnTheProvider checks that an adapter
// validation failure keeps its own type even when a Network Profile is
// selected; only the dial seam's errors gain provider context.
func TestTelnetConfigErrorIsNotBlamedOnTheProvider(t *testing.T) {
	provider := &telnetTestProvider{dialFn: func(context.Context, string, string) (net.Conn, error) {
		t.Error("an invalid configuration still dialed")
		return nil, errors.New("unexpected dial")
	}}
	registry := appconnection.NewNetworkProviderRegistry(&telnetTestNetworkFactory{provider: provider})
	factory := newTelnetFactory(adapterstnet.Factory{},
		&fakeHostNetworkRoutes{route: &appconnection.NetworkRoute{
			ProfileID: "net-console", Kind: "tailscale", Fingerprint: "fp-telnet",
		}}, registry)
	cfg := domaintelnet.Config{Hostname: "", Port: 23}.Normalized()
	_, err := factory.Open(context.Background(), appsession.StartSpec{
		Kind: appsession.KindTelnet, HostID: "console", Telnet: &cfg,
	})
	if !errors.Is(err, domaintelnet.ErrInvalidConfig) {
		t.Fatalf("Open = %v, want a validation failure", err)
	}
	var typed *appconnection.Error
	if errors.As(err, &typed) {
		t.Fatalf("a validation failure became a typed connection error: %v", err)
	}
	networks, _ := provider.recorded()
	if len(networks) != 0 {
		t.Fatalf("provider networks = %v, want no dial", networks)
	}
}

// TestTelnetNetworkProfileWithoutAProviderFailsClosed covers the no-factory
// case: a selected profile never falls back to the adapter's direct dialer.
func TestTelnetNetworkProfileWithoutAProviderFailsClosed(t *testing.T) {
	directCalls := 0
	inner := adapterstnet.Factory{Dial: func(context.Context, string, string) (net.Conn, error) {
		directCalls++
		return nil, errors.New("direct dial is forbidden")
	}}
	factory := newTelnetFactory(inner, &fakeHostNetworkRoutes{route: &appconnection.NetworkRoute{
		ProfileID: "net-console", Kind: "tailscale", Fingerprint: "fp-telnet",
	}}, appconnection.NewNetworkProviderRegistry(nil))

	_, err := factory.Open(context.Background(), telnetStartSpec())
	var typed *appconnection.Error
	if !errors.As(err, &typed) || typed.Kind != appconnection.KindProvider {
		t.Fatalf("Open error = %v, want a typed provider failure", err)
	}
	if typed.Provider == nil || typed.Provider.ProfileID != "net-console" {
		t.Fatalf("Provider info = %+v, want the selected profile", typed.Provider)
	}
	if directCalls != 0 {
		t.Fatalf("direct dials = %d, want no fallback", directCalls)
	}
}

// TestTelnetReopenReusesTheSameProvider checks an explicit reconnect opens a
// second Telnet Session through the registry's existing Provider rather than
// starting a second one or dialing directly.
func TestTelnetReopenReusesTheSameProvider(t *testing.T) {
	dials := 0
	provider := &telnetTestProvider{dialFn: func(context.Context, string, string) (net.Conn, error) {
		dials++
		client, server := net.Pipe()
		_ = server.Close()
		return client, nil
	}}
	inner := adapterstnet.Factory{Dial: func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("direct dial is forbidden")
	}}
	networkFactory := &telnetTestNetworkFactory{provider: provider}
	registry := appconnection.NewNetworkProviderRegistry(networkFactory)
	factory := newTelnetFactory(inner, &fakeHostNetworkRoutes{route: &appconnection.NetworkRoute{
		ProfileID: "net-console", Kind: "tailscale", Fingerprint: "fp-telnet",
	}}, registry)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for i := 0; i < 2; i++ {
		transport, err := factory.Open(ctx, telnetStartSpec())
		if err != nil {
			t.Fatalf("Open %d: %v", i, err)
		}
		_ = transport.Close()
	}
	if opens := networkFactory.openCount(); opens != 1 {
		t.Fatalf("provider opens = %d, want one reused provider", opens)
	}
	if dials != 2 {
		t.Fatalf("provider dials = %d, want one per Session", dials)
	}
}

// TestVaultRoutesHostNetworkRouteIgnoresJumps checks Telnet resolves the target
// Host's own effective profile even when an SSH route would prefer a jump's.
func TestVaultRoutesHostNetworkRouteIgnoresJumps(t *testing.T) {
	vault := &fakeRouteVault{
		hosts: map[model.HostID]domainvault.Host{
			"target": {
				ID: "target", Hostname: "target.internal", NetworkProfileID: "net-target",
				HostChain: &domainvault.HostChain{HostIDs: []model.HostID{"jump-1"}},
			},
			"jump-1": {ID: "jump-1", Hostname: "bastion.internal", NetworkProfileID: "net-first"},
		},
		networkProfiles: map[string]domainvault.NetworkProfile{
			"net-target": {ID: "net-target", Kind: domainvault.NetworkKindTailscale},
			"net-first":  {ID: "net-first", Kind: domainvault.NetworkKindWireGuard},
		},
		secrets: map[string]string{},
	}
	routes := vaultRoutes{vault: vault}

	sshRoute, err := routes.Route(context.Background(), "target")
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if sshRoute.Network == nil || sshRoute.Network.ProfileID != "net-first" {
		t.Fatalf("SSH Network = %+v, want the first jump's profile", sshRoute.Network)
	}
	hostRoute, err := routes.NetworkRouteForHost(context.Background(), "target")
	if err != nil {
		t.Fatalf("NetworkRouteForHost: %v", err)
	}
	if hostRoute == nil || hostRoute.ProfileID != "net-target" ||
		hostRoute.Kind != string(domainvault.NetworkKindTailscale) {
		t.Fatalf("telnet Network = %+v, want the target Host's profile", hostRoute)
	}
}
