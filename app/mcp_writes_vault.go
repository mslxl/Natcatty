package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	appmcp "github.com/mslxl/natcatty/application/mcp"
	appvault "github.com/mslxl/natcatty/application/vault"
	domainforward "github.com/mslxl/natcatty/domain/forwarding"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/domain/vaultimport"
)

// This file maps the Vault, Note, Group, Snippet, and Port Forwarding write
// tools onto their application services. Fields the application has no
// behavior for are reported in the result's ignoredFields list instead of
// being silently represented as saved state.

func hasArg(arguments map[string]any, keys ...string) bool {
	for _, key := range keys {
		if _, ok := arguments[key]; ok {
			return true
		}
	}
	return false
}

func firstStringArg(arguments map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := stringParam(arguments, key); value != "" {
			return value
		}
	}
	return ""
}

func (e *mcpWriteExecutor) executeHosts(ctx context.Context, tool string,
	arguments map[string]any) (map[string]any, error) {

	switch tool {
	case "vault_hosts_create":
		return e.createHosts(ctx, tool, arguments)
	case "vault_hosts_update":
		return e.updateHost(ctx, tool, arguments)
	case "vault_hosts_delete":
		hostID := stringParam(arguments, "hostId")
		if hostID == "" {
			return nil, appmcp.InvalidArgumentError(tool, "hostId is required.")
		}
		if err := e.vault.Delete(ctx, model.HostID(hostID)); err != nil {
			return nil, e.vaultToolError(tool, err)
		}
		return map[string]any{"ok": true, "hostId": hostID, "deleted": true}, nil
	case "vault_hosts_import":
		return e.importHosts(ctx, tool, arguments)
	case "host_notes_set":
		hostID := stringParam(arguments, "hostId")
		notes := stringParam(arguments, "notes")
		if hostID == "" {
			return nil, appmcp.InvalidArgumentError(tool, "hostId is required.")
		}
		host, err := e.findHost(ctx, model.HostID(hostID))
		if err != nil {
			return nil, e.vaultToolError(tool, err)
		}
		draft := hostDraftFromHost(host)
		draft.Notes = notes
		updated, err := e.vault.Update(ctx, host.ID, draft)
		if err != nil {
			return nil, e.vaultToolError(tool, err)
		}
		return map[string]any{"ok": true, "hostId": string(updated.ID)}, nil
	}
	return nil, appmcp.ToolUnavailableError(tool, "Unsupported Host write.")
}

func (e *mcpWriteExecutor) createHosts(ctx context.Context, tool string,
	arguments map[string]any) (map[string]any, error) {

	raw := stringParam(arguments, "hosts")
	if raw == "" {
		return nil, appmcp.InvalidArgumentError(tool, "hosts is required.")
	}
	entries, err := parseJSONArray(raw)
	if err != nil {
		return nil, appmcp.InvalidArgumentError(tool, "hosts must be a JSON array.")
	}
	dryRun := boolArg(arguments, "dryRun")
	skipDuplicates := true
	if hasArg(arguments, "skipDuplicates") {
		skipDuplicates = boolArg(arguments, "skipDuplicates")
	}
	existing, err := e.vault.List(ctx)
	if err != nil {
		return nil, e.vaultToolError(tool, err)
	}
	existingKeys := make(map[string]bool, len(existing))
	for _, host := range existing {
		existingKeys[host.MergeKey()] = true
	}

	created := make([]map[string]any, 0, len(entries))
	skipped := 0
	ignored := make([]string, 0)
	for _, entry := range entries {
		draft, entryIgnored := hostDraftFromCreateObject(entry)
		ignored = appendUnique(ignored, entryIgnored...)
		normalized, normalizeErr := domainvault.NormalizeDraft(draft)
		if normalizeErr != nil {
			return nil, appmcp.InvalidArgumentError(tool,
				fmt.Sprintf("Host %q: %s", entryHostname(entry), normalizeErr))
		}
		probe := domainvault.Host{
			Label: draft.Label, Hostname: normalized.Hostname, Port: normalized.Port,
			Username: normalized.Username, Protocol: normalized.Protocol, Group: normalized.Group,
		}
		if existingKeys[probe.MergeKey()] {
			if skipDuplicates {
				skipped++
				continue
			}
			return nil, appmcp.InvalidArgumentError(tool,
				fmt.Sprintf("A Host matching %q already exists; the Vault does not allow duplicates.",
					normalized.Hostname))
		}
		if dryRun {
			created = append(created, hostSummary(domainvault.Host{
				Label: normalized.Label, Hostname: normalized.Hostname,
				Port: normalized.Port, Username: normalized.Username,
				Protocol: normalized.Protocol, Group: normalized.Group,
			}))
			continue
		}
		if keyPath := firstStringArg(entry, "keyPath", "keypath"); keyPath != "" {
			key, keyErr := e.keyForPath(ctx, keyPath, stringParam(entry, "passphrase"))
			if keyErr != nil {
				return nil, e.vaultToolError(tool, keyErr)
			}
			draft.KeyID = key.ID
		}
		host, createErr := e.vault.Create(ctx, draft)
		if createErr != nil {
			return nil, e.vaultToolError(tool, createErr)
		}
		existingKeys[host.MergeKey()] = true
		created = append(created, hostSummary(host))
	}
	result := map[string]any{
		"ok": true, "dryRun": dryRun, "created": created,
		"createdCount": len(created), "skippedDuplicates": skipped,
	}
	if len(ignored) > 0 {
		result["ignoredFields"] = ignored
	}
	return result, nil
}

func entryHostname(entry map[string]any) string {
	return firstStringArg(entry, "hostname", "host", "ip")
}

func hostSummary(host domainvault.Host) map[string]any {
	return map[string]any{
		"id": string(host.ID), "label": host.Label, "hostname": host.Hostname,
		"port": host.Port, "username": host.Username,
		"protocol": string(host.Protocol), "group": host.Group,
	}
}

func hostDraftFromCreateObject(entry map[string]any) (domainvault.Draft, []string) {
	ignored := make([]string, 0)
	draft := domainvault.Draft{
		Label:    firstStringArg(entry, "label", "name"),
		Hostname: firstStringArg(entry, "hostname", "host", "ip"),
		Username: stringParam(entry, "username"),
		Protocol: domainvault.Protocol(strings.ToLower(stringParam(entry, "protocol"))),
		Group:    stringParam(entry, "group"),
		Tags:     stringSliceArg(entry, "tags"),
		Notes:    stringParam(entry, "notes"),
		Password: stringParam(entry, "password"),
	}
	if port, ok := intArg(entry, "port"); ok {
		draft.Port = port
	}
	if draft.Label == "" {
		draft.Label = draft.Hostname
	}
	for _, key := range []string{"os", "savePassword"} {
		if hasArg(entry, key) {
			ignored = append(ignored, key)
		}
	}
	return draft, ignored
}

// hostDraftFromHost preserves every editable field the application stores.
func hostDraftFromHost(host domainvault.Host) domainvault.Draft {
	draft := domainvault.Draft{
		Label: host.Label, Hostname: host.Hostname, Port: host.Port,
		Username: host.Username, Protocol: host.Protocol, Group: host.Group,
		Tags: append([]string(nil), host.Tags...), Notes: host.Notes,
		Pinned: host.Pinned, IdentityID: host.IdentityID, KeyID: host.KeyID,
		AuthMethod: host.AuthMethod, UseSSHAgent: host.UseSSHAgent,
		RequiresMFA:   host.RequiresMFA,
		X11Forwarding: host.X11Forwarding, X11WarningAcknowledged: host.X11WarningAcknowledged,
		ProxyProfileID: host.ProxyProfileID, LegacyAlgorithms: host.LegacyAlgorithms,
		SkipEcdsaHostKey: host.SkipEcdsaHostKey, Algorithms: host.Algorithms,
		SFTPFileProtocol: host.SFTPFileProtocol, SFTPSudo: host.SFTPSudo,
		DeviceType: host.DeviceType, MoshServerPath: host.MoshServerPath,
		ETPort: host.ETPort,
	}
	if host.HostChain != nil {
		chain := *host.HostChain
		chain.HostIDs = append([]model.HostID(nil), host.HostChain.HostIDs...)
		draft.HostChain = &chain
	}
	if host.Proxy != nil {
		proxy := *host.Proxy
		draft.Proxy = &proxy
	}
	return draft
}

func (e *mcpWriteExecutor) updateHost(ctx context.Context, tool string,
	arguments map[string]any) (map[string]any, error) {

	hostID := stringParam(arguments, "hostId")
	if hostID == "" {
		return nil, appmcp.InvalidArgumentError(tool, "hostId is required.")
	}
	host, err := e.findHost(ctx, model.HostID(hostID))
	if err != nil {
		return nil, e.vaultToolError(tool, err)
	}
	draft := hostDraftFromHost(host)
	ignored := make([]string, 0)

	if hasArg(arguments, "label", "name") {
		draft.Label = firstStringArg(arguments, "label", "name")
	}
	if hasArg(arguments, "hostname", "host", "ip") {
		draft.Hostname = firstStringArg(arguments, "hostname", "host", "ip")
	}
	if hasArg(arguments, "port") {
		port, ok := intArg(arguments, "port")
		if !ok {
			return nil, appmcp.InvalidArgumentError(tool, "port must be a number.")
		}
		draft.Port = port
	}
	if hasArg(arguments, "username") {
		username := stringParam(arguments, "username")
		if username != draft.Username {
			// The catalog contract detaches a reusable Identity when the login
			// name changes so the explicit value takes effect.
			draft.IdentityID = ""
		}
		draft.Username = username
	}
	if hasArg(arguments, "group") {
		draft.Group = stringParam(arguments, "group")
	}
	if hasArg(arguments, "tags") {
		draft.Tags = stringSliceArg(arguments, "tags")
	}
	if hasArg(arguments, "notes") {
		draft.Notes = stringParam(arguments, "notes")
	}
	if hasArg(arguments, "protocol") {
		draft.Protocol = domainvault.Protocol(strings.ToLower(stringParam(arguments, "protocol")))
	}
	if hasArg(arguments, "identityId") {
		draft.IdentityID = model.IdentityID(stringParam(arguments, "identityId"))
	}
	if hasArg(arguments, "jumpHostIds") {
		ids := stringSliceArg(arguments, "jumpHostIds")
		if len(ids) == 0 {
			draft.HostChain = nil
		} else {
			converted := make([]model.HostID, 0, len(ids))
			for _, id := range ids {
				converted = append(converted, model.HostID(id))
			}
			draft.HostChain = &domainvault.HostChain{HostIDs: converted}
		}
	}
	if hasArg(arguments, "proxyProfileId") {
		draft.ProxyProfileID = stringParam(arguments, "proxyProfileId")
	}
	if hasArg(arguments, "password") {
		draft.Password = stringParam(arguments, "password")
	}
	if hasArg(arguments, "keyPath", "keypath") {
		keyPath := firstStringArg(arguments, "keyPath", "keypath")
		if keyPath == "" {
			draft.KeyID = ""
		} else {
			key, keyErr := e.keyForPath(ctx, keyPath, stringParam(arguments, "passphrase"))
			if keyErr != nil {
				return nil, e.vaultToolError(tool, keyErr)
			}
			draft.KeyID = key.ID
		}
	} else if hasArg(arguments, "passphrase") && draft.KeyID != "" {
		if err := e.setKeyPassphrase(ctx, draft.KeyID, stringParam(arguments, "passphrase")); err != nil {
			return nil, e.vaultToolError(tool, err)
		}
	}
	for _, key := range []string{
		"os", "startupCommand", "startupCommandRunMode", "environmentVariables",
		"moshEnabled", "moshServerPath", "etEnabled", "etPort", "serialConfig",
	} {
		if hasArg(arguments, key) {
			ignored = append(ignored, key)
		}
	}

	updated, err := e.vault.Update(ctx, host.ID, draft)
	if err != nil {
		return nil, e.vaultToolError(tool, err)
	}
	if hasArg(arguments, "password") && stringParam(arguments, "password") == "" {
		if err := e.vault.DeleteSecret(ctx, host.ID, "password"); err != nil {
			return nil, e.vaultToolError(tool, err)
		}
	}
	if hasArg(arguments, "savePassword") && !boolArg(arguments, "savePassword") &&
		!hasArg(arguments, "password") {
		if err := e.vault.DeleteSecret(ctx, host.ID, "password"); err != nil {
			return nil, e.vaultToolError(tool, err)
		}
	}
	result := hostSummary(updated)
	result["ok"] = true
	result["hasPassword"] = updated.HasPassword
	if len(ignored) > 0 {
		result["ignoredFields"] = ignored
	}
	return result, nil
}

func (e *mcpWriteExecutor) findHost(ctx context.Context, id model.HostID) (domainvault.Host, error) {
	hosts, err := e.vault.List(ctx)
	if err != nil {
		return domainvault.Host{}, err
	}
	for _, host := range hosts {
		if host.ID == id {
			return host, nil
		}
	}
	return domainvault.Host{}, appvault.ErrNotFound
}

func (e *mcpWriteExecutor) keyForPath(ctx context.Context, keyPath, passphrase string) (domainvault.Key, error) {
	keys, err := e.vault.ListKeys(ctx)
	if err != nil {
		return domainvault.Key{}, err
	}
	for _, key := range keys {
		if strings.EqualFold(key.FilePath, keyPath) {
			if passphrase != "" {
				if err := e.setKeyPassphrase(ctx, key.ID, passphrase); err != nil {
					return domainvault.Key{}, err
				}
			}
			return key, nil
		}
	}
	expanded := expandHome(keyPath)
	info, err := os.Stat(expanded)
	if err != nil {
		return domainvault.Key{}, fmt.Errorf("Key file %q could not be read.", keyPath)
	}
	if info.Size() > 256<<10 {
		return domainvault.Key{}, fmt.Errorf("Key file %q is too large.", keyPath)
	}
	material, err := os.ReadFile(expanded)
	if err != nil {
		return domainvault.Key{}, fmt.Errorf("Key file %q could not be read.", keyPath)
	}
	label := strings.TrimSuffix(filepath.Base(expanded), filepath.Ext(expanded))
	if label == "" {
		label = keyPath
	}
	return e.vault.CreateKey(ctx, domainvault.KeyDraft{
		Label: label, PrivateKey: string(material), Passphrase: passphrase,
		SavePassphrase: passphrase != "", Source: domainvault.KeySourceReference,
		FilePath: keyPath,
	})
}

func (e *mcpWriteExecutor) setKeyPassphrase(ctx context.Context, keyID model.KeyID, passphrase string) error {
	key, err := e.vault.GetKey(ctx, keyID)
	if err != nil {
		return err
	}
	draft := domainvault.KeyDraft{
		Label: key.Label, Source: key.Source, FilePath: key.FilePath,
	}
	if passphrase == "" {
		draft.ClearPassphrase = true
	} else {
		draft.Passphrase = passphrase
		draft.SavePassphrase = true
	}
	_, err = e.vault.UpdateKey(ctx, keyID, draft)
	return err
}

func expandHome(value string) string {
	if value == "~" || strings.HasPrefix(value, "~/") || strings.HasPrefix(value, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			if len(value) == 1 {
				return home
			}
			return filepath.Join(home, value[2:])
		}
	}
	return value
}

func (e *mcpWriteExecutor) importHosts(ctx context.Context, tool string,
	arguments map[string]any) (map[string]any, error) {

	text := stringParam(arguments, "text")
	if text == "" {
		return nil, appmcp.InvalidArgumentError(tool, "text is required.")
	}
	format := strings.ToLower(strings.TrimSpace(stringParam(arguments, "format")))
	if format == "auto" {
		format = ""
	}
	dryRun := boolArg(arguments, "dryRun")
	merge := appvault.ImportMergeSkip
	if hasArg(arguments, "skipDuplicates") && !boolArg(arguments, "skipDuplicates") {
		merge = appvault.ImportMergeOverwrite
	}
	preview, err := e.vault.PreviewImport(ctx, text, appvault.PreviewImportOptions{
		Format: vaultimport.Format(format), FileName: stringParam(arguments, "fileName"),
	})
	if err != nil {
		return nil, e.vaultToolError(tool, err)
	}
	if dryRun {
		return map[string]any{
			"ok": true, "dryRun": true, "format": string(preview.Format),
			"total": preview.Counts.Total, "new": preview.Counts.New,
			"conflicts": preview.Counts.Conflicts, "duplicates": preview.Counts.Duplicates,
			"invalid": preview.Counts.Invalid,
		}, nil
	}
	committed, err := e.vault.CommitImport(ctx, preview, appvault.ImportCommitOptions{Merge: merge})
	if err != nil {
		return nil, e.vaultToolError(tool, err)
	}
	return map[string]any{
		"ok": true, "dryRun": false, "format": string(preview.Format),
		"imported": len(committed.Imported), "overwritten": len(committed.Overwritten),
		"skipped": committed.Skipped, "groupsCreated": committed.GroupsCreated,
		"familyCreated": committed.FamilyCreated, "done": committed.Done,
	}, nil
}

func (e *mcpWriteExecutor) executeNotes(ctx context.Context, tool string,
	arguments map[string]any) (map[string]any, error) {

	switch tool {
	case "vault_notes_create":
		title := stringParam(arguments, "title")
		if title == "" {
			return nil, appmcp.InvalidArgumentError(tool, "title is required.")
		}
		note, err := e.vault.CreateNote(ctx, domainvault.NoteDraft{
			Title: title, Content: stringParam(arguments, "content"),
			Group: stringParam(arguments, "group"),
		})
		if err != nil {
			return nil, e.vaultToolError(tool, err)
		}
		result := noteSummary(note)
		result["ok"] = true
		if ignored := noteIgnoredFields(arguments); len(ignored) > 0 {
			result["ignoredFields"] = ignored
		}
		return result, nil
	case "vault_notes_update":
		noteID := stringParam(arguments, "noteId")
		if noteID == "" {
			return nil, appmcp.InvalidArgumentError(tool, "noteId is required.")
		}
		existing, err := e.vault.GetNote(ctx, noteID)
		if err != nil {
			return nil, e.vaultToolError(tool, err)
		}
		draft := domainvault.NoteDraft{
			Title: existing.Title, Content: existing.Content, Group: existing.Group,
		}
		if hasArg(arguments, "title") {
			draft.Title = stringParam(arguments, "title")
		}
		if hasArg(arguments, "content") {
			draft.Content = stringParam(arguments, "content")
		}
		if hasArg(arguments, "group") {
			draft.Group = stringParam(arguments, "group")
		}
		note, err := e.vault.UpdateNote(ctx, noteID, draft)
		if err != nil {
			return nil, e.vaultToolError(tool, err)
		}
		result := noteSummary(note)
		result["ok"] = true
		if ignored := noteIgnoredFields(arguments); len(ignored) > 0 {
			result["ignoredFields"] = ignored
		}
		return result, nil
	case "vault_notes_delete":
		noteID := stringParam(arguments, "noteId")
		if noteID == "" {
			return nil, appmcp.InvalidArgumentError(tool, "noteId is required.")
		}
		if err := e.vault.DeleteNote(ctx, noteID); err != nil {
			return nil, e.vaultToolError(tool, err)
		}
		return map[string]any{"ok": true, "noteId": noteID, "deleted": true}, nil
	case "vault_notes_import":
		return e.importNotes(ctx, tool, arguments)
	}
	return nil, appmcp.ToolUnavailableError(tool, "Unsupported Note write.")
}

func noteSummary(note domainvault.Note) map[string]any {
	return map[string]any{
		"id": note.ID, "title": note.Title, "group": note.Group,
		"contentLength": len(note.Content),
	}
}

func noteIgnoredFields(arguments map[string]any) []string {
	ignored := make([]string, 0)
	for _, key := range []string{"linkedHostIds", "tags"} {
		if hasArg(arguments, key) {
			ignored = append(ignored, key)
		}
	}
	return ignored
}

type noteImportDocument struct {
	FileName string `json:"fileName"`
	Content  string `json:"content"`
	Title    string `json:"title"`
}

func (e *mcpWriteExecutor) importNotes(ctx context.Context, tool string,
	arguments map[string]any) (map[string]any, error) {

	group := stringParam(arguments, "group")
	documents := make([]noteImportDocument, 0)
	if rawDocuments := stringParam(arguments, "documents"); rawDocuments != "" {
		if hasArg(arguments, "content") {
			return nil, appmcp.InvalidArgumentError(tool,
				"content and documents cannot be combined.")
		}
		if err := json.Unmarshal([]byte(rawDocuments), &documents); err != nil {
			return nil, appmcp.InvalidArgumentError(tool, "documents must be a JSON array.")
		}
	} else {
		documents = append(documents, noteImportDocument{
			FileName: stringParam(arguments, "fileName"),
			Content:  stringParam(arguments, "content"),
			Title:    stringParam(arguments, "title"),
		})
	}
	if len(documents) == 0 {
		return nil, appmcp.InvalidArgumentError(tool, "documents must not be empty.")
	}
	created := make([]map[string]any, 0, len(documents))
	for _, document := range documents {
		title := strings.TrimSpace(document.Title)
		if title == "" {
			title = noteTitleFromMarkdown(document.Content)
		}
		if title == "" {
			title = titleFromFileName(document.FileName)
		}
		if title == "" {
			return nil, appmcp.InvalidArgumentError(tool,
				"Each imported Note needs a title, a level-one heading, or a fileName.")
		}
		note, err := e.vault.CreateNote(ctx, domainvault.NoteDraft{
			Title: title, Content: document.Content, Group: group,
		})
		if err != nil {
			return nil, e.vaultToolError(tool, err)
		}
		created = append(created, noteSummary(note))
	}
	return map[string]any{"ok": true, "imported": created, "count": len(created)}, nil
}

func noteTitleFromMarkdown(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if strings.HasPrefix(line, "# ") {
			if title := strings.TrimSpace(line[2:]); title != "" {
				return title
			}
		}
	}
	return ""
}

func titleFromFileName(fileName string) string {
	if strings.TrimSpace(fileName) == "" {
		return ""
	}
	base := path.Base(strings.ReplaceAll(fileName, `\`, "/"))
	base = strings.TrimSuffix(base, path.Ext(base))
	return strings.TrimSpace(base)
}

func (e *mcpWriteExecutor) executeGroups(ctx context.Context, tool string,
	arguments map[string]any) (map[string]any, error) {

	switch tool {
	case "vault_groups_create":
		groupPath := stringParam(arguments, "path")
		if groupPath == "" {
			return nil, appmcp.InvalidArgumentError(tool, "path is required.")
		}
		defaults, parseErr := parseDefaults(arguments)
		if parseErr != nil {
			return nil, appmcp.InvalidArgumentError(tool, parseErr.Error())
		}
		draft := domainvault.GroupDraft{Path: groupPath}
		ignored := applyGroupDefaults(&draft, defaults)
		group, err := e.vault.CreateGroup(ctx, draft)
		if err != nil {
			return nil, e.vaultToolError(tool, err)
		}
		result := map[string]any{"ok": true, "path": group.Path}
		if len(ignored) > 0 {
			result["ignoredFields"] = ignored
		}
		return result, nil
	case "vault_groups_update":
		groupPath := stringParam(arguments, "path")
		if groupPath == "" {
			return nil, appmcp.InvalidArgumentError(tool, "path is required.")
		}
		defaults, parseErr := parseDefaults(arguments)
		if parseErr != nil {
			return nil, appmcp.InvalidArgumentError(tool, parseErr.Error())
		}
		groups, err := e.vault.ListGroups(ctx)
		if err != nil {
			return nil, e.vaultToolError(tool, err)
		}
		draft := domainvault.GroupDraft{Path: domainvault.NormalizeGroup(groupPath)}
		for _, group := range groups {
			if group.Path != draft.Path {
				continue
			}
			draft = domainvault.GroupDraft{
				Path: group.Path, Notes: group.Notes, Username: group.Username,
				Port: group.Port, Protocol: group.Protocol, AuthMethod: group.AuthMethod,
				IdentityID: group.IdentityID, KeyID: group.KeyID,
				UseSSHAgent: group.UseSSHAgent, RequiresMFA: group.RequiresMFA,
				LegacyAlgorithms: group.LegacyAlgorithms,
				SkipEcdsaHostKey: group.SkipEcdsaHostKey,
				Algorithms:       group.Algorithms, ProxyProfileID: group.ProxyProfileID,
			}
			break
		}
		if hasArg(arguments, "newPath") {
			draft.Path = stringParam(arguments, "newPath")
		}
		ignored := applyGroupDefaults(&draft, defaults)
		group, err := e.vault.UpdateGroup(ctx, groupPath, draft)
		if err != nil {
			return nil, e.vaultToolError(tool, err)
		}
		result := map[string]any{"ok": true, "path": group.Path}
		if len(ignored) > 0 {
			result["ignoredFields"] = ignored
		}
		return result, nil
	case "vault_groups_delete":
		groupPath := stringParam(arguments, "path")
		if groupPath == "" {
			return nil, appmcp.InvalidArgumentError(tool, "path is required.")
		}
		deleteHosts := boolArg(arguments, "deleteHosts")
		hosts, err := e.vault.List(ctx)
		if err != nil {
			return nil, e.vaultToolError(tool, err)
		}
		deletedHosts := 0
		if deleteHosts {
			for _, host := range hosts {
				if !domainvault.IsGroupAtOrBelow(host.Group, groupPath) {
					continue
				}
				if err := e.vault.Delete(ctx, host.ID); err != nil {
					return nil, e.vaultToolError(tool, err)
				}
				deletedHosts++
			}
		}
		if err := e.vault.DeleteGroup(ctx, groupPath); err != nil {
			if !errors.Is(err, appvault.ErrNotFound) || deletedHosts == 0 {
				return nil, e.vaultToolError(tool, err)
			}
		}
		return map[string]any{
			"ok": true, "path": domainvault.NormalizeGroup(groupPath),
			"deletedHosts": deletedHosts,
		}, nil
	}
	return nil, appmcp.ToolUnavailableError(tool, "Unsupported Group write.")
}

// parseDefaults accepts the catalog's defaults JSON string or a native object.
func parseDefaults(arguments map[string]any) (map[string]any, error) {
	value, present := arguments["defaults"]
	if !present {
		return map[string]any{}, nil
	}
	if object, ok := value.(map[string]any); ok {
		return object, nil
	}
	text, ok := value.(string)
	if !ok {
		return nil, errors.New("defaults must be a JSON object.")
	}
	return parseJSONObject(text)
}

func applyGroupDefaults(draft *domainvault.GroupDraft, defaults map[string]any) []string {
	ignored := make([]string, 0)
	for key, value := range defaults {
		switch key {
		case "username":
			draft.Username = anyString(value)
		case "identityId":
			draft.IdentityID = model.IdentityID(anyString(value))
		case "proxyProfileId":
			draft.ProxyProfileID = anyString(value)
		case "keyId":
			draft.KeyID = model.KeyID(anyString(value))
		case "port":
			if parsed, ok := anyInt(value); ok {
				draft.Port = parsed
			}
		case "protocol":
			draft.Protocol = domainvault.Protocol(anyString(value))
		case "authMethod":
			draft.AuthMethod = domainvault.HostAuthMethod(anyString(value))
		case "useSshAgent":
			draft.UseSSHAgent = anyBool(value)
		case "requiresMfa":
			draft.RequiresMFA = anyBool(value)
		case "jumpHostIds", "startupCommand", "environmentVariables",
			"moshEnabled", "moshServerPath", "etEnabled", "etPort":
			ignored = append(ignored, "defaults."+key)
		}
	}
	sortStrings(ignored)
	return ignored
}

func (e *mcpWriteExecutor) executeSnippets(ctx context.Context, tool string,
	arguments map[string]any) (map[string]any, error) {

	switch tool {
	case "snippets_create":
		if strings.EqualFold(stringParam(arguments, "kind"), "script") {
			return nil, appmcp.ToolUnavailableError(tool,
				"Script snippets are not available in this build.")
		}
		label := stringParam(arguments, "label")
		if label == "" {
			return nil, appmcp.InvalidArgumentError(tool, "label is required.")
		}
		draft := snippetDraftFromArguments(arguments)
		draft.Label = label
		draft.Command = stringParam(arguments, "content")
		snippet, err := e.vault.CreateSnippet(ctx, draft)
		if err != nil {
			return nil, e.vaultToolError(tool, err)
		}
		result := snippetSummary(snippet)
		result["ok"] = true
		if ignored := snippetIgnoredFields(arguments); len(ignored) > 0 {
			result["ignoredFields"] = ignored
		}
		return result, nil
	case "snippets_update":
		snippetID := stringParam(arguments, "snippetId")
		if snippetID == "" {
			return nil, appmcp.InvalidArgumentError(tool, "snippetId is required.")
		}
		if strings.EqualFold(stringParam(arguments, "kind"), "script") {
			return nil, appmcp.ToolUnavailableError(tool,
				"Script snippets are not available in this build.")
		}
		existing, err := e.findSnippet(ctx, snippetID)
		if err != nil {
			return nil, e.vaultToolError(tool, err)
		}
		draft := domainvault.SnippetDraft{
			Label: existing.Label, Command: existing.Command, Package: existing.Package,
			Tags: existing.Tags, Targets: existing.Targets, TargetGroups: existing.TargetGroups,
			TargetsAllHosts: existing.TargetsAllHosts, NoAutoRun: existing.NoAutoRun,
			MultiLineRunMode: existing.MultiLineRunMode,
		}
		if hasArg(arguments, "label") {
			draft.Label = stringParam(arguments, "label")
		}
		if hasArg(arguments, "content") {
			draft.Command = stringParam(arguments, "content")
		}
		applySnippetArguments(arguments, &draft)
		snippet, err := e.vault.UpdateSnippet(ctx, snippetID, draft)
		if err != nil {
			return nil, e.vaultToolError(tool, err)
		}
		result := snippetSummary(snippet)
		result["ok"] = true
		if ignored := snippetIgnoredFields(arguments); len(ignored) > 0 {
			result["ignoredFields"] = ignored
		}
		return result, nil
	case "snippets_delete":
		snippetID := stringParam(arguments, "snippetId")
		if snippetID == "" {
			return nil, appmcp.InvalidArgumentError(tool, "snippetId is required.")
		}
		if err := e.vault.DeleteSnippet(ctx, snippetID); err != nil {
			return nil, e.vaultToolError(tool, err)
		}
		return map[string]any{"ok": true, "snippetId": snippetID, "deleted": true}, nil
	case "snippets_run":
		return e.runSnippet(ctx, tool, arguments)
	}
	return nil, appmcp.ToolUnavailableError(tool, "Unsupported Snippet write.")
}

func snippetDraftFromArguments(arguments map[string]any) domainvault.SnippetDraft {
	draft := domainvault.SnippetDraft{}
	applySnippetArguments(arguments, &draft)
	return draft
}

func applySnippetArguments(arguments map[string]any, draft *domainvault.SnippetDraft) {
	if hasArg(arguments, "package") {
		draft.Package = stringParam(arguments, "package")
	}
	if hasArg(arguments, "tags") {
		draft.Tags = stringSliceArg(arguments, "tags")
	}
	if hasArg(arguments, "targets") {
		ids := stringSliceArg(arguments, "targets")
		converted := make([]model.HostID, 0, len(ids))
		for _, id := range ids {
			converted = append(converted, model.HostID(id))
		}
		draft.Targets = converted
	}
	if hasArg(arguments, "targetGroups") {
		draft.TargetGroups = stringSliceArg(arguments, "targetGroups")
	}
	if hasArg(arguments, "targetsAllHosts") {
		draft.TargetsAllHosts = boolArg(arguments, "targetsAllHosts")
	}
	if hasArg(arguments, "noAutoRun") {
		draft.NoAutoRun = boolArg(arguments, "noAutoRun")
	}
	if hasArg(arguments, "multiLineRunMode") {
		draft.MultiLineRunMode = domainvault.SnippetMultiLineRunMode(
			strings.ToLower(stringParam(arguments, "multiLineRunMode")))
	}
}

func snippetSummary(snippet domainvault.Snippet) map[string]any {
	return map[string]any{
		"id": snippet.ID, "label": snippet.Label, "package": snippet.Package,
		"tags": snippet.Tags, "targetsAllHosts": snippet.TargetsAllHosts,
		"noAutoRun": snippet.NoAutoRun, "multiLineRunMode": string(snippet.MultiLineRunMode),
	}
}

func snippetIgnoredFields(arguments map[string]any) []string {
	ignored := make([]string, 0)
	for _, key := range []string{"shortkey", "language", "description", "trigger", "triggerPattern"} {
		if hasArg(arguments, key) {
			ignored = append(ignored, key)
		}
	}
	return ignored
}

func (e *mcpWriteExecutor) findSnippet(ctx context.Context, id string) (domainvault.Snippet, error) {
	snippets, err := e.vault.ListSnippets(ctx)
	if err != nil {
		return domainvault.Snippet{}, err
	}
	for _, snippet := range snippets {
		if snippet.ID == id {
			return snippet, nil
		}
	}
	return domainvault.Snippet{}, appvault.ErrNotFound
}

func (e *mcpWriteExecutor) runSnippet(ctx context.Context, tool string,
	arguments map[string]any) (map[string]any, error) {

	snippetID := stringParam(arguments, "snippetId")
	sessionID := stringParam(arguments, "sessionId")
	if snippetID == "" || sessionID == "" {
		return nil, appmcp.InvalidArgumentError(tool, "snippetId and sessionId are required.")
	}
	values := map[string]string{}
	if rawValues := stringParam(arguments, "variables"); rawValues != "" {
		parsed, err := parseJSONObject(rawValues)
		if err != nil {
			return nil, appmcp.InvalidArgumentError(tool, "variables must be a JSON object.")
		}
		for key, value := range parsed {
			values[key] = anyString(value)
		}
	}
	run, err := e.vault.PrepareSnippet(ctx, snippetID, values)
	if err != nil {
		return nil, e.vaultToolError(tool, err)
	}
	if err := e.checkCommand(tool, sessionID, run.Command); err != nil {
		return nil, err
	}
	preview := snippetPreview(run.Snippet, run.Command)
	if preview.Empty() {
		return map[string]any{"ok": true, "snippetId": snippetID, "sessionId": sessionID}, nil
	}
	callCtx, cancel := e.bounded(ctx)
	defer cancel()
	send := e.sessions.SendChunksUntil(callCtx,
		[]model.SessionID{model.SessionID(sessionID)}, preview.Chunks,
		snippetChunkDelay(run.Snippet))
	if !send.Complete() {
		return nil, appmcp.ToolUnavailableError(tool, "The Snippet could not be sent to the Session.")
	}
	return map[string]any{
		"ok": true, "snippetId": snippetID, "sessionId": sessionID,
		"bytes": send.Bytes,
	}, nil
}

func (e *mcpWriteExecutor) executeForwarding(ctx context.Context, tool string,
	arguments map[string]any) (map[string]any, error) {

	if e.forwarding == nil {
		return nil, appmcp.ToolUnavailableError(tool, "Port forwarding is not available in this build.")
	}
	switch tool {
	case "portforward_rules_create":
		draft := domainforward.Draft{}
		if err := applyForwardDraft(arguments, &draft, true); err != nil {
			return nil, appmcp.InvalidArgumentError(tool, err.Error())
		}
		rule, err := e.forwarding.Create(ctx, draft)
		if err != nil {
			return nil, e.forwardingToolError(tool, err)
		}
		result := forwardingSummary(rule)
		result["ok"] = true
		return result, nil
	case "portforward_rules_update":
		rule, err := e.findForwardRule(ctx, stringParam(arguments, "ruleId"))
		if err != nil {
			return nil, e.forwardingToolError(tool, err)
		}
		draft := forwardDraftFromRule(rule)
		if err := applyForwardDraft(arguments, &draft, false); err != nil {
			return nil, appmcp.InvalidArgumentError(tool, err.Error())
		}
		updated, err := e.forwarding.Update(ctx, rule.ID, draft)
		if err != nil {
			return nil, e.forwardingToolError(tool, err)
		}
		result := forwardingSummary(updated)
		result["ok"] = true
		return result, nil
	case "portforward_rules_duplicate":
		rule, err := e.findForwardRule(ctx, stringParam(arguments, "ruleId"))
		if err != nil {
			return nil, e.forwardingToolError(tool, err)
		}
		draft := forwardDraftFromRule(rule)
		if strings.TrimSpace(draft.Label) != "" {
			draft.Label = strings.TrimSpace(draft.Label) + " (copy)"
		}
		duplicate, err := e.forwarding.Create(ctx, draft)
		if err != nil {
			return nil, e.forwardingToolError(tool, err)
		}
		result := forwardingSummary(duplicate)
		result["ok"] = true
		result["duplicatedFrom"] = string(rule.ID)
		return result, nil
	case "portforward_rules_delete":
		rule, err := e.findForwardRule(ctx, stringParam(arguments, "ruleId"))
		if err != nil {
			return nil, e.forwardingToolError(tool, err)
		}
		if err := e.forwarding.Delete(ctx, rule.ID); err != nil {
			return nil, e.forwardingToolError(tool, err)
		}
		return map[string]any{"ok": true, "ruleId": string(rule.ID), "deleted": true}, nil
	case "portforward_start":
		rule, err := e.findForwardRule(ctx, stringParam(arguments, "ruleId"))
		if err != nil {
			return nil, e.forwardingToolError(tool, err)
		}
		if err := e.forwarding.Start(ctx, rule.ID); err != nil {
			return nil, e.forwardingToolError(tool, err)
		}
		return e.forwardStatus(ctx, rule.ID), nil
	case "portforward_stop":
		rule, err := e.findForwardRule(ctx, stringParam(arguments, "ruleId"))
		if err != nil {
			return nil, e.forwardingToolError(tool, err)
		}
		if err := e.forwarding.Stop(ctx, rule.ID); err != nil {
			return nil, e.forwardingToolError(tool, err)
		}
		return e.forwardStatus(ctx, rule.ID), nil
	}
	return nil, appmcp.ToolUnavailableError(tool, "Unsupported Port Forwarding write.")
}

func (e *mcpWriteExecutor) findForwardRule(ctx context.Context,
	idText string) (domainforward.Rule, error) {

	if idText == "" {
		return domainforward.Rule{}, appmcp.InvalidArgumentError("portforward", "ruleId is required.")
	}
	views, err := e.forwarding.List(ctx)
	if err != nil {
		return domainforward.Rule{}, err
	}
	for _, view := range views {
		if string(view.Rule.ID) == idText {
			return view.Rule, nil
		}
	}
	return domainforward.Rule{}, fmt.Errorf("Port forwarding rule %q was not found.", idText)
}

func applyForwardDraft(arguments map[string]any, draft *domainforward.Draft, create bool) error {
	if create || hasArg(arguments, "label") {
		draft.Label = stringParam(arguments, "label")
	}
	if create || hasArg(arguments, "type") {
		draft.Type = domainforward.Type(strings.ToLower(stringParam(arguments, "type")))
	}
	if create || hasArg(arguments, "localPort") {
		port, ok := intArg(arguments, "localPort")
		if !ok {
			return errors.New("localPort must be a number.")
		}
		draft.LocalPort = port
	}
	if hasArg(arguments, "bindAddress") {
		draft.BindAddress = stringParam(arguments, "bindAddress")
	}
	if hasArg(arguments, "remoteHost") {
		draft.RemoteHost = stringParam(arguments, "remoteHost")
	}
	if hasArg(arguments, "remotePort") {
		port, ok := intArg(arguments, "remotePort")
		if !ok {
			return errors.New("remotePort must be a number.")
		}
		draft.RemotePort = port
	}
	if create || hasArg(arguments, "hostId") {
		draft.HostID = model.HostID(stringParam(arguments, "hostId"))
	}
	if hasArg(arguments, "autoStart") {
		draft.AutoStart = boolArg(arguments, "autoStart")
	}
	if hasArg(arguments, "autoReconnect") {
		value := boolArg(arguments, "autoReconnect")
		draft.AutoReconnect = &value
	}
	return nil
}

func forwardDraftFromRule(rule domainforward.Rule) domainforward.Draft {
	return domainforward.Draft{
		Label: rule.Label, Type: rule.Type, BindAddress: rule.BindAddress,
		LocalPort: rule.LocalPort, RemoteHost: rule.RemoteHost, RemotePort: rule.RemotePort,
		HostID: rule.HostID, AutoStart: rule.AutoStart, AutoReconnect: rule.AutoReconnect,
		Enabled: rule.Enabled,
	}
}

func forwardingSummary(rule domainforward.Rule) map[string]any {
	return map[string]any{
		"ruleId": string(rule.ID), "label": rule.Label, "type": string(rule.Type),
		"bindAddress": rule.BindAddress, "localPort": rule.LocalPort,
		"remoteHost": rule.RemoteHost, "remotePort": rule.RemotePort,
		"hostId": string(rule.HostID), "autoStart": rule.AutoStart,
	}
}

func (e *mcpWriteExecutor) forwardStatus(ctx context.Context, id model.ForwardingRuleID) map[string]any {
	views, err := e.forwarding.List(ctx)
	if err == nil {
		for _, view := range views {
			if view.Rule.ID == id {
				result := forwardingSummary(view.Rule)
				result["ok"] = true
				result["status"] = string(view.Status)
				return result
			}
		}
	}
	return map[string]any{"ok": true, "ruleId": string(id)}
}

func (e *mcpWriteExecutor) vaultToolError(tool string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, appvault.ErrNotFound) {
		return appmcp.InvalidArgumentError(tool, "The Vault record was not found.")
	}
	var validation *domainvault.ValidationError
	if errors.As(err, &validation) {
		return appmcp.InvalidArgumentError(tool, validation.Error())
	}
	return appmcp.ToolUnavailableError(tool, err.Error())
}

func (e *mcpWriteExecutor) forwardingToolError(tool string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return appmcp.CommandTimeoutError(tool, e.toolTimeout)
	}
	return appmcp.ToolUnavailableError(tool, err.Error())
}

func anyString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(typed)
	default:
		return ""
	}
}

func anyInt(value any) (int, bool) {
	switch typed := value.(type) {
	case float64:
		return int(typed), true
	case int:
		return typed, true
	case json.Number:
		parsed, err := typed.Int64()
		return int(parsed), err == nil
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		return parsed, err == nil
	default:
		return 0, false
	}
}

func anyBool(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		parsed, _ := strconv.ParseBool(strings.TrimSpace(typed))
		return parsed
	case float64:
		return typed != 0
	default:
		return false
	}
}

func appendUnique(target []string, values ...string) []string {
	for _, value := range values {
		found := false
		for _, existing := range target {
			if existing == value {
				found = true
				break
			}
		}
		if !found {
			target = append(target, value)
		}
	}
	return target
}

func sortStrings(values []string) {
	for index := 1; index < len(values); index++ {
		for cursor := index; cursor > 0 && values[cursor] < values[cursor-1]; cursor-- {
			values[cursor], values[cursor-1] = values[cursor-1], values[cursor]
		}
	}
}
