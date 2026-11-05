package vault

import (
	"context"
	"errors"

	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// GroupRepository persists Host Groups. Path remains the storage key and the
// value Hosts and Snippets reference, while ID is the stable identity
// convergent sync uses so a rename or move keeps one record.
type GroupRepository interface {
	ListGroups(ctx context.Context) ([]domainvault.Group, error)
	GetGroup(ctx context.Context, path string) (domainvault.Group, error)
	SaveGroup(ctx context.Context, group domainvault.Group) error
	DeleteGroup(ctx context.Context, path string) error
}

// WithGroups gives the Service Host Group persistence and group defaults.
func WithGroups(repo GroupRepository) Option {
	return func(s *Service) { s.groups = repo }
}

// ListGroups returns every Host Group in display order.
func (s *Service) ListGroups(ctx context.Context) ([]domainvault.Group, error) {
	if s.groups == nil {
		return nil, unsupported(ErrGroupsUnavailable)
	}
	groups, err := s.groups.ListGroups(ctx)
	if err != nil {
		return nil, internal(err)
	}
	return groups, nil
}

// CreateGroup validates and persists a Host Group.
func (s *Service) CreateGroup(ctx context.Context, draft domainvault.GroupDraft) (domainvault.Group, error) {
	if s.groups == nil {
		return domainvault.Group{}, unsupported(ErrGroupsUnavailable)
	}
	normalized, err := domainvault.NormalizeGroupDraft(draft)
	if err != nil {
		return domainvault.Group{}, invalid(err)
	}
	groups, err := s.groups.ListGroups(ctx)
	if err != nil {
		return domainvault.Group{}, internal(err)
	}
	groups = domainvault.EnsureGroupIdentities(groups)
	for _, group := range groups {
		if group.Path == normalized.Path {
			return domainvault.Group{}, conflict(nil)
		}
	}
	now := s.now()
	group := groupFromDraft(domainvault.Group{
		ID:        s.newGroupID(),
		ParentID:  groupIdentityForPath(groups, domainvault.ParentGroupPath(normalized.Path)),
		Name:      domainvault.GroupName(normalized.Path),
		Path:      normalized.Path,
		OrderKey:  nextGroupOrder(groups),
		CreatedAt: now,
		UpdatedAt: now,
	}, normalized)
	if err := s.groups.SaveGroup(ctx, group); err != nil {
		return domainvault.Group{}, internal(err)
	}
	return group, nil
}

// UpdateGroup edits a Host Group. A changed Path renames the group and every
// descendant group and Host reference below it. Deleting a group is separate:
// UpdateGroup never removes Hosts.
func (s *Service) UpdateGroup(ctx context.Context, path string, draft domainvault.GroupDraft) (domainvault.Group, error) {
	if s.groups == nil {
		return domainvault.Group{}, unsupported(ErrGroupsUnavailable)
	}
	oldPath := domainvault.NormalizeGroup(path)
	if oldPath == "" {
		return domainvault.Group{}, invalid(&domainvault.ValidationError{
			Field: domainvault.FieldGroup, Rule: domainvault.RuleRequired,
		})
	}
	normalized, err := domainvault.NormalizeGroupDraft(draft)
	if err != nil {
		return domainvault.Group{}, invalid(err)
	}
	if normalized.Path != oldPath && domainvault.IsGroupAtOrBelow(normalized.Path, oldPath) {
		return domainvault.Group{}, invalid(&domainvault.ValidationError{
			Field: domainvault.FieldGroup, Rule: domainvault.RuleInvalid,
		})
	}

	groups, err := s.groups.ListGroups(ctx)
	if err != nil {
		return domainvault.Group{}, internal(err)
	}
	groups = domainvault.EnsureGroupIdentities(groups)
	existing, found := findGroup(groups, oldPath)
	if !found {
		// An implicit group exists only while a Host references its path.
		hosts, listErr := s.repo.List(ctx)
		if listErr != nil {
			return domainvault.Group{}, internal(listErr)
		}
		if !hostsReferenceGroup(hosts, oldPath) {
			return domainvault.Group{}, ErrNotFound
		}
	}
	if normalized.Path != oldPath {
		for _, group := range groups {
			if group.Path == normalized.Path {
				return domainvault.Group{}, conflict(nil)
			}
		}
		if err := s.renameHosts(ctx, oldPath, normalized.Path); err != nil {
			return domainvault.Group{}, err
		}
		if err := s.renameDescendantGroups(ctx, groups, oldPath, normalized.Path); err != nil {
			return domainvault.Group{}, err
		}
		// Snippet target group paths follow the rename, the same as Host
		// references do.
		if err := s.remapSnippetTargetGroups(ctx, oldPath, normalized.Path); err != nil {
			return domainvault.Group{}, err
		}
	}

	now := s.now()
	updated := groupFromDraft(existing, normalized)
	// A path that only a Host referenced has no persisted identity yet. Give
	// it the deterministic path identity so another device that upgrades the
	// same implicit group produces the same ID.
	if updated.ID == "" {
		updated.ID = domainvault.GroupIDFromPath(oldPath)
	}
	updated.Name = domainvault.GroupName(normalized.Path)
	updated.ParentID = groupIdentityForPath(groups, domainvault.ParentGroupPath(normalized.Path))
	if updated.CreatedAt.IsZero() {
		updated.CreatedAt = now
	}
	updated.UpdatedAt = now
	if normalized.Path != oldPath && updated.OrderKey == 0 {
		updated.OrderKey = nextGroupOrder(groups)
	}
	if err := s.groups.SaveGroup(ctx, updated); err != nil {
		return domainvault.Group{}, internal(err)
	}
	if normalized.Path != oldPath {
		if err := s.groups.DeleteGroup(ctx, oldPath); err != nil && !errors.Is(err, ErrNotFound) {
			return domainvault.Group{}, internal(err)
		}
	}
	return updated, nil
}

// DeleteGroup removes a Host Group and its descendant groups. Member Hosts
// survive and move to the Vault root, matching the original Vault's default
// non-cascading group delete.
func (s *Service) DeleteGroup(ctx context.Context, path string) error {
	if s.groups == nil {
		return unsupported(ErrGroupsUnavailable)
	}
	root := domainvault.NormalizeGroup(path)
	if root == "" {
		return invalid(&domainvault.ValidationError{
			Field: domainvault.FieldGroup, Rule: domainvault.RuleRequired,
		})
	}
	groups, err := s.groups.ListGroups(ctx)
	if err != nil {
		return internal(err)
	}
	hosts, err := s.repo.List(ctx)
	if err != nil {
		return internal(err)
	}

	matched := false
	for _, group := range groups {
		if !domainvault.IsGroupAtOrBelow(group.Path, root) {
			continue
		}
		matched = true
		if err := s.groups.DeleteGroup(ctx, group.Path); err != nil && !errors.Is(err, ErrNotFound) {
			return internal(err)
		}
	}
	for _, host := range hosts {
		if !domainvault.IsGroupAtOrBelow(host.Group, root) {
			continue
		}
		matched = true
		host.Group = ""
		host.GroupID = ""
		host.UpdatedAt = s.now()
		if err := s.repo.Save(ctx, host); err != nil {
			return internal(err)
		}
	}
	if !matched {
		return ErrNotFound
	}
	// Snippet target group references are cleared, never deleted with the
	// group; the Snippets stay in the Vault.
	if err := s.removeSnippetTargetGroups(ctx, root); err != nil {
		return err
	}
	return nil
}

// AssignHostGroup moves one Host into a Host Group, or to the Vault root when
// groupPath is empty. The Host's other fields, including credentials, are
// preserved.
func (s *Service) AssignHostGroup(ctx context.Context, id model.HostID, groupPath string) error {
	if s.groups == nil {
		return unsupported(ErrGroupsUnavailable)
	}
	path := domainvault.NormalizeGroup(groupPath)
	groupID := ""
	if path != "" {
		groups, err := s.groups.ListGroups(ctx)
		if err != nil {
			return internal(err)
		}
		groups = domainvault.EnsureGroupIdentities(groups)
		if !groupsAtOrBelow(groups, path) {
			// A path that only Hosts reference, or that only leads to a saved
			// group, is an implicit group: it has no saved defaults yet, but
			// the Vault already displays it and the editor lets a Host use
			// it. Accepting it here keeps the visible drop target and the
			// saved membership in agreement.
			hosts, listErr := s.repo.List(ctx)
			if listErr != nil {
				return internal(listErr)
			}
			if !hostsReferenceGroup(hosts, path) {
				return invalid(&domainvault.ValidationError{
					Field: domainvault.FieldGroup, Rule: domainvault.RuleInvalid,
				})
			}
		}
		groupID = groupIdentityForPath(groups, path)
	}
	host, err := s.repo.Get(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return internal(err)
	}
	host.Group = path
	host.GroupID = groupID
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
		groups, err := s.groups.ListGroups(ctx)
		if err != nil {
			return domainvault.Host{}, internal(err)
		}
		effective = domainvault.ApplyGroupDefaults(host,
			domainvault.ResolveGroupDefaults(groups, host.Group))
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
	groups, err := s.groups.ListGroups(ctx)
	if err != nil {
		return domainvault.GroupDefaults{}, internal(err)
	}
	return domainvault.ResolveGroupDefaults(groups, path), nil
}

func (s *Service) renameHosts(ctx context.Context, oldPath, newPath string) error {
	hosts, err := s.repo.List(ctx)
	if err != nil {
		return internal(err)
	}
	for _, host := range hosts {
		if !domainvault.IsGroupAtOrBelow(host.Group, oldPath) {
			continue
		}
		host.Group = domainvault.ReplaceGroupPrefix(host.Group, oldPath, newPath)
		host.UpdatedAt = s.now()
		if err := s.repo.Save(ctx, host); err != nil {
			return internal(err)
		}
	}
	return nil
}

func (s *Service) renameDescendantGroups(ctx context.Context, groups []domainvault.Group, oldPath, newPath string) error {
	for _, group := range groups {
		if !domainvault.IsGroupAtOrBelow(group.Path, oldPath) {
			continue
		}
		if group.Path == oldPath {
			continue
		}
		previousPath := group.Path
		if group.ID == "" {
			group.ID = domainvault.GroupIDFromPath(previousPath)
		}
		group.Path = domainvault.ReplaceGroupPrefix(previousPath, oldPath, newPath)
		group.Name = domainvault.GroupName(group.Path)
		group.UpdatedAt = s.now()
		if err := s.groups.SaveGroup(ctx, group); err != nil {
			return internal(err)
		}
		// Move the record rather than leaving the old path behind: SaveGroup
		// writes the new path, so the old row must be removed explicitly.
		if err := s.groups.DeleteGroup(ctx, previousPath); err != nil && !errors.Is(err, ErrNotFound) {
			return internal(err)
		}
	}
	return nil
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

func findGroup(groups []domainvault.Group, path string) (domainvault.Group, bool) {
	for _, group := range groups {
		if group.Path == path {
			return group, true
		}
	}
	return domainvault.Group{}, false
}

// groupsAtOrBelow reports whether a saved group is path or lies below it,
// which makes path one of its levels.
func groupsAtOrBelow(groups []domainvault.Group, path string) bool {
	for _, group := range groups {
		if domainvault.IsGroupAtOrBelow(group.Path, path) {
			return true
		}
	}
	return false
}

// groupIdentityForPath returns the stable ID of the saved group at path, or
// the deterministic identity of an implicit path. An empty path is the Vault
// root and has no identity.
func groupIdentityForPath(groups []domainvault.Group, path string) string {
	path = domainvault.NormalizeGroup(path)
	if path == "" {
		return ""
	}
	if group, ok := findGroup(groups, path); ok && group.ID != "" {
		return group.ID
	}
	return domainvault.GroupIDFromPath(path)
}

func hostsReferenceGroup(hosts []domainvault.Host, path string) bool {
	for _, host := range hosts {
		if domainvault.IsGroupAtOrBelow(host.Group, path) {
			return true
		}
	}
	return false
}

func nextGroupOrder(groups []domainvault.Group) int64 {
	var max int64
	for _, group := range groups {
		if group.OrderKey > max {
			max = group.OrderKey
		}
	}
	return max + orderStep
}

// ErrGroupsUnavailable reports a Service built without Host Group persistence.
var ErrGroupsUnavailable = errors.New("vault: Host Groups are unavailable")
