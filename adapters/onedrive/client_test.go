package onedrive_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/mslxl/natcatty/adapters/onedrive"
	"github.com/mslxl/natcatty/adapters/onedrive/fake"
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

func clientFor(server *fake.Server) *onedrive.Client {
	return onedrive.New(onedrive.Options{
		AuthBase:        server.URL(),
		TokenBase:       server.URL(),
		GraphBase:       server.URL() + "/v1.0",
		ClientID:        server.ClientID(),
		AuthTimeout:     5 * time.Second,
		NotFoundBackoff: time.Millisecond,
		HTTPClient:      &http.Client{Timeout: 5 * time.Second},
	})
}

// openInBrowser drives the fake the way the system browser would: it requests
// the consent URL, follows the redirect to the loopback listener, and drains
// the page. Authorization success is judged by the returned token.
func openInBrowser(authURL string) error {
	response, err := (&http.Client{Timeout: 5 * time.Second}).Get(authURL)
	if err == nil {
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
	}
	return nil
}

func authorize(t *testing.T, server *fake.Server, client *onedrive.Client) (domainsync.Token, appsync.Grant) {
	t.Helper()
	token, grant, err := client.Authorize(context.Background(), openInBrowser)
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	return token, grant
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
	if authURL.Path != "/authorize" {
		t.Fatalf("authorize path = %q", authURL.Path)
	}
	for key, want := range map[string]string{
		"client_id":             server.ClientID(),
		"response_type":         "code",
		"code_challenge_method": "S256",
		"response_mode":         "query",
		"prompt":                "consent",
	} {
		if got := query.Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	scope := query.Get("scope")
	if !strings.Contains(scope, "Files.ReadWrite.AppFolder") || !strings.Contains(scope, "offline_access") {
		t.Fatalf("scope = %q, want the app-folder and offline scopes", scope)
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
	if grant.ID != "onedrive-account-1" || grant.Login != "one@example.test" {
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
	if user.ID != "onedrive-account-1" || user.Login != "one@example.test" {
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
	client := onedrive.New(onedrive.Options{
		AuthBase:    server.URL(),
		TokenBase:   server.URL(),
		GraphBase:   server.URL() + "/v1.0",
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
	_, _, err := onedrive.New(onedrive.Options{}).Authorize(context.Background(), openInBrowser)
	if !errors.Is(err, appsync.ErrNotConfigured) {
		t.Fatalf("error = %v, want ErrNotConfigured", err)
	}
}

func TestRefreshRotatesAndRejectsTheConsumedToken(t *testing.T) {
	server := startFake(t)
	client := clientFor(server)
	token, _ := authorize(t, server, client)

	refreshed, err := client.Refresh(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.AccessToken == "" || refreshed.AccessToken == token.AccessToken {
		t.Fatalf("access token did not rotate: %+v", refreshed)
	}
	if refreshed.RefreshToken == "" || refreshed.RefreshToken == token.RefreshToken {
		t.Fatalf("refresh token did not rotate: %+v", refreshed)
	}
	if _, err := client.Refresh(context.Background(), token); !errors.Is(err, appsync.ErrReauthorizationRequired) {
		t.Fatalf("consumed refresh token error = %v, want ErrReauthorizationRequired", err)
	}
	if _, err := client.Refresh(context.Background(), refreshed); err != nil {
		t.Fatalf("the rotated refresh token failed: %v", err)
	}
	if server.State().SecretViolations != 0 {
		t.Fatal("refresh sent a client secret")
	}
}

func TestRefreshKeepsThePresentedTokenWhenMicrosoftOmitsIt(t *testing.T) {
	server := startFake(t)
	server.Configure(func(control *fake.Control) { control.KeepRefresh = true })
	client := clientFor(server)
	token, _ := authorize(t, server, client)

	refreshed, err := client.Refresh(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.AccessToken == "" || refreshed.AccessToken == token.AccessToken {
		t.Fatalf("access token did not rotate: %+v", refreshed)
	}
	if refreshed.RefreshToken != "" {
		t.Fatalf("refresh response = %q, want the omitted-token shape", refreshed.RefreshToken)
	}
	// The application merges the omitted field from the token it presented;
	// this is the merged credential the service retries with.
	carried := refreshed
	carried.RefreshToken = token.RefreshToken
	again, err := client.Refresh(context.Background(), carried)
	if err != nil {
		t.Fatalf("the carried-forward refresh token stopped working: %v", err)
	}
	if again.AccessToken == "" {
		t.Fatal("the second refresh returned no access token")
	}
}

func TestRefreshInvalidGrantRequiresReauthorization(t *testing.T) {
	server := startFake(t)
	client := clientFor(server)
	token, _ := authorize(t, server, client)

	server.Configure(func(control *fake.Control) { control.InvalidateRefresh = true })
	if _, err := client.Refresh(context.Background(), token); !errors.Is(err, appsync.ErrReauthorizationRequired) {
		t.Fatalf("revoked refresh token error = %v, want ErrReauthorizationRequired", err)
	}
}

func TestRefreshWithoutARefreshTokenRequiresReauthorization(t *testing.T) {
	client := onedrive.New(onedrive.Options{ClientID: "client"})
	_, err := client.Refresh(context.Background(), domainsync.Token{AccessToken: "only"})
	if !errors.Is(err, appsync.ErrReauthorizationRequired) {
		t.Fatalf("error = %v, want ErrReauthorizationRequired", err)
	}
}

func TestApprootReadWriteAndAccountIsolation(t *testing.T) {
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
		t.Fatalf("primary approot = %+v", state.Accounts[0])
	}

	// A second Microsoft account sees an empty approot while the first
	// account's own token still sees its file.
	server.Configure(func(control *fake.Control) { control.Account = "second" })
	secondToken, secondGrant := authorize(t, server, client)
	if secondGrant.ID != "onedrive-account-2" || secondGrant.Login != "two@example.test" {
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

func TestCreateIsConditionalOnNoExistingFile(t *testing.T) {
	server := startFake(t)
	client := clientFor(server)
	token, _ := authorize(t, server, client)

	if _, err := server.SeedFile("primary", domainsync.SyncFileName, []byte("peer-copy")); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Write(context.Background(), token, appsync.RemoteFile{}, []byte("ours")); !errors.Is(err, appsync.ErrConflict) {
		t.Fatalf("create over a peer file = %v, want ErrConflict", err)
	}
	state := server.State()
	if state.Creates != 0 || state.Conflicts == 0 {
		t.Fatalf("fake counters = %+v", state)
	}
	content, ok := server.FileContent("primary", domainsync.SyncFileName)
	if !ok || string(content) != "peer-copy" {
		t.Fatalf("the peer file changed: %q, %v", content, ok)
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
	if err := server.BumpETag("primary", current.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Write(context.Background(), token, current, []byte("vault-stale")); !errors.Is(err, appsync.ErrConflict) {
		t.Fatalf("stale update = %v, want ErrConflict", err)
	}
	if updates := server.State().Updates; updates != 0 {
		t.Fatalf("the fake received %d content updates, want none for a stale read", updates)
	}
	after, exists, err := client.Read(context.Background(), token)
	if err != nil || !exists || string(after.Content) != "vault-one" {
		t.Fatalf("content after the rejected update = %+v, %v, %v", after, exists, err)
	}
}

func TestUpdateOfADeletedFileBecomesAConflict(t *testing.T) {
	server := startFake(t)
	client := clientFor(server)
	token, _ := authorize(t, server, client)
	_, err := client.Write(context.Background(), token, appsync.RemoteFile{ID: "missing", ETag: `"gone"`}, []byte("content"))
	if !errors.Is(err, appsync.ErrConflict) {
		t.Fatalf("update of a deleted file = %v, want ErrConflict", err)
	}
}

func TestUpdateRefusesAnUnconditionalWrite(t *testing.T) {
	server := startFake(t)
	client := clientFor(server)
	token, _ := authorize(t, server, client)

	created, err := client.Write(context.Background(), token, appsync.RemoteFile{}, []byte("vault-one"))
	if err != nil {
		t.Fatal(err)
	}
	created.ETag = ""
	if _, err := client.Write(context.Background(), token, created, []byte("unconditional")); !errors.Is(err, appsync.ErrConflict) {
		t.Fatalf("update without an eTag = %v, want ErrConflict", err)
	}
	state := server.State()
	if state.Updates != 0 || state.Conflicts != 0 {
		t.Fatalf("an unconditional update reached the fake: %+v", state)
	}
	content, ok := server.FileContent("primary", domainsync.SyncFileName)
	if !ok || string(content) != "vault-one" {
		t.Fatalf("the file changed without a condition: %q, %v", content, ok)
	}
}

func TestReadRetriesATransientNotFound(t *testing.T) {
	server := startFake(t)
	client := clientFor(server)
	token, _ := authorize(t, server, client)
	if _, err := server.SeedFile("primary", domainsync.SyncFileName, []byte("peer-copy")); err != nil {
		t.Fatal(err)
	}
	// Graph briefly 404s a file another device uploaded moments ago. One
	// transient miss must not be reported as an empty cloud.
	server.Configure(func(control *fake.Control) { control.TransientNotFound = 1 })

	file, exists, err := client.Read(context.Background(), token)
	if err != nil || !exists || string(file.Content) != "peer-copy" {
		t.Fatalf("Read = %+v, %v, %v", file, exists, err)
	}
	if reads := server.State().Reads; reads != 2 {
		t.Fatalf("metadata reads = %d, want the 404 and its retry", reads)
	}
}

func TestReadRetriesBeforeDeclaringTheCloudEmpty(t *testing.T) {
	server := startFake(t)
	client := clientFor(server)
	token, _ := authorize(t, server, client)

	_, exists, err := client.Read(context.Background(), token)
	if err != nil || exists {
		t.Fatalf("Read = %v, %v", exists, err)
	}
	if reads := server.State().Reads; reads != 3 {
		t.Fatalf("metadata reads = %d, want the initial read plus two retries", reads)
	}
}

func TestReadRetriesATransientDownloadNotFound(t *testing.T) {
	server := startFake(t)
	client := clientFor(server)
	token, _ := authorize(t, server, client)
	if _, err := server.SeedFile("primary", domainsync.SyncFileName, []byte("peer-copy")); err != nil {
		t.Fatal(err)
	}
	server.Configure(func(control *fake.Control) { control.TransientDownloadNotFound = 1 })

	file, exists, err := client.Read(context.Background(), token)
	if err != nil || !exists || string(file.Content) != "peer-copy" {
		t.Fatalf("Read = %+v, %v, %v", file, exists, err)
	}
	state := server.State()
	if state.Downloads != 1 || state.Reads != 1 {
		t.Fatalf("fake counters = %+v", state)
	}
}

func TestGraphErrorsMapOntoTheSyncTaxonomy(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, want: appsync.ErrUnauthorized},
		{name: "rate limited", status: http.StatusTooManyRequests, want: appsync.ErrRateLimited},
		{name: "forbidden quota", status: http.StatusForbidden, body: `{"error":{"code":"quotaLimitReached"}}`, want: appsync.ErrRateLimited},
		{name: "forbidden permission", status: http.StatusForbidden, want: appsync.ErrUnauthorized},
		{name: "server failure", status: http.StatusInternalServerError, want: appsync.ErrNetwork},
		{name: "locked", status: http.StatusLocked, want: appsync.ErrConflict},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			client := onedrive.New(onedrive.Options{
				GraphBase:  server.URL + "/v1.0",
				ClientID:   "client",
				HTTPClient: server.Client(),
			})
			_, _, err := client.Read(context.Background(), domainsync.Token{AccessToken: "token"})
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestOptionsFromEnvReadsTheFakeOverrides(t *testing.T) {
	t.Setenv(onedrive.EnvAuthBase, "http://127.0.0.1:1/oauth2/v2.0")
	t.Setenv(onedrive.EnvTokenBase, "http://127.0.0.1:1/oauth2/v2.0")
	t.Setenv(onedrive.EnvGraphBase, "http://127.0.0.1:1/v1.0")
	t.Setenv(onedrive.EnvClientID, "fake-client")
	options := onedrive.OptionsFromEnv()
	if options.AuthBase != "http://127.0.0.1:1/oauth2/v2.0" ||
		options.TokenBase != "http://127.0.0.1:1/oauth2/v2.0" ||
		options.GraphBase != "http://127.0.0.1:1/v1.0" ||
		options.ClientID != "fake-client" {
		t.Fatalf("options = %+v", options)
	}
}
