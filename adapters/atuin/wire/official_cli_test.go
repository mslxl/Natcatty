package wire

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// These fixtures were produced by the official Atuin CLI (18.24.2) from a
// private ATUIN_HOME with a synthetic master key. The pinned reference
// (v18.24.5, commit ad5bf5c) speaks the same wire format; see
// testdata/official_cli_history.json for provenance.

type officialCLIFixture struct {
	Source  string                  `json:"source"`
	KeyHex  string                  `json:"key_hex"`
	HostID  string                  `json:"host_id"`
	Records []officialCLIHistoryRec `json:"records"`
}

type officialCLIHistoryRec struct {
	Kind   string `json:"kind"`
	Record struct {
		ID        string        `json:"id"`
		Index     uint64        `json:"idx"`
		Host      string        `json:"host"`
		Timestamp uint64        `json:"timestamp"`
		Version   string        `json:"version"`
		Tag       string        `json:"tag"`
		Data      EncryptedData `json:"data"`
	} `json:"record"`
	ExpectedHistory *officialCLIHistory `json:"expected_history"`
	ExpectedDelete  string              `json:"expected_delete_id"`
}

type officialCLIHistory struct {
	ID         string      `json:"id"`
	Timestamp  uint64      `json:"timestamp"`
	Duration   int64       `json:"duration"`
	Exit       int64       `json:"exit"`
	Command    string      `json:"command"`
	CWD        string      `json:"cwd"`
	Session    string      `json:"session"`
	Origin     string      `json:"origin"`
	DeletedAt  *uint64     `json:"deleted_at"`
	Author     *string     `json:"author"`
	Intent     *string     `json:"intent"`
	Shell      *string     `json:"shell"`
	AuthorKind *AuthorKind `json:"author_kind"`
}

func readOfficialCLIFixture(t *testing.T) (officialCLIFixture, Key) {
	t.Helper()
	data, err := os.ReadFile("testdata/official_cli_history.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var fixture officialCLIFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	if len(fixture.Records) == 0 {
		t.Fatal("fixture has no records")
	}
	return fixture, keyFromHex(t, fixture.KeyHex)
}

func TestOfficialCLIRecordsDecryptAndDecode(t *testing.T) {
	fixture, key := readOfficialCLIFixture(t)
	creates, deletes := 0, 0
	for _, item := range fixture.Records {
		record := Record{
			ID:         item.Record.ID,
			Index:      item.Record.Index,
			SeriesHost: item.Record.Host,
			Timestamp:  item.Record.Timestamp,
			Version:    item.Record.Version,
			Tag:        item.Record.Tag,
			Data:       item.Record.Data,
		}
		plaintext, err := record.Decrypt(key)
		if err != nil {
			t.Fatalf("record %d: Decrypt: %v", item.Record.Index, err)
		}
		decoded, err := UnmarshalHistoryRecord(record.Version, plaintext)
		if err != nil {
			t.Fatalf("record %d: UnmarshalHistoryRecord: %v", item.Record.Index, err)
		}

		switch item.Kind {
		case "create":
			creates++
			if decoded.Kind != HistoryRecordCreate || decoded.History == nil {
				t.Fatalf("record %d: kind = %v, want create", item.Record.Index, decoded.Kind)
			}
			if item.ExpectedHistory == nil {
				t.Fatalf("record %d: fixture has no expected history", item.Record.Index)
			}
			expected := item.ExpectedHistory
			got := *decoded.History
			want := History{
				ID:         expected.ID,
				Timestamp:  expected.Timestamp,
				Duration:   expected.Duration,
				Exit:       expected.Exit,
				Command:    expected.Command,
				CWD:        expected.CWD,
				Session:    expected.Session,
				Origin:     expected.Origin,
				DeletedAt:  expected.DeletedAt,
				Author:     expected.Author,
				Intent:     expected.Intent,
				Shell:      expected.Shell,
				AuthorKind: expected.AuthorKind,
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("record %d: history = %#v, want %#v", item.Record.Index, got, want)
			}
		case "delete":
			deletes++
			if decoded.Kind != HistoryRecordDelete {
				t.Fatalf("record %d: kind = %v, want delete", item.Record.Index, decoded.Kind)
			}
			if decoded.DeleteID != item.ExpectedDelete {
				t.Fatalf("record %d: delete id = %q, want %q",
					item.Record.Index, decoded.DeleteID, item.ExpectedDelete)
			}
		default:
			t.Fatalf("record %d: unknown fixture kind %q", item.Record.Index, item.Kind)
		}

		// The module's encoder must reproduce the official plaintext
		// byte-for-byte.
		reencoded, err := decoded.MarshalBinary(Version2)
		if err != nil {
			t.Fatalf("record %d: MarshalBinary: %v", item.Record.Index, err)
		}
		if !bytes.Equal(reencoded, plaintext) {
			t.Fatalf("record %d: re-encoded bytes differ\n got %x\nwant %x",
				item.Record.Index, reencoded, plaintext)
		}
	}
	if creates == 0 || deletes == 0 {
		t.Fatalf("fixture exercised %d creates and %d deletes", creates, deletes)
	}
}

func TestOfficialCLIRecordsShareTheFixtureHost(t *testing.T) {
	fixture, _ := readOfficialCLIFixture(t)
	for _, item := range fixture.Records {
		if item.Record.Host != fixture.HostID {
			t.Fatalf("record %d host = %q, want %q",
				item.Record.Index, item.Record.Host, fixture.HostID)
		}
	}
}
