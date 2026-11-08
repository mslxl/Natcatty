package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/mslxl/natcatty/adapters/atuin/wire"
	historysync "github.com/mslxl/natcatty/application/historysync"
	atuinmodel "github.com/mslxl/natcatty/domain/model/atuin"
)

// Test UUIDs are shaped like UUIDv7 values but carry no meaning.
const (
	testWriterID    = "0190aaaa-bbbb-7ccc-8ddd-eeeeeeeeeee0"
	testOtherWriter = "0190aaaa-bbbb-7ccc-8ddd-eeeeeeeeeee1"
	testRecordID0   = "0190aaaa-bbbb-7ccc-8ddd-eeeeeeeeeee2"
	testRecordID1   = "0190aaaa-bbbb-7ccc-8ddd-eeeeeeeeeee3"
	testRecordID2   = "0190aaaa-bbbb-7ccc-8ddd-eeeeeeeeeee4"
	testHistoryID0  = "0190aaaa-bbbb-7ccc-8ddd-eeeeeeeeeee5"
	testHistoryID1  = "0190aaaa-bbbb-7ccc-8ddd-eeeeeeeeeee6"
)

func testKey(t *testing.T) wire.Key {
	t.Helper()
	var key wire.Key
	for index := range key {
		key[index] = byte(index + 1)
	}
	return key
}

func testKeyBytes(t *testing.T) []byte {
	t.Helper()
	return testKey(t).Bytes()
}

type requestSnapshot struct {
	Method string
	Path   string
	Query  string
	Header http.Header
	Body   []byte
}

// recorder captures every request a test server receives.
type recorder struct {
	mu       sync.Mutex
	requests []requestSnapshot
}

func (recorder *recorder) add(request *http.Request) {
	body, _ := io.ReadAll(request.Body)
	// Restore the body so the route handler can read it too.
	request.Body = io.NopCloser(bytes.NewReader(body))
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.requests = append(recorder.requests, requestSnapshot{
		Method: request.Method,
		Path:   request.URL.Path,
		Query:  request.URL.RawQuery,
		Header: request.Header.Clone(),
		Body:   append([]byte(nil), body...),
	})
}

func (recorder *recorder) all() []requestSnapshot {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]requestSnapshot(nil), recorder.requests...)
}

// newTestServer serves routes keyed by URL path, stamps the Atuin-Version
// header on every response, and records the requests. An empty version omits
// the header on purpose.
func newTestServer(t *testing.T, version string, routes map[string]http.HandlerFunc) (*httptest.Server, *recorder) {
	t.Helper()
	recorder := &recorder{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		recorder.add(request)
		handler, ok := routes[request.URL.Path]
		if !ok {
			http.NotFound(writer, request)
			return
		}
		if version != "" {
			writer.Header().Set(headerAtuinVersion, version)
		}
		handler(writer, request)
	}))
	t.Cleanup(server.Close)
	return server, recorder
}

func writeJSON(t *testing.T, writer http.ResponseWriter, status int, value any) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		t.Errorf("write JSON response: %v", err)
	}
}

func openTestRemote(t *testing.T, serverURL string, config historysync.Config) *Remote {
	t.Helper()
	config.BaseURL = serverURL
	remote, err := NewFactory(Options{}).Open(context.Background(), config)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = remote.Close() })
	typed, ok := remote.(*Remote)
	if !ok {
		t.Fatalf("Open returned %T, want *Remote", remote)
	}
	return typed
}

func TestAuthenticatePastedTokenSendsTokenHeaderAndReadsAccount(t *testing.T) {
	server, recorder := newTestServer(t, historysync.PinnedAtuinVersion, map[string]http.HandlerFunc{
		"/api/v0/me": func(writer http.ResponseWriter, request *http.Request) {
			writeJSON(t, writer, http.StatusOK, map[string]string{"username": "alice"})
		},
	})
	remote := openTestRemote(t, server.URL, historysync.Config{
		AuthMode: atuinmodel.AuthModeSession, Token: "session-token",
	})

	result, err := remote.Authenticate(context.Background())
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if result.Token != "" {
		t.Fatalf("validated token result = %q, want empty", result.Token)
	}
	if result.Account.Username != "alice" || result.Account.ServerURL != server.URL {
		t.Fatalf("account = %+v, want alice at %s", result.Account, server.URL)
	}

	requests := recorder.all()
	if len(requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(requests))
	}
	request := requests[0]
	if request.Method != http.MethodGet || request.Path != "/api/v0/me" {
		t.Fatalf("request = %s %s, want GET /api/v0/me", request.Method, request.Path)
	}
	if got := request.Header.Get("Authorization"); got != "Token session-token" {
		t.Fatalf("Authorization = %q, want Token session-token", got)
	}
	if got := request.Header.Get(headerAtuinVersion); got != historysync.PinnedAtuinVersion {
		t.Fatalf("Atuin-Version = %q, want %q", got, historysync.PinnedAtuinVersion)
	}
	if got := request.Header.Get("Accept"); got != "application/json" {
		t.Fatalf("Accept = %q, want application/json", got)
	}
	if got := request.Header.Get("User-Agent"); got != DefaultUserAgent {
		t.Fatalf("User-Agent = %q, want %q", got, DefaultUserAgent)
	}
}

func TestAuthenticateBearerTokenUsesBearerHeader(t *testing.T) {
	server, recorder := newTestServer(t, historysync.PinnedAtuinVersion, map[string]http.HandlerFunc{
		"/api/v0/me": func(writer http.ResponseWriter, request *http.Request) {
			writeJSON(t, writer, http.StatusOK, map[string]string{"username": "bob"})
		},
	})
	remote := openTestRemote(t, server.URL, historysync.Config{
		AuthMode: atuinmodel.AuthModeBearer, Token: "hub-token",
	})
	if _, err := remote.Authenticate(context.Background()); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	requests := recorder.all()
	if got := requests[0].Header.Get("Authorization"); got != "Bearer hub-token" {
		t.Fatalf("Authorization = %q, want Bearer hub-token", got)
	}
}

func TestAuthenticatePasswordLoginMintsToken(t *testing.T) {
	server, recorder := newTestServer(t, historysync.PinnedAtuinVersion, map[string]http.HandlerFunc{
		"/login": func(writer http.ResponseWriter, request *http.Request) {
			var body struct {
				Username string `json:"username"`
				Password string `json:"password"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Errorf("decode login body: %v", err)
			}
			if body.Username != "alice" || body.Password != "hunter2" {
				t.Errorf("login body = %+v", body)
			}
			if got := request.Header.Get("Authorization"); got != "" {
				t.Errorf("login Authorization = %q, want empty", got)
			}
			writeJSON(t, writer, http.StatusOK, map[string]string{
				"session": "minted-session", "auth": "cli", "user_id": "user-1",
			})
		},
		"/api/v0/capabilities": func(writer http.ResponseWriter, request *http.Request) {
			writeJSON(t, writer, http.StatusOK, validCapabilityDocument())
		},
	})
	remote := openTestRemote(t, server.URL, historysync.Config{
		AuthMode: atuinmodel.AuthModeSession, Username: "alice", Password: "hunter2",
	})

	result, err := remote.Authenticate(context.Background())
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if result.Token != "minted-session" {
		t.Fatalf("minted token = %q, want minted-session", result.Token)
	}
	if result.Account.UserID != "user-1" || result.Account.Username != "alice" {
		t.Fatalf("account = %+v", result.Account)
	}

	// The minted token must authenticate the following capabilities request.
	if _, err := remote.Capabilities(context.Background()); err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	requests := recorder.all()
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(requests))
	}
	if got := requests[1].Header.Get("Authorization"); got != "Token minted-session" {
		t.Fatalf("capabilities Authorization = %q, want Token minted-session", got)
	}
}

func validCapabilityDocument() map[string]any {
	return map[string]any{
		"version": "4f2a1c00d9b37e11",
		"capabilities": map[string]any{
			historysync.CapabilityServer:   map[string]any{"version": 1},
			historysync.CapabilityPageSize: map[string]any{"version": 1, "page_size": 100},
		},
	}
}

func TestAuthenticateRejectsBadCredentialsWithoutLeakingThem(t *testing.T) {
	tests := []struct {
		name   string
		status int
	}{
		{"unauthorized", http.StatusUnauthorized},
		{"forbidden", http.StatusForbidden},
		{"missing user", http.StatusNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, _ := newTestServer(t, historysync.PinnedAtuinVersion, map[string]http.HandlerFunc{
				"/login": func(writer http.ResponseWriter, request *http.Request) {
					writeJSON(t, writer, test.status, map[string]string{"reason": "server-secret-body"})
				},
			})
			remote := openTestRemote(t, server.URL, historysync.Config{
				AuthMode: atuinmodel.AuthModeSession, Username: "alice", Password: "hunter2",
			})
			_, err := remote.Authenticate(context.Background())
			if !errors.Is(err, historysync.ErrAuthFailed) {
				t.Fatalf("Authenticate = %v, want ErrAuthFailed", err)
			}
			if strings.Contains(err.Error(), "hunter2") || strings.Contains(err.Error(), "secret-token") {
				t.Fatalf("error leaked a credential: %v", err)
			}
		})
	}
}

func TestAuthenticateValidatesPastedTokenFailure(t *testing.T) {
	server, _ := newTestServer(t, historysync.PinnedAtuinVersion, map[string]http.HandlerFunc{
		"/api/v0/me": func(writer http.ResponseWriter, request *http.Request) {
			writeJSON(t, writer, http.StatusForbidden, map[string]string{"reason": "session not found"})
		},
	})
	remote := openTestRemote(t, server.URL, historysync.Config{
		AuthMode: atuinmodel.AuthModeSession, Token: "stale-token",
	})
	_, err := remote.Authenticate(context.Background())
	if !errors.Is(err, historysync.ErrAuthFailed) {
		t.Fatalf("Authenticate = %v, want ErrAuthFailed", err)
	}
	if strings.Contains(err.Error(), "stale-token") {
		t.Fatalf("error leaked the token: %v", err)
	}
}
