package wire

import (
	"bytes"
	"encoding/hex"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
)

// The byte vectors below come from the pinned Atuin source at ad5bf5c
// (v18.24.5):
//
//   - historyV0Hex and historyV1Hex are History::deserialize fixture bytes from
//     crates/atuin-client/src/history.rs.
//   - historyCreateHex and historyDeleteHex are HistoryRecord::serialize
//     snapshots from crates/atuin-client/src/history_store.rs.
const (
	historyV0Hex = "cd000099d9203636643136636265653763643437353338653563356238623434653930303665cf1763627518d2f680ce02eed2f000aa67697420737461747573d92a2f55736572732f636f6e7261642e6c7564676174652f446f63756d656e74732f636f64652f617475696ed9206239376439613330366632373434373361323033643265626134316639343537bb6676666739333663306b70663a636f6e7261642e6c756467617465c0"
	historyV1Hex = "cd00019bd9203636643136636265653763643437353338653563356238623434653930303665cf1763627518d2f680ce02eed2f000aa67697420737461747573d92a2f55736572732f636f6e7261642e6c7564676174652f446f63756d656e74732f636f64652f617475696ed9206239376439613330366632373434373361323033643265626134316639343537bb6676666739333663306b70663a636f6e7261642e6c756467617465cf18c253eb6cce0a00ae636f6e7261642e6c756467617465ad73616d706c6520696e74656e74"

	historyCreateHex = "cc00c49acd00029dd9203031386364346665383137353763643261656536356364373836316639633831cf17a6fbd4b55200006400a26c73d9292f55736572732f656c6c69652f7372632f6769746875622e636f6d2f617475696e73682f617475696ed9203031386364346665616438393735393738353235323761333163393938303539aa626f6f703a656c6c6965c0a5656c6c6965c0a462617368c0"
	historyDeleteHex = "cc01d9203031386364346665383137353763643261656536356364373836316639633831"
)

func hexBytes(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatalf("hex: %v", err)
	}
	return decoded
}

func stringPtr(value string) *string             { return &value }
func uint64Ptr(value uint64) *uint64             { return &value }
func authorKindPtr(value AuthorKind) *AuthorKind { return &value }

func sampleV2() History {
	return History{
		ID:        "018cd4fe-8175-7cd2-aee6-5cd7861f9c81",
		Timestamp: 1704326400000000000,
		Duration:  100,
		Exit:      0,
		Command:   "ls",
		CWD:       "/Users/ellie/src/github.com/atuinsh/atuin",
		Session:   "018cd4fead897597852527a31c998059",
		Origin:    "boop:ellie",
		Author:    stringPtr("ellie"),
		Shell:     stringPtr("bash"),
	}
}

func TestUnmarshalPinnedV0(t *testing.T) {
	history, err := UnmarshalHistory("v0", hexBytes(t, historyV0Hex))
	if err != nil {
		t.Fatalf("UnmarshalHistory: %v", err)
	}
	want := History{
		ID:        "66d16cbe-e7cd-4753-8e5c-5b8b44e9006e",
		Timestamp: 1685298940633872000,
		Duration:  49206000,
		Exit:      0,
		Command:   "git status",
		CWD:       "/Users/conrad.ludgate/Documents/code/atuin",
		Session:   "b97d9a306f274473a203d2eba41f9457",
		Origin:    "fvfg936c0kpf:conrad.ludgate",
	}
	if !reflect.DeepEqual(history, want) {
		t.Fatalf("history = %#v, want %#v", history, want)
	}

	encoded, err := history.MarshalBinary(Version0)
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}
	if !bytes.Equal(encoded, hexBytes(t, historyV0Hex)) {
		t.Fatalf("encoded v0 = %x, want %s", encoded, historyV0Hex)
	}
}

func TestUnmarshalPinnedV1(t *testing.T) {
	history, err := UnmarshalHistory("v1", hexBytes(t, historyV1Hex))
	if err != nil {
		t.Fatalf("UnmarshalHistory: %v", err)
	}
	want := History{
		ID:        "66d16cbe-e7cd-4753-8e5c-5b8b44e9006e",
		Timestamp: 1685298940633872000,
		Duration:  49206000,
		Exit:      0,
		Command:   "git status",
		CWD:       "/Users/conrad.ludgate/Documents/code/atuin",
		Session:   "b97d9a306f274473a203d2eba41f9457",
		Origin:    "fvfg936c0kpf:conrad.ludgate",
		DeletedAt: uint64Ptr(1784080673000000000),
		Author:    stringPtr("conrad.ludgate"),
		Intent:    stringPtr("sample intent"),
	}
	if !reflect.DeepEqual(history, want) {
		t.Fatalf("history = %#v, want %#v", history, want)
	}

	encoded, err := history.MarshalBinary(Version1)
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}
	if !bytes.Equal(encoded, hexBytes(t, historyV1Hex)) {
		t.Fatalf("encoded v1 = %x, want %s", encoded, historyV1Hex)
	}
}

func TestUnmarshalPinnedCreateRecord(t *testing.T) {
	data := hexBytes(t, historyCreateHex)
	record, err := UnmarshalHistoryRecord("v2", data)
	if err != nil {
		t.Fatalf("UnmarshalHistoryRecord: %v", err)
	}
	if record.Kind != HistoryRecordCreate || record.History == nil {
		t.Fatalf("record = %#v, want a create", record)
	}
	want := sampleV2()
	if !reflect.DeepEqual(*record.History, want) {
		t.Fatalf("history = %#v, want %#v", *record.History, want)
	}

	encoded, err := record.MarshalBinary(Version2)
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}
	if !bytes.Equal(encoded, data) {
		t.Fatalf("encoded create = %x, want %x", encoded, data)
	}

	// The payload itself decodes with the same result; the wrapper only adds
	// the record kind and the bin envelope.
	payload := hexBytes(t, historyCreateHex)[4:]
	direct, err := UnmarshalHistory("v2", payload)
	if err != nil {
		t.Fatalf("UnmarshalHistory: %v", err)
	}
	if !reflect.DeepEqual(direct, want) {
		t.Fatalf("direct history = %#v, want %#v", direct, want)
	}
}

func TestUnmarshalPinnedDeleteRecord(t *testing.T) {
	data := hexBytes(t, historyDeleteHex)
	record, err := UnmarshalHistoryRecord("v2", data)
	if err != nil {
		t.Fatalf("UnmarshalHistoryRecord: %v", err)
	}
	want := HistoryRecord{Kind: HistoryRecordDelete, DeleteID: "018cd4fe-8175-7cd2-aee6-5cd7861f9c81"}
	if !reflect.DeepEqual(record, want) {
		t.Fatalf("record = %#v, want %#v", record, want)
	}
	encoded, err := record.MarshalBinary(Version2)
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}
	if !bytes.Equal(encoded, data) {
		t.Fatalf("encoded delete = %x, want %x", encoded, data)
	}
}

func TestHistoryRoundTrip(t *testing.T) {
	base := sampleV2()
	v1Base := sampleV2()
	v1Base.Shell = nil
	v1Base.AuthorKind = nil
	v1Intent := v1Base
	v1Intent.Intent = stringPtr("why")
	tests := []struct {
		name    string
		version Version
		history History
	}{
		{"v0 minimal", Version0, History{
			ID: base.ID, Timestamp: base.Timestamp, Command: base.Command,
			CWD: base.CWD, Session: base.Session, Origin: base.Origin,
		}},
		{"v0 boundaries", Version0, History{
			ID: "ffffffff-ffff-ffff-ffff-ffffffffffff", Timestamp: math.MaxUint64,
			Duration: math.MinInt64, Exit: math.MinInt64, Command: "x\x00y",
			CWD: "/", Session: "00000000000000000000000000000000", Origin: "",
		}},
		{"v1 minimal", Version1, History{
			ID: base.ID, Timestamp: 0, Duration: 0, Exit: 0,
			Command: base.Command, CWD: base.CWD, Session: base.Session,
			Origin: base.Origin, Author: stringPtr(""),
		}},
		{"v1 with intent", Version1, v1Intent},
		{"v1 boundaries", Version1, History{
			ID: base.ID, Timestamp: 1, Duration: math.MaxInt64, Exit: math.MaxInt64,
			Command: base.Command, CWD: base.CWD, Session: base.Session,
			Origin: base.Origin, DeletedAt: uint64Ptr(math.MaxUint64),
			Author: stringPtr("agent"), Intent: stringPtr(""),
		}},
		{"v2 unicode", Version2, History{
			ID: base.ID, Timestamp: 1704326400000000000, Duration: 1500000000,
			Exit: -1, Command: "echo 'héllo 世界 🚀 שלום'\nprintf 'a\\nb'",
			CWD: "/tmp/дир/📁", Session: base.Session, Origin: "host:üser",
			Author: stringPtr("üser"), Intent: stringPtr("显示状态\n第二行"),
			Shell: stringPtr("zsh"), AuthorKind: authorKindPtr(AuthorKindUser),
		}},
		{"v2 full", Version2, base},
		{"v2 deleted", Version2, History{
			ID: base.ID, Timestamp: base.Timestamp, Duration: 2, Exit: 255,
			Command: "false\n", CWD: "/", Session: base.Session, Origin: "h:u",
			DeletedAt: uint64Ptr(1700425080000000000), Author: stringPtr("u"),
			Intent: stringPtr("i"), Shell: stringPtr("fish"), AuthorKind: authorKindPtr(AuthorKindAgent),
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := test.history.MarshalBinary(test.version)
			if err != nil {
				t.Fatalf("MarshalBinary: %v", err)
			}
			decoded, err := UnmarshalHistory(string(test.version), encoded)
			if err != nil {
				t.Fatalf("UnmarshalHistory: %v", err)
			}
			want := test.history
			if test.version == Version0 {
				// v0 carries no author, intent, shell, or kind; the wire value
				// is the raw origin and the adapter resolves the author.
				want.Author, want.Intent, want.Shell, want.AuthorKind = nil, nil, nil, nil
				want.DeletedAt = nil
			}
			if !reflect.DeepEqual(decoded, want) {
				t.Fatalf("decoded = %#v, want %#v", decoded, want)
			}
		})
	}
}

func TestHistoryIDCanonicalization(t *testing.T) {
	history := sampleV2()
	history.ID = "018CD4FE81757CD2AEE65CD7861F9C81"
	encoded, err := history.MarshalBinary(Version2)
	if err != nil {
		t.Fatal(err)
	}
	// The payload carries the simple lowercase form.
	want := "018cd4fe81757cd2aee65cd7861f9c81"
	if !bytes.Contains(encoded, []byte(want)) {
		t.Fatalf("encoded id is not %q: %x", want, encoded)
	}
	decoded, err := UnmarshalHistory("v2", encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.ID != "018cd4fe-8175-7cd2-aee6-5cd7861f9c81" {
		t.Fatalf("decoded id = %q", decoded.ID)
	}
	for _, input := range []string{
		"{018CD4FE-8175-7CD2-AEE6-5CD7861F9C81}",
		"urn:uuid:018CD4FE-8175-7CD2-AEE6-5CD7861F9C81",
	} {
		history.ID = input
		encoded, err := history.MarshalBinary(Version2)
		if err != nil {
			t.Fatalf("id %q: %v", input, err)
		}
		decoded, err := UnmarshalHistory("v2", encoded)
		if err != nil || decoded.ID != "018cd4fe-8175-7cd2-aee6-5cd7861f9c81" {
			t.Fatalf("id %q decoded as %q, %v", input, decoded.ID, err)
		}
	}
	for _, invalid := range []string{"", "not-a-uuid", "018cd4fe-8175-7cd2-aee6-5cd7861f9c8", "g18cd4fe-8175-7cd2-aee6-5cd7861f9c81"} {
		history.ID = invalid
		if _, err := history.MarshalBinary(Version2); !errors.Is(err, ErrMalformed) {
			t.Fatalf("id %q error = %v, want ErrMalformed", invalid, err)
		}
	}
}

func TestHistoryIntegerBoundaries(t *testing.T) {
	values := []int64{
		math.MinInt64, math.MinInt64 + 1, math.MinInt32 - 1, math.MinInt32,
		math.MinInt16 - 1, math.MinInt16, math.MinInt8 - 1, math.MinInt8,
		-33, -32, -1, 0, 1, 127, 128, 255, 256, 65535, 65536,
		math.MaxUint32, math.MaxUint32 + 1, math.MaxInt64,
	}
	for _, value := range values {
		history := sampleV2()
		history.Duration = value
		history.Exit = value
		encoded, err := history.MarshalBinary(Version2)
		if err != nil {
			t.Fatalf("%d: MarshalBinary: %v", value, err)
		}
		decoded, err := UnmarshalHistory("v2", encoded)
		if err != nil {
			t.Fatalf("%d: UnmarshalHistory: %v", value, err)
		}
		if decoded.Duration != value || decoded.Exit != value {
			t.Fatalf("%d: decoded dur=%d exit=%d", value, decoded.Duration, decoded.Exit)
		}
	}
}

func TestHistoryTimestampBoundaries(t *testing.T) {
	for _, value := range []uint64{0, 1, math.MaxUint32, math.MaxUint32 + 1, math.MaxInt64, math.MaxUint64} {
		history := sampleV2()
		history.Timestamp = value
		encoded, err := history.MarshalBinary(Version2)
		if err != nil {
			t.Fatalf("%d: MarshalBinary: %v", value, err)
		}
		decoded, err := UnmarshalHistory("v2", encoded)
		if err != nil {
			t.Fatalf("%d: UnmarshalHistory: %v", value, err)
		}
		if decoded.Timestamp != value {
			t.Fatalf("timestamp = %d, want %d", decoded.Timestamp, value)
		}
	}
	if got := hexBytes(t, historyV0Hex)[38]; got != 0xcf {
		t.Fatalf("pinned v0 timestamp marker = 0x%02x, want 0xcf", got)
	}
}

func TestHistoryStringMarkerBoundaries(t *testing.T) {
	for _, length := range []int{0, 1, 31, 32, 255, 256, 65535, 65536} {
		history := sampleV2()
		history.Command = strings.Repeat("a", length)
		encoded, err := history.MarshalBinary(Version2)
		if err != nil {
			t.Fatalf("%d: MarshalBinary: %v", length, err)
		}
		decoded, err := UnmarshalHistory("v2", encoded)
		if err != nil {
			t.Fatalf("%d: UnmarshalHistory: %v", length, err)
		}
		if len(decoded.Command) != length {
			t.Fatalf("command length = %d, want %d", len(decoded.Command), length)
		}
	}
}

func TestHistoryV2FieldCountTolerance(t *testing.T) {
	history := sampleV2()
	history.AuthorKind = authorKindPtr(AuthorKindAgent)
	encoded, err := history.MarshalBinary(Version2)
	if err != nil {
		t.Fatal(err)
	}
	if encoded[3] != 0x90|13 {
		t.Fatalf("field count marker = 0x%02x, want 13 fields", encoded[3])
	}

	// A v2 record written before author_kind existed: one field short.
	twelve := append([]byte(nil), encoded[:len(encoded)-1]...)
	twelve[3] = 0x90 | 12
	decoded, err := UnmarshalHistory("v2", twelve)
	if err != nil {
		t.Fatalf("12-field v2: %v", err)
	}
	if decoded.AuthorKind != nil {
		t.Fatalf("author kind = %v, want nil", *decoded.AuthorKind)
	}
	want := history
	want.AuthorKind = nil
	if !reflect.DeepEqual(decoded, want) {
		t.Fatalf("decoded = %#v, want %#v", decoded, want)
	}

	// A future v2 writer appends a field: the pinned decoder ignores unknown
	// trailing fields, and so does this one.
	fourteen := append(append([]byte(nil), encoded...), 0xc0)
	fourteen[3] = 0x90 | 14
	decoded, err = UnmarshalHistory("v2", fourteen)
	if err != nil {
		t.Fatalf("14-field v2: %v", err)
	}
	if !reflect.DeepEqual(decoded, history) {
		t.Fatalf("decoded = %#v, want %#v", decoded, history)
	}

	// A sixteen-field v2 record uses an array16 header instead of a fixarray.
	// It is still a v2 record and remains readable.
	sixteen := append(append([]byte(nil), encoded...), 0xc0, 0xc0, 0xc0)
	sixteen[3] = 0xdc
	sixteen = append(sixteen[:4], append([]byte{0x00, 0x10}, sixteen[4:]...)...)
	decoded, err = UnmarshalHistory("v2", sixteen)
	if err != nil {
		t.Fatalf("16-field v2: %v", err)
	}
	if !reflect.DeepEqual(decoded, history) {
		t.Fatalf("decoded = %#v, want %#v", decoded, history)
	}
}

func TestHistoryV1IntentFieldCount(t *testing.T) {
	history := sampleV2()
	history.Shell = nil
	history.AuthorKind = nil

	withoutIntent := history
	withoutIntent.Intent = nil
	encoded, err := withoutIntent.MarshalBinary(Version1)
	if err != nil {
		t.Fatal(err)
	}
	if encoded[3] != 0x90|10 {
		t.Fatalf("v1 marker = 0x%02x, want 10 fields", encoded[3])
	}
	decoded, err := UnmarshalHistory("v1", encoded)
	if err != nil || decoded.Intent != nil {
		t.Fatalf("10-field v1 = %#v, %v", decoded, err)
	}

	withIntent := history
	withIntent.Intent = stringPtr("why")
	encoded, err = withIntent.MarshalBinary(Version1)
	if err != nil {
		t.Fatal(err)
	}
	if encoded[3] != 0x90|11 {
		t.Fatalf("v1 marker = 0x%02x, want 11 fields", encoded[3])
	}
	decoded, err = UnmarshalHistory("v1", encoded)
	if err != nil || decoded.Intent == nil || *decoded.Intent != "why" {
		t.Fatalf("11-field v1 = %#v, %v", decoded, err)
	}
}

func TestHistoryRejectsFieldsOlderVersionsCannotCarry(t *testing.T) {
	tests := []struct {
		name    string
		version Version
		mutate  func(*History)
	}{
		{"v0 author", Version0, func(h *History) { h.Author = stringPtr("a") }},
		{"v0 intent", Version0, func(h *History) { h.Intent = stringPtr("i") }},
		{"v0 shell", Version0, func(h *History) { h.Shell = stringPtr("bash") }},
		{"v0 kind", Version0, func(h *History) { h.AuthorKind = authorKindPtr(AuthorKindUser) }},
		{"v1 shell", Version1, func(h *History) { h.Shell = stringPtr("bash") }},
		{"v1 kind", Version1, func(h *History) { h.AuthorKind = authorKindPtr(AuthorKindUser) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			history := sampleV2()
			test.mutate(&history)
			if _, err := history.MarshalBinary(test.version); !errors.Is(err, ErrMalformed) {
				t.Fatalf("error = %v, want ErrMalformed", err)
			}
		})
	}
	if _, err := sampleV2().MarshalBinary(Version("v9")); !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("unsupported version error = %v", err)
	}
}

func TestUnmarshalHistoryRejectsMalformedPayloads(t *testing.T) {
	valid, err := sampleV2().MarshalBinary(Version2)
	if err != nil {
		t.Fatal(err)
	}
	v0 := hexBytes(t, historyV0Hex)
	v1 := hexBytes(t, historyV1Hex)

	tests := []struct {
		name    string
		version string
		data    []byte
		want    error
	}{
		{"empty", "v2", nil, ErrMalformed},
		{"unknown version", "v3", valid, ErrUnsupportedVersion},
		{"version mismatch", "v1", valid, ErrMalformed},
		{"v0 decoded as v2", "v2", v0, ErrMalformed},
		{"compact version marker", "v2", append([]byte{0x02}, valid[3:]...), ErrMalformed},
		{"v0 too many fields", "v0", append([]byte{0xcd, 0, 0, 0x9a}, v0[4:]...), ErrMalformed},
		{"v1 too many fields", "v1", append([]byte{0xcd, 0, 1, 0x9c}, v1[4:]...), ErrMalformed},
		{"v2 too few fields", "v2", append([]byte{0xcd, 0, 2, 0x90 | 11}, valid[4:]...), ErrMalformed},
		{"array is nil", "v2", []byte{0xcd, 0, 2, 0xc0}, ErrMalformed},
		{"timestamp compact", "v2", replaceFirst(t, valid, 0xcf, 0xce), ErrMalformed},
		{"timestamp truncated", "v2", valid[:len(valid)-1], ErrMalformed},
		{"trailing v0", "v0", append(append([]byte(nil), v0...), 0xc0), ErrMalformed},
		{"trailing v1", "v1", append(append([]byte(nil), v1...), 0xc0), ErrMalformed},
		{"invalid utf8", "v0", invalidUTF8(t, v0, "git status"), ErrMalformed},
		{"unknown record kind", "", []byte{0xcc, 0x02}, ErrMalformed},
		{"delete trailing", "", append(hexBytes(t, historyDeleteHex), 0xc0), ErrMalformed},
		{"delete not uuid", "", []byte{0xcc, 0x01, 0xa5, 'h', 'e', 'l', 'l', 'o'}, ErrMalformed},
		{"create missing bin", "", []byte{0xcc, 0x00, 0xc0}, ErrMalformed},
		{"create truncated", "", hexBytes(t, historyCreateHex)[:20], ErrMalformed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			version := test.version
			var err error
			if version == "" {
				_, err = UnmarshalHistoryRecord("v2", test.data)
			} else {
				_, err = UnmarshalHistory(version, test.data)
			}
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}

	// Every truncation of a valid payload must fail without panicking.
	for length := 0; length < len(valid); length++ {
		if _, err := UnmarshalHistory("v2", valid[:length]); err == nil {
			t.Fatalf("truncation at %d was accepted", length)
		}
	}
}

func TestUnmarshalHistoryAuthorKind(t *testing.T) {
	history := sampleV2()
	history.AuthorKind = authorKindPtr(AuthorKindAgent)
	encoded, err := history.MarshalBinary(Version2)
	if err != nil {
		t.Fatal(err)
	}

	// Unknown in-range values decode as "not stated".
	unknown := append([]byte(nil), encoded...)
	unknown[len(unknown)-1] = 0x03
	decoded, err := UnmarshalHistory("v2", unknown)
	if err != nil {
		t.Fatalf("unknown author kind: %v", err)
	}
	if decoded.AuthorKind != nil {
		t.Fatalf("unknown author kind = %d, want nil", *decoded.AuthorKind)
	}

	// Out-of-range values are malformed, like rmp's read_int::<u8>.
	negative := append([]byte(nil), encoded...)
	negative[len(negative)-1] = 0xff
	if _, err := UnmarshalHistory("v2", negative); !errors.Is(err, ErrMalformed) {
		t.Fatalf("negative author kind error = %v, want ErrMalformed", err)
	}
}

func replaceFirst(t *testing.T, data []byte, from, to byte) []byte {
	t.Helper()
	index := bytes.IndexByte(data, from)
	if index < 0 {
		t.Fatalf("marker 0x%02x not found", from)
	}
	out := append([]byte(nil), data...)
	out[index] = to
	return out
}

func invalidUTF8(t *testing.T, data []byte, value string) []byte {
	t.Helper()
	index := bytes.Index(data, []byte(value))
	if index < 0 {
		t.Fatalf("%q not found", value)
	}
	out := append([]byte(nil), data...)
	out[index] = 0xff
	return out
}
