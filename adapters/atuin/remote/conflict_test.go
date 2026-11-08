package remote

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/mslxl/natcatty/adapters/atuin/wire"
	historysync "github.com/mslxl/natcatty/application/historysync"
	domainhistory "github.com/mslxl/natcatty/domain/history"
	atuinmodel "github.com/mslxl/natcatty/domain/model/atuin"
)

func encodedTestRecord(t *testing.T, record historysync.SyncRecord, key wire.Key) wire.Record {
	t.Helper()
	encoded, err := encodeRecord(record, key)
	if err != nil {
		t.Fatalf("encode test record: %v", err)
	}
	return encoded
}

func newConflictRemote(t *testing.T, routes map[string]http.HandlerFunc, key wire.Key) *Remote {
	t.Helper()
	server, _ := newTestServer(t, historysync.PinnedAtuinVersion, routes)
	return openTestRemote(t, server.URL, historysync.Config{
		AuthMode: atuinmodel.AuthModeSession, Token: "session-token", MasterKey: key.Bytes(),
	})
}

func TestUploadAcceptsAnExactDuplicateAfterReadBack(t *testing.T) {
	key := testKey(t)
	store := &uploadStore{records: make(map[string]wire.Record)}
	remote := newConflictRemote(t, store.routes(t, key), key)
	batch := []historysync.SyncRecord{testCreateRecord(testUploadEntry()), testDeleteRecord()}
	for attempt := 0; attempt < 2; attempt++ {
		if err := remote.Upload(context.Background(), batch); err != nil {
			t.Fatalf("Upload attempt %d: %v", attempt, err)
		}
	}
	if stored := store.count(); stored != len(batch) {
		t.Fatalf("server stored %d records, want %d", stored, len(batch))
	}
}

func TestUploadRejectsAMismatchedDuplicateAtTheSameIndex(t *testing.T) {
	key := testKey(t)
	store := &uploadStore{records: make(map[string]wire.Record)}
	// The server already holds a different record at (writer, history, 0).
	existingEntry := testUploadEntry()
	existingEntry.ID = testHistoryID1
	existingEntry.Command = "server keeps this command"
	existing := testCreateRecord(existingEntry)
	existing.ID = testRecordID1
	existing.Index = 0
	store.insertExisting(encodedTestRecord(t, existing, key))
	remote := newConflictRemote(t, store.routes(t, key), key)

	err := remote.Upload(context.Background(), []historysync.SyncRecord{testCreateRecord(testUploadEntry())})
	if err == nil {
		t.Fatal("Upload accepted a mismatched duplicate")
	}
	if !errors.Is(err, historysync.ErrRemote) {
		t.Fatalf("Upload = %v, want ErrRemote", err)
	}
	// The engine must not retry a permanent series-index collision, so the
	// adapter also reports the engine's own conflict sentinel.
	if !errors.Is(err, historysync.ErrUploadConflict) {
		t.Fatalf("Upload = %v, want ErrUploadConflict", err)
	}
	var conflict *uploadConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("Upload = %v, want a conflict error", err)
	}
	if conflict.index != 0 {
		t.Fatalf("conflict index = %d, want 0", conflict.index)
	}
	for _, secret := range []string{"session-token", "server keeps this command", testUploadEntry().Command} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("conflict error leaked %q: %v", secret, err)
		}
	}
}

func TestUploadRejectsADifferentPayloadWithMatchingOuterMetadata(t *testing.T) {
	key := testKey(t)
	sent := testCreateRecord(testUploadEntry())
	encoded := encodedTestRecord(t, sent, key)
	// Same id, index, host, timestamp, version, and tag, but the payload the
	// server holds decrypts to a different history record.
	tamperedHistory := wire.History{
		ID:        testHistoryID0,
		Timestamp: uint64(testRecordedAt.UnixNano()),
		Command:   "server-tampered-command",
	}
	payload, err := (wire.HistoryRecord{
		Kind: wire.HistoryRecordCreate, History: &tamperedHistory,
	}).MarshalBinary(wire.Version2)
	if err != nil {
		t.Fatalf("marshal tampered payload: %v", err)
	}
	tampered, err := encoded.Encrypt(payload, key)
	if err != nil {
		t.Fatalf("encrypt tampered record: %v", err)
	}

	store := &uploadStore{records: make(map[string]wire.Record)}
	store.insertExisting(tampered)
	remote := newConflictRemote(t, store.routes(t, key), key)
	err = remote.Upload(context.Background(), []historysync.SyncRecord{sent})
	if err == nil {
		t.Fatal("Upload accepted a server record with different plaintext")
	}
	if !errors.Is(err, historysync.ErrRemote) {
		t.Fatalf("Upload = %v, want ErrRemote", err)
	}
	var conflict *uploadConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("Upload = %v, want a conflict error", err)
	}
	if strings.Contains(err.Error(), "server-tampered-command") {
		t.Fatalf("conflict error leaked plaintext: %v", err)
	}
}

func TestUploadRejectsAMissingServerRecord(t *testing.T) {
	key := testKey(t)
	routes := map[string]http.HandlerFunc{
		"/api/v0/record": func(writer http.ResponseWriter, request *http.Request) {
			// Acknowledge without storing anything.
			writer.WriteHeader(http.StatusOK)
		},
		"/api/v0/record/next": func(writer http.ResponseWriter, request *http.Request) {
			writeJSON(t, writer, http.StatusOK, []wire.Record{})
		},
	}
	remote := newConflictRemote(t, routes, key)
	err := remote.Upload(context.Background(), []historysync.SyncRecord{testCreateRecord(testUploadEntry())})
	if err == nil {
		t.Fatal("Upload accepted a record the server did not store")
	}
	if !errors.Is(err, historysync.ErrRemote) {
		t.Fatalf("Upload = %v, want ErrRemote", err)
	}
	// The engine must not retry a permanent series-index collision, so the
	// adapter also reports the engine's own conflict sentinel.
	if !errors.Is(err, historysync.ErrUploadConflict) {
		t.Fatalf("Upload = %v, want ErrUploadConflict", err)
	}
	var conflict *uploadConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("Upload = %v, want a conflict error", err)
	}
	if conflict.index != 0 {
		t.Fatalf("conflict index = %d, want 0", conflict.index)
	}
}

func TestUploadIgnoresExtraServerRecordsInTheBatchRange(t *testing.T) {
	key := testKey(t)
	store := &uploadStore{records: make(map[string]wire.Record)}
	first := testCreateRecord(testUploadEntry())
	secondEntry := testUploadEntry()
	secondEntry.ID = testHistoryID1
	second := testCreateRecord(secondEntry)
	second.ID = testRecordID2
	second.Index = 2
	store.insertExisting(encodedTestRecord(t, first, key))
	store.insertExisting(encodedTestRecord(t, second, key))
	// The server already holds an unrequested record at index 1, for example
	// from an earlier upload by this writer.
	store.insertExisting(mustWireRecord(t, recordSpec{ID: testRecordID1, Index: 1}, key))

	remote := newConflictRemote(t, store.routes(t, key), key)
	if err := remote.Upload(context.Background(), []historysync.SyncRecord{first, second}); err != nil {
		t.Fatalf("Upload with an unrelated record in range: %v", err)
	}
}

func TestUploadReadBackFailureIsNotAcknowledged(t *testing.T) {
	tests := []struct {
		name   string
		status int
		want   error
	}{
		{"server error", http.StatusInternalServerError, historysync.ErrRemote},
		{"rejected session", http.StatusUnauthorized, historysync.ErrAuthExpired},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			key := testKey(t)
			routes := map[string]http.HandlerFunc{
				"/api/v0/record": func(writer http.ResponseWriter, request *http.Request) {
					writer.WriteHeader(http.StatusOK)
				},
				"/api/v0/record/next": func(writer http.ResponseWriter, request *http.Request) {
					http.Error(writer, "read-back-secret", test.status)
				},
			}
			remote := newConflictRemote(t, routes, key)
			err := remote.Upload(context.Background(), []historysync.SyncRecord{testCreateRecord(testUploadEntry())})
			if err == nil {
				t.Fatal("Upload acknowledged after its read-back failed")
			}
			if !errors.Is(err, test.want) {
				t.Fatalf("Upload = %v, want %v", err, test.want)
			}
			for _, secret := range []string{"session-token", "read-back-secret", testUploadEntry().Command} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("read-back error leaked %q: %v", secret, err)
				}
			}
		})
	}
}

func TestUploadRejectsAnInconsistentBatchBeforeAnyRequest(t *testing.T) {
	key := testKey(t)
	valid := testCreateRecord(testUploadEntry())
	second := testCreateRecord(testUploadEntry())
	second.ID = testRecordID2
	second.Index = 2

	tests := []struct {
		name  string
		batch []historysync.SyncRecord
	}{
		{"duplicate index", []historysync.SyncRecord{valid, func() historysync.SyncRecord {
			other := second
			other.Index = valid.Index
			return other
		}()}},
		{"descending index", []historysync.SyncRecord{second, valid}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, recorder := newTestServer(t, historysync.PinnedAtuinVersion, nil)
			remote := openTestRemote(t, server.URL, historysync.Config{
				AuthMode: atuinmodel.AuthModeSession, Token: "session-token", MasterKey: key.Bytes(),
			})
			err := remote.Upload(context.Background(), test.batch)
			if !errors.Is(err, historysync.ErrRemote) {
				t.Fatalf("Upload = %v, want ErrRemote", err)
			}
			if len(recorder.all()) != 0 {
				t.Fatalf("invalid batch made %d requests, want none", len(recorder.all()))
			}
		})
	}
}

func TestUploadVerifiesEveryWriterSeriesInAMixedBatch(t *testing.T) {
	key := testKey(t)
	store := &uploadStore{records: make(map[string]wire.Record)}
	first := testCreateRecord(testUploadEntry())
	otherEntry := testUploadEntry()
	otherEntry.ID = testHistoryID1
	other := testCreateRecord(otherEntry)
	other.ID = testRecordID2
	other.SeriesHost = testOtherWriter
	other.Index = 0

	remote := newConflictRemote(t, store.routes(t, key), key)
	if err := remote.Upload(context.Background(), []historysync.SyncRecord{first, other}); err != nil {
		t.Fatalf("Upload of two writer series: %v", err)
	}
	if stored := store.count(); stored != 2 {
		t.Fatalf("server stored %d records, want 2", stored)
	}
}

func TestUploadMatchesReadBackWhenTheSeriesHostIsNotCanonical(t *testing.T) {
	key := testKey(t)
	store := &uploadStore{records: make(map[string]wire.Record)}
	sent := testCreateRecord(testUploadEntry())
	sent.SeriesHost = strings.ToUpper(testWriterID)
	var queryHosts []string
	routes := store.routes(t, key)
	next := routes["/api/v0/record/next"]
	routes["/api/v0/record/next"] = func(writer http.ResponseWriter, request *http.Request) {
		queryHosts = append(queryHosts, request.URL.Query().Get("host"))
		next(writer, request)
	}
	remote := newConflictRemote(t, routes, key)
	// The server stores and returns the canonical host form.
	if err := remote.Upload(context.Background(), []historysync.SyncRecord{sent}); err != nil {
		t.Fatalf("Upload with a non-canonical series host: %v", err)
	}
	if len(queryHosts) != 1 || queryHosts[0] != testWriterID {
		t.Fatalf("read-back query hosts = %v, want %s", queryHosts, testWriterID)
	}
}

func TestUploadReadBackUsesTheHistorySeriesOnly(t *testing.T) {
	key := testKey(t)
	store := &uploadStore{records: make(map[string]wire.Record)}
	var paths []string
	var queryHosts []string
	routes := store.routes(t, key)
	next := routes["/api/v0/record/next"]
	routes["/api/v0/record/next"] = func(writer http.ResponseWriter, request *http.Request) {
		paths = append(paths, request.URL.Path)
		queryHosts = append(queryHosts, request.URL.Query().Get("host")+"|"+request.URL.Query().Get("tag"))
		next(writer, request)
	}
	remote := newConflictRemote(t, routes, key)
	if err := remote.Upload(context.Background(), []historysync.SyncRecord{testCreateRecord(testUploadEntry())}); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if len(paths) != 1 || paths[0] != "/api/v0/record/next" {
		t.Fatalf("read-back paths = %v, want /api/v0/record/next", paths)
	}
	if len(queryHosts) != 1 || queryHosts[0] != testWriterID+"|"+domainhistory.RecordTagHistory {
		t.Fatalf("read-back query = %v, want the history series", queryHosts)
	}
}
