package store

import (
	"context"
	"fmt"
	"time"

	apphistory "github.com/mslxl/natcatty/application/history"
	domainhistory "github.com/mslxl/natcatty/domain/history"
	"gorm.io/gorm"
)

var _ apphistory.Store = (*Store)(nil)

// SaveConnectionLog inserts or replaces one connection log.
func (s *Store) SaveConnectionLog(ctx context.Context, log domainhistory.ConnectionLog) error {
	model, err := connectionLogModel(log)
	if err != nil {
		return err
	}
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		return upsertModel(tx, &model, "id", "session_id", "host_id", "host_label", "hostname", "username", "kind", "status", "error", "started_at", "ended_at", "data")
	}); err != nil {
		return fmt.Errorf("store: save connection log: %w", err)
	}
	return nil
}

// ListConnectionLogs returns every connection log newest first.
func (s *Store) ListConnectionLogs(ctx context.Context) ([]domainhistory.ConnectionLog, error) {
	var data []string
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		var err error
		data, err = loadDataRows(tx, &connectionLogSchema{}, "started_at DESC", "id DESC")
		return err
	}); err != nil {
		return nil, fmt.Errorf("store: list connection logs: %w", err)
	}
	logs, err := decodeDataList[domainhistory.ConnectionLog](data, "connection log")
	if err != nil {
		return nil, err
	}
	return logs, nil
}

// DeleteConnectionLog removes one log idempotently.
func (s *Store) DeleteConnectionLog(ctx context.Context, id string) error {
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		_, err := deleteModel(tx, &connectionLogSchema{}, &connectionLogSchema{ID: id})
		return err
	}); err != nil {
		return fmt.Errorf("store: delete connection log: %w", err)
	}
	return nil
}

// PruneConnectionLogs deletes logs outside retention and reports how many it removed.
func (s *Store) PruneConnectionLogs(ctx context.Context, retention domainhistory.Retention, now time.Time) (int, error) {
	return s.pruneHistory(ctx, retention, now, &connectionLogSchema{}, "started_at", "connection logs")
}

// ClearConnectionLogs removes every connection log and reports the count.
func (s *Store) ClearConnectionLogs(ctx context.Context) (int, error) {
	var removed int64
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		result := tx.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&connectionLogSchema{})
		removed = result.RowsAffected
		return result.Error
	}); err != nil {
		return 0, fmt.Errorf("store: clear connection logs: %w", err)
	}
	return int(removed), nil
}

// SaveHistoryEntry inserts or replaces one recorded command.
func (s *Store) SaveHistoryEntry(ctx context.Context, entry domainhistory.Entry) error {
	model, err := historyEntryModel(entry)
	if err != nil {
		return err
	}
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		return upsertModel(tx, &model, "id", "session_id", "host_id", "host_label", "scope", "command", "source", "recorded_at", "data")
	}); err != nil {
		return fmt.Errorf("store: save history entry: %w", err)
	}
	return nil
}

// ListHistoryEntries returns every recorded command newest first.
func (s *Store) ListHistoryEntries(ctx context.Context) ([]domainhistory.Entry, error) {
	var data []string
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		var err error
		data, err = loadDataRows(tx, &historyEntrySchema{}, "recorded_at DESC", "id DESC")
		return err
	}); err != nil {
		return nil, fmt.Errorf("store: list history entries: %w", err)
	}
	entries, err := decodeDataList[domainhistory.Entry](data, "history entry")
	if err != nil {
		return nil, err
	}
	return entries, nil
}

// DeleteHistoryEntry removes one recorded command.
func (s *Store) DeleteHistoryEntry(ctx context.Context, id string) error {
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		_, err := deleteModel(tx, &historyEntrySchema{}, &historyEntrySchema{ID: id})
		return err
	}); err != nil {
		return fmt.Errorf("store: delete history entry: %w", err)
	}
	return nil
}

// ClearHistoryEntries removes every recorded command and reports the count.
func (s *Store) ClearHistoryEntries(ctx context.Context) (int, error) {
	var removed int64
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		result := tx.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&historyEntrySchema{})
		removed = result.RowsAffected
		return result.Error
	}); err != nil {
		return 0, fmt.Errorf("store: clear history entries: %w", err)
	}
	return int(removed), nil
}

// PruneHistoryEntries deletes commands outside retention and reports how many it removed.
func (s *Store) PruneHistoryEntries(ctx context.Context, retention domainhistory.Retention, now time.Time) (int, error) {
	return s.pruneHistory(ctx, retention, now, &historyEntrySchema{}, "recorded_at", "history entries")
}

func (s *Store) pruneHistory(ctx context.Context, retention domainhistory.Retention, now time.Time, model any, timeColumn, label string) (int, error) {
	if retention.MaxAge <= 0 && retention.Count <= 0 {
		return 0, nil
	}
	var removed int64
	if err := s.db.ORMTransaction(ctx, func(tx *gorm.DB) error {
		if retention.MaxAge > 0 {
			result := tx.Where(timeColumn+" < ?", now.Add(-retention.MaxAge).UnixMilli()).Delete(model)
			if result.Error != nil {
				return result.Error
			}
			removed += result.RowsAffected
		}
		if retention.Count > 0 {
			var keep []struct {
				ID string `gorm:"column:id"`
			}
			if err := tx.Model(model).Select("id").Order(timeColumn + " DESC").Order("id DESC").Limit(retention.Count).Find(&keep).Error; err != nil {
				return err
			}
			keepIDs := make([]string, 0, len(keep))
			for _, row := range keep {
				keepIDs = append(keepIDs, row.ID)
			}
			query := tx.Session(&gorm.Session{AllowGlobalUpdate: true})
			if len(keepIDs) > 0 {
				query = query.Where("id NOT IN ?", keepIDs)
			}
			result := query.Delete(model)
			if result.Error != nil {
				return result.Error
			}
			removed += result.RowsAffected
		}
		return nil
	}); err != nil {
		return 0, fmt.Errorf("store: prune %s: %w", label, err)
	}
	return int(removed), nil
}
