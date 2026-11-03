package app

import (
	"context"
	"errors"
	"reflect"
	"testing"

	appconnection "github.com/mslxl/natcatty/application/connection"
	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
	etmodel "github.com/mslxl/natcatty/domain/model/et"
	transportmodel "github.com/mslxl/natcatty/domain/model/transport"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// routeRecordingFactory stands in for the in-process ET client at the app
// layer. It resolves the same route source the real factory uses, so the test
// proves the app hands that implementation the full route instead of applying
// the bundled client's restriction.
type routeRecordingFactory struct {
	routes routeSource
	calls  int
	route  appconnection.Route
}

func (f *routeRecordingFactory) Open(ctx context.Context, spec appsession.StartSpec) (appsession.Transport, error) {
	f.calls++
	route, err := f.routes.Route(ctx, spec.HostID)
	if err != nil {
		return nil, err
	}
	f.route = route
	return &factoryTerminal{}, nil
}

// TestETRouteValidationDependsOnTheSelectedImplementation exercises the same
// proxy and two-jump routes through both implementations: the bundled client
// keeps its original restrictions, while the in-process selection accepts and
// forwards the route.
func TestETRouteValidationDependsOnTheSelectedImplementation(t *testing.T) {
	cases := []struct {
		name       string
		route      appconnection.Route
		bundledErr error
	}{
		{
			name: "proxy",
			route: appconnection.Route{Proxy: &appconnection.Proxy{
				Type: appconnection.ProxyHTTP, Host: "proxy.internal", Port: 8080,
			}},
			bundledErr: etmodel.ErrProxyUnsupported,
		},
		{
			name: "two-jump chain",
			route: appconnection.Route{Jumps: []appconnection.Hop{
				{HostID: "j1", Hostname: "j1.internal", Port: 22},
				{HostID: "j2", Hostname: "j2.internal", Port: 22},
			}},
			bundledErr: etmodel.ErrMultipleJumpHosts,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			host := domainvault.Host{
				ID: "et", Protocol: domainvault.ProtocolET, Hostname: "et.internal", Port: 2222,
			}
			hosts := fakeImplementationHosts{items: map[model.HostID]domainvault.Host{"et": host}}
			bundled := &etTransportFactory{
				vault:  fakeETVault{hosts: map[string]domainvault.Host{"et": host}},
				routes: fakeTransportRoute{route: tc.route},
			}
			inProcess := &routeRecordingFactory{routes: fakeTransportRoute{route: tc.route}}
			cfg := etmodel.Config{Hostname: "et.internal", Port: 2222}.Normalized()
			spec := appsession.StartSpec{
				Kind: appsession.KindET, HostID: "et", Hostname: "et.internal", Port: 2222, ET: &cfg,
			}

			bundledFactory := newTransportFactory(&factoryLocal{}, nil, nil, nil, nil).
				withClients(nil, bundled).
				withInProcessET(inProcess).
				withImplementations(hosts, fakeImplementationDefaults{settings: domainpreferences.Defaults()})
			if _, err := bundledFactory.Open(context.Background(), spec); !errors.Is(err, tc.bundledErr) {
				t.Fatalf("bundled Open error = %v, want %v", err, tc.bundledErr)
			}
			if inProcess.calls != 0 {
				t.Fatal("the in-process client opened for a bundled selection")
			}

			inProcessFactory := newTransportFactory(&factoryLocal{}, nil, nil, nil, nil).
				withClients(nil, bundled).
				withInProcessET(inProcess).
				withImplementations(hosts, fakeImplementationDefaults{
					settings: settingsWithImplementation(transportmodel.InProcess),
				})
			transport, err := inProcessFactory.Open(context.Background(), spec)
			if err != nil {
				t.Fatalf("in-process Open rejected %s: %v", tc.name, err)
			}
			_ = transport.Close()
			if inProcess.calls != 1 {
				t.Fatalf("in-process opens = %d, want 1", inProcess.calls)
			}
			if !reflect.DeepEqual(inProcess.route, tc.route) {
				t.Fatalf("in-process route = %+v, want %+v", inProcess.route, tc.route)
			}
		})
	}
}
