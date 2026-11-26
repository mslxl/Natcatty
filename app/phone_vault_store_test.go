package app

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	sshadapter "github.com/mslxl/natcatty/adapters/ssh"
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

	vault := appvault.NewService(db, appvault.WithKeys(db), appvault.WithIdentities(db), appvault.WithKnownHosts(db))
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

// TestPhoneKeychainWritesTheSharedVault drives Add, Edit, and Delete for an
// SSH Key and Add for an Identity through the real Vault service and SQLite
// store. A second phone Shell opened on the same store is the desktop reload:
// it sees the records the first Shell wrote, and the first Shell's deletion is
// gone from the store the desktop reads.
func TestPhoneKeychainWritesTheSharedVault(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "natcatty.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	block, err := ssh.MarshalPrivateKey(private, "")
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	material := string(pem.EncodeToMemory(block))

	vault := appvault.NewService(db,
		appvault.WithKeys(db),
		appvault.WithIdentities(db),
		appvault.WithKnownHosts(db),
		appvault.WithSecrets(newMemoryFieldSecrets()),
		appvault.WithKeyInspector(sshadapter.KeyParser{}),
	)
	shell := phoneshell.New(phoneshell.WithVault(vault))
	if err := shell.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}
	tt := phonePage(t, shell, phoneshell.Vault, "Keychain")

	// Add an SSH Key with real, parsable material.
	if err := tt.Click("Add key"); err != nil {
		t.Fatalf("click Add key: %v", err)
	}
	if err := tt.Click("Label"); err != nil {
		t.Fatalf("focus Label: %v", err)
	}
	tt.Type("phone-added")
	if err := tt.Click("Private key"); err != nil {
		t.Fatalf("focus Private key: %v", err)
	}
	tt.Type(material)
	if err := tt.Click("Save key"); err != nil {
		t.Fatalf("click Save key: %v", err)
	}
	if !tt.HasText("phone-added") {
		t.Fatalf("the added key is not listed; texts: %v", tt.Texts())
	}
	keys, err := db.ListKeys(ctx)
	if err != nil {
		t.Fatalf("list keys: %v", err)
	}
	var added domainvault.Key
	for _, key := range keys {
		if key.Label == "phone-added" {
			added = key
		}
	}
	if added.ID == "" || !added.HasPrivateKey {
		t.Fatalf("the shared store has no usable phone-added key: %+v", keys)
	}
	for _, text := range tt.Texts() {
		if text == material || strings.Contains(text, "OPENSSH PRIVATE KEY") {
			t.Fatalf("the Keychain rendered private material: %q", text)
		}
	}

	// Add an Identity selecting that Key.
	if err := tt.Click("Add identity"); err != nil {
		t.Fatalf("click Add identity: %v", err)
	}
	if err := tt.Click("Label"); err != nil {
		t.Fatalf("focus Identity Label: %v", err)
	}
	tt.Type("phone-identity")
	if err := tt.Click("Username"); err != nil {
		t.Fatalf("focus Username: %v", err)
	}
	tt.Type("deploy")
	if err := tt.Click("SSH key"); err != nil {
		t.Fatalf("choose SSH key auth: %v", err)
	}
	if err := tt.Click("Use key phone-added"); err != nil {
		t.Fatalf("select the added key: %v", err)
	}
	if err := tt.Click("Save identity"); err != nil {
		t.Fatalf("click Save identity: %v", err)
	}
	identities, err := vault.ListIdentities(ctx)
	if err != nil {
		t.Fatalf("list identities: %v", err)
	}
	foundIdentity := false
	for _, identity := range identities {
		if identity.Label != "phone-identity" {
			continue
		}
		foundIdentity = true
		if identity.AuthMethod != domainvault.IdentityAuthKey || identity.KeyID != added.ID {
			t.Fatalf("stored Identity = %+v, want key auth with %q", identity, added.ID)
		}
	}
	if !foundIdentity {
		t.Fatalf("the shared store has no phone-identity: %+v", identities)
	}

	// A reload of the same store sees both records, as the desktop does.
	desktop := phoneshell.New(phoneshell.WithVault(vault))
	if err := desktop.Load(ctx); err != nil {
		t.Fatalf("desktop Load: %v", err)
	}
	reloaded := phonePage(t, desktop, phoneshell.Vault, "Keychain")
	for _, want := range []string{"phone-added", "phone-identity"} {
		if !reloaded.HasText(want) {
			t.Fatalf("a reloaded shell does not show %q; texts: %v", want, reloaded.Texts())
		}
	}

	// Edit the Identity's username through the first Shell, then delete it.
	if err := tt.Click("Edit identity phone-identity"); err != nil {
		t.Fatalf("click Edit identity: %v", err)
	}
	if err := tt.Click("Username"); err != nil {
		t.Fatalf("focus Username in edit: %v", err)
	}
	tt.Type("2")
	if err := tt.Click("Save identity"); err != nil {
		t.Fatalf("click Save identity in edit: %v", err)
	}
	identities, err = vault.ListIdentities(ctx)
	if err != nil {
		t.Fatalf("list identities after edit: %v", err)
	}
	editedIdentity := false
	for _, identity := range identities {
		if identity.Label == "phone-identity" && identity.Username == "deploy2" {
			editedIdentity = true
		}
	}
	if !editedIdentity {
		t.Fatalf("the shared store did not receive the edited Identity: %+v", identities)
	}

	if err := tt.Click("Delete identity phone-identity"); err != nil {
		t.Fatalf("click delete identity: %v", err)
	}
	if err := tt.Click("Delete"); err != nil {
		t.Fatalf("click confirm delete identity: %v", err)
	}
	identities, err = vault.ListIdentities(ctx)
	if err != nil {
		t.Fatalf("list identities after delete: %v", err)
	}
	for _, identity := range identities {
		if identity.Label == "phone-identity" {
			t.Fatalf("the deleted Identity is still in the shared store: %+v", identity)
		}
	}

	// Edit the Key's label through the first Shell, then delete it.
	if err := tt.Click("Edit key phone-added"); err != nil {
		t.Fatalf("click Edit key: %v", err)
	}
	if err := tt.Click("Label"); err != nil {
		t.Fatalf("focus Label in edit: %v", err)
	}
	tt.Type(" v2")
	if err := tt.Click("Save key"); err != nil {
		t.Fatalf("click Save key in edit: %v", err)
	}
	keys, err = db.ListKeys(ctx)
	if err != nil {
		t.Fatalf("list keys after edit: %v", err)
	}
	edited := false
	for _, key := range keys {
		if key.ID == added.ID && key.Label == "phone-added v2" {
			edited = true
		}
	}
	if !edited {
		t.Fatalf("the shared store did not receive the edited label: %+v", keys)
	}

	if err := tt.Click("Delete key phone-added v2"); err != nil {
		t.Fatalf("click delete key: %v", err)
	}
	if err := tt.Click("Delete"); err != nil {
		t.Fatalf("click confirm delete key: %v", err)
	}
	keys, err = db.ListKeys(ctx)
	if err != nil {
		t.Fatalf("list keys after delete: %v", err)
	}
	for _, key := range keys {
		if key.ID == added.ID {
			t.Fatalf("the deleted Key is still in the shared store: %+v", key)
		}
	}
}
