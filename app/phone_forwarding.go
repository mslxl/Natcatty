// Phone Port Forwarding wiring.
//
// The phone reuses the shared application forwarding runtime, the same rule
// repository, bind-error classification, SSH route resolver, and connection
// pool the desktop uses. This file supplies only the phone's transport adapter
// (which is excluded from desktop builds' forwarding seam) and its lifecycle:
// rule auto-start at launch, a rebuild of the running rules when the network
// changes or the app returns to the foreground, and a UI repaint when a rule's
// runtime state changes.

package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/egoist/mygo"

	appconnection "github.com/mslxl/natcatty/application/connection"
	appforwarding "github.com/mslxl/natcatty/application/forwarding"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/phoneshell"
)

// phoneForwardingVault is the Vault read the connector needs to build a
// Connection request from a rule's Host.
type phoneForwardingVault interface {
	Get(ctx context.Context, id model.HostID) (domainvault.Host, error)
}

// phoneForwardingConnector adapts the shared Connection Manager to the
// forwarding runtime's Connector port. Each running rule holds one channel
// lease for its lifetime; jump chains, proxies, host-key verification, and
// algorithm policy all come from the same route resolver a Session uses.
type phoneForwardingConnector struct {
	vault       phoneForwardingVault
	routes      routeSource
	connections *appconnection.Manager
}

var _ appforwarding.Connector = phoneForwardingConnector{}

func (c phoneForwardingConnector) Connect(ctx context.Context, hostID model.HostID) (appforwarding.Connection, error) {
	if c.connections == nil {
		return nil, appforwarding.NewRuntimeError(
			appforwarding.ErrorInternal, nil, "no connection manager", nil)
	}
	if c.vault == nil {
		return nil, appforwarding.NewRuntimeError(
			appforwarding.ErrorInternal, nil, "no vault", nil)
	}
	host, err := c.vault.Get(ctx, hostID)
	if err != nil {
		return nil, phoneForwardingRuntimeError(err)
	}
	if !host.SupportsForwarding() {
		return nil, appforwarding.NewRuntimeError(
			appforwarding.ErrorUnsupported, nil,
			fmt.Sprintf("app: %s Hosts cannot forward ports", host.Protocol), nil)
	}
	var route appconnection.Route
	if c.routes != nil {
		route, err = c.routes.Route(ctx, hostID)
		if err != nil {
			return nil, phoneForwardingRuntimeError(err)
		}
	}
	lease, err := c.connections.Acquire(ctx, appconnection.Request{
		HostID:            host.ID,
		Hostname:          host.Hostname,
		Port:              host.Port,
		Username:          host.Username,
		Jumps:             route.Jumps,
		Proxy:             route.Proxy,
		Network:           route.Network,
		Algorithms:        route.Algorithms,
		ConfigFingerprint: route.ConfigFingerprint,
		// A forwarding rule shares a transport with other channel leases but
		// never with a Session shell.
		LeaseKind: appconnection.LeaseChannel,
		Keepalive: &appconnection.DefaultKeepalive,
	})
	if err != nil {
		return nil, phoneForwardingRuntimeError(err)
	}
	forwarding, err := lease.Forwarding()
	if err != nil {
		_ = lease.Discard()
		return nil, phoneForwardingRuntimeError(err)
	}
	return &phoneForwardingLease{lease: lease, ForwardingClient: forwarding}, nil
}

// phoneForwardingLease ties the forwarding capability to its Connection lease
// so a stop releases a healthy transport and an unrecoverable one is
// discarded. It mirrors the desktop adapter; the type lives here because that
// file is excluded from phone builds.
type phoneForwardingLease struct {
	lease *appconnection.Lease
	appconnection.ForwardingClient
}

func (l *phoneForwardingLease) Release() { _ = l.lease.Release() }
func (l *phoneForwardingLease) Discard() { _ = l.lease.Discard() }

// Done forwards the leased transport's lifetime so the runtime can stop
// reporting active when the Connection dies.
func (l *phoneForwardingLease) Done() <-chan struct{} { return l.lease.Done() }

// ListenTCP maps a connection-layer failure before the forwarding runtime
// classifies it, so a denied remote bind stays a permanent bind conflict
// instead of looking like a retryable network failure.
func (l *phoneForwardingLease) ListenTCP(address string) (net.Listener, error) {
	listener, err := l.ForwardingClient.ListenTCP(address)
	if err != nil {
		return nil, phoneForwardingRuntimeError(err)
	}
	return listener, nil
}

// phoneForwardingRuntimeError projects a typed connection or Vault failure
// onto the forwarding runtime's error kinds, preserving the failing jump hop.
// It mirrors the desktop adapter: the manager decides retryability from this
// kind, so a bind conflict, rejected credential, and rejected host key stay
// permanent instead of looking like a retryable network failure.
func phoneForwardingRuntimeError(err error) error {
	if errors.Is(err, appconnection.ErrBindConflict) {
		return appforwarding.NewRuntimeError(
			appforwarding.ErrorBindConflict, nil, err.Error(), err)
	}
	if errors.Is(err, appconnection.ErrForwardingUnavailable) {
		return appforwarding.NewRuntimeError(
			appforwarding.ErrorUnsupported, nil, err.Error(), err)
	}
	var typed *appconnection.Error
	if errors.As(err, &typed) {
		kind := appforwarding.ErrorNetwork
		switch typed.Kind {
		case appconnection.KindAuth:
			kind = appforwarding.ErrorAuth
		case appconnection.KindHostKey:
			kind = appforwarding.ErrorHostKey
		case appconnection.KindTimeout:
			kind = appforwarding.ErrorTimeout
		case appconnection.KindUnsupported:
			kind = appforwarding.ErrorUnsupported
		case appconnection.KindCancelled:
			kind = appforwarding.ErrorCancelled
		case appconnection.KindInternal:
			kind = appforwarding.ErrorInternal
		}
		var hop *appforwarding.Hop
		if typed.Hop != nil {
			hop = &appforwarding.Hop{
				Index: typed.Hop.Index,
				Total: typed.Hop.Total,
				Label: typed.Hop.Label,
			}
		}
		return appforwarding.NewRuntimeError(kind, hop, typed.Error(), err)
	}
	return appforwarding.NewRuntimeError(appforwarding.ErrorInternal, nil, err.Error(), err)
}

// phoneForwarding owns the manager and the lifecycle hooks the phone adds on
// top of the shared runtime. The hooks are registered only for the production
// runtime; tests construct the manager themselves and call the lifecycle
// methods directly.
type phoneForwarding struct {
	manager *appforwarding.Manager

	mu   sync.Mutex
	offs []func()
	// backgrounded records that the app left the foreground, so the launch
	// activation does not rebuild the rules the auto-start path is still
	// connecting.
	backgrounded bool
	// rebuildMu serializes a foreground and a network rebuild, which can
	// arrive close together. Rebuilds themselves run off the UI thread.
	rebuildMu sync.Mutex
	closed    bool
}

// newPhoneForwarding builds the phone runtime when the known-host repository
// also stores Port Forwarding Rules (the SQLite store does; a test's fake may
// not). watch installs the app lifecycle hooks and starts the auto-start
// rules, which is what the phone root wants and a focused unit test does not.
func newPhoneForwarding(rules appconnection.KnownHostRepository,
	vault phoneForwardingVault, routes routeSource,
	connections *appconnection.Manager, watch bool) *phoneForwarding {
	repo, ok := rules.(appforwarding.RuleRepository)
	if !ok || repo == nil {
		return nil
	}
	manager := appforwarding.NewManager(repo, phoneForwardingConnector{
		vault:       vault,
		routes:      routes,
		connections: connections,
	})
	runtime := &phoneForwarding{manager: manager}
	if !watch {
		return runtime
	}
	// A runtime event changes a status row. Repaint every phone window so the
	// new state appears without waiting for another tap.
	runtime.offs = append(runtime.offs, manager.Subscribe(func(appforwarding.Event) {
		for _, window := range mygo.Windows() {
			window.Invalidate()
		}
	}))
	// A process restart starts a fresh manager, so only the rules the user
	// marked auto-start run again.
	runtime.startAutoRules(context.Background())
	runtime.offs = append(runtime.offs,
		mygo.App.OnDidResignActive(func() { runtime.resignedActive() }),
		mygo.App.OnDidBecomeActive(func() { runtime.becameActive() }),
		mygo.Network.OnChanged(func(mygo.NetworkStatus) { runtime.networkChanged() }),
	)
	return runtime
}

// resignedActive records that the app left the foreground.
func (f *phoneForwarding) resignedActive() {
	if f == nil {
		return
	}
	f.mu.Lock()
	f.backgrounded = true
	f.mu.Unlock()
}

// becameActive rebuilds the rules only for a real return from the background.
// The activation the OS reports at launch must not abort the auto-start
// connects that are still in flight.
func (f *phoneForwarding) becameActive() {
	if f == nil {
		return
	}
	f.mu.Lock()
	wasBackgrounded := f.backgrounded
	f.backgrounded = false
	f.mu.Unlock()
	if wasBackgrounded {
		f.foreground()
	}
}

// foreground rebuilds the running rules after the app returns from the
// background.
func (f *phoneForwarding) foreground() { f.rebuild() }

// networkChanged rebuilds the running rules after the device's network path
// changed, so every direction dials the new route.
func (f *phoneForwarding) networkChanged() { f.rebuild() }

// ForwardingService is the Shell's seam onto the shared runtime.
func (f *phoneForwarding) ForwardingService() phoneshell.ForwardingService {
	if f == nil || f.manager == nil {
		return nil
	}
	return f.manager
}

// ForwardingService lets the phone Shell take the Port Forwarding runtime from
// the same Session opener it already receives, so the root wiring stays one
// option.
func (r *phoneSessionRuntime) ForwardingService() phoneshell.ForwardingService {
	if r == nil || r.forwarding == nil {
		return nil
	}
	return r.forwarding.ForwardingService()
}

var _ phoneshell.ForwardingProvider = (*phoneSessionRuntime)(nil)

// startAutoRules starts every enabled rule marked auto-start. It is also the
// restart filter: a rule without the flag stays stopped.
func (f *phoneForwarding) startAutoRules(ctx context.Context) {
	if f == nil || f.manager == nil {
		return
	}
	f.manager.AutoStart(ctx)
}

// rebuild re-establishes every running Port Forwarding Rule after a network
// change or a return to the foreground. It runs off the caller's thread
// because releasing a remote Port Forwarding Rule is bounded by the unforward
// timeout.
func (f *phoneForwarding) rebuild() {
	if f == nil || f.manager == nil {
		return
	}
	go func() {
		f.rebuildMu.Lock()
		defer f.rebuildMu.Unlock()
		f.mu.Lock()
		closed := f.closed
		f.mu.Unlock()
		if closed {
			return
		}
		f.manager.RebuildRunning(context.Background())
	}()
}

// Close stops the rules, releases their leases, and unregisters the lifecycle
// hooks. It is safe to call more than once.
func (f *phoneForwarding) Close() {
	if f == nil {
		return
	}
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return
	}
	f.closed = true
	offs := f.offs
	f.offs = nil
	f.mu.Unlock()
	for _, off := range offs {
		if off != nil {
			off()
		}
	}
	// Wait for an in-flight rebuild so manager.Close does not race its cleanup.
	// A rebuild queued behind this point sees closed and returns without
	// touching the manager.
	f.rebuildMu.Lock()
	defer f.rebuildMu.Unlock()
	if f.manager != nil {
		f.manager.Close()
	}
}
