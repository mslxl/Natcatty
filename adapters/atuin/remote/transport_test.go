package remote

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mslxl/natcatty/adapters/atuin/wire"
	historysync "github.com/mslxl/natcatty/application/historysync"
	domainhistory "github.com/mslxl/natcatty/domain/history"
	atuinmodel "github.com/mslxl/natcatty/domain/model/atuin"
)

func TestCanceledContextIsReportedOffline(t *testing.T) {
	key := testKeyBytes(t)
	record := mustWireRecord(t, recordSpec{ID: testRecordID0, Index: 0}, testKey(t))
	server, recorder := newTestServer(t, historysync.PinnedAtuinVersion, map[string]http.HandlerFunc{
		"/api/v0/me": func(writer http.ResponseWriter, request *http.Request) {
			writeJSON(t, writer, http.StatusOK, map[string]string{"username": "alice"})
		},
		"/api/v0/record/next": func(writer http.ResponseWriter, request *http.Request) {
			writeJSON(t, writer, http.StatusOK, []wire.Record{record})
		},
	})
	tests := []struct {
		name string
		call func(context.Context, historysync.Remote) error
	}{
		{"authenticate", func(ctx context.Context, remote historysync.Remote) error {
			_, err := remote.Authenticate(ctx)
			return err
		}},
		{"capabilities", func(ctx context.Context, remote historysync.Remote) error {
			_, err := remote.Capabilities(ctx)
			return err
		}},
		{"record status", func(ctx context.Context, remote historysync.Remote) error {
			_, err := remote.RecordStatus(ctx)
			return err
		}},
		{"download", func(ctx context.Context, remote historysync.Remote) error {
			_, err := remote.Download(ctx, historysync.Series{
				SeriesHost: testWriterID, Tag: domainhistory.RecordTagHistory,
			}, 0, 1)
			return err
		}},
		{"upload", func(ctx context.Context, remote historysync.Remote) error {
			return remote.Upload(ctx, []historysync.SyncRecord{testCreateRecord(testUploadEntry())})
		}},
		{"validate key", func(ctx context.Context, remote historysync.Remote) error {
			_, err := remote.ValidateMasterKey(ctx)
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			remote := openTestRemote(t, server.URL, historysync.Config{
				AuthMode: atuinmodel.AuthModeSession, Token: "session-token", MasterKey: key,
			})
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			err := test.call(ctx, remote)
			if !errors.Is(err, historysync.ErrOffline) {
				t.Fatalf("call = %v, want ErrOffline", err)
			}
			if strings.Contains(err.Error(), "session-token") {
				t.Fatalf("offline error leaked the token: %v", err)
			}
		})
	}
	if len(recorder.all()) != 0 {
		t.Fatalf("canceled requests reached the server: %+v", recorder.all())
	}
}

func TestRequestTimeoutIsReportedOffline(t *testing.T) {
	server, _ := newTestServer(t, historysync.PinnedAtuinVersion, map[string]http.HandlerFunc{
		"/api/v0/capabilities": func(writer http.ResponseWriter, request *http.Request) {
			select {
			case <-request.Context().Done():
			case <-time.After(5 * time.Second):
			}
		},
	})
	factory := NewFactory(Options{Timeout: 100 * time.Millisecond})
	opened, err := factory.Open(context.Background(), historysync.Config{
		BaseURL: server.URL, AuthMode: atuinmodel.AuthModeSession, Token: "session-token",
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = opened.Close() })

	started := time.Now()
	_, err = opened.Capabilities(context.Background())
	if !errors.Is(err, historysync.ErrOffline) {
		t.Fatalf("Capabilities = %v, want ErrOffline", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("a timed out request took %v", elapsed)
	}
}

func TestUnreachableServerIsReportedOfflineWithoutTheAddress(t *testing.T) {
	server, _ := newTestServer(t, historysync.PinnedAtuinVersion, nil)
	address := server.URL
	server.Close()
	remote := openTestRemote(t, address, historysync.Config{
		AuthMode: atuinmodel.AuthModeSession, Token: "session-token",
	})
	_, err := remote.Capabilities(context.Background())
	if !errors.Is(err, historysync.ErrOffline) {
		t.Fatalf("Capabilities = %v, want ErrOffline", err)
	}
	if strings.Contains(err.Error(), address) || strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("offline error leaked the address: %v", err)
	}
}

func TestSuccessWithoutAVersionHeaderFailsClosed(t *testing.T) {
	server, _ := newTestServer(t, "", map[string]http.HandlerFunc{
		"/api/v0/capabilities": func(writer http.ResponseWriter, request *http.Request) {
			writeJSON(t, writer, http.StatusOK, validCapabilityDocument())
		},
	})
	remote := openTestRemote(t, server.URL, historysync.Config{
		AuthMode: atuinmodel.AuthModeSession, Token: "session-token",
	})
	_, err := remote.Capabilities(context.Background())
	if !errors.Is(err, historysync.ErrIncompatibleServer) {
		t.Fatalf("Capabilities = %v, want ErrIncompatibleServer", err)
	}
}

func TestRedirectIsNotFollowed(t *testing.T) {
	target, targetRecorder := newTestServer(t, historysync.PinnedAtuinVersion, map[string]http.HandlerFunc{
		"/token-capture": func(writer http.ResponseWriter, request *http.Request) {
			writer.WriteHeader(http.StatusOK)
		},
	})
	server, _ := newTestServer(t, historysync.PinnedAtuinVersion, map[string]http.HandlerFunc{
		"/api/v0/capabilities": func(writer http.ResponseWriter, request *http.Request) {
			writer.Header().Set("Location", target.URL+"/token-capture")
			writer.WriteHeader(http.StatusTemporaryRedirect)
		},
	})
	remote := openTestRemote(t, server.URL, historysync.Config{
		AuthMode: atuinmodel.AuthModeSession, Token: "session-token",
	})
	_, err := remote.Capabilities(context.Background())
	if !errors.Is(err, historysync.ErrRemote) {
		t.Fatalf("Capabilities = %v, want ErrRemote", err)
	}
	if len(targetRecorder.all()) != 0 {
		t.Fatal("a redirect forwarded the request to another origin")
	}
}

func TestStatusErrorsMapWithoutLeaking(t *testing.T) {
	tests := []struct {
		name   string
		status int
		want   error
		// capabilitiesWant overrides want for the capabilities endpoint when a
		// status means something endpoint-specific there.
		capabilitiesWant error
	}{
		{"unauthorized", http.StatusUnauthorized, historysync.ErrAuthExpired, nil},
		{"forbidden", http.StatusForbidden, historysync.ErrAuthExpired, nil},
		// A server without the capabilities endpoint cannot be negotiated
		// with; that is not transient.
		{"not found", http.StatusNotFound, historysync.ErrRemote, historysync.ErrIncompatibleServer},
		{"rate limited", http.StatusTooManyRequests, historysync.ErrRemote, nil},
		{"server error", http.StatusInternalServerError, historysync.ErrRemote, nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := func(writer http.ResponseWriter, request *http.Request) {
				http.Error(writer, "server-secret-body", test.status)
			}
			server, recorder := newTestServer(t, historysync.PinnedAtuinVersion, map[string]http.HandlerFunc{
				"/api/v0/capabilities": handler,
				"/api/v0/record":       handler,
				"/api/v0/record/next":  handler,
			})
			remote := openTestRemote(t, server.URL, historysync.Config{
				AuthMode: atuinmodel.AuthModeSession, Token: "session-token", MasterKey: testKeyBytes(t),
			})
			operations := map[string]error{
				"capabilities": func() error {
					_, err := remote.Capabilities(context.Background())
					return err
				}(),
				"status": func() error {
					_, err := remote.RecordStatus(context.Background())
					return err
				}(),
				"download": func() error {
					_, err := remote.Download(context.Background(), historysync.Series{
						SeriesHost: testWriterID, Tag: domainhistory.RecordTagHistory,
					}, 0, 1)
					return err
				}(),
				"upload": remote.Upload(context.Background(), []historysync.SyncRecord{
					testCreateRecord(testUploadEntry()),
				}),
			}
			for name, err := range operations {
				want := test.want
				if name == "capabilities" && test.capabilitiesWant != nil {
					want = test.capabilitiesWant
				}
				if !errors.Is(err, want) {
					t.Errorf("%s = %v, want %v", name, err, want)
				}
				if err != nil && strings.Contains(err.Error(), "server-secret-body") {
					t.Errorf("%s leaked a response body: %v", name, err)
				}
			}
			if len(recorder.all()) != 4 {
				t.Fatalf("requests = %d, want one per operation", len(recorder.all()))
			}
		})
	}
}

func TestStoreDeleteEndpointIsNeverRequested(t *testing.T) {
	key := testKey(t)
	record := encodedTestRecord(t, testCreateRecord(testUploadEntry()), key)
	server, recorder := newTestServer(t, historysync.PinnedAtuinVersion, map[string]http.HandlerFunc{
		"/api/v0/me": func(writer http.ResponseWriter, request *http.Request) {
			writeJSON(t, writer, http.StatusOK, map[string]string{"username": "alice"})
		},
		"/api/v0/capabilities": func(writer http.ResponseWriter, request *http.Request) {
			writeJSON(t, writer, http.StatusOK, validCapabilityDocument())
		},
		"/api/v0/record": func(writer http.ResponseWriter, request *http.Request) {
			if request.Method == http.MethodPost {
				writer.WriteHeader(http.StatusOK)
				return
			}
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
	ctx := context.Background()
	operations := []func() error{
		func() error { _, err := remote.Authenticate(ctx); return err },
		func() error { _, err := remote.Capabilities(ctx); return err },
		func() error { _, err := remote.RecordStatus(ctx); return err },
		func() error {
			_, err := remote.Download(ctx, historysync.Series{
				SeriesHost: testWriterID, Tag: domainhistory.RecordTagHistory,
			}, 0, 1)
			return err
		},
		func() error {
			return remote.Upload(ctx, []historysync.SyncRecord{testCreateRecord(testUploadEntry())})
		},
		func() error { _, err := remote.ValidateMasterKey(ctx); return err },
	}
	for index, operation := range operations {
		if err := operation(); err != nil {
			t.Fatalf("operation %d: %v", index, err)
		}
	}
	requests := recorder.all()
	if len(requests) < len(operations) {
		t.Fatalf("requests = %d, want at least one per operation", len(requests))
	}
	allowed := map[string]bool{
		"/api/v0/me": true, "/api/v0/capabilities": true,
		"/api/v0/record": true, "/api/v0/record/next": true,
	}
	for _, request := range requests {
		if request.Method == http.MethodDelete {
			t.Fatalf("adapter sent DELETE %s", request.Path)
		}
		if request.Path == "/api/v0/store" || strings.Contains(request.Path, "store") {
			t.Fatalf("adapter requested the store endpoint: %s %s", request.Method, request.Path)
		}
		if !allowed[request.Path] {
			t.Fatalf("adapter requested an unexpected endpoint: %s %s", request.Method, request.Path)
		}
	}
}

func TestCloseWipesCredentialsAndKeyAndIsIdempotent(t *testing.T) {
	server, recorder := newTestServer(t, historysync.PinnedAtuinVersion, nil)
	remote := openTestRemote(t, server.URL, historysync.Config{
		AuthMode: atuinmodel.AuthModeSession, Token: "session-token", MasterKey: testKeyBytes(t),
	})
	if err := remote.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := remote.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	err := remote.Upload(context.Background(), []historysync.SyncRecord{testCreateRecord(testUploadEntry())})
	if !errors.Is(err, historysync.ErrKeyMismatch) {
		t.Fatalf("Upload after Close = %v, want ErrKeyMismatch", err)
	}
	if _, err := remote.Authenticate(context.Background()); !errors.Is(err, historysync.ErrAuthFailed) {
		t.Fatalf("Authenticate after Close = %v, want ErrAuthFailed", err)
	}
	if len(recorder.all()) != 0 {
		t.Fatalf("a closed remote made requests: %+v", recorder.all())
	}
}

func TestOpenedRemoteCopiesTheMasterKey(t *testing.T) {
	key := testKey(t)
	record := mustWireRecord(t, recordSpec{ID: testRecordID0, Index: 0}, key)
	server, _ := newTestServer(t, historysync.PinnedAtuinVersion, map[string]http.HandlerFunc{
		"/api/v0/record/next": func(writer http.ResponseWriter, request *http.Request) {
			writeJSON(t, writer, http.StatusOK, []wire.Record{record})
		},
	})
	config := historysync.Config{
		BaseURL: server.URL, AuthMode: atuinmodel.AuthModeSession,
		Token: "session-token", MasterKey: key.Bytes(),
	}
	remote, err := NewFactory(Options{}).Open(context.Background(), config)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = remote.Close() })
	config.Wipe()
	page, err := remote.Download(context.Background(), historysync.Series{
		SeriesHost: testWriterID, Tag: domainhistory.RecordTagHistory,
	}, 0, 1)
	if err != nil {
		t.Fatalf("Download after the caller wiped its config: %v", err)
	}
	if len(page.Records) != 1 {
		t.Fatalf("records = %d, want 1", len(page.Records))
	}
}

func TestOpenRejectsIncompleteOrConflictingCredentials(t *testing.T) {
	tests := []struct {
		name   string
		config historysync.Config
	}{
		{"no credentials", historysync.Config{}},
		{"token and password", historysync.Config{
			Token: "session-token", Username: "alice", Password: "hunter2",
		}},
		{"username without a password", historysync.Config{Username: "alice"}},
		{"password login with bearer mode", historysync.Config{
			AuthMode: atuinmodel.AuthModeBearer, Username: "alice", Password: "hunter2",
		}},
		{"token with a line break", historysync.Config{Token: "hmkjuvw" + "\n" + "xmxmx"}},
		{"master key is not 32 bytes", historysync.Config{
			Token: "session-token", MasterKey: []byte("short"),
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.config.BaseURL = "https://example.com"
			if _, err := NewFactory(Options{}).Open(context.Background(), test.config); err == nil {
				t.Fatal("Open succeeded, want an error")
			}
		})
	}
}
