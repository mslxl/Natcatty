package vault

import (
	"context"
	"errors"

	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// GroupRepository persists Host Groups. A group is addressed by its stable ID;
// its path is derived from the tree and is not a storage key. Renaming or
// moving a group therefore never rewrites the Hosts that belong to it.
type GroupRepository interface {
	ListGroups(ctx context.Context) ([]domainvault.Group, error)
	GetGroup(ctx context.Context, id string) (domainvault.Group, error)
	SaveGroup(ctx context.Context, group domainvault.Group) error
	DeleteGroup(ctx context.Context, id string) error
}

// WithGroups gives the Service Host Group persistence and group defaults.
func WithGroups(repo GroupRepository) Option {
	return func(s *Service) { s.groups = repo }
}

// ListGroups returns every Host Group with its derived path, in display order.
func (s *Service) ListGroups(ctx context.Context) ([]domainvault.Group, error) {
	if s.groups == nil {
		return nil, unsupported(ErrGroupsUnavailable)
	}
	groups, err := s.groups.ListGroups(ctx)
	if err != nil {
		return nil, internal(err)
	}
	return domainvault.MaterializeGroupPaths(groups), nil
}

// CreateGroup validates and persists a Host Group. The draft names the desired
// path; the path's missing ancestors are created so the tree is always
// well-formed.
func (s *Service) CreateGroup(ctx context.Context, draft domainvault.GroupDraft) (domainvault.Group, error) {
	if s.groups == nil {
		return domainvault.Group{}, unsupported(ErrGroupsUnavailable)
	}
	normalized, err := domainvault.NormalizeGroupDraft(draft)
	if err != nil {
		return domainvault.Group{}, invalid(err)
	}
	groups, err := s.ListGroups(ctx)
	if err != nil {
		return domainvault.Group{}, err
	}
	if _, exists := domainvault.GroupByPath(groups, normalized.Path); exists {
		return domainvault.Group{}, conflict(nil)
	}
	parentID, err := s.ensureGroupPath(ctx, &groups, domainvault.ParentGroupPath(normalized.Path))
	if err != nil {
		return domainvault.Group{}, err
	}
	now := s.now()
	group := groupFromDraft(domainvault.Group{
		ID:        s.newGroupID(),
		ParentID:  parentID,
		Name:      domainvault.GroupName(normalized.Path),
		Path:      normalized.Path,
		OrderKey:  nextGroupOrder(groups) + orderStep,
		CreatedAt: now,
		UpdatedAt: now,
	}, normalized)
	if err := s.groups.SaveGroup(ctx, group); err != nil {
		return domainvault.Group{}, internal(err)
	}
	return group, nil
}

// UpdateGroup edits the Host Group with the given stable ID. A changed path
// moves the group in the tree (renaming it and/or changing its parent) by
// updating this record alone: member Hosts keep the ID and follow the move,
// and descendant groups derive their new paths from the tree.
func (s *Service) UpdateGroup(ctx context.Context, id string, draft domainvault.GroupDraft) (domainvault.Group, error) {
	if s.groups == nil {
		return domainvault.Group{}, unsupported(ErrGroupsUnavailable)
	}
	if id == "" {
		return domainvault.Group{}, invalid(&domainvault.ValidationError{
			Field: domainvault.FieldGroup, Rule: domainvault.RuleRequired,
		})
	}
	normalized, err := domainvault.NormalizeGroupDraft(draft)
	if err != nil {
		return domainvault.Group{}, invalid(err)
	}
	groups, err := s.ListGroups(ctx)
	if err != nil {
		return domainvault.Group{}, err
	}
	existing, found := domainvault.GroupByID(groups, id)
	if !found {
		return domainvault.Group{}, ErrNotFound
	}
	oldPath := existing.Path
	if normalized.Path != oldPath {
		if domainvault.IsGroupAtOrBelow(normalized.Path, oldPath) {
			return domainvault.Group{}, invalid(&domainvault.ValidationError{
				Field: domainvault.FieldGroup, Rule: domainvault.RuleInvalid,
			})
		}
		if other, exists := domainvault.GroupByPath(groups, normalized.Path); exists && other.ID != id {
			return domainvault.Group{}, conflict(nil)
		}
	}
	parentID, err := s.ensureGroupPath(ctx, &groups, domainvault.ParentGroupPath(normalized.Path))
	if err != nil {
		return domainvault.Group{}, err
	}

	now := s.now()
	updated := groupFromDraft(existing, normalized)
	updated.Name = domainvault.GroupName(normalized.Path)
	updated.ParentID = parentID
	updated.Path = normalized.Path
	if updated.CreatedAt.IsZero() {
		updated.CreatedAt = now
	}
	updated.UpdatedAt = now
	if err := s.groups.SaveGroup(ctx, updated); err != nil {
		return domainvault.Group{}, internal(err)
	}
	// Snippets still address groups by path, so their target lists follow a
	// rename. Hosts do not: their membership is the stable ID.
	if normalized.Path != oldPath {
		if err := s.remapSnippetTargetGroups(ctx, oldPath, normalized.Path); err != nil {
			return domainvault.Group{}, err
		}
	}
	return updated, nil
}

// DeleteGroup removes a Host Group and its descendant groups. Member Hosts
// survive and move to the Vault root, matching the original Vault's default
// non-cascading group delete.
func (s *Service) DeleteGroup(ctx context.Context, id string) error {
	if s.groups == nil {
		return unsupported(ErrGroupsUnavailable)
	}
	groups, err := s.ListGroups(ctx)
	if err != nil {
		return err
	}
	root, found := domainvault.GroupByID(groups, id)
	if !found {
		return ErrNotFound
	}
	removed := make(map[string]bool)
	for _, group := range groups {
		if domainvault.IsGroupAtOrBelow(group.Path, root.Path) {
			removed[group.ID] = true
		}
	}
	hosts, err := s.repo.List(ctx)
	if err != nil {
		return internal(err)
	}
	for _, host := range hosts {
		if !removed[host.GroupID] {
			continue
		}
		host.GroupID = ""
		host.Group = ""
		host.UpdatedAt = s.now()
		if err := s.repo.Save(ctx, host); err != nil {
			return internal(err)
		}
	}
	for _, group := range groups {
		if !removed[group.ID] {
			continue
		}
		if err := s.groups.DeleteGroup(ctx, group.ID); err != nil && !errors.Is(err, ErrNotFound) {
			return internal(err)
		}
	}
	// Snippet target group references are cleared, never deleted with the
	// group; the Snippets stay in the Vault.
	if err := s.removeSnippetTargetGroups(ctx, root.Path); err != nil {
		return err
	}
	return nil
}

// AssignHostGroup moves one Host into a Host Group addressed by its stable ID,
// or to the Vault root when groupID is empty. The Host's other fields,
// including credentials, are preserved.
func (s *Service) AssignHostGroup(ctx context.Context, hostID model.HostID, groupID string) error {
	if s.groups == nil {
		return unsupported(ErrGroupsUnavailable)
	}
	if groupID != "" {
		groups, err := s.ListGroups(ctx)
		if err != nil {
			return err
		}
		if _, found := domainvault.GroupByID(groups, groupID); !found {
			return invalid(&domainvault.ValidationError{
				Field: domainvault.FieldGroup, Rule: domainvault.RuleInvalid,
			})
		}
	}
	host, err := s.repo.Get(ctx, hostID)
	if errors.Is(err, ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return internal(err)
	}
	host.GroupID = groupID
	host.Group = ""
	host.UpdatedAt = s.now()
	if err := s.repo.Save(ctx, host); err != nil {
		return internal(err)
	}
	return nil
}

// EffectiveHost returns a Host with its group defaults applied and its Proxy
// Profile materialized. The stored Host is unchanged; the connection path uses
// this resolved view.
func (s *Service) EffectiveHost(ctx context.Context, id model.HostID) (domainvault.Host, error) {
	host, err := s.repo.Get(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return domainvault.Host{}, ErrNotFound
	}
	if err != nil {
		return domainvault.Host{}, internal(err)
	}
	return s.resolveEffective(ctx, host)
}

// resolveEffective applies group defaults, the protocol's port fallback, and a
// selected Proxy Profile to a stored Host. The stored record itself is
// unchanged.
func (s *Service) resolveEffective(ctx context.Context, host domainvault.Host) (domainvault.Host, error) {
	effective := host
	if s.groups != nil {
		groups, err := s.ListGroups(ctx)
		if err != nil {
			return domainvault.Host{}, err
		}
		effective = domainvault.MaterializeHostGroup(effective, groups)
		// A record written before stable IDs kept only its path; resolve it by
		// path so an upgrade never drops the defaults it inherited.
		defaults := domainvault.GroupDefaults{}
		if host.GroupID != "" {
			defaults = domainvault.ResolveGroupDefaultsByID(groups, host.GroupID)
		} else if legacyPath := host.Group; legacyPath != "" {
			defaults = domainvault.ResolveGroupDefaults(groups, legacyPath)
		}
		effective = domainvault.ApplyGroupDefaults(effective, defaults)
	}
	// A Host that leaves Port empty inherits its group's port; without one the
	// protocol default is what the connection would use anyway, so resolve it
	// here and every caller sees the same endpoint.
	effective.Port = domainvault.EffectivePort(effective.Protocol, effective.Port)
	if effective.Proxy == nil && effective.ProxyProfileID != "" && s.proxies != nil {
		profile, err := s.proxies.GetProxyProfile(ctx, effective.ProxyProfileID)
		switch {
		case err == nil:
			effective.Proxy = domainvault.CloneProxy(&profile.Config)
			if profile.Config.HasPassword {
				password, secretErr := s.secretOrEmpty(ctx,
					ProxyProfileSecretRef(profile.ID, FieldProxyPassword))
				if secretErr != nil {
					return domainvault.Host{}, secretErr
				}
				if password != "" {
					effective.Proxy.Password = password
				}
			}
		case errors.Is(err, ErrNotFound):
			// A dangling profile reference stays dangling, matching the
			// Vault's non-cascading delete behavior.
		default:
			return domainvault.Host{}, internal(err)
		}
	}
	return effective, nil
}

// GroupDefaults returns the merged defaults that apply to a Host Group path.
func (s *Service) GroupDefaults(ctx context.Context, path string) (domainvault.GroupDefaults, error) {
	if s.groups == nil {
		return domainvault.GroupDefaults{}, nil
	}
	groups, err := s.ListGroups(ctx)
	if err != nil {
		return domainvault.GroupDefaults{}, err
	}
	return domainvault.ResolveGroupDefaults(groups, path), nil
}

// ensureGroupPath returns the ID of the group at path, creating the path and
// any missing ancestor so every level of the tree has a record. An empty path
// is the Vault root and has no identity.
func (s *Service) ensureGroupPath(ctx context.Context, groups *[]domainvault.Group, path string) (string, error) {
	path = domainvault.NormalizeGroup(path)
	if path == "" {
		return "", nil
	}
	byPath := indexGroupsByPath(*groups)
	if id := byPath[path]; id != "" {
		return id, nil
	}
	now := s.now()
	order := nextGroupOrder(*groups)
	var parentID string
	for _, ancestor := range domainvault.GroupAncestors(path) {
		if id := byPath[ancestor]; id != "" {
			parentID = id
			continue
		}
		order += orderStep
		group := domainvault.Group{
			ID: s.newGroupID(), ParentID: parentID,
			Name: domainvault.GroupName(ancestor), Path: ancestor,
			OrderKey: order, CreatedAt: now, UpdatedAt: now,
		}
		if err := s.groups.SaveGroup(ctx, group); err != nil {
			return "", internal(err)
		}
		*groups = append(*groups, group)
		byPath[ancestor] = group.ID
		parentID = group.ID
	}
	return byPath[path], nil
}

// resolveHostGroup turns a Host draft's group path into the stable ID a Host
// stores, creating the group (and its ancestors) when the path is new.
func (s *Service) resolveHostGroup(ctx context.Context, path string) (string, error) {
	path = domainvault.NormalizeGroup(path)
	if path == "" {
		return "", nil
	}
	if s.groups == nil {
		return "", unsupported(ErrGroupsUnavailable)
	}
	groups, err := s.ListGroups(ctx)
	if err != nil {
		return "", err
	}
	return s.ensureGroupPath(ctx, &groups, path)
}

func indexGroupsByPath(groups []domainvault.Group) map[string]string {
	byPath := make(map[string]string, len(groups))
	for _, group := range groups {
		byPath[domainvault.NormalizeGroup(group.Path)] = group.ID
	}
	return byPath
}

func groupFromDraft(group domainvault.Group, draft domainvault.GroupDraft) domainvault.Group {
	group.Path = draft.Path
	group.Notes = draft.Notes
	group.Username = draft.Username
	group.Port = draft.Port
	group.Protocol = draft.Protocol
	group.AuthMethod = draft.AuthMethod
	group.IdentityID = draft.IdentityID
	group.KeyID = draft.KeyID
	group.UseSSHAgent = draft.UseSSHAgent
	group.RequiresMFA = draft.RequiresMFA
	group.LegacyAlgorithms = draft.LegacyAlgorithms
	group.SkipEcdsaHostKey = draft.SkipEcdsaHostKey
	group.Algorithms = domainvault.CloneAlgorithms(draft.Algorithms)
	group.ProxyProfileID = draft.ProxyProfileID
	group.NetworkProfileID = draft.NetworkProfileID
	return group
}

func nextGroupOrder(groups []domainvault.Group) int64 {
	var max int64
	for _, group := range groups {
		if group.OrderKey > max {
			max = group.OrderKey
		}
	}
	return max
}

// ErrGroupsUnavailable reports a Service built without Host Group persistence.
var ErrGroupsUnavailable = errors.New("vault: Host Groups are unavailable")
