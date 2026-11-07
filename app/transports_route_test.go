package app

import (
	"context"
	"reflect"
	"testing"

	appconnection "github.com/mslxl/natcatty/application/connection"
	appsession "github.com/mslxl/natcatty/application/session"
	etmodel "github.com/mslxl/natcatty/domain/model/et"
)

// routeRecordingFactory stands in for the in-process ET client at the app
// layer. It resolves the same route source the real factory uses, so the test
// proves the app hands that implementation the full route instead of applying a
// bundled client's restriction.
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

// TestETInProcessReceivesTheFullRoute exercises a proxy and a two-jump chain
// through the in-process ET factory: unlike the removed bundled client, it
// accepts and forwards the whole Connection Manager route.
func TestETInProcessReceivesTheFullRoute(t *testing.T) {
	cases := []struct {
		name  string
		route appconnection.Route
	}{
		{
			name: "proxy",
			route: appconnection.Route{Proxy: &appconnection.Proxy{
				Type: appconnection.ProxyHTTP, Host: "proxy.internal", Port: 8080,
			}},
		},
		{
			name: "two-jump chain",
			route: appconnection.Route{Jumps: []appconnection.Hop{
				{HostID: "j1", Hostname: "j1.internal", Port: 22},
				{HostID: "j2", Hostname: "j2.internal", Port: 22},
			}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inProcess := &routeRecordingFactory{routes: fakeTransportRoute{route: tc.route}}
			cfg := etmodel.Config{Hostname: "et.internal", Port: 2222}.Normalized()
			spec := appsession.StartSpec{
				Kind: appsession.KindET, HostID: "et", Hostname: "et.internal", Port: 2222, ET: &cfg,
			}
			factory := newInProcessFactory(&implementationFactory{}, inProcess)
			transport, err := factory.Open(context.Background(), spec)
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
