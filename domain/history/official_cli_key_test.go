package history

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

type officialCLIKey struct {
	Source   string `json:"source"`
	KeyHex   string `json:"key_hex"`
	Base64   string `json:"base64"`
	Mnemonic string `json:"mnemonic"`
}

// TestParseOfficialCLIKey imports the two forms the official Atuin CLI exports
// for the same synthetic key: the base64 MessagePack form from
// `atuin key --base64` and the 24-word BIP39 mnemonic from `atuin key`.
func TestParseOfficialCLIKey(t *testing.T) {
	data, err := os.ReadFile("testdata/official_cli_key.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var fixture officialCLIKey
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	want, err := hex.DecodeString(fixture.KeyHex)
	if err != nil || len(want) != MasterKeyBytes {
		t.Fatalf("fixture key: %v (%d bytes)", err, len(want))
	}
	if len(fixture.Base64) == 0 || len(fixture.Mnemonic) == 0 {
		t.Fatal("fixture is missing an export form")
	}

	for name, input := range map[string]string{
		"base64":   fixture.Base64,
		"mnemonic": fixture.Mnemonic,
	} {
		t.Run(name, func(t *testing.T) {
			key, err := ParseMasterKey(input)
			if err != nil {
				t.Fatalf("ParseMasterKey: %v", err)
			}
			if !bytes.Equal(key, want) {
				t.Fatalf("key = %x, want %x", key, want)
			}
		})
	}
}
