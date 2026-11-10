package sync

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/mslxl/natcatty/domain/model/plugindata"
	domainsync "github.com/mslxl/natcatty/domain/sync"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// TestDecodePayloadRecordCanonicalizesEveryEmptyContainerToAbsent proves the
// v1/v2 payload decoder and SanitizeRecord agree on the one representation of
// a record with no surviving namespace. The v2 envelope comparison compares
// the state sanitizer with this decoder, so a difference here is exactly the
// envelope-materialized-mismatch blocker.
func TestDecodePayloadRecordCanonicalizesEveryEmptyContainerToAbsent(t *testing.T) {
	cases := []struct {
		name        string
		pluginData  string
		quarantines int
		reason      plugindata.Reason
		pluginID    string
	}{
		{
			name: "malformed-only", pluginData: `{"acme.bad":null}`,
			quarantines: 1, reason: plugindata.ReasonNamespace, pluginID: "acme.bad",
		},
		{
			name: "empty-object", pluginData: `{}`,
		},
		{
			name: "null", pluginData: `null`,
		},
	}
	for _, kind := range []plugindata.RecordKind{
		plugindata.RecordHost, plugindata.RecordDataProfile,
	} {
		for _, testCase := range cases {
			for _, spelling := range []string{
				"pluginData", "PluginData", "PLUGINDATA",
			} {
				t.Run(string(kind)+"/"+testCase.name+"/"+spelling, func(t *testing.T) {
					record := json.RawMessage(`{"id":"r1","label":"keep","` +
						spelling + `":` + testCase.pluginData + `}`)
					clean, rejected, err := decodePayloadRecord(kind, record)
					if err != nil {
						t.Fatalf("decodePayloadRecord: %v", err)
					}
					want := `{"id":"r1","label":"keep"}`
					if string(clean) != want {
						t.Fatalf("decoded empty record = %s, want %s", clean, want)
					}
					if len(rejected) != testCase.quarantines {
						t.Fatalf("rejected = %+v", rejected)
					}
					if testCase.quarantines > 0 {
						entry := rejected[0].Quarantine
						if entry.PluginID != testCase.pluginID ||
							entry.Reason != testCase.reason {
							t.Fatalf("quarantine = %+v", entry)
						}
					}
					// SanitizeRecord is the stored/state-side transform. Both
					// sides of the v2 comparison must produce the same bytes.
					sanitized, _, err := plugindata.SanitizeRecord(kind, "r1",
						[]byte(record))
					if err != nil {
						t.Fatalf("SanitizeRecord: %v", err)
					}
					if !bytes.Equal(sanitized, clean) {
						t.Fatalf("SanitizeRecord = %s, decodePayloadRecord = %s",
							sanitized, clean)
					}
				})
			}
		}
	}
}

// TestNormalizeMaterializedPluginDataTreatsEmptyAsAbsent pins the
// materialization boundary: an empty namespace map and a nil map encode the
// same record, so neither can leave a container behind.
func TestNormalizeMaterializedPluginDataTreatsEmptyAsAbsent(t *testing.T) {
	base := Payload{Hosts: []domainvault.Host{{
		ID: "h1", Label: "One", Hostname: "one.example",
	}}}
	absent := base
	absent.Hosts = append([]domainvault.Host(nil), base.Hosts...)
	empty := base
	empty.Hosts = append([]domainvault.Host(nil), base.Hosts...)
	empty.Hosts[0].PluginData = plugindata.Map{}

	if err := normalizeMaterializedPluginData(&absent); err != nil {
		t.Fatalf("normalize absent: %v", err)
	}
	if err := normalizeMaterializedPluginData(&empty); err != nil {
		t.Fatalf("normalize empty: %v", err)
	}
	absentBytes, err := domainsync.MarshalJSON(absent)
	if err != nil {
		t.Fatalf("MarshalJSON absent: %v", err)
	}
	emptyBytes, err := domainsync.MarshalJSON(empty)
	if err != nil {
		t.Fatalf("MarshalJSON empty: %v", err)
	}
	if !bytes.Equal(absentBytes, emptyBytes) {
		t.Fatalf("empty map normalized differently:\nabsent=%s\nempty =%s",
			absentBytes, emptyBytes)
	}
	if bytes.Contains(absentBytes, []byte("pluginData")) {
		t.Fatalf("an empty container was materialized: %s", absentBytes)
	}
	absentSnapshot, err := payloadSnapshot(absent)
	if err != nil {
		t.Fatalf("payloadSnapshot absent: %v", err)
	}
	emptySnapshot, err := payloadSnapshot(empty)
	if err != nil {
		t.Fatalf("payloadSnapshot empty: %v", err)
	}
	if !snapshotsEqual(absentSnapshot, emptySnapshot) {
		t.Fatal("empty and absent pluginData produced different snapshots")
	}
}

// TestDecodePayloadLenientRoundTripsEmptyFamilies keeps the payload signature
// stable for a Vault with no Hosts: an explicit empty array must decode to an
// empty slice, not nil, or sealing and reloading the verified payload reports
// a signature mismatch.
func TestDecodePayloadLenientRoundTripsEmptyFamilies(t *testing.T) {
	cases := []struct {
		name  string
		hosts []domainvault.Host
	}{
		{name: "absent-hosts", hosts: nil},
		{name: "explicit-empty-hosts", hosts: []domainvault.Host{}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			original := interopF1ProfilePayload()
			original.Hosts = testCase.hosts
			encoded, err := domainsync.MarshalJSON(original)
			if err != nil {
				t.Fatalf("MarshalJSON: %v", err)
			}
			decoded, err := decodePayloadBytes(encoded)
			if err != nil {
				t.Fatalf("decodePayloadBytes: %v", err)
			}
			reencoded, err := domainsync.MarshalJSON(decoded)
			if err != nil {
				t.Fatalf("MarshalJSON decoded: %v", err)
			}
			if !bytes.Equal(encoded, reencoded) {
				t.Fatalf("payload changed across decode:\nbefore=%s\nafter =%s",
					encoded, reencoded)
			}
			if testCase.hosts != nil && decoded.Hosts == nil {
				t.Fatal("an explicit empty Hosts array decoded to nil")
			}
		})
	}
}
