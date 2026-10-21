package vault

import (
	"context"
	"errors"
	"slices"
	"strings"

	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// SnippetRepository persists Snippets and Snippet packages.
type SnippetRepository interface {
	ListSnippets(ctx context.Context) ([]domainvault.Snippet, error)
	GetSnippet(ctx context.Context, id string) (domainvault.Snippet, error)
	SaveSnippet(ctx context.Context, snippet domainvault.Snippet) error
	DeleteSnippet(ctx context.Context, id string) error

	ListSnippetPackages(ctx context.Context) ([]domainvault.SnippetPackage, error)
	GetSnippetPackage(ctx context.Context, path string) (domainvault.SnippetPackage, error)
	SaveSnippetPackage(ctx context.Context, pkg domainvault.SnippetPackage) error
	DeleteSnippetPackage(ctx context.Context, path string) error
}

// WithSnippets gives the Service Snippet and package persistence.
func WithSnippets(repo SnippetRepository) Option {
	return func(s *Service) { s.snippets = repo }
}

// WithSnippetIDGenerator sets how the Service names Snippets.
func WithSnippetIDGenerator(fn func() string) Option {
	return func(s *Service) {
		if fn != nil {
			s.newSnippetID = fn
		}
	}
}

// SnippetVariableError reports the variables a run could not fill. The UI
// localizes the names instead of showing this text.
type SnippetVariableError struct {
	Missing []string
}

func (e *SnippetVariableError) Error() string {
	return "vault: missing Snippet variables: " + strings.Join(e.Missing, ", ")
}

// SnippetRun is a Snippet resolved for execution: its command is already
// substituted, and Hosts are the connectable Hosts its saved targets select.
type SnippetRun struct {
	Snippet domainvault.Snippet
	Command string
	Values  map[string]string
	Hosts   []domainvault.Host
}

// ListSnippets returns every Snippet in display order.
func (s *Service) ListSnippets(ctx context.Context) ([]domainvault.Snippet, error) {
	if s.snippets == nil {
		return nil, unsupported(ErrSnippetsUnavailable)
	}
	snippets, err := s.snippets.ListSnippets(ctx)
	if err != nil {
		return nil, internal(err)
	}
	return snippets, nil
}

// CreateSnippet validates and persists a Snippet.
func (s *Service) CreateSnippet(ctx context.Context, draft domainvault.SnippetDraft) (domainvault.Snippet, error) {
	if s.snippets == nil {
		return domainvault.Snippet{}, unsupported(ErrSnippetsUnavailable)
	}
	normalized, err := domainvault.NormalizeSnippetDraft(draft)
	if err != nil {
		return domainvault.Snippet{}, invalid(err)
	}
	existing, err := s.snippets.ListSnippets(ctx)
	if err != nil {
		return domainvault.Snippet{}, internal(err)
	}
	now := s.now()
	snippet := domainvault.Snippet{
		ID:               s.newSnippetID(),
		Label:            normalized.Label,
		Command:          normalized.Command,
		Package:          normalized.Package,
		Tags:             normalized.Tags,
		Targets:          normalized.Targets,
		TargetGroups:     normalized.TargetGroups,
		TargetsAllHosts:  normalized.TargetsAllHosts,
		NoAutoRun:        normalized.NoAutoRun,
		MultiLineRunMode: normalized.MultiLineRunMode,
		OrderKey:         nextSnippetOrder(existing),
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := s.snippets.SaveSnippet(ctx, snippet); err != nil {
		return domainvault.Snippet{}, internal(err)
	}
	return snippet, nil
}

// UpdateSnippet replaces a Snippet's editable fields. Identity, creation time,
// order, and remembered variable values are preserved.
func (s *Service) UpdateSnippet(ctx context.Context, id string, draft domainvault.SnippetDraft) (domainvault.Snippet, error) {
	if s.snippets == nil {
		return domainvault.Snippet{}, unsupported(ErrSnippetsUnavailable)
	}
	existing, err := s.snippets.GetSnippet(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return domainvault.Snippet{}, ErrNotFound
	}
	if err != nil {
		return domainvault.Snippet{}, internal(err)
	}
	normalized, err := domainvault.NormalizeSnippetDraft(draft)
	if err != nil {
		return domainvault.Snippet{}, invalid(err)
	}
	existing.Label = normalized.Label
	existing.Command = normalized.Command
	existing.Package = normalized.Package
	existing.Tags = normalized.Tags
	existing.Targets = normalized.Targets
	existing.TargetGroups = normalized.TargetGroups
	existing.TargetsAllHosts = normalized.TargetsAllHosts
	existing.NoAutoRun = normalized.NoAutoRun
	existing.MultiLineRunMode = normalized.MultiLineRunMode
	existing.UpdatedAt = s.now()
	if err := s.snippets.SaveSnippet(ctx, existing); err != nil {
		return domainvault.Snippet{}, internal(err)
	}
	return existing, nil
}

// DeleteSnippet removes one Snippet.
func (s *Service) DeleteSnippet(ctx context.Context, id string) error {
	if s.snippets == nil {
		return unsupported(ErrSnippetsUnavailable)
	}
	err := s.snippets.DeleteSnippet(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return internal(err)
	}
	return nil
}

// SaveSnippetVariableValues remembers the last values the user entered for a
// Snippet, merging them into the stored map.
func (s *Service) SaveSnippetVariableValues(ctx context.Context, id string, values map[string]string) error {
	if s.snippets == nil {
		return unsupported(ErrSnippetsUnavailable)
	}
	snippet, err := s.snippets.GetSnippet(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return internal(err)
	}
	if snippet.VariableValues == nil {
		snippet.VariableValues = make(map[string]string, len(values))
	}
	for name, value := range values {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		snippet.VariableValues[name] = value
	}
	snippet.UpdatedAt = s.now()
	if err := s.snippets.SaveSnippet(ctx, snippet); err != nil {
		return internal(err)
	}
	return nil
}

// PrepareSnippet resolves a Snippet for execution: it substitutes the provided
// variable values and collects the Hosts the saved targets select. A missing
// required variable returns a *SnippetVariableError wrapped in a
// KindValidation Error, so no partially filled command can run.
func (s *Service) PrepareSnippet(ctx context.Context, id string, values map[string]string) (SnippetRun, error) {
	if s.snippets == nil {
		return SnippetRun{}, unsupported(ErrSnippetsUnavailable)
	}
	snippet, err := s.snippets.GetSnippet(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return SnippetRun{}, ErrNotFound
	}
	if err != nil {
		return SnippetRun{}, internal(err)
	}

	command, missing := domainvault.ApplySnippetVariables(snippet.Command, values)
	if len(missing) > 0 {
		return SnippetRun{}, invalid(&SnippetVariableError{Missing: missing})
	}

	hosts, err := s.repo.List(ctx)
	if err != nil {
		return SnippetRun{}, internal(err)
	}
	selected := domainvault.SnippetRunnableHosts(snippet, hosts)
	effective := make([]domainvault.Host, 0, len(selected))
	for _, host := range selected {
		resolved, err := s.resolveEffective(ctx, host)
		if err != nil {
			return SnippetRun{}, err
		}
		effective = append(effective, resolved)
	}
	return SnippetRun{
		Snippet: snippet,
		Command: command,
		Values:  cloneVariableValues(values),
		Hosts:   effective,
	}, nil
}

// ListSnippetPackages returns saved packages plus every ancestor path a
// Snippet implies, in display order.
func (s *Service) ListSnippetPackages(ctx context.Context) ([]domainvault.SnippetPackage, error) {
	if s.snippets == nil {
		return nil, unsupported(ErrSnippetsUnavailable)
	}
	packages, err := s.snippets.ListSnippetPackages(ctx)
	if err != nil {
		return nil, internal(err)
	}
	snippets, err := s.snippets.ListSnippets(ctx)
	if err != nil {
		return nil, internal(err)
	}
	byPath := make(map[string]domainvault.SnippetPackage, len(packages))
	saved := make([]string, 0, len(packages))
	for _, pkg := range packages {
		byPath[pkg.Path] = pkg
		saved = append(saved, pkg.Path)
	}
	paths := domainvault.CollectSnippetPackagePaths(saved, snippets)
	out := make([]domainvault.SnippetPackage, 0, len(paths))
	for _, path := range paths {
		if pkg, ok := byPath[path]; ok {
			out = append(out, pkg)
			continue
		}
		out = append(out, domainvault.SnippetPackage{Path: path})
	}
	return out, nil
}

// CreateSnippetPackage validates and persists an empty package.
func (s *Service) CreateSnippetPackage(ctx context.Context, draft domainvault.SnippetPackageDraft) (domainvault.SnippetPackage, error) {
	if s.snippets == nil {
		return domainvault.SnippetPackage{}, unsupported(ErrSnippetsUnavailable)
	}
	normalized, err := domainvault.NormalizeSnippetPackageDraft(draft)
	if err != nil {
		return domainvault.SnippetPackage{}, invalid(err)
	}
	packages, err := s.snippets.ListSnippetPackages(ctx)
	if err != nil {
		return domainvault.SnippetPackage{}, internal(err)
	}
	for _, pkg := range packages {
		if pkg.Path == normalized.Path {
			return domainvault.SnippetPackage{}, conflict(nil)
		}
	}
	now := s.now()
	pkg := domainvault.SnippetPackage{
		Path:      normalized.Path,
		OrderKey:  nextSnippetPackageOrder(packages),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.snippets.SaveSnippetPackage(ctx, pkg); err != nil {
		return domainvault.SnippetPackage{}, internal(err)
	}
	return pkg, nil
}

// RenameSnippetPackage moves a package and its descendants to a new path and
// rewrites every Snippet that referenced the old subtree. A package cannot be
// moved inside itself, and an existing package path is a conflict.
func (s *Service) RenameSnippetPackage(ctx context.Context, path, newPath string) (domainvault.SnippetPackage, error) {
	if s.snippets == nil {
		return domainvault.SnippetPackage{}, unsupported(ErrSnippetsUnavailable)
	}
	oldPath, err := domainvault.NormalizeSnippetPackagePath(path)
	if err != nil {
		return domainvault.SnippetPackage{}, invalid(err)
	}
	if oldPath == "" {
		return domainvault.SnippetPackage{}, invalid(&domainvault.ValidationError{
			Field: domainvault.FieldSnippetPackage, Rule: domainvault.RuleRequired,
		})
	}
	normalized, err := domainvault.NormalizeSnippetPackageDraft(domainvault.SnippetPackageDraft{Path: newPath})
	if err != nil {
		return domainvault.SnippetPackage{}, invalid(err)
	}
	if normalized.Path == oldPath {
		packages, err := s.snippets.ListSnippetPackages(ctx)
		if err != nil {
			return domainvault.SnippetPackage{}, internal(err)
		}
		for _, pkg := range packages {
			if pkg.Path == oldPath {
				return pkg, nil
			}
		}
		return domainvault.SnippetPackage{Path: oldPath}, nil
	}
	if domainvault.SnippetPackageAtOrBelow(normalized.Path, oldPath) {
		return domainvault.SnippetPackage{}, invalid(&domainvault.ValidationError{
			Field: domainvault.FieldSnippetPackage, Rule: domainvault.RuleInvalid,
		})
	}

	packages, err := s.snippets.ListSnippetPackages(ctx)
	if err != nil {
		return domainvault.SnippetPackage{}, internal(err)
	}
	// A path outside the renamed subtree that already occupies the destination
	// makes the rename ambiguous.
	for _, pkg := range packages {
		if domainvault.SnippetPackageAtOrBelow(pkg.Path, oldPath) {
			continue
		}
		if domainvault.SnippetPackageAtOrBelow(pkg.Path, normalized.Path) {
			return domainvault.SnippetPackage{}, conflict(nil)
		}
	}
	snippets, err := s.snippets.ListSnippets(ctx)
	if err != nil {
		return domainvault.SnippetPackage{}, internal(err)
	}
	for _, snippet := range snippets {
		if snippet.Package == "" || domainvault.SnippetPackageAtOrBelow(snippet.Package, oldPath) {
			continue
		}
		if domainvault.SnippetPackageAtOrBelow(snippet.Package, normalized.Path) {
			return domainvault.SnippetPackage{}, conflict(nil)
		}
	}

	now := s.now()
	renamed := domainvault.SnippetPackage{
		Path:     normalized.Path,
		OrderKey: nextSnippetPackageOrder(packages),
		// Reuse the package's own timestamps and order when it existed.
		CreatedAt: now,
	}
	for _, pkg := range packages {
		if pkg.Path != oldPath {
			continue
		}
		renamed.OrderKey = pkg.OrderKey
		renamed.CreatedAt = pkg.CreatedAt
	}
	renamed.UpdatedAt = now
	if err := s.snippets.SaveSnippetPackage(ctx, renamed); err != nil {
		return domainvault.SnippetPackage{}, internal(err)
	}
	for _, pkg := range packages {
		if !domainvault.SnippetPackageAtOrBelow(pkg.Path, oldPath) || pkg.Path == oldPath {
			continue
		}
		next := pkg
		next.Path = domainvault.ReplaceSnippetPackagePrefix(pkg.Path, oldPath, normalized.Path)
		next.UpdatedAt = now
		if err := s.snippets.SaveSnippetPackage(ctx, next); err != nil {
			return domainvault.SnippetPackage{}, internal(err)
		}
	}
	for _, snippet := range snippets {
		if snippet.Package == "" || !domainvault.SnippetPackageAtOrBelow(snippet.Package, oldPath) {
			continue
		}
		snippet.Package = domainvault.ReplaceSnippetPackagePrefix(snippet.Package, oldPath, normalized.Path)
		snippet.UpdatedAt = now
		if err := s.snippets.SaveSnippet(ctx, snippet); err != nil {
			return domainvault.SnippetPackage{}, internal(err)
		}
	}
	// Old paths are removed only after every new path is written.
	for _, pkg := range packages {
		if !domainvault.SnippetPackageAtOrBelow(pkg.Path, oldPath) {
			continue
		}
		if err := s.snippets.DeleteSnippetPackage(ctx, pkg.Path); err != nil &&
			!errors.Is(err, ErrNotFound) {
			return domainvault.SnippetPackage{}, internal(err)
		}
	}
	return renamed, nil
}

// DeleteSnippetPackage removes a package and its descendants, and clears the
// package path on every Snippet that used them. Snippets themselves are kept.
func (s *Service) DeleteSnippetPackage(ctx context.Context, path string) error {
	if s.snippets == nil {
		return unsupported(ErrSnippetsUnavailable)
	}
	root, err := domainvault.NormalizeSnippetPackagePath(path)
	if err != nil {
		return invalid(err)
	}
	if root == "" {
		return invalid(&domainvault.ValidationError{
			Field: domainvault.FieldSnippetPackage, Rule: domainvault.RuleRequired,
		})
	}
	packages, err := s.snippets.ListSnippetPackages(ctx)
	if err != nil {
		return internal(err)
	}
	snippets, err := s.snippets.ListSnippets(ctx)
	if err != nil {
		return internal(err)
	}

	matched := false
	for _, pkg := range packages {
		if !domainvault.SnippetPackageAtOrBelow(pkg.Path, root) {
			continue
		}
		matched = true
		if err := s.snippets.DeleteSnippetPackage(ctx, pkg.Path); err != nil &&
			!errors.Is(err, ErrNotFound) {
			return internal(err)
		}
	}
	now := s.now()
	for _, snippet := range snippets {
		if snippet.Package == "" || !domainvault.SnippetPackageAtOrBelow(snippet.Package, root) {
			continue
		}
		matched = true
		snippet.Package = ""
		snippet.UpdatedAt = now
		if err := s.snippets.SaveSnippet(ctx, snippet); err != nil {
			return internal(err)
		}
	}
	if !matched {
		return ErrNotFound
	}
	return nil
}

// remapSnippetTargetGroups follows a Host Group rename in every Snippet that
// targets the group. It is a no-op when the Service has no Snippet repository.
func (s *Service) remapSnippetTargetGroups(ctx context.Context, oldPath, newPath string) error {
	if s.snippets == nil {
		return nil
	}
	snippets, err := s.snippets.ListSnippets(ctx)
	if err != nil {
		return internal(err)
	}
	for _, snippet := range snippets {
		remapped := domainvault.RemapSnippetTargetGroups(snippet, oldPath, newPath)
		if slices.Equal(remapped.TargetGroups, snippet.TargetGroups) {
			continue
		}
		remapped.UpdatedAt = s.now()
		if err := s.snippets.SaveSnippet(ctx, remapped); err != nil {
			return internal(err)
		}
	}
	return nil
}

// removeSnippetTargetGroups drops references to a deleted Host Group. The
// Snippets stay in the Vault.
func (s *Service) removeSnippetTargetGroups(ctx context.Context, path string) error {
	if s.snippets == nil {
		return nil
	}
	snippets, err := s.snippets.ListSnippets(ctx)
	if err != nil {
		return internal(err)
	}
	for _, snippet := range snippets {
		cleared := domainvault.RemoveSnippetTargetGroups(snippet, path)
		if slices.Equal(cleared.TargetGroups, snippet.TargetGroups) {
			continue
		}
		cleared.UpdatedAt = s.now()
		if err := s.snippets.SaveSnippet(ctx, cleared); err != nil {
			return internal(err)
		}
	}
	return nil
}

func nextSnippetOrder(snippets []domainvault.Snippet) int64 {
	var max int64
	for _, snippet := range snippets {
		if snippet.OrderKey > max {
			max = snippet.OrderKey
		}
	}
	return max + orderStep
}

func nextSnippetPackageOrder(packages []domainvault.SnippetPackage) int64 {
	var max int64
	for _, pkg := range packages {
		if pkg.OrderKey > max {
			max = pkg.OrderKey
		}
	}
	return max + orderStep
}

func cloneVariableValues(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	copied := make(map[string]string, len(values))
	for name, value := range values {
		copied[name] = value
	}
	return copied
}
