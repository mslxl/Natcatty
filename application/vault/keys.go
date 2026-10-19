package vault

import (
	"context"
	"errors"

	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// KeyRepository persists SSH Keys. Private material and passphrases live in
// the field-secret store, never in these records.
type KeyRepository interface {
	ListKeys(ctx context.Context) ([]domainvault.Key, error)
	GetKey(ctx context.Context, id model.KeyID) (domainvault.Key, error)
	SaveKey(ctx context.Context, key domainvault.Key) error
	DeleteKey(ctx context.Context, id model.KeyID) error
}

// KeyInspection is the non-secret result of parsing key material.
type KeyInspection struct {
	Type            domainvault.KeyType
	PublicKey       string
	Encrypted       bool
	NeedsPassphrase bool
}

// KeyInspector parses OpenSSH, PEM, encrypted PKCS#8, and PuTTY PPK key
// material. It returns ErrKeyPassphraseInvalid for a supplied passphrase that
// does not decrypt, ErrKeyMaterialUnsupported for a format this build cannot
// read, and ErrKeyMaterialInvalid for corrupt material. An empty passphrase
// on encrypted material reports NeedsPassphrase without error.
type KeyInspector interface {
	Inspect(privateKey, passphrase, certificate string) (KeyInspection, error)
}

// Key-inspection sentinels. The SSH adapter's errors are mapped onto these by
// the app, keeping this package independent of key-format libraries.
var (
	// ErrKeyMaterialInvalid reports corrupt key material.
	ErrKeyMaterialInvalid = errors.New("vault: invalid key material")
	// ErrKeyMaterialUnsupported reports a key format this build cannot read.
	ErrKeyMaterialUnsupported = errors.New("vault: unsupported key material")
	// ErrKeyPassphraseInvalid reports a passphrase that does not decrypt.
	ErrKeyPassphraseInvalid = errors.New("vault: invalid key passphrase")
	// ErrKeyCertificateInvalid reports a certificate that cannot be parsed or
	// does not match the private key.
	ErrKeyCertificateInvalid = errors.New("vault: invalid certificate")
	// ErrKeyInspectorUnavailable reports that no key parser is wired, so the
	// Vault refuses to store unvalidated key material.
	ErrKeyInspectorUnavailable = errors.New("vault: key inspector unavailable")
)

// ListKeys returns every SSH Key in display order.
func (s *Service) ListKeys(ctx context.Context) ([]domainvault.Key, error) {
	if s.keys == nil {
		return nil, unsupported(ErrKeyInspectorUnavailable)
	}
	keys, err := s.keys.ListKeys(ctx)
	if err != nil {
		return nil, internal(err)
	}
	return keys, nil
}

// GetKey returns one SSH Key.
func (s *Service) GetKey(ctx context.Context, id model.KeyID) (domainvault.Key, error) {
	if s.keys == nil {
		return domainvault.Key{}, unsupported(ErrKeyInspectorUnavailable)
	}
	key, err := s.keys.GetKey(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return domainvault.Key{}, ErrNotFound
	}
	if err != nil {
		return domainvault.Key{}, internal(err)
	}
	return key, nil
}

// CreateKey validates key material, encrypts it, and persists the Key. The
// private key and passphrase never touch the Key record or the JSON tail.
func (s *Service) CreateKey(ctx context.Context, draft domainvault.KeyDraft) (domainvault.Key, error) {
	normalized, err := domainvault.NormalizeKeyDraft(draft)
	if err != nil {
		return domainvault.Key{}, invalid(err)
	}
	if normalized.PrivateKey == "" {
		return domainvault.Key{}, invalid(&domainvault.ValidationError{Field: domainvault.FieldKeyMaterial, Rule: domainvault.RuleRequired})
	}
	if err := s.requireKeyStorage(); err != nil {
		return domainvault.Key{}, err
	}
	if normalized.ClearPrivateKey {
		return domainvault.Key{}, invalid(&domainvault.ValidationError{Field: domainvault.FieldPrivateKey, Rule: domainvault.RuleInvalid})
	}

	inspection, err := s.inspectKey(normalized.PrivateKey, normalized.Passphrase, normalized.Certificate)
	if err != nil {
		return domainvault.Key{}, err
	}
	keys, err := s.keys.ListKeys(ctx)
	if err != nil {
		return domainvault.Key{}, internal(err)
	}

	now := s.now()
	key := domainvault.Key{
		ID:            s.newKeyID(),
		Label:         normalized.Label,
		Type:          firstKeyType(inspection.Type, normalized.Type),
		PublicKey:     firstNonEmpty(inspection.PublicKey, normalized.PublicKey),
		Certificate:   normalized.Certificate,
		Source:        normalized.Source,
		OrderKey:      nextKeyOrder(keys),
		HasPrivateKey: s.SecretMode() != SecretModeSession,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	savePassphrase := normalized.SavePassphrase && inspection.Encrypted && normalized.Passphrase != ""
	key.HasPassphrase = savePassphrase && s.SecretMode() != SecretModeSession

	if err := s.secrets.Set(ctx, KeySecretRef(key.ID, FieldPrivateKey), normalized.PrivateKey); err != nil {
		return domainvault.Key{}, secretError(err)
	}
	if savePassphrase {
		if err := s.secrets.Set(ctx, KeySecretRef(key.ID, FieldPassphrase), normalized.Passphrase); err != nil {
			_ = s.secrets.Delete(ctx, KeySecretRef(key.ID, FieldPrivateKey))
			return domainvault.Key{}, secretError(err)
		}
	}
	if err := s.keys.SaveKey(ctx, key); err != nil {
		_ = s.secrets.Delete(ctx, KeySecretRef(key.ID, FieldPrivateKey))
		_ = s.secrets.Delete(ctx, KeySecretRef(key.ID, FieldPassphrase))
		return domainvault.Key{}, internal(err)
	}
	return key, nil
}

// UpdateKey replaces a Key's editable fields. An empty PrivateKey keeps the
// stored material; ClearPrivateKey is rejected because an imported Key needs
// material to be usable.
func (s *Service) UpdateKey(ctx context.Context, id model.KeyID, draft domainvault.KeyDraft) (domainvault.Key, error) {
	if err := s.requireKeyStorage(); err != nil {
		return domainvault.Key{}, err
	}
	existing, err := s.GetKey(ctx, id)
	if err != nil {
		return domainvault.Key{}, err
	}
	normalized, err := domainvault.NormalizeKeyDraft(draft)
	if err != nil {
		return domainvault.Key{}, invalid(err)
	}
	if normalized.ClearPrivateKey {
		return domainvault.Key{}, invalid(&domainvault.ValidationError{Field: domainvault.FieldPrivateKey, Rule: domainvault.RuleInvalid})
	}

	updated := existing
	updated.Label = normalized.Label
	updated.Source = normalized.Source
	if normalized.ClearCertificate {
		updated.Certificate = ""
	} else if normalized.Certificate != "" {
		updated.Certificate = normalized.Certificate
	}
	updated.UpdatedAt = s.now()

	replacedMaterial := normalized.PrivateKey != ""
	if replacedMaterial {
		inspection, err := s.inspectKey(normalized.PrivateKey, normalized.Passphrase, updated.Certificate)
		if err != nil {
			return domainvault.Key{}, err
		}
		updated.Type = firstKeyType(inspection.Type, normalized.Type)
		updated.PublicKey = firstNonEmpty(inspection.PublicKey, normalized.PublicKey)
		updated.HasPrivateKey = s.SecretMode() != SecretModeSession
	}

	// Snapshot the old secrets so a failed record save cannot leave the Key
	// describing material that is no longer there.
	previousPrivate, privateErr := s.previousSecret(ctx, KeySecretRef(existing.ID, FieldPrivateKey))
	previousPassphrase, passphraseErr := s.previousSecret(ctx, KeySecretRef(existing.ID, FieldPassphrase))
	// ADR-0010: unreadable ciphertext is preserved, never overwritten or
	// dropped. Refuse only the write that would touch it; a metadata-only edit
	// remains possible.
	passphraseTouched := normalized.ClearPassphrase ||
		(normalized.SavePassphrase && normalized.Passphrase != "") || replacedMaterial
	if replacedMaterial && privateErr != nil && !errors.Is(privateErr, ErrSecretNotFound) {
		return domainvault.Key{}, secretError(privateErr)
	}
	if passphraseTouched && passphraseErr != nil && !errors.Is(passphraseErr, ErrSecretNotFound) {
		return domainvault.Key{}, secretError(passphraseErr)
	}

	// A certificate-only edit cannot verify the match by parsing the incoming
	// form alone, so validate it against the stored material when that material
	// can be read without prompting.
	if !replacedMaterial && (normalized.Certificate != "" || normalized.ClearCertificate) && updated.Certificate != "" {
		privateKey, passphrase, materialErr := s.KeyMaterial(ctx, existing.ID)
		switch {
		case materialErr == nil && privateKey != "":
			if _, err := s.inspectKey(privateKey, passphrase, updated.Certificate); err != nil {
				return domainvault.Key{}, err
			}
		case materialErr != nil && !errors.Is(materialErr, ErrNotFound):
			return domainvault.Key{}, materialErr
		}
	}

	if replacedMaterial {
		if err := s.secrets.Set(ctx, KeySecretRef(updated.ID, FieldPrivateKey), normalized.PrivateKey); err != nil {
			return domainvault.Key{}, secretError(err)
		}
	}

	switch {
	case normalized.ClearPassphrase:
		if err := s.deleteSecret(ctx, KeySecretRef(updated.ID, FieldPassphrase)); err != nil {
			return domainvault.Key{}, err
		}
		updated.HasPassphrase = false
	case normalized.SavePassphrase && normalized.Passphrase != "":
		if err := s.secrets.Set(ctx, KeySecretRef(updated.ID, FieldPassphrase), normalized.Passphrase); err != nil {
			return domainvault.Key{}, secretError(err)
		}
		// The passphrase was just stored, so the record must report it even
		// when the existing material had no saved passphrase.
		updated.HasPassphrase = s.SecretMode() != SecretModeSession
	case replacedMaterial:
		// The new material has no saved passphrase. Drop the old one so a
		// mismatched secret cannot break future connections.
		if err := s.deleteSecret(ctx, KeySecretRef(updated.ID, FieldPassphrase)); err != nil {
			return domainvault.Key{}, err
		}
		updated.HasPassphrase = false
	}

	if err := s.keys.SaveKey(ctx, updated); err != nil {
		s.restoreSecret(ctx, KeySecretRef(existing.ID, FieldPrivateKey), previousPrivate, privateErr)
		s.restoreSecret(ctx, KeySecretRef(existing.ID, FieldPassphrase), previousPassphrase, passphraseErr)
		return domainvault.Key{}, internal(err)
	}
	return updated, nil
}

// DeleteKey removes a Key and its encrypted material. Host and Identity
// references are deliberately left dangling, matching the original Vault.
func (s *Service) DeleteKey(ctx context.Context, id model.KeyID) error {
	if s.keys == nil {
		return unsupported(ErrKeyInspectorUnavailable)
	}
	err := s.keys.DeleteKey(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return internal(err)
	}
	if s.secrets != nil {
		_ = s.secrets.Delete(ctx, KeySecretRef(id, FieldPrivateKey))
		_ = s.secrets.Delete(ctx, KeySecretRef(id, FieldPassphrase))
	}
	return nil
}

func (s *Service) requireKeyStorage() error {
	if s.keys == nil || s.secrets == nil {
		return unsupported(ErrSecretsUnavailable)
	}
	return nil
}

func (s *Service) inspectKey(privateKey, passphrase, certificate string) (KeyInspection, error) {
	if s.inspector == nil {
		return KeyInspection{}, unsupported(ErrKeyInspectorUnavailable)
	}
	inspection, err := s.inspector.Inspect(privateKey, passphrase, certificate)
	if err == nil {
		return inspection, nil
	}
	switch {
	case errors.Is(err, ErrKeyPassphraseInvalid):
		return KeyInspection{}, invalid(&domainvault.ValidationError{Field: domainvault.FieldPassphrase, Rule: domainvault.RuleInvalid})
	case errors.Is(err, ErrKeyCertificateInvalid):
		return KeyInspection{}, invalid(&domainvault.ValidationError{Field: domainvault.FieldCertificate, Rule: domainvault.RuleInvalid})
	case errors.Is(err, ErrKeyMaterialUnsupported):
		return KeyInspection{}, invalid(&domainvault.ValidationError{Field: domainvault.FieldKeyMaterial, Rule: domainvault.RuleUnsupported})
	default:
		return KeyInspection{}, invalid(&domainvault.ValidationError{Field: domainvault.FieldKeyMaterial, Rule: domainvault.RuleInvalid})
	}
}

func (s *Service) previousSecret(ctx context.Context, ref SecretRef) (string, error) {
	value, err := s.secrets.Get(ctx, ref)
	if err == nil {
		return value, nil
	}
	if errors.Is(err, ErrSecretNotFound) {
		return "", ErrSecretNotFound
	}
	return "", err
}

func (s *Service) deleteSecret(ctx context.Context, ref SecretRef) error {
	if err := s.secrets.Delete(ctx, ref); err != nil && !errors.Is(err, ErrSecretNotFound) {
		return secretError(err)
	}
	return nil
}

func (s *Service) restoreSecret(ctx context.Context, ref SecretRef, previous string, previousErr error) {
	switch {
	case previousErr == nil:
		_ = s.secrets.Set(ctx, ref, previous)
	case errors.Is(previousErr, ErrSecretNotFound):
		_ = s.secrets.Delete(ctx, ref)
	default:
		// Unreadable ciphertext was never replaced; preserve it untouched.
	}
}

func firstKeyType(primary, fallback domainvault.KeyType) domainvault.KeyType {
	if primary != "" {
		return primary
	}
	return fallback
}

func firstNonEmpty(primary, fallback string) string {
	if primary != "" {
		return primary
	}
	return fallback
}

func nextKeyOrder(keys []domainvault.Key) int64 {
	var max int64
	for _, key := range keys {
		if key.OrderKey > max {
			max = key.OrderKey
		}
	}
	return max + orderStep
}

// KeyMaterial returns a Key's decrypted material for authentication. The
// caller must not persist or log the result.
func (s *Service) KeyMaterial(ctx context.Context, id model.KeyID) (privateKey, passphrase string, err error) {
	if s.secrets == nil {
		return "", "", unsupported(ErrSecretsUnavailable)
	}
	privateKey, err = s.secrets.Get(ctx, KeySecretRef(id, FieldPrivateKey))
	if err != nil {
		if errors.Is(err, ErrSecretNotFound) {
			return "", "", ErrNotFound
		}
		return "", "", secretError(err)
	}
	passphrase, err = s.secrets.Get(ctx, KeySecretRef(id, FieldPassphrase))
	if err != nil {
		if errors.Is(err, ErrSecretNotFound) {
			return privateKey, "", nil
		}
		return "", "", secretError(err)
	}
	return privateKey, passphrase, nil
}
