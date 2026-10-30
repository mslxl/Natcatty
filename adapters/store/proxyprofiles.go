package store

import (
	"context"
	"fmt"

	appvault "github.com/mslxl/natcatty/application/vault"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"gorm.io/gorm"
)

// ListProxyProfiles returns every Proxy Profile in display order.
func (s *Store) ListProxyProfiles(ctx context.Context) ([]domainvault.ProxyProfile, error) {
	var data []string
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		var err error
		data, err = loadDataRows(tx, &proxyProfileSchema{}, "order_key ASC", "label COLLATE NOCASE ASC", "id ASC")
		return err
	}); err != nil {
		return nil, fmt.Errorf("store: list Proxy Profiles: %w", err)
	}
	profiles, err := decodeDataList[domainvault.ProxyProfile](data, "Proxy Profile")
	if err != nil {
		return nil, err
	}
	return profiles, nil
}

// GetProxyProfile returns one Proxy Profile by id.
func (s *Store) GetProxyProfile(ctx context.Context, id string) (domainvault.ProxyProfile, error) {
	var data string
	var found bool
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		var err error
		data, found, err = loadDataRow(tx, &proxyProfileSchema{}, &proxyProfileSchema{ID: id})
		return err
	}); err != nil {
		return domainvault.ProxyProfile{}, fmt.Errorf("store: get Proxy Profile: %w", err)
	}
	if !found {
		return domainvault.ProxyProfile{}, appvault.ErrNotFound
	}
	var profile domainvault.ProxyProfile
	if err := decodeData(data, "Proxy Profile", &profile); err != nil {
		return domainvault.ProxyProfile{}, err
	}
	return profile, nil
}

// SaveProxyProfile inserts or replaces one Proxy Profile.
func (s *Store) SaveProxyProfile(ctx context.Context, profile domainvault.ProxyProfile) error {
	model, err := proxyProfileModel(profile)
	if err != nil {
		return err
	}
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		return upsertModel(tx, &model, "id", "label", "type", "host", "port", "order_key", "updated_at", "data")
	}); err != nil {
		return fmt.Errorf("store: save Proxy Profile: %w", err)
	}
	s.vaultChanged()
	return nil
}

// DeleteProxyProfile removes one Proxy Profile. References are cleared by the
// Vault service, which owns that non-cascading rule.
func (s *Store) DeleteProxyProfile(ctx context.Context, id string) error {
	var changed int64
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		var err error
		changed, err = deleteModel(tx, &proxyProfileSchema{}, &proxyProfileSchema{ID: id})
		return err
	}); err != nil {
		return fmt.Errorf("store: delete Proxy Profile: %w", err)
	}
	if changed == 0 {
		return appvault.ErrNotFound
	}
	s.vaultChanged()
	return nil
}
