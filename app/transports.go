package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/mslxl/natcatty/adapters/bundle"
	etadapter "github.com/mslxl/natcatty/adapters/et"
	moshadapter "github.com/mslxl/natcatty/adapters/mosh"
	appconnection "github.com/mslxl/natcatty/application/connection"
	appsession "github.com/mslxl/natcatty/application/session"
	appvault "github.com/mslxl/natcatty/application/vault"
	domainconn "github.com/mslxl/natcatty/domain/connection"
	"github.com/mslxl/natcatty/domain/model"
	etmodel "github.com/mslxl/natcatty/domain/model/et"
	moshmodel "github.com/mslxl/natcatty/domain/model/mosh"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// transportBundleRoots is the search order for bundled client binaries:
// NATCATTY_RESOURCES, next to the executable, a packaged Resources directory,
// and the working directory's resources during go run and tests.
func transportBundleRoots() []string {
	executable, _ := os.Executable()
	workingDir, _ := os.Getwd()
	return bundle.Roots("", executable, workingDir)
}

// newMoshFactory builds the Mosh transport. The bundled client is resolved and
// verified by adapters/bundle on every open; the SSH bootstrap runs over the
// app's Connection Manager so saved credentials, MFA prompts, and host-key
// trust behave exactly as they do for an SSH Session.
func newMoshFactory(manager *appconnection.Manager, routes routeSource, roots []string) appsession.Factory {
	return &moshTransportFactory{
		inner:   moshadapter.Factory{Spec: bundle.MoshSpec(roots)},
		manager: manager,
		routes:  routes,
	}
}

// moshTransportFactory checks the Mosh route restriction before any process or
// channel starts. Mosh's UDP channel cannot carry a jump chain or a proxy.
type moshTransportFactory struct {
	inner   moshadapter.Factory
	manager *appconnection.Manager
	routes  routeSource
}

func (f *moshTransportFactory) Open(ctx context.Context, spec appsession.StartSpec) (appsession.Transport, error) {
	if spec.Mosh == nil {
		return nil, fmt.Errorf("%w: no Mosh configuration", moshmodel.ErrInvalidConfig)
	}
	route, err := f.route(ctx, spec.HostID)
	if err != nil {
		return nil, err
	}
	if err := moshadapter.CheckRoute(len(route.Jumps), route.Proxy != nil); err != nil {
		return nil, err
	}
	if f.manager == nil {
		return nil, errors.New("app: no SSH connection manager for Mosh")
	}
	inner := f.inner
	inner.Bootstrap = &moshBootstrap{manager: f.manager, route: route}
	return inner.Open(ctx, spec)
}

// route resolves the non-secret jump chain and proxy for one Host.
func (f *moshTransportFactory) route(ctx context.Context, id model.HostID) (appconnection.Route, error) {
	if f.routes == nil {
		return appconnection.Route{}, nil
	}
	return f.routes.Route(ctx, id)
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

// etVault is the Vault surface an EternalTerminal Session needs: the Host, its
// resolved credentials, and the known-host snapshot. The Vault service
// implements it; tests provide a fake.
type etVault interface {
	Get(ctx context.Context, id model.HostID) (domainvault.Host, error)
	ResolveAuth(ctx context.Context, id model.HostID) (appvault.ResolvedAuth, error)
	ListKnownHosts(ctx context.Context) ([]domainconn.KnownHost, error)
}

// newETFactory builds the EternalTerminal transport. The bundled `et` performs
// its own SSH bootstrap, so the app gives it a private HOME with the resolved
// credentials, the Vault's known-host snapshot, and at most one jump Host.
func newETFactory(vault etVault, routes routeSource, roots []string) appsession.Factory {
	return &etTransportFactory{
		inner:  etadapter.Factory{Spec: bundle.ETSpec(roots)},
		vault:  vault,
		routes: routes,
	}
}

type etTransportFactory struct {
	inner  etadapter.Factory
	vault  etVault
	routes routeSource
}

func (f *etTransportFactory) Open(ctx context.Context, spec appsession.StartSpec) (appsession.Transport, error) {
	if spec.ET == nil {
		return nil, fmt.Errorf("%w: no EternalTerminal configuration", etmodel.ErrInvalidConfig)
	}
	if f.vault == nil {
		return nil, errors.New("app: no Vault for an EternalTerminal Session")
	}
	cfg := spec.ET.Normalized()

	var route appconnection.Route
	if f.routes != nil {
		resolved, err := f.routes.Route(ctx, spec.HostID)
		if err != nil {
			return nil, err
		}
		route = resolved
	}
	if err := etmodel.CheckRoute(len(route.Jumps), route.Proxy != nil); err != nil {
		return nil, err
	}

	cfg, err := f.configure(ctx, spec, cfg, route)
	if err != nil {
		return nil, err
	}
	spec.ET = &cfg
	return f.inner.Open(ctx, spec)
}

// configure adds the Vault's credentials, known-host snapshot, and single jump
// Host to a Session's non-secret configuration.
func (f *etTransportFactory) configure(ctx context.Context, spec appsession.StartSpec,
	cfg etmodel.Config, route appconnection.Route) (etmodel.Config, error) {

	host, err := f.vault.Get(ctx, spec.HostID)
	if err != nil {
		return cfg, routeError(fmt.Errorf("Host %s: %w", spec.HostID, err))
	}
	// The saved Host owns the ET port. A restored descriptor may carry an
	// older value, and the domain default (2022) is already filled in by
	// Normalized, so a recorded non-zero port always wins.
	if host.ETPort != 0 {
		cfg.ETPort = host.ETPort
	}

	auth, err := f.auth(ctx, host.ID)
	if err != nil {
		return cfg, err
	}
	cfg.Auth = auth.auth
	if auth.username != "" {
		cfg.Username = auth.username
	}

	known, err := f.vault.ListKnownHosts(ctx)
	if err != nil {
		return cfg, routeError(fmt.Errorf("Known Hosts: %w", err))
	}
	cfg.VerifyHostKeys = true
	cfg.KnownHostsContent = knownHostsContent(known)

	if len(route.Jumps) == 1 {
		hop := route.Jumps[0]
		jumpHost, err := f.vault.Get(ctx, hop.HostID)
		if err != nil {
			return cfg, routeError(fmt.Errorf("jump Host %s: %w", hop.HostID, err))
		}
		jumpAuth, err := f.auth(ctx, hop.HostID)
		if err != nil {
			return cfg, err
		}
		cfg.Jump = &etmodel.Hop{
			Hostname:         hop.Hostname,
			Port:             hop.Port,
			ETPort:           jumpHost.ETPort,
			Username:         firstNonEmpty(jumpAuth.username, hop.Username),
			Auth:             jumpAuth.auth,
			LegacyAlgorithms: jumpHost.LegacyAlgorithms,
		}
		cfg.JumpKnownHostsContent = cfg.KnownHostsContent
	}
	return cfg, nil
}

// etResolvedAuth is one endpoint's username and authentication material.
type etResolvedAuth struct {
	username string
	auth     etmodel.Auth
}

// auth resolves the same credentials the SSH Session path would offer, so a
// saved password, Key, certificate, or agent setting reaches `et` unchanged.
func (f *etTransportFactory) auth(ctx context.Context, id model.HostID) (etResolvedAuth, error) {
	resolved, err := f.vault.ResolveAuth(ctx, id)
	if err != nil {
		return etResolvedAuth{}, routeError(fmt.Errorf("credentials for Host %s: %w", id, err))
	}
	selection := resolved.Selection
	material := etmodel.Auth{
		Method:      string(selection.Method),
		Password:    resolved.Password,
		UseSSHAgent: selection.UseSSHAgent,
		RequiresMFA: selection.RequiresMFA,
	}.Normalized()
	if resolved.Key != nil {
		material.PrivateKey = resolved.Key.PrivateKey
		material.Passphrase = resolved.Key.Passphrase
		material.Certificate = resolved.Key.Certificate
	}
	// A dangling Key reference must fail closed: SSH_ASKPASS cannot invent the
	// missing material, and falling back to the password would silently change
	// the configured route.
	if resolved.MissingKey {
		return etResolvedAuth{}, &appconnection.Error{
			Kind:  appconnection.KindAuth,
			Cause: appconnection.ErrNoCredentials,
		}
	}
	return etResolvedAuth{username: selection.Username, auth: material}, nil
}

// knownHostsContent renders the Vault's trust decisions as one authoritative
// OpenSSH known_hosts snapshot. The adapter points both UserKnownHostsFile and
// GlobalKnownHostsFile at it, so a system file cannot override a Vault pin.
func knownHostsContent(records []domainconn.KnownHost) string {
	var builder strings.Builder
	for _, record := range records {
		line := knownHostsLine(record)
		if line == "" {
			continue
		}
		builder.WriteString(line)
		builder.WriteByte('\n')
	}
	return builder.String()
}

func knownHostsLine(record domainconn.KnownHost) string {
	hostname := strings.ToLower(strings.TrimSpace(record.Hostname))
	if hostname == "" || hostname == "(hashed)" {
		return ""
	}
	fields := strings.Fields(record.PublicKey)
	if len(fields) < 2 || !isPublicKeyType(fields[0]) {
		return ""
	}
	host := hostname
	if record.Port != 0 && record.Port != 22 {
		host = "[" + hostname + "]:" + strconv.Itoa(record.Port)
	}
	return host + " " + fields[0] + " " + fields[1]
}

func isPublicKeyType(value string) bool {
	return strings.HasPrefix(value, "ssh-") ||
		strings.HasPrefix(value, "ecdsa-") ||
		strings.HasPrefix(value, "sk-")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
