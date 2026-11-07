package wire

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

// The PASETO v4.local vectors are the official Paseto test vectors, extracted
// from rusty_paseto 0.8.0 (the crate the pinned Atuin client uses).
type pasetoVector struct {
	Name      string `json:"name"`
	Key       string `json:"key"`
	Nonce     string `json:"nonce"`
	Payload   string `json:"payload"`
	Token     string `json:"token"`
	Footer    string `json:"footer"`
	Assertion string `json:"assertion"`
}

type pieVector struct {
	Name        string `json:"name"`
	Unwrapped   string `json:"unwrapped"`
	WrappingKey string `json:"wrapping-key"`
	Paserk      string `json:"paserk"`
}

func readVectors[T any](t *testing.T, name string, target *T) {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
}

// hasFooter reports whether the token carries a PASETO footer. The pinned
// Atuin client never uses one, so vectors with a footer only exercise
// rejection.
func hasFooter(vector pasetoVector) bool {
	return vector.Footer != "" ||
		strings.Count(strings.TrimPrefix(vector.Token, pasetoHeader), ".") > 0
}

func keyFromHex(t *testing.T, value string) Key {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatalf("hex: %v", err)
	}
	key, err := KeyFromBytes(decoded)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	return key
}

func TestPASETODecryptsOfficialVectors(t *testing.T) {
	var vectors struct {
		Tests []pasetoVector `json:"tests"`
	}
	readVectors(t, "paseto_v4_local.json", &vectors)
	tested := 0
	for _, vector := range vectors.Tests {
		if hasFooter(vector) {
			// Atuin never uses a footer; the pinned client rejects one.
			continue
		}
		t.Run(vector.Name, func(t *testing.T) {
			key := keyFromHex(t, vector.Key)
			plaintext, err := DecryptPASETO(vector.Token, []byte(vector.Assertion), key)
			if err != nil {
				t.Fatalf("DecryptPASETO: %v", err)
			}
			if string(plaintext) != vector.Payload {
				t.Fatalf("payload = %q, want %q", plaintext, vector.Payload)
			}
		})
		tested++
	}
	if tested == 0 {
		t.Fatal("no PASETO vectors were tested")
	}
}

func TestPASETOEncryptsOfficialVectors(t *testing.T) {
	var vectors struct {
		Tests []pasetoVector `json:"tests"`
	}
	readVectors(t, "paseto_v4_local.json", &vectors)
	tested := 0
	for _, vector := range vectors.Tests {
		if hasFooter(vector) {
			continue
		}
		t.Run(vector.Name, func(t *testing.T) {
			key := keyFromHex(t, vector.Key)
			nonce := keyFromHex(t, vector.Nonce)
			token, err := encryptPASETOWithNonce([]byte(vector.Payload), []byte(vector.Assertion), key, nonce)
			if err != nil {
				t.Fatalf("encryptPASETOWithNonce: %v", err)
			}
			if token != vector.Token {
				t.Fatalf("token = %q, want %q", token, vector.Token)
			}
		})
		tested++
	}
	if tested == 0 {
		t.Fatal("no PASETO vectors were tested")
	}
}

func TestPASETORejectsATamperedAssertion(t *testing.T) {
	var vectors struct {
		Tests []pasetoVector `json:"tests"`
	}
	readVectors(t, "paseto_v4_local.json", &vectors)
	vector := vectors.Tests[0]
	key := keyFromHex(t, vector.Key)
	if _, err := DecryptPASETO(vector.Token, []byte(vector.Assertion+"x"), key); !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("tampered assertion error = %v, want ErrKeyMismatch", err)
	}
}

func TestPASETORejectsATamperedToken(t *testing.T) {
	var vectors struct {
		Tests []pasetoVector `json:"tests"`
	}
	readVectors(t, "paseto_v4_local.json", &vectors)
	vector := vectors.Tests[0]
	key := keyFromHex(t, vector.Key)
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(vector.Token, pasetoHeader))
	if err != nil {
		t.Fatal(err)
	}
	payload[len(payload)-1] ^= 1
	tampered := pasetoHeader + base64.RawURLEncoding.EncodeToString(payload)
	if _, err := DecryptPASETO(tampered, nil, key); !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("tampered token error = %v, want ErrKeyMismatch", err)
	}
}

func TestPASETORejectsAFooter(t *testing.T) {
	var vectors struct {
		Tests []pasetoVector `json:"tests"`
	}
	readVectors(t, "paseto_v4_local.json", &vectors)
	for _, vector := range vectors.Tests {
		if !hasFooter(vector) {
			continue
		}
		key := keyFromHex(t, vector.Key)
		if _, err := DecryptPASETO(vector.Token, []byte(vector.Assertion), key); !errors.Is(err, ErrMalformed) {
			t.Fatalf("%s: footer error = %v, want ErrMalformed", vector.Name, err)
		}
	}
}

func TestPIEUnwrapsOfficialVector(t *testing.T) {
	var vectors struct {
		Tests []pieVector `json:"tests"`
	}
	readVectors(t, "paserk_k4_local_wrap_pie.json", &vectors)
	if len(vectors.Tests) == 0 {
		t.Fatal("no PIE vectors")
	}
	vector := vectors.Tests[0]
	wrapping := keyFromHex(t, vector.WrappingKey)
	unwrapped, err := UnwrapPIE(vector.Paserk, wrapping)
	if err != nil {
		t.Fatalf("UnwrapPIE: %v", err)
	}
	if hex.EncodeToString(unwrapped[:]) != vector.Unwrapped {
		t.Fatalf("unwrapped = %x, want %s", unwrapped, vector.Unwrapped)
	}

	// Reproduce the exact vector by reusing its nonce, proving the writer
	// emits the same bytes as the official implementation.
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(vector.Paserk, paserkWrapPrefix))
	if err != nil || len(payload) != 96 {
		t.Fatalf("decode PIE payload: %v (%d bytes)", err, len(payload))
	}
	nonce, err := KeyFromBytes(payload[32:64])
	if err != nil {
		t.Fatal(err)
	}
	rewrapped, err := wrapPIEWithNonce(unwrapped, wrapping, nonce)
	if err != nil {
		t.Fatalf("wrapPIEWithNonce: %v", err)
	}
	if rewrapped != vector.Paserk {
		t.Fatalf("rewrapped = %q, want %q", rewrapped, vector.Paserk)
	}
}

func TestPIERejectsAWrongWrappingKey(t *testing.T) {
	var vectors struct {
		Tests []pieVector `json:"tests"`
	}
	readVectors(t, "paserk_k4_local_wrap_pie.json", &vectors)
	vector := vectors.Tests[0]
	wrong := bytesOf(0x11)
	if _, err := UnwrapPIE(vector.Paserk, wrong); !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("wrong wrapping key error = %v, want ErrKeyMismatch", err)
	}
}

func bytesOf(value byte) Key {
	var key Key
	for index := range key {
		key[index] = value
	}
	return key
}

func TestPIEAndKeyIDRoundTrip(t *testing.T) {
	master := bytesOf(0x55)
	cek := bytesOf(0x22)
	wrapped, err := WrapPIE(cek, master)
	if err != nil {
		t.Fatalf("WrapPIE: %v", err)
	}
	if !strings.HasPrefix(wrapped, paserkWrapPrefix) {
		t.Fatalf("wrapped = %q", wrapped)
	}
	unwrapped, err := UnwrapPIE(wrapped, master)
	if err != nil {
		t.Fatalf("UnwrapPIE: %v", err)
	}
	if unwrapped != cek {
		t.Fatalf("unwrapped = %x, want %x", unwrapped, cek)
	}
	keyID := KeyID(master)
	if !strings.HasPrefix(keyID, paserkKeyIDPrefix) {
		t.Fatalf("key id = %q", keyID)
	}
	if KeyID(bytesOf(0x56)) == keyID {
		t.Fatal("different keys share a key id")
	}
}

func TestEncryptDataRoundTripAndWrongKey(t *testing.T) {
	master := bytesOf(0x55)
	other := bytesOf(0x56)
	assertion := []byte(`{"id":"00000000-0000-0000-0000-000000000001"}`)
	plaintext := []byte("hello, 世界\nsecond line")

	encrypted, err := EncryptData(plaintext, assertion, master)
	if err != nil {
		t.Fatalf("EncryptData: %v", err)
	}
	if !strings.HasPrefix(encrypted.Raw, pasetoHeader) {
		t.Fatalf("raw = %q", encrypted.Raw)
	}
	var envelope struct {
		WPK string `json:"wpk"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal([]byte(encrypted.CEK), &envelope); err != nil {
		t.Fatalf("cek envelope: %v", err)
	}
	if envelope.Kid != KeyID(master) {
		t.Fatalf("kid = %q, want %q", envelope.Kid, KeyID(master))
	}
	if !strings.HasPrefix(envelope.WPK, paserkWrapPrefix) {
		t.Fatalf("wpk = %q", envelope.WPK)
	}

	decrypted, err := DecryptData(encrypted, assertion, master)
	if err != nil {
		t.Fatalf("DecryptData: %v", err)
	}
	if string(decrypted) != string(plaintext) {
		t.Fatalf("decrypted = %q, want %q", decrypted, plaintext)
	}

	if _, err := DecryptData(encrypted, assertion, other); !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("wrong key error = %v, want ErrKeyMismatch", err)
	}
	if _, err := DecryptData(encrypted, []byte("other assertion"), master); !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("wrong assertion error = %v, want ErrKeyMismatch", err)
	}
}

func TestEncryptDataRoundTripsAnEmptyPayload(t *testing.T) {
	master := bytesOf(0x55)
	encrypted, err := EncryptData(nil, nil, master)
	if err != nil {
		t.Fatalf("EncryptData: %v", err)
	}
	decrypted, err := DecryptData(encrypted, nil, master)
	if err != nil {
		t.Fatalf("DecryptData: %v", err)
	}
	if len(decrypted) != 0 {
		t.Fatalf("decrypted = %x, want empty", decrypted)
	}
}

func TestDecryptDataRejectsAMalformedInnerPayload(t *testing.T) {
	master := bytesOf(0x55)
	cek := bytesOf(0x77)
	wrap := func(inner string) EncryptedData {
		t.Helper()
		token, err := EncryptPASETO([]byte(inner), nil, cek)
		if err != nil {
			t.Fatal(err)
		}
		wrapped, err := WrapPIE(cek, master)
		if err != nil {
			t.Fatal(err)
		}
		return EncryptedData{
			Raw: token,
			CEK: `{"wpk":` + quoteJSON(wrapped) + `,"kid":` + quoteJSON(KeyID(master)) + `}`,
		}
	}
	for name, inner := range map[string]string{
		"missing data":  `{}`,
		"null data":     `{"data":null}`,
		"invalid b64":   `{"data":"!!!"}`,
		"not json":      `not json`,
		"data not text": `{"data":1}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecryptData(wrap(inner), nil, master); !errors.Is(err, ErrMalformed) {
				t.Fatalf("error = %v, want ErrMalformed", err)
			}
		})
	}
}

func TestDecryptDataRejectsMalformedEnvelopes(t *testing.T) {
	master := bytesOf(0x55)
	good, err := EncryptData([]byte("payload"), nil, master)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		data EncryptedData
	}{
		{"empty cek", EncryptedData{Raw: good.Raw}},
		{"invalid cek json", EncryptedData{Raw: good.Raw, CEK: "not json"}},
		{"missing wpk", EncryptedData{Raw: good.Raw, CEK: `{"kid":"` + KeyID(master) + `"}`}},
		{"missing kid", EncryptedData{Raw: good.Raw, CEK: `{"wpk":"` + paserkWrapPrefix + `x"}`}},
		{"invalid kid prefix", EncryptedData{Raw: good.Raw, CEK: `{"wpk":"x","kid":"x"}`}},
		{"not a token", EncryptedData{Raw: "not-a-token", CEK: good.CEK}},
		{"truncated token", EncryptedData{Raw: pasetoHeader + "AAAA", CEK: good.CEK}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := DecryptData(test.data, nil, master); err == nil {
				t.Fatal("malformed envelope was accepted")
			}
		})
	}
}

func TestEncryptedDataUnmarshalWrapsMalformedErrors(t *testing.T) {
	for _, input := range []string{
		`{"data":"x"}`,
		`{"data":null,"content_encryption_key":"c"}`,
		`{"data":1,"content_encryption_key":"c"}`,
		`{"data":"d","content_encryption_key":"c","raw":"r"}`,
	} {
		var data EncryptedData
		if err := json.Unmarshal([]byte(input), &data); !errors.Is(err, ErrMalformed) {
			t.Fatalf("%s: error = %v, want ErrMalformed", input, err)
		}
	}
	// encoding/json rejects a syntactically invalid document before it calls
	// UnmarshalJSON, so there is no error for this package to classify.
	var data EncryptedData
	if err := json.Unmarshal([]byte(`not json`), &data); err == nil {
		t.Fatal("invalid JSON was accepted")
	}
}

func TestDecryptDataAcceptsAHistoricalAliasEnvelope(t *testing.T) {
	master := bytesOf(0x55)
	encrypted, err := EncryptData([]byte("payload"), nil, master)
	if err != nil {
		t.Fatal(err)
	}
	aliased := EncryptedData{}
	input := `{"raw":` + quoteJSON(encrypted.Raw) + `,"cek":` + quoteJSON(encrypted.CEK) + `}`
	if err := json.Unmarshal([]byte(input), &aliased); err != nil {
		t.Fatalf("alias unmarshal: %v", err)
	}
	decrypted, err := DecryptData(aliased, nil, master)
	if err != nil {
		t.Fatalf("DecryptData: %v", err)
	}
	if string(decrypted) != "payload" {
		t.Fatalf("decrypted = %q", decrypted)
	}
}

func TestDecryptDataRejectsATamperedCEK(t *testing.T) {
	master := bytesOf(0x55)
	encrypted, err := EncryptData([]byte("payload"), nil, master)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		WPK string `json:"wpk"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal([]byte(encrypted.CEK), &envelope); err != nil {
		t.Fatal(err)
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(envelope.WPK, paserkWrapPrefix))
	if err != nil {
		t.Fatal(err)
	}
	payload[len(payload)-1] ^= 1
	tampered := EncryptedData{
		Raw: encrypted.Raw,
		CEK: `{"wpk":` + quoteJSON(paserkWrapPrefix+base64.RawURLEncoding.EncodeToString(payload)) +
			`,"kid":` + quoteJSON(envelope.Kid) + `}`,
	}
	if _, err := DecryptData(tampered, nil, master); !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("tampered CEK error = %v, want ErrKeyMismatch", err)
	}
}
