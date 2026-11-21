package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"

	moshadapter "github.com/mslxl/natcatty/adapters/mosh"
	adapterstnet "github.com/mslxl/natcatty/adapters/telnet"
	adaptervnc "github.com/mslxl/natcatty/adapters/vnc"
	appconnection "github.com/mslxl/natcatty/application/connection"
	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
	moshmodel "github.com/mslxl/natcatty/domain/model/mosh"
	domaintelnet "github.com/mslxl/natcatty/domain/model/telnet"
	modelvnc "github.com/mslxl/natcatty/domain/model/vnc"
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

// newTelnetFactory gives the Telnet adapter the route resolved for the Host.
// A route with SSH jumps or a proxy is opened by the shared Connection Manager
// as a raw routed stream: every jump authenticates and verifies its host key
// exactly as an SSH Session would, and the final TCP stream is handed to
// Telnet without an SSH handshake on the target. Without such a route the
// Host's Network Profile still supplies the one dial, and without a profile
// the adapter dials exactly as it did before Network Profiles existed.
func newTelnetFactory(inner adapterstnet.Factory, routes routeSource,
	providers *appconnection.NetworkProviderRegistry,
	manager *appconnection.Manager) appsession.Factory {
	return &telnetTransportFactory{
		inner: inner, routes: routes, providers: providers, manager: manager,
	}
}

type telnetTransportFactory struct {
	inner     adapterstnet.Factory
	routes    routeSource
	providers *appconnection.NetworkProviderRegistry
	manager   *appconnection.Manager
}

func (f *telnetTransportFactory) Open(ctx context.Context, spec appsession.StartSpec) (appsession.Transport, error) {
	route, err := f.route(ctx, spec.HostID)
	if err != nil {
		return nil, err
	}
	if len(route.Jumps) > 0 || route.Proxy != nil {
		return f.openRouted(ctx, spec, route)
	}
	if route.Network == nil {
		return f.inner.Open(ctx, spec)
	}
	return f.openNetwork(ctx, spec, *route.Network)
}

// openRouted hands the Telnet adapter a dial seam backed by the Connection
// Manager. The adapter still validates its configuration before it calls Dial,
// so a malformed Host opens no SSH chain.
func (f *telnetTransportFactory) openRouted(ctx context.Context,
	spec appsession.StartSpec, route appconnection.Route) (appsession.Transport, error) {

	if f.manager == nil {
		return nil, &appconnection.Error{
			Kind:  appconnection.KindInternal,
			Cause: errors.New("app: no SSH connection manager for a routed Telnet Host"),
		}
	}
	cfg := telnetConfig(spec)
	factory := f.inner
	factory.Dial = func(dialCtx context.Context, network, address string) (net.Conn, error) {
		return f.manager.DialStream(dialCtx, appconnection.Request{
			HostID:            spec.HostID,
			Hostname:          cfg.Hostname,
			Port:              cfg.Port,
			Username:          spec.Username,
			Jumps:             route.Jumps,
			Proxy:             route.Proxy,
			Network:           route.Network,
			Algorithms:        route.Algorithms,
			ConfigFingerprint: route.ConfigFingerprint,
			// A raw stream has no pooled Connection to keep alive. The jump
			// clients live exactly as long as this Telnet stream, and the
			// Telnet adapter owns closing it.
		})
	}
	return factory.Open(ctx, spec)
}

// openNetwork keeps the original Network Profile path: the Provider supplies
// the one Telnet dial. A selected profile is never replaced with the adapter's
// direct dialer.
func (f *telnetTransportFactory) openNetwork(ctx context.Context,
	spec appsession.StartSpec, network appconnection.NetworkRoute) (appsession.Transport, error) {

	if f.providers == nil {
		return nil, appconnection.ProviderUnavailable(network)
	}
	provider, err := f.providers.Acquire(ctx, network)
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

// route resolves the Host's full route. An unsaved target (Quick Connect) has
// no Host record, so the routes source falls back to the app-wide default
// proxy; with no routes source at all the adapter keeps its direct dial.
func (f *telnetTransportFactory) route(ctx context.Context, id model.HostID) (appconnection.Route, error) {
	if f.routes == nil {
		return appconnection.Route{}, nil
	}
	return f.routes.Route(ctx, id)
}

// telnetConfig is the endpoint the adapter will validate and dial. Using the
// normalized configuration keeps the route target on the same port the
// adapter computes when the Host stores a zero port.
func telnetConfig(spec appsession.StartSpec) domaintelnet.Config {
	if spec.Telnet == nil {
		return domaintelnet.Config{Hostname: spec.Hostname, Port: spec.Port}.Normalized()
	}
	return spec.Telnet.Normalized()
}

// newVNCFactory gives the VNC adapter the route resolved for the Host. A route
// with SSH jumps or a proxy is opened by the shared Connection Manager as a
// raw routed stream: every jump authenticates and verifies its host key
// exactly as an SSH Session would, and the final TCP stream is handed to VNC
// without an SSH handshake on the target. A route with only a Network Profile
// still supplies the one VNC dial through that Provider, and without a route
// the adapter dials directly.
func newVNCFactory(inner adaptervnc.Factory, routes routeSource,
	providers *appconnection.NetworkProviderRegistry,
	manager *appconnection.Manager) appsession.Factory {
	return &vncTransportFactory{
		inner: inner, routes: routes, providers: providers, manager: manager,
	}
}

type vncTransportFactory struct {
	inner     adaptervnc.Factory
	routes    routeSource
	providers *appconnection.NetworkProviderRegistry
	manager   *appconnection.Manager
}

func (f *vncTransportFactory) Open(ctx context.Context, spec appsession.StartSpec) (appsession.Transport, error) {
	// Validate before resolving a route or touching a Provider so a malformed
	// Host opens no jump, proxy, or provider connection. The adapter validates
	// again; this early check is what keeps the route seams from starting.
	cfg, err := vncConfig(spec)
	if err != nil {
		return nil, err
	}
	route, err := f.route(ctx, spec.HostID)
	if err != nil {
		return nil, err
	}
	switch {
	case len(route.Jumps) > 0 || route.Proxy != nil:
		return f.openRouted(ctx, spec, cfg, route)
	case route.Network != nil:
		return f.openNetwork(ctx, spec, *route.Network)
	default:
		return f.openDirect(ctx, spec)
	}
}

// openRouted hands the VNC adapter a dial seam backed by the Connection
// Manager. The adapter still validates its configuration before it calls Dial,
// so a malformed Host opens no SSH chain.
func (f *vncTransportFactory) openRouted(ctx context.Context, spec appsession.StartSpec,
	cfg modelvnc.SessionConfig, route appconnection.Route) (appsession.Transport, error) {

	if f.manager == nil {
		return nil, &appconnection.Error{
			Kind:  appconnection.KindInternal,
			Cause: errors.New("app: no SSH connection manager for a routed VNC Host"),
		}
	}
	factory := f.inner
	factory.Routed = true
	factory.Dial = func(dialCtx context.Context, network, address string) (net.Conn, error) {
		return f.manager.DialStream(dialCtx, appconnection.Request{
			HostID:            spec.HostID,
			Hostname:          cfg.Hostname,
			Port:              cfg.Port,
			Username:          spec.Username,
			Jumps:             route.Jumps,
			Proxy:             route.Proxy,
			Network:           route.Network,
			Algorithms:        route.Algorithms,
			ConfigFingerprint: route.ConfigFingerprint,
			// A raw stream has no pooled Connection to keep alive. The jump
			// clients live exactly as long as this VNC stream, and the VNC
			// adapter owns closing it.
		})
	}
	return factory.Open(ctx, spec)
}

// openNetwork keeps the original Network Profile path: the Provider supplies
// the one VNC dial. A selected profile is never replaced with the adapter's
// direct dialer.
func (f *vncTransportFactory) openNetwork(ctx context.Context, spec appsession.StartSpec,
	network appconnection.NetworkRoute) (appsession.Transport, error) {

	if f.providers == nil {
		return nil, appconnection.ProviderUnavailable(network)
	}
	provider, err := f.providers.Acquire(ctx, network)
	if err != nil {
		return nil, err
	}
	factory := f.inner
	factory.Routed = true
	// Tag only the failure that came from the dial seam. A configuration error
	// the adapter catches before dialing must keep its own meaning instead of
	// blaming the profile.
	dialFailed := false
	factory.Dial = func(dialCtx context.Context, network, address string) (net.Conn, error) {
		conn, err := provider.DialContext(dialCtx, network, address)
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
		// Keep the transport's Kind (an unreachable target stays KindNetwork
		// and an expired deadline stays KindTimeout), but name the profile
		// behind the failure. A typed provider failure keeps KindProvider.
		return nil, appconnection.TagProvider(err, appconnection.ProviderInfo{
			ProfileID: network.ProfileID,
			Label:     network.Label,
			Kind:      network.Kind,
			Phase:     appconnection.ProviderPhaseRunning,
		})
	}
	return transport, nil
}

// openDirect is the Host with no route: the adapter's own dialer is used and
// the stream is not marked as tunnel-encrypted.
func (f *vncTransportFactory) openDirect(ctx context.Context, spec appsession.StartSpec) (appsession.Transport, error) {
	factory := f.inner
	factory.Routed = false
	return factory.Open(ctx, spec)
}

// route resolves the Host's full route. An unsaved target (Quick Connect) has
// no Host record, so the routes source falls back to the app-wide default
// proxy; with no routes source at all the adapter keeps its direct dial.
func (f *vncTransportFactory) route(ctx context.Context, id model.HostID) (appconnection.Route, error) {
	if f.routes == nil {
		return appconnection.Route{}, nil
	}
	return f.routes.Route(ctx, id)
}

// vncConfig is the endpoint the adapter will validate and dial. Using the
// normalized configuration keeps the route target on the same port the
// adapter computes when the Host stores a zero port.
func vncConfig(spec appsession.StartSpec) (modelvnc.SessionConfig, error) {
	if spec.VNC == nil {
		return modelvnc.SessionConfig{}, &modelvnc.ValidationError{Field: "vnc", Reason: "is required"}
	}
	cfg := spec.VNC.Normalized()
	if err := cfg.Validate(); err != nil {
		return modelvnc.SessionConfig{}, err
	}
	return cfg, nil
}
