package app

import (
	"context"
	"errors"

	appconnection "github.com/mslxl/natcatty/application/connection"
	appsession "github.com/mslxl/natcatty/application/session"
	appvault "github.com/mslxl/natcatty/application/vault"
	"github.com/mslxl/natcatty/domain/model"
)

// transportFactory routes a Session to the local PTY adapter or to an SSH
// Connection. The SSH path acquires a lease for the Session's lifetime and
// releases it when the transport closes, which is the seam ticket 08's jump
// chains and ticket 09's pool extend.
type transportFactory struct {
	local   appsession.Factory
	manager *appconnection.Manager
}

func newTransportFactory(local appsession.Factory, manager *appconnection.Manager) appsession.Factory {
	return &transportFactory{local: local, manager: manager}
}

func (f *transportFactory) Open(ctx context.Context, spec appsession.StartSpec) (appsession.Transport, error) {
	if spec.Kind != appsession.KindSSH {
		return f.local.Open(ctx, spec)
	}
	if f.manager == nil {
		return nil, errors.New("app: no SSH connection manager")
	}

	lease, err := f.manager.Acquire(ctx, appconnection.Request{
		HostID:   spec.HostID,
		Hostname: spec.Hostname,
		Port:     spec.Port,
		Username: spec.Username,
	})
	if err != nil {
		return nil, err
	}

	terminal, err := lease.OpenTerminal(ctx, appconnection.TerminalSpec{
		Cols: spec.Cols,
		Rows: spec.Rows,
		Term: "xterm-256color",
	})
	if err != nil {
		_ = lease.Release()
		return nil, err
	}
	return &leasedTransport{terminal: terminal, lease: lease}, nil
}

// leasedTransport adapts an SSH terminal to the Session transport port and
// ties the Connection lease to the transport's lifetime.
type leasedTransport struct {
	terminal appconnection.Terminal
	lease    *appconnection.Lease
}

func (t *leasedTransport) Read(p []byte) (int, error)  { return t.terminal.Read(p) }
func (t *leasedTransport) Write(p []byte) (int, error) { return t.terminal.Write(p) }

func (t *leasedTransport) Resize(cols, rows int) error { return t.terminal.Resize(cols, rows) }

func (t *leasedTransport) Close() error {
	terminalErr := t.terminal.Close()
	leaseErr := t.lease.Release()
	if terminalErr != nil {
		return terminalErr
	}
	return leaseErr
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
