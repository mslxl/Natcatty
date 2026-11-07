package historysync

import (
	"context"
	"errors"
)

// The Atuin credentials live under a dedicated field-secret entity type. The
// Vault cloud-sync snapshot enumerates known Vault entity types and never this
// one, so neither the token nor the key can enter a cloud payload or a
// portable export. Encrypted Local Backups snapshot the secret table
// wholesale and may include it, as ADR-0010 permits.
const (
	// SecretEntityType is the field-secret entity type for both credentials.
	SecretEntityType = "atuin_account"
	// SecretEntityID is the one account this install supports.
	SecretEntityID = "default"
	// SecretFieldSessionToken stores the session token.
	SecretFieldSessionToken = "session_token"
	// SecretFieldMasterKey stores the base64 master key.
	SecretFieldMasterKey = "master_key"
)

// SecretRef identifies one field-secret value. It mirrors the field-secret
// adapter's reference without making this context import the Vault service.
type SecretRef struct {
	EntityType string
	EntityID   string
	Field      string
}

// SessionTokenRef names the stored session token.
func SessionTokenRef() SecretRef {
	return SecretRef{EntityType: SecretEntityType, EntityID: SecretEntityID, Field: SecretFieldSessionToken}
}

// MasterKeyRef names the stored master key.
func MasterKeyRef() SecretRef {
	return SecretRef{EntityType: SecretEntityType, EntityID: SecretEntityID, Field: SecretFieldMasterKey}
}

// SecretMode says how the field-secret store protects credentials.
type SecretMode string

const (
	// SecretModeOS keeps the field key in the OS credential store.
	SecretModeOS SecretMode = "os"
	// SecretModePassphraseLocked has a wrapped key that is not unlocked yet.
	SecretModePassphraseLocked SecretMode = "passphrase_locked"
	// SecretModePassphrase has an unlocked passphrase-wrapped key.
	SecretModePassphrase SecretMode = "passphrase"
	// SecretModeSession keeps credentials in memory for this run only.
	SecretModeSession SecretMode = "session"
	// SecretModeUnavailable needs the user to choose passphrase or
	// session-only mode.
	SecretModeUnavailable SecretMode = "unavailable"
)

// Secret-store sentinels. The adapter maps the field-secret store's errors to
// these so the account service need not import that adapter.
var (
	// ErrSecretNotFound reports a missing credential.
	ErrSecretNotFound = errors.New("historysync: secret not found")
	// ErrSecretUnreadable reports ciphertext this device cannot decrypt.
	ErrSecretUnreadable = errors.New("historysync: secret unreadable")
	// ErrSecretsLocked reports a passphrase-locked store.
	ErrSecretsLocked = errors.New("historysync: secrets locked")
	// ErrSecretsUnavailable reports that no secret mode has been chosen.
	ErrSecretsUnavailable = errors.New("historysync: secrets unavailable")
)

// Secrets is the narrow field-secret port. The sync service and account
// service receive this, never the Vault service. Has reports presence without
// decrypting the value, so status checks never pull the key into memory.
type Secrets interface {
	Mode() SecretMode
	Has(ctx context.Context, ref SecretRef) (bool, error)
	Get(ctx context.Context, ref SecretRef) (string, error)
	Set(ctx context.Context, ref SecretRef, value string) error
	Delete(ctx context.Context, ref SecretRef) error
}
