package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	apphistory "github.com/mslxl/natcatty/application/history"
	domainhistory "github.com/mslxl/natcatty/domain/history"
	"gorm.io/gorm"
)

// errInvalidWriterUUID reports a writer identity that cannot be an Atuin
// series host. Netcatty generates the identity once and never replaces it.
var errInvalidWriterUUID = errors.New("store: invalid history writer UUID")

// The history metadata keys are namespaced so they cannot collide with any
// other state kept in the shared meta table. None of these keys is part of the
// Vault backup table set.
const (
	metaKeyHistoryWriter              = "history.writer_uuid"
	metaKeyHistoryShellImport         = "history.shell_history_imported"
	metaKeyHistoryKeyFingerprint      = "history.key_fingerprint"
	metaKeyHistoryUnverifiedConfirmed = "history.unverified_key_confirmed"
	metaKeyHistoryAccount             = "history.account"
	metaKeyHistoryHostImports         = "history.host_imports"
	metaKeyHistoryHostNotified        = "history.host_atuin_notified"
	metaKeyHistorySeries              = "history.series_checkpoints"
)

// EnsureWriterUUID returns the per-install writer UUID, generating and
// persisting it inside one transaction the first time. A later call always
// returns the stored identity; a stored value that is not a UUID is an error
// rather than a silent replacement.
func (s *Store) EnsureWriterUUID(ctx context.Context, generate func() string) (string, error) {
	var writer string
	if err := s.db.ORMTransaction(ctx, func(tx *gorm.DB) error {
		var err error
		writer, err = ensureWriterUUID(tx, generate)
		return err
	}); err != nil {
		return "", fmt.Errorf("store: ensure history writer UUID: %w", err)
	}
	return writer, nil
}

func ensureWriterUUID(tx *gorm.DB, generate func() string) (string, error) {
	stored, found, err := readMeta(tx, metaKeyHistoryWriter)
	if err != nil {
		return "", err
	}
	if found {
		if !domainhistory.IsUUID(stored) {
			return "", fmt.Errorf("%w: stored %q", errInvalidWriterUUID, stored)
		}
		return stored, nil
	}
	if generate == nil {
		return "", fmt.Errorf("%w: no generator", errInvalidWriterUUID)
	}
	generated := generate()
	if !domainhistory.IsUUID(generated) {
		return "", fmt.Errorf("%w: generated %q", errInvalidWriterUUID, generated)
	}
	if err := writeMeta(tx, metaKeyHistoryWriter, generated); err != nil {
		return "", err
	}
	return generated, nil
}

// HistoryMetadata reads the device-local sync state. The writer UUID is
// reported even though SaveHistoryMetadata never changes it.
func (s *Store) HistoryMetadata(ctx context.Context) (domainhistory.Metadata, error) {
	var metadata domainhistory.Metadata
	// One transaction keeps the several meta rows a consistent snapshot.
	if err := s.db.ORMTransaction(ctx, func(tx *gorm.DB) error {
		writer, found, err := readMeta(tx, metaKeyHistoryWriter)
		if err != nil {
			return err
		}
		if found {
			metadata.WriterUUID = writer
		}
		metadata.ShellHistoryImported, err = readMetaFlag(tx, metaKeyHistoryShellImport)
		if err != nil {
			return err
		}
		if metadata.KeyFingerprint, _, err = readMeta(tx, metaKeyHistoryKeyFingerprint); err != nil {
			return err
		}
		metadata.UnverifiedKeyConfirmed, err = readMetaFlag(tx, metaKeyHistoryUnverifiedConfirmed)
		if err != nil {
			return err
		}
		if err := readMetaJSON(tx, metaKeyHistoryAccount, &metadata.Account); err != nil {
			return err
		}
		if err := readMetaJSON(tx, metaKeyHistoryHostImports, &metadata.HostImports); err != nil {
			return err
		}
		if err := readMetaJSON(tx, metaKeyHistoryHostNotified, &metadata.HostAtuinNotified); err != nil {
			return err
		}
		return readMetaJSON(tx, metaKeyHistorySeries, &metadata.Series)
	}); err != nil {
		return domainhistory.Metadata{}, fmt.Errorf("store: read history metadata: %w", err)
	}
	return metadata, nil
}

// SaveHistoryMetadata replaces the device-local checkpoints and account
// identity in one transaction. An empty field clears its stored value; the
// caller decides which state survives a given transition (the spec keeps the
// writer UUID and the key fingerprint across logout). The writer UUID is owned
// by EnsureWriterUUID and is never rewritten here.
func (s *Store) SaveHistoryMetadata(ctx context.Context, metadata domainhistory.Metadata) error {
	if err := s.db.ORMTransaction(ctx, func(tx *gorm.DB) error {
		if err := writeMetaFlag(tx, metaKeyHistoryShellImport, metadata.ShellHistoryImported); err != nil {
			return err
		}
		if err := writeMetaText(tx, metaKeyHistoryKeyFingerprint, metadata.KeyFingerprint); err != nil {
			return err
		}
		if err := writeMetaFlag(tx, metaKeyHistoryUnverifiedConfirmed,
			metadata.UnverifiedKeyConfirmed); err != nil {
			return err
		}
		if err := writeMetaJSON(tx, metaKeyHistoryAccount, metadata.Account,
			metadata.Account != domainhistory.AccountIdentity{}); err != nil {
			return err
		}
		if err := writeMetaJSON(tx, metaKeyHistoryHostImports, metadata.HostImports,
			len(metadata.HostImports) > 0); err != nil {
			return err
		}
		if err := writeMetaJSON(tx, metaKeyHistoryHostNotified, metadata.HostAtuinNotified,
			len(metadata.HostAtuinNotified) > 0); err != nil {
			return err
		}
		return writeMetaJSON(tx, metaKeyHistorySeries, metadata.Series,
			len(metadata.Series) > 0)
	}); err != nil {
		return fmt.Errorf("store: save history metadata: %w", err)
	}
	return nil
}

// ImportShellHistory writes the one-time Shell History File rows with their
// create records and sets the durable import marker in one transaction. It is
// a no-op when the marker is already set, so a repeated or concurrent import
// cannot duplicate rows. A failed write leaves the projection, the record log,
// and the marker unchanged.
func (s *Store) ImportShellHistory(ctx context.Context, imports []apphistory.ShellHistoryImport) error {
	for _, imported := range imports {
		if !imported.Entry.Valid() {
			return fmt.Errorf("store: invalid shell-history entry")
		}
		if imported.Record.Kind != domainhistory.RecordKindCreate ||
			imported.Record.HistoryID != imported.Entry.ID {
			return fmt.Errorf("store: shell-history record does not belong to entry %q", imported.Entry.ID)
		}
	}
	if err := s.db.ORMTransaction(ctx, func(tx *gorm.DB) error {
		alreadyImported, err := readMetaFlag(tx, metaKeyHistoryShellImport)
		if err != nil {
			return err
		}
		if alreadyImported {
			return nil
		}
		for _, imported := range imports {
			record := imported.Record
			if err := writeHistoryEntry(tx, imported.Entry, &record); err != nil {
				return err
			}
		}
		return writeMetaFlag(tx, metaKeyHistoryShellImport, true)
	}); err != nil {
		return fmt.Errorf("store: import shell history: %w", err)
	}
	return nil
}

// ImportHostAtuinEntries writes one read-only Host Atuin snapshot and its
// checkpoint in one transaction. Imported rows are local projections, so no
// create record is appended and the checkpoint advances only when every row
// was written. A repeated import upserts the same history ids instead of
// producing duplicates, and an older checkpoint never moves the stored one
// backwards.
func (s *Store) ImportHostAtuinEntries(ctx context.Context, hostID string,
	checkpoint domainhistory.ImportCheckpoint, entries []domainhistory.Entry) error {
	if hostID == "" {
		return fmt.Errorf("store: import Host Atuin history: empty Host id")
	}
	if checkpoint.Timestamp.IsZero() || checkpoint.HistoryID == "" {
		return fmt.Errorf("store: import Host Atuin history: empty checkpoint")
	}
	models := make([]historyEntrySchema, 0, len(entries))
	for _, entry := range entries {
		if entry.Source != domainhistory.SourceAtuinImport {
			return fmt.Errorf("store: import Host Atuin history: row %q has source %q", entry.ID, entry.Source)
		}
		if !entry.Valid() {
			return fmt.Errorf("store: import Host Atuin history: invalid row %q", entry.ID)
		}
		if !domainhistory.CheckpointAfter(checkpoint, entry) {
			return fmt.Errorf("store: import Host Atuin history: checkpoint does not cover row %q", entry.ID)
		}
		model, err := historyEntryModel(entry)
		if err != nil {
			return err
		}
		models = append(models, model)
	}
	if err := s.db.ORMTransaction(ctx, func(tx *gorm.DB) error {
		for index := range models {
			if err := upsertModel(tx, &models[index], "id", historyEntryColumns...); err != nil {
				return err
			}
		}
		var imports map[string]domainhistory.ImportCheckpoint
		if err := readMetaJSON(tx, metaKeyHistoryHostImports, &imports); err != nil {
			return err
		}
		if imports == nil {
			imports = make(map[string]domainhistory.ImportCheckpoint)
		}
		current, found := imports[hostID]
		if !found || domainhistory.CompareImportCheckpoint(checkpoint, current) > 0 {
			imports[hostID] = checkpoint
		}
		return writeMetaJSON(tx, metaKeyHistoryHostImports, imports, true)
	}); err != nil {
		return fmt.Errorf("store: import Host Atuin history: %w", err)
	}
	return nil
}

// MarkHostAtuinNoticed records that the one-time Host Atuin notice was
// published. It reports whether this call set the marker, so a later Session
// or a concurrent open suppresses a duplicate notice.
func (s *Store) MarkHostAtuinNoticed(ctx context.Context, hostID string) (bool, error) {
	if hostID == "" {
		return false, nil
	}
	var first bool
	if err := s.db.ORMTransaction(ctx, func(tx *gorm.DB) error {
		var notified map[string]bool
		if err := readMetaJSON(tx, metaKeyHistoryHostNotified, &notified); err != nil {
			return err
		}
		if notified == nil {
			notified = make(map[string]bool)
		}
		if notified[hostID] {
			return nil
		}
		notified[hostID] = true
		first = true
		return writeMetaJSON(tx, metaKeyHistoryHostNotified, notified, true)
	}); err != nil {
		return false, fmt.Errorf("store: mark Host Atuin noticed: %w", err)
	}
	return first, nil
}

// ListHistoryRecords returns the append-only log in series and index order.
func (s *Store) ListHistoryRecords(ctx context.Context) ([]domainhistory.Record, error) {
	var rows []historyRecordSchema
	if err := s.db.ORM(ctx, func(tx *gorm.DB) error {
		return tx.Order("series_host ASC").Order("tag ASC").Order("idx ASC").Find(&rows).Error
	}); err != nil {
		return nil, fmt.Errorf("store: list history records: %w", err)
	}
	records := make([]domainhistory.Record, 0, len(rows))
	for _, row := range rows {
		records = append(records, historyRecord(row))
	}
	return records, nil
}

// appendHistoryRecord assigns the next index in one writer series and inserts
// the record. It runs inside the caller's transaction so the projection and
// the log commit or roll back together.
func appendHistoryRecord(tx *gorm.DB, record domainhistory.Record) error {
	if !record.Valid() {
		return fmt.Errorf("store: invalid history record")
	}
	var next struct {
		Index int64 `gorm:"column:next"`
	}
	if err := tx.Model(&historyRecordSchema{}).
		Where("series_host = ? AND tag = ?", record.SeriesHost, record.Tag).
		Select("COALESCE(MAX(idx), -1) + 1 AS next").
		Scan(&next).Error; err != nil {
		return err
	}
	row := historyRecordSchema{
		ID: record.ID, SeriesHost: record.SeriesHost, Tag: record.Tag,
		Index: next.Index, RecordedAt: timestampNano(record.RecordedAt),
		Version: int64(record.Version), Kind: record.Kind, HistoryID: record.HistoryID,
		Payload: record.Payload, Direction: record.Direction,
		UploadedAt: timestampNano(record.UploadedAt),
	}
	return tx.Create(&row).Error
}

func historyRecord(row historyRecordSchema) domainhistory.Record {
	return domainhistory.Record{
		ID: row.ID, SeriesHost: row.SeriesHost, Tag: row.Tag, Index: row.Index,
		RecordedAt: timeFromNano(row.RecordedAt), Version: int(row.Version),
		Kind: row.Kind, HistoryID: row.HistoryID, Payload: row.Payload,
		Direction: row.Direction, UploadedAt: timeFromNano(row.UploadedAt),
	}
}

// Record timestamps are nanoseconds because Atuin's history timestamp is
// nanoseconds and the adapter must preserve that fidelity. Zero means unset.
func timestampNano(at time.Time) int64 {
	if at.IsZero() {
		return 0
	}
	return at.UnixNano()
}

func timeFromNano(nanoseconds int64) time.Time {
	if nanoseconds == 0 {
		return time.Time{}
	}
	return time.Unix(0, nanoseconds)
}

func readMeta(tx *gorm.DB, key string) (string, bool, error) {
	var row metaSchema
	if err := tx.Where("key = ?", key).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", false, nil
		}
		return "", false, err
	}
	return row.Value, true, nil
}

func readMetaFlag(tx *gorm.DB, key string) (bool, error) {
	value, found, err := readMeta(tx, key)
	if err != nil {
		return false, err
	}
	return found && value == "1", nil
}

func readMetaJSON(tx *gorm.DB, key string, value any) error {
	raw, found, err := readMeta(tx, key)
	if err != nil || !found || raw == "" {
		return err
	}
	return json.Unmarshal([]byte(raw), value)
}

func writeMeta(tx *gorm.DB, key, value string) error {
	return upsertModel(tx, &metaSchema{Key: key, Value: value}, "key", "value")
}

func writeMetaText(tx *gorm.DB, key, value string) error {
	if value == "" {
		return clearMeta(tx, key)
	}
	return writeMeta(tx, key, value)
}

func writeMetaFlag(tx *gorm.DB, key string, value bool) error {
	if !value {
		return clearMeta(tx, key)
	}
	return writeMeta(tx, key, "1")
}

func writeMetaJSON(tx *gorm.DB, key string, value any, present bool) error {
	if !present {
		return clearMeta(tx, key)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return writeMeta(tx, key, string(encoded))
}

func clearMeta(tx *gorm.DB, key string) error {
	_, err := deleteModel(tx, &metaSchema{}, &metaSchema{Key: key})
	return err
}
