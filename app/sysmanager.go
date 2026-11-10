package app

import (
	"context"
	"errors"
	"os"
	"runtime"

	appconnection "github.com/mslxl/natcatty/application/connection"
	appsysmanager "github.com/mslxl/natcatty/application/sysmanager"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/platform/process"
	uisysmanager "github.com/mslxl/natcatty/ui/sysmanager"
)

// sysmanagerConnector adapts the shared Connection Manager to the System
// manager. Each sample or action opens a channel lease through Acquire, which
// runs the same host-key verification, trust prompt, jump chain, proxy, and
// algorithm policy a Session uses; no ps, ss, systemctl, or kill command can
// run before the Host key is trusted. A local-shell Host, such as this
// computer, runs the same commands in this computer's shell instead.
type sysmanagerConnector struct {
	routes      routeSource
	connections *appconnection.Manager
}

var _ appsysmanager.Connector = sysmanagerConnector{}

func (c sysmanagerConnector) Open(ctx context.Context, host domainvault.Host) (appsysmanager.Connection, error) {
	if host.Protocol == domainvault.ProtocolLocal {
		if runtime.GOOS == "windows" {
			// The commands are POSIX shell; cmd.exe cannot run them.
			return nil, &appsysmanager.Error{Reason: appsysmanager.ReasonUnsupported,
				Cause: errors.New("no POSIX shell on Windows")}
		}
		return localSystemConnection{}, nil
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

// localSystemConnection runs the System manager's commands in this computer's
// shell, as an SSH exec runs them on a Host.
type localSystemConnection struct{}

func (localSystemConnection) Exec(ctx context.Context, command string) (appsysmanager.ExecResult, error) {
	result, err := process.Run(ctx, command)
	if err != nil {
		return appsysmanager.ExecResult{}, sysmanagerFailure(err)
	}
	return appsysmanager.ExecResult{
		Stdout:      string(result.Stdout),
		Stderr:      string(result.Stderr),
		ExitCode:    result.ExitCode,
		HasExitCode: true,
	}, nil
}

func (localSystemConnection) Close() error { return nil }

// thisComputerID names this computer among the System tool's Hosts. No Vault
// Host has it: the Vault's IDs are generated.
const thisComputerID = "local:this-computer"

// thisComputer is the Host a local Session's System tool samples: this
// computer's shell, labelled with its name.
func thisComputer() domainvault.Host {
	name, err := os.Hostname()
	if err != nil || name == "" {
		name = "localhost"
	}
	return domainvault.Host{ID: thisComputerID, Label: name, Protocol: domainvault.ProtocolLocal}
}

// withThisComputer lists this computer ahead of the Vault's Hosts, so the
// side panel's System tool can show it for a local Session.
type withThisComputer struct{ uisysmanager.Hosts }

func (h withThisComputer) List(ctx context.Context) ([]domainvault.Host, error) {
	hosts, err := h.Hosts.List(ctx)
	return append([]domainvault.Host{thisComputer()}, hosts...), err
}

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
