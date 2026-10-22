package store

import (
	"context"
	"encoding/json"
	"fmt"

	appvault "github.com/mslxl/natcatty/application/vault"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// ListIdentities returns every Identity in display order.
func (s *Store) ListIdentities(ctx context.Context) ([]domainvault.Identity, error) {
	rows, err := s.db.Query(ctx, `SELECT data FROM identities
		ORDER BY order_key ASC, label COLLATE NOCASE ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("store: list Identities: %w", err)
	}
	identities := make([]domainvault.Identity, 0, len(rows.Values))
	for _, row := range rows.Values {
		if len(row) == 0 {
			continue
		}
		data, ok := row[0].(string)
		if !ok {
			return nil, fmt.Errorf("store: Identity data is %T", row[0])
		}
		var identity domainvault.Identity
		if err := json.Unmarshal([]byte(data), &identity); err != nil {
			return nil, fmt.Errorf("store: decode Identity: %w", err)
		}
		identities = append(identities, identity)
	}
	return identities, nil
}

// GetIdentity returns one Identity.
func (s *Store) GetIdentity(ctx context.Context, id model.IdentityID) (domainvault.Identity, error) {
	rows, err := s.db.Query(ctx, `SELECT data FROM identities WHERE id = ?`, string(id))
	if err != nil {
		return domainvault.Identity{}, fmt.Errorf("store: get Identity: %w", err)
	}
	if len(rows.Values) == 0 {
		return domainvault.Identity{}, appvault.ErrNotFound
	}
	data, ok := rows.Values[0][0].(string)
	if !ok {
		return domainvault.Identity{}, fmt.Errorf("store: Identity data is %T", rows.Values[0][0])
	}
	var identity domainvault.Identity
	if err := json.Unmarshal([]byte(data), &identity); err != nil {
		return domainvault.Identity{}, fmt.Errorf("store: decode Identity: %w", err)
	}
	return identity, nil
}

// SaveIdentity inserts or replaces one Identity aggregate. Password ciphertext
// is stored separately and is never part of this record.
func (s *Store) SaveIdentity(ctx context.Context, identity domainvault.Identity) error {
	if identity.ID == "" || identity.Label == "" {
		return fmt.Errorf("store: save Identity: missing identity")
	}
	data, err := json.Marshal(identity)
	if err != nil {
		return fmt.Errorf("store: encode Identity: %w", err)
	}
	_, err = s.db.Execute(ctx, `INSERT INTO identities (
			id, label, username, auth_method, key_id, order_key,
			created_at, updated_at, data
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			label = excluded.label,
			username = excluded.username,
			auth_method = excluded.auth_method,
			key_id = excluded.key_id,
			order_key = excluded.order_key,
			updated_at = excluded.updated_at,
			data = excluded.data`,
		string(identity.ID),
		identity.Label,
		identity.Username,
		string(identity.AuthMethod),
		string(identity.KeyID),
		identity.OrderKey,
		identity.CreatedAt.UnixMilli(),
		identity.UpdatedAt.UnixMilli(),
		string(data),
	)
	if err != nil {
		return fmt.Errorf("store: save Identity: %w", err)
	}
	s.vaultChanged()
	return nil
}

// DeleteIdentity removes one Identity and nothing else.
func (s *Store) DeleteIdentity(ctx context.Context, id model.IdentityID) error {
	result, err := s.db.Execute(ctx, `DELETE FROM identities WHERE id = ?`, string(id))
	if err != nil {
		return fmt.Errorf("store: delete Identity: %w", err)
	}
	if result.Changes == 0 {
		return appvault.ErrNotFound
	}
	s.vaultChanged()
	return nil
}
