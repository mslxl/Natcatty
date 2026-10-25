package sync

import (
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
	Secrets             []SecretValue        `json:"secrets,omitempty"`
	// SyncedAt is the newest content timestamp. The newest-wins decision uses
	// it against the remote envelope's updatedAt.
	SyncedAt int64 `json:"syncedAt"`
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
