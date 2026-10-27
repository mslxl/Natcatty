package sync

import (
	"encoding/json"

	domainforward "github.com/mslxl/natcatty/domain/forwarding"
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
	Hosts           []domainvault.Host           `json:"hosts"`
	Keys            []domainvault.Key            `json:"keys"`
	Identities      []domainvault.Identity       `json:"identities"`
	ProxyProfiles   []domainvault.ProxyProfile   `json:"proxyProfiles"`
	Snippets        []domainvault.Snippet        `json:"snippets"`
	Groups          []domainvault.Group          `json:"groups"`
	SnippetPackages []domainvault.SnippetPackage `json:"snippetPackages"`
	Notes           []domainvault.Note           `json:"notes"`
	NoteGroups      []domainvault.NoteGroup      `json:"noteGroups"`
	// PortForwardingRules follows notes in the original's field order.
	PortForwardingRules []domainforward.Rule `json:"portForwardingRules"`
	// Settings is the original payload's flat settings object. This build
	// treats app preferences as device-local and never publishes them (spec
	// item 111), but the field is carried so a payload written by another
	// client merges with the original's deep-merge rules instead of losing
	// unknown keys.
	Settings map[string]json.RawMessage `json:"settings,omitempty"`
	Secrets  []SecretValue              `json:"secrets,omitempty"`
	// SyncMeta carries durable deletion tombstones, matching the original
	// syncReliability metadata. It lets a device distinguish "deleted" from
	// "this client did not send the entity" when no base anchor exists.
	SyncMeta *SyncMeta `json:"syncMeta,omitempty"`
	// SyncedAt is the newest content timestamp.
	SyncedAt int64 `json:"syncedAt"`
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
		len(p.Groups) > 0 ||
		len(p.PortForwardingRules) > 0 ||
		len(p.Snippets) > 0 ||
		len(p.SnippetPackages) > 0 ||
		len(p.Notes) > 0 ||
		len(p.NoteGroups) > 0
}
