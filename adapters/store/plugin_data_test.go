package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	appsync "github.com/mslxl/natcatty/application/sync"
	appbackup "github.com/mslxl/natcatty/application/vaultbackup"
	"github.com/mslxl/natcatty/domain/model"
	"github.com/mslxl/natcatty/domain/model/plugindata"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	domainbackup "github.com/mslxl/natcatty/domain/vaultbackup"
)

func namespace(version int, fields map[string]string) plugindata.Namespace {
	values := make(map[string]json.RawMessage, len(fields))
	for key, value := range fields {
		values[key] = json.RawMessage(value)
	}
	return plugindata.Namespace{SchemaVersion: version, Fields: values}
}

func pluginHost(id string, data plugindata.Map) domainvault.Host {
	now := time.UnixMilli(1_700_000_000_000)
	return domainvault.Host{
		ID: model.HostID(id), Label: id, Hostname: id + ".example.com", Port: 22,
		Username: "root", Protocol: domainvault.ProtocolSSH,
		CreatedAt: now, UpdatedAt: now, PluginData: data,
	}
}

func pluginProfile(id string, data plugindata.Map) domainplugin.DataProfile {
	now := time.UnixMilli(1_700_000_000_000)
	return domainplugin.DataProfile{
		ID: id, Provider: "plugin.acme.data.db", Label: id,
		Mode: domainplugin.DataModeReadOnly, CreatedAt: now, UpdatedAt: now,
		PluginData: data,
	}
}

func TestHostPluginDataRoundTripsAndQuarantinesInvalidNamespaces(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	good := namespace(1, map[string]string{"name": `"ok"`})
	good.Extra = map[string]json.RawMessage{"future": json.RawMessage(`true`)}
	host := pluginHost("h1", plugindata.Map{
		"acme.one": good,
		"acme.bad": namespace(0, map[string]string{"n": "1"}),
	})
	if err := store.Save(ctx, host); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := store.Get(ctx, "h1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(loaded.PluginData) != 1 || string(loaded.PluginData["acme.one"].Fields["name"]) != `"ok"` {
		t.Fatalf("pluginData = %+v", loaded.PluginData)
	}
	if string(loaded.PluginData["acme.one"].Extra["future"]) != "true" {
		t.Fatal("unknown namespace property was not preserved")
	}
	diagnostics, err := store.PluginDataDiagnostics(ctx)
	if err != nil {
		t.Fatalf("PluginDataDiagnostics: %v", err)
	}
	if len(diagnostics) != 1 || diagnostics[0].PluginID != "acme.bad" ||
		diagnostics[0].State != plugindata.StateQuarantined ||
		diagnostics[0].Reason != plugindata.ReasonSchemaVersion ||
		diagnostics[0].RecordKind != plugindata.RecordHost {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}

	// An ordinary save that does not mention acme.bad must not erase its
	// quarantine: the row records imported data the user has not repaired or
	// deleted.
	loaded.PluginData = plugindata.Map{"acme.one": good}
	if err := store.Save(ctx, loaded); err != nil {
		t.Fatalf("Save corrected: %v", err)
	}
	diagnostics, _ = store.PluginDataDiagnostics(ctx)
	if len(diagnostics) != 1 || diagnostics[0].PluginID != "acme.bad" {
		t.Fatalf("an ordinary save erased the quarantine: %+v", diagnostics)
	}

	// Repairing the namespace clears its quarantine.
	repaired := plugindata.Clone(loaded.PluginData)
	repaired["acme.bad"] = namespace(1, map[string]string{"n": "1"})
	loaded.PluginData = repaired
	if err := store.Save(ctx, loaded); err != nil {
		t.Fatalf("Save repaired: %v", err)
	}
	diagnostics, _ = store.PluginDataDiagnostics(ctx)
	if len(diagnostics) != 0 {
		t.Fatalf("quarantine survived a repaired save: %+v", diagnostics)
	}

	// Deleting the record removes its diagnostics with it.
	if err := store.Save(ctx, pluginHost("h2", plugindata.Map{
		"acme.bad": namespace(-1, nil),
	})); err != nil {
		t.Fatalf("Save h2: %v", err)
	}
	diagnostics, _ = store.PluginDataDiagnostics(ctx)
	if len(diagnostics) != 1 {
		t.Fatalf("diagnostics after h2 = %+v", diagnostics)
	}
	if err := store.Delete(ctx, "h2"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	diagnostics, _ = store.PluginDataDiagnostics(ctx)
	if len(diagnostics) != 0 {
		t.Fatalf("quarantine survived deletion: %+v", diagnostics)
	}
}

func TestPluginDataSurvivesBackupRestoreVerbatim(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	host := pluginHost("h1", plugindata.Map{
		"acme.one": namespace(3, map[string]string{
			"nested": `{"a":[1,{"b":"<&>"}]}`,
			"label":  `"alpha"`,
		}),
	})
	if err := store.Save(ctx, host); err != nil {
		t.Fatalf("Save Host: %v", err)
	}
	profiles := NewDataProfiles(store)
	if err := profiles.Save(ctx, pluginProfile("p1", plugindata.Map{
		"acme.one": namespace(2, map[string]string{"secret": `"vault:data-profile:p1:token"`}),
	})); err != nil {
		t.Fatalf("Save profile: %v", err)
	}
	before, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	beforeBytes, err := domainbackup.CanonicalBytes(before)
	if err != nil {
		t.Fatalf("CanonicalBytes: %v", err)
	}

	// Replace both records, then restore the snapshot.
	if err := store.Save(ctx, pluginHost("h1", plugindata.Map{
		"other.two": namespace(1, map[string]string{"x": "1"}),
	})); err != nil {
		t.Fatalf("replace Host: %v", err)
	}
	if err := profiles.Save(ctx, pluginProfile("p1", nil)); err != nil {
		t.Fatalf("replace profile: %v", err)
	}
	if err := store.Restore(ctx, before); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	loaded, err := store.Get(ctx, "h1")
	if err != nil {
		t.Fatalf("Get restored Host: %v", err)
	}
	nested := loaded.PluginData["acme.one"].Fields["nested"]
	// The nested value keeps its HTML characters unexpanded; the snapshot
	// comparison below proves the stored bytes survived the backup/restore
	// verbatim.
	var decoded struct {
		A []json.RawMessage `json:"a"`
	}
	var inner struct {
		B string `json:"b"`
	}
	if err := json.Unmarshal(nested, &decoded); err != nil || len(decoded.A) != 2 ||
		json.Unmarshal(decoded.A[1], &inner) != nil || inner.B != "<&>" {
		t.Fatalf("restored Host pluginData = %s, nested = %+v, err = %v",
			nested, decoded, err)
	}
	loadedProfile, err := profiles.Get(ctx, "p1")
	if err != nil {
		t.Fatalf("Get restored profile: %v", err)
	}
	if string(loadedProfile.PluginData["acme.one"].Fields["secret"]) != `"vault:data-profile:p1:token"` {
		t.Fatalf("restored profile pluginData = %+v", loadedProfile.PluginData)
	}
	after, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot after: %v", err)
	}
	afterBytes, err := domainbackup.CanonicalBytes(after)
	if err != nil {
		t.Fatalf("CanonicalBytes after: %v", err)
	}
	if string(beforeBytes) != string(afterBytes) {
		t.Fatalf("pluginData changed across backup/restore\nbefore: %s\nafter:  %s",
			beforeBytes, afterBytes)
	}
	diagnostics, _ := store.PluginDataDiagnostics(ctx)
	if len(diagnostics) != 0 {
		t.Fatalf("valid restore produced diagnostics: %+v", diagnostics)
	}
}

func TestRestoreQuarantinesInvalidAndOversizedRecordsIndividually(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	if err := store.Save(ctx, pluginHost("h1", pluginDataWithFuture())); err != nil {
		t.Fatalf("Save: %v", err)
	}
	snapshot, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	var goodBytes []byte
	for _, table := range snapshot.Tables {
		if table.Name != domainbackup.TableHosts {
			continue
		}
		goodBytes = []byte(table.Rows[0]["data"].Text)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(goodBytes, &top); err != nil {
		t.Fatalf("decode host: %v", err)
	}
	top["pluginData"] = json.RawMessage(`{` +
		`"acme.good":{"schemaVersion":1,"fields":{"name":"ok"},"future":true},` +
		`"acme.bad":{"schemaVersion":1,"fields":{"deep":` +
		strings.Repeat("[", plugindata.MaxDepth+1) + "0" + strings.Repeat("]", plugindata.MaxDepth+1) + `}}}`)
	tampered, _ := json.Marshal(top)
	for index := range snapshot.Tables {
		if snapshot.Tables[index].Name != domainbackup.TableHosts {
			continue
		}
		snapshot.Tables[index].Rows[0]["data"] = domainbackup.Text(string(tampered))
	}
	if err := store.Restore(ctx, snapshot); err != nil {
		t.Fatalf("Restore with one invalid namespace: %v", err)
	}
	loaded, err := store.Get(ctx, "h1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(loaded.PluginData) != 1 {
		t.Fatalf("pluginData = %+v", loaded.PluginData)
	}
	if string(loaded.PluginData["acme.good"].Extra["future"]) != "true" {
		t.Fatal("valid namespace lost its unknown property across restore")
	}
	diagnostics, _ := store.PluginDataDiagnostics(ctx)
	if len(diagnostics) != 1 || diagnostics[0].PluginID != "acme.bad" ||
		diagnostics[0].Reason != plugindata.ReasonDepth {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}

	// An over-limit record quarantines its namespaces deterministically; it
	// must not poison the whole restore. The valid namespace and every other
	// record still apply.
	oversized := make(map[string]json.RawMessage, 5)
	fields := map[string]string{}
	for index := 0; index < 5; index++ {
		fields[string(rune('a'+index))] = `"` + strings.Repeat("x", 14<<10) + `"`
	}
	encoded, _ := json.Marshal(namespace(1, fields))
	oversized["acme.big"] = encoded
	oversizedBytes, _ := json.Marshal(oversized)
	top["pluginData"] = oversizedBytes
	tampered, _ = json.Marshal(top)
	for index := range snapshot.Tables {
		if snapshot.Tables[index].Name == domainbackup.TableHosts {
			snapshot.Tables[index].Rows[0]["data"] = domainbackup.Text(string(tampered))
		}
	}
	if err := store.Restore(ctx, snapshot); err != nil {
		t.Fatalf("Restore rejected an over-limit record instead of quarantining it: %v", err)
	}
	loaded, err = store.Get(ctx, "h1")
	if err != nil {
		t.Fatalf("Get after oversized restore: %v", err)
	}
	if len(loaded.PluginData) != 0 {
		t.Fatalf("over-limit namespace stayed in the record: %+v", loaded.PluginData)
	}
	diagnostics, _ = store.PluginDataDiagnostics(ctx)
	if len(diagnostics) != 1 || diagnostics[0].PluginID != "acme.big" ||
		diagnostics[0].Reason != plugindata.ReasonRecordSize {
		t.Fatalf("oversized diagnostics = %+v", diagnostics)
	}
}

func pluginDataWithFuture() plugindata.Map {
	namespace := namespace(1, map[string]string{"name": `"ok"`})
	namespace.Extra = map[string]json.RawMessage{"future": json.RawMessage(`true`)}
	return plugindata.Map{"acme.good": namespace}
}

func TestApplySyncCarriesPluginDataAndQuarantinesPerNamespace(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	payload := appsync.Payload{}
	payload.Hosts = []domainvault.Host{pluginHost("h1", plugindata.Map{
		"acme.good": namespace(1, map[string]string{"name": `"ok"`}),
		"acme.bad":  namespace(0, nil),
	})}
	payload.DataProfiles = []domainplugin.DataProfile{pluginProfile("p1", plugindata.Map{
		"acme.good": namespace(1, map[string]string{"name": `"ok"`}),
		"acme.bad":  namespace(0, nil),
	})}
	if err := store.ApplySync(ctx, payload); err != nil {
		t.Fatalf("ApplySync: %v", err)
	}
	host, err := store.Get(ctx, "h1")
	if err != nil {
		t.Fatalf("Get Host: %v", err)
	}
	if len(host.PluginData) != 1 || host.PluginData["acme.good"].SchemaVersion != 1 {
		t.Fatalf("Host pluginData = %+v", host.PluginData)
	}
	profile, err := NewDataProfiles(store).Get(ctx, "p1")
	if err != nil {
		t.Fatalf("Get profile: %v", err)
	}
	if len(profile.PluginData) != 1 {
		t.Fatalf("profile pluginData = %+v", profile.PluginData)
	}
	diagnostics, _ := store.PluginDataDiagnostics(ctx)
	if len(diagnostics) != 2 {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
	seen := map[plugindata.RecordKind]bool{}
	for _, diagnostic := range diagnostics {
		seen[diagnostic.RecordKind] = true
		if diagnostic.PluginID != "acme.bad" ||
			diagnostic.State != plugindata.StateQuarantined {
			t.Fatalf("diagnostic = %+v", diagnostic)
		}
	}
	if !seen[plugindata.RecordHost] || !seen[plugindata.RecordDataProfile] {
		t.Fatalf("record kinds = %+v", seen)
	}

	// A later payload that still lacks the invalid namespace preserves its
	// quarantine; the payload is not evidence the data was deleted.
	payload.Hosts = []domainvault.Host{pluginHost("h1", pluginDataWithFuture())}
	payload.DataProfiles = nil
	if err := store.ApplySync(ctx, payload); err != nil {
		t.Fatalf("ApplySync clean: %v", err)
	}
	diagnostics, _ = store.PluginDataDiagnostics(ctx)
	if len(diagnostics) != 1 || diagnostics[0].RecordKind != plugindata.RecordHost ||
		diagnostics[0].PluginID != "acme.bad" {
		t.Fatalf("an ordinary sync apply erased the quarantine: %+v", diagnostics)
	}

	// A later payload carrying a valid acme.bad namespace repairs it and
	// clears the row; the profile's row disappeared with its record.
	payload.Hosts = []domainvault.Host{pluginHost("h1", plugindata.Map{
		"acme.bad": namespace(1, map[string]string{"n": `"repaired"`}),
	})}
	if err := store.ApplySync(ctx, payload); err != nil {
		t.Fatalf("ApplySync repaired: %v", err)
	}
	diagnostics, _ = store.PluginDataDiagnostics(ctx)
	if len(diagnostics) != 0 {
		t.Fatalf("quarantine survived a repaired payload: %+v", diagnostics)
	}
}

func TestPurgePluginDataRemovesOnlyThatNamespace(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	host := pluginHost("h1", plugindata.Map{
		"acme.one":  namespace(1, map[string]string{"name": `"acme"`}),
		"other.two": namespace(1, map[string]string{"name": `"other"`}),
		"acme.bad":  namespace(0, nil),
	})
	if err := store.Save(ctx, host); err != nil {
		t.Fatalf("Save Host: %v", err)
	}
	profiles := NewDataProfiles(store)
	if err := profiles.Save(ctx, pluginProfile("p1", plugindata.Map{
		"acme.one": namespace(1, map[string]string{"name": `"acme"`}),
	})); err != nil {
		t.Fatalf("Save profile: %v", err)
	}
	if diagnostics, _ := store.PluginDataDiagnostics(ctx); len(diagnostics) != 1 {
		t.Fatalf("setup quarantine = %+v", diagnostics)
	}
	if err := store.PurgePluginData(ctx, "acme.one"); err != nil {
		t.Fatalf("PurgePluginData: %v", err)
	}
	loaded, err := store.Get(ctx, "h1")
	if err != nil {
		t.Fatalf("Get Host: %v", err)
	}
	if _, ok := loaded.PluginData["acme.one"]; ok {
		t.Fatal("purged namespace survived on the Host")
	}
	if _, ok := loaded.PluginData["other.two"]; !ok {
		t.Fatal("purge removed another plugin's namespace")
	}
	// The quarantine belonged to a different plugin ID and therefore remains.
	diagnostics, _ := store.PluginDataDiagnostics(ctx)
	if len(diagnostics) != 1 || diagnostics[0].PluginID != "acme.bad" {
		t.Fatalf("quarantine after purge = %+v", diagnostics)
	}
	if err := store.PurgePluginData(ctx, "acme.bad"); err != nil {
		t.Fatalf("PurgePluginData bad: %v", err)
	}
	if diagnostics, _ := store.PluginDataDiagnostics(ctx); len(diagnostics) != 0 {
		t.Fatalf("quarantine survived purging its plugin: %+v", diagnostics)
	}
	profile, err := profiles.Get(ctx, "p1")
	if err != nil {
		t.Fatalf("Get profile: %v", err)
	}
	if len(profile.PluginData) != 0 {
		t.Fatalf("profile pluginData after purge = %+v", profile.PluginData)
	}
}

func TestPluginDataRecordsExposesProfileSecretRefs(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	profile := pluginProfile("p1", pluginDataWithFuture())
	profile.SecretRefs = map[string]string{"token": "vault:data-profile:p1:token"}
	if err := NewDataProfiles(store).Save(ctx, profile); err != nil {
		t.Fatalf("Save profile: %v", err)
	}
	records, err := store.PluginDataRecords(ctx)
	if err != nil {
		t.Fatalf("PluginDataRecords: %v", err)
	}
	if len(records) != 1 || records[0].Kind != plugindata.RecordDataProfile ||
		records[0].Provider != profile.Provider ||
		records[0].SecretRefs["token"] != "vault:data-profile:p1:token" ||
		len(records[0].Data) != 1 {
		t.Fatalf("records = %+v", records)
	}
}

func TestRestoreRejectsANonTextRecordPayload(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	if err := store.Save(ctx, pluginHost("h1", pluginDataWithFuture())); err != nil {
		t.Fatalf("Save: %v", err)
	}
	snapshot, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	found := false
	for index := range snapshot.Tables {
		if snapshot.Tables[index].Name != domainbackup.TableHosts {
			continue
		}
		found = true
		snapshot.Tables[index].Rows[0]["data"] = domainbackup.Blob(
			[]byte(`{"id":"h1","pluginData":{"acme.one":{"schemaVersion":1}}}`))
	}
	if !found {
		t.Fatal("snapshot has no hosts table")
	}
	before, _ := store.Snapshot(ctx)
	beforeBytes, _ := domainbackup.CanonicalBytes(before)
	err = store.Restore(ctx, snapshot)
	if err == nil || !errors.Is(err, appbackup.ErrIncompatibleSnapshot) {
		t.Fatalf("Restore accepted a blob record payload: %v", err)
	}
	after, _ := store.Snapshot(ctx)
	afterBytes, _ := domainbackup.CanonicalBytes(after)
	if string(beforeBytes) != string(afterBytes) {
		t.Fatal("failed restore changed the Vault")
	}
}
