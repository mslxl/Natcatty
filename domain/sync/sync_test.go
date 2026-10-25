package sync

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestEnvelopeMatchesTheOriginalElectronVector is the cross-implementation
// proof: the fixture was produced by the original WebCrypto writer, and this Go
// reader decrypts it and reproduces the same document byte for byte.
func TestEnvelopeMatchesTheOriginalElectronVector(t *testing.T) {
	vector := OriginalFormatVector()

	file, err := UnmarshalFile([]byte(vector.Envelope))
	if err != nil {
		t.Fatalf("parse the original envelope: %v", err)
	}
	plaintext, err := DecryptPayload(file, vector.Password)
	if err != nil {
		t.Fatalf("decrypt the original envelope: %v", err)
	}
	if string(plaintext) != vector.Plaintext {
		t.Fatalf("plaintext differs:\n got %s\nwant %s", plaintext, vector.Plaintext)
	}

	rebuilt, err := EncryptBytes([]byte(vector.Plaintext), vector.Password, EncryptOptions{
		Version:    vector.Meta.Version,
		UpdatedAt:  vector.Meta.UpdatedAt,
		DeviceID:   vector.Meta.DeviceID,
		DeviceName: vector.Meta.DeviceName,
		AppVersion: vector.Meta.AppVersion,
		Salt:       vector.Salt,
		IV:         vector.IV,
	})
	if err != nil {
		t.Fatalf("re-encrypt the vector: %v", err)
	}
	data, err := MarshalFile(rebuilt)
	if err != nil {
		t.Fatalf("marshal the rebuilt envelope: %v", err)
	}
	if string(data) != vector.Envelope {
		t.Fatalf("envelope differs from the original:\n got %s\nwant %s", data, vector.Envelope)
	}

	if rebuilt.Meta != vector.Meta {
		t.Fatalf("meta = %+v, want %+v", rebuilt.Meta, vector.Meta)
	}
}

func TestEnvelopeFieldOrderMatchesTheOriginal(t *testing.T) {
	vector := OriginalFormatVector()
	file, err := UnmarshalFile([]byte(vector.Envelope))
	if err != nil {
		t.Fatal(err)
	}
	data, err := MarshalFile(file)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		`"meta"`, `"version"`, `"updatedAt"`, `"deviceId"`, `"deviceName"`,
		`"appVersion"`, `"iv"`, `"salt"`, `"algorithm"`, `"kdf"`,
		`"kdfIterations"`, `"payload"`,
	}
	at := 0
	for _, key := range want {
		index := strings.Index(string(data[at:]), key)
		if index < 0 {
			t.Fatalf("field %s is missing or out of order in:\n%s", key, data)
		}
		at += index + len(key)
	}
}

func TestEncryptPayloadRoundTripsAndUsesFreshMaterial(t *testing.T) {
	payload := map[string]any{"hosts": []string{"a"}, "syncedAt": 5}
	first, err := EncryptPayload(payload, "secret", EncryptOptions{
		Version: 1, DeviceID: "d", DeviceName: "n", AppVersion: "v",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := EncryptPayload(payload, "secret", EncryptOptions{
		Version: 1, DeviceID: "d", DeviceName: "n", AppVersion: "v",
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Meta.Salt == second.Meta.Salt || first.Meta.IV == second.Meta.IV {
		t.Fatal("two encryptions reused salt or IV")
	}
	if first.Meta.KDFIterations != KDFIterations || first.Meta.Algorithm != AlgorithmAESGCM {
		t.Fatalf("meta = %+v", first.Meta)
	}
	plaintext, err := DecryptPayload(first, "secret")
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(plaintext, &decoded); err != nil {
		t.Fatalf("plaintext is not JSON: %v", err)
	}
	if decoded["syncedAt"] != float64(5) {
		t.Fatalf("decoded = %v", decoded)
	}
}

func TestDecryptRejectsWrongPasswordAndTampering(t *testing.T) {
	file, err := EncryptPayload(map[string]any{"n": 1}, "secret", EncryptOptions{Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecryptPayload(file, "wrong"); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("wrong password error = %v, want ErrWrongPassword", err)
	}

	sealed, err := base64.StdEncoding.DecodeString(file.Payload)
	if err != nil {
		t.Fatal(err)
	}
	sealed[len(sealed)-1] ^= 0xff
	file.Payload = base64.StdEncoding.EncodeToString(sealed)
	if _, err := DecryptPayload(file, "secret"); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("tampered error = %v, want ErrWrongPassword", err)
	}

	for name, corrupt := range map[string]File{
		"algorithm": {Meta: Meta{Algorithm: "AES-128", KDF: KDFPBKDF2}},
		"kdf":       {Meta: Meta{Algorithm: AlgorithmAESGCM, KDF: "Argon2id"}},
		"salt":      {Meta: Meta{Algorithm: AlgorithmAESGCM, KDF: KDFPBKDF2, Salt: "!!"}},
		"iv":        {Meta: Meta{Algorithm: AlgorithmAESGCM, KDF: KDFPBKDF2, Salt: file.Meta.Salt, IV: "!!"}},
		"payload":   {Meta: file.Meta, Payload: "!!"},
	} {
		if _, err := DecryptPayload(corrupt, "secret"); !errors.Is(err, ErrCorrupt) {
			t.Errorf("%s corruption = %v, want ErrCorrupt", name, err)
		}
	}
}

func TestMarshalJSONMatchesJSONStringify(t *testing.T) {
	lineSeparator := string(rune(0x2028))
	payload := map[string]string{"html": "<b>&</b>", "line": "a" + lineSeparator + "b"}
	data, err := MarshalJSON(payload)
	if err != nil {
		t.Fatal(err)
	}
	// JSON.stringify does not escape <, >, &, U+2028, or U+2029.
	backslash := "\\"
	if bytes.Contains(data, []byte(backslash+"u003c")) || bytes.Contains(data, []byte(backslash+"u0026")) {
		t.Fatalf("MarshalJSON escaped HTML rather than matching JSON.stringify: %s", data)
	}
	if !bytes.Contains(data, []byte("<b>&</b>")) {
		t.Fatalf("MarshalJSON changed raw HTML characters: %s", data)
	}
	if !bytes.Contains(data, []byte(lineSeparator)) {
		t.Fatalf("MarshalJSON escaped a literal U+2028: %s", data)
	}

	// A literal backslash-u sequence must stay a literal, not be rewritten.
	literal, err := MarshalJSON(map[string]string{"literal": backslash + "u2028"})
	if err != nil {
		t.Fatal(err)
	}
	if string(literal) != `{"literal":"\\u2028"}` {
		t.Fatalf("a literal backslash-u sequence was corrupted: %s", literal)
	}

	file, err := EncryptPayload(payload, "secret", EncryptOptions{Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := DecryptPayload(file, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if string(plaintext) != string(data) {
		t.Fatalf("round trip changed the JSON: got %s want %s", plaintext, data)
	}
}

func TestMasterKeyConfigMatchesTheOriginalVerifierVector(t *testing.T) {
	vector := OriginalFormatVector()
	config := MasterKeyConfig{
		VerificationHash: vector.VerificationHash,
		Salt:             base64.StdEncoding.EncodeToString(vector.Salt),
		KDF:              KDFPBKDF2,
		KDFIterations:    KDFIterations,
		CreatedAt:        1,
	}
	if !config.Verify(vector.Password) {
		t.Fatal("the original verification hash did not verify")
	}
	if config.Verify("wrong") {
		t.Fatal("a wrong password verified")
	}
	if !config.Valid() {
		t.Fatal("the original-shaped verifier was rejected")
	}

	created, err := NewMasterKeyConfig("secret", bytes.NewReader(make([]byte, SaltBytes)), time.UnixMilli(10))
	if err != nil {
		t.Fatal(err)
	}
	if !created.Verify("secret") || created.Verify("other") {
		t.Fatal("a fresh verifier does not verify only its password")
	}
	if created.KDF != KDFPBKDF2 || created.KDFIterations != KDFIterations {
		t.Fatalf("created = %+v", created)
	}
}

func TestEmptyPasswordIsRefused(t *testing.T) {
	if _, err := EncryptPayload(map[string]any{}, "", EncryptOptions{}); !errors.Is(err, ErrPasswordRequired) {
		t.Fatalf("encrypt error = %v", err)
	}
	if _, err := DecryptPayload(File{}, ""); !errors.Is(err, ErrPasswordRequired) {
		t.Fatalf("decrypt error = %v", err)
	}
	if _, err := NewMasterKeyConfig("", nil, time.Now()); !errors.Is(err, ErrPasswordRequired) {
		t.Fatalf("master key error = %v", err)
	}
}
