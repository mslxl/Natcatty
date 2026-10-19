package store

import (
	"bytes"
	"context"
	"errors"
	"testing"

	platformsecrets "github.com/mslxl/natcatty/platform/secrets"

	appvault "github.com/mslxl/natcatty/application/vault"
)

// fakeKeyStore is the OS credential store the secret adapter is tested
// against. It never touches a real keychain.
type fakeKeyStore struct {
	values      map[string][]byte
	unsupported bool
}

func newFakeKeyStore() *fakeKeyStore {
	return &fakeKeyStore{values: map[string][]byte{}}
}

func (f *fakeKeyStore) Get(key string) ([]byte, error) {
	if f.unsupported {
		return nil, platformsecrets.ErrUnsupported
	}
	value, ok := f.values[key]
	if !ok {
		return nil, platformsecrets.ErrNotFound
	}
	return append([]byte(nil), value...), nil
}

func (f *fakeKeyStore) Set(key string, value []byte) error {
	if f.unsupported {
		return platformsecrets.ErrUnsupported
	}
	f.values[key] = append([]byte(nil), value...)
	return nil
}

func (f *fakeKeyStore) Delete(key string) error {
	delete(f.values, key)
	return nil
}

func openTestSecretStore(t *testing.T, keys platformsecrets.Store, opts ...SecretOption) (*Store, *SecretStore) {
	t.Helper()
	db, _ := openTestStore(t)
	options := append([]SecretOption{WithArgon2Params(1, 1024, 1)}, opts...)
	secrets, err := OpenSecretStore(context.Background(), db, keys, options...)
	if err != nil {
		t.Fatalf("OpenSecretStore: %v", err)
	}
	t.Cleanup(secrets.Close)
	return db, secrets
}

var testHostRef = appvault.SecretRef{EntityType: appvault.EntityHost, EntityID: "host-1", Field: appvault.FieldPassword}

func TestOSModeCreatesAndKeepsTheFieldKey(t *testing.T) {
	keys := newFakeKeyStore()
	_, secrets := openTestSecretStore(t, keys)

	if got := secrets.Mode(); got != appvault.SecretModeOS {
		t.Fatalf("Mode = %q, want os", got)
	}
	key := keys.values[fieldKeyName]
	if len(key) != 32 {
		t.Fatalf("field key is %d bytes, want 32", len(key))
	}

	// Reopening with the same OS store reuses the key.
	db, _ := openTestStore(t)
	reopened, err := OpenSecretStore(context.Background(), db, keys)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if !bytes.Equal(reopened.key, key) {
		t.Fatal("reopening generated a different field key")
	}
}

func TestSecretRoundTripsAndIsCiphertextAtRest(t *testing.T) {
	keys := newFakeKeyStore()
	db, secrets := openTestSecretStore(t, keys)
	ctx := context.Background()

	if err := secrets.Set(ctx, testHostRef, "hunter2"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	value, err := secrets.Get(ctx, testHostRef)
	if err != nil || value != "hunter2" {
		t.Fatalf("Get = %q, %v, want hunter2", value, err)
	}

	row, found, err := db.loadSecretValue(ctx, testHostRef)
	if err != nil || !found {
		t.Fatalf("loadSecretValue = %+v, %v, found %v", row, err, found)
	}
	if row.Alg != secretAlgorithm {
		t.Errorf("alg = %q, want %q", row.Alg, secretAlgorithm)
	}
	if row.KeyVersion != currentKeyVersion {
		t.Errorf("key version = %d, want %d", row.KeyVersion, currentKeyVersion)
	}
	if len(row.Nonce) != nonceBytes {
		t.Errorf("nonce is %d bytes, want %d", len(row.Nonce), nonceBytes)
	}
	if len(row.Ciphertext) < len("hunter2")+tagBytes {
		t.Errorf("ciphertext is %d bytes, too short for plaintext plus tag", len(row.Ciphertext))
	}
	if bytes.Contains(row.Ciphertext, []byte("hunter2")) {
		t.Fatal("the ciphertext contains the password")
	}

	// The key is the only thing that makes the row readable.
	reopened, err := OpenSecretStore(ctx, db, keys)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got, err := reopened.Get(ctx, testHostRef); err != nil || got != "hunter2" {
		t.Fatalf("Get after reopen = %q, %v", got, err)
	}
}

func TestEachWriteUsesAFreshNonce(t *testing.T) {
	db, secrets := openTestSecretStore(t, newFakeKeyStore())
	ctx := context.Background()
	if err := secrets.Set(ctx, testHostRef, "same"); err != nil {
		t.Fatal(err)
	}
	first, _, _ := db.loadSecretValue(ctx, testHostRef)
	if err := secrets.Set(ctx, testHostRef, "same"); err != nil {
		t.Fatal(err)
	}
	second, _, _ := db.loadSecretValue(ctx, testHostRef)

	if bytes.Equal(first.Nonce, second.Nonce) {
		t.Fatal("two writes reused a nonce")
	}
	if bytes.Equal(first.Ciphertext, second.Ciphertext) {
		t.Fatal("two writes produced the same ciphertext")
	}
}

func TestTamperedCiphertextIsUnreadableAndPreserved(t *testing.T) {
	keys := newFakeKeyStore()
	db, secrets := openTestSecretStore(t, keys)
	ctx := context.Background()
	if err := secrets.Set(ctx, testHostRef, "hunter2"); err != nil {
		t.Fatal(err)
	}
	row, _, _ := db.loadSecretValue(ctx, testHostRef)
	tampered := append([]byte(nil), row.Ciphertext...)
	tampered[len(tampered)-1] ^= 0xff
	if _, err := db.db.Execute(ctx, `UPDATE secret_values SET ciphertext = ? WHERE entity_type = ? AND entity_id = ? AND field = ?`,
		tampered, row.Ref.EntityType, row.Ref.EntityID, row.Ref.Field); err != nil {
		t.Fatal(err)
	}

	if _, err := secrets.Get(ctx, testHostRef); !errors.Is(err, appvault.ErrSecretUnreadable) {
		t.Fatalf("Get = %v, want ErrSecretUnreadable", err)
	}
	after, _, _ := db.loadSecretValue(ctx, testHostRef)
	if !bytes.Equal(after.Ciphertext, tampered) {
		t.Fatal("the unreadable ciphertext was overwritten")
	}
}

func TestCiphertextIsBoundToItsField(t *testing.T) {
	db, secrets := openTestSecretStore(t, newFakeKeyStore())
	ctx := context.Background()
	if err := secrets.Set(ctx, testHostRef, "hunter2"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Execute(ctx, `UPDATE secret_values SET field = ? WHERE entity_type = ? AND entity_id = ? AND field = ?`,
		appvault.FieldPassphrase, testHostRef.EntityType, testHostRef.EntityID, testHostRef.Field); err != nil {
		t.Fatal(err)
	}

	moved := testHostRef
	moved.Field = appvault.FieldPassphrase
	if _, err := secrets.Get(ctx, moved); !errors.Is(err, appvault.ErrSecretUnreadable) {
		t.Fatalf("Get moved ciphertext = %v, want ErrSecretUnreadable", err)
	}
}

func TestCiphertextIsBoundToItsEntity(t *testing.T) {
	db, secrets := openTestSecretStore(t, newFakeKeyStore())
	ctx := context.Background()
	if err := secrets.Set(ctx, testHostRef, "hunter2"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Execute(ctx, `UPDATE secret_values SET entity_id = ? WHERE entity_type = ? AND entity_id = ? AND field = ?`,
		"host-2", testHostRef.EntityType, testHostRef.EntityID, testHostRef.Field); err != nil {
		t.Fatal(err)
	}

	moved := testHostRef
	moved.EntityID = "host-2"
	if _, err := secrets.Get(ctx, moved); !errors.Is(err, appvault.ErrSecretUnreadable) {
		t.Fatalf("Get moved ciphertext = %v, want ErrSecretUnreadable", err)
	}
}

func TestDeleteRemovesTheRow(t *testing.T) {
	db, secrets := openTestSecretStore(t, newFakeKeyStore())
	ctx := context.Background()
	if err := secrets.Set(ctx, testHostRef, "hunter2"); err != nil {
		t.Fatal(err)
	}
	if err := secrets.Delete(ctx, testHostRef); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := secrets.Get(ctx, testHostRef); !errors.Is(err, appvault.ErrSecretNotFound) {
		t.Fatalf("Get after delete = %v, want ErrSecretNotFound", err)
	}
	if _, found, err := db.loadSecretValue(ctx, testHostRef); err != nil || found {
		t.Fatalf("row found = %v, err = %v, want deleted", found, err)
	}
}

func TestMissingValueIsNotFound(t *testing.T) {
	_, secrets := openTestSecretStore(t, newFakeKeyStore())
	if _, err := secrets.Get(context.Background(), testHostRef); !errors.Is(err, appvault.ErrSecretNotFound) {
		t.Fatalf("Get = %v, want ErrSecretNotFound", err)
	}
}

func TestNoOSStoreNeedsTheUserToChooseAMode(t *testing.T) {
	_, secrets := openTestSecretStore(t, &fakeKeyStore{unsupported: true})
	if got := secrets.Mode(); got != appvault.SecretModeUnavailable {
		t.Fatalf("Mode = %q, want unavailable", got)
	}
	if err := secrets.Set(context.Background(), testHostRef, "hunter2"); !errors.Is(err, appvault.ErrSecretsUnavailable) {
		t.Fatalf("Set = %v, want ErrSecretsUnavailable", err)
	}
}

func TestPassphraseModeWrapsAndUnlocksTheFieldKey(t *testing.T) {
	db, secrets := openTestSecretStore(t, &fakeKeyStore{unsupported: true})
	ctx := context.Background()
	if err := secrets.SetPassphrase(ctx, "correct horse"); err != nil {
		t.Fatalf("SetPassphrase: %v", err)
	}
	if got := secrets.Mode(); got != appvault.SecretModePassphrase {
		t.Fatalf("Mode = %q, want passphrase", got)
	}
	if err := secrets.Set(ctx, testHostRef, "hunter2"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	row, found, err := db.loadCryptoKey(ctx, "field")
	if err != nil || !found {
		t.Fatalf("wrapped key found = %v, err = %v", found, err)
	}
	if row.WrapKDF != "argon2id" || len(row.WrapSalt) != wrapSaltBytes {
		t.Fatalf("wrapped key metadata = %+v", row)
	}
	if bytes.Contains(row.WrappedKey, secrets.key) {
		t.Fatal("the wrapped key contains the raw field key")
	}

	reopened, err := OpenSecretStore(ctx, db, &fakeKeyStore{unsupported: true}, WithArgon2Params(1, 1024, 1))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got := reopened.Mode(); got != appvault.SecretModePassphraseLocked {
		t.Fatalf("reopened Mode = %q, want passphrase_locked", got)
	}
	if _, err := reopened.Get(ctx, testHostRef); !errors.Is(err, appvault.ErrSecretsLocked) {
		t.Fatalf("Get while locked = %v, want ErrSecretsLocked", err)
	}
	if err := reopened.UnlockWithPassphrase(ctx, "wrong"); !errors.Is(err, appvault.ErrWrongPassphrase) {
		t.Fatalf("Unlock(wrong) = %v, want ErrWrongPassphrase", err)
	}
	if err := reopened.UnlockWithPassphrase(ctx, "correct horse"); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if got, err := reopened.Get(ctx, testHostRef); err != nil || got != "hunter2" {
		t.Fatalf("Get after unlock = %q, %v", got, err)
	}
}

func TestAWrappedKeySurvivesAMissingOSKey(t *testing.T) {
	db, secrets := openTestSecretStore(t, &fakeKeyStore{unsupported: true})
	ctx := context.Background()
	if err := secrets.SetPassphrase(ctx, "correct horse"); err != nil {
		t.Fatal(err)
	}
	if err := secrets.Set(ctx, testHostRef, "hunter2"); err != nil {
		t.Fatal(err)
	}

	// The OS store is now present but has no field key. The wrapped key
	// must win over generating a fresh one.
	reopened, err := OpenSecretStore(ctx, db, newFakeKeyStore(), WithArgon2Params(1, 1024, 1))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got := reopened.Mode(); got != appvault.SecretModePassphraseLocked {
		t.Fatalf("Mode = %q, want passphrase_locked", got)
	}
	if err := reopened.UnlockWithPassphrase(ctx, "correct horse"); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if got, err := reopened.Get(ctx, testHostRef); err != nil || got != "hunter2" {
		t.Fatalf("Get = %q, %v", got, err)
	}
}

func TestPassphraseSetupCannotRunTwice(t *testing.T) {
	_, secrets := openTestSecretStore(t, &fakeKeyStore{unsupported: true})
	ctx := context.Background()
	if err := secrets.SetPassphrase(ctx, "pass"); err != nil {
		t.Fatal(err)
	}
	if err := secrets.SetPassphrase(ctx, "another"); !errors.Is(err, appvault.ErrPassphraseAlreadySet) {
		t.Fatalf("second SetPassphrase = %v, want ErrPassphraseAlreadySet", err)
	}
}

func TestEmptyPassphraseIsRejected(t *testing.T) {
	_, secrets := openTestSecretStore(t, &fakeKeyStore{unsupported: true})
	if err := secrets.SetPassphrase(context.Background(), ""); !errors.Is(err, appvault.ErrPassphraseRequired) {
		t.Fatalf("SetPassphrase = %v, want ErrPassphraseRequired", err)
	}
}

func TestSessionOnlyModeWritesNoSecretRows(t *testing.T) {
	db, secrets := openTestSecretStore(t, &fakeKeyStore{unsupported: true})
	ctx := context.Background()
	if err := secrets.UseSessionOnly(ctx); err != nil {
		t.Fatalf("UseSessionOnly: %v", err)
	}
	if got := secrets.Mode(); got != appvault.SecretModeSession {
		t.Fatalf("Mode = %q, want session", got)
	}
	if err := secrets.Set(ctx, testHostRef, "hunter2"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got, err := secrets.Get(ctx, testHostRef); err != nil || got != "hunter2" {
		t.Fatalf("Get = %q, %v", got, err)
	}

	rows, err := db.db.Query(ctx, `SELECT COUNT(*) FROM secret_values`)
	if err != nil {
		t.Fatal(err)
	}
	if count := rows.Values[0][0].(int64); count != 0 {
		t.Fatalf("secret_values rows = %d, want 0 in session mode", count)
	}

	// A restart without a wrapped key offers the choice again and has no
	// credential to restore.
	reopened, err := OpenSecretStore(ctx, db, &fakeKeyStore{unsupported: true})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got := reopened.Mode(); got != appvault.SecretModeUnavailable {
		t.Fatalf("reopened Mode = %q, want unavailable", got)
	}
}

func TestSecretReferencesAreValidated(t *testing.T) {
	_, secrets := openTestSecretStore(t, newFakeKeyStore())
	ctx := context.Background()
	if err := secrets.Set(ctx, appvault.SecretRef{}, "value"); err == nil {
		t.Fatal("Set accepted an empty reference")
	}
	if _, err := secrets.Get(ctx, appvault.SecretRef{}); err == nil {
		t.Fatal("Get accepted an empty reference")
	}
}
