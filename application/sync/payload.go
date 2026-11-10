package sync

import (
	"encoding/json"

	domainforward "github.com/mslxl/natcatty/domain/forwarding"
	"github.com/mslxl/natcatty/domain/model/plugindata"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// Payload is the plaintext carried inside netcatty-vault.json. The envelope is
// byte-for-byte compatible with the original app. This build's payload uses the
// original's top-level family names where the data model matches, in the
// original's order; Host Groups are this build's richer "groups" records, not
// the original's customGroups string list. Timestamps are RFC 3339 through the
// domain types.
//
// Secrets travel in a separate array because this build stores field
// credentials outside the entity records (ADR-0010). The array is inside the
// ciphertext, so the provider never sees plaintext; the applying device
// re-encrypts each value under its own field key.
type Payload struct {
	Hosts         []domainvault.Host         `json:"hosts"`
	Keys          []domainvault.Key          `json:"keys"`
	Identities    []domainvault.Identity     `json:"identities"`
	ProxyProfiles []domainvault.ProxyProfile `json:"proxyProfiles"`
	// NetworkProfiles carries the profile records, including WireGuard peers
	// and the Has* credential markers. Secret material still travels in Secrets
	// under the network_profile and network_peer entity types.
	//
	// A nil slice means the payload was written before this family existed; an
	// empty non-nil slice means the writer intentionally has none. Merge treats
	// nil as "no opinion" so a pre-family peer cannot delete local profiles,
	// while [] propagates a deletion. Writers must always set a non-nil slice.
	NetworkProfiles []domainvault.NetworkProfile `json:"networkProfiles"`
	// DataProfiles carries provider-owned profile records. The record's
	// pluginData map travels verbatim and secret fields stay references; their
	// plaintext travels in Secrets like every other credential.
	DataProfiles    []domainplugin.DataProfile   `json:"dataProfiles,omitempty"`
	Snippets        []domainvault.Snippet        `json:"snippets"`
	Groups          []domainvault.Group          `json:"groups"`
	SnippetPackages []domainvault.SnippetPackage `json:"snippetPackages"`
	Notes           []domainvault.Note           `json:"notes"`
	NoteGroups      []domainvault.NoteGroup      `json:"noteGroups"`
	// PortForwardingRules follows notes in the original's field order.
	PortForwardingRules []domainforward.Rule `json:"portForwardingRules"`
	// Settings is the original payload's flat settings object. This build
	// publishes only portable settings under namespaced keys and keeps
	// device-local fields out; unknown keys from another client are carried so
	// the original's deep-merge rules don't lose them. In v2 the whole object
	// is an object-shaped CRDT family, one entity per key.
	Settings map[string]json.RawMessage `json:"settings,omitempty"`
	Secrets  []SecretValue              `json:"secrets,omitempty"`
	// ConvergentSync is the v2 CRDT envelope. It is present only when the
	// plaintext metadata advertises syncSchemaVersion 2; a v1 payload must
	// never carry it. The complete materialized v1 fields stay beside it so an
	// older client can still read the Vault.
	ConvergentSync json.RawMessage `json:"convergentSync,omitempty"`
	// SyncMeta carries durable deletion tombstones, matching the original
	// syncReliability metadata. It lets a device distinguish "deleted" from
	// "this client did not send the entity" when no base anchor exists.
	SyncMeta *SyncMeta `json:"syncMeta,omitempty"`
	// SyncedAt is the newest content timestamp.
	SyncedAt int64 `json:"syncedAt"`

	// PluginDataQuarantines carries namespaces that decoding this payload
	// rejected. It is host-local diagnostic material: it is never encoded into
	// the payload, never signed, and is written to the local quarantine table
	// when the payload is applied so the sync report can surface it.
	PluginDataQuarantines []RejectedPluginData `json:"-"`
}

// RejectedPluginData is one namespace or record-level pluginData container an
// incoming payload could not accept. Quarantine carries the structural reason
// and bounded raw diagnostic bytes; it is never merged back automatically.
type RejectedPluginData struct {
	Kind       plugindata.RecordKind
	RecordID   string
	Quarantine plugindata.Quarantine
}

// SyncMeta is the original's durable sync metadata. Only the parts this build
// reads and writes are modeled: a schema version, the generation time, the
// device that generated it, and the deletion tombstones.
type SyncMeta struct {
	SchemaVersion int            `json:"schemaVersion"`
	GeneratedAt   int64          `json:"generatedAt"`
	DeviceID      string         `json:"deviceId,omitempty"`
	Deletions     []SyncDeletion `json:"deletions,omitempty"`
}

// SyncDeletion is one tombstone. EntityType uses this build's payload family
// name and ID is the entity's ID (or Path for path-identified families).
type SyncDeletion struct {
	EntityType string `json:"entityType"`
	ID         string `json:"id"`
	DeletedAt  int64  `json:"deletedAt"`
	DeviceID   string `json:"deviceId,omitempty"`
}

// HasData reports whether any synced family has a record. Known Hosts are
// deliberately absent, matching the original's cloud payload.
func (p Payload) HasData() bool {
	return len(p.Hosts) > 0 ||
		len(p.Keys) > 0 ||
		len(p.Identities) > 0 ||
		len(p.ProxyProfiles) > 0 ||
		len(p.NetworkProfiles) > 0 ||
		len(p.DataProfiles) > 0 ||
		len(p.Groups) > 0 ||
		len(p.PortForwardingRules) > 0 ||
		len(p.Snippets) > 0 ||
		len(p.SnippetPackages) > 0 ||
		len(p.Notes) > 0 ||
		len(p.NoteGroups) > 0
}
