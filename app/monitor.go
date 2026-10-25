package app

import (
	"context"
	"errors"

	appconnection "github.com/mslxl/natcatty/application/connection"
	appmonitor "github.com/mslxl/natcatty/application/monitor"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	uimonitoring "github.com/mslxl/natcatty/ui/monitoring"
)

// monitorConnector adapts the shared Connection Manager to the monitoring
// runtime. Each sampled Host holds one channel lease; the Acquire call runs
// the same host-key verification, trust prompt, jump-chain, proxy, and
// algorithm policy a Session uses, so no metric command can run before the
// Host key is trusted.
type monitorConnector struct {
	routes      routeSource
	connections *appconnection.Manager
}

var _ appmonitor.Connector = monitorConnector{}

func (c monitorConnector) Open(ctx context.Context, host domainvault.Host) (appmonitor.Connection, error) {
	if c.connections == nil {
		return nil, &appmonitor.Error{Reason: appmonitor.ReasonInternal,
			Cause: errors.New("no connection manager")}
	}
	var route appconnection.Route
	if c.routes != nil {
		var err error
		route, err = c.routes.Route(ctx, host.ID)
		if err != nil {
			return nil, monitorFailure(err)
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
		// Monitoring shares a channel-capable transport but never a Session
		// shell.
		LeaseKind: appconnection.LeaseChannel,
		Keepalive: &appconnection.DefaultKeepalive,
	})
	if err != nil {
		return nil, monitorFailure(err)
	}
	return &monitorConnection{lease: lease}, nil
}

// monitorConnection is one leased Host transport used only for metric commands.
type monitorConnection struct {
	lease *appconnection.Lease
}

func (m *monitorConnection) Exec(ctx context.Context, command string) (appmonitor.ExecResult, error) {
	result, err := m.lease.Exec(ctx, command)
	if err != nil {
		return appmonitor.ExecResult{}, monitorFailure(err)
	}
	return appmonitor.ExecResult{
		Stdout:      result.Stdout,
		Stderr:      result.Stderr,
		ExitCode:    result.ExitCode,
		HasExitCode: result.HasExitCode,
	}, nil
}

func (m *monitorConnection) Close() error { return m.lease.Release() }

// monitorFailure projects the connection layer's typed failures onto the
// monitor runtime's stable reasons. The surface localizes a reason; an
// arbitrary error never becomes display text.
func monitorFailure(err error) error {
	if err == nil {
		return nil
	}
	var typed *appmonitor.Error
	if errors.As(err, &typed) {
		return err
	}
	reason := appmonitor.ReasonNetwork
	switch {
	case errors.Is(err, appconnection.ErrHostKeyRejected):
		reason = appmonitor.ReasonHostKey
	case errors.Is(err, appconnection.ErrAuthentication),
		errors.Is(err, appconnection.ErrNoCredentials),
		errors.Is(err, appconnection.ErrPromptCancelled):
		reason = appmonitor.ReasonAuth
	case errors.Is(err, appconnection.ErrDialTimeout),
		errors.Is(err, appconnection.ErrAuthTimeout):
		reason = appmonitor.ReasonTimeout
	case errors.Is(err, appconnection.ErrUnsupportedAlgorithm),
		errors.Is(err, appconnection.ErrUnsupportedProxy):
		reason = appmonitor.ReasonUnsupported
	}
	var connectionError *appconnection.Error
	if errors.As(err, &connectionError) {
		switch connectionError.Kind {
		case appconnection.KindAuth:
			reason = appmonitor.ReasonAuth
		case appconnection.KindTimeout:
			reason = appmonitor.ReasonTimeout
		case appconnection.KindHostKey:
			reason = appmonitor.ReasonHostKey
		case appconnection.KindUnsupported:
			reason = appmonitor.ReasonUnsupported
		case appconnection.KindCancelled:
			reason = appmonitor.ReasonCancelled
		case appconnection.KindInternal:
			reason = appmonitor.ReasonInternal
		}
	}
	if errors.Is(err, context.Canceled) {
		reason = appmonitor.ReasonCancelled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		reason = appmonitor.ReasonTimeout
	}
	return &appmonitor.Error{Reason: reason, Cause: err}
}

// closeMonitoring stops sampling and releases the surface's subscription.
// The service owns the leases; closing it closes each sampler's transport.
func closeMonitoring(surface *uimonitoring.State, service *appmonitor.Service) {
	if surface != nil {
		surface.Close()
	}
	if service != nil {
		service.Close()
	}
}
