package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	apphistory "github.com/mslxl/natcatty/application/history"
	historysync "github.com/mslxl/natcatty/application/historysync"
	"github.com/mslxl/natcatty/application/historysync/fake"
	domainhistory "github.com/mslxl/natcatty/domain/history"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	"gorm.io/gorm"
)

const (
	syncTestWriter = "0198c7e2-6a1b-7c3d-8e4f-5a6b7c8d9e0f"
	syncTestRemote = "0198d000-0000-7000-8000-000000000001"
)

func syncTestEntry(id, command string, at time.Time) domainhistory.Entry {
	return domainhistory.Entry{
		ID: id, Scope: "local", Command: command, RecordedAt: at,
		Source: domainhistory.SourceSession, Shell: "zsh",
	}
}

func syncTestCreateRecord(writer, id string, index int64, at time.Time,
	entry domainhistory.Entry) domainhistory.Record {
	payload, err := json.Marshal(entry)
	if err != nil {
		panic(err)
	}
	return domainhistory.Record{
		ID: "record-" + id, SeriesHost: writer, Tag: domainhistory.RecordTagHistory,
		Index: index, RecordedAt: at, Version: domainhistory.RecordVersion,
		Kind: domainhistory.RecordKindCreate, HistoryID: id, Payload: string(payload),
		Direction: domainhistory.RecordDirectionRemote,
	}
}

func syncTestDeleteRecord(writer, id string, index int64, at time.Time) domainhistory.Record {
	payload, err := json.Marshal(domainhistory.DeleteRecordPayload{
		HistoryID: id, DeletedAt: at,
	})
	if err != nil {
		panic(err)
	}
	return domainhistory.Record{
		ID: "delete-" + id, SeriesHost: writer, Tag: domainhistory.RecordTagHistory,
		Index: index, RecordedAt: at, Version: domainhistory.RecordVersion,
		Kind: domainhistory.RecordKindDelete, HistoryID: id, Payload: string(payload),
		Direction: domainhistory.RecordDirectionRemote,
	}
}

func TestApplyRemotePageCommitsRecordsProjectionsAndCheckpointTogether(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	entry := syncTestEntry("0198d000-0000-7000-8000-000000000011", "remote", at)
	create := syncTestCreateRecord(syncTestRemote, entry.ID, 0, at, entry)
	deletedAt := at.Add(time.Minute)
	deleteRecord := syncTestDeleteRecord(syncTestRemote, entry.ID, 1, deletedAt)
	checkpoint := domainhistory.SeriesCheckpoint{
		SeriesHost: syncTestRemote, Tag: domainhistory.RecordTagHistory,
		AppliedIndex: 1, UploadedIndex: -1,
	}
	page := historysync.RemotePage{
		Checkpoint: checkpoint,
		Records:    []domainhistory.Record{create, deleteRecord},
		Entries:    []domainhistory.Entry{entry},
		Deletes:    []historysync.RemoteDelete{{HistoryID: entry.ID, DeletedAt: deletedAt}},
	}

	if err := store.ApplyRemotePage(ctx, page); err != nil {
		t.Fatalf("ApplyRemotePage: %v", err)
	}
	records, err := store.ListHistoryRecords(ctx)
	if err != nil {
		t.Fatalf("ListHistoryRecords: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("records = %d, want the two remote log rows", len(records))
	}
	if records[0].Direction != domainhistory.RecordDirectionRemote {
		t.Fatalf("direction = %q, want remote", records[0].Direction)
	}
	stored, found, err := store.HistoryEntry(ctx, entry.ID)
	if err != nil || !found {
		t.Fatalf("HistoryEntry = %+v, %v, %v", stored, found, err)
	}
	if stored.DeletedAt.IsZero() {
		t.Fatal("the remote tombstone did not mark the projection deleted")
	}
	live, err := store.ListHistoryEntries(ctx)
	if err != nil {
		t.Fatalf("ListHistoryEntries: %v", err)
	}
	if len(live) != 0 {
		t.Fatalf("live entries = %d, want the soft-deleted row excluded", len(live))
	}
	metadata, err := store.HistoryMetadata(ctx)
	if err != nil {
		t.Fatalf("HistoryMetadata: %v", err)
	}
	got := metadata.Series[domainhistory.SeriesKey(syncTestRemote, domainhistory.RecordTagHistory)]
	if got != checkpoint {
		t.Fatalf("checkpoint = %+v, want %+v", got, checkpoint)
	}

	// A retried page is idempotent.
	if err := store.ApplyRemotePage(ctx, page); err != nil {
		t.Fatalf("reapplied page: %v", err)
	}
	records, err = store.ListHistoryRecords(ctx)
	if err != nil || len(records) != 2 {
		t.Fatalf("records after retry = %d, %v", len(records), err)
	}
}

func TestApplyRemotePageRollsBackEveryRecordAndCheckpointOnFailure(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	existing := syncTestCreateRecord(syncTestRemote, "0198d000-0000-7000-8000-000000000021",
		2, at, syncTestEntry("0198d000-0000-7000-8000-000000000021", "existing", at))
	if err := store.ApplyRemotePage(ctx, historysync.RemotePage{
		Checkpoint: domainhistory.SeriesCheckpoint{
			SeriesHost: syncTestRemote, Tag: domainhistory.RecordTagHistory, AppliedIndex: 2,
		},
		Records: []domainhistory.Record{existing},
		Entries: []domainhistory.Entry{
			syncTestEntry("0198d000-0000-7000-8000-000000000021", "existing", at),
		},
	}); err != nil {
		t.Fatalf("seed page: %v", err)
	}

	// Records are deliberately out of order: the valid index 3 is inserted
	// before the conflicting index 2 fails. The whole transaction must roll
	// back, leaving index 3 and the checkpoint unchanged.
	valid := syncTestCreateRecord(syncTestRemote, "0198d000-0000-7000-8000-000000000022",
		3, at.Add(time.Minute), syncTestEntry("0198d000-0000-7000-8000-000000000022", "new", at))
	conflicting := existing
	conflicting.ID = "a-foreign-record"
	conflicting.Payload = "{}"
	err := store.ApplyRemotePage(ctx, historysync.RemotePage{
		Checkpoint: domainhistory.SeriesCheckpoint{
			SeriesHost: syncTestRemote, Tag: domainhistory.RecordTagHistory, AppliedIndex: 3,
		},
		Records: []domainhistory.Record{valid, conflicting},
		Entries: []domainhistory.Entry{
			syncTestEntry("0198d000-0000-7000-8000-000000000022", "new", at),
		},
	})
	if err == nil {
		t.Fatal("ApplyRemotePage accepted a conflicting record")
	}
	records, err := store.ListHistoryRecords(ctx)
	if err != nil {
		t.Fatalf("ListHistoryRecords: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %d, want only the seeded row after rollback", len(records))
	}
	metadata, err := store.HistoryMetadata(ctx)
	if err != nil {
		t.Fatalf("HistoryMetadata: %v", err)
	}
	got := metadata.Series[domainhistory.SeriesKey(syncTestRemote, domainhistory.RecordTagHistory)]
	if got.AppliedIndex != 2 {
		t.Fatalf("checkpoint advanced to %+v after rollback", got)
	}
	if _, found, _ := store.HistoryEntry(ctx, "0198d000-0000-7000-8000-000000000022"); found {
		t.Fatal("the rolled-back projection was applied")
	}
}

// TestApplyRemotePageReindexesAnUnsyncedCaptureAtomically proves a downloaded
// record can take an index an unsent local capture occupied, with both rows and
// the checkpoint committing together.
func TestApplyRemotePageReindexesAnUnsyncedCaptureAtomically(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	writer, err := store.EnsureWriterUUID(ctx, func() string { return syncTestWriter })
	if err != nil {
		t.Fatalf("EnsureWriterUUID: %v", err)
	}
	captured := recordSyncLocalEntry(t, store, writer,
		"0198d000-0000-7000-8000-000000000401", "captured", at.Add(5*time.Minute))
	serverEntry := syncTestEntry("0198d000-0000-7000-8000-000000000402", "server", at.Add(5*time.Minute))
	serverRecord := syncTestCreateRecord(writer, serverEntry.ID, 5, at.Add(5*time.Minute), serverEntry)

	if err := store.ApplyRemotePage(ctx, historysync.RemotePage{
		Checkpoint: domainhistory.SeriesCheckpoint{
			SeriesHost: writer, Tag: domainhistory.RecordTagHistory, AppliedIndex: 5,
		},
		Records:   []domainhistory.Record{serverRecord},
		Entries:   []domainhistory.Entry{serverEntry},
		Reindexes: []historysync.Reindex{{RecordID: captured.ID + "-record", Index: 6}},
	}); err != nil {
		t.Fatalf("ApplyRemotePage: %v", err)
	}

	records, err := store.ListHistoryRecords(ctx)
	if err != nil {
		t.Fatalf("ListHistoryRecords: %v", err)
	}
	var moved, remote *domainhistory.Record
	for index := range records {
		switch records[index].ID {
		case captured.ID + "-record":
			moved = &records[index]
		case serverRecord.ID:
			remote = &records[index]
		}
	}
	if remote == nil || remote.Index != 5 {
		t.Fatalf("server record = %+v, want index 5", remote)
	}
	if moved == nil || moved.Index != 6 {
		t.Fatalf("capture = %+v, want it moved to index 6", moved)
	}
	if !moved.UploadedAt.IsZero() {
		t.Fatalf("the moved capture was falsely acknowledged: %+v", moved)
	}
	if _, found, err := store.HistoryEntry(ctx, serverEntry.ID); err != nil || !found {
		t.Fatalf("server projection found=%v err=%v, want it applied", found, err)
	}
	metadata, err := store.HistoryMetadata(ctx)
	if err != nil {
		t.Fatalf("HistoryMetadata: %v", err)
	}
	if got := metadata.Series[domainhistory.SeriesKey(writer, domainhistory.RecordTagHistory)]; got.AppliedIndex != 5 {
		t.Fatalf("checkpoint = %+v, want applied index 5", got)
	}
}

// TestApplyRemotePageAcknowledgesMatchedLocalRecordsAtomically proves a page
// that proved the server holds a local event marks that record uploaded in the
// page transaction, tolerates a replay, and rejects an unknown record without
// advancing the checkpoint.
func TestApplyRemotePageAcknowledgesMatchedLocalRecordsAtomically(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	writer, err := store.EnsureWriterUUID(ctx, func() string { return syncTestWriter })
	if err != nil {
		t.Fatalf("EnsureWriterUUID: %v", err)
	}
	local := recordSyncLocalEntry(t, store, writer,
		"0198d000-0000-7000-8000-000000000601", "local", at)
	checkpoint := domainhistory.SeriesCheckpoint{
		SeriesHost: writer, Tag: domainhistory.RecordTagHistory,
		AppliedIndex: 0, UploadedIndex: -1,
	}
	page := historysync.RemotePage{
		Checkpoint:     checkpoint,
		Acknowledged:   []string{local.ID + "-record"},
		AcknowledgedAt: at.Add(time.Minute),
	}
	if err := store.ApplyRemotePage(ctx, page); err != nil {
		t.Fatalf("ApplyRemotePage: %v", err)
	}
	record := storeRecordByID(t, store, local.ID+"-record")
	if !record.UploadedAt.Equal(page.AcknowledgedAt) {
		t.Fatalf("record uploaded at %v, want %v", record.UploadedAt, page.AcknowledgedAt)
	}

	// A retried page is idempotent.
	if err := store.ApplyRemotePage(ctx, page); err != nil {
		t.Fatalf("reapplied page: %v", err)
	}
	record = storeRecordByID(t, store, local.ID+"-record")
	if !record.UploadedAt.Equal(page.AcknowledgedAt) {
		t.Fatalf("record uploaded at %v after replay, want %v", record.UploadedAt, page.AcknowledgedAt)
	}

	// An unknown acknowledgement fails the whole transaction.
	bad := page
	bad.Acknowledged = []string{"not-a-local-record"}
	bad.Checkpoint.AppliedIndex = 3
	if err := store.ApplyRemotePage(ctx, bad); err == nil {
		t.Fatal("ApplyRemotePage accepted an unknown acknowledged record")
	}
	metadata, err := store.HistoryMetadata(ctx)
	if err != nil {
		t.Fatalf("HistoryMetadata: %v", err)
	}
	if got := metadata.Series[domainhistory.SeriesKey(writer, domainhistory.RecordTagHistory)]; got != checkpoint {
		t.Fatalf("checkpoint = %+v, want the failed page rolled back to %+v", got, checkpoint)
	}
}

// TestRestoredLocalCaptureIsReindexedAndUploadedWithRealStore runs the exact
// restoration scenario through the engine and a real SQLite store: older local
// indexes 0-4, a capture at index 5, and server records at indexes 5-6. The
// server record must be applied and the capture must reach the server.
func TestRestoredLocalCaptureIsReindexedAndUploadedWithRealStore(t *testing.T) {
	ctx := context.Background()
	db, _ := openTestStore(t)
	writer, err := db.EnsureWriterUUID(ctx, func() string { return syncTestWriter })
	if err != nil {
		t.Fatalf("EnsureWriterUUID: %v", err)
	}
	at := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	for index := 0; index < 5; index++ {
		recordSyncLocalEntry(t, db, writer,
			"0198d000-0000-7000-8000-0000000004a"+string(rune('0'+index)),
			"old", at.Add(time.Duration(index)*time.Minute))
	}
	captured := recordSyncLocalEntry(t, db, writer,
		"0198d000-0000-7000-8000-0000000004b1", "captured after restore", at.Add(7*time.Minute))

	serverFive := syncTestServerCreateRecord(writer, "server-history-five", "server five", 5, at.Add(5*time.Minute))
	serverSix := syncTestServerCreateRecord(writer, "server-history-six", "server six", 6, at.Add(6*time.Minute))
	metadata, err := db.HistoryMetadata(ctx)
	if err != nil {
		t.Fatalf("HistoryMetadata: %v", err)
	}
	metadata.Account = domainhistory.AccountIdentity{
		ServerURL: "https://atuin.example.test", UserID: "fake-atuin-user",
	}
	if err := db.SaveHistoryMetadata(ctx, metadata); err != nil {
		t.Fatalf("SaveHistoryMetadata: %v", err)
	}
	settings := domainpreferences.Defaults().History
	settings.SyncEnabled = true
	settings.SyncAddress = "https://atuin.example.test"
	factory := fake.NewFactory(fake.Options{Records: []historysync.SyncRecord{serverFive, serverSix}})
	service := historysync.NewService(&restoreSettings{history: settings}, &restoreSecrets{values: map[historysync.SecretRef]string{
		historysync.SessionTokenRef(): "session-token",
		historysync.MasterKeyRef():    base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")),
	}}, db, factory)
	t.Cleanup(service.Stop)

	if _, err := service.SyncNow(ctx); err != nil {
		t.Fatalf("SyncNow: %v", err)
	}
	if _, found, err := db.HistoryEntry(ctx, serverFive.HistoryID); err != nil || !found {
		t.Fatalf("server index 5 projection found=%v err=%v, want it applied", found, err)
	}
	capturedUploaded := false
	for _, record := range factory.ServerRecords() {
		if record.ID == captured.ID+"-record" {
			capturedUploaded = true
		}
	}
	if !capturedUploaded {
		t.Fatalf("the captured local record never reached the server; pending=%d",
			service.Status().PendingUploads)
	}
	records, err := db.ListHistoryRecords(ctx)
	if err != nil {
		t.Fatalf("ListHistoryRecords: %v", err)
	}
	for _, record := range records {
		if record.ID == captured.ID+"-record" {
			if record.Index <= 6 {
				t.Fatalf("capture index = %d, want it moved after the server maximum", record.Index)
			}
			if record.UploadedAt.IsZero() {
				t.Fatalf("the capture was not acknowledged: %+v", record)
			}
		}
	}
	if status := service.Status(); status.PendingUploads != 0 {
		t.Fatalf("pending uploads = %d, want none", status.PendingUploads)
	}
}

// TestRestoredLocalCapturesAtServerMaxWithRealStore mirrors the in-memory
// regression through SQLite: indexes 0-4 are acknowledged, two captures take
// 5-6, and different server records occupy those indexes. The server suffix
// must be applied and both captures reindexed and uploaded, proving the
// rebuilt checkpoint ignores unacknowledged captures even when their indexes
// reach the server's high-water mark.
func TestRestoredLocalCapturesAtServerMaxWithRealStore(t *testing.T) {
	ctx := context.Background()
	db, _ := openTestStore(t)
	writer, err := db.EnsureWriterUUID(ctx, func() string { return syncTestWriter })
	if err != nil {
		t.Fatalf("EnsureWriterUUID: %v", err)
	}
	at := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	var acknowledged []string
	for index := 0; index < 5; index++ {
		entry := recordSyncLocalEntry(t, db, writer,
			"0198d000-0000-7000-8000-0000000004c"+string(rune('0'+index)),
			"old", at.Add(time.Duration(index)*time.Minute))
		acknowledged = append(acknowledged, entry.ID+"-record")
	}
	if err := db.MarkUploaded(ctx, acknowledged, at.Add(time.Hour), domainhistory.SeriesCheckpoint{
		SeriesHost: writer, Tag: domainhistory.RecordTagHistory, AppliedIndex: -1, UploadedIndex: 4,
	}); err != nil {
		t.Fatalf("MarkUploaded: %v", err)
	}
	captures := []domainhistory.Entry{
		recordSyncLocalEntry(t, db, writer,
			"0198d000-0000-7000-8000-0000000004d1", "captured five", at.Add(7*time.Minute)),
		recordSyncLocalEntry(t, db, writer,
			"0198d000-0000-7000-8000-0000000004d2", "captured six", at.Add(8*time.Minute)),
	}

	serverFive := syncTestServerCreateRecord(writer, "server-history-five-c", "server five", 5, at.Add(5*time.Minute))
	serverSix := syncTestServerCreateRecord(writer, "server-history-six-c", "server six", 6, at.Add(6*time.Minute))
	metadata, err := db.HistoryMetadata(ctx)
	if err != nil {
		t.Fatalf("HistoryMetadata: %v", err)
	}
	metadata.Account = domainhistory.AccountIdentity{
		ServerURL: "https://atuin.example.test", UserID: "fake-atuin-user",
	}
	if err := db.SaveHistoryMetadata(ctx, metadata); err != nil {
		t.Fatalf("SaveHistoryMetadata: %v", err)
	}
	settings := domainpreferences.Defaults().History
	settings.SyncEnabled = true
	settings.SyncAddress = "https://atuin.example.test"
	factory := fake.NewFactory(fake.Options{Records: []historysync.SyncRecord{serverFive, serverSix}})
	service := historysync.NewService(&restoreSettings{history: settings}, &restoreSecrets{values: map[historysync.SecretRef]string{
		historysync.SessionTokenRef(): "session-token",
		historysync.MasterKeyRef():    base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")),
	}}, db, factory)
	t.Cleanup(service.Stop)

	if _, err := service.SyncNow(ctx); err != nil {
		t.Fatalf("SyncNow: %v", err)
	}
	if _, found, err := db.HistoryEntry(ctx, serverFive.HistoryID); err != nil || !found {
		t.Fatalf("server index 5 projection found=%v err=%v, want it applied", found, err)
	}
	sent := make(map[string]bool)
	for _, record := range factory.ServerRecords() {
		sent[record.ID] = true
	}
	for _, capture := range captures {
		if !sent[capture.ID+"-record"] {
			t.Fatalf("capture %s never reached the server; pending=%d",
				capture.ID, service.Status().PendingUploads)
		}
	}
	records, err := db.ListHistoryRecords(ctx)
	if err != nil {
		t.Fatalf("ListHistoryRecords: %v", err)
	}
	for _, record := range records {
		for _, capture := range captures {
			if record.ID != capture.ID+"-record" {
				continue
			}
			if record.Index <= 6 {
				t.Fatalf("capture index = %d, want it moved after the server maximum", record.Index)
			}
			if record.UploadedAt.IsZero() {
				t.Fatalf("the capture was not acknowledged: %+v", record)
			}
		}
	}
	metadata, err = db.HistoryMetadata(ctx)
	if err != nil {
		t.Fatalf("HistoryMetadata after sync: %v", err)
	}
	if got := metadata.Series[domainhistory.SeriesKey(writer, domainhistory.RecordTagHistory)]; got.AppliedIndex != 6 || got.UploadedIndex != 8 {
		t.Fatalf("checkpoint = %+v, want applied 6 and uploaded 8", got)
	}
	if status := service.Status(); status.PendingUploads != 0 {
		t.Fatalf("pending uploads = %d, want none", status.PendingUploads)
	}
}

// newStoreSyncService wires the engine to a real SQLite store and the fake
// server, with a stored account matching the configured address.
func newStoreSyncService(t *testing.T, db *Store, factory *fake.Factory) *historysync.Service {
	t.Helper()
	ctx := context.Background()
	metadata, err := db.HistoryMetadata(ctx)
	if err != nil {
		t.Fatalf("HistoryMetadata: %v", err)
	}
	metadata.Account = domainhistory.AccountIdentity{
		ServerURL: "https://atuin.example.test", UserID: "fake-atuin-user",
	}
	if err := db.SaveHistoryMetadata(ctx, metadata); err != nil {
		t.Fatalf("SaveHistoryMetadata: %v", err)
	}
	settings := domainpreferences.Defaults().History
	settings.SyncEnabled = true
	settings.SyncAddress = "https://atuin.example.test"
	service := historysync.NewService(&restoreSettings{history: settings}, &restoreSecrets{values: map[historysync.SecretRef]string{
		historysync.SessionTokenRef(): "session-token",
		historysync.MasterKeyRef():    base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")),
	}}, db, factory)
	t.Cleanup(service.Stop)
	return service
}

// storeRecordByID returns one log record or fails the test.
func storeRecordByID(t *testing.T, db *Store, id string) domainhistory.Record {
	t.Helper()
	records, err := db.ListHistoryRecords(context.Background())
	if err != nil {
		t.Fatalf("ListHistoryRecords: %v", err)
	}
	for _, record := range records {
		if record.ID == id {
			return record
		}
	}
	t.Fatalf("record %s not found in log", id)
	return domainhistory.Record{}
}

// seedStoreAcknowledgedPrefix records count local entries and marks them
// uploaded, returning matching server records for the same indexes.
func seedStoreAcknowledgedPrefix(t *testing.T, db *Store, writer string, count int,
	at time.Time) []historysync.SyncRecord {
	t.Helper()
	var ids []string
	server := make([]historysync.SyncRecord, 0, count)
	for index := 0; index < count; index++ {
		id := "0198d000-0000-7000-8000-0000000005a" + string(rune('0'+index))
		when := at.Add(time.Duration(index) * time.Minute)
		entry := recordSyncLocalEntry(t, db, writer, id, "old", when)
		ids = append(ids, entry.ID+"-record")
		server = append(server, syncTestServerCreateRecord(writer, id, "old", int64(index), when))
	}
	if err := db.MarkUploaded(context.Background(), ids, at.Add(time.Hour), domainhistory.SeriesCheckpoint{
		SeriesHost: writer, Tag: domainhistory.RecordTagHistory,
		AppliedIndex: -1, UploadedIndex: int64(count - 1),
	}); err != nil {
		t.Fatalf("MarkUploaded prefix: %v", err)
	}
	return server
}

// TestServerHoleWithLocalCaptureIsFilledWithRealStore is the SQLite version
// of the server-hole regression: the server has indexes 0-4 and 6, the local
// log has 0-4 acknowledged and one unsent capture at 5. The capture must be
// uploaded into the hole, not acknowledged by the server's high-water mark.
func TestServerHoleWithLocalCaptureIsFilledWithRealStore(t *testing.T) {
	ctx := context.Background()
	db, _ := openTestStore(t)
	writer, err := db.EnsureWriterUUID(ctx, func() string { return syncTestWriter })
	if err != nil {
		t.Fatalf("EnsureWriterUUID: %v", err)
	}
	at := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	server := seedStoreAcknowledgedPrefix(t, db, writer, 5, at)
	captured := recordSyncLocalEntry(t, db, writer, "0198d000-0000-7000-8000-0000000005b1",
		"capture in the server hole", at.Add(7*time.Minute))
	server = append(server, syncTestServerCreateRecord(writer, "server-six", "server six", 6, at.Add(6*time.Minute)))
	factory := fake.NewFactory(fake.Options{Records: server})
	service := newStoreSyncService(t, db, factory)

	if _, err := service.SyncNow(ctx); err != nil {
		t.Fatalf("SyncNow: %v", err)
	}
	stored := storeRecordByID(t, db, captured.ID+"-record")
	if stored.Index != 5 || stored.UploadedAt.IsZero() {
		t.Fatalf("capture = %+v, want index 5 and acknowledged only by a real upload", stored)
	}
	var onServer bool
	for _, record := range factory.ServerRecords() {
		if record.ID == stored.ID && record.Index == 5 {
			onServer = true
		}
	}
	if !onServer {
		t.Fatalf("capture %s was acknowledged but never reached the server: %v",
			captured.ID, factory.Events())
	}
	metadata, err := db.HistoryMetadata(ctx)
	if err != nil {
		t.Fatalf("HistoryMetadata: %v", err)
	}
	if got := metadata.Series[domainhistory.SeriesKey(writer, domainhistory.RecordTagHistory)]; got.AppliedIndex != 6 || got.UploadedIndex != 6 {
		t.Fatalf("checkpoint = %+v, want applied 6 and uploaded 6", got)
	}
	if status := service.Status(); status.PendingUploads != 0 {
		t.Fatalf("pending uploads = %d, want none", status.PendingUploads)
	}
}

// TestServerHoleWithoutLocalCaptureFailsClosedWithRealStore proves an
// uncovered server hole aborts the download before any record is applied or
// acknowledged locally.
func TestServerHoleWithoutLocalCaptureFailsClosedWithRealStore(t *testing.T) {
	ctx := context.Background()
	db, _ := openTestStore(t)
	writer, err := db.EnsureWriterUUID(ctx, func() string { return syncTestWriter })
	if err != nil {
		t.Fatalf("EnsureWriterUUID: %v", err)
	}
	at := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	server := seedStoreAcknowledgedPrefix(t, db, writer, 5, at)
	server = append(server, syncTestServerCreateRecord(writer, "server-six", "server six", 6, at.Add(6*time.Minute)))
	factory := fake.NewFactory(fake.Options{Records: server})
	service := newStoreSyncService(t, db, factory)

	if _, err := service.SyncNow(ctx); !errors.Is(err, historysync.ErrIncompletePage) {
		t.Fatalf("SyncNow = %v, want ErrIncompletePage for the unfilled hole", err)
	}
	if calls := factory.Last().Calls(); calls.Upload != 0 {
		t.Fatalf("upload calls = %d, want none after a failed download", calls.Upload)
	}
	records, err := db.ListHistoryRecords(ctx)
	if err != nil {
		t.Fatalf("ListHistoryRecords: %v", err)
	}
	for _, record := range records {
		if record.SeriesHost != writer {
			continue
		}
		if record.UploadedAt.IsZero() {
			t.Fatalf("record at index %d was left unacknowledged: %+v", record.Index, record)
		}
	}
	metadata, err := db.HistoryMetadata(ctx)
	if err != nil {
		t.Fatalf("HistoryMetadata: %v", err)
	}
	if got := metadata.Series[domainhistory.SeriesKey(writer, domainhistory.RecordTagHistory)]; got.AppliedIndex > 4 {
		t.Fatalf("applied checkpoint = %d, want no advance past the trusted prefix", got.AppliedIndex)
	}
	if status := service.Status(); status.PendingUploads != 0 {
		t.Fatalf("pending uploads = %d, want none", status.PendingUploads)
	}
}

// TestServerMultipleHolesWithLocalCapturesAreFilledWithRealStore proves holes
// in the middle of a downloaded page that are covered by local captures are
// accepted by the download and then filled by the upload.
func TestServerMultipleHolesWithLocalCapturesAreFilledWithRealStore(t *testing.T) {
	ctx := context.Background()
	db, _ := openTestStore(t)
	writer, err := db.EnsureWriterUUID(ctx, func() string { return syncTestWriter })
	if err != nil {
		t.Fatalf("EnsureWriterUUID: %v", err)
	}
	at := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	server := seedStoreAcknowledgedPrefix(t, db, writer, 4, at)
	captures := []domainhistory.Entry{
		recordSyncLocalEntry(t, db, writer,
			"0198d000-0000-7000-8000-0000000005c1", "capture four", at.Add(4*time.Minute)),
		recordSyncLocalEntry(t, db, writer,
			"0198d000-0000-7000-8000-0000000005c2", "capture five", at.Add(5*time.Minute)),
		recordSyncLocalEntry(t, db, writer,
			"0198d000-0000-7000-8000-0000000005c3", "capture seven", at.Add(7*time.Minute)),
	}
	// The third capture was appended at index 6; move it to 7 so the page has
	// a hole before index 6 and another before index 8.
	thirdID := captures[2].ID + "-record"
	if err := db.db.ORMTransaction(ctx, func(tx *gorm.DB) error {
		return tx.Model(&historyRecordSchema{}).Where("id = ?", thirdID).Update("idx", 7).Error
	}); err != nil {
		t.Fatalf("move third capture: %v", err)
	}
	server = append(server,
		syncTestServerCreateRecord(writer, "server-six", "server six", 6, at.Add(6*time.Minute)),
		syncTestServerCreateRecord(writer, "server-eight", "server eight", 8, at.Add(8*time.Minute)))
	factory := fake.NewFactory(fake.Options{Records: server})
	service := newStoreSyncService(t, db, factory)

	if _, err := service.SyncNow(ctx); err != nil {
		t.Fatalf("SyncNow: %v", err)
	}
	onServer := make(map[string]historysync.SyncRecord)
	for _, record := range factory.ServerRecords() {
		onServer[record.ID] = record
	}
	for _, capture := range captures {
		stored := storeRecordByID(t, db, capture.ID+"-record")
		if stored.UploadedAt.IsZero() {
			t.Fatalf("capture %s was not acknowledged: %+v", capture.ID, stored)
		}
		serverRecord, found := onServer[stored.ID]
		if !found {
			t.Fatalf("capture %s was acknowledged but never reached the server: %v",
				capture.ID, factory.Events())
		}
		if serverRecord.Index != stored.Index {
			t.Fatalf("capture %s server index %d, local %d", capture.ID, serverRecord.Index, stored.Index)
		}
	}
	metadata, err := db.HistoryMetadata(ctx)
	if err != nil {
		t.Fatalf("HistoryMetadata: %v", err)
	}
	if got := metadata.Series[domainhistory.SeriesKey(writer, domainhistory.RecordTagHistory)]; got.AppliedIndex != 8 || got.UploadedIndex != 8 {
		t.Fatalf("checkpoint = %+v, want applied 8 and uploaded 8", got)
	}
	if status := service.Status(); status.PendingUploads != 0 {
		t.Fatalf("pending uploads = %d, want none", status.PendingUploads)
	}
}

// recordSyncLocalEntry appends one unacknowledged local capture to the store
// and returns the projected entry. The log assigns its writer-series index.
func recordSyncLocalEntry(t *testing.T, store *Store, writer, id, command string, at time.Time) domainhistory.Entry {
	t.Helper()
	entry := syncTestEntry(id, command, at)
	record := domainhistory.Record{
		ID: id + "-record", SeriesHost: writer, Tag: domainhistory.RecordTagHistory,
		RecordedAt: at, Version: domainhistory.RecordVersion,
		Kind: domainhistory.RecordKindCreate, HistoryID: id,
		Payload: mustSyncPayload(t, entry), Direction: domainhistory.RecordDirectionLocal,
	}
	if err := store.RecordHistoryEntry(context.Background(), entry, &record); err != nil {
		t.Fatalf("RecordHistoryEntry: %v", err)
	}
	return entry
}

// syncTestServerCreateRecord builds a server-side create record for the local
// writer series, as another install of the same writer would have uploaded it.
func syncTestServerCreateRecord(writer, id, command string, index int64, at time.Time) historysync.SyncRecord {
	return historysync.SyncRecord{
		ID: "server-record-" + id, SeriesHost: writer, Tag: domainhistory.RecordTagHistory,
		Index: index, RecordedAt: at, Version: domainhistory.RecordVersion,
		Kind: domainhistory.RecordKindCreate, HistoryID: id,
		Entry: &domainhistory.Entry{
			ID: id, Scope: "local", Command: command,
			RecordedAt: at, Source: domainhistory.SourceAtuinSync,
		},
	}
}

func mustSyncPayload(t *testing.T, entry domainhistory.Entry) string {
	t.Helper()
	payload, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal entry: %v", err)
	}
	return string(payload)
}

type restoreSettings struct{ history domainpreferences.History }

func (s *restoreSettings) HistorySettings() domainpreferences.History { return s.history }

func (s *restoreSettings) UpdateHistorySettings(change func(*domainpreferences.History)) error {
	change(&s.history)
	return nil
}

type restoreSecrets struct {
	values map[historysync.SecretRef]string
}

func (s *restoreSecrets) Mode() historysync.SecretMode { return historysync.SecretModeOS }

func (s *restoreSecrets) Has(_ context.Context, ref historysync.SecretRef) (bool, error) {
	_, ok := s.values[ref]
	return ok, nil
}

func (s *restoreSecrets) Get(_ context.Context, ref historysync.SecretRef) (string, error) {
	value, ok := s.values[ref]
	if !ok {
		return "", historysync.ErrSecretNotFound
	}
	return value, nil
}

func (s *restoreSecrets) Set(_ context.Context, ref historysync.SecretRef, value string) error {
	s.values[ref] = value
	return nil
}

func (s *restoreSecrets) Delete(_ context.Context, ref historysync.SecretRef) error {
	delete(s.values, ref)
	return nil
}

func TestMarkAndResetUploadedUpdateTheLogAndCheckpoint(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	writer, err := store.EnsureWriterUUID(ctx, func() string { return syncTestWriter })
	if err != nil {
		t.Fatalf("EnsureWriterUUID: %v", err)
	}
	entry := syncTestEntry("0198d000-0000-7000-8000-000000000031", "local", at)
	record := domainhistory.Record{
		ID: "local-record-1", SeriesHost: writer, Tag: domainhistory.RecordTagHistory,
		RecordedAt: at, Version: domainhistory.RecordVersion,
		Kind: domainhistory.RecordKindCreate, HistoryID: entry.ID, Payload: "{}",
		Direction: domainhistory.RecordDirectionLocal,
	}
	if err := store.RecordHistoryEntry(ctx, entry, &record); err != nil {
		t.Fatalf("RecordHistoryEntry: %v", err)
	}
	checkpoint := domainhistory.SeriesCheckpoint{
		SeriesHost: writer, Tag: domainhistory.RecordTagHistory,
		AppliedIndex: -1, UploadedIndex: 0,
	}
	uploadedAt := at.Add(time.Minute)
	if err := store.MarkUploaded(ctx, []string{record.ID}, uploadedAt, checkpoint); err != nil {
		t.Fatalf("MarkUploaded: %v", err)
	}
	records, err := store.ListHistoryRecords(ctx)
	if err != nil {
		t.Fatalf("ListHistoryRecords: %v", err)
	}
	if len(records) != 1 || !records[0].UploadedAt.Equal(uploadedAt) {
		t.Fatalf("records = %+v, want the uploaded timestamp", records)
	}
	metadata, err := store.HistoryMetadata(ctx)
	if err != nil {
		t.Fatalf("HistoryMetadata: %v", err)
	}
	if got := metadata.Series[domainhistory.SeriesKey(writer, domainhistory.RecordTagHistory)]; got != checkpoint {
		t.Fatalf("checkpoint = %+v, want %+v", got, checkpoint)
	}

	reset := checkpoint
	reset.UploadedIndex = -1
	if err := store.ResetUploaded(ctx, reset); err != nil {
		t.Fatalf("ResetUploaded: %v", err)
	}
	records, err = store.ListHistoryRecords(ctx)
	if err != nil {
		t.Fatalf("ListHistoryRecords after reset: %v", err)
	}
	if len(records) != 1 || !records[0].UploadedAt.IsZero() {
		t.Fatalf("records = %+v, want the upload mark cleared", records)
	}
	metadata, err = store.HistoryMetadata(ctx)
	if err != nil {
		t.Fatalf("HistoryMetadata after reset: %v", err)
	}
	if got := metadata.Series[domainhistory.SeriesKey(writer, domainhistory.RecordTagHistory)]; got != reset {
		t.Fatalf("checkpoint after reset = %+v, want %+v", got, reset)
	}
}

// TestRemoteTombstoneSuppressesALaterCreateForTheSameHistoryID proves a
// tombstone that arrived before its row still wins when the create is
// downloaded later from a different writer's series. A tombstone is keyed by
// history id, not by the series or page that happened to deliver it.
func TestRemoteTombstoneSuppressesALaterCreateForTheSameHistoryID(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	const historyID = "0198d000-0000-7000-8000-000000000701"
	deletedAt := at.Add(time.Minute)

	tombstone := syncTestDeleteRecord(syncTestRemote, historyID, 0, deletedAt)
	if err := store.ApplyRemotePage(ctx, historysync.RemotePage{
		Checkpoint: domainhistory.SeriesCheckpoint{
			SeriesHost: syncTestRemote, Tag: domainhistory.RecordTagHistory, AppliedIndex: 0,
		},
		Records: []domainhistory.Record{tombstone},
		Deletes: []historysync.RemoteDelete{{HistoryID: historyID, DeletedAt: deletedAt}},
	}); err != nil {
		t.Fatalf("apply unseen tombstone: %v", err)
	}
	if _, found, err := store.HistoryEntry(ctx, historyID); err != nil || found {
		t.Fatalf("an unseen tombstone created a projection: found=%v err=%v", found, err)
	}

	entry := syncTestEntry(historyID, "doomed", at)
	create := syncTestCreateRecord(syncTestWriter, historyID, 0, at, entry)
	if err := store.ApplyRemotePage(ctx, historysync.RemotePage{
		Checkpoint: domainhistory.SeriesCheckpoint{
			SeriesHost: syncTestWriter, Tag: domainhistory.RecordTagHistory, AppliedIndex: 0,
		},
		Records: []domainhistory.Record{create},
		Entries: []domainhistory.Entry{entry},
	}); err != nil {
		t.Fatalf("apply the later create: %v", err)
	}
	stored, found, err := store.HistoryEntry(ctx, historyID)
	if err != nil || !found {
		t.Fatalf("HistoryEntry = %+v, found=%v, err=%v", stored, found, err)
	}
	if stored.DeletedAt.IsZero() {
		t.Fatalf("a later create undid the stored tombstone: %+v", stored)
	}
	live, err := store.ListHistoryEntries(ctx)
	if err != nil {
		t.Fatalf("ListHistoryEntries: %v", err)
	}
	if len(live) != 0 {
		t.Fatalf("live entries = %+v, want the tombstoned row hidden", live)
	}

	// Replaying the create page must not resurrect the row either.
	if err := store.ApplyRemotePage(ctx, historysync.RemotePage{
		Checkpoint: domainhistory.SeriesCheckpoint{
			SeriesHost: syncTestWriter, Tag: domainhistory.RecordTagHistory, AppliedIndex: 0,
		},
		Records: []domainhistory.Record{create},
		Entries: []domainhistory.Entry{entry},
	}); err != nil {
		t.Fatalf("replay the create page: %v", err)
	}
	if stored, _, err := store.HistoryEntry(ctx, historyID); err != nil || stored.DeletedAt.IsZero() {
		t.Fatalf("a replayed create resurrected the row: %+v (err %v)", stored, err)
	}
}

// TestRemoteTombstoneOlderThanALiveLocalRowWins proves deletion is decided by
// tombstone presence, never by comparing timestamps. Clock skew can make the
// other device's tombstone older than the row it deletes.
func TestRemoteTombstoneOlderThanALiveLocalRowWins(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	writer, err := store.EnsureWriterUUID(ctx, func() string { return syncTestWriter })
	if err != nil {
		t.Fatalf("EnsureWriterUUID: %v", err)
	}
	const historyID = "0198d000-0000-7000-8000-000000000702"
	recordSyncLocalEntry(t, store, writer, historyID, "live", at)
	deletedAt := at.Add(-24 * time.Hour)

	tombstone := syncTestDeleteRecord(syncTestRemote, historyID, 0, deletedAt)
	if err := store.ApplyRemotePage(ctx, historysync.RemotePage{
		Checkpoint: domainhistory.SeriesCheckpoint{
			SeriesHost: syncTestRemote, Tag: domainhistory.RecordTagHistory, AppliedIndex: 0,
		},
		Records: []domainhistory.Record{tombstone},
		Deletes: []historysync.RemoteDelete{{HistoryID: historyID, DeletedAt: deletedAt}},
	}); err != nil {
		t.Fatalf("apply older tombstone: %v", err)
	}
	stored, found, err := store.HistoryEntry(ctx, historyID)
	if err != nil || !found {
		t.Fatalf("HistoryEntry = %+v, found=%v, err=%v", stored, found, err)
	}
	if !stored.DeletedAt.Equal(deletedAt) {
		t.Fatalf("deleted at %v, want the tombstone time %v despite the newer row",
			stored.DeletedAt, deletedAt)
	}
	live, err := store.ListHistoryEntries(ctx)
	if err != nil {
		t.Fatalf("ListHistoryEntries: %v", err)
	}
	if len(live) != 0 {
		t.Fatalf("live entries = %+v, want the row hidden", live)
	}
}

// TestMalformedTombstonePayloadFailsClosed proves an unreadable tombstone is
// rejected as a whole page instead of being stored as a live row.
func TestMalformedTombstonePayloadFailsClosed(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	const historyID = "0198d000-0000-7000-8000-000000000703"

	malformed := syncTestDeleteRecord(syncTestRemote, historyID, 0, at)
	malformed.Payload = "{not-json"
	err := store.ApplyRemotePage(ctx, historysync.RemotePage{
		Checkpoint: domainhistory.SeriesCheckpoint{
			SeriesHost: syncTestRemote, Tag: domainhistory.RecordTagHistory, AppliedIndex: 0,
		},
		Records: []domainhistory.Record{malformed},
		Deletes: []historysync.RemoteDelete{{HistoryID: historyID, DeletedAt: at}},
	})
	if err == nil {
		t.Fatal("ApplyRemotePage accepted a malformed tombstone payload")
	}
	records, listErr := store.ListHistoryRecords(ctx)
	if listErr != nil {
		t.Fatalf("ListHistoryRecords: %v", listErr)
	}
	if len(records) != 0 {
		t.Fatalf("records = %+v, want the rejected page rolled back", records)
	}
	metadata, metaErr := store.HistoryMetadata(ctx)
	if metaErr != nil {
		t.Fatalf("HistoryMetadata: %v", metaErr)
	}
	if _, found := metadata.Series[domainhistory.SeriesKey(syncTestRemote, domainhistory.RecordTagHistory)]; found {
		t.Fatalf("checkpoint advanced for a rejected tombstone page: %+v", metadata.Series)
	}
}

// TestMalformedStoredTombstoneBlocksALaterCreate proves a corrupted tombstone
// already in the local log fails the download closed instead of being ignored
// and resurrecting the row.
func TestMalformedStoredTombstoneBlocksALaterCreate(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	const historyID = "0198d000-0000-7000-8000-000000000704"

	if err := store.db.ORMTransaction(ctx, func(tx *gorm.DB) error {
		return tx.Create(&historyRecordSchema{
			ID: "corrupt-tombstone", SeriesHost: syncTestRemote,
			Tag: domainhistory.RecordTagHistory, Index: 0, RecordedAt: at.UnixNano(),
			Version: domainhistory.RecordVersion, Kind: domainhistory.RecordKindDelete,
			HistoryID: historyID, Payload: "{not-json",
			Direction: domainhistory.RecordDirectionRemote,
		}).Error
	}); err != nil {
		t.Fatalf("seed corrupt tombstone: %v", err)
	}

	entry := syncTestEntry(historyID, "doomed", at)
	create := syncTestCreateRecord(syncTestWriter, historyID, 0, at, entry)
	err := store.ApplyRemotePage(ctx, historysync.RemotePage{
		Checkpoint: domainhistory.SeriesCheckpoint{
			SeriesHost: syncTestWriter, Tag: domainhistory.RecordTagHistory, AppliedIndex: 0,
		},
		Records: []domainhistory.Record{create},
		Entries: []domainhistory.Entry{entry},
	})
	if err == nil {
		t.Fatal("ApplyRemotePage ignored a corrupted stored tombstone")
	}
	if _, found, lookupErr := store.HistoryEntry(ctx, historyID); lookupErr != nil || found {
		t.Fatalf("the rejected create was projected: found=%v err=%v", found, lookupErr)
	}
}

// confirmStoreKey marks the account key confirmed so a first sync against an
// empty server is allowed to upload.
func confirmStoreKey(t *testing.T, db *Store) {
	t.Helper()
	ctx := context.Background()
	metadata, err := db.HistoryMetadata(ctx)
	if err != nil {
		t.Fatalf("HistoryMetadata: %v", err)
	}
	metadata.UnverifiedKeyConfirmed = true
	if err := db.SaveHistoryMetadata(ctx, metadata); err != nil {
		t.Fatalf("SaveHistoryMetadata: %v", err)
	}
}

func syncStoreNow(t *testing.T, service *historysync.Service) historysync.SyncResult {
	t.Helper()
	result, err := service.SyncNow(context.Background())
	if err != nil {
		t.Fatalf("SyncNow: %v", err)
	}
	return result
}

// TestTwoDevicesConvergeAndPropagateDeletesWithRealStore runs two writers
// against one fake account through SQLite. Each row appears exactly once on
// both devices, and a delete on either device removes the row on the other.
func TestTwoDevicesConvergeAndPropagateDeletesWithRealStore(t *testing.T) {
	ctx := context.Background()
	storeA, _ := openTestStore(t)
	storeB, _ := openTestStore(t)
	if _, err := storeA.EnsureWriterUUID(ctx, func() string { return syncTestWriter }); err != nil {
		t.Fatalf("writer a: %v", err)
	}
	if _, err := storeB.EnsureWriterUUID(ctx, func() string { return syncTestRemote }); err != nil {
		t.Fatalf("writer b: %v", err)
	}
	historyA := apphistory.NewService(storeA)
	historyB := apphistory.NewService(storeB)
	at := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	entryA := syncTestEntry("0198d000-0000-7000-8000-000000000841",
		"from writer a", at.Add(10*time.Minute))
	entryB := syncTestEntry("0198d000-0000-7000-8000-000000000842",
		"from writer b", at)
	if err := historyA.Record(ctx, entryA); err != nil {
		t.Fatalf("record on a: %v", err)
	}
	if err := historyB.Record(ctx, entryB); err != nil {
		t.Fatalf("record on b: %v", err)
	}
	confirmStoreKey(t, storeA)
	confirmStoreKey(t, storeB)
	factory := fake.NewFactory(fake.Options{})
	serviceA := newStoreSyncService(t, storeA, factory)
	serviceB := newStoreSyncService(t, storeB, factory)

	syncStoreNow(t, serviceA)
	syncStoreNow(t, serviceB)
	syncStoreNow(t, serviceA)

	assertLive := func(label string, historyService *apphistory.Service, want map[string]bool) {
		t.Helper()
		entries, err := historyService.Entries(ctx)
		if err != nil {
			t.Fatalf("%s Entries: %v", label, err)
		}
		if len(entries) != len(want) {
			t.Fatalf("%s live rows = %d, want %d: %+v", label, len(entries), len(want), entries)
		}
		for _, entry := range entries {
			if !want[entry.ID] {
				t.Fatalf("%s shows an unexpected row %s: %+v", label, entry.ID, entries)
			}
		}
	}
	both := map[string]bool{entryA.ID: true, entryB.ID: true}
	assertLive("writer a", historyA, both)
	assertLive("writer b", historyB, both)
	creates := make(map[string]int)
	for _, record := range factory.ServerRecords() {
		if record.Tag != domainhistory.RecordTagHistory {
			t.Fatalf("a non-history tag reached the server: %+v", record)
		}
		if record.Kind == domainhistory.RecordKindCreate {
			creates[record.HistoryID]++
		}
	}
	for _, entry := range []domainhistory.Entry{entryA, entryB} {
		if creates[entry.ID] != 1 {
			t.Fatalf("server holds %d creates for %s, want exactly one",
				creates[entry.ID], entry.ID)
		}
	}

	// Writer A deletes its row; writer B must lose it.
	if err := historyA.DeleteEntry(ctx, entryA.ID); err != nil {
		t.Fatalf("delete on a: %v", err)
	}
	syncStoreNow(t, serviceA)
	syncStoreNow(t, serviceB)
	assertLive("writer a after its delete", historyA, map[string]bool{entryB.ID: true})
	assertLive("writer b after a's delete", historyB, map[string]bool{entryB.ID: true})
	if stored, found, err := storeB.HistoryEntry(ctx, entryA.ID); err != nil || !found ||
		stored.DeletedAt.IsZero() {
		t.Fatalf("writer b's tombstoned row = %+v, found=%v, err=%v", stored, found, err)
	}

	// Writer A deletes writer B's downloaded row: the create lives in B's
	// series while the tombstone lives in A's, so B must apply a cross-series
	// tombstone to a row it created itself.
	if err := historyA.DeleteEntry(ctx, entryB.ID); err != nil {
		t.Fatalf("delete b's row on a: %v", err)
	}
	syncStoreNow(t, serviceA)
	syncStoreNow(t, serviceB)
	assertLive("writer a after deleting b's row", historyA, map[string]bool{})
	assertLive("writer b after a deletes its row", historyB, map[string]bool{})
	if stored, found, err := storeB.HistoryEntry(ctx, entryB.ID); err != nil || !found ||
		stored.DeletedAt.IsZero() {
		t.Fatalf("writer b's cross-series tombstoned row = %+v, found=%v, err=%v",
			stored, found, err)
	}

	tombstones := make(map[string]int)
	for _, record := range factory.ServerRecords() {
		if record.Kind == domainhistory.RecordKindDelete {
			tombstones[record.HistoryID]++
		}
	}
	for _, entry := range []domainhistory.Entry{entryA, entryB} {
		if tombstones[entry.ID] != 1 {
			t.Fatalf("server holds %d tombstones for %s, want exactly one",
				tombstones[entry.ID], entry.ID)
		}
	}
}

// TestPruneAndClearUploadTombstonesOnlyForUUIDRowsWithRealStore proves Prune
// and Clear this device write tombstones for every UUID row they delete, never
// for a legacy non-UUID row, and that the engine uploads those tombstones.
func TestPruneAndClearUploadTombstonesOnlyForUUIDRowsWithRealStore(t *testing.T) {
	ctx := context.Background()
	db, _ := openTestStore(t)
	if _, err := db.EnsureWriterUUID(ctx, func() string { return syncTestWriter }); err != nil {
		t.Fatalf("EnsureWriterUUID: %v", err)
	}
	historyService := apphistory.NewService(db)
	at := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	const (
		pruneOne   = "0198d000-0000-7000-8000-000000000851"
		pruneTwo   = "0198d000-0000-7000-8000-000000000852"
		clearOne   = "0198d000-0000-7000-8000-000000000853"
		clearTwo   = "0198d000-0000-7000-8000-000000000854"
		importedID = "0198d000-0000-7000-8000-000000000855"
		legacyGone = "legacy-prune-row"
		legacyKeep = "legacy-clear-row"
	)
	rows := []domainhistory.Entry{
		{ID: pruneOne, Scope: "local", Command: "git status",
			Source: domainhistory.SourceSession, RecordedAt: at},
		{ID: pruneTwo, Scope: "local", Command: "git fetch",
			Source: domainhistory.SourceSession, RecordedAt: at.Add(time.Minute)},
		{ID: legacyGone, Scope: "local", Command: "git old",
			Source: domainhistory.SourceSession, RecordedAt: at.Add(2 * time.Minute)},
		{ID: clearOne, Scope: "local", Command: "make test",
			Source: domainhistory.SourceShellHistory, RecordedAt: at.Add(3 * time.Minute)},
		{ID: clearTwo, Scope: "local", Command: "make build",
			Source: domainhistory.SourceSession, RecordedAt: at.Add(4 * time.Minute)},
		{ID: legacyKeep, Scope: "local", Command: "whoami",
			Source: domainhistory.SourceSession, RecordedAt: at.Add(5 * time.Minute)},
		{ID: importedID, Scope: "remote:web:deploy", Origin: "web:deploy",
			Command: "uptime", Source: domainhistory.SourceAtuinImport,
			RecordedAt: at.Add(6 * time.Minute)},
	}
	for _, entry := range rows {
		if err := historyService.Record(ctx, entry); err != nil {
			t.Fatalf("record %s: %v", entry.Command, err)
		}
	}
	if removed, err := historyService.PruneHistory(ctx,
		domainhistory.EntryFilter{Query: "git"}, 0); err != nil || removed != 3 {
		t.Fatalf("PruneHistory = %d, %v; want the three git rows", removed, err)
	}
	if removed, err := historyService.ClearThisDevice(ctx); err != nil || removed != 3 {
		t.Fatalf("ClearThisDevice = %d, %v; want the three remaining local rows", removed, err)
	}

	// The local log holds one tombstone per UUID row and none for the legacy
	// rows or the imported projection.
	records, err := db.ListHistoryRecords(ctx)
	if err != nil {
		t.Fatalf("ListHistoryRecords: %v", err)
	}
	deletes := make(map[string]int)
	for _, record := range records {
		if record.Tag != domainhistory.RecordTagHistory {
			t.Fatalf("non-history tag in the local log: %+v", record)
		}
		if record.Kind == domainhistory.RecordKindDelete {
			deletes[record.HistoryID]++
		}
		if record.HistoryID == legacyGone || record.HistoryID == legacyKeep ||
			record.HistoryID == importedID {
			t.Fatalf("an unrepresentable or imported row got a record: %+v", record)
		}
	}
	for _, id := range []string{pruneOne, pruneTwo, clearOne, clearTwo} {
		if deletes[id] != 1 {
			t.Fatalf("tombstones for %s = %d, want one", id, deletes[id])
		}
	}

	confirmStoreKey(t, db)
	factory := fake.NewFactory(fake.Options{})
	service := newStoreSyncService(t, db, factory)
	syncStoreNow(t, service)

	serverDeletes := make(map[string]int)
	serverIDs := make(map[string]bool)
	for _, record := range factory.ServerRecords() {
		if record.Tag != domainhistory.RecordTagHistory {
			t.Fatalf("non-history tag uploaded: %+v", record)
		}
		serverIDs[record.HistoryID] = true
		if record.Kind == domainhistory.RecordKindDelete {
			serverDeletes[record.HistoryID]++
		}
	}
	for _, id := range []string{pruneOne, pruneTwo, clearOne, clearTwo} {
		if serverDeletes[id] != 1 {
			t.Fatalf("uploaded tombstones for %s = %d, want one", id, serverDeletes[id])
		}
	}
	for _, id := range []string{legacyGone, legacyKeep, importedID} {
		if serverIDs[id] {
			t.Fatalf("legacy or imported row %s reached the server", id)
		}
	}

	// Tombstones are ordinary delete records and are retained after upload,
	// not compacted away.
	retained, err := db.ListHistoryRecords(ctx)
	if err != nil {
		t.Fatalf("ListHistoryRecords after sync: %v", err)
	}
	retainedDeletes := make(map[string]int)
	uploadedDeletes := make(map[string]int)
	for _, record := range retained {
		if record.Kind != domainhistory.RecordKindDelete {
			continue
		}
		retainedDeletes[record.HistoryID]++
		if !record.UploadedAt.IsZero() {
			uploadedDeletes[record.HistoryID]++
		}
	}
	for _, id := range []string{pruneOne, pruneTwo, clearOne, clearTwo} {
		if retainedDeletes[id] != 1 || uploadedDeletes[id] != 1 {
			t.Fatalf("tombstone for %s retained/uploaded = %d/%d, want 1/1",
				id, retainedDeletes[id], uploadedDeletes[id])
		}
	}

	// Imported history stays visible; the legacy rows are deleted locally.
	live, err := historyService.Entries(ctx)
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	if len(live) != 1 || live[0].ID != importedID {
		t.Fatalf("live rows = %+v, want only the imported row", live)
	}
	for _, id := range []string{legacyGone, legacyKeep} {
		entry, found, err := db.HistoryEntry(ctx, id)
		if err != nil || !found || entry.DeletedAt.IsZero() {
			t.Fatalf("legacy row %s = %+v, found=%v, err=%v; want it deleted locally",
				id, entry, found, err)
		}
	}
}
