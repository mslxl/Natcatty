package store

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	apphistory "github.com/mslxl/natcatty/application/history"
	domainhistory "github.com/mslxl/natcatty/domain/history"
)

const (
	testWriterUUID  = "0198c7e2-6a1b-7c3d-8e4f-5a6b7c8d9e0f"
	testEntryOne    = "0198c7e2-6a1b-7c3d-8e4f-5a6b7c8d9e10"
	testEntryTwo    = "0198c7e2-6a1b-7c3d-8e4f-5a6b7c8d9e11"
	testEntryThree  = "0198c7e2-6a1b-7c3d-8e4f-5a6b7c8d9e12"
	testRecordOne   = "0198c7e2-6a1b-7c3d-8e4f-5a6b7c8d9e20"
	testRecordTwo   = "0198c7e2-6a1b-7c3d-8e4f-5a6b7c8d9e21"
	testRecordThree = "0198c7e2-6a1b-7c3d-8e4f-5a6b7c8d9e22"
	testRecordFour  = "0198c7e2-6a1b-7c3d-8e4f-5a6b7c8d9e23"
)

func testHistoryRecord(kind, historyID string, id string, at time.Time) domainhistory.Record {
	return domainhistory.Record{
		ID:         id,
		SeriesHost: testWriterUUID,
		Tag:        domainhistory.RecordTagHistory,
		RecordedAt: at,
		Version:    domainhistory.RecordVersion,
		Kind:       kind,
		HistoryID:  historyID,
		Payload:    `{"command":"ls"}`,
		Direction:  domainhistory.RecordDirectionLocal,
	}
}

func openWriterStore(t *testing.T) (*Store, string) {
	t.Helper()
	store := openHistoryStore(t)
	writer, err := store.EnsureWriterUUID(context.Background(), func() string { return testWriterUUID })
	if err != nil {
		t.Fatalf("EnsureWriterUUID: %v", err)
	}
	return store, writer
}

func TestRecordHistoryEntryWritesProjectionAndOneCreateRecord(t *testing.T) {
	ctx := context.Background()
	store, writer := openWriterStore(t)
	if writer != testWriterUUID {
		t.Fatalf("writer = %q, want %q", writer, testWriterUUID)
	}

	entries := []domainhistory.Entry{
		{ID: testEntryOne, Scope: "local", Command: "ls", RecordedAt: at(9), Source: domainhistory.SourceSession},
		{ID: testEntryTwo, Scope: "local", Command: "pwd", RecordedAt: at(10), Source: domainhistory.SourceSession},
	}
	recordIDs := []string{testRecordOne, testRecordTwo}
	recordTimes := []time.Time{
		at(9).Add(123456789 * time.Nanosecond),
		at(10).Add(987654321 * time.Nanosecond),
	}
	for index, entry := range entries {
		record := testHistoryRecord(domainhistory.RecordKindCreate, entry.ID, recordIDs[index], recordTimes[index])
		if err := store.RecordHistoryEntry(ctx, entry, &record); err != nil {
			t.Fatalf("RecordHistoryEntry %d: %v", index, err)
		}
	}

	stored, err := store.ListHistoryEntries(ctx)
	if err != nil {
		t.Fatalf("ListHistoryEntries: %v", err)
	}
	if len(stored) != 2 {
		t.Fatalf("entries = %d, want 2", len(stored))
	}

	records, err := store.ListHistoryRecords(ctx)
	if err != nil {
		t.Fatalf("ListHistoryRecords: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("records = %d, want exactly one per recorded command", len(records))
	}
	for index, record := range records {
		if record.SeriesHost != writer || record.Tag != domainhistory.RecordTagHistory {
			t.Fatalf("record %d series = %s/%s, want the local writer series", index, record.SeriesHost, record.Tag)
		}
		if record.Index != int64(index) {
			t.Fatalf("record %d index = %d, want %d", index, record.Index, index)
		}
		if record.Kind != domainhistory.RecordKindCreate || record.HistoryID != entries[index].ID {
			t.Fatalf("record %d = %+v, want a create for %s", index, record, entries[index].ID)
		}
		if record.Direction != domainhistory.RecordDirectionLocal || !record.UploadedAt.IsZero() {
			t.Fatalf("record %d direction/upload = %s/%v, want a pending local record", index, record.Direction, record.UploadedAt)
		}
		if record.Version != domainhistory.RecordVersion {
			t.Fatalf("record %d version = %d, want %d", index, record.Version, domainhistory.RecordVersion)
		}
		if !record.RecordedAt.Equal(recordTimes[index]) {
			t.Fatalf("record %d timestamp = %v, want nanosecond fidelity %v", index, record.RecordedAt, recordTimes[index])
		}
	}
}

func TestNewWriterSeriesStartsAtZeroAndContinuesAfterReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vault.db")
	first, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	writer, err := first.EnsureWriterUUID(ctx, func() string { return testWriterUUID })
	if err != nil {
		t.Fatalf("EnsureWriterUUID: %v", err)
	}
	entry := domainhistory.Entry{
		ID: testEntryOne, Scope: "local", Command: "ls",
		RecordedAt: at(9), Source: domainhistory.SourceSession,
	}
	record := testHistoryRecord(domainhistory.RecordKindCreate, entry.ID, testRecordOne, at(9))
	if err := first.RecordHistoryEntry(ctx, entry, &record); err != nil {
		t.Fatalf("first RecordHistoryEntry: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer second.Close()
	reopenedWriter, err := second.EnsureWriterUUID(ctx, func() string {
		t.Fatal("writer UUID was regenerated after reopen")
		return ""
	})
	if err != nil {
		t.Fatalf("EnsureWriterUUID after reopen: %v", err)
	}
	if reopenedWriter != writer || reopenedWriter != testWriterUUID {
		t.Fatalf("writer after reopen = %q, want %q", reopenedWriter, writer)
	}

	nextEntry := domainhistory.Entry{
		ID: testEntryTwo, Scope: "local", Command: "pwd",
		RecordedAt: at(11), Source: domainhistory.SourceSession,
	}
	nextRecord := testHistoryRecord(domainhistory.RecordKindCreate, nextEntry.ID, testRecordTwo, at(11))
	if err := second.RecordHistoryEntry(ctx, nextEntry, &nextRecord); err != nil {
		t.Fatalf("second RecordHistoryEntry: %v", err)
	}
	records, err := second.ListHistoryRecords(ctx)
	if err != nil {
		t.Fatalf("ListHistoryRecords: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("records = %d, want 2", len(records))
	}
	if records[0].Index != 0 || records[1].Index != 1 {
		t.Fatalf("indexes = %d,%d, want 0,1 with no reuse after reopen", records[0].Index, records[1].Index)
	}
}

func TestRecordLogRejectsADuplicateSeriesIndex(t *testing.T) {
	ctx := context.Background()
	store, _ := openWriterStore(t)
	entry := domainhistory.Entry{
		ID: testEntryOne, Scope: "local", Command: "ls",
		RecordedAt: at(9), Source: domainhistory.SourceSession,
	}
	record := testHistoryRecord(domainhistory.RecordKindCreate, entry.ID, testRecordOne, at(9))
	if err := store.RecordHistoryEntry(ctx, entry, &record); err != nil {
		t.Fatalf("RecordHistoryEntry: %v", err)
	}
	_, err := store.db.Execute(ctx, `INSERT INTO history_records
			(id, series_host, tag, idx, recorded_at, version, kind, history_id, payload, direction, uploaded_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		testRecordTwo, testWriterUUID, domainhistory.RecordTagHistory, 0, at(10).UnixNano(),
		domainhistory.RecordVersion, domainhistory.RecordKindCreate, testEntryTwo,
		"{}", domainhistory.RecordDirectionLocal, 0)
	if err == nil {
		t.Fatal("a duplicate (series host, tag, index) was accepted")
	}
}

func TestRecordHistoryEntryRollsBackProjectionWhenTheRecordFails(t *testing.T) {
	ctx := context.Background()
	store, _ := openWriterStore(t)
	if _, err := store.db.Execute(ctx, `CREATE TRIGGER fail_record_insert
		BEFORE INSERT ON history_records
		BEGIN SELECT RAISE(ABORT, 'injected record failure'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	entry := domainhistory.Entry{
		ID: testEntryOne, Scope: "local", Command: "ls",
		RecordedAt: at(9), Source: domainhistory.SourceSession,
	}
	record := testHistoryRecord(domainhistory.RecordKindCreate, entry.ID, testRecordOne, at(9))
	if err := store.RecordHistoryEntry(ctx, entry, &record); err == nil {
		t.Fatal("RecordHistoryEntry succeeded despite the injected record failure")
	}
	entries, err := store.ListHistoryEntries(ctx)
	if err != nil {
		t.Fatalf("ListHistoryEntries: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("projection changed after a failed write: %+v", entries)
	}
	records, err := store.ListHistoryRecords(ctx)
	if err != nil {
		t.Fatalf("ListHistoryRecords: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("record log changed after a failed write: %+v", records)
	}
}

func TestSoftDeleteWritesTheSoftDeleteStateAndOneDeleteRecord(t *testing.T) {
	ctx := context.Background()
	store, _ := openWriterStore(t)
	entry := domainhistory.Entry{
		ID: testEntryOne, Scope: "local", Command: "ls",
		RecordedAt: at(9), Source: domainhistory.SourceSession,
	}
	create := testHistoryRecord(domainhistory.RecordKindCreate, entry.ID, testRecordOne, at(9))
	if err := store.RecordHistoryEntry(ctx, entry, &create); err != nil {
		t.Fatalf("RecordHistoryEntry: %v", err)
	}
	deletedAt := at(10)
	deleteRecord := testHistoryRecord(domainhistory.RecordKindDelete, entry.ID, testRecordTwo, deletedAt)
	if err := store.SoftDeleteHistoryEntry(ctx, entry.ID, deletedAt, &deleteRecord); err != nil {
		t.Fatalf("SoftDeleteHistoryEntry: %v", err)
	}

	// A deleted row is excluded from normal queries but keeps its canonical
	// data, including the deletion time.
	live, err := store.ListHistoryEntries(ctx)
	if err != nil {
		t.Fatalf("ListHistoryEntries: %v", err)
	}
	if len(live) != 0 {
		t.Fatalf("soft-deleted row is still listed: %+v", live)
	}
	stored, found, err := store.HistoryEntry(ctx, entry.ID)
	if err != nil || !found {
		t.Fatalf("HistoryEntry = %v, %v; want the soft-deleted row", found, err)
	}
	if !stored.DeletedAt.Equal(deletedAt) {
		t.Fatalf("DeletedAt = %v, want %v", stored.DeletedAt, deletedAt)
	}

	records, err := store.ListHistoryRecords(ctx)
	if err != nil {
		t.Fatalf("ListHistoryRecords: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("records = %d, want create and delete", len(records))
	}
	tombstone := records[1]
	if tombstone.Kind != domainhistory.RecordKindDelete || tombstone.HistoryID != entry.ID ||
		tombstone.Index != 1 {
		t.Fatalf("tombstone = %+v, want a delete at index 1", tombstone)
	}

	// Deleting again is idempotent and must not append a second tombstone.
	again := testHistoryRecord(domainhistory.RecordKindDelete, entry.ID, testRecordThree, deletedAt)
	if err := store.SoftDeleteHistoryEntry(ctx, entry.ID, deletedAt, &again); err != nil {
		t.Fatalf("repeated SoftDeleteHistoryEntry: %v", err)
	}
	records, _ = store.ListHistoryRecords(ctx)
	if len(records) != 2 {
		t.Fatalf("records = %d after a repeated delete, want 2", len(records))
	}
}

func TestSoftDeleteOfALegacyRowAppendsNoWireRecord(t *testing.T) {
	ctx := context.Background()
	store, _ := openWriterStore(t)
	entry := domainhistory.Entry{
		ID: "legacy-row-1", Scope: "local", Command: "ls",
		RecordedAt: at(9), Source: domainhistory.SourceSession,
	}
	if entry.HasUUIDIdentity() {
		t.Fatal("test entry unexpectedly has a UUID identity")
	}
	// A legacy row has no create record either: it is local-only.
	if err := store.RecordHistoryEntry(ctx, entry, nil); err != nil {
		t.Fatalf("RecordHistoryEntry: %v", err)
	}
	if err := store.SoftDeleteHistoryEntry(ctx, entry.ID, at(10), nil); err != nil {
		t.Fatalf("SoftDeleteHistoryEntry: %v", err)
	}
	stored, found, err := store.HistoryEntry(ctx, entry.ID)
	if err != nil || !found {
		t.Fatalf("HistoryEntry = %v, %v", found, err)
	}
	if stored.DeletedAt.IsZero() {
		t.Fatal("legacy row was not soft-deleted")
	}
	records, err := store.ListHistoryRecords(ctx)
	if err != nil {
		t.Fatalf("ListHistoryRecords: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("legacy row produced wire records: %+v", records)
	}
}

func TestSoftDeleteRollsBackWhenTheTombstoneFails(t *testing.T) {
	ctx := context.Background()
	store, _ := openWriterStore(t)
	entry := domainhistory.Entry{
		ID: testEntryOne, Scope: "local", Command: "ls",
		RecordedAt: at(9), Source: domainhistory.SourceSession,
	}
	create := testHistoryRecord(domainhistory.RecordKindCreate, entry.ID, testRecordOne, at(9))
	if err := store.RecordHistoryEntry(ctx, entry, &create); err != nil {
		t.Fatalf("RecordHistoryEntry: %v", err)
	}
	if _, err := store.db.Execute(ctx, `CREATE TRIGGER fail_record_insert
		BEFORE INSERT ON history_records
		BEGIN SELECT RAISE(ABORT, 'injected record failure'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	tombstone := testHistoryRecord(domainhistory.RecordKindDelete, entry.ID, testRecordTwo, at(10))
	if err := store.SoftDeleteHistoryEntry(ctx, entry.ID, at(10), &tombstone); err == nil {
		t.Fatal("SoftDeleteHistoryEntry succeeded despite the injected tombstone failure")
	}
	stored, found, err := store.HistoryEntry(ctx, entry.ID)
	if err != nil || !found {
		t.Fatalf("HistoryEntry = %v, %v", found, err)
	}
	if !stored.DeletedAt.IsZero() {
		t.Fatalf("projection was soft-deleted after a failed write: %+v", stored)
	}
	live, _ := store.ListHistoryEntries(ctx)
	if len(live) != 1 {
		t.Fatalf("live entries = %d, want the row still visible", len(live))
	}
}

func TestHistoryMetadataRoundTripsAcrossReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vault.db")
	first, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := first.EnsureWriterUUID(ctx, func() string { return testWriterUUID }); err != nil {
		t.Fatalf("EnsureWriterUUID: %v", err)
	}
	want := domainhistory.Metadata{
		ShellHistoryImported:   true,
		KeyFingerprint:         "sha256:ab12cd34",
		UnverifiedKeyConfirmed: true,
		Account: domainhistory.AccountIdentity{
			ServerURL: "https://api.atuin.sh",
			UserID:    "user-1",
		},
		HostImports: map[string]domainhistory.ImportCheckpoint{
			"host-1": {Timestamp: at(9), HistoryID: testEntryOne},
		},
		Series: map[string]domainhistory.SeriesCheckpoint{
			domainhistory.SeriesKey(testWriterUUID, domainhistory.RecordTagHistory): {
				SeriesHost: testWriterUUID, Tag: domainhistory.RecordTagHistory,
				AppliedIndex: -1, UploadedIndex: 4,
			},
			domainhistory.SeriesKey(testEntryThree, domainhistory.RecordTagHistory): {
				SeriesHost: testEntryThree, Tag: domainhistory.RecordTagHistory,
				AppliedIndex: 7, UploadedIndex: -1,
			},
		},
	}
	if err := first.SaveHistoryMetadata(ctx, want); err != nil {
		t.Fatalf("SaveHistoryMetadata: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer second.Close()
	got, err := second.HistoryMetadata(ctx)
	if err != nil {
		t.Fatalf("HistoryMetadata: %v", err)
	}
	want.WriterUUID = testWriterUUID
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("metadata after reopen = %+v, want %+v", got, want)
	}
}

func TestSaveHistoryMetadataReplacesCheckpointsWithoutRewritingTheWriter(t *testing.T) {
	ctx := context.Background()
	store, _ := openWriterStore(t)
	full := domainhistory.Metadata{
		ShellHistoryImported: true,
		KeyFingerprint:       "sha256:ab12cd34",
		Account:              domainhistory.AccountIdentity{ServerURL: "https://api.atuin.sh", UserID: "user-1"},
		HostImports:          map[string]domainhistory.ImportCheckpoint{"host-1": {Timestamp: at(9), HistoryID: testEntryOne}},
		Series: map[string]domainhistory.SeriesCheckpoint{
			domainhistory.SeriesKey(testWriterUUID, domainhistory.RecordTagHistory): {
				SeriesHost: testWriterUUID, AppliedIndex: 2, UploadedIndex: 2,
			},
		},
	}
	if err := store.SaveHistoryMetadata(ctx, full); err != nil {
		t.Fatalf("SaveHistoryMetadata full: %v", err)
	}
	// Empty checkpoints are removed while supplied state survives. The writer
	// UUID is never written through this path, so it cannot be clobbered.
	if err := store.SaveHistoryMetadata(ctx, domainhistory.Metadata{
		ShellHistoryImported: true,
		KeyFingerprint:       "sha256:ab12cd34",
	}); err != nil {
		t.Fatalf("SaveHistoryMetadata replaced: %v", err)
	}
	got, err := store.HistoryMetadata(ctx)
	if err != nil {
		t.Fatalf("HistoryMetadata: %v", err)
	}
	want := domainhistory.Metadata{
		WriterUUID: testWriterUUID, ShellHistoryImported: true,
		KeyFingerprint: "sha256:ab12cd34",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("metadata = %+v, want %+v", got, want)
	}
}

func TestEnsureWriterUUIDGeneratesExactlyOnce(t *testing.T) {
	ctx := context.Background()
	store := openHistoryStore(t)
	calls := 0
	first, err := store.EnsureWriterUUID(ctx, func() string {
		calls++
		return testWriterUUID
	})
	if err != nil {
		t.Fatalf("EnsureWriterUUID: %v", err)
	}
	second, err := store.EnsureWriterUUID(ctx, func() string {
		calls++
		return testEntryOne
	})
	if err != nil {
		t.Fatalf("EnsureWriterUUID again: %v", err)
	}
	if first != testWriterUUID || second != first {
		t.Fatalf("writer UUIDs = %q, %q; want one stable identity", first, second)
	}
	if calls != 1 {
		t.Fatalf("generator calls = %d, want 1", calls)
	}
}

func TestHistoryRecordWriteRequiresAValidWriterUUID(t *testing.T) {
	ctx := context.Background()
	store := openHistoryStore(t)
	if _, err := store.EnsureWriterUUID(ctx, func() string { return "not-a-uuid" }); !errors.Is(err, errInvalidWriterUUID) {
		t.Fatalf("EnsureWriterUUID error = %v, want errInvalidWriterUUID", err)
	}
	metadata, err := store.HistoryMetadata(ctx)
	if err != nil {
		t.Fatalf("HistoryMetadata: %v", err)
	}
	if metadata.WriterUUID != "" {
		t.Fatalf("an invalid writer identity was persisted: %q", metadata.WriterUUID)
	}
}

func TestHistoryRecordLogAndMetadataStayOutsideVaultBackups(t *testing.T) {
	ctx := context.Background()
	store, writer := openWriterStore(t)
	entry := domainhistory.Entry{
		ID: testEntryOne, Scope: "local", Command: "ls",
		RecordedAt: at(9), Source: domainhistory.SourceSession,
	}
	record := testHistoryRecord(domainhistory.RecordKindCreate, entry.ID, testRecordOne, at(9))
	if err := store.RecordHistoryEntry(ctx, entry, &record); err != nil {
		t.Fatalf("RecordHistoryEntry: %v", err)
	}
	if err := store.SaveHistoryMetadata(ctx, domainhistory.Metadata{
		ShellHistoryImported: true,
		Series: map[string]domainhistory.SeriesCheckpoint{
			domainhistory.SeriesKey(writer, domainhistory.RecordTagHistory): {
				SeriesHost: writer, AppliedIndex: 0, UploadedIndex: -1,
			},
		},
	}); err != nil {
		t.Fatalf("SaveHistoryMetadata: %v", err)
	}
	snapshot, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snapshot.SchemaVersion != 1 {
		t.Fatalf("backup schema version = %d, want the unchanged version 1", snapshot.SchemaVersion)
	}
	for _, table := range snapshot.Tables {
		if table.Name == "history_records" || table.Name == "meta" {
			t.Fatalf("the Vault backup carries the runtime table %q", table.Name)
		}
	}
}

func TestConcurrentRecordWritesAssignUniqueMonotonicIndexes(t *testing.T) {
	ctx := context.Background()
	store, writer := openWriterStore(t)
	const count = 24
	var wait sync.WaitGroup
	failures := make(chan error, count)
	for index := 0; index < count; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			entry := domainhistory.Entry{
				ID: domainhistory.UUIDv7(), Scope: "local",
				Command: "ls", RecordedAt: at(9), Source: domainhistory.SourceSession,
			}
			record := testHistoryRecord(domainhistory.RecordKindCreate, entry.ID, domainhistory.UUIDv7(), at(9))
			if err := store.RecordHistoryEntry(ctx, entry, &record); err != nil {
				failures <- err
			}
		}()
	}
	wait.Wait()
	close(failures)
	for err := range failures {
		t.Fatalf("concurrent RecordHistoryEntry: %v", err)
	}

	records, err := store.ListHistoryRecords(ctx)
	if err != nil {
		t.Fatalf("ListHistoryRecords: %v", err)
	}
	if len(records) != count {
		t.Fatalf("records = %d, want %d", len(records), count)
	}
	seen := make(map[int64]bool, count)
	for _, record := range records {
		if record.SeriesHost != writer {
			t.Fatalf("record series = %q, want %q", record.SeriesHost, writer)
		}
		if seen[record.Index] {
			t.Fatalf("index %d was reused", record.Index)
		}
		seen[record.Index] = true
	}
	for index := int64(0); index < count; index++ {
		if !seen[index] {
			t.Fatalf("index %d is missing; got %v", index, seen)
		}
	}
}

func TestImportShellHistoryWritesRowsRecordsAndMarkerTogether(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vault.db")
	first, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	writer, err := first.EnsureWriterUUID(ctx, func() string { return testWriterUUID })
	if err != nil {
		t.Fatalf("EnsureWriterUUID: %v", err)
	}
	imports := []apphistory.ShellHistoryImport{
		{
			Entry: domainhistory.Entry{
				ID: testEntryOne, Scope: "local", Command: "git status", Shell: "zsh",
				Source: domainhistory.SourceShellHistory, Origin: "workstation:alice", RecordedAt: at(9),
			},
			Record: testHistoryRecord(domainhistory.RecordKindCreate, testEntryOne, testRecordOne, at(9)),
		},
		{
			Entry: domainhistory.Entry{
				ID: testEntryTwo, Scope: "local", Command: "secret command", Shell: "bash",
				Source: domainhistory.SourceShellHistory, Origin: "workstation:alice", RecordedAt: at(10),
			},
			Record: testHistoryRecord(domainhistory.RecordKindCreate, testEntryTwo, testRecordTwo, at(10)),
		},
	}
	if err := first.ImportShellHistory(ctx, imports); err != nil {
		t.Fatalf("ImportShellHistory: %v", err)
	}
	live, err := first.ListHistoryEntries(ctx)
	if err != nil {
		t.Fatalf("ListHistoryEntries: %v", err)
	}
	if len(live) != 2 || live[0].ID != testEntryTwo || live[1].ID != testEntryOne {
		t.Fatalf("live entries = %+v, want both imports newest first", live)
	}
	all, err := first.ListHistoryEntriesIncludingDeleted(ctx)
	if err != nil || len(all) != 2 {
		t.Fatalf("all entries = %d, %v; want 2", len(all), err)
	}
	log, err := first.ListHistoryRecords(ctx)
	if err != nil {
		t.Fatalf("ListHistoryRecords: %v", err)
	}
	if len(log) != 2 {
		t.Fatalf("records = %d, want 2 creates", len(log))
	}
	for index, record := range log {
		if record.Kind != domainhistory.RecordKindCreate ||
			record.SeriesHost != writer || record.Index != int64(index) ||
			record.HistoryID != imports[index].Entry.ID {
			t.Fatalf("record %d = %+v", index, record)
		}
	}
	metadata, err := first.HistoryMetadata(ctx)
	if err != nil {
		t.Fatalf("HistoryMetadata: %v", err)
	}
	if !metadata.ShellHistoryImported {
		t.Fatal("ImportShellHistory did not set the durable marker")
	}

	// A repeated import is a no-op even if the caller supplies new rows.
	extra := apphistory.ShellHistoryImport{
		Entry: domainhistory.Entry{
			ID: testEntryThree, Scope: "local", Command: "later",
			Source: domainhistory.SourceShellHistory, RecordedAt: at(11),
		},
		Record: testHistoryRecord(domainhistory.RecordKindCreate, testEntryThree, testRecordThree, at(11)),
	}
	if err := first.ImportShellHistory(ctx, []apphistory.ShellHistoryImport{extra}); err != nil {
		t.Fatalf("repeated ImportShellHistory: %v", err)
	}
	live, _ = first.ListHistoryEntries(ctx)
	if len(live) != 2 {
		t.Fatalf("a repeated import wrote %d rows", len(live))
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer second.Close()
	metadata, err = second.HistoryMetadata(ctx)
	if err != nil || !metadata.ShellHistoryImported {
		t.Fatalf("marker after reopen = %v, %v; want set", metadata.ShellHistoryImported, err)
	}
	live, err = second.ListHistoryEntries(ctx)
	if err != nil || len(live) != 2 {
		t.Fatalf("rows after reopen = %d, %v; want 2", len(live), err)
	}
}

func TestImportShellHistoryRollsBackRowsAndMarkerOnFailure(t *testing.T) {
	ctx := context.Background()
	store, _ := openWriterStore(t)
	imports := []apphistory.ShellHistoryImport{
		{
			Entry: domainhistory.Entry{
				ID: testEntryOne, Scope: "local", Command: "ls",
				Source: domainhistory.SourceShellHistory, RecordedAt: at(9),
			},
			Record: testHistoryRecord(domainhistory.RecordKindCreate, testEntryOne, testRecordOne, at(9)),
		},
		{
			Entry: domainhistory.Entry{
				ID: testEntryTwo, Scope: "local", Command: "pwd",
				Source: domainhistory.SourceShellHistory, RecordedAt: at(10),
			},
			Record: testHistoryRecord(domainhistory.RecordKindCreate, testEntryTwo, testRecordTwo, at(10)),
		},
	}
	if _, err := store.db.Execute(ctx, `CREATE TRIGGER fail_import_record_insert
		BEFORE INSERT ON history_records
		BEGIN SELECT RAISE(ABORT, 'injected import failure'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	if err := store.ImportShellHistory(ctx, imports); err == nil {
		t.Fatal("ImportShellHistory succeeded despite the injected record failure")
	}
	live, err := store.ListHistoryEntriesIncludingDeleted(ctx)
	if err != nil || len(live) != 0 {
		t.Fatalf("failed import left entries = %+v, %v", live, err)
	}
	log, err := store.ListHistoryRecords(ctx)
	if err != nil || len(log) != 0 {
		t.Fatalf("failed import left records = %+v, %v", log, err)
	}
	metadata, err := store.HistoryMetadata(ctx)
	if err != nil {
		t.Fatalf("HistoryMetadata: %v", err)
	}
	if metadata.ShellHistoryImported {
		t.Fatal("a failed import set the durable marker")
	}
}

func TestImportShellHistorySetsMarkerForAnEmptyFileSet(t *testing.T) {
	ctx := context.Background()
	store := openHistoryStore(t)
	if err := store.ImportShellHistory(ctx, nil); err != nil {
		t.Fatalf("ImportShellHistory empty: %v", err)
	}
	metadata, err := store.HistoryMetadata(ctx)
	if err != nil || !metadata.ShellHistoryImported {
		t.Fatalf("empty import marker = %v, %v; want set", metadata.ShellHistoryImported, err)
	}
	log, err := store.ListHistoryRecords(ctx)
	if err != nil || len(log) != 0 {
		t.Fatalf("empty import wrote records = %+v, %v", log, err)
	}
}
func TestImportHostAtuinEntriesWritesOnlyTheProjectionAndCheckpoint(t *testing.T) {
	ctx := context.Background()
	store := openHistoryStore(t)
	entries := []domainhistory.Entry{
		{
			ID: testEntryOne, Scope: "remote:web-1:deploy", Origin: "web-1:deploy",
			Command: "ls", RecordedAt: at(9), Source: domainhistory.SourceAtuinImport,
		},
		{
			ID: testEntryTwo, Scope: "remote:web-1:deploy", Origin: "web-1:deploy",
			Command: "pwd", RecordedAt: at(10), Source: domainhistory.SourceAtuinImport,
		},
	}
	checkpoint := domainhistory.ImportCheckpoint{Timestamp: at(10), HistoryID: testEntryTwo}
	if err := store.ImportHostAtuinEntries(ctx, "host-1", checkpoint, entries); err != nil {
		t.Fatalf("ImportHostAtuinEntries: %v", err)
	}

	stored, err := store.ListHistoryEntries(ctx)
	if err != nil {
		t.Fatalf("ListHistoryEntries: %v", err)
	}
	if len(stored) != 2 {
		t.Fatalf("entries = %d, want 2", len(stored))
	}
	records, err := store.ListHistoryRecords(ctx)
	if err != nil {
		t.Fatalf("ListHistoryRecords: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("an import appended records: %+v", records)
	}
	metadata, err := store.HistoryMetadata(ctx)
	if err != nil {
		t.Fatalf("HistoryMetadata: %v", err)
	}
	if got := metadata.HostImports["host-1"]; got != checkpoint {
		t.Fatalf("checkpoint = %+v, want %+v", got, checkpoint)
	}

	// Re-importing the same snapshot upserts the same history ids instead of
	// duplicating projection rows, and appends no records.
	if err := store.ImportHostAtuinEntries(ctx, "host-1", checkpoint, entries); err != nil {
		t.Fatalf("repeated ImportHostAtuinEntries: %v", err)
	}
	stored, err = store.ListHistoryEntries(ctx)
	if err != nil {
		t.Fatalf("ListHistoryEntries after repeat: %v", err)
	}
	if len(stored) != 2 {
		t.Fatalf("entries after repeat = %d, want 2", len(stored))
	}
	records, err = store.ListHistoryRecords(ctx)
	if err != nil || len(records) != 0 {
		t.Fatalf("records after repeat = %+v, %v", records, err)
	}

	// An older snapshot never moves the stored checkpoint backwards.
	older := domainhistory.Entry{
		ID: testEntryThree, Scope: "remote:web-1:deploy", Origin: "web-1:deploy",
		Command: "uptime", RecordedAt: at(8), Source: domainhistory.SourceAtuinImport,
	}
	olderCheckpoint := domainhistory.ImportCheckpoint{Timestamp: at(8), HistoryID: testEntryThree}
	if err := store.ImportHostAtuinEntries(ctx, "host-1", olderCheckpoint,
		[]domainhistory.Entry{older}); err != nil {
		t.Fatalf("older ImportHostAtuinEntries: %v", err)
	}
	metadata, err = store.HistoryMetadata(ctx)
	if err != nil {
		t.Fatalf("HistoryMetadata after older: %v", err)
	}
	if got := metadata.HostImports["host-1"]; got != checkpoint {
		t.Fatalf("checkpoint moved backwards to %+v", got)
	}
}

func TestImportHostAtuinEntriesRejectsRowsOutsideTheSnapshotContract(t *testing.T) {
	ctx := context.Background()
	store := openHistoryStore(t)
	valid := domainhistory.Entry{
		ID: testEntryOne, Scope: "remote:web-1:deploy", Command: "ls",
		RecordedAt: at(9), Source: domainhistory.SourceAtuinImport,
	}
	checkpoint := domainhistory.ImportCheckpoint{Timestamp: at(9), HistoryID: testEntryOne}

	wrongSource := valid
	wrongSource.ID = testEntryTwo
	wrongSource.Source = domainhistory.SourceSession
	if err := store.ImportHostAtuinEntries(ctx, "host-1", checkpoint,
		[]domainhistory.Entry{wrongSource}); err == nil {
		t.Fatal("a captured row was accepted as a Host Atuin import")
	}

	uncovered := valid
	uncovered.ID = testEntryTwo
	uncovered.RecordedAt = at(10)
	if err := store.ImportHostAtuinEntries(ctx, "host-1", checkpoint,
		[]domainhistory.Entry{uncovered}); err == nil {
		t.Fatal("a row newer than the checkpoint was accepted")
	}

	if stored, err := store.ListHistoryEntries(ctx); err != nil || len(stored) != 0 {
		t.Fatalf("a rejected import wrote rows: %+v, %v", stored, err)
	}
	metadata, err := store.HistoryMetadata(ctx)
	if err != nil {
		t.Fatalf("HistoryMetadata: %v", err)
	}
	if _, found := metadata.HostImports["host-1"]; found {
		t.Fatal("a rejected import advanced the checkpoint")
	}
}

func TestMarkHostAtuinNoticedPersistsOncePerHost(t *testing.T) {
	ctx := context.Background()
	store := openHistoryStore(t)
	first, err := store.MarkHostAtuinNoticed(ctx, "host-1")
	if err != nil || !first {
		t.Fatalf("first = %t, %v", first, err)
	}
	second, err := store.MarkHostAtuinNoticed(ctx, "host-1")
	if err != nil || second {
		t.Fatalf("second = %t, %v", second, err)
	}
	metadata, err := store.HistoryMetadata(ctx)
	if err != nil {
		t.Fatalf("HistoryMetadata: %v", err)
	}
	if !metadata.HostAtuinNotified["host-1"] {
		t.Fatal("the notice marker was not persisted")
	}
}
