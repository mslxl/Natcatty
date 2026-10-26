package sync

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	domainsync "github.com/mslxl/natcatty/domain/sync"
)

// browserRemote is a browser-authorized provider. It answers the pure remote
// port and the two optional capabilities the service uses for Google Drive.
type browserRemote struct {
	mu sync.Mutex

	token     domainsync.Token
	grant     Grant
	authorize error

	readErr    error
	readErrs   []error
	readToken  string
	writeToken string
	calls      int

	refreshErr  error
	refreshNext domainsync.Token
	refreshes   int

	file   RemoteFile
	exists bool
}

func newBrowserRemote() *browserRemote {
	return &browserRemote{
		token: domainsync.Token{AccessToken: "browser-token", RefreshToken: "browser-refresh", TokenType: "Bearer"},
		grant: Grant{ID: "google-id-1", Login: "one@example.test"},
	}
}

func (r *browserRemote) Authorize(_ context.Context, open func(string) error) (domainsync.Token, Grant, error) {
	if r.authorize != nil {
		return domainsync.Token{}, Grant{}, r.authorize
	}
	if err := open("https://accounts.example.test/authorize?state=abc"); err != nil {
		return domainsync.Token{}, Grant{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.token, r.grant, nil
}

func (r *browserRemote) User(context.Context, domainsync.Token) (Grant, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.grant, nil
}

func (r *browserRemote) Read(_ context.Context, token domainsync.Token) (RemoteFile, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	r.readToken = token.AccessToken
	if len(r.readErrs) > 0 {
		err := r.readErrs[0]
		r.readErrs = r.readErrs[1:]
		if err != nil {
			return RemoteFile{}, false, err
		}
	}
	if r.readErr != nil {
		return RemoteFile{}, false, r.readErr
	}
	return r.file, r.exists, nil
}

func (r *browserRemote) Write(_ context.Context, token domainsync.Token, _ RemoteFile, content []byte) (RemoteFile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	r.writeToken = token.AccessToken
	return RemoteFile{ID: "drive-file", ETag: "modified-1", Content: content}, nil
}

func (r *browserRemote) Refresh(_ context.Context, _ domainsync.Token) (domainsync.Token, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.refreshes++
	if r.refreshErr != nil {
		return domainsync.Token{}, r.refreshErr
	}
	refreshed := r.refreshNext
	if refreshed.AccessToken == "" {
		refreshed = domainsync.Token{
			AccessToken:  "refreshed-access",
			RefreshToken: "rotated-refresh",
			TokenType:    "Bearer",
			ExpiresAt:    time.Now().Add(time.Hour).UnixMilli(),
		}
	}
	return refreshed, nil
}

func (r *browserRemote) refreshCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.refreshes
}

func (r *browserRemote) writeTokenValue() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.writeToken
}

func browserService(t *testing.T, remote *browserRemote, tokens *memoryTokens, configs *memoryConfigs) *Service {
	t.Helper()
	vault := &fakeVault{payload: payloadWithHost("local", 1000)}
	service := NewService(remote, vault, configs, tokens,
		WithProvider(ProviderGoogleDrive),
		WithDevice("device-1", "Test Device", "1.0"),
		WithClock(func() time.Time { return time.UnixMilli(5000) }),
		WithBrowserOpener(func(string) error { return nil }),
	)
	if err := service.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := service.SetPassword("secret"); err != nil {
		t.Fatal(err)
	}
	return service
}

func TestBrowserAuthorizationConnectsAndStoresTheProvider(t *testing.T) {
	remote := newBrowserRemote()
	configs := &memoryConfigs{config: Config{Strategy: domainsync.StrategyNewest}}
	tokens := &memoryTokens{}
	service := browserService(t, remote, tokens, configs)

	var devices []DeviceCode
	if err := service.Connect(context.Background(), "secret", func(code DeviceCode) {
		devices = append(devices, code)
	}); err != nil {
		t.Fatal(err)
	}
	snapshot := service.Snapshot()
	if !snapshot.Connected || snapshot.Login != "one@example.test" || snapshot.Provider != ProviderGoogleDrive {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if len(devices) != 0 {
		t.Fatalf("browser authorization reported device codes: %+v", devices)
	}
	stored, err := tokens.Load(context.Background())
	if err != nil || stored.AccessToken != "browser-token" || stored.Provider != ProviderGoogleDrive {
		t.Fatalf("stored token = %+v, %v", stored, err)
	}
	if configs.config.AccountID != "google-id-1" || configs.config.Provider != ProviderGoogleDrive {
		t.Fatalf("config = %+v", configs.config)
	}
}

func TestExpiredTokenRefreshesOnceAndPersistsRotation(t *testing.T) {
	remote := newBrowserRemote()
	tokens := &memoryTokens{set: true, token: domainsync.Token{
		AccessToken:  "expired",
		RefreshToken: "old-refresh",
		ExpiresAt:    time.UnixMilli(4000).UnixMilli(),
	}}
	configs := &memoryConfigs{config: Config{
		Provider: ProviderGoogleDrive, Connected: true, Login: "one@example.test",
		AccountID: "google-id-1", Strategy: domainsync.StrategyNewest,
	}}
	service := browserService(t, remote, tokens, configs)

	if _, err := service.Sync(context.Background(), TriggerManual); err != nil {
		t.Fatal(err)
	}
	if remote.refreshCount() != 1 {
		t.Fatalf("refresh calls = %d, want 1", remote.refreshCount())
	}
	if got := remote.writeTokenValue(); got != "refreshed-access" {
		t.Fatalf("operation used token %q, want the refreshed one", got)
	}
	stored, err := tokens.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stored.AccessToken != "refreshed-access" || stored.RefreshToken != "rotated-refresh" {
		t.Fatalf("persisted token = %+v, want the rotated pair", stored)
	}
}

func TestUnauthorizedReadRefreshesAndRetries(t *testing.T) {
	remote := newBrowserRemote()
	remote.readErrs = []error{ErrUnauthorized}
	tokens := &memoryTokens{set: true, token: domainsync.Token{
		AccessToken: "stale", RefreshToken: "old-refresh", Provider: ProviderGoogleDrive,
	}}
	configs := &memoryConfigs{config: Config{
		Provider: ProviderGoogleDrive, Connected: true, Login: "one@example.test",
		AccountID: "google-id-1", Strategy: domainsync.StrategyNewest,
	}}
	service := browserService(t, remote, tokens, configs)

	if _, err := service.Sync(context.Background(), TriggerManual); err != nil {
		t.Fatal(err)
	}
	if remote.refreshCount() != 1 {
		t.Fatalf("refresh calls = %d, want 1 after a 401", remote.refreshCount())
	}
	if got := remote.writeTokenValue(); got != "refreshed-access" {
		t.Fatalf("write used token %q, want the refreshed one", got)
	}
}

func TestInvalidGrantReauthorizesAndResumesSync(t *testing.T) {
	remote := newBrowserRemote()
	remote.refreshErr = ErrReauthorizationRequired
	remote.token = domainsync.Token{AccessToken: "reauth-token", RefreshToken: "reauth-refresh", Provider: ProviderGoogleDrive}
	tokens := &memoryTokens{set: true, token: domainsync.Token{
		AccessToken: "expired", RefreshToken: "dead-refresh",
		ExpiresAt: time.UnixMilli(4000).UnixMilli(), Provider: ProviderGoogleDrive,
	}}
	configs := &memoryConfigs{config: Config{
		Provider: ProviderGoogleDrive, Connected: true, Login: "one@example.test",
		AccountID: "google-id-1", LocalSignature: "anchor", RemoteID: "drive-1",
		RemoteETag: "etag", RemoteUpdatedAt: 7, LastSyncedAt: 7,
		Strategy: domainsync.StrategyNewest,
	}}
	service := browserService(t, remote, tokens, configs)

	result, err := service.Sync(context.Background(), TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Uploaded {
		t.Fatalf("result = %+v, want the resumed sync to upload", result)
	}
	if got := remote.writeTokenValue(); got != "reauth-token" {
		t.Fatalf("operation used token %q, want the reauthorized token", got)
	}
	stored, err := tokens.Load(context.Background())
	if err != nil || stored.AccessToken != "reauth-token" || stored.RefreshToken != "reauth-refresh" {
		t.Fatalf("stored token = %+v, %v", stored, err)
	}
	if configs.config.AccountID != "google-id-1" || configs.config.Login != "one@example.test" {
		t.Fatalf("reauthorization changed the account: %+v", configs.config)
	}
}

func TestReauthorizationFailureSurfacesAnActionableProblem(t *testing.T) {
	remote := newBrowserRemote()
	remote.refreshErr = ErrReauthorizationRequired
	remote.authorize = ErrAuthorizationDenied
	tokens := &memoryTokens{set: true, token: domainsync.Token{
		AccessToken: "expired", RefreshToken: "dead-refresh",
		ExpiresAt: time.UnixMilli(4000).UnixMilli(), Provider: ProviderGoogleDrive,
	}}
	configs := &memoryConfigs{config: Config{
		Provider: ProviderGoogleDrive, Connected: true, Login: "one@example.test",
		AccountID: "google-id-1", Strategy: domainsync.StrategyNewest,
	}}
	service := browserService(t, remote, tokens, configs)

	_, err := service.Sync(context.Background(), TriggerManual)
	if !errors.Is(err, ErrReauthorizationRequired) {
		t.Fatalf("error = %v, want ErrReauthorizationRequired", err)
	}
	if !errors.Is(err, ErrAuthorizationDenied) {
		t.Fatalf("error = %v, want the denial chained", err)
	}
	if problem := service.Snapshot().Problem; problem != ProblemReauthorization {
		t.Fatalf("problem = %q, want %q", problem, ProblemReauthorization)
	}
}

func TestDifferentAccountClearsTheMergeAnchor(t *testing.T) {
	remote := newBrowserRemote()
	remote.grant = Grant{ID: "google-id-2", Login: "two@example.test"}
	remote.token = domainsync.Token{AccessToken: "second-account", RefreshToken: "r2", Provider: ProviderGoogleDrive}
	configs := &memoryConfigs{config: Config{
		Provider: ProviderGoogleDrive, Connected: true, Login: "one@example.test",
		AccountID: "google-id-1", LocalSignature: "old-anchor", RemoteID: "drive-1",
		RemoteETag: "etag-1", RemoteUpdatedAt: 11, LastSyncedAt: 11,
		Strategy: domainsync.StrategyNewest,
	}}
	tokens := &memoryTokens{set: true, token: domainsync.Token{AccessToken: "first-account", Provider: ProviderGoogleDrive}}
	service := browserService(t, remote, tokens, configs)

	if err := service.Connect(context.Background(), "secret", nil); err != nil {
		t.Fatal(err)
	}
	config := configs.config
	if config.AccountID != "google-id-2" || config.Login != "two@example.test" {
		t.Fatalf("account = %q %q", config.AccountID, config.Login)
	}
	if config.LocalSignature != "" || config.RemoteID != "" || config.RemoteETag != "" ||
		config.RemoteUpdatedAt != 0 || config.LastSyncedAt != 0 {
		t.Fatalf("anchor was not cleared: %+v", config)
	}
}

func TestStableAccountIDKeepsTheAnchorAcrossALoginRename(t *testing.T) {
	remote := newBrowserRemote()
	remote.grant = Grant{ID: "42", Login: "octocat-renamed"}
	configs := &memoryConfigs{config: Config{
		Provider: ProviderGitHub, Connected: true, Login: "octocat",
		AccountID: "42", LocalSignature: "old-anchor", RemoteID: "gist-1",
		RemoteETag: "etag-1", RemoteUpdatedAt: 11, LastSyncedAt: 11,
		Strategy: domainsync.StrategyNewest,
	}}
	tokens := &memoryTokens{set: true, token: domainsync.Token{AccessToken: "first-account", Provider: ProviderGitHub}}
	service := NewService(remote, &fakeVault{}, configs, tokens,
		WithProvider(ProviderGitHub),
		WithDevice("device-1", "Test Device", "1.0"),
		WithBrowserOpener(func(string) error { return nil }))
	if err := service.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := service.Connect(context.Background(), "secret", nil); err != nil {
		t.Fatal(err)
	}
	config := configs.config
	if config.AccountID != "42" || config.Login != "octocat-renamed" {
		t.Fatalf("account = %q %q", config.AccountID, config.Login)
	}
	if config.LocalSignature != "old-anchor" || config.RemoteID != "gist-1" ||
		config.RemoteETag != "etag-1" || config.RemoteUpdatedAt != 11 || config.LastSyncedAt != 11 {
		t.Fatalf("a login rename rotated the anchor: %+v", config)
	}
}

func TestSameAccountKeepsTheMergeAnchor(t *testing.T) {
	remote := newBrowserRemote()
	remote.grant = Grant{ID: "google-id-1", Login: "one@example.test"}
	configs := &memoryConfigs{config: Config{
		Provider: ProviderGoogleDrive, Connected: true, Login: "one@example.test",
		AccountID: "google-id-1", LocalSignature: "old-anchor", RemoteID: "drive-1",
		RemoteETag: "etag-1", RemoteUpdatedAt: 11, LastSyncedAt: 11,
		Strategy: domainsync.StrategyNewest,
	}}
	tokens := &memoryTokens{set: true, token: domainsync.Token{AccessToken: "first-account", Provider: ProviderGoogleDrive}}
	service := browserService(t, remote, tokens, configs)

	if err := service.Connect(context.Background(), "secret", nil); err != nil {
		t.Fatal(err)
	}
	config := configs.config
	if config.LocalSignature != "old-anchor" || config.RemoteID != "drive-1" ||
		config.RemoteETag != "etag-1" || config.RemoteUpdatedAt != 11 || config.LastSyncedAt != 11 {
		t.Fatalf("same account rotated the anchor: %+v", config)
	}
}

func TestRefreshIsSingleFlight(t *testing.T) {
	remote := newBrowserRemote()
	remote.mu.Lock()
	remote.refreshNext = domainsync.Token{
		AccessToken: "single-flight", RefreshToken: "rotated-refresh",
		ExpiresAt: time.Now().Add(time.Hour).UnixMilli(),
	}
	remote.mu.Unlock()
	tokens := &memoryTokens{set: true, token: domainsync.Token{
		AccessToken: "expired", RefreshToken: "old-refresh",
		ExpiresAt: time.UnixMilli(4000).UnixMilli(), Provider: ProviderGoogleDrive,
	}}
	configs := &memoryConfigs{config: Config{
		Provider: ProviderGoogleDrive, Connected: true, AccountID: "google-id-1",
		Strategy: domainsync.StrategyNewest,
	}}
	service := browserService(t, remote, tokens, configs)

	var wg sync.WaitGroup
	var failures atomic.Int32
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			token, err := service.ensureFreshToken(context.Background())
			if err != nil || token.AccessToken != "single-flight" {
				failures.Add(1)
			}
		}()
	}
	wg.Wait()
	if failures.Load() != 0 {
		t.Fatalf("%d concurrent refreshes did not see the rotated token", failures.Load())
	}
	if got := remote.refreshCount(); got != 1 {
		t.Fatalf("refresh calls = %d, want one", got)
	}
}

func TestWithProviderDoesNotLeaveThePrimaryRemoteUnderGitHub(t *testing.T) {
	google := newBrowserRemote()
	service := NewService(google, &fakeVault{}, &memoryConfigs{}, &memoryTokens{},
		WithProvider(ProviderGoogleDrive))
	if _, ok := service.remotes[ProviderGitHub]; ok {
		t.Fatalf("GitHub still points at the primary remote: %v", service.remotes)
	}
	if service.remotes[ProviderGoogleDrive] != google {
		t.Fatalf("Google Drive remote = %v, want the primary remote", service.remotes[ProviderGoogleDrive])
	}
}

func TestSwitchingBackToGitHubUsesTheRegisteredGitHubRemote(t *testing.T) {
	google := newBrowserRemote()
	github := newFakeRemote()
	configs := &memoryConfigs{}
	tokens := &memoryTokens{}
	service := NewService(google, &fakeVault{}, configs, tokens,
		WithRemote(ProviderGitHub, github),
		WithProvider(ProviderGoogleDrive),
		WithDevice("d", "n", "v"))
	if err := service.Load(context.Background()); err != nil {
		t.Fatal(err)
	}

	if err := service.SetProvider(context.Background(), ProviderGitHub); err != nil {
		t.Fatal(err)
	}
	service.mu.Lock()
	active := service.remote
	service.mu.Unlock()
	if active != github {
		t.Fatalf("GitHub switch used %v, want the registered GitHub remote", active)
	}

	if err := service.SetProvider(context.Background(), ProviderGoogleDrive); err != nil {
		t.Fatal(err)
	}
	service.mu.Lock()
	active = service.remote
	service.mu.Unlock()
	if active != google {
		t.Fatalf("Google Drive switch used %v, want the primary remote", active)
	}
}

func TestSetProviderResetsTheConnectionAndAnchor(t *testing.T) {
	github := newFakeRemote()
	google := newBrowserRemote()
	configs := &memoryConfigs{config: Config{
		Provider: ProviderGitHub, Connected: true, Login: "octocat",
		AccountID: "github-1", LocalSignature: "anchor", RemoteID: "gist-1",
		RemoteUpdatedAt: 4, LastSyncedAt: 4, Strategy: domainsync.StrategyNewest,
	}}
	tokens := &memoryTokens{set: true, token: domainsync.Token{AccessToken: "github-token"}}
	service := NewService(github, &fakeVault{}, configs, tokens,
		WithRemote(ProviderGoogleDrive, google),
		WithDevice("d", "n", "v"))
	if err := service.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := service.SetProvider(context.Background(), ProviderGoogleDrive); err != nil {
		t.Fatal(err)
	}
	snapshot := service.Snapshot()
	if snapshot.Connected || snapshot.Provider != ProviderGoogleDrive {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if configs.config.LocalSignature != "" || configs.config.RemoteID != "" ||
		configs.config.AccountID != "" || configs.config.LastSyncedAt != 0 {
		t.Fatalf("provider switch left the old anchor: %+v", configs.config)
	}
	if _, err := tokens.Load(context.Background()); !errors.Is(err, ErrNoToken) {
		t.Fatalf("token after switch = %v, want ErrNoToken", err)
	}
}

func TestAProviderTokenNeverLeaksToAnotherProvider(t *testing.T) {
	google := newBrowserRemote()
	configs := &memoryConfigs{config: Config{
		Provider: ProviderGoogleDrive, Connected: true, AccountID: "google-id-1",
		Strategy: domainsync.StrategyNewest,
	}}
	tokens := &memoryTokens{set: true, token: domainsync.Token{AccessToken: "github-token", Provider: ProviderGitHub}}
	service := NewService(newFakeRemote(), &fakeVault{}, configs, tokens,
		WithRemote(ProviderGoogleDrive, google))
	if err := service.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := service.SetPassword("secret"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Sync(context.Background(), TriggerManual); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("error = %v, want ErrNotConfigured for a foreign token", err)
	}
}
