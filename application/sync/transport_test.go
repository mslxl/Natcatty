package sync

import (
	"encoding/json"
	"testing"

	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// TestNormalizeHostRecordCarriesTheTransport pins the CRDT register shape: the
// transport is an ordinary Host field, the input spelling "ssh" never becomes
// state, and a non-SSH protocol carries no transport.
func TestNormalizeHostRecordCarriesTheTransport(t *testing.T) {
	tests := []struct {
		name      string
		record    string
		protocol  string
		transport string
		hasField  bool
	}{
		{"mosh", `{"label":"H","protocol":"ssh","transport":"mosh"}`, "ssh", "mosh", true},
		{"et", `{"label":"H","protocol":"SSH","transport":"ET"}`, "ssh", "et", true},
		{"plain ssh input", `{"label":"H","protocol":"ssh","transport":"ssh"}`, "ssh", "", false},
		// An unknown transport is preserved for validation; it is not accepted
		// as plain SSH and has no compatibility alias.
		{"unknown transport", `{"label":"H","protocol":"ssh","transport":"ssh2"}`, "ssh", "ssh2", true},
		{"blank protocol", `{"label":"H"}`, "ssh", "", false},
		{"non-ssh clears transport", `{"label":"H","protocol":"telnet","transport":"mosh"}`, "telnet", "", false},
		// An unknown protocol is preserved for validation and cannot carry a
		// transport. It is not mapped to ssh or to a transport.
		{"removed mosh protocol", `{"label":"H","protocol":"Mosh"}`, "mosh", "", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			normalized, err := normalizeHostRecord(json.RawMessage(test.record))
			if err != nil {
				t.Fatalf("normalizeHostRecord: %v", err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(normalized, &fields); err != nil {
				t.Fatalf("unmarshal normalized: %v", err)
			}
			var protocol string
			if err := json.Unmarshal(fields["protocol"], &protocol); err != nil {
				t.Fatalf("protocol = %q: %v", fields["protocol"], err)
			}
			if protocol != test.protocol {
				t.Errorf("protocol = %q, want %q", protocol, test.protocol)
			}
			rawTransport, ok := fields["transport"]
			if ok != test.hasField {
				t.Fatalf("transport field present = %v, want %v (%s)", ok, test.hasField, normalized)
			}
			if ok {
				var transport string
				if err := json.Unmarshal(rawTransport, &transport); err != nil {
					t.Fatalf("transport = %q: %v", rawTransport, err)
				}
				if transport != test.transport {
					t.Errorf("transport = %q, want %q", transport, test.transport)
				}
			}
		})
	}
}

func TestDecodePayloadBytesCanonicalizesTheTransport(t *testing.T) {
	document := `{"hosts":[
		{"id":"ssh","label":"plain","hostname":"plain.example","port":22,"protocol":"ssh","transport":"ssh"},
		{"id":"mosh","label":"mosh","hostname":"mosh.example","port":22,"protocol":"ssh","transport":"mosh"},
		{"id":"et","label":"et","hostname":"et.example","port":22,"protocol":"ssh","transport":"et"},
		{"id":"telnet","label":"telnet","hostname":"telnet.example","port":23,"protocol":"telnet","transport":"mosh"}
	]}`
	payload, err := decodePayloadBytes([]byte(document))
	if err != nil {
		t.Fatalf("decodePayloadBytes: %v", err)
	}
	want := map[string]domainvault.Transport{
		"ssh":    domainvault.TransportPlain,
		"mosh":   domainvault.TransportMosh,
		"et":     domainvault.TransportET,
		"telnet": domainvault.TransportPlain,
	}
	for _, host := range payload.Hosts {
		if got, ok := want[string(host.ID)]; !ok {
			t.Fatalf("unexpected Host %+v", host)
		} else if host.Transport != got {
			t.Errorf("%s transport = %q, want %q", host.ID, host.Transport, got)
		}
	}
}

// TestPayloadJSONRoundTripsTheTransport proves the v1/v2 envelope carries the
// field without a separate encoding path.
func TestPayloadJSONRoundTripsTheTransport(t *testing.T) {
	mosh := host("m", "mosh", "mosh.example")
	mosh.Transport = domainvault.TransportMosh
	payload := payloadWithHosts(mosh)
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	decoded, err := decodePayloadBytes(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(decoded.Hosts) != 1 || decoded.Hosts[0].Transport != domainvault.TransportMosh {
		t.Fatalf("decoded hosts = %+v", decoded.Hosts)
	}
}
