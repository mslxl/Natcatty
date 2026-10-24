package app

import (
	"context"
	"errors"
	"fmt"
	"sync"

	adaptersftp "github.com/mslxl/natcatty/adapters/sftp"
	appconnection "github.com/mslxl/natcatty/application/connection"
	apptransfer "github.com/mslxl/natcatty/application/transfer"
	appvault "github.com/mslxl/natcatty/application/vault"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// vaultHostSource is the slice of the Vault the remote opener needs: the Host
// record and, for sudo mode, its stored password.
type vaultHostSource interface {
	Get(context.Context, model.HostID) (domainvault.Host, error)
	Secret(ctx context.Context, id model.HostID, field string) (string, error)
}

// sftpRemoteOpener opens a remote pane according to the Host's file-protocol
// setting. It reuses the same route resolution, host-key trust, and
// authentication as a terminal Session; only the file channel differs. The
// LeaseKindChannel reuse rule lets a parked transport serve a pane without
// requiring an agent-forwarding match.
type sftpRemoteOpener struct {
	manager *appconnection.Manager
	routes  routeSource
	vault   vaultHostSource
}

var _ apptransfer.RemoteConnector = sftpRemoteConnector{}

// sftpRemoteConnector adapts the opener to the transfer context's selection
// port.
type sftpRemoteConnector struct {
	opener *sftpRemoteOpener
}

func (c sftpRemoteConnector) ConnectRemote(ctx context.Context, hostID model.HostID) (apptransfer.RemoteSession, error) {
	return c.opener.connect(ctx, hostID)
}

// OpenRemote authenticates the Host once and lets protocol selection pick
// SFTP, SCP, or sudo SFTP.
func (o *sftpRemoteOpener) OpenRemote(ctx context.Context, hostID model.HostID, _ string) (apptransfer.FileSystem, error) {
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
	if host.Protocol != domainvault.ProtocolSSH {
		return nil, fmt.Errorf("app: %s Hosts do not support file transfer", host.Protocol)
	}
	password := ""
	if host.SFTPSudo {
		stored, err := o.vault.Secret(ctx, hostID, appvault.FieldPassword)
		switch {
		case err == nil:
			password = stored
		case errors.Is(err, appvault.ErrSecretNotFound):
			// Passwordless sudo may still work; the handshake reports the
			// refusal if it does not.
		default:
			return nil, err
		}
	}
	files, _, err := apptransfer.SelectRemote(ctx, sftpRemoteConnector{opener: o},
		apptransfer.RemoteOptions{
			HostID:       hostID,
			Protocol:     apptransfer.NormalizeFileProtocol(string(host.SFTPFileProtocol)),
			Sudo:         host.SFTPSudo,
			SudoPassword: password,
		})
	return files, err
}

// connect authenticates the Host and returns the session protocol selection
// drives. The session owns the lease until a filesystem takes it over.
func (o *sftpRemoteOpener) connect(ctx context.Context, hostID model.HostID) (apptransfer.RemoteSession, error) {
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
	return &sftpRemoteSession{
		lease:   lease,
		backing: "remote:" + string(hostID),
	}, nil
}

// sftpRemoteSession turns one Connection lease into a filesystem. Successful
// opens transfer lease ownership to the filesystem; a failure leaves it with
// the session until the selector decides whether to fall back or discard.
type sftpRemoteSession struct {
	lease   *appconnection.Lease
	backing string

	mu     sync.Mutex
	handed bool
}

func (s *sftpRemoteSession) SingleChannel() bool { return s.lease.SingleChannel() }
func (s *sftpRemoteSession) Reused() bool        { return s.lease.Reused() }

// OpenSFTP opens the SFTP subsystem. A refused subsystem leaves the transport
// healthy so auto mode can still probe SCP on it.
func (s *sftpRemoteSession) OpenSFTP(ctx context.Context) (apptransfer.FileSystem, error) {
	stream, err := s.lease.Subsystem(ctx, "sftp")
	if err != nil {
		return nil, err
	}
	options := []adaptersftp.RemoteOption{adaptersftp.WithBacking(s.backing)}
	// Compressed folder upload runs tar on a second channel of the same
	// Connection. A single-channel Host cannot open one, so the capability is
	// left off and the engine copies file by file without a failed attempt.
	if !s.lease.SingleChannel() {
		if executor, err := s.lease.Executor(); err == nil {
			options = append(options, adaptersftp.WithExecutor(executor))
		}
	}
	remote, err := adaptersftp.NewRemote(stream, s.release, options...)
	if err != nil {
		// NewRemote already closed the stream and called release with the
		// failure, so there is nothing more to clean up here.
		return nil, err
	}
	return remote, nil
}

// OpenSCP probes for a working shell and scp binary, then wraps the same
// lease as an SCP filesystem. The caller bound ctx with the probe timeout.
func (s *sftpRemoteSession) OpenSCP(ctx context.Context) (apptransfer.FileSystem, error) {
	if err := adaptersftp.ProbeSCP(ctx, s.lease); err != nil {
		return nil, err
	}
	files, err := adaptersftp.NewSCP(s.lease, s.release,
		adaptersftp.WithSCPBacking(s.backing))
	if err != nil {
		_ = s.release(err)
		return nil, err
	}
	return files, nil
}

// OpenSudoSFTP runs the password-on-stdin handshake and hands the resulting
// subsystem stream to the SFTP client.
func (s *sftpRemoteSession) OpenSudoSFTP(ctx context.Context, password string) (apptransfer.FileSystem, error) {
	stream, err := adaptersftp.OpenSudoSFTP(ctx, s.lease, password)
	if err != nil {
		return nil, err
	}
	remote, err := adaptersftp.NewRemote(stream, s.release,
		adaptersftp.WithBacking(s.backing))
	if err != nil {
		return nil, err
	}
	return remote, nil
}

// Discard closes a transport protocol selection gave up on. A healthy
// transport returns to the pool; one whose peer already vanished is dropped.
func (s *sftpRemoteSession) Discard() {
	if !s.claim() {
		return
	}
	select {
	case <-s.lease.Done():
		_ = s.lease.Discard()
	default:
		_ = s.lease.Release()
	}
}

// release hands the lease back when a filesystem closes, or discards a
// transport whose protocol failed after ownership moved.
func (s *sftpRemoteSession) release(cause error) error {
	if !s.claim() {
		return nil
	}
	if cause != nil {
		return s.lease.Discard()
	}
	return s.lease.Release()
}

// claim reports whether this session still owns the lease.
func (s *sftpRemoteSession) claim() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.handed {
		return false
	}
	s.handed = true
	return true
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
