package store

import (
	"bytes"
	"context"
	"testing"

	historysync "github.com/mslxl/natcatty/application/historysync"
	appvault "github.com/mslxl/natcatty/application/vault"
)

func TestHistorySecretsUseTheFieldStoreAndEncryptAtRest(t *testing.T) {
	db, fieldStore := openTestSecretStore(t, newFakeKeyStore())
	secrets := OpenHistorySecrets(fieldStore)
	ctx := context.Background()

	if got := secrets.Mode(); got != historysync.SecretModeOS {
		t.Fatalf("Mode = %q, want os", got)
	}
	ref := historysync.MasterKeyRef()
	if has, err := secrets.Has(ctx, ref); err != nil || has {
		t.Fatalf("Has before Set = %v, %v, want false, nil", has, err)
	}

	const plaintext = "not-a-real-key-but-must-not-touch-disk"
	if err := secrets.Set(ctx, ref, plaintext); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if has, err := secrets.Has(ctx, ref); err != nil || !has {
		t.Fatalf("Has after Set = %v, %v, want true, nil", has, err)
	}
	value, err := secrets.Get(ctx, ref)
	if err != nil || value != plaintext {
		t.Fatalf("Get = %q, %v, want %q", value, err, plaintext)
	}

	row, found, err := db.loadSecretValue(ctx, appvault.SecretRef(ref))
	if err != nil || !found {
		t.Fatalf("loadSecretValue = %+v, %v, found %v", row, err, found)
	}
	if bytes.Contains(row.Ciphertext, []byte(plaintext)) {
		t.Fatal("field-store ciphertext contains the plaintext credential")
	}
	if row.Alg != secretAlgorithm || row.KeyVersion != currentKeyVersion {
		t.Fatalf("row = %+v, want the field-store algorithm and key version", row)
	}

	if err := secrets.Delete(ctx, ref); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if has, err := secrets.Has(ctx, ref); err != nil || has {
		t.Fatalf("Has after Delete = %v, %v, want false, nil", has, err)
	}
}

func TestHistorySecretsReportAMissingCredential(t *testing.T) {
	_, fieldStore := openTestSecretStore(t, newFakeKeyStore())
	secrets := OpenHistorySecrets(fieldStore)
	if _, err := secrets.Get(context.Background(), historysync.SessionTokenRef()); err != historysync.ErrSecretNotFound {
		t.Fatalf("Get(missing) = %v, want ErrSecretNotFound", err)
	}
}
