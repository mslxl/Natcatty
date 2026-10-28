package sync

import (
	"context"
	"errors"
	"testing"
	"time"

	domainsync "github.com/mslxl/natcatty/domain/sync"
)

// oneDriveService builds a loaded, unlocked service whose active provider is
// OneDrive. The browser opener is wired so the service can start a fresh
// authorization.
func oneDriveService(t *testing.T, remote *browserRemote, tokens *memoryTokens, configs *memoryConfigs) *Service {
	t.Helper()
	service := NewService(remote, &fakeVault{payload: payloadWithHost("local", 1000)}, configs, tokens,
		WithKDFIterations(testKDFIterations),
		WithProvider(ProviderOneDrive),
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

func oneDriveRemote() *browserRemote {
	remote := newBrowserRemote()
	remote.grant = Grant{ID: "onedrive-id-1", Login: "one@example.test"}
	remote.token = domainsync.Token{
		AccessToken: "onedrive-token", RefreshToken: "onedrive-refresh",
		TokenType: "Bearer", Provider: ProviderOneDrive,
	}
	return remote
}

func oneDriveConfig() Config {
	return Config{
		Provider: ProviderOneDrive, Connected: true, Login: "one@example.test",
		AccountID: "onedrive-id-1", LocalSignature: "anchor", RemoteID: "drive-item-1",
		RemoteETag: "etag-1", RemoteUpdatedAt: 11, LastSyncedAt: 11,
		Strategy: domainsync.StrategyNewest,
	}
}

func TestOneDriveNormalizeProviderKeepsItsProfile(t *testing.T) {
	if got := NormalizeProvider(ProviderOneDrive); got != ProviderOneDrive {
		t.Fatalf("NormalizeProvider(%q) = %q", ProviderOneDrive, got)
	}
}

func TestOneDriveAuthorizationConnectsAndStoresTheProvider(t *testing.T) {
	remote := oneDriveRemote()
	configs := &memoryConfigs{config: Config{Strategy: domainsync.StrategyNewest}}
	tokens := &memoryTokens{}
	service := oneDriveService(t, remote, tokens, configs)

	if err := service.Connect(context.Background(), "secret", nil); err != nil {
		t.Fatal(err)
	}
	snapshot := service.Snapshot()
	if !snapshot.Connected || snapshot.Login != "one@example.test" || snapshot.Provider != ProviderOneDrive {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	stored, err := tokens.Load(context.Background())
	if err != nil || stored.AccessToken != "onedrive-token" || stored.Provider != ProviderOneDrive {
		t.Fatalf("stored token = %+v, %v", stored, err)
	}
	if configs.config.AccountID != "onedrive-id-1" || configs.config.Provider != ProviderOneDrive {
		t.Fatalf("config = %+v", configs.config)
	}
}

func TestOneDriveSameAccountKeepsTheMergeAnchor(t *testing.T) {
	remote := oneDriveRemote()
	// A display-name change must not rotate the anchor: identity is the
	// immutable Graph user id.
	remote.grant = Grant{ID: "onedrive-id-1", Login: "one-renamed@example.test"}
	configs := &memoryConfigs{config: oneDriveConfig()}
	tokens := &memoryTokens{set: true, token: domainsync.Token{
		AccessToken: "first-account", Provider: ProviderOneDrive,
	}}
	service := oneDriveService(t, remote, tokens, configs)

	if err := service.Connect(context.Background(), "secret", nil); err != nil {
		t.Fatal(err)
	}
	config := configs.config
	if config.AccountID != "onedrive-id-1" || config.Login != "one-renamed@example.test" {
		t.Fatalf("account = %q %q", config.AccountID, config.Login)
	}
	if config.LocalSignature != "anchor" || config.RemoteID != "drive-item-1" ||
		config.RemoteETag != "etag-1" || config.RemoteUpdatedAt != 11 || config.LastSyncedAt != 11 {
		t.Fatalf("the same account rotated the anchor: %+v", config)
	}
}

func TestOneDriveDifferentAccountClearsTheMergeAnchor(t *testing.T) {
	remote := oneDriveRemote()
	remote.grant = Grant{ID: "onedrive-id-2", Login: "two@example.test"}
	remote.token = domainsync.Token{AccessToken: "second-account", RefreshToken: "r2", Provider: ProviderOneDrive}
	configs := &memoryConfigs{config: oneDriveConfig()}
	tokens := &memoryTokens{set: true, token: domainsync.Token{
		AccessToken: "first-account", Provider: ProviderOneDrive,
	}}
	service := oneDriveService(t, remote, tokens, configs)

	if err := service.Connect(context.Background(), "secret", nil); err != nil {
		t.Fatal(err)
	}
	config := configs.config
	if config.AccountID != "onedrive-id-2" || config.Login != "two@example.test" {
		t.Fatalf("account = %q %q", config.AccountID, config.Login)
	}
	if config.LocalSignature != "" || config.RemoteID != "" || config.RemoteETag != "" ||
		config.RemoteUpdatedAt != 0 || config.LastSyncedAt != 0 {
		t.Fatalf("the new account kept the previous anchor: %+v", config)
	}
}

func TestOneDriveReauthorizationResumesSync(t *testing.T) {
	remote := oneDriveRemote()
	remote.refreshErr = ErrReauthorizationRequired
	remote.token = domainsync.Token{
		AccessToken: "reauth-token", RefreshToken: "reauth-refresh", Provider: ProviderOneDrive,
	}
	tokens := &memoryTokens{set: true, token: domainsync.Token{
		AccessToken: "expired", RefreshToken: "dead-refresh",
		ExpiresAt: time.UnixMilli(4000).UnixMilli(), Provider: ProviderOneDrive,
	}}
	configs := &memoryConfigs{config: oneDriveConfig()}
	service := oneDriveService(t, remote, tokens, configs)

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
	config := configs.config
	if config.AccountID != "onedrive-id-1" || config.Login != "one@example.test" {
		t.Fatalf("reauthorization changed the account: %+v", config)
	}
}

func TestOneDriveReauthorizationOfTheSameAccountKeepsTheAnchor(t *testing.T) {
	remote := oneDriveRemote()
	remote.refreshErr = ErrReauthorizationRequired
	// A display-name change must not rotate the anchor: identity is the
	// immutable Graph user id.
	remote.grant = Grant{ID: "onedrive-id-1", Login: "one-renamed@example.test"}
	remote.token = domainsync.Token{
		AccessToken: "reauth-token", RefreshToken: "reauth-refresh", Provider: ProviderOneDrive,
	}
	tokens := &memoryTokens{set: true, token: domainsync.Token{
		AccessToken: "expired", RefreshToken: "dead-refresh",
		ExpiresAt: time.UnixMilli(4000).UnixMilli(), Provider: ProviderOneDrive,
	}}
	configs := &memoryConfigs{config: oneDriveConfig()}
	service := oneDriveService(t, remote, tokens, configs)

	if _, err := service.ensureFreshToken(context.Background()); err != nil {
		t.Fatal(err)
	}
	config := configs.config
	if config.AccountID != "onedrive-id-1" || config.Login != "one-renamed@example.test" {
		t.Fatalf("account = %q %q", config.AccountID, config.Login)
	}
	if config.LocalSignature != "anchor" || config.RemoteID != "drive-item-1" ||
		config.RemoteETag != "etag-1" || config.RemoteUpdatedAt != 11 || config.LastSyncedAt != 11 {
		t.Fatalf("reauthorizing the same account rotated the anchor: %+v", config)
	}
}

func TestOneDriveReauthorizationOfADifferentAccountClearsTheAnchor(t *testing.T) {
	remote := oneDriveRemote()
	remote.refreshErr = ErrReauthorizationRequired
	remote.grant = Grant{ID: "onedrive-id-2", Login: "two@example.test"}
	remote.token = domainsync.Token{
		AccessToken: "reauth-token", RefreshToken: "reauth-refresh", Provider: ProviderOneDrive,
	}
	tokens := &memoryTokens{set: true, token: domainsync.Token{
		AccessToken: "expired", RefreshToken: "dead-refresh",
		ExpiresAt: time.UnixMilli(4000).UnixMilli(), Provider: ProviderOneDrive,
	}}
	configs := &memoryConfigs{config: oneDriveConfig()}
	service := oneDriveService(t, remote, tokens, configs)

	if _, err := service.ensureFreshToken(context.Background()); err != nil {
		t.Fatal(err)
	}
	config := configs.config
	if config.AccountID != "onedrive-id-2" || config.Login != "two@example.test" {
		t.Fatalf("account = %q %q", config.AccountID, config.Login)
	}
	if config.LocalSignature != "" || config.RemoteID != "" || config.RemoteETag != "" ||
		config.RemoteUpdatedAt != 0 || config.LastSyncedAt != 0 {
		t.Fatalf("authorizing a different account kept the old anchor: %+v", config)
	}
}

func TestOneDriveFailedReauthorizationPreservesTheAnchorAndCredential(t *testing.T) {
	cases := []struct {
		name   string
		failed error
	}{
		{name: "denied", failed: ErrAuthorizationDenied},
		{name: "cancelled", failed: ErrAuthorizationTimeout},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			remote := oneDriveRemote()
			remote.refreshErr = ErrReauthorizationRequired
			remote.authorize = test.failed
			original := domainsync.Token{
				AccessToken: "expired", RefreshToken: "dead-refresh",
				ExpiresAt: time.UnixMilli(4000).UnixMilli(), Provider: ProviderOneDrive,
			}
			tokens := &memoryTokens{set: true, token: original}
			configs := &memoryConfigs{config: oneDriveConfig()}
			service := oneDriveService(t, remote, tokens, configs)

			_, err := service.Sync(context.Background(), TriggerManual)
			if !errors.Is(err, ErrReauthorizationRequired) || !errors.Is(err, test.failed) {
				t.Fatalf("error = %v, want the reauthorization and %v chain", err, test.failed)
			}
			if problem := service.Snapshot().Problem; problem != ProblemReauthorization {
				t.Fatalf("problem = %q, want %q", problem, ProblemReauthorization)
			}
			config := configs.config
			if config.AccountID != "onedrive-id-1" || config.Login != "one@example.test" {
				t.Fatalf("a failed authorization changed the account: %+v", config)
			}
			if config.LocalSignature != "anchor" || config.RemoteID != "drive-item-1" ||
				config.RemoteETag != "etag-1" || config.RemoteUpdatedAt != 11 || config.LastSyncedAt != 11 {
				t.Fatalf("a failed authorization rotated the anchor: %+v", config)
			}
			stored, loadErr := tokens.Load(context.Background())
			if loadErr != nil || stored.AccessToken != original.AccessToken ||
				stored.RefreshToken != original.RefreshToken {
				t.Fatalf("a failed authorization overwrote the credential: %+v, %v", stored, loadErr)
			}
		})
	}
}

func TestOneDrivePersistentUnauthorizedRefreshesOnceAndStops(t *testing.T) {
	remote := oneDriveRemote()
	remote.readErrs = []error{ErrUnauthorized, ErrUnauthorized}
	tokens := &memoryTokens{set: true, token: domainsync.Token{
		AccessToken: "stale", RefreshToken: "old-refresh", Provider: ProviderOneDrive,
	}}
	configs := &memoryConfigs{config: oneDriveConfig()}
	service := oneDriveService(t, remote, tokens, configs)

	_, err := service.Sync(context.Background(), TriggerManual)
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("error = %v, want ErrUnauthorized after the single retry", err)
	}
	if got := remote.refreshCount(); got != 1 {
		t.Fatalf("refresh calls = %d, want exactly one", got)
	}
	if got := remote.calls; got != 2 {
		t.Fatalf("read calls = %d, want the original and one retry", got)
	}
}
