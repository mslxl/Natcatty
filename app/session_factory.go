package app

import (
	"context"
	"errors"
	"fmt"

	appconnection "github.com/mslxl/natcatty/application/connection"
	appsession "github.com/mslxl/natcatty/application/session"
	appvault "github.com/mslxl/natcatty/application/vault"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// routeSource resolves the non-secret jump chain and proxy for a Host. The app
// backs it with the Vault; tests provide a fake.
type routeSource interface {
	Route(ctx context.Context, id model.HostID) (appconnection.Route, error)
}

// transportFactory routes a Session to the local PTY adapter or to an SSH
// Connection. The SSH path acquires a lease for the Session's lifetime and
// releases it when the transport closes, which is the seam ticket 09's pool
// extends.
type transportFactory struct {
	local   appsession.Factory
	manager *appconnection.Manager
	routes  routeSource
}

func newTransportFactory(local appsession.Factory, manager *appconnection.Manager, routes routeSource) appsession.Factory {
	return &transportFactory{local: local, manager: manager, routes: routes}
}

func (f *transportFactory) Open(ctx context.Context, spec appsession.StartSpec) (appsession.Transport, error) {
	if spec.Kind != appsession.KindSSH {
		return f.local.Open(ctx, spec)
	}
	if f.manager == nil {
		return nil, errors.New("app: no SSH connection manager")
	}

	var route appconnection.Route
	if f.routes != nil {
		resolved, err := f.routes.Route(ctx, spec.HostID)
		if err != nil {
			return nil, err
		}
		route = resolved
	}

	lease, err := f.manager.Acquire(ctx, appconnection.Request{
		HostID:     spec.HostID,
		Hostname:   spec.Hostname,
		Port:       spec.Port,
		Username:   spec.Username,
		Jumps:      route.Jumps,
		Proxy:      route.Proxy,
		Algorithms: route.Algorithms,
	})
	if err != nil {
		return nil, err
	}

	terminal, err := lease.OpenTerminal(ctx, appconnection.TerminalSpec{
		Cols: spec.Cols,
		Rows: spec.Rows,
		Term: "xterm-256color",
	})
	if err != nil {
		_ = lease.Release()
		return nil, err
	}
	return &leasedTransport{terminal: terminal, lease: lease}, nil
}

// leasedTransport adapts an SSH terminal to the Session transport port and
// ties the Connection lease to the transport's lifetime.
type leasedTransport struct {
	terminal appconnection.Terminal
	lease    *appconnection.Lease
}

func (t *leasedTransport) Read(p []byte) (int, error)  { return t.terminal.Read(p) }
func (t *leasedTransport) Write(p []byte) (int, error) { return t.terminal.Write(p) }

func (t *leasedTransport) Resize(cols, rows int) error { return t.terminal.Resize(cols, rows) }

func (t *leasedTransport) Close() error {
	terminalErr := t.terminal.Close()
	leaseErr := t.lease.Release()
	if terminalErr != nil {
		return terminalErr
	}
	return leaseErr
}

// hostSecrets is the Vault read the password source needs.
type hostSecrets interface {
	Secret(ctx context.Context, id model.HostID, field string) (string, error)
}

// vaultPasswords reads a Host's saved password from the Vault. A Host with no
// saved password is an auth failure the user can fix by editing the Host, not
// an internal storage error.
type vaultPasswords struct {
	vault hostSecrets
}

func (p vaultPasswords) Password(ctx context.Context, id model.HostID) (string, error) {
	if p.vault == nil {
		return "", &appconnection.Error{Kind: appconnection.KindAuth, Cause: appconnection.ErrNoCredentials}
	}
	value, err := p.vault.Secret(ctx, id, appvault.FieldPassword)
	if err != nil {
		if errors.Is(err, appvault.ErrNotFound) {
			return "", &appconnection.Error{Kind: appconnection.KindAuth, Cause: appconnection.ErrNoCredentials}
		}
		// A locked, unavailable, or failing secret store is not a rejected
		// credential. Keep it internal so the UI does not tell the user their
		// password is wrong when the Vault could not read it.
		return "", &appconnection.Error{Kind: appconnection.KindInternal, Cause: err}
	}
	if value == "" {
		return "", &appconnection.Error{Kind: appconnection.KindAuth, Cause: appconnection.ErrNoCredentials}
	}
	return value, nil
}

// routeVault is the Vault read surface the route resolver needs.
type routeVault interface {
	Get(ctx context.Context, id model.HostID) (domainvault.Host, error)
	Secret(ctx context.Context, id model.HostID, field string) (string, error)
}

// vaultRoutes resolves a Host's ordered jump chain and its effective proxy.
// The proxy belongs to the first endpoint, matching the original: the first
// jump's proxy wins, otherwise the target Host's proxy is used. Later hops
// never use a proxy because they travel inside the SSH chain.
type vaultRoutes struct {
	vault routeVault
}

func (r vaultRoutes) Route(ctx context.Context, id model.HostID) (appconnection.Route, error) {
	if r.vault == nil {
		return appconnection.Route{}, routeError(errors.New("app: no Vault for route resolution"))
	}
	target, err := r.vault.Get(ctx, id)
	if err != nil {
		return appconnection.Route{}, routeError(fmt.Errorf("Host %s: %w", id, err))
	}

	route := appconnection.Route{Algorithms: algorithmPolicy(target)}
	var first *domainvault.Host
	if target.HostChain != nil {
		for _, jumpID := range target.HostChain.HostIDs {
			jump, err := r.vault.Get(ctx, jumpID)
			if err != nil {
				return appconnection.Route{}, routeError(fmt.Errorf("jump Host %s: %w", jumpID, err))
			}
			if first == nil {
				first = &jump
			}
			route.Jumps = append(route.Jumps, appconnection.Hop{
				HostID:     jump.ID,
				Label:      jump.Label,
				Hostname:   jump.Hostname,
				Port:       jump.Port,
				Username:   jump.Username,
				Algorithms: algorithmPolicy(jump),
			})
		}
	}

	proxyOwner := target
	if first != nil && first.Proxy != nil {
		proxyOwner = *first
	}
	proxy, err := r.proxy(ctx, proxyOwner)
	if err != nil {
		return appconnection.Route{}, err
	}
	route.Proxy = proxy
	return route, nil
}

func (r vaultRoutes) proxy(ctx context.Context, host domainvault.Host) (*appconnection.Proxy, error) {
	if host.Proxy == nil {
		return nil, nil
	}
	proxy := &appconnection.Proxy{
		Type:     appconnection.ProxyType(host.Proxy.Type),
		Host:     host.Proxy.Host,
		Port:     host.Proxy.Port,
		Command:  host.Proxy.Command,
		Username: host.Proxy.Username,
	}
	// Read the proxy credential regardless of HasPassword. Session-only
	// secrets live in memory and the record deliberately does not advertise
	// them, so gating on the flag would lose a configured password.
	password, err := r.vault.Secret(ctx, host.ID, appvault.FieldProxyPassword)
	switch {
	case err == nil:
		proxy.Password = password
	case errors.Is(err, appvault.ErrNotFound):
		if host.Proxy.HasPassword {
			// The record claims a password that the secret store no longer
			// holds. Connecting without it would silently change the user's
			// configured route, so treat the stale record as invalid.
			return nil, routeError(fmt.Errorf("%w: stored password for the proxy on Host %s is missing", appconnection.ErrInvalidProxy, host.ID))
		}
	default:
		return nil, routeError(fmt.Errorf("proxy password for Host %s: %w", host.ID, err))
	}
	return proxy, nil
}

// algorithmPolicy maps a Host's advanced SSH settings onto the transport's
// per-endpoint policy.
func algorithmPolicy(host domainvault.Host) appconnection.AlgorithmPolicy {
	policy := appconnection.AlgorithmPolicy{
		Legacy:           host.LegacyAlgorithms,
		SkipECDSAHostKey: host.SkipEcdsaHostKey,
	}
	if overrides := host.Algorithms; overrides != nil {
		policy.Ciphers = append([]string(nil), overrides.Ciphers...)
		policy.KeyExchanges = append([]string(nil), overrides.KeyExchanges...)
		policy.MACs = append([]string(nil), overrides.MACs...)
		policy.HostKeys = append([]string(nil), overrides.HostKeys...)
	}
	return policy
}

func routeError(cause error) error {
	return &appconnection.Error{Kind: appconnection.KindInternal, Cause: cause}
}
