package history

import (
	"strings"
	"testing"
	"time"
)

func TestNewUUIDv7EmbedsTheTimestampVersionAndVariant(t *testing.T) {
	when := time.Date(2026, 11, 6, 12, 34, 56, 789_000_000, time.UTC)
	first, err := NewUUIDv7(when)
	if err != nil {
		t.Fatalf("NewUUIDv7: %v", err)
	}
	if len(first) != 36 || first != strings.ToLower(first) {
		t.Fatalf("UUIDv7 = %q, want canonical lowercase text", first)
	}
	parsed, ok := parseUUID(first)
	if !ok {
		t.Fatalf("UUIDv7 %q did not parse", first)
	}
	if version := parsed[6] >> 4; version != 7 {
		t.Fatalf("version = %d, want 7", version)
	}
	if variant := parsed[8] >> 6; variant != 0b10 {
		t.Fatalf("variant = %#b, want RFC 4122/9562", variant)
	}
	embedded := int64(parsed[0])<<40 | int64(parsed[1])<<32 | int64(parsed[2])<<24 |
		int64(parsed[3])<<16 | int64(parsed[4])<<8 | int64(parsed[5])
	if embedded != when.UnixMilli() {
		t.Fatalf("embedded milliseconds = %d, want %d", embedded, when.UnixMilli())
	}

	second, err := NewUUIDv7(when)
	if err != nil {
		t.Fatalf("second NewUUIDv7: %v", err)
	}
	if first == second {
		t.Fatalf("two UUIDv7 values collided: %q", first)
	}
	if !IsUUID(second) {
		t.Fatalf("UUIDv7 %q was not recognized as a UUID", second)
	}
}

func TestUUIDv7UsesTheCurrentTimeAndIsValid(t *testing.T) {
	before := time.Now().UnixMilli()
	value := UUIDv7()
	after := time.Now().UnixMilli()
	if !IsUUID(value) {
		t.Fatalf("UUIDv7() = %q, not a UUID", value)
	}
	parsed, _ := parseUUID(value)
	embedded := int64(parsed[0])<<40 | int64(parsed[1])<<32 | int64(parsed[2])<<24 |
		int64(parsed[3])<<16 | int64(parsed[4])<<8 | int64(parsed[5])
	if embedded < before || embedded > after {
		t.Fatalf("embedded time %d outside [%d, %d]", embedded, before, after)
	}
}

func TestIsUUIDAcceptsAtuinIdentitiesAndRejectsNonUUIDS(t *testing.T) {
	valid := []string{
		"9b2f3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d",
		"7a4a48be-d6e8-582b-9cfb-ee8845ac7ed7",
		"9B2F3C4D-5E6F-4A7B-8C9D-0E1F2A3B4C5D",
	}
	for _, value := range valid {
		if !IsUUID(value) {
			t.Errorf("IsUUID(%q) = false, want true", value)
		}
	}
	invalid := []string{
		"",
		"legacy-local-id",
		"9b2f3c4d5e6f4a7b8c9d0e1f2a3b4c5d",
		"9b2f3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5",
		"9b2f3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5g",
		"00000000-0000-0000-0000-000000000000",
		"9b2f3c4d_5e6f_4a7b_8c9d_0e1f2a3b4c5d",
	}
	for _, value := range invalid {
		if IsUUID(value) {
			t.Errorf("IsUUID(%q) = true, want false", value)
		}
	}
}

func TestDerivedUUIDIsStableVersionFive(t *testing.T) {
	first := DerivedUUID("deploy@example.com")
	second := DerivedUUID("deploy@example.com")
	other := DerivedUUID("deploy@example.org")
	if first != second {
		t.Fatalf("DerivedUUID is not stable: %q != %q", first, second)
	}
	if first == other {
		t.Fatalf("different names derived the same UUID %q", first)
	}
	parsed, ok := parseUUID(first)
	if !ok {
		t.Fatalf("DerivedUUID %q did not parse", first)
	}
	if version := parsed[6] >> 4; version != 5 {
		t.Fatalf("version = %d, want 5", version)
	}
	if variant := parsed[8] >> 6; variant != 0b10 {
		t.Fatalf("variant = %#b, want RFC 4122/9562", variant)
	}
}

func TestSessionUUIDIsStableAndDocumented(t *testing.T) {
	if got := SessionUUID(""); got != "" {
		t.Fatalf("empty Session UUID = %q, want empty", got)
	}
	// Golden vector: UUIDv5(NetcattyNamespace, "session:session-1").
	if got := SessionUUID("session-1"); got != "7a4a48be-d6e8-582b-9cfb-ee8845ac7ed7" {
		t.Fatalf("SessionUUID = %q, want the documented derivation", got)
	}
	if SessionUUID("session-1") != SessionUUID("session-1") {
		t.Fatal("the same Session derived different UUIDs")
	}
	if SessionUUID("session-1") == SessionUUID("session-2") {
		t.Fatal("different Sessions derived the same UUID")
	}
}

func TestDerivedEntryUUIDIsStableAndIgnoresTheInvalidID(t *testing.T) {
	entry := Entry{
		ID:          "not-a-uuid",
		Source:      SourceAtuinImport,
		Origin:      "example.com:deploy",
		RecordedAt:  time.Unix(1_700_000_000, 0).UTC(),
		Command:     "git status",
		SessionUUID: "7a4a48be-d6e8-582b-9cfb-ee8845ac7ed7",
	}
	// Golden vector: UUIDv5 over the documented natural key.
	const want = "0fed996b-78a9-57e1-b49e-c60474cb8083"
	got := DerivedEntryUUID(entry)
	if got != want {
		t.Fatalf("DerivedEntryUUID = %q, want %q", got, want)
	}
	renamed := entry
	renamed.ID = "another-invalid-id"
	if DerivedEntryUUID(renamed) != got {
		t.Fatal("the derived UUID changed with the invalid ID")
	}
	changed := entry
	changed.Command = "git log"
	if DerivedEntryUUID(changed) == got {
		t.Fatal("a different natural key derived the same UUID")
	}
}

func TestHasUUIDIdentitySeparatesLegacyLocalRows(t *testing.T) {
	legacy := Entry{ID: "legacy-123"}
	if legacy.HasUUIDIdentity() {
		t.Fatal("a legacy local identity was reported as a UUID")
	}
	captured := Entry{ID: UUIDv7()}
	if !captured.HasUUIDIdentity() {
		t.Fatal("a UUIDv7 identity was not recognized")
	}
}
