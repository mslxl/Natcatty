package vault

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

type fakeKeys struct {
	keys    []domainvault.Key
	saveErr error
}

func (f *fakeKeys) ListKeys(context.Context) ([]domainvault.Key, error) {
	return append([]domainvault.Key(nil), f.keys...), nil
}

func (f *fakeKeys) GetKey(_ context.Context, id model.KeyID) (domainvault.Key, error) {
	for _, key := range f.keys {
		if key.ID == id {
			return key, nil
		}
	}
	return domainvault.Key{}, ErrNotFound
}

func (f *fakeKeys) SaveKey(_ context.Context, key domainvault.Key) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	for i, existing := range f.keys {
		if existing.ID == key.ID {
			f.keys[i] = key
			return nil
		}
	}
	f.keys = append(f.keys, key)
	return nil
}

func (f *fakeKeys) DeleteKey(_ context.Context, id model.KeyID) error {
	for i, key := range f.keys {
		if key.ID == id {
			f.keys = append(f.keys[:i], f.keys[i+1:]...)
			return nil
		}
	}
	return ErrNotFound
}

type fakeIdentities struct {
	identities []domainvault.Identity
	saveErr    error
}

func (f *fakeIdentities) ListIdentities(context.Context) ([]domainvault.Identity, error) {
	return append([]domainvault.Identity(nil), f.identities...), nil
}

func (f *fakeIdentities) GetIdentity(_ context.Context, id model.IdentityID) (domainvault.Identity, error) {
	for _, identity := range f.identities {
		if identity.ID == id {
			return identity, nil
		}
	}
	return domainvault.Identity{}, ErrNotFound
}

func (f *fakeIdentities) SaveIdentity(_ context.Context, identity domainvault.Identity) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	for i, existing := range f.identities {
		if existing.ID == identity.ID {
			f.identities[i] = identity
			return nil
		}
	}
	f.identities = append(f.identities, identity)
	return nil
}

func (f *fakeIdentities) DeleteIdentity(_ context.Context, id model.IdentityID) error {
	for i, identity := range f.identities {
		if identity.ID == id {
			f.identities = append(f.identities[:i], f.identities[i+1:]...)
			return nil
		}
	}
	return ErrNotFound
}

func TestUpdateKeyNeverOverwritesUnreadableCiphertext(t *testing.T) {
	t.Run("material replacement", func(t *testing.T) {
		keys := &fakeKeys{keys: []domainvault.Key{{
			ID: "key-1", Label: "work", HasPrivateKey: true,
		}}}
		secrets := newFakeSecrets()
		secrets.getErr = ErrSecretUnreadable
		service := newKeyService(t, &fakeHosts{}, keys, &fakeIdentities{}, secrets, &fakeInspector{})

		_, err := service.UpdateKey(context.Background(), "key-1", domainvault.KeyDraft{
			Label: "renamed", PrivateKey: "NEW MATERIAL",
		})
		if !errors.Is(err, ErrSecretUnreadable) {
			t.Fatalf("error = %v, want ErrSecretUnreadable", err)
		}
		var typed *Error
		if !errors.As(err, &typed) || typed.Kind != KindInternal {
			t.Fatalf("error = %v, want an internal secret error", err)
		}
		if len(secrets.values) != 0 {
			t.Fatalf("secrets = %v, want no writes", secrets.values)
		}
		if keys.keys[0].Label != "work" {
			t.Fatalf("Key label = %q, want the original", keys.keys[0].Label)
		}
	})

	t.Run("metadata only", func(t *testing.T) {
		keys := &fakeKeys{keys: []domainvault.Key{{
			ID: "key-1", Label: "work", HasPrivateKey: true,
		}}}
		secrets := newFakeSecrets()
		secrets.getErr = ErrSecretUnreadable
		service := newKeyService(t, &fakeHosts{}, keys, &fakeIdentities{}, secrets, &fakeInspector{})

		updated, err := service.UpdateKey(context.Background(), "key-1", domainvault.KeyDraft{Label: "renamed"})
		if err != nil {
			t.Fatalf("UpdateKey: %v", err)
		}
		if updated.Label != "renamed" {
			t.Fatalf("Label = %q, want renamed", updated.Label)
		}
	})
}

func TestUpdateIdentityNeverOverwritesUnreadableCiphertext(t *testing.T) {
	t.Run("password", func(t *testing.T) {
		identities := &fakeIdentities{identities: []domainvault.Identity{{
			ID: "identity-1", Label: "deploy", Username: "deploy",
			AuthMethod: domainvault.IdentityAuthPassword, HasPassword: true,
		}}}
		secrets := newFakeSecrets()
		secrets.getErr = ErrSecretUnreadable
		service := newKeyService(t, &fakeHosts{}, &fakeKeys{}, identities, secrets, &fakeInspector{})

		_, err := service.UpdateIdentity(context.Background(), "identity-1", domainvault.IdentityDraft{
			Label: "renamed", Username: "deploy",
			AuthMethod: domainvault.IdentityAuthPassword, Password: "NEW",
		})
		if !errors.Is(err, ErrSecretUnreadable) {
			t.Fatalf("error = %v, want ErrSecretUnreadable", err)
		}
		if len(secrets.values) != 0 {
			t.Fatalf("secrets = %v, want no writes", secrets.values)
		}
	})

	t.Run("metadata only", func(t *testing.T) {
		identities := &fakeIdentities{identities: []domainvault.Identity{{
			ID: "identity-1", Label: "deploy", Username: "deploy",
			AuthMethod: domainvault.IdentityAuthPassword, HasPassword: true,
		}}}
		secrets := newFakeSecrets()
		secrets.getErr = ErrSecretUnreadable
		service := newKeyService(t, &fakeHosts{}, &fakeKeys{}, identities, secrets, &fakeInspector{})

		updated, err := service.UpdateIdentity(context.Background(), "identity-1", domainvault.IdentityDraft{
			Label: "renamed", Username: "deploy", AuthMethod: domainvault.IdentityAuthPassword,
		})
		if err != nil {
			t.Fatalf("UpdateIdentity: %v", err)
		}
		if updated.Label != "renamed" {
			t.Fatalf("Label = %q, want renamed", updated.Label)
		}
	})
}

type fakeInspector struct {
	inspect   func(privateKey, passphrase, certificate string) (KeyInspection, error)
	lastKey   string
	lastPass  string
	lastCert  string
	callCount int
}

func (f *fakeInspector) Inspect(privateKey, passphrase, certificate string) (KeyInspection, error) {
	f.callCount++
	f.lastKey = privateKey
	f.lastPass = passphrase
	f.lastCert = certificate
	if f.inspect != nil {
		return f.inspect(privateKey, passphrase, certificate)
	}
	return KeyInspection{
		Type:      domainvault.KeyTypeED25519,
		PublicKey: "ssh-ed25519 AAAATEST",
		Encrypted: passphrase != "",
	}, nil
}

func newKeyService(t *testing.T, hosts *fakeHosts, keys *fakeKeys, identities *fakeIdentities, secrets FieldSecrets, inspector KeyInspector) *Service {
	t.Helper()
	var hostIDs, keyIDs, identityIDs int
	return NewService(hosts,
		WithKeys(keys),
		WithIdentities(identities),
		WithSecrets(secrets),
		WithKeyInspector(inspector),
		WithIDGenerator(func() model.HostID {
			hostIDs++
			return model.HostID("host-" + strconv.Itoa(hostIDs))
		}),
		WithKeyIDGenerator(func() model.KeyID {
			keyIDs++
			return model.KeyID("key-" + strconv.Itoa(keyIDs))
		}),
		WithIdentityIDGenerator(func() model.IdentityID {
			identityIDs++
			return model.IdentityID("identity-" + strconv.Itoa(identityIDs))
		}),
		WithClock(func() time.Time { return time.Unix(1700000000, 0).UTC() }),
	)
}

func TestCreateKeyEncryptsMaterialAndKeepsTheRecordSecretFree(t *testing.T) {
	t.Parallel()

	keys := &fakeKeys{}
	secrets := newFakeSecrets()
	inspector := &fakeInspector{}
	service := newKeyService(t, &fakeHosts{}, keys, &fakeIdentities{}, secrets, inspector)

	key, err := service.CreateKey(context.Background(), domainvault.KeyDraft{
		Label:          "work",
		PrivateKey:     "-----BEGIN OPENSSH PRIVATE KEY-----\nmaterial\n-----END OPENSSH PRIVATE KEY-----",
		Passphrase:     "keypass",
		SavePassphrase: true,
	})
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}
	if key.ID != "key-1" || key.Label != "work" {
		t.Errorf("Key = %+v, want key-1/work", key)
	}
	if key.Type != domainvault.KeyTypeED25519 {
		t.Errorf("Type = %q, want ED25519 from the inspector", key.Type)
	}
	if !key.HasPrivateKey || !key.HasPassphrase {
		t.Errorf("HasPrivateKey/HasPassphrase = %v/%v, want true/true", key.HasPrivateKey, key.HasPassphrase)
	}
	if got := secrets.values[KeySecretRef(key.ID, FieldPrivateKey)]; !strings.Contains(got, "material") {
		t.Errorf("stored private material = %q", got)
	}
	if got := secrets.values[KeySecretRef(key.ID, FieldPassphrase)]; got != "keypass" {
		t.Errorf("stored passphrase = %q, want keypass", got)
	}

	data, err := json.Marshal(key)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "material") || strings.Contains(string(data), "keypass") {
		t.Fatalf("Key JSON contains credential material: %s", data)
	}
}

func TestCreateKeyRejectsAWrongStoredPassphrase(t *testing.T) {
	t.Parallel()

	inspector := &fakeInspector{inspect: func(_, passphrase, _ string) (KeyInspection, error) {
		if passphrase == "wrong" {
			return KeyInspection{}, ErrKeyPassphraseInvalid
		}
		return KeyInspection{Type: domainvault.KeyTypeRSA}, nil
	}}
	service := newKeyService(t, &fakeHosts{}, &fakeKeys{}, &fakeIdentities{}, newFakeSecrets(), inspector)

	_, err := service.CreateKey(context.Background(), domainvault.KeyDraft{
		Label: "work", PrivateKey: "material", Passphrase: "wrong", SavePassphrase: true,
	})
	var typed *Error
	if !errors.As(err, &typed) || typed.Kind != KindValidation {
		t.Fatalf("err = %v, want validation", err)
	}
	var detail *domainvault.ValidationError
	if !errors.As(err, &detail) || detail.Field != domainvault.FieldPassphrase {
		t.Fatalf("detail = %+v, want FieldPassphrase", detail)
	}
}

func TestCreateKeyRequiresASecretStoreAndInspector(t *testing.T) {
	t.Parallel()

	service := newKeyService(t, &fakeHosts{}, &fakeKeys{}, &fakeIdentities{}, nil, &fakeInspector{})
	if _, err := service.CreateKey(context.Background(), domainvault.KeyDraft{Label: "work", PrivateKey: "material"}); err == nil {
		t.Fatal("CreateKey succeeded without a secret store")
	}

	service = newKeyService(t, &fakeHosts{}, &fakeKeys{}, &fakeIdentities{}, newFakeSecrets(), nil)
	if _, err := service.CreateKey(context.Background(), domainvault.KeyDraft{Label: "work", PrivateKey: "material"}); err == nil {
		t.Fatal("CreateKey succeeded without a key inspector")
	}
}

func TestUpdateKeyKeepsMaterialAndCanClearThePassphrase(t *testing.T) {
	t.Parallel()

	keys := &fakeKeys{}
	secrets := newFakeSecrets()
	service := newKeyService(t, &fakeHosts{}, keys, &fakeIdentities{}, secrets, &fakeInspector{})
	key, err := service.CreateKey(context.Background(), domainvault.KeyDraft{
		Label: "work", PrivateKey: "material", Passphrase: "keypass", SavePassphrase: true,
	})
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}

	updated, err := service.UpdateKey(context.Background(), key.ID, domainvault.KeyDraft{
		Label: "renamed", ClearPassphrase: true,
	})
	if err != nil {
		t.Fatalf("UpdateKey: %v", err)
	}
	if updated.Label != "renamed" || updated.HasPassphrase {
		t.Errorf("updated Key = %+v, want renamed without a passphrase", updated)
	}
	if got := secrets.values[KeySecretRef(key.ID, FieldPrivateKey)]; got != "material" {
		t.Errorf("private material = %q, want it preserved", got)
	}
	if _, ok := secrets.values[KeySecretRef(key.ID, FieldPassphrase)]; ok {
		t.Error("passphrase was not removed")
	}
}

func TestUpdateKeyValidatesACertificateOnlyEditAgainstStoredMaterial(t *testing.T) {
	keys := &fakeKeys{keys: []domainvault.Key{{
		ID: "key-1", Label: "work", HasPrivateKey: true,
	}}}
	secrets := newFakeSecrets()
	secrets.values[KeySecretRef("key-1", FieldPrivateKey)] = "STORED"
	inspector := &fakeInspector{inspect: func(privateKey, passphrase, certificate string) (KeyInspection, error) {
		if privateKey != "STORED" {
			t.Errorf("inspected material = %q, want the stored material", privateKey)
		}
		if certificate == "bad" {
			return KeyInspection{}, ErrKeyCertificateInvalid
		}
		return KeyInspection{Type: domainvault.KeyTypeED25519, PublicKey: "CERTIFIED"}, nil
	}}
	service := newKeyService(t, &fakeHosts{}, keys, &fakeIdentities{}, secrets, inspector)

	_, err := service.UpdateKey(context.Background(), "key-1", domainvault.KeyDraft{
		Label: "work", Certificate: "bad",
	})
	var invalid *domainvault.ValidationError
	if !errors.As(err, &invalid) || invalid.Field != domainvault.FieldCertificate {
		t.Fatalf("error = %v, want a certificate validation error", err)
	}
	if keys.keys[0].Certificate != "" {
		t.Fatalf("a rejected certificate was saved: %q", keys.keys[0].Certificate)
	}

	updated, err := service.UpdateKey(context.Background(), "key-1", domainvault.KeyDraft{
		Label: "work", Certificate: "good",
	})
	if err != nil {
		t.Fatalf("UpdateKey: %v", err)
	}
	if updated.Certificate != "good" {
		t.Fatalf("Certificate = %q, want good", updated.Certificate)
	}
}

func TestDeleteKeyRemovesEncryptedMaterial(t *testing.T) {
	t.Parallel()

	keys := &fakeKeys{}
	secrets := newFakeSecrets()
	service := newKeyService(t, &fakeHosts{}, keys, &fakeIdentities{}, secrets, &fakeInspector{})
	key, err := service.CreateKey(context.Background(), domainvault.KeyDraft{
		Label: "work", PrivateKey: "material", Passphrase: "keypass", SavePassphrase: true,
	})
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}

	if err := service.DeleteKey(context.Background(), key.ID); err != nil {
		t.Fatalf("DeleteKey: %v", err)
	}
	if len(keys.keys) != 0 {
		t.Errorf("keys = %d, want 0", len(keys.keys))
	}
	if len(secrets.values) != 0 {
		t.Errorf("secrets = %v, want none", secrets.values)
	}
}

func TestCreateIdentityEncryptsPasswordAndClearsKeyOnPasswordAuth(t *testing.T) {
	t.Parallel()

	identities := &fakeIdentities{}
	secrets := newFakeSecrets()
	service := newKeyService(t, &fakeHosts{}, &fakeKeys{}, identities, secrets, &fakeInspector{})

	identity, err := service.CreateIdentity(context.Background(), domainvault.IdentityDraft{
		Label: "root", Username: "root", AuthMethod: domainvault.IdentityAuthPassword,
		KeyID: "stale-key", Password: "hunter2",
	})
	if err != nil {
		t.Fatalf("CreateIdentity: %v", err)
	}
	if identity.KeyID != "" {
		t.Errorf("KeyID = %q, want cleared for password auth", identity.KeyID)
	}
	if !identity.HasPassword {
		t.Error("HasPassword = false")
	}
	if got := secrets.values[IdentitySecretRef(identity.ID, FieldPassword)]; got != "hunter2" {
		t.Errorf("stored identity password = %q", got)
	}

	data, err := json.Marshal(identity)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "hunter2") {
		t.Fatalf("Identity JSON contains the password: %s", data)
	}
}

func TestKeyIdentityRequiresAKey(t *testing.T) {
	t.Parallel()

	service := newKeyService(t, &fakeHosts{}, &fakeKeys{}, &fakeIdentities{}, newFakeSecrets(), &fakeInspector{})
	_, err := service.CreateIdentity(context.Background(), domainvault.IdentityDraft{
		Label: "work", Username: "alice", AuthMethod: domainvault.IdentityAuthKey,
	})
	var typed *Error
	if !errors.As(err, &typed) || typed.Kind != KindValidation {
		t.Fatalf("err = %v, want validation", err)
	}
	var detail *domainvault.ValidationError
	if !errors.As(err, &detail) || detail.Field != domainvault.FieldKey {
		t.Fatalf("detail = %+v, want FieldKey", detail)
	}
}

func TestResolveAuthAppliesIdentityOverHostAndLoadsKeyMaterial(t *testing.T) {
	t.Parallel()

	hosts := &fakeHosts{hosts: []domainvault.Host{{
		ID: "host-1", Label: "web", Hostname: "example.com", Port: 22,
		Username: "hostuser", Protocol: domainvault.ProtocolSSH,
		IdentityID: "identity-1", KeyID: "key-1",
		AuthMethod: domainvault.HostAuthKey,
	}}}
	keys := &fakeKeys{keys: []domainvault.Key{{
		ID: "key-1", Label: "work", Type: domainvault.KeyTypeED25519,
		Certificate: "ssh-ed25519-cert-v01@openssh.com AAAA",
	}}}
	identities := &fakeIdentities{identities: []domainvault.Identity{{
		ID: "identity-1", Label: "alice", Username: "alice",
		AuthMethod: domainvault.IdentityAuthCertificate, KeyID: "key-1",
	}}}
	secrets := newFakeSecrets()
	secrets.values[KeySecretRef("key-1", FieldPrivateKey)] = "private-material"
	secrets.values[KeySecretRef("key-1", FieldPassphrase)] = "keypass"
	secrets.values[HostSecretRef("host-1", FieldPassword)] = "hostpass"
	service := newKeyService(t, hosts, keys, identities, secrets, &fakeInspector{})

	resolved, err := service.ResolveAuth(context.Background(), "host-1")
	if err != nil {
		t.Fatalf("ResolveAuth: %v", err)
	}
	if resolved.Selection.Username != "alice" {
		t.Errorf("username = %q, want the Identity username", resolved.Selection.Username)
	}
	if resolved.Selection.Method != domainvault.HostAuthCertificate {
		t.Errorf("method = %q, want certificate", resolved.Selection.Method)
	}
	if resolved.MissingKey {
		t.Error("MissingKey = true")
	}
	if resolved.Key == nil || resolved.Key.PrivateKey != "private-material" || resolved.Key.Passphrase != "keypass" {
		t.Fatalf("resolved key = %+v", resolved.Key)
	}
	if resolved.Key.Certificate == "" {
		t.Error("certificate was not carried into the resolved key")
	}
	if resolved.Password != "hostpass" {
		t.Errorf("password = %q, want the Host password", resolved.Password)
	}
}

func TestResolveAuthKeyModeDiscardsTheKeyAndDanglingKeyFailsClosed(t *testing.T) {
	t.Parallel()

	hosts := &fakeHosts{hosts: []domainvault.Host{{
		ID: "host-1", Label: "web", Hostname: "example.com", Port: 22,
		Username: "root", Protocol: domainvault.ProtocolSSH,
		KeyID: "key-1", AuthMethod: domainvault.HostAuthPassword,
	}}}
	keys := &fakeKeys{keys: []domainvault.Key{{ID: "key-1", Label: "work"}}}
	secrets := newFakeSecrets()
	secrets.values[KeySecretRef("key-1", FieldPrivateKey)] = "private-material"
	service := newKeyService(t, hosts, keys, &fakeIdentities{}, secrets, &fakeInspector{})

	resolved, err := service.ResolveAuth(context.Background(), "host-1")
	if err != nil {
		t.Fatalf("ResolveAuth: %v", err)
	}
	if resolved.Selection.Method != domainvault.HostAuthPassword || resolved.Key != nil {
		t.Fatalf("resolved = %+v, want password without key material", resolved)
	}

	hosts.hosts[0].AuthMethod = domainvault.HostAuthKey
	hosts.hosts[0].KeyID = "missing-key"
	resolved, err = service.ResolveAuth(context.Background(), "host-1")
	if err != nil {
		t.Fatalf("ResolveAuth with a dangling Key: %v", err)
	}
	if !resolved.MissingKey || resolved.Key != nil {
		t.Fatalf("resolved = %+v, want MissingKey with no substitution", resolved)
	}
}

func TestHostDraftPersistsAuthenticationSelection(t *testing.T) {
	t.Parallel()

	hosts := &fakeHosts{}
	service := newKeyService(t, hosts, &fakeKeys{}, &fakeIdentities{}, newFakeSecrets(), &fakeInspector{})
	host, err := service.Create(context.Background(), domainvault.Draft{
		Hostname: "example.com", Username: "alice",
		IdentityID: "identity-1", KeyID: "key-1",
		AuthMethod: domainvault.HostAuthKey, UseSSHAgent: true, RequiresMFA: true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if host.IdentityID != "identity-1" || host.KeyID != "key-1" || host.AuthMethod != domainvault.HostAuthKey {
		t.Errorf("Host = %+v, want the selected Identity, Key, and method", host)
	}
	if !host.UseSSHAgent || !host.RequiresMFA {
		t.Errorf("agent/MFA = %v/%v, want true/true", host.UseSSHAgent, host.RequiresMFA)
	}
}
