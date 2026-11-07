package remote

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/mslxl/natcatty/adapters/atuin/wire"
	historysync "github.com/mslxl/natcatty/application/historysync"
	atuinmodel "github.com/mslxl/natcatty/domain/model/atuin"
)

func TestValidateMasterKeyDecryptsAnExistingRecord(t *testing.T) {
	key := testKey(t)
	record := mustWireRecord(t, recordSpec{ID: testRecordID0, Index: 0}, key)
	server, recorder := newTestServer(t, historysync.PinnedAtuinVersion, map[string]http.HandlerFunc{
		"/api/v0/record": func(writer http.ResponseWriter, request *http.Request) {
			writeJSON(t, writer, http.StatusOK, map[string]any{
				"hosts": map[string]any{
					testWriterID: map[string]any{"history": 4, "kv": 2},
				},
			})
		},
		"/api/v0/record/next": func(writer http.ResponseWriter, request *http.Request) {
			query := request.URL.Query()
			if query.Get("host") != testWriterID || query.Get("tag") != "history" ||
				query.Get("start") != "0" || query.Get("count") != "1" {
				t.Errorf("validation query = %v", query)
			}
			writeJSON(t, writer, http.StatusOK, []wire.Record{record})
		},
	})
	remote := openTestRemote(t, server.URL, historysync.Config{
		AuthMode: atuinmodel.AuthModeSession, Token: "session-token", MasterKey: key.Bytes(),
	})
	validation, err := remote.ValidateMasterKey(context.Background())
	if err != nil {
		t.Fatalf("ValidateMasterKey: %v", err)
	}
	if validation != historysync.KeyValidationVerified {
		t.Fatalf("validation = %q, want verified", validation)
	}
	requests := recorder.all()
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want status then one record", len(requests))
	}
	for _, request := range requests {
		if got := request.Header.Get("Authorization"); got != "Token session-token" {
			t.Fatalf("Authorization = %q", got)
		}
	}
}

func TestValidateMasterKeyReportsAMismatch(t *testing.T) {
	otherKey := testKey(t)
	otherKey[0] ^= 0xff
	record := mustWireRecord(t, recordSpec{ID: testRecordID0, Index: 0}, otherKey)
	server, _ := newTestServer(t, historysync.PinnedAtuinVersion, map[string]http.HandlerFunc{
		"/api/v0/record": func(writer http.ResponseWriter, request *http.Request) {
			writeJSON(t, writer, http.StatusOK, map[string]any{
				"hosts": map[string]any{testWriterID: map[string]any{"history": 0}},
			})
		},
		"/api/v0/record/next": func(writer http.ResponseWriter, request *http.Request) {
			writeJSON(t, writer, http.StatusOK, []wire.Record{record})
		},
	})
	remote := openTestRemote(t, server.URL, historysync.Config{
		AuthMode: atuinmodel.AuthModeSession, Token: "session-token", MasterKey: testKeyBytes(t),
	})
	validation, err := remote.ValidateMasterKey(context.Background())
	if err != nil {
		t.Fatalf("ValidateMasterKey: %v", err)
	}
	if validation != historysync.KeyValidationMismatch {
		t.Fatalf("validation = %q, want mismatch", validation)
	}
}

func TestValidateMasterKeyIsUnverifiedWithoutHistory(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"empty account", `{"hosts":{}}`},
		{"only other tags", `{"hosts":{"` + testWriterID + `":{"kv":7}}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, recorder := newTestServer(t, historysync.PinnedAtuinVersion, map[string]http.HandlerFunc{
				"/api/v0/record": func(writer http.ResponseWriter, request *http.Request) {
					_, _ = writer.Write([]byte(test.body))
				},
			})
			remote := openTestRemote(t, server.URL, historysync.Config{
				AuthMode: atuinmodel.AuthModeSession, Token: "session-token", MasterKey: testKeyBytes(t),
			})
			validation, err := remote.ValidateMasterKey(context.Background())
			if err != nil {
				t.Fatalf("ValidateMasterKey: %v", err)
			}
			if validation != historysync.KeyValidationUnverified {
				t.Fatalf("validation = %q, want unverified", validation)
			}
			if len(recorder.all()) != 1 {
				t.Fatalf("requests = %d, want only the status read", len(recorder.all()))
			}
		})
	}
}

func TestValidateMasterKeyIsUnverifiedWhenTheAdvertisedRecordIsGone(t *testing.T) {
	server, _ := newTestServer(t, historysync.PinnedAtuinVersion, map[string]http.HandlerFunc{
		"/api/v0/record": func(writer http.ResponseWriter, request *http.Request) {
			writeJSON(t, writer, http.StatusOK, map[string]any{
				"hosts": map[string]any{testWriterID: map[string]any{"history": 5}},
			})
		},
		"/api/v0/record/next": func(writer http.ResponseWriter, request *http.Request) {
			writeJSON(t, writer, http.StatusOK, []wire.Record{})
		},
	})
	remote := openTestRemote(t, server.URL, historysync.Config{
		AuthMode: atuinmodel.AuthModeSession, Token: "session-token", MasterKey: testKeyBytes(t),
	})
	validation, err := remote.ValidateMasterKey(context.Background())
	if err != nil {
		t.Fatalf("ValidateMasterKey: %v", err)
	}
	if validation != historysync.KeyValidationUnverified {
		t.Fatalf("validation = %q, want unverified", validation)
	}
}

func TestValidateMasterKeyRejectsAForeignRecord(t *testing.T) {
	key := testKey(t)
	record := mustWireRecord(t, recordSpec{ID: testRecordID0, Index: 0, SeriesHost: testOtherWriter}, key)
	server, _ := newTestServer(t, historysync.PinnedAtuinVersion, map[string]http.HandlerFunc{
		"/api/v0/record": func(writer http.ResponseWriter, request *http.Request) {
			writeJSON(t, writer, http.StatusOK, map[string]any{
				"hosts": map[string]any{testWriterID: map[string]any{"history": 0}},
			})
		},
		"/api/v0/record/next": func(writer http.ResponseWriter, request *http.Request) {
			writeJSON(t, writer, http.StatusOK, []wire.Record{record})
		},
	})
	remote := openTestRemote(t, server.URL, historysync.Config{
		AuthMode: atuinmodel.AuthModeSession, Token: "session-token", MasterKey: key.Bytes(),
	})
	if _, err := remote.ValidateMasterKey(context.Background()); !errors.Is(err, historysync.ErrRemote) {
		t.Fatalf("ValidateMasterKey = %v, want ErrRemote", err)
	}
}

func TestValidateMasterKeyMapsStatusFailure(t *testing.T) {
	server, _ := newTestServer(t, historysync.PinnedAtuinVersion, map[string]http.HandlerFunc{
		"/api/v0/record": func(writer http.ResponseWriter, request *http.Request) {
			http.Error(writer, "server-secret-body", http.StatusInternalServerError)
		},
	})
	remote := openTestRemote(t, server.URL, historysync.Config{
		AuthMode: atuinmodel.AuthModeSession, Token: "session-token", MasterKey: testKeyBytes(t),
	})
	_, err := remote.ValidateMasterKey(context.Background())
	if !errors.Is(err, historysync.ErrRemote) {
		t.Fatalf("ValidateMasterKey = %v, want ErrRemote", err)
	}
}

func TestValidateMasterKeyRequiresAKeyEvenBeforeUpload(t *testing.T) {
	server, recorder := newTestServer(t, historysync.PinnedAtuinVersion, map[string]http.HandlerFunc{
		"/api/v0/record": func(writer http.ResponseWriter, request *http.Request) {
			writeJSON(t, writer, http.StatusOK, map[string]any{
				"hosts": map[string]any{testWriterID: map[string]any{"history": 0}},
			})
		},
	})
	remote := openTestRemote(t, server.URL, historysync.Config{
		AuthMode: atuinmodel.AuthModeSession, Token: "session-token",
	})
	_, err := remote.ValidateMasterKey(context.Background())
	if !errors.Is(err, historysync.ErrKeyMismatch) {
		t.Fatalf("ValidateMasterKey = %v, want ErrKeyMismatch", err)
	}
	requests := recorder.all()
	if len(requests) != 1 || requests[0].Path != "/api/v0/record" {
		t.Fatalf("requests = %+v, want only the status read", requests)
	}
}

func TestValidateMasterKeyIgnoresOtherTagRecordsInThePage(t *testing.T) {
	key := testKey(t)
	unknown := mustWireRecord(t, recordSpec{ID: testRecordID1, Index: 0, Tag: "kv"}, key)
	server, _ := newTestServer(t, historysync.PinnedAtuinVersion, map[string]http.HandlerFunc{
		"/api/v0/record": func(writer http.ResponseWriter, request *http.Request) {
			writeJSON(t, writer, http.StatusOK, map[string]any{
				"hosts": map[string]any{testWriterID: map[string]any{"history": 0}},
			})
		},
		"/api/v0/record/next": func(writer http.ResponseWriter, request *http.Request) {
			writeJSON(t, writer, http.StatusOK, []wire.Record{unknown})
		},
	})
	remote := openTestRemote(t, server.URL, historysync.Config{
		AuthMode: atuinmodel.AuthModeSession, Token: "session-token", MasterKey: key.Bytes(),
	})
	validation, err := remote.ValidateMasterKey(context.Background())
	if err != nil {
		t.Fatalf("ValidateMasterKey: %v", err)
	}
	if validation != historysync.KeyValidationUnverified {
		t.Fatalf("validation = %q, want unverified", validation)
	}
}
