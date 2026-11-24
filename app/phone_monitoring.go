// Phone monitoring wiring.
//
// The phone reuses application/monitor — the same immutable snapshots, demand
// reconciliation, metric parsing, and threshold helpers the desktop surface
// reads — through the phone's own Connection Manager and Vault route resolver.
// Each sampled Host holds one channel lease; the Acquire path runs the same
// host-key verification, trust prompt, jump chain, proxy, and algorithm policy
// a Session uses.
//
// The service is created lazily on the Session runtime the shell already
// receives, so the phone composition root keeps its single-opener wiring. One
// service exists per runtime for the app's lifetime, and the shell closes it.

package app

import (
	"context"
	"errors"
	"fmt"
	"sync"

	appconnection "github.com/mslxl/natcatty/application/connection"
	appmonitor "github.com/mslxl/natcatty/application/monitor"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/phoneshell"
)

// phoneMonitorConnector adapts the shared Connection Manager to the monitor
// runtime's Connector port. It mirrors the desktop connector; the desktop file
// is excluded from phone builds, so the phone keeps its own copy.
type phoneMonitorConnector struct {
	routes      routeSource
	connections *appconnection.Manager
}

var _ appmonitor.Connector = phoneMonitorConnector{}

func (c phoneMonitorConnector) Open(ctx context.Context, host domainvault.Host) (appmonitor.Connection, error) {
	if !host.SupportsMonitoring() {
		return nil, &appmonitor.Error{
			Reason: appmonitor.ReasonUnsupported,
			Cause:  fmt.Errorf("app: %s Hosts have no monitoring channel", host.Protocol),
		}
	}
	if c.connections == nil {
		return nil, &appmonitor.Error{Reason: appmonitor.ReasonInternal,
			Cause: errors.New("no connection manager")}
	}
	var route appconnection.Route
	if c.routes != nil {
		var err error
		route, err = c.routes.Route(ctx, host.ID)
		if err != nil {
			return nil, phoneMonitorFailure(err)
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
		// Monitoring shares a channel-capable transport but never a Session
		// shell.
		LeaseKind: appconnection.LeaseChannel,
		Keepalive: &appconnection.DefaultKeepalive,
	})
	if err != nil {
		return nil, phoneMonitorFailure(err)
	}
	return &phoneMonitorConnection{lease: lease}, nil
}

// phoneMonitorConnection is one leased Host transport used only for metric
// commands.
type phoneMonitorConnection struct {
	lease *appconnection.Lease
}

func (m *phoneMonitorConnection) Exec(ctx context.Context, command string) (appmonitor.ExecResult, error) {
	result, err := m.lease.Exec(ctx, command)
	if err != nil {
		return appmonitor.ExecResult{}, phoneMonitorFailure(err)
	}
	return appmonitor.ExecResult{
		Stdout:      result.Stdout,
		Stderr:      result.Stderr,
		ExitCode:    result.ExitCode,
		HasExitCode: result.HasExitCode,
	}, nil
}

func (m *phoneMonitorConnection) Close() error { return m.lease.Release() }

// phoneMonitorFailure projects the connection layer's typed failures onto the
// monitor runtime's stable reasons.
func phoneMonitorFailure(err error) error {
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

// phoneMonitoringServices keeps the one monitor runtime each Session runtime
// minted. The Session runtime's field set is shared with the rest of the phone
// root, so the lazy runtime is held here rather than added to it.
var phoneMonitoringServices = struct {
	mu   sync.Mutex
	byID map[*phoneSessionRuntime]*phoneMonitoringRuntime
}{byID: make(map[*phoneSessionRuntime]*phoneMonitoringRuntime)}

// phoneMonitoringRuntime is the service plus the bookkeeping that forgets it
// when the Shell closes it, so a test that builds many runtimes does not keep
// them reachable.
type phoneMonitoringRuntime struct {
	*appmonitor.Service
	runtime *phoneSessionRuntime
}

func (p *phoneMonitoringRuntime) Close() {
	p.Service.Close()
	phoneMonitoringServices.mu.Lock()
	if phoneMonitoringServices.byID[p.runtime] == p {
		delete(phoneMonitoringServices.byID, p.runtime)
	}
	phoneMonitoringServices.mu.Unlock()
}

// MonitoringService is the Shell's seam onto the shared monitoring runtime.
// It is called once while the shell is built.
func (r *phoneSessionRuntime) MonitoringService() phoneshell.MonitoringService {
	if r == nil || r.connections == nil {
		return nil
	}
	phoneMonitoringServices.mu.Lock()
	defer phoneMonitoringServices.mu.Unlock()
	if service := phoneMonitoringServices.byID[r]; service != nil {
		return service
	}
	service := &phoneMonitoringRuntime{
		Service: appmonitor.NewService(phoneMonitorConnector{
			// The Session factory and this connector share the exact same route
			// resolver, so Monitoring sees the app-wide default Proxy Profile
			// (and every other route option) a Session sees.
			routes:      r.routes,
			connections: r.connections,
		}),
		runtime: r,
	}
	phoneMonitoringServices.byID[r] = service
	return service
}

var _ phoneshell.MonitoringProvider = (*phoneSessionRuntime)(nil)
