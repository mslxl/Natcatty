package app

import (
	"context"
	"errors"
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

// TestMoshRejectsANetworkProfileBeforeOpeningAnything checks the in-process
// Mosh client fails closed, with the typed OS-level-VPN guidance, before the
// SSH bootstrap dial starts.
func TestMoshRejectsANetworkProfileBeforeOpeningAnything(t *testing.T) {
	dialer := &factoryDialer{client: &factoryClient{terminal: &factoryTerminal{}}}
	manager := appconnection.NewManager(dialer, nil, &factoryKnownHosts{})
	t.Cleanup(manager.CloseAll)
	routes := fakeTransportRoute{route: appconnection.Route{Network: &appconnection.NetworkRoute{
		ProfileID: "net-mosh", Kind: "tailscale", Label: "Office", Fingerprint: "fp-mosh",
	}}}
	cfg := moshmodel.Config{Hostname: "mosh.example", Port: 22}.Normalized()
	spec := appsession.StartSpec{
		Kind: appsession.KindMosh, HostID: "mosh", Hostname: "mosh.example", Port: 22, Mosh: &cfg,
	}
	factory := &moshTransportFactory{routes: routes, manager: manager}
	_, err := factory.Open(context.Background(), spec)
	if !errors.Is(err, moshmodel.ErrNetworkUnsupported) {
		t.Fatalf("Open = %v, want the Network Profile restriction", err)
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
