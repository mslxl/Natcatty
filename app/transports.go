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
	appvault "github.com/mslxl/natcatty/application/vault"
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
		Password:          spec.Password,
		Jumps:             b.route.Jumps,
		Proxy:             b.route.Proxy,
		Network:           b.route.Network,
		Algorithms:        b.route.Algorithms,
		ConfigFingerprint: b.route.ConfigFingerprint,
		LeaseKind:         appconnection.LeaseShell,
		// A target with no saved Host (Quick Connect) must not share or park a
		// transport: another unsaved target could carry different credentials.
		Dedicated: spec.HostID == "",
		Keepalive: &appconnection.DefaultKeepalive,
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
	manager *appconnection.Manager) *vncTransportFactory {
	return &vncTransportFactory{
		inner: inner, routes: routes, providers: providers, manager: manager,
	}
}

// vncPlaintextWarnings is the persisted acknowledgement of the one-time
// unencrypted-VNC warning. The app backs it with the Vault.
type vncPlaintextWarnings interface {
	// PlaintextWarningAcknowledged reports whether the Host already accepted
	// the warning.
	PlaintextWarningAcknowledged(ctx context.Context, id model.HostID) (bool, error)
	// AcknowledgePlaintextWarning records the acceptance for the Host.
	AcknowledgePlaintextWarning(ctx context.Context, id model.HostID) error
}

// vncPlaintextPrompter asks the user to accept an unencrypted VNC connection
// after security negotiation has identified a genuinely plaintext type and
// before any credential is sent. The stream is already open at that point, but
// nothing but the version and security offers has crossed it. ConfirmPlaintext
// blocks on the open goroutine; the UI answers it. It is an interface so the
// transport layer stays free of UI types.
type vncPlaintextPrompter interface {
	// ConfirmPlaintext returns true only when the user chose to continue. A
	// false decision is an error that aborts the connection.
	ConfirmPlaintext(ctx context.Context, id model.HostID, name string) (bool, error)
}

type vncTransportFactory struct {
	inner     adaptervnc.Factory
	routes    routeSource
	providers *appconnection.NetworkProviderRegistry
	manager   *appconnection.Manager
	// secrets resolves the Host's saved VNC password. It is optional so a test
	// can build the factory without a Vault; an absent source leaves the
	// credential empty and the engine reports a missing password only when the
	// server demands one.
	secrets hostSecrets
	// plaintextWarnings persists the one-time unencrypted warning. An absent
	// value skips the warning entirely.
	plaintextWarnings vncPlaintextWarnings
	// plaintextPrompt asks the user to acknowledge an unencrypted connection.
	// An absent value falls back to a post-connect notice that never records
	// the acknowledgement by itself.
	plaintextPrompt vncPlaintextPrompter
}

// withVNCPasswords attaches the Vault read for the Host's VNC secret.
func (f *vncTransportFactory) withVNCPasswords(secrets hostSecrets) *vncTransportFactory {
	f.secrets = secrets
	return f
}

// withPlaintextWarnings attaches the per-Host warning acknowledgement store.
func (f *vncTransportFactory) withPlaintextWarnings(warnings vncPlaintextWarnings) *vncTransportFactory {
	f.plaintextWarnings = warnings
	return f
}

// withPlaintextPrompt attaches the user confirmation shown when an
// unacknowledged unrouted VNC Host negotiates a plaintext security type.
func (f *vncTransportFactory) withPlaintextPrompt(prompt vncPlaintextPrompter) *vncTransportFactory {
	f.plaintextPrompt = prompt
	return f
}

func (f *vncTransportFactory) Open(ctx context.Context, spec appsession.StartSpec) (appsession.Transport, error) {
	// Validate before resolving a route or touching a Provider so a malformed
	// Host opens no jump, proxy, or provider connection. The adapter validates
	// again; this early check is what keeps the route seams from starting.
	if _, err := vncConfig(spec); err != nil {
		return nil, err
	}
	spec, err := f.withCredentials(ctx, spec)
	if err != nil {
		return nil, err
	}
	cfg, err := vncConfig(spec)
	if err != nil {
		return nil, err
	}
	route, err := f.route(ctx, spec.HostID)
	if err != nil {
		return nil, err
	}
	routed := len(route.Jumps) > 0 || route.Proxy != nil || route.Network != nil
	var transport appsession.Transport
	// The warning decision now depends on the negotiated security type, so the
	// gate runs inside the handshake, after the type is known but before any
	// credential leaves the client. A routed Host is encrypted by its tunnel
	// and never warns. An unrouted Host asks the user only when the negotiated
	// type is genuinely plaintext; the acknowledgement belongs to that explicit
	// action, never to the delivery of a notice.
	needsNotice := false
	gate := f.securityGate(spec, routed, &needsNotice)
	switch {
	case len(route.Jumps) > 0 || route.Proxy != nil:
		transport, err = f.openRouted(ctx, spec, cfg, route, gate)
	case route.Network != nil:
		transport, err = f.openNetwork(ctx, spec, *route.Network, gate)
	default:
		transport, err = f.openDirect(ctx, spec, gate)
	}
	if err != nil {
		return nil, err
	}
	return f.withPlaintextNotice(transport, needsNotice), nil
}

// withCredentials fills the endpoint's username and saved VNC password from the
// Host. A password already on the start section wins, so a one-shot caller
// keeps its own value. A missing secret is left empty: only the negotiated
// security type can decide whether that is acceptable.
func (f *vncTransportFactory) withCredentials(ctx context.Context, spec appsession.StartSpec) (appsession.StartSpec, error) {
	if spec.VNC == nil {
		return spec, nil
	}
	cfg := *spec.VNC
	if cfg.Username == "" {
		cfg.Username = spec.Username
	}
	if cfg.Password == "" {
		cfg.Password = spec.Password
	}
	if cfg.Password == "" && spec.HostID != "" && f.secrets != nil {
		password, err := f.secrets.Secret(ctx, spec.HostID, appvault.FieldVNCPassword)
		switch {
		case err == nil:
			cfg.Password = password
		case errors.Is(err, appvault.ErrNotFound):
			// Leave it empty; the engine reports the missing credential only
			// when the server selects the password type.
		default:
			return spec, &appconnection.Error{Kind: appconnection.KindInternal, Cause: err}
		}
	}
	spec.VNC = &cfg
	return spec, nil
}

// securityGate builds the callback the adapter consults after negotiating the
// security type. The gate is the only place the warning decision is made, so
// an encrypted type (VeNCrypt TLS/X509, RA2, or a raw encrypted tunnel) never
// prompts. A genuinely plaintext type on an unrouted Host asks before any
// credential is sent: Continue records the acknowledgement, Cancel aborts the
// handshake, and when no UI is attached the caller shows a post-connect notice
// without recording consent.
func (f *vncTransportFactory) securityGate(spec appsession.StartSpec, routed bool,
	needsNotice *bool) func(context.Context, bool) error {

	return func(ctx context.Context, plaintext bool) error {
		if routed || !plaintext || f.plaintextWarnings == nil || spec.HostID == "" {
			return nil
		}
		acknowledged, ackErr := f.plaintextWarnings.PlaintextWarningAcknowledged(ctx, spec.HostID)
		if ackErr == nil && acknowledged {
			return nil
		}
		if f.plaintextPrompt == nil {
			// No prompt to ask. Show the notice after the stream opens, but
			// leave the acknowledgement unrecorded so consent is never assumed.
			*needsNotice = true
			return nil
		}
		name := spec.Label
		if name == "" {
			name = spec.Hostname
		}
		accepted, err := f.plaintextPrompt.ConfirmPlaintext(ctx, spec.HostID, name)
		if err != nil {
			return err
		}
		if !accepted {
			// A prompt that declines without an error still must not connect.
			return errors.New("app: unencrypted VNC connection declined")
		}
		// Continue is the explicit acknowledgement. A store failure only means
		// the warning is shown again next time; it never blocks the Session the
		// user just approved.
		_ = f.plaintextWarnings.AcknowledgePlaintextWarning(ctx, spec.HostID)
		return nil
	}
}

// withPlaintextNotice adds the one-time unencrypted notice to a transport when
// no post-negotiation confirmation was available. It never writes the
// acknowledgement: only an explicit user action may do that.
func (f *vncTransportFactory) withPlaintextNotice(transport appsession.Transport, needsNotice bool) appsession.Transport {
	if !needsNotice || transport == nil {
		return transport
	}
	reporter, ok := transport.(interface{ Plaintext() bool })
	if !ok || !reporter.Plaintext() {
		return transport
	}
	notices := make(chan appsession.Notice, 1)
	notices <- appsession.Notice{Code: appsession.NoticeVNCPlaintext}
	close(notices)
	return &plaintextWarnedTransport{Transport: transport, notices: notices}
}

// plaintextWarnedTransport adds the warning notice stream to an opened VNC
// transport while preserving the display and end-reporting interfaces the
// Session consumes.
type plaintextWarnedTransport struct {
	appsession.Transport
	notices <-chan appsession.Notice
}

var (
	_ appsession.Notifier        = (*plaintextWarnedTransport)(nil)
	_ appsession.DisplayProvider = (*plaintextWarnedTransport)(nil)
	_ appsession.EndReporter     = (*plaintextWarnedTransport)(nil)
)

func (t *plaintextWarnedTransport) Notices() <-chan appsession.Notice { return t.notices }

func (t *plaintextWarnedTransport) Display() appsession.DisplaySession {
	if provider, ok := t.Transport.(appsession.DisplayProvider); ok {
		return provider.Display()
	}
	return nil
}

// End forwards the adapter's classification so reconnect policy still sees a
// user close versus a transport error.
func (t *plaintextWarnedTransport) End() appsession.End {
	if reporter, ok := t.Transport.(appsession.EndReporter); ok {
		return reporter.End()
	}
	return appsession.End{Reason: appsession.EndReasonError}
}

// Routed and Plaintext preserve the adapter's decisions for a consumer that
// unwraps the warning transport.
func (t *plaintextWarnedTransport) Routed() bool {
	if reporter, ok := t.Transport.(interface{ Routed() bool }); ok {
		return reporter.Routed()
	}
	return false
}

func (t *plaintextWarnedTransport) Plaintext() bool {
	if reporter, ok := t.Transport.(interface{ Plaintext() bool }); ok {
		return reporter.Plaintext()
	}
	return true
}

// openRouted hands the VNC adapter a dial seam backed by the Connection
// Manager. The adapter still validates its configuration before it calls Dial,
// so a malformed Host opens no SSH chain.
func (f *vncTransportFactory) openRouted(ctx context.Context, spec appsession.StartSpec,
	cfg modelvnc.SessionConfig, route appconnection.Route,
	gate func(context.Context, bool) error) (appsession.Transport, error) {

	if f.manager == nil {
		return nil, &appconnection.Error{
			Kind:  appconnection.KindInternal,
			Cause: errors.New("app: no SSH connection manager for a routed VNC Host"),
		}
	}
	factory := f.inner
	factory.Routed = true
	factory.SecurityGate = gate
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
	network appconnection.NetworkRoute,
	gate func(context.Context, bool) error) (appsession.Transport, error) {

	if f.providers == nil {
		return nil, appconnection.ProviderUnavailable(network)
	}
	provider, err := f.providers.Acquire(ctx, network)
	if err != nil {
		return nil, err
	}
	factory := f.inner
	factory.Routed = true
	factory.SecurityGate = gate
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
func (f *vncTransportFactory) openDirect(ctx context.Context, spec appsession.StartSpec,
	gate func(context.Context, bool) error) (appsession.Transport, error) {
	factory := f.inner
	factory.Routed = false
	factory.SecurityGate = gate
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
