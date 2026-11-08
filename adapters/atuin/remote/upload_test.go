package remote

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mslxl/natcatty/adapters/atuin/wire"
	historysync "github.com/mslxl/natcatty/application/historysync"
	domainhistory "github.com/mslxl/natcatty/domain/history"
	atuinmodel "github.com/mslxl/natcatty/domain/model/atuin"
)

const testSessionUUID = "018cd4fe-ad89-7597-8525-27a31c998059"

func testUploadEntry() domainhistory.Entry {
	return domainhistory.Entry{
		ID:          testHistoryID0,
		SessionUUID: testSessionUUID,
		Scope:       "default",
		Command:     "cat /etc/shadow",
		CWD:         "/home/alice",
		Shell:       "zsh",
		RecordedAt:  testRecordedAt,
		FinishedAt:  testRecordedAt.Add(time.Second),
		Duration:    time.Second,
		ExitCode:    7,
		HasExitCode: true,
		Origin:      "web-1:deploy",
		Author:      "alice",
		AuthorKind:  domainhistory.AuthorKindUser,
		Intent:      "see what happens",
		Source:      domainhistory.SourceAtuinSync,
	}
}

func testCreateRecord(entry domainhistory.Entry) historysync.SyncRecord {
	return historysync.SyncRecord{
		ID: testRecordID0, SeriesHost: testWriterID,
		Tag: domainhistory.RecordTagHistory, Index: 0,
		RecordedAt: entry.RecordedAt, Version: 2,
		Kind: domainhistory.RecordKindCreate, HistoryID: entry.ID, Entry: &entry,
	}
}

func testDeleteRecord() historysync.SyncRecord {
	return historysync.SyncRecord{
		ID: testRecordID1, SeriesHost: testWriterID,
		Tag: domainhistory.RecordTagHistory, Index: 1,
		RecordedAt: testRecordedAt.Add(time.Minute), Version: 2,
		Kind: domainhistory.RecordKindDelete, HistoryID: testHistoryID1,
		DeletedAt: testRecordedAt.Add(2 * time.Minute),
	}
}

func uploadedRecords(t *testing.T, request requestSnapshot, key wire.Key) []wire.Record {
	t.Helper()
	var records []wire.Record
	if err := json.Unmarshal(request.Body, &records); err != nil {
		t.Fatalf("upload body is not a wire record array: %v", err)
	}
	return records
}

func decryptUploaded(t *testing.T, record wire.Record, key wire.Key) wire.HistoryRecord {
	t.Helper()
	plaintext, err := record.Decrypt(key)
	if err != nil {
		t.Fatalf("decrypt uploaded record %s: %v", record.ID, err)
	}
	wrapper, err := wire.UnmarshalHistoryRecord(record.Version, plaintext)
	if err != nil {
		t.Fatalf("decode uploaded record %s: %v", record.ID, err)
	}
	return wrapper
}

func TestUploadEncodesCreateAndDeleteRecords(t *testing.T) {
	key := testKey(t)
	store := &uploadStore{records: make(map[string]wire.Record)}
	server, recorder := newTestServer(t, historysync.PinnedAtuinVersion, map[string]http.HandlerFunc{
		"/api/v0/record":      newUploadHandler(t, store, key),
		"/api/v0/record/next": store.readBackHandler(t),
	})
	remote := openTestRemote(t, server.URL, historysync.Config{
		AuthMode: atuinmodel.AuthModeSession, Token: "session-token", MasterKey: key.Bytes(),
	})
	entry := testUploadEntry()
	create := testCreateRecord(entry)
	deletion := testDeleteRecord()
	if err := remote.Upload(context.Background(), []historysync.SyncRecord{create, deletion}); err != nil {
		t.Fatalf("Upload: %v", err)
	}

	requests := recorder.all()
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want a POST and a read-back GET", len(requests))
	}
	request := requests[0]
	if request.Method != http.MethodPost || request.Path != "/api/v0/record" {
		t.Fatalf("request = %s %s, want POST /api/v0/record", request.Method, request.Path)
	}
	if got := request.Header.Get("Authorization"); got != "Token session-token" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := request.Header.Get(headerAtuinVersion); got != historysync.PinnedAtuinVersion {
		t.Fatalf("Atuin-Version = %q", got)
	}
	if got := request.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	if bytesContain(request.Body, entry.Command) || bytesContain(request.Body, "session-token") {
		t.Fatal("upload body leaked plaintext command or session token")
	}

	records := uploadedRecords(t, request, key)
	if len(records) != 2 {
		t.Fatalf("uploaded records = %d, want 2", len(records))
	}

	first := records[0]
	if first.ID != testRecordID0 || first.Index != 0 || first.SeriesHost != testWriterID ||
		first.Tag != domainhistory.RecordTagHistory || first.Version != "v2" ||
		first.Timestamp != uint64(testRecordedAt.UnixNano()) {
		t.Fatalf("outer create record = %+v", first)
	}
	created := decryptUploaded(t, first, key)
	if created.Kind != wire.HistoryRecordCreate || created.History == nil {
		t.Fatalf("create payload = %+v", created)
	}
	history := *created.History
	if history.ID != testHistoryID0 ||
		history.Timestamp != uint64(testRecordedAt.UnixNano()) ||
		history.Duration != int64(time.Second) ||
		history.Exit != 7 ||
		history.Command != entry.Command ||
		history.CWD != entry.CWD ||
		history.Session != testSessionUUID ||
		history.Origin != "web-1:deploy" {
		t.Fatalf("history payload = %+v", history)
	}
	if history.Author == nil || *history.Author != "alice" ||
		history.Intent == nil || *history.Intent != "see what happens" ||
		history.Shell == nil || *history.Shell != "zsh" ||
		history.AuthorKind == nil || *history.AuthorKind != wire.AuthorKindUser {
		t.Fatalf("optional V2 fields = %+v", history)
	}

	second := records[1]
	if second.ID != testRecordID1 || second.Index != 1 ||
		second.Timestamp != uint64(testRecordedAt.Add(time.Minute).UnixNano()) {
		t.Fatalf("outer delete record = %+v", second)
	}
	deleted := decryptUploaded(t, second, key)
	if deleted.Kind != wire.HistoryRecordDelete || deleted.DeleteID != testHistoryID1 {
		t.Fatalf("delete payload = %+v", deleted)
	}
}

func TestUploadMapsUnknownDurationAndExitAndAgentKind(t *testing.T) {
	key := testKey(t)
	store := &uploadStore{records: make(map[string]wire.Record)}
	server, recorder := newTestServer(t, historysync.PinnedAtuinVersion, map[string]http.HandlerFunc{
		"/api/v0/record":      newUploadHandler(t, store, key),
		"/api/v0/record/next": store.readBackHandler(t),
	})
	remote := openTestRemote(t, server.URL, historysync.Config{
		AuthMode: atuinmodel.AuthModeSession, Token: "session-token", MasterKey: key.Bytes(),
	})
	entry := testUploadEntry()
	entry.Duration = 0
	entry.FinishedAt = time.Time{}
	entry.HasExitCode = false
	entry.ExitCode = 0
	entry.AuthorKind = domainhistory.AuthorKindAgent
	entry.Intent = ""
	entry.Shell = ""
	record := testCreateRecord(entry)
	if err := remote.Upload(context.Background(), []historysync.SyncRecord{record}); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	uploaded := uploadedRecords(t, recorder.all()[0], key)
	history := *decryptUploaded(t, uploaded[0], key).History
	if history.Exit != -1 {
		t.Fatalf("exit = %d, want -1 for an unknown exit", history.Exit)
	}
	if history.Duration != 0 {
		t.Fatalf("duration = %d, want 0", history.Duration)
	}
	if history.AuthorKind == nil || *history.AuthorKind != wire.AuthorKindAgent {
		t.Fatalf("author kind = %v, want agent", history.AuthorKind)
	}
	if history.Intent != nil || history.Shell != nil {
		t.Fatalf("empty optional fields were written: intent=%v shell=%v", history.Intent, history.Shell)
	}
}

func TestUploadRejectsInvalidRecordsBeforeAnyRequest(t *testing.T) {
	entry := testUploadEntry()
	valid := testCreateRecord(entry)
	tests := []struct {
		name   string
		record historysync.SyncRecord
	}{
		{"non-history tag", func() historysync.SyncRecord {
			record := valid
			record.Tag = "kv"
			return record
		}()},
		{"record id is not a UUID", func() historysync.SyncRecord {
			record := valid
			record.ID = "not-a-uuid"
			return record
		}()},
		{"mismatched history id", func() historysync.SyncRecord {
			record := valid
			record.HistoryID = testHistoryID1
			return record
		}()},
		{"unsupported version", func() historysync.SyncRecord {
			record := valid
			record.Version = 3
			return record
		}()},
		{"non-V2 write", func() historysync.SyncRecord {
			record := valid
			record.Version = 1
			return record
		}()},
		{"create without an entry", func() historysync.SyncRecord {
			record := valid
			record.Entry = nil
			return record
		}()},
		{"unknown kind", func() historysync.SyncRecord {
			record := valid
			record.Kind = "upsert"
			return record
		}()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, recorder := newTestServer(t, historysync.PinnedAtuinVersion, nil)
			remote := openTestRemote(t, server.URL, historysync.Config{
				AuthMode: atuinmodel.AuthModeSession, Token: "session-token", MasterKey: testKeyBytes(t),
			})
			err := remote.Upload(context.Background(), []historysync.SyncRecord{test.record})
			if !errors.Is(err, historysync.ErrRemote) {
				t.Fatalf("Upload = %v, want ErrRemote", err)
			}
			if len(recorder.all()) != 0 {
				t.Fatalf("requests = %d, want none", len(recorder.all()))
			}
		})
	}
}

func TestUploadWithoutAKeyFailsBeforeAnyRequest(t *testing.T) {
	server, recorder := newTestServer(t, historysync.PinnedAtuinVersion, nil)
	remote := openTestRemote(t, server.URL, historysync.Config{
		AuthMode: atuinmodel.AuthModeSession, Token: "session-token",
	})
	err := remote.Upload(context.Background(), []historysync.SyncRecord{testCreateRecord(testUploadEntry())})
	if !errors.Is(err, historysync.ErrKeyMismatch) {
		t.Fatalf("Upload = %v, want ErrKeyMismatch", err)
	}
	if len(recorder.all()) != 0 {
		t.Fatalf("requests = %d, want none", len(recorder.all()))
	}
}

func TestUploadEmptyBatchIsANoOp(t *testing.T) {
	server, recorder := newTestServer(t, historysync.PinnedAtuinVersion, nil)
	remote := openTestRemote(t, server.URL, historysync.Config{
		AuthMode: atuinmodel.AuthModeSession, Token: "session-token", MasterKey: testKeyBytes(t),
	})
	if err := remote.Upload(context.Background(), nil); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if len(recorder.all()) != 0 {
		t.Fatalf("requests = %d, want none", len(recorder.all()))
	}
}

// uploadStore models the pinned server's (user, host, tag, idx) unique key and
// its ON CONFLICT DO NOTHING behaviour. partial commits only the first n
// records of a batch before failing.
type uploadStore struct {
	rejected int
	partial  int
	records  map[string]wire.Record
}

func uploadRecordKey(record wire.Record) string {
	return record.SeriesHost + "\x00" + record.Tag + "\x00" + strconv.FormatUint(record.Index, 10)
}

func (store *uploadStore) commit(records []wire.Record) {
	for _, record := range records {
		key := uploadRecordKey(record)
		if _, exists := store.records[key]; !exists {
			store.records[key] = record
		}
	}
}

// insertExisting seeds the server with a record it already holds, bypassing
// the duplicate-insert rule.
func (store *uploadStore) insertExisting(record wire.Record) {
	store.records[uploadRecordKey(record)] = record
}

func (store *uploadStore) count() int { return len(store.records) }

// routes serves the pinned server's record endpoints. POST ignores a duplicate
// insert, exactly as the server's INSERT ... ON CONFLICT DO NOTHING does.
func (store *uploadStore) routes(t *testing.T, key wire.Key) map[string]http.HandlerFunc {
	t.Helper()
	return map[string]http.HandlerFunc{
		"/api/v0/record":      newUploadHandler(t, store, key),
		"/api/v0/record/next": store.readBackHandler(t),
	}
}

// next serves the pinned (host, tag, start, count) read-back query.
func (store *uploadStore) next(host, tag string, start uint64, count int) []wire.Record {
	records := make([]wire.Record, 0, len(store.records))
	for _, record := range store.records {
		if record.SeriesHost == host && record.Tag == tag && record.Index >= start {
			records = append(records, record)
		}
	}
	sort.Slice(records, func(left, right int) bool {
		return records[left].Index < records[right].Index
	})
	if count >= 0 && len(records) > count {
		records = records[:count]
	}
	return records
}

func (store *uploadStore) readBackHandler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(writer http.ResponseWriter, request *http.Request) {
		query := request.URL.Query()
		start := uint64(0)
		if raw := query.Get("start"); raw != "" {
			parsed, err := strconv.ParseUint(raw, 10, 64)
			if err != nil {
				http.Error(writer, "bad start", http.StatusBadRequest)
				return
			}
			start = parsed
		}
		count, err := strconv.Atoi(query.Get("count"))
		if err != nil || count < 0 {
			http.Error(writer, "bad count", http.StatusBadRequest)
			return
		}
		writeJSON(t, writer, http.StatusOK, store.next(query.Get("host"), query.Get("tag"), start, count))
	}
}

func newUploadHandler(t *testing.T, store *uploadStore, key wire.Key) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		var records []wire.Record
		if err := json.NewDecoder(request.Body).Decode(&records); err != nil {
			t.Errorf("decode upload: %v", err)
			http.Error(writer, "bad body", http.StatusBadRequest)
			return
		}
		for _, record := range records {
			if _, err := record.Decrypt(key); err != nil {
				t.Errorf("server could not decrypt uploaded record: %v", err)
			}
		}
		if store.partial > 0 {
			commit := min(store.partial, len(records))
			store.partial = 0
			store.commit(records[:commit])
			http.Error(writer, "server-secret-body", http.StatusInternalServerError)
			return
		}
		store.commit(records)
		if store.rejected > 0 {
			// The batch is committed; only the acknowledgement is lost.
			store.rejected--
			http.Error(writer, "server-secret-body", http.StatusInternalServerError)
			return
		}
		writer.WriteHeader(http.StatusOK)
	}
}

func TestUploadRetryAfterLostAcknowledgementIsIdempotent(t *testing.T) {
	key := testKey(t)
	store := &uploadStore{records: make(map[string]wire.Record), rejected: 0}
	server, recorder := newTestServer(t, historysync.PinnedAtuinVersion, map[string]http.HandlerFunc{
		"/api/v0/record":      newUploadHandler(t, store, key),
		"/api/v0/record/next": store.readBackHandler(t),
	})
	remote := openTestRemote(t, server.URL, historysync.Config{
		AuthMode: atuinmodel.AuthModeSession, Token: "session-token", MasterKey: key.Bytes(),
	})
	batch := []historysync.SyncRecord{testCreateRecord(testUploadEntry()), testDeleteRecord()}

	// First attempt: the server commits the batch but the acknowledgement is
	// lost to a 500. The adapter must not retry on its own.
	store.rejected = 1
	err := remote.Upload(context.Background(), batch)
	if !errors.Is(err, historysync.ErrRemote) {
		t.Fatalf("first Upload = %v, want ErrRemote", err)
	}
	if errors.Is(err, historysync.ErrKeyMismatch) || errors.Is(err, historysync.ErrAuthExpired) {
		t.Fatalf("a server failure was misclassified: %v", err)
	}
	if len(recorder.all()) != 1 {
		t.Fatalf("adapter retried a failed request: %d requests", len(recorder.all()))
	}
	if len(store.records) != 2 {
		t.Fatalf("server committed %d records despite the lost ack, want 2", len(store.records))
	}

	// The caller retries the same batch; the unique series index absorbs it.
	if err := remote.Upload(context.Background(), batch); err != nil {
		t.Fatalf("retry Upload: %v", err)
	}
	if len(store.records) != 2 {
		t.Fatalf("retry duplicated records: %d stored", len(store.records))
	}
	requests := recorder.all()
	if len(requests) != 3 {
		t.Fatalf("requests = %d, want two POSTs and one read-back GET", len(requests))
	}
	// A fresh content encryption key per attempt means the ciphertext differs,
	// but the outer identities the server deduplicates on are stable.
	firstAttempt := uploadedRecords(t, requests[0], key)
	retried := uploadedRecords(t, requests[1], key)
	if len(firstAttempt) != len(retried) {
		t.Fatalf("retry size = %d, want %d", len(retried), len(firstAttempt))
	}
	for index := range firstAttempt {
		if firstAttempt[index].SeriesHost != retried[index].SeriesHost ||
			firstAttempt[index].Tag != retried[index].Tag ||
			firstAttempt[index].Index != retried[index].Index ||
			firstAttempt[index].ID != retried[index].ID {
			t.Fatalf("retry changed a record's deduplication identity: %+v vs %+v",
				firstAttempt[index], retried[index])
		}
	}
}

func TestUploadRetryCompletesAPartiallyAppliedBatch(t *testing.T) {
	key := testKey(t)
	store := &uploadStore{records: make(map[string]wire.Record)}
	server, recorder := newTestServer(t, historysync.PinnedAtuinVersion, map[string]http.HandlerFunc{
		"/api/v0/record":      newUploadHandler(t, store, key),
		"/api/v0/record/next": store.readBackHandler(t),
	})
	remote := openTestRemote(t, server.URL, historysync.Config{
		AuthMode: atuinmodel.AuthModeSession, Token: "session-token", MasterKey: key.Bytes(),
	})
	secondEntry := testUploadEntry()
	secondEntry.ID = testHistoryID1
	batch := []historysync.SyncRecord{
		testCreateRecord(testUploadEntry()),
		testDeleteRecord(),
		testCreateRecord(secondEntry),
	}
	batch[2].ID = testRecordID2
	batch[2].Index = 2

	// Only the first record lands before the request fails. The caller retries
	// the same ordered batch; unique indexes close the gap without duplicates.
	store.partial = 1
	if err := remote.Upload(context.Background(), batch); !errors.Is(err, historysync.ErrRemote) {
		t.Fatalf("first Upload = %v, want ErrRemote", err)
	}
	if len(store.records) != 1 {
		t.Fatalf("stored after partial failure = %d, want 1", len(store.records))
	}
	if err := remote.Upload(context.Background(), batch); err != nil {
		t.Fatalf("retry Upload: %v", err)
	}
	if len(store.records) != len(batch) {
		t.Fatalf("stored after retry = %d, want %d", len(store.records), len(batch))
	}
	if len(recorder.all()) != 3 {
		t.Fatalf("requests = %d, want two POSTs and one read-back GET", len(recorder.all()))
	}
}

func TestUploadTreatsDuplicateConflictAsSuccess(t *testing.T) {
	key := testKey(t)
	store := &uploadStore{records: make(map[string]wire.Record)}
	server, recorder := newTestServer(t, historysync.PinnedAtuinVersion, map[string]http.HandlerFunc{
		"/api/v0/record":      newUploadHandler(t, store, key),
		"/api/v0/record/next": store.readBackHandler(t),
	})
	remote := openTestRemote(t, server.URL, historysync.Config{
		AuthMode: atuinmodel.AuthModeSession, Token: "session-token", MasterKey: key.Bytes(),
	})
	batch := []historysync.SyncRecord{testCreateRecord(testUploadEntry())}
	for attempt := 0; attempt < 2; attempt++ {
		if err := remote.Upload(context.Background(), batch); err != nil {
			t.Fatalf("Upload attempt %d: %v", attempt, err)
		}
	}
	if len(store.records) != 1 {
		t.Fatalf("stored = %d, want the duplicate to be ignored", len(store.records))
	}
	if len(recorder.all()) != 4 {
		t.Fatalf("requests = %d, want two POSTs and two read-back GETs", len(recorder.all()))
	}
}

func TestUploadMapsRejectedSessionToAuthExpired(t *testing.T) {
	server, recorder := newTestServer(t, historysync.PinnedAtuinVersion, map[string]http.HandlerFunc{
		"/api/v0/record": func(writer http.ResponseWriter, request *http.Request) {
			writeJSON(t, writer, http.StatusUnauthorized, map[string]string{"reason": "expired hunter2"})
		},
	})
	remote := openTestRemote(t, server.URL, historysync.Config{
		AuthMode: atuinmodel.AuthModeSession, Token: "session-token", MasterKey: testKeyBytes(t),
	})
	err := remote.Upload(context.Background(), []historysync.SyncRecord{testCreateRecord(testUploadEntry())})
	if !errors.Is(err, historysync.ErrAuthExpired) {
		t.Fatalf("Upload = %v, want ErrAuthExpired", err)
	}
	if len(recorder.all()) != 1 {
		t.Fatalf("requests = %d, want 1", len(recorder.all()))
	}
	for _, secret := range []string{"session-token", "hunter2", "server-secret-body", testUploadEntry().Command} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaked %q: %v", secret, err)
		}
	}
}

func TestUploadServerFailureIsNotRetriedAndDoesNotLeak(t *testing.T) {
	server, recorder := newTestServer(t, historysync.PinnedAtuinVersion, map[string]http.HandlerFunc{
		"/api/v0/record": func(writer http.ResponseWriter, request *http.Request) {
			http.Error(writer, "server-secret-body", http.StatusInternalServerError)
		},
	})
	remote := openTestRemote(t, server.URL, historysync.Config{
		AuthMode: atuinmodel.AuthModeSession, Token: "session-token", MasterKey: testKeyBytes(t),
	})
	err := remote.Upload(context.Background(), []historysync.SyncRecord{testCreateRecord(testUploadEntry())})
	if !errors.Is(err, historysync.ErrRemote) {
		t.Fatalf("Upload = %v, want ErrRemote", err)
	}
	if strings.Contains(err.Error(), "server-secret-body") ||
		strings.Contains(err.Error(), testUploadEntry().Command) {
		t.Fatalf("error leaked a response body or plaintext: %v", err)
	}
	if len(recorder.all()) != 1 {
		t.Fatalf("requests = %d, want 1 (no retry)", len(recorder.all()))
	}
}

func bytesContain(haystack []byte, needle string) bool {
	return strings.Contains(string(haystack), needle)
}
