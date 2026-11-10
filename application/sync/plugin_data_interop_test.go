package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mslxl/natcatty/domain/model"
	"github.com/mslxl/natcatty/domain/model/plugindata"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	domainsync "github.com/mslxl/natcatty/domain/sync"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

func interopHostRecord(t *testing.T, id, label, pluginData string) string {
	t.Helper()
	host := domainvault.Host{
		ID: model.HostID(id), Label: label, Hostname: label + ".example.test",
		Port: 22, Username: "demo", Protocol: domainvault.ProtocolSSH,
		CreatedAt: time.UnixMilli(1000), UpdatedAt: time.UnixMilli(9000),
	}
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
	return string(out)
}

func interopProfileRecord(t *testing.T, id, pluginData string) string {
	t.Helper()
	top := map[string]json.RawMessage{
		"id":       json.RawMessage(`"` + id + `"`),
		"provider": json.RawMessage(`"plugin.acme.data.db"`),
		"label":    json.RawMessage(`"` + id + `"`),
		"mode":     json.RawMessage(`"read-only"`),
	}
	if pluginData != "" {
		top["pluginData"] = json.RawMessage(pluginData)
	}
	out, err := domainsync.MarshalJSON(top)
	if err != nil {
		t.Fatalf("encode profile: %v", err)
	}
	return string(out)
}

func interopPlaintext(t *testing.T, hosts, profiles []string) []byte {
	t.Helper()
	parts := make([]string, 0, len(hosts)+len(profiles)+1)
	parts = append(parts, `"hosts":[`+strings.Join(hosts, ",")+`]`)
	parts = append(parts, `"dataProfiles":[`+strings.Join(profiles, ",")+`]`)
	parts = append(parts, `"syncedAt":9000`)
	return []byte(`{` + strings.Join(parts, ",") + `}`)
}

func interopSizedField(fill string, size int) json.RawMessage {
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
	value := plugindata.Map{"acme.data": {SchemaVersion: 1, Fields: fields}}
	size := 0
	encoded, err := plugindata.EncodeMap(value)
	if err != nil {
		t.Fatalf("EncodeMap: %v", err)
	}
	size = len(encoded)
	fields["e"] = interopSizedField(fill, len(fields["e"])-2+
		plugindata.MaxRecordBytes+over-size+2)
	encoded, err = plugindata.EncodeMap(value)
	if err != nil {
		t.Fatalf("EncodeMap: %v", err)
	}
	if len(encoded) != plugindata.MaxRecordBytes+over {
		t.Fatalf("record construction = %d bytes, want %d",
			len(encoded), plugindata.MaxRecordBytes+over)
	}
	return value
}

func interopQuarantine(t *testing.T, payload Payload, kind plugindata.RecordKind,
	recordID, pluginID string) (plugindata.Quarantine, bool) {
	t.Helper()
	for _, rejected := range payload.PluginDataQuarantines {
		if rejected.Kind == kind && rejected.RecordID == recordID &&
			rejected.Quarantine.PluginID == pluginID {
			return rejected.Quarantine, true
		}
	}
	return plugindata.Quarantine{}, false
}

func TestDecodePayloadQuarantinesEachMalformedNamespaceIndividually(t *testing.T) {
	badPluginData := `{` +
		`"acme.null":null,` +
		`"acme.string":"oops",` +
		`"acme.array":[1],` +
		`"acme.fields":{"schemaVersion":1,"fields":[]},` +
		`"acme.version":{"schemaVersion":1.5,"fields":{}},` +
		`"acme.utf8":{"schemaVersion":1,"fields":{"name":"` + "\xff" + `"}}` +
		`}`
	plaintext := interopPlaintext(t,
		[]string{
			interopHostRecord(t, "h-valid", "valid",
				`{"acme.good":{"schemaVersion":1,"fields":{"name":"ok"}}}`),
			interopHostRecord(t, "h-bad", "bad", badPluginData),
			interopHostRecord(t, "h-container", "container", `[1,2]`),
			interopHostRecord(t, "h-null", "null", `null`),
		},
		[]string{
			interopProfileRecord(t, "p-valid",
				`{"acme.good":{"schemaVersion":1,"fields":{"name":"ok"}}}`),
			interopProfileRecord(t, "p-bad",
				`{"acme.bad":{"schemaVersion":1,"fields":[]}}`),
		})
	payload, err := decodePayloadBytes(plaintext)
	if err != nil {
		t.Fatalf("one malformed namespace rejected the whole payload: %v", err)
	}
	if len(payload.Hosts) != 4 || len(payload.DataProfiles) != 2 {
		t.Fatalf("decoded hosts = %d, profiles = %d",
			len(payload.Hosts), len(payload.DataProfiles))
	}
	byID := map[string]domainvault.Host{}
	for _, host := range payload.Hosts {
		byID[string(host.ID)] = host
	}
	valid, ok := byID["h-valid"]
	if !ok || string(valid.PluginData["acme.good"].Fields["name"]) != `"ok"` {
		t.Fatalf("valid sibling = %+v", valid.PluginData)
	}
	bad := byID["h-bad"]
	if len(bad.PluginData) != 0 || bad.Label != "bad" {
		t.Fatalf("malformed record = %+v", bad)
	}
	if container := byID["h-container"]; container.Label != "container" {
		t.Fatalf("record-level rejection lost the record: %+v", container)
	}
	if null := byID["h-null"]; len(null.PluginData) != 0 {
		t.Fatalf("null pluginData was not empty: %+v", null.PluginData)
	}
	for _, pluginID := range []string{
		"acme.null", "acme.string", "acme.array",
		"acme.fields", "acme.version", "acme.utf8",
	} {
		entry, found := interopQuarantine(t, payload, plugindata.RecordHost, "h-bad", pluginID)
		if !found {
			t.Fatalf("namespace %s was not quarantined: %+v", pluginID,
				payload.PluginDataQuarantines)
		}
		if len(entry.PluginID) > plugindata.MaxDiagnosticPluginIDBytes ||
			len(entry.Detail) > plugindata.MaxDiagnosticDetailBytes ||
			len(entry.Raw) > plugindata.MaxRecordBytes {
			t.Fatalf("quarantine %s is unbounded: %+v", pluginID, entry)
		}
	}
	if entry, found := interopQuarantine(t, payload, plugindata.RecordHost,
		"h-container", ""); !found || entry.Reason != plugindata.ReasonRecord {
		t.Fatalf("record-level quarantine = %+v, found = %v", entry, found)
	}
	if entry, found := interopQuarantine(t, payload, plugindata.RecordDataProfile,
		"p-bad", "acme.bad"); !found || entry.Reason != plugindata.ReasonJSON {
		t.Fatalf("profile quarantine = %+v, found = %v", entry, found)
	}
}

func TestDecodePayloadKeepsHTMLCharactersInProviderFields(t *testing.T) {
	field := func(fill string) string {
		return `"` + strings.Repeat(fill, 3000) + `"`
	}
	pluginData := `{"acme.data":{"schemaVersion":1,"fields":{` +
		`"less":` + field("<") + `,` +
		`"greater":` + field(">") + `,` +
		`"amp":` + field("&") + `,` +
		`"four":` + field("&") + `}}}`
	payload, err := decodePayloadBytes(interopPlaintext(t, []string{
		interopHostRecord(t, "h-html", "html", pluginData),
	}, nil))
	if err != nil {
		t.Fatalf("decodePayloadBytes: %v", err)
	}
	stored := payload.Hosts[0].PluginData["acme.data"].Fields
	for key, fill := range map[string]string{
		"less": "<", "greater": ">", "amp": "&", "four": "&",
	} {
		want := `"` + strings.Repeat(fill, 3000) + `"`
		if string(stored[key]) != want {
			t.Fatalf("%s field = %d bytes, want %d unchanged bytes",
				key, len(stored[key]), len(want))
		}
	}
	reencoded, err := domainsync.MarshalJSON(payload)
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	for _, escape := range []string{"\\u003c", "\\u003e", "\\u0026"} {
		if bytes.Contains(reencoded, []byte(escape)) {
			t.Fatalf("sync payload expanded a provider field to %s: %d bytes",
				escape, len(reencoded))
		}
	}
}

func TestDecodePayloadEnforcesFieldAndRecordLimitsOnStoredBytes(t *testing.T) {
	fieldExact := `{"acme.data":{"schemaVersion":1,"fields":{"blob":` +
		string(interopSizedField("<", plugindata.MaxFieldBytes)) + `}}}`
	fieldOver := `{"acme.data":{"schemaVersion":1,"fields":{"blob":` +
		string(interopSizedField("&", plugindata.MaxFieldBytes+1)) + `}}}`
	recordExact, err := plugindata.EncodeMap(interopRecordAtLimit(t, ">", 0))
	if err != nil {
		t.Fatalf("EncodeMap exact: %v", err)
	}
	recordOver, err := plugindata.EncodeMap(interopRecordAtLimit(t, ">", 1))
	if err != nil {
		t.Fatalf("EncodeMap over: %v", err)
	}
	plaintext := interopPlaintext(t, []string{
		interopHostRecord(t, "h-field-exact", "field-exact", fieldExact),
		interopHostRecord(t, "h-field-over", "field-over", fieldOver),
		interopHostRecord(t, "h-record-exact", "record-exact", string(recordExact)),
		interopHostRecord(t, "h-record-over", "record-over", string(recordOver)),
	}, nil)
	payload, err := decodePayloadBytes(plaintext)
	if err != nil {
		t.Fatalf("decodePayloadBytes: %v", err)
	}
	byID := map[string]domainvault.Host{}
	for _, host := range payload.Hosts {
		byID[string(host.ID)] = host
	}
	if got := byID["h-field-exact"].PluginData["acme.data"].Fields["blob"]; len(got) != plugindata.MaxFieldBytes {
		t.Fatalf("exact field limit = %d bytes", len(got))
	}
	if len(byID["h-field-over"].PluginData) != 0 {
		t.Fatalf("one-byte-over field was accepted: %+v",
			byID["h-field-over"].PluginData)
	}
	if len(byID["h-record-exact"].PluginData) != 1 {
		t.Fatalf("exact record limit was quarantined: %+v",
			byID["h-record-exact"].PluginData)
	}
	if len(byID["h-record-over"].PluginData) != 0 {
		t.Fatalf("one-byte-over record was accepted: %+v",
			byID["h-record-over"].PluginData)
	}
	if entry, found := interopQuarantine(t, payload, plugindata.RecordHost,
		"h-field-over", "acme.data"); !found ||
		entry.Reason != plugindata.ReasonFieldSize {
		t.Fatalf("over-field quarantine = %+v, found = %v", entry, found)
	}
	if entry, found := interopQuarantine(t, payload, plugindata.RecordHost,
		"h-record-over", "acme.data"); !found ||
		entry.Reason != plugindata.ReasonRecordSize {
		t.Fatalf("over-record quarantine = %+v, found = %v", entry, found)
	}
}

func TestConvergentMaterializationDoesNotExpandProviderFields(t *testing.T) {
	field := func(fill string) json.RawMessage {
		return json.RawMessage(`"` + strings.Repeat(fill, 3000) + `"`)
	}
	host := domainvault.Host{
		ID: "h-html", Label: "html", Hostname: "html.example.test", Port: 22,
		Username: "demo", Protocol: domainvault.ProtocolSSH,
		CreatedAt: time.UnixMilli(1000), UpdatedAt: time.UnixMilli(9000),
		PluginData: plugindata.Map{"acme.data": {
			SchemaVersion: 1,
			Fields: map[string]json.RawMessage{
				"less": field("<"), "greater": field(">"), "amp": field("&"),
			},
		}},
	}
	payload := Payload{Hosts: []domainvault.Host{host}, SyncedAt: 9000}
	snapshot, err := payloadSnapshot(payload)
	if err != nil {
		t.Fatalf("payloadSnapshot: %v", err)
	}
	state, err := domainsync.CreateConvergentStateFromSnapshot(snapshot, "device-a", 1000)
	if err != nil {
		t.Fatalf("CreateConvergentStateFromSnapshot: %v", err)
	}
	materialized, err := payloadFromState(state, nil, 2000, "device-a")
	if err != nil {
		t.Fatalf("payloadFromState: %v", err)
	}
	if len(materialized.Hosts) != 1 {
		t.Fatalf("hosts = %+v", materialized.Hosts)
	}
	stored := materialized.Hosts[0].PluginData["acme.data"].Fields
	for key, fill := range map[string]string{
		"less": "<", "greater": ">", "amp": "&",
	} {
		want := `"` + strings.Repeat(fill, 3000) + `"`
		if string(stored[key]) != want {
			t.Fatalf("v2 %s field = %d bytes, want %d unescaped bytes",
				key, len(stored[key]), len(want))
		}
	}
}

func TestSyncDownloadsValidSiblingsDespiteMalformedNamespaces(t *testing.T) {
	service, remote, vault, _, _, _ := testService(t)
	if _, err := service.Sync(context.Background(), TriggerManual); err != nil {
		t.Fatalf("initial upload: %v", err)
	}
	hosts := []string{
		interopHostRecord(t, "h-valid", "cloud-valid",
			`{"acme.good":{"schemaVersion":1,"fields":{"name":"ok"}}}`),
		interopHostRecord(t, "h-bad", "cloud-bad", `{"acme.bad":null}`),
	}
	raw := interopPlaintext(t, hosts, nil)
	envelope, err := domainsync.EncryptPayload(json.RawMessage(raw), "secret",
		domainsync.EncryptOptions{
			Version: 1, UpdatedAt: 9000, DeviceID: "peer", DeviceName: "Peer",
			AppVersion: "1.0", KDFIterations: testKDFIterations,
		})
	if err != nil {
		t.Fatalf("EncryptPayload: %v", err)
	}
	content, err := domainsync.MarshalFile(envelope)
	if err != nil {
		t.Fatalf("MarshalFile: %v", err)
	}
	remote.mu.Lock()
	remote.exists = true
	remote.file = RemoteFile{ID: "gist-1", ETag: "etag-malformed", UpdatedAt: 9000, Content: content}
	remote.mu.Unlock()

	result, err := service.Sync(context.Background(), TriggerManual)
	if err != nil {
		t.Fatalf("a malformed remote namespace aborted the sync: %v", err)
	}
	if !result.Downloaded {
		t.Fatalf("result = %+v, want a download", result)
	}
	vault.mu.Lock()
	applied := clonePayload(vault.payload)
	vault.mu.Unlock()
	if len(applied.Hosts) != 2 {
		t.Fatalf("applied hosts = %+v", applied.Hosts)
	}
	byID := map[string]domainvault.Host{}
	for _, host := range applied.Hosts {
		byID[string(host.ID)] = host
	}
	if string(byID["h-valid"].PluginData["acme.good"].Fields["name"]) != `"ok"` {
		t.Fatalf("valid sibling was not applied: %+v", byID["h-valid"].PluginData)
	}
	if len(byID["h-bad"].PluginData) != 0 {
		t.Fatalf("malformed namespace stayed on the record: %+v", byID["h-bad"].PluginData)
	}
	if _, found := interopQuarantine(t, applied, plugindata.RecordHost,
		"h-bad", "acme.bad"); !found {
		t.Fatalf("decoder quarantine was dropped before Apply: %+v",
			applied.PluginDataQuarantines)
	}
}

func interopHostWithPluginData(id, label string, data plugindata.Map) domainvault.Host {
	return domainvault.Host{
		ID: model.HostID(id), Label: label, Hostname: label + ".example.test",
		Port: 22, Username: "demo", Protocol: domainvault.ProtocolSSH,
		CreatedAt: time.UnixMilli(1000), UpdatedAt: time.UnixMilli(9000),
		PluginData: data,
	}
}

func interopFieldPluginData(value json.RawMessage) plugindata.Map {
	return plugindata.Map{"acme.data": {
		SchemaVersion: 1,
		Fields:        map[string]json.RawMessage{"blob": value},
	}}
}

// TestMergePayloadsKeepsHTMLCharactersInProviderFields pins the merge paths
// that used to round-trip pluginData through an HTML-escaping json.Marshal. A
// 3,002-byte '<', '>', or '&' field came back as its 18,002-byte \uXXXX
// spelling, and ApplySync then quarantined a legal provider field.
func TestMergePayloadsKeepsHTMLCharactersInProviderFields(t *testing.T) {
	for _, fill := range []string{"<", ">", "&"} {
		t.Run(fill, func(t *testing.T) {
			value := json.RawMessage(`"` + strings.Repeat(fill, 3000) + `"`)
			host := interopHostWithPluginData("h-html", "same",
				interopFieldPluginData(value))
			identical, err := MergePayloads(nil,
				Payload{Hosts: []domainvault.Host{host}},
				Payload{Hosts: []domainvault.Host{host}}, MergeOptions{})
			if err != nil {
				t.Fatalf("identical MergePayloads: %v", err)
			}
			if len(identical.Payload.Hosts) != 1 {
				t.Fatalf("identical merge hosts = %+v", identical.Payload.Hosts)
			}
			assertMergedPluginField(t, identical.Payload.Hosts[0], value)
			assertPayloadHasNoHTMLEscapes(t, identical.Payload)

			base := interopHostWithPluginData("h-html", "same",
				interopFieldPluginData(json.RawMessage(`"base"`)))
			remote := interopHostWithPluginData("h-html", "same",
				interopFieldPluginData(
					json.RawMessage(`"`+strings.Repeat("x", 3000)+`"`)))
			conflict, err := MergePayloads(
				&Payload{Hosts: []domainvault.Host{base}},
				Payload{Hosts: []domainvault.Host{host}},
				Payload{Hosts: []domainvault.Host{remote}}, MergeOptions{})
			if err != nil {
				t.Fatalf("conflict MergePayloads: %v", err)
			}
			if len(conflict.Conflicts) != 1 || conflict.Conflicts[0].EntityID != "h-html" {
				t.Fatalf("pluginData clash conflicts = %+v", conflict.Conflicts)
			}
			assertMergedPluginField(t, conflict.Payload.Hosts[0], value)
			assertPayloadHasNoHTMLEscapes(t, conflict.Payload)
		})
	}
}

func assertMergedPluginField(t *testing.T, host domainvault.Host, want json.RawMessage) {
	t.Helper()
	got := host.PluginData["acme.data"].Fields["blob"]
	if len(got) != 3002 || string(got) != string(want) {
		t.Fatalf("merged field = %d bytes, want %d: %.60s",
			len(got), len(want), got)
	}
}

func assertPayloadHasNoHTMLEscapes(t *testing.T, payload Payload) {
	t.Helper()
	data, err := domainsync.MarshalJSON(payload)
	if err != nil {
		t.Fatalf("MarshalJSON payload: %v", err)
	}
	for _, escape := range []string{"\\u003c", "\\u003e", "\\u0026"} {
		if bytes.Contains(data, []byte(escape)) {
			t.Fatalf("merged payload expanded HTML to %s: %d bytes",
				escape, len(data))
		}
	}
}

// TestMergePayloadsKeepsExactFieldAndRecordLimitBoundaries checks that the
// merge path neither expands a field at the 16 KiB boundary nor a record at the
// 64 KiB boundary, on which an escaped byte would push either over the limit
// and make ApplySync quarantine it.
func TestMergePayloadsKeepsExactFieldAndRecordLimitBoundaries(t *testing.T) {
	for _, fill := range []string{"<", ">", "&"} {
		t.Run(fill, func(t *testing.T) {
			field := interopSizedField(fill, plugindata.MaxFieldBytes)
			atFieldLimit := interopHostWithPluginData("h-field", "field",
				interopFieldPluginData(field))
			result, err := MergePayloads(nil,
				Payload{Hosts: []domainvault.Host{atFieldLimit}},
				Payload{Hosts: []domainvault.Host{atFieldLimit}}, MergeOptions{})
			if err != nil {
				t.Fatalf("field-limit merge: %v", err)
			}
			stored := result.Payload.Hosts[0].PluginData["acme.data"].Fields["blob"]
			if len(stored) != plugindata.MaxFieldBytes {
				t.Fatalf("16 KiB boundary merged to %d bytes", len(stored))
			}
			if err := plugindata.Validate(result.Payload.Hosts[0].PluginData); err != nil {
				t.Fatalf("16 KiB boundary no longer validates: %v", err)
			}

			record := interopRecordAtLimit(t, fill, 0)
			atRecordLimit := interopHostWithPluginData("h-record", "record", record)
			result, err = MergePayloads(nil,
				Payload{Hosts: []domainvault.Host{atRecordLimit}},
				Payload{Hosts: []domainvault.Host{atRecordLimit}}, MergeOptions{})
			if err != nil {
				t.Fatalf("record-limit merge: %v", err)
			}
			merged := result.Payload.Hosts[0].PluginData
			if err := plugindata.Validate(merged); err != nil {
				t.Fatalf("64 KiB boundary no longer validates: %v", err)
			}
			encoded, err := plugindata.EncodeMap(merged)
			if err != nil {
				t.Fatalf("EncodeMap: %v", err)
			}
			if len(encoded) != plugindata.MaxRecordBytes {
				t.Fatalf("64 KiB boundary merged to %d bytes", len(encoded))
			}
			assertPayloadHasNoHTMLEscapes(t, result.Payload)
		})
	}
}

// interopVariantRecord appends a top-level pluginData spelling to an existing
// compact record.
func interopVariantRecord(t *testing.T, record, spelling, container string) string {
	t.Helper()
	if !strings.HasSuffix(record, "}") {
		t.Fatalf("record is not an object: %s", record)
	}
	return record[:len(record)-1] + `,"` + spelling + `":` + container + `}`
}

func TestDecodePayloadHandlesCaseVariantPluginDataSpellings(t *testing.T) {
	variantHost := interopVariantRecord(t,
		interopHostRecord(t, "h-var", "variant", ""), "PluginData",
		`{"acme.bad":null,"acme.good":{"schemaVersion":1,"fields":{"name":"ok"}}}`)
	mixedHost := interopVariantRecord(t,
		interopVariantRecord(t, interopHostRecord(t, "h-mixed", "mixed", ""),
			"PluginData", `{"acme.shadow":null}`),
		"plugindata",
		`{"acme.good":{"schemaVersion":1,"fields":{"name":"last"}}}`)
	arrayHost := interopVariantRecord(t,
		interopHostRecord(t, "h-array", "array", ""), "PLUGINDATA", `[1]`)
	validHost := interopHostRecord(t, "h-valid", "valid",
		`{"acme.good":{"schemaVersion":1,"fields":{"name":"ok"}}}`)

	variantProfile := interopVariantRecord(t,
		interopProfileRecord(t, "p-var", ""), "PluginData",
		`{"acme.bad":null,"acme.good":{"schemaVersion":1,"fields":{"name":"ok"}}}`)
	arrayProfile := interopVariantRecord(t,
		interopProfileRecord(t, "p-array", ""), "PLUGINDATA", `[1]`)
	validProfile := interopProfileRecord(t, "p-valid",
		`{"acme.good":{"schemaVersion":1,"fields":{"name":"ok"}}}`)

	payload, err := decodePayloadBytes(interopPlaintext(t,
		[]string{variantHost, mixedHost, arrayHost, validHost},
		[]string{variantProfile, arrayProfile, validProfile}))
	if err != nil {
		t.Fatalf("a case-variant pluginData spelling rejected the payload: %v", err)
	}
	if len(payload.Hosts) != 4 || len(payload.DataProfiles) != 3 {
		t.Fatalf("decoded hosts = %d, profiles = %d",
			len(payload.Hosts), len(payload.DataProfiles))
	}
	hosts := map[string]domainvault.Host{}
	for _, host := range payload.Hosts {
		hosts[string(host.ID)] = host
	}
	variant := hosts["h-var"]
	if variant.Label != "variant" ||
		string(variant.PluginData["acme.good"].Fields["name"]) != `"ok"` ||
		len(variant.PluginData) != 1 {
		t.Fatalf("variant Host = %+v", variant)
	}
	if entry, found := interopQuarantine(t, payload, plugindata.RecordHost,
		"h-var", "acme.bad"); !found || entry.Reason != plugindata.ReasonNamespace {
		t.Fatalf("variant quarantine = %+v, found = %v", entry, found)
	}
	mixed := hosts["h-mixed"]
	if len(mixed.PluginData) != 1 ||
		string(mixed.PluginData["acme.good"].Fields["name"]) != `"last"` {
		t.Fatalf("mixed-spelling Host = %+v", mixed.PluginData)
	}
	if _, found := interopQuarantine(t, payload, plugindata.RecordHost,
		"h-mixed", "acme.shadow"); found {
		t.Fatal("the shadowed container was applied")
	}
	if array := hosts["h-array"]; len(array.PluginData) != 0 ||
		array.Label != "array" {
		t.Fatalf("array-container Host = %+v", array)
	}
	if entry, found := interopQuarantine(t, payload, plugindata.RecordHost,
		"h-array", ""); !found || entry.Reason != plugindata.ReasonRecord {
		t.Fatalf("record-level variant quarantine = %+v, found = %v", entry, found)
	}
	if valid := hosts["h-valid"]; len(valid.PluginData) != 1 {
		t.Fatalf("canonical valid sibling = %+v", valid.PluginData)
	}
	profiles := map[string]domainplugin.DataProfile{}
	for _, profile := range payload.DataProfiles {
		profiles[profile.ID] = profile
	}
	if len(profiles["p-var"].PluginData) != 1 {
		t.Fatalf("variant profile = %+v", profiles["p-var"].PluginData)
	}
	if entry, found := interopQuarantine(t, payload, plugindata.RecordDataProfile,
		"p-var", "acme.bad"); !found {
		t.Fatalf("profile variant quarantine = %+v", entry)
	}
	if len(profiles["p-array"].PluginData) != 0 {
		t.Fatalf("array-container profile = %+v", profiles["p-array"].PluginData)
	}
	if entry, found := interopQuarantine(t, payload, plugindata.RecordDataProfile,
		"p-array", ""); !found || entry.Reason != plugindata.ReasonRecord {
		t.Fatalf("profile record-level quarantine = %+v, found = %v", entry, found)
	}
	if len(profiles["p-valid"].PluginData) != 1 {
		t.Fatalf("canonical valid profile = %+v", profiles["p-valid"].PluginData)
	}
}

func TestDecodePayloadQuarantinesOverLimitCaseVariantContainer(t *testing.T) {
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
		t.Fatalf("fixture = %d bytes, want above %d",
			len(encoded), plugindata.MaxRecordBytes)
	}
	host := interopVariantRecord(t,
		interopHostRecord(t, "h-big", "big", ""), "PluginData", string(encoded))
	payload, err := decodePayloadBytes(interopPlaintext(t, []string{host}, nil))
	if err != nil {
		t.Fatalf("over-limit variant rejected the payload: %v", err)
	}
	if len(payload.Hosts) != 1 || payload.Hosts[0].Label != "big" {
		t.Fatalf("hosts = %+v", payload.Hosts)
	}
	stored := payload.Hosts[0].PluginData
	if len(stored) == 0 || len(stored) >= len(big) {
		t.Fatalf("over-limit container stored %d of %d namespaces",
			len(stored), len(big))
	}
	size, err := plugindata.RecordBytes(stored)
	if err != nil {
		t.Fatalf("RecordBytes: %v", err)
	}
	if size > plugindata.MaxRecordBytes {
		t.Fatalf("stored variant container = %d bytes", size)
	}
	quarantined := 0
	for _, rejected := range payload.PluginDataQuarantines {
		if rejected.Kind == plugindata.RecordHost && rejected.RecordID == "h-big" {
			quarantined++
			if rejected.Quarantine.Reason != plugindata.ReasonRecordSize {
				t.Fatalf("quarantine = %+v", rejected.Quarantine)
			}
		}
	}
	if quarantined != len(big)-len(stored) {
		t.Fatalf("quarantines = %d, removed = %d", quarantined, len(big)-len(stored))
	}
}

func TestSyncDownloadsValidSiblingsFromCaseVariantPluginData(t *testing.T) {
	service, remote, vault, _, _, _ := testService(t)
	if _, err := service.Sync(context.Background(), TriggerManual); err != nil {
		t.Fatalf("initial upload: %v", err)
	}
	variant := interopVariantRecord(t,
		interopHostRecord(t, "h-var", "cloud-var", ""), "PluginData",
		`{"acme.bad":null,"acme.good":{"schemaVersion":1,"fields":{"name":"ok"}}}`)
	valid := interopHostRecord(t, "h-valid", "cloud-valid",
		`{"acme.good":{"schemaVersion":1,"fields":{"name":"ok"}}}`)
	raw := interopPlaintext(t, []string{variant, valid}, nil)
	envelope, err := domainsync.EncryptPayload(json.RawMessage(raw), "secret",
		domainsync.EncryptOptions{
			Version: 1, UpdatedAt: 9000, DeviceID: "peer", DeviceName: "Peer",
			AppVersion: "1.0", KDFIterations: testKDFIterations,
		})
	if err != nil {
		t.Fatalf("EncryptPayload: %v", err)
	}
	content, err := domainsync.MarshalFile(envelope)
	if err != nil {
		t.Fatalf("MarshalFile: %v", err)
	}
	remote.mu.Lock()
	remote.exists = true
	remote.file = RemoteFile{
		ID: "gist-1", ETag: "etag-variant", UpdatedAt: 9000, Content: content,
	}
	remote.mu.Unlock()

	result, err := service.Sync(context.Background(), TriggerManual)
	if err != nil {
		t.Fatalf("a case-variant remote namespace aborted the sync: %v", err)
	}
	if !result.Downloaded {
		t.Fatalf("result = %+v, want a download", result)
	}
	vault.mu.Lock()
	applied := clonePayload(vault.payload)
	vault.mu.Unlock()
	byID := map[string]domainvault.Host{}
	for _, host := range applied.Hosts {
		byID[string(host.ID)] = host
	}
	if string(byID["h-var"].PluginData["acme.good"].Fields["name"]) != `"ok"` ||
		len(byID["h-var"].PluginData) != 1 {
		t.Fatalf("variant valid namespace was not applied: %+v",
			byID["h-var"].PluginData)
	}
	if string(byID["h-valid"].PluginData["acme.good"].Fields["name"]) != `"ok"` {
		t.Fatalf("canonical sibling was not applied: %+v",
			byID["h-valid"].PluginData)
	}
	if _, found := interopQuarantine(t, applied, plugindata.RecordHost,
		"h-var", "acme.bad"); !found {
		t.Fatalf("variant quarantine was not applied: %+v",
			applied.PluginDataQuarantines)
	}
}

// stateWithRecordPluginData replaces one materialized record's pluginData
// container with raw bytes and applies the edited snapshot as an ordinary
// causal write. The result is a valid v2 state whose pluginData register holds
// a value the structural subset can reject, exactly like a replica written by
// another client.
func stateWithRecordPluginData(t *testing.T, state domainsync.ConvergentState,
	node string, now int64, family, recordID, container string) domainsync.ConvergentState {

	t.Helper()
	snapshot, err := domainsync.MaterializeConvergentSnapshot(state)
	if err != nil {
		t.Fatalf("MaterializeConvergentSnapshot: %v", err)
	}
	records := snapshot.Families[family]
	found := false
	for index := range records {
		if records[index].ID != recordID {
			continue
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(records[index].Value, &fields); err != nil {
			t.Fatalf("record %s/%s: %v", family, recordID, err)
		}
		value, err := domainsync.CanonicalValue(json.RawMessage(container))
		if err != nil {
			t.Fatalf("CanonicalValue container: %v", err)
		}
		fields["pluginData"] = value
		encoded, err := domainsync.MarshalJSON(fields)
		if err != nil {
			t.Fatalf("encode fields: %v", err)
		}
		records[index].Value, err = domainsync.CanonicalValue(encoded)
		if err != nil {
			t.Fatalf("CanonicalValue record: %v", err)
		}
		found = true
	}
	if !found {
		t.Fatalf("state has no %s record %q", family, recordID)
	}
	snapshot.Families[family] = records
	next, _, err := domainsync.ApplyConvergentSnapshot(state, snapshot, node, now, false)
	if err != nil {
		t.Fatalf("ApplyConvergentSnapshot: %v", err)
	}
	return next
}

func interopGoodNamespace(name string) string {
	return `{"schemaVersion":1,"fields":{"name":"` + name + `"}}`
}

func interopLastApplied(t *testing.T, vault *fakeVault) Payload {
	t.Helper()
	vault.mu.Lock()
	defer vault.mu.Unlock()
	if len(vault.applied) == 0 {
		t.Fatal("the Vault was never applied")
	}
	return clonePayload(vault.applied[len(vault.applied)-1])
}

func interopAppliedCount(vault *fakeVault) int {
	vault.mu.Lock()
	defer vault.mu.Unlock()
	return len(vault.applied)
}

func interopAssertRemoteConvergentConsistent(t *testing.T, remote *fakeRemote) {
	t.Helper()
	remote.mu.Lock()
	content := append([]byte(nil), remote.file.Content...)
	remote.mu.Unlock()
	file, err := domainsync.UnmarshalFile(content)
	if err != nil {
		t.Fatalf("UnmarshalFile: %v", err)
	}
	plaintext, err := domainsync.DecryptPayload(file, convergentTestPassword)
	if err != nil {
		t.Fatalf("DecryptPayload: %v", err)
	}
	if bytes.Contains(plaintext, []byte("acme.bad")) {
		t.Fatalf("the provider still carries the malformed namespace: %.200s", plaintext)
	}
	payload, err := decodePayloadBytes(plaintext)
	if err != nil {
		t.Fatalf("decodePayloadBytes: %v", err)
	}
	envelope, err := domainsync.ParseConvergentEnvelope(payload.ConvergentSync)
	if err != nil {
		t.Fatalf("ParseConvergentEnvelope: %v", err)
	}
	snapshot, err := payloadSnapshot(payload)
	if err != nil {
		t.Fatalf("payloadSnapshot: %v", err)
	}
	if err := domainsync.EnvelopeMatchesSnapshot(envelope.State, snapshot); err != nil {
		t.Fatalf("envelope and materialized payload disagree: %v", err)
	}
}

func TestPayloadFromStateQuarantinesMalformedPluginDataNamespace(t *testing.T) {
	payload := Payload{Hosts: []domainvault.Host{
		interopHostWithPluginData("h1", "One",
			interopFieldPluginData(json.RawMessage(`"ok"`))),
		interopHostWithPluginData("h2", "Two", nil),
	}}
	state := convergentStateForTest(t, payload, "device-a", 1000)
	bad := stateWithRecordPluginData(t, state, "other-device",
		state.HLC.WallTime+1000, "hosts", "h1",
		`{"acme.good":`+interopGoodNamespace("ok")+`,"acme.bad":[1]}`)

	materialized, err := payloadFromState(bad, nil, 2000, "device-a")
	if err != nil {
		t.Fatalf("one malformed namespace failed the whole materialization: %v", err)
	}
	if len(materialized.Hosts) != 2 {
		t.Fatalf("materialized hosts = %d", len(materialized.Hosts))
	}
	byID := map[string]domainvault.Host{}
	for _, host := range materialized.Hosts {
		byID[string(host.ID)] = host
	}
	if len(byID["h1"].PluginData) != 1 ||
		string(byID["h1"].PluginData["acme.good"].Fields["name"]) != `"ok"` {
		t.Fatalf("valid namespace was not materialized: %+v", byID["h1"].PluginData)
	}
	if len(byID["h2"].PluginData) != 0 {
		t.Fatalf("sibling record changed: %+v", byID["h2"].PluginData)
	}
	if _, found := interopQuarantine(t, materialized, plugindata.RecordHost,
		"h1", "acme.bad"); !found {
		t.Fatalf("materialization dropped the quarantine: %+v",
			materialized.PluginDataQuarantines)
	}
}

func TestConvergentSyncRepairsARemoteMalformedPluginDataNamespace(t *testing.T) {
	remote := newFakeRemote()
	device := newConvergentDevice(t, remote, "device-a", payloadWithHosts(
		interopHostWithPluginData("h1", "One",
			interopFieldPluginData(json.RawMessage(`"ok"`))),
		host("h2", "Two", "two.example"),
	))
	if _, err := device.service.SetConvergentEnabled(context.Background(), true); err != nil {
		t.Fatalf("enable convergent sync: %v", err)
	}
	state := device.state(t)
	bad := stateWithRecordPluginData(t, state, "other-device",
		state.HLC.WallTime+1000, "hosts", "h1",
		`{"acme.good":`+interopGoodNamespace("ok")+`,"acme.bad":[1]}`)
	installRemoteContent(t, remote,
		convergentContentForState(t, bad, 9000, "other-device"), 9000)
	appliedBefore := interopAppliedCount(device.vault)

	result, err := device.service.Sync(context.Background(), TriggerManual)
	if err != nil {
		t.Fatalf("a remote malformed v2 namespace failed the sync: %v", err)
	}
	if result.Writes == 0 {
		t.Fatal("the provider was not rewritten with the quarantined state")
	}
	applied := interopLastApplied(t, device.vault)
	if interopAppliedCount(device.vault) <= appliedBefore {
		t.Fatal("the quarantined materialization was not applied locally")
	}
	byID := map[string]domainvault.Host{}
	for _, item := range applied.Hosts {
		byID[string(item.ID)] = item
	}
	if len(byID["h1"].PluginData) != 1 ||
		string(byID["h1"].PluginData["acme.good"].Fields["name"]) != `"ok"` {
		t.Fatalf("valid namespace after remote repair = %+v", byID["h1"].PluginData)
	}
	if _, ok := byID["h2"]; !ok {
		t.Fatalf("valid sibling record was lost: %+v", hostIDs(applied))
	}
	if entry, found := interopQuarantine(t, applied, plugindata.RecordHost,
		"h1", "acme.bad"); !found {
		t.Fatalf("remote quarantine diagnostics = %+v",
			applied.PluginDataQuarantines)
	} else if entry.Reason != plugindata.ReasonNamespace {
		t.Fatalf("remote quarantine reason = %+v", entry)
	}
	interopAssertRemoteConvergentConsistent(t, remote)

	repeated, err := device.service.Sync(context.Background(), TriggerManual)
	if err != nil {
		t.Fatalf("a repeated sync after repair failed: %v", err)
	}
	if repeated.Writes != 0 {
		t.Fatalf("a repeated sync after repair wrote %d times", repeated.Writes)
	}
	if got := interopAppliedCount(device.vault); got != appliedBefore+1 {
		t.Fatalf("a repeated sync applied the Vault again: %d applies", got)
	}
}

func TestConvergentSyncRepairsALocalMalformedPluginDataNamespace(t *testing.T) {
	remote := newFakeRemote()
	device := newConvergentDevice(t, remote, "device-a", payloadWithHosts(
		interopHostWithPluginData("h1", "One", plugindata.Map{
			"acme.good": {
				SchemaVersion: 1,
				Fields: map[string]json.RawMessage{
					"name": json.RawMessage(`"ok"`),
				},
			},
		}),
		host("h2", "Two", "two.example"),
	))
	if _, err := device.service.SetConvergentEnabled(context.Background(), true); err != nil {
		t.Fatalf("enable convergent sync: %v", err)
	}
	state := device.state(t)
	bad := stateWithRecordPluginData(t, state, "device-a",
		state.HLC.WallTime+1000, "hosts", "h1",
		`{"acme.good":`+interopGoodNamespace("ok")+`,"acme.bad":[1]}`)
	blob, signature, err := device.service.sealConvergentState(bad)
	if err != nil {
		t.Fatalf("seal malformed local replica: %v", err)
	}
	config := device.service.currentConfig()
	config.Convergent.Replica = blob
	config.Convergent.ReplicaSignature = signature
	if err := device.service.saveConvergentConfig(config); err != nil {
		t.Fatalf("persist malformed local replica: %v", err)
	}
	appliedBefore := interopAppliedCount(device.vault)

	result, err := device.service.Sync(context.Background(), TriggerManual)
	if err != nil {
		t.Fatalf("a local malformed v2 namespace failed the sync: %v", err)
	}
	if result.Writes == 0 {
		t.Fatal("the repaired local state was not published")
	}
	applied := interopLastApplied(t, device.vault)
	if interopAppliedCount(device.vault) <= appliedBefore {
		t.Fatal("the repaired materialization was not applied locally")
	}
	byID := map[string]domainvault.Host{}
	for _, item := range applied.Hosts {
		byID[string(item.ID)] = item
	}
	if len(byID["h1"].PluginData) != 1 ||
		string(byID["h1"].PluginData["acme.good"].Fields["name"]) != `"ok"` {
		t.Fatalf("valid namespace after local repair = %+v", byID["h1"].PluginData)
	}
	if _, ok := byID["h2"]; !ok {
		t.Fatalf("valid sibling record was lost: %+v", hostIDs(applied))
	}
	if _, found := interopQuarantine(t, applied, plugindata.RecordHost,
		"h1", "acme.bad"); !found {
		t.Fatalf("local quarantine diagnostics = %+v",
			applied.PluginDataQuarantines)
	}
	interopAssertRemoteConvergentConsistent(t, remote)

	// The repaired replica is what the next run loads: it must not fail and
	// must not rewrite the provider.
	repeated, err := device.service.Sync(context.Background(), TriggerManual)
	if err != nil {
		t.Fatalf("a repeated sync after local repair failed: %v", err)
	}
	if repeated.Writes != 0 {
		t.Fatalf("a repeated sync after local repair wrote %d times", repeated.Writes)
	}
	if got := interopAppliedCount(device.vault); got != appliedBefore+1 {
		t.Fatalf("a repeated sync applied the Vault again: %d applies", got)
	}
	reloaded, err := device.service.loadConvergentStateStrict(device.service.currentConfig())
	if err != nil {
		t.Fatalf("reloaded repaired replica: %v", err)
	}
	materialized, err := domainsync.MaterializeConvergentSnapshot(reloaded)
	if err != nil {
		t.Fatalf("materialize repaired replica: %v", err)
	}
	badLeft := false
	for _, family := range []string{"hosts", "dataProfiles"} {
		for _, record := range materialized.Families[family] {
			if bytes.Contains(record.Value, []byte("acme.bad")) {
				badLeft = true
			}
		}
	}
	if badLeft {
		t.Fatal("the repaired replica still carries the malformed namespace")
	}
}

// inlineMalformedConvergentContent builds v2 ciphertext whose inline payload
// carries a malformed pluginData namespace the envelope state does not
// mention. That is the inline-payload-only quarantine case.
func inlineMalformedConvergentContent(t *testing.T, state domainsync.ConvergentState,
	container string) []byte {

	t.Helper()
	payload, err := payloadFromState(state, nil, 9000, "other-device")
	if err != nil {
		t.Fatalf("payloadFromState: %v", err)
	}
	data, err := domainsync.MarshalJSON(payload)
	if err != nil {
		t.Fatalf("MarshalJSON payload: %v", err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var hosts []json.RawMessage
	if err := json.Unmarshal(top["hosts"], &hosts); err != nil || len(hosts) != 1 {
		t.Fatalf("payload hosts = %s, %v", top["hosts"], err)
	}
	hosts[0] = json.RawMessage(interopVariantRecord(t, string(hosts[0]),
		"pluginData", container))
	top["hosts"], err = domainsync.MarshalJSON(hosts)
	if err != nil {
		t.Fatalf("encode hosts: %v", err)
	}
	raw, err := domainsync.MarshalJSON(top)
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	envelope, err := domainsync.EncryptPayload(json.RawMessage(raw),
		convergentTestPassword, domainsync.EncryptOptions{
			Version: 1, UpdatedAt: 9000, DeviceID: "other-device",
			DeviceName: "Other Device", AppVersion: "1.0",
			KDFIterations: testKDFIterations, SyncSchemaVersion: 2,
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

func TestConvergentSyncPreservesInlinePayloadOnlyQuarantine(t *testing.T) {
	remote := newFakeRemote()
	device := newConvergentDevice(t, remote, "device-a",
		payloadWithHosts(host("h1", "One", "one.example")))
	if _, err := device.service.SetConvergentEnabled(context.Background(), true); err != nil {
		t.Fatalf("enable convergent sync: %v", err)
	}
	state := device.state(t)
	installRemoteContent(t, remote, inlineMalformedConvergentContent(t, state,
		`{"acme.bad":null}`), 9000)
	appliedBefore := interopAppliedCount(device.vault)

	result, err := device.service.Sync(context.Background(), TriggerManual)
	if err != nil {
		t.Fatalf("an inline-only malformed namespace failed the sync: %v", err)
	}
	if result.Writes == 0 {
		t.Fatal("the provider was not rewritten to realize the quarantine")
	}
	applied := interopLastApplied(t, device.vault)
	if interopAppliedCount(device.vault) <= appliedBefore {
		t.Fatalf("the inline-payload quarantine was not applied: %d applies",
			interopAppliedCount(device.vault))
	}
	if entry, found := interopQuarantine(t, applied, plugindata.RecordHost,
		"h1", "acme.bad"); !found {
		t.Fatalf("inline-payload quarantine was dropped: %+v",
			applied.PluginDataQuarantines)
	} else if entry.Reason != plugindata.ReasonNamespace {
		t.Fatalf("inline-payload quarantine = %+v", entry)
	}
	if len(applied.Hosts) != 1 || string(applied.Hosts[0].ID) != "h1" {
		t.Fatalf("valid record was lost: %+v", applied.Hosts)
	}
	interopAssertRemoteConvergentConsistent(t, remote)

	repeated, err := device.service.Sync(context.Background(), TriggerManual)
	if err != nil {
		t.Fatalf("a repeated inline-only sync failed: %v", err)
	}
	if repeated.Writes != 0 {
		t.Fatalf("a repeated inline-only sync wrote %d times", repeated.Writes)
	}
	if got := interopAppliedCount(device.vault); got != appliedBefore+1 {
		t.Fatalf("a repeated inline-only sync applied the Vault again: %d applies", got)
	}
}

func TestConvergentEnablePreservesInlinePayloadOnlyQuarantine(t *testing.T) {
	backups := &fakeBackups{}
	service, remote, vault, _ := serviceForStrategy(t,
		domainsync.StrategySmartMerge, backups)
	payload := payloadWithHosts(host("h1", "One", "one.example"))
	vault.set(payload)
	state := convergentStateForTest(t, payload, "other-device", 1000)
	installRemoteContent(t, remote, inlineMalformedConvergentContent(t, state,
		`{"acme.bad":null}`), 9000)
	appliedBefore := interopAppliedCount(vault)

	if _, err := service.SetConvergentEnabled(context.Background(), true); err != nil {
		t.Fatalf("enabling v2 against an inline-only malformed replica: %v", err)
	}
	applied := interopLastApplied(t, vault)
	if interopAppliedCount(vault) <= appliedBefore {
		t.Fatal("migration did not apply the provider materialization")
	}
	if entry, found := interopQuarantine(t, applied, plugindata.RecordHost,
		"h1", "acme.bad"); !found {
		t.Fatalf("enable-time inline quarantine was dropped: %+v",
			applied.PluginDataQuarantines)
	} else if entry.Reason != plugindata.ReasonNamespace {
		t.Fatalf("enable-time quarantine = %+v", entry)
	}
	if len(applied.Hosts) != 1 || string(applied.Hosts[0].ID) != "h1" {
		t.Fatalf("valid record was lost during migration: %+v", applied.Hosts)
	}
	interopAssertRemoteConvergentConsistent(t, remote)

	repeated, err := service.Sync(context.Background(), TriggerManual)
	if err != nil {
		t.Fatalf("a repeated sync after migration failed: %v", err)
	}
	if repeated.Writes != 0 {
		t.Fatalf("a repeated sync after migration wrote %d times", repeated.Writes)
	}
}
