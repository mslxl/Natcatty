package plugindata

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func raw(value string) json.RawMessage { return json.RawMessage(value) }

func validNamespace(fields map[string]string) Namespace {
	values := make(map[string]json.RawMessage, len(fields))
	for key, value := range fields {
		values[key] = raw(value)
	}
	return Namespace{SchemaVersion: 1, Fields: values}
}

func TestValidateNamespaceAcceptsStructuralSubsetAndUnknownProperties(t *testing.T) {
	namespace := validNamespace(map[string]string{
		"name": `"plain"`, "count": "12", "enabled": "true",
		"nested": `{"a":[1,{"b":"c"}]}`,
	})
	namespace.Extra = map[string]json.RawMessage{"future": raw(`{"x":1}`)}
	if err := ValidateNamespace("acme.data", namespace); err != nil {
		t.Fatalf("ValidateNamespace: %v", err)
	}
	encoded, err := json.Marshal(namespace)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	decoded, err := DecodeNamespace(encoded)
	if err != nil {
		t.Fatalf("DecodeNamespace: %v", err)
	}
	if string(decoded.Extra["future"]) != `{"x":1}` {
		t.Fatalf("unknown property = %s, want preserved", decoded.Extra["future"])
	}
	if string(decoded.Fields["nested"]) != `{"a":[1,{"b":"c"}]}` {
		t.Fatalf("nested field = %s", decoded.Fields["nested"])
	}
}

func TestValidateNamespaceRejectsEveryStructuralLimit(t *testing.T) {
	oversizedField := raw(`"` + strings.Repeat("a", MaxFieldBytes) + `"`)
	deep := raw(strings.Repeat("[", MaxDepth+1) + "0" + strings.Repeat("]", MaxDepth+1))
	cases := []struct {
		name      string
		pluginID  string
		namespace Namespace
		reason    Reason
	}{
		{
			name: "bad namespace id", pluginID: "not-a-plugin-id",
			namespace: validNamespace(nil), reason: ReasonNamespace,
		},
		{
			name: "missing schema version", pluginID: "acme.data",
			namespace: Namespace{Fields: map[string]json.RawMessage{"a": raw("1")}},
			reason:    ReasonSchemaVersion,
		},
		{
			name: "oversized field", pluginID: "acme.data",
			namespace: Namespace{SchemaVersion: 1,
				Fields: map[string]json.RawMessage{"blob": oversizedField}},
			reason: ReasonFieldSize,
		},
		{
			name: "excessive depth", pluginID: "acme.data",
			namespace: Namespace{SchemaVersion: 1,
				Fields: map[string]json.RawMessage{"deep": deep}},
			reason: ReasonDepth,
		},
		{
			name: "invalid utf8 value", pluginID: "acme.data",
			namespace: Namespace{SchemaVersion: 1,
				Fields: map[string]json.RawMessage{"name": raw("\"\xff\"")}},
			reason: ReasonUTF8,
		},
		{
			name: "empty field key", pluginID: "acme.data",
			namespace: Namespace{SchemaVersion: 1,
				Fields: map[string]json.RawMessage{"": raw("1")}},
			reason: ReasonFieldKey,
		},
		{
			name: "oversized field key", pluginID: "acme.data",
			namespace: Namespace{SchemaVersion: 1,
				Fields: map[string]json.RawMessage{
					strings.Repeat("k", MaxFieldKeyBytes+1): raw("1"),
				}},
			reason: ReasonFieldKey,
		},
		{
			name: "schema version above the maximum", pluginID: "acme.data",
			namespace: Namespace{SchemaVersion: MaxSchemaVersion + 1},
			reason:    ReasonSchemaVersion,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateNamespace(test.pluginID, test.namespace)
			var invalid *ValidationError
			if !errors.As(err, &invalid) {
				t.Fatalf("error = %v, want ValidationError", err)
			}
			if invalid.Reason != test.reason {
				t.Fatalf("reason = %q, want %q", invalid.Reason, test.reason)
			}
		})
	}
}

func TestSanitizeQuarantinesOneNamespaceAndKeepsTheValidRemainder(t *testing.T) {
	valid := validNamespace(map[string]string{"name": `"ok"`})
	valid.Extra = map[string]json.RawMessage{"future": raw(`"kept"`)}
	invalid := Namespace{SchemaVersion: 1,
		Fields: map[string]json.RawMessage{"deep": raw(strings.Repeat("[", MaxDepth+1) + "0" + strings.Repeat("]", MaxDepth+1))}}
	clean, quarantined, err := Sanitize(Map{"acme.ok": valid, "acme.bad": invalid})
	if err != nil {
		t.Fatalf("Sanitize: %v", err)
	}
	if len(clean) != 1 {
		t.Fatalf("clean namespaces = %d, want 1", len(clean))
	}
	if _, ok := clean["acme.bad"]; ok {
		t.Fatal("invalid namespace stayed in the record")
	}
	if string(clean["acme.ok"].Extra["future"]) != `"kept"` {
		t.Fatal("valid namespace lost its unknown property")
	}
	if len(quarantined) != 1 || quarantined[0].PluginID != "acme.bad" ||
		quarantined[0].Reason != ReasonDepth || len(quarantined[0].Raw) == 0 {
		t.Fatalf("quarantines = %+v", quarantined)
	}
}

func TestSanitizeRejectsAValidRemainderAboveTheRecordLimit(t *testing.T) {
	fields := map[string]string{}
	for index := 0; index < 5; index++ {
		fields[string(rune('a'+index))] = `"` + strings.Repeat("x", 14<<10) + `"`
	}
	_, _, err := Sanitize(Map{"acme.data": validNamespace(fields)})
	if !errors.Is(err, ErrRecordTooLarge) {
		t.Fatalf("Sanitize error = %v, want ErrRecordTooLarge", err)
	}
}

func TestSanitizeRecordKeepsUnknownRecordPropertiesAndReturnsValidBytesVerbatim(t *testing.T) {
	// The container is already canonical (deterministic key order, compact,
	// no HTML escaping), so the exact stored bytes must survive a restore or
	// export unchanged.
	record := []byte(`{"id":"h1","label":"alpha","futureRecordProperty":{"n":1},` +
		`"pluginData":{"acme.data":{"fields":{"name":"ok"},"futureNamespace":true,"schemaVersion":1}}}`)
	clean, quarantined, err := SanitizeRecord(RecordHost, "h1", record)
	if err != nil {
		t.Fatalf("SanitizeRecord: %v", err)
	}
	if len(quarantined) != 0 {
		t.Fatalf("quarantines = %+v", quarantined)
	}
	if string(clean) != string(record) {
		t.Fatalf("valid canonical record was re-encoded:\n%s\n%s", record, clean)
	}

	invalid := []byte(`{"id":"h1","futureRecordProperty":7,"pluginData":{` +
		`"acme.bad":{"schemaVersion":0,"fields":{}},` +
		`"acme.good":{"schemaVersion":1,"fields":{"name":"ok"},"futureNamespace":"kept"}}}`)
	clean, quarantined, err = SanitizeRecord(RecordHost, "h1", invalid)
	if err != nil {
		t.Fatalf("SanitizeRecord invalid: %v", err)
	}
	if len(quarantined) != 1 || quarantined[0].PluginID != "acme.bad" ||
		quarantined[0].Reason != ReasonSchemaVersion {
		t.Fatalf("quarantines = %+v", quarantined)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(clean, &top); err != nil {
		t.Fatalf("clean record: %v", err)
	}
	if string(top["futureRecordProperty"]) != "7" {
		t.Fatalf("unknown record property = %s", top["futureRecordProperty"])
	}
	var namespaces map[string]json.RawMessage
	if err := json.Unmarshal(top["pluginData"], &namespaces); err != nil {
		t.Fatalf("clean pluginData: %v", err)
	}
	if _, ok := namespaces["acme.bad"]; ok {
		t.Fatal("quarantined namespace stayed in the record")
	}
	if !strings.Contains(string(namespaces["acme.good"]), `"futureNamespace":"kept"`) {
		t.Fatalf("valid namespace = %s", namespaces["acme.good"])
	}
}

func TestSanitizeRecordQuarantinesInvalidUTF8AndKeepsValidNamespaces(t *testing.T) {
	record := []byte(`{"id":"h1","pluginData":{` +
		`"acme.bad":{"schemaVersion":1,"fields":{"name":"` + "\xff" + `"}},` +
		`"acme.good":{"schemaVersion":1,"fields":{"name":"ok"}}}}`)
	clean, quarantined, err := SanitizeRecord(RecordHost, "h1", record)
	if err != nil {
		t.Fatalf("SanitizeRecord: %v", err)
	}
	if len(quarantined) != 1 || quarantined[0].PluginID != "acme.bad" ||
		quarantined[0].Reason != ReasonUTF8 {
		t.Fatalf("quarantines = %+v", quarantined)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(clean, &top); err != nil {
		t.Fatalf("clean record: %v", err)
	}
	var namespaces map[string]json.RawMessage
	if err := json.Unmarshal(top["pluginData"], &namespaces); err != nil {
		t.Fatalf("clean pluginData: %v", err)
	}
	if _, ok := namespaces["acme.bad"]; ok {
		t.Fatal("invalid UTF-8 namespace stayed in the record")
	}
	if !strings.Contains(string(namespaces["acme.good"]), `"ok"`) {
		t.Fatalf("valid namespace = %s", namespaces["acme.good"])
	}
}

func TestSanitizeRecordQuarantinesAnInvalidPluginDataContainerAtRecordLevel(t *testing.T) {
	clean, quarantined, err := SanitizeRecord(RecordDataProfile, "p1",
		[]byte(`{"id":"p1","label":"kept","pluginData":[1,2]}`))
	if err != nil {
		t.Fatalf("SanitizeRecord: %v", err)
	}
	if len(quarantined) != 1 || quarantined[0].PluginID != "" ||
		quarantined[0].Reason != ReasonRecord {
		t.Fatalf("quarantines = %+v", quarantined)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(clean, &top); err != nil {
		t.Fatalf("clean record: %v", err)
	}
	if _, ok := top[JSONField]; ok {
		t.Fatalf("record-level quarantine left pluginData in place: %s", clean)
	}
	if string(top["label"]) != `"kept"` {
		t.Fatalf("record properties were lost: %s", clean)
	}
}

func TestEffectiveSchemaVersionDefaultsToOne(t *testing.T) {
	if EffectiveSchemaVersion(0) != 1 || EffectiveSchemaVersion(-3) != 1 ||
		EffectiveSchemaVersion(7) != 7 {
		t.Fatal("EffectiveSchemaVersion does not apply the version-1 default")
	}
}
