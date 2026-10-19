package ssh

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"

	"github.com/kayrus/putty"
	xssh "golang.org/x/crypto/ssh"

	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// Key-format sentinels. They let the application and the adapter tell a
// missing passphrase apart from a wrong one, and an unsupported format apart
// from corrupt material, without exposing parser text.
var (
	// ErrKeyPassphraseRequired reports encrypted key material with no
	// passphrase supplied.
	ErrKeyPassphraseRequired = errors.New("ssh: key passphrase required")
	// ErrKeyWrongPassphrase reports a passphrase that does not decrypt the
	// key.
	ErrKeyWrongPassphrase = errors.New("ssh: wrong key passphrase")
	// ErrKeyUnsupported reports a key format this build cannot read.
	ErrKeyUnsupported = errors.New("ssh: unsupported key format")
	// ErrKeyInvalid reports material that is not a private key.
	ErrKeyInvalid = errors.New("ssh: invalid private key")
	// ErrCertificateInvalid reports a certificate line this build cannot
	// parse or one that does not match the private key.
	ErrCertificateInvalid = errors.New("ssh: invalid certificate")
)

const (
	ppkMarker          = "PuTTY-User-Key-File-"
	encryptedPKCS8Type = "ENCRYPTED PRIVATE KEY"
)

// KeyInfo is the non-secret description of loaded key material.
type KeyInfo struct {
	Type          domainvault.KeyType
	PublicKey     string
	Encrypted     bool
	HasPrivateKey bool
}

// RepairPEM rebuilds clean PEM framing for a key whose newlines were lost,
// turned into literal "\n" escapes, or indented in transit. The base64 body
// is preserved byte-for-byte. Encrypted legacy PEM with Proc-Type or
// DEK-Info headers is left alone because those header lines are not base64.
func RepairPEM(text string) (string, bool) {
	unescaped := strings.NewReplacer(`\r\n`, "\n", `\n`, "\n", `\r`, "\n").Replace(text)
	if unescaped != text {
		// Base64 cannot contain a backslash, so an escaped newline is always
		// transport damage rather than key content.
		if block, _ := pem.Decode([]byte(unescaped)); block != nil {
			return unescaped, true
		}
	}
	if block, _ := pem.Decode([]byte(unescaped)); block != nil {
		// Already well framed.
		return text, false
	}
	label, body, ok := findPEMBlock(unescaped)
	if !ok || strings.Contains(body, "Proc-Type:") || strings.Contains(body, "DEK-Info:") {
		return text, false
	}
	base64 := strings.Map(func(r rune) rune {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9',
			r == '+', r == '/', r == '=':
			return r
		default:
			return -1
		}
	}, body)
	if base64 == "" {
		return text, false
	}
	var wrapped strings.Builder
	for len(base64) > 64 {
		wrapped.WriteString(base64[:64])
		wrapped.WriteByte('\n')
		base64 = base64[64:]
	}
	wrapped.WriteString(base64)
	return fmt.Sprintf("-----BEGIN %s-----\n%s\n-----END %s-----\n", label, wrapped.String(), label), true
}

// findPEMBlock returns the label and raw body between the first BEGIN/END
// markers.
func findPEMBlock(text string) (label, body string, ok bool) {
	begin := strings.Index(text, "-----BEGIN ")
	if begin < 0 {
		return "", "", false
	}
	rest := text[begin+len("-----BEGIN "):]
	label, tail, found := strings.Cut(rest, "-----")
	if !found {
		return "", "", false
	}
	endMarker := "-----END " + label + "-----"
	end := strings.Index(tail, endMarker)
	if end < 0 {
		return "", "", false
	}
	return label, tail[:end], true
}

// InspectKey reports the type and public key of private key material without
// returning the private key. passphrase may be empty when the caller intends
// to prompt at connect time; encrypted material then reports
// NeedsPassphrase.
type KeyInspection struct {
	KeyInfo
	NeedsPassphrase bool
}

// InspectKey parses key material in OpenSSH, PEM, encrypted PKCS#8, or PuTTY
// PPK v2/v3 form.
func InspectKey(privateKey, passphrase string) (KeyInspection, error) {
	signer, info, err := parseKeyMaterial(privateKey, passphrase)
	if signer != nil {
		_ = signer
		return KeyInspection{KeyInfo: info}, nil
	}
	var needs *passphraseRequiredError
	if errors.As(err, &needs) {
		return KeyInspection{KeyInfo: needs.info, NeedsPassphrase: true}, nil
	}
	return KeyInspection{}, err
}

// LoadSigner parses key material and returns a signer. When certificate is
// non-empty, the signer advertises the certificate instead of the bare key.
func LoadSigner(privateKey, passphrase, certificate string) (xssh.Signer, KeyInfo, error) {
	signer, info, err := parseKeyMaterial(privateKey, passphrase)
	if err != nil {
		return nil, KeyInfo{}, err
	}
	if strings.TrimSpace(certificate) == "" {
		return signer, info, nil
	}

	cert, err := ParseCertificate(certificate)
	if err != nil {
		return nil, KeyInfo{}, err
	}
	if !bytes.Equal(cert.Key.Marshal(), signer.PublicKey().Marshal()) {
		return nil, KeyInfo{}, fmt.Errorf("%w: certificate does not match the private key", ErrCertificateInvalid)
	}
	certSigner, err := xssh.NewCertSigner(cert, signer)
	if err != nil {
		return nil, KeyInfo{}, fmt.Errorf("%w: %v", ErrCertificateInvalid, err)
	}
	info.PublicKey = strings.TrimSpace(string(xssh.MarshalAuthorizedKey(cert)))
	return certSigner, info, nil
}

// ParseCertificate parses one OpenSSH certificate line.
func ParseCertificate(text string) (*xssh.Certificate, error) {
	key, _, _, _, err := xssh.ParseAuthorizedKey([]byte(strings.TrimSpace(text)))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCertificateInvalid, err)
	}
	cert, ok := key.(*xssh.Certificate)
	if !ok {
		return nil, fmt.Errorf("%w: not a certificate", ErrCertificateInvalid)
	}
	return cert, nil
}

// parseKeyMaterial normalizes and parses one private key. A nil signer with a
// *passphraseRequiredError means the caller must prompt.
func parseKeyMaterial(privateKey, passphrase string) (xssh.Signer, KeyInfo, error) {
	if strings.TrimSpace(privateKey) == "" {
		return nil, KeyInfo{}, ErrKeyInvalid
	}
	material := privateKey
	if repaired, changed := RepairPEM(privateKey); changed {
		material = repaired
	}

	if strings.Contains(material, ppkMarker) {
		return parsePPK(material, passphrase)
	}

	block, _ := pem.Decode([]byte(material))
	if block != nil && block.Type == encryptedPKCS8Type {
		return parseEncryptedPKCS8(block.Bytes, passphrase)
	}

	signer, err := xssh.ParsePrivateKey([]byte(material))
	if err == nil {
		return describedSigner(signer, false)
	}
	var missing *xssh.PassphraseMissingError
	if errors.As(err, &missing) {
		info := KeyInfo{Encrypted: true, HasPrivateKey: true}
		if missing.PublicKey != nil {
			describePublicKey(&info, missing.PublicKey)
		}
		if passphrase == "" {
			return nil, info, &passphraseRequiredError{info: info}
		}
		signer, err = xssh.ParsePrivateKeyWithPassphrase([]byte(material), []byte(passphrase))
		if err != nil {
			return nil, info, fmt.Errorf("%w: %v", ErrKeyWrongPassphrase, err)
		}
		return describedSigner(signer, true)
	}

	if block != nil {
		return nil, KeyInfo{}, fmt.Errorf("%w: %v", ErrKeyUnsupported, err)
	}
	return nil, KeyInfo{}, fmt.Errorf("%w: %v", ErrKeyInvalid, err)
}

func parsePPK(material, passphrase string) (xssh.Signer, KeyInfo, error) {
	key, err := putty.New([]byte(material))
	if err != nil {
		return nil, KeyInfo{}, fmt.Errorf("%w: %v", ErrKeyInvalid, err)
	}
	info := KeyInfo{Encrypted: key.Encryption != "none", HasPrivateKey: true}
	if publicKey, err := key.ParseRawPublicKey(); err == nil {
		if parsed, err := xssh.NewPublicKey(publicKey); err == nil {
			describePublicKey(&info, parsed)
		}
	}
	if info.Encrypted && passphrase == "" {
		return nil, info, &passphraseRequiredError{info: info}
	}
	raw, err := key.ParseRawPrivateKey([]byte(passphrase))
	if err != nil {
		if info.Encrypted {
			return nil, info, fmt.Errorf("%w: %v", ErrKeyWrongPassphrase, err)
		}
		return nil, info, fmt.Errorf("%w: %v", ErrKeyInvalid, err)
	}
	signer, err := xssh.NewSignerFromKey(raw)
	if err != nil {
		return nil, info, fmt.Errorf("%w: %v", ErrKeyUnsupported, err)
	}
	return describedSigner(signer, info.Encrypted)
}

func describedSigner(signer xssh.Signer, encrypted bool) (xssh.Signer, KeyInfo, error) {
	info := KeyInfo{Encrypted: encrypted, HasPrivateKey: true}
	describePublicKey(&info, signer.PublicKey())
	return signer, info, nil
}

func describePublicKey(info *KeyInfo, key xssh.PublicKey) {
	info.PublicKey = strings.TrimSpace(string(xssh.MarshalAuthorizedKey(key)))
	if keyType, ok := domainvault.KeyTypeForAlgorithm(key.Type()); ok {
		info.Type = keyType
	}
}

// passphraseRequiredError carries the public-key knowledge a caller may use
// while the passphrase prompt is open. It is an internal control-flow error;
// InspectKey converts it into NeedsPassphrase.
type passphraseRequiredError struct {
	info KeyInfo
}

func (e *passphraseRequiredError) Error() string { return ErrKeyPassphraseRequired.Error() }

func (e *passphraseRequiredError) Is(target error) bool { return target == ErrKeyPassphraseRequired }

// parseEncryptedPKCS8 decrypts a PKCS#8 EncryptedPrivateKeyInfo and parses the
// inner key. x/crypto/ssh does not support encrypted PKCS#8 directly.
func parseEncryptedPKCS8(der []byte, passphrase string) (xssh.Signer, KeyInfo, error) {
	if passphrase == "" {
		return nil, KeyInfo{Encrypted: true, HasPrivateKey: true}, &passphraseRequiredError{
			info: KeyInfo{Encrypted: true, HasPrivateKey: true},
		}
	}
	plain, err := DecryptPKCS8(der, []byte(passphrase))
	if err != nil {
		if errors.Is(err, errPKCS8Unsupported) {
			return nil, KeyInfo{Encrypted: true, HasPrivateKey: true}, fmt.Errorf("%w: %v", ErrKeyUnsupported, err)
		}
		return nil, KeyInfo{Encrypted: true, HasPrivateKey: true}, fmt.Errorf("%w: %v", ErrKeyWrongPassphrase, err)
	}
	raw, err := x509.ParsePKCS8PrivateKey(plain)
	if err != nil {
		return nil, KeyInfo{}, fmt.Errorf("%w: %v", ErrKeyInvalid, err)
	}
	signer, err := xssh.NewSignerFromKey(raw)
	if err != nil {
		return nil, KeyInfo{}, fmt.Errorf("%w: %v", ErrKeyUnsupported, err)
	}
	return describedSigner(signer, true)
}
