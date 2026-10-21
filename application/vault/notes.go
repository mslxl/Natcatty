package vault

import (
	"context"
	"errors"

	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// NoteRepository persists Vault Notes and Note Groups. A Note references its
// group by path; the adapter keeps both tables independent, so deleting a
// group cannot cascade to a Note.
type NoteRepository interface {
	ListNotes(ctx context.Context) ([]domainvault.Note, error)
	GetNote(ctx context.Context, id string) (domainvault.Note, error)
	SaveNote(ctx context.Context, note domainvault.Note) error
	DeleteNote(ctx context.Context, id string) error

	ListNoteGroups(ctx context.Context) ([]domainvault.NoteGroup, error)
	SaveNoteGroup(ctx context.Context, group domainvault.NoteGroup) error
	DeleteNoteGroup(ctx context.Context, path string) error
}

// WithNotes gives the Service Vault Note and Note Group persistence.
func WithNotes(repo NoteRepository) Option {
	return func(s *Service) { s.notes = repo }
}

// WithNoteIDGenerator sets how the Service names Vault Notes.
func WithNoteIDGenerator(fn func() string) Option {
	return func(s *Service) {
		if fn != nil {
			s.newNoteID = fn
		}
	}
}

// ListNotes returns every Vault Note in display order.
func (s *Service) ListNotes(ctx context.Context) ([]domainvault.Note, error) {
	if s.notes == nil {
		return nil, unsupported(ErrNotesUnavailable)
	}
	notes, err := s.notes.ListNotes(ctx)
	if err != nil {
		return nil, internal(err)
	}
	return notes, nil
}

// GetNote returns one Vault Note.
func (s *Service) GetNote(ctx context.Context, id string) (domainvault.Note, error) {
	if s.notes == nil {
		return domainvault.Note{}, unsupported(ErrNotesUnavailable)
	}
	note, err := s.notes.GetNote(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return domainvault.Note{}, ErrNotFound
	}
	if err != nil {
		return domainvault.Note{}, internal(err)
	}
	return note, nil
}

// CreateNote validates and persists a Vault Note.
func (s *Service) CreateNote(ctx context.Context, draft domainvault.NoteDraft) (domainvault.Note, error) {
	if s.notes == nil {
		return domainvault.Note{}, unsupported(ErrNotesUnavailable)
	}
	normalized, err := domainvault.NormalizeNoteDraft(draft)
	if err != nil {
		return domainvault.Note{}, invalid(err)
	}
	existing, err := s.notes.ListNotes(ctx)
	if err != nil {
		return domainvault.Note{}, internal(err)
	}
	now := s.now()
	note := domainvault.Note{
		ID:        s.newNoteID(),
		Title:     normalized.Title,
		Content:   normalized.Content,
		Group:     normalized.Group,
		OrderKey:  nextNoteOrder(existing),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.notes.SaveNote(ctx, note); err != nil {
		return domainvault.Note{}, internal(err)
	}
	return note, nil
}

// UpdateNote replaces a Vault Note's editable fields. Identity, creation time,
// and order are preserved.
func (s *Service) UpdateNote(ctx context.Context, id string, draft domainvault.NoteDraft) (domainvault.Note, error) {
	if s.notes == nil {
		return domainvault.Note{}, unsupported(ErrNotesUnavailable)
	}
	existing, err := s.notes.GetNote(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return domainvault.Note{}, ErrNotFound
	}
	if err != nil {
		return domainvault.Note{}, internal(err)
	}
	normalized, err := domainvault.NormalizeNoteDraft(draft)
	if err != nil {
		return domainvault.Note{}, invalid(err)
	}
	existing.Title = normalized.Title
	existing.Content = normalized.Content
	existing.Group = normalized.Group
	existing.UpdatedAt = s.now()
	if err := s.notes.SaveNote(ctx, existing); err != nil {
		return domainvault.Note{}, internal(err)
	}
	return existing, nil
}

// DeleteNote removes one Vault Note.
func (s *Service) DeleteNote(ctx context.Context, id string) error {
	if s.notes == nil {
		return unsupported(ErrNotesUnavailable)
	}
	err := s.notes.DeleteNote(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return internal(err)
	}
	return nil
}

// SearchNotes returns the Notes matching query over title, Markdown source,
// and Note Group path, case-insensitively. An empty query lists every Note.
func (s *Service) SearchNotes(ctx context.Context, query string) ([]domainvault.Note, error) {
	notes, err := s.ListNotes(ctx)
	if err != nil {
		return nil, err
	}
	return domainvault.SearchNotes(notes, query), nil
}

// ListNoteGroups returns saved Note Groups plus every ancestor path a saved or
// referenced path implies, in display order. A path only Notes reference is
// included as an empty record so the tree can show it.
func (s *Service) ListNoteGroups(ctx context.Context) ([]domainvault.NoteGroup, error) {
	if s.notes == nil {
		return nil, unsupported(ErrNotesUnavailable)
	}
	groups, err := s.notes.ListNoteGroups(ctx)
	if err != nil {
		return nil, internal(err)
	}
	notes, err := s.notes.ListNotes(ctx)
	if err != nil {
		return nil, internal(err)
	}
	byPath := make(map[string]domainvault.NoteGroup, len(groups))
	saved := make([]string, 0, len(groups))
	for _, group := range groups {
		byPath[group.Path] = group
		saved = append(saved, group.Path)
	}
	paths := domainvault.CollectNoteGroupPaths(saved, notes)
	out := make([]domainvault.NoteGroup, 0, len(paths))
	for _, path := range paths {
		if group, ok := byPath[path]; ok {
			out = append(out, group)
			continue
		}
		out = append(out, domainvault.NoteGroup{Path: path})
	}
	return out, nil
}

// CreateNoteGroup validates and persists an empty Note Group.
func (s *Service) CreateNoteGroup(ctx context.Context, draft domainvault.NoteGroupDraft) (domainvault.NoteGroup, error) {
	if s.notes == nil {
		return domainvault.NoteGroup{}, unsupported(ErrNotesUnavailable)
	}
	normalized, err := domainvault.NormalizeNoteGroupDraft(draft)
	if err != nil {
		return domainvault.NoteGroup{}, invalid(err)
	}
	groups, err := s.notes.ListNoteGroups(ctx)
	if err != nil {
		return domainvault.NoteGroup{}, internal(err)
	}
	for _, group := range groups {
		if group.Path == normalized.Path {
			return domainvault.NoteGroup{}, conflict(nil)
		}
	}
	now := s.now()
	group := domainvault.NoteGroup{
		Path:      normalized.Path,
		OrderKey:  nextNoteGroupOrder(groups),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.notes.SaveNoteGroup(ctx, group); err != nil {
		return domainvault.NoteGroup{}, internal(err)
	}
	return group, nil
}

// RenameNoteGroup moves a Note Group and its descendants to a new path and
// rewrites every Note that referenced the old subtree. A group cannot be
// moved inside itself, and an occupied destination is a conflict.
func (s *Service) RenameNoteGroup(ctx context.Context, path, newPath string) (domainvault.NoteGroup, error) {
	if s.notes == nil {
		return domainvault.NoteGroup{}, unsupported(ErrNotesUnavailable)
	}
	oldPath := domainvault.NormalizeGroup(path)
	if oldPath == "" {
		return domainvault.NoteGroup{}, invalid(&domainvault.ValidationError{
			Field: domainvault.FieldNoteGroup, Rule: domainvault.RuleRequired,
		})
	}
	normalized, err := domainvault.NormalizeNoteGroupDraft(domainvault.NoteGroupDraft{Path: newPath})
	if err != nil {
		return domainvault.NoteGroup{}, invalid(err)
	}
	if normalized.Path == oldPath {
		groups, err := s.notes.ListNoteGroups(ctx)
		if err != nil {
			return domainvault.NoteGroup{}, internal(err)
		}
		for _, group := range groups {
			if group.Path == oldPath {
				return group, nil
			}
		}
		return domainvault.NoteGroup{Path: oldPath}, nil
	}
	if domainvault.NoteGroupAtOrBelow(normalized.Path, oldPath) {
		return domainvault.NoteGroup{}, invalid(&domainvault.ValidationError{
			Field: domainvault.FieldNoteGroup, Rule: domainvault.RuleInvalid,
		})
	}

	groups, err := s.notes.ListNoteGroups(ctx)
	if err != nil {
		return domainvault.NoteGroup{}, internal(err)
	}
	for _, group := range groups {
		if domainvault.NoteGroupAtOrBelow(group.Path, oldPath) {
			continue
		}
		if domainvault.NoteGroupAtOrBelow(group.Path, normalized.Path) {
			return domainvault.NoteGroup{}, conflict(nil)
		}
	}
	notes, err := s.notes.ListNotes(ctx)
	if err != nil {
		return domainvault.NoteGroup{}, internal(err)
	}
	for _, note := range notes {
		if note.Group == "" || domainvault.NoteGroupAtOrBelow(note.Group, oldPath) {
			continue
		}
		if domainvault.NoteGroupAtOrBelow(note.Group, normalized.Path) {
			return domainvault.NoteGroup{}, conflict(nil)
		}
	}

	now := s.now()
	renamed := domainvault.NoteGroup{
		Path:      normalized.Path,
		OrderKey:  nextNoteGroupOrder(groups),
		CreatedAt: now,
		UpdatedAt: now,
	}
	for _, group := range groups {
		if group.Path != oldPath {
			continue
		}
		renamed.OrderKey = group.OrderKey
		renamed.CreatedAt = group.CreatedAt
	}
	if err := s.notes.SaveNoteGroup(ctx, renamed); err != nil {
		return domainvault.NoteGroup{}, internal(err)
	}
	for _, group := range groups {
		if !domainvault.NoteGroupAtOrBelow(group.Path, oldPath) || group.Path == oldPath {
			continue
		}
		next := group
		next.Path = domainvault.ReplaceNoteGroupPrefix(group.Path, oldPath, normalized.Path)
		next.UpdatedAt = now
		if err := s.notes.SaveNoteGroup(ctx, next); err != nil {
			return domainvault.NoteGroup{}, internal(err)
		}
	}
	for _, note := range notes {
		if note.Group == "" || !domainvault.NoteGroupAtOrBelow(note.Group, oldPath) {
			continue
		}
		note.Group = domainvault.ReplaceNoteGroupPrefix(note.Group, oldPath, normalized.Path)
		note.UpdatedAt = now
		if err := s.notes.SaveNote(ctx, note); err != nil {
			return domainvault.NoteGroup{}, internal(err)
		}
	}
	// Old paths are removed only after every new path is written.
	for _, group := range groups {
		if !domainvault.NoteGroupAtOrBelow(group.Path, oldPath) {
			continue
		}
		if err := s.notes.DeleteNoteGroup(ctx, group.Path); err != nil &&
			!errors.Is(err, ErrNotFound) {
			return domainvault.NoteGroup{}, internal(err)
		}
	}
	return renamed, nil
}

// DeleteNoteGroup removes a Note Group and its descendants. Its Notes are
// never deleted: they move to the Vault root, matching the original's
// non-cascading folder delete. A group path only Notes reference is cleared
// too, so an imported dangling path cannot leave Notes invisible.
func (s *Service) DeleteNoteGroup(ctx context.Context, path string) error {
	if s.notes == nil {
		return unsupported(ErrNotesUnavailable)
	}
	root := domainvault.NormalizeGroup(path)
	if root == "" {
		return invalid(&domainvault.ValidationError{
			Field: domainvault.FieldNoteGroup, Rule: domainvault.RuleRequired,
		})
	}
	groups, err := s.notes.ListNoteGroups(ctx)
	if err != nil {
		return internal(err)
	}
	notes, err := s.notes.ListNotes(ctx)
	if err != nil {
		return internal(err)
	}
	matched := false
	for _, group := range groups {
		if !domainvault.NoteGroupAtOrBelow(group.Path, root) {
			continue
		}
		matched = true
		if err := s.notes.DeleteNoteGroup(ctx, group.Path); err != nil &&
			!errors.Is(err, ErrNotFound) {
			return internal(err)
		}
	}
	now := s.now()
	for _, note := range notes {
		if note.Group == "" || !domainvault.NoteGroupAtOrBelow(note.Group, root) {
			continue
		}
		matched = true
		note.Group = ""
		note.UpdatedAt = now
		if err := s.notes.SaveNote(ctx, note); err != nil {
			return internal(err)
		}
	}
	if !matched {
		return ErrNotFound
	}
	return nil
}

func nextNoteOrder(notes []domainvault.Note) int64 {
	var max int64
	for _, note := range notes {
		if note.OrderKey > max {
			max = note.OrderKey
		}
	}
	return max + orderStep
}

func nextNoteGroupOrder(groups []domainvault.NoteGroup) int64 {
	var max int64
	for _, group := range groups {
		if group.OrderKey > max {
			max = group.OrderKey
		}
	}
	return max + orderStep
}
