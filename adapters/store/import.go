package store

import (
	"context"
	"fmt"

	appvault "github.com/mslxl/natcatty/application/vault"
	"gorm.io/gorm"
)

var _ appvault.ImportStore = (*Store)(nil)

// SaveImportBatch writes Host Groups and Hosts in one ORM transaction. Models
// are built before the transaction so invalid records leave the database untouched.
func (s *Store) SaveImportBatch(ctx context.Context, batch appvault.ImportBatch) error {
	if len(batch.Groups) == 0 && len(batch.Hosts) == 0 {
		return nil
	}
	groups := make([]hostGroupSchema, 0, len(batch.Groups))
	for _, group := range batch.Groups {
		model, err := groupModel(group)
		if err != nil {
			return err
		}
		groups = append(groups, model)
	}
	hosts := make([]hostSchema, 0, len(batch.Hosts))
	for _, host := range batch.Hosts {
		model, err := hostModel(host)
		if err != nil {
			return err
		}
		hosts = append(hosts, model)
	}
	if err := s.db.ORMTransaction(ctx, func(tx *gorm.DB) error {
		for index := range groups {
			if err := upsertModel(tx, &groups[index], "path",
				"notes", "username", "port", "protocol", "auth_method", "identity_id", "key_id",
				"use_ssh_agent", "requires_mfa", "legacy_algorithms", "skip_ecdsa_host_key",
				"proxy_profile_id", "order_key", "updated_at", "data"); err != nil {
				return err
			}
		}
		for index := range hosts {
			if err := upsertModel(tx, &hosts[index], "id",
				"label", "hostname", "port", "username", "protocol", "group_path",
				"pinned", "order_key", "updated_at", "data"); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("store: save import batch: %w", err)
	}
	s.vaultChanged()
	return nil
}
