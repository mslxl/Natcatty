package vault

import (
	"context"
	"errors"
	"fmt"

	"github.com/mslxl/natcatty/domain/model"
)

// SecretMode says how the field-secret store protects credentials. The UI
// offers passphrase or session-only mode when the OS store is unavailable;
// there is no plaintext mode.
type SecretMode string

const (
	// SecretModeOS keeps the field key in the OS credential store.
	SecretModeOS SecretMode = "os"
	// SecretModePassphraseLocked has a passphrase-wrapped key that is not
	// unlocked yet.
	SecretModePassphraseLocked SecretMode = "passphrase_locked"
	// SecretModePassphrase has an unlocked passphrase-wrapped key.
	SecretModePassphrase SecretMode = "passphrase"
	// SecretModeSession keeps credentials in memory for this run only.
	SecretModeSession SecretMode = "session"
	// SecretModeUnavailable needs the user to choose passphrase or
	// session-only mode.
	SecretModeUnavailable SecretMode = "unavailable"
)

// Entity types and field names the Vault encrypts. They are part of the AAD
// and the persisted secret_values key, so do not rename them.
const (
	EntityHost     = "host"
	EntitySSHKey   = "ssh_key"
	EntityIdentity = "identity"

	FieldPassword      = "password"
	FieldPassphrase    = "passphrase"
	FieldPrivateKey    = "private_key"
	FieldProxyPassword = "proxy_password"
)

// EncryptedPlaceholder prefixes a portable credential value meaning
// "device-bound secret, unavailable here". A placeholder never contains
// plaintext; no Vault JSON stores credential plaintext at all.
const EncryptedPlaceholder = "enc:v1:"

// SecretRef identifies one encrypted field.
type SecretRef struct {
	EntityType string
	EntityID   string
	Field      string
}

// HostSecretRef names a Host's credential field.
func HostSecretRef(id model.HostID, field string) SecretRef {
	return SecretRef{EntityType: EntityHost, EntityID: string(id), Field: field}
}

// KeySecretRef names an SSH Key's encrypted material field. Private key and
// passphrase ciphertext never appear on the Key entity.
func KeySecretRef(id model.KeyID, field string) SecretRef {
	return SecretRef{EntityType: EntitySSHKey, EntityID: string(id), Field: field}
}

// IdentitySecretRef names an Identity's credential field.
func IdentitySecretRef(id model.IdentityID, field string) SecretRef {
	return SecretRef{EntityType: EntityIdentity, EntityID: string(id), Field: field}
}

// FieldSecrets is the field-secret port the Vault consumes. The adapter owns
// key management and encryption; the service never sees ciphertext.
type FieldSecrets interface {
	Mode() SecretMode
	Set(ctx context.Context, ref SecretRef, value string) error
	Get(ctx context.Context, ref SecretRef) (string, error)
	Delete(ctx context.Context, ref SecretRef) error
	// SetPassphrase configures passphrase mode when no OS store is
	// available.
	SetPassphrase(ctx context.Context, passphrase string) error
	// UnlockWithPassphrase unwraps the field key for this run.
	UnlockWithPassphrase(ctx context.Context, passphrase string) error
	// UseSessionOnly keeps new credentials in memory and persists none.
	UseSessionOnly(ctx context.Context) error
}

// Secret-store sentinels. The adapter returns them and the Service maps them
// to typed Vault errors.
var (
	// ErrSecretNotFound reports a field that has no stored value.
	ErrSecretNotFound = errors.New("vault: secret not found")
	// ErrSecretsLocked reports a passphrase-wrapped store that is not
	// unlocked.
	ErrSecretsLocked = errors.New("vault: secrets locked")
	// ErrSecretsUnavailable reports that the user has not chosen passphrase
	// or session-only mode.
	ErrSecretsUnavailable = errors.New("vault: secrets unavailable")
	// ErrWrongPassphrase reports a passphrase that does not unwrap the key.
	ErrWrongPassphrase = errors.New("vault: wrong passphrase")
	// ErrPassphraseRequired reports an empty passphrase.
	ErrPassphraseRequired = errors.New("vault: passphrase required")
	// ErrPassphraseAlreadySet reports a second passphrase setup request.
	ErrPassphraseAlreadySet = errors.New("vault: passphrase already set")
	// ErrSecretUnreadable reports ciphertext that cannot be authenticated,
	// for example after tampering or key loss.
	ErrSecretUnreadable = errors.New("vault: secret unreadable")
)

// SecretMode reports how credentials are protected. A service without a
// secret store reports OS mode so a test or early shell does not show the
// setup UI.
func (s *Service) SecretMode() SecretMode {
	if s.secrets == nil {
		return SecretModeOS
	}
	return s.secrets.Mode()
}

// SetSecret stores a Host credential. An empty value deletes the stored
// secret, which lets the editor clear a password explicitly.
func (s *Service) SetSecret(ctx context.Context, id model.HostID, field, value string) error {
	host, err := s.repo.Get(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return internal(err)
	}
	if s.secrets == nil {
		return unsupported(ErrSecretsUnavailable)
	}
	if !hostField(field) {
		return invalidField(field)
	}

	if value == "" {
		if err := s.secrets.Delete(ctx, HostSecretRef(id, field)); err != nil && !errors.Is(err, ErrSecretNotFound) {
			return secretError(err)
		}
	} else if err := s.secrets.Set(ctx, HostSecretRef(id, field), value); err != nil {
		return secretError(err)
	}

	if s.SecretMode() == SecretModeSession {
		// Session-only credentials never survive a restart, so they are not
		// announced by the persisted Host record.
		return nil
	}
	changed := false
	switch field {
	case FieldPassword:
		host.HasPassword = value != ""
		changed = true
	case FieldProxyPassword:
		if host.Proxy != nil {
			host.Proxy.HasPassword = value != ""
			changed = true
		}
	}
	if changed {
		host.UpdatedAt = s.now()
		if err := s.repo.Save(ctx, host); err != nil {
			return internal(err)
		}
	}
	return nil
}

// Secret reads a Host credential after the field-store is available.
func (s *Service) Secret(ctx context.Context, id model.HostID, field string) (string, error) {
	if _, err := s.repo.Get(ctx, id); err != nil {
		if errors.Is(err, ErrNotFound) {
			return "", ErrNotFound
		}
		return "", internal(err)
	}
	if s.secrets == nil {
		return "", unsupported(ErrSecretsUnavailable)
	}
	if !hostField(field) {
		return "", invalidField(field)
	}
	value, err := s.secrets.Get(ctx, HostSecretRef(id, field))
	if err != nil {
		return "", secretError(err)
	}
	return value, nil
}

// DeleteSecret removes a Host credential.
func (s *Service) DeleteSecret(ctx context.Context, id model.HostID, field string) error {
	return s.SetSecret(ctx, id, field, "")
}

// UnlockSecrets unwraps a passphrase-mode field key.
func (s *Service) UnlockSecrets(ctx context.Context, passphrase string) error {
	if s.secrets == nil {
		return unsupported(ErrSecretsUnavailable)
	}
	if err := s.secrets.UnlockWithPassphrase(ctx, passphrase); err != nil {
		return secretError(err)
	}
	return nil
}

// SetSecretsPassphrase chooses passphrase mode when the OS store is
// unavailable.
func (s *Service) SetSecretsPassphrase(ctx context.Context, passphrase string) error {
	if s.secrets == nil {
		return unsupported(ErrSecretsUnavailable)
	}
	if err := s.secrets.SetPassphrase(ctx, passphrase); err != nil {
		return secretError(err)
	}
	return nil
}

// UseSessionSecrets chooses session-only mode when the OS store is
// unavailable. Nothing new is written to disk.
func (s *Service) UseSessionSecrets(ctx context.Context) error {
	if s.secrets == nil {
		return unsupported(ErrSecretsUnavailable)
	}
	if err := s.secrets.UseSessionOnly(ctx); err != nil {
		return secretError(err)
	}
	return nil
}

// hostField accepts the Host fields the Vault encrypts.
func hostField(field string) bool {
	return field == FieldPassword || field == FieldPassphrase || field == FieldProxyPassword
}

func invalidField(field string) error {
	return invalid(fmt.Errorf("vault: %q cannot hold a secret", field))
}

// secretError maps a field-store sentinel to a typed Vault error.
func secretError(err error) error {
	switch {
	case errors.Is(err, ErrWrongPassphrase), errors.Is(err, ErrSecretsLocked):
		return permission(err)
	case errors.Is(err, ErrSecretsUnavailable), errors.Is(err, ErrPassphraseRequired), errors.Is(err, ErrPassphraseAlreadySet):
		return unsupported(err)
	case errors.Is(err, ErrSecretNotFound):
		return ErrNotFound
	default:
		return internal(err)
	}
}
