package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	appsync "github.com/mslxl/natcatty/application/sync"
	"github.com/mslxl/natcatty/domain/model/plugindata"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	domainbackup "github.com/mslxl/natcatty/domain/vaultbackup"
	"gorm.io/gorm"
)

func interopSizedField(fill string, size int) json.RawMessage {
	if size < 2 || fill == "" {
		panic("field value must include its quotes and a fill")
	}
	content := size - 2
	repeated := strings.Repeat(fill, content/len(fill)+1)
	return json.RawMessage(`"` + repeated[:content] + `"`)
}

func interopRecordAtLimit(t *testing.T, fill string, over int) plugindata.Map {
	t.Helper()
	fields := map[string]json.RawMessage{}
	for _, key := range []string{"a", "b", "c", "d"} {
		fields[key] = interopSizedField(fill, plugindata.MaxFieldBytes-(2<<10))
	}
	fields["e"] = interopSizedField(fill, 2)
	value := plugindata.Map{"acme.data": plugindata.Namespace{
		SchemaVersion: 1, Fields: fields}}
	size := interopEncodedSize(t, value)
	delta := plugindata.MaxRecordBytes + over - size
	fields["e"] = interopSizedField(fill, len(fields["e"])-2+delta+2)
	if got := interopEncodedSize(t, value); got != plugindata.MaxRecordBytes+over {
		t.Fatalf("record construction = %d bytes, want %d",
			got, plugindata.MaxRecordBytes+over)
	}
	return value
}

func interopEncodedSize(t *testing.T, value plugindata.Map) int {
	t.Helper()
	data, err := plugindata.EncodeMap(value)
	if err != nil {
		t.Fatalf("EncodeMap: %v", err)
	}
	return len(data)
}

func interopHostData(t *testing.T, store *Store, id string) string {
	t.Helper()
	var data string
	var found bool
	if err := store.db.ORM(context.Background(), func(tx *gorm.DB) error {
		var err error
		data, found, err = loadDataRow(tx, &hostSchema{}, &hostSchema{ID: id})
		return err
	}); err != nil {
		t.Fatalf("read Host row: %v", err)
	}
	if !found {
		t.Fatalf("Host %s is missing", id)
	}
	return data
}

func interopOverwriteHostData(t *testing.T, store *Store, id, data string) {
	t.Helper()
	if err := store.db.ORM(context.Background(), func(tx *gorm.DB) error {
		return tx.Model(&hostSchema{}).Where("id = ?", id).Update("data", data).Error
	}); err != nil {
		t.Fatalf("overwrite Host %s: %v", id, err)
	}
}

func interopFieldValues(t *testing.T, host domainvault.Host, pluginID string,
	keys []string) map[string]string {
	t.Helper()
	namespace, ok := host.PluginData[pluginID]
	if !ok {
		t.Fatalf("Host %s has no %s namespace: %+v", host.ID, pluginID, host.PluginData)
	}
	out := map[string]string{}
	for _, key := range keys {
		out[key] = string(namespace.Fields[key])
	}
	return out
}

func TestStoredProviderFieldsStayByteStableAcrossSaveGetBackupAndRestore(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	fields := map[string]string{
		"less":    `"` + strings.Repeat("<", 3000) + `"`,
		"greater": `"` + strings.Repeat(">", 3000) + `"`,
		"amp":     `"` + strings.Repeat("&", 3000) + `"`,
		"amp2":    `"` + strings.Repeat("&", 3000) + `"`,
	}
	host := pluginHost("h-html", plugindata.Map{
		"acme.data": namespace(1, fields),
	})
	if err := store.Save(ctx, host); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw := interopHostData(t, store, "h-html")
	for _, escape := range []string{"\\u003c", "\\u003e", "\\u0026"} {
		if strings.Contains(raw, escape) {
			t.Fatalf("stored Host expanded HTML to %s: %d bytes", escape, len(raw))
		}
	}
	if len(raw) > 14000 {
		t.Fatalf("four 3,002-byte fields stored as %d bytes", len(raw))
	}
	loaded, err := store.Get(ctx, "h-html")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	stored := interopFieldValues(t, loaded, "acme.data",
		[]string{"less", "greater", "amp", "amp2"})
	for key, want := range fields {
		if stored[key] != want {
			t.Fatalf("%s field changed across Save/Get: %d bytes, want %d",
				key, len(stored[key]), len(want))
		}
	}
	if diagnostics, _ := store.PluginDataDiagnostics(ctx); len(diagnostics) != 0 {
		t.Fatalf("a legal HTML-heavy namespace was quarantined: %+v", diagnostics)
	}

	before, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	beforeBytes, err := domainbackup.CanonicalBytes(before)
	if err != nil {
		t.Fatalf("CanonicalBytes: %v", err)
	}
	if err := store.Save(ctx, pluginHost("h-html", plugindata.Map{
		"other.two": namespace(1, map[string]string{"x": "1"}),
	})); err != nil {
		t.Fatalf("replace Host: %v", err)
	}
	if err := store.Restore(ctx, before); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	loaded, err = store.Get(ctx, "h-html")
	if err != nil {
		t.Fatalf("Get restored: %v", err)
	}
	restored := interopFieldValues(t, loaded, "acme.data",
		[]string{"less", "greater", "amp", "amp2"})
	for key, want := range fields {
		if restored[key] != want {
			t.Fatalf("%s field changed across backup/restore", key)
		}
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
		t.Fatal("HTML-heavy pluginData changed across backup/restore")
	}
}

func TestFieldAndRecordLimitsCountStoredBytes(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()

	exactField := pluginHost("h-field-exact", plugindata.Map{
		"acme.data": {SchemaVersion: 1, Fields: map[string]json.RawMessage{
			"blob": interopSizedField("<", plugindata.MaxFieldBytes),
		}},
	})
	if err := store.Save(ctx, exactField); err != nil {
		t.Fatalf("exact field limit: %v", err)
	}
	loaded, _ := store.Get(ctx, "h-field-exact")
	if len(loaded.PluginData["acme.data"].Fields["blob"]) != plugindata.MaxFieldBytes {
		t.Fatalf("exact-limit field was not stored verbatim")
	}

	overField := pluginHost("h-field-over", plugindata.Map{
		"acme.data": {SchemaVersion: 1, Fields: map[string]json.RawMessage{
			"blob": interopSizedField("&", plugindata.MaxFieldBytes+1),
		}},
	})
	if err := store.Save(ctx, overField); err != nil {
		t.Fatalf("an invalid namespace should be quarantined, not fail the save: %v", err)
	}
	loaded, _ = store.Get(ctx, "h-field-over")
	if len(loaded.PluginData) != 0 {
		t.Fatalf("one-byte-over field was stored: %+v", loaded.PluginData)
	}
	diagnostics, _ := store.PluginDataDiagnostics(ctx)
	if len(diagnostics) != 1 || diagnostics[0].PluginID != "acme.data" ||
		diagnostics[0].Reason != plugindata.ReasonFieldSize {
		t.Fatalf("field diagnostics = %+v", diagnostics)
	}

	exactRecord := pluginHost("h-record-exact", interopRecordAtLimit(t, "<", 0))
	if err := store.Save(ctx, exactRecord); err != nil {
		t.Fatalf("exact record limit: %v", err)
	}
	loaded, _ = store.Get(ctx, "h-record-exact")
	if len(loaded.PluginData) != 1 {
		t.Fatalf("exact-limit record was not stored: %+v", loaded.PluginData)
	}
	before, _ := store.Snapshot(ctx)
	beforeBytes, _ := domainbackup.CanonicalBytes(before)

	overRecord := pluginHost("h-record-exact", interopRecordAtLimit(t, "<", 1))
	err := store.Save(ctx, overRecord)
	if !errors.Is(err, plugindata.ErrRecordTooLarge) {
		t.Fatalf("one-byte-over record error = %v", err)
	}
	after, _ := store.Snapshot(ctx)
	afterBytes, _ := domainbackup.CanonicalBytes(after)
	if string(beforeBytes) != string(afterBytes) {
		t.Fatal("a rejected over-limit edit changed the Vault")
	}
}

func TestRestoreQuarantinesOneOversizedRecordAndKeepsOtherRecords(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	if err := store.Save(ctx, pluginHost("h-ok", pluginDataWithFuture())); err != nil {
		t.Fatalf("Save: %v", err)
	}
	snapshot, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	var hostTable *domainbackup.Table
	for index := range snapshot.Tables {
		if snapshot.Tables[index].Name == domainbackup.TableHosts {
			hostTable = &snapshot.Tables[index]
		}
	}
	if hostTable == nil || len(hostTable.Rows) != 1 {
		t.Fatalf("hosts table = %+v", hostTable)
	}
	row := domainbackup.Row{}
	for column, value := range hostTable.Rows[0] {
		row[column] = value
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(row["data"].Text), &top); err != nil {
		t.Fatalf("decode template Host: %v", err)
	}
	top["id"] = json.RawMessage(`"h-big"`)
	top["label"] = json.RawMessage(`"h-big"`)
	top["hostname"] = json.RawMessage(`"h-big.example.com"`)
	fields := map[string]json.RawMessage{}
	for index := 0; index < 5; index++ {
		fields[string(rune('a'+index))] = interopSizedField("x", 14<<10)
	}
	pluginData, err := json.Marshal(plugindata.Map{
		"acme.big": {SchemaVersion: 1, Fields: fields},
	})
	if err != nil {
		t.Fatalf("marshal big namespace: %v", err)
	}
	top["pluginData"] = pluginData
	data, _ := json.Marshal(top)
	row["id"] = domainbackup.Text("h-big")
	row["label"] = domainbackup.Text("h-big")
	row["hostname"] = domainbackup.Text("h-big.example.com")
	row["data"] = domainbackup.Text(string(data))
	hostTable.Rows = append(hostTable.Rows, row)

	if err := store.Restore(ctx, snapshot); err != nil {
		t.Fatalf("one oversized record aborted the restore: %v", err)
	}
	ok, err := store.Get(ctx, "h-ok")
	if err != nil || string(ok.PluginData["acme.good"].Fields["name"]) != `"ok"` {
		t.Fatalf("valid sibling after restore = %+v, err = %v", ok.PluginData, err)
	}
	big, err := store.Get(ctx, "h-big")
	if err != nil {
		t.Fatalf("the oversized record was not restored: %v", err)
	}
	if len(big.PluginData) != 0 {
		t.Fatalf("over-limit namespace stayed on the record: %+v", big.PluginData)
	}
	diagnostics, _ := store.PluginDataDiagnostics(ctx)
	if len(diagnostics) != 1 || diagnostics[0].RecordID != "h-big" ||
		diagnostics[0].PluginID != "acme.big" ||
		diagnostics[0].Reason != plugindata.ReasonRecordSize {
		t.Fatalf("restore diagnostics = %+v", diagnostics)
	}
}

func TestRestoreReencodesDuplicatePluginDataPropertiesBeforePersisting(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	if err := store.Save(ctx, pluginHost("h-dup", plugindata.Map{
		"acme.data": namespace(1, map[string]string{"name": `"ok"`}),
	})); err != nil {
		t.Fatalf("Save: %v", err)
	}
	fields := map[string]json.RawMessage{}
	for index := 0; index < 5; index++ {
		fields[string(rune('a'+index))] = interopSizedField("x", 14<<10)
	}
	huge, err := plugindata.EncodeMap(plugindata.Map{
		"acme.huge": {SchemaVersion: 1, Fields: fields},
	})
	if err != nil {
		t.Fatalf("EncodeMap: %v", err)
	}
	base := interopHostData(t, store, "h-dup")
	duplicate := strings.Replace(base, `"pluginData":`,
		`"pluginData":`+string(huge)+`,"pluginData":`, 1)
	if duplicate == base {
		t.Fatal("the duplicate-property fixture did not inject a property")
	}
	// The earlier container is last-wins-invisible to the decoder but stays in
	// the raw bytes unless restore re-encodes the record.
	interopOverwriteHostData(t, store, "h-dup", duplicate)
	snapshot, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if err := store.Restore(ctx, snapshot); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	stored := interopHostData(t, store, "h-dup")
	// json.Unmarshal keeps only the last duplicate, so the decoded view alone
	// cannot prove the raw row was rewritten. Check the stored bytes: a guard
	// that only re-encodes for another reason would leave the huge shadowed
	// container in place and still pass a decoded-only check.
	if len(stored) > 2000 || strings.Contains(stored, "acme.huge") ||
		strings.Contains(stored, strings.Repeat("x", 1<<10)) {
		t.Fatalf("restore persisted the shadowed pluginData bytes: %d bytes",
			len(stored))
	}
	properties, err := plugindata.ScanPluginDataProperties([]byte(stored))
	if err != nil {
		t.Fatalf("ScanPluginDataProperties: %v", err)
	}
	if !properties.Found || len(properties.Keys) != 1 ||
		properties.Keys[0] != plugindata.JSONField {
		t.Fatalf("restored pluginData spellings = %+v", properties.Keys)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(stored), &top); err != nil {
		t.Fatalf("decode restored Host: %v", err)
	}
	if len(top["pluginData"]) > plugindata.MaxRecordBytes ||
		strings.Contains(string(top["pluginData"]), "acme.huge") {
		t.Fatalf("restore persisted the shadowed pluginData: %d bytes",
			len(top["pluginData"]))
	}
	host, err := store.Get(ctx, "h-dup")
	if err != nil {
		t.Fatalf("Get h-dup: %v", err)
	}
	if string(host.PluginData["acme.data"].Fields["name"]) != `"ok"` {
		t.Fatalf("restored Host = %+v", host.PluginData)
	}
}

func TestApplySyncQuarantinesOneOversizedRecordAndKeepsSiblings(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	fields := map[string]json.RawMessage{}
	for index := 0; index < 5; index++ {
		fields[string(rune('a'+index))] = interopSizedField("&", 14<<10)
	}
	payload := appsync.Payload{
		Hosts: []domainvault.Host{
			pluginHost("h-ok", plugindata.Map{
				"acme.good": namespace(1, map[string]string{"name": `"ok"`}),
			}),
			pluginHost("h-big", plugindata.Map{
				"acme.big": {SchemaVersion: 1, Fields: fields},
			}),
		},
		DataProfiles: []domainplugin.DataProfile{
			pluginProfile("p-ok", plugindata.Map{
				"acme.good": namespace(1, map[string]string{"name": `"ok"`}),
			}),
		},
	}
	if err := store.ApplySync(ctx, payload); err != nil {
		t.Fatalf("one oversized record aborted the sync: %v", err)
	}
	if got, err := store.Get(ctx, "h-ok"); err != nil ||
		string(got.PluginData["acme.good"].Fields["name"]) != `"ok"` {
		t.Fatalf("valid Host after sync = %+v, err = %v", got.PluginData, err)
	}
	big, err := store.Get(ctx, "h-big")
	if err != nil {
		t.Fatalf("oversized Host was not applied: %v", err)
	}
	if len(big.PluginData) != 0 {
		t.Fatalf("over-limit namespace stayed on the synced record: %+v", big.PluginData)
	}
	if profile, err := NewDataProfiles(store).Get(ctx, "p-ok"); err != nil ||
		len(profile.PluginData) != 1 {
		t.Fatalf("valid profile after sync = %+v, err = %v", profile.PluginData, err)
	}
	diagnostics, _ := store.PluginDataDiagnostics(ctx)
	if len(diagnostics) != 1 || diagnostics[0].RecordID != "h-big" ||
		diagnostics[0].PluginID != "acme.big" ||
		diagnostics[0].Reason != plugindata.ReasonRecordSize {
		t.Fatalf("sync diagnostics = %+v", diagnostics)
	}
}

func TestApplySyncPersistsDecoderQuarantinesAndClearsRepairedOnes(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	payload := appsync.Payload{
		Hosts: []domainvault.Host{pluginHost("h1", plugindata.Map{
			"acme.good": namespace(1, map[string]string{"name": `"ok"`}),
		})},
		PluginDataQuarantines: []appsync.RejectedPluginData{{
			Kind: plugindata.RecordHost, RecordID: "h1",
			Quarantine: plugindata.Quarantine{
				PluginID: "acme.bad", Reason: plugindata.ReasonNamespace,
				Detail: "namespace", Raw: []byte(`{"schemaVersion":1}`),
			},
		}},
	}
	if err := store.ApplySync(ctx, payload); err != nil {
		t.Fatalf("ApplySync: %v", err)
	}
	diagnostics, _ := store.PluginDataDiagnostics(ctx)
	if len(diagnostics) != 1 || diagnostics[0].RecordID != "h1" ||
		diagnostics[0].PluginID != "acme.bad" ||
		diagnostics[0].Reason != plugindata.ReasonNamespace {
		t.Fatalf("decoder quarantine was not persisted: %+v", diagnostics)
	}
	loaded, _ := store.Get(ctx, "h1")
	if len(loaded.PluginData) != 1 {
		t.Fatalf("valid namespace after sync = %+v", loaded.PluginData)
	}

	payload.PluginDataQuarantines = nil
	payload.Hosts = []domainvault.Host{pluginHost("h1", plugindata.Map{
		"acme.good": namespace(1, map[string]string{"name": `"ok"`}),
		"acme.bad":  namespace(1, map[string]string{"n": "1"}),
	})}
	if err := store.ApplySync(ctx, payload); err != nil {
		t.Fatalf("ApplySync repaired: %v", err)
	}
	if diagnostics, _ = store.PluginDataDiagnostics(ctx); len(diagnostics) != 0 {
		t.Fatalf("a repaired namespace kept its quarantine: %+v", diagnostics)
	}

	// A record-level decoder quarantine has an empty Plugin ID and still
	// persists once for its record.
	payload.Hosts = []domainvault.Host{pluginHost("h2", nil)}
	payload.PluginDataQuarantines = []appsync.RejectedPluginData{{
		Kind: plugindata.RecordHost, RecordID: "h2",
		Quarantine: plugindata.Quarantine{
			Reason: plugindata.ReasonRecord, Raw: []byte(`[1,2]`),
		},
	}}
	if err := store.ApplySync(ctx, payload); err != nil {
		t.Fatalf("ApplySync record quarantine: %v", err)
	}
	if diagnostics, _ = store.PluginDataDiagnostics(ctx); len(diagnostics) != 1 ||
		diagnostics[0].RecordID != "h2" || diagnostics[0].PluginID != "" ||
		diagnostics[0].Reason != plugindata.ReasonRecord {
		t.Fatalf("record-level quarantine = %+v", diagnostics)
	}
}

func TestPurgeFailsOnADamagedContainerAndNotifiesOnSuccess(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	if err := store.Save(ctx, pluginHost("h1", plugindata.Map{
		"acme.one":  namespace(1, map[string]string{"name": `"acme"`}),
		"other.two": namespace(1, map[string]string{"name": `"other"`}),
	})); err != nil {
		t.Fatalf("Save: %v", err)
	}
	interopOverwriteHostData(t, store, "h1", `{"id":"h1","pluginData":[1,2]}`)
	changes := 0
	store.SetChangeHook(func() { changes++ })
	err := store.PurgePluginData(ctx, "acme.one")
	if err == nil || !strings.Contains(err.Error(), "not an object") {
		t.Fatalf("a damaged container purge = %v, want an error", err)
	}
	if changes != 0 {
		t.Fatal("a failed purge notified the Vault change hook")
	}
	if _, err := store.Get(ctx, "h1"); err == nil {
		t.Fatal("the damaged row unexpectedly decodes")
	}

	interopOverwriteHostData(t, store, "h1",
		`{"id":"h1","label":"h1","pluginData":{`+
			`"acme.one":{"schemaVersion":1,"fields":{"name":"acme"}},`+
			`"other.two":{"schemaVersion":1,"fields":{"name":"other"}}}}`)
	if err := store.PurgePluginData(ctx, "acme.one"); err != nil {
		t.Fatalf("PurgePluginData: %v", err)
	}
	if changes != 1 {
		t.Fatalf("a successful purge notified %d times, want once", changes)
	}
	loaded, err := store.Get(ctx, "h1")
	if err != nil {
		t.Fatalf("Get after purge: %v", err)
	}
	if _, ok := loaded.PluginData["acme.one"]; ok {
		t.Fatalf("purged namespace survived: %+v", loaded.PluginData)
	}
	if _, ok := loaded.PluginData["other.two"]; !ok {
		t.Fatalf("purge removed another namespace: %+v", loaded.PluginData)
	}
}

func TestQuarantineRowsBoundAttackerControlledLabels(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	hugeKey := strings.Repeat("k", 1<<20)
	hugeField := strings.Repeat("f", 1<<20)
	invalid := plugindata.Namespace{SchemaVersion: 1,
		Fields: map[string]json.RawMessage{
			hugeField: json.RawMessage("1"),
		}}
	host := pluginHost("h1", plugindata.Map{
		"acme.data": invalid,
		hugeKey:     namespace(1, nil),
	})
	if err := store.Save(ctx, host); err != nil {
		t.Fatalf("Save: %v", err)
	}
	diagnostics, err := store.PluginDataDiagnostics(ctx)
	if err != nil {
		t.Fatalf("PluginDataDiagnostics: %v", err)
	}
	if len(diagnostics) != 2 {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
	for _, diagnostic := range diagnostics {
		if len(diagnostic.PluginID) > plugindata.MaxDiagnosticPluginIDBytes ||
			len(diagnostic.Detail) > plugindata.MaxDiagnosticDetailBytes {
			t.Fatalf("unbounded diagnostic labels = plugin %d, detail %d",
				len(diagnostic.PluginID), len(diagnostic.Detail))
		}
		if !utf8.ValidString(diagnostic.PluginID) ||
			!utf8.ValidString(diagnostic.Detail) {
			t.Fatalf("diagnostic labels are not valid UTF-8: %+v", diagnostic)
		}
	}
	var rows []pluginDataQuarantineSchema
	if err := store.db.ORM(ctx, func(tx *gorm.DB) error {
		return tx.Find(&rows).Error
	}); err != nil {
		t.Fatalf("read quarantine rows: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("quarantine rows = %d", len(rows))
	}
	for _, row := range rows {
		if len(row.Data) > plugindata.MaxRecordBytes {
			t.Fatalf("raw quarantine bytes = %d", len(row.Data))
		}
	}
}

func interopProfileData(t *testing.T, store *Store, id string) string {
	t.Helper()
	var data string
	var found bool
	if err := store.db.ORM(context.Background(), func(tx *gorm.DB) error {
		var err error
		data, found, err = loadDataRow(tx, &dataProfileSchema{},
			&dataProfileSchema{ID: id})
		return err
	}); err != nil {
		t.Fatalf("read DataProfile row: %v", err)
	}
	if !found {
		t.Fatalf("DataProfile %s is missing", id)
	}
	return data
}

func interopOverwriteProfileData(t *testing.T, store *Store, id, data string) {
	t.Helper()
	if err := store.db.ORM(context.Background(), func(tx *gorm.DB) error {
		return tx.Model(&dataProfileSchema{}).Where("id = ?", id).
			Update("data", data).Error
	}); err != nil {
		t.Fatalf("overwrite DataProfile %s: %v", id, err)
	}
}

func interopAssertSingleCanonicalProperty(t *testing.T, raw string) {
	t.Helper()
	properties, err := plugindata.ScanPluginDataProperties([]byte(raw))
	if err != nil {
		t.Fatalf("ScanPluginDataProperties: %v", err)
	}
	if !properties.Found || len(properties.Keys) != 1 ||
		properties.Keys[0] != plugindata.JSONField {
		t.Fatalf("stored pluginData spellings = %+v", properties.Keys)
	}
}

// TestApplySyncAfterMergeKeepsHTMLProviderFields replays the default smart
// merge path and then applies its payload. Before the shared no-HTML-escaping
// encoder, a 3,002-byte '<', '>', or '&' field reached ApplySync as 18,002
// escaped bytes and was quarantined as oversized-field.
func TestApplySyncAfterMergeKeepsHTMLProviderFields(t *testing.T) {
	for _, fill := range []string{"<", ">", "&"} {
		t.Run(fill, func(t *testing.T) {
			store, _ := openTestStore(t)
			ctx := context.Background()
			value := `"` + strings.Repeat(fill, 3000) + `"`
			host := pluginHost("h-html", plugindata.Map{
				"acme.data": namespace(1, map[string]string{"blob": value}),
			})
			merged, err := appsync.MergePayloads(nil,
				appsync.Payload{Hosts: []domainvault.Host{host}},
				appsync.Payload{Hosts: []domainvault.Host{host}},
				appsync.MergeOptions{})
			if err != nil {
				t.Fatalf("MergePayloads: %v", err)
			}
			if err := store.ApplySync(ctx, merged.Payload); err != nil {
				t.Fatalf("ApplySync: %v", err)
			}
			loaded, err := store.Get(ctx, "h-html")
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			stored := string(loaded.PluginData["acme.data"].Fields["blob"])
			if len(stored) != 3002 || stored != value {
				t.Fatalf("merged+applied field = %d bytes, want 3002", len(stored))
			}
			raw := interopHostData(t, store, "h-html")
			for _, escape := range []string{"\\u003c", "\\u003e", "\\u0026"} {
				if strings.Contains(raw, escape) {
					t.Fatalf("ApplySync stored HTML escape %s", escape)
				}
			}
			if diagnostics, err := store.PluginDataDiagnostics(ctx); err != nil ||
				len(diagnostics) != 0 {
				t.Fatalf("a merged legal field was quarantined: %+v, %v",
					diagnostics, err)
			}
		})
	}
}

func TestApplySyncAfterMergeKeepsExactLimitBoundaries(t *testing.T) {
	for _, fill := range []string{"<", ">", "&"} {
		t.Run(fill, func(t *testing.T) {
			store, _ := openTestStore(t)
			ctx := context.Background()
			fieldExact := pluginHost("h-field", plugindata.Map{
				"acme.data": {SchemaVersion: 1, Fields: map[string]json.RawMessage{
					"blob": interopSizedField(fill, plugindata.MaxFieldBytes),
				}},
			})
			recordExact := pluginHost("h-record", interopRecordAtLimit(t, fill, 0))
			payload := appsync.Payload{
				Hosts: []domainvault.Host{fieldExact, recordExact},
			}
			merged, err := appsync.MergePayloads(nil, payload, payload,
				appsync.MergeOptions{})
			if err != nil {
				t.Fatalf("MergePayloads: %v", err)
			}
			// ApplySync replaces the synced families, so both boundary records
			// must be applied in one payload.
			if err := store.ApplySync(ctx, merged.Payload); err != nil {
				t.Fatalf("ApplySync: %v", err)
			}
			field, err := store.Get(ctx, "h-field")
			if err != nil {
				t.Fatalf("Get field: %v", err)
			}
			if got := len(field.PluginData["acme.data"].Fields["blob"]); got != plugindata.MaxFieldBytes {
				t.Fatalf("16 KiB boundary applied as %d bytes", got)
			}
			record, err := store.Get(ctx, "h-record")
			if err != nil {
				t.Fatalf("Get record: %v", err)
			}
			size, err := plugindata.RecordBytes(record.PluginData)
			if err != nil {
				t.Fatalf("RecordBytes: %v", err)
			}
			if size != plugindata.MaxRecordBytes {
				t.Fatalf("64 KiB boundary applied as %d bytes", size)
			}
			if err := plugindata.Validate(record.PluginData); err != nil {
				t.Fatalf("applied boundary no longer validates: %v", err)
			}
			if diagnostics, _ := store.PluginDataDiagnostics(ctx); len(diagnostics) != 0 {
				t.Fatalf("a boundary record was quarantined: %+v", diagnostics)
			}
		})
	}
}

func TestRestoreCanonicalizesCaseVariantPluginDataSpellings(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	if err := store.Save(ctx, pluginHost("h1", plugindata.Map{
		"acme.data": namespace(1, map[string]string{"name": `"ok"`}),
	})); err != nil {
		t.Fatalf("Save Host: %v", err)
	}
	if err := NewDataProfiles(store).Save(ctx, pluginProfile("p1", plugindata.Map{
		"acme.data": namespace(1, map[string]string{"name": `"ok"`}),
	})); err != nil {
		t.Fatalf("Save DataProfile: %v", err)
	}

	inject := func(t *testing.T, base, spelling string) string {
		t.Helper()
		variant := strings.Replace(base, `"pluginData":{`,
			`"`+spelling+`":{"acme.bad":null,`, 1)
		if variant == base {
			t.Fatal("the case-variant fixture did not inject a property")
		}
		return variant
	}
	interopOverwriteHostData(t, store, "h1", inject(t,
		interopHostData(t, store, "h1"), "PluginData"))
	interopOverwriteProfileData(t, store, "p1", inject(t,
		interopProfileData(t, store, "p1"), "PLUGINDATA"))

	snapshot, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if err := store.Restore(ctx, snapshot); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	host, err := store.Get(ctx, "h1")
	if err != nil {
		t.Fatalf("Get Host: %v", err)
	}
	if len(host.PluginData) != 1 ||
		string(host.PluginData["acme.data"].Fields["name"]) != `"ok"` {
		t.Fatalf("restored Host pluginData = %+v", host.PluginData)
	}
	hostRaw := interopHostData(t, store, "h1")
	interopAssertSingleCanonicalProperty(t, hostRaw)
	if strings.Contains(hostRaw, "acme.bad") ||
		strings.Contains(hostRaw, `"PluginData"`) {
		t.Fatalf("restore kept a case-variant spelling or rejected bytes: %s",
			hostRaw)
	}
	profile, err := NewDataProfiles(store).Get(ctx, "p1")
	if err != nil {
		t.Fatalf("Get DataProfile: %v", err)
	}
	if len(profile.PluginData) != 1 ||
		string(profile.PluginData["acme.data"].Fields["name"]) != `"ok"` {
		t.Fatalf("restored profile pluginData = %+v", profile.PluginData)
	}
	profileRaw := interopProfileData(t, store, "p1")
	interopAssertSingleCanonicalProperty(t, profileRaw)
	if strings.Contains(profileRaw, "acme.bad") ||
		strings.Contains(profileRaw, `"PLUGINDATA"`) {
		t.Fatalf("restore kept a profile variant spelling: %s", profileRaw)
	}
	if _, err := store.ListPluginDataHosts(ctx); err != nil {
		t.Fatalf("ListPluginDataHosts after restore: %v", err)
	}
	if _, err := store.PluginDataRecords(ctx); err != nil {
		t.Fatalf("PluginDataRecords after restore: %v", err)
	}
	diagnostics, err := store.PluginDataDiagnostics(ctx)
	if err != nil {
		t.Fatalf("PluginDataDiagnostics: %v", err)
	}
	kinds := map[plugindata.RecordKind]bool{}
	for _, diagnostic := range diagnostics {
		if diagnostic.PluginID == "acme.bad" {
			kinds[diagnostic.RecordKind] = true
		}
	}
	if !kinds[plugindata.RecordHost] || !kinds[plugindata.RecordDataProfile] {
		t.Fatalf("variant quarantines = %+v", diagnostics)
	}
}

func TestRestoreQuarantinesCaseVariantNonObjectAndOverLimitContainers(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	for _, host := range []domainvault.Host{
		pluginHost("h-array", plugindata.Map{
			"acme.data": namespace(1, map[string]string{"name": `"ok"`}),
		}),
		pluginHost("h-big", plugindata.Map{
			"acme.data": namespace(1, map[string]string{"name": `"ok"`}),
		}),
	} {
		if err := store.Save(ctx, host); err != nil {
			t.Fatalf("Save %s: %v", host.ID, err)
		}
	}
	if err := NewDataProfiles(store).Save(ctx, pluginProfile("p-array",
		plugindata.Map{
			"acme.data": namespace(1, map[string]string{"name": `"ok"`}),
		})); err != nil {
		t.Fatalf("Save profile: %v", err)
	}

	interopOverwriteHostData(t, store, "h-array",
		`{"id":"h-array","label":"h-array","PluginData":[1]}`)
	big := plugindata.Map{}
	for index := 0; index < 5; index++ {
		big[fmt.Sprintf("acme.big%d", index)] = plugindata.Namespace{
			SchemaVersion: 1,
			Fields: map[string]json.RawMessage{
				"blob": interopSizedField("x", 14<<10),
			},
		}
	}
	encoded, err := plugindata.EncodeMap(big)
	if err != nil {
		t.Fatalf("EncodeMap: %v", err)
	}
	if len(encoded) <= plugindata.MaxRecordBytes {
		t.Fatalf("over-limit fixture = %d bytes", len(encoded))
	}
	interopOverwriteHostData(t, store, "h-big",
		`{"id":"h-big","label":"h-big","PluginData":`+string(encoded)+`}`)
	interopOverwriteProfileData(t, store, "p-array",
		`{"id":"p-array","provider":"plugin.acme.data.db","label":"p-array",`+
			`"mode":"read-only","PLUGINDATA":[1]}`)

	snapshot, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if err := store.Restore(ctx, snapshot); err != nil {
		t.Fatalf("a case-variant malformed container failed the restore: %v", err)
	}

	array, err := store.Get(ctx, "h-array")
	if err != nil {
		t.Fatalf("Get h-array: %v", err)
	}
	if len(array.PluginData) != 0 {
		t.Fatalf("non-object variant container was stored: %+v", array.PluginData)
	}
	arrayRaw := interopHostData(t, store, "h-array")
	if properties, err := plugindata.ScanPluginDataProperties([]byte(arrayRaw)); err != nil ||
		properties.Found {
		t.Fatalf("non-object variant container survived: %s, %v", arrayRaw, err)
	}

	bigHost, err := store.Get(ctx, "h-big")
	if err != nil {
		t.Fatalf("Get h-big: %v", err)
	}
	if len(bigHost.PluginData) == 0 || len(bigHost.PluginData) >= len(big) {
		t.Fatalf("over-limit variant stored %d of %d namespaces",
			len(bigHost.PluginData), len(big))
	}
	bigRaw := interopHostData(t, store, "h-big")
	interopAssertSingleCanonicalProperty(t, bigRaw)
	if strings.Contains(bigRaw, `"PluginData"`) {
		t.Fatalf("over-limit variant spelling survived: %s", bigRaw)
	}
	if size, err := plugindata.RecordBytes(bigHost.PluginData); err != nil ||
		size > plugindata.MaxRecordBytes {
		t.Fatalf("stored over-limit variant = %d bytes, %v", size, err)
	}
	if _, err := store.ListPluginDataHosts(ctx); err != nil {
		t.Fatalf("ListPluginDataHosts after restore: %v", err)
	}
	if _, err := store.PluginDataRecords(ctx); err != nil {
		t.Fatalf("PluginDataRecords after restore: %v", err)
	}
	if _, err := NewDataProfiles(store).Get(ctx, "p-array"); err != nil {
		t.Fatalf("Get p-array after restore: %v", err)
	}
	profileRaw := interopProfileData(t, store, "p-array")
	if properties, err := plugindata.ScanPluginDataProperties([]byte(profileRaw)); err != nil ||
		properties.Found {
		t.Fatalf("profile non-object variant survived: %s, %v", profileRaw, err)
	}
	diagnostics, err := store.PluginDataDiagnostics(ctx)
	if err != nil {
		t.Fatalf("PluginDataDiagnostics: %v", err)
	}
	reasons := map[string]plugindata.Reason{}
	for _, diagnostic := range diagnostics {
		key := string(diagnostic.RecordKind) + "/" + diagnostic.RecordID + "/" + diagnostic.PluginID
		reasons[key] = diagnostic.Reason
	}
	if reasons["host/h-array/"] != plugindata.ReasonRecord ||
		reasons["data_profile/p-array/"] != plugindata.ReasonRecord {
		t.Fatalf("non-object variant diagnostics = %+v", diagnostics)
	}
	recordSize := 0
	for _, diagnostic := range diagnostics {
		if diagnostic.RecordID == "h-big" &&
			diagnostic.Reason == plugindata.ReasonRecordSize {
			recordSize++
		}
	}
	if recordSize != len(big)-len(bigHost.PluginData) {
		t.Fatalf("over-limit diagnostics = %d, removed = %d",
			recordSize, len(big)-len(bigHost.PluginData))
	}
}

func TestPurgeCanonicalizesCaseVariantPluginDataSpellings(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	if err := store.Save(ctx, pluginHost("h1", plugindata.Map{
		"other.two": namespace(1, map[string]string{"name": `"other"`}),
	})); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// The purged namespace exists only in the shadowed first container. The
	// last-wins property does not contain it, but the purge must still remove
	// the variant spelling and write one canonical property.
	interopOverwriteHostData(t, store, "h1",
		`{"id":"h1","label":"h1",`+
			`"PluginData":{"acme.one":{"schemaVersion":1,"fields":{"name":"acme"}}},`+
			`"pluginData":{"other.two":{"schemaVersion":1,"fields":{"name":"other"}}}}`)
	if err := store.PurgePluginData(ctx, "acme.one"); err != nil {
		t.Fatalf("PurgePluginData shadowed variant: %v", err)
	}
	raw := interopHostData(t, store, "h1")
	if strings.Contains(raw, "acme.one") || strings.Contains(raw, `"PluginData"`) {
		t.Fatalf("purge kept the shadowed variant bytes: %s", raw)
	}
	interopAssertSingleCanonicalProperty(t, raw)
	host, err := store.Get(ctx, "h1")
	if err != nil {
		t.Fatalf("Get after purge: %v", err)
	}
	if _, ok := host.PluginData["other.two"]; !ok || len(host.PluginData) != 1 {
		t.Fatalf("purge damaged the surviving namespace: %+v", host.PluginData)
	}

	if err := store.Save(ctx, pluginHost("h2", plugindata.Map{
		"acme.one":  namespace(1, map[string]string{"name": `"acme"`}),
		"other.two": namespace(1, map[string]string{"name": `"other"`}),
	})); err != nil {
		t.Fatalf("Save h2: %v", err)
	}
	variant := strings.Replace(interopHostData(t, store, "h2"),
		`"pluginData":`, `"PLUGINDATA":`, 1)
	interopOverwriteHostData(t, store, "h2", variant)
	if err := store.PurgePluginData(ctx, "acme.one"); err != nil {
		t.Fatalf("PurgePluginData variant: %v", err)
	}
	rawH2 := interopHostData(t, store, "h2")
	if strings.Contains(rawH2, "acme.one") || strings.Contains(rawH2, `"PLUGINDATA"`) {
		t.Fatalf("purge kept a variant spelling: %s", rawH2)
	}
	interopAssertSingleCanonicalProperty(t, rawH2)
	h2, err := store.Get(ctx, "h2")
	if err != nil || len(h2.PluginData) != 1 {
		t.Fatalf("h2 after variant purge = %+v, %v", h2.PluginData, err)
	}

	if err := NewDataProfiles(store).Save(ctx, pluginProfile("p1", plugindata.Map{
		"acme.one":  namespace(1, map[string]string{"name": `"acme"`}),
		"other.two": namespace(1, map[string]string{"name": `"other"`}),
	})); err != nil {
		t.Fatalf("Save profile: %v", err)
	}
	profileVariant := strings.Replace(interopProfileData(t, store, "p1"),
		`"pluginData":`, `"PluginData":`, 1)
	interopOverwriteProfileData(t, store, "p1", profileVariant)
	if err := store.PurgePluginData(ctx, "acme.one"); err != nil {
		t.Fatalf("PurgePluginData profile variant: %v", err)
	}
	profileRaw := interopProfileData(t, store, "p1")
	if strings.Contains(profileRaw, "acme.one") ||
		strings.Contains(profileRaw, `"PluginData"`) {
		t.Fatalf("profile purge kept a variant spelling: %s", profileRaw)
	}
	interopAssertSingleCanonicalProperty(t, profileRaw)
}

func TestApplySyncBoundsAnAttackerControlledRecordID(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	hugeID := strings.Repeat("r", 1<<20)
	payload := appsync.Payload{
		// The Host itself stays ordinary: the attacker-controlled value here is
		// the quarantine record ID carried by an imported payload.
		Hosts: []domainvault.Host{pluginHost("h1", nil)},
		PluginDataQuarantines: []appsync.RejectedPluginData{{
			Kind: plugindata.RecordHost, RecordID: hugeID,
			Quarantine: plugindata.Quarantine{
				PluginID: "acme.bad", Reason: plugindata.ReasonNamespace,
				Raw: []byte(`{"schemaVersion":1}`),
			},
		}},
	}
	if err := store.ApplySync(ctx, payload); err != nil {
		t.Fatalf("ApplySync: %v", err)
	}
	diagnostics, err := store.PluginDataDiagnostics(ctx)
	if err != nil {
		t.Fatalf("PluginDataDiagnostics: %v", err)
	}
	if len(diagnostics) != 1 {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
	if len(diagnostics[0].RecordID) != plugindata.MaxDiagnosticRecordIDBytes ||
		diagnostics[0].RecordID != hugeID[:plugindata.MaxDiagnosticRecordIDBytes] {
		t.Fatalf("record ID = %d bytes, want a %d-byte prefix",
			len(diagnostics[0].RecordID), plugindata.MaxDiagnosticRecordIDBytes)
	}
	if !utf8.ValidString(diagnostics[0].RecordID) {
		t.Fatal("bounded record ID is not valid UTF-8")
	}
	raw, found, err := store.QuarantinedNamespace(ctx, plugindata.RecordHost,
		hugeID, "acme.bad")
	if err != nil || !found || len(raw) == 0 {
		t.Fatalf("bounded record ID lookup = %q, found = %v, err = %v",
			raw, found, err)
	}
	if err := store.ClearPluginDataQuarantine(ctx, plugindata.RecordHost,
		hugeID, "acme.bad"); err != nil {
		t.Fatalf("ClearPluginDataQuarantine: %v", err)
	}
	if diagnostics, _ = store.PluginDataDiagnostics(ctx); len(diagnostics) != 0 {
		t.Fatalf("bounded record ID quarantine was not cleared: %+v", diagnostics)
	}
}
