package store

import (
	"context"
	"fmt"

	appvault "github.com/mslxl/natcatty/application/vault"
	"github.com/mslxl/natcatty/domain/model"
	"github.com/mslxl/natcatty/domain/model/plugindata"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"gorm.io/gorm"
)

// List returns every Host, pinned Hosts first, then each Host Group's order.
func (s *Store) List(ctx context.Context) ([]domainvault.Host, error) {
	var data []string
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		var err error
		data, err = loadDataRows(tx, &hostSchema{}, "pinned DESC", "order_key ASC", "label COLLATE NOCASE ASC", "id ASC")
		return err
	}); err != nil {
		return nil, fmt.Errorf("store: list Hosts: %w", err)
	}
	hosts, err := decodeDataList[domainvault.Host](data, "Host")
	if err != nil {
		return nil, err
	}
	return s.materializeHostGroups(ctx, hosts)
}

// materializeHostGroups fills each Host's display path from the group tree.
// Storage keeps only the stable GroupID, so this is the one place a path is
// produced for a Host read from disk.
func (s *Store) materializeHostGroups(ctx context.Context, hosts []domainvault.Host) ([]domainvault.Host, error) {
	if len(hosts) == 0 {
		return hosts, nil
	}
	groups, err := s.ListGroups(ctx)
	if err != nil {
		return nil, err
	}
	for index := range hosts {
		hosts[index] = domainvault.MaterializeHostGroup(hosts[index], groups)
	}
	return hosts, nil
}

// Get returns one Host.
func (s *Store) Get(ctx context.Context, id model.HostID) (domainvault.Host, error) {
	var data string
	var found bool
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		var err error
		data, found, err = loadDataRow(tx, &hostSchema{}, &hostSchema{ID: string(id)})
		return err
	}); err != nil {
		return domainvault.Host{}, fmt.Errorf("store: get Host: %w", err)
	}
	if !found {
		return domainvault.Host{}, appvault.ErrNotFound
	}
	var host domainvault.Host
	if err := decodeData(data, "Host", &host); err != nil {
		return domainvault.Host{}, err
	}
	hosts, err := s.materializeHostGroups(ctx, []domainvault.Host{host})
	if err != nil {
		return domainvault.Host{}, err
	}
	return hosts[0], nil
}

// Save inserts or replaces one Host aggregate. The indexed columns are
// projections of the JSON fields and are written through the ORM together.
func (s *Store) Save(ctx context.Context, host domainvault.Host) error {
	host, quarantined, err := sanitizeHostPluginData(host)
	if err != nil {
		return fmt.Errorf("store: save Host: %w", err)
	}
	model, err := hostModel(host)
	if err != nil {
		return err
	}
	if err := s.db.ORMTransaction(ctx, func(tx *gorm.DB) error {
		if err := upsertModel(tx, &model, "id",
			"label", "hostname", "port", "username", "protocol", "group_id",
			"pinned", "order_key", "updated_at", "data"); err != nil {
			return err
		}
		return saveQuarantines(tx, plugindata.RecordHost, string(host.ID), host.PluginData, quarantined)
	}); err != nil {
		return fmt.Errorf("store: save Host: %w", err)
	}
	s.vaultChanged()
	return nil
}

// Delete removes one Host and nothing else. Vault references are soft, as in
// the original, so no related record is cascaded.
func (s *Store) Delete(ctx context.Context, id model.HostID) error {
	var changed int64
	if err := s.db.ORMTransaction(ctx, func(tx *gorm.DB) error {
		var err error
		changed, err = deleteModel(tx, &hostSchema{}, &hostSchema{ID: string(id)})
		if err != nil {
			return err
		}
		return tx.Where("record_kind = ? AND record_id = ?",
			string(plugindata.RecordHost), string(id)).
			Delete(&pluginDataQuarantineSchema{}).Error
	}); err != nil {
		return fmt.Errorf("store: delete Host: %w", err)
	}
	if changed == 0 {
		return appvault.ErrNotFound
	}
	s.vaultChanged()
	return nil
}
