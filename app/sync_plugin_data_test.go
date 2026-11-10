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
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

const syncPluginDataKDFIterations = 1000

func rawCloudHost(t *testing.T, host domainvault.Host, pluginData string) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(host)
	if err != nil {
		t.Fatalf("marshal Host: %v", err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &top); err != nil {
		t.Fatalf("decode Host: %v", err)
	}
	if pluginData != "" {
		top["pluginData"] = json.RawMessage(pluginData)
	}
	out, err := domainsync.MarshalJSON(top)
	if err != nil {
		t.Fatalf("encode Host: %v", err)
	}
	return out
}

func malformedCloudEnvelope(t *testing.T, hosts []json.RawMessage,
	updatedAt int64) []byte {

	t.Helper()
	payload := map[string]any{"hosts": hosts, "syncedAt": updatedAt}
	envelope, err := domainsync.EncryptPayload(payload, syncRefreshPassword,
		domainsync.EncryptOptions{
			Version: 1, UpdatedAt: updatedAt, DeviceID: "peer-device",
			DeviceName: "Peer Device", AppVersion: "test",
			KDFIterations: syncPluginDataKDFIterations,
		})
	if err != nil {
		t.Fatalf("EncryptPayload: %v", err)
	}
	content, err := domainsync.MarshalFile(envelope)
	if err != nil {
		t.Fatalf("MarshalFile: %v", err)
	}
	return content
}

// TestSyncResultReportsQuarantinesAppliedByTheStore drives the real sync stack:
// a cloud copy carries one malformed namespace on an otherwise valid Host. The
// sync must not fail with ErrRemoteCorrupt; it must apply the valid sibling
// namespaces, persist the quarantine through ApplySync, and report that stored
// quarantine in the result the UI renders.
func TestSyncResultReportsQuarantinesAppliedByTheStore(t *testing.T) {
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
	if err := database.Save(ctx, local); err != nil {
		t.Fatalf("save the local Host: %v", err)
	}

	remote := &syncRefreshRemote{}
	configs := &syncRefreshConfigs{config: appsync.Config{
		Provider: "github", Connected: true, Login: "octocat",
		Strategy: domainsync.StrategyNewest,
	}}
	tokens := &syncRefreshTokens{token: domainsync.Token{AccessToken: "token"}, set: true}
	service := appsync.NewService(remote, adaptersyncvault.New(database, secrets), configs, tokens,
		appsync.WithClock(func() time.Time { return time.UnixMilli(5000) }),
		appsync.WithDevice("device-quarantine", "Quarantine Device", "test"),
		appsync.WithKDFIterations(syncPluginDataKDFIterations),
	)
	if err := service.Load(ctx); err != nil {
		t.Fatalf("load the sync profile: %v", err)
	}
	defer service.Stop()
	if err := service.SetPassword(syncRefreshPassword); err != nil {
		t.Fatalf("set the master password: %v", err)
	}
	if _, err := service.Sync(ctx, appsync.TriggerManual); err != nil {
		t.Fatalf("initial upload: %v", err)
	}

	valid := syncRefreshHost("host-cloud", "cloud", time.UnixMilli(9000))
	source := valid
	source.PluginData = plugindata.Map{"acme.good": {
		SchemaVersion: 1,
		Fields:        map[string]json.RawMessage{"name": json.RawMessage(`"ok"`)},
	}}
	hosts := []json.RawMessage{
		rawCloudHost(t, source, `{"acme.good":{"schemaVersion":1,"fields":{"name":"ok"}}}`),
		rawCloudHost(t, syncRefreshHost("host-bad", "bad", time.UnixMilli(9000)),
			`{"acme.bad":null,"acme.good":{"schemaVersion":1,"fields":{"name":"ok"}}}`),
	}
	remote.mu.Lock()
	remote.file = appsync.RemoteFile{
		ID: "gist-refresh", ETag: "etag-malformed", UpdatedAt: 9000,
		Content: malformedCloudEnvelope(t, hosts, 9000),
	}
	remote.exists = true
	remote.mu.Unlock()

	result, err := service.Sync(ctx, appsync.TriggerManual)
	if err != nil {
		t.Fatalf("a malformed namespace aborted the whole sync: %v", err)
	}
	if !result.Downloaded {
		t.Fatalf("result = %+v, want a download", result)
	}
	diagnostics := result.PluginDataDiagnostics
	found := false
	for _, diagnostic := range diagnostics {
		if diagnostic.RecordKind == plugindata.RecordHost &&
			diagnostic.RecordID == "host-bad" &&
			diagnostic.PluginID == "acme.bad" &&
			diagnostic.Reason == plugindata.ReasonNamespace &&
			diagnostic.State == plugindata.StateQuarantined {
			found = true
		}
	}
	if !found {
		t.Fatalf("sync result does not report the applied quarantine: %+v", diagnostics)
	}

	bad, err := database.Get(ctx, "host-bad")
	if err != nil {
		t.Fatalf("Get host-bad: %v", err)
	}
	if string(bad.PluginData["acme.good"].Fields["name"]) != `"ok"` {
		t.Fatalf("valid sibling namespace was not applied: %+v", bad.PluginData)
	}
	if _, ok := bad.PluginData["acme.bad"]; ok {
		t.Fatalf("malformed namespace was stored: %+v", bad.PluginData)
	}
	if _, err := database.Get(ctx, "host-cloud"); err != nil {
		t.Fatalf("valid sibling record was not applied: %v", err)
	}
}
