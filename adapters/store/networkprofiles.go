package store

import (
	"context"
	"fmt"

	appvault "github.com/mslxl/natcatty/application/vault"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"gorm.io/gorm"
)

// ListNetworkProfiles returns every Network Profile in display order.
func (s *Store) ListNetworkProfiles(ctx context.Context) ([]domainvault.NetworkProfile, error) {
	var data []string
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		var err error
		data, err = loadDataRows(tx, &networkProfileSchema{}, "order_key ASC", "label COLLATE NOCASE ASC", "id ASC")
		return err
	}); err != nil {
		return nil, fmt.Errorf("store: list Network Profiles: %w", err)
	}
	profiles, err := decodeDataList[domainvault.NetworkProfile](data, "Network Profile")
	if err != nil {
		return nil, err
	}
	return profiles, nil
}

// GetNetworkProfile returns one Network Profile by id.
func (s *Store) GetNetworkProfile(ctx context.Context, id string) (domainvault.NetworkProfile, error) {
	var data string
	var found bool
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		var err error
		data, found, err = loadDataRow(tx, &networkProfileSchema{}, &networkProfileSchema{ID: id})
		return err
	}); err != nil {
		return domainvault.NetworkProfile{}, fmt.Errorf("store: get Network Profile: %w", err)
	}
	if !found {
		return domainvault.NetworkProfile{}, appvault.ErrNotFound
	}
	var profile domainvault.NetworkProfile
	if err := decodeData(data, "Network Profile", &profile); err != nil {
		return domainvault.NetworkProfile{}, err
	}
	return profile, nil
}

// SaveNetworkProfile inserts or replaces one Network Profile. The record
// carries no credential plaintext; secrets live in the field-secret store.
func (s *Store) SaveNetworkProfile(ctx context.Context, profile domainvault.NetworkProfile) error {
	model, err := networkProfileModel(profile)
	if err != nil {
		return err
	}
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		return upsertModel(tx, &model, "id", "label", "kind", "order_key", "updated_at", "data")
	}); err != nil {
		return fmt.Errorf("store: save Network Profile: %w", err)
	}
	s.vaultChanged()
	return nil
}

// DeleteNetworkProfile removes one Network Profile. Field secrets are removed
// by the Vault service, which owns that rule.
func (s *Store) DeleteNetworkProfile(ctx context.Context, id string) error {
	var changed int64
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		var err error
		changed, err = deleteModel(tx, &networkProfileSchema{}, &networkProfileSchema{ID: id})
		return err
	}); err != nil {
		return fmt.Errorf("store: delete Network Profile: %w", err)
	}
	if changed == 0 {
		return appvault.ErrNotFound
	}
	s.vaultChanged()
	return nil
}
