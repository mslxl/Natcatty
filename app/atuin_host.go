package app

import (
	"context"
	"errors"
	"fmt"
	"io"

	apphistory "github.com/mslxl/natcatty/application/history"
	apphostatuin "github.com/mslxl/natcatty/application/hostatuin"
	appsession "github.com/mslxl/natcatty/application/session"
	apptransfer "github.com/mslxl/natcatty/application/transfer"
	domainhistory "github.com/mslxl/natcatty/domain/history"
	"github.com/mslxl/natcatty/domain/model"
	domainsftp "github.com/mslxl/natcatty/domain/transfer"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// atuinRemoteSource opens a Host's Atuin files over a read-only SFTP channel.
// It deliberately uses SFTP only: it never falls back to SCP, so the detector
// runs no remote command, and RequireExtraChannel refuses a single-channel
// Host before opening anything.
type atuinRemoteSource struct {
	opener *sftpRemoteOpener
}

var _ apphostatuin.RemoteSource = atuinRemoteSource{}

// atuinProbeProtocol reports whether a saved Host's protocol can be probed over
// its SSH bootstrap. SSH, Mosh, and ET Hosts all bootstrap over SSH, so the
// separate read-only SFTP channel reaches the same files. Telnet has no SSH
// bootstrap, so it cannot be probed and keeps the journaled capture path.
func atuinProbeProtocol(protocol domainvault.Protocol) bool {
	switch protocol {
	case domainvault.ProtocolSSH, domainvault.ProtocolMosh, domainvault.ProtocolET:
		return true
	}
	return false
}

func (s atuinRemoteSource) OpenReadOnly(ctx context.Context, hostID model.HostID) (apphostatuin.RemoteFiles, error) {
	if s.opener == nil || s.opener.vault == nil {
		return nil, errors.New("app: no Vault for the Atuin detection export")
	}
	host, err := s.opener.vault.Get(ctx, hostID)
	if err != nil {
		return nil, err
	}
	if !atuinProbeProtocol(host.Protocol) {
		return nil, fmt.Errorf("app: %s Hosts have no SFTP detection channel", host.Protocol)
	}
	files, _, err := apptransfer.SelectRemote(ctx,
		sftpRemoteConnector{opener: s.opener},
		apptransfer.RemoteOptions{
			HostID:              hostID,
			Protocol:            apptransfer.ProtocolSFTP,
			RequireExtraChannel: true,
		})
	if err != nil {
		return nil, err
	}
	return atuinRemoteFiles{files: files}, nil
}

// atuinRemoteFiles narrows the transfer filesystem to the read-only port the
// importer uses. Stat and Open are the only remote operations it exposes.
type atuinRemoteFiles struct {
	files apptransfer.FileSystem
}

func (f atuinRemoteFiles) Root(ctx context.Context) (string, error) {
	return f.files.Root(ctx)
}

func (f atuinRemoteFiles) Stat(ctx context.Context, path string) (apphostatuin.FileInfo, error) {
	entry, err := f.files.Stat(ctx, path)
	if err != nil {
		return apphostatuin.FileInfo{}, err
	}
	return apphostatuin.FileInfo{
		Size:    entry.Size,
		ModTime: entry.ModTime,
		Regular: entry.Kind == domainsftp.KindFile,
		Symlink: entry.Symlink,
	}, nil
}

func (f atuinRemoteFiles) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	return f.files.Open(ctx, path)
}

func (f atuinRemoteFiles) Close() error {
	return f.files.Close()
}

// atuinHostPolicy adapts the Host-Atuin service to the Session's pre-open
// port. A saved SSH, Mosh, or ET Host has a separate SFTP channel over its SSH
// bootstrap and an identity the import checkpoint can be keyed on. Telnet and
// unsaved Hosts cannot be probed: they keep the journaled capture path, and an
// SSH Session to the same machine can later import the same command. That
// accepted residual duplicate window is shared with the read-only-channel
// failure described in the hostatuin package doc.
type atuinHostPolicy struct {
	service *apphostatuin.Service
}

var _ appsession.HostAtuin = atuinHostPolicy{}

func (p atuinHostPolicy) Probe(ctx context.Context, spec appsession.StartSpec) appsession.HostAtuinProbe {
	ref, ok := p.hostRef(spec)
	if !ok {
		return appsession.HostAtuinProbe{}
	}
	result, probeErr := p.service.Probe(ctx, ref)
	return atuinProbeReport(result, probeErr)
}

// atuinProbeReport maps one Host-Atuin service result to the Session port. A
// failed import still reports Present: the Host runs Atuin, so the capture hook
// stays skipped and a later Session retries the copy. Failed only makes sense
// together with Present and is surfaced to the Session so the failure is not
// silent.
func atuinProbeReport(result apphostatuin.ProbeResult, probeErr error) appsession.HostAtuinProbe {
	return appsession.HostAtuinProbe{
		Present: result.Present,
		Failed:  result.Present && probeErr != nil,
	}
}

func (p atuinHostPolicy) MarkNoticed(ctx context.Context, spec appsession.StartSpec) bool {
	ref, ok := p.hostRef(spec)
	if !ok {
		return false
	}
	return p.service.MarkNoticed(ctx, ref)
}

func (p atuinHostPolicy) ImportAfterClose(spec appsession.StartSpec) {
	ref, ok := p.hostRef(spec)
	if !ok {
		return
	}
	p.service.ImportAfterClose(ref)
}

// hostRef reports whether spec names a saved Host with an identity the
// read-only channel can open and the import checkpoint can key on. SSH, Mosh,
// and ET Sessions all bootstrap over SSH and can be probed; Telnet, Local, and
// unsaved Sessions cannot, so they keep the journaled capture path. An SSH
// Session to the same machine can later import a command those fallback
// Sessions journaled; that is the accepted residual duplicate window.
func (p atuinHostPolicy) hostRef(spec appsession.StartSpec) (apphostatuin.HostRef, bool) {
	if p.service == nil || spec.HostID == "" {
		return apphostatuin.HostRef{}, false
	}
	switch spec.Kind {
	case appsession.KindSSH, appsession.KindMosh, appsession.KindET:
	default:
		return apphostatuin.HostRef{}, false
	}
	return atuinHostRef(spec), true
}

// atuinHistoryRecorder adapts the history service's import port to the
// Host-Atuin service's recorder port. The adapter lives in the composition
// root because an application package may not import another context. reload
// refreshes the autocomplete view after an import writes rows, so a newly
// imported command is suggestible without restarting the app.
type atuinHistoryRecorder struct {
	history *apphistory.Service
	reload  func()
}

var _ apphostatuin.Recorder = atuinHistoryRecorder{}

func (r atuinHistoryRecorder) ImportHostAtuin(ctx context.Context, hostID string,
	rows []domainhistory.AtuinRow, hostOrigin string) (int, error) {
	if r.history == nil {
		return 0, errors.New("app: no history service for the Host Atuin import")
	}
	result, err := r.history.ImportHostAtuin(ctx, hostID, rows, hostOrigin)
	if err != nil {
		return 0, err
	}
	if result.Imported > 0 && r.reload != nil {
		r.reload()
	}
	return result.Imported, nil
}

func (r atuinHistoryRecorder) MarkHostAtuinNoticed(ctx context.Context, hostID string) (bool, error) {
	if r.history == nil {
		return false, errors.New("app: no history service for the Host Atuin notice")
	}
	return r.history.MarkHostAtuinNoticed(ctx, hostID)
}

func atuinHostRef(spec appsession.StartSpec) apphostatuin.HostRef {
	return apphostatuin.HostRef{
		ID:       spec.HostID,
		Hostname: spec.Hostname,
		Username: spec.Username,
		Label:    spec.Label,
	}
}
