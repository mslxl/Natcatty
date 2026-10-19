package connection

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

// knownFingerprint is the SHA-256 fingerprint of the bytes
// "host-key-bytes", computed independently with the standard library in the
// test below. Keeping it as a literal makes the assertion independent of the
// implementation it tests.
const knownBlob = "host-key-bytes"

func TestFingerprintIsSHA256Base64WithoutPadding(t *testing.T) {
	want := "uqlUaJEm3yLsW7r4i4a5I0LgW5jDcMKWYoNA/8Nn0Eo"
	if got := Fingerprint([]byte(knownBlob)); got != want {
		t.Fatalf("Fingerprint = %q, want %q", got, want)
	}
	if strings.Contains(Fingerprint([]byte(knownBlob)), "=") {
		t.Fatal("fingerprint contains base64 padding")
	}
}

func TestFingerprintFromPublicKeyMatchesTheBlob(t *testing.T) {
	blob := []byte(knownBlob)
	publicKey := "ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob)
	if got, want := FingerprintFromPublicKey(publicKey), Fingerprint(blob); got != want {
		t.Fatalf("FingerprintFromPublicKey = %q, want %q", got, want)
	}

	if got, want := FingerprintFromPublicKey("SHA256:"+Fingerprint(blob)+"="), Fingerprint(blob); got != want {
		t.Fatalf("stored SHA256 value = %q, want %q", got, want)
	}
}

func TestClassifyTrustedKeyUsesFingerprintFirst(t *testing.T) {
	fingerprint := Fingerprint([]byte(knownBlob))
	known := []KnownHost{{
		ID: "kh-1", Hostname: "Example.COM", Port: 22,
		KeyType: "ssh-ed25519", Fingerprint: fingerprint,
	}}

	got := Classify(known, "example.com", 22, "ssh-rsa", fingerprint)
	if got.Status != StatusTrusted {
		t.Fatalf("Status = %q, want trusted", got.Status)
	}
	if got.KnownHostID != "kh-1" {
		t.Fatalf("KnownHostID = %q, want kh-1", got.KnownHostID)
	}
}

func TestClassifyChangedKeyRequiresTheSameKeyType(t *testing.T) {
	known := []KnownHost{{
		ID: "kh-1", Hostname: "example.com", Port: 2222,
		KeyType: "ssh-ed25519", Fingerprint: Fingerprint([]byte("old")),
	}}

	changed := Classify(known, "example.com", 2222, "ssh-ed25519", Fingerprint([]byte("new")))
	if changed.Status != StatusChanged {
		t.Fatalf("Status = %q, want changed", changed.Status)
	}
	if changed.ExpectedFingerprint != Fingerprint([]byte("old")) {
		t.Fatalf("ExpectedFingerprint = %q, want the saved fingerprint", changed.ExpectedFingerprint)
	}
	if changed.KnownHostID != "kh-1" {
		t.Fatalf("KnownHostID = %q, want kh-1", changed.KnownHostID)
	}

	// A different algorithm on a host with other trusted keys is a first-use
	// prompt, not a rotation alarm.
	unknown := Classify(known, "example.com", 2222, "ssh-rsa", Fingerprint([]byte("new")))
	if unknown.Status != StatusUnknown {
		t.Fatalf("new algorithm Status = %q, want unknown", unknown.Status)
	}
}

func TestClassifyUnknownKeyForAnUnseenHost(t *testing.T) {
	known := []KnownHost{{
		ID: "kh-1", Hostname: "other.example", Port: 22,
		KeyType: "ssh-ed25519", Fingerprint: Fingerprint([]byte("old")),
	}}
	got := Classify(known, "example.com", 22, "ssh-ed25519", Fingerprint([]byte("new")))
	if got.Status != StatusUnknown {
		t.Fatalf("Status = %q, want unknown", got.Status)
	}
}

func TestClassifyIgnoresHashedKnownHostsAndUsesDefaultPort(t *testing.T) {
	fingerprint := Fingerprint([]byte("key"))
	hashed := []KnownHost{{
		ID: "kh-hashed", Hostname: "(hashed)", Port: 22,
		KeyType: "ssh-ed25519", Fingerprint: fingerprint,
	}}
	got := Classify(hashed, "example.com", 0, "ssh-ed25519", fingerprint)
	if got.Status != StatusUnknown {
		t.Fatalf("hashed record Status = %q, want unknown", got.Status)
	}

	known := []KnownHost{{
		ID: "kh-1", Hostname: "example.com", Port: 22,
		KeyType: "ssh-ed25519", Fingerprint: fingerprint,
	}}
	got = Classify(known, "example.com", 0, "ssh-ed25519", fingerprint)
	if got.Status != StatusTrusted {
		t.Fatalf("default port Status = %q, want trusted", got.Status)
	}
}

func TestNewKnownHostFromKeyRecordsTheTrustDecision(t *testing.T) {
	when := time.Unix(1700000000, 0).UTC()
	key := Key{
		Hostname: "example.com", Port: 22, KeyType: "ssh-ed25519",
		Fingerprint: "abc", PublicKey: "ssh-ed25519 AAAA",
	}
	known := NewKnownHost("kh-1", key, when)
	if known.ID != "kh-1" || known.Hostname != "example.com" || known.Port != 22 ||
		known.KeyType != "ssh-ed25519" || known.Fingerprint != "abc" ||
		known.PublicKey != "ssh-ed25519 AAAA" {
		t.Fatalf("KnownHost = %+v", known)
	}
	if !known.DiscoveredAt.Equal(when) || !known.LastSeen.Equal(when) {
		t.Fatalf("timestamps = %v / %v, want %v", known.DiscoveredAt, known.LastSeen, when)
	}
}
