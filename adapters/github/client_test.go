package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	appsync "github.com/mslxl/natcatty/application/sync"
	domainsync "github.com/mslxl/natcatty/domain/sync"
)

func testClient(server *httptest.Server) *Client {
	return New(Options{APIBase: server.URL, AuthBase: server.URL, ClientID: "test-client"})
}

func TestRequestDeviceCodeSendsTheOriginalForm(t *testing.T) {
	var gotForm url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/login/device/code" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Accept") != "application/json" {
			t.Errorf("accept = %q", r.Header.Get("Accept"))
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		gotForm = r.PostForm
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_code": "device", "user_code": "ABCD-1234",
			"verification_uri": "https://github.com/login/device",
			"expires_in":       900, "interval": 5,
		})
	}))
	defer server.Close()

	code, err := testClient(server).RequestDeviceCode(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotForm.Get("client_id") != "test-client" || gotForm.Get("scope") != "gist read:user" {
		t.Fatalf("form = %v", gotForm)
	}
	if code.UserCode != "ABCD-1234" || code.Interval != 5 || code.ExpiresIn != 900 {
		t.Fatalf("code = %+v", code)
	}
}

func TestPollDeviceTokenMapsEveryGrantState(t *testing.T) {
	tests := []struct {
		name     string
		response map[string]any
		want     error
		token    bool
	}{
		{"success", map[string]any{"access_token": "token", "token_type": "bearer", "scope": "gist"}, nil, true},
		{"pending", map[string]any{"error": "authorization_pending"}, appsync.ErrAuthorizationPending, false},
		{"slow down", map[string]any{"error": "slow_down"}, appsync.ErrSlowDown, false},
		{"denied", map[string]any{"error": "access_denied"}, appsync.ErrAuthorizationDenied, false},
		{"expired", map[string]any{"error": "expired_token"}, appsync.ErrDeviceCodeExpired, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/login/oauth/access_token" {
					t.Errorf("path = %s", r.URL.Path)
				}
				_ = r.ParseForm()
				if r.PostForm.Get("grant_type") != "urn:ietf:params:oauth:grant-type:device_code" {
					t.Errorf("grant = %q", r.PostForm.Get("grant_type"))
				}
				if r.PostForm.Get("device_code") != "device" {
					t.Errorf("device = %q", r.PostForm.Get("device_code"))
				}
				_ = json.NewEncoder(w).Encode(test.response)
			}))
			defer server.Close()

			token, err := testClient(server).PollDeviceToken(context.Background(), appsync.DeviceCode{DeviceCode: "device"})
			if test.want != nil {
				if !errors.Is(err, test.want) {
					t.Fatalf("error = %v, want %v", err, test.want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if token.AccessToken != "token" || token.Scope != "gist" || token.TokenType != "bearer" {
				t.Fatalf("token = %+v", token)
			}
		})
	}
}

func TestUserReturnsTheStableAccountIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user" {
			t.Errorf("path = %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 583231, "login": "octocat"})
	}))
	defer server.Close()

	grant, err := testClient(server).User(context.Background(), domainsync.Token{AccessToken: "token"})
	if err != nil {
		t.Fatal(err)
	}
	if grant.ID != "583231" || grant.Login != "octocat" {
		t.Fatalf("grant = %+v, want the numeric id and login", grant)
	}
	if grant.Identity() != "583231" {
		t.Fatalf("identity = %q, want the stable id", grant.Identity())
	}
}

func TestUserMapsUnauthorizedAndRateLimits(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user":
			if r.Header.Get("Authorization") != "Bearer revoked" {
				t.Errorf("authorization = %q", r.Header.Get("Authorization"))
			}
			w.WriteHeader(http.StatusUnauthorized)
		case "/gists":
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.WriteHeader(http.StatusForbidden)
		}
	}))
	defer server.Close()
	client := testClient(server)

	if _, err := client.User(context.Background(), domainsync.Token{AccessToken: "revoked"}); !errors.Is(err, appsync.ErrUnauthorized) {
		t.Fatalf("revoked token error = %v", err)
	}
	if _, err := client.findGist(context.Background(), domainsync.Token{AccessToken: "x"}); !errors.Is(err, appsync.ErrRateLimited) {
		t.Fatalf("rate limit error = %v", err)
	}
}

func TestReadFindsTheGistAndCachesTheETag(t *testing.T) {
	envelope := testEnvelope(t)
	var mu sync.Mutex
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
		switch r.URL.Path {
		case "/gists":
			if r.URL.Query().Get("per_page") != "100" {
				t.Errorf("per_page = %q", r.URL.Query().Get("per_page"))
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id": "ignored", "description": "other",
			}, {
				"id": "gist-1", "description": domainsync.GistDescription,
				"files": map[string]any{domainsync.SyncFileName: map[string]any{"filename": domainsync.SyncFileName}},
			}})
		case "/gists/gist-1":
			if r.Header.Get("If-None-Match") == "etag-1" {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", "etag-1")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "gist-1", "updated_at": "2025-01-01T00:00:00Z",
				"files": map[string]any{domainsync.SyncFileName: map[string]any{
					"filename": domainsync.SyncFileName,
					"content":  string(envelope),
					"size":     len(envelope),
				}},
			})
		}
	}))
	defer server.Close()
	client := testClient(server)

	file, exists, err := client.Read(context.Background(), domainsync.Token{AccessToken: "token"})
	if err != nil || !exists {
		t.Fatalf("Read = %v, %v", exists, err)
	}
	if file.ID != "gist-1" || file.ETag != "etag-1" || string(file.Content) != string(envelope) {
		t.Fatalf("file = %+v", file)
	}
	if file.UpdatedAt == 0 {
		t.Fatal("updatedAt was not read from the envelope meta")
	}
	// The second read sends If-None-Match and serves the cached bytes on 304.
	again, exists, err := client.Read(context.Background(), domainsync.Token{AccessToken: "token"})
	if err != nil || !exists {
		t.Fatalf("second Read = %v, %v", exists, err)
	}
	if string(again.Content) != string(envelope) || again.ETag != "etag-1" {
		t.Fatalf("cached read = %+v", again)
	}
	mu.Lock()
	defer mu.Unlock()
	if requests != 3 {
		t.Fatalf("requests = %d, want list + gist + conditional 304", requests)
	}
}

func TestReadReturnsNoFileWhenTheAccountHasNoSyncGist(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	defer server.Close()
	_, exists, err := testClient(server).Read(context.Background(), domainsync.Token{AccessToken: "token"})
	if err != nil || exists {
		t.Fatalf("Read = %v, %v", exists, err)
	}
}

func TestReadFetchesTruncatedContentFromTheRawURL(t *testing.T) {
	envelope := testEnvelope(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/gists":
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id": "gist-1", "description": domainsync.GistDescription,
				"files": map[string]any{domainsync.SyncFileName: map[string]any{"filename": domainsync.SyncFileName}},
			}})
		case "/gists/gist-1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "gist-1",
				"files": map[string]any{domainsync.SyncFileName: map[string]any{
					"filename":  domainsync.SyncFileName,
					"content":   string(envelope[:20]),
					"truncated": true,
					"size":      len(envelope),
					"raw_url":   serverURL(r) + "/raw/gist-1",
				}},
			})
		case "/raw/gist-1":
			if r.Header.Get("Accept") != "application/vnd.github.raw" {
				t.Errorf("raw accept = %q", r.Header.Get("Accept"))
			}
			_, _ = w.Write(envelope)
		}
	}))
	defer server.Close()
	file, exists, err := testClient(server).Read(context.Background(), domainsync.Token{AccessToken: "token"})
	if err != nil || !exists {
		t.Fatalf("Read = %v, %v", exists, err)
	}
	if string(file.Content) != string(envelope) {
		t.Fatal("truncated content was not replaced with the raw body")
	}
}

func TestWriteCreatesAndConditionallyUpdates(t *testing.T) {
	envelope := testEnvelope(t)
	var mu sync.Mutex
	var patched bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/gists":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["description"] != domainsync.GistDescription || body["public"] != false {
				t.Errorf("body = %v", body)
			}
			if !strings.Contains(string(envelope), "payload") {
				t.Error("unexpected fixture")
			}
			w.Header().Set("ETag", "etag-1")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "gist-1"})
		case r.Method == http.MethodPatch && r.URL.Path == "/gists/gist-1":
			mu.Lock()
			patched = true
			mu.Unlock()
			if r.Header.Get("If-Match") != "etag-1" {
				t.Errorf("If-Match = %q", r.Header.Get("If-Match"))
			}
			w.Header().Set("ETag", "etag-2")
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "gist-1"})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	client := testClient(server)

	created, err := client.Write(context.Background(), domainsync.Token{AccessToken: "token"}, appsync.RemoteFile{}, envelope)
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != "gist-1" || created.ETag != "etag-1" || created.UpdatedAt == 0 {
		t.Fatalf("created = %+v", created)
	}
	updated, err := client.Write(context.Background(), domainsync.Token{AccessToken: "token"}, created, envelope)
	if err != nil {
		t.Fatal(err)
	}
	if updated.ETag != "etag-2" || !patched {
		t.Fatalf("updated = %+v patched=%v", updated, patched)
	}
}

func TestWriteMapsPreconditionFailureToConflict(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPreconditionFailed)
	}))
	defer server.Close()
	_, err := testClient(server).Write(context.Background(), domainsync.Token{AccessToken: "token"},
		appsync.RemoteFile{ID: "gist-1", ETag: "stale"}, []byte("{}"))
	if !errors.Is(err, appsync.ErrConflict) {
		t.Fatalf("error = %v, want ErrConflict", err)
	}
}

func TestNetworkFailureIsTyped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	client := testClient(server)
	server.Close()
	if _, err := client.User(context.Background(), domainsync.Token{AccessToken: "token"}); !errors.Is(err, appsync.ErrNetwork) {
		t.Fatalf("error = %v, want ErrNetwork", err)
	}
}

func TestRefreshExchangesARefreshToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.PostForm.Get("grant_type") != "refresh_token" || r.PostForm.Get("refresh_token") != "refresh" {
			t.Errorf("form = %v", r.PostForm)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "fresh", "refresh_token": "next", "token_type": "bearer", "scope": "gist",
		})
	}))
	defer server.Close()
	token, err := testClient(server).Refresh(context.Background(), domainsync.Token{RefreshToken: "refresh"})
	if err != nil {
		t.Fatal(err)
	}
	if token.AccessToken != "fresh" || token.RefreshToken != "next" {
		t.Fatalf("token = %+v", token)
	}
}

func TestMissingClientIDIsReported(t *testing.T) {
	client := New(Options{})
	if _, err := client.RequestDeviceCode(context.Background()); !errors.Is(err, appsync.ErrNotConfigured) {
		t.Fatalf("error = %v", err)
	}
}

func testEnvelope(t *testing.T) []byte {
	t.Helper()
	envelope, err := domainsync.EncryptPayload(map[string]any{"hosts": []string{"h"}}, "secret", domainsync.EncryptOptions{Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	data, err := domainsync.MarshalFile(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func serverURL(r *http.Request) string { return "http://" + r.Host }
