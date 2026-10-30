package store

import (
	"context"
	"fmt"
	"strings"

	appconnection "github.com/mslxl/natcatty/application/connection"
	appvault "github.com/mslxl/natcatty/application/vault"
	domainconn "github.com/mslxl/natcatty/domain/connection"
	"gorm.io/gorm"
)

var (
	_ appconnection.KnownHostRepository = (*Store)(nil)
	_ appvault.KnownHostRepository      = (*Store)(nil)
)

// ListKnownHosts returns every Known Host. A corrupted JSON tail is skipped.
func (s *Store) ListKnownHosts(ctx context.Context) ([]domainconn.KnownHost, error) {
	var data []string
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		var err error
		data, err = loadDataRows(tx, &knownHostSchema{}, "hostname COLLATE NOCASE ASC", "port ASC", "key_type ASC", "id ASC")
		return err
	}); err != nil {
		return nil, fmt.Errorf("store: list Known Hosts: %w", err)
	}
	records := make([]domainconn.KnownHost, 0, len(data))
	for _, item := range data {
		var record domainconn.KnownHost
		if decodeData(item, "Known Host", &record) == nil {
			records = append(records, record)
		}
	}
	return records, nil
}

// DeleteKnownHost removes one trust record. Deleting a Known Host never
// touches a Host.
func (s *Store) DeleteKnownHost(ctx context.Context, id string) error {
	var changed int64
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		var err error
		changed, err = deleteModel(tx, &knownHostSchema{}, &knownHostSchema{ID: id})
		return err
	}); err != nil {
		return fmt.Errorf("store: delete Known Host: %w", err)
	}
	if changed == 0 {
		return appvault.ErrNotFound
	}
	s.vaultChanged()
	return nil
}

// UpsertKnownHost inserts a trust record or updates the record with the same
// id or endpoint selector. Identity and discovery time are preserved on a
// rotation.
func (s *Store) UpsertKnownHost(ctx context.Context, record domainconn.KnownHost) error {
	if record.ID == "" {
		return fmt.Errorf("store: Known Host has no id")
	}
	if record.Port == 0 {
		record.Port = 22
	}
	if record.LastSeen.IsZero() {
		record.LastSeen = record.DiscoveredAt
	}
	if record.DiscoveredAt.IsZero() {
		record.DiscoveredAt = record.LastSeen
	}
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		var existing knownHostSchema
		query := tx.Where(&knownHostSchema{ID: record.ID}).First(&existing)
		if query.Error == gorm.ErrRecordNotFound {
			var candidates []knownHostSchema
			query = tx.Where(&knownHostSchema{Port: int64(record.Port), KeyType: record.KeyType}).Find(&candidates)
			for _, candidate := range candidates {
				if strings.EqualFold(candidate.Hostname, record.Hostname) {
					existing = candidate
					query.Error = nil
					break
				}
			}
		}
		if query.Error == nil {
			var prior domainconn.KnownHost
			if decodeData(existing.Data, "Known Host", &prior) == nil {
				record.ID = prior.ID
				record.DiscoveredAt = prior.DiscoveredAt
				if record.ConvertedToHostID == "" {
					record.ConvertedToHostID = prior.ConvertedToHostID
				}
			}
		} else if query.Error != gorm.ErrRecordNotFound {
			return query.Error
		}
		model, err := knownHostModel(record)
		if err != nil {
			return err
		}
		return upsertModel(tx, &model, "id", "hostname", "port", "key_type", "fingerprint", "public_key", "last_seen", "converted_to_host_id", "order_key", "data")
	}); err != nil {
		return fmt.Errorf("store: save Known Host: %w", err)
	}
	s.vaultChanged()
	return nil
}
