package ssh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	xssh "golang.org/x/crypto/ssh"

	"github.com/mslxl/natcatty/adapters/sshtest/fakessh"
	appconnection "github.com/mslxl/natcatty/application/connection"
	domainconn "github.com/mslxl/natcatty/domain/connection"
)

// TestDialNegotiatesAndVerifiesAHostCertificate proves the adapter offers the
// certificate algorithms, completes KEX against a certificate-only host, and
// hands the application the certificate, its embedded key, and its CA so a
// renewal can be matched without a fingerprint that changes.
func TestDialNegotiatesAndVerifiesAHostCertificate(t *testing.T) {
	_, caPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("CA key: %v", err)
	}
	caSigner, err := xssh.NewSignerFromKey(caPrivate)
	if err != nil {
		t.Fatalf("CA signer: %v", err)
	}

	server, dialFunc := startFixture(t, fakessh.Options{
		Password:                  "certpass",
		HostCertificateAuthority:  caSigner,
		HostCertificatePrincipals: []string{"example.com"},
	})
	dialer := NewDialer(WithDialFunc(dialFunc), WithTimeouts(2*time.Second, 2*time.Second))

	var seen domainconn.Key
	client, err := dialer.Dial(context.Background(), appconnection.DialRequest{
		Hostname: "example.com", Port: 22, Username: "root", Password: "certpass",
		VerifyHostKey: func(_ context.Context, key domainconn.Key) error {
			seen = key
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	certificate := server.Certificate()
	if certificate == nil {
		t.Fatal("the fixture did not present a host certificate")
	}
	if !seen.IsHostCertificate {
		t.Fatalf("host key = %+v, want a host certificate", seen)
	}
	if seen.KeyType != xssh.CertAlgoED25519v01 {
		t.Fatalf("host key type = %q, want the Ed25519 certificate type %q", seen.KeyType, xssh.CertAlgoED25519v01)
	}
	if want := domainconn.Fingerprint(certificate.Marshal()); seen.Fingerprint != want {
		t.Fatalf("certificate fingerprint = %q, want %q", seen.Fingerprint, want)
	}
	if want := domainconn.Fingerprint(certificate.Key.Marshal()); seen.CertificateKeyFingerprint != want {
		t.Fatalf("embedded key fingerprint = %q, want %q", seen.CertificateKeyFingerprint, want)
	}
	if want := domainconn.Fingerprint(certificate.SignatureKey.Marshal()); seen.CertificateCAFingerprint != want {
		t.Fatalf("CA fingerprint = %q, want %q", seen.CertificateCAFingerprint, want)
	}

	// A certificate-only host still becomes a usable shell after verification.
	terminal, err := client.OpenTerminal(context.Background(), appconnection.TerminalSpec{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("OpenTerminal: %v", err)
	}
	if _, err := terminal.Write([]byte("ok")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	echo := make([]byte, 2)
	if err := readFull(t, terminal, echo); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(echo) != "ok" {
		t.Fatalf("echo = %q, want ok", echo)
	}
}

// TestHostCertificateForAnotherPrincipalFailsClosed makes sure certificate
// structure is checked before it can be trusted.
func TestHostCertificateForAnotherPrincipalFailsClosed(t *testing.T) {
	_, caPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("CA key: %v", err)
	}
	caSigner, err := xssh.NewSignerFromKey(caPrivate)
	if err != nil {
		t.Fatalf("CA signer: %v", err)
	}

	_, dialFunc := startFixture(t, fakessh.Options{
		Password:                  "certpass",
		HostCertificateAuthority:  caSigner,
		HostCertificatePrincipals: []string{"other.example.com"},
	})
	dialer := NewDialer(WithDialFunc(dialFunc), WithTimeouts(2*time.Second, 2*time.Second))

	_, err = dialer.Dial(context.Background(), appconnection.DialRequest{
		Hostname: "example.com", Port: 22, Username: "root", Password: "certpass",
		VerifyHostKey: acceptEveryKey,
	})
	assertKind(t, err, appconnection.KindHostKey)
}
