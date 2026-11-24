package app

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/adapters/store"
	apppreferences "github.com/mslxl/natcatty/application/preferences"
	appsync "github.com/mslxl/natcatty/application/sync"
	appterminalsettings "github.com/mslxl/natcatty/application/terminalsettings"
	appvault "github.com/mslxl/natcatty/application/vault"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	platformsecrets "github.com/mslxl/natcatty/platform/secrets"
	"github.com/mslxl/natcatty/ui/phoneshell"
)

// flakyKeyStore is the platform credential store seam. It reports the platform
// "unsupported" state while unavailable, then the same field key again after
// recovery, which is the outage-and-retry story the ticket asks for.
type flakyKeyStore struct {
	mu        sync.Mutex
	available bool
	value     []byte
}

func (f *flakyKeyStore) Get(string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.available {
		return nil, platformsecrets.ErrUnsupported
	}
	if f.value == nil {
		return nil, platformsecrets.ErrNotFound
	}
	return append([]byte(nil), f.value...), nil
}

func (f *flakyKeyStore) Set(_ string, value []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.value = append([]byte(nil), value...)
	return nil
}

func (f *flakyKeyStore) Delete(string) error { return nil }

func (f *flakyKeyStore) setAvailable(available bool) {
	f.mu.Lock()
	f.available = available
	f.mu.Unlock()
}

// phoneCredentialTestSync is the Vault Sync engine seam. The test only needs
// the Settings page to recognize an attached engine; no request is made.
type phoneCredentialTestSync struct{}

func (*phoneCredentialTestSync) Snapshot() appsync.Snapshot              { return appsync.Snapshot{} }
func (*phoneCredentialTestSync) SetPassword(string) error                { return nil }
func (*phoneCredentialTestSync) SetAutoSync(context.Context, bool) error { return nil }
func (*phoneCredentialTestSync) ConnectWebDAV(context.Context, string, appsync.WebDAVSettings, bool) error {
	return nil
}
func (*phoneCredentialTestSync) ConnectS3(context.Context, string, appsync.S3Settings, bool) error {
	return nil
}
func (*phoneCredentialTestSync) StartGitHubConnect(string) error          { return nil }
func (*phoneCredentialTestSync) StartBrowserConnect(string, string) error { return nil }
func (*phoneCredentialTestSync) CancelGitHubConnect()                     {}
func (*phoneCredentialTestSync) CancelBrowserConnect()                    {}
func (*phoneCredentialTestSync) SyncNow()                                 {}
func (*phoneCredentialTestSync) Disconnect(context.Context) error         { return nil }

// TestPhoneCredentialOutageKeepsVaultUsableAndRecoversOnForeground drives the
// whole phone behavior over the real Vault service and SQLite store:
//
//   - the Vault home states the outage;
//   - non-secret records stay readable and writable;
//   - Vault Sync is unavailable and says so;
//   - a Key delete still removes its encrypted material from the same Vault;
//   - returning to the foreground reopens the credential store, clears the
//     notice, and makes Vault Sync available again.
func TestPhoneCredentialOutageKeepsVaultUsableAndRecoversOnForeground(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "natcatty.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	keys := &flakyKeyStore{available: true}

	// Seed a Key and its encrypted private material as a previous unlocked run
	// would have left them, then take the credential service away.
	seed, err := store.OpenSecretStore(ctx, db, keys)
	if err != nil {
		t.Fatalf("open seed secret store: %v", err)
	}
	if err := seed.Set(ctx, appvault.KeySecretRef("phone-key", appvault.FieldPrivateKey), "seed-private-material"); err != nil {
		t.Fatalf("seed private material: %v", err)
	}
	seed.Close()
	if err := db.SaveKey(ctx, domainvault.Key{
		ID:        "phone-key",
		Label:     "phone-key",
		Type:      domainvault.KeyTypeED25519,
		PublicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPhoneKey phone-key",
	}); err != nil {
		t.Fatalf("save key: %v", err)
	}
	keys.setAvailable(false)

	credentials := newPhoneCredentials(db, keys)
	if credentials.Retry(ctx) {
		t.Fatal("the credential service opened while the platform store was unavailable")
	}
	vault := newPhoneVault(db, credentials)

	preferences := apppreferences.NewService(store.OpenPreferences(filepath.Join(dir, "preferences.json")))
	if err := preferences.Load(ctx); err != nil {
		t.Fatalf("load preferences: %v", err)
	}
	terminal := appterminalsettings.NewService(store.OpenTerminalSettings(filepath.Join(dir, "terminal-settings.json")))
	if err := terminal.Load(ctx); err != nil {
		t.Fatalf("load terminal settings: %v", err)
	}

	shell := phoneshell.New(
		phoneshell.WithVault(vault),
		phoneshell.WithCredentialService(credentials),
		phoneshell.WithSettings(phoneshell.NewSettingsService(preferences, terminal)),
	)
	if err := shell.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Acceptance 1: the Vault home states the outage with a clear reason.
	home := ui.NewTester(shell.View(phoneshell.Vault), 390, 844)
	for _, want := range []string{"system credential store is unavailable", "Vault Sync is paused"} {
		if !home.HasText(want) {
			t.Fatalf("the Vault home does not say %q; texts: %v", want, home.Texts())
		}
	}

	// Acceptance 2a: non-secret records remain writable and readable.
	host, err := vault.Create(ctx, domainvault.Draft{
		Label: "prod box", Hostname: "prod.example.com", Protocol: domainvault.ProtocolSSH,
	})
	if err != nil {
		t.Fatalf("create Host during the outage: %v", err)
	}
	if err := shell.Load(ctx); err != nil {
		t.Fatalf("reload during the outage: %v", err)
	}
	keychain := phonePage(t, shell, phoneshell.Vault, "Keychain")
	if !keychain.HasText("phone-key") {
		t.Fatalf("the shared Key is not listed during the outage; texts: %v", keychain.Texts())
	}

	// Secrets fail closed rather than falling back to an insecure store.
	if err := vault.SetSecret(ctx, host.ID, appvault.FieldPassword, "hunter2"); !errors.Is(err, appvault.ErrSecretsUnavailable) {
		t.Fatalf("SetSecret during the outage = %v, want ErrSecretsUnavailable", err)
	}
	// Acceptance 2c: Key deletion still acts on the same Vault.
	if err := keychain.Click("Delete key phone-key"); err != nil {
		t.Fatalf("click delete key: %v", err)
	}
	if err := keychain.Click("Delete"); err != nil {
		t.Fatalf("confirm delete key: %v", err)
	}
	if remaining, err := db.ListKeys(ctx); err != nil {
		t.Fatalf("list keys: %v", err)
	} else if len(remaining) != 0 {
		t.Fatalf("the deleted Key is still in the shared Vault: %+v", remaining)
	}

	// Acceptance 2b: Vault Sync is unavailable and says so.
	settings := phonePage(t, shell, phoneshell.Settings, "Vault sync")
	if !settings.HasText("Vault sync is not available on this device.") {
		t.Fatalf("the Vault sync page does not report its disabled state; texts: %v", settings.Texts())
	}

	// Acceptance 3: returning to the foreground retries; success clears the
	// notice and attaches the engine.
	keys.setAvailable(true)
	var attached phoneshell.VaultSyncService
	credentials.SetOnAvailable(func(*store.SecretStore) {
		attached = &phoneCredentialTestSync{}
		shell.AttachVaultSync(attached)
	})
	shell.Foreground()

	if available, reason := credentials.Status(); !available {
		t.Fatalf("the credential service did not recover on foreground: %q", reason)
	}
	if attached == nil {
		t.Fatal("the recovery did not attach Vault Sync")
	}
	home.Frame()
	if home.HasText("system credential store is unavailable") {
		t.Fatalf("the outage notice survived the recovery; texts: %v", home.Texts())
	}
	settings.Frame()
	if !settings.HasText("Sync provider") {
		t.Fatalf("Vault Sync did not become usable after recovery; texts: %v", settings.Texts())
	}

	// The deleted Key's encrypted material is gone from the same Vault. The
	// recovered store still holds the same field key, so a surviving row would
	// decrypt again.
	if _, err := credentials.Get(ctx, appvault.KeySecretRef("phone-key", appvault.FieldPrivateKey)); !errors.Is(err, appvault.ErrSecretNotFound) {
		t.Fatalf("deleted Key secret = %v, want ErrSecretNotFound", err)
	}
}

// keychainErrorStore is a platform credential store that fails with an
// arbitrary OS error while locked, the shape iOS and Android report for a
// keychain/keystore that cannot be read ("mygo: keychain error <status>",
// "mygo: keystore: ..."). It is deliberately not ErrUnsupported: the platform
// store exists, it is only temporarily unavailable, so OpenSecretStore returns
// an error and never hands back a store at all.
type keychainErrorStore struct {
	mu        sync.Mutex
	available bool
	value     []byte
}

func (k *keychainErrorStore) Get(string) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if !k.available {
		return nil, errors.New("mygo: keychain error -25308")
	}
	if k.value == nil {
		return nil, platformsecrets.ErrNotFound
	}
	return append([]byte(nil), k.value...), nil
}

func (k *keychainErrorStore) Set(_ string, value []byte) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if !k.available {
		return errors.New("mygo: keychain error -25308")
	}
	k.value = append([]byte(nil), value...)
	return nil
}

func (k *keychainErrorStore) Delete(string) error { return nil }

func (k *keychainErrorStore) setAvailable(available bool) {
	k.mu.Lock()
	k.available = available
	k.mu.Unlock()
}

// TestPhoneCredentialTransientKeychainErrorStillDeletesCiphertext drives the
// real mobile outage shape: the platform store rejects reads with an arbitrary
// keychain error, so there is no SecretStore to carry a keyless delete. An
// explicit Key delete and a cleared Host secret must still remove their exact
// ciphertext rows from the same Vault, keep other rows, and leave nothing that
// decrypts after recovery. Real credential writes stay failed closed and the
// Vault home keeps its outage notice.
func TestPhoneCredentialTransientKeychainErrorStillDeletesCiphertext(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "natcatty.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	keys := &keychainErrorStore{available: true}

	// Seed a Host secret, a Key's encrypted material, and a second field that
	// must survive, as a previous unlocked run would have left them.
	seed, err := store.OpenSecretStore(ctx, db, keys)
	if err != nil {
		t.Fatalf("open seed secret store: %v", err)
	}
	seedVault := newPhoneVault(db, seed)
	host, err := seedVault.Create(ctx, domainvault.Draft{
		Label: "prod box", Hostname: "prod.example.com", Protocol: domainvault.ProtocolSSH,
	})
	if err != nil {
		t.Fatalf("create Host: %v", err)
	}
	if err := seedVault.SetSecret(ctx, host.ID, appvault.FieldPassword, "hunter2"); err != nil {
		t.Fatalf("seed Host password: %v", err)
	}
	if err := seed.Set(ctx, appvault.KeySecretRef("phone-key", appvault.FieldPrivateKey), "seed-private-material"); err != nil {
		t.Fatalf("seed private material: %v", err)
	}
	if err := seed.Set(ctx, appvault.HostSecretRef("kept-host", appvault.FieldPassword), "keep-me"); err != nil {
		t.Fatalf("seed neighbouring secret: %v", err)
	}
	seed.Close()
	if err := db.SaveKey(ctx, domainvault.Key{
		ID:        "phone-key",
		Label:     "phone-key",
		Type:      domainvault.KeyTypeED25519,
		PublicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPhoneKey phone-key",
	}); err != nil {
		t.Fatalf("save key: %v", err)
	}

	// The keychain is now locked: every read returns a platform error, not
	// ErrUnsupported.
	keys.setAvailable(false)

	credentials := newPhoneCredentials(db, keys)
	if credentials.Retry(ctx) {
		t.Fatal("the credential service opened while the keychain errored")
	}
	if mode := credentials.Mode(); mode != appvault.SecretModeUnavailable {
		t.Fatalf("credential mode = %q, want unavailable", mode)
	}
	vault := newPhoneVault(db, credentials)

	shell := phoneshell.New(
		phoneshell.WithVault(vault),
		phoneshell.WithCredentialService(credentials),
	)
	if err := shell.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}
	home := ui.NewTester(shell.View(phoneshell.Vault), 390, 844)
	if !home.HasText("system credential store is unavailable") {
		t.Fatalf("the Vault home does not state the outage; texts: %v", home.Texts())
	}

	// Non-secret records stay usable.
	other, err := vault.Create(ctx, domainvault.Draft{
		Label: "other box", Hostname: "other.example.com", Protocol: domainvault.ProtocolSSH,
	})
	if err != nil {
		t.Fatalf("create Host during the outage: %v", err)
	}

	// Writing a real credential still fails closed, not into an insecure store.
	if err := vault.SetSecret(ctx, host.ID, appvault.FieldPassword, "nope"); !errors.Is(err, appvault.ErrSecretsUnavailable) {
		t.Fatalf("SetSecret during the outage = %v, want ErrSecretsUnavailable", err)
	}

	// Clearing a Host secret deletes its ciphertext through the same Vault.
	if err := vault.SetSecret(ctx, host.ID, appvault.FieldPassword, ""); err != nil {
		t.Fatalf("clear Host password during the outage: %v", err)
	}
	cleared, err := vault.Get(ctx, host.ID)
	if err != nil {
		t.Fatalf("read Host after clearing its password: %v", err)
	}
	if cleared.HasPassword {
		t.Fatal("the cleared Host still reports a saved password")
	}

	// Deleting a Key removes its record and its ciphertext.
	if err := vault.DeleteKey(ctx, "phone-key"); err != nil {
		t.Fatalf("delete Key during the outage: %v", err)
	}
	if remaining, err := db.ListKeys(ctx); err != nil {
		t.Fatalf("list keys: %v", err)
	} else if len(remaining) != 0 {
		t.Fatalf("the deleted Key is still in the shared Vault: %+v", remaining)
	}

	// Recovery: the same Vault now has its field key back. The deleted rows must
	// not reappear; the neighbouring row must still decrypt.
	keys.setAvailable(true)
	if !credentials.Retry(ctx) {
		t.Fatal("the credential service did not recover")
	}
	if _, err := credentials.Get(ctx, appvault.KeySecretRef("phone-key", appvault.FieldPrivateKey)); !errors.Is(err, appvault.ErrSecretNotFound) {
		t.Fatalf("deleted Key secret = %v, want ErrSecretNotFound", err)
	}
	if _, err := credentials.Get(ctx, appvault.HostSecretRef(host.ID, appvault.FieldPassword)); !errors.Is(err, appvault.ErrSecretNotFound) {
		t.Fatalf("cleared Host secret = %v, want ErrSecretNotFound", err)
	}
	if value, err := credentials.Get(ctx, appvault.HostSecretRef("kept-host", appvault.FieldPassword)); err != nil {
		t.Fatalf("neighbouring secret was deleted too: %v", err)
	} else if value != "keep-me" {
		t.Fatalf("neighbouring secret = %q, want %q", value, "keep-me")
	}

	home.Frame()
	if home.HasText("system credential store is unavailable") {
		t.Fatalf("the outage notice survived the recovery; texts: %v", home.Texts())
	}
	if _, err := vault.Get(ctx, other.ID); err != nil {
		t.Fatalf("non-secret Host did not survive the outage: %v", err)
	}
}
