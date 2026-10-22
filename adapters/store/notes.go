package store

import (
	"context"
	"encoding/json"
	"fmt"

	appvault "github.com/mslxl/natcatty/application/vault"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// ListNotes returns every Vault Note in display order.
func (s *Store) ListNotes(ctx context.Context) ([]domainvault.Note, error) {
	rows, err := s.db.Query(ctx, `SELECT data FROM vault_notes
		ORDER BY order_key ASC, title COLLATE NOCASE ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("store: list Vault Notes: %w", err)
	}
	notes := make([]domainvault.Note, 0, len(rows.Values))
	for _, row := range rows.Values {
		if len(row) == 0 {
			continue
		}
		data, ok := row[0].(string)
		if !ok {
			return nil, fmt.Errorf("store: Vault Note data is %T", row[0])
		}
		var note domainvault.Note
		if err := json.Unmarshal([]byte(data), &note); err != nil {
			return nil, fmt.Errorf("store: decode Vault Note: %w", err)
		}
		notes = append(notes, note)
	}
	return notes, nil
}

// GetNote returns one Vault Note by id.
func (s *Store) GetNote(ctx context.Context, id string) (domainvault.Note, error) {
	rows, err := s.db.Query(ctx, `SELECT data FROM vault_notes WHERE id = ?`, id)
	if err != nil {
		return domainvault.Note{}, fmt.Errorf("store: get Vault Note: %w", err)
	}
	if len(rows.Values) == 0 {
		return domainvault.Note{}, appvault.ErrNotFound
	}
	data, ok := rows.Values[0][0].(string)
	if !ok {
		return domainvault.Note{}, fmt.Errorf("store: Vault Note data is %T", rows.Values[0][0])
	}
	var note domainvault.Note
	if err := json.Unmarshal([]byte(data), &note); err != nil {
		return domainvault.Note{}, fmt.Errorf("store: decode Vault Note: %w", err)
	}
	return note, nil
}

// SaveNote inserts or replaces one Vault Note.
func (s *Store) SaveNote(ctx context.Context, note domainvault.Note) error {
	if note.ID == "" {
		return fmt.Errorf("store: Vault Note has no id")
	}
	data, err := json.Marshal(note)
	if err != nil {
		return fmt.Errorf("store: encode Vault Note: %w", err)
	}
	_, err = s.db.Execute(ctx, `INSERT INTO vault_notes (
			id, title, group_path, order_key, created_at, updated_at, data
		) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			title = excluded.title,
			group_path = excluded.group_path,
			order_key = excluded.order_key,
			updated_at = excluded.updated_at,
			data = excluded.data`,
		note.ID,
		note.Title,
		note.Group,
		note.OrderKey,
		note.CreatedAt.UnixMilli(),
		note.UpdatedAt.UnixMilli(),
		string(data),
	)
	if err != nil {
		return fmt.Errorf("store: save Vault Note: %w", err)
	}
	s.vaultChanged()
	return nil
}

// DeleteNote removes one Vault Note.
func (s *Store) DeleteNote(ctx context.Context, id string) error {
	result, err := s.db.Execute(ctx, `DELETE FROM vault_notes WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete Vault Note: %w", err)
	}
	if result.Changes == 0 {
		return appvault.ErrNotFound
	}
	s.vaultChanged()
	return nil
}

// ListNoteGroups returns every saved Note Group in display order.
func (s *Store) ListNoteGroups(ctx context.Context) ([]domainvault.NoteGroup, error) {
	rows, err := s.db.Query(ctx, `SELECT data FROM note_groups
		ORDER BY order_key ASC, path COLLATE NOCASE ASC`)
	if err != nil {
		return nil, fmt.Errorf("store: list Note Groups: %w", err)
	}
	groups := make([]domainvault.NoteGroup, 0, len(rows.Values))
	for _, row := range rows.Values {
		if len(row) == 0 {
			continue
		}
		data, ok := row[0].(string)
		if !ok {
			return nil, fmt.Errorf("store: Note Group data is %T", row[0])
		}
		var group domainvault.NoteGroup
		if err := json.Unmarshal([]byte(data), &group); err != nil {
			return nil, fmt.Errorf("store: decode Note Group: %w", err)
		}
		groups = append(groups, group)
	}
	return groups, nil
}

// SaveNoteGroup inserts or replaces one Note Group.
func (s *Store) SaveNoteGroup(ctx context.Context, group domainvault.NoteGroup) error {
	if group.Path == "" {
		return fmt.Errorf("store: Note Group has no path")
	}
	data, err := json.Marshal(group)
	if err != nil {
		return fmt.Errorf("store: encode Note Group: %w", err)
	}
	_, err = s.db.Execute(ctx, `INSERT INTO note_groups (
			path, order_key, created_at, updated_at, data
		) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET
			order_key = excluded.order_key,
			updated_at = excluded.updated_at,
			data = excluded.data`,
		group.Path,
		group.OrderKey,
		group.CreatedAt.UnixMilli(),
		group.UpdatedAt.UnixMilli(),
		string(data),
	)
	if err != nil {
		return fmt.Errorf("store: save Note Group: %w", err)
	}
	s.vaultChanged()
	return nil
}

// DeleteNoteGroup removes one Note Group. Notes that reference it are left
// untouched; the Vault service owns that non-cascading rule and the adapter
// never touches another table.
func (s *Store) DeleteNoteGroup(ctx context.Context, path string) error {
	result, err := s.db.Execute(ctx, `DELETE FROM note_groups WHERE path = ?`, path)
	if err != nil {
		return fmt.Errorf("store: delete Note Group: %w", err)
	}
	if result.Changes == 0 {
		return appvault.ErrNotFound
	}
	s.vaultChanged()
	return nil
}
