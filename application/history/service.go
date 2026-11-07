// Package history records connection logs and command history, applies the
// configured Connection Log retention, and exposes one change signal to the UI.
//
// The service owns no storage and no UI: adapters implement Store, and the
// composition root supplies the retention read from Settings. Command History
// is the journal autocomplete reads, so both surfaces share one persisted
// record instead of forking it. Command History has no automatic count/day
// retention.
package history

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	domainhistory "github.com/mslxl/natcatty/domain/history"
)

// ErrInvalid reports a record that cannot be persisted.
var ErrInvalid = errors.New("history: invalid record")

// Store is the persistence port the Vault database implements.
type Store interface {
	ListConnectionLogs(context.Context) ([]domainhistory.ConnectionLog, error)
	SaveConnectionLog(context.Context, domainhistory.ConnectionLog) error
	DeleteConnectionLog(context.Context, string) error
	ClearConnectionLogs(context.Context) (int, error)
	PruneConnectionLogs(context.Context, domainhistory.Retention, time.Time) (int, error)

	ListHistoryEntries(context.Context) ([]domainhistory.Entry, error)
	HistoryEntry(context.Context, string) (domainhistory.Entry, bool, error)
	// RecordHistoryEntry writes the projection and, when record is not nil,
	// one create record in the same transaction.
	RecordHistoryEntry(context.Context, domainhistory.Entry, *domainhistory.Record) error
	// SoftDeleteHistoryEntry writes the soft-delete state and, when record is
	// not nil, one delete record in the same transaction.
	SoftDeleteHistoryEntry(context.Context, string, time.Time, *domainhistory.Record) error
	// SoftDeleteHistoryEntries writes every listed row's soft-delete state and
	// its tombstone in one transaction. Rows that are missing or already
	// deleted are skipped, so a retried prune appends no duplicate tombstone.
	SoftDeleteHistoryEntries(context.Context, []domainhistory.EntryDeletion, time.Time) (int, error)
	// EnsureWriterUUID returns the per-install series host, creating it once.
	EnsureWriterUUID(context.Context, func() string) (string, error)
	// HistoryMetadata reads the device-local import, notice, and sync state,
	// including the durable one-time Shell History File import marker.
	HistoryMetadata(context.Context) (domainhistory.Metadata, error)
	// ListHistoryEntriesIncludingDeleted returns every recorded command,
	// including soft-deleted rows. The one-time import deduplicates against
	// them so it never resurrects a command the user deleted.
	ListHistoryEntriesIncludingDeleted(context.Context) ([]domainhistory.Entry, error)
	// ImportShellHistory writes the one-time Shell History File rows with
	// their create records and sets the durable import marker in one
	// transaction. It is a no-op when the marker is already set.
	ImportShellHistory(context.Context, []ShellHistoryImport) error
	// ImportHostAtuinEntries writes one read-only Host Atuin snapshot and its
	// checkpoint in one transaction, without appending create records.
	ImportHostAtuinEntries(context.Context, string, domainhistory.ImportCheckpoint, []domainhistory.Entry) error
	// MarkHostAtuinNoticed records the one-time Host Atuin notice and reports
	// whether this call set it.
	MarkHostAtuinNoticed(context.Context, string) (bool, error)
}

// ShellHistoryImport pairs one imported journal row with its create record.
// The store writes both in the same transaction.
type ShellHistoryImport struct {
	Entry  domainhistory.Entry
	Record domainhistory.Record
}

// Retention is the configured bound for Connection Logs. Command History is
// never pruned automatically.
type Retention struct {
	Logs domainhistory.Retention
}

// PruneResult reports how many connection logs retention removed.
type PruneResult struct {
	ConnectionLogs int
}

// Option customizes a Service.
type Option func(*Service)

// ShellHistorySource returns the parsed Shell History File commands. It is
// called only when the durable import marker is absent, so a later startup
// never reads the files again. An empty result still completes the import.
type ShellHistorySource func() []domainhistory.ShellHistoryCommand

// WithRetention supplies the live retention. The function is read on every
// record and prune, so a Settings change applies without rebuilding.
func WithRetention(fn func() Retention) Option {
	return func(s *Service) { s.retention = fn }
}

// WithClock replaces the clock. Tests use it to make timestamps and age
// pruning deterministic.
func WithClock(fn func() time.Time) Option {
	return func(s *Service) {
		if fn != nil {
			s.now = fn
		}
	}
}

// WithIDGenerator replaces identity generation for the connection log, the
// history entry, and the record log. Tests use it for deterministic ordering.
func WithIDGenerator(fn func() string) Option {
	return func(s *Service) {
		if fn != nil {
			s.newLogID = fn
			s.newEntryID = fn
			s.newHistoryRecordID = fn
		}
	}
}

// Service records, lists, deletes, and prunes both record families. It is safe
// for concurrent use: command recording runs on a Session goroutine while the
// UI lists.
type Service struct {
	store              Store
	retention          func() Retention
	now                func() time.Time
	newLogID           func() string
	newEntryID         func() string
	newHistoryRecordID func() string
	newWriterID        func() string

	mu         sync.Mutex
	revision   uint64
	listeners  map[int]func()
	nextListen int
}

// NewService returns a Service over store. A nil retention keeps every
// record.
func NewService(store Store, opts ...Option) *Service {
	service := &Service{
		store:              store,
		now:                time.Now,
		newLogID:           newRecordID,
		newEntryID:         newHistoryID,
		newHistoryRecordID: newHistoryID,
		newWriterID:        newHistoryID,
		listeners:          make(map[int]func()),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(service)
		}
	}
	return service
}

// RecordConnection stores one connection attempt and applies log retention.
func (s *Service) RecordConnection(ctx context.Context, log domainhistory.ConnectionLog) error {
	if s.store == nil {
		return ErrInvalid
	}
	log = domainhistory.NormalizeConnectionLog(log)
	if log.ID == "" {
		log.ID = s.newLogID()
	}
	if log.StartedAt.IsZero() {
		log.StartedAt = s.now()
	}
	if log.EndedAt.IsZero() {
		log.EndedAt = s.now()
	}
	if !log.Valid() {
		return ErrInvalid
	}
	if err := s.store.SaveConnectionLog(ctx, log); err != nil {
		return err
	}
	if retention := s.retentionFor().Logs; retention.Bounded() {
		if _, err := s.store.PruneConnectionLogs(ctx, retention, s.now()); err != nil {
			return err
		}
	}
	s.notify()
	return nil
}

// Record appends one command execution to the journal. It is autocomplete's
// and the History surface's single write path. No count/day retention is
// applied: a row is removed only by an explicit user action.
func (s *Service) Record(ctx context.Context, entry domainhistory.Entry) error {
	if s.store == nil {
		return ErrInvalid
	}
	entry = domainhistory.NormalizeEntry(entry)
	if entry.SessionUUID == "" && entry.SessionID != "" {
		entry.SessionUUID = domainhistory.SessionUUID(entry.SessionID)
	}
	if entry.Scope == "" {
		entry.Scope = domainhistory.NormalizeScope(entry.HostID)
	}
	switch entry.Source {
	case domainhistory.SourceAtuinImport, domainhistory.SourceAtuinSync:
		// An imported or synced row keeps a valid Atuin UUID and otherwise
		// gets a stable derived UUID, so a repeated import maps to one row.
		// Derive before defaulting RecordedAt: a remote row with no timestamp
		// must still derive the same identity on the next import.
		if !entry.HasUUIDIdentity() {
			entry.ID = domainhistory.DerivedEntryUUID(entry)
		}
	default:
		// Captured and shell-history rows get a UUIDv7 when they have no
		// identity. A row that already carries one keeps it, even a legacy
		// non-UUID identity: Netcatty never fabricates a wire identity for
		// local history it did not create.
		if entry.ID == "" {
			entry.ID = s.newEntryID()
		}
	}
	if entry.RecordedAt.IsZero() {
		entry.RecordedAt = s.now()
	}
	if !entry.Valid() {
		return ErrInvalid
	}
	record, err := s.createRecord(ctx, entry)
	if err != nil {
		return err
	}
	if err := s.store.RecordHistoryEntry(ctx, entry, record); err != nil {
		return err
	}
	s.notify()
	return nil
}

// ImportShellHistory runs the one-time Shell History File import. A durable
// marker makes it a no-op after the first completed attempt, even after a
// restart or after the imported rows were deleted. An empty file set still
// sets the marker. Rows are deduplicated against every existing journal row,
// including soft-deleted ones, and each inserted row gets a create record in
// this install's writer series.
func (s *Service) ImportShellHistory(ctx context.Context, source ShellHistorySource, origin string) error {
	if s.store == nil {
		return ErrInvalid
	}
	metadata, err := s.store.HistoryMetadata(ctx)
	if err != nil {
		return err
	}
	if metadata.ShellHistoryImported {
		return nil
	}
	var commands []domainhistory.ShellHistoryCommand
	if source != nil {
		commands = source()
	}
	existing, err := s.store.ListHistoryEntriesIncludingDeleted(ctx)
	if err != nil {
		return err
	}

	origin = strings.TrimSpace(origin)
	accepted := make([]domainhistory.Entry, 0, len(commands))
	// dedup keeps the candidate rows before timestamps are defaulted: a
	// timestamp-less command matches by command and scope, not by the import
	// time it will be persisted with.
	dedup := make([]domainhistory.Entry, 0, len(commands))
	for _, command := range commands {
		entry := domainhistory.NormalizeEntry(domainhistory.Entry{
			Scope:      domainhistory.NormalizeScope(""),
			Command:    command.Command,
			Shell:      command.Shell,
			RecordedAt: command.RecordedAt,
			Origin:     origin,
			Source:     domainhistory.SourceShellHistory,
		})
		if entry.Command == "" {
			continue
		}
		if shellHistoryDuplicate(entry, existing) || shellHistoryDuplicate(entry, dedup) {
			continue
		}
		dedup = append(dedup, entry)
		// A file format without timestamps leaves the time unknown; the
		// journal row still needs a start time, so it is stamped with the
		// import time (the pre-existing import behaviour).
		if entry.RecordedAt.IsZero() {
			entry.RecordedAt = s.now()
		}
		entry.ID = s.newEntryID()
		if !entry.HasUUIDIdentity() {
			return fmt.Errorf("history: shell-history identity %q is not a UUID", entry.ID)
		}
		accepted = append(accepted, entry)
	}

	imports := make([]ShellHistoryImport, 0, len(accepted))
	if len(accepted) > 0 {
		writer, err := s.store.EnsureWriterUUID(ctx, s.newWriterID)
		if err != nil {
			return err
		}
		for _, entry := range accepted {
			record, err := s.createRecordForWriter(entry, writer)
			if err != nil {
				return err
			}
			imports = append(imports, ShellHistoryImport{Entry: entry, Record: *record})
		}
	}
	if err := s.store.ImportShellHistory(ctx, imports); err != nil {
		return err
	}
	if len(accepted) > 0 {
		s.notify()
	}
	return nil
}

// shellHistoryDuplicate reports whether candidate duplicates any existing row.
func shellHistoryDuplicate(candidate domainhistory.Entry, existing []domainhistory.Entry) bool {
	for _, row := range existing {
		if domainhistory.ShellHistoryDuplicate(candidate, row) {
			return true
		}
	}
	return false
}

// createRecord returns the create record that belongs to a newly recorded
// command, or nil when the row must stay local-only. Imported and synced rows
// are projections of another writer's history and are never uploaded as
// creates; a legacy non-UUID identity cannot be represented on the Atuin wire.
func (s *Service) createRecord(ctx context.Context, entry domainhistory.Entry) (*domainhistory.Record, error) {
	switch entry.Source {
	case domainhistory.SourceAtuinImport, domainhistory.SourceAtuinSync:
		return nil, nil
	}
	if !entry.HasUUIDIdentity() {
		return nil, nil
	}
	writer, err := s.store.EnsureWriterUUID(ctx, s.newWriterID)
	if err != nil {
		return nil, err
	}
	return s.createRecordForWriter(entry, writer)
}

// createRecordForWriter builds one create record for entry in the given
// writer series. The caller guarantees a UUID identity. The store assigns the
// series index inside the writing transaction.
func (s *Service) createRecordForWriter(entry domainhistory.Entry, writer string) (*domainhistory.Record, error) {
	payload, err := json.Marshal(entry)
	if err != nil {
		return nil, fmt.Errorf("history: encode create record: %w", err)
	}
	return s.localRecord(writer, entry.ID, domainhistory.RecordKindCreate, entry.RecordedAt, payload), nil
}

// newLocalRecord builds one pending record in this install's writer series.
func (s *Service) newLocalRecord(ctx context.Context, historyID, kind string, recordedAt time.Time, payload []byte) (*domainhistory.Record, error) {
	writer, err := s.store.EnsureWriterUUID(ctx, s.newWriterID)
	if err != nil {
		return nil, err
	}
	return s.localRecord(writer, historyID, kind, recordedAt, payload), nil
}

// localRecord builds one pending record for a known writer series. Bulk
// deletes resolve the writer once instead of asking the store for it per row.
func (s *Service) localRecord(writer, historyID, kind string, recordedAt time.Time, payload []byte) *domainhistory.Record {
	return &domainhistory.Record{
		ID:         s.newHistoryRecordID(),
		SeriesHost: writer,
		Tag:        domainhistory.RecordTagHistory,
		RecordedAt: recordedAt,
		Version:    domainhistory.RecordVersion,
		Kind:       kind,
		HistoryID:  historyID,
		Payload:    string(payload),
		Direction:  domainhistory.RecordDirectionLocal,
	}
}

// HostImportResult reports one read-only Host Atuin import. Imported counts
// the rows written; Skipped counts rows at or below the stored checkpoint.
// Checkpoint is the resulting per-Host checkpoint, which does not move when
// the snapshot fails.
type HostImportResult struct {
	Imported   int
	Skipped    int
	Checkpoint domainhistory.ImportCheckpoint
}

// ImportHostAtuin maps one Host Atuin snapshot onto the journal and writes it
// with the per-Host checkpoint in one transaction. A valid Atuin UUID is
// preserved; a malformed one gets a stable derived UUID. Rows at or below the
// stored (timestamp, history id) checkpoint are skipped, so reopening and
// re-importing the same database never duplicates a row. Imported rows are
// projections: they get no create record and are never uploaded.
func (s *Service) ImportHostAtuin(ctx context.Context, hostID string,
	rows []domainhistory.AtuinRow, hostOrigin string) (HostImportResult, error) {
	result := HostImportResult{}
	if s.store == nil || hostID == "" {
		return result, ErrInvalid
	}
	metadata, err := s.store.HistoryMetadata(ctx)
	if err != nil {
		return result, err
	}
	checkpoint := metadata.HostImports[hostID]
	result.Checkpoint = checkpoint
	pending := make([]domainhistory.Entry, 0, len(rows))
	for _, row := range rows {
		entry, err := domainhistory.MapAtuinRow(row, hostOrigin)
		if err != nil {
			return result, err
		}
		if domainhistory.CheckpointAfter(checkpoint, entry) {
			result.Skipped++
			continue
		}
		pending = append(pending, entry)
	}
	// Order by the checkpoint key so the last row is the new high-water mark
	// regardless of the database's row order.
	sort.SliceStable(pending, func(i, j int) bool {
		left := domainhistory.ImportCheckpoint{
			Timestamp: pending[i].RecordedAt, HistoryID: pending[i].ID,
		}
		right := domainhistory.ImportCheckpoint{
			Timestamp: pending[j].RecordedAt, HistoryID: pending[j].ID,
		}
		return domainhistory.CompareImportCheckpoint(left, right) < 0
	})
	for _, entry := range pending {
		candidate := domainhistory.ImportCheckpoint{
			Timestamp: entry.RecordedAt, HistoryID: entry.ID,
		}
		if domainhistory.CompareImportCheckpoint(candidate, result.Checkpoint) > 0 {
			result.Checkpoint = candidate
		}
	}
	if len(pending) == 0 {
		return result, nil
	}
	if err := s.store.ImportHostAtuinEntries(ctx, hostID, result.Checkpoint, pending); err != nil {
		result.Imported = 0
		return result, err
	}
	result.Imported = len(pending)
	s.notify()
	return result, nil
}

// MarkHostAtuinNoticed records the one-time Host Atuin notice and reports
// whether this call set it. A duplicate call is a no-op.
func (s *Service) MarkHostAtuinNoticed(ctx context.Context, hostID string) (bool, error) {
	if s.store == nil {
		return false, ErrInvalid
	}
	return s.store.MarkHostAtuinNoticed(ctx, hostID)
}

// Load returns the persisted command history newest first. It implements
// autocomplete's journal.
func (s *Service) Load(ctx context.Context) ([]domainhistory.Entry, error) {
	if s.store == nil {
		return nil, nil
	}
	return s.store.ListHistoryEntries(ctx)
}

// Entries is Load under the history surface's vocabulary.
func (s *Service) Entries(ctx context.Context) ([]domainhistory.Entry, error) {
	return s.Load(ctx)
}

// ConnectionLogs returns the persisted connection logs newest first.
func (s *Service) ConnectionLogs(ctx context.Context) ([]domainhistory.ConnectionLog, error) {
	if s.store == nil {
		return nil, nil
	}
	return s.store.ListConnectionLogs(ctx)
}

// DeleteConnectionLog removes one log.
func (s *Service) DeleteConnectionLog(ctx context.Context, id string) error {
	if s.store == nil {
		return ErrInvalid
	}
	if err := s.store.DeleteConnectionLog(ctx, id); err != nil {
		return err
	}
	s.notify()
	return nil
}

// DeleteEntry soft-deletes one recorded command and appends its delete
// tombstone when the row has a UUID identity. A legacy non-UUID row is deleted
// locally without a record because it cannot be represented on the Atuin wire.
// A missing or already-deleted row is a no-op.
func (s *Service) DeleteEntry(ctx context.Context, id string) error {
	if s.store == nil {
		return ErrInvalid
	}
	entry, found, err := s.store.HistoryEntry(ctx, id)
	if err != nil {
		return err
	}
	if !found || !entry.DeletedAt.IsZero() {
		return nil
	}
	_, err = s.deleteEntries(ctx, []domainhistory.Entry{entry})
	return err
}

// ClearConnectionLogs removes every connection log and reports how many were
// deleted. Command history is a separate record family and is untouched.
func (s *Service) ClearConnectionLogs(ctx context.Context) (int, error) {
	if s.store == nil {
		return 0, ErrInvalid
	}
	removed, err := s.store.ClearConnectionLogs(ctx)
	if err != nil {
		return 0, err
	}
	s.notify()
	return removed, nil
}

// clearBatchSize bounds one Clear this device transaction. A large journal is
// cleared in several committed batches instead of one transaction that holds
// the writer lock for the whole operation.
const clearBatchSize = 500

// ClearThisDevice soft-deletes every live command this install created: rows
// captured in a Session or imported from a local Shell History File, including
// legacy rows with no source. Imported and synced remote-origin rows are left
// in place so one device's clear never silently erases another writer's
// history. Each removed row with a UUID identity also gets a delete tombstone;
// a legacy non-UUID row stays local-only. Rows are removed in batches, so a
// failure part-way through leaves the already-committed batches deleted and
// stops before the rest.
func (s *Service) ClearThisDevice(ctx context.Context) (int, error) {
	if s.store == nil {
		return 0, ErrInvalid
	}
	entries, err := s.store.ListHistoryEntries(ctx)
	if err != nil {
		return 0, err
	}
	owned := make([]domainhistory.Entry, 0, len(entries))
	for _, entry := range entries {
		if entry.IsLocallyOwned() {
			owned = append(owned, entry)
		}
	}
	removed := 0
	for start := 0; start < len(owned); start += clearBatchSize {
		end := start + clearBatchSize
		if end > len(owned) {
			end = len(owned)
		}
		removedInBatch, err := s.deleteEntries(ctx, owned[start:end])
		if err != nil {
			return removed, err
		}
		removed += removedInBatch
	}
	return removed, nil
}

// PreviewPrune reports how many live rows the filter-based prune would remove.
// limit bounds the count; zero or a negative value means every match. The
// optional limit selects the oldest matching rows first, so a bounded prune
// trims age rather than the newest history.
func (s *Service) PreviewPrune(ctx context.Context, filter domainhistory.EntryFilter, limit int) (int, error) {
	matched, err := s.pruneMatches(ctx, filter)
	if err != nil {
		return 0, err
	}
	return len(boundedPrune(matched, limit)), nil
}

// PruneHistory soft-deletes the live rows matching filter and appends a delete
// tombstone for every removed row with a UUID identity, all in one
// transaction. It removes only matched rows; an optional limit selects the
// oldest matches first. A legacy non-UUID row is soft-deleted locally and
// excluded from the tombstone batch.
func (s *Service) PruneHistory(ctx context.Context, filter domainhistory.EntryFilter, limit int) (int, error) {
	matched, err := s.pruneMatches(ctx, filter)
	if err != nil {
		return 0, err
	}
	return s.deleteEntries(ctx, boundedPrune(matched, limit))
}

// pruneMatches returns the live rows matching filter newest first, using the
// same deterministic ordering as the History surface.
func (s *Service) pruneMatches(ctx context.Context, filter domainhistory.EntryFilter) ([]domainhistory.Entry, error) {
	if s.store == nil {
		return nil, ErrInvalid
	}
	entries, err := s.store.ListHistoryEntries(ctx)
	if err != nil {
		return nil, err
	}
	matched := make([]domainhistory.Entry, 0, len(entries))
	for _, entry := range entries {
		if entry.DeletedAt.IsZero() && filter.Match(entry) {
			matched = append(matched, entry)
		}
	}
	domainhistory.SortEntries(matched)
	return matched, nil
}

// boundedPrune applies an optional prune limit to the newest-first matches by
// keeping the oldest matches, which are at the end of the slice.
func boundedPrune(entries []domainhistory.Entry, limit int) []domainhistory.Entry {
	if limit <= 0 || len(entries) <= limit {
		return entries
	}
	return entries[len(entries)-limit:]
}

// deleteEntries soft-deletes the listed rows and appends one tombstone per
// UUID-identified row in a single store transaction. It is the shared path for
// Delete one, Prune, and Clear this device.
func (s *Service) deleteEntries(ctx context.Context, entries []domainhistory.Entry) (int, error) {
	if len(entries) == 0 {
		return 0, nil
	}
	deletedAt := s.now()
	writer := ""
	deletions := make([]domainhistory.EntryDeletion, 0, len(entries))
	for _, entry := range entries {
		deletion := domainhistory.EntryDeletion{ID: entry.ID}
		if entry.HasUUIDIdentity() {
			if writer == "" {
				var err error
				writer, err = s.store.EnsureWriterUUID(ctx, s.newWriterID)
				if err != nil {
					return 0, err
				}
			}
			payload, err := json.Marshal(domainhistory.DeleteRecordPayload{
				HistoryID: entry.ID,
				DeletedAt: deletedAt,
			})
			if err != nil {
				return 0, fmt.Errorf("history: encode delete record: %w", err)
			}
			deletion.Record = s.localRecord(writer, entry.ID,
				domainhistory.RecordKindDelete, deletedAt, payload)
		}
		deletions = append(deletions, deletion)
	}
	removed, err := s.store.SoftDeleteHistoryEntries(ctx, deletions, deletedAt)
	if err != nil {
		return 0, err
	}
	if removed > 0 {
		s.notify()
	}
	return removed, nil
}

// Prune applies the configured Connection Log retention. Command History is
// never pruned automatically and is left untouched.
func (s *Service) Prune(ctx context.Context) (PruneResult, error) {
	if s.store == nil {
		return PruneResult{}, ErrInvalid
	}
	retention := s.retentionFor()
	result := PruneResult{}
	if retention.Logs.Bounded() {
		removed, err := s.store.PruneConnectionLogs(ctx, retention.Logs, s.now())
		if err != nil {
			return result, err
		}
		result.ConnectionLogs = removed
	}
	if result.ConnectionLogs > 0 {
		s.notify()
	}
	return result, nil
}

// Subscribe registers fn for record and prune changes. It returns an
// unsubscribe function. The callback runs outside the service lock and only
// asks the UI for a redraw.
func (s *Service) Subscribe(fn func()) func() {
	if fn == nil {
		return func() {}
	}
	s.mu.Lock()
	id := s.nextListen
	s.nextListen++
	s.listeners[id] = fn
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		delete(s.listeners, id)
		s.mu.Unlock()
	}
}

// Revision increments on every change, so a UI that misses a callback can
// still observe that a reload is needed.
func (s *Service) Revision() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.revision
}

func (s *Service) notify() {
	s.mu.Lock()
	s.revision++
	listeners := make([]func(), 0, len(s.listeners))
	for _, listener := range s.listeners {
		listeners = append(listeners, listener)
	}
	s.mu.Unlock()
	for _, listener := range listeners {
		listener()
	}
}

func (s *Service) retentionFor() Retention {
	if s.retention == nil {
		return Retention{}
	}
	return s.retention()
}

// newHistoryID returns a UUIDv7 for a captured or shell-history entry. UUIDv7
// sorts in recording order like the Connection Log identity, but it is the
// wire identity Atuin requires.
func newHistoryID() string {
	return domainhistory.UUIDv7()
}

// newRecordID returns a time-prefixed random identity, so IDs sort in
// recording order and stay unique across processes. Connection Logs keep this
// format; only Command History moved to UUIDv7.
func newRecordID() string {
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		// crypto/rand does not fail on supported platforms; the timestamp and
		// a zero suffix still keep the record usable in unusual sandboxes.
		return fmt.Sprintf("%013x0000000000000000", time.Now().UnixMilli())
	}
	return fmt.Sprintf("%013x%s", time.Now().UnixMilli(), hex.EncodeToString(random[:]))
}
