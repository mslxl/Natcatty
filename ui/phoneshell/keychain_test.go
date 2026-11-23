package phoneshell

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	domainconn "github.com/mslxl/natcatty/domain/connection"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// testPrivateMaterial and testPassphrase stand in for the plaintext a user
// pastes. The fake store does not parse it; the page must still never render
// it after the editor closes.
const (
	testPrivateMaterial = "phone-private-material-never-rendered"
	testPassphrase      = "phone-passphrase-never-rendered"
)

func sampleIdentity() domainvault.Identity {
	return domainvault.Identity{
		ID:         "identity-1",
		Label:      "ops",
		Username:   "deploy",
		AuthMethod: domainvault.IdentityAuthKey,
		KeyID:      "key-1",
	}
}

// assertNoSecretText fails when any frame text contains secret material.
func assertNoSecretText(t *testing.T, tt *ui.Tester, secrets ...string) {
	t.Helper()
	for _, text := range tt.Texts() {
		for _, secret := range secrets {
			if secret != "" && strings.Contains(text, secret) {
				t.Fatalf("the page rendered secret material %q in %q", secret, text)
			}
		}
	}
}

// TestKeychainCreatesEditsAndDeletesKey drives the whole SSH Key lifecycle
// through real clicks: the editor writes through the shared Vault seam, the
// list reloads from it, and a delete needs confirmation.
func TestKeychainCreatesEditsAndDeletesKey(t *testing.T) {
	store := &fakeVault{}
	shell := New(WithVault(store))
	if err := shell.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	tt := openSectionPage(t, shell, Vault, "Keychain")

	// Create.
	if err := tt.Click("Add key"); err != nil {
		t.Fatalf("click Add key: %v", err)
	}
	if err := tt.Click("Label"); err != nil {
		t.Fatalf("focus Label: %v", err)
	}
	tt.Type("phone-key")
	if err := tt.Click("Private key"); err != nil {
		t.Fatalf("focus Private key: %v", err)
	}
	tt.Type(testPrivateMaterial)
	if err := tt.Click("Passphrase"); err != nil {
		t.Fatalf("focus Passphrase: %v", err)
	}
	tt.Type(testPassphrase)
	if err := tt.Click("Save key"); err != nil {
		t.Fatalf("click Save key: %v", err)
	}

	if len(store.createdKeys) != 1 {
		t.Fatalf("created keys = %d, want 1", len(store.createdKeys))
	}
	created := store.createdKeys[0]
	if created.Label != "phone-key" || created.PrivateKey != testPrivateMaterial ||
		created.Passphrase != testPassphrase || !created.SavePassphrase {
		t.Fatalf("create draft = %+v, want the typed values", created)
	}
	if !tt.HasText("phone-key") {
		t.Fatalf("the created key is not in the list; texts: %v", texts(t, tt))
	}
	assertNoSecretText(t, tt, testPrivateMaterial, testPassphrase, "-----BEGIN", "PRIVATE KEY")

	// Edit. The editor must not prefill the stored material.
	if err := tt.Click("Edit key phone-key"); err != nil {
		t.Fatalf("click Edit key: %v", err)
	}
	assertNoSecretText(t, tt, testPrivateMaterial, testPassphrase)
	if err := tt.Click("Label"); err != nil {
		t.Fatalf("focus Label in edit: %v", err)
	}
	tt.Type(" edited")
	if err := tt.Click("Save key"); err != nil {
		t.Fatalf("click Save key in edit: %v", err)
	}
	if len(store.updatedKeys) != 1 {
		t.Fatalf("updated keys = %d, want 1", len(store.updatedKeys))
	}
	if store.updatedKeys[0].ID != "key-1" || store.updatedKeys[0].Draft.Label != "phone-key edited" {
		t.Fatalf("update draft = %+v, want the edited label", store.updatedKeys[0])
	}
	if store.updatedKeys[0].Draft.PrivateKey != "" {
		t.Fatalf("an untouched edit sent private material again: %q", store.updatedKeys[0].Draft.PrivateKey)
	}
	if !tt.HasText("phone-key edited") {
		t.Fatalf("the edited key is not in the list; texts: %v", texts(t, tt))
	}

	// Delete, with a cancellation first.
	if err := tt.Click("Delete key phone-key edited"); err != nil {
		t.Fatalf("click delete: %v", err)
	}
	if !tt.HasText("Delete the key phone-key edited?") {
		t.Fatalf("confirmation is not shown; texts: %v", texts(t, tt))
	}
	if err := tt.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	if len(store.deletedKeys) != 0 || !tt.HasText("phone-key edited") {
		t.Fatalf("cancel removed the key: deleted=%v texts=%v", store.deletedKeys, texts(t, tt))
	}
	if err := tt.Click("Delete key phone-key edited"); err != nil {
		t.Fatalf("click delete again: %v", err)
	}
	if err := tt.Click("Delete"); err != nil {
		t.Fatalf("click confirm: %v", err)
	}
	if len(store.deletedKeys) != 1 || store.deletedKeys[0] != "key-1" {
		t.Fatalf("deleted keys = %v, want [key-1]", store.deletedKeys)
	}
	if !tt.HasText("No SSH keys yet") {
		t.Fatalf("the list did not reload after delete; texts: %v", texts(t, tt))
	}
}

// TestKeychainCreatesEditsAndDeletesIdentity covers the Identity half: a key
// authentication choice, the shared write, the edit, and the confirmed delete.
func TestKeychainCreatesEditsAndDeletesIdentity(t *testing.T) {
	store := &fakeVault{keys: []domainvault.Key{sampleKey()}}
	shell := New(WithVault(store))
	if err := shell.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	tt := openSectionPage(t, shell, Vault, "Keychain")

	if err := tt.Click("Add identity"); err != nil {
		t.Fatalf("click Add identity: %v", err)
	}
	if err := tt.Click("Label"); err != nil {
		t.Fatalf("focus Label: %v", err)
	}
	tt.Type("phone-identity")
	if err := tt.Click("Username"); err != nil {
		t.Fatalf("focus Username: %v", err)
	}
	tt.Type("deploy")
	if err := tt.Click("SSH key"); err != nil {
		t.Fatalf("choose SSH key auth: %v", err)
	}
	if err := tt.Click("Use key laptop"); err != nil {
		t.Fatalf("select key: %v", err)
	}
	if err := tt.Click("Save identity"); err != nil {
		t.Fatalf("click Save identity: %v", err)
	}

	if len(store.createdIdentities) != 1 {
		t.Fatalf("created identities = %d, want 1", len(store.createdIdentities))
	}
	created := store.createdIdentities[0]
	if created.Label != "phone-identity" || created.Username != "deploy" ||
		created.AuthMethod != domainvault.IdentityAuthKey || created.KeyID != "key-1" {
		t.Fatalf("create draft = %+v, want the typed values", created)
	}
	if !tt.HasText("phone-identity") || !tt.HasText("deploy") || !tt.HasText("key: laptop") {
		t.Fatalf("the created Identity is not listed with its key; texts: %v", texts(t, tt))
	}

	// Edit the username.
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
	if len(store.updatedIdentities) != 1 {
		t.Fatalf("updated identities = %d, want 1", len(store.updatedIdentities))
	}
	if store.updatedIdentities[0].ID != "identity-1" ||
		store.updatedIdentities[0].Draft.Username != "deploy2" {
		t.Fatalf("update draft = %+v, want the edited username", store.updatedIdentities[0])
	}
	if !tt.HasText("deploy2") {
		t.Fatalf("the edited Identity is not listed; texts: %v", texts(t, tt))
	}

	// Delete, with a cancellation first.
	if err := tt.Click("Delete identity phone-identity"); err != nil {
		t.Fatalf("click delete identity: %v", err)
	}
	if !tt.HasText("Delete the identity phone-identity?") {
		t.Fatalf("confirmation is not shown; texts: %v", texts(t, tt))
	}
	if err := tt.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	if len(store.deletedIdentities) != 0 || !tt.HasText("phone-identity") {
		t.Fatalf("cancel removed the Identity: deleted=%v texts=%v", store.deletedIdentities, texts(t, tt))
	}
	if err := tt.Click("Delete identity phone-identity"); err != nil {
		t.Fatalf("click delete identity again: %v", err)
	}
	if err := tt.Click("Delete"); err != nil {
		t.Fatalf("click confirm: %v", err)
	}
	if len(store.deletedIdentities) != 1 || store.deletedIdentities[0] != "identity-1" {
		t.Fatalf("deleted identities = %v, want [identity-1]", store.deletedIdentities)
	}
	if !tt.HasText("No Identities yet") {
		t.Fatalf("the list did not reload after delete; texts: %v", texts(t, tt))
	}
}

// TestKeychainFailuresKeepTheCurrentList covers a failed reload and a failed
// write: both report in place and leave the rows the user already had.
func TestKeychainFailuresKeepTheCurrentList(t *testing.T) {
	store := &fakeVault{
		keys:       []domainvault.Key{sampleKey()},
		identities: []domainvault.Identity{sampleIdentity()},
	}
	shell := New(WithVault(store))
	if err := shell.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	tt := openSectionPage(t, shell, Vault, "Keychain")

	// A failed Identity read is shown on its own section and keeps the row.
	store.identitiesErr = errors.New("identities unreadable")
	if err := tt.Click("Refresh"); err != nil {
		t.Fatalf("click Refresh: %v", err)
	}
	if !tt.HasText("Could not load Identities.") {
		t.Fatalf("Identity read failure is not shown; texts: %v", texts(t, tt))
	}
	if !tt.HasText("ops") || !tt.HasText("laptop") {
		t.Fatalf("a failed read discarded the current rows; texts: %v", texts(t, tt))
	}
	store.identitiesErr = nil

	// A failed Key read is shown on the SSH Key section, not the whole page.
	store.keysErr = errors.New("keys unreadable")
	if err := tt.Click("Refresh"); err != nil {
		t.Fatalf("click Refresh: %v", err)
	}
	if !tt.HasText("Could not load SSH keys.") {
		t.Fatalf("Key read failure is not shown; texts: %v", texts(t, tt))
	}
	if !tt.HasText("laptop") || !tt.HasText("ops") {
		t.Fatalf("a failed Key read discarded the current rows; texts: %v", texts(t, tt))
	}
	store.keysErr = nil

	// A failed Key delete keeps the row and reports in place.
	store.deleteErr = errors.New("write refused")
	if err := tt.Click("Delete key laptop"); err != nil {
		t.Fatalf("click delete key: %v", err)
	}
	if err := tt.Click("Delete"); err != nil {
		t.Fatalf("click confirm delete key: %v", err)
	}
	if !tt.HasText("Could not delete the key.") {
		t.Fatalf("Key delete failure is not shown; texts: %v", texts(t, tt))
	}
	if !tt.HasText("laptop") {
		t.Fatalf("a failed Key delete removed the row; texts: %v", texts(t, tt))
	}
	store.deleteErr = nil

	// A failed Identity delete keeps the row and reports in place.
	store.deleteIdentityErr = errors.New("write refused")
	if err := tt.Click("Delete identity ops"); err != nil {
		t.Fatalf("click delete identity: %v", err)
	}
	if err := tt.Click("Delete"); err != nil {
		t.Fatalf("click confirm delete identity: %v", err)
	}
	if !tt.HasText("Could not delete the Identity.") {
		t.Fatalf("Identity delete failure is not shown; texts: %v", texts(t, tt))
	}
	if !tt.HasText("ops") {
		t.Fatalf("a failed Identity delete removed the row; texts: %v", texts(t, tt))
	}
	store.deleteIdentityErr = nil

	// A successful reload clears both messages and keeps the rows.
	if err := tt.Click("Refresh"); err != nil {
		t.Fatalf("click final Refresh: %v", err)
	}
	if tt.HasText("Could not delete") || tt.HasText("Could not load") {
		t.Fatalf("the failure message survived a successful reload; texts: %v", texts(t, tt))
	}
	if !tt.HasText("laptop") || !tt.HasText("ops") {
		t.Fatalf("a successful reload lost the rows; texts: %v", texts(t, tt))
	}
}

// TestKeychainEditorReportsARejectedWrite keeps the editor open with its typed
// values when the shared service refuses the write.
func TestKeychainEditorReportsARejectedWrite(t *testing.T) {
	store := &fakeVault{createKeyErr: errors.New("not parsed")}
	shell := New(WithVault(store))
	if err := shell.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	tt := openSectionPage(t, shell, Vault, "Keychain")

	if err := tt.Click("Add key"); err != nil {
		t.Fatalf("click Add key: %v", err)
	}
	if err := tt.Click("Label"); err != nil {
		t.Fatalf("focus Label: %v", err)
	}
	tt.Type("broken-key")
	if err := tt.Click("Private key"); err != nil {
		t.Fatalf("focus Private key: %v", err)
	}
	tt.Type(testPrivateMaterial)
	if err := tt.Click("Save key"); err != nil {
		t.Fatalf("click Save key: %v", err)
	}
	if !tt.HasText("Could not save the key.") {
		t.Fatalf("the rejected write is not reported; texts: %v", texts(t, tt))
	}
	if !tt.HasText("New SSH key") {
		t.Fatalf("the editor closed after a rejected write; texts: %v", texts(t, tt))
	}
	// The editor still holds the typed values: saving again sends the same
	// draft instead of an empty one.
	if err := tt.Click("Save key"); err != nil {
		t.Fatalf("click Save key again: %v", err)
	}
	if len(store.attemptedKeys) != 2 {
		t.Fatalf("save attempts = %d, want 2", len(store.attemptedKeys))
	}
	retried := store.attemptedKeys[1]
	if retried.Label != "broken-key" || retried.PrivateKey != testPrivateMaterial {
		t.Fatalf("retried draft = %+v, want the typed values", retried)
	}
	assertNoSecretText(t, tt, testPassphrase, "-----BEGIN", "PRIVATE KEY")
}

// TestKnownHostsShowsFullFingerprintAndIssuingCA pins the audit view: the
// complete fingerprint, the CA marker, and a removal that needs confirmation.
func TestKnownHostsShowsFullFingerprintAndIssuingCA(t *testing.T) {
	record := domainconn.KnownHost{
		ID:                     "known-ca",
		Hostname:               "trusted.example",
		Port:                   22,
		KeyType:                "ssh-ed25519",
		Fingerprint:            "SHA256:fullCertificateAuthorityFingerprintForAudit012345",
		IsCertificateAuthority: true,
	}
	store := &fakeVault{known: []domainconn.KnownHost{record}}
	shell := New(WithVault(store))
	if err := shell.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	tt := openSectionPage(t, shell, Vault, "Known hosts")

	for _, want := range []string{"trusted.example:22", "Issuing CA", "ssh-ed25519", record.Fingerprint} {
		if !tt.HasText(want) {
			t.Fatalf("Known hosts page does not show %q; texts: %v", want, texts(t, tt))
		}
	}

	// Removal is confirmed, and cancel keeps the trust decision.
	if err := tt.Click("Forget trusted.example:22"); err != nil {
		t.Fatalf("click Forget: %v", err)
	}
	if !tt.HasText("Forget trusted.example:22?") {
		t.Fatalf("confirmation is not shown; texts: %v", texts(t, tt))
	}
	if err := tt.Click("Cancel"); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	if len(store.deletedHosts) != 0 || !tt.HasText("trusted.example:22") {
		t.Fatalf("cancel removed the record: deleted=%v texts=%v", store.deletedHosts, texts(t, tt))
	}

	if err := tt.Click("Forget trusted.example:22"); err != nil {
		t.Fatalf("click Forget again: %v", err)
	}
	if err := tt.Click("Forget"); err != nil {
		t.Fatalf("click confirm: %v", err)
	}
	if len(store.deletedHosts) != 1 || store.deletedHosts[0] != "known-ca" {
		t.Fatalf("deleted hosts = %v, want [known-ca]", store.deletedHosts)
	}
	if !tt.HasText("No known hosts yet") {
		t.Fatalf("the list did not reload after forget; texts: %v", texts(t, tt))
	}
}

// TestKnownHostsFailureKeepsTheList is the trust view's copy of the same rule.
func TestKnownHostsFailureKeepsTheList(t *testing.T) {
	store := &fakeVault{known: []domainconn.KnownHost{sampleKnownHost()}}
	shell := New(WithVault(store))
	if err := shell.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	tt := openSectionPage(t, shell, Vault, "Known hosts")

	store.hostsErr = errors.New("known hosts unreadable")
	if err := tt.Click("Refresh"); err != nil {
		t.Fatalf("click Refresh: %v", err)
	}
	if !tt.HasText("Could not load known hosts.") {
		t.Fatalf("the read failure is not shown; texts: %v", texts(t, tt))
	}
	if !tt.HasText("example.com:22") || !tt.HasText(sampleKnownHost().Fingerprint) {
		t.Fatalf("a failed read discarded the rows; texts: %v", texts(t, tt))
	}
	store.hostsErr = nil

	store.deleteErr = errors.New("write refused")
	if err := tt.Click("Forget example.com:22"); err != nil {
		t.Fatalf("click Forget: %v", err)
	}
	if err := tt.Click("Forget"); err != nil {
		t.Fatalf("click confirm: %v", err)
	}
	if !tt.HasText("Could not forget the known host.") {
		t.Fatalf("the write failure is not shown; texts: %v", texts(t, tt))
	}
	if !tt.HasText("example.com:22") {
		t.Fatalf("a failed write removed the row; texts: %v", texts(t, tt))
	}
}

// TestIdentityEditorNeverRendersAStoredPassword keeps the credential out of
// the editor and the list even when the record carries one.
func TestIdentityEditorNeverRendersAStoredPassword(t *testing.T) {
	const storedPassword = "stored-identity-password-never-rendered"
	identity := sampleIdentity()
	identity.AuthMethod = domainvault.IdentityAuthPassword
	identity.KeyID = ""
	identity.HasPassword = true
	store := &fakeVault{identities: []domainvault.Identity{identity}}
	shell := New(WithVault(store))
	if err := shell.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	tt := openSectionPage(t, shell, Vault, "Keychain")
	// The page only knows that a password exists.
	if !tt.HasText("password saved") {
		t.Fatalf("the stored-password flag is not shown; texts: %v", texts(t, tt))
	}
	assertNoSecretText(t, tt, storedPassword)
	if err := tt.Click("Edit identity ops"); err != nil {
		t.Fatalf("click Edit identity: %v", err)
	}
	assertNoSecretText(t, tt, storedPassword)
	if !tt.HasText("Leave blank to keep the stored password.") {
		t.Fatalf("the editor does not explain the kept password; texts: %v", texts(t, tt))
	}
	// Saving the untouched editor keeps the credential and clears nothing.
	if err := tt.Click("Save identity"); err != nil {
		t.Fatalf("click Save identity: %v", err)
	}
	if len(store.updatedIdentities) != 1 {
		t.Fatalf("updated identities = %d, want 1", len(store.updatedIdentities))
	}
	draft := store.updatedIdentities[0].Draft
	if draft.Password != "" || draft.ClearPassword {
		t.Fatalf("an untouched editor touched the password: %+v", draft)
	}
	assertNoSecretText(t, tt, storedPassword)
}
