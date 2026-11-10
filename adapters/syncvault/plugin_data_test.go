package syncvault

import (
	"context"
	"encoding/json"
	"testing"

	appsync "github.com/mslxl/natcatty/application/sync"
	appvault "github.com/mslxl/natcatty/application/vault"
	"github.com/mslxl/natcatty/domain/model/plugindata"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

func syncNamespace(fields map[string]string) plugindata.Namespace {
	values := make(map[string]json.RawMessage, len(fields))
	for key, value := range fields {
		values[key] = json.RawMessage(value)
	}
	return plugindata.Namespace{SchemaVersion: 1, Fields: values}
}

func dataProfileRef(profileID, field string) appvault.SecretRef {
	return appvault.SecretRef{
		EntityType: appvault.EntityDataProfile, EntityID: profileID, Field: field,
	}
}

func dataProfileForSync(id string, refs ...string) domainplugin.DataProfile {
	secretRefs := make(map[string]string, len(refs))
	for _, field := range refs {
		secretRefs[field] = appvault.DataProfileSecretRef(id, field)
	}
	return domainplugin.DataProfile{
		ID: id, Provider: "plugin.acme.data.db", Label: id,
		Mode: domainplugin.DataModeReadOnly,
		PluginData: plugindata.Map{"acme.data": syncNamespace(
			map[string]string{"name": `"profile"`})},
		SecretRefs: secretRefs,
	}
}

func TestSnapshotCarriesDataProfilePluginDataAndItsPlaintextSecrets(t *testing.T) {
	profile := dataProfileForSync("p1", "token")
	records := &fakeRecords{dataProfiles: []domainplugin.DataProfile{profile}}
	secrets := &fakeSecrets{mode: appvault.SecretModeOS,
		values: map[appvault.SecretRef]string{dataProfileRef("p1", "token"): "hunter2"}}
	vault := New(records, secrets)
	payload, err := vault.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(payload.DataProfiles) != 1 ||
		string(payload.DataProfiles[0].PluginData["acme.data"].Fields["name"]) != `"profile"` {
		t.Fatalf("dataProfiles = %+v", payload.DataProfiles)
	}
	// The record keeps the reference; only the payload's secret array carries
	// plaintext, exactly like every other credential family.
	if payload.DataProfiles[0].SecretRefs["token"] != appvault.DataProfileSecretRef("p1", "token") {
		t.Fatalf("secretRefs = %+v", payload.DataProfiles[0].SecretRefs)
	}
	if len(payload.Secrets) != 1 ||
		payload.Secrets[0] != (appsync.SecretValue{
			EntityType: appvault.EntityDataProfile, EntityID: "p1",
			Field: "token", Value: "hunter2",
		}) {
		t.Fatalf("secrets = %+v", payload.Secrets)
	}
	if diagnostics := vault.PluginDataDiagnostics(); len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
}

func TestSnapshotReportsUnresolvedDataProfileSecretsWithoutEmptyingThem(t *testing.T) {
	profile := dataProfileForSync("p1", "password", "token")
	records := &fakeRecords{dataProfiles: []domainplugin.DataProfile{profile}}
	secrets := &fakeSecrets{mode: appvault.SecretModeOS,
		values: map[appvault.SecretRef]string{dataProfileRef("p1", "token"): "kept"}}
	vault := New(records, secrets)
	payload, err := vault.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(payload.Secrets) != 1 || payload.Secrets[0].Field != "token" ||
		payload.Secrets[0].Value != "kept" {
		t.Fatalf("secrets = %+v", payload.Secrets)
	}
	if payload.DataProfiles[0].SecretRefs["password"] !=
		appvault.DataProfileSecretRef("p1", "password") {
		t.Fatalf("the unresolved reference was rewritten: %+v",
			payload.DataProfiles[0].SecretRefs)
	}
	diagnostics := vault.PluginDataDiagnostics()
	if len(diagnostics) != 1 ||
		diagnostics[0].RecordKind != plugindata.RecordDataProfile ||
		diagnostics[0].RecordID != "p1" ||
		diagnostics[0].PluginID != "acme.data" ||
		diagnostics[0].State != plugindata.StateDormant ||
		diagnostics[0].Reason != plugindata.ReasonUnresolvedSecret ||
		diagnostics[0].Detail != "password" {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}

	// A profile with no namespace still reports the loss under its provider's
	// Plugin ID, so an unresolved secret cannot be invisible.
	empty := domainplugin.DataProfile{
		ID: "p2", Provider: "plugin.acme.data.db", Mode: domainplugin.DataModeReadOnly,
		SecretRefs: map[string]string{"token": appvault.DataProfileSecretRef("p2", "token")},
	}
	records.dataProfiles = []domainplugin.DataProfile{empty}
	if _, err := vault.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot without namespaces: %v", err)
	}
	diagnostics = vault.PluginDataDiagnostics()
	if len(diagnostics) != 1 || diagnostics[0].RecordID != "p2" ||
		diagnostics[0].PluginID != "acme.data" || diagnostics[0].Fields != 0 {
		t.Fatalf("profile-level diagnostics = %+v", diagnostics)
	}
}

func TestApplyStoresDataProfileSecretsAndKeepsUnresolvedReferences(t *testing.T) {
	previous := dataProfileForSync("p1", "dropped", "token")
	records := &fakeRecords{dataProfiles: []domainplugin.DataProfile{previous}}
	secrets := &fakeSecrets{mode: appvault.SecretModeOS, values: map[appvault.SecretRef]string{
		dataProfileRef("p1", "dropped"): "stale",
		dataProfileRef("p1", "token"):   "old",
	}}
	incoming := dataProfileForSync("p1", "password", "token")
	payload := appsync.Payload{
		DataProfiles: []domainplugin.DataProfile{incoming},
		Secrets: []appsync.SecretValue{{
			EntityType: appvault.EntityDataProfile, EntityID: "p1",
			Field: "token", Value: "fresh",
		}},
	}
	vault := New(records, secrets)
	if err := vault.Apply(context.Background(), payload); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if secrets.values[dataProfileRef("p1", "token")] != "fresh" {
		t.Fatalf("token = %q", secrets.values[dataProfileRef("p1", "token")])
	}
	// The incoming record dropped the field, so its old ciphertext goes away.
	if _, ok := secrets.values[dataProfileRef("p1", "dropped")]; ok {
		t.Fatal("a secret the incoming profile dropped survived")
	}
	// The incoming record kept "password" but no plaintext arrived; its
	// reference remains and the loss is reported, never replaced with empty.
	applied := records.applied[len(records.applied)-1].DataProfiles[0]
	if applied.SecretRefs["password"] != appvault.DataProfileSecretRef("p1", "password") {
		t.Fatalf("applied secretRefs = %+v", applied.SecretRefs)
	}
	// clearStale must not delete a reference the incoming record still keeps.
	if _, ok := secrets.values[dataProfileRef("p1", "password")]; ok {
		t.Fatal("unexpected plaintext for an unresolved incoming reference")
	}
	diagnostics := vault.PluginDataDiagnostics()
	if len(diagnostics) != 1 || diagnostics[0].Reason != plugindata.ReasonUnresolvedSecret ||
		diagnostics[0].Detail != "password" {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
}

func TestApplyKeepsMalformedDataProfileReferences(t *testing.T) {
	profile := dataProfileForSync("p1")
	profile.SecretRefs = map[string]string{"foreign": "foreign:reference"}
	records := &fakeRecords{}
	vault := New(records, &fakeSecrets{mode: appvault.SecretModeOS})
	if err := vault.Apply(context.Background(), appsync.Payload{
		DataProfiles: []domainplugin.DataProfile{profile},
	}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	applied := records.applied[0].DataProfiles[0]
	if applied.SecretRefs["foreign"] != "foreign:reference" {
		t.Fatalf("foreign reference was rewritten: %+v", applied.SecretRefs)
	}
	if diagnostics := vault.PluginDataDiagnostics(); len(diagnostics) != 1 ||
		diagnostics[0].Detail != "foreign" {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
}
