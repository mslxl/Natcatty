package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	appvault "github.com/mslxl/natcatty/application/vault"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

func TestKeysSurviveReopenWithoutMaterialInTheRecord(t *testing.T) {
	ctx := context.Background()
	store, _ := openTestStore(t)
	now := time.Unix(1700000000, 0).UTC()
	key := domainvault.Key{
		ID:            "key-1",
		Label:         "work",
		Type:          domainvault.KeyTypeED25519,
		PublicKey:     "ssh-ed25519 AAAA",
		Certificate:   "ssh-ed25519-cert-v01@openssh.com AAAA",
		Source:        domainvault.KeySourceImported,
		HasPrivateKey: true,
		HasPassphrase: true,
		OrderKey:      2000,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := store.SaveKey(ctx, key); err != nil {
		t.Fatalf("SaveKey: %v", err)
	}

	got, err := store.GetKey(ctx, key.ID)
	if err != nil {
		t.Fatalf("GetKey: %v", err)
	}
	if got.Label != key.Label || got.Type != key.Type || got.PublicKey != key.PublicKey ||
		got.Certificate != key.Certificate || got.Source != key.Source ||
		!got.HasPrivateKey || !got.HasPassphrase || got.OrderKey != key.OrderKey {
		t.Fatalf("Key = %+v, want %+v", got, key)
	}
	if !got.CreatedAt.Equal(now) || !got.UpdatedAt.Equal(now) {
		t.Fatalf("timestamps = %v / %v, want %v", got.CreatedAt, got.UpdatedAt, now)
	}

	rows, err := store.db.Query(ctx, `SELECT data FROM ssh_keys WHERE id = ?`, string(key.ID))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := rows.Values[0][0].(string)
	for _, forbidden := range []string{"PRIVATE KEY", "privateKey", "passphrase"} {
		if strings.Contains(data, forbidden) {
			t.Fatalf("SSH Key record contains %q: %s", forbidden, data)
		}
	}
}

func TestListKeysOrdersByOrderKeyThenLabel(t *testing.T) {
	ctx := context.Background()
	store, _ := openTestStore(t)

	for _, key := range []domainvault.Key{
		{ID: "c", Label: "charlie", Type: domainvault.KeyTypeRSA, Source: domainvault.KeySourceImported, OrderKey: 3000},
		{ID: "b", Label: "bravo", Type: domainvault.KeyTypeRSA, Source: domainvault.KeySourceImported, OrderKey: 1000},
		{ID: "a", Label: "alpha", Type: domainvault.KeyTypeRSA, Source: domainvault.KeySourceImported, OrderKey: 1000},
	} {
		if err := store.SaveKey(ctx, key); err != nil {
			t.Fatal(err)
		}
	}
	keys, err := store.ListKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 3 || keys[0].ID != "a" || keys[1].ID != "b" || keys[2].ID != "c" {
		t.Fatalf("key order = %+v, want a, b, c", keys)
	}
}

func TestMissingKeysReportNotFound(t *testing.T) {
	ctx := context.Background()
	store, _ := openTestStore(t)

	if _, err := store.GetKey(ctx, "missing"); !errors.Is(err, appvault.ErrNotFound) {
		t.Fatalf("GetKey error = %v, want ErrNotFound", err)
	}
	if err := store.DeleteKey(ctx, "missing"); !errors.Is(err, appvault.ErrNotFound) {
		t.Fatalf("DeleteKey error = %v, want ErrNotFound", err)
	}
}

func TestIdentitiesSurviveReopenAndDeleteOnlyThemselves(t *testing.T) {
	ctx := context.Background()
	store, _ := openTestStore(t)
	now := time.Unix(1700000000, 0).UTC()
	identity := domainvault.Identity{
		ID:          "identity-1",
		Label:       "prod",
		Username:    "deploy",
		AuthMethod:  domainvault.IdentityAuthKey,
		KeyID:       "key-1",
		HasPassword: true,
		OrderKey:    1000,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := store.SaveIdentity(ctx, identity); err != nil {
		t.Fatalf("SaveIdentity: %v", err)
	}

	got, err := store.GetIdentity(ctx, identity.ID)
	if err != nil {
		t.Fatalf("GetIdentity: %v", err)
	}
	if got.Label != identity.Label || got.Username != identity.Username ||
		got.AuthMethod != identity.AuthMethod || got.KeyID != identity.KeyID ||
		!got.HasPassword || got.OrderKey != identity.OrderKey {
		t.Fatalf("Identity = %+v, want %+v", got, identity)
	}

	other := domainvault.Identity{ID: "identity-2", Label: "other", Username: "root",
		AuthMethod: domainvault.IdentityAuthPassword, OrderKey: 2000}
	if err := store.SaveIdentity(ctx, other); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteIdentity(ctx, identity.ID); err != nil {
		t.Fatalf("DeleteIdentity: %v", err)
	}
	identities, err := store.ListIdentities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(identities) != 1 || identities[0].ID != other.ID {
		t.Fatalf("identities = %+v, want only %q", identities, other.ID)
	}

	rows, err := store.db.Query(ctx, `SELECT data FROM identities WHERE id = ?`, string(other.ID))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := rows.Values[0][0].(string)
	if strings.Contains(data, `"password":`) {
		t.Fatalf("Identity record contains a password field: %s", data)
	}
}

func TestMissingIdentitiesReportNotFound(t *testing.T) {
	ctx := context.Background()
	store, _ := openTestStore(t)

	if _, err := store.GetIdentity(ctx, "missing"); !errors.Is(err, appvault.ErrNotFound) {
		t.Fatalf("GetIdentity error = %v, want ErrNotFound", err)
	}
	if err := store.DeleteIdentity(ctx, "missing"); !errors.Is(err, appvault.ErrNotFound) {
		t.Fatalf("DeleteIdentity error = %v, want ErrNotFound", err)
	}
}
