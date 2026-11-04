// SPDX-License-Identifier: GPL-3.0-or-later

package et

import (
	"context"
	"errors"
	"testing"

	appvault "github.com/mslxl/natcatty/application/vault"
)

type fakeFieldSecrets struct {
	values    map[appvault.SecretRef]string
	failField string
}

func newFakeFieldSecrets() *fakeFieldSecrets {
	return &fakeFieldSecrets{values: make(map[appvault.SecretRef]string)}
}

func (s *fakeFieldSecrets) Mode() appvault.SecretMode { return appvault.SecretModeOS }

func (s *fakeFieldSecrets) Set(_ context.Context, ref appvault.SecretRef, value string) error {
	if ref.Field == s.failField {
		return errors.New("field store write failed")
	}
	s.values[ref] = value
	return nil
}

func (s *fakeFieldSecrets) Get(_ context.Context, ref appvault.SecretRef) (string, error) {
	value, ok := s.values[ref]
	if !ok {
		return "", appvault.ErrSecretNotFound
	}
	return value, nil
}

func (s *fakeFieldSecrets) Delete(_ context.Context, ref appvault.SecretRef) error {
	if _, ok := s.values[ref]; !ok {
		return appvault.ErrSecretNotFound
	}
	delete(s.values, ref)
	return nil
}

func (s *fakeFieldSecrets) SetPassphrase(context.Context, string) error        { return nil }
func (s *fakeFieldSecrets) UnlockWithPassphrase(context.Context, string) error { return nil }
func (s *fakeFieldSecrets) UseSessionOnly(context.Context) error               { return nil }

// TestSecretSessionStoreUsesOnlyEncryptedFields pins where the ET id and
// passkey live: the existing field-secret store, under separate fields keyed
// by the non-secret resume key. Nothing else records them.
func TestSecretSessionStoreUsesOnlyEncryptedFields(t *testing.T) {
	secrets := newFakeFieldSecrets()
	store := SecretSessionStore{Secrets: secrets}
	record := SessionRecord{ID: testID, Passkey: testPasskey}
	if err := store.Save(t.Context(), testResumeKey, record); err != nil {
		t.Fatalf("Save: %v", err)
	}

	idRef := appvault.ETSessionSecretRef(testResumeKey, appvault.FieldETSessionID)
	keyRef := appvault.ETSessionSecretRef(testResumeKey, appvault.FieldETSessionKey)
	if got := secrets.values[idRef]; got != testID {
		t.Fatalf("session id stored under %+v = %q", idRef, got)
	}
	if got := secrets.values[keyRef]; got != testPasskey {
		t.Fatalf("session passkey stored under %+v = %q", keyRef, got)
	}
	if len(secrets.values) != 2 {
		t.Fatalf("field store holds %d values, want exactly the two session fields", len(secrets.values))
	}
	for ref := range secrets.values {
		if ref.EntityType != appvault.EntityETSession || ref.EntityID != testResumeKey {
			t.Fatalf("session field stored under unexpected ref %+v", ref)
		}
	}

	loaded, err := store.Load(t.Context(), testResumeKey)
	if err != nil || loaded != record {
		t.Fatalf("Load = %+v, %v; want %+v", loaded, err, record)
	}
	if _, err := store.Load(t.Context(), "another-key"); !errors.Is(err, appvault.ErrSecretNotFound) {
		t.Fatalf("Load missing = %v, want ErrSecretNotFound", err)
	}

	if err := store.Delete(t.Context(), testResumeKey); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(secrets.values) != 0 {
		t.Fatalf("Delete left %d session fields", len(secrets.values))
	}
	// Deleting an already absent record is not an error: the close path must
	// stay idempotent.
	if err := store.Delete(t.Context(), testResumeKey); err != nil {
		t.Fatalf("second Delete: %v", err)
	}
}

// TestSecretSessionStoreCleansUpAPartialWrite keeps a failed save from
// leaving a half credential that a later load would misread as a session.
func TestSecretSessionStoreCleansUpAPartialWrite(t *testing.T) {
	secrets := newFakeFieldSecrets()
	secrets.failField = appvault.FieldETSessionKey
	store := SecretSessionStore{Secrets: secrets}
	err := store.Save(t.Context(), testResumeKey, SessionRecord{ID: testID, Passkey: testPasskey})
	if err == nil {
		t.Fatal("Save succeeded with a failing passkey field")
	}
	if len(secrets.values) != 0 {
		t.Fatalf("partial save left the id behind: %v", secrets.values)
	}
	if _, err := store.Load(t.Context(), testResumeKey); !errors.Is(err, appvault.ErrSecretNotFound) {
		t.Fatalf("Load after partial save = %v, want ErrSecretNotFound", err)
	}
}
