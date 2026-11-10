package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	appvault "github.com/mslxl/natcatty/application/vault"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

type fakeFieldSecrets struct {
	mu     sync.Mutex
	values map[appvault.SecretRef]string
}

func newFakeFieldSecrets() *fakeFieldSecrets {
	return &fakeFieldSecrets{values: make(map[appvault.SecretRef]string)}
}

func (f *fakeFieldSecrets) Mode() appvault.SecretMode { return appvault.SecretModeOS }

func (f *fakeFieldSecrets) Set(_ context.Context, ref appvault.SecretRef, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.values[ref] = value
	return nil
}

func (f *fakeFieldSecrets) Get(_ context.Context, ref appvault.SecretRef) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	value, ok := f.values[ref]
	if !ok {
		return "", appvault.ErrSecretNotFound
	}
	return value, nil
}

func (f *fakeFieldSecrets) Delete(_ context.Context, ref appvault.SecretRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.values, ref)
	return nil
}

func (f *fakeFieldSecrets) SetPassphrase(context.Context, string) error { return nil }
func (f *fakeFieldSecrets) UnlockWithPassphrase(context.Context, string) error {
	return nil
}
func (f *fakeFieldSecrets) UseSessionOnly(context.Context) error { return nil }

func TestDataProfilesPersistAcrossReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vault.db")
	database, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	secrets := newFakeFieldSecrets()
	profileSecrets := NewDataProfileSecrets(secrets)
	profiles := NewDataProfiles(database)

	ref, err := profileSecrets.Put(ctx, "profile-1", "password", "hunter2")
	if err != nil {
		t.Fatalf("put secret: %v", err)
	}
	if ref != "vault:data-profile:profile-1:password" {
		t.Fatalf("secret reference = %q", ref)
	}
	second, err := profileSecrets.Put(ctx, "profile-1", "password", "hunter3")
	if err != nil || second != ref {
		t.Fatalf("stable reference = %q, %v", second, err)
	}
	if value, err := profileSecrets.Get(ctx, ref); err != nil || value != "hunter3" {
		t.Fatalf("secret value = %q, %v", value, err)
	}

	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	profile := domainplugin.DataProfile{
		ID:       "profile-1",
		Provider: "plugin.acme.data.warehouse",
		Label:    "Reporting",
		Mode:     domainplugin.DataModeReadOnly,
		Fields: map[string]string{
			"endpoint": "warehouse.example.test:443",
			"tls":      "true",
		},
		SecretRefs: map[string]string{"password": ref},
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := profiles.Save(ctx, profile); err != nil {
		t.Fatalf("save profile: %v", err)
	}
	encoded, _ := json.Marshal(profile)
	if strings.Contains(string(encoded), "hunter3") {
		t.Fatal("profile record contains secret plaintext")
	}

	loaded, err := profiles.Get(ctx, profile.ID)
	if err != nil {
		t.Fatalf("get profile: %v", err)
	}
	if loaded.Label != profile.Label || loaded.SecretRefs["password"] != ref ||
		loaded.Fields["endpoint"] != profile.Fields["endpoint"] {
		t.Fatalf("loaded profile = %+v", loaded)
	}
	listed, err := profiles.List(ctx)
	if err != nil || len(listed) != 1 || listed[0].ID != profile.ID {
		t.Fatalf("list = %+v, %v", listed, err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	database, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer database.Close()
	profiles = NewDataProfiles(database)
	loaded, err = profiles.Get(ctx, profile.ID)
	if err != nil || loaded.SecretRefs["password"] != ref {
		t.Fatalf("profile after reopen = %+v, %v", loaded, err)
	}
	if err := profiles.Delete(ctx, profile.ID); err != nil {
		t.Fatalf("delete profile: %v", err)
	}
	if _, err := profiles.Get(ctx, profile.ID); !errors.Is(err,
		appplugin.ErrDataProfileNotFound) {
		t.Fatalf("deleted profile lookup = %v", err)
	}
	if err := profiles.Delete(ctx, profile.ID); !errors.Is(err,
		appplugin.ErrDataProfileNotFound) {
		t.Fatalf("second delete = %v", err)
	}
}

func TestDataProfileSecretsRejectUnsupportedReferences(t *testing.T) {
	ctx := context.Background()
	profileSecrets := NewDataProfileSecrets(newFakeFieldSecrets())
	if _, err := profileSecrets.Get(ctx, "memory:profile-1:password"); err == nil {
		t.Fatal("unsupported reference was accepted")
	}
	if err := profileSecrets.Delete(ctx, "vault:data-profile:"); err == nil {
		t.Fatal("malformed reference was accepted")
	}
	if _, err := profileSecrets.Get(ctx,
		"vault:data-profile:profile-1:missing"); !errors.Is(err,
		appplugin.ErrDataSecretNotFound) {
		t.Fatalf("missing secret = %v", err)
	}
	if err := profileSecrets.Delete(ctx,
		"vault:data-profile:profile-1:missing"); err != nil {
		t.Fatalf("deleting a missing secret: %v", err)
	}
}
