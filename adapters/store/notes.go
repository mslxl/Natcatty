package store

import (
	"context"
	"fmt"

	appvault "github.com/mslxl/natcatty/application/vault"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"gorm.io/gorm"
)

// ListNotes returns every Vault Note in display order.
func (s *Store) ListNotes(ctx context.Context) ([]domainvault.Note, error) {
	var data []string
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		var err error
		data, err = loadDataRows(tx, &noteSchema{}, "order_key ASC", "title COLLATE NOCASE ASC", "id ASC")
		return err
	}); err != nil {
		return nil, fmt.Errorf("store: list Vault Notes: %w", err)
	}
	notes, err := decodeDataList[domainvault.Note](data, "Vault Note")
	if err != nil {
		return nil, err
	}
	return notes, nil
}

// GetNote returns one Vault Note by id.
func (s *Store) GetNote(ctx context.Context, id string) (domainvault.Note, error) {
	var data string
	var found bool
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		var err error
		data, found, err = loadDataRow(tx, &noteSchema{}, &noteSchema{ID: id})
		return err
	}); err != nil {
		return domainvault.Note{}, fmt.Errorf("store: get Vault Note: %w", err)
	}
	if !found {
		return domainvault.Note{}, appvault.ErrNotFound
	}
	var note domainvault.Note
	if err := decodeData(data, "Vault Note", &note); err != nil {
		return domainvault.Note{}, err
	}
	return note, nil
}

// SaveNote inserts or replaces one Vault Note.
func (s *Store) SaveNote(ctx context.Context, note domainvault.Note) error {
	model, err := noteModel(note)
	if err != nil {
		return err
	}
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		return upsertModel(tx, &model, "id", "title", "group_path", "order_key", "updated_at", "data")
	}); err != nil {
		return fmt.Errorf("store: save Vault Note: %w", err)
	}
	s.vaultChanged()
	return nil
}

// DeleteNote removes one Vault Note.
func (s *Store) DeleteNote(ctx context.Context, id string) error {
	var changed int64
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		var err error
		changed, err = deleteModel(tx, &noteSchema{}, &noteSchema{ID: id})
		return err
	}); err != nil {
		return fmt.Errorf("store: delete Vault Note: %w", err)
	}
	if changed == 0 {
		return appvault.ErrNotFound
	}
	s.vaultChanged()
	return nil
}

// ListNoteGroups returns every saved Note Group in display order.
func (s *Store) ListNoteGroups(ctx context.Context) ([]domainvault.NoteGroup, error) {
	var data []string
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		var err error
		data, err = loadDataRows(tx, &noteGroupSchema{}, "order_key ASC", "path COLLATE NOCASE ASC")
		return err
	}); err != nil {
		return nil, fmt.Errorf("store: list Note Groups: %w", err)
	}
	groups, err := decodeDataList[domainvault.NoteGroup](data, "Note Group")
	if err != nil {
		return nil, err
	}
	return groups, nil
}

// SaveNoteGroup inserts or replaces one Note Group.
func (s *Store) SaveNoteGroup(ctx context.Context, group domainvault.NoteGroup) error {
	model, err := noteGroupModel(group)
	if err != nil {
		return err
	}
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		return upsertModel(tx, &model, "path", "order_key", "updated_at", "data")
	}); err != nil {
		return fmt.Errorf("store: save Note Group: %w", err)
	}
	s.vaultChanged()
	return nil
}

// DeleteNoteGroup removes one Note Group. Notes that reference it are left
// untouched; the Vault service owns that non-cascading rule.
func (s *Store) DeleteNoteGroup(ctx context.Context, path string) error {
	var changed int64
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		var err error
		changed, err = deleteModel(tx, &noteGroupSchema{}, &noteGroupSchema{Path: path})
		return err
	}); err != nil {
		return fmt.Errorf("store: delete Note Group: %w", err)
	}
	if changed == 0 {
		return appvault.ErrNotFound
	}
	s.vaultChanged()
	return nil
}
