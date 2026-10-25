package sync

// This file is the executable interoperability contract for the encrypted
// netcatty-vault.json envelope. The fixture below was produced with the
// original Electron writer's primitives and constants (WebCrypto
// PBKDF2-SHA-256, 600000 iterations, AES-256-GCM, 12-byte IV, 128-bit tag,
// base64 payload) with fixed salt, IV, and metadata, using a compact
// JSON.stringify plaintext whose implemented families follow the original
// SyncPayload order, and JSON.stringify(file, null, 2) as the uploaded
// document.
//
// The unit test decrypts it with this Go implementation and re-encrypts the
// same plaintext with the same material, requiring a byte-identical envelope.
// The ticket-49 self-test performs the same check in the running app, so an
// accidental change to the KDF, field encoding, or ordering fails acceptance.

// FormatVector is the fixed fixture.
type FormatVector struct {
	Password         string
	Plaintext        string
	Envelope         string
	Salt             []byte
	IV               []byte
	VerificationHash string
	Meta             Meta
}

// OriginalFormatVector returns the embedded fixture. The slices are fresh
// copies, so a caller may modify them.
func OriginalFormatVector() FormatVector {
	return FormatVector{
		Password:         vectorPassword,
		Plaintext:        vectorPlaintext,
		Envelope:         vectorEnvelope,
		Salt:             vectorSalt(),
		IV:               vectorIV(),
		VerificationHash: vectorVerificationHash,
		Meta: Meta{
			Version:       7,
			UpdatedAt:     1720000000123,
			DeviceID:      "11111111-2222-4333-8444-555555555555",
			DeviceName:    "Test Device",
			AppVersion:    "1.1.84",
			IV:            "AAECAwQFBgcICQoL",
			Salt:          "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=",
			Algorithm:     AlgorithmAESGCM,
			KDF:           KDFPBKDF2,
			KDFIterations: KDFIterations,
		},
	}
}

const vectorPassword = "correct horse battery staple"

const vectorVerificationHash = "vA7geQyXlzLSlzN9uYew+zUh/F9Mew5tAV7hng422kA="

const vectorPlaintext = `{"hosts":[{"id":"host-1","label":"Demo","hostname":"example.com","port":22,"username":"root","protocol":"ssh","group":"","tags":["prod"],"pinned":false,"orderKey":1000,"createdAt":"2025-01-01T00:00:00Z","updatedAt":"2025-01-02T00:00:00Z"}],"keys":[],"identities":[],"proxyProfiles":[],"snippets":[],"groups":[],"snippetPackages":[],"notes":[],"noteGroups":[],"portForwardingRules":[],"syncedAt":1720000000000}`

const vectorEnvelope = `{
  "meta": {
    "version": 7,
    "updatedAt": 1720000000123,
    "deviceId": "11111111-2222-4333-8444-555555555555",
    "deviceName": "Test Device",
    "appVersion": "1.1.84",
    "iv": "AAECAwQFBgcICQoL",
    "salt": "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=",
    "algorithm": "AES-256-GCM",
    "kdf": "PBKDF2",
    "kdfIterations": 600000
  },
  "payload": "+fPvFDTVfqXT/YW0Ok6pQxZz0DmaH/n+wFCgb4QpYyRbfKkwNgN4UjR8fg7McyNmD/BpK4BedacBNooefEBaGkxY8fB5qHQCWbo5E3TS5P2u72glFuwfsBEGvEOKHq3PFlZO0VchWcUlPfVEvaJ3ldY6HRaD/82CShlp52gHT0qpW4TXpczGLMjXT92jz4B+0YYsCLKn1uIruobErpK1+Nwb1v0Otr0G3IwGA+994DRzpYWW4EZDDawc2lPx0OyzQRwjm0KOHHCa2cA8C6dZsKK2tupwiE+/79E9vEveQSRUOZf8lU2PNiiv4fCqxCL+6z7lduXGbjMwPO2BhWNpV1fwK7jecXA27PEGBPRBQBFWqZ3cucgg0deJTJc0ck1yJ8OGzVW59vrxDkqRqt6M3i18A166cYcUbIbKQJPZDA4ZIbj1EmtiPRiRosaUc8b9Rlw1LA+75ec5DRAAXsofIfCgQwqS+Aovf72RKsB4xxPHge71dVZgTyXvF69idne1qqc2Xb91ykySDiqYwiQ9vBvV97ZBMBqRmxKJXhB4l15cHr3hFsuGNqU="
}`

func vectorSalt() []byte {
	salt := make([]byte, SaltBytes)
	for i := range salt {
		salt[i] = byte(i)
	}
	return salt
}

func vectorIV() []byte {
	iv := make([]byte, IVBytes)
	for i := range iv {
		iv[i] = byte(i)
	}
	return iv
}
