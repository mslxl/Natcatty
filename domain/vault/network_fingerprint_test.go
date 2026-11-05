package vault

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

func fingerprintProfile() NetworkProfile {
	return NetworkProfile{
		ID:        "profile-a",
		Label:     "Office",
		Kind:      NetworkKindWireGuard,
		OrderKey:  1000,
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC),
		WireGuard: &WireGuardNetwork{
			Addresses:     []string{"10.10.0.2/32", "fd00::2/128"},
			DNS:           []string{"10.10.0.1"},
			MTU:           1420,
			ListenPort:    51820,
			HasPrivateKey: true,
			Peers: []WireGuardPeer{{
				ID:                  "peer-a",
				PublicKey:           "peer-public-key",
				Endpoint:            "vpn.example.com:51820",
				AllowedIPs:          []string{"10.10.0.0/24", "192.168.0.0/16"},
				PersistentKeepalive: 25,
				HasPresharedKey:     true,
			}},
		},
	}
}

var fingerprintPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

func TestNetworkProfileFingerprintIsStable(t *testing.T) {
	first := NetworkProfileFingerprint(fingerprintProfile())
	second := NetworkProfileFingerprint(fingerprintProfile())
	if first != second {
		t.Fatalf("fingerprint is not stable: %q != %q", first, second)
	}
	if !fingerprintPattern.MatchString(first) {
		t.Fatalf("fingerprint %q is not a lowercase hex digest", first)
	}
}

func TestNetworkProfileFingerprintChangesWithConfiguration(t *testing.T) {
	base := NetworkProfileFingerprint(fingerprintProfile())
	tests := map[string]func(*NetworkProfile){
		"kind": func(profile *NetworkProfile) {
			profile.Kind = NetworkKindTailscale
			profile.Tailscale = &TailscaleNetwork{Hostname: "node", HasAuthKey: true}
		},
		"address": func(profile *NetworkProfile) {
			profile.WireGuard.Addresses = append(profile.WireGuard.Addresses, "10.10.0.3/32")
		},
		"dns": func(profile *NetworkProfile) {
			profile.WireGuard.DNS = []string{"1.1.1.1"}
		},
		"mtu": func(profile *NetworkProfile) {
			profile.WireGuard.MTU = 1280
		},
		"listen port": func(profile *NetworkProfile) {
			profile.WireGuard.ListenPort = 51821
		},
		"peer endpoint": func(profile *NetworkProfile) {
			profile.WireGuard.Peers[0].Endpoint = "other.example.com:51820"
		},
		"peer allowed ips": func(profile *NetworkProfile) {
			profile.WireGuard.Peers[0].AllowedIPs = []string{"0.0.0.0/0"}
		},
		"peer keepalive": func(profile *NetworkProfile) {
			profile.WireGuard.Peers[0].PersistentKeepalive = 0
		},
		"private key presence": func(profile *NetworkProfile) {
			profile.WireGuard.HasPrivateKey = false
		},
		"preshared key presence": func(profile *NetworkProfile) {
			profile.WireGuard.Peers[0].HasPresharedKey = false
		},
		"tailscale auth key presence": func(profile *NetworkProfile) {
			profile.Kind = NetworkKindTailscale
			profile.WireGuard = nil
			profile.Tailscale = &TailscaleNetwork{Hostname: "node", HasAuthKey: true}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			profile := fingerprintProfile()
			mutate(&profile)
			if got := NetworkProfileFingerprint(profile); got == base {
				t.Fatalf("a %s change did not change the fingerprint", name)
			}
		})
	}
}

func TestNetworkProfileFingerprintChangesWithTheSecretMaterialVersion(t *testing.T) {
	profile := fingerprintProfile()
	base := NetworkProfileFingerprint(profile)
	// The Vault bumps UpdatedAt on every edit, including key rotation, so a
	// rotated secret changes the digest without the secret ever entering it.
	profile.UpdatedAt = profile.UpdatedAt.Add(time.Second)
	if got := NetworkProfileFingerprint(profile); got == base {
		t.Fatal("a secret-material version change did not change the fingerprint")
	}
}

func TestNetworkProfileFingerprintIgnoresBookkeeping(t *testing.T) {
	profile := fingerprintProfile()
	base := NetworkProfileFingerprint(profile)

	renamed := fingerprintProfile()
	renamed.Label = "Renamed"
	renamed.OrderKey = 5000
	renamed.CreatedAt = renamed.CreatedAt.Add(-48 * time.Hour)
	if got := NetworkProfileFingerprint(renamed); got != base {
		t.Fatal("a label, order, or creation change invalidated the fingerprint")
	}
}

func TestNetworkProfileFingerprintContainsNoPlaintext(t *testing.T) {
	profile := fingerprintProfile()
	digest := NetworkProfileFingerprint(profile)
	for _, plaintext := range []string{
		profile.Label,
		profile.WireGuard.Addresses[0],
		profile.WireGuard.Peers[0].PublicKey,
		profile.WireGuard.Peers[0].Endpoint,
		"peer-public-key",
		"vpn.example.com",
	} {
		if plaintext == "" {
			continue
		}
		if strings.Contains(digest, plaintext) {
			t.Fatalf("fingerprint %q contains plaintext %q", digest, plaintext)
		}
	}
	// The profile record deliberately carries no secret field at all; the
	// Has* markers are the only secret-related input, and they still only
	// move the digest instead of appearing in it.
	if strings.Contains(digest, "hasPrivateKey") || strings.Contains(digest, "preshared") {
		t.Fatalf("fingerprint %q embeds secret-presence field names", digest)
	}
}
