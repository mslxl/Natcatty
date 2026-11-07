package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"

	moshadapter "github.com/mslxl/natcatty/adapters/mosh"
	adapterstnet "github.com/mslxl/natcatty/adapters/telnet"
	appconnection "github.com/mslxl/natcatty/application/connection"
	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
	moshmodel "github.com/mslxl/natcatty/domain/model/mosh"
)

// newMoshFactory builds the in-process Mosh transport. The SSH bootstrap runs
// over the app's Connection Manager so saved credentials, MFA prompts, and
// host-key trust behave exactly as they do for an SSH Session; the UDP flow
// can live inside a Network Provider, which is why the factory takes the
// registry.
func newMoshFactory(manager *appconnection.Manager, routes routeSource,
	providers *appconnection.NetworkProviderRegistry) appsession.Factory {
	return &moshTransportFactory{
		inner:     moshadapter.InProcessFactory{},
		manager:   manager,
		routes:    routes,
		providers: providers,
	}
}

// moshTransportFactory checks the Mosh route restriction before any channel
// starts, then opens the in-process client. Mosh's UDP channel cannot carry a
// jump chain or a proxy.
type moshTransportFactory struct {
	inner     moshadapter.InProcessFactory
	manager   *appconnection.Manager
	routes    routeSource
	providers *appconnection.NetworkProviderRegistry
}

func (f *moshTransportFactory) Open(ctx context.Context, spec appsession.StartSpec) (appsession.Transport, error) {
	route, err := moshRoute(ctx, spec, f.manager, f.routes)
	if err != nil {
		return nil, err
	}
	inner := f.inner
	inner.Bootstrap = &moshBootstrap{manager: f.manager, route: route}
	if route.Network != nil {
		datagram, err := f.datagramProvider(ctx, *route.Network)
		if err != nil {
			return nil, err
		}
		inner.DialDatagram = datagram.DialDatagram
	}
	return inner.Open(ctx, spec)
}

// datagramProvider acquires the live Provider for the route and requires its
// optional datagram capability. A Provider without it fails the Session closed:
// the UDP flow must never fall back to a host socket outside the profile. The
// SSH bootstrap uses the same registry entry through the Connection Manager, so
// one profile still has one Provider.
func (f *moshTransportFactory) datagramProvider(ctx context.Context,
	network appconnection.NetworkRoute) (appconnection.NetworkProviderDatagram, error) {

	provider, err := f.providers.Acquire(ctx, network)
	if err != nil {
		return nil, err
	}
	datagram, ok := provider.(appconnection.NetworkProviderDatagram)
	if !ok {
		// Every production Provider implements the capability. This branch
		// guards a registry built with a custom factory and still fails closed
		// rather than dialing around it.
		return nil, &appconnection.Error{
			Kind: appconnection.KindUnsupported,
			Cause: fmt.Errorf(
				"%w: Network Provider for profile %q cannot carry mosh datagrams",
				moshmodel.ErrNetworkUnsupported, network.ProfileID),
			Provider: &appconnection.ProviderInfo{
				ProfileID: network.ProfileID,
				Label:     network.Label,
				Kind:      network.Kind,
			},
		}
	}
	return datagram, nil
}

// moshRoute validates the Mosh configuration and resolves the route
// restrictions Mosh has always applied.
func moshRoute(ctx context.Context, spec appsession.StartSpec,
	manager *appconnection.Manager, routes routeSource) (appconnection.Route, error) {

	if spec.Mosh == nil {
		return appconnection.Route{}, fmt.Errorf("%w: no Mosh configuration", moshmodel.ErrInvalidConfig)
	}
	route, err := resolveMoshRoute(ctx, spec.HostID, routes)
	if err != nil {
		return appconnection.Route{}, err
	}
	if err := moshadapter.CheckRoute(len(route.Jumps), route.Proxy != nil); err != nil {
		return appconnection.Route{}, err
	}
	if manager == nil {
		return appconnection.Route{}, errors.New("app: no SSH connection manager for Mosh")
	}
	return route, nil
}

func resolveMoshRoute(ctx context.Context, id model.HostID, routes routeSource) (appconnection.Route, error) {
	if routes == nil {
		return appconnection.Route{}, nil
	}
	return routes.Route(ctx, id)
}

// moshBootstrap opens the one SSH exec channel that launches mosh-server.
type moshBootstrap struct {
	manager *appconnection.Manager
	route   appconnection.Route
}

func (b *moshBootstrap) OpenBootstrap(ctx context.Context, spec appsession.StartSpec, cfg moshmodel.Config) (io.ReadWriteCloser, error) {
	lease, err := b.manager.Acquire(ctx, appconnection.Request{
		HostID:            spec.HostID,
		Hostname:          spec.Hostname,
		Port:              spec.Port,
		Username:          spec.Username,
		Jumps:             b.route.Jumps,
		Proxy:             b.route.Proxy,
		Network:           b.route.Network,
		Algorithms:        b.route.Algorithms,
		ConfigFingerprint: b.route.ConfigFingerprint,
		LeaseKind:         appconnection.LeaseShell,
		Keepalive:         &appconnection.DefaultKeepalive,
	})
	if err != nil {
		return nil, err
	}
	stream, err := lease.OpenExec(ctx, cfg.RemoteCommand())
	if err != nil {
		_ = lease.Discard()
		return nil, err
	}
	return &moshBootstrapStream{ExecStream: stream, lease: lease}, nil
}

// moshBootstrapStream ties the bootstrap channel to its Connection lease. A
// normal handshake closes the channel and parks the Connection; the mosh-server
// it started keeps running.
type moshBootstrapStream struct {
	appconnection.ExecStream
	lease *appconnection.Lease
}

func (s *moshBootstrapStream) Close() error {
	err := s.ExecStream.Close()
	_ = s.lease.Release()
	return err
}

// telnetNetworkRoutes resolves only the target Host's effective Network
// Profile. Telnet has no jump chain or proxy, so the first endpoint is always
// the target Host.
type telnetNetworkRoutes interface {
	NetworkRouteForHost(ctx context.Context, id model.HostID) (*appconnection.NetworkRoute, error)
}

// newTelnetFactory gives the Telnet adapter the Network Provider selected for
// the Host's effective Network Profile. The profile is resolved at Open time;
// without one the adapter dials exactly as it did before Network Profiles
// existed. A selected profile is never replaced with the adapter's direct
// dialer: a Provider failure is returned typed and the Telnet socket is not
// opened.
func newTelnetFactory(inner adapterstnet.Factory, routes telnetNetworkRoutes,
	providers *appconnection.NetworkProviderRegistry) appsession.Factory {
	return &telnetTransportFactory{inner: inner, routes: routes, providers: providers}
}

type telnetTransportFactory struct {
	inner     adapterstnet.Factory
	routes    telnetNetworkRoutes
	providers *appconnection.NetworkProviderRegistry
}

func (f *telnetTransportFactory) Open(ctx context.Context, spec appsession.StartSpec) (appsession.Transport, error) {
	network, err := f.networkRoute(ctx, spec.HostID)
	if err != nil {
		return nil, err
	}
	if network == nil {
		return f.inner.Open(ctx, spec)
	}
	provider, err := f.providers.Acquire(ctx, *network)
	if err != nil {
		return nil, err
	}
	factory := f.inner
	// Tag only the failure that came from the dial seam. A configuration error
	// the adapter catches before dialing (an empty hostname, an out-of-range
	// port) must keep its own meaning instead of blaming the profile.
	dialFailed := false
	factory.Dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := provider.DialContext(ctx, network, address)
		if err != nil {
			dialFailed = true
		}
		return conn, err
	}
	transport, err := factory.Open(ctx, spec)
	if err != nil {
		if !dialFailed {
			return nil, err
		}
		// Keep the transport's Kind (an unreachable target stays KindNetwork and
		// an expired deadline stays KindTimeout), but name the profile behind
		// the failure. A typed provider failure keeps KindProvider.
		return nil, appconnection.TagProvider(err, appconnection.ProviderInfo{
			ProfileID: network.ProfileID,
			Label:     network.Label,
			Kind:      network.Kind,
			Phase:     appconnection.ProviderPhaseRunning,
		})
	}
	return transport, nil
}

// networkRoute returns nil when the Host has no saved identity or no effective
// profile, which keeps the adapter's original direct dial byte-identical for
// ad-hoc restore descriptors and Hosts without a Network Profile.
func (f *telnetTransportFactory) networkRoute(ctx context.Context, id model.HostID) (*appconnection.NetworkRoute, error) {
	if f.routes == nil || id == "" {
		return nil, nil
	}
	return f.routes.NetworkRouteForHost(ctx, id)
}
