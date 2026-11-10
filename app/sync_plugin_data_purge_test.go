package app

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/mslxl/natcatty/adapters/store"
	adaptersyncvault "github.com/mslxl/natcatty/adapters/syncvault"
	appsync "github.com/mslxl/natcatty/application/sync"
	"github.com/mslxl/natcatty/domain/model/plugindata"
	domainsync "github.com/mslxl/natcatty/domain/sync"
)

// vaultInteropBackups is the mandatory restore-point port for convergent
// migration; this test only exercises the publish/converge path.
type vaultInteropBackups struct{}

func (vaultInteropBackups) CreateProtective(context.Context) error { return nil }

func (vaultInteropBackups) List(context.Context) ([]appsync.BackupInfo, error) {
	return nil, nil
}

func (vaultInteropBackups) Restore(context.Context, string) error { return nil }

// TestV2SyncAfterPurgingTheLastPluginDataNamespace is the N2 integration:
// PurgePluginData removes a plugin's last namespace from a real Vault, then a
// v2 convergent sync runs over the rewritten record. The purge must leave the
// canonical absent property so the local payload and the CRDT state converge.
func TestV2SyncAfterPurgingTheLastPluginDataNamespace(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatalf("open the Vault database: %v", err)
	}
	defer database.Close()
	secrets, err := store.OpenSecretStore(ctx, database,
		&syncRefreshKeyStore{values: map[string][]byte{}},
		store.WithArgon2Params(1, 1024, 1))
	if err != nil {
		t.Fatalf("open the field-secret store: %v", err)
	}
	defer secrets.Close()

	local := syncRefreshHost("host-local", "local", time.UnixMilli(1000))
	local.PluginData = plugindata.Map{"acme.one": {
		SchemaVersion: 1,
		Fields:        map[string]json.RawMessage{"name": json.RawMessage(`"acme"`)},
	}}
	if err := database.Save(ctx, local); err != nil {
		t.Fatalf("save the local Host: %v", err)
	}
	if err := database.Save(ctx,
		syncRefreshHost("host-two", "two", time.UnixMilli(1000))); err != nil {
		t.Fatalf("save the sibling Host: %v", err)
	}

	remote := &syncRefreshRemote{}
	configs := &syncRefreshConfigs{config: appsync.Config{
		Provider: "github", Connected: true, Login: "octocat",
		Strategy: domainsync.StrategySmartMerge,
	}}
	tokens := &syncRefreshTokens{token: domainsync.Token{AccessToken: "token"}, set: true}
	service := appsync.NewService(remote, adaptersyncvault.New(database, secrets),
		configs, tokens,
		appsync.WithClock(func() time.Time { return time.UnixMilli(5000) }),
		appsync.WithDevice("device-purge", "Purge Device", "test"),
		appsync.WithKDFIterations(syncPluginDataKDFIterations),
		appsync.WithBackups(vaultInteropBackups{}),
	)
	if err := service.Load(ctx); err != nil {
		t.Fatalf("load the sync profile: %v", err)
	}
	defer service.Stop()
	if err := service.SetPassword(syncRefreshPassword); err != nil {
		t.Fatalf("set the master password: %v", err)
	}
	if _, err := service.SetConvergentEnabled(ctx, true); err != nil {
		t.Fatalf("enable convergent sync: %v", err)
	}

	if err := database.PurgePluginData(ctx, "acme.one"); err != nil {
		t.Fatalf("PurgePluginData: %v", err)
	}
	host, err := database.Get(ctx, "host-local")
	if err != nil || len(host.PluginData) != 0 {
		t.Fatalf("purged Host = %+v, %v", host.PluginData, err)
	}

	result, err := service.Sync(ctx, appsync.TriggerManual)
	if err != nil {
		t.Fatalf("a v2 sync after purging the last namespace failed: %v", err)
	}
	if result.Writes == 0 {
		t.Fatal("the purge was not published to the v2 replica")
	}
	if _, err := database.Get(ctx, "host-two"); err != nil {
		t.Fatalf("valid sibling Host was lost: %v", err)
	}
	if host, err = database.Get(ctx, "host-local"); err != nil ||
		len(host.PluginData) != 0 {
		t.Fatalf("sync restored the purged namespace: %+v, %v", host.PluginData, err)
	}

	repeated, err := service.Sync(ctx, appsync.TriggerManual)
	if err != nil {
		t.Fatalf("a repeated v2 sync after purge failed: %v", err)
	}
	if repeated.Writes != 0 {
		t.Fatalf("a repeated v2 sync after purge wrote %d times", repeated.Writes)
	}
}
