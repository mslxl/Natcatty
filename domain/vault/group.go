package vault

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mslxl/natcatty/domain/model"
	protocolmodel "github.com/mslxl/natcatty/domain/model/protocol"
)

// Group is one Host Group: a user-defined grouping of Hosts with shared
// connection defaults. Identity is the stable ID and the tree position is
// (ParentID, Name); Path is derived from the tree when a list or payload is
// materialized, so a rename or a move never rewrites a member. Hosts and
// Snippets reference the group by ID and by path respectively, matching the
// convergent payload and the portable file format.
type Group struct {
	// ID is the sync identity. It is generated once and survives renames and
	// moves. Path-derived IDs are used only for records written before IDs
	// existed, so those records still merge with their old counterparts.
	ID string `json:"id,omitempty"`
	// ParentID is the containing group's ID, or "" at the Vault root. The
	// pair (ParentID, Name) is the movable-tree structure; Path is derived
	// from it when a payload is materialized.
	ParentID string `json:"parentId,omitempty"`
	// Name is the last path segment. It is what a move or rename changes.
	Name  string `json:"name,omitempty"`
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
	// NetworkProfileID is the group's default Network Profile. A member Host's
	// own reference wins; with neither, the Host has no Network Route.
	NetworkProfileID string    `json:"networkProfileId,omitempty"`
	OrderKey         int64     `json:"orderKey"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
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
	NetworkProfileID string
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
	draft.NetworkProfileID = strings.TrimSpace(draft.NetworkProfileID)
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
	if draft.Protocol != "" {
		descriptor, ok := protocolmodel.Lookup(protocolmodel.ID(draft.Protocol))
		if !ok || !descriptor.GroupDefault {
			return GroupDraft{}, validation(FieldProtocol, RuleUnsupported)
		}
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
	if err := validateText(FieldNetworkProfile, draft.NetworkProfileID, maxLabelLength, false); err != nil {
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
		NetworkProfileID: g.NetworkProfileID,
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
	NetworkProfileID string
}

// ResolveGroupDefaults merges the group configs along a Host Group path. The
// path only locates the group; the chain follows the tree's ParentID links, so
// a rename or a move never changes which defaults a Host sees. A path with no
// saved group yields a zero value.
func ResolveGroupDefaults(groups []Group, path string) GroupDefaults {
	groups = EnsureGroupIdentities(groups)
	group, ok := GroupByPath(groups, path)
	if !ok {
		return GroupDefaults{}
	}
	return ResolveGroupDefaultsByID(groups, group.ID)
}

// ResolveGroupDefaultsByID merges the defaults of the group and its ancestors,
// root first, so a child overrides its parent. A list whose records predate
// stable IDs is upgraded first so the chain still resolves.
func ResolveGroupDefaultsByID(groups []Group, id string) GroupDefaults {
	groups = EnsureGroupIdentities(groups)
	var merged GroupDefaults
	for _, group := range GroupChainByID(groups, id) {
		mergeGroupDefaults(&merged, group.Defaults())
	}
	return merged
}

// mergeGroupDefaults layers one group's defaults onto the merged set.
func mergeGroupDefaults(merged *GroupDefaults, defaults GroupDefaults) {
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
	if defaults.NetworkProfileID != "" {
		merged.NetworkProfileID = defaults.NetworkProfileID
	}
}

// GroupByID returns one saved group.
func GroupByID(groups []Group, id string) (Group, bool) {
	if id == "" {
		return Group{}, false
	}
	for _, group := range groups {
		if group.ID == id {
			return group, true
		}
	}
	return Group{}, false
}

// GroupByPath returns the group whose derived path is path.
func GroupByPath(groups []Group, path string) (Group, bool) {
	normalized := NormalizeGroup(path)
	if normalized == "" {
		return Group{}, false
	}
	for _, group := range groups {
		if NormalizeGroup(group.Path) == normalized {
			return group, true
		}
	}
	return Group{}, false
}

// GroupChainByID returns the group and its ancestors, root first. A missing
// parent or a cycle ends the chain instead of looping.
func GroupChainByID(groups []Group, id string) []Group {
	byID := make(map[string]Group, len(groups))
	for _, group := range groups {
		if group.ID != "" {
			byID[group.ID] = group
		}
	}
	chain := make([]Group, 0, len(byID))
	seen := make(map[string]bool, len(byID))
	for id != "" && !seen[id] {
		seen[id] = true
		group, ok := byID[id]
		if !ok {
			break
		}
		chain = append(chain, group)
		id = group.ParentID
	}
	for left, right := 0, len(chain)-1; left < right; left, right = left+1, right-1 {
		chain[left], chain[right] = chain[right], chain[left]
	}
	return chain
}

// MaterializeGroupPaths derives every group's Path from the tree its ID and
// ParentID links describe. Storage keeps identity and structure only; a path is
// never a stored field. A legacy record without an ID gets the deterministic
// path identity so an old payload still materializes.
func MaterializeGroupPaths(groups []Group) []Group {
	// A legacy list carries paths but no IDs or parent links; derive them
	// first so the tree rebuild does not mistake a nested path for a root.
	out := EnsureGroupIdentities(groups)
	nodes := make([]GroupTreeNode, 0, len(out))
	for index := range out {
		if out[index].Name == "" {
			out[index].Name = GroupName(out[index].Path)
		}
		nodes = append(nodes, GroupTreeNode{
			ID: out[index].ID, ParentID: out[index].ParentID,
			Name: out[index].Name, OrderKey: out[index].OrderKey,
		})
	}
	paths := ResolveGroupTree(nodes)
	for index := range out {
		out[index].Path = paths[out[index].ID]
	}
	return out
}

// MaterializeHostGroup fills a Host's derived group path from the tree. A
// Host whose GroupID names no group is at the Vault root; a legacy record with
// no ID keeps the path it was stored with.
func MaterializeHostGroup(host Host, groups []Group) Host {
	if host.GroupID == "" {
		return host
	}
	host.Group = ""
	for _, group := range groups {
		if group.ID == host.GroupID {
			host.Group = group.Path
			return host
		}
	}
	return host
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
	if effective.NetworkProfileID == "" {
		effective.NetworkProfileID = defaults.NetworkProfileID
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

// GroupIDFromPath returns the deterministic identity used for a Host Group
// written before stable IDs existed. The same path always produces the same
// ID, so an old path-keyed record and a new ID-keyed record describing the
// same group merge instead of duplicating.
func GroupIDFromPath(path string) string {
	path = NormalizeGroup(path)
	if path == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("natcatty-group\x00" + path))
	return "group-" + hex.EncodeToString(sum[:16])
}

// EnsureGroupIdentities fills the sync identity fields on records loaded from
// storage that predates them. A saved ID and ParentID are preserved; only
// missing values are derived. The returned slice is a copy.
func EnsureGroupIdentities(groups []Group) []Group {
	out := make([]Group, len(groups))
	pathIDs := make(map[string]string, len(groups))
	for index, group := range groups {
		group.Path = NormalizeGroup(group.Path)
		if group.ID == "" {
			group.ID = GroupIDFromPath(group.Path)
		}
		if group.Name == "" {
			group.Name = GroupName(group.Path)
		}
		out[index] = group
		if group.Path != "" {
			pathIDs[group.Path] = group.ID
		}
	}
	for index := range out {
		if out[index].Path == "" {
			continue
		}
		parentPath := ParentGroupPath(out[index].Path)
		if parentPath == "" {
			out[index].ParentID = ""
			continue
		}
		if out[index].ParentID != "" {
			continue
		}
		if id, ok := pathIDs[parentPath]; ok {
			out[index].ParentID = id
		} else {
			out[index].ParentID = GroupIDFromPath(parentPath)
		}
	}
	return out
}

// EnsureGroupAncestors adds a minimal record for every implicit ancestor of a
// saved group or a Host/Snippet reference. A Host may name "prod/web" while
// only "prod/web" has a saved record; the tree still needs "prod" as a node,
// otherwise a payload materializer would treat "prod/web" as a root group.
// Existing records are never replaced.
func EnsureGroupAncestors(groups []Group, referencedPaths []string) []Group {
	out := EnsureGroupIdentities(groups)
	byPath := make(map[string]Group, len(out))
	needed := map[string]bool{}
	for _, group := range out {
		if group.Path != "" {
			byPath[group.Path] = group
			for _, ancestor := range GroupAncestors(group.Path) {
				needed[ancestor] = true
			}
		}
	}
	for _, path := range referencedPaths {
		path = NormalizeGroup(path)
		if path == "" {
			continue
		}
		for _, ancestor := range GroupAncestors(path) {
			needed[ancestor] = true
		}
	}
	pathIDs := make(map[string]string, len(needed))
	for path, group := range byPath {
		pathIDs[path] = group.ID
	}
	for path := range needed {
		if pathIDs[path] != "" {
			continue
		}
		pathIDs[path] = GroupIDFromPath(path)
	}

	missing := make([]string, 0, len(needed))
	for path := range needed {
		if _, ok := byPath[path]; !ok {
			missing = append(missing, path)
		}
	}
	sort.Strings(missing)
	for _, path := range missing {
		parentPath := ParentGroupPath(path)
		parentID := ""
		if parentPath != "" {
			parentID = pathIDs[parentPath]
		}
		out = append(out, Group{
			ID:       pathIDs[path],
			ParentID: parentID,
			Name:     GroupName(path),
			Path:     path,
		})
	}
	return out
}

// GroupTreeNode is the structural part of a Host Group used to materialize
// paths. Display text and connection defaults are deliberately absent: only
// identity, parent, name, and sibling order affect the tree.
type GroupTreeNode struct {
	ID       string
	ParentID string
	Name     string
	OrderKey int64
}

// ResolveGroupTree materializes each node's slash-separated path from the
// parent links. It is deterministic for every replica:
//
//   - a parent that does not exist, or a self-link, makes the node a root;
//   - every cycle is broken by making the lexicographically smallest node in
//     that cycle a root, so two devices that concurrently move groups into a
//     cycle still derive the same forest;
//   - duplicate sibling names get " (2)", " (3)" suffixes in
//     (OrderKey, ID) order, so a concurrent create cannot produce two
//     indistinguishable paths.
func ResolveGroupTree(nodes []GroupTreeNode) map[string]string {
	byID := make(map[string]GroupTreeNode, len(nodes))
	for _, node := range nodes {
		if node.ID == "" {
			continue
		}
		if existing, ok := byID[node.ID]; ok {
			// Two records with one ID are already a protocol error. Keeping the
			// first is deterministic and lets the caller report the state
			// separately.
			if existing.Name == "" && node.Name != "" {
				byID[node.ID] = node
			}
			continue
		}
		byID[node.ID] = node
	}

	parent := make(map[string]string, len(byID))
	for id := range byID {
		candidate := byID[id].ParentID
		if candidate == "" || candidate == id {
			parent[id] = ""
			continue
		}
		if _, ok := byID[candidate]; !ok {
			parent[id] = ""
			continue
		}
		parent[id] = candidate
	}
	repairGroupCycles(parent)

	children := make(map[string][]string, len(byID)+1)
	for id := range byID {
		children[parent[id]] = append(children[parent[id]], id)
	}
	for _, ids := range children {
		sort.Slice(ids, func(left, right int) bool {
			leftNode, rightNode := byID[ids[left]], byID[ids[right]]
			if leftNode.OrderKey != rightNode.OrderKey {
				return leftNode.OrderKey < rightNode.OrderKey
			}
			return leftNode.ID < rightNode.ID
		})
	}

	names := make(map[string]string, len(byID))
	for _, ids := range children {
		used := make(map[string]bool, len(ids))
		for _, id := range ids {
			name := sanitizeGroupSegment(byID[id].Name)
			if name == "" {
				name = sanitizeGroupSegment(id)
			}
			if name == "" {
				name = "group"
			}
			candidate := name
			for suffix := 2; used[strings.ToLower(candidate)]; suffix++ {
				candidate = name + " (" + strconv.Itoa(suffix) + ")"
			}
			used[strings.ToLower(candidate)] = true
			names[id] = candidate
		}
	}

	paths := make(map[string]string, len(byID))
	var resolve func(string) string
	resolve = func(id string) string {
		if path, ok := paths[id]; ok {
			return path
		}
		name := names[id]
		path := name
		if p := parent[id]; p != "" {
			path = resolve(p) + "/" + name
		}
		paths[id] = path
		return path
	}
	for id := range byID {
		resolve(id)
	}
	return paths
}

func repairGroupCycles(parent map[string]string) {
	for {
		cycle := firstGroupCycle(parent)
		if len(cycle) == 0 {
			return
		}
		root := cycle[0]
		for _, id := range cycle[1:] {
			if id < root {
				root = id
			}
		}
		parent[root] = ""
	}
}

func firstGroupCycle(parent map[string]string) []string {
	ids := make([]string, 0, len(parent))
	for id := range parent {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, start := range ids {
		seen := make(map[string]int, len(parent))
		path := make([]string, 0, len(parent))
		for id := start; id != ""; id = parent[id] {
			if at, ok := seen[id]; ok {
				return append([]string(nil), path[at:]...)
			}
			seen[id] = len(path)
			path = append(path, id)
		}
	}
	return nil
}

func sanitizeGroupSegment(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "/", "-")
	name = strings.Trim(name, "/")
	var out strings.Builder
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			continue
		}
		out.WriteRune(r)
	}
	return strings.TrimSpace(out.String())
}
