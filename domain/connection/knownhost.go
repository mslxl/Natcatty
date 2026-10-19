// Package connection holds the SSH connection domain vocabulary: host-key
// fingerprints, Known Hosts, and the trust classification rules. It imports
// only the standard library.
//
// The classification is fingerprint-first, matching the original Netcatty
// behavior: any Saved Known Host for the endpoint whose fingerprint matches is
// trusted; a Known Host for the same endpoint and key type with a different
// fingerprint is changed; everything else is unknown. A new algorithm on an
// endpoint that trusts another algorithm is unknown, not changed.
package connection

import (
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"time"
)

// Status is the trust decision for a server's host key.
type Status string

const (
	// StatusUnknown is a host key this Vault has never trusted.
	StatusUnknown Status = "unknown"
	// StatusTrusted is a host key whose fingerprint matches a Known Host.
	StatusTrusted Status = "trusted"
	// StatusChanged is a host key of the same type as a Known Host for the
	// same endpoint, but with a different fingerprint.
	StatusChanged Status = "changed"
)

// Key is one server host key and its classification against the Vault.
type Key struct {
	Hostname string
	Port     int
	KeyType  string
	// Fingerprint is SHA-256 over the key blob, base64 without padding and
	// without the "SHA256:" prefix, as OpenSSH displays it.
	Fingerprint string
	// PublicKey is "keytype base64-blob", or empty when it cannot be parsed.
	PublicKey string

	// Status, KnownHostID, and ExpectedFingerprint are filled by Classify.
	Status              Status
	KnownHostID         string
	ExpectedFingerprint string
}

// KnownHost is a recorded host-key trust decision for one endpoint and key
// type. Hostname matching is case-insensitive and port-exact.
type KnownHost struct {
	ID        string
	Hostname  string
	Port      int
	KeyType   string
	PublicKey string
	// Fingerprint is SHA-256 base64 without padding; it is the ground truth
	// for matching. PublicKey is display and import metadata.
	Fingerprint       string
	DiscoveredAt      time.Time
	LastSeen          time.Time
	ConvertedToHostID string
	Order             int64
}

// normalizeFingerprint removes the display prefix and base64 padding so stored
// and live fingerprints compare equal.
func normalizeFingerprint(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "SHA256:")
	value = strings.TrimPrefix(value, "sha256:")
	return strings.TrimRight(value, "=")
}

// Fingerprint returns the OpenSSH SHA-256 fingerprint of a key blob.
func Fingerprint(blob []byte) string {
	sum := sha256.Sum256(blob)
	return base64.RawStdEncoding.EncodeToString(sum[:])
}

// FingerprintFromPublicKey derives a fingerprint from a stored public key. It
// accepts "SHA256:<value>", "keytype <base64>", or a bare fingerprint.
func FingerprintFromPublicKey(publicKey string) string {
	trimmed := strings.TrimSpace(publicKey)
	if trimmed == "" {
		return ""
	}
	if normalized := normalizeFingerprint(trimmed); strings.HasPrefix(strings.ToLower(trimmed), "sha256:") {
		return normalized
	}
	fields := strings.Fields(trimmed)
	if len(fields) >= 2 && isKeyType(fields[0]) {
		if blob, err := base64.StdEncoding.DecodeString(fields[1]); err == nil {
			return Fingerprint(blob)
		}
	}
	return normalizeFingerprint(trimmed)
}

// PublicKey renders a key blob in the usual "keytype base64" form.
func PublicKey(keyType string, blob []byte) string {
	if keyType == "" {
		return ""
	}
	return keyType + " " + base64.StdEncoding.EncodeToString(blob)
}

// NewKnownHost records a trusted key. The caller owns the id so the
// application can generate an opaque identifier.
func NewKnownHost(id string, key Key, when time.Time) KnownHost {
	return KnownHost{
		ID:           id,
		Hostname:     normalizeHostname(key.Hostname),
		Port:         portOr22(key.Port),
		KeyType:      key.KeyType,
		PublicKey:    key.PublicKey,
		Fingerprint:  normalizeFingerprint(key.Fingerprint),
		DiscoveredAt: when,
		LastSeen:     when,
	}
}

// Classify compares a live host key with the Vault's Known Hosts. The returned
// Key carries the status and, for changed keys, the fingerprint the user
// previously trusted.
func Classify(known []KnownHost, hostname string, port int, keyType, fingerprint string) Key {
	normalizedFingerprint := normalizeFingerprint(fingerprint)
	port = portOr22(port)
	normalizedKeyType := strings.TrimSpace(keyType)

	candidates := make([]KnownHost, 0, len(known))
	for _, record := range known {
		if matchesEndpoint(record, hostname, port) {
			candidates = append(candidates, record)
		}
	}

	result := Key{
		Hostname:    normalizeHostname(hostname),
		Port:        port,
		KeyType:     normalizedKeyType,
		Fingerprint: normalizedFingerprint,
		Status:      StatusUnknown,
	}
	if len(candidates) == 0 {
		return result
	}

	comparable := make([]struct {
		record      KnownHost
		fingerprint string
	}, 0, len(candidates))
	for _, record := range candidates {
		stored := normalizeFingerprint(record.Fingerprint)
		if stored == "" {
			stored = FingerprintFromPublicKey(record.PublicKey)
		}
		if stored != "" {
			comparable = append(comparable, struct {
				record      KnownHost
				fingerprint string
			}{record, stored})
		}
	}

	for _, entry := range comparable {
		if entry.fingerprint == normalizedFingerprint {
			result.Status = StatusTrusted
			result.KnownHostID = entry.record.ID
			return result
		}
	}

	if normalizedKeyType != "" && normalizedKeyType != "unknown" {
		for _, entry := range comparable {
			if entry.record.KeyType == normalizedKeyType {
				result.Status = StatusChanged
				result.KnownHostID = entry.record.ID
				result.ExpectedFingerprint = entry.fingerprint
				return result
			}
		}
	}
	return result
}

// matchesEndpoint reports whether record describes the endpoint. A hashed
// system-known-hosts entry cannot be attributed to a host and never matches.
func matchesEndpoint(record KnownHost, hostname string, port int) bool {
	recordHost := normalizeHostname(record.Hostname)
	if recordHost == "" || recordHost == "(hashed)" {
		return false
	}
	return recordHost == normalizeHostname(hostname) && portOr22(record.Port) == portOr22(port)
}

func normalizeHostname(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func portOr22(port int) int {
	if port == 0 {
		return 22
	}
	return port
}

func isKeyType(value string) bool {
	return strings.HasPrefix(value, "ssh-") ||
		strings.HasPrefix(value, "ecdsa-") ||
		strings.HasPrefix(value, "sk-")
}
