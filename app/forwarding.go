package app

import (
	"context"
	"errors"
	"net"

	appconnection "github.com/mslxl/natcatty/application/connection"
	appforwarding "github.com/mslxl/natcatty/application/forwarding"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	uiforwarding "github.com/mslxl/natcatty/ui/forwarding"
)

// forwardingVault is the Vault read the forwarding connector needs to build a
// Connection request from a Port Forwarding Rule's Host.
type forwardingVault interface {
	Get(ctx context.Context, id model.HostID) (domainvault.Host, error)
}

// forwardingConnector adapts the shared Connection Manager to the forwarding
// runtime's Connector port. Each running rule holds one Connection lease for
// its lifetime; jump chains, proxies, host-key verification, and algorithm
// policy all come from the same route resolver a Session uses.
type forwardingConnector struct {
	vault       forwardingVault
	routes      routeSource
	connections *appconnection.Manager
}

var _ appforwarding.Connector = forwardingConnector{}

func (c forwardingConnector) Connect(ctx context.Context, hostID model.HostID) (appforwarding.Connection, error) {
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
		return nil, forwardingRuntimeError(err)
	}
	var route appconnection.Route
	if c.routes != nil {
		route, err = c.routes.Route(ctx, hostID)
		if err != nil {
			return nil, forwardingRuntimeError(err)
		}
	}

	lease, err := c.connections.Acquire(ctx, appconnection.Request{
		HostID:            host.ID,
		Hostname:          host.Hostname,
		Port:              host.Port,
		Username:          host.Username,
		Jumps:             route.Jumps,
		Proxy:             route.Proxy,
		Algorithms:        route.Algorithms,
		ConfigFingerprint: route.ConfigFingerprint,
		// A forwarding rule shares a transport with other channel leases but
		// never with a Session shell.
		LeaseKind: appconnection.LeaseChannel,
		Keepalive: &appconnection.DefaultKeepalive,
	})
	if err != nil {
		return nil, forwardingRuntimeError(err)
	}
	forwarding, err := lease.Forwarding()
	if err != nil {
		_ = lease.Discard()
		return nil, forwardingRuntimeError(err)
	}
	return &forwardingLease{lease: lease, ForwardingClient: forwarding}, nil
}

// forwardingLease ties the forwarding capability to its Connection lease so a
// stop releases a healthy transport and an unrecoverable one is discarded.
type forwardingLease struct {
	lease *appconnection.Lease
	appconnection.ForwardingClient
}

func (l *forwardingLease) Release() { _ = l.lease.Release() }
func (l *forwardingLease) Discard() { _ = l.lease.Discard() }

// Done forwards the leased transport's lifetime so the runtime can stop
// reporting active when the Connection dies.
func (l *forwardingLease) Done() <-chan struct{} { return l.lease.Done() }

// ListenTCP maps a connection-layer failure before the forwarding runtime
// classifies it, so a denied remote bind stays a permanent bind conflict
// instead of looking like a retryable network failure.
func (l *forwardingLease) ListenTCP(address string) (net.Listener, error) {
	listener, err := l.ForwardingClient.ListenTCP(address)
	if err != nil {
		return nil, forwardingRuntimeError(err)
	}
	return listener, nil
}

// forwardingRuntimeError projects a typed connection or Vault failure onto the
// forwarding runtime's error kinds, preserving the failing jump hop. The
// forwarding manager decides retryability from this kind; a bind conflict is
// permanent because reconnecting cannot free the listen address.
func forwardingRuntimeError(err error) error {
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
	return appforwarding.NewRuntimeError(
		appforwarding.ErrorInternal, nil, err.Error(), err)
}

// closeForwarding stops every running rule and releases the runtime surface's
// event subscription. It is safe to call from either window-close path.
func closeForwarding(manager *appforwarding.Manager, surface *uiforwarding.State) {
	if surface != nil {
		surface.Close()
	}
	if manager != nil {
		manager.Close()
	}
}
