package vaultbackup

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func sampleSnapshot() Snapshot {
	return Snapshot{
		SchemaVersion: 10,
		Tables: []Table{
			{Name: TableHosts, Rows: []Row{
				{"id": Text("h1"), "port": Int(22), "data": Text(`{"id":"h1"}`)},
				{"id": Text("h2"), "port": Int(2222), "data": Text(`{"id":"h2"}`)},
			}},
			{Name: TableKeys, Rows: []Row{
				{"id": Text("k1"), "data": Blob([]byte{0, 1, 2})},
			}},
		},
	}
}

func TestSanitizeRetentionClampsToBounds(t *testing.T) {
	cases := map[int]int{-1: 1, 0: 1, 1: 1, 20: 20, 100: 100, 101: 100, 1000: 100}
	for input, want := range cases {
		if got := SanitizeRetention(input); got != want {
			t.Errorf("SanitizeRetention(%d) = %d, want %d", input, got, want)
		}
	}
	if DefaultRetention < MinRetention || DefaultRetention > MaxRetention {
		t.Fatalf("default retention %d outside %d-%d", DefaultRetention, MinRetention, MaxRetention)
	}
	if MaxPayloadBytes != 25<<20 {
		t.Fatalf("payload cap = %d, want 25 MiB", MaxPayloadBytes)
	}
}

func TestCanonicalBytesIgnoreTableAndRowOrder(t *testing.T) {
	forward := sampleSnapshot()
	reversed := Snapshot{
		SchemaVersion: 10,
		Tables: []Table{
			{Name: TableKeys, Rows: []Row{
				{"data": Blob([]byte{0, 1, 2}), "id": Text("k1")},
			}},
			{Name: TableHosts, Rows: []Row{
				{"data": Text(`{"id":"h2"}`), "port": Int(2222), "id": Text("h2")},
				{"data": Text(`{"id":"h1"}`), "port": Int(22), "id": Text("h1")},
			}},
		},
	}
	first, err := CanonicalBytes(forward)
	if err != nil {
		t.Fatalf("CanonicalBytes: %v", err)
	}
	second, err := CanonicalBytes(reversed)
	if err != nil {
		t.Fatalf("CanonicalBytes reversed: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("canonical bytes depend on order:\n%s\n%s", first, second)
	}
	firstPrint, _ := Fingerprint(forward)
	secondPrint, _ := Fingerprint(reversed)
	if firstPrint != secondPrint {
		t.Fatalf("fingerprints differ: %s vs %s", firstPrint, secondPrint)
	}
}

func TestParseRoundTripsEveryValueKind(t *testing.T) {
	snapshot := Snapshot{SchemaVersion: 10, Tables: []Table{{
		Name: TableSecrets,
		Rows: []Row{{
			"entity_type": Text("host"),
			"entity_id":   Text("h1"),
			"key_version": Int(3),
			"ratio":       Float(1.5),
			"retired_at":  Null(),
			"nonce":       Blob([]byte{9, 8, 7}),
		}},
	}}}
	encoded, err := CanonicalBytes(snapshot)
	if err != nil {
		t.Fatalf("CanonicalBytes: %v", err)
	}
	decoded, err := Parse(encoded)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	row := decoded.Tables[0].Rows[0]
	if row["key_version"].Kind != KindInt || row["key_version"].Int != 3 {
		t.Fatalf("int did not round trip: %+v", row["key_version"])
	}
	if row["ratio"].Kind != KindFloat || row["ratio"].Float != 1.5 {
		t.Fatalf("float did not round trip: %+v", row["ratio"])
	}
	if row["retired_at"].Kind != KindNull {
		t.Fatalf("null did not round trip: %+v", row["retired_at"])
	}
	if !bytes.Equal(row["nonce"].Blob, []byte{9, 8, 7}) {
		t.Fatalf("blob did not round trip: %+v", row["nonce"])
	}
}

func TestValidateRejectsMalformedSnapshots(t *testing.T) {
	cases := map[string]Snapshot{
		"no schema":       {Tables: []Table{{Name: TableHosts}}},
		"unknown table":   {SchemaVersion: 10, Tables: []Table{{Name: "unknown"}}},
		"duplicate table": {SchemaVersion: 10, Tables: []Table{{Name: TableHosts}, {Name: TableHosts}}},
		"empty row":       {SchemaVersion: 10, Tables: []Table{{Name: TableHosts, Rows: []Row{{}}}}},
		"unknown kind": {SchemaVersion: 10, Tables: []Table{{Name: TableHosts,
			Rows: []Row{{"id": {Kind: "guid"}}}}}},
	}
	for name, snapshot := range cases {
		if err := Validate(snapshot); !errors.Is(err, ErrInvalidSnapshot) {
			t.Errorf("%s: Validate = %v, want ErrInvalidSnapshot", name, err)
		}
	}
}

// Duplicate primary keys are deliberately not rejected here: the restore
// transaction must hit the database constraint and roll back, which is what
// proves the failed-restore path.
func TestValidateAllowsDuplicatePrimaryKeys(t *testing.T) {
	snapshot := Snapshot{SchemaVersion: 10, Tables: []Table{{Name: TableHosts, Rows: []Row{
		{"id": Text("same")},
		{"id": Text("same")},
	}}}}
	if err := Validate(snapshot); err != nil {
		t.Fatalf("Validate rejected duplicate keys: %v", err)
	}
}

func TestCountsAndEmpty(t *testing.T) {
	snapshot := sampleSnapshot()
	counts := snapshot.Counts()
	if counts.Hosts != 2 || counts.Keys != 1 || counts.Total() != 3 {
		t.Fatalf("counts = %+v", counts)
	}
	if snapshot.Empty() {
		t.Fatal("a snapshot with records reported empty")
	}
	empty := Snapshot{SchemaVersion: 10, Tables: []Table{{Name: TableHosts}}}
	if !empty.Empty() {
		t.Fatal("an empty table reported non-empty")
	}
}

func TestReasonValid(t *testing.T) {
	for _, reason := range []Reason{ReasonVaultChange, ReasonBeforeRestore, ReasonAppVersionChange} {
		if !reason.Valid() {
			t.Errorf("%q should be valid", reason)
		}
	}
	if Reason("manual").Valid() {
		t.Error("unknown reason was accepted")
	}
}

func TestParseRejectsInvalidJSON(t *testing.T) {
	if _, err := Parse([]byte("{")); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("Parse = %v, want ErrInvalidSnapshot", err)
	}
	if _, err := Parse([]byte(strings.Repeat("x", 64))); err == nil {
		t.Fatal("Parse accepted garbage")
	}
}
