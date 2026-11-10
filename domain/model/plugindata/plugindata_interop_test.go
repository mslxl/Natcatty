package plugindata

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func sizedFieldValue(fill string, size int) json.RawMessage {
	if size < 2 || fill == "" {
		panic("field value must include its quotes and a fill")
	}
	content := size - 2
	repeated := strings.Repeat(fill, content/len(fill)+1)
	return raw(`"` + repeated[:content] + `"`)
}

func sizedNamespace(fill string, size int) Namespace {
	return Namespace{SchemaVersion: 1,
		Fields: map[string]json.RawMessage{"blob": sizedFieldValue(fill, size)}}
}

func encodedMapSize(t *testing.T, value Map) int {
	t.Helper()
	data, err := EncodeMap(value)
	if err != nil {
		t.Fatalf("EncodeMap: %v", err)
	}
	return len(data)
}

// recordAtLimit builds one valid namespace map whose canonical encoding is
// exactly MaxRecordBytes+over. Increasing the last field by one byte changes
// the encoding by exactly one byte because the fill character is never
// escaped.
func recordAtLimit(t *testing.T, fill string, over int) Map {
	t.Helper()
	fields := map[string]json.RawMessage{}
	for _, key := range []string{"a", "b", "c", "d"} {
		fields[key] = sizedFieldValue(fill, MaxFieldBytes-(2<<10))
	}
	fields["e"] = sizedFieldValue(fill, 2)
	value := Map{"acme.data": {SchemaVersion: 1, Fields: fields}}

	size := encodedMapSize(t, value)
	delta := MaxRecordBytes + over - size
	content := len(fields["e"]) - 2 + delta
	if content < 0 {
		t.Fatalf("limit map construction underflows: delta %d", delta)
	}
	fields["e"] = sizedFieldValue(fill, content+2)
	if got := encodedMapSize(t, value); got != MaxRecordBytes+over {
		t.Fatalf("record construction = %d bytes, want %d", got, MaxRecordBytes+over)
	}
	for key, field := range fields {
		if len(field) > MaxFieldBytes {
			t.Fatalf("field %q = %d bytes, above the field limit", key, len(field))
		}
	}
	return value
}

func TestNamespaceEncodingLeavesHTMLCharactersUnescaped(t *testing.T) {
	value := sizedFieldValue("<&>", 3002)
	namespace := Namespace{SchemaVersion: 1,
		Fields: map[string]json.RawMessage{"blob": value}}
	encoded, err := EncodeMap(Map{"acme.data": namespace})
	if err != nil {
		t.Fatalf("EncodeMap: %v", err)
	}
	escaped := []string{"\\u003c", "\\u003e", "\\u0026"}
	for _, escape := range escaped {
		if bytes.Contains(encoded, []byte(escape)) {
			t.Fatalf("encoding escaped HTML: %s", encoded)
		}
	}
	if !bytes.Contains(encoded, value) {
		t.Fatalf("field bytes changed: %s", encoded)
	}
	if len(encoded) > 3300 {
		t.Fatalf("a 3,002-byte field encoded to %d bytes", len(encoded))
	}

	// Four legal 3,002-byte fields of '&' are 12,008 stored bytes, not the
	// 72,075-byte escaped spelling a json.Marshal-based encoder produced.
	fields := map[string]json.RawMessage{}
	for _, key := range []string{"a", "b", "c", "d"} {
		fields[key] = sizedFieldValue("&", 3002)
	}
	four := Map{"acme.data": {SchemaVersion: 1, Fields: fields}}
	if err := Validate(four); err != nil {
		t.Fatalf("four legal fields were rejected: %v", err)
	}
	if size := encodedMapSize(t, four); size > 12500 {
		t.Fatalf("four 3,002-byte '&' fields measured %d bytes", size)
	}
}

func TestFieldSizeLimitCountsStoredHTMLEscapeBytes(t *testing.T) {
	for _, fill := range []string{"<", ">", "&"} {
		t.Run(fill, func(t *testing.T) {
			exact := sizedNamespace(fill, MaxFieldBytes)
			if err := ValidateNamespace("acme.data", exact); err != nil {
				t.Fatalf("exact-limit field was rejected: %v", err)
			}
			if len(exact.Fields["blob"]) != MaxFieldBytes {
				t.Fatalf("field bytes = %d", len(exact.Fields["blob"]))
			}
			over := sizedNamespace(fill, MaxFieldBytes+1)
			err := ValidateNamespace("acme.data", over)
			var invalid *ValidationError
			if !errors.As(err, &invalid) || invalid.Reason != ReasonFieldSize {
				t.Fatalf("one-byte-over field error = %v", err)
			}
		})
	}
}

func TestRecordSizeLimitMeasuresStoredBytesExactly(t *testing.T) {
	for _, fill := range []string{"<", ">", "&"} {
		t.Run(fill, func(t *testing.T) {
			exact := recordAtLimit(t, fill, 0)
			clean, quarantined, err := Sanitize(exact)
			if err != nil {
				t.Fatalf("exact-limit record was rejected: %v", err)
			}
			if len(quarantined) != 0 || len(clean) != 1 {
				t.Fatalf("exact-limit record = %d namespaces, quarantines = %+v",
					len(clean), quarantined)
			}

			over := recordAtLimit(t, fill, 1)
			if err := Validate(over); !errors.Is(err, ErrRecordTooLarge) {
				t.Fatalf("one-byte-over record error = %v", err)
			}
			if _, _, err := Sanitize(over); !errors.Is(err, ErrRecordTooLarge) {
				t.Fatalf("strict Sanitize error = %v", err)
			}
			clean, quarantined, err = SanitizeLenient(over)
			if err != nil {
				t.Fatalf("SanitizeLenient: %v", err)
			}
			if len(clean) != 0 || len(quarantined) != 1 ||
				quarantined[0].PluginID != "acme.data" ||
				quarantined[0].Reason != ReasonRecordSize {
				t.Fatalf("lenient over-limit result = %d namespaces, %+v",
					len(clean), quarantined)
			}
		})
	}
}

func TestSanitizeLenientDropsTheLargestNamespaceAndBreaksTiesByID(t *testing.T) {
	large := func(fill string) Namespace {
		fields := map[string]json.RawMessage{}
		for _, key := range []string{"a", "b", "c"} {
			fields[key] = sizedFieldValue(fill, 11500)
		}
		return Namespace{SchemaVersion: 1, Fields: fields}
	}
	// Both namespaces fit alone but not together; acme.b is the larger one.
	value := Map{
		"acme.a": large("a"),
		"acme.b": {SchemaVersion: 1, Fields: map[string]json.RawMessage{
			"a": sizedFieldValue("b", 11500),
			"b": sizedFieldValue("b", 11500),
			"c": sizedFieldValue("b", 11500),
			"d": sizedFieldValue("b", 100),
		}},
	}
	for attempt := 0; attempt < 3; attempt++ {
		clean, quarantined, err := SanitizeLenient(value)
		if err != nil {
			t.Fatalf("SanitizeLenient: %v", err)
		}
		if _, ok := clean["acme.b"]; ok {
			t.Fatalf("the largest namespace was not quarantined: %+v", clean)
		}
		if _, ok := clean["acme.a"]; !ok {
			t.Fatalf("the smaller namespace was dropped: %+v", clean)
		}
		if len(quarantined) != 1 || quarantined[0].PluginID != "acme.b" ||
			quarantined[0].Reason != ReasonRecordSize {
			t.Fatalf("quarantines = %+v", quarantined)
		}
	}

	tie := Map{"acme.bb": large("x"), "acme.aa": large("x")}
	clean, quarantined, err := SanitizeLenient(tie)
	if err != nil {
		t.Fatalf("SanitizeLenient tie: %v", err)
	}
	if _, ok := clean["acme.aa"]; ok {
		t.Fatalf("tie did not drop the ascending first Plugin ID: %+v", clean)
	}
	if _, ok := clean["acme.bb"]; !ok || len(quarantined) != 1 ||
		quarantined[0].PluginID != "acme.aa" {
		t.Fatalf("tie result = %+v, quarantines = %+v", clean, quarantined)
	}
}

func TestSanitizeRecordReencodesDuplicateKeysAndWhitespace(t *testing.T) {
	bigNamespace := func() string {
		fields := make([]string, 0, 8)
		for index := 0; index < 8; index++ {
			fields = append(fields, `"f`+string(rune('a'+index))+`":`+
				string(sizedFieldValue("x", MaxFieldBytes)))
		}
		return `{"schemaVersion":1,"fields":{` + strings.Join(fields, ",") + `}}`
	}()
	huge := bigNamespace
	if len(huge) <= MaxRecordBytes {
		t.Fatalf("duplicate-namespace fixture = %d bytes, want > %d",
			len(huge), MaxRecordBytes)
	}
	record := []byte(`{"id":"h1","pluginData":{"acme.data":` + huge +
		`,"acme.data":{"schemaVersion":1,"fields":{"name":"ok"}}}}`)
	clean, quarantined, err := SanitizeRecord(RecordHost, "h1", record)
	if err != nil {
		t.Fatalf("SanitizeRecord duplicate namespace: %v", err)
	}
	if len(quarantined) != 0 {
		t.Fatalf("quarantines = %+v", quarantined)
	}
	if len(clean) >= len(record) || len(clean) > MaxRecordBytes {
		t.Fatalf("duplicate-key record persisted raw bytes: %d of %d",
			len(clean), len(record))
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(clean, &top); err != nil {
		t.Fatalf("clean record: %v", err)
	}
	if !bytes.Contains(top["pluginData"], []byte(`"name":"ok"`)) {
		t.Fatalf("the validated duplicate was lost: %s", top["pluginData"])
	}

	duplicateField := []byte(`{"id":"h1","pluginData":{"acme.data":` +
		`{"schemaVersion":1,"fields":{"name":` + string(sizedFieldValue("y", 1000)) +
		`,"name":"kept"}}}}`)
	clean, quarantined, err = SanitizeRecord(RecordHost, "h1", duplicateField)
	if err != nil {
		t.Fatalf("SanitizeRecord duplicate field: %v", err)
	}
	if len(quarantined) != 0 {
		t.Fatalf("duplicate field quarantines = %+v", quarantined)
	}
	if !bytes.Contains(clean, []byte(`"name":"kept"`)) ||
		bytes.Contains(clean, []byte(strings.Repeat("y", 1000))) {
		t.Fatalf("duplicate field bytes survived: %s", clean)
	}

	duplicateProperty := []byte(`{"id":"h1","pluginData":{"acme.data":` +
		`{"schemaVersion":0,"schemaVersion":1,"fields":{}}}}`)
	clean, quarantined, err = SanitizeRecord(RecordHost, "h1", duplicateProperty)
	if err != nil {
		t.Fatalf("SanitizeRecord duplicate property: %v", err)
	}
	if len(quarantined) != 0 {
		t.Fatalf("duplicate property quarantines = %+v", quarantined)
	}
	if !bytes.Contains(clean, []byte(`"schemaVersion":1`)) ||
		bytes.Contains(clean, []byte(`"schemaVersion":0`)) {
		t.Fatalf("duplicate property bytes survived: %s", clean)
	}

	whitespace := []byte(`{"id":"h1","pluginData":{"acme.data":` +
		`{ "schemaVersion" : 1 , "fields" : { "name" : "ok" } }` +
		strings.Repeat(" ", 70<<10) + `}}`)
	if len(whitespace) <= MaxRecordBytes {
		t.Fatalf("whitespace fixture = %d bytes", len(whitespace))
	}
	clean, quarantined, err = SanitizeRecord(RecordHost, "h1", whitespace)
	if err != nil {
		t.Fatalf("SanitizeRecord whitespace: %v", err)
	}
	if len(quarantined) != 0 {
		t.Fatalf("whitespace quarantines = %+v", quarantined)
	}
	if len(clean) > 200 {
		t.Fatalf("whitespace-padded record stayed expanded: %d bytes", len(clean))
	}
	if !bytes.Contains(clean, []byte(`"name":"ok"`)) {
		t.Fatalf("whitespace record lost its value: %s", clean)
	}
}

func TestSanitizeRecordReencodesDuplicateTopLevelPluginDataProperties(t *testing.T) {
	hugeNamespace := func() string {
		fields := make([]string, 0, 5)
		for index := 0; index < 5; index++ {
			fields = append(fields, `"f`+string(rune('a'+index))+`":`+
				string(sizedFieldValue("x", 14<<10)))
		}
		return `{"schemaVersion":1,"fields":{` + strings.Join(fields, ",") + `}}`
	}()
	if len(hugeNamespace)+len(`{"acme.huge":}`) <= MaxRecordBytes {
		t.Fatalf("duplicate-property fixture = %d bytes, want > %d",
			len(hugeNamespace), MaxRecordBytes)
	}
	// The surviving container must be canonical in every other respect. A
	// fixture that is re-encoded for a second reason (namespace property order,
	// whitespace, escapes) would pass even if the duplicate-top-level guard were
	// removed, so the guard would not actually be pinned.
	validEncoded, err := EncodeMap(Map{"acme.data": {
		SchemaVersion: 1,
		Fields:        map[string]json.RawMessage{"name": json.RawMessage(`"ok"`)},
	}})
	if err != nil {
		t.Fatalf("EncodeMap valid: %v", err)
	}
	valid := string(validEncoded)
	record := []byte(`{"id":"h1","pluginData":{"acme.huge":` + hugeNamespace +
		`},"pluginData":` + valid + `}`)
	clean, quarantined, err := SanitizeRecord(RecordHost, "h1", record)
	if err != nil {
		t.Fatalf("SanitizeRecord duplicate top-level property: %v", err)
	}
	if len(quarantined) != 0 {
		t.Fatalf("quarantines = %+v", quarantined)
	}
	if len(clean) > MaxRecordBytes || bytes.Contains(clean, []byte("acme.huge")) ||
		bytes.Contains(clean, []byte(strings.Repeat("x", 1<<10))) {
		t.Fatalf("shadowed pluginData persisted: %d of %d bytes",
			len(clean), len(record))
	}
	if !bytes.Contains(clean, []byte(`"name":"ok"`)) {
		t.Fatalf("the validated duplicate was lost: %s", clean)
	}

	nullRecord := []byte(`{"id":"h1","pluginData":{"acme.huge":` + hugeNamespace +
		`},"pluginData":null}`)
	clean, quarantined, err = SanitizeRecord(RecordHost, "h1", nullRecord)
	if err != nil {
		t.Fatalf("SanitizeRecord duplicate null: %v", err)
	}
	if len(quarantined) != 0 {
		t.Fatalf("duplicate null quarantines = %+v", quarantined)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(clean, &top); err != nil {
		t.Fatalf("clean null record: %v", err)
	}
	if _, exists := top[JSONField]; exists ||
		bytes.Contains(clean, []byte("acme.huge")) || len(clean) > 200 {
		t.Fatalf("null-shadowed pluginData persisted: %s", clean)
	}
}

func TestSanitizeRecordDropsANullContainer(t *testing.T) {
	record := []byte(`{"id":"h1","label":"null","pluginData":null}`)
	clean, quarantined, err := SanitizeRecord(RecordHost, "h1", record)
	if err != nil {
		t.Fatalf("SanitizeRecord: %v", err)
	}
	// A container with no surviving namespace is represented by the absent
	// property; null and {} must not survive as a second spelling of empty.
	if len(quarantined) != 0 || string(clean) != `{"id":"h1","label":"null"}` {
		t.Fatalf("null container = %s, quarantines = %+v", clean, quarantined)
	}
	namespaces, quarantined, err := DecodeMapLenient([]byte("null"))
	if err != nil || len(namespaces) != 0 || len(quarantined) != 0 {
		t.Fatalf("DecodeMapLenient(null) = %+v, %+v, %v",
			namespaces, quarantined, err)
	}
}

func TestQuarantineDiagnosticsAreBoundedAndUTF8Safe(t *testing.T) {
	hugePluginID := strings.Repeat("k", 1<<20)
	hugeField := strings.Repeat("f", 1<<20)
	container := []byte(`{"` + hugePluginID + `":{"schemaVersion":1,"fields":{}},` +
		`"acme.data":{"schemaVersion":1,"fields":{"` + hugeField + `":1}}}`)
	namespaces, quarantined, err := DecodeMapLenient(container)
	if err != nil {
		t.Fatalf("DecodeMapLenient: %v", err)
	}
	if len(namespaces) != 0 || len(quarantined) != 2 {
		t.Fatalf("result = %+v, quarantines = %d", namespaces, len(quarantined))
	}
	for _, entry := range quarantined {
		if len(entry.PluginID) > MaxDiagnosticPluginIDBytes ||
			len(entry.Detail) > MaxDiagnosticDetailBytes ||
			len(entry.Raw) > MaxRecordBytes {
			t.Fatalf("unbounded quarantine = plugin %d, detail %d, raw %d",
				len(entry.PluginID), len(entry.Detail), len(entry.Raw))
		}
		if !utf8.ValidString(entry.PluginID) || !utf8.ValidString(entry.Detail) {
			t.Fatal("bounded diagnostic text is not valid UTF-8")
		}
	}

	split := BoundText(strings.Repeat("é", 100), 5)
	if len(split) > 5 || !utf8.ValidString(split) || split != "éé" {
		t.Fatalf("BoundText split a multi-byte rune: %q", split)
	}
	invalid := BoundText("bad\xffvalue", 4)
	if !utf8.ValidString(invalid) || len(invalid) > 4 {
		t.Fatalf("BoundText kept invalid UTF-8: %q", invalid)
	}
}

// assertOnlyCanonicalPluginDataProperty pins the invariant that a sanitized
// record carries at most one pluginData spelling and that it is the canonical
// one encoding/json's struct decoder writes.
func assertOnlyCanonicalPluginDataProperty(t *testing.T, clean []byte) {
	t.Helper()
	properties, err := ScanPluginDataProperties(clean)
	if err != nil {
		t.Fatalf("ScanPluginDataProperties: %v", err)
	}
	if !properties.Found || len(properties.Keys) != 1 ||
		properties.Keys[0] != JSONField {
		t.Fatalf("pluginData spellings after sanitize = %+v", properties.Keys)
	}
}

func TestSanitizeRecordQuarantinesCaseVariantPluginDataSpellings(t *testing.T) {
	const container = `{"acme.bad":null,` +
		`"acme.good":{"schemaVersion":1,"fields":{"name":"ok"}}}`
	for _, spelling := range []string{"PluginData", "PLUGINDATA", "plugindata"} {
		for _, kind := range []RecordKind{RecordHost, RecordDataProfile} {
			t.Run(string(kind)+"/"+spelling, func(t *testing.T) {
				record := []byte(`{"id":"r1","label":"keep","` + spelling + `":` +
					container + `}`)
				clean, quarantined, err := SanitizeRecord(kind, "r1", record)
				if err != nil {
					t.Fatalf("SanitizeRecord: %v", err)
				}
				if len(quarantined) != 1 ||
					quarantined[0].PluginID != "acme.bad" ||
					quarantined[0].Reason != ReasonNamespace {
					t.Fatalf("quarantines = %+v", quarantined)
				}
				assertOnlyCanonicalPluginDataProperty(t, clean)
				var top map[string]json.RawMessage
				if err := json.Unmarshal(clean, &top); err != nil {
					t.Fatalf("clean record: %v", err)
				}
				if string(top["label"]) != `"keep"` {
					t.Fatalf("unrelated property was lost: %s", clean)
				}
				// The canonical property carries only the validated namespace.
				namespaces, rejected, err := DecodeMapLenient(top[JSONField])
				if err != nil || len(rejected) != 0 {
					t.Fatalf("canonical container = %+v, %+v, %v",
						namespaces, rejected, err)
				}
				if len(namespaces) != 1 ||
					string(namespaces["acme.good"].Fields["name"]) != `"ok"` {
					t.Fatalf("valid sibling was lost: %+v", namespaces)
				}
				if bytes.Contains(clean, []byte(`"acme.bad"`)) {
					t.Fatalf("rejected namespace bytes survived: %s", clean)
				}
			})
		}
	}
}

func TestSanitizeRecordDropsACaseVariantNonObjectContainer(t *testing.T) {
	for _, spelling := range []string{"PluginData", "PLUGINDATA", "plugindata"} {
		t.Run(spelling, func(t *testing.T) {
			record := []byte(`{"id":"r1","label":"keep","` + spelling + `":[1]}`)
			clean, quarantined, err := SanitizeRecord(RecordHost, "r1", record)
			if err != nil {
				t.Fatalf("SanitizeRecord: %v", err)
			}
			if len(quarantined) != 1 || quarantined[0].Reason != ReasonRecord {
				t.Fatalf("record-level quarantines = %+v", quarantined)
			}
			if properties, err := ScanPluginDataProperties(clean); err != nil ||
				properties.Found {
				t.Fatalf("a non-object container survived: %s, %v", clean, err)
			}
			var top map[string]json.RawMessage
			if err := json.Unmarshal(clean, &top); err != nil {
				t.Fatalf("clean record: %v", err)
			}
			if string(top["label"]) != `"keep"` {
				t.Fatalf("unrelated property was lost: %s", clean)
			}
		})
	}
}

func TestSanitizeRecordQuarantinesAnOverLimitCaseVariantContainer(t *testing.T) {
	fields := map[string]json.RawMessage{}
	for index := 0; index < 5; index++ {
		fields[string(rune('a'+index))] = sizedFieldValue("x", 14<<10)
	}
	huge, err := EncodeMap(Map{"acme.huge": {SchemaVersion: 1, Fields: fields}})
	if err != nil {
		t.Fatalf("EncodeMap: %v", err)
	}
	if len(huge) <= MaxRecordBytes {
		t.Fatalf("fixture = %d bytes, want > %d", len(huge), MaxRecordBytes)
	}
	record := []byte(`{"id":"r1","PluginData":` + string(huge) + `}`)
	clean, quarantined, err := SanitizeRecord(RecordHost, "r1", record)
	if err != nil {
		t.Fatalf("SanitizeRecord: %v", err)
	}
	if len(quarantined) != 1 ||
		quarantined[0].PluginID != "acme.huge" ||
		quarantined[0].Reason != ReasonRecordSize {
		t.Fatalf("quarantines = %+v", quarantined)
	}
	// The only namespace was over the record limit, so no namespace survives
	// and the property is dropped rather than persisted as {}.
	if properties, err := ScanPluginDataProperties(clean); err != nil ||
		properties.Found {
		t.Fatalf("an all-quarantined container survived: %s, %v", clean, err)
	}
	if bytes.Contains(clean, []byte(strings.Repeat("x", 1<<10))) ||
		len(clean) > 200 {
		t.Fatalf("over-limit variant persisted: %d bytes", len(clean))
	}
}

func TestSanitizeRecordResolvesMixedPluginDataSpellingsLastWins(t *testing.T) {
	good := `{"acme.good":{"schemaVersion":1,"fields":{"name":"ok"}}}`
	bad := `{"acme.bad":null}`
	huge := `{"acme.shadow":{"schemaVersion":1,"fields":{"name":"shadow"}}}`
	cases := []struct {
		name     string
		first    string
		firstKey string
		last     string
		lastKey  string
		valid    bool
		expect   string
		shadowed string
	}{
		{"variant-then-canonical", huge, "PluginData", good, "pluginData",
			true, "acme.good", "acme.shadow"},
		{"canonical-then-variant", good, "pluginData", huge, "PLUGINDATA",
			true, "acme.shadow", "acme.good"},
		{"variant-then-other-variant", bad, "PluginData", good, "plugindata",
			true, "acme.good", "acme.bad"},
		{"valid-then-malformed", huge, "PLUGINDATA", bad, "PluginData",
			false, "", "acme.shadow"},
		{"malformed-then-valid-variant", bad, "plugindata", good, "PluginData",
			true, "acme.good", "acme.bad"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			record := []byte(`{"id":"r1","` + testCase.firstKey + `":` +
				testCase.first + `,"` + testCase.lastKey + `":` + testCase.last + `}`)
			clean, quarantined, err := SanitizeRecord(RecordHost, "r1", record)
			if err != nil {
				t.Fatalf("SanitizeRecord: %v", err)
			}
			var top map[string]json.RawMessage
			if err := json.Unmarshal(clean, &top); err != nil {
				t.Fatalf("clean record: %v", err)
			}
			if bytes.Contains(clean, []byte(testCase.shadowed)) {
				t.Fatalf("the shadowed container survived: %s", clean)
			}
			if testCase.valid {
				assertOnlyCanonicalPluginDataProperty(t, clean)
				namespaces, rejected, err := DecodeMapLenient(top[JSONField])
				if err != nil {
					t.Fatalf("canonical container: %v", err)
				}
				if len(rejected) != 0 {
					t.Fatalf("canonical container quarantines = %+v", rejected)
				}
				if len(quarantined) != 0 || len(namespaces) != 1 {
					t.Fatalf("valid last container = %+v, quarantines = %+v",
						namespaces, quarantined)
				}
				if _, ok := namespaces[testCase.expect]; !ok {
					t.Fatalf("last container %s was lost: %+v",
						testCase.expect, namespaces)
				}
				return
			}
			// The last container was malformed-only: the canonical
			// representation is the absent property.
			if properties, err := ScanPluginDataProperties(clean); err != nil ||
				properties.Found {
				t.Fatalf("all-quarantined container survived: %s, %v", clean, err)
			}
			if len(quarantined) != 1 ||
				quarantined[0].PluginID != "acme.bad" ||
				quarantined[0].Reason != ReasonNamespace {
				t.Fatalf("malformed last container quarantines = %+v", quarantined)
			}
		})
	}
}

func TestSanitizeRecordDropsACaseVariantNullContainer(t *testing.T) {
	for _, spelling := range []string{"PluginData", "PLUGINDATA", "plugindata"} {
		t.Run(spelling, func(t *testing.T) {
			record := []byte(`{"id":"r1","label":"keep","` + spelling + `":null}`)
			clean, quarantined, err := SanitizeRecord(RecordHost, "r1", record)
			if err != nil {
				t.Fatalf("SanitizeRecord: %v", err)
			}
			if len(quarantined) != 0 {
				t.Fatalf("quarantines = %+v", quarantined)
			}
			if string(clean) != `{"id":"r1","label":"keep"}` {
				t.Fatalf("canonical null container = %s", clean)
			}
			if properties, err := ScanPluginDataProperties(clean); err != nil ||
				properties.Found {
				t.Fatalf("a null container survived: %s, %v", clean, err)
			}
		})
	}
}

func TestBoundRecordIDIsBoundedAndUTF8Safe(t *testing.T) {
	huge := strings.Repeat("r", 1<<20)
	bounded := BoundRecordID(huge)
	if len(bounded) != MaxDiagnosticRecordIDBytes {
		t.Fatalf("bounded size = %d, want %d", len(bounded), MaxDiagnosticRecordIDBytes)
	}
	if bounded != huge[:MaxDiagnosticRecordIDBytes] {
		t.Fatal("bounding is not a deterministic prefix")
	}
	multi := BoundRecordID(strings.Repeat("é", 200))
	if len(multi) > MaxDiagnosticRecordIDBytes || !utf8.ValidString(multi) ||
		multi != strings.Repeat("é", MaxDiagnosticRecordIDBytes/2) {
		t.Fatalf("multi-byte record ID = %q (%d bytes)", multi, len(multi))
	}
	invalid := BoundRecordID("bad\xff" + strings.Repeat("x", 10))
	if !utf8.ValidString(invalid) || len(invalid) > MaxDiagnosticRecordIDBytes {
		t.Fatalf("invalid UTF-8 record ID = %q", invalid)
	}
	if short := BoundRecordID("h1"); short != "h1" {
		t.Fatalf("a short record ID changed: %q", short)
	}
}

// multiNamespaceRecordAtLimit builds two valid namespaces whose canonical
// encoding is exactly MaxRecordBytes+over. The first namespace is always the
// larger one, so a one-byte-over record must quarantine acme.aa.
func multiNamespaceRecordAtLimit(t *testing.T, over int) Map {
	t.Helper()
	fieldsA := map[string]json.RawMessage{
		"a": sizedFieldValue("a", MaxFieldBytes),
		"b": sizedFieldValue("b", MaxFieldBytes),
		"c": sizedFieldValue("c", MaxFieldBytes),
	}
	fieldsB := map[string]json.RawMessage{
		"d": sizedFieldValue("d", MaxFieldBytes),
	}
	value := Map{
		"acme.aa": {SchemaVersion: 1, Fields: fieldsA},
		"acme.bb": {SchemaVersion: 1, Fields: fieldsB},
	}
	size := encodedMapSize(t, value)
	delta := size - MaxRecordBytes - over
	if delta < 0 || delta > MaxFieldBytes-2 {
		t.Fatalf("multi-namespace fixture needs delta %d", delta)
	}
	fieldsA["a"] = sizedFieldValue("a", MaxFieldBytes-delta)
	if got := encodedMapSize(t, value); got != MaxRecordBytes+over {
		t.Fatalf("record construction = %d bytes, want %d", got, MaxRecordBytes+over)
	}
	return value
}

func TestFitRecordKeepsTheExactBoundaryAcrossMultipleNamespaces(t *testing.T) {
	for _, fill := range []string{"<", ">", "&"} {
		t.Run(fill, func(t *testing.T) {
			exact := multiNamespaceRecordAtLimit(t, 0)
			clean, quarantined, err := SanitizeLenient(exact)
			if err != nil {
				t.Fatalf("SanitizeLenient exact: %v", err)
			}
			if len(quarantined) != 0 || len(clean) != 2 {
				t.Fatalf("exact-limit record = %d namespaces, %+v",
					len(clean), quarantined)
			}
			if size := encodedMapSize(t, clean); size != MaxRecordBytes {
				t.Fatalf("exact boundary re-encoded to %d bytes", size)
			}

			over := multiNamespaceRecordAtLimit(t, 1)
			clean, quarantined, err = SanitizeLenient(over)
			if err != nil {
				t.Fatalf("SanitizeLenient over: %v", err)
			}
			if len(quarantined) != 1 ||
				quarantined[0].PluginID != "acme.aa" ||
				quarantined[0].Reason != ReasonRecordSize {
				t.Fatalf("one-byte-over quarantines = %+v", quarantined)
			}
			if _, ok := clean["acme.aa"]; ok {
				t.Fatalf("the largest namespace was not quarantined: %+v", clean)
			}
			if _, ok := clean["acme.bb"]; !ok {
				t.Fatalf("the smaller namespace was lost: %+v", clean)
			}
			if size := encodedMapSize(t, clean); size > MaxRecordBytes {
				t.Fatalf("stored remainder = %d bytes, above %d", size, MaxRecordBytes)
			}
		})
	}
}

func TestFitRecordKeepsAnEmptyOrSingleNamespace(t *testing.T) {
	clean, quarantined, err := SanitizeLenient(Map{})
	if err != nil || len(clean) != 0 || len(quarantined) != 0 {
		t.Fatalf("empty map = %+v, %+v, %v", clean, quarantined, err)
	}
	clean, quarantined, err = SanitizeLenient(Map{
		"acme.data": {SchemaVersion: 1, Fields: map[string]json.RawMessage{
			"name": json.RawMessage(`"ok"`),
		}},
	})
	if err != nil || len(clean) != 1 || len(quarantined) != 0 {
		t.Fatalf("single namespace = %+v, %+v, %v", clean, quarantined, err)
	}

	fields := map[string]json.RawMessage{}
	for index := 0; index < 5; index++ {
		fields[string(rune('a'+index))] = sizedFieldValue("x", MaxFieldBytes)
	}
	single := Map{"acme.huge": {SchemaVersion: 1, Fields: fields}}
	if err := Validate(single); !errors.Is(err, ErrRecordTooLarge) {
		t.Fatalf("single over-limit fixture error = %v", err)
	}
	clean, quarantined, err = SanitizeLenient(single)
	if err != nil {
		t.Fatalf("SanitizeLenient single over-limit: %v", err)
	}
	if len(clean) != 0 || len(quarantined) != 1 ||
		quarantined[0].PluginID != "acme.huge" ||
		quarantined[0].Reason != ReasonRecordSize {
		t.Fatalf("single over-limit = %+v, %+v", clean, quarantined)
	}
}

func TestFitRecordAccountsExactlyForLargePluginIDsAndFieldKeys(t *testing.T) {
	largeID := strings.Repeat("a", 62) + ".b"
	if len(largeID) != 64 || !ValidPluginID(largeID) {
		t.Fatalf("large Plugin ID fixture = %q", largeID)
	}
	largeFieldKey := strings.Repeat("k", MaxFieldKeyBytes)
	value := Map{largeID: {SchemaVersion: 1, Fields: map[string]json.RawMessage{
		largeFieldKey: json.RawMessage(`"v"`),
	}}}
	for index := 0; index < 1500; index++ {
		value[fmt.Sprintf("q%05d.n", index)] = Namespace{
			SchemaVersion: 1,
			Fields:        map[string]json.RawMessage{"n": json.RawMessage("1")},
		}
	}
	if size := encodedMapSize(t, value); size <= MaxRecordBytes {
		t.Fatalf("large-key fixture = %d bytes, want above %d", size, MaxRecordBytes)
	}
	clean, quarantined, err := SanitizeLenient(value)
	if err != nil {
		t.Fatalf("SanitizeLenient: %v", err)
	}
	size := encodedMapSize(t, clean)
	if size > MaxRecordBytes {
		t.Fatalf("stored remainder = %d bytes, above %d", size, MaxRecordBytes)
	}
	// Recomputing the container from its members must match the encoder: "{"
	// plus every encoded key, colon, and encoded namespace, plus commas, plus
	// "}".
	expected := 2
	index := 0
	for pluginID, namespace := range clean {
		encodedKey, err := marshalNoEscape(pluginID)
		if err != nil {
			t.Fatalf("encode key: %v", err)
		}
		encodedValue, err := marshalNoEscape(namespace)
		if err != nil {
			t.Fatalf("encode namespace: %v", err)
		}
		expected += len(encodedKey) + 1 + len(encodedValue)
		index++
	}
	if index > 0 {
		expected += index - 1
	}
	if size != expected {
		t.Fatalf("encoded size = %d, member accounting = %d", size, expected)
	}
	found := false
	for _, entry := range quarantined {
		if entry.PluginID == largeID {
			found = true
			if entry.Reason != ReasonRecordSize {
				t.Fatalf("large Plugin ID quarantine = %+v", entry)
			}
			if entry.SizeBytes != lenMustMarshal(t, value[largeID]) {
				t.Fatalf("large Plugin ID size = %d", entry.SizeBytes)
			}
		}
	}
	if !found {
		t.Fatalf("the largest namespace was not quarantined first: %+v", quarantined)
	}
}

func lenMustMarshal(t *testing.T, namespace Namespace) int {
	t.Helper()
	data, err := marshalNoEscape(namespace)
	if err != nil {
		t.Fatalf("marshalNoEscape: %v", err)
	}
	return len(data)
}

func TestFitRecordScalesToEightThousandNamespaces(t *testing.T) {
	const count = 8000
	value := make(Map, count)
	for index := 0; index < count; index++ {
		value[fmt.Sprintf("p%05d.n", index)] = Namespace{
			SchemaVersion: 1,
			Fields:        map[string]json.RawMessage{"n": json.RawMessage("1")},
		}
	}
	start := time.Now()
	clean, quarantined, err := SanitizeLenient(value)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("SanitizeLenient: %v", err)
	}
	t.Logf("SanitizeLenient(%d namespaces) = %s, kept %d, quarantined %d",
		count, elapsed, len(clean), len(quarantined))
	if elapsed > 2*time.Second {
		t.Fatalf("SanitizeLenient(%d) took %s, above the 2s hard bound",
			count, elapsed)
	}
	if len(quarantined) == 0 || len(clean) == 0 {
		t.Fatalf("kept %d, quarantined %d", len(clean), len(quarantined))
	}
	if size := encodedMapSize(t, clean); size > MaxRecordBytes {
		t.Fatalf("stored remainder = %d bytes, above %d", size, MaxRecordBytes)
	}
	// Equal-size members are removed largest-first with an ascending Plugin ID
	// tiebreak: the quarantined set must be exactly the ascending prefix.
	removed := make(map[string]bool, len(quarantined))
	for _, entry := range quarantined {
		if entry.Reason != ReasonRecordSize {
			t.Fatalf("quarantine reason = %+v", entry)
		}
		removed[entry.PluginID] = true
	}
	firstKept := -1
	for index := 0; index < count; index++ {
		pluginID := fmt.Sprintf("p%05d.n", index)
		if _, ok := clean[pluginID]; ok && firstKept < 0 {
			firstKept = index
		}
		if removed[pluginID] != (firstKept < 0 || index < firstKept) {
			t.Fatalf("non-deterministic removal at %s (first kept %d)",
				pluginID, firstKept)
		}
	}

	repeat, repeatQuarantined, err := SanitizeLenient(value)
	if err != nil {
		t.Fatalf("SanitizeLenient repeat: %v", err)
	}
	if encodedMapSize(t, repeat) != encodedMapSize(t, clean) ||
		len(repeatQuarantined) != len(quarantined) {
		t.Fatal("repeated sanitize is not deterministic")
	}
	for index := range quarantined {
		if repeatQuarantined[index].PluginID != quarantined[index].PluginID {
			t.Fatal("repeated quarantine order is not deterministic")
		}
	}
}
