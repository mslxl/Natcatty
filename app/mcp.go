// External MCP read surface: the app side of the stdio bridge. It projects
// Vault records, live Sessions, SFTP listings, and Port Forwarding views into
// the original catalog's read-only result shapes. Every write tool is answered
// with a bounded read-only explanation; write execution belongs to the later
// approvals ticket. Nothing here touches UI state.

package app

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	appforwarding "github.com/mslxl/natcatty/application/forwarding"
	appmcp "github.com/mslxl/natcatty/application/mcp"
	appsession "github.com/mslxl/natcatty/application/session"
	apptransfer "github.com/mslxl/natcatty/application/transfer"
	appvault "github.com/mslxl/natcatty/application/vault"
	domainforward "github.com/mslxl/natcatty/domain/forwarding"
	"github.com/mslxl/natcatty/domain/model"
	domainsftp "github.com/mslxl/natcatty/domain/transfer"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/internal/adapters/mcprpc"
	"github.com/mslxl/natcatty/internal/mcp/catalog"
)

// Result bounds. The stdio layer also caps one tool result at 256 KiB; these
// limits stop an individual listing or file read from allocating an unbounded
// buffer before that cap is applied.
const (
	// maxMCPListEntries bounds one directory listing. The write executor and the
	// read bridge share one source of truth for the original SFTP ceilings.
	maxMCPListEntries = appmcp.SFTPListLimit
	// maxMCPReadFileBytes matches the original in-memory SFTP read ceiling.
	// Larger files are refused with the original guidance, never truncated.
	maxMCPReadFileBytes = appmcp.SFTPReadLimit
)

// mcpVaultReader is the read-only Vault surface the bridge consumes.
type mcpVaultReader interface {
	List(context.Context) ([]domainvault.Host, error)
	Get(context.Context, model.HostID) (domainvault.Host, error)
	ListNotes(context.Context) ([]domainvault.Note, error)
	GetNote(context.Context, string) (domainvault.Note, error)
	ListIdentities(context.Context) ([]domainvault.Identity, error)
	ListProxyProfiles(context.Context) ([]domainvault.ProxyProfile, error)
	ListGroups(context.Context) ([]domainvault.Group, error)
	ListSnippets(context.Context) ([]domainvault.Snippet, error)
}

// mcpSessionReader is the read-only Session surface the bridge consumes.
type mcpSessionReader interface {
	Describe(model.SessionID) (appsession.SessionDescription, bool)
	DescribeAll() []appsession.SessionDescription
	ScreenText(model.SessionID) (appsession.ScreenTextSnapshot, bool)
}

// mcpForwardingReader is the read-only Port Forwarding surface.
type mcpForwardingReader interface {
	List(context.Context) ([]appforwarding.View, error)
}

// mcpTransferReader opens a remote filesystem for one saved Host.
type mcpTransferReader interface {
	OpenRemote(context.Context, model.HostID, string) (apptransfer.FileSystem, error)
}

// mcpReadBridge answers the external MCP transport's read RPCs.
type mcpReadBridge struct {
	vault      mcpVaultReader
	forwarding mcpForwardingReader
	sessions   mcpSessionReader
	transfer   mcpTransferReader
}

func newMCPReadBridge(vault mcpVaultReader, forwarding mcpForwardingReader,
	sessions mcpSessionReader, transfer mcpTransferReader) *mcpReadBridge {
	return &mcpReadBridge{
		vault: vault, forwarding: forwarding, sessions: sessions, transfer: transfer,
	}
}

// Handle is the mcprpc server handler: decode one request's params and run it.
func (b *mcpReadBridge) Handle(ctx context.Context, method string, params json.RawMessage) (any, error) {
	arguments := map[string]any{}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &arguments); err != nil {
			return nil, fmt.Errorf("external MCP: invalid params for %s: %w", method, err)
		}
	}
	return b.Call(ctx, method, arguments)
}

// Call executes one read RPC. Application-level failures are returned as
// {ok:false,error}, matching the original bridge, so the stdio layer renders
// them as tool errors rather than transport errors.
func (b *mcpReadBridge) Call(ctx context.Context, method string, params map[string]any) (any, error) {
	switch method {
	case "netcatty/getContext":
		return b.environment(ctx), nil
	case "netcatty/readContext":
		return b.terminalContext(params), nil
	case "netcatty/listAttachments":
		// The native app has no AI chat attachment store yet, so the deterministic
		// answer is an empty list rather than an error.
		return map[string]any{"ok": true, "attachments": []any{}}, nil
	case "netcatty/readAttachment":
		return mcpFailure("Attachment was not found."), nil
	case "netcatty/jobPoll":
		return mcpFailure("Background jobs are not available in this build."), nil
	case "netcatty/sftp/list":
		return b.sftpList(ctx, params), nil
	case "netcatty/sftp/read":
		return b.sftpRead(ctx, params), nil
	case "netcatty/sftp/stat":
		return b.sftpStat(ctx, params), nil
	case "netcatty/sftp/home":
		return b.sftpHome(ctx, params), nil
	case "public/vault/hosts/list":
		return b.hosts(ctx), nil
	case "public/vault/host/get":
		return b.host(ctx, params), nil
	case "public/vault/hostNotes/get":
		return b.hostNotes(ctx, params), nil
	case "public/vault/notes/list":
		return b.notes(ctx), nil
	case "public/vault/notes/get":
		return b.note(ctx, params), nil
	case "public/vault/identities/list":
		return b.identities(ctx), nil
	case "public/vault/proxyProfiles/list":
		return b.proxyProfiles(ctx), nil
	case "public/vault/groups/list":
		return b.groups(ctx), nil
	case "public/vault/snippets/list":
		return b.snippets(ctx), nil
	case "public/vault/snippets/get":
		return b.snippet(ctx, params), nil
	case "public/vault/scripts/list":
		// Scripts are stored as script-kind snippets in the original. This build
		// has no script subsystem yet, so the list is deterministically empty.
		return map[string]any{"ok": true, "scripts": []any{}}, nil
	case "public/vault/scripts/get":
		return mcpFailure(fmt.Sprintf("Script %q was not found.", stringParam(params, "scriptId"))), nil
	case "public/vault/scripts/reference":
		return mcpFailure("Scripts are not available in this build."), nil
	case "public/vault/scripts/runs/list":
		return map[string]any{"ok": true, "runs": []any{}}, nil
	case "public/vault/hostConnectScripts/list":
		return b.hostConnectScripts(ctx, params), nil
	case "public/portforward/rules/list":
		return b.forwardingRules(ctx), nil
	case "public/portforward/tunnels/list":
		return b.forwardingTunnels(ctx), nil
	}
	if tool, known := catalog.ByRPCMethod(method); known && tool.Policy.Write {
		return mcpFailure("External MCP is read-only in this build; write tools are not available yet."), nil
	}
	return nil, fmt.Errorf("external MCP: unknown method %s", method)
}

// environment mirrors getContext: environment identity, the scoped-session
// listing, active tunnels, and the small tool hint map. It deliberately omits
// the original's long prose guidance because the embedded catalog descriptions
// already carry the per-tool guidance.
func (b *mcpReadBridge) environment(ctx context.Context) map[string]any {
	hosts := make([]any, 0)
	for _, description := range b.sessions.DescribeAll() {
		hosts = append(hosts, sessionHostView(description))
	}
	return map[string]any{
		"environment": "netcatty-terminal",
		"description": "You are operating inside Netcatty, a multi-session terminal manager. " +
			"Call get_environment first, select a session by label or hostname, and pass its " +
			"sessionId to terminal and SFTP tools.",
		"hosts":                    hosts,
		"hostCount":                len(hosts),
		"activePortForwardTunnels": b.activeTunnels(ctx),
		"tools": map[string]any{
			"environment": "get_environment",
			"terminal": map[string]any{
				"execute": "terminal_execute",
				"start":   "terminal_start",
				"poll":    "terminal_poll",
				"stop":    "terminal_stop",
			},
			"attachments": map[string]any{
				"list": "list_attachments",
				"read": "read_attachment",
			},
			"sftp": map[string]any{
				"list":     "sftp_list",
				"read":     "sftp_read_file",
				"write":    "sftp_write_file",
				"download": "sftp_download",
				"upload":   "sftp_upload",
			},
		},
	}
}

// sessionHostView is the environment entry for one live Session. The read
// bridge and the scoped policy handler share it so their host shapes cannot
// drift apart.
func sessionHostView(description appsession.SessionDescription) map[string]any {
	return map[string]any{
		"sessionId":          string(description.ID),
		"hostname":           description.Hostname,
		"label":              description.Label,
		"os":                 "",
		"username":           description.Username,
		"protocol":           string(description.Kind),
		"shellType":          shellTypeFor(description.Kind),
		"deviceType":         "",
		"connected":          description.Connected,
		"hostId":             string(description.HostID),
		"hostChain":          []any{},
		"activePortForwards": []any{},
	}
}

func shellTypeFor(kind appsession.Kind) string {
	if kind == appsession.KindSerial {
		return "raw"
	}
	return ""
}

// terminalContext returns a bounded window of a Session's rendered replay.
func (b *mcpReadBridge) terminalContext(params map[string]any) map[string]any {
	sessionID := stringParam(params, "sessionId")
	if sessionID == "" {
		descriptions := b.sessions.DescribeAll()
		if len(descriptions) != 1 {
			return mcpFailure("sessionId is required.")
		}
		sessionID = string(descriptions[0].ID)
	}
	id := model.SessionID(sessionID)
	screen, ok := b.sessions.ScreenText(id)
	if !ok {
		return mcpFailure(fmt.Sprintf("Session %q was not found.", sessionID))
	}
	description, _ := b.sessions.Describe(id)
	lines := appsession.TerminalLines(screen.Text)
	totalLines := len(lines)

	rangeName := appsession.NormalizeTerminalRange(stringParam(params, "range"))
	maxLines := appsession.TerminalContextDefaultMaxLines
	if value, present := numberParam(params, "maxLines"); present {
		maxLines = appsession.NormalizeTerminalMaxLines(int(math.Round(value)))
	}
	startLine := 0
	if value, present := numberParam(params, "startLine"); present {
		startLine = int(math.Floor(value))
	}

	viewportStart, viewportEnd := 0, -1
	if totalLines > 0 && screen.Rows > 0 {
		viewportEnd = totalLines - 1
		viewportStart = max(0, totalLines-screen.Rows)
	}
	window := appsession.ResolveTerminalLineWindow(rangeName, totalLines,
		viewportStart, viewportEnd, startLine, maxLines)
	selected := make([]string, 0)
	if window.EndLine >= window.StartLine {
		selected = lines[window.StartLine : window.EndLine+1]
	}
	result := map[string]any{
		"ok":              true,
		"sessionId":       sessionID,
		"range":           string(rangeName),
		"content":         strings.Join(selected, "\n"),
		"totalLines":      totalLines,
		"startLine":       window.StartLine,
		"endLine":         window.EndLine,
		"returnedLines":   len(selected),
		"hasMoreBefore":   window.StartLine > 0,
		"hasMoreAfter":    window.EndLine >= 0 && window.EndLine < totalLines-1,
		"source":          "snapshot",
		"alternateScreen": screen.AlternateScreen,
	}
	if description.Label != "" {
		result["label"] = description.Label
	}
	return result
}

func (b *mcpReadBridge) sftpList(ctx context.Context, params map[string]any) map[string]any {
	return b.withRemoteFiles(ctx, params, func(files apptransfer.FileSystem) map[string]any {
		directory := stringParam(params, "path")
		if directory == "" {
			root, err := files.Root(ctx)
			if err != nil {
				return mcpFailure(err.Error())
			}
			directory = root
		}
		entries, err := files.List(ctx, directory)
		if err != nil {
			return mcpFailure(err.Error())
		}
		truncated := false
		if len(entries) > maxMCPListEntries {
			entries = entries[:maxMCPListEntries]
			truncated = true
		}
		listed := make([]any, 0, len(entries))
		for _, entry := range entries {
			listed = append(listed, mcpSFTPListEntry(entry))
		}
		result := map[string]any{"ok": true, "entries": listed}
		if truncated {
			result["truncated"] = true
		}
		return result
	})
}

func (b *mcpReadBridge) sftpRead(ctx context.Context, params map[string]any) map[string]any {
	path := stringParam(params, "path")
	if path == "" {
		return mcpFailure("path is required")
	}
	return b.withRemoteFiles(ctx, params, func(files apptransfer.FileSystem) map[string]any {
		knownSize := int64(-1)
		if entry, err := files.Stat(ctx, path); err == nil {
			knownSize = entry.Size
			if knownSize > maxMCPReadFileBytes {
				return mcpFailure(sftpReadTooLarge(knownSize))
			}
		}
		reader, err := files.Open(ctx, path)
		if err != nil {
			return mcpFailure(err.Error())
		}
		defer reader.Close()
		data, err := io.ReadAll(io.LimitReader(reader, maxMCPReadFileBytes+1))
		if err != nil {
			return mcpFailure(err.Error())
		}
		if len(data) > maxMCPReadFileBytes {
			size := int64(len(data))
			if knownSize >= 0 {
				size = knownSize
			}
			return mcpFailure(sftpReadTooLarge(size))
		}
		return map[string]any{"ok": true, "path": path, "content": string(data)}
	})
}

// sftpReadTooLarge is the original in-memory read refusal, verbatim.
func sftpReadTooLarge(size int64) string {
	return fmt.Sprintf("Remote file is too large to read into memory (%d bytes; maximum 10 MB). "+
		"Use SFTP download for large files, or open it with an external app.", size)
}

// mcpSFTPListEntry projects one Entry into the original listSftp shape:
// a string size, an ISO timestamp, symbolic permissions, and a linkTarget
// that names the resolved type rather than the link path.
func mcpSFTPListEntry(entry domainsftp.Entry) map[string]any {
	typeName := "file"
	if entry.Kind == domainsftp.KindDir {
		typeName = "directory"
	}
	var linkTarget any
	if entry.Symlink {
		typeName = "symlink"
		switch entry.Kind {
		case domainsftp.KindDir:
			linkTarget = "directory"
		case domainsftp.KindFile:
			linkTarget = "file"
		}
	}
	record := map[string]any{
		"name":         entry.Name,
		"type":         typeName,
		"linkTarget":   linkTarget,
		"size":         fmt.Sprintf("%d bytes", entry.Size),
		"lastModified": entry.ModTime.UTC().Format("2006-01-02T15:04:05.000Z"),
	}
	if permissions := mcpSymbolicPermissions(entry.Mode); permissions != "" {
		record["permissions"] = permissions
	}
	if entry.Owner != "" {
		record["owner"] = entry.Owner
	}
	return record
}

// mcpSFTPStat projects a followed stat into the original formatSftpStatResult
// shape: numeric size, Unix-millisecond modify time, octal permissions.
func mcpSFTPStat(entry domainsftp.Entry, requestedPath string) map[string]any {
	typeName := "file"
	if entry.Kind == domainsftp.KindDir {
		typeName = "directory"
	}
	record := map[string]any{
		"name":         path.Base(requestedPath),
		"type":         typeName,
		"size":         entry.Size,
		"sizeKnown":    true,
		"lastModified": entry.ModTime.UnixMilli(),
	}
	if entry.Mode != 0 {
		record["permissions"] = strconv.FormatUint(uint64(entry.Mode&0o777), 8)
	}
	return record
}

func mcpSymbolicPermissions(mode uint32) string {
	if mode == 0 {
		return ""
	}
	triplet := func(bits uint32) string {
		read, write, execute := "-", "-", "-"
		if bits&4 != 0 {
			read = "r"
		}
		if bits&2 != 0 {
			write = "w"
		}
		if bits&1 != 0 {
			execute = "x"
		}
		return read + write + execute
	}
	return triplet((mode>>6)&7) + triplet((mode>>3)&7) + triplet(mode&7)
}

func (b *mcpReadBridge) sftpStat(ctx context.Context, params map[string]any) map[string]any {
	path := stringParam(params, "path")
	if path == "" {
		return mcpFailure("path is required")
	}
	return b.withRemoteFiles(ctx, params, func(files apptransfer.FileSystem) map[string]any {
		entry, err := files.Stat(ctx, path)
		if err != nil {
			return mcpFailure(err.Error())
		}
		return map[string]any{"ok": true, "stat": mcpSFTPStat(entry, path)}
	})
}

func (b *mcpReadBridge) sftpHome(ctx context.Context, params map[string]any) map[string]any {
	return b.withRemoteFiles(ctx, params, func(files apptransfer.FileSystem) map[string]any {
		root, err := files.Root(ctx)
		if err != nil {
			return mcpFailure(err.Error())
		}
		return map[string]any{"ok": true, "homeDir": root}
	})
}

// withRemoteFiles resolves a Session's saved Host and opens one remote
// filesystem for the operation, closing it afterwards.
func (b *mcpReadBridge) withRemoteFiles(ctx context.Context, params map[string]any,
	action func(apptransfer.FileSystem) map[string]any) map[string]any {
	sessionID := stringParam(params, "sessionId")
	if sessionID == "" {
		return mcpFailure("sessionId is required.")
	}
	description, ok := b.sessions.Describe(model.SessionID(sessionID))
	if !ok {
		return mcpFailure(fmt.Sprintf("Session %q was not found.", sessionID))
	}
	if description.Kind != appsession.KindSSH || description.HostID == "" {
		return mcpFailure(fmt.Sprintf("Session %q is not an SSH session with file transfer.", sessionID))
	}
	if b.transfer == nil {
		return mcpFailure("File transfer is not available in this build.")
	}
	files, err := b.transfer.OpenRemote(ctx, description.HostID, description.Label)
	if err != nil {
		return mcpFailure(err.Error())
	}
	defer files.Close()
	return action(files)
}

func (b *mcpReadBridge) hosts(ctx context.Context) map[string]any {
	hosts, err := b.vault.List(ctx)
	if err != nil {
		return mcpFailure(err.Error())
	}
	summaries := make([]any, 0, len(hosts))
	for _, host := range hosts {
		summaries = append(summaries, summarizeHost(host))
	}
	return map[string]any{"ok": true, "hosts": summaries}
}

func (b *mcpReadBridge) host(ctx context.Context, params map[string]any) map[string]any {
	hostID := stringParam(params, "hostId")
	host, err := b.vault.Get(ctx, model.HostID(hostID))
	if errors.Is(err, appvault.ErrNotFound) {
		return mcpFailure(fmt.Sprintf("Host %q was not found.", hostID))
	}
	if err != nil {
		return mcpFailure(err.Error())
	}
	// The Host struct carries no credential plaintext: secrets live in the
	// field-secret store and every password field is json:"-". Marshaling the
	// record therefore yields the original's sanitized detail shape.
	encoded, err := json.Marshal(host)
	if err != nil {
		return mcpFailure(err.Error())
	}
	detail := map[string]any{}
	_ = json.Unmarshal(encoded, &detail)
	detail["ok"] = true
	detail["os"] = ""
	return map[string]any{"ok": true, "host": detail}
}

func (b *mcpReadBridge) hostNotes(ctx context.Context, params map[string]any) map[string]any {
	hostID := stringParam(params, "hostId")
	host, err := b.vault.Get(ctx, model.HostID(hostID))
	if errors.Is(err, appvault.ErrNotFound) {
		return mcpFailure(fmt.Sprintf("Host %q was not found.", hostID))
	}
	if err != nil {
		return mcpFailure(err.Error())
	}
	return map[string]any{"ok": true, "hostId": hostID, "notes": host.Notes}
}

func (b *mcpReadBridge) notes(ctx context.Context) map[string]any {
	notes, err := b.vault.ListNotes(ctx)
	if err != nil {
		return mcpFailure(err.Error())
	}
	summaries := make([]any, 0, len(notes))
	for _, note := range notes {
		summaries = append(summaries, map[string]any{
			"id":            note.ID,
			"title":         note.Title,
			"group":         note.Group,
			"tags":          []any{},
			"linkedHostIds": []any{},
			"updatedAt":     appvault.FormatVaultTime(note.UpdatedAt),
			"contentLength": appvault.UTF16Length(note.Content),
		})
	}
	return map[string]any{"ok": true, "notes": summaries}
}

func (b *mcpReadBridge) note(ctx context.Context, params map[string]any) map[string]any {
	noteID := stringParam(params, "noteId")
	note, err := b.vault.GetNote(ctx, noteID)
	if errors.Is(err, appvault.ErrNotFound) {
		return mcpFailure(fmt.Sprintf("Vault note %q was not found.", noteID))
	}
	if err != nil {
		return mcpFailure(err.Error())
	}
	excerpt, err := appvault.ReadNoteExcerpt(note, params)
	if err != nil {
		return mcpFailure(noteReadFailure(err))
	}
	result := map[string]any{
		"ok": true,
		"note": map[string]any{
			"id":        excerpt.Note.ID,
			"title":     excerpt.Note.Title,
			"content":   excerpt.Note.Content,
			"createdAt": appvault.FormatVaultTime(excerpt.Note.CreatedAt),
			"updatedAt": appvault.FormatVaultTime(excerpt.Note.UpdatedAt),
		},
		"offset":     excerpt.Offset,
		"endOffset":  excerpt.EndOffset,
		"totalChars": excerpt.TotalChars,
		"nextOffset": optionalInt(excerpt.NextOffset),
		"hasMore":    excerpt.HasMore,
	}
	if excerpt.QuerySent {
		if excerpt.MatchOffset != nil && *excerpt.MatchOffset < 0 {
			result["matchOffset"] = nil
		} else {
			result["matchOffset"] = optionalInt(excerpt.MatchOffset)
		}
	}
	return result
}

// noteReadFailure renders the original protocol messages for the typed
// application validation errors. The prose lives here, in the composition
// root, so application errors stay free of user-visible strings.
func noteReadFailure(err error) string {
	switch {
	case errors.Is(err, appvault.ErrNoteOffset):
		return "offset must be an integer within the note content."
	case errors.Is(err, appvault.ErrNoteMaxChars):
		return "maxChars must be an integer of at least 2 (capped at 6000)."
	case errors.Is(err, appvault.ErrNoteQuery):
		return "query must be a nonempty literal string of at most 200 characters."
	case errors.Is(err, appvault.ErrNoteChanged):
		return "The note changed. Restart reading from offset 0 without expectedUpdatedAt."
	case errors.Is(err, appvault.ErrNoteSplit):
		return "offset splits a Unicode character. Use the returned nextOffset."
	}
	return err.Error()
}

func (b *mcpReadBridge) identities(ctx context.Context) map[string]any {
	identities, err := b.vault.ListIdentities(ctx)
	if err != nil {
		return mcpFailure(err.Error())
	}
	records := make([]any, 0, len(identities))
	for _, identity := range identities {
		records = append(records, map[string]any{
			"id":         string(identity.ID),
			"label":      identity.Label,
			"username":   identity.Username,
			"authMethod": string(identity.AuthMethod),
			"keyId":      string(identity.KeyID),
		})
	}
	return map[string]any{"ok": true, "identities": records}
}

func (b *mcpReadBridge) proxyProfiles(ctx context.Context) map[string]any {
	profiles, err := b.vault.ListProxyProfiles(ctx)
	if err != nil {
		return mcpFailure(err.Error())
	}
	records := make([]any, 0, len(profiles))
	for _, profile := range profiles {
		records = append(records, map[string]any{
			"id":    profile.ID,
			"label": profile.Label,
			"type":  string(profile.Config.Type),
			"host":  profile.Config.Host,
			"port":  profile.Config.Port,
		})
	}
	return map[string]any{"ok": true, "proxyProfiles": records}
}

func (b *mcpReadBridge) groups(ctx context.Context) map[string]any {
	groups, err := b.vault.ListGroups(ctx)
	if err != nil {
		return mcpFailure(err.Error())
	}
	records := make([]any, 0, len(groups))
	for _, group := range groups {
		defaults := map[string]any{
			"path":             group.Path,
			"username":         group.Username,
			"port":             group.Port,
			"protocol":         string(group.Protocol),
			"authMethod":       string(group.AuthMethod),
			"identityId":       string(group.IdentityID),
			"keyId":            string(group.KeyID),
			"useSshAgent":      group.UseSSHAgent,
			"requiresMfa":      group.RequiresMFA,
			"legacyAlgorithms": group.LegacyAlgorithms,
			"skipEcdsaHostKey": group.SkipEcdsaHostKey,
			"proxyProfileId":   group.ProxyProfileID,
		}
		if group.Algorithms != nil {
			defaults["algorithms"] = group.Algorithms
		}
		records = append(records, map[string]any{"path": group.Path, "defaults": defaults})
	}
	return map[string]any{"ok": true, "groups": records}
}

func (b *mcpReadBridge) snippets(ctx context.Context) map[string]any {
	snippets, err := b.vault.ListSnippets(ctx)
	if err != nil {
		return mcpFailure(err.Error())
	}
	records := make([]any, 0, len(snippets))
	for _, snippet := range snippets {
		records = append(records, serializeSnippet(snippet, false))
	}
	return map[string]any{"ok": true, "snippets": records}
}

func (b *mcpReadBridge) snippet(ctx context.Context, params map[string]any) map[string]any {
	snippetID := stringParam(params, "snippetId")
	snippets, err := b.vault.ListSnippets(ctx)
	if err != nil {
		return mcpFailure(err.Error())
	}
	for _, snippet := range snippets {
		if snippet.ID == snippetID {
			return map[string]any{"ok": true, "snippet": serializeSnippet(snippet, true)}
		}
	}
	return mcpFailure(fmt.Sprintf("Snippet %q was not found.", snippetID))
}

// serializeSnippet matches the original agent serializer. This build has no
// script-kind Snippets, so kind is always "snippet".
func serializeSnippet(snippet domainvault.Snippet, includeCommand bool) map[string]any {
	targets := make([]any, 0, len(snippet.Targets))
	for _, target := range snippet.Targets {
		targets = append(targets, string(target))
	}
	record := map[string]any{
		"id":               snippet.ID,
		"label":            snippet.Label,
		"kind":             "snippet",
		"tags":             stringSlice(snippet.Tags),
		"targets":          targets,
		"targetsAllHosts":  snippet.TargetsAllHosts,
		"package":          snippet.Package,
		"noAutoRun":        snippet.NoAutoRun,
		"multiLineRunMode": string(snippet.MultiLineRunMode),
	}
	if snippet.TargetGroups != nil {
		record["targetGroups"] = stringSlice(snippet.TargetGroups)
	}
	if includeCommand {
		record["command"] = snippet.Command
	}
	return record
}

func (b *mcpReadBridge) hostConnectScripts(ctx context.Context, params map[string]any) map[string]any {
	hostID := stringParam(params, "hostId")
	if _, err := b.vault.Get(ctx, model.HostID(hostID)); err != nil {
		if errors.Is(err, appvault.ErrNotFound) {
			return mcpFailure(fmt.Sprintf("Host %q was not found.", hostID))
		}
		return mcpFailure(err.Error())
	}
	// Connect scripts are script-kind Snippets; this build has none.
	return map[string]any{
		"ok":               true,
		"hostId":           hostID,
		"connectScriptIds": []any{},
		"resolvedScripts":  []any{},
	}
}

func (b *mcpReadBridge) forwardingRules(ctx context.Context) map[string]any {
	views, err := b.forwarding.List(ctx)
	if err != nil {
		return mcpFailure(err.Error())
	}
	rules := make([]any, 0, len(views))
	for _, view := range views {
		rules = append(rules, ruleViewForAgent(view))
	}
	return map[string]any{"ok": true, "rules": rules}
}

func (b *mcpReadBridge) forwardingTunnels(ctx context.Context) map[string]any {
	return map[string]any{"ok": true, "tunnels": b.activeTunnels(ctx)}
}

func (b *mcpReadBridge) activeTunnels(ctx context.Context) []any {
	// A graph built without forwarding still answers get_environment; it just
	// has no tunnels to report.
	if b.forwarding == nil {
		return []any{}
	}
	views, err := b.forwarding.List(ctx)
	if err != nil {
		return []any{}
	}
	tunnels := make([]any, 0)
	for _, view := range views {
		if view.Status != domainforward.StatusActive {
			continue
		}
		tunnels = append(tunnels, ruleViewForAgent(view))
	}
	return tunnels
}

func ruleViewForAgent(view appforwarding.View) map[string]any {
	rule := view.Rule
	autoReconnect := rule.AutoStart
	if rule.AutoReconnect != nil {
		autoReconnect = *rule.AutoReconnect
	}
	detail := view.Detail
	if detail == "" && view.Error != "" {
		detail = string(view.Error)
	}
	return map[string]any{
		"id":            string(rule.ID),
		"label":         rule.Label,
		"type":          string(rule.Type),
		"localPort":     rule.LocalPort,
		"bindAddress":   rule.BindAddress,
		"remoteHost":    rule.RemoteHost,
		"remotePort":    rule.RemotePort,
		"hostId":        string(rule.HostID),
		"autoStart":     rule.AutoStart,
		"autoReconnect": autoReconnect,
		"status":        string(view.Status),
		"error":         detail,
		"createdAt":     appvault.FormatVaultTime(rule.CreatedAt),
	}
}

func summarizeHost(host domainvault.Host) map[string]any {
	return map[string]any{
		"id":        string(host.ID),
		"label":     host.Label,
		"hostname":  host.Hostname,
		"port":      host.Port,
		"username":  host.Username,
		"protocol":  string(host.Protocol),
		"group":     host.Group,
		"tags":      stringSlice(host.Tags),
		"os":        "",
		"createdAt": appvault.FormatVaultTime(host.CreatedAt),
	}
}

func mcpFailure(message string) map[string]any {
	return map[string]any{"ok": false, "error": message}
}

func optionalInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func stringSlice(values []string) []any {
	result := make([]any, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	return result
}

func stringParam(params map[string]any, key string) string {
	value, _ := params[key].(string)
	return value
}

// numberParam reads a JSON number. It reports false for a missing value or a
// non-number, which the call sites treat as "use the default".
func numberParam(params map[string]any, key string) (float64, bool) {
	switch value := params[key].(type) {
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return 0, false
		}
		return value, true
	case int:
		return float64(value), true
	default:
		return 0, false
	}
}

// externalMCP is one running authenticated loopback listener plus its
// discovery pointer.
type externalMCP struct {
	cancel        context.CancelFunc
	done          chan error
	discoveryPath string
	port          int
	token         string
	closeOnce     sync.Once
}

// externalMCPOption customizes the listener without changing the ticket-56
// startExternalMCP call shape.
type externalMCPOption func(*externalMCPConfig)

type externalMCPConfig struct {
	handler mcprpc.Handler
	mode    *mcpModeState
}

// withMCPHandler serves a policy handler instead of the bare read bridge.
func withMCPHandler(handler mcprpc.Handler) externalMCPOption {
	return func(config *externalMCPConfig) { config.handler = handler }
}

// withMCPModeState makes the discovery file's permissionMode authoritative and
// lets Settings rewrite it.
func withMCPModeState(mode *mcpModeState) externalMCPOption {
	return func(config *externalMCPConfig) { config.mode = mode }
}

// runningExternalMCP is the listener run() started, published so the selftest
// build can authenticate against the production surface. It stays nil when the
// listener failed to start and when a test builds the window graph directly.
var runningExternalMCP *externalMCP

// startExternalMCP binds loopback, serves the read bridge, and publishes the
// discovery file. The listener is bound before the file is written, so a
// client that reads the file can always connect.
func startExternalMCP(parent context.Context, bridge *mcpReadBridge,
	discoveryPath string, logf func(string, ...any), options ...externalMCPOption) (*externalMCP, error) {
	config := externalMCPConfig{}
	for _, option := range options {
		option(&config)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("external MCP: listen: %w", err)
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("external MCP: create token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	port := listener.Addr().(*net.TCPAddr).Port

	handler := config.handler
	if handler == nil {
		handler = mcprpc.HandlerFunc(bridge.Handle)
	}
	server := mcprpc.NewServer(token, handler)
	ctx, cancel := context.WithCancel(parent)
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, listener) }()

	mode := "confirm"
	if config.mode != nil {
		mode = string(config.mode.Get())
	}
	if _, err := mcprpc.WriteDiscovery(discoveryPath, mcprpc.Discovery{
		Host:           "127.0.0.1",
		Port:           port,
		Token:          token,
		PID:            os.Getpid(),
		PermissionMode: mode,
		ChatSessionID:  mcprpc.ExternalScope,
	}); err != nil {
		cancel()
		_ = listener.Close()
		<-done
		return nil, err
	}
	if logf != nil {
		logf("natcatty: external MCP listening on 127.0.0.1:%d", port)
	}
	return &externalMCP{
		cancel: cancel, done: done, discoveryPath: discoveryPath, port: port, token: token,
	}, nil
}

// Close stops the listener and removes the discovery file only if it still
// points at this listener.
func (e *externalMCP) Close() {
	if e == nil {
		return
	}
	e.closeOnce.Do(func() {
		e.cancel()
		select {
		case <-e.done:
		case <-time.After(5 * time.Second):
		}
		_ = mcprpc.RemoveDiscoveryIfOwned(e.discoveryPath, e.port, e.token)
	})
}

// externalMCPDiscoveryPath resolves the launch-time override, then the app's
// user-data directory. It is split from startExternalMCP so the composition
// root supplies the same path MyGo uses.
func externalMCPDiscoveryPath(userData string) string {
	if configured := strings.TrimSpace(os.Getenv(mcprpc.EnvDiscoveryFile)); configured != "" {
		return configured
	}
	return filepath.Join(userData, "external-mcp", "discovery.json")
}
