package phoneshell

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	domainconn "github.com/mslxl/natcatty/domain/connection"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// fakeVault is the VaultStore the page tests drive. The real store's behaviour
// is covered by TestVaultPagesReadTheSharedStore, which uses SQLite.
type fakeVault struct {
	keys              []domainvault.Key
	identities        []domainvault.Identity
	known             []domainconn.KnownHost
	keysErr           error
	identitiesErr     error
	hostsErr          error
	deleteErr         error
	createKeyErr      error
	updateKeyErr      error
	deleteIdentityErr error

	createdKeys       []domainvault.KeyDraft
	attemptedKeys     []domainvault.KeyDraft
	updatedKeys       []keyUpdate
	deletedKeys       []model.KeyID
	createdIdentities []domainvault.IdentityDraft
	updatedIdentities []identityUpdate
	deletedIdentities []model.IdentityID
	deletedHosts      []string
}

// keyUpdate records one UpdateKey call with the draft it received.
type keyUpdate struct {
	ID    model.KeyID
	Draft domainvault.KeyDraft
}

// identityUpdate records one UpdateIdentity call with the draft it received.
type identityUpdate struct {
	ID    model.IdentityID
	Draft domainvault.IdentityDraft
}

func (f *fakeVault) ListKeys(context.Context) ([]domainvault.Key, error) {
	if f.keysErr != nil {
		return nil, f.keysErr
	}
	return append([]domainvault.Key(nil), f.keys...), nil
}

func (f *fakeVault) DeleteKey(_ context.Context, id model.KeyID) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deletedKeys = append(f.deletedKeys, id)
	kept := f.keys[:0]
	for _, key := range f.keys {
		if key.ID != id {
			kept = append(kept, key)
		}
	}
	f.keys = kept
	return nil
}

func (f *fakeVault) ListKnownHosts(context.Context) ([]domainconn.KnownHost, error) {
	if f.hostsErr != nil {
		return nil, f.hostsErr
	}
	return append([]domainconn.KnownHost(nil), f.known...), nil
}

func (f *fakeVault) DeleteKnownHost(_ context.Context, id string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deletedHosts = append(f.deletedHosts, id)
	kept := f.known[:0]
	for _, record := range f.known {
		if record.ID != id {
			kept = append(kept, record)
		}
	}
	f.known = kept
	return nil
}

// The VaultStore seam carries the Host Group half so the Vault's Hosts family
// shares it. These pages do not exercise the group operations; the Hosts tests
// drive them through fakeHostStore.
func (f *fakeVault) ListGroups(context.Context) ([]domainvault.Group, error) { return nil, nil }

func (f *fakeVault) AssignHostsGroup(context.Context, []model.HostID, string) error { return nil }

func (f *fakeVault) ListIdentities(context.Context) ([]domainvault.Identity, error) {
	if f.identitiesErr != nil {
		return nil, f.identitiesErr
	}
	return append([]domainvault.Identity(nil), f.identities...), nil
}

func (f *fakeVault) CreateKey(_ context.Context, draft domainvault.KeyDraft) (domainvault.Key, error) {
	f.attemptedKeys = append(f.attemptedKeys, draft)
	if f.createKeyErr != nil {
		return domainvault.Key{}, f.createKeyErr
	}
	f.createdKeys = append(f.createdKeys, draft)
	keyType := draft.Type
	if keyType == "" {
		keyType = domainvault.KeyTypeED25519
	}
	key := domainvault.Key{
		ID:            model.KeyID(fmt.Sprintf("key-%d", len(f.keys)+1)),
		Label:         draft.Label,
		Type:          keyType,
		PublicKey:     samplePublicKey,
		HasPrivateKey: draft.PrivateKey != "",
		HasPassphrase: draft.SavePassphrase && draft.Passphrase != "",
	}
	f.keys = append(f.keys, key)
	return key, nil
}

func (f *fakeVault) UpdateKey(_ context.Context, id model.KeyID, draft domainvault.KeyDraft) (domainvault.Key, error) {
	if f.updateKeyErr != nil {
		return domainvault.Key{}, f.updateKeyErr
	}
	f.updatedKeys = append(f.updatedKeys, keyUpdate{ID: id, Draft: draft})
	for i := range f.keys {
		if f.keys[i].ID != id {
			continue
		}
		f.keys[i].Label = draft.Label
		if draft.PrivateKey != "" {
			f.keys[i].HasPrivateKey = true
		}
		if draft.ClearPassphrase {
			f.keys[i].HasPassphrase = false
		} else if draft.SavePassphrase && draft.Passphrase != "" {
			f.keys[i].HasPassphrase = true
		}
		return f.keys[i], nil
	}
	return domainvault.Key{}, errors.New("key not found")
}

func (f *fakeVault) CreateIdentity(_ context.Context, draft domainvault.IdentityDraft) (domainvault.Identity, error) {
	f.createdIdentities = append(f.createdIdentities, draft)
	identity := domainvault.Identity{
		ID:          model.IdentityID(fmt.Sprintf("identity-%d", len(f.identities)+1)),
		Label:       draft.Label,
		Username:    draft.Username,
		AuthMethod:  draft.AuthMethod,
		KeyID:       draft.KeyID,
		HasPassword: draft.Password != "",
	}
	f.identities = append(f.identities, identity)
	return identity, nil
}

func (f *fakeVault) UpdateIdentity(_ context.Context, id model.IdentityID, draft domainvault.IdentityDraft) (domainvault.Identity, error) {
	f.updatedIdentities = append(f.updatedIdentities, identityUpdate{ID: id, Draft: draft})
	for i := range f.identities {
		if f.identities[i].ID != id {
			continue
		}
		f.identities[i].Label = draft.Label
		f.identities[i].Username = draft.Username
		f.identities[i].AuthMethod = draft.AuthMethod
		f.identities[i].KeyID = draft.KeyID
		if draft.ClearPassword {
			f.identities[i].HasPassword = false
		} else if draft.Password != "" {
			f.identities[i].HasPassword = true
		}
		return f.identities[i], nil
	}
	return domainvault.Identity{}, errors.New("identity not found")
}

func (f *fakeVault) DeleteIdentity(_ context.Context, id model.IdentityID) error {
	if f.deleteIdentityErr != nil {
		return f.deleteIdentityErr
	}
	f.deletedIdentities = append(f.deletedIdentities, id)
	kept := f.identities[:0]
	for _, identity := range f.identities {
		if identity.ID != id {
			kept = append(kept, identity)
		}
	}
	f.identities = kept
	return nil
}

func openSectionPage(t *testing.T, shell *Shell, sec Section, title string) *ui.Tester {
	t.Helper()
	tt := ui.NewTester(shell.View(sec), 390, 844)
	if err := tt.Click(title); err != nil {
		t.Fatalf("click %q: %v", title, err)
	}
	// A route push slides the new page in. Advance the tester's clock so the
	// page is where a user would see it before the test taps inside it.
	tt.Advance(600 * time.Millisecond)
	return tt
}

// samplePublicKey is a synthetic public key. A public key is not a secret,
// but the row shows only its beginning, so the test derives the expected
// excerpt from the same helper the page uses.
const samplePublicKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExampleKeyMaterialForTests laptop"

func sampleKey() domainvault.Key {
	return domainvault.Key{
		ID:            "key-1",
		Label:         "laptop",
		Type:          domainvault.KeyTypeED25519,
		PublicKey:     samplePublicKey,
		HasPrivateKey: true,
		HasPassphrase: true,
	}
}

func sampleKnownHost() domainconn.KnownHost {
	return domainconn.KnownHost{
		ID:          "known-1",
		Hostname:    "example.com",
		Port:        22,
		KeyType:     "ssh-ed25519",
		Fingerprint: "SHA256:abcdefghijklmnopqrstuvwxyz0123456789",
	}
}

// TestVaultListsIdentifiableEntries pins the detail each row shows: a Key by
// label, algorithm and public key, and a Known Host by endpoint, key type and
// fingerprint. Everything listed is public material; a private key and a
// passphrase are not in the record and must not appear in the frame.
func TestVaultListsIdentifiableEntries(t *testing.T) {
	store := &fakeVault{keys: []domainvault.Key{sampleKey()}, known: []domainconn.KnownHost{sampleKnownHost()}}
	shell := New(WithVault(store))
	if err := shell.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}

	tt := openSectionPage(t, shell, Vault, "Keychain")
	blob := "AAAAC3NzaC1lZDI1NTE5AAAAIExampleKeyMaterialForTests"
	wantShort := blob[:24] + "…"
	for _, want := range []string{"laptop", "ED25519", wantShort, "passphrase protected"} {
		if !tt.HasText(want) {
			t.Fatalf("SSH keys page does not show %q; texts: %v", want, texts(t, tt))
		}
	}
	// A public key longer than the row shows is shortened, not dropped.
	if tt.HasText(blob) {
		t.Fatalf("SSH keys page shows the whole blob instead of an excerpt; texts: %v", texts(t, tt))
	}

	if err := tt.Click("Back"); err != nil {
		t.Fatalf("click Back: %v", err)
	}
	tt.Advance(600 * time.Millisecond)
	if err := tt.Click("Known hosts"); err != nil {
		t.Fatalf("click Known hosts: %v", err)
	}
	tt.Advance(600 * time.Millisecond)
	fingerprint := "SHA256:abcdefghijklmnopqrstuvwxyz0123456789"
	for _, want := range []string{"example.com:22", "ssh-ed25519", fingerprint} {
		if !tt.HasText(want) {
			t.Fatalf("Known hosts page does not show %q; texts: %v", want, texts(t, tt))
		}
	}

	// No list may render secret material, even if a future record carried a
	// field the page did not expect to show.
	for _, forbidden := range []string{
		"-----BEGIN", "PRIVATE KEY", "correct horse battery staple", "ssh-ed25519 AAAA_PRIVATE",
	} {
		if tt.HasText(forbidden) {
			t.Fatalf("Vault page rendered secret material %q; texts: %v", forbidden, texts(t, tt))
		}
	}
}

// TestVaultDeleteRequiresConfirmation drives the destructive path: tapping
// delete only opens the confirmation, cancelling leaves the store alone, and
// confirming deletes through the seam and reloads the list.
func TestVaultDeleteRequiresConfirmation(t *testing.T) {
	store := &fakeVault{keys: []domainvault.Key{sampleKey()}}
	shell := New(WithVault(store))
	if err := shell.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	tt := openSectionPage(t, shell, Vault, "Keychain")

	if err := tt.Click("Delete key laptop"); err != nil {
		t.Fatalf("click delete: %v", err)
	}
	if !tt.HasText("Delete the key laptop?") {
		t.Fatalf("confirmation is not shown; texts: %v", texts(t, tt))
	}
	if len(store.deletedKeys) != 0 {
		t.Fatalf("delete happened before confirmation: %v", store.deletedKeys)
	}

	if err := tt.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	if len(store.deletedKeys) != 0 {
		t.Fatalf("cancel still deleted: %v", store.deletedKeys)
	}
	if !tt.HasText("laptop") {
		t.Fatalf("the key disappeared after cancel; texts: %v", texts(t, tt))
	}

	if err := tt.Click("Delete key laptop"); err != nil {
		t.Fatalf("click delete again: %v", err)
	}
	if err := tt.Click("Delete"); err != nil {
		t.Fatalf("click confirm: %v", err)
	}
	if len(store.deletedKeys) != 1 || store.deletedKeys[0] != "key-1" {
		t.Fatalf("deleted keys = %v, want [key-1]", store.deletedKeys)
	}
	if !tt.HasText("No SSH keys yet") {
		t.Fatalf("list did not reload after delete; texts: %v", texts(t, tt))
	}
}

// TestVaultForgetKnownHostRequiresConfirmation is the trust-record half of
// the same rule.
func TestVaultForgetKnownHostRequiresConfirmation(t *testing.T) {
	store := &fakeVault{known: []domainconn.KnownHost{sampleKnownHost()}}
	shell := New(WithVault(store))
	if err := shell.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	tt := openSectionPage(t, shell, Vault, "Known hosts")

	if err := tt.Click("Forget example.com:22"); err != nil {
		t.Fatalf("click forget: %v", err)
	}
	if !tt.HasText("Forget example.com:22?") {
		t.Fatalf("confirmation is not shown; texts: %v", texts(t, tt))
	}
	if len(store.deletedHosts) != 0 {
		t.Fatalf("forget happened before confirmation: %v", store.deletedHosts)
	}
	if err := tt.Click("Forget"); err != nil {
		t.Fatalf("click confirm: %v", err)
	}
	if len(store.deletedHosts) != 1 || store.deletedHosts[0] != "known-1" {
		t.Fatalf("deleted hosts = %v, want [known-1]", store.deletedHosts)
	}
	if !tt.HasText("No known hosts yet") {
		t.Fatalf("list did not reload after forget; texts: %v", texts(t, tt))
	}
}

// TestVaultStatesAreRendered covers loading, empty and failure: none of them
// may leave the page blank.
func TestVaultStatesAreRendered(t *testing.T) {
	t.Run("loading", func(t *testing.T) {
		// A fresh shell has not loaded yet, which is the state a slow read
		// shows.
		shell := New(WithVault(&fakeVault{}))
		tt := openSectionPage(t, shell, Vault, "Keychain")
		if !tt.HasText("Loading the Vault") {
			t.Fatalf("loading state is not rendered; texts: %v", texts(t, tt))
		}
	})

	t.Run("empty", func(t *testing.T) {
		shell := New(WithVault(&fakeVault{}))
		if err := shell.Load(context.Background()); err != nil {
			t.Fatalf("Load: %v", err)
		}
		tt := openSectionPage(t, shell, Vault, "Keychain")
		if !tt.HasText("No SSH keys yet") {
			t.Fatalf("empty key state is not rendered; texts: %v", texts(t, tt))
		}
		if err := tt.Click("Back"); err != nil {
			t.Fatalf("click Back: %v", err)
		}
		tt.Advance(600 * time.Millisecond)
		if err := tt.Click("Known hosts"); err != nil {
			t.Fatalf("click Known hosts: %v", err)
		}
		tt.Advance(600 * time.Millisecond)
		if !tt.HasText("No known hosts yet") {
			t.Fatalf("empty known-host state is not rendered; texts: %v", texts(t, tt))
		}
	})

	t.Run("error", func(t *testing.T) {
		store := &fakeVault{keysErr: errors.New("disk unreadable")}
		shell := New(WithVault(store))
		if err := shell.Load(context.Background()); err == nil {
			t.Fatal("Load returned nil for a failing store")
		}
		tt := openSectionPage(t, shell, Vault, "Keychain")
		if !tt.HasText("Could not load SSH keys.") {
			t.Fatalf("error state is not rendered; texts: %v", texts(t, tt))
		}
		// The error state offers a retry instead of leaving the page stuck.
		store.keysErr = nil
		if err := tt.Click("Retry"); err != nil {
			t.Fatalf("click Retry: %v", err)
		}
		if !tt.HasText("No SSH keys yet") {
			t.Fatalf("retry did not reload the list; texts: %v", texts(t, tt))
		}
	})

	t.Run("unavailable", func(t *testing.T) {
		shell := New()
		if err := shell.Load(context.Background()); err != nil {
			t.Fatalf("Load without a store = %v, want nil", err)
		}
		tt := openSectionPage(t, shell, Vault, "Keychain")
		if !tt.HasText("The Vault is not available on this device.") {
			t.Fatalf("unavailable state is not rendered; texts: %v", texts(t, tt))
		}
	})
}

// TestVaultRefreshSeesStoreChanges is how a change made on the desktop becomes
// visible while the phone app stays open: the page reloads from the same
// store instead of keeping a private copy.
func TestVaultRefreshSeesStoreChanges(t *testing.T) {
	store := &fakeVault{keys: []domainvault.Key{sampleKey()}}
	shell := New(WithVault(store))
	if err := shell.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	tt := openSectionPage(t, shell, Vault, "Keychain")
	if err := tt.Click("Refresh"); err != nil {
		t.Fatalf("click Refresh: %v", err)
	}
	if tt.HasText("server") {
		t.Fatalf("server key appeared before it existed; texts: %v", texts(t, tt))
	}

	store.keys = append(store.keys, domainvault.Key{
		ID: "key-2", Label: "server", Type: domainvault.KeyTypeRSA,
		PublicKey: "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQExampleServerKey server",
	})
	if err := tt.Click("Refresh"); err != nil {
		t.Fatalf("click Refresh after storing a key: %v", err)
	}
	if !tt.HasText("server") {
		t.Fatalf("refreshed list does not show the new key; texts: %v", texts(t, tt))
	}
}
