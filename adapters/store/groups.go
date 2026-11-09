package store

import (
	"context"
	"fmt"

	appvault "github.com/mslxl/natcatty/application/vault"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"gorm.io/gorm"
)

// ListGroups returns every Host Group with the path its tree position derives,
// in display order.
func (s *Store) ListGroups(ctx context.Context) ([]domainvault.Group, error) {
	var data []string
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		var err error
		data, err = loadDataRows(tx, &hostGroupSchema{}, "order_key ASC", "name COLLATE NOCASE ASC", "id ASC")
		return err
	}); err != nil {
		return nil, fmt.Errorf("store: list Host Groups: %w", err)
	}
	return decodeGroupRows(data)
}

// GetGroup returns one Host Group by its stable ID. A group is located through
// the whole tree because its path is derived.
func (s *Store) GetGroup(ctx context.Context, id string) (domainvault.Group, error) {
	groups, err := s.ListGroups(ctx)
	if err != nil {
		return domainvault.Group{}, err
	}
	for _, group := range groups {
		if group.ID == id {
			return group, nil
		}
	}
	return domainvault.Group{}, appvault.ErrNotFound
}

// SaveGroup inserts or replaces one Host Group, keyed by its stable ID. A
// rename or a move updates this row alone; no member Host is rewritten.
func (s *Store) SaveGroup(ctx context.Context, group domainvault.Group) error {
	model, err := groupModel(group)
	if err != nil {
		return err
	}
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		return upsertModel(tx, &model, "id",
			"parent_id", "name",
			"notes", "username", "port", "protocol", "auth_method", "identity_id", "key_id",
			"use_ssh_agent", "requires_mfa", "legacy_algorithms", "skip_ecdsa_host_key",
			"proxy_profile_id", "order_key", "updated_at", "data")
	}); err != nil {
		return fmt.Errorf("store: save Host Group: %w", err)
	}
	s.vaultChanged()
	return nil
}

// DeleteGroup removes one Host Group by ID. Hosts and descendant groups are not
// cascaded: the Vault service decides what happens to them.
func (s *Store) DeleteGroup(ctx context.Context, id string) error {
	var changed int64
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		var err error
		changed, err = deleteModel(tx, &hostGroupSchema{}, &hostGroupSchema{ID: id})
		return err
	}); err != nil {
		return fmt.Errorf("store: delete Host Group: %w", err)
	}
	if changed == 0 {
		return appvault.ErrNotFound
	}
	s.vaultChanged()
	return nil
}
