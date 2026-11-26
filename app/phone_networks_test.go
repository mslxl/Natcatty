package app

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	sshadapter "github.com/mslxl/natcatty/adapters/ssh"
	"github.com/mslxl/natcatty/adapters/store"
	appconnection "github.com/mslxl/natcatty/application/connection"
	appvault "github.com/mslxl/natcatty/application/vault"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/phoneshell"
)

// openPhoneNetworkVault opens the real phone Vault over SQLite and its
// encrypted field-secret store. A second Shell over the same service is the
// desktop reload the acceptance requires.
func openPhoneNetworkVault(t *testing.T) (*store.Store, *appvault.Service) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "natcatty.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	secrets, err := store.OpenSecretStore(ctx, db, newPhoneTestKeys())
	if err != nil {
		t.Fatalf("open secret store: %v", err)
	}
	t.Cleanup(secrets.Close)
	return db, newPhoneVault(db, secrets)
}

func loadPhoneNetworks(t *testing.T, vault *appvault.Service) *phoneshell.Shell {
	t.Helper()
	shell := phoneshell.New(phoneshell.WithVault(vault))
	if err := shell.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	return shell
}

// TestPhoneNetworksWriteTheSharedVault drives Tailscale profile Add, Edit, and
// Delete through the real Vault service and SQLite store. A second Shell
// opened on the same service is the desktop reload.
func TestPhoneNetworksWriteTheSharedVault(t *testing.T) {
	ctx := context.Background()
	_, vault := openPhoneNetworkVault(t)
	shell := loadPhoneNetworks(t, vault)
	tt := phonePage(t, shell, phoneshell.Vault, "Networks")

	if err := tt.Click("Add Tailscale profile"); err != nil {
		t.Fatalf("click Add Tailscale profile: %v", err)
	}
	if err := tt.Click("Label"); err != nil {
		t.Fatalf("focus Label: %v", err)
	}
	tt.Type("phone office")
	if err := tt.Click("Hostname"); err != nil {
		t.Fatalf("focus Hostname: %v", err)
	}
	tt.Type("phone-node")
	if err := tt.Click("Auth key"); err != nil {
		t.Fatalf("focus Auth key: %v", err)
	}
	tt.Type("tskey-auth-phone")
	if err := tt.Click("Save profile"); err != nil {
		t.Fatalf("click Save profile: %v", err)
	}
	if !tt.HasText("phone office") || !tt.HasText("Tailscale Net · phone-node") {
		t.Fatalf("the added profile is not listed; texts: %v", tt.Texts())
	}

	profiles, err := vault.ListNetworkProfiles(ctx)
	if err != nil {
		t.Fatalf("list Network Profiles: %v", err)
	}
	if len(profiles) != 1 {
		t.Fatalf("profiles = %+v, want one", profiles)
	}
	added := profiles[0]
	if added.Kind != domainvault.NetworkKindTailscale || added.Tailscale == nil ||
		added.Tailscale.Hostname != "phone-node" || !added.Tailscale.HasAuthKey {
		t.Fatalf("stored Tailscale profile = %+v", added)
	}
	key, err := vault.NetworkProfileSecret(ctx, added.ID, appvault.FieldNetworkAuthKey)
	if err != nil {
		t.Fatalf("read stored Auth Key: %v", err)
	}
	if key != "tskey-auth-phone" {
		t.Fatalf("stored Auth Key = %q, want the typed value", key)
	}

	// A second Shell on the same Vault is the desktop reload.
	desktop := loadPhoneNetworks(t, vault)
	reloaded := phonePage(t, desktop, phoneshell.Vault, "Networks")
	if !reloaded.HasText("phone office") {
		t.Fatalf("a reloaded Shell does not show the added profile; texts: %v", reloaded.Texts())
	}

	// Edit the profile on the phone. A blank credential field keeps the stored
	// Auth Key rather than clearing it.
	if err := tt.Click("Edit profile phone office"); err != nil {
		t.Fatalf("click Edit profile: %v", err)
	}
	if err := tt.Click("Label"); err != nil {
		t.Fatalf("focus Label in edit: %v", err)
	}
	tt.Type(" v2")
	if err := tt.Click("Save profile"); err != nil {
		t.Fatalf("click Save profile in edit: %v", err)
	}
	edited, err := vault.NetworkProfile(ctx, added.ID)
	if err != nil {
		t.Fatalf("read edited profile: %v", err)
	}
	if edited.Label != "phone office v2" || edited.Tailscale == nil ||
		!edited.Tailscale.HasAuthKey {
		t.Fatalf("edited profile = %+v, want the new label and retained Auth Key", edited)
	}
	desktop = loadPhoneNetworks(t, vault)
	reloaded = phonePage(t, desktop, phoneshell.Vault, "Networks")
	if !reloaded.HasText("phone office v2") {
		t.Fatalf("a reloaded Shell does not show the edit; texts: %v", reloaded.Texts())
	}

	if err := tt.Click("Delete profile phone office v2"); err != nil {
		t.Fatalf("click Delete profile: %v", err)
	}
	if err := tt.Click("Delete"); err != nil {
		t.Fatalf("confirm delete: %v", err)
	}
	profiles, err = vault.ListNetworkProfiles(ctx)
	if err != nil {
		t.Fatalf("list profiles after delete: %v", err)
	}
	if len(profiles) != 0 {
		t.Fatalf("the deleted profile is still in the shared store: %+v", profiles)
	}
}

// phoneNetworkConfPicker is the phone's system picker stand-in for the app
// test. It returns the same file shape MyGo's Dialog.Open hands the adapter.
type phoneNetworkConfPicker struct {
	name    string
	content []byte
}

func (p phoneNetworkConfPicker) PickWireGuardConf() (string, []byte, error) {
	return p.name, p.content, nil
}

func phoneWireGuardKeyFill(fill byte) string {
	key := make([]byte, 32)
	for index := range key {
		key[index] = fill
	}
	return base64.StdEncoding.EncodeToString(key)
}

func phoneWireGuardConf() []byte {
	return []byte(fmt.Sprintf(`[Interface]
PrivateKey = %s
Address = 10.9.0.2/32
DNS = 10.9.0.1
ListenPort = 51820

[Peer]
PublicKey = %s
Endpoint = vpn.example.com:51820
AllowedIPs = 10.9.0.0/24
`, phoneWireGuardKeyFill(1), phoneWireGuardKeyFill(2)))
}

// TestPhoneWireGuardConfImportSavesThroughSharedVault imports a .conf through
// the injected system picker and saves it through the real Vault service: the
// parsed configuration reaches SQLite and the Private Key reaches the
// encrypted field-secret store, not the record.
func TestPhoneWireGuardConfImportSavesThroughSharedVault(t *testing.T) {
	ctx := context.Background()
	_, vault := openPhoneNetworkVault(t)
	picker := phoneNetworkConfPicker{name: "wg0.conf", content: phoneWireGuardConf()}
	shell := phoneshell.New(
		phoneshell.WithVault(vault),
		phoneshell.WithNetworkConfPicker(picker),
	)
	if err := shell.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}
	tt := phonePage(t, shell, phoneshell.Vault, "Networks")

	if err := tt.Click("Add WireGuard profile"); err != nil {
		t.Fatalf("click Add WireGuard profile: %v", err)
	}
	if err := tt.Click("Import .conf"); err != nil {
		t.Fatalf("click Import .conf: %v", err)
	}
	if err := tt.Click("Label"); err != nil {
		t.Fatalf("focus Label: %v", err)
	}
	tt.Type("imported wg")
	if err := tt.Click("Save profile"); err != nil {
		t.Fatalf("click Save profile: %v", err)
	}

	profiles, err := vault.ListNetworkProfiles(ctx)
	if err != nil {
		t.Fatalf("list profiles: %v", err)
	}
	if len(profiles) != 1 || profiles[0].Kind != domainvault.NetworkKindWireGuard {
		t.Fatalf("profiles = %+v, want one WireGuard profile", profiles)
	}
	profile := profiles[0]
	if profile.WireGuard == nil || len(profile.WireGuard.Addresses) != 1 ||
		profile.WireGuard.Addresses[0] != "10.9.0.2/32" ||
		len(profile.WireGuard.Peers) != 1 ||
		profile.WireGuard.Peers[0].PublicKey != phoneWireGuardKeyFill(2) {
		t.Fatalf("stored WireGuard config = %+v", profile.WireGuard)
	}
	if !profile.WireGuard.HasPrivateKey {
		t.Fatal("the stored profile does not mark its Private Key")
	}
	private, err := vault.NetworkProfileSecret(ctx, profile.ID, appvault.FieldNetworkPrivateKey)
	if err != nil {
		t.Fatalf("read stored Private Key: %v", err)
	}
	if private != phoneWireGuardKeyFill(1) {
		t.Fatalf("stored Private Key = %q, want the imported value", private)
	}
}

// TestPhoneNetworkProxyCommandConflictStaysShared proves the phone does not
// bypass the shared rule: a route selecting both a ProxyCommand and a Network
// Profile is rejected by the shared adapter before any process starts.
func TestPhoneNetworkProxyCommandConflictStaysShared(t *testing.T) {
	dialer := sshadapter.NewDialer()
	_, err := dialer.Dial(context.Background(), appconnection.DialRequest{
		Hostname: "target.internal", Port: 22, Username: "root",
		Proxy: &appconnection.Proxy{
			Type:    appconnection.ProxyCommand,
			Command: "ssh -W %h:%p bastion",
		},
		Network: &appconnection.NetworkRoute{
			ProfileID: "net-1",
			Kind:      string(domainvault.NetworkKindTailscale),
			Label:     "office",
		},
	})
	if !errors.Is(err, appconnection.ErrProxyCommandNetworkConflict) {
		t.Fatalf("Dial error = %v, want the shared ProxyCommand/Network conflict", err)
	}
	var typed *appconnection.Error
	if !errors.As(err, &typed) || typed.Provider == nil ||
		typed.Provider.ProfileID != "net-1" {
		t.Fatalf("error %v does not carry the selected Network Profile", err)
	}
	if !strings.Contains(err.Error(), "ProxyCommand") {
		t.Fatalf("error %q does not explain the conflict", err)
	}
}
