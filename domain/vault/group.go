package vault

import (
	"strings"
	"time"

	"github.com/mslxl/natcatty/domain/model"
)

// Group is one Host Group: a user-defined grouping of Hosts with shared
// connection defaults. Its identity is its slash-separated path, so a rename
// is a path change and every reference is a path, not an id. A Host may keep a
// Group path that no longer exists, matching the original Vault's
// non-cascading references.
type Group struct {
	Path  string `json:"path"`
	Notes string `json:"notes,omitempty"`
	// The fields below are defaults for member Hosts. A zero value means the
	// group does not set that default. A Host's own value wins.
	Username         string              `json:"username,omitempty"`
	Port             int                 `json:"port,omitempty"`
	Protocol         Protocol            `json:"protocol,omitempty"`
	AuthMethod       HostAuthMethod      `json:"authMethod,omitempty"`
	IdentityID       model.IdentityID    `json:"identityId,omitempty"`
	KeyID            model.KeyID         `json:"keyId,omitempty"`
	UseSSHAgent      bool                `json:"useSshAgent,omitempty"`
	RequiresMFA      bool                `json:"requiresMfa,omitempty"`
	LegacyAlgorithms bool                `json:"legacyAlgorithms,omitempty"`
	SkipEcdsaHostKey bool                `json:"skipEcdsaHostKey,omitempty"`
	Algorithms       *AlgorithmOverrides `json:"algorithms,omitempty"`
	ProxyProfileID   string              `json:"proxyProfileId,omitempty"`
	OrderKey         int64               `json:"orderKey"`
	CreatedAt        time.Time           `json:"createdAt"`
	UpdatedAt        time.Time           `json:"updatedAt"`
}

// GroupDraft is the editable part of a Host Group.
type GroupDraft struct {
	Path             string
	Notes            string
	Username         string
	Port             int
	Protocol         Protocol
	AuthMethod       HostAuthMethod
	IdentityID       model.IdentityID
	KeyID            model.KeyID
	UseSSHAgent      bool
	RequiresMFA      bool
	LegacyAlgorithms bool
	SkipEcdsaHostKey bool
	Algorithms       *AlgorithmOverrides
	ProxyProfileID   string
}

// NormalizeGroupDraft trims and canonicalizes a Host Group's input and returns
// a *ValidationError when it cannot be saved.
func NormalizeGroupDraft(draft GroupDraft) (GroupDraft, error) {
	draft.Path = NormalizeGroup(draft.Path)
	draft.Notes = strings.TrimSpace(draft.Notes)
	draft.Username = strings.TrimSpace(draft.Username)
	draft.Protocol = Protocol(strings.ToLower(strings.TrimSpace(string(draft.Protocol))))
	draft.AuthMethod = HostAuthMethod(strings.ToLower(strings.TrimSpace(string(draft.AuthMethod))))
	draft.IdentityID = model.IdentityID(strings.TrimSpace(string(draft.IdentityID)))
	draft.KeyID = model.KeyID(strings.TrimSpace(string(draft.KeyID)))
	draft.ProxyProfileID = strings.TrimSpace(draft.ProxyProfileID)
	draft.Algorithms = normalizeAlgorithms(draft.Algorithms)
	// A blank protocol inherits from the parent group, so only an explicit
	// non-SSH protocol drops defaults its members would never read.
	if draft.Protocol != "" && !draft.Protocol.UsesSSHAuth() {
		draft.IdentityID = ""
		draft.KeyID = ""
		draft.AuthMethod = ""
		draft.UseSSHAgent = false
		draft.RequiresMFA = false
	}

	if draft.Path == "" {
		return GroupDraft{}, validation(FieldGroup, RuleRequired)
	}
	if err := validateText(FieldGroup, draft.Path, maxGroupLength, false); err != nil {
		return GroupDraft{}, err
	}
	for _, segment := range strings.Split(draft.Path, "/") {
		if err := validateText(FieldGroup, segment, maxLabelLength, false); err != nil {
			return GroupDraft{}, err
		}
	}
	if len(draft.Notes) > maxNotesLength {
		return GroupDraft{}, validation(FieldNotes, RuleTooLong)
	}
	if err := validateText(FieldUsername, draft.Username, maxUsernameLength, false); err != nil {
		return GroupDraft{}, err
	}
	switch draft.Protocol {
	case "", ProtocolSSH, ProtocolTelnet:
	default:
		return GroupDraft{}, validation(FieldProtocol, RuleUnsupported)
	}
	switch draft.AuthMethod {
	case "", HostAuthAuto, HostAuthPassword, HostAuthKey, HostAuthCertificate:
	default:
		return GroupDraft{}, validation(FieldAuthMethod, RuleUnsupported)
	}
	if draft.Port < 0 || draft.Port > 65535 {
		return GroupDraft{}, validation(FieldPort, RuleOutOfRange)
	}
	if err := validateText(FieldProxy, draft.ProxyProfileID, maxLabelLength, false); err != nil {
		return GroupDraft{}, err
	}
	return draft, nil
}

// Defaults returns the group's own defaults, without inheriting ancestors.
func (g Group) Defaults() GroupDefaults {
	return GroupDefaults{
		Username:         g.Username,
		Port:             g.Port,
		Protocol:         g.Protocol,
		AuthMethod:       g.AuthMethod,
		IdentityID:       g.IdentityID,
		KeyID:            g.KeyID,
		UseSSHAgent:      g.UseSSHAgent,
		RequiresMFA:      g.RequiresMFA,
		LegacyAlgorithms: g.LegacyAlgorithms,
		SkipEcdsaHostKey: g.SkipEcdsaHostKey,
		Algorithms:       CloneAlgorithms(g.Algorithms),
		ProxyProfileID:   g.ProxyProfileID,
	}
}

// GroupDefaults is the merged, effective default set for one Host Group path.
// A zero value means the chain of groups does not set that default.
type GroupDefaults struct {
	Username         string
	Port             int
	Protocol         Protocol
	AuthMethod       HostAuthMethod
	IdentityID       model.IdentityID
	KeyID            model.KeyID
	UseSSHAgent      bool
	RequiresMFA      bool
	LegacyAlgorithms bool
	SkipEcdsaHostKey bool
	Algorithms       *AlgorithmOverrides
	ProxyProfileID   string
}

// ResolveGroupDefaults merges the group configs along a Host Group's ancestor
// chain. For "A/B/C" it applies A, then A/B, then A/B/C, so a child overrides
// its parent. A group path with no saved group config yields a zero value.
func ResolveGroupDefaults(groups []Group, path string) GroupDefaults {
	byPath := make(map[string]Group, len(groups))
	for _, group := range groups {
		byPath[group.Path] = group
	}

	var merged GroupDefaults
	for _, ancestor := range GroupAncestors(path) {
		group, ok := byPath[ancestor]
		if !ok {
			continue
		}
		defaults := group.Defaults()
		// SSH credentials are one bundle, as in the original Vault: a group
		// that selects an Identity replaces inherited manual credentials, and
		// a group that sets manual credentials replaces an inherited Identity.
		// Mixing an Identity with a lower level's username would resolve a
		// different method than either group intended.
		switch {
		case defaults.IdentityID != "":
			merged.Username = ""
			merged.AuthMethod = ""
			merged.KeyID = ""
		case defaults.Username != "" || defaults.AuthMethod != "" || defaults.KeyID != "":
			replacedIdentity := merged.IdentityID != ""
			merged.IdentityID = ""
			if replacedIdentity {
				merged.Username = ""
				merged.AuthMethod = ""
				merged.KeyID = ""
			}
		}
		if defaults.Username != "" {
			merged.Username = defaults.Username
		}
		if defaults.Port != 0 {
			merged.Port = defaults.Port
		}
		if defaults.Protocol != "" {
			merged.Protocol = defaults.Protocol
		}
		if defaults.AuthMethod != "" {
			merged.AuthMethod = defaults.AuthMethod
		}
		if defaults.IdentityID != "" {
			merged.IdentityID = defaults.IdentityID
		}
		if defaults.KeyID != "" {
			merged.KeyID = defaults.KeyID
		}
		if defaults.UseSSHAgent {
			merged.UseSSHAgent = true
		}
		if defaults.RequiresMFA {
			merged.RequiresMFA = true
		}
		if defaults.LegacyAlgorithms {
			merged.LegacyAlgorithms = true
		}
		if defaults.SkipEcdsaHostKey {
			merged.SkipEcdsaHostKey = true
		}
		if defaults.Algorithms != nil {
			merged.Algorithms = CloneAlgorithms(defaults.Algorithms)
		}
		if defaults.ProxyProfileID != "" {
			merged.ProxyProfileID = defaults.ProxyProfileID
		}
	}
	return merged
}

// ApplyGroupDefaults fills a Host's unset connection settings from its group's
// effective defaults. The Host's own value wins, as in the original Vault.
// Local shells never inherit remote credentials or SSH transport settings.
func ApplyGroupDefaults(host Host, defaults GroupDefaults) Host {
	effective := host
	effective.Tags = append([]string(nil), host.Tags...)
	effective.HostChain = CloneHostChain(host.HostChain)
	effective.Proxy = CloneProxy(host.Proxy)
	effective.Algorithms = CloneAlgorithms(host.Algorithms)

	if effective.Protocol == ProtocolLocal {
		return effective
	}
	// A Host that already owns an Identity must not gain another level's
	// username or key, and a Host that owns manual credentials must not gain
	// the group's Identity. Either would resolve to a credential bundle the
	// user did not choose.
	hostHasManualCredentials := effective.Username != "" || effective.KeyID != "" ||
		(effective.AuthMethod != "" && effective.AuthMethod != HostAuthAuto)
	skipCredentialBundle := effective.IdentityID != "" ||
		(defaults.IdentityID != "" && hostHasManualCredentials)
	if !skipCredentialBundle {
		if effective.Username == "" {
			effective.Username = defaults.Username
		}
		if effective.AuthMethod == "" || effective.AuthMethod == HostAuthAuto {
			if defaults.AuthMethod != "" {
				effective.AuthMethod = defaults.AuthMethod
			}
		}
		if effective.IdentityID == "" {
			effective.IdentityID = defaults.IdentityID
		}
		if effective.KeyID == "" {
			effective.KeyID = defaults.KeyID
		}
	}
	if effective.Port == 0 {
		effective.Port = defaults.Port
	}
	if effective.Protocol == "" {
		effective.Protocol = defaults.Protocol
	}
	if !effective.UseSSHAgent {
		effective.UseSSHAgent = defaults.UseSSHAgent
	}
	if !effective.RequiresMFA {
		effective.RequiresMFA = defaults.RequiresMFA
	}
	if !effective.LegacyAlgorithms {
		effective.LegacyAlgorithms = defaults.LegacyAlgorithms
	}
	if !effective.SkipEcdsaHostKey {
		effective.SkipEcdsaHostKey = defaults.SkipEcdsaHostKey
	}
	if effective.Algorithms == nil {
		effective.Algorithms = CloneAlgorithms(defaults.Algorithms)
	}
	if effective.ProxyProfileID == "" {
		effective.ProxyProfileID = defaults.ProxyProfileID
	}
	return effective
}

// GroupAncestors returns the path itself and its ancestors, root first:
// "A/B/C" yields ["A", "A/B", "A/B/C"]. An empty path yields nothing.
func GroupAncestors(path string) []string {
	path = NormalizeGroup(path)
	if path == "" {
		return nil
	}
	segments := strings.Split(path, "/")
	ancestors := make([]string, 0, len(segments))
	for i := range segments {
		ancestors = append(ancestors, strings.Join(segments[:i+1], "/"))
	}
	return ancestors
}

// ParentGroupPath returns the group that contains path, or "" at the root.
// "A/B/C" yields "A/B"; "A" and "" yield "".
func ParentGroupPath(path string) string {
	path = NormalizeGroup(path)
	if path == "" {
		return ""
	}
	index := strings.LastIndex(path, "/")
	if index < 0 {
		return ""
	}
	return path[:index]
}

// GroupName returns the last segment of a Host Group path.
func GroupName(path string) string {
	segments := strings.Split(NormalizeGroup(path), "/")
	return segments[len(segments)-1]
}

// IsGroupAtOrBelow reports whether path is root or a descendant of root.
func IsGroupAtOrBelow(path, root string) bool {
	path, root = NormalizeGroup(path), NormalizeGroup(root)
	if root == "" {
		return true
	}
	return path == root || strings.HasPrefix(path, root+"/")
}

// ReplaceGroupPrefix rewrites path when it is root or below root, replacing
// the root prefix with next. Other paths are returned unchanged.
func ReplaceGroupPrefix(path, root, next string) string {
	path, root, next = NormalizeGroup(path), NormalizeGroup(root), NormalizeGroup(next)
	switch {
	case path == root:
		return next
	case strings.HasPrefix(path, root+"/"):
		if next == "" {
			return strings.TrimPrefix(path, root+"/")
		}
		return next + path[len(root):]
	default:
		return path
	}
}
