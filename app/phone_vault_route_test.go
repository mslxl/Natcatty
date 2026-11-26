package app

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mslxl/natcatty/adapters/store"
	appconnection "github.com/mslxl/natcatty/application/connection"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// TestPhoneVaultResolvesProfilesGroupsAndIdentities guards the phone
// composition root's Vault wiring against the defect this test was added for:
// the phone built a Vault with Keys and Known Hosts only, so a Host's Proxy
// Profile was silently ignored (the phone dialed directly), a Network Profile
// reference failed the whole route, group defaults never reached the endpoint,
// and an Identity selection resolved to no credential.
//
// It builds the Vault through newPhoneVault, the helper the phone root uses,
// over a real SQLite store and the real Vault service; the route is resolved
// by the same vaultRoutes the phone's Session opener uses.
func TestPhoneVaultResolvesProfilesGroupsAndIdentities(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "natcatty.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	secrets, err := store.OpenSecretStore(ctx, db,
		&writeTestKeyRing{values: map[string][]byte{}},
		store.WithArgon2Params(1, 1024, 1))
	if err != nil {
		t.Fatalf("open secret store: %v", err)
	}
	t.Cleanup(secrets.Close)

	vault := newPhoneVault(db, secrets)

	if err := db.SaveProxyProfile(ctx, domainvault.ProxyProfile{
		ID: "proxy-1", Label: "office",
		Config: domainvault.ProxyConfig{
			Type: domainvault.ProxySOCKS5, Host: "socks.example.com", Port: 1080,
		},
	}); err != nil {
		t.Fatalf("save proxy profile: %v", err)
	}
	if err := db.SaveNetworkProfile(ctx, domainvault.NetworkProfile{
		ID: "net-1", Label: "office tailnet", Kind: domainvault.NetworkKindTailscale,
	}); err != nil {
		t.Fatalf("save network profile: %v", err)
	}

	if _, err := vault.CreateGroup(ctx, domainvault.GroupDraft{
		Path: "prod", Username: "groupuser", Port: 2200,
	}); err != nil {
		t.Fatalf("create group: %v", err)
	}
	identity, err := vault.CreateIdentity(ctx, domainvault.IdentityDraft{
		Label: "ops", Username: "opsuser",
		AuthMethod: domainvault.IdentityAuthPassword, Password: "identity-secret",
	})
	if err != nil {
		t.Fatalf("create identity: %v", err)
	}

	jump, err := vault.Create(ctx, domainvault.Draft{
		Label: "bastion", Hostname: "bastion.internal", Port: 2222,
		Username: "jumpuser", Protocol: domainvault.ProtocolSSH,
	})
	if err != nil {
		t.Fatalf("create jump Host: %v", err)
	}
	target, err := vault.Create(ctx, domainvault.Draft{
		Label: "prod box", Hostname: "target.internal", Protocol: domainvault.ProtocolSSH,
		Group:            "prod",
		IdentityID:       identity.ID,
		HostChain:        &domainvault.HostChain{HostIDs: []model.HostID{jump.ID}},
		ProxyProfileID:   "proxy-1",
		NetworkProfileID: "net-1",
	})
	if err != nil {
		t.Fatalf("create target Host: %v", err)
	}

	route, err := (vaultRoutes{vault: vault}).Route(ctx, target.ID)
	if err != nil {
		t.Fatalf("resolve route: %v", err)
	}
	if len(route.Jumps) != 1 || route.Jumps[0].Hostname != "bastion.internal" ||
		route.Jumps[0].Port != 2222 {
		t.Fatalf("jumps = %+v, want the Host chain's bastion", route.Jumps)
	}
	if route.Proxy == nil ||
		route.Proxy.Type != appconnection.ProxySOCKS5 ||
		route.Proxy.Host != "socks.example.com" || route.Proxy.Port != 1080 {
		t.Fatalf("proxy = %+v, want the selected SOCKS5 Proxy Profile", route.Proxy)
	}
	if route.Network == nil || route.Network.ProfileID != "net-1" ||
		route.Network.Kind != string(domainvault.NetworkKindTailscale) {
		t.Fatalf("network = %+v, want the selected Network Profile", route.Network)
	}

	effective, err := vault.Get(ctx, target.ID)
	if err != nil {
		t.Fatalf("effective Host: %v", err)
	}
	if effective.Port != 2200 {
		t.Fatalf("effective port = %d, want the group default 2200", effective.Port)
	}
	if effective.IdentityID != identity.ID {
		t.Fatalf("effective Identity = %q, want %q", effective.IdentityID, identity.ID)
	}

	// A Host that owns no credential bundle inherits the group's username too.
	grouped, err := vault.Create(ctx, domainvault.Draft{
		Label: "grouped", Hostname: "grouped.internal", Protocol: domainvault.ProtocolSSH,
		Group: "prod",
	})
	if err != nil {
		t.Fatalf("create grouped Host: %v", err)
	}
	groupDefaults, err := vault.Get(ctx, grouped.ID)
	if err != nil {
		t.Fatalf("effective grouped Host: %v", err)
	}
	if groupDefaults.Port != 2200 || groupDefaults.Username != "groupuser" {
		t.Fatalf("grouped endpoint = %s:%d, want 2200/groupuser",
			groupDefaults.Username, groupDefaults.Port)
	}

	resolved, err := vault.ResolveAuth(ctx, target.ID)
	if err != nil {
		t.Fatalf("resolve auth: %v", err)
	}
	if resolved.Selection.Username != "opsuser" || resolved.Password != "identity-secret" {
		t.Fatalf("auth = username %q password %q, want the selected Identity",
			resolved.Selection.Username, resolved.Password)
	}
}
