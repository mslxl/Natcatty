package store

import (
	"context"
	"errors"

	historysync "github.com/mslxl/natcatty/application/historysync"
	appvault "github.com/mslxl/natcatty/application/vault"
)

// HistorySecrets adapts the field-secret store to the narrow history-sync
// port. It exposes only the Atuin credential operations; the sync service
// never receives the Vault service or the secret store's key management.
type HistorySecrets struct {
	store *SecretStore
}

// OpenHistorySecrets wraps an open field-secret store for the Atuin account
// and sync services.
func OpenHistorySecrets(store *SecretStore) *HistorySecrets {
	return &HistorySecrets{store: store}
}

var _ historysync.Secrets = (*HistorySecrets)(nil)

// Mode reports how credentials are protected.
func (s *HistorySecrets) Mode() historysync.SecretMode {
	if s == nil || s.store == nil {
		return historysync.SecretModeUnavailable
	}
	switch s.store.Mode() {
	case appvault.SecretModeOS:
		return historysync.SecretModeOS
	case appvault.SecretModePassphraseLocked:
		return historysync.SecretModePassphraseLocked
	case appvault.SecretModePassphrase:
		return historysync.SecretModePassphrase
	case appvault.SecretModeSession:
		return historysync.SecretModeSession
	default:
		return historysync.SecretModeUnavailable
	}
}

// Has reports whether a credential exists without decrypting it. The status
// panel uses it so the master key is not pulled into memory just to show a
// fingerprint.
func (s *HistorySecrets) Has(ctx context.Context, ref historysync.SecretRef) (bool, error) {
	if s == nil || s.store == nil {
		return false, historysync.ErrSecretsUnavailable
	}
	found, err := s.store.hasSecret(ctx, appvault.SecretRef(ref))
	return found, historySecretError(err)
}

// Get decrypts one credential.
func (s *HistorySecrets) Get(ctx context.Context, ref historysync.SecretRef) (string, error) {
	if s == nil || s.store == nil {
		return "", historysync.ErrSecretsUnavailable
	}
	value, err := s.store.Get(ctx, appvault.SecretRef(ref))
	return value, historySecretError(err)
}

// Set encrypts one credential. The plaintext never reaches disk.
func (s *HistorySecrets) Set(ctx context.Context, ref historysync.SecretRef, value string) error {
	if s == nil || s.store == nil {
		return historysync.ErrSecretsUnavailable
	}
	return historySecretError(s.store.Set(ctx, appvault.SecretRef(ref), value))
}

// Delete removes one credential.
func (s *HistorySecrets) Delete(ctx context.Context, ref historysync.SecretRef) error {
	if s == nil || s.store == nil {
		return historysync.ErrSecretsUnavailable
	}
	return historySecretError(s.store.Delete(ctx, appvault.SecretRef(ref)))
}

func historySecretError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, appvault.ErrSecretNotFound):
		return historysync.ErrSecretNotFound
	case errors.Is(err, appvault.ErrSecretUnreadable):
		return historysync.ErrSecretUnreadable
	case errors.Is(err, appvault.ErrSecretsLocked):
		return historysync.ErrSecretsLocked
	case errors.Is(err, appvault.ErrSecretsUnavailable):
		return historysync.ErrSecretsUnavailable
	default:
		return err
	}
}

// hasSecret checks presence without decrypting. It mirrors Get's mode checks
// so a locked store never reports a credential as available.
func (s *SecretStore) hasSecret(ctx context.Context, ref appvault.SecretRef) (bool, error) {
	if err := validateRef(ref); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mode == appvault.SecretModeSession {
		_, ok := s.session[ref]
		return ok, nil
	}
	if err := s.modeError(); err != nil {
		return false, err
	}
	_, found, err := s.db.loadSecretValue(ctx, ref)
	return found, err
}
