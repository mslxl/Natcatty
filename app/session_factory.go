package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"

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

	lease, terminal, err := f.acquireTerminal(ctx, spec, route)
	if err != nil {
		return nil, err
	}
	return &leasedTransport{terminal: terminal, lease: lease}, nil
}

// acquireTerminal opens a shell on a Connection lease. A parked transport that
// dies during the liveness window is discarded by the pool; one retry opens a
// fresh transport instead of failing the Session.
func (f *transportFactory) acquireTerminal(ctx context.Context, spec appsession.StartSpec, route appconnection.Route) (*appconnection.Lease, appconnection.Terminal, error) {
	request := appconnection.Request{
		HostID:            spec.HostID,
		Hostname:          spec.Hostname,
		Port:              spec.Port,
		Username:          spec.Username,
		Jumps:             route.Jumps,
		Proxy:             route.Proxy,
		Algorithms:        route.Algorithms,
		ConfigFingerprint: route.ConfigFingerprint,
		LeaseKind:         appconnection.LeaseShell,
		Keepalive:         &appconnection.DefaultKeepalive,
	}
	specification := appconnection.TerminalSpec{
		Cols: spec.Cols,
		Rows: spec.Rows,
		Term: "xterm-256color",
	}
	for attempt := 0; ; attempt++ {
		lease, err := f.manager.Acquire(ctx, request)
		if err != nil {
			return nil, nil, err
		}
		terminal, err := lease.OpenTerminal(ctx, specification)
		if err == nil {
			return lease, terminal, nil
		}
		_ = lease.Discard()
		if attempt == 0 && errors.Is(err, appconnection.ErrStaleTransport) {
			continue
		}
		return nil, nil, err
	}
}

// testConnection opens and releases a Connection for a Host without opening a
// shell. Success means TCP, every jump hop, host-key verification, and
// authentication all completed; a typed failure keeps its kind for the Vault
// to localize.
func testConnection(ctx context.Context, manager *appconnection.Manager, routes routeSource, host domainvault.Host) error {
	if manager == nil {
		return routeError(errors.New("app: no SSH connection manager"))
	}
	var route appconnection.Route
	if routes != nil {
		resolved, err := routes.Route(ctx, host.ID)
		if err != nil {
			return err
		}
		route = resolved
	}
	return manager.Test(ctx, appconnection.Request{
		HostID:            host.ID,
		Hostname:          host.Hostname,
		Port:              host.Port,
		Username:          host.Username,
		Jumps:             route.Jumps,
		Proxy:             route.Proxy,
		Algorithms:        route.Algorithms,
		ConfigFingerprint: route.ConfigFingerprint,
		LeaseKind:         appconnection.LeaseShell,
		Keepalive:         &appconnection.DefaultKeepalive,
	})
}

// leasedTransport adapts an SSH terminal to the Session transport port and
// ties the Connection lease to the transport's lifetime. A transport that ends
// in an error is discarded; a deliberate shell exit or a user-initiated close
// of a live shell leaves the Connection parked for reuse.
type leasedTransport struct {
	terminal appconnection.Terminal
	lease    *appconnection.Lease

	mu     sync.Mutex
	closed bool
	ended  bool
	end    appsession.End
}

func (t *leasedTransport) Read(p []byte) (int, error) {
	n, err := t.terminal.Read(p)
	if err != nil {
		// Classify now, while the channel still carries the remote exit status.
		// Close must not have to ask a channel it just hung up why it ended.
		t.End()
	}
	return n, err
}

func (t *leasedTransport) Write(p []byte) (int, error) {
	n, err := t.terminal.Write(p)
	if err != nil {
		t.remember(appsession.End{Reason: appsession.EndReasonError})
	}
	return n, err
}

func (t *leasedTransport) Resize(cols, rows int) error { return t.terminal.Resize(cols, rows) }

// End maps the adapter's exit vocabulary onto the Session's. A terminal that
// cannot classify its end is treated as a transport error so reconnect policy
// gives it a chance. The first classification is cached, because asking after
// Close loses the remote exit status.
func (t *leasedTransport) End() appsession.End {
	t.mu.Lock()
	ended, end := t.ended, t.end
	t.mu.Unlock()
	if ended {
		return end
	}

	ending, ok := t.terminal.(appconnection.EndingTerminal)
	if !ok {
		return t.remember(appsession.End{Reason: appsession.EndReasonError})
	}
	reported := ending.End()
	return t.remember(appsession.End{
		Reason:      appsession.EndReason(reported.Reason),
		ExitCode:    reported.ExitCode,
		HasExitCode: reported.HasExitCode,
	})
}

func (t *leasedTransport) remember(end appsession.End) appsession.End {
	t.mu.Lock()
	if !t.ended {
		t.ended = true
		t.end = end
	}
	t.mu.Unlock()
	return end
}

func (t *leasedTransport) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	ended, end := t.ended, t.end
	t.mu.Unlock()

	terminalErr := t.terminal.Close()
	// Only a transport that itself ended in an error or a bare close is
	// unusable. Closing a live shell (user close, app quit) frees the lease so
	// the Connection can be parked while other leases keep it open.
	if ended && (end.Reason == appsession.EndReasonError || end.Reason == appsession.EndReasonClosed) {
		_ = t.lease.Discard()
	} else {
		_ = t.lease.Release()
	}
	return terminalErr
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

// authMaterialResolver is implemented by the Vault service. Route fakes need
// not implement it; when present, its digest scopes pool reuse to the resolved
// credentials so rotating a password or Key never reuses a parked transport.
type authMaterialResolver interface {
	ResolveAuth(ctx context.Context, hostID model.HostID) (appvault.ResolvedAuth, error)
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

	route := appconnection.Route{
		Algorithms:        algorithmPolicy(target),
		ConfigFingerprint: r.configFingerprint(ctx, target),
	}
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
				HostID:            jump.ID,
				Label:             jump.Label,
				Hostname:          jump.Hostname,
				Port:              jump.Port,
				Username:          jump.Username,
				Algorithms:        algorithmPolicy(jump),
				ConfigFingerprint: r.configFingerprint(ctx, jump),
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

// configFingerprint digests the connection configuration that scopes pool
// reuse. A changed credential selection, rotated credential, MFA requirement,
// or algorithm policy opens a fresh authenticated transport instead of reusing
// a parked one built from the old configuration.
func (r vaultRoutes) configFingerprint(ctx context.Context, host domainvault.Host) string {
	return configFingerprint(host, r.authMaterial(ctx, host.ID))
}

// authMaterial digests the credentials the Vault would offer this Host. The
// raw values are never retained: only the digest reaches the pool key. A Vault
// that cannot resolve them yields a stable placeholder, and the dial itself
// reports the typed failure.
func (r vaultRoutes) authMaterial(ctx context.Context, id model.HostID) string {
	resolver, ok := r.vault.(authMaterialResolver)
	if !ok {
		return ""
	}
	resolved, err := resolver.ResolveAuth(ctx, id)
	if err != nil {
		return "unresolved"
	}
	digest := sha256.New()
	fmt.Fprintf(digest, "password=%q\nmissing=%t\n", resolved.Password, resolved.MissingKey)
	if resolved.Key != nil {
		fmt.Fprintf(digest, "key=%q\npassphrase=%q\ncertificate=%q\n",
			resolved.Key.PrivateKey, resolved.Key.Passphrase, resolved.Key.Certificate)
	}
	return hex.EncodeToString(digest.Sum(nil))[:32]
}

func configFingerprint(host domainvault.Host, material string) string {
	digest := sha256.New()
	fmt.Fprintf(digest, "auth=%q\nidentity=%q\nkey=%q\nagent=%t\nmfa=%t\nlegacy=%t\necdsa=%t\nmaterial=%s\n",
		host.AuthMethod, host.IdentityID, host.KeyID, host.UseSSHAgent, host.RequiresMFA,
		host.LegacyAlgorithms, host.SkipEcdsaHostKey, material)
	if overrides := host.Algorithms; overrides != nil {
		fmt.Fprintf(digest, "ciphers=%q\nkex=%q\nmacs=%q\nhostkeys=%q\n",
			overrides.Ciphers, overrides.KeyExchanges, overrides.MACs, overrides.HostKeys)
	}
	return hex.EncodeToString(digest.Sum(nil))[:32]
}

func routeError(cause error) error {
	return &appconnection.Error{Kind: appconnection.KindInternal, Cause: cause}
}
