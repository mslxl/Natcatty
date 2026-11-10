package app

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/mslxl/natcatty/adapters/store"
	appvault "github.com/mslxl/natcatty/application/vault"
	"github.com/mslxl/natcatty/domain/model"
	"github.com/mslxl/natcatty/domain/model/plugindata"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

func openRoundTripVault(t *testing.T, directory string) (*appvault.Service, *store.Store) {
	t.Helper()
	ctx := context.Background()
	database, err := store.Open(ctx, filepath.Join(directory, "vault.db"))
	if err != nil {
		t.Fatalf("open Vault database: %v", err)
	}
	keys := &writeTestKeyRing{values: map[string][]byte{}}
	secrets, err := store.OpenSecretStore(ctx, database, keys,
		store.WithArgon2Params(1, 1024, 1))
	if err != nil {
		_ = database.Close()
		t.Fatalf("open secret store: %v", err)
	}
	service := appvault.NewService(database,
		appvault.WithSecrets(secrets),
		appvault.WithKeys(database),
		appvault.WithIdentities(database),
		appvault.WithGroups(database),
		appvault.WithProxyProfiles(database),
		appvault.WithKnownHosts(database),
		appvault.WithSnippets(database),
		appvault.WithNotes(database),
		appvault.WithImportStore(database),
	)
	t.Cleanup(func() {
		secrets.Close()
		_ = database.Close()
	})
	return service, database
}

// TestVaultExportImportRoundTripPreservesHostPluginData drives the production
// Vault service and SQLite store for a JSON export and a fresh-database import.
// The Host's provider-owned namespace, its schema version, and an unknown
// property a provider added must survive byte-for-byte; a data migration that
// silently rewrites or drops pluginData fails here.
func TestVaultExportImportRoundTripPreservesHostPluginData(t *testing.T) {
	ctx := context.Background()
	source, sourceDatabase := openRoundTripVault(t, t.TempDir())
	host, err := source.Create(ctx, domainvault.Draft{
		Label: "Plugin Data Host", Hostname: "plugin.example.test", Port: 22,
		Username: "deploy", Protocol: domainvault.ProtocolSSH,
	})
	if err != nil {
		t.Fatalf("Create source host: %v", err)
	}
	namespace := plugindata.Namespace{
		SchemaVersion: 2,
		Fields: map[string]json.RawMessage{
			"profile": json.RawMessage(`{"label":"Prod <one>","count":3}`),
		},
		Extra: map[string]json.RawMessage{
			"future": json.RawMessage(`{"kept":true}`),
		},
	}
	if err := sourceDatabase.SetHostPluginData(ctx, string(host.ID),
		plugindata.Map{"acme.provider": namespace}); err != nil {
		t.Fatalf("seed Host pluginData: %v", err)
	}

	exportedAt := time.Unix(1700000000, 0).UTC()
	document, err := source.ExportVault(ctx, appvault.ExportOptions{
		Format: appvault.ExportJSON, ExportedAt: exportedAt,
	})
	if err != nil {
		t.Fatalf("ExportVault: %v", err)
	}
	destination, _ := openRoundTripVault(t, t.TempDir())
	preview, err := destination.PreviewImport(ctx, string(document.Content),
		appvault.PreviewImportOptions{FileName: "netcatty-vault.json"})
	if err != nil {
		t.Fatalf("PreviewImport: %v", err)
	}
	if preview.Format != "netcatty_json" {
		t.Fatalf("detected format = %q", preview.Format)
	}
	if _, err := destination.CommitImport(ctx, preview, appvault.ImportCommitOptions{}); err != nil {
		t.Fatalf("CommitImport: %v", err)
	}
	hosts, err := destination.List(ctx)
	if err != nil {
		t.Fatalf("List imported hosts: %v", err)
	}
	if len(hosts) != 1 {
		t.Fatalf("imported hosts = %d, want 1", len(hosts))
	}
	imported := hosts[0]
	if imported.Label != host.Label || imported.Hostname != host.Hostname {
		t.Fatalf("imported Host = %+v", imported)
	}
	restored, ok := imported.PluginData["acme.provider"]
	if !ok {
		t.Fatalf("import dropped Host pluginData: %+v", imported.PluginData)
	}
	if restored.SchemaVersion != 2 {
		t.Fatalf("pluginData schema version = %d, want 2", restored.SchemaVersion)
	}
	if string(restored.Fields["profile"]) != string(namespace.Fields["profile"]) {
		t.Fatalf("pluginData profile field = %s", restored.Fields["profile"])
	}
	if _, ok := restored.Extra["future"]; !ok {
		t.Fatalf("import dropped an unknown pluginData property: %+v", restored.Extra)
	}

	sourceHost, err := sourceDatabase.Get(ctx, model.HostID(host.ID))
	if err != nil {
		t.Fatalf("read source Host: %v", err)
	}
	if string(sourceHost.PluginData["acme.provider"].Fields["profile"]) !=
		string(restored.Fields["profile"]) {
		t.Fatalf("round trip changed pluginData: source=%+v imported=%+v",
			sourceHost.PluginData, imported.PluginData)
	}
}
