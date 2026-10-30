package store

import (
	"context"
	"fmt"

	appvault "github.com/mslxl/natcatty/application/vault"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"gorm.io/gorm"
)

// ListKeys returns every SSH Key in display order.
func (s *Store) ListKeys(ctx context.Context) ([]domainvault.Key, error) {
	var data []string
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		var err error
		data, err = loadDataRows(tx, &sshKeySchema{}, "order_key ASC", "label COLLATE NOCASE ASC", "id ASC")
		return err
	}); err != nil {
		return nil, fmt.Errorf("store: list SSH Keys: %w", err)
	}
	keys, err := decodeDataList[domainvault.Key](data, "SSH Key")
	if err != nil {
		return nil, err
	}
	return keys, nil
}

// GetKey returns one SSH Key.
func (s *Store) GetKey(ctx context.Context, id model.KeyID) (domainvault.Key, error) {
	var data string
	var found bool
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		var err error
		data, found, err = loadDataRow(tx, &sshKeySchema{}, &sshKeySchema{ID: string(id)})
		return err
	}); err != nil {
		return domainvault.Key{}, fmt.Errorf("store: get SSH Key: %w", err)
	}
	if !found {
		return domainvault.Key{}, appvault.ErrNotFound
	}
	var key domainvault.Key
	if err := decodeData(data, "SSH Key", &key); err != nil {
		return domainvault.Key{}, err
	}
	return key, nil
}

// SaveKey inserts or replaces one SSH Key aggregate.
func (s *Store) SaveKey(ctx context.Context, key domainvault.Key) error {
	model, err := keyModel(key)
	if err != nil {
		return err
	}
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		return upsertModel(tx, &model, "id", "label", "type", "source", "order_key", "updated_at", "data")
	}); err != nil {
		return fmt.Errorf("store: save SSH Key: %w", err)
	}
	s.vaultChanged()
	return nil
}

// DeleteKey removes one SSH Key and nothing else.
func (s *Store) DeleteKey(ctx context.Context, id model.KeyID) error {
	var changed int64
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		var err error
		changed, err = deleteModel(tx, &sshKeySchema{}, &sshKeySchema{ID: string(id)})
		return err
	}); err != nil {
		return fmt.Errorf("store: delete SSH Key: %w", err)
	}
	if changed == 0 {
		return appvault.ErrNotFound
	}
	s.vaultChanged()
	return nil
}
