package store

import (
	"context"
	"fmt"

	appvault "github.com/mslxl/natcatty/application/vault"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"gorm.io/gorm"
)

// ListIdentities returns every Identity in display order.
func (s *Store) ListIdentities(ctx context.Context) ([]domainvault.Identity, error) {
	var data []string
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		var err error
		data, err = loadDataRows(tx, &identitySchema{}, "order_key ASC", "label COLLATE NOCASE ASC", "id ASC")
		return err
	}); err != nil {
		return nil, fmt.Errorf("store: list Identities: %w", err)
	}
	identities, err := decodeDataList[domainvault.Identity](data, "Identity")
	if err != nil {
		return nil, err
	}
	return identities, nil
}

// GetIdentity returns one Identity.
func (s *Store) GetIdentity(ctx context.Context, id model.IdentityID) (domainvault.Identity, error) {
	var data string
	var found bool
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		var err error
		data, found, err = loadDataRow(tx, &identitySchema{}, &identitySchema{ID: string(id)})
		return err
	}); err != nil {
		return domainvault.Identity{}, fmt.Errorf("store: get Identity: %w", err)
	}
	if !found {
		return domainvault.Identity{}, appvault.ErrNotFound
	}
	var identity domainvault.Identity
	if err := decodeData(data, "Identity", &identity); err != nil {
		return domainvault.Identity{}, err
	}
	return identity, nil
}

// SaveIdentity inserts or replaces one Identity aggregate.
func (s *Store) SaveIdentity(ctx context.Context, identity domainvault.Identity) error {
	model, err := identityModel(identity)
	if err != nil {
		return err
	}
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		return upsertModel(tx, &model, "id", "label", "username", "auth_method", "key_id", "order_key", "updated_at", "data")
	}); err != nil {
		return fmt.Errorf("store: save Identity: %w", err)
	}
	s.vaultChanged()
	return nil
}

// DeleteIdentity removes one Identity and nothing else.
func (s *Store) DeleteIdentity(ctx context.Context, id model.IdentityID) error {
	var changed int64
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		var err error
		changed, err = deleteModel(tx, &identitySchema{}, &identitySchema{ID: string(id)})
		return err
	}); err != nil {
		return fmt.Errorf("store: delete Identity: %w", err)
	}
	if changed == 0 {
		return appvault.ErrNotFound
	}
	s.vaultChanged()
	return nil
}
