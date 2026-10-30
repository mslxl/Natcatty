package store

import (
	"context"
	"fmt"

	appvault "github.com/mslxl/natcatty/application/vault"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"gorm.io/gorm"
)

// ListGroups returns every Host Group in display order.
func (s *Store) ListGroups(ctx context.Context) ([]domainvault.Group, error) {
	var data []string
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		var err error
		data, err = loadDataRows(tx, &hostGroupSchema{}, "order_key ASC", "path COLLATE NOCASE ASC")
		return err
	}); err != nil {
		return nil, fmt.Errorf("store: list Host Groups: %w", err)
	}
	groups, err := decodeDataList[domainvault.Group](data, "Host Group")
	if err != nil {
		return nil, err
	}
	return groups, nil
}

// GetGroup returns one Host Group by path.
func (s *Store) GetGroup(ctx context.Context, path string) (domainvault.Group, error) {
	var data string
	var found bool
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		var err error
		data, found, err = loadDataRow(tx, &hostGroupSchema{}, &hostGroupSchema{Path: path})
		return err
	}); err != nil {
		return domainvault.Group{}, fmt.Errorf("store: get Host Group: %w", err)
	}
	if !found {
		return domainvault.Group{}, appvault.ErrNotFound
	}
	var group domainvault.Group
	if err := decodeData(data, "Host Group", &group); err != nil {
		return domainvault.Group{}, err
	}
	return group, nil
}

// SaveGroup inserts or replaces one Host Group.
func (s *Store) SaveGroup(ctx context.Context, group domainvault.Group) error {
	model, err := groupModel(group)
	if err != nil {
		return err
	}
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		return upsertModel(tx, &model, "path",
			"notes", "username", "port", "protocol", "auth_method", "identity_id", "key_id",
			"use_ssh_agent", "requires_mfa", "legacy_algorithms", "skip_ecdsa_host_key",
			"proxy_profile_id", "order_key", "updated_at", "data")
	}); err != nil {
		return fmt.Errorf("store: save Host Group: %w", err)
	}
	s.vaultChanged()
	return nil
}

// DeleteGroup removes one Host Group. Hosts and descendant groups are not
// cascaded: the Vault service decides what happens to them.
func (s *Store) DeleteGroup(ctx context.Context, path string) error {
	var changed int64
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		var err error
		changed, err = deleteModel(tx, &hostGroupSchema{}, &hostGroupSchema{Path: path})
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
