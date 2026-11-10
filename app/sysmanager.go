package app

import (
	"context"
	"errors"

	appconnection "github.com/mslxl/natcatty/application/connection"
	appplugin "github.com/mslxl/natcatty/application/plugin"
	appsysmanager "github.com/mslxl/natcatty/application/sysmanager"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	uisysmanager "github.com/mslxl/natcatty/ui/sysmanager"
)

// sysmanagerConnector adapts the shared Connection Manager to the System
// manager. Each sample or action opens a channel lease through Acquire, which
// runs the same host-key verification, trust prompt, jump chain, proxy, and
// algorithm policy a Session uses; no ps, ss, systemctl, or kill command can
// run before the Host key is trusted.
type sysmanagerConnector struct {
	routes      routeSource
	connections *appconnection.Manager
}

var _ appsysmanager.Connector = sysmanagerConnector{}

func (c sysmanagerConnector) Open(ctx context.Context, host domainvault.Host) (appsysmanager.Connection, error) {
	if c.connections == nil {
		return nil, &appsysmanager.Error{Reason: appsysmanager.ReasonInternal,
			Cause: errors.New("no connection manager")}
	}
	var route appconnection.Route
	if c.routes != nil {
		var err error
		route, err = c.routes.Route(ctx, host.ID)
		if err != nil {
			return nil, sysmanagerFailure(err)
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
		// A channel lease is enough for exec and never claims a Session
		// shell.
		LeaseKind: appconnection.LeaseChannel,
		Keepalive: &appconnection.DefaultKeepalive,
	})
	if err != nil {
		return nil, sysmanagerFailure(err)
	}
	return &sysmanagerConnection{lease: lease}, nil
}

// sysmanagerConnection is one leased Host transport used for fixed exec
// commands. Actions open their own short-lived lease so a confirmed command
// does not race the sampler's command on the same channel.
type sysmanagerConnection struct {
	lease *appconnection.Lease
}

func (c *sysmanagerConnection) Exec(ctx context.Context, command string) (appsysmanager.ExecResult, error) {
	result, err := c.lease.Exec(ctx, command)
	if err != nil {
		return appsysmanager.ExecResult{}, sysmanagerFailure(err)
	}
	return appsysmanager.ExecResult{
		Stdout:      result.Stdout,
		Stderr:      result.Stderr,
		ExitCode:    result.ExitCode,
		HasExitCode: result.HasExitCode,
	}, nil
}

func (c *sysmanagerConnection) Close() error { return c.lease.Release() }

// sysmanagerFailure projects the connection layer's typed failures onto the
// System manager's stable reasons. The view localizes a reason; a transport
// error never becomes display text.
func sysmanagerFailure(err error) error {
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

// systemPanelSnapshotSource serves the System panel's Host snapshot service.
// It resolves the Host through the Vault and runs only the fixed built-in
// collector templates; the plugin cannot name a command. It is a dedicated
// service, so its lease never races the System manager's own sampler.
type systemPanelSnapshotSource struct {
	service *appsysmanager.Service
	host    func(context.Context, string) (domainvault.Host, error)
}

var _ appplugin.PanelSnapshotSource = systemPanelSnapshotSource{}

func (s systemPanelSnapshotSource) CollectPanelSnapshot(ctx context.Context, hostID string,
	collectors []domainplugin.PanelCollector) (domainplugin.PanelSnapshot, error) {
	if s.service == nil {
		return domainplugin.PanelSnapshot{}, errors.New("sysmanager: no snapshot service")
	}
	host, err := s.host(ctx, hostID)
	if err != nil {
		return domainplugin.PanelSnapshot{}, err
	}
	tabs := make([]appsysmanager.Tab, 0, len(collectors))
	for _, collector := range collectors {
		tabs = append(tabs, appsysmanager.Tab(collector))
	}
	result, err := s.service.CollectSnapshot(ctx, host, tabs)
	if err != nil {
		return domainplugin.PanelSnapshot{}, err
	}
	return domainplugin.PanelSnapshot{
		HostID:     hostID,
		SampledAt:  result.SampledAt,
		Collectors: result.Collectors,
		Skipped:    result.Skipped,
	}, nil
}

// closeSystemManager stops sampling, cancels actions, and releases the
// surface's subscription. The service owns every lease; closing it closes each
// sampler's transport.
func closeSystemManager(surface *uisysmanager.State, service *appsysmanager.Service) {
	if surface != nil {
		surface.Close()
	}
	if service != nil {
		service.Close()
	}
}
