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

	exported, err := service.ExportVault(context.Background(), ExportOptions{
		Format: ExportJSON, ExportedAt: fixedFamilyTime(),
	})
	if err != nil {
		t.Fatalf("ExportVault: %v", err)
	}
	content := string(exported.Content)
	for _, secret := range []string{
		testWireGuardKey(1), testWireGuardKey(3), "tskey-auth-secret",
	} {
		if strings.Contains(content, secret) {
			t.Fatalf("an ordinary export contains a Network Profile secret: %s", content)
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
	if exported.OmittedSecrets != 3 {
		t.Fatalf("OmittedSecrets = %d, want the three stored Network Profile secrets",
			exported.OmittedSecrets)
	}
}

// TestDestructiveExportWritesNetworkSecrets proves IncludeSecrets is the only
// switch that puts credential plaintext in the portable document, and that the
// presence markers stay alongside it.
func TestDestructiveExportWritesNetworkSecrets(t *testing.T) {
	secrets := newFakeSecrets()
	networks := &fakeNetworkProfiles{items: []domainvault.NetworkProfile{
		{
			ID: "network-1", Label: "wg", Kind: domainvault.NetworkKindWireGuard,
			WireGuard: &domainvault.WireGuardNetwork{
				Addresses: []string{"10.0.0.2/32"}, HasPrivateKey: true,
				Peers: []domainvault.WireGuardPeer{{
					ID: "peer-1", PublicKey: testWireGuardKey(2),
					HasPresharedKey: true,
				}},
			},
			OrderKey: 1000, CreatedAt: fixedFamilyTime(), UpdatedAt: fixedFamilyTime(),
		},
		{
			ID: "network-2", Label: "tailnet", Kind: domainvault.NetworkKindTailscale,
			Tailscale: &domainvault.TailscaleNetwork{Hostname: "app-node", HasAuthKey: true},
			OrderKey:  2000, CreatedAt: fixedFamilyTime(), UpdatedAt: fixedFamilyTime(),
		},
	}}
	privateKey := testWireGuardKey(1)
	presharedKey := testWireGuardKey(3)
	const authKey = "tskey-auth-secret"
	secrets.values[NetworkProfileSecretRef("network-1", FieldNetworkPrivateKey)] = privateKey
	secrets.values[NetworkPeerSecretRef("peer-1", FieldNetworkPresharedKey)] = presharedKey
	secrets.values[NetworkProfileSecretRef("network-2", FieldNetworkAuthKey)] = authKey
	service := newNetworkExportService(networks, secrets)

	exported, err := service.ExportVault(context.Background(), ExportOptions{
		Format: ExportJSON, IncludeSecrets: true, ExportedAt: fixedFamilyTime(),
	})
	if err != nil {
		t.Fatalf("ExportVault: %v", err)
	}
	content := string(exported.Content)
	for _, secret := range []string{privateKey, presharedKey, authKey} {
		if !strings.Contains(content, secret) {
			t.Fatalf("the destructive export does not contain a Network Profile secret: %s",
				content)
		}
	}
	if exported.OmittedSecrets != 0 || exported.UnreadableSecrets != 0 {
		t.Fatalf("destructive export counts = omitted %d, unreadable %d; want 0/0",
			exported.OmittedSecrets, exported.UnreadableSecrets)
	}
	var document struct {
		NetworkProfiles []struct {
			ID        string `json:"id"`
			Tailscale *struct {
				HasAuthKey bool   `json:"hasAuthKey"`
				AuthKey    string `json:"authKey"`
			} `json:"tailscale"`
			WireGuard *struct {
				HasPrivateKey bool   `json:"hasPrivateKey"`
				PrivateKey    string `json:"privateKey"`
				Peers         []struct {
					HasPresharedKey bool   `json:"hasPresharedKey"`
					PresharedKey    string `json:"presharedKey"`
				} `json:"peers"`
			} `json:"wireguard"`
		} `json:"networkProfiles"`
	}
	if err := json.Unmarshal(exported.Content, &document); err != nil {
		t.Fatalf("decode export: %v", err)
	}
	if len(document.NetworkProfiles) != 2 {
		t.Fatalf("networkProfiles = %+v", document.NetworkProfiles)
	}
	wireGuard := document.NetworkProfiles[0]
	if wireGuard.WireGuard == nil || !wireGuard.WireGuard.HasPrivateKey ||
		wireGuard.WireGuard.PrivateKey != privateKey ||
		len(wireGuard.WireGuard.Peers) != 1 ||
		!wireGuard.WireGuard.Peers[0].HasPresharedKey ||
		wireGuard.WireGuard.Peers[0].PresharedKey != presharedKey {
		t.Fatalf("destructive WireGuard document = %+v", wireGuard)
	}
	tailscale := document.NetworkProfiles[1]
	if tailscale.Tailscale == nil || !tailscale.Tailscale.HasAuthKey ||
		tailscale.Tailscale.AuthKey != authKey {
		t.Fatalf("destructive Tailscale document = %+v", tailscale)
	}
}

// TestDestructiveExportCountsAMarkedButMissingNetworkSecretAsUnreadable
// keeps the Network family consistent with Host, Identity, and Proxy exports:
// a marker promises a credential, so a destructive backup that cannot produce
// it reports an unreadable secret rather than silently omitting it.
func TestDestructiveExportCountsAMarkedButMissingNetworkSecretAsUnreadable(t *testing.T) {
	secrets := newFakeSecrets()
	networks := &fakeNetworkProfiles{items: []domainvault.NetworkProfile{
		{
			ID: "network-1", Label: "wg", Kind: domainvault.NetworkKindWireGuard,
			WireGuard: &domainvault.WireGuardNetwork{
				HasPrivateKey: true,
				Peers: []domainvault.WireGuardPeer{{
					ID: "peer-1", PublicKey: testWireGuardKey(2), HasPresharedKey: true,
				}},
			},
		},
		{
			ID: "network-2", Label: "tailnet", Kind: domainvault.NetworkKindTailscale,
			Tailscale: &domainvault.TailscaleNetwork{HasAuthKey: true},
		},
	}}
	service := newNetworkExportService(networks, secrets)

	exported, err := service.ExportVault(context.Background(), ExportOptions{
		Format: ExportJSON, IncludeSecrets: true, ExportedAt: fixedFamilyTime(),
	})
	if err != nil {
		t.Fatalf("ExportVault: %v", err)
	}
	if exported.UnreadableSecrets != 3 {
		t.Fatalf("UnreadableSecrets = %d, want the three marked but missing secrets",
			exported.UnreadableSecrets)
	}
	for _, field := range []string{`"authKey"`, `"privateKey"`, `"presharedKey"`} {
		if strings.Contains(string(exported.Content), field) {
			t.Fatalf("a missing secret was written as %s: %s", field, exported.Content)
		}
	}
}

// TestNetworkProfileReferenceRoundTripsThroughJSONExportAndImport proves the
// Host's soft reference travels in the document and is remapped onto the
// import-created profile instead of keeping the source ID.
func TestNetworkProfileReferenceRoundTripsThroughJSONExportAndImport(t *testing.T) {
	ctx := context.Background()
	sourceHosts := &fakeHosts{hosts: []domainvault.Host{{
		ID: "source-host", Label: "web", Hostname: "10.0.0.2", Port: 22,
		Username: "root", Protocol: domainvault.ProtocolSSH,
	}}}
	sourceNetworks := &fakeNetworkProfiles{}
	var sourceProfiles int
	source := NewService(sourceHosts,
		WithSecrets(newFakeSecrets()),
		WithNetworkProfiles(sourceNetworks),
		WithNetworkProfileIDGenerator(func() string {
			sourceProfiles++
			return "network-source-" + strconv.Itoa(sourceProfiles)
		}),
		WithNetworkPeerIDGenerator(func() string { return "peer-source" }),
		WithClock(fixedFamilyTime),
	)
	profile, err := source.CreateNetworkProfile(ctx,
		wireGuardNetworkDraft(testWireGuardKey(1), testWireGuardKey(2), testWireGuardKey(3)))
	if err != nil {
		t.Fatalf("CreateNetworkProfile: %v", err)
	}
	sourceHosts.hosts[0].NetworkProfileID = profile.ID

	exported, err := source.ExportVault(ctx, ExportOptions{
		Format: ExportJSON, ExportedAt: fixedFamilyTime(),
	})
	if err != nil {
		t.Fatalf("ExportVault: %v", err)
	}
	if !strings.Contains(string(exported.Content), `"networkProfileId": "`+profile.ID+`"`) {
		t.Fatalf("the export does not carry the Host's Network Profile reference: %s",
			exported.Content)
	}

	destinationHosts := &fakeHosts{}
	destinationGroups := &fakeGroups{}
	destinationNetworks := &fakeNetworkProfiles{}
	destination := NewService(destinationHosts,
		WithGroups(destinationGroups),
		WithSecrets(newFakeSecrets()),
		WithNetworkProfiles(destinationNetworks),
		WithImportStore(&fakeImportStore{hosts: destinationHosts, groups: destinationGroups}),
		WithNetworkProfileIDGenerator(func() string { return "network-destination" }),
		WithClock(fixedFamilyTime),
	)
	preview, err := destination.PreviewImport(ctx, string(exported.Content),
		PreviewImportOptions{FileName: "netcatty-vault.json"})
	if err != nil {
		t.Fatalf("PreviewImport: %v", err)
	}
	if _, err := destination.CommitImport(ctx, preview, ImportCommitOptions{}); err != nil {
		t.Fatalf("CommitImport: %v", err)
	}
	if len(destinationNetworks.items) != 1 || len(destinationHosts.hosts) != 1 {
		t.Fatalf("imported profiles = %+v, hosts = %+v",
			destinationNetworks.items, destinationHosts.hosts)
	}
	created := destinationNetworks.items[0]
	host := destinationHosts.hosts[0]
	if host.NetworkProfileID != created.ID {
		t.Fatalf("Host NetworkProfileID = %q, want the remapped %q",
			host.NetworkProfileID, created.ID)
	}
	if host.NetworkProfileID == profile.ID {
		t.Fatal("the Host kept the source Network Profile ID")
	}
	if created.Label != profile.Label || created.Kind != profile.Kind {
		t.Fatalf("imported profile = %+v, want the exported identity", created)
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

// TestImportRestoresDestructiveExportSecretsAndMarkers proves a document that
// carries credential plaintext (a destructive export) restores the credentials
// into the destination field-secret store and advertises them with true Has*
// markers. The old-format test above proves the opposite case: no plaintext
// means no marker, so a record never claims a key the destination lacks.
func TestImportRestoresDestructiveExportSecretsAndMarkers(t *testing.T) {
	privateKey := testWireGuardKey(1)
	publicKey := testWireGuardKey(2)
	presharedKey := testWireGuardKey(3)
	const authKey = "wbmkw-qpxu-tawura"
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
        "mtu": 1280,
        "hasPrivateKey": true,
        "privateKey": "` + privateKey + `",
        "peers": [{
          "id": "source-peer",
          "publicKey": "` + publicKey + `",
          "endpoint": "vpn.example.com:51820",
          "allowedIps": ["10.0.0.0/24"],
          "hasPresharedKey": true,
          "presharedKey": "` + presharedKey + `"
        }]
      }
    },
    {
      "id": "source-ts",
      "label": "tailnet",
      "kind": "tailscale",
      "tailscale": {
        "hostname": "app-node",
        "hasAuthKey": true,
        "authKey": "` + authKey + `"
      }
    }
  ]
}`

	networks := &fakeNetworkProfiles{}
	secrets := newFakeSecrets()
	destination := newNetworkExportService(networks, secrets)
	preview, err := destination.PreviewImport(context.Background(), document,
		PreviewImportOptions{FileName: "netcatty-vault.json"})
	if err != nil {
		t.Fatalf("PreviewImport: %v", err)
	}
	if _, err := destination.CommitImport(context.Background(), preview, ImportCommitOptions{}); err != nil {
		t.Fatalf("CommitImport: %v", err)
	}
	if len(networks.items) != 2 {
		t.Fatalf("imported profiles = %+v", networks.items)
	}
	wireGuard := networks.items[0]
	if wireGuard.WireGuard == nil || !wireGuard.WireGuard.HasPrivateKey ||
		len(wireGuard.WireGuard.Peers) != 1 || !wireGuard.WireGuard.Peers[0].HasPresharedKey {
		t.Fatalf("imported WireGuard markers = %+v", wireGuard)
	}
	peerID := wireGuard.WireGuard.Peers[0].ID
	if got := secrets.values[NetworkProfileSecretRef(wireGuard.ID, FieldNetworkPrivateKey)]; got != privateKey {
		t.Fatalf("stored Private Key = %q, want the document value", got)
	}
	if got := secrets.values[NetworkPeerSecretRef(peerID, FieldNetworkPresharedKey)]; got != presharedKey {
		t.Fatalf("stored Preshared Key = %q, want the document value", got)
	}
	tailscale := networks.items[1]
	if tailscale.Tailscale == nil || !tailscale.Tailscale.HasAuthKey {
		t.Fatalf("imported Tailscale marker = %+v", tailscale)
	}
	if got := secrets.values[NetworkProfileSecretRef(tailscale.ID, FieldNetworkAuthKey)]; got != authKey {
		t.Fatalf("stored Auth Key = %q, want the document value", got)
	}
}
