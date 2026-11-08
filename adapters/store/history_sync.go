package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	historysync "github.com/mslxl/natcatty/application/historysync"
	domainhistory "github.com/mslxl/natcatty/domain/history"
	"gorm.io/gorm"
)

var _ historysync.SyncStore = (*Store)(nil)

// ApplyRemotePage appends one downloaded page to the record log, applies its
// projections, reindexes any local capture that would collide with a page
// record, and advances the series checkpoint in one transaction. A failed
// commit leaves the previous checkpoint and log intact.
func (s *Store) ApplyRemotePage(ctx context.Context, page historysync.RemotePage) error {
	checkpoint := page.Checkpoint
	if checkpoint.SeriesHost == "" || checkpoint.Tag == "" || checkpoint.AppliedIndex < 0 {
		return fmt.Errorf("store: apply remote page: invalid checkpoint")
	}
	for _, record := range page.Records {
		if !record.Valid() || record.Direction != domainhistory.RecordDirectionRemote {
			return fmt.Errorf("store: apply remote page: invalid record")
		}
		if record.SeriesHost != checkpoint.SeriesHost || record.Tag != checkpoint.Tag ||
			record.Index > checkpoint.AppliedIndex {
			return fmt.Errorf("store: apply remote page: record outside checkpoint")
		}
	}
	for _, entry := range page.Entries {
		if entry.ID == "" || entry.RecordedAt.IsZero() {
			return fmt.Errorf("store: apply remote page: invalid entry")
		}
	}
	for _, deleted := range page.Deletes {
		if deleted.HistoryID == "" || deleted.DeletedAt.IsZero() {
			return fmt.Errorf("store: apply remote page: invalid tombstone")
		}
	}
	for _, reindex := range page.Reindexes {
		// A moved local capture must land after the page it makes room for.
		if reindex.RecordID == "" || reindex.Index <= checkpoint.AppliedIndex {
			return fmt.Errorf("store: apply remote page: invalid reindex")
		}
	}
	if len(page.Acknowledged) > 0 {
		if page.AcknowledgedAt.IsZero() {
			return fmt.Errorf("store: apply remote page: missing acknowledgement timestamp")
		}
		for _, id := range page.Acknowledged {
			if id == "" {
				return fmt.Errorf("store: apply remote page: invalid acknowledged record")
			}
		}
	}

	if err := s.db.ORMTransaction(ctx, func(tx *gorm.DB) error {
		for _, reindex := range page.Reindexes {
			result := tx.Model(&historyRecordSchema{}).
				Where("id = ? AND series_host = ? AND tag = ? AND direction = ? AND uploaded_at = 0",
					reindex.RecordID, checkpoint.SeriesHost, checkpoint.Tag,
					domainhistory.RecordDirectionLocal).
				Update("idx", reindex.Index)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return fmt.Errorf("store: apply remote page: reindex target is not an unsynced local record")
			}
		}
		if len(page.Acknowledged) > 0 {
			// A downloaded record proved the server already holds these local
			// events. The local log ids may differ from the server copies, so
			// record the acknowledgement here and never resend them.
			query := func() *gorm.DB {
				return tx.Model(&historyRecordSchema{}).
					Where("id IN ? AND series_host = ? AND tag = ? AND direction = ?",
						page.Acknowledged, checkpoint.SeriesHost, checkpoint.Tag,
						domainhistory.RecordDirectionLocal)
			}
			var present int64
			if err := query().Count(&present).Error; err != nil {
				return err
			}
			if present != int64(len(page.Acknowledged)) {
				return fmt.Errorf("store: apply remote page: acknowledged record is not a local record")
			}
			if err := query().Update("uploaded_at", timestampNano(page.AcknowledgedAt)).Error; err != nil {
				return err
			}
		}
		for _, record := range page.Records {
			if err := insertRemoteHistoryRecord(tx, record); err != nil {
				return err
			}
		}
		for _, entry := range page.Entries {
			model, err := historyEntryModel(entry)
			if err != nil {
				return err
			}
			if err := upsertModel(tx, &model, "id", historyEntryColumns...); err != nil {
				return err
			}
		}
		for _, deleted := range page.Deletes {
			if err := applyRemoteTombstone(tx, deleted.HistoryID, deleted.DeletedAt); err != nil {
				return err
			}
		}
		metadata, err := readHistoryMetadata(tx)
		if err != nil {
			return err
		}
		if metadata.Series == nil {
			metadata.Series = make(map[string]domainhistory.SeriesCheckpoint)
		}
		metadata.Series[domainhistory.SeriesKey(checkpoint.SeriesHost, checkpoint.Tag)] = checkpoint
		return writeHistoryMetadata(tx, metadata)
	}); err != nil {
		return fmt.Errorf("store: apply remote page: %w", err)
	}
	return nil
}

// insertRemoteHistoryRecord appends one record with the index the remote
// series assigned. A repeated download is idempotent: the same series index
// with the same metadata is accepted, while a different record at that index
// fails closed. UploadedAt is never set for a remote record.
func insertRemoteHistoryRecord(tx *gorm.DB, record domainhistory.Record) error {
	var existing historyRecordSchema
	err := tx.Where("series_host = ? AND tag = ? AND idx = ?",
		record.SeriesHost, record.Tag, record.Index).First(&existing).Error
	if err == nil {
		if !sameHistoryRecordMetadata(historyRecord(existing), record) {
			return fmt.Errorf("store: remote record conflicts with the local log")
		}
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	row := historyRecordSchema{
		ID: record.ID, SeriesHost: record.SeriesHost, Tag: record.Tag,
		Index: record.Index, RecordedAt: timestampNano(record.RecordedAt),
		Version: int64(record.Version), Kind: record.Kind, HistoryID: record.HistoryID,
		Payload: record.Payload, Direction: domainhistory.RecordDirectionRemote,
	}
	return tx.Create(&row).Error
}

func sameHistoryRecordMetadata(existing, incoming domainhistory.Record) bool {
	return existing.ID == incoming.ID &&
		existing.SeriesHost == incoming.SeriesHost &&
		existing.Tag == incoming.Tag &&
		existing.Index == incoming.Index &&
		existing.Version == incoming.Version &&
		existing.Kind == incoming.Kind &&
		existing.HistoryID == incoming.HistoryID &&
		timestampNano(existing.RecordedAt) == timestampNano(incoming.RecordedAt)
}

// applyRemoteTombstone soft-deletes a projection without appending another
// record: the downloaded delete record is the tombstone. A history id this
// device never saw has no projection to update.
func applyRemoteTombstone(tx *gorm.DB, historyID string, deletedAt time.Time) error {
	entry, found, err := loadHistoryEntry(tx, historyID)
	if err != nil || !found || !entry.DeletedAt.IsZero() {
		return err
	}
	entry.DeletedAt = deletedAt
	encoded, err := marshalData(entry, "history entry")
	if err != nil {
		return err
	}
	return tx.Model(&historyEntrySchema{}).Where("id = ?", historyID).
		Updates(map[string]any{
			"deleted_at": timestampMilli(deletedAt),
			"data":       encoded,
		}).Error
}

// MarkUploaded records the server's acknowledgement of a batch and advances
// the upload checkpoint in one transaction.
func (s *Store) MarkUploaded(ctx context.Context, recordIDs []string, at time.Time,
	checkpoint domainhistory.SeriesCheckpoint) error {
	if checkpoint.SeriesHost == "" || checkpoint.Tag == "" {
		return fmt.Errorf("store: mark history uploaded: invalid checkpoint")
	}
	if err := s.db.ORMTransaction(ctx, func(tx *gorm.DB) error {
		if len(recordIDs) > 0 {
			if err := tx.Model(&historyRecordSchema{}).
				Where("id IN ? AND series_host = ? AND tag = ?",
					recordIDs, checkpoint.SeriesHost, checkpoint.Tag).
				Update("uploaded_at", timestampNano(at)).Error; err != nil {
				return err
			}
		}
		metadata, err := readHistoryMetadata(tx)
		if err != nil {
			return err
		}
		metadata.Series = withSeriesCheckpoint(metadata.Series, checkpoint)
		return writeHistoryMetadata(tx, metadata)
	}); err != nil {
		return fmt.Errorf("store: mark history uploaded: %w", err)
	}
	return nil
}

// ResetUploaded clears the upload acknowledgement display for one writer
// series and records the server's new high-water mark in the same transaction.
// The engine calls it when an account change means the server no longer holds
// what this device thought it uploaded.
func (s *Store) ResetUploaded(ctx context.Context, checkpoint domainhistory.SeriesCheckpoint) error {
	if checkpoint.SeriesHost == "" || checkpoint.Tag == "" {
		return fmt.Errorf("store: reset history uploads: invalid checkpoint")
	}
	if err := s.db.ORMTransaction(ctx, func(tx *gorm.DB) error {
		if err := tx.Model(&historyRecordSchema{}).
			Where("series_host = ? AND tag = ?", checkpoint.SeriesHost, checkpoint.Tag).
			Update("uploaded_at", 0).Error; err != nil {
			return err
		}
		metadata, err := readHistoryMetadata(tx)
		if err != nil {
			return err
		}
		metadata.Series = withSeriesCheckpoint(metadata.Series, checkpoint)
		return writeHistoryMetadata(tx, metadata)
	}); err != nil {
		return fmt.Errorf("store: reset history uploads: %w", err)
	}
	return nil
}

func withSeriesCheckpoint(series map[string]domainhistory.SeriesCheckpoint,
	checkpoint domainhistory.SeriesCheckpoint) map[string]domainhistory.SeriesCheckpoint {
	if series == nil {
		series = make(map[string]domainhistory.SeriesCheckpoint)
	}
	series[domainhistory.SeriesKey(checkpoint.SeriesHost, checkpoint.Tag)] = checkpoint
	return series
}
