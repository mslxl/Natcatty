package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"strconv"
	"time"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	appsession "github.com/mslxl/natcatty/application/session"
	apptransfer "github.com/mslxl/natcatty/application/transfer"
	appvault "github.com/mslxl/natcatty/application/vault"
	"github.com/mslxl/natcatty/domain/model"
	domainsftp "github.com/mslxl/natcatty/domain/transfer"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// maxRemoteRewriteBytes bounds the read-modify-write a range write performs
// through a transfer filesystem, whose Create truncates. A larger remote file
// is refused instead of being buffered without limit. A future range-write
// port avoids the rewrite; the file service still chunks every call.
const maxRemoteRewriteBytes = 64 << 20

// pluginHostCatalog adapts the Vault use cases to the hosts host service. It
// projects only non-secret fields and preserves Host provenance across an
// update.
type pluginHostCatalog struct {
	vault *appvault.Service
}

var _ appplugin.HostCatalog = (*pluginHostCatalog)(nil)

func newPluginHostCatalog(vault *appvault.Service) *pluginHostCatalog {
	return &pluginHostCatalog{vault: vault}
}

func (c *pluginHostCatalog) List(ctx context.Context) ([]appplugin.HostSummary, error) {
	if c == nil || c.vault == nil {
		return nil, errors.New("host catalog: the Vault is unavailable")
	}
	hosts, err := c.vault.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]appplugin.HostSummary, 0, len(hosts))
	for _, host := range hosts {
		out = append(out, pluginHostSummary(host))
	}
	return out, nil
}

func (c *pluginHostCatalog) Get(ctx context.Context, id string) (appplugin.HostSummary, bool, error) {
	if c == nil || c.vault == nil {
		return appplugin.HostSummary{}, false, errors.New("host catalog: the Vault is unavailable")
	}
	host, err := c.vault.Get(ctx, model.HostID(id))
	if errors.Is(err, appvault.ErrNotFound) {
		return appplugin.HostSummary{}, false, nil
	}
	if err != nil {
		return appplugin.HostSummary{}, false, err
	}
	return pluginHostSummary(host), true, nil
}

func (c *pluginHostCatalog) Create(ctx context.Context, draft appplugin.HostDraft) (appplugin.HostSummary, error) {
	if c == nil || c.vault == nil {
		return appplugin.HostSummary{}, errors.New("host catalog: the Vault is unavailable")
	}
	host, err := c.vault.Create(ctx, domainvault.Draft{
		CreatedBy: draft.CreatedBy,
		Label:     draft.Label,
		Hostname:  draft.Hostname,
		Port:      draft.Port,
		Username:  draft.Username,
		Protocol:  domainvault.Protocol(draft.Protocol),
		Tags:      append([]string(nil), draft.Tags...),
	})
	if err != nil {
		return appplugin.HostSummary{}, err
	}
	return pluginHostSummary(host), nil
}

func (c *pluginHostCatalog) Update(ctx context.Context, id string,
	patch appplugin.HostPatch) (appplugin.HostSummary, error) {
	if c == nil || c.vault == nil {
		return appplugin.HostSummary{}, errors.New("host catalog: the Vault is unavailable")
	}
	host, err := c.vault.Get(ctx, model.HostID(id))
	if errors.Is(err, appvault.ErrNotFound) {
		return appplugin.HostSummary{}, os.ErrNotExist
	}
	if err != nil {
		return appplugin.HostSummary{}, err
	}
	draft := pluginDraftFromHost(host)
	if patch.Label != nil {
		draft.Label = *patch.Label
	}
	if patch.Hostname != nil {
		draft.Hostname = *patch.Hostname
	}
	if patch.Port != nil {
		draft.Port = *patch.Port
	}
	if patch.Protocol != nil {
		draft.Protocol = domainvault.Protocol(*patch.Protocol)
	}
	if patch.Username != nil {
		draft.Username = *patch.Username
	}
	if patch.Tags != nil {
		draft.Tags = append([]string(nil), (*patch.Tags)...)
	}
	updated, err := c.vault.Update(ctx, model.HostID(id), draft)
	if errors.Is(err, appvault.ErrNotFound) {
		return appplugin.HostSummary{}, os.ErrNotExist
	}
	if err != nil {
		return appplugin.HostSummary{}, err
	}
	return pluginHostSummary(updated), nil
}

func (c *pluginHostCatalog) Import(ctx context.Context,
	drafts []appplugin.HostDraft) ([]appplugin.HostSummary, error) {
	out := make([]appplugin.HostSummary, 0, len(drafts))
	for _, draft := range drafts {
		host, err := c.Create(ctx, draft)
		if err != nil {
			return out, err
		}
		out = append(out, host)
	}
	return out, nil
}

// Test probes TCP reachability with the Host's saved endpoint. It never reads
// or returns credentials and reports an unreachable Host as a result, not a
// host-service fault.
func (c *pluginHostCatalog) Test(ctx context.Context, id string) (appplugin.HostTestResult, error) {
	if c == nil || c.vault == nil {
		return appplugin.HostTestResult{}, errors.New("host catalog: the Vault is unavailable")
	}
	host, err := c.vault.Get(ctx, model.HostID(id))
	if errors.Is(err, appvault.ErrNotFound) {
		return appplugin.HostTestResult{}, os.ErrNotExist
	}
	if err != nil {
		return appplugin.HostTestResult{}, err
	}
	port := host.Port
	if port <= 0 || port > 65535 {
		port = defaultProbePort(host.Protocol)
	}
	address := net.JoinHostPort(host.Hostname, strconv.Itoa(port))
	dialer := net.Dialer{Timeout: 2 * time.Second}
	started := time.Now()
	connection, err := dialer.DialContext(ctx, "tcp", address)
	latency := time.Since(started).Milliseconds()
	if err != nil {
		return appplugin.HostTestResult{Reachable: false, LatencyMS: latency, Detail: "unreachable"}, nil
	}
	_ = connection.Close()
	if latency < 1 {
		latency = 1
	}
	return appplugin.HostTestResult{Reachable: true, LatencyMS: latency, Detail: "reachable"}, nil
}

func defaultProbePort(protocol domainvault.Protocol) int {
	switch protocol {
	case domainvault.ProtocolTelnet:
		return 23
	default:
		return 22
	}
}

func pluginHostSummary(host domainvault.Host) appplugin.HostSummary {
	return appplugin.HostSummary{
		ID:        string(host.ID),
		Label:     host.Label,
		Hostname:  host.Hostname,
		Port:      host.Port,
		Protocol:  string(host.Protocol),
		Username:  host.Username,
		Tags:      append([]string(nil), host.Tags...),
		CreatedBy: host.CreatedBy,
	}
}

// pluginDraftFromHost copies every editable Host field so a partial patch
// never clears settings the plugin did not name.
func pluginDraftFromHost(host domainvault.Host) domainvault.Draft {
	draft := domainvault.Draft{
		CreatedBy:              host.CreatedBy,
		Label:                  host.Label,
		Hostname:               host.Hostname,
		Port:                   host.Port,
		Username:               host.Username,
		Protocol:               host.Protocol,
		Group:                  host.Group,
		Tags:                   append([]string(nil), host.Tags...),
		Notes:                  host.Notes,
		Pinned:                 host.Pinned,
		IdentityID:             host.IdentityID,
		KeyID:                  host.KeyID,
		AuthMethod:             host.AuthMethod,
		UseSSHAgent:            host.UseSSHAgent,
		RequiresMFA:            host.RequiresMFA,
		X11Forwarding:          host.X11Forwarding,
		X11WarningAcknowledged: host.X11WarningAcknowledged,
		ProxyProfileID:         host.ProxyProfileID,
		LegacyAlgorithms:       host.LegacyAlgorithms,
		SkipEcdsaHostKey:       host.SkipEcdsaHostKey,
		SFTPFileProtocol:       host.SFTPFileProtocol,
		SFTPSudo:               host.SFTPSudo,
		DeviceType:             host.DeviceType,
		AutoAttachTmux:         host.AutoAttachTmux,
		MoshServerPath:         host.MoshServerPath,
		ETPort:                 host.ETPort,
	}
	if host.HostChain != nil {
		chain := domainvault.HostChain{HostIDs: append([]model.HostID(nil), host.HostChain.HostIDs...)}
		draft.HostChain = &chain
	}
	if host.Proxy != nil {
		proxy := *host.Proxy
		draft.Proxy = &proxy
	}
	if host.Algorithms != nil {
		algorithms := *host.Algorithms
		algorithms.KeyExchanges = append([]string(nil), host.Algorithms.KeyExchanges...)
		algorithms.Ciphers = append([]string(nil), host.Algorithms.Ciphers...)
		algorithms.MACs = append([]string(nil), host.Algorithms.MACs...)
		algorithms.HostKeys = append([]string(nil), host.Algorithms.HostKeys...)
		draft.Algorithms = &algorithms
	}
	return draft
}

// pluginSessionCatalog adapts the session manager to the sessions host
// service. Connect opens through the host-owned shell path so the Session has
// the same transport, logging, and trust policy as a user-opened one.
type pluginSessionCatalog struct {
	sessions *appsession.Service
	openHost func(ctx context.Context, hostID model.HostID) (model.SessionID, error)
}

var _ appplugin.SessionCatalog = (*pluginSessionCatalog)(nil)

func newPluginSessionCatalog(sessions *appsession.Service,
	openHost func(context.Context, model.HostID) (model.SessionID, error)) *pluginSessionCatalog {
	return &pluginSessionCatalog{sessions: sessions, openHost: openHost}
}

func (c *pluginSessionCatalog) List(context.Context) ([]appplugin.SessionSummary, error) {
	if c == nil || c.sessions == nil {
		return nil, errors.New("session catalog: the session manager is unavailable")
	}
	descriptions := c.sessions.DescribeAll()
	out := make([]appplugin.SessionSummary, 0, len(descriptions))
	for _, description := range descriptions {
		out = append(out, pluginSessionSummary(description))
	}
	return out, nil
}

func (c *pluginSessionCatalog) Get(_ context.Context, id string) (appplugin.SessionSummary, bool, error) {
	if c == nil || c.sessions == nil {
		return appplugin.SessionSummary{}, false, errors.New("session catalog: the session manager is unavailable")
	}
	description, ok := c.sessions.Describe(model.SessionID(id))
	if !ok {
		return appplugin.SessionSummary{}, false, nil
	}
	return pluginSessionSummary(description), true, nil
}

func (c *pluginSessionCatalog) Connect(ctx context.Context, hostID string) (appplugin.SessionSummary, error) {
	if c == nil || c.sessions == nil || c.openHost == nil {
		return appplugin.SessionSummary{}, errors.New("session catalog: opening a Host is unavailable")
	}
	id, err := c.openHost(ctx, model.HostID(hostID))
	if err != nil {
		return appplugin.SessionSummary{}, err
	}
	if id == "" {
		return appplugin.SessionSummary{}, errors.New("session catalog: opening the Host produced no Session")
	}
	description, ok := c.sessions.Describe(id)
	if !ok {
		return appplugin.SessionSummary{}, errors.New("session catalog: the opened Session ended immediately")
	}
	return pluginSessionSummary(description), nil
}

func (c *pluginSessionCatalog) Reconnect(_ context.Context, id string) error {
	if c == nil || c.sessions == nil {
		return errors.New("session catalog: the session manager is unavailable")
	}
	if !c.sessions.Reconnect(model.SessionID(id)) {
		return fmt.Errorf("session %s cannot reconnect now", id)
	}
	return nil
}

func (c *pluginSessionCatalog) Disconnect(_ context.Context, id string) error {
	if c == nil || c.sessions == nil {
		return errors.New("session catalog: the session manager is unavailable")
	}
	closed, err := c.sessions.Close(model.SessionID(id))
	if err != nil {
		return err
	}
	if !closed {
		return os.ErrNotExist
	}
	return nil
}

func pluginSessionSummary(description appsession.SessionDescription) appplugin.SessionSummary {
	title := description.Title
	if title == "" {
		title = description.Label
	}
	return appplugin.SessionSummary{
		ID:      string(description.ID),
		Kind:    string(description.Kind),
		Title:   title,
		HostID:  string(description.HostID),
		State:   string(description.Status),
		Cols:    description.Cols,
		Rows:    description.Rows,
		CursorX: 0,
		CursorY: 0,
	}
}

// pluginTerminalCatalog exposes the host's read-only Session observation and
// the separate input path. Observation never consumes or backpressures the
// live terminal view.
type pluginTerminalCatalog struct {
	sessions *appsession.Service
}

var _ appplugin.TerminalCatalog = (*pluginTerminalCatalog)(nil)

func newPluginTerminalCatalog(sessions *appsession.Service) *pluginTerminalCatalog {
	return &pluginTerminalCatalog{sessions: sessions}
}

func (c *pluginTerminalCatalog) Observe(_ context.Context,
	sessionID string) (appplugin.TerminalObservation, error) {
	if c == nil || c.sessions == nil {
		return nil, errors.New("terminal: the session manager is unavailable")
	}
	session, ok := c.sessions.Session(model.SessionID(sessionID))
	if !ok {
		return nil, os.ErrNotExist
	}
	observation, err := session.Observe()
	if err != nil {
		return nil, err
	}
	return &pluginTerminalObservation{observation: observation}, nil
}

func (c *pluginTerminalCatalog) Write(_ context.Context, sessionID string, data []byte) error {
	if c == nil || c.sessions == nil {
		return errors.New("terminal: the session manager is unavailable")
	}
	result := c.sessions.SendInput([]model.SessionID{model.SessionID(sessionID)}, data)
	for _, delivery := range result.Deliveries {
		if delivery.Status == appsession.DeliveryDelivered {
			continue
		}
		if delivery.Err != nil {
			return delivery.Err
		}
		if delivery.Status == appsession.DeliveryMissing {
			return os.ErrNotExist
		}
		return errors.New("terminal: input was not delivered")
	}
	return nil
}

func (c *pluginTerminalCatalog) Info(_ context.Context, sessionID string) (appplugin.TerminalInfo, bool, error) {
	if c == nil || c.sessions == nil {
		return appplugin.TerminalInfo{}, false, errors.New("terminal: the session manager is unavailable")
	}
	description, ok := c.sessions.Describe(model.SessionID(sessionID))
	if !ok {
		return appplugin.TerminalInfo{}, false, nil
	}
	title := description.Title
	if title == "" {
		title = description.Label
	}
	return appplugin.TerminalInfo{
		SessionID: string(description.ID),
		Title:     title,
		Cols:      description.Cols,
		Rows:      description.Rows,
	}, true, nil
}

// pluginTerminalObservation adapts the bounded session observation. Read has
// no context because Close closes the feed and unblocks it; the host-service
// tap owns the bounded lossy buffer above this adapter.
type pluginTerminalObservation struct {
	observation *appsession.Observation
}

var _ appplugin.TerminalObservation = (*pluginTerminalObservation)(nil)

func (o *pluginTerminalObservation) Read(_ context.Context, buffer []byte) (int, error) {
	return o.observation.Read(buffer)
}

func (o *pluginTerminalObservation) Info() appplugin.TerminalInfo {
	screen := o.observation.Screen()
	return appplugin.TerminalInfo{SessionID: string(screen.SessionID), Cols: screen.Cols, Rows: screen.Rows}
}

func (o *pluginTerminalObservation) Close() error {
	return o.observation.Close()
}

// pluginRemoteFiles adapts the transfer application service to the remote file
// port. Every call opens the Host's own filesystem and closes it before
// returning, so a plugin handle never outlives one operation and never reaches
// the local filesystem.
type pluginRemoteFiles struct {
	transfer *apptransfer.Service
}

var _ appplugin.RemoteFiles = (*pluginRemoteFiles)(nil)

func newPluginRemoteFiles(transfer *apptransfer.Service) *pluginRemoteFiles {
	return &pluginRemoteFiles{transfer: transfer}
}

func (r *pluginRemoteFiles) filesystem(ctx context.Context, host string) (apptransfer.FileSystem, error) {
	if r == nil || r.transfer == nil {
		return nil, errors.New("remote files: the transfer service is unavailable")
	}
	files, err := r.transfer.OpenRemote(ctx, model.HostID(host), host)
	if err != nil {
		return nil, mapTransferError(err)
	}
	return files, nil
}

func (r *pluginRemoteFiles) List(ctx context.Context, host, remotePath string) ([]appplugin.RemoteEntry, error) {
	files, err := r.filesystem(ctx, host)
	if err != nil {
		return nil, err
	}
	defer files.Close()
	entries, err := files.List(ctx, remotePath)
	if err != nil {
		return nil, mapTransferError(err)
	}
	out := make([]appplugin.RemoteEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Name == ".." {
			continue
		}
		out = append(out, pluginRemoteEntry(entry))
	}
	return out, nil
}

func (r *pluginRemoteFiles) Stat(ctx context.Context, host, remotePath string) (appplugin.RemoteEntry, bool, error) {
	files, err := r.filesystem(ctx, host)
	if err != nil {
		return appplugin.RemoteEntry{}, false, err
	}
	defer files.Close()
	entry, err := files.Stat(ctx, remotePath)
	if err != nil {
		if apptransfer.KindOf(err) == apptransfer.KindNotFound {
			return appplugin.RemoteEntry{}, false, nil
		}
		return appplugin.RemoteEntry{}, false, mapTransferError(err)
	}
	return pluginRemoteEntry(entry), true, nil
}

func (r *pluginRemoteFiles) Read(ctx context.Context, host, remotePath string,
	offset int64, length int) ([]byte, bool, error) {
	files, err := r.filesystem(ctx, host)
	if err != nil {
		return nil, false, err
	}
	defer files.Close()
	reader, err := files.Open(ctx, remotePath)
	if err != nil {
		return nil, false, mapTransferError(err)
	}
	defer reader.Close()
	return readRemoteRange(ctx, reader, offset, length)
}

func (r *pluginRemoteFiles) Write(ctx context.Context, host, remotePath string,
	offset int64, data []byte) (int, error) {
	files, err := r.filesystem(ctx, host)
	if err != nil {
		return 0, err
	}
	defer files.Close()
	return rewriteRemoteRange(ctx, files, remotePath, offset, data)
}

func (r *pluginRemoteFiles) Rename(ctx context.Context, host, from, to string) error {
	files, err := r.filesystem(ctx, host)
	if err != nil {
		return err
	}
	defer files.Close()
	if err := files.Rename(ctx, from, to); err != nil {
		return mapTransferError(err)
	}
	return nil
}

func (r *pluginRemoteFiles) Delete(ctx context.Context, host, remotePath string, recursive bool) error {
	files, err := r.filesystem(ctx, host)
	if err != nil {
		return err
	}
	defer files.Close()
	if recursive {
		err = files.RemoveAll(ctx, remotePath)
	} else {
		err = files.Remove(ctx, remotePath)
	}
	if err != nil {
		return mapTransferError(err)
	}
	return nil
}

func (r *pluginRemoteFiles) Mkdir(ctx context.Context, host, remotePath string) error {
	files, err := r.filesystem(ctx, host)
	if err != nil {
		return err
	}
	defer files.Close()
	if err := files.Mkdir(ctx, remotePath); err != nil {
		return mapTransferError(err)
	}
	return nil
}

func pluginRemoteEntry(entry domainsftp.Entry) appplugin.RemoteEntry {
	return appplugin.RemoteEntry{
		Name:     entry.Name,
		Path:     entry.Path,
		Dir:      entry.IsDir(),
		Size:     entry.Size,
		Mode:     entry.Mode,
		Modified: entry.ModTime.UTC(),
	}
}

// readRemoteRange reads one bounded range. It prefers ReaderAt, then Seek, and
// falls back to discarding the prefix on a pure stream.
func readRemoteRange(ctx context.Context, reader io.Reader,
	offset int64, length int) ([]byte, bool, error) {
	if offset < 0 || length < 0 {
		return nil, false, fs.ErrInvalid
	}
	buffer := make([]byte, length)
	if readerAt, ok := reader.(io.ReaderAt); ok {
		n, err := readerAt.ReadAt(buffer, offset)
		switch {
		case errors.Is(err, io.EOF):
			return buffer[:n], true, nil
		case err != nil:
			return nil, false, err
		default:
			return buffer[:n], false, nil
		}
	}
	if seeker, ok := reader.(io.Seeker); ok {
		if _, err := seeker.Seek(offset, io.SeekStart); err != nil {
			return nil, false, err
		}
	} else if offset > 0 {
		if _, err := io.CopyN(io.Discard, &contextReader{ctx: ctx, reader: reader}, offset); err != nil {
			if errors.Is(err, io.EOF) {
				return nil, true, nil
			}
			return nil, false, err
		}
	}
	n, err := io.ReadFull(&contextReader{ctx: ctx, reader: reader}, buffer)
	switch {
	case err == nil:
		return buffer[:n], false, nil
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return buffer[:n], true, nil
	default:
		return nil, false, err
	}
}

// rewriteRemoteRange performs a bounded read-modify-write because the transfer
// filesystem's Create truncates. The rewritten file is capped so a plugin
// cannot force an unbounded buffer.
func rewriteRemoteRange(ctx context.Context, files apptransfer.FileSystem,
	remotePath string, offset int64, data []byte) (int, error) {
	if offset < 0 {
		return 0, fs.ErrInvalid
	}
	end := offset + int64(len(data))
	if end > maxRemoteRewriteBytes {
		return 0, fmt.Errorf("%w: remote range write exceeds %d bytes",
			fs.ErrInvalid, maxRemoteRewriteBytes)
	}
	existing, err := readRemoteWhole(ctx, files, remotePath)
	if err != nil {
		return 0, err
	}
	if int64(len(existing)) < end {
		grown := make([]byte, end)
		copy(grown, existing)
		existing = grown
	}
	copy(existing[offset:], data)
	writer, err := files.Create(ctx, remotePath)
	if err != nil {
		return 0, mapTransferError(err)
	}
	if _, err := writer.Write(existing); err != nil {
		_ = writer.Close()
		return 0, mapTransferError(err)
	}
	if err := writer.Close(); err != nil {
		return 0, mapTransferError(err)
	}
	return len(data), nil
}

func readRemoteWhole(ctx context.Context, files apptransfer.FileSystem,
	remotePath string) ([]byte, error) {
	reader, err := files.Open(ctx, remotePath)
	if err != nil {
		if apptransfer.KindOf(err) == apptransfer.KindNotFound {
			return nil, nil
		}
		return nil, mapTransferError(err)
	}
	defer reader.Close()
	limited := io.LimitReader(&contextReader{ctx: ctx, reader: reader}, maxRemoteRewriteBytes+1)
	content, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if len(content) > maxRemoteRewriteBytes {
		return nil, fmt.Errorf("%w: remote file exceeds %d bytes",
			fs.ErrInvalid, maxRemoteRewriteBytes)
	}
	return content, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(buffer []byte) (int, error) {
	if r.ctx != nil {
		if err := r.ctx.Err(); err != nil {
			return 0, err
		}
	}
	return r.reader.Read(buffer)
}

// mapTransferError exposes not-found, permission, and exists outcomes as the
// standard filesystem sentinels the host service classifies; every other
// transfer failure stays a fault.
func mapTransferError(err error) error {
	switch apptransfer.KindOf(err) {
	case apptransfer.KindNotFound:
		return fmt.Errorf("%w: %v", fs.ErrNotExist, err)
	case apptransfer.KindPermission:
		return fmt.Errorf("%w: %v", fs.ErrPermission, err)
	case apptransfer.KindExists:
		return fmt.Errorf("%w: %v", fs.ErrExist, err)
	default:
		return err
	}
}
