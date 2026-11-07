package wire

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

type frozenRecord struct {
	Source       string `json:"source"`
	KeyHex       string `json:"key_hex"`
	ID           string `json:"id"`
	Index        uint64 `json:"idx"`
	Host         string `json:"host"`
	Timestamp    uint64 `json:"timestamp"`
	Version      string `json:"version"`
	Tag          string `json:"tag"`
	Raw          string `json:"raw"`
	CEK          string `json:"cek"`
	PlaintextB64 string `json:"plaintext_b64"`
}

func readFrozenRecord(t *testing.T) (frozenRecord, Key) {
	t.Helper()
	data, err := os.ReadFile("testdata/frozen_record_v1.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var fixture frozenRecord
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	return fixture, keyFromHex(t, fixture.KeyHex)
}

func (fixture frozenRecord) record() Record {
	return Record{
		ID:         fixture.ID,
		Index:      fixture.Index,
		SeriesHost: fixture.Host,
		Timestamp:  fixture.Timestamp,
		Version:    fixture.Version,
		Tag:        fixture.Tag,
		Data:       EncryptedData{Raw: fixture.Raw, CEK: fixture.CEK},
	}
}

func TestRecordDecryptsPinnedFrozenBlob(t *testing.T) {
	fixture, key := readFrozenRecord(t)
	record := fixture.record()

	assertion, err := record.Assertion()
	if err != nil {
		t.Fatalf("Assertion: %v", err)
	}
	wantAssertion := `{"id":"00000000-0000-0000-0000-000000000001","idx":7,"version":"v1","tag":"history","host":"00000000-0000-0000-0000-000000000002"}`
	if string(assertion) != wantAssertion {
		t.Fatalf("assertion = %s, want %s", assertion, wantAssertion)
	}

	plaintext, err := record.Decrypt(key)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	want, err := base64.StdEncoding.DecodeString(fixture.PlaintextB64)
	if err != nil {
		t.Fatal(err)
	}
	if string(plaintext) != string(want) {
		t.Fatalf("plaintext = %x, want %x", plaintext, want)
	}
}

func TestRecordAssertionIsFrozen(t *testing.T) {
	record := Record{
		ID:         "00000000-0000-0000-0000-000000000001",
		Index:      7,
		SeriesHost: "00000000-0000-0000-0000-000000000002",
		Timestamp:  1687244806000000000,
		Version:    "v1",
		Tag:        "history",
	}
	assertion, err := record.Assertion()
	if err != nil {
		t.Fatal(err)
	}
	want := `{"id":"00000000-0000-0000-0000-000000000001","idx":7,"version":"v1","tag":"history","host":"00000000-0000-0000-0000-000000000002"}`
	if string(assertion) != want {
		t.Fatalf("assertion = %s, want %s", assertion, want)
	}

	record.Tag = "a\"b\\c\nd\x01"
	assertion, err = record.Assertion()
	if err != nil {
		t.Fatal(err)
	}
	want = `{"id":"00000000-0000-0000-0000-000000000001","idx":7,"version":"v1","tag":"a\"b\\c\nd\u0001","host":"00000000-0000-0000-0000-000000000002"}`
	if string(assertion) != want {
		t.Fatalf("escaped assertion = %s, want %s", assertion, want)
	}

	record.ID = "not-a-uuid"
	if _, err := record.Assertion(); !errors.Is(err, ErrMalformed) {
		t.Fatalf("invalid id error = %v, want ErrMalformed", err)
	}
}

func TestRecordMarshalJSONIsCanonical(t *testing.T) {
	record := Record{
		ID:         "018cd4fe-8175-7cd2-aee6-5cd7861f9c81",
		Index:      18446744073709551615,
		SeriesHost: "018cd4fe-ad89-7597-8525-27a31c998059",
		Timestamp:  1704326400000000000,
		Version:    "v2",
		Tag:        "history",
		Data: EncryptedData{
			Raw: "raw<>&value",
			CEK: `{"wpk":"wpk-value","kid":"kid-value"}`,
		},
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	if strings.HasSuffix(string(encoded), "\n") {
		t.Fatal("encoded record has a trailing newline")
	}
	// encoding/json compact-escapes the bytes returned by a Marshaler, so the
	// <, >, and & in the payload arrive as \u00xx. The wire meaning is
	// unchanged: serde_json decodes both to the same strings, and only the
	// implicit assertion (built separately) needs byte equality.
	want := `{"id":"018cd4fe-8175-7cd2-aee6-5cd7861f9c81","idx":18446744073709551615,` +
		`"host":{"id":"018cd4fe-ad89-7597-8525-27a31c998059","name":""},` +
		`"timestamp":1704326400000000000,"version":"v2","tag":"history",` +
		`"data":{"data":` + quoteJSON(record.Data.Raw) +
		`,"content_encryption_key":` + quoteJSON(record.Data.CEK) + `}}`
	if string(encoded) != want {
		t.Fatalf("encoded = %s, want %s", encoded, want)
	}

	// Simple UUIDs in and hyphenated UUIDs out, like serde's Uuid display.
	record.ID = "018cd4fe81757cd2aee65cd7861f9c81"
	record.SeriesHost = "018CD4FEAD897597852527A31C998059"
	encoded, err = json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"host":{"id":"018cd4fe-ad89-7597-8525-27a31c998059","name":""}`) {
		t.Fatalf("host was not canonicalized: %s", encoded)
	}

	record.SeriesHost = "not-a-uuid"
	if _, err := json.Marshal(record); !errors.Is(err, ErrMalformed) {
		t.Fatalf("invalid host error = %v, want ErrMalformed", err)
	}
}

func TestRecordJSONRoundTrip(t *testing.T) {
	fixture, _ := readFrozenRecord(t)
	record := fixture.record()
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Record
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}
	if decoded.ID != record.ID || decoded.Index != record.Index ||
		decoded.SeriesHost != record.SeriesHost || decoded.Timestamp != record.Timestamp ||
		decoded.Version != record.Version || decoded.Tag != record.Tag ||
		decoded.Data.Raw != record.Data.Raw || decoded.Data.CEK != record.Data.CEK {
		t.Fatalf("decoded = %#v, want %#v", decoded, record)
	}
}

func TestRecordUnmarshalAcceptsUnknownFieldsAndAliases(t *testing.T) {
	base := `{"id":"00000000-0000-0000-0000-000000000001","idx":2,` +
		`"host":{"id":"00000000-0000-0000-0000-000000000002","name":"ignored"},` +
		`"timestamp":3,"version":"v2","tag":"history",` +
		`"data":{"data":"raw","content_encryption_key":"cek"}}`

	var record Record
	withUnknown := strings.Replace(base, `{"id":`, `{"extra":{"nested":true},"id":`, 1)
	withUnknown = strings.Replace(withUnknown, `"data":{"data":`, `"data":{"future":1,"data":`, 1)
	if err := json.Unmarshal([]byte(withUnknown), &record); err != nil {
		t.Fatalf("unknown fields: %v", err)
	}
	if record.ID != "00000000-0000-0000-0000-000000000001" || record.Data.Raw != "raw" ||
		record.Data.CEK != "cek" || record.SeriesHost != "00000000-0000-0000-0000-000000000002" {
		t.Fatalf("record = %#v", record)
	}

	aliased := strings.Replace(base, `"data":"raw","content_encryption_key":"cek"`,
		`"raw":"raw-alias","cek":"cek-alias"`, 1)
	record = Record{}
	if err := json.Unmarshal([]byte(aliased), &record); err != nil {
		t.Fatalf("aliases: %v", err)
	}
	if record.Data.Raw != "raw-alias" || record.Data.CEK != "cek-alias" {
		t.Fatalf("aliased data = %#v", record.Data)
	}

	// A missing legacy host name is fine; serde skips deserializing it too.
	withoutName := strings.Replace(base, `,"name":"ignored"`, ``, 1)
	record = Record{}
	if err := json.Unmarshal([]byte(withoutName), &record); err != nil {
		t.Fatalf("missing host name: %v", err)
	}
}

func TestRecordUnmarshalRejectsMalformedJSON(t *testing.T) {
	base := `{"id":"00000000-0000-0000-0000-000000000001","idx":2,` +
		`"host":{"id":"00000000-0000-0000-0000-000000000002"},` +
		`"timestamp":3,"version":"v2","tag":"history",` +
		`"data":{"data":"raw","content_encryption_key":"cek"}}`
	tests := []struct {
		name  string
		input string
	}{
		{"empty", ``},
		{"null", `null`},
		{"trailing json", base + `{}`},
		{"missing id", `{"idx":2,"host":{"id":"00000000-0000-0000-0000-000000000002"},"timestamp":3,"version":"v2","tag":"history","data":{"data":"raw","content_encryption_key":"cek"}}`},
		{"missing idx", `{"id":"00000000-0000-0000-0000-000000000001","host":{"id":"00000000-0000-0000-0000-000000000002"},"timestamp":3,"version":"v2","tag":"history","data":{"data":"raw","content_encryption_key":"cek"}}`},
		{"missing host id", `{"id":"00000000-0000-0000-0000-000000000001","idx":2,"host":{},"timestamp":3,"version":"v2","tag":"history","data":{"data":"raw","content_encryption_key":"cek"}}`},
		{"missing timestamp", `{"id":"00000000-0000-0000-0000-000000000001","idx":2,"host":{"id":"00000000-0000-0000-0000-000000000002"},"version":"v2","tag":"history","data":{"data":"raw","content_encryption_key":"cek"}}`},
		{"missing version", `{"id":"00000000-0000-0000-0000-000000000001","idx":2,"host":{"id":"00000000-0000-0000-0000-000000000002"},"timestamp":3,"tag":"history","data":{"data":"raw","content_encryption_key":"cek"}}`},
		{"missing tag", `{"id":"00000000-0000-0000-0000-000000000001","idx":2,"host":{"id":"00000000-0000-0000-0000-000000000002"},"timestamp":3,"version":"v2","data":{"data":"raw","content_encryption_key":"cek"}}`},
		{"missing data", `{"id":"00000000-0000-0000-0000-000000000001","idx":2,"host":{"id":"00000000-0000-0000-0000-000000000002"},"timestamp":3,"version":"v2","tag":"history"}`},
		{"missing data.raw", `{"id":"00000000-0000-0000-0000-000000000001","idx":2,"host":{"id":"00000000-0000-0000-0000-000000000002"},"timestamp":3,"version":"v2","tag":"history","data":{"content_encryption_key":"cek"}}`},
		{"null data.raw", `{"id":"00000000-0000-0000-0000-000000000001","idx":2,"host":{"id":"00000000-0000-0000-0000-000000000002"},"timestamp":3,"version":"v2","tag":"history","data":{"data":null,"content_encryption_key":"cek"}}`},
		{"both aliases", `{"id":"00000000-0000-0000-0000-000000000001","idx":2,"host":{"id":"00000000-0000-0000-0000-000000000002"},"timestamp":3,"version":"v2","tag":"history","data":{"data":"a","raw":"b","content_encryption_key":"cek"}}`},
		{"negative idx", strings.Replace(base, `"idx":2`, `"idx":-1`, 1)},
		{"idx wrong type", strings.Replace(base, `"idx":2`, `"idx":"2"`, 1)},
		{"timestamp wrong type", strings.Replace(base, `"timestamp":3`, `"timestamp":"3"`, 1)},
		{"id not uuid", strings.Replace(base, `00000000-0000-0000-0000-000000000001`, `nope`, 1)},
		{"host not uuid", strings.Replace(base, `00000000-0000-0000-0000-000000000002`, `nope`, 1)},
		{"data not object", strings.Replace(base, `"data":{"data":"raw","content_encryption_key":"cek"}`, `"data":"raw"`, 1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var record Record
			if err := json.Unmarshal([]byte(test.input), &record); err == nil {
				t.Fatalf("malformed record was accepted: %#v", record)
			}
		})
	}
}

func TestRecordEncryptDecryptRoundTrip(t *testing.T) {
	key := bytesOf(0x55)
	record := Record{
		ID:         "018cd4fe-8175-7cd2-aee6-5cd7861f9c81",
		Index:      9,
		SeriesHost: "018cd4fe-ad89-7597-8525-27a31c998059",
		Timestamp:  1704326400000000000,
		Version:    "v2",
		Tag:        "history",
	}
	plaintext := []byte("héllo 世界\nsecond line\x00end")

	encrypted, err := record.Encrypt(plaintext, key)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if encrypted.Data.Raw == "" || encrypted.Data.CEK == "" {
		t.Fatalf("encrypted data = %#v", encrypted.Data)
	}
	decrypted, err := encrypted.Decrypt(key)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if string(decrypted) != string(plaintext) {
		t.Fatalf("decrypted = %q, want %q", decrypted, plaintext)
	}

	if _, err := encrypted.Decrypt(bytesOf(0x56)); !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("wrong key error = %v, want ErrKeyMismatch", err)
	}
}

func TestRecordDecryptRejectsTamperedMetadata(t *testing.T) {
	key := bytesOf(0x55)
	base := Record{
		ID:         "018cd4fe-8175-7cd2-aee6-5cd7861f9c81",
		Index:      9,
		SeriesHost: "018cd4fe-ad89-7597-8525-27a31c998059",
		Timestamp:  1704326400000000000,
		Version:    "v2",
		Tag:        "history",
	}
	encrypted, err := base.Encrypt([]byte("payload"), key)
	if err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(*Record){
		"id":      func(record *Record) { record.ID = "018cd4fe-8175-7cd2-aee6-5cd7861f9c82" },
		"idx":     func(record *Record) { record.Index++ },
		"host":    func(record *Record) { record.SeriesHost = "018cd4fe-ad89-7597-8525-27a31c99805a" },
		"version": func(record *Record) { record.Version = "v1" },
		"tag":     func(record *Record) { record.Tag = "other" },
		"raw": func(record *Record) {
			payload, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(record.Data.Raw, pasetoHeader))
			if err != nil {
				t.Fatalf("decode token: %v", err)
			}
			payload[len(payload)-1] ^= 1
			record.Data.Raw = pasetoHeader + base64.RawURLEncoding.EncodeToString(payload)
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			tampered := encrypted
			mutate(&tampered)
			if _, err := tampered.Decrypt(key); !errors.Is(err, ErrKeyMismatch) {
				t.Fatalf("error = %v, want ErrKeyMismatch", err)
			}
		})
	}
}

func TestRecordEncryptRejectsInvalidMetadata(t *testing.T) {
	record := Record{
		ID:         "not-a-uuid",
		Index:      0,
		SeriesHost: "018cd4fe-ad89-7597-8525-27a31c998059",
		Version:    "v2",
		Tag:        "history",
	}
	if _, err := record.Encrypt([]byte("payload"), bytesOf(0x55)); !errors.Is(err, ErrMalformed) {
		t.Fatalf("error = %v, want ErrMalformed", err)
	}
}
