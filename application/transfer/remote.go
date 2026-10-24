package transfer

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mslxl/natcatty/domain/model"
)

// FileProtocol is a Host's file-transfer protocol preference.
type FileProtocol string

const (
	// ProtocolAuto tries SFTP and may fall back to SCP when the server has no
	// SFTP subsystem, unless the host uses a single SSH channel.
	ProtocolAuto FileProtocol = "auto"
	// ProtocolSFTP opens SFTP only. A failure is reported, never hidden by a
	// quieter SCP fallback.
	ProtocolSFTP FileProtocol = "sftp"
	// ProtocolSCP browses and transfers over shell exec and scp -t/-f. It is
	// the choice for servers without an SFTP subsystem.
	ProtocolSCP FileProtocol = "scp"
)

// Valid reports whether p is one of the three selectable protocols.
func (p FileProtocol) Valid() bool {
	switch p {
	case ProtocolAuto, ProtocolSFTP, ProtocolSCP:
		return true
	}
	return false
}

// NormalizeFileProtocol maps an empty or unknown value to auto, matching the
// original's normalization of persisted settings.
func NormalizeFileProtocol(value string) FileProtocol {
	switch FileProtocol(value) {
	case ProtocolSFTP, ProtocolSCP:
		return FileProtocol(value)
	default:
		return ProtocolAuto
	}
}

// SCPProbeTimeout bounds the shell and scp check so a hung remote exec cannot
// leave a pane connecting forever. The original used the same 15 seconds.
const SCPProbeTimeout = 15 * time.Second

// Protocol-selection failures. Each is typed so the UI can explain the exact
// refusal instead of showing a generic connection error.
var (
	// ErrSudoWithSCP reports a contradictory Host: SCP cannot elevate, and
	// silently dropping sudo would hide the loss of access.
	ErrSudoWithSCP = errors.New("transfer: sudo mode requires SFTP; SCP cannot elevate")
	// ErrSingleChannel reports a transport that is already serving the one
	// channel a bastion allows. Opening another would disconnect the terminal.
	ErrSingleChannel = errors.New("transfer: this host uses a single SSH channel; opening SFTP would disconnect the terminal")
	// ErrSingleChannelFallback reports an SFTP failure on a single-channel host
	// where the SCP fallback is therefore disabled.
	ErrSingleChannelFallback = errors.New("transfer: SFTP is unavailable and SCP fallback is disabled on a single-channel host")
)

// RemoteSession is one authenticated transport that protocol selection may
// turn into a filesystem. The app adapts a Connection lease to it; tests use
// a fake. A session owns the transport: the selector calls Discard exactly
// once when it gives up without returning a filesystem.
type RemoteSession interface {
	// SingleChannel reports a bastion or vendor banner that permits one SSH
	// channel only.
	SingleChannel() bool
	// Reused reports a transport that already served a terminal or another
	// lease before this open.
	Reused() bool
	// OpenSFTP opens the SFTP subsystem.
	OpenSFTP(ctx context.Context) (FileSystem, error)
	// OpenSCP probes the shell and scp binary, then returns an SCP filesystem.
	// The selector bounds ctx with SCPProbeTimeout.
	OpenSCP(ctx context.Context) (FileSystem, error)
	// OpenSudoSFTP runs the password-on-stdin sudo handshake and returns the
	// elevated SFTP filesystem. It never falls back to the login account.
	OpenSudoSFTP(ctx context.Context, password string) (FileSystem, error)
	// Discard closes the transport after a failure.
	Discard()
}

// RemoteConnector authenticates a Host and hands the transport to protocol
// selection. The app binds it to the connection manager and Vault.
type RemoteConnector interface {
	ConnectRemote(ctx context.Context, hostID model.HostID) (RemoteSession, error)
}

// RemoteOptions is one remote-pane open request.
type RemoteOptions struct {
	HostID       model.HostID
	Protocol     FileProtocol
	Sudo         bool
	SudoPassword string
}

// SelectRemote opens a remote filesystem according to the Host's protocol
// setting. It returns the protocol actually used so the UI can show SCP mode.
//
// Rules, preserved from the original:
//   - explicit SCP activates SCP and its probe, with no SFTP attempt;
//   - explicit SFTP never falls back;
//   - auto may fall back, but not on a single-channel host;
//   - a reused single-channel transport is refused before any channel opens;
//   - sudo is never silently downgraded to unprivileged SFTP or SCP.
func SelectRemote(ctx context.Context, connector RemoteConnector, options RemoteOptions) (FileSystem, FileProtocol, error) {
	protocol := options.Protocol
	if !protocol.Valid() {
		protocol = ProtocolAuto
	}
	if protocol == ProtocolSCP && options.Sudo {
		return nil, "", ErrSudoWithSCP
	}
	if connector == nil {
		return nil, "", errors.New("transfer: no remote connector")
	}
	session, err := connector.ConnectRemote(ctx, options.HostID)
	if err != nil {
		return nil, "", err
	}
	if session.Reused() && session.SingleChannel() {
		session.Discard()
		return nil, "", ErrSingleChannel
	}

	if options.Sudo {
		files, err := session.OpenSudoSFTP(ctx, options.SudoPassword)
		if err != nil {
			session.Discard()
			return nil, "", err
		}
		return files, ProtocolSFTP, nil
	}

	switch protocol {
	case ProtocolSCP:
		files, err := openWithProbe(ctx, session.OpenSCP)
		if err != nil {
			session.Discard()
			return nil, "", err
		}
		return files, ProtocolSCP, nil
	case ProtocolSFTP:
		files, err := session.OpenSFTP(ctx)
		if err != nil {
			session.Discard()
			return nil, "", err
		}
		return files, ProtocolSFTP, nil
	default: // auto
		files, sftpErr := session.OpenSFTP(ctx)
		if sftpErr == nil {
			return files, ProtocolSFTP, nil
		}
		if session.SingleChannel() {
			session.Discard()
			return nil, "", fmt.Errorf("%w: %v", ErrSingleChannelFallback, sftpErr)
		}
		files, scpErr := openWithProbe(ctx, session.OpenSCP)
		if scpErr != nil {
			session.Discard()
			return nil, "", scpErr
		}
		return files, ProtocolSCP, nil
	}
}

// openWithProbe bounds an SCP probe without changing an earlier caller
// deadline.
func openWithProbe(ctx context.Context, open func(context.Context) (FileSystem, error)) (FileSystem, error) {
	probeCtx, cancel := context.WithTimeout(ctx, SCPProbeTimeout)
	defer cancel()
	return open(probeCtx)
}
