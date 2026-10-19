package ssh

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"testing"
	"time"

	xssh "golang.org/x/crypto/ssh"

	appvault "github.com/mslxl/natcatty/application/vault"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

func TestKeyParserReportsNonSecretKeyFacts(t *testing.T) {
	material := testEd25519PEM(t)

	inspection, err := (KeyParser{}).Inspect(material, "", "")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if inspection.Type != domainvault.KeyTypeED25519 {
		t.Errorf("Type = %q, want ED25519", inspection.Type)
	}
	if inspection.PublicKey == "" || inspection.NeedsPassphrase {
		t.Errorf("inspection = %+v", inspection)
	}
}

func TestKeyParserReportsAnEncryptedKeyWithoutAPassphrase(t *testing.T) {
	material := testEncryptedEd25519PEM(t, "s3cret")

	inspection, err := (KeyParser{}).Inspect(material, "", "")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if !inspection.Encrypted || !inspection.NeedsPassphrase {
		t.Fatalf("inspection = %+v, want encrypted and needing a passphrase", inspection)
	}

	if _, err := (KeyParser{}).Inspect(material, "wrong", ""); !errors.Is(err, appvault.ErrKeyPassphraseInvalid) {
		t.Fatalf("wrong passphrase error = %v, want ErrKeyPassphraseInvalid", err)
	}
}

func TestKeyParserMapsCorruptMaterial(t *testing.T) {
	if _, err := (KeyParser{}).Inspect("not a key", "", ""); !errors.Is(err, appvault.ErrKeyMaterialInvalid) {
		t.Fatalf("error = %v, want ErrKeyMaterialInvalid", err)
	}
}

func TestKeyParserValidatesACertificateAgainstItsKey(t *testing.T) {
	_, caPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caSigner, err := xssh.NewSignerFromKey(caPrivate)
	if err != nil {
		t.Fatal(err)
	}
	_, userPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	userSigner, err := xssh.NewSignerFromKey(userPrivate)
	if err != nil {
		t.Fatal(err)
	}
	certificate := &xssh.Certificate{
		Key:             userSigner.PublicKey(),
		CertType:        xssh.UserCert,
		KeyId:           "ticket-07",
		ValidPrincipals: []string{"alice"},
		ValidAfter:      uint64(time.Now().Add(-time.Minute).Unix()),
		ValidBefore:     uint64(time.Now().Add(time.Hour).Unix()),
	}
	if err := certificate.SignCert(rand.Reader, caSigner); err != nil {
		t.Fatal(err)
	}
	certLine := string(xssh.MarshalAuthorizedKey(certificate))
	material := string(pem.EncodeToMemory(mustMarshalPrivateKey(t, userPrivate)))

	inspection, err := (KeyParser{}).Inspect(material, "", certLine)
	if err != nil {
		t.Fatalf("Inspect with certificate: %v", err)
	}
	if len(inspection.PublicKey) < len("ssh-ed25519-cert-v01@openssh.com") ||
		inspection.PublicKey[:len("ssh-ed25519-cert-v01@openssh.com")] != "ssh-ed25519-cert-v01@openssh.com" {
		t.Fatalf("PublicKey = %q, want the certificate line", inspection.PublicKey)
	}

	if _, err := (KeyParser{}).Inspect(material, "", "not a certificate"); !errors.Is(err, appvault.ErrKeyCertificateInvalid) {
		t.Fatalf("error = %v, want ErrKeyCertificateInvalid", err)
	}

	_, otherPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	otherMaterial := string(pem.EncodeToMemory(mustMarshalPrivateKey(t, otherPrivate)))
	if _, err := (KeyParser{}).Inspect(otherMaterial, "", certLine); !errors.Is(err, appvault.ErrKeyCertificateInvalid) {
		t.Fatalf("mismatched certificate error = %v, want ErrKeyCertificateInvalid", err)
	}
}
