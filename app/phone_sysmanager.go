// Phone System Manager wiring.
//
// The phone reuses application/sysmanager/core — the same capability probe,
// tab sampling, Docker inspect/logs, tmux, Kubernetes/Pod, permission
// classification, and confirmed-action executor the desktop surface drives —
// through the phone's own Connection Manager and Vault route resolver. Each
// surface receives its own Service, so a page that closes releases every
// channel lease it opened.
//
// The shared core is imported directly rather than through the traditional
// application/sysmanager path: the phone dependency gate forbids that exact
// path so the desktop UI and the whole application service cannot slip into a
// phone build. The core package imports only domain/vault and the standard
// library.

package app

import (
	"context"
	"errors"

	appconnection "github.com/mslxl/natcatty/application/connection"
	appsysmanager "github.com/mslxl/natcatty/application/sysmanager/core"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/phoneshell"
)

// phoneSystemConnector adapts the shared Connection Manager to the System
// Manager's Connector port. Every sample and every action opens a channel
// lease through Acquire, so the same host-key verification, trust prompt,
// jump chain, proxy, Network Profile, and algorithm policy a Session uses
// gates every remote command.
type phoneSystemConnector struct {
	routes      routeSource
	connections *appconnection.Manager
}

var _ appsysmanager.Connector = phoneSystemConnector{}

func (c phoneSystemConnector) Open(ctx context.Context, host domainvault.Host) (appsysmanager.Connection, error) {
	// The manager runs POSIX commands over an SSH channel. Mosh and
	// EternalTerminal bootstrap through SSH; Telnet and a local shell have no
	// such channel, so the surface fails closed instead of pretending.
	if !host.Protocol.UsesSSHAuth() {
		return nil, &appsysmanager.Error{
			Reason: appsysmanager.ReasonUnsupported,
			Cause:  errors.New("the Host has no system shell over SSH"),
		}
	}
	if c.connections == nil {
		return nil, &appsysmanager.Error{Reason: appsysmanager.ReasonInternal,
			Cause: errors.New("no connection manager")}
	}
	var route appconnection.Route
	if c.routes != nil {
		var err error
		route, err = c.routes.Route(ctx, host.ID)
		if err != nil {
			return nil, phoneSystemFailure(err)
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
		// A channel lease is enough for exec and never claims a Session shell.
		LeaseKind: appconnection.LeaseChannel,
		Keepalive: &appconnection.DefaultKeepalive,
	})
	if err != nil {
		return nil, phoneSystemFailure(err)
	}
	return &phoneSystemConnection{lease: lease}, nil
}

// phoneSystemConnection is one leased Host transport used only for fixed exec
// commands.
type phoneSystemConnection struct {
	lease *appconnection.Lease
}

func (c *phoneSystemConnection) Exec(ctx context.Context, command string) (appsysmanager.ExecResult, error) {
	result, err := c.lease.Exec(ctx, command)
	if err != nil {
		return appsysmanager.ExecResult{}, phoneSystemFailure(err)
	}
	return appsysmanager.ExecResult{
		Stdout:      result.Stdout,
		Stderr:      result.Stderr,
		ExitCode:    result.ExitCode,
		HasExitCode: result.HasExitCode,
	}, nil
}

func (c *phoneSystemConnection) Close() error { return c.lease.Release() }

// phoneSystemFailure projects the connection layer's typed failures onto the
// System Manager's stable reasons. The view localizes a reason; a transport
// error never becomes display text.
func phoneSystemFailure(err error) error {
	if err == nil {
		return nil
	}
	var typed *appsysmanager.Error
	if errors.As(err, &typed) {
		return err
	}
	reason := appsysmanager.ReasonNetwork
	switch {
	case errors.Is(err, appconnection.ErrHostKeyRejected):
		reason = appsysmanager.ReasonHostKey
	case errors.Is(err, appconnection.ErrAuthentication),
		errors.Is(err, appconnection.ErrNoCredentials),
		errors.Is(err, appconnection.ErrPromptCancelled):
		reason = appsysmanager.ReasonAuth
	case errors.Is(err, appconnection.ErrDialTimeout),
		errors.Is(err, appconnection.ErrAuthTimeout):
		reason = appsysmanager.ReasonTimeout
	case errors.Is(err, appconnection.ErrUnsupportedAlgorithm),
		errors.Is(err, appconnection.ErrUnsupportedProxy):
		reason = appsysmanager.ReasonUnsupported
	}
	var connectionError *appconnection.Error
	if errors.As(err, &connectionError) {
		switch connectionError.Kind {
		case appconnection.KindAuth:
			reason = appsysmanager.ReasonAuth
		case appconnection.KindTimeout:
			reason = appsysmanager.ReasonTimeout
		case appconnection.KindHostKey:
			reason = appsysmanager.ReasonHostKey
		case appconnection.KindUnsupported:
			reason = appsysmanager.ReasonUnsupported
		case appconnection.KindCancelled:
			reason = appsysmanager.ReasonCancelled
		case appconnection.KindInternal:
			reason = appsysmanager.ReasonInternal
		}
	}
	if errors.Is(err, context.Canceled) {
		reason = appsysmanager.ReasonCancelled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		reason = appsysmanager.ReasonTimeout
	}
	return &appsysmanager.Error{Reason: reason, Cause: err}
}

// NewSystemService builds one System Manager runtime over this phone's
// Connection Manager and the same Vault route resolver a Session uses. The
// page that called it closes it, which releases its sampler and leases.
func (r *phoneSessionRuntime) NewSystemService() phoneshell.SystemService {
	if r == nil || r.connections == nil {
		return nil
	}
	return appsysmanager.NewService(phoneSystemConnector{
		routes:      r.routes,
		connections: r.connections,
	})
}

var _ phoneshell.SystemProvider = (*phoneSessionRuntime)(nil)
