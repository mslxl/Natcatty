package plugindata

import (
	"bytes"
	"testing"
)

// TestSanitizeRecordCanonicalizesEveryEmptyContainerToAbsent pins the one
// representation of "this record has no surviving namespace": the pluginData
// property is absent. Malformed-only, empty, null, and case-variant spellings
// must all sanitize to the same record bytes, because the v2 envelope
// comparison treats a difference between {} or null and absence as damage.
func TestSanitizeRecordCanonicalizesEveryEmptyContainerToAbsent(t *testing.T) {
	cases := []struct {
		name        string
		record      string
		quarantines int
		reason      Reason
		pluginID    string
	}{
		{
			name:        "malformed-only",
			record:      `{"id":"r1","label":"keep","pluginData":{"acme.bad":null}}`,
			quarantines: 1, reason: ReasonNamespace, pluginID: "acme.bad",
		},
		{
			name:        "empty-object",
			record:      `{"id":"r1","label":"keep","pluginData":{}}`,
			quarantines: 0,
		},
		{
			name:        "null",
			record:      `{"id":"r1","label":"keep","pluginData":null}`,
			quarantines: 0,
		},
		{
			name:        "variant-null",
			record:      `{"id":"r1","label":"keep","PluginData":null}`,
			quarantines: 0,
		},
		{
			name:        "variant-empty",
			record:      `{"id":"r1","label":"keep","PLUGINDATA":{}}`,
			quarantines: 0,
		},
		{
			name:        "duplicate-null",
			record:      `{"id":"r1","label":"keep","pluginData":{},"pluginData":null}`,
			quarantines: 0,
		},
	}
	for _, kind := range []RecordKind{RecordHost, RecordDataProfile} {
		for _, testCase := range cases {
			t.Run(string(kind)+"/"+testCase.name, func(t *testing.T) {
				clean, quarantined, err := SanitizeRecord(kind, "r1",
					[]byte(testCase.record))
				if err != nil {
					t.Fatalf("SanitizeRecord: %v", err)
				}
				if string(clean) != `{"id":"r1","label":"keep"}` {
					t.Fatalf("canonical empty record = %s", clean)
				}
				if properties, err := ScanPluginDataProperties(clean); err != nil ||
					properties.Found {
					t.Fatalf("pluginData survived: %s, %v", clean, err)
				}
				if len(quarantined) != testCase.quarantines {
					t.Fatalf("quarantines = %+v", quarantined)
				}
				if testCase.quarantines > 0 {
					if quarantined[0].PluginID != testCase.pluginID ||
						quarantined[0].Reason != testCase.reason {
						t.Fatalf("quarantine = %+v", quarantined[0])
					}
				}
			})
		}
	}
}

// TestSanitizeRecordKeepsAVerbatimRecordWithASurvivingNamespace proves the
// canonicalization above does not start rewriting records that still carry a
// valid namespace: the byte-for-byte round trip is preserved.
func TestSanitizeRecordKeepsAVerbatimRecordWithASurvivingNamespace(t *testing.T) {
	record := []byte(`{"id":"r1","label":"keep","pluginData":` +
		`{"acme.good":{"fields":{"name":"ok"},"schemaVersion":1}}}`)
	clean, quarantined, err := SanitizeRecord(RecordHost, "r1", record)
	if err != nil {
		t.Fatalf("SanitizeRecord: %v", err)
	}
	if len(quarantined) != 0 || !bytes.Equal(clean, record) {
		t.Fatalf("valid record changed: %s, quarantines = %+v", clean, quarantined)
	}
}
