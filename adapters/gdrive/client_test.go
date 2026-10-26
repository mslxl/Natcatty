package gdrive_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/mslxl/natcatty/adapters/gdrive"
	"github.com/mslxl/natcatty/adapters/gdrive/fake"
	appsync "github.com/mslxl/natcatty/application/sync"
	domainsync "github.com/mslxl/natcatty/domain/sync"
)

func startFake(t *testing.T) *fake.Server {
	t.Helper()
	server, err := fake.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	return server
}

func clientFor(server *fake.Server) *gdrive.Client {
	return gdrive.New(gdrive.Options{
		AuthBase:    server.URL(),
		TokenBase:   server.URL(),
		APIBase:     server.URL() + "/drive/v3",
		ClientID:    server.ClientID(),
		AuthTimeout: 5 * time.Second,
		HTTPClient:  &http.Client{Timeout: 5 * time.Second},
	})
}

// openInBrowser drives the fake the way a system browser would: it requests
// the authorize URL, follows the loopback redirect, and ignores the page's
// connection teardown. Authorization success is judged by the returned token.
func openInBrowser(authURL string) error {
	response, err := (&http.Client{Timeout: 5 * time.Second}).Get(authURL)
	if err == nil {
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
	}
	return nil
}

func authorize(t *testing.T, server *fake.Server, client *gdrive.Client) (domainsync.Token, appsync.Grant) {
	t.Helper()
	token, grant, err := client.Authorize(context.Background(), openInBrowser)
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	return token, grant
}

func TestNewPKCEProducesAnS256Proof(t *testing.T) {
	first, err := gdrive.NewPKCE(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Verifier) != 43 {
		t.Fatalf("verifier length = %d, want 43", len(first.Verifier))
	}
	if strings.ContainsAny(first.Verifier, "=+/") {
		t.Fatalf("verifier is not base64url without padding: %q", first.Verifier)
	}
	sum := sha256.Sum256([]byte(first.Verifier))
	want := base64.RawURLEncoding.EncodeToString(sum[:])
	if first.Challenge != want {
		t.Fatalf("challenge = %q, want %q", first.Challenge, want)
	}
	if first.State == "" {
		t.Fatal("state is empty")
	}
	second, err := gdrive.NewPKCE(nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.Verifier == first.Verifier || second.State == first.State {
		t.Fatal("two attempts reused a verifier or state")
	}
}

func TestAuthorizeUsesTheSystemBrowserLoopbackAndPKCE(t *testing.T) {
	server := startFake(t)
	client := clientFor(server)

	var authURL *url.URL
	opener := func(raw string) error {
		parsed, err := url.Parse(raw)
		if err != nil {
			return err
		}
		authURL = parsed
		return openInBrowser(raw)
	}
	token, grant, err := client.Authorize(context.Background(), opener)
	if err != nil {
		t.Fatal(err)
	}
	if authURL == nil {
		t.Fatal("the adapter did not call the system-browser opener")
	}
	query := authURL.Query()
	if authURL.Path != "/o/oauth2/v2/auth" {
		t.Fatalf("authorize path = %q", authURL.Path)
	}
	for key, want := range map[string]string{
		"client_id":             server.ClientID(),
		"response_type":         "code",
		"code_challenge_method": "S256",
		"access_type":           "offline",
		"prompt":                "consent",
	} {
		if got := query.Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if query.Get("code_challenge") == "" || query.Get("state") == "" {
		t.Fatalf("authorize URL lacks PKCE state: %s", authURL)
	}
	if query.Get("client_secret") != "" {
		t.Fatal("the authorize URL contains a client secret")
	}
	redirect, err := url.Parse(query.Get("redirect_uri"))
	if err != nil {
		t.Fatal(err)
	}
	if redirect.Scheme != "http" || redirect.Hostname() != "127.0.0.1" ||
		redirect.Port() == "" || redirect.Path != "/oauth/callback" {
		t.Fatalf("redirect_uri = %q, want an ephemeral 127.0.0.1 loopback", redirect)
	}

	if token.AccessToken == "" || token.RefreshToken == "" || token.ExpiresAt == 0 {
		t.Fatalf("token = %+v", token)
	}
	if grant.ID != "google-account-1" || grant.Login != "one@example.test" {
		t.Fatalf("grant = %+v", grant)
	}
	state := server.State()
	if state.TokenExchanges != 1 || state.Authorizations != 1 {
		t.Fatalf("fake state = %+v", state)
	}
	if state.SecretViolations != 0 {
		t.Fatalf("the adapter sent a client secret %d times", state.SecretViolations)
	}

	user, err := client.User(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if user.ID != "google-account-1" || user.Login != "one@example.test" {
		t.Fatalf("User = %+v", user)
	}
}

func TestAuthorizeRejectsAMismatchedState(t *testing.T) {
	server := startFake(t)
	server.Configure(func(control *fake.Control) { control.TamperState = true })
	client := clientFor(server)

	_, _, err := client.Authorize(context.Background(), openInBrowser)
	if !errors.Is(err, appsync.ErrAuthorizationState) {
		t.Fatalf("error = %v, want ErrAuthorizationState", err)
	}
	if state := server.State(); state.TokenExchanges != 0 || state.StateMismatches != 1 {
		t.Fatalf("fake state = %+v", state)
	}
}

func TestAuthorizeReportsADeniedConsent(t *testing.T) {
	server := startFake(t)
	server.Configure(func(control *fake.Control) { control.Deny = true })
	client := clientFor(server)

	_, _, err := client.Authorize(context.Background(), openInBrowser)
	if !errors.Is(err, appsync.ErrAuthorizationDenied) {
		t.Fatalf("error = %v, want ErrAuthorizationDenied", err)
	}
	if state := server.State(); state.TokenExchanges != 0 {
		t.Fatalf("a denied consent exchanged a code: %+v", state)
	}
}

func TestAuthorizeTimesOutWithoutARedirect(t *testing.T) {
	server := startFake(t)
	client := gdrive.New(gdrive.Options{
		AuthBase:    server.URL(),
		TokenBase:   server.URL(),
		APIBase:     server.URL() + "/drive/v3",
		ClientID:    server.ClientID(),
		AuthTimeout: 150 * time.Millisecond,
	})
	_, _, err := client.Authorize(context.Background(), func(string) error { return nil })
	if !errors.Is(err, appsync.ErrAuthorizationTimeout) {
		t.Fatalf("error = %v, want ErrAuthorizationTimeout", err)
	}
}

func TestAuthorizeReportsAnUnavailableBrowser(t *testing.T) {
	server := startFake(t)
	client := clientFor(server)
	_, _, err := client.Authorize(context.Background(), func(string) error {
		return errors.New("no browser registered")
	})
	if !errors.Is(err, appsync.ErrBrowserUnavailable) {
		t.Fatalf("error = %v, want ErrBrowserUnavailable", err)
	}
}

func TestAuthorizeRequiresAClientID(t *testing.T) {
	_, _, err := gdrive.New(gdrive.Options{}).Authorize(context.Background(), openInBrowser)
	if !errors.Is(err, appsync.ErrNotConfigured) {
		t.Fatalf("error = %v, want ErrNotConfigured", err)
	}
}

func TestRefreshKeepsTheOriginalRefreshTokenWhenGoogleOmitsIt(t *testing.T) {
	server := startFake(t)
	client := clientFor(server)
	token, _ := authorize(t, server, client)

	refreshed, err := client.Refresh(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.AccessToken == token.AccessToken || refreshed.AccessToken == "" {
		t.Fatalf("access token did not rotate: %+v", refreshed)
	}
	if refreshed.RefreshToken != "" {
		t.Fatalf("refresh response = %q, want the omitted-token shape", refreshed.RefreshToken)
	}
	again, err := client.Refresh(context.Background(), token)
	if err != nil {
		t.Fatalf("the original refresh token stopped working: %v", err)
	}
	if again.AccessToken == "" {
		t.Fatal("the second refresh returned no access token")
	}
	if server.State().SecretViolations != 0 {
		t.Fatal("refresh sent a client secret")
	}
}

func TestRefreshRotationAndInvalidGrant(t *testing.T) {
	server := startFake(t)
	server.Configure(func(control *fake.Control) { control.KeepRefresh = false })
	client := clientFor(server)
	token, _ := authorize(t, server, client)

	refreshed, err := client.Refresh(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.RefreshToken == "" || refreshed.RefreshToken == token.RefreshToken {
		t.Fatalf("refresh token did not rotate: %+v", refreshed)
	}
	if _, err := client.Refresh(context.Background(), token); !errors.Is(err, appsync.ErrReauthorizationRequired) {
		t.Fatalf("old refresh token error = %v, want ErrReauthorizationRequired", err)
	}
	if _, err := client.Refresh(context.Background(), refreshed); err != nil {
		t.Fatalf("the rotated refresh token failed: %v", err)
	}

	server.Configure(func(control *fake.Control) { control.InvalidateRefresh = true })
	if _, err := client.Refresh(context.Background(), refreshed); !errors.Is(err, appsync.ErrReauthorizationRequired) {
		t.Fatalf("revoked refresh token error = %v, want ErrReauthorizationRequired", err)
	}
}

func TestRefreshWithoutARefreshTokenRequiresReauthorization(t *testing.T) {
	client := gdrive.New(gdrive.Options{ClientID: "client"})
	_, err := client.Refresh(context.Background(), domainsync.Token{AccessToken: "only"})
	if !errors.Is(err, appsync.ErrReauthorizationRequired) {
		t.Fatalf("error = %v, want ErrReauthorizationRequired", err)
	}
}

func TestAppDataFolderReadWriteAndAccountIsolation(t *testing.T) {
	server := startFake(t)
	client := clientFor(server)
	token, _ := authorize(t, server, client)

	file, exists, err := client.Read(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatalf("an empty account has a file: %+v", file)
	}

	created, err := client.Write(context.Background(), token, appsync.RemoteFile{}, []byte("vault-one"))
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.ETag == "" {
		t.Fatalf("created file = %+v", created)
	}
	read, exists, err := client.Read(context.Background(), token)
	if err != nil || !exists {
		t.Fatalf("Read after create = %+v, %v, %v", read, exists, err)
	}
	if read.ID != created.ID || string(read.Content) != "vault-one" || read.ETag == "" {
		t.Fatalf("read file = %+v", read)
	}

	updated, err := client.Write(context.Background(), token, read, []byte("vault-two"))
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != created.ID {
		t.Fatalf("update changed the file id: %+v", updated)
	}
	read, exists, err = client.Read(context.Background(), token)
	if err != nil || !exists || string(read.Content) != "vault-two" {
		t.Fatalf("Read after update = %+v, %v, %v", read, exists, err)
	}
	state := server.State()
	if state.Creates != 1 || state.Updates != 1 || state.Downloads < 2 {
		t.Fatalf("fake counters = %+v", state)
	}
	if len(state.Accounts[0].Files) != 1 || state.Accounts[0].Files[0].Name != domainsync.SyncFileName {
		t.Fatalf("primary appDataFolder = %+v", state.Accounts[0])
	}

	// A second Google account sees an empty appDataFolder, while the first
	// account's token still sees its own file.
	server.Configure(func(control *fake.Control) { control.Account = "second" })
	secondToken, secondGrant := authorize(t, server, client)
	if secondGrant.ID != "google-account-2" {
		t.Fatalf("second grant = %+v", secondGrant)
	}
	if other, exists, err := client.Read(context.Background(), secondToken); err != nil || exists {
		t.Fatalf("second account read = %+v, %v, %v", other, exists, err)
	}
	if _, err := client.Write(context.Background(), secondToken, appsync.RemoteFile{}, []byte("vault-two-account")); err != nil {
		t.Fatal(err)
	}
	primary, exists, err := client.Read(context.Background(), token)
	if err != nil || !exists || string(primary.Content) != "vault-two" {
		t.Fatalf("primary account after second account wrote = %+v, %v, %v", primary, exists, err)
	}
	second, exists, err := client.Read(context.Background(), secondToken)
	if err != nil || !exists || string(second.Content) != "vault-two-account" {
		t.Fatalf("second account after write = %+v, %v, %v", second, exists, err)
	}
}

func TestUpdateRejectsAFileChangedSinceTheRead(t *testing.T) {
	server := startFake(t)
	client := clientFor(server)
	token, _ := authorize(t, server, client)

	if _, err := client.Write(context.Background(), token, appsync.RemoteFile{}, []byte("vault-one")); err != nil {
		t.Fatal(err)
	}
	current, exists, err := client.Read(context.Background(), token)
	if err != nil || !exists {
		t.Fatalf("Read = %+v, %v, %v", current, exists, err)
	}
	if err := server.BumpModified("primary", current.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Write(context.Background(), token, current, []byte("vault-stale")); !errors.Is(err, appsync.ErrConflict) {
		t.Fatalf("stale update = %v, want ErrConflict", err)
	}
	if updates := server.State().Updates; updates != 0 {
		t.Fatalf("the fake received %d media updates, want none for a stale read", updates)
	}
	after, exists, err := client.Read(context.Background(), token)
	if err != nil || !exists || string(after.Content) != "vault-one" {
		t.Fatalf("content after the rejected update = %+v, %v, %v", after, exists, err)
	}
}

func TestAppDataFolderPaginationIsFollowed(t *testing.T) {
	server := startFake(t)
	client := clientFor(server)
	token, _ := authorize(t, server, client)
	// A decoy created first occupies page one, so the product file is only
	// found after following nextPageToken.
	if _, err := server.SeedFile("primary", "other.json", []byte("decoy")); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Write(context.Background(), token, appsync.RemoteFile{}, []byte("first")); err != nil {
		t.Fatal(err)
	}
	server.Configure(func(control *fake.Control) {
		control.PageSize = 1
		control.IgnoreNameFilter = true
	})
	before := server.State().Lists
	file, exists, err := client.Read(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if !exists || len(file.Content) == 0 {
		t.Fatalf("paginated read = %+v, %v", file, exists)
	}
	if lists := server.State().Lists - before; lists < 2 {
		t.Fatalf("list requests = %d, want at least two pages", lists)
	}
}

func TestReadRequiresAppDataFolder(t *testing.T) {
	server := startFake(t)
	client := clientFor(server)
	token, _ := authorize(t, server, client)
	// The fake rejects any list that is not restricted to appDataFolder, so a
	// successful read proves the adapter sent spaces=appDataFolder.
	if _, _, err := client.Read(context.Background(), token); err != nil {
		t.Fatal(err)
	}
}

func TestDriveErrorsMapOntoTheSyncTaxonomy(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, want: appsync.ErrUnauthorized},
		{name: "rate limited", status: http.StatusTooManyRequests, want: appsync.ErrRateLimited},
		{name: "forbidden quota", status: http.StatusForbidden, body: `{"error":{"errors":[{"reason":"userRateLimitExceeded"}]}}`, want: appsync.ErrRateLimited},
		{name: "forbidden permission", status: http.StatusForbidden, want: appsync.ErrUnauthorized},
		{name: "server failure", status: http.StatusInternalServerError, want: appsync.ErrNetwork},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			client := gdrive.New(gdrive.Options{
				APIBase:  server.URL + "/drive/v3",
				ClientID: "client",
			})
			_, _, err := client.Read(context.Background(), domainsync.Token{AccessToken: "token"})
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestDriveMissingFilesMapToThePort(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/files") && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"files": []map[string]string{{"id": "gone", "name": domainsync.SyncFileName, "modifiedTime": "2026-01-01T00:00:00Z"}},
			})
		case strings.Contains(r.URL.Path, "/files/gone") && r.Method == http.MethodGet:
			http.NotFound(w, r)
		case strings.Contains(r.URL.Path, "/files/gone") && r.Method == http.MethodPatch:
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := gdrive.New(gdrive.Options{
		APIBase:  server.URL + "/drive/v3",
		ClientID: "client",
	})
	token := domainsync.Token{AccessToken: "token"}
	_, exists, err := client.Read(context.Background(), token)
	if err != nil || exists {
		t.Fatalf("Read of a deleted file = exists %v, err %v", exists, err)
	}
	if _, err := client.Write(context.Background(), token, appsync.RemoteFile{ID: "gone"}, []byte("content")); !errors.Is(err, appsync.ErrConflict) {
		t.Fatalf("update of a deleted file = %v, want ErrConflict", err)
	}
}

func TestOptionsFromEnvReadsTheFakeOverrides(t *testing.T) {
	t.Setenv(gdrive.EnvAuthBase, "http://127.0.0.1:1/auth")
	t.Setenv(gdrive.EnvTokenBase, "http://127.0.0.1:1/token")
	t.Setenv(gdrive.EnvAPIBase, "http://127.0.0.1:1/drive/v3")
	t.Setenv(gdrive.EnvUploadBase, "http://127.0.0.1:1/upload/drive/v3")
	t.Setenv(gdrive.EnvClientID, "fake-client")
	options := gdrive.OptionsFromEnv()
	if options.AuthBase != "http://127.0.0.1:1/auth" ||
		options.TokenBase != "http://127.0.0.1:1/token" ||
		options.APIBase != "http://127.0.0.1:1/drive/v3" ||
		options.UploadBase != "http://127.0.0.1:1/upload/drive/v3" ||
		options.ClientID != "fake-client" {
		t.Fatalf("options = %+v", options)
	}
}
