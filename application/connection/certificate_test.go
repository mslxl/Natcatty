package connection

import (
	"context"
	"testing"

	domainconn "github.com/mslxl/natcatty/domain/connection"
)

func hostCertificateKey(certFingerprint, keyFingerprint, caFingerprint string) domainconn.Key {
	return domainconn.Key{
		Hostname:                  "example.com",
		Port:                      22,
		KeyType:                   "host-cert",
		Fingerprint:               certFingerprint,
		PublicKey:                 "host-cert AAAA",
		IsHostCertificate:         true,
		CertificateKeyType:        "ssh-key",
		CertificateKeyFingerprint: keyFingerprint,
		CertificateKeyPublicKey:   "ssh-key AAAAkey",
		CertificateCAType:         "ssh-key",
		CertificateCAFingerprint:  caFingerprint,
		CertificateCAPublicKey:    "ssh-key AAAAca",
	}
}

func caKnownHost(fingerprint string) domainconn.KnownHost {
	return domainconn.KnownHost{
		ID: "kh-ca", Hostname: "example.com", Port: 22,
		KeyType: "ssh-key", Fingerprint: fingerprint,
	}
}

// TestHostCertificateTrustedByCAAndRenewalSkipsThePrompt proves a certificate
// signed by a Known CA is accepted without asking, that a renewal (new
// certificate blob, same embedded key and CA) stays trusted, and that a
// certificate signed by a different CA is an unknown trust anchor and prompts.
func TestHostCertificateTrustedByCAAndRenewalSkipsThePrompt(t *testing.T) {
	const caFingerprint = "SHA256:ca-one"
	manager, _, dialer, known, prompter := newTestManager(t)
	known.records = []domainconn.KnownHost{caKnownHost(caFingerprint)}
	dialer.verify = true
	dialer.key = hostCertificateKey("SHA256:cert-first", "SHA256:key-one", caFingerprint)

	lease, err := manager.Acquire(context.Background(), sshRequest())
	if err != nil {
		t.Fatalf("Acquire with a CA-trusted certificate: %v", err)
	}
	t.Cleanup(func() { _ = lease.Release() })
	if got := len(prompter.prompted()); got != 0 {
		t.Fatalf("prompts for a CA-trusted certificate = %d, want 0", got)
	}

	verify := dialer.request().VerifyHostKey
	renewed := hostCertificateKey("SHA256:cert-renewed", "SHA256:key-one", caFingerprint)
	if err := verify(context.Background(), renewed); err != nil {
		t.Fatalf("renewal with the same CA: %v", err)
	}
	if got := len(prompter.prompted()); got != 0 {
		t.Fatalf("prompts after a renewal = %d, want 0", got)
	}

	otherCA := hostCertificateKey("SHA256:cert-other", "SHA256:key-one", "SHA256:ca-two")
	if err := verify(context.Background(), otherCA); err != nil {
		t.Fatalf("trusting the certificate under a new CA: %v", err)
	}
	if got := len(prompter.prompted()); got != 1 {
		t.Fatalf("prompts for a different CA = %d, want 1", got)
	}
}

// TestHostCertificateTrustedUnderItsEmbeddedKey proves a certificate still
// matches a Known Host recorded for the certificate's own key, which is the
// path that keeps a pre-existing key trust working after a certificate is
// introduced.
func TestHostCertificateTrustedUnderItsEmbeddedKey(t *testing.T) {
	manager, _, dialer, known, prompter := newTestManager(t)
	known.records = []domainconn.KnownHost{{
		ID: "kh-key", Hostname: "example.com", Port: 22,
		KeyType: "ssh-key", Fingerprint: "SHA256:key-one",
	}}
	dialer.verify = true
	dialer.key = hostCertificateKey("SHA256:cert-new", "SHA256:key-one", "SHA256:ca-unknown")

	lease, err := manager.Acquire(context.Background(), sshRequest())
	if err != nil {
		t.Fatalf("Acquire with an embedded-key-trusted certificate: %v", err)
	}
	t.Cleanup(func() { _ = lease.Release() })
	if got := len(prompter.prompted()); got != 0 {
		t.Fatalf("prompts = %d, want 0 for a certificate whose key is known", got)
	}
}

// TestTrustingAHostCertificatePersistsTheCA proves the trust prompt's record
// is the stable trust anchor, so later renewals match and a different CA does
// not.
func TestTrustingAHostCertificatePersistsTheCA(t *testing.T) {
	manager, _, dialer, known, prompter := newTestManager(t)
	dialer.verify = true
	dialer.key = hostCertificateKey("SHA256:cert-new", "SHA256:key-one", "SHA256:ca-two")

	lease, err := manager.Acquire(context.Background(), sshRequest())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	t.Cleanup(func() { _ = lease.Release() })

	if got := len(prompter.prompted()); got != 1 {
		t.Fatalf("prompts for an unknown certificate = %d, want 1", got)
	}
	if len(known.upserts) != 1 {
		t.Fatalf("known-host upserts = %d, want 1", len(known.upserts))
	}
	record := known.upserts[0]
	if record.Fingerprint != "ca-two" {
		t.Fatalf("persisted fingerprint = %q, want the normalized CA fingerprint", record.Fingerprint)
	}
	if record.KeyType != "ssh-key" {
		t.Fatalf("persisted key type = %q, want the CA key type", record.KeyType)
	}
}
