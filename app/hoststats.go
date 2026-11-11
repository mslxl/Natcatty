package app

import (
	"context"
	"errors"
	"runtime"

	appconnection "github.com/mslxl/natcatty/application/connection"
	apphoststats "github.com/mslxl/natcatty/application/hoststats"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/platform/process"
)

// hostStatsConnector adapts the shared Connection Manager to the Host Stats
// collector. Monitoring and the System manager share one collector, so a
// Host's counters advance the same way whichever surface asked. Each
// collection opens a channel lease through Acquire, running the same host-key
// verification, trust prompt, jump chain, proxy, and algorithm policy a
// Session uses. A local-shell Host runs the same commands in this computer's
// shell instead.
type hostStatsConnector struct {
	routes      routeSource
	connections *appconnection.Manager
}

var _ apphoststats.Connector = hostStatsConnector{}

func (c hostStatsConnector) Open(ctx context.Context, host domainvault.Host) (apphoststats.Connection, error) {
	if host.Protocol == domainvault.ProtocolLocal {
		if runtime.GOOS == "windows" {
			// The commands are POSIX shell; cmd.exe cannot run them.
			return nil, &apphoststats.Error{Reason: apphoststats.ReasonOS,
				Cause: errors.New("no POSIX shell on Windows")}
		}
		return localHostStatsConnection{}, nil
	}
	if c.connections == nil {
		return nil, &apphoststats.Error{Reason: apphoststats.ReasonInternal,
			Cause: errors.New("no connection manager")}
	}
	var route appconnection.Route
	if c.routes != nil {
		var err error
		route, err = c.routes.Route(ctx, host.ID)
		if err != nil {
			return nil, hostStatsFailure(err)
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
		// Mosh and EternalTerminal reach their Host through this independent
		// SSH lease, so sampling never disturbs their session transport.
		LeaseKind: appconnection.LeaseChannel,
		Keepalive: &appconnection.DefaultKeepalive,
	})
	if err != nil {
		return nil, hostStatsFailure(err)
	}
	return &hostStatsLeaseConnection{lease: lease}, nil
}

// hostStatsLeaseConnection is one leased Host transport used only for stats
// commands.
type hostStatsLeaseConnection struct {
	lease *appconnection.Lease
}

func (c *hostStatsLeaseConnection) Exec(ctx context.Context, command string) (apphoststats.ExecResult, error) {
	result, err := c.lease.Exec(ctx, command)
	if err != nil {
		return apphoststats.ExecResult{}, hostStatsFailure(err)
	}
	return apphoststats.ExecResult{
		Stdout:      result.Stdout,
		Stderr:      result.Stderr,
		ExitCode:    result.ExitCode,
		HasExitCode: result.HasExitCode,
	}, nil
}

func (c *hostStatsLeaseConnection) Close() error { return c.lease.Release() }

// SingleChannel forwards the peer's one-channel property, so the collector
// can refuse to consume the only channel a bastion accepts.
func (c *hostStatsLeaseConnection) SingleChannel() bool { return c.lease.SingleChannel() }

// localHostStatsConnection runs the stats command in this computer's shell, as
// an SSH exec runs it on a Host.
type localHostStatsConnection struct{}

func (localHostStatsConnection) Exec(ctx context.Context, command string) (apphoststats.ExecResult, error) {
	result, err := process.Run(ctx, command)
	if err != nil {
		return apphoststats.ExecResult{}, hostStatsFailure(err)
	}
	return apphoststats.ExecResult{
		Stdout:      string(result.Stdout),
		Stderr:      string(result.Stderr),
		ExitCode:    result.ExitCode,
		HasExitCode: true,
	}, nil
}

func (localHostStatsConnection) Close() error { return nil }

// hostStatsFailure projects the connection layer's typed failures onto the
// collector's stable reasons. The consumers localize a reason; a transport
// error never becomes display text.
func hostStatsFailure(err error) error {
	if err == nil {
		return nil
	}
	var typed *apphoststats.Error
	if errors.As(err, &typed) {
		return err
	}
	reason := apphoststats.ReasonNetwork
	switch {
	case errors.Is(err, appconnection.ErrHostKeyRejected):
		reason = apphoststats.ReasonHostKey
	case errors.Is(err, appconnection.ErrAuthentication),
		errors.Is(err, appconnection.ErrNoCredentials),
		errors.Is(err, appconnection.ErrPromptCancelled):
		reason = apphoststats.ReasonAuth
	case errors.Is(err, appconnection.ErrDialTimeout),
		errors.Is(err, appconnection.ErrAuthTimeout):
		reason = apphoststats.ReasonTimeout
	case errors.Is(err, appconnection.ErrUnsupportedAlgorithm),
		errors.Is(err, appconnection.ErrUnsupportedProxy):
		reason = apphoststats.ReasonUnsupported
	}
	var connectionError *appconnection.Error
	if errors.As(err, &connectionError) {
		switch connectionError.Kind {
		case appconnection.KindAuth:
			reason = apphoststats.ReasonAuth
		case appconnection.KindTimeout:
			reason = apphoststats.ReasonTimeout
		case appconnection.KindHostKey:
			reason = apphoststats.ReasonHostKey
		case appconnection.KindUnsupported:
			reason = apphoststats.ReasonUnsupported
		case appconnection.KindCancelled:
			reason = apphoststats.ReasonCancelled
		case appconnection.KindInternal:
			reason = apphoststats.ReasonInternal
		}
	}
	if errors.Is(err, context.Canceled) {
		reason = apphoststats.ReasonCancelled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		reason = apphoststats.ReasonTimeout
	}
	return &apphoststats.Error{Reason: reason, Cause: err}
}
