// Package syncvault adapts the SQLite Vault and the field-secret store to the
// sync application's Vault port. It is the only place that knows which local
// tables carry synced data and how credentials are decrypted for the payload.
package syncvault

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	appsync "github.com/mslxl/natcatty/application/sync"
	appvault "github.com/mslxl/natcatty/application/vault"
	domainforward "github.com/mslxl/natcatty/domain/forwarding"
	"github.com/mslxl/natcatty/domain/model/plugindata"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// Records is the read/write surface of *store.Store that the adapter uses.
type Records interface {
	List(ctx context.Context) ([]domainvault.Host, error)
	ListGroups(ctx context.Context) ([]domainvault.Group, error)
	ListKeys(ctx context.Context) ([]domainvault.Key, error)
	ListIdentities(ctx context.Context) ([]domainvault.Identity, error)
	ListProxyProfiles(ctx context.Context) ([]domainvault.ProxyProfile, error)
	ListNetworkProfiles(ctx context.Context) ([]domainvault.NetworkProfile, error)
	ListDataProfiles(ctx context.Context) ([]domainplugin.DataProfile, error)
	ListForwardingRules(ctx context.Context) ([]domainforward.Rule, error)
	ListSnippets(ctx context.Context) ([]domainvault.Snippet, error)
	ListSnippetPackages(ctx context.Context) ([]domainvault.SnippetPackage, error)
	ListNotes(ctx context.Context) ([]domainvault.Note, error)
	ListNoteGroups(ctx context.Context) ([]domainvault.NoteGroup, error)
	ApplySync(ctx context.Context, payload appsync.Payload) error
	// PluginDataDiagnostics reports the persisted quarantine rows this device
	// holds. The sync report must surface namespaces an import rejected, not
	// only unresolved secrets observed in memory.
	PluginDataDiagnostics(ctx context.Context) ([]plugindata.Diagnostic, error)
}

// Secrets is the field-secret surface of *store.SecretStore.
type Secrets interface {
	Mode() appvault.SecretMode
	Get(ctx context.Context, ref appvault.SecretRef) (string, error)
	Set(ctx context.Context, ref appvault.SecretRef, value string) error
	Delete(ctx context.Context, ref appvault.SecretRef) error
}

// Settings reads and applies the portable settings documents that travel in
// the payload's flat settings object. A nil implementation leaves settings
// device-local.
type Settings interface {
	Snapshot(ctx context.Context) (map[string]json.RawMessage, error)
	Apply(ctx context.Context, settings map[string]json.RawMessage) error
}

// Vault implements appsync.Vault over the local records and credentials.
type Vault struct {
	records  Records
	secrets  Secrets
	settings Settings

	pluginMu          sync.Mutex
	pluginDiagnostics []plugindata.Diagnostic
}

var (
	_ appsync.Vault              = (*Vault)(nil)
	_ appsync.PluginDataReporter = (*Vault)(nil)
)

// PluginDataDiagnostics reports the dormant provider namespaces the last
// Snapshot or Apply observed. An unresolved secret reference marks its
// namespaces dormant; the reference itself is never rewritten or emptied.
func (v *Vault) PluginDataDiagnostics() []plugindata.Diagnostic {
	v.pluginMu.Lock()
	defer v.pluginMu.Unlock()
	return append([]plugindata.Diagnostic(nil), v.pluginDiagnostics...)
}

func (v *Vault) setPluginDataDiagnostics(diagnostics []plugindata.Diagnostic) {
	v.pluginMu.Lock()
	v.pluginDiagnostics = append([]plugindata.Diagnostic(nil), diagnostics...)
	v.pluginMu.Unlock()
}

// Option customizes a Vault adapter.
type Option func(*Vault)

// WithSettings adds the portable settings document set to the payload.
func WithSettings(settings Settings) Option {
	return func(v *Vault) { v.settings = settings }
}

// New returns a Vault adapter.
func New(records Records, secrets Secrets, opts ...Option) *Vault {
	vault := &Vault{records: records, secrets: secrets}
	for _, opt := range opts {
		opt(vault)
	}
	return vault
}

// Snapshot reads every synced family plus the plaintext credentials the
// envelope must carry. A credential that cannot be decrypted fails the
// snapshot: uploading a silently incomplete Vault would let another device
// wipe the missing fields on its next download.
func (v *Vault) Snapshot(ctx context.Context) (appsync.Payload, error) {
	var payload appsync.Payload
	var newest int64
	add := func(when int64) {
		if when > newest {
			newest = when
		}
	}

	var err error
	var diagnostics []plugindata.Diagnostic
	if payload.Hosts, err = v.records.List(ctx); err != nil {
		return appsync.Payload{}, err
	}
	// A connection is this device's telemetry, not a Vault fact: the
	// original's sanitizeHostsForSync strips it before upload so a peer's
	// clock never reorders another device's Recently Connected list. Copy the
	// slice first; a repository is free to return its own backing array.
	hosts := make([]domainvault.Host, len(payload.Hosts))
	copy(hosts, payload.Hosts)
	for index := range hosts {
		hosts[index].LastConnectedAt = 0
	}
	payload.Hosts = hosts
	for index := range payload.Hosts {
		host := &payload.Hosts[index]
		add(host.UpdatedAt.UnixMilli())
		if host.HasPassword {
			if err := v.addSecret(ctx, &payload, appvault.HostSecretRef(host.ID, appvault.FieldPassword)); err != nil {
				return appsync.Payload{}, err
			}
		}
		if host.Proxy != nil && host.Proxy.HasPassword {
			if err := v.addSecret(ctx, &payload, appvault.HostSecretRef(host.ID, appvault.FieldProxyPassword)); err != nil {
				return appsync.Payload{}, err
			}
		}
	}
	if payload.Groups, err = v.records.ListGroups(ctx); err != nil {
		return appsync.Payload{}, err
	}
	for _, group := range payload.Groups {
		add(group.UpdatedAt.UnixMilli())
	}
	if payload.Keys, err = v.records.ListKeys(ctx); err != nil {
		return appsync.Payload{}, err
	}
	for _, key := range payload.Keys {
		add(key.UpdatedAt.UnixMilli())
		if key.HasPrivateKey {
			if err := v.addSecret(ctx, &payload, appvault.KeySecretRef(key.ID, appvault.FieldPrivateKey)); err != nil {
				return appsync.Payload{}, err
			}
		}
		if key.HasPassphrase {
			if err := v.addSecret(ctx, &payload, appvault.KeySecretRef(key.ID, appvault.FieldPassphrase)); err != nil {
				return appsync.Payload{}, err
			}
		}
	}
	if payload.Identities, err = v.records.ListIdentities(ctx); err != nil {
		return appsync.Payload{}, err
	}
	for _, identity := range payload.Identities {
		add(identity.UpdatedAt.UnixMilli())
		if identity.HasPassword {
			if err := v.addSecret(ctx, &payload, appvault.IdentitySecretRef(identity.ID, appvault.FieldPassword)); err != nil {
				return appsync.Payload{}, err
			}
		}
	}
	if payload.ProxyProfiles, err = v.records.ListProxyProfiles(ctx); err != nil {
		return appsync.Payload{}, err
	}
	for _, profile := range payload.ProxyProfiles {
		add(profile.UpdatedAt.UnixMilli())
		if profile.Config.HasPassword {
			if err := v.addSecret(ctx, &payload, appvault.ProxyProfileSecretRef(profile.ID, appvault.FieldProxyPassword)); err != nil {
				return appsync.Payload{}, err
			}
		}
	}
	if payload.NetworkProfiles, err = v.records.ListNetworkProfiles(ctx); err != nil {
		return appsync.Payload{}, err
	}
	if payload.NetworkProfiles == nil {
		// "This writer has no Network Profiles" must be encoded as [] rather
		// than a missing key: the merge reads nil as a payload written before
		// the family existed and must not treat it as a deletion.
		payload.NetworkProfiles = []domainvault.NetworkProfile{}
	}
	for _, profile := range payload.NetworkProfiles {
		add(profile.UpdatedAt.UnixMilli())
		if err := v.addNetworkSecrets(ctx, &payload, profile); err != nil {
			return appsync.Payload{}, err
		}
	}
	if payload.DataProfiles, err = v.records.ListDataProfiles(ctx); err != nil {
		return appsync.Payload{}, err
	}
	for _, profile := range payload.DataProfiles {
		add(profile.UpdatedAt.UnixMilli())
		var unresolved []string
		for field, raw := range profile.SecretRefs {
			ref, parseErr := appvault.ParseDataProfileSecretRef(raw)
			if parseErr != nil {
				unresolved = append(unresolved, field)
				continue
			}
			value, readErr := v.secrets.Get(ctx, ref)
			if errors.Is(readErr, appvault.ErrSecretNotFound) {
				unresolved = append(unresolved, field)
				continue
			}
			if readErr != nil {
				return appsync.Payload{}, fmt.Errorf(
					"syncvault: read data profile secret %s: %w", field, readErr)
			}
			payload.Secrets = append(payload.Secrets, appsync.SecretValue{
				EntityType: ref.EntityType, EntityID: ref.EntityID,
				Field: ref.Field, Value: value,
			})
		}
		diagnostics = append(diagnostics, unresolvedProfileDiagnostics(profile, unresolved)...)
	}
	if payload.PortForwardingRules, err = v.records.ListForwardingRules(ctx); err != nil {
		return appsync.Payload{}, err
	}
	for _, rule := range payload.PortForwardingRules {
		add(rule.UpdatedAt.UnixMilli())
	}
	if payload.Snippets, err = v.records.ListSnippets(ctx); err != nil {
		return appsync.Payload{}, err
	}
	for _, snippet := range payload.Snippets {
		add(snippet.UpdatedAt.UnixMilli())
	}
	if payload.SnippetPackages, err = v.records.ListSnippetPackages(ctx); err != nil {
		return appsync.Payload{}, err
	}
	for _, pkg := range payload.SnippetPackages {
		add(pkg.UpdatedAt.UnixMilli())
	}
	if payload.Notes, err = v.records.ListNotes(ctx); err != nil {
		return appsync.Payload{}, err
	}
	for _, note := range payload.Notes {
		add(note.UpdatedAt.UnixMilli())
	}
	if payload.NoteGroups, err = v.records.ListNoteGroups(ctx); err != nil {
		return appsync.Payload{}, err
	}
	for _, group := range payload.NoteGroups {
		add(group.UpdatedAt.UnixMilli())
	}
	if err := decorateGroupReferences(&payload); err != nil {
		return appsync.Payload{}, err
	}
	if v.settings != nil {
		settings, err := v.settings.Snapshot(ctx)
		if err != nil {
			return appsync.Payload{}, err
		}
		payload.Settings = settings
	}
	payload.SyncedAt = newest
	diagnostics, err = v.withStoredDiagnostics(ctx, diagnostics)
	if err != nil {
		return appsync.Payload{}, err
	}
	v.setPluginDataDiagnostics(diagnostics)
	return payload, nil
}

// addNetworkSecrets appends the secret material a Network Profile advertises
// through its Has* markers. The profile record itself never carries plaintext:
// the Auth Key, Private Key, and each peer's Preshared Key are addressed by
// their field-secret entity and field names.
func (v *Vault) addNetworkSecrets(ctx context.Context, payload *appsync.Payload, profile domainvault.NetworkProfile) error {
	for _, secret := range networkSecretRefs(profile) {
		if !secret.advertised {
			continue
		}
		if err := v.addSecret(ctx, payload, secret.ref); err != nil {
			return err
		}
	}
	return nil
}

// networkSecretRef is one credential address a Network Profile can own.
// advertised mirrors the record's Has* marker; clearing visits the address
// regardless because a stale marker must not strand ciphertext.
type networkSecretRef struct {
	ref        appvault.SecretRef
	advertised bool
}

func networkSecretRefs(profile domainvault.NetworkProfile) []networkSecretRef {
	var refs []networkSecretRef
	if profile.Tailscale != nil {
		refs = append(refs, networkSecretRef{
			ref:        appvault.NetworkProfileSecretRef(profile.ID, appvault.FieldNetworkAuthKey),
			advertised: profile.Tailscale.HasAuthKey,
		})
	}
	if profile.WireGuard == nil {
		return refs
	}
	refs = append(refs, networkSecretRef{
		ref:        appvault.NetworkProfileSecretRef(profile.ID, appvault.FieldNetworkPrivateKey),
		advertised: profile.WireGuard.HasPrivateKey,
	})
	for _, peer := range profile.WireGuard.Peers {
		refs = append(refs, networkSecretRef{
			ref:        appvault.NetworkPeerSecretRef(peer.ID, appvault.FieldNetworkPresharedKey),
			advertised: peer.HasPresharedKey,
		})
	}
	return refs
}

// withStoredDiagnostics appends the quarantine rows persisted by the Store.
// The sync report must include namespaces an earlier import quarantined even
// when no secret is unresolved, and an apply must report the rows it just
// wrote.
func (v *Vault) withStoredDiagnostics(ctx context.Context,
	diagnostics []plugindata.Diagnostic) ([]plugindata.Diagnostic, error) {

	stored, err := v.records.PluginDataDiagnostics(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]plugindata.Diagnostic, 0, len(diagnostics)+len(stored))
	out = append(out, diagnostics...)
	out = append(out, stored...)
	return out, nil
}

// unresolvedProfileDiagnostics marks every namespace of a profile dormant when
// one of its secret references cannot be resolved. A profile with no namespaces
// still reports the record itself so the sync report cannot hide the loss.
func unresolvedProfileDiagnostics(profile domainplugin.DataProfile,
	unresolved []string) []plugindata.Diagnostic {

	if len(unresolved) == 0 {
		return nil
	}
	sort.Strings(unresolved)
	detail := plugindata.BoundText(strings.Join(unresolved, ","),
		plugindata.MaxDiagnosticDetailBytes)
	if len(profile.PluginData) == 0 {
		pluginID, _ := domainplugin.PluginIDFromDataProviderID(profile.Provider)
		return []plugindata.Diagnostic{{
			RecordKind: plugindata.RecordDataProfile, RecordID: profile.ID,
			PluginID: plugindata.BoundText(pluginID, plugindata.MaxDiagnosticPluginIDBytes),
			State:    plugindata.StateDormant, Reason: plugindata.ReasonUnresolvedSecret,
			Detail: detail,
		}}
	}
	pluginIDs := make([]string, 0, len(profile.PluginData))
	for pluginID := range profile.PluginData {
		pluginIDs = append(pluginIDs, pluginID)
	}
	sort.Strings(pluginIDs)
	diagnostics := make([]plugindata.Diagnostic, 0, len(pluginIDs))
	for _, pluginID := range pluginIDs {
		namespace := profile.PluginData[pluginID]
		diagnostics = append(diagnostics, plugindata.Diagnostic{
			RecordKind: plugindata.RecordDataProfile, RecordID: profile.ID,
			PluginID:      plugindata.BoundText(pluginID, plugindata.MaxDiagnosticPluginIDBytes),
			SchemaVersion: namespace.SchemaVersion,
			Fields:        len(namespace.Fields), State: plugindata.StateDormant,
			Reason: plugindata.ReasonUnresolvedSecret, Detail: detail,
		})
	}
	return diagnostics
}

func (v *Vault) addSecret(ctx context.Context, payload *appsync.Payload, ref appvault.SecretRef) error {
	value, err := v.secrets.Get(ctx, ref)
	if errors.Is(err, appvault.ErrSecretNotFound) {
		// The entity advertises a credential that is no longer stored. Omit it
		// rather than failing; the local record's flag is stale.
		return nil
	}
	if err != nil {
		return fmt.Errorf("syncvault: read %s %s: %w", ref.EntityType, ref.Field, err)
	}
	payload.Secrets = append(payload.Secrets, appsync.SecretValue{
		EntityType: ref.EntityType,
		EntityID:   ref.EntityID,
		Field:      ref.Field,
		Value:      value,
	})
	return nil
}

// Apply replaces the synced records, then writes the payload's credentials
// under this device's field key and removes stale credentials for entities the
// payload still contains. Local-only tables and the local key material are
// untouched.
func (v *Vault) Apply(ctx context.Context, payload appsync.Payload) error {
	mode := v.secrets.Mode()
	if mode == appvault.SecretModeUnavailable || mode == appvault.SecretModePassphraseLocked {
		return fmt.Errorf("syncvault: cannot apply credentials in %s mode", mode)
	}
	// Network Profile secrets are addressed by profile and peer id, and a peer
	// is nested inside a record that the apply replaces wholesale. Capture the
	// local profiles first so a profile or peer the payload drops still has its
	// ciphertext removed instead of becoming an orphan.
	previousNetworkProfiles, err := v.records.ListNetworkProfiles(ctx)
	if err != nil {
		return fmt.Errorf("syncvault: list local Network Profiles: %w", err)
	}
	// A nil family means the payload predates Network Profiles. Its writer
	// cannot express a deletion, so keep the local records and credentials
	// instead of replacing them with an empty family.
	preserveNetworkSecrets := payload.NetworkProfiles == nil
	if preserveNetworkSecrets {
		payload.NetworkProfiles = previousNetworkProfiles
	}
	// Data Profile secrets are addressed by an opaque reference stored inside
	// each record, so capture the local records before the apply replaces them.
	previousDataProfiles, err := v.records.ListDataProfiles(ctx)
	if err != nil {
		return err
	}
	if err := v.records.ApplySync(ctx, payload); err != nil {
		return err
	}
	if v.settings != nil && len(payload.Settings) > 0 {
		if err := v.settings.Apply(ctx, payload.Settings); err != nil {
			return err
		}
	}
	byRef := make(map[appvault.SecretRef]bool, len(payload.Secrets))
	for _, secret := range payload.Secrets {
		ref := appvault.SecretRef{EntityType: secret.EntityType, EntityID: secret.EntityID, Field: secret.Field}
		if err := v.secrets.Set(ctx, ref, secret.Value); err != nil {
			return fmt.Errorf("syncvault: store %s %s: %w", ref.EntityType, ref.Field, err)
		}
		byRef[ref] = true
	}
	var diagnostics []plugindata.Diagnostic
	for _, profile := range payload.DataProfiles {
		var unresolved []string
		for field, raw := range profile.SecretRefs {
			ref, parseErr := appvault.ParseDataProfileSecretRef(raw)
			if parseErr != nil {
				unresolved = append(unresolved, field)
				continue
			}
			if byRef[ref] {
				continue
			}
			if _, err := v.secrets.Get(ctx, ref); errors.Is(err, appvault.ErrSecretNotFound) {
				unresolved = append(unresolved, field)
			} else if err != nil {
				return fmt.Errorf("syncvault: verify data profile secret %s: %w", field, err)
			}
		}
		diagnostics = append(diagnostics, unresolvedProfileDiagnostics(profile, unresolved)...)
	}
	if err := v.clearStale(ctx, payload, byRef, previousNetworkProfiles, preserveNetworkSecrets, previousDataProfiles); err != nil {
		return err
	}
	diagnostics, err = v.withStoredDiagnostics(ctx, diagnostics)
	if err != nil {
		return err
	}
	v.setPluginDataDiagnostics(diagnostics)
	return nil
}

// clearStale deletes credential fields that an incoming record no longer
// advertises, plus every Network Profile credential the apply removed unless
// the payload predates the family. Orphan rows for other entities absent from
// the payload are left for a later cleanup: they are ciphertext and cannot be
// addressed without listing the secret store.
func (v *Vault) clearStale(ctx context.Context, payload appsync.Payload,
	byRef map[appvault.SecretRef]bool,
	previousNetworkProfiles []domainvault.NetworkProfile, preserveNetworkSecrets bool,
	previousDataProfiles []domainplugin.DataProfile) error {
	clear := func(ref appvault.SecretRef) error {
		if byRef[ref] {
			return nil
		}
		err := v.secrets.Delete(ctx, ref)
		if errors.Is(err, appvault.ErrSecretNotFound) {
			return nil
		}
		return err
	}
	for _, host := range payload.Hosts {
		if err := clear(appvault.HostSecretRef(host.ID, appvault.FieldPassword)); err != nil {
			return err
		}
		if err := clear(appvault.HostSecretRef(host.ID, appvault.FieldProxyPassword)); err != nil {
			return err
		}
	}
	for _, key := range payload.Keys {
		if err := clear(appvault.KeySecretRef(key.ID, appvault.FieldPrivateKey)); err != nil {
			return err
		}
		if err := clear(appvault.KeySecretRef(key.ID, appvault.FieldPassphrase)); err != nil {
			return err
		}
	}
	for _, identity := range payload.Identities {
		if err := clear(appvault.IdentitySecretRef(identity.ID, appvault.FieldPassword)); err != nil {
			return err
		}
	}
	for _, profile := range payload.ProxyProfiles {
		if err := clear(appvault.ProxyProfileSecretRef(profile.ID, appvault.FieldProxyPassword)); err != nil {
			return err
		}
	}
	// A Network Profile that survived still needs any credential the payload
	// did not carry removed, and a profile or peer that disappeared needs all
	// of its ciphertext removed. previousNetworkProfiles is authoritative for
	// the addresses that existed before the apply. A payload that predates the
	// family says nothing about these credentials, so keep them.
	if !preserveNetworkSecrets {
		for _, profile := range previousNetworkProfiles {
			for _, secret := range networkSecretRefs(profile) {
				if err := clear(secret.ref); err != nil {
					return err
				}
			}
		}
	}
	// A data profile still advertises every reference it wants retained. A
	// reference the incoming record dropped is stale; a reference it kept is
	// never deleted merely because this device cannot resolve it.
	advertised := make(map[string]map[string]bool, len(payload.DataProfiles))
	for _, profile := range payload.DataProfiles {
		refs := make(map[string]bool, len(profile.SecretRefs))
		for _, raw := range profile.SecretRefs {
			refs[raw] = true
		}
		advertised[profile.ID] = refs
	}
	for _, previous := range previousDataProfiles {
		retained := advertised[previous.ID]
		for _, raw := range previous.SecretRefs {
			if retained[raw] {
				continue
			}
			ref, err := appvault.ParseDataProfileSecretRef(raw)
			if err != nil {
				continue
			}
			if err := clear(ref); err != nil {
				return err
			}
		}
	}
	return nil
}

// decorateGroupReferences makes the portable payload internally consistent:
// every group has a stable ID, every implicit ancestor exists as a node, and
// Host and Snippet references carry both the stable ID and the current path.
// Local callers still use paths; convergent sync uses IDs so a move on another
// device carries its members instead of looking like a delete.
func decorateGroupReferences(payload *appsync.Payload) error {
	groups := domainvault.EnsureGroupIdentities(payload.Groups)
	referenced := make([]string, 0, len(payload.Hosts)+len(payload.Snippets))
	for _, host := range payload.Hosts {
		referenced = append(referenced, host.Group)
	}
	for _, snippet := range payload.Snippets {
		referenced = append(referenced, snippet.TargetGroups...)
	}
	groups = domainvault.EnsureGroupAncestors(groups, referenced)
	payload.Groups = groups

	pathIDs := make(map[string]string, len(groups))
	idPaths := make(map[string]string, len(groups))
	for _, group := range groups {
		pathIDs[group.Path] = group.ID
		idPaths[group.ID] = group.Path
	}
	for index := range payload.Hosts {
		host := &payload.Hosts[index]
		host.Group = domainvault.NormalizeGroup(host.Group)
		if host.Group == "" {
			host.GroupID = ""
			continue
		}
		if host.GroupID != "" {
			if path, ok := idPaths[host.GroupID]; ok {
				host.Group = path
				continue
			}
		}
		if id, ok := pathIDs[host.Group]; ok {
			host.GroupID = id
		} else {
			host.GroupID = ""
		}
	}
	for index := range payload.Snippets {
		snippet := &payload.Snippets[index]
		paths := make([]string, 0, len(snippet.TargetGroups)+len(snippet.TargetGroupIDs))
		for _, path := range snippet.TargetGroups {
			paths = append(paths, path)
		}
		if len(paths) == 0 {
			for _, id := range snippet.TargetGroupIDs {
				if path, ok := idPaths[id]; ok {
					paths = append(paths, path)
				}
			}
		}
		ids := make([]string, 0, len(paths))
		targets := make([]string, 0, len(paths))
		seen := make(map[string]bool, len(paths))
		for _, path := range paths {
			path = domainvault.NormalizeGroup(path)
			if path == "" {
				continue
			}
			id, ok := pathIDs[path]
			if !ok {
				id = domainvault.GroupIDFromPath(path)
			}
			if seen[id] {
				continue
			}
			seen[id] = true
			ids = append(ids, id)
			if canonical, ok := idPaths[id]; ok {
				targets = append(targets, canonical)
			} else {
				targets = append(targets, path)
			}
		}
		snippet.TargetGroupIDs = ids
		snippet.TargetGroups = targets
	}
	return nil
}
