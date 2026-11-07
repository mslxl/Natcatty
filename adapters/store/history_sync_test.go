package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	historysync "github.com/mslxl/natcatty/application/historysync"
	domainhistory "github.com/mslxl/natcatty/domain/history"
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
