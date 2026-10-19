package ssh

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	xssh "golang.org/x/crypto/ssh"

	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// PPK fixtures are copied from github.com/kayrus/putty's Apache-2.0 test
// vectors; they were produced by PuTTYgen, not by this package's parser.
func readFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(data)
}

func testEd25519PEM(t *testing.T) string {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ed25519: %v", err)
	}
	block, err := xssh.MarshalPrivateKey(private, "test")
	if err != nil {
		t.Fatalf("marshal openssh key: %v", err)
	}
	return string(pem.EncodeToMemory(block))
}

func testEncryptedEd25519PEM(t *testing.T, passphrase string) string {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ed25519: %v", err)
	}
	block, err := xssh.MarshalPrivateKeyWithPassphrase(private, "test", []byte(passphrase))
	if err != nil {
		t.Fatalf("marshal encrypted openssh key: %v", err)
	}
	return string(pem.EncodeToMemory(block))
}

func testRSAPEM(t *testing.T) string {
	t.Helper()
	private, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(private),
	}))
}

func TestLoadSignerReadsEverySupportedFormat(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		material   string
		passphrase string
		wantType   domainvault.KeyType
		wantPrefix string
	}{
		{
			name:       "openssh unencrypted",
			material:   testEd25519PEM(t),
			wantType:   domainvault.KeyTypeED25519,
			wantPrefix: "ssh-ed25519 ",
		},
		{
			name:       "openssh encrypted",
			material:   testEncryptedEd25519PEM(t, "secret"),
			passphrase: "secret",
			wantType:   domainvault.KeyTypeED25519,
			wantPrefix: "ssh-ed25519 ",
		},
		{
			name:       "legacy pem",
			material:   testRSAPEM(t),
			wantType:   domainvault.KeyTypeRSA,
			wantPrefix: "ssh-rsa ",
		},
		{
			name:       "unencrypted pkcs8",
			material:   readFixture(t, "ed25519-plain-pkcs8.pem"),
			wantType:   domainvault.KeyTypeED25519,
			wantPrefix: "ssh-ed25519 ",
		},
		{
			name:       "encrypted pkcs8",
			material:   readFixture(t, "rsa-enc-pkcs8.pem"),
			passphrase: "keypass",
			wantType:   domainvault.KeyTypeRSA,
			wantPrefix: "ssh-rsa ",
		},
		{
			name:       "ppk v3 unencrypted",
			material:   readFixture(t, "ppk_v3_rsa_unencrypted.ppk"),
			wantType:   domainvault.KeyTypeRSA,
			wantPrefix: "ssh-rsa ",
		},
		{
			name:       "ppk v3 encrypted",
			material:   readFixture(t, "ppk_v3_rsa_encrypted.ppk"),
			passphrase: "testkey",
			wantType:   domainvault.KeyTypeRSA,
			wantPrefix: "ssh-rsa ",
		},
		{
			name:       "ppk v2 rsa encrypted",
			material:   readFixture(t, "ppk_v2_rsa_encrypted.ppk"),
			passphrase: "testkey",
			wantType:   domainvault.KeyTypeRSA,
			wantPrefix: "ssh-rsa ",
		},
		{
			name:       "ppk v2 ed25519 encrypted",
			material:   readFixture(t, "ppk_v2_ed25519_encrypted.ppk"),
			passphrase: "testkey",
			wantType:   domainvault.KeyTypeED25519,
			wantPrefix: "ssh-ed25519 ",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			signer, info, err := LoadSigner(tc.material, tc.passphrase, "")
			if err != nil {
				t.Fatalf("LoadSigner: %v", err)
			}
			if signer == nil {
				t.Fatal("LoadSigner returned a nil signer")
			}
			if info.Type != tc.wantType {
				t.Errorf("type = %q, want %q", info.Type, tc.wantType)
			}
			if !strings.HasPrefix(info.PublicKey, tc.wantPrefix) {
				t.Errorf("public key = %q, want prefix %q", info.PublicKey, tc.wantPrefix)
			}
			if !info.HasPrivateKey {
				t.Error("HasPrivateKey = false")
			}
		})
	}
}

func TestInspectKeyReportsMissingPassphraseWithoutMaterial(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		material string
	}{
		{"openssh", testEncryptedEd25519PEM(t, "secret")},
		{"pkcs8", readFixture(t, "rsa-enc-pkcs8.pem")},
		{"ppk v3", readFixture(t, "ppk_v3_rsa_encrypted.ppk")},
		{"ppk v2", readFixture(t, "ppk_v2_ed25519_encrypted.ppk")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			inspection, err := InspectKey(tc.material, "")
			if err != nil {
				t.Fatalf("InspectKey: %v", err)
			}
			if !inspection.NeedsPassphrase {
				t.Error("NeedsPassphrase = false")
			}
			if !inspection.Encrypted {
				t.Error("Encrypted = false")
			}
		})
	}
}

func TestLoadSignerRejectsWrongPassphrase(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		material string
	}{
		{"openssh", testEncryptedEd25519PEM(t, "secret")},
		{"pkcs8", readFixture(t, "rsa-enc-pkcs8.pem")},
		{"ppk v3", readFixture(t, "ppk_v3_rsa_encrypted.ppk")},
		{"ppk v2", readFixture(t, "ppk_v2_ed25519_encrypted.ppk")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, _, err := LoadSigner(tc.material, "not-the-passphrase", ""); !errors.Is(err, ErrKeyWrongPassphrase) {
				t.Fatalf("err = %v, want ErrKeyWrongPassphrase", err)
			}
		})
	}
}

func TestLoadSignerRepairsLostPEMNewlines(t *testing.T) {
	t.Parallel()

	material := testEd25519PEM(t)
	broken := strings.ReplaceAll(strings.TrimSpace(material), "\n", `\n`)
	if _, _, err := LoadSigner(broken, "", ""); err != nil {
		t.Fatalf("LoadSigner with escaped newlines: %v", err)
	}
}

func TestLoadSignerRejectsGarbage(t *testing.T) {
	t.Parallel()

	if _, _, err := LoadSigner("not a key at all", "", ""); !errors.Is(err, ErrKeyInvalid) {
		t.Fatalf("err = %v, want ErrKeyInvalid", err)
	}
}

func TestLoadSignerUsesMatchingCertificate(t *testing.T) {
	t.Parallel()

	_, caPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate CA: %v", err)
	}
	caSigner, err := xssh.NewSignerFromKey(caPrivate)
	if err != nil {
		t.Fatalf("CA signer: %v", err)
	}
	_, userPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate user key: %v", err)
	}
	userSigner, err := xssh.NewSignerFromKey(userPrivate)
	if err != nil {
		t.Fatalf("user signer: %v", err)
	}

	certificate := &xssh.Certificate{
		Key:             userSigner.PublicKey(),
		CertType:        xssh.UserCert,
		KeyId:           "ticket-07",
		ValidPrincipals: []string{"alice"},
		ValidAfter:      uint64(time.Now().Add(-time.Minute).Unix()),
		ValidBefore:     uint64(time.Now().Add(time.Hour).Unix()),
		Permissions: xssh.Permissions{
			Extensions: map[string]string{"permit-pty": ""},
		},
	}
	if err := certificate.SignCert(rand.Reader, caSigner); err != nil {
		t.Fatalf("sign certificate: %v", err)
	}
	certLine := string(xssh.MarshalAuthorizedKey(certificate))

	keyPEM := string(pem.EncodeToMemory(mustMarshalPrivateKey(t, userPrivate)))
	signer, info, err := LoadSigner(keyPEM, "", certLine)
	if err != nil {
		t.Fatalf("LoadSigner with certificate: %v", err)
	}
	if _, ok := signer.PublicKey().(*xssh.Certificate); !ok {
		t.Fatalf("signer public key = %T, want *ssh.Certificate", signer.PublicKey())
	}
	if !strings.HasPrefix(info.PublicKey, "ssh-ed25519-cert-v01@openssh.com ") {
		t.Errorf("info.PublicKey = %q, want certificate line", info.PublicKey)
	}

	// A certificate for a different key must fail closed rather than fall
	// back to the bare key.
	_, otherPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate other key: %v", err)
	}
	otherSigner, err := xssh.NewSignerFromKey(otherPrivate)
	if err != nil {
		t.Fatalf("other signer: %v", err)
	}
	other := &xssh.Certificate{
		Key:         otherSigner.PublicKey(),
		CertType:    xssh.UserCert,
		KeyId:       "other",
		ValidAfter:  uint64(time.Now().Add(-time.Minute).Unix()),
		ValidBefore: uint64(time.Now().Add(time.Hour).Unix()),
	}
	if err := other.SignCert(rand.Reader, caSigner); err != nil {
		t.Fatalf("sign other certificate: %v", err)
	}
	_, _, err = LoadSigner(keyPEM, "", string(xssh.MarshalAuthorizedKey(other)))
	if !errors.Is(err, ErrCertificateInvalid) {
		t.Fatalf("mismatched certificate err = %v, want ErrCertificateInvalid", err)
	}
}

func TestLoadSignerRejectsUnparseableCertificate(t *testing.T) {
	t.Parallel()

	material := testEd25519PEM(t)
	if _, _, err := LoadSigner(material, "", "ssh-ed25519 AAAA not really"); !errors.Is(err, ErrCertificateInvalid) {
		t.Fatalf("err = %v, want ErrCertificateInvalid", err)
	}
}

func mustMarshalPrivateKey(t *testing.T, key ed25519.PrivateKey) *pem.Block {
	t.Helper()
	block, err := xssh.MarshalPrivateKey(key, "")
	if err != nil {
		t.Fatalf("marshal private key: %v", err)
	}
	return block
}
