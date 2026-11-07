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
	if retention.MaxAge <= 0 && retention.Count <= 0 {
		return 0, nil
	}
	var removed int64
	if err := s.db.ORMTransaction(ctx, func(tx *gorm.DB) error {
		if retention.MaxAge > 0 {
			result := tx.Where("started_at < ?", now.Add(-retention.MaxAge).UnixMilli()).
				Delete(&connectionLogSchema{})
			if result.Error != nil {
				return result.Error
			}
			removed += result.RowsAffected
		}
		if retention.Count > 0 {
			var keep []struct {
				ID string `gorm:"column:id"`
			}
			if err := tx.Model(&connectionLogSchema{}).Select("id").
				Order("started_at DESC").Order("id DESC").Limit(retention.Count).
				Find(&keep).Error; err != nil {
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
			result := query.Delete(&connectionLogSchema{})
			if result.Error != nil {
				return result.Error
			}
			removed += result.RowsAffected
		}
		return nil
	}); err != nil {
		return 0, fmt.Errorf("store: prune connection logs: %w", err)
	}
	return int(removed), nil
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

// historyEntryColumns is the projection update list shared by the write path.
var historyEntryColumns = []string{
	"session_id", "session_uuid", "host_id", "host_label", "scope", "command",
	"cwd", "shell", "source", "recorded_at", "finished_at", "duration",
	"exit_code", "has_exit_code", "origin", "author", "author_kind", "intent",
	"deleted_at", "capture_tier", "data",
}

// RecordHistoryEntry writes one recorded command and, when record is not nil,
// its create record in one transaction. A failed insert on either side leaves
// both unchanged.
func (s *Store) RecordHistoryEntry(ctx context.Context, entry domainhistory.Entry, record *domainhistory.Record) error {
	if record != nil && (record.HistoryID != entry.ID || record.Kind != domainhistory.RecordKindCreate) {
		return fmt.Errorf("store: create record does not belong to entry %q", entry.ID)
	}
	if err := s.db.ORMTransaction(ctx, func(tx *gorm.DB) error {
		return writeHistoryEntry(tx, entry, record)
	}); err != nil {
		return fmt.Errorf("store: record history entry: %w", err)
	}
	return nil
}

// writeHistoryEntry writes the projection and, when record is not nil, its
// record inside the caller's transaction. The projection and the record log
// commit or roll back together.
func writeHistoryEntry(tx *gorm.DB, entry domainhistory.Entry, record *domainhistory.Record) error {
	model, err := historyEntryModel(entry)
	if err != nil {
		return err
	}
	if err := upsertModel(tx, &model, "id", historyEntryColumns...); err != nil {
		return err
	}
	if record == nil {
		return nil
	}
	return appendHistoryRecord(tx, *record)
}

// HistoryEntry returns one recorded command, including a soft-deleted row.
// found is false when the identity is unknown.
func (s *Store) HistoryEntry(ctx context.Context, id string) (domainhistory.Entry, bool, error) {
	var entry domainhistory.Entry
	var found bool
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		var err error
		entry, found, err = loadHistoryEntry(tx, id)
		return err
	}); err != nil {
		return domainhistory.Entry{}, false, fmt.Errorf("store: read history entry: %w", err)
	}
	return entry, found, nil
}

// loadHistoryEntry reads and decodes one row, including a soft-deleted one.
func loadHistoryEntry(tx *gorm.DB, id string) (domainhistory.Entry, bool, error) {
	data, found, err := loadDataRow(tx, &historyEntrySchema{}, &historyEntrySchema{ID: id})
	if err != nil || !found {
		return domainhistory.Entry{}, found, err
	}
	var entry domainhistory.Entry
	if err := decodeData(data, "history entry", &entry); err != nil {
		return domainhistory.Entry{}, false, err
	}
	return entry, true, nil
}

// ListHistoryEntries returns the live recorded commands newest first.
// Soft-deleted rows are excluded; they remain readable through HistoryEntry.
func (s *Store) ListHistoryEntries(ctx context.Context) ([]domainhistory.Entry, error) {
	return s.listHistoryEntries(ctx, false)
}

// ListHistoryEntriesIncludingDeleted returns every recorded command newest
// first. The one-time Shell History File import uses it to deduplicate against
// soft-deleted rows so a deleted command is not resurrected.
func (s *Store) ListHistoryEntriesIncludingDeleted(ctx context.Context) ([]domainhistory.Entry, error) {
	return s.listHistoryEntries(ctx, true)
}

func (s *Store) listHistoryEntries(ctx context.Context, includeDeleted bool) ([]domainhistory.Entry, error) {
	condition := "deleted_at = 0"
	if includeDeleted {
		condition = ""
	}
	var data []string
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		var err error
		data, err = loadDataRowsWhere(tx, &historyEntrySchema{}, condition,
			"recorded_at DESC", "id DESC")
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

// SoftDeleteHistoryEntry marks one recorded command deleted and, when record
// is not nil, appends its delete record in the same transaction. A legacy
// non-UUID row is passed a nil record by the service and stays local-only. A
// missing or already-deleted row is a no-op, so a repeated delete appends no
// second tombstone.
func (s *Store) SoftDeleteHistoryEntry(ctx context.Context, id string, deletedAt time.Time, record *domainhistory.Record) error {
	if deletedAt.IsZero() {
		return fmt.Errorf("store: delete history entry: zero deletion time")
	}
	if err := s.db.ORMTransaction(ctx, func(tx *gorm.DB) error {
		entry, found, err := loadHistoryEntry(tx, id)
		if err != nil {
			return err
		}
		if !found || !entry.DeletedAt.IsZero() {
			return nil
		}
		entry.DeletedAt = deletedAt
		encoded, err := marshalData(entry, "history entry")
		if err != nil {
			return err
		}
		result := tx.Model(&historyEntrySchema{}).Where("id = ?", id).
			Updates(map[string]any{"deleted_at": timestampMilli(deletedAt), "data": encoded})
		if result.Error != nil {
			return result.Error
		}
		if record == nil {
			return nil
		}
		if record.HistoryID != id || record.Kind != domainhistory.RecordKindDelete {
			return fmt.Errorf("store: delete record for %q does not match entry %q", record.HistoryID, id)
		}
		return appendHistoryRecord(tx, *record)
	}); err != nil {
		return fmt.Errorf("store: delete history entry: %w", err)
	}
	return nil
}

// ClearHistoryEntries removes every recorded command and reports the count.
// Bulk soft-delete and tombstone semantics belong to the History actions
// ticket; this method keeps the projection's existing clear behaviour.
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
