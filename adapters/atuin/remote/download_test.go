package remote

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/mslxl/natcatty/adapters/atuin/wire"
	historysync "github.com/mslxl/natcatty/application/historysync"
	domainhistory "github.com/mslxl/natcatty/domain/history"
	atuinmodel "github.com/mslxl/natcatty/domain/model/atuin"
)

var testRecordedAt = time.Date(2026, 3, 1, 12, 0, 0, 123456789, time.UTC)

type recordSpec struct {
	ID         string
	SeriesHost string
	Index      uint64
	RecordedAt time.Time
	Version    wire.Version
	Tag        string
	History    *wire.History
	DeleteID   string
}

func mustWireRecord(t *testing.T, spec recordSpec, key wire.Key) wire.Record {
	t.Helper()
	version := spec.Version
	if version == "" {
		version = wire.Version2
	}
	tag := spec.Tag
	if tag == "" {
		tag = domainhistory.RecordTagHistory
	}
	host := spec.SeriesHost
	if host == "" {
		host = testWriterID
	}
	recordedAt := spec.RecordedAt
	if recordedAt.IsZero() {
		recordedAt = testRecordedAt
	}
	var payload []byte
	var err error
	if spec.DeleteID != "" {
		payload, err = (wire.HistoryRecord{
			Kind: wire.HistoryRecordDelete, DeleteID: spec.DeleteID,
		}).MarshalBinary(version)
	} else {
		history := spec.History
		if history == nil {
			history = &wire.History{
				ID: testHistoryID0, Timestamp: uint64(recordedAt.UnixNano()),
				Command: "ls", Session: "018cd4fead897597852527a31c998059",
			}
		}
		payload, err = (wire.HistoryRecord{
			Kind: wire.HistoryRecordCreate, History: history,
		}).MarshalBinary(version)
	}
	if err != nil {
		t.Fatalf("marshal history wrapper: %v", err)
	}
	record := wire.Record{
		ID:         spec.ID,
		Index:      spec.Index,
		SeriesHost: host,
		Timestamp:  uint64(recordedAt.UnixNano()),
		Version:    string(version),
		Tag:        tag,
	}
	encrypted, err := record.Encrypt(payload, key)
	if err != nil {
		t.Fatalf("encrypt record: %v", err)
	}
	return encrypted
}

func stringPtr(value string) *string { return &value }

func authorKindPtr(kind wire.AuthorKind) *wire.AuthorKind { return &kind }

// mustEncryptedRecord builds a history-tagged record around an arbitrary
// payload so a test can prove a malformed plaintext is rejected.
func mustEncryptedRecord(t *testing.T, id string, index uint64, payload []byte, key wire.Key) wire.Record {
	t.Helper()
	record := wire.Record{
		ID: id, Index: index, SeriesHost: testWriterID,
		Timestamp: uint64(testRecordedAt.UnixNano()), Version: "v2",
		Tag: domainhistory.RecordTagHistory,
	}
	encrypted, err := record.Encrypt(payload, key)
	if err != nil {
		t.Fatalf("encrypt record: %v", err)
	}
	return encrypted
}

func TestDownloadPagesAndMapsCreateAndDelete(t *testing.T) {
	key := testKey(t)
	history := wire.History{
		ID:         testHistoryID0,
		Timestamp:  uint64(testRecordedAt.UnixNano()),
		Duration:   int64(250 * time.Millisecond),
		Exit:       0,
		Command:    "  git status  ",
		CWD:        "/src",
		Session:    "018cd4fead897597852527a31c998059",
		Origin:     "web-1:deploy",
		Author:     stringPtr("alice"),
		Intent:     stringPtr("inspect"),
		Shell:      stringPtr("bash"),
		AuthorKind: authorKindPtr(wire.AuthorKindUser),
	}
	create := mustWireRecord(t, recordSpec{
		ID: testRecordID0, Index: 0, History: &history,
	}, key)
	deletion := mustWireRecord(t, recordSpec{
		ID: testRecordID1, Index: 1, DeleteID: testHistoryID1,
		RecordedAt: testRecordedAt.Add(time.Minute),
	}, key)

	server, recorder := newTestServer(t, historysync.PinnedAtuinVersion, map[string]http.HandlerFunc{
		"/api/v0/record/next": func(writer http.ResponseWriter, request *http.Request) {
			if got := request.URL.Query().Get("tag"); got != "history" {
				t.Errorf("tag = %q, want history", got)
			}
			if got := request.URL.Query().Get("host"); got != testWriterID {
				t.Errorf("host = %q, want %s", got, testWriterID)
			}
			start, _ := strconv.Atoi(request.URL.Query().Get("start"))
			count, _ := strconv.Atoi(request.URL.Query().Get("count"))
			all := []wire.Record{create, deletion}
			page := []wire.Record{}
			for index := start; index < len(all) && index < start+count; index++ {
				page = append(page, all[index])
			}
			writeJSON(t, writer, http.StatusOK, page)
		},
	})
	remote := openTestRemote(t, server.URL, historysync.Config{
		AuthMode: atuinmodel.AuthModeSession, Token: "session-token", MasterKey: testKeyBytes(t),
	})
	series := historysync.Series{SeriesHost: testWriterID, Tag: domainhistory.RecordTagHistory}

	first, err := remote.Download(context.Background(), series, 0, 2)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if first.NextIndex != 2 || len(first.Records) != 2 {
		t.Fatalf("first page = %+v, want two records and next 2", first)
	}
	created := first.Records[0]
	if created.Kind != domainhistory.RecordKindCreate || created.Entry == nil {
		t.Fatalf("created record = %+v", created)
	}
	if created.ID != testRecordID0 || created.SeriesHost != testWriterID || created.Index != 0 ||
		created.Version != 2 || created.Tag != domainhistory.RecordTagHistory {
		t.Fatalf("created identity = %+v", created)
	}
	entry := *created.Entry
	if entry.ID != testHistoryID0 ||
		entry.SessionUUID != "018cd4fe-ad89-7597-8525-27a31c998059" ||
		entry.Scope != "remote:web-1:deploy" ||
		entry.Command != "git status" ||
		entry.CWD != "/src" ||
		entry.Shell != "bash" ||
		entry.Origin != "web-1:deploy" ||
		entry.Author != "alice" ||
		entry.AuthorKind != domainhistory.AuthorKindUser ||
		entry.Intent != "inspect" ||
		entry.Source != domainhistory.SourceAtuinSync {
		t.Fatalf("entry = %+v", entry)
	}
	if !entry.RecordedAt.Equal(testRecordedAt) {
		t.Fatalf("recorded at = %v, want %v", entry.RecordedAt, testRecordedAt)
	}
	if entry.Duration != 250*time.Millisecond || !entry.FinishedAt.Equal(testRecordedAt.Add(250*time.Millisecond)) {
		t.Fatalf("duration = %v finish = %v", entry.Duration, entry.FinishedAt)
	}
	if !entry.HasExitCode || entry.ExitCode != 0 {
		t.Fatalf("exit = %d/%v, want 0/true", entry.ExitCode, entry.HasExitCode)
	}

	deleted := first.Records[1]
	if deleted.Kind != domainhistory.RecordKindDelete || deleted.Entry != nil {
		t.Fatalf("delete record = %+v", deleted)
	}
	if deleted.HistoryID != testHistoryID1 || !deleted.DeletedAt.Equal(testRecordedAt.Add(time.Minute)) {
		t.Fatalf("tombstone = %+v", deleted)
	}

	second, err := remote.Download(context.Background(), series, 2, 2)
	if err != nil {
		t.Fatalf("Download at end: %v", err)
	}
	if second.NextIndex != 0 || len(second.Records) != 0 {
		t.Fatalf("empty page = %+v, want empty with next 0", second)
	}

	requests := recorder.all()
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(requests))
	}
	if requests[0].Query == "" || requests[1].Query == "" {
		t.Fatal("download sent no query parameters")
	}
}

func TestDownloadCountIsClampedToAdvertisedPageSize(t *testing.T) {
	key := testKey(t)
	records := []wire.Record{
		mustWireRecord(t, recordSpec{ID: testRecordID0, Index: 0}, key),
		mustWireRecord(t, recordSpec{ID: testRecordID1, Index: 1}, key),
		mustWireRecord(t, recordSpec{ID: testRecordID2, Index: 2}, key),
	}
	server, recorder := newTestServer(t, historysync.PinnedAtuinVersion, map[string]http.HandlerFunc{
		"/api/v0/capabilities": func(writer http.ResponseWriter, request *http.Request) {
			writeJSON(t, writer, http.StatusOK, capabilityDocumentWith(func(document map[string]any) {
				document["capabilities"].(map[string]any)[historysync.CapabilityPageSize] =
					map[string]any{"version": 1, "page_size": 2}
			}))
		},
		"/api/v0/record/next": func(writer http.ResponseWriter, request *http.Request) {
			count, _ := strconv.Atoi(request.URL.Query().Get("count"))
			if count != 2 {
				t.Errorf("count = %d, want the advertised page size 2", count)
			}
			writeJSON(t, writer, http.StatusOK, records[:2])
		},
	})
	remote := openTestRemote(t, server.URL, historysync.Config{
		AuthMode: atuinmodel.AuthModeSession, Token: "session-token", MasterKey: testKeyBytes(t),
	})
	if _, err := remote.Capabilities(context.Background()); err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	series := historysync.Series{SeriesHost: testWriterID, Tag: domainhistory.RecordTagHistory}
	page, err := remote.Download(context.Background(), series, 0, 50)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if len(page.Records) != 2 {
		t.Fatalf("records = %d, want 2", len(page.Records))
	}
	if len(recorder.all()) != 2 {
		t.Fatalf("requests = %d, want 2", len(recorder.all()))
	}
}

func TestDownloadIgnoresUnknownTags(t *testing.T) {
	key := testKey(t)
	first := mustWireRecord(t, recordSpec{ID: testRecordID0, Index: 0}, key)
	unknownTag := mustWireRecord(t, recordSpec{
		ID: testRecordID1, Index: 1, Tag: "kv", Version: wire.Version2,
	}, key)
	second := mustWireRecord(t, recordSpec{ID: testRecordID2, Index: 2}, key)
	server, _ := newTestServer(t, historysync.PinnedAtuinVersion, map[string]http.HandlerFunc{
		"/api/v0/record/next": func(writer http.ResponseWriter, request *http.Request) {
			writeJSON(t, writer, http.StatusOK, []wire.Record{first, unknownTag, second})
		},
	})
	remote := openTestRemote(t, server.URL, historysync.Config{
		AuthMode: atuinmodel.AuthModeSession, Token: "session-token", MasterKey: testKeyBytes(t),
	})
	series := historysync.Series{SeriesHost: testWriterID, Tag: domainhistory.RecordTagHistory}
	page, err := remote.Download(context.Background(), series, 0, 10)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if len(page.Records) != 2 || page.NextIndex != 3 {
		t.Fatalf("page = %+v, want history records at 0 and 2 with next 3", page)
	}
}

func TestDownloadRejectsUnsupportedSeriesBeforeAnyRequest(t *testing.T) {
	server, recorder := newTestServer(t, historysync.PinnedAtuinVersion, nil)
	remote := openTestRemote(t, server.URL, historysync.Config{
		AuthMode: atuinmodel.AuthModeSession, Token: "session-token", MasterKey: testKeyBytes(t),
	})
	for _, series := range []historysync.Series{
		{SeriesHost: testWriterID, Tag: "kv"},
		{SeriesHost: "not-a-uuid", Tag: domainhistory.RecordTagHistory},
	} {
		if _, err := remote.Download(context.Background(), series, 0, 1); !errors.Is(err, historysync.ErrRemote) {
			t.Fatalf("Download(%+v) = %v, want ErrRemote", series, err)
		}
	}
	if len(recorder.all()) != 0 {
		t.Fatalf("requests = %d, want none", len(recorder.all()))
	}
}

func TestDownloadRejectsMalformedPages(t *testing.T) {
	key := testKey(t)
	foreign := mustWireRecord(t, recordSpec{ID: testRecordID0, Index: 0, SeriesHost: testOtherWriter}, key)
	ordered := []wire.Record{
		mustWireRecord(t, recordSpec{ID: testRecordID0, Index: 0}, key),
		mustWireRecord(t, recordSpec{ID: testRecordID1, Index: 1}, key),
	}
	unsupported := mustWireRecord(t, recordSpec{ID: testRecordID0, Index: 0, Version: wire.Version2}, key)
	unsupported.Version = "v3"

	tests := []struct {
		name    string
		body    string
		wantErr error
	}{
		{"invalid JSON", "{", historysync.ErrRemote},
		{"null page", "null", historysync.ErrRemote},
		{"missing required field", `[{"id":"` + testRecordID0 + `"}]`, historysync.ErrRemote},
		{"foreign series host", mustJSON(t, []wire.Record{foreign}), historysync.ErrRemote},
		{"out of order", mustJSON(t, append(ordered, ordered[0])), historysync.ErrRemote},
		{"unsupported record version", mustJSON(t, []wire.Record{unsupported}), historysync.ErrRemote},
		{"corrupt ciphertext", mustJSON(t, []wire.Record{{
			ID: testRecordID0, Index: 0, SeriesHost: testWriterID,
			Timestamp: uint64(testRecordedAt.UnixNano()), Version: "v2", Tag: "history",
			Data: wire.EncryptedData{Raw: "v4.local.AAAA", CEK: `{"wpk":"x","kid":"k4.lid.y"}`},
		}}), historysync.ErrKeyMismatch},
		{"malformed history payload", mustJSON(t, []wire.Record{
			mustEncryptedRecord(t, testRecordID0, 0, []byte{0xff, 0x01}, key),
		}), historysync.ErrRemote},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, _ := newTestServer(t, historysync.PinnedAtuinVersion, map[string]http.HandlerFunc{
				"/api/v0/record/next": func(writer http.ResponseWriter, request *http.Request) {
					_, _ = writer.Write([]byte(test.body))
				},
			})
			remote := openTestRemote(t, server.URL, historysync.Config{
				AuthMode: atuinmodel.AuthModeSession, Token: "session-token", MasterKey: testKeyBytes(t),
			})
			series := historysync.Series{SeriesHost: testWriterID, Tag: domainhistory.RecordTagHistory}
			_, err := remote.Download(context.Background(), series, 0, 10)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Download = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestDownloadWrongKeyIsAKeyMismatch(t *testing.T) {
	otherKey := testKey(t)
	otherKey[0] ^= 0xff
	record := mustWireRecord(t, recordSpec{ID: testRecordID0, Index: 0}, otherKey)
	server, _ := newTestServer(t, historysync.PinnedAtuinVersion, map[string]http.HandlerFunc{
		"/api/v0/record/next": func(writer http.ResponseWriter, request *http.Request) {
			writeJSON(t, writer, http.StatusOK, []wire.Record{record})
		},
	})
	remote := openTestRemote(t, server.URL, historysync.Config{
		AuthMode: atuinmodel.AuthModeSession, Token: "session-token", MasterKey: testKeyBytes(t),
	})
	series := historysync.Series{SeriesHost: testWriterID, Tag: domainhistory.RecordTagHistory}
	_, err := remote.Download(context.Background(), series, 0, 10)
	if !errors.Is(err, historysync.ErrKeyMismatch) {
		t.Fatalf("Download = %v, want ErrKeyMismatch", err)
	}
	if errors.Is(err, historysync.ErrRemote) {
		t.Fatalf("wrong key was reported as a generic remote failure: %v", err)
	}
}

func TestDownloadRejectsMoreRecordsThanRequested(t *testing.T) {
	key := testKey(t)
	records := []wire.Record{
		mustWireRecord(t, recordSpec{ID: testRecordID0, Index: 0}, key),
		mustWireRecord(t, recordSpec{ID: testRecordID1, Index: 1}, key),
	}
	server, _ := newTestServer(t, historysync.PinnedAtuinVersion, map[string]http.HandlerFunc{
		"/api/v0/record/next": func(writer http.ResponseWriter, request *http.Request) {
			writeJSON(t, writer, http.StatusOK, records)
		},
	})
	remote := openTestRemote(t, server.URL, historysync.Config{
		AuthMode: atuinmodel.AuthModeSession, Token: "session-token", MasterKey: testKeyBytes(t),
	})
	series := historysync.Series{SeriesHost: testWriterID, Tag: domainhistory.RecordTagHistory}
	if _, err := remote.Download(context.Background(), series, 0, 1); !errors.Is(err, historysync.ErrRemote) {
		t.Fatalf("Download = %v, want ErrRemote", err)
	}
}

func TestDownloadWithoutAKeyFailsBeforeAnyRequest(t *testing.T) {
	server, recorder := newTestServer(t, historysync.PinnedAtuinVersion, nil)
	remote := openTestRemote(t, server.URL, historysync.Config{
		AuthMode: atuinmodel.AuthModeSession, Token: "session-token",
	})
	series := historysync.Series{SeriesHost: testWriterID, Tag: domainhistory.RecordTagHistory}
	if _, err := remote.Download(context.Background(), series, 0, 1); !errors.Is(err, historysync.ErrKeyMismatch) {
		t.Fatalf("Download = %v, want ErrKeyMismatch", err)
	}
	if len(recorder.all()) != 0 {
		t.Fatalf("requests = %d, want none", len(recorder.all()))
	}
}

func TestDownloadRejectsNegativeStart(t *testing.T) {
	server, recorder := newTestServer(t, historysync.PinnedAtuinVersion, nil)
	remote := openTestRemote(t, server.URL, historysync.Config{
		AuthMode: atuinmodel.AuthModeSession, Token: "session-token", MasterKey: testKeyBytes(t),
	})
	series := historysync.Series{SeriesHost: testWriterID, Tag: domainhistory.RecordTagHistory}
	if _, err := remote.Download(context.Background(), series, -1, 1); !errors.Is(err, historysync.ErrRemote) {
		t.Fatalf("Download = %v, want ErrRemote", err)
	}
	if len(recorder.all()) != 0 {
		t.Fatalf("requests = %d, want none", len(recorder.all()))
	}
}

func TestDownloadMapsMissingAuthorFromOrigin(t *testing.T) {
	key := testKey(t)
	history := wire.History{
		ID:        testHistoryID0,
		Timestamp: uint64(testRecordedAt.UnixNano()),
		Duration:  -1,
		Exit:      -1,
		Command:   "ls",
		Origin:    "web-1:deploy",
		Session:   "",
	}
	record := mustWireRecord(t, recordSpec{ID: testRecordID0, Index: 0, History: &history}, key)
	server, _ := newTestServer(t, historysync.PinnedAtuinVersion, map[string]http.HandlerFunc{
		"/api/v0/record/next": func(writer http.ResponseWriter, request *http.Request) {
			writeJSON(t, writer, http.StatusOK, []wire.Record{record})
		},
	})
	remote := openTestRemote(t, server.URL, historysync.Config{
		AuthMode: atuinmodel.AuthModeSession, Token: "session-token", MasterKey: testKeyBytes(t),
	})
	series := historysync.Series{SeriesHost: testWriterID, Tag: domainhistory.RecordTagHistory}
	page, err := remote.Download(context.Background(), series, 0, 1)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	entry := page.Records[0].Entry
	if entry.Author != "deploy" {
		t.Fatalf("author = %q, want the origin user deploy", entry.Author)
	}
	if entry.SessionUUID != "" {
		t.Fatalf("session = %q, want empty", entry.SessionUUID)
	}
	if entry.HasExitCode {
		t.Fatal("a missing exit was reported as known")
	}
	if !entry.FinishedAt.IsZero() || entry.Duration != 0 {
		t.Fatalf("missing duration = %v/%v, want unknown", entry.Duration, entry.FinishedAt)
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal JSON: %v", err)
	}
	return string(encoded)
}
