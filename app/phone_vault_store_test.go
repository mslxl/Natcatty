package app

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mslxl/natcatty/adapters/store"
	appvault "github.com/mslxl/natcatty/application/vault"
	domainconn "github.com/mslxl/natcatty/domain/connection"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/phoneshell"
)

// TestVaultPagesReadTheSharedStore proves the phone pages are a second reader
// of the desktop's Vault, not a copy: a Key and a Known Host written directly
// to the SQLite store are listed, a row written while the page is open appears
// after Refresh, and a deletion made on the phone is gone from the store the
// desktop reads.
func TestVaultPagesReadTheSharedStore(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "natcatty.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := db.SaveKey(ctx, domainvault.Key{
		ID:            "key-desk",
		Label:         "desk",
		Type:          domainvault.KeyTypeED25519,
		PublicKey:     "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIDeskKeyMaterial desk",
		HasPrivateKey: true,
	}); err != nil {
		t.Fatalf("save key: %v", err)
	}
	if err := db.UpsertKnownHost(ctx, domainconn.KnownHost{
		ID:          "known-desk",
		Hostname:    "example.com",
		Port:        2222,
		KeyType:     "ssh-ed25519",
		Fingerprint: "SHA256:deskFingerprint0123456789",
	}); err != nil {
		t.Fatalf("save known host: %v", err)
	}

	vault := appvault.NewService(db, appvault.WithKeys(db), appvault.WithKnownHosts(db))
	shell := phoneshell.New(phoneshell.WithVault(vault))
	if err := shell.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}

	tt := phonePage(t, shell, phoneshell.Vault, "Keychain")
	if !tt.HasText("desk") {
		t.Fatalf("shared key is not listed; texts: %v", tt.Texts())
	}

	// The desktop creates a Key while the phone page is open. Refresh reads
	// the same table again.
	if err := db.SaveKey(ctx, domainvault.Key{
		ID:        "key-created-elsewhere",
		Label:     "travel-laptop",
		Type:      domainvault.KeyTypeRSA,
		PublicKey: "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQElsewhereKey travel",
	}); err != nil {
		t.Fatalf("save second key: %v", err)
	}
	if err := tt.Click("Refresh"); err != nil {
		t.Fatalf("click Refresh: %v", err)
	}
	if !tt.HasText("travel-laptop") {
		t.Fatalf("a key created elsewhere is not visible after Refresh; texts: %v", tt.Texts())
	}

	// Deleting on the phone must remove the row from the shared store, which
	// is what makes future connections stop offering the key.
	if err := tt.Click("Delete key desk"); err != nil {
		t.Fatalf("click delete: %v", err)
	}
	if err := tt.Click("Delete"); err != nil {
		t.Fatalf("click confirm: %v", err)
	}
	keys, err := db.ListKeys(ctx)
	if err != nil {
		t.Fatalf("list keys after delete: %v", err)
	}
	for _, key := range keys {
		if key.ID == "key-desk" {
			t.Fatalf("deleted key is still in the shared store: %+v", key)
		}
	}

	if err := tt.Click("Back"); err != nil {
		t.Fatalf("click Back: %v", err)
	}
	tt.Advance(600 * time.Millisecond)
	if err := tt.Click("Known hosts"); err != nil {
		t.Fatalf("click Known hosts: %v", err)
	}
	tt.Advance(600 * time.Millisecond)
	if !tt.HasText("example.com:2222") {
		t.Fatalf("shared known host is not listed; texts: %v", tt.Texts())
	}
	if err := tt.Click("Forget example.com:2222"); err != nil {
		t.Fatalf("click forget: %v", err)
	}
	if err := tt.Click("Forget"); err != nil {
		t.Fatalf("click confirm forget: %v", err)
	}
	known, err := db.ListKnownHosts(ctx)
	if err != nil {
		t.Fatalf("list known hosts after forget: %v", err)
	}
	if len(known) != 0 {
		t.Fatalf("forgotten known host is still in the shared store: %+v", known)
	}
}

// TestVaultHostsMoveWritesSharedGroup drives the Vault's multi-select Move
// through the real Vault service: the group membership the phone writes is the
// same stored record the desktop reads back.
func TestVaultHostsMoveWritesSharedGroup(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "natcatty.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	vault := newPhoneVault(db, nil)
	group, err := vault.CreateGroup(ctx, domainvault.GroupDraft{Path: "prod"})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	host, err := vault.Create(ctx, domainvault.Draft{
		Label: "prod box", Hostname: "prod.example.com", Protocol: domainvault.ProtocolSSH,
	})
	if err != nil {
		t.Fatalf("create Host: %v", err)
	}

	shell := phoneshell.New(phoneshell.WithHosts(vault))
	if err := shell.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}
	tt := phonePage(t, shell, phoneshell.Vault, "Hosts")

	for _, action := range []string{"Select", "Select prod box", "Move", "Move to prod"} {
		if err := tt.Click(action); err != nil {
			t.Fatalf("click %q: %v", action, tt.Texts())
		}
		tt.Frame()
	}

	stored, err := db.List(ctx)
	if err != nil {
		t.Fatalf("list Hosts after move: %v", err)
	}
	for _, record := range stored {
		if record.ID != host.ID {
			continue
		}
		if record.GroupID != group.ID {
			t.Fatalf("stored Host GroupID = %q, want %q", record.GroupID, group.ID)
		}
		return
	}
	t.Fatalf("Host %q is missing from the shared store", host.ID)
}
