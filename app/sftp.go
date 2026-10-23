package app

import (
	"context"
	"errors"

	adaptersftp "github.com/mslxl/natcatty/adapters/sftp"
	appconnection "github.com/mslxl/natcatty/application/connection"
	apptransfer "github.com/mslxl/natcatty/application/transfer"
	appvault "github.com/mslxl/natcatty/application/vault"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// sftpRemoteOpener opens an SFTP subsystem on an authenticated SSH
// Connection. It reuses the same route resolution, host-key trust, and
// authentication as a terminal Session; only the channel request differs. The
// LeaseKindChannel reuse rule lets a parked transport serve an SFTP pane
// without requiring an agent-forwarding match.
type sftpRemoteOpener struct {
	manager *appconnection.Manager
	routes  routeSource
	vault   interface {
		Get(context.Context, model.HostID) (domainvault.Host, error)
	}
}

func (o sftpRemoteOpener) OpenRemote(ctx context.Context, hostID model.HostID, _ string) (apptransfer.FileSystem, error) {
	if o.manager == nil {
		return nil, errors.New("app: no SSH connection manager")
	}
	if o.vault == nil {
		return nil, errors.New("app: no Vault for the SFTP Host")
	}
	host, err := o.vault.Get(ctx, hostID)
	if err != nil {
		return nil, err
	}
	var route appconnection.Route
	if o.routes != nil {
		route, err = o.routes.Route(ctx, hostID)
		if err != nil {
			return nil, err
		}
	}
	lease, err := o.manager.Acquire(ctx, appconnection.Request{
		HostID:            host.ID,
		Hostname:          host.Hostname,
		Port:              host.Port,
		Username:          host.Username,
		Jumps:             route.Jumps,
		Proxy:             route.Proxy,
		Algorithms:        route.Algorithms,
		ConfigFingerprint: route.ConfigFingerprint,
		LeaseKind:         appconnection.LeaseChannel,
		Keepalive:         &appconnection.DefaultKeepalive,
	})
	if err != nil {
		return nil, err
	}
	stream, err := lease.Subsystem(ctx, "sftp")
	if err != nil {
		// A server without the subsystem is still a healthy transport that
		// other leases may use. Any other failure leaves the channel unusable,
		// so the connection is discarded instead of parked.
		if errors.Is(err, appconnection.ErrSubsystemUnavailable) {
			_ = lease.Release()
		} else {
			_ = lease.Discard()
		}
		return nil, err
	}
	remote, err := adaptersftp.NewRemote(stream, func(cause error) error {
		if cause != nil {
			return lease.Discard()
		}
		return lease.Release()
	})
	if err != nil {
		// NewRemote already closed the stream and released the lease with the
		// failure, so cleaning up again would release it twice.
		return nil, err
	}
	return remote, nil
}

// sftpHostSource lists the saved Hosts for the remote pane's picker.
type sftpHostSource struct {
	vault *appvault.Service
}

func (s sftpHostSource) Hosts(ctx context.Context) ([]apptransfer.HostTarget, error) {
	if s.vault == nil {
		return nil, nil
	}
	hosts, err := s.vault.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]apptransfer.HostTarget, 0, len(hosts))
	for _, host := range hosts {
		out = append(out, apptransfer.HostTarget{
			ID:       host.ID,
			Label:    host.Label,
			Hostname: host.Hostname,
			Username: host.Username,
			Port:     host.Port,
		})
	}
	return out, nil
}
