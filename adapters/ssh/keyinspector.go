package ssh

import (
	"errors"
	"strings"

	appvault "github.com/mslxl/natcatty/application/vault"
)

// KeyParser adapts the key-format parser to the Vault's KeyInspector port. It
// maps format failures onto the Vault's sentinels so the application layer
// stays independent of x/crypto.
type KeyParser struct{}

// Inspect parses key material and, when a certificate is supplied and the
// material can be decrypted, verifies that the certificate matches the key.
func (KeyParser) Inspect(privateKey, passphrase, certificate string) (appvault.KeyInspection, error) {
	parsed, err := InspectKey(privateKey, passphrase)
	if err != nil {
		return appvault.KeyInspection{}, mapKeyFormatError(err)
	}
	inspection := appvault.KeyInspection{
		Type:            parsed.Type,
		PublicKey:       parsed.PublicKey,
		Encrypted:       parsed.Encrypted,
		NeedsPassphrase: parsed.NeedsPassphrase,
	}
	if parsed.NeedsPassphrase {
		return inspection, nil
	}
	if strings.TrimSpace(certificate) != "" {
		_, info, err := LoadSigner(privateKey, passphrase, certificate)
		if err != nil {
			return appvault.KeyInspection{}, mapKeyFormatError(err)
		}
		inspection.PublicKey = info.PublicKey
	}
	return inspection, nil
}

func mapKeyFormatError(err error) error {
	switch {
	case errors.Is(err, ErrKeyPassphraseRequired):
		return appvault.ErrKeyMaterialInvalid
	case errors.Is(err, ErrKeyWrongPassphrase):
		return appvault.ErrKeyPassphraseInvalid
	case errors.Is(err, ErrKeyUnsupported):
		return appvault.ErrKeyMaterialUnsupported
	case errors.Is(err, ErrCertificateInvalid):
		return appvault.ErrKeyCertificateInvalid
	default:
		return appvault.ErrKeyMaterialInvalid
	}
}
