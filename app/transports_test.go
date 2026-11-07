package app

import (
	"context"
	"errors"
	"net"
	"testing"

	appconnection "github.com/mslxl/natcatty/application/connection"
	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
	moshmodel "github.com/mslxl/natcatty/domain/model/mosh"
)

type fakeTransportRoute struct {
	route appconnection.Route
	err   error
}

func (f fakeTransportRoute) Route(context.Context, model.HostID) (appconnection.Route, error) {
	return f.route, f.err
}

func TestMoshRejectsAJumpChainBeforeOpeningAnything(t *testing.T) {
	factory := &moshTransportFactory{
		routes: fakeTransportRoute{route: appconnection.Route{
			Jumps: []appconnection.Hop{{HostID: "jump", Hostname: "jump.example"}},
		}},
	}
	cfg := moshmodel.Config{Hostname: "mosh.example", Port: 22}.Normalized()
	_, err := factory.Open(context.Background(), appsession.StartSpec{
		Kind: appsession.KindMosh, Hostname: "mosh.example", Port: 22, Mosh: &cfg,
	})
	if !errors.Is(err, moshmodel.ErrJumpUnsupported) {
		t.Fatalf("Open = %v, want a typed jump restriction", err)
	}
}

// moshTestRoute is the Profile every Network Profile Mosh test selects.
func moshTestRoute() fakeTransportRoute {
	return fakeTransportRoute{route: appconnection.Route{Network: &appconnection.NetworkRoute{
		ProfileID: "net-mosh", Kind: "tailscale", Label: "Office", Fingerprint: "fp-mosh",
	}}}
}

func moshTestSpec() appsession.StartSpec {
	cfg := moshmodel.Config{Hostname: "mosh.example", Port: 22}.Normalized()
	return appsession.StartSpec{
		Kind: appsession.KindMosh, HostID: "mosh", Hostname: "mosh.example", Port: 22, Mosh: &cfg,
	}
}

// TestMoshRejectsANetworkProfileWithoutADatagramCapability pins the
// fail-closed rule for a Provider that cannot carry the UDP flow: neither the
// SSH bootstrap nor a host UDP socket may start outside the selected profile.
// The named test below covers the capability check in detail; this case keeps
// the route-level expectation explicit.

// datagramTestProvider is a Provider that carries the datagram capability.
type datagramTestProvider struct{}

func (p *datagramTestProvider) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, errors.New("datagram test provider is inert")
}

func (p *datagramTestProvider) DialDatagram(context.Context, string, string) (net.Conn, error) {
	return nil, errors.New("datagram test provider is inert")
}

// tcpOnlyTestProvider is deliberately missing DialDatagram.
type tcpOnlyTestProvider struct{}

func (tcpOnlyTestProvider) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, errors.New("tcp-only test provider is inert")
}

type staticProviderFactory struct{ provider appconnection.NetworkProvider }

func (f staticProviderFactory) Open(context.Context, appconnection.NetworkRoute) (appconnection.NetworkProvider, error) {
	return f.provider, nil
}

// TestInProcessMoshRequiresAProviderDatagramCapability keeps the datagram seam
// fail-closed: when the running Provider cannot carry UDP the Session stops
// before the SSH bootstrap, instead of opening a host socket outside the
// profile.
func TestInProcessMoshRequiresAProviderDatagramCapability(t *testing.T) {
	dialer := &factoryDialer{client: &factoryClient{terminal: &factoryTerminal{}}}
	registry := appconnection.NewNetworkProviderRegistry(staticProviderFactory{provider: tcpOnlyTestProvider{}})
	manager := appconnection.NewManager(dialer, nil, &factoryKnownHosts{},
		appconnection.WithNetworkProviders(registry))
	t.Cleanup(manager.CloseAll)
	factory := &moshTransportFactory{
		routes: moshTestRoute(), manager: manager, providers: registry,
	}
	_, err := factory.Open(context.Background(), moshTestSpec())
	if !errors.Is(err, moshmodel.ErrNetworkUnsupported) {
		t.Fatalf("Open = %v, want the datagram capability restriction", err)
	}
	var typed *appconnection.Error
	if !errors.As(err, &typed) || typed.Kind != appconnection.KindUnsupported {
		t.Fatalf("Open error = %v, want a typed unsupported failure", err)
	}
	if typed.Provider == nil || typed.Provider.ProfileID != "net-mosh" {
		t.Fatalf("Provider info = %+v, want the selected profile", typed.Provider)
	}
	if dialer.calls != 0 {
		t.Fatalf("SSH bootstrap dials = %d, want none", dialer.calls)
	}
}

// TestInProcessMoshRoutesTheBootstrapThroughTheSelectedProvider checks that a
// datagram-capable Provider passes the capability check and that the SSH
// bootstrap request carries the same live Provider. The fake dialer returns no
// exec capability, so the Open stops there; what matters is that the bootstrap
// started with the selected Provider attached.
func TestInProcessMoshRoutesTheBootstrapThroughTheSelectedProvider(t *testing.T) {
	provider := &datagramTestProvider{}
	dialer := &factoryDialer{client: &factoryClient{terminal: &factoryTerminal{}}}
	registry := appconnection.NewNetworkProviderRegistry(staticProviderFactory{provider: provider})
	manager := appconnection.NewManager(dialer,
		vaultPasswords{vault: factoryPasswords{value: "pw"}}, &factoryKnownHosts{},
		appconnection.WithNetworkProviders(registry))
	t.Cleanup(manager.CloseAll)
	factory := &moshTransportFactory{
		routes: moshTestRoute(), manager: manager, providers: registry,
	}
	_, err := factory.Open(context.Background(), moshTestSpec())
	if errors.Is(err, moshmodel.ErrNetworkUnsupported) {
		t.Fatalf("Open = %v, want the datagram capability accepted", err)
	}
	if dialer.calls != 1 {
		t.Fatalf("SSH bootstrap dials = %d, want exactly one (Open error: %v)", dialer.calls, err)
	}
	if dialer.request.Provider != provider {
		t.Fatalf("bootstrap Provider = %T, want the selected Provider", dialer.request.Provider)
	}
}

func TestMoshRejectsAProxyBeforeOpeningAnything(t *testing.T) {
	factory := &moshTransportFactory{
		routes: fakeTransportRoute{route: appconnection.Route{
			Proxy: &appconnection.Proxy{Type: appconnection.ProxySOCKS5, Host: "proxy"},
		}},
	}
	cfg := moshmodel.Config{Hostname: "mosh.example", Port: 22}.Normalized()
	_, err := factory.Open(context.Background(), appsession.StartSpec{
		Kind: appsession.KindMosh, Hostname: "mosh.example", Port: 22, Mosh: &cfg,
	})
	if !errors.Is(err, moshmodel.ErrProxyUnsupported) {
		t.Fatalf("Open = %v, want a typed proxy restriction", err)
	}
}
