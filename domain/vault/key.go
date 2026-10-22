package vault

import (
	"strings"
	"time"

	"github.com/mslxl/natcatty/domain/model"
)

// KeyType is the algorithm family of an SSH Key. It is a display and
// validation value; the cryptographic material stays in the Key entity.
type KeyType string

const (
	// KeyTypeRSA is an RSA SSH Key.
	KeyTypeRSA KeyType = "RSA"
	// KeyTypeECDSA is an ECDSA SSH Key.
	KeyTypeECDSA KeyType = "ECDSA"
	// KeyTypeED25519 is an Ed25519 SSH Key.
	KeyTypeED25519 KeyType = "ED25519"
)

// KeySource records how an SSH Key entered the Vault.
type KeySource string

const (
	// KeySourceImported is key material pasted or imported by the user.
	KeySourceImported KeySource = "imported"
	// KeySourceReference is a Key that points at external material.
	KeySourceReference KeySource = "reference"
)

// Key is one saved SSH Key. The private key and its passphrase are never
// stored on this struct: the Vault encrypts them into the field-secret store,
// and this record carries only the non-secret HasPassphrase flag.
type Key struct {
	ID    model.KeyID `json:"id"`
	Label string      `json:"label"`
	Type  KeyType     `json:"type"`
	// PublicKey is the OpenSSH "algorithm base64" line, when known.
	PublicKey string `json:"publicKey,omitempty"`
	// Certificate is the signed OpenSSH certificate line, when the Key is a
	// certificate credential. It is public material.
	Certificate string    `json:"certificate,omitempty"`
	Source      KeySource `json:"source"`
	// FilePath is the referenced IdentityFile path for a reference Key. It is
	// non-secret metadata, used by CSV export so an exported inventory can be
	// re-imported against the same material.
	FilePath string `json:"filePath,omitempty"`
	// HasPrivateKey reports that encrypted private material exists for this
	// Key. The ciphertext lives in secret_values, never in this record. It is
	// false for session-only material after a restart.
	HasPrivateKey bool `json:"hasPrivateKey,omitempty"`
	// HasPassphrase reports that an encrypted passphrase exists for this Key.
	// The ciphertext lives in secret_values, never in this record.
	HasPassphrase bool      `json:"hasPassphrase,omitempty"`
	OrderKey      int64     `json:"orderKey"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// KeyDraft is the editable part of an SSH Key. PrivateKey and Passphrase are
// plaintext form input; the Vault encrypts them and never copies them onto
// Key or into the JSON tail. An empty PrivateKey on Update keeps the stored
// material.
type KeyDraft struct {
	Label      string
	PrivateKey string
	Passphrase string
	// SavePassphrase stores Passphrase encrypted. When false, the passphrase
	// is used to validate the material and is then forgotten.
	SavePassphrase bool
	Certificate    string
	// ClearCertificate removes a stored certificate on Update.
	ClearCertificate bool
	// ClearPassphrase removes a stored passphrase on Update.
	ClearPassphrase bool
	// ClearPrivateKey removes stored key material on Update. It is rejected
	// for an imported Key because a Key with no material cannot authenticate.
	ClearPrivateKey bool
	// PublicKey and Type are optional hints from the importer. The Vault
	// replaces them with what the key material reports.
	PublicKey string
	Type      KeyType
	Source    KeySource
	// FilePath records where reference material lives. The app fills it for an
	// imported IdentityFile; it is ignored for pasted material.
	FilePath string
}

// NormalizeKeyDraft trims display input, fills defaults, and validates the
// values that do not require parsing key material.
func NormalizeKeyDraft(draft KeyDraft) (KeyDraft, error) {
	draft.Label = strings.TrimSpace(draft.Label)
	draft.Type = KeyType(strings.ToUpper(strings.TrimSpace(string(draft.Type))))
	draft.PublicKey = strings.TrimSpace(draft.PublicKey)
	draft.Certificate = strings.TrimSpace(draft.Certificate)
	draft.FilePath = strings.TrimSpace(draft.FilePath)
	if draft.Source == "" {
		draft.Source = KeySourceImported
	}
	if draft.Label == "" {
		return KeyDraft{}, validation(FieldLabel, RuleRequired)
	}
	if err := validateText(FieldLabel, draft.Label, maxLabelLength, false); err != nil {
		return KeyDraft{}, err
	}
	if draft.Type != "" {
		switch draft.Type {
		case KeyTypeRSA, KeyTypeECDSA, KeyTypeED25519:
		default:
			return KeyDraft{}, validation(FieldKeyType, RuleUnsupported)
		}
	}
	switch draft.Source {
	case KeySourceImported, KeySourceReference:
	default:
		return KeyDraft{}, validation(FieldKeyType, RuleUnsupported)
	}
	if len(draft.PrivateKey) > maxKeyMaterialLength {
		return KeyDraft{}, validation(FieldKeyMaterial, RuleTooLong)
	}
	if len(draft.Certificate) > maxKeyMaterialLength {
		return KeyDraft{}, validation(FieldCertificate, RuleTooLong)
	}
	return draft, nil
}

// KeyTypeForAlgorithm maps an SSH public-key algorithm to its KeyType.
func KeyTypeForAlgorithm(algorithm string) (KeyType, bool) {
	algorithm = strings.ToLower(strings.TrimSpace(algorithm))
	switch {
	case strings.HasPrefix(algorithm, "ssh-ed25519"):
		return KeyTypeED25519, true
	case strings.HasPrefix(algorithm, "ssh-rsa"), strings.HasPrefix(algorithm, "rsa-sha2-"):
		return KeyTypeRSA, true
	case strings.HasPrefix(algorithm, "ecdsa-sha2-"):
		return KeyTypeECDSA, true
	default:
		return "", false
	}
}
