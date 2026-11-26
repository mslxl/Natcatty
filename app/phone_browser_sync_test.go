package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mslxl/natcatty/adapters/store"
	adaptersyncstore "github.com/mslxl/natcatty/adapters/syncstore"
	apppreferences "github.com/mslxl/natcatty/application/preferences"
	appsync "github.com/mslxl/natcatty/application/sync"
	appterminalsettings "github.com/mslxl/natcatty/application/terminalsettings"
	"github.com/mslxl/natcatty/domain/model"
	domainsync "github.com/mslxl/natcatty/domain/sync"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// browserTestToken is the credential the fake browser provider hands out. The
// tests prove it reaches the token store and nowhere a user or a sync document
// can read.
const browserTestToken = "ya29_phone_browser_test_token"

// googleReversedClientRedirectURI is the host-less redirect Google's mobile
// OAuth clients require. The acceptance escape hatch in docs/mobile.md sets
// NATCATTY_OAUTH_REDIRECT_URI to exactly this shape; the composition root must
// build Vault Sync with it instead of hiding every provider.
const googleReversedClientRedirectURI = "com.googleusercontent.apps.123-abc.apps.googleusercontent.com:/oauth2redirect"

// browserTestRemote is an in-memory Drive/OneDrive account. It implements the
// provider port and the shared browser-authorization port, so the phone runs
// the real appsync.Service connect and upload/download state machine; only the
// browser, the network, and the token exchange are replaced.
type browserTestRemote struct {
	mu   sync.Mutex
	file appsync.RemoteFile

	token        domainsync.Token
	grant        appsync.Grant
	authorizeErr error
	// release blocks Authorize after the browser opener returns. A nil
	// channel authorizes at once; a test closes it to let a cancel test's
	// retry finish.
	release chan struct{}
	started chan struct{}
	writes  int
	opens   int
}

var _ appsync.Remote = (*browserTestRemote)(nil)
var _ appsync.BrowserAuthorizer = (*browserTestRemote)(nil)
var _ appsync.TokenRefresher = (*browserTestRemote)(nil)

func newBrowserTestRemote() *browserTestRemote {
	return &browserTestRemote{
		token: domainsync.Token{AccessToken: browserTestToken, TokenType: "Bearer"},
		grant: appsync.Grant{ID: "browser-id-1", Login: "user@example.test"},
	}
}

func (r *browserTestRemote) Authorize(ctx context.Context, open func(string) error) (domainsync.Token, appsync.Grant, error) {
	r.mu.Lock()
	r.opens++
	err := r.authorizeErr
	release := r.release
	started := r.started
	r.mu.Unlock()
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if openErr := open("https://accounts.example/authorize"); openErr != nil {
		return domainsync.Token{}, appsync.Grant{}, openErr
	}
	if err != nil {
		return domainsync.Token{}, appsync.Grant{}, err
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return domainsync.Token{}, appsync.Grant{}, ctx.Err()
		}
	}
	return r.token, r.grant, nil
}

func (r *browserTestRemote) Refresh(context.Context, domainsync.Token) (domainsync.Token, error) {
	return domainsync.Token{}, appsync.ErrReauthorizationRequired
}

func (r *browserTestRemote) User(context.Context, domainsync.Token) (appsync.Grant, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.grant, nil
}

func (r *browserTestRemote) Read(context.Context, domainsync.Token) (appsync.RemoteFile, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file.ID == "" {
		return appsync.RemoteFile{}, false, nil
	}
	file := r.file
	file.Content = append([]byte(nil), r.file.Content...)
	return file, true, nil
}

func (r *browserTestRemote) Write(_ context.Context, _ domainsync.Token,
	current appsync.RemoteFile, content []byte) (appsync.RemoteFile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if current.ID != "" && current.ETag != r.file.ETag {
		return appsync.RemoteFile{}, appsync.ErrConflict
	}
	r.writes++
	r.file = appsync.RemoteFile{
		ID:        "browser-file-1",
		ETag:      "browser-etag",
		Content:   append([]byte(nil), content...),
		UpdatedAt: time.Now().UnixMilli(),
	}
	return r.file, nil
}

func (r *browserTestRemote) content() ([]byte, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file.ID == "" {
		return nil, false
	}
	return append([]byte(nil), r.file.Content...), true
}

// browserDevice is one phone built by the real newPhoneSync composition root
// with a browser-authorized provider.
type browserDevice struct {
	sync     *phoneSync
	database *store.Store
	tokens   *phoneSyncTokens
	dir      string
}

func newBrowserDevice(t *testing.T, ctx context.Context, dir, name, provider string,
	remote *browserTestRemote, tokens *phoneSyncTokens) *browserDevice {
	t.Helper()
	database, err := store.Open(ctx, filepath.Join(dir, "natcatty.db"))
	if err != nil {
		t.Fatalf("%s: open store: %v", name, err)
	}
	t.Cleanup(func() { _ = database.Close() })
	secrets, err := store.OpenSecretStore(ctx, database, newPhoneTestKeys())
	if err != nil {
		t.Fatalf("%s: open secret store: %v", name, err)
	}
	preferences := apppreferences.NewService(
		store.OpenPreferences(filepath.Join(dir, "preferences.json")))
	if err := preferences.Load(ctx); err != nil {
		t.Fatalf("%s: load preferences: %v", name, err)
	}
	terminal := appterminalsettings.NewService(
		store.OpenTerminalSettings(filepath.Join(dir, "terminal-settings.json")))
	if err := terminal.Load(ctx); err != nil {
		t.Fatalf("%s: load terminal settings: %v", name, err)
	}
	if err := database.Save(ctx, domainvault.Host{
		ID:        model.HostID(name + "-host"),
		Label:     name + " host",
		Hostname:  name + ".example",
		Port:      22,
		Protocol:  domainvault.ProtocolSSH,
		UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("%s: save host: %v", name, err)
	}
	synced, err := newPhoneSync(ctx, database, secrets, preferences, terminal, dir,
		withPhoneSyncConfigs(adaptersyncstore.OpenConfig(filepath.Join(dir, "cloud-sync.json"))),
		withPhoneSyncTokens(tokens),
		withPhoneSyncRemote(provider, remote),
		withPhoneSyncProvider(provider),
		withPhoneSyncDevice(name+"-device", name, "test"),
		// The browser opener is a fake: the fake provider records that the
		// consent URL was handed to the system browser.
		withPhoneSyncBrowserOpener(func(string) error { return nil }),
	)
	if err != nil {
		t.Fatalf("%s: build phone sync: %v", name, err)
	}
	t.Cleanup(synced.Stop)
	return &browserDevice{sync: synced, database: database, tokens: tokens, dir: dir}
}

// waitForBrowserSync polls the observable snapshot with a hard deadline. The
// poll interval is a cadence; the condition and deadline are the
// synchronization.
func waitForBrowserSync(t *testing.T, phone *phoneSync, want string, cond func(appsync.Snapshot) bool) appsync.Snapshot {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		snapshot := phone.Snapshot()
		if cond(snapshot) {
			return snapshot
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s; last snapshot = %+v", want, snapshot)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func startBrowserConnect(t *testing.T, phone *phoneSync, provider string) {
	t.Helper()
	if err := phone.StartBrowserConnect(syncMasterPassword, provider); err != nil {
		t.Fatalf("start %s connect: %v", provider, err)
	}
}

// TestPhoneDriveSyncUploadsAndDownloads drives the ticket's acceptance through
// the shared engine: the browser sign-in connects the phone, a portable change
// uploads in the same encrypted envelope the other providers use, and a second
// device configured for the same account downloads it. The access token reaches
// only the credential store.
func TestPhoneDriveSyncUploadsAndDownloads(t *testing.T) {
	ctx := context.Background()
	remote := newBrowserTestRemote()

	phone := newBrowserDevice(t, ctx, t.TempDir(), "phone", appsync.ProviderGoogleDrive,
		remote, &phoneSyncTokens{})
	startBrowserConnect(t, phone.sync, appsync.ProviderGoogleDrive)
	// Wait for Idle, not just Connected: Connect publishes Connected in memory
	// before it durably saves the profile, and the test reads that file.
	snapshot := waitForBrowserSync(t, phone.sync, "a connected Drive profile",
		func(s appsync.Snapshot) bool { return s.Connected && s.Status == appsync.StatusIdle })
	if snapshot.Provider != appsync.ProviderGoogleDrive {
		t.Fatalf("provider = %q, want google", snapshot.Provider)
	}
	if snapshot.Login != "user@example.test" {
		t.Fatalf("login = %q, want the authorized account", snapshot.Login)
	}

	// The credential is in the token store and tagged for the provider.
	token, err := phone.tokens.Load(ctx)
	if err != nil {
		t.Fatalf("load stored token: %v", err)
	}
	if token.AccessToken != browserTestToken || token.Provider != appsync.ProviderGoogleDrive {
		t.Fatalf("stored token provider/credential = %q/%q, want google/the fake credential",
			token.Provider, token.AccessToken)
	}

	// Nothing in the non-secret profile may contain the credential.
	profile, err := os.ReadFile(filepath.Join(phone.dir, "cloud-sync.json"))
	if err != nil {
		t.Fatalf("read sync profile: %v", err)
	}
	if strings.Contains(string(profile), browserTestToken) {
		t.Fatal("the access token was written into the sync profile document")
	}

	if err := phone.sync.service.SetPassword(syncMasterPassword); err != nil {
		t.Fatalf("set password: %v", err)
	}
	result, err := phone.sync.service.Sync(ctx, appsync.TriggerManual)
	if err != nil {
		t.Fatalf("phone upload: %v", err)
	}
	if !result.Uploaded {
		t.Fatalf("phone sync result = %+v, want an upload", result)
	}
	content, ok := remote.content()
	if !ok {
		t.Fatal("the phone did not publish a cloud copy")
	}
	envelope, err := domainsync.UnmarshalFile(content)
	if err != nil {
		t.Fatalf("the cloud copy is not the shared sync envelope: %v", err)
	}
	if envelope.Meta.DeviceID != "phone-device" {
		t.Fatalf("envelope device = %q, want the phone's device id", envelope.Meta.DeviceID)
	}

	desktop := newBrowserDevice(t, ctx, t.TempDir(), "desktop", appsync.ProviderGoogleDrive,
		remote, &phoneSyncTokens{})
	startBrowserConnect(t, desktop.sync, appsync.ProviderGoogleDrive)
	waitForBrowserSync(t, desktop.sync, "the second connected profile",
		func(s appsync.Snapshot) bool { return s.Connected && s.Status == appsync.StatusIdle })
	if err := desktop.sync.service.SetStrategy(ctx, domainsync.StrategyPreferCloud); err != nil {
		t.Fatalf("set desktop strategy: %v", err)
	}
	result, err = desktop.sync.service.Sync(ctx, appsync.TriggerManual)
	if err != nil {
		t.Fatalf("desktop download: %v", err)
	}
	if !result.Downloaded {
		t.Fatalf("desktop sync result = %+v, want a download", result)
	}
	hosts, err := desktop.database.List(ctx)
	if err != nil {
		t.Fatalf("list desktop hosts: %v", err)
	}
	found := false
	for _, host := range hosts {
		if host.Label == "phone host" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the desktop did not receive the phone's host; hosts = %+v", hosts)
	}
}

// TestPhoneOneDriveCancelIsRetryable proves the page's Cancel aborts a
// browser sign-in, stores no token, reports the retryable authorization state,
// and that a fresh attempt succeeds.
func TestPhoneOneDriveCancelIsRetryable(t *testing.T) {
	ctx := context.Background()
	remote := newBrowserTestRemote()
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	remote.release = release
	remote.started = started

	phone := newBrowserDevice(t, ctx, t.TempDir(), "phone", appsync.ProviderOneDrive,
		remote, &phoneSyncTokens{})
	startBrowserConnect(t, phone.sync, appsync.ProviderOneDrive)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the browser authorization did not start")
	}
	phone.sync.CancelBrowserConnect()
	snapshot := waitForBrowserSync(t, phone.sync, "the cancelled state",
		func(s appsync.Snapshot) bool { return s.Status == appsync.StatusFailed })
	if snapshot.Problem != appsync.ProblemAuthorization {
		t.Fatalf("problem = %q, want authorization", snapshot.Problem)
	}
	if snapshot.Connected {
		t.Fatal("a cancelled sign-in must not connect the profile")
	}
	if _, err := phone.tokens.Load(ctx); err == nil {
		t.Fatal("a cancelled authorization stored a token")
	}

	// The retry opens the browser again; this time the fake completes.
	remote.mu.Lock()
	remote.release = nil
	remote.started = nil
	remote.mu.Unlock()
	close(release)
	startBrowserConnect(t, phone.sync, appsync.ProviderOneDrive)
	snapshot = waitForBrowserSync(t, phone.sync, "the retried connected profile",
		func(s appsync.Snapshot) bool { return s.Connected })
	if snapshot.Provider != appsync.ProviderOneDrive {
		t.Fatalf("provider = %q, want onedrive", snapshot.Provider)
	}
}

// TestPhoneDriveFailedExchangeIsRetryable proves a rejected token exchange or
// a state mismatch returns the retryable authorization state without storing a
// token, and that retrying reconnects.
func TestPhoneDriveFailedExchangeIsRetryable(t *testing.T) {
	ctx := context.Background()
	remote := newBrowserTestRemote()
	remote.authorizeErr = fmt.Errorf("%w: the state did not match", appsync.ErrAuthorizationState)

	phone := newBrowserDevice(t, ctx, t.TempDir(), "phone", appsync.ProviderGoogleDrive,
		remote, &phoneSyncTokens{})
	startBrowserConnect(t, phone.sync, appsync.ProviderGoogleDrive)
	snapshot := waitForBrowserSync(t, phone.sync, "the failed exchange",
		func(s appsync.Snapshot) bool { return s.Status == appsync.StatusFailed })
	if snapshot.Problem != appsync.ProblemAuthorization {
		t.Fatalf("problem = %q, want authorization", snapshot.Problem)
	}
	if _, err := phone.tokens.Load(ctx); err == nil {
		t.Fatal("a failed exchange stored a token")
	}

	remote.mu.Lock()
	remote.authorizeErr = nil
	remote.mu.Unlock()
	startBrowserConnect(t, phone.sync, appsync.ProviderGoogleDrive)
	waitForBrowserSync(t, phone.sync, "the retried connected profile",
		func(s appsync.Snapshot) bool { return s.Connected })
}

// TestPhoneRoutesOnlyItsOwnOAuthCallback proves the composition root can tell
// an OAuth redirect from an ssh:// Host link, and that a redirect arriving
// with no authorization waiting is consumed harmlessly.
func TestPhoneRoutesOnlyItsOwnOAuthCallback(t *testing.T) {
	ctx := context.Background()
	phone := newBrowserDevice(t, ctx, t.TempDir(), "phone", appsync.ProviderGoogleDrive,
		newBrowserTestRemote(), &phoneSyncTokens{})
	if phone.sync.HandleOpenURL("ssh://host.example") {
		t.Fatal("an ssh:// link was claimed by the OAuth router")
	}
	if !phone.sync.HandleOpenURL("netcatty://oauth/callback?code=x&state=y") {
		t.Fatal("the app's OAuth scheme was not claimed")
	}
	if !phone.sync.HandleOpenURL("netcatty://other/path") {
		t.Fatal("another URL of the app's scheme must still be consumed")
	}
}

// TestPhoneBrowserConnectRejectsNonBrowserProviders keeps the page's provider
// switch honest: StartBrowserConnect only accepts the two browser providers.
func TestPhoneBrowserConnectRejectsNonBrowserProviders(t *testing.T) {
	ctx := context.Background()
	phone := newBrowserDevice(t, ctx, t.TempDir(), "phone", appsync.ProviderGoogleDrive,
		newBrowserTestRemote(), &phoneSyncTokens{})
	for _, provider := range []string{appsync.ProviderWebDAV, appsync.ProviderS3, appsync.ProviderGitHub} {
		err := phone.sync.StartBrowserConnect(syncMasterPassword, provider)
		if !errors.Is(err, appsync.ErrNotConfigured) {
			t.Fatalf("StartBrowserConnect(%q) = %v, want ErrNotConfigured", provider, err)
		}
	}
}

// TestPhoneSyncAcceptsAGoogleReversedClientRedirect covers the provider-mandated
// host-less redirect end to end: newPhoneSync builds the real graph, the
// consent URL carries the reversed-client-id URI, the callback delivered by
// the OS reaches the shared PKCE flow, and the fake Google endpoints exchange
// the code. Before the registry accepted scheme:/path, newPhoneSync failed
// here, the composition root logged "Vault sync unavailable", and every sync
// provider disappeared from Settings on a Google-configured build.
func TestPhoneSyncAcceptsAGoogleReversedClientRedirect(t *testing.T) {
	ctx := context.Background()
	authURL := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/token":
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse the token request: %v", err)
				http.Error(w, "bad form", http.StatusBadRequest)
				return
			}
			if got := r.Form.Get("code"); got != "google-code" {
				t.Errorf("token request code = %q, want the delivered callback code", got)
			}
			if got := r.Form.Get("redirect_uri"); got != googleReversedClientRedirectURI {
				t.Errorf("token request redirect_uri = %q, want %q", got, googleReversedClientRedirectURI)
			}
			if r.Form.Get("code_verifier") == "" {
				t.Error("the token request carried no PKCE verifier")
			}
			fmt.Fprint(w, `{"access_token":"google-access","refresh_token":"google-refresh","token_type":"Bearer","expires_in":3600}`)
		case r.Method == http.MethodGet && r.URL.Path == "/oauth2/v2/userinfo":
			fmt.Fprint(w, `{"id":"google-user","email":"user@example.test"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("NATCATTY_GDRIVE_AUTH_BASE", server.URL)
	t.Setenv("NATCATTY_GDRIVE_TOKEN_BASE", server.URL)
	t.Setenv("NATCATTY_GDRIVE_API_BASE", server.URL+"/drive/v3")
	t.Setenv("NATCATTY_GDRIVE_UPLOAD_BASE", server.URL+"/upload/drive/v3")
	t.Setenv("NATCATTY_GDRIVE_CLIENT_ID", "google-mobile-client")

	dir := t.TempDir()
	database, err := store.Open(ctx, filepath.Join(dir, "natcatty.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	secrets, err := store.OpenSecretStore(ctx, database, newPhoneTestKeys())
	if err != nil {
		t.Fatalf("open secret store: %v", err)
	}
	preferences := apppreferences.NewService(
		store.OpenPreferences(filepath.Join(dir, "preferences.json")))
	if err := preferences.Load(ctx); err != nil {
		t.Fatalf("load preferences: %v", err)
	}
	terminal := appterminalsettings.NewService(
		store.OpenTerminalSettings(filepath.Join(dir, "terminal-settings.json")))
	if err := terminal.Load(ctx); err != nil {
		t.Fatalf("load terminal settings: %v", err)
	}
	tokens := &phoneSyncTokens{}
	synced, err := newPhoneSync(ctx, database, secrets, preferences, terminal, dir,
		withPhoneSyncConfigs(adaptersyncstore.OpenConfig(filepath.Join(dir, "cloud-sync.json"))),
		withPhoneSyncTokens(tokens),
		withPhoneSyncProvider(appsync.ProviderGoogleDrive),
		withPhoneSyncDevice("phone-device", "phone", "test"),
		withPhoneSyncRedirectURI(googleReversedClientRedirectURI),
		withPhoneSyncBrowserOpener(func(raw string) error {
			authURL <- raw
			return nil
		}),
	)
	if err != nil {
		t.Fatalf("newPhoneSync with a Google mobile redirect: %v", err)
	}
	t.Cleanup(synced.Stop)

	// The app's own scheme still claims another path; a foreign scheme falls
	// through to the Host deep-link router.
	if !synced.HandleOpenURL(googleReversedClientRedirectURI + "/elsewhere") {
		t.Fatal("a URL of the OAuth scheme must be claimed even for another path")
	}
	if synced.HandleOpenURL("ssh://host.example") {
		t.Fatal("an ssh:// link was claimed by the OAuth router")
	}

	if err := synced.StartBrowserConnect(syncMasterPassword, appsync.ProviderGoogleDrive); err != nil {
		t.Fatalf("start the Google connect: %v", err)
	}
	var raw string
	select {
	case raw = <-authURL:
	case <-time.After(5 * time.Second):
		t.Fatal("the system browser was never opened")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse the consent URL: %v", err)
	}
	if got := parsed.Query().Get("redirect_uri"); got != googleReversedClientRedirectURI {
		t.Fatalf("consent redirect_uri = %q, want the reversed-client-id URI", got)
	}
	state := parsed.Query().Get("state")
	if state == "" {
		t.Fatal("the consent URL carried no state")
	}
	callback := googleReversedClientRedirectURI + "?code=google-code&state=" + url.QueryEscape(state)
	if !synced.HandleOpenURL(callback) {
		t.Fatalf("the OS-delivered callback %q was not claimed", callback)
	}
	snapshot := waitForBrowserSync(t, synced, "the connected Google profile",
		func(s appsync.Snapshot) bool { return s.Connected && s.Status == appsync.StatusIdle })
	if snapshot.Provider != appsync.ProviderGoogleDrive {
		t.Fatalf("provider = %q, want google", snapshot.Provider)
	}
	if snapshot.Login != "user@example.test" {
		t.Fatalf("login = %q, want the fake account", snapshot.Login)
	}
	token, err := tokens.Load(ctx)
	if err != nil {
		t.Fatalf("load the stored token: %v", err)
	}
	if token.AccessToken != "google-access" || token.Provider != appsync.ProviderGoogleDrive {
		t.Fatalf("stored token provider/credential = %q/%q, want google/google-access",
			token.Provider, token.AccessToken)
	}
}
