package vault

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

// wireGuardKey renders a deterministic 32-byte WireGuard key in the standard
// base64 form wg-quick writes.
func wireGuardKey(b byte) string {
	return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{b}, 32))
}

func TestNormalizeNetworkProfileDraftAcceptsTailscale(t *testing.T) {
	normalized, err := NormalizeNetworkProfileDraft(NetworkProfileDraft{
		Label: "  tailnet ",
		Kind:  " Tailscale ",
		Tailscale: &TailscaleNetworkDraft{
			Hostname:   " app-node ",
			AuthKey:    " tskey-auth-example ",
			ControlURL: "https://headscale.example.com",
			Ephemeral:  true,
			Tags:       []string{" tag:prod ", "tag:prod", ""},
		},
	})
	if err != nil {
		t.Fatalf("NormalizeNetworkProfileDraft: %v", err)
	}
	if normalized.Label != "tailnet" || normalized.Kind != NetworkKindTailscale {
		t.Fatalf("normalized identity = %q/%q", normalized.Label, normalized.Kind)
	}
	if normalized.Tailscale == nil || normalized.Tailscale.Hostname != "app-node" ||
		normalized.Tailscale.AuthKey != "tskey-auth-example" ||
		normalized.Tailscale.ControlURL != "https://headscale.example.com" ||
		!normalized.Tailscale.Ephemeral {
		t.Fatalf("normalized Tailscale config = %+v", normalized.Tailscale)
	}
	if len(normalized.Tailscale.Tags) != 1 || normalized.Tailscale.Tags[0] != "tag:prod" {
		t.Fatalf("normalized tags = %v", normalized.Tailscale.Tags)
	}
	if normalized.WireGuard != nil {
		t.Fatalf("Tailscale draft kept a WireGuard config: %+v", normalized.WireGuard)
	}
}

func TestNormalizeNetworkProfileDraftAcceptsWireGuard(t *testing.T) {
	privateKey := wireGuardKey(1)
	publicKey := wireGuardKey(2)
	presharedKey := wireGuardKey(3)
	normalized, err := NormalizeNetworkProfileDraft(NetworkProfileDraft{
		Label: "wg",
		Kind:  NetworkKindWireGuard,
		WireGuard: &WireGuardNetworkDraft{
			PrivateKey: privateKey,
			Addresses:  []string{"10.0.0.2/32", "fd00::2"},
			DNS:        []string{"10.0.0.1"},
			MTU:        1280,
			ListenPort: 51820,
			Peers: []WireGuardPeerDraft{{
				ID:                  "peer-1",
				PublicKey:           publicKey,
				PresharedKey:        presharedKey,
				Endpoint:            "vpn.example.com:51820",
				AllowedIPs:          []string{"10.0.0.0/24", "fd00::/64"},
				PersistentKeepalive: 25,
			}},
		},
	})
	if err != nil {
		t.Fatalf("NormalizeNetworkProfileDraft: %v", err)
	}
	config := normalized.WireGuard
	if config == nil || config.PrivateKey != privateKey || config.MTU != 1280 ||
		config.ListenPort != 51820 {
		t.Fatalf("normalized WireGuard config = %+v", config)
	}
	if len(config.Addresses) != 2 || config.Addresses[1] != "fd00::2/128" {
		t.Fatalf("normalized addresses = %v", config.Addresses)
	}
	if len(config.DNS) != 1 || config.DNS[0] != "10.0.0.1" {
		t.Fatalf("normalized DNS = %v", config.DNS)
	}
	peer := config.Peers[0]
	if peer.ID != "peer-1" || peer.PublicKey != publicKey || peer.PresharedKey != presharedKey ||
		peer.Endpoint != "vpn.example.com:51820" || peer.PersistentKeepalive != 25 ||
		len(peer.AllowedIPs) != 2 || peer.AllowedIPs[0] != "10.0.0.0/24" {
		t.Fatalf("normalized peer = %+v", peer)
	}
	if normalized.Tailscale != nil {
		t.Fatalf("WireGuard draft kept a Tailscale config: %+v", normalized.Tailscale)
	}
}

func TestNormalizeNetworkProfileDraftKeepsAddressHostBitsButMasksAllowedIPs(t *testing.T) {
	normalized, err := NormalizeNetworkProfileDraft(NetworkProfileDraft{
		Label: "wg",
		Kind:  NetworkKindWireGuard,
		WireGuard: &WireGuardNetworkDraft{
			PrivateKey: wireGuardKey(1),
			Addresses:  []string{"10.0.0.2/24"},
			Peers: []WireGuardPeerDraft{{
				PublicKey:  wireGuardKey(2),
				AllowedIPs: []string{"10.0.0.9/24"},
			}},
		},
	})
	if err != nil {
		t.Fatalf("NormalizeNetworkProfileDraft: %v", err)
	}
	if got := normalized.WireGuard.Addresses[0]; got != "10.0.0.2/24" {
		t.Fatalf("address = %q, want the host bits kept for display", got)
	}
	if got := normalized.WireGuard.Peers[0].AllowedIPs[0]; got != "10.0.0.0/24" {
		t.Fatalf("AllowedIPs = %q, want a masked prefix", got)
	}
}

func TestNormalizeNetworkProfileDraftDefaultsMTUAndCanonicalizesHexKeys(t *testing.T) {
	normalized, err := NormalizeNetworkProfileDraft(NetworkProfileDraft{
		Label: "wg",
		Kind:  NetworkKindWireGuard,
		WireGuard: &WireGuardNetworkDraft{
			PrivateKey: hex.EncodeToString(bytes.Repeat([]byte{4}, 32)),
			Addresses:  []string{"10.0.0.2"},
			Peers: []WireGuardPeerDraft{{
				PublicKey:  wireGuardKey(5),
				AllowedIPs: []string{"0.0.0.0/0"},
			}},
		},
	})
	if err != nil {
		t.Fatalf("NormalizeNetworkProfileDraft: %v", err)
	}
	if normalized.WireGuard.MTU != DefaultWireGuardMTU {
		t.Fatalf("MTU = %d, want the default %d", normalized.WireGuard.MTU, DefaultWireGuardMTU)
	}
	if normalized.WireGuard.PrivateKey != wireGuardKey(4) {
		t.Fatalf("private key = %q, want canonical base64", normalized.WireGuard.PrivateKey)
	}
	if normalized.WireGuard.Addresses[0] != "10.0.0.2/32" {
		t.Fatalf("address = %q, want a /32 prefix", normalized.WireGuard.Addresses[0])
	}
}

func TestNormalizeNetworkProfileDraftRejectsInvalidProfiles(t *testing.T) {
	validPeer := WireGuardPeerDraft{PublicKey: wireGuardKey(2), AllowedIPs: []string{"10.0.0.0/24"}}
	wireGuard := func(mutate func(*WireGuardNetworkDraft)) *WireGuardNetworkDraft {
		draft := &WireGuardNetworkDraft{
			PrivateKey: wireGuardKey(1),
			Addresses:  []string{"10.0.0.2/32"},
			Peers:      []WireGuardPeerDraft{validPeer},
		}
		if mutate != nil {
			mutate(draft)
		}
		return draft
	}
	cases := []struct {
		name  string
		draft NetworkProfileDraft
		field Field
		rule  Rule
	}{
		{"missing label", NetworkProfileDraft{Kind: NetworkKindTailscale, Tailscale: &TailscaleNetworkDraft{}}, FieldLabel, RuleRequired},
		{"unknown kind", NetworkProfileDraft{Label: "x", Kind: "openvpn"}, FieldNetworkProfile, RuleUnsupported},
		{"missing config", NetworkProfileDraft{Label: "x", Kind: NetworkKindTailscale}, FieldNetworkProfile, RuleRequired},
		{"mismatched config", NetworkProfileDraft{Label: "x", Kind: NetworkKindTailscale,
			Tailscale: &TailscaleNetworkDraft{}, WireGuard: wireGuard(nil)}, FieldNetworkProfile, RuleInvalid},
		{"bad control url", NetworkProfileDraft{Label: "x", Kind: NetworkKindTailscale,
			Tailscale: &TailscaleNetworkDraft{ControlURL: "ftp://example.com"}}, FieldNetworkProfile, RuleInvalid},
		{"no addresses", NetworkProfileDraft{Label: "x", Kind: NetworkKindWireGuard,
			WireGuard: wireGuard(func(w *WireGuardNetworkDraft) { w.Addresses = nil })}, FieldNetworkProfile, RuleRequired},
		{"bad address", NetworkProfileDraft{Label: "x", Kind: NetworkKindWireGuard,
			WireGuard: wireGuard(func(w *WireGuardNetworkDraft) { w.Addresses = []string{"not-an-ip"} })}, FieldNetworkProfile, RuleInvalid},
		{"no peers", NetworkProfileDraft{Label: "x", Kind: NetworkKindWireGuard,
			WireGuard: wireGuard(func(w *WireGuardNetworkDraft) { w.Peers = nil })}, FieldNetworkProfile, RuleRequired},
		{"short private key", NetworkProfileDraft{Label: "x", Kind: NetworkKindWireGuard,
			WireGuard: wireGuard(func(w *WireGuardNetworkDraft) { w.PrivateKey = "dG9vLXNob3J0" })}, FieldNetworkProfile, RuleInvalid},
		{"short public key", NetworkProfileDraft{Label: "x", Kind: NetworkKindWireGuard,
			WireGuard: wireGuard(func(w *WireGuardNetworkDraft) { w.Peers[0].PublicKey = "dG9vLXNob3J0" })}, FieldNetworkProfile, RuleInvalid},
		{"short preshared key", NetworkProfileDraft{Label: "x", Kind: NetworkKindWireGuard,
			WireGuard: wireGuard(func(w *WireGuardNetworkDraft) { w.Peers[0].PresharedKey = "dG9vLXNob3J0" })}, FieldNetworkProfile, RuleInvalid},
		{"duplicate peer", NetworkProfileDraft{Label: "x", Kind: NetworkKindWireGuard,
			WireGuard: wireGuard(func(w *WireGuardNetworkDraft) {
				w.Peers = append(w.Peers, WireGuardPeerDraft{PublicKey: wireGuardKey(2)})
			})}, FieldNetworkProfile, RuleInvalid},
		{"bad endpoint", NetworkProfileDraft{Label: "x", Kind: NetworkKindWireGuard,
			WireGuard: wireGuard(func(w *WireGuardNetworkDraft) { w.Peers[0].Endpoint = "example.com" })}, FieldNetworkProfile, RuleInvalid},
		{"endpoint bad port", NetworkProfileDraft{Label: "x", Kind: NetworkKindWireGuard,
			WireGuard: wireGuard(func(w *WireGuardNetworkDraft) { w.Peers[0].Endpoint = "example.com:70000" })}, FieldNetworkProfile, RuleInvalid},
		{"bad allowed ip", NetworkProfileDraft{Label: "x", Kind: NetworkKindWireGuard,
			WireGuard: wireGuard(func(w *WireGuardNetworkDraft) { w.Peers[0].AllowedIPs = []string{"10.0.0.0"} })}, FieldNetworkProfile, RuleInvalid},
		{"dns is cidr", NetworkProfileDraft{Label: "x", Kind: NetworkKindWireGuard,
			WireGuard: wireGuard(func(w *WireGuardNetworkDraft) { w.DNS = []string{"10.0.0.0/24"} })}, FieldNetworkProfile, RuleInvalid},
		{"mtu too small", NetworkProfileDraft{Label: "x", Kind: NetworkKindWireGuard,
			WireGuard: wireGuard(func(w *WireGuardNetworkDraft) { w.MTU = 575 })}, FieldNetworkProfile, RuleOutOfRange},
		{"mtu too large", NetworkProfileDraft{Label: "x", Kind: NetworkKindWireGuard,
			WireGuard: wireGuard(func(w *WireGuardNetworkDraft) { w.MTU = 65536 })}, FieldNetworkProfile, RuleOutOfRange},
		{"listen port too large", NetworkProfileDraft{Label: "x", Kind: NetworkKindWireGuard,
			WireGuard: wireGuard(func(w *WireGuardNetworkDraft) { w.ListenPort = 65536 })}, FieldNetworkProfile, RuleOutOfRange},
		{"listen port negative", NetworkProfileDraft{Label: "x", Kind: NetworkKindWireGuard,
			WireGuard: wireGuard(func(w *WireGuardNetworkDraft) { w.ListenPort = -1 })}, FieldNetworkProfile, RuleOutOfRange},
		{"keepalive out of range", NetworkProfileDraft{Label: "x", Kind: NetworkKindWireGuard,
			WireGuard: wireGuard(func(w *WireGuardNetworkDraft) { w.Peers[0].PersistentKeepalive = 65536 })}, FieldNetworkProfile, RuleOutOfRange},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := NormalizeNetworkProfileDraft(test.draft)
			var invalid *ValidationError
			if !groupAsValidation(err, &invalid) {
				t.Fatalf("error = %v, want ValidationError", err)
			}
			if invalid.Field != test.field || invalid.Rule != test.rule {
				t.Fatalf("error = %+v, want %s/%s", invalid, test.field, test.rule)
			}
		})
	}
}

func TestNormalizeNetworkProfileDraftControlURLPolicy(t *testing.T) {
	cases := []struct {
		name      string
		url       string
		wantError bool
		wantWarn  bool
	}{
		{"https public", "https://controlplane.tailscale.com", false, false},
		{"https public with port and path", "https://headscale.example.com:8443/api", false, false},
		{"http loopback ipv4", "http://127.0.0.1:8080", false, true},
		{"http loopback ipv6", "http://[::1]:8080", false, true},
		{"http localhost", "http://localhost:8080", false, true},
		{"http localhost subdomain", "http://headscale.localhost:8080", false, true},
		{"http private ipv4 192.168", "http://192.168.1.10:8080", false, true},
		{"http private ipv4 10", "http://10.20.30.40", false, true},
		{"http private ipv4 172.16", "http://172.16.5.4:8080", false, true},
		{"http private ipv6 ula", "http://[fd7a:115c:a1e0::1]:8080", false, true},
		{"http link-local ipv4", "http://169.254.10.20:8080", false, true},
		{"http link-local ipv6", "http://[fe80::1]:8080", false, true},
		{"http public hostname", "http://headscale.example.com:8080", true, false},
		{"http public ipv4", "http://203.0.113.10:8080", true, false},
		{"http public ipv6", "http://[2001:db8::1]:8080", true, false},
		{"invalid scheme", "ftp://example.com", true, false},
		{"missing scheme", "headscale.example.com", true, false},
		{"empty host", "https://", true, false},
		{"malformed url", "https://exa\x00mple.com", true, false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			draft := NetworkProfileDraft{
				Label:     "tailnet",
				Kind:      NetworkKindTailscale,
				Tailscale: &TailscaleNetworkDraft{ControlURL: test.url},
			}
			_, err := NormalizeNetworkProfileDraft(draft)
			if test.wantError {
				var invalid *ValidationError
				if !groupAsValidation(err, &invalid) {
					t.Fatalf("error = %v, want a ValidationError", err)
				}
				if invalid.Field != FieldNetworkProfile || invalid.Rule != RuleInvalid {
					t.Fatalf("error = %+v, want %s/%s", invalid, FieldNetworkProfile, RuleInvalid)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeNetworkProfileDraft(%q): %v", test.url, err)
			}
			warnings := NetworkProfileWarnings(NetworkProfileDraft{
				Kind:      NetworkKindTailscale,
				Tailscale: &TailscaleNetworkDraft{ControlURL: test.url},
			})
			if test.wantWarn {
				if len(warnings) != 1 || warnings[0] != WarningControlURLInsecure {
					t.Fatalf("warnings = %v, want the insecure ControlURL warning", warnings)
				}
			} else if len(warnings) != 0 {
				t.Fatalf("warnings = %v, want none", warnings)
			}
		})
	}
}

func TestNetworkProfileWarningsReportAnInsecureControlURL(t *testing.T) {
	warnings := NetworkProfileWarnings(NetworkProfileDraft{
		Kind:      NetworkKindTailscale,
		Tailscale: &TailscaleNetworkDraft{ControlURL: "http://127.0.0.1:8080"},
	})
	if len(warnings) != 1 || warnings[0] != WarningControlURLInsecure {
		t.Fatalf("warnings = %v, want the insecure ControlURL warning", warnings)
	}
	if warnings := NetworkProfileWarnings(NetworkProfileDraft{
		Kind:      NetworkKindTailscale,
		Tailscale: &TailscaleNetworkDraft{ControlURL: "https://headscale.example.com"},
	}); len(warnings) != 0 {
		t.Fatalf("https produced warnings: %v", warnings)
	}
	if warnings := NetworkProfileWarnings(NetworkProfileDraft{
		Kind:      NetworkKindTailscale,
		Tailscale: &TailscaleNetworkDraft{ControlURL: "http://headscale.example.com"},
	}); len(warnings) != 0 {
		t.Fatalf("remote http produced warnings: %v", warnings)
	}
	if warnings := NetworkProfileWarnings(NetworkProfileDraft{
		Kind:      NetworkKindWireGuard,
		WireGuard: &WireGuardNetworkDraft{},
	}); len(warnings) != 1 || warnings[0] != WarningWireGuardDNSUnavailable {
		t.Fatalf("a WireGuard draft without DNS produced %v, want the tunnel-DNS warning", warnings)
	}
	if warnings := NetworkProfileWarnings(NetworkProfileDraft{
		Kind:      NetworkKindWireGuard,
		WireGuard: &WireGuardNetworkDraft{DNS: []string{"10.0.0.1"}},
	}); len(warnings) != 0 {
		t.Fatalf("a WireGuard draft with DNS produced warnings: %v", warnings)
	}
}

func TestNetworkProfileJSONCarriesOnlyPresenceMarkers(t *testing.T) {
	privateKey := wireGuardKey(1)
	profile := NetworkProfile{
		ID: "np-1", Label: "wg", Kind: NetworkKindWireGuard,
		WireGuard: &WireGuardNetwork{
			HasPrivateKey: true,
			Addresses:     []string{"10.0.0.2/32"},
			Peers: []WireGuardPeer{{
				ID: "peer-1", PublicKey: wireGuardKey(2), HasPresharedKey: true,
			}},
		},
	}
	data, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), privateKey) {
		t.Fatalf("profile JSON contains a private key: %s", data)
	}
	for _, marker := range []string{`"hasPrivateKey":true`, `"hasPresharedKey":true`} {
		if !strings.Contains(string(data), marker) {
			t.Fatalf("profile JSON %s is missing %s", data, marker)
		}
	}
	if strings.Contains(string(data), "privateKey\":\"") || strings.Contains(string(data), "presharedKey\":\"") {
		t.Fatalf("profile JSON carries a secret field: %s", data)
	}
}
