package vault

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	domainvault "github.com/mslxl/natcatty/domain/vault"
)

func newNetworkExportService(networks *fakeNetworkProfiles, secrets *fakeSecrets) *Service {
	var profiles, peers int
	return NewService(&fakeHosts{},
		WithSecrets(secrets),
		WithNetworkProfiles(networks),
		WithNetworkProfileIDGenerator(func() string {
			profiles++
			return "imported-network-" + strconv.Itoa(profiles)
		}),
		WithNetworkPeerIDGenerator(func() string {
			peers++
			return "imported-peer-" + strconv.Itoa(peers)
		}),
		WithClock(fixedFamilyTime),
	)
}

func TestExportJSONCarriesNetworkProfilesWithoutSecrets(t *testing.T) {
	secrets := newFakeSecrets()
	networks := &fakeNetworkProfiles{items: []domainvault.NetworkProfile{
		{
			ID: "network-1", Label: "wg", Kind: domainvault.NetworkKindWireGuard,
			WireGuard: &domainvault.WireGuardNetwork{
				Addresses: []string{"10.0.0.2/32"}, DNS: []string{"10.0.0.1"},
				MTU: 1420, ListenPort: 51820, HasPrivateKey: true,
				Peers: []domainvault.WireGuardPeer{{
					ID: "peer-1", PublicKey: testWireGuardKey(2),
					Endpoint: "vpn.example.com:51820", AllowedIPs: []string{"10.0.0.0/24"},
					HasPresharedKey: true,
				}},
			},
			OrderKey: 1000, CreatedAt: fixedFamilyTime(), UpdatedAt: fixedFamilyTime(),
		},
		{
			ID: "network-2", Label: "tailnet", Kind: domainvault.NetworkKindTailscale,
			Tailscale: &domainvault.TailscaleNetwork{
				Hostname: "app-node", ControlURL: "https://headscale.example.com",
				HasAuthKey: true,
			},
			OrderKey: 2000, CreatedAt: fixedFamilyTime(), UpdatedAt: fixedFamilyTime(),
		},
	}}
	secrets.values[NetworkProfileSecretRef("network-1", FieldNetworkPrivateKey)] = testWireGuardKey(1)
	secrets.values[NetworkPeerSecretRef("peer-1", FieldNetworkPresharedKey)] = testWireGuardKey(3)
	secrets.values[NetworkProfileSecretRef("network-2", FieldNetworkAuthKey)] = "tskey-auth-secret"
	service := newNetworkExportService(networks, secrets)

	for _, includeSecrets := range []bool{false, true} {
		exported, err := service.ExportVault(context.Background(), ExportOptions{
			Format: ExportJSON, IncludeSecrets: includeSecrets, ExportedAt: fixedFamilyTime(),
		})
		if err != nil {
			t.Fatalf("ExportVault(includeSecrets=%v): %v", includeSecrets, err)
		}
		content := string(exported.Content)
		for _, secret := range []string{
			testWireGuardKey(1), testWireGuardKey(3), "tskey-auth-secret",
		} {
			if strings.Contains(content, secret) {
				t.Fatalf("export contains a Network Profile secret: %s", content)
			}
		}
		var document struct {
			NetworkProfiles []domainvault.NetworkProfile `json:"networkProfiles"`
		}
		if err := json.Unmarshal(exported.Content, &document); err != nil {
			t.Fatalf("decode export: %v", err)
		}
		if len(document.NetworkProfiles) != 2 {
			t.Fatalf("networkProfiles = %d, want 2", len(document.NetworkProfiles))
		}
		if document.NetworkProfiles[0].ID != "network-1" ||
			document.NetworkProfiles[1].ID != "network-2" {
			t.Fatalf("networkProfiles order = %+v", document.NetworkProfiles)
		}
		if !document.NetworkProfiles[0].WireGuard.HasPrivateKey ||
			!document.NetworkProfiles[0].WireGuard.Peers[0].HasPresharedKey ||
			!document.NetworkProfiles[1].Tailscale.HasAuthKey {
			t.Fatalf("presence markers were not exported: %+v", document.NetworkProfiles)
		}
		if document.NetworkProfiles[0].WireGuard.ListenPort != 51820 {
			t.Fatalf("exported ListenPort = %d, want 51820",
				document.NetworkProfiles[0].WireGuard.ListenPort)
		}
		if !includeSecrets && exported.OmittedSecrets != 3 {
			t.Fatalf("OmittedSecrets = %d, want the three stored Network Profile secrets",
				exported.OmittedSecrets)
		}
	}
}

func TestImportRestoresNetworkProfilesFromTheJSONFamily(t *testing.T) {
	document := `{
  "kind": "netcatty.vault",
  "version": 1,
  "hosts": [],
  "networkProfiles": [
    {
      "id": "source-wg",
      "label": "wg",
      "kind": "wireguard",
      "wireguard": {
        "addresses": ["10.0.0.2/32"],
        "dns": ["10.0.0.1"],
        "mtu": 1280,
        "listenPort": 51821,
        "hasPrivateKey": true,
        "peers": [{
          "id": "source-peer",
          "publicKey": "` + testWireGuardKey(2) + `",
          "endpoint": "vpn.example.com:51820",
          "allowedIps": ["10.0.0.0/24"],
          "persistentKeepalive": 25,
          "hasPresharedKey": true
        }]
      },
      "orderKey": 1000,
      "createdAt": "2024-01-01T00:00:00Z",
      "updatedAt": "2024-01-01T00:00:00Z"
    },
    {
      "id": "source-ts",
      "label": "tailnet",
      "kind": "tailscale",
      "tailscale": {
        "hostname": "app-node",
        "controlUrl": "https://headscale.example.com",
        "ephemeral": true,
        "tags": ["tag:prod"],
        "hasAuthKey": true
      },
      "orderKey": 2000,
      "createdAt": "2024-01-01T00:00:00Z",
      "updatedAt": "2024-01-01T00:00:00Z"
    }
  ]
}`

	networks := &fakeNetworkProfiles{}
	destination := newNetworkExportService(networks, newFakeSecrets())
	preview, err := destination.PreviewImport(context.Background(), document,
		PreviewImportOptions{FileName: "netcatty-vault.json"})
	if err != nil {
		t.Fatalf("PreviewImport: %v", err)
	}
	if preview.FamilyCounts.NetworkProfiles != 2 {
		t.Fatalf("FamilyCounts.NetworkProfiles = %d, want 2", preview.FamilyCounts.NetworkProfiles)
	}
	if _, err := destination.CommitImport(context.Background(), preview, ImportCommitOptions{}); err != nil {
		t.Fatalf("CommitImport: %v", err)
	}
	if len(networks.items) != 2 {
		t.Fatalf("imported profiles = %+v", networks.items)
	}
	wireguard := networks.items[0]
	if wireguard.Kind != domainvault.NetworkKindWireGuard || wireguard.Label != "wg" ||
		wireguard.WireGuard == nil || wireguard.WireGuard.MTU != 1280 ||
		wireguard.WireGuard.ListenPort != 51821 ||
		len(wireguard.WireGuard.Peers) != 1 ||
		wireguard.WireGuard.Peers[0].PublicKey != testWireGuardKey(2) ||
		wireguard.WireGuard.Peers[0].Endpoint != "vpn.example.com:51820" {
		t.Fatalf("imported WireGuard profile = %+v", wireguard)
	}
	// The JSON family never carries key material, so an imported profile must
	// not claim a stored key it does not have.
	if wireguard.WireGuard.HasPrivateKey || wireguard.WireGuard.Peers[0].HasPresharedKey {
		t.Fatalf("import claimed a key that was not in the document: %+v", wireguard)
	}
	tailscale := networks.items[1]
	if tailscale.Kind != domainvault.NetworkKindTailscale || tailscale.Tailscale == nil ||
		tailscale.Tailscale.ControlURL != "https://headscale.example.com" ||
		!tailscale.Tailscale.Ephemeral || tailscale.Tailscale.HasAuthKey {
		t.Fatalf("imported Tailscale profile = %+v", tailscale)
	}
}
