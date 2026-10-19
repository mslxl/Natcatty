package store

import (
	"context"
	"encoding/json"
	"fmt"

	appvault "github.com/mslxl/natcatty/application/vault"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// ListKeys returns every SSH Key in display order.
func (s *Store) ListKeys(ctx context.Context) ([]domainvault.Key, error) {
	rows, err := s.db.Query(ctx, `SELECT data FROM ssh_keys
		ORDER BY order_key ASC, label COLLATE NOCASE ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("store: list SSH Keys: %w", err)
	}
	keys := make([]domainvault.Key, 0, len(rows.Values))
	for _, row := range rows.Values {
		if len(row) == 0 {
			continue
		}
		data, ok := row[0].(string)
		if !ok {
			return nil, fmt.Errorf("store: SSH Key data is %T", row[0])
		}
		var key domainvault.Key
		if err := json.Unmarshal([]byte(data), &key); err != nil {
			return nil, fmt.Errorf("store: decode SSH Key: %w", err)
		}
		keys = append(keys, key)
	}
	return keys, nil
}

// GetKey returns one SSH Key.
func (s *Store) GetKey(ctx context.Context, id model.KeyID) (domainvault.Key, error) {
	rows, err := s.db.Query(ctx, `SELECT data FROM ssh_keys WHERE id = ?`, string(id))
	if err != nil {
		return domainvault.Key{}, fmt.Errorf("store: get SSH Key: %w", err)
	}
	if len(rows.Values) == 0 {
		return domainvault.Key{}, appvault.ErrNotFound
	}
	data, ok := rows.Values[0][0].(string)
	if !ok {
		return domainvault.Key{}, fmt.Errorf("store: SSH Key data is %T", rows.Values[0][0])
	}
	var key domainvault.Key
	if err := json.Unmarshal([]byte(data), &key); err != nil {
		return domainvault.Key{}, fmt.Errorf("store: decode SSH Key: %w", err)
	}
	return key, nil
}

// SaveKey inserts or replaces one SSH Key aggregate. Credential ciphertext is
// stored separately and is never part of this record.
func (s *Store) SaveKey(ctx context.Context, key domainvault.Key) error {
	if key.ID == "" || key.Label == "" {
		return fmt.Errorf("store: save SSH Key: missing identity")
	}
	data, err := json.Marshal(key)
	if err != nil {
		return fmt.Errorf("store: encode SSH Key: %w", err)
	}
	_, err = s.db.Execute(ctx, `INSERT INTO ssh_keys (
			id, label, type, source, order_key, created_at, updated_at, data
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			label = excluded.label,
			type = excluded.type,
			source = excluded.source,
			order_key = excluded.order_key,
			updated_at = excluded.updated_at,
			data = excluded.data`,
		string(key.ID),
		key.Label,
		string(key.Type),
		string(key.Source),
		key.OrderKey,
		key.CreatedAt.UnixMilli(),
		key.UpdatedAt.UnixMilli(),
		string(data),
	)
	if err != nil {
		return fmt.Errorf("store: save SSH Key: %w", err)
	}
	return nil
}

// DeleteKey removes one SSH Key and nothing else.
func (s *Store) DeleteKey(ctx context.Context, id model.KeyID) error {
	result, err := s.db.Execute(ctx, `DELETE FROM ssh_keys WHERE id = ?`, string(id))
	if err != nil {
		return fmt.Errorf("store: delete SSH Key: %w", err)
	}
	if result.Changes == 0 {
		return appvault.ErrNotFound
	}
	return nil
}
