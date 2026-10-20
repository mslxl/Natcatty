package vault

import (
	"context"
	"errors"

	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// ResolvedKey is an SSH Key with decrypted material for one connection. The
// PrivateKey and Passphrase fields are memory-only; callers must not persist
// or log them.
type ResolvedKey struct {
	ID          model.KeyID
	Label       string
	Type        domainvault.KeyType
	PrivateKey  string
	Passphrase  string
	Certificate string
}

// ResolvedAuth is a Host's authentication selection after Identity, explicit
// Key, Host method, and encrypted secrets are resolved. Password and Key carry
// credential plaintext in memory only.
type ResolvedAuth struct {
	Selection domainvault.AuthSelection
	Password  string
	Key       *ResolvedKey
	// MissingKey reports a selected Key that no longer exists or has no
	// stored private material. The connection fails closed rather than
	// substituting unrelated credentials.
	MissingKey bool
}

// ResolveAuth resolves one Host's authentication material for a connection.
// A dangling Identity or Key reference is preserved as a missing credential,
// matching the Vault's non-cascading delete behavior.
func (s *Service) ResolveAuth(ctx context.Context, hostID model.HostID) (ResolvedAuth, error) {
	if s.repo == nil {
		return ResolvedAuth{}, internal(errors.New("vault: no Host store"))
	}
	host, err := s.EffectiveHost(ctx, hostID)
	if errors.Is(err, ErrNotFound) {
		return ResolvedAuth{}, ErrNotFound
	}
	if err != nil {
		return ResolvedAuth{}, err
	}

	var identity *domainvault.Identity
	if host.IdentityID != "" && s.identities != nil {
		loaded, err := s.identities.GetIdentity(ctx, host.IdentityID)
		switch {
		case err == nil:
			identity = &loaded
		case errors.Is(err, ErrNotFound):
			// Dangling reference: keep it and fail closed if it was the only
			// credential source.
		default:
			return ResolvedAuth{}, internal(err)
		}
	}

	keyID := host.KeyID
	if identity != nil && identity.KeyID != "" {
		keyID = identity.KeyID
	}
	var key *domainvault.Key
	if keyID != "" && s.keys != nil {
		loaded, err := s.keys.GetKey(ctx, keyID)
		switch {
		case err == nil:
			key = &loaded
		case errors.Is(err, ErrNotFound):
		default:
			return ResolvedAuth{}, internal(err)
		}
	}

	selection := domainvault.ResolveHostAuth(host, identity)
	resolved := ResolvedAuth{Selection: selection}

	// The original resolves the Identity password first, then the Host
	// password. An Identity that uses a Key leaves the Host credential as the
	// later password fallback.
	password := ""
	if identity != nil {
		identityPassword, err := s.secretOrEmpty(ctx, IdentitySecretRef(identity.ID, FieldPassword))
		if err != nil {
			return ResolvedAuth{}, err
		}
		password = identityPassword
	}
	if password == "" {
		hostPassword, err := s.secretOrEmpty(ctx, HostSecretRef(host.ID, FieldPassword))
		if err != nil {
			return ResolvedAuth{}, err
		}
		password = hostPassword
	}
	resolved.Password = password

	// Automatic mode also loads a selected Key: it takes its place in the
	// automatic order after the agent. A dangling reference is reported as a
	// missing credential, but automatic mode may still fall back to the other
	// configured methods.
	if selection.KeyID == "" {
		return resolved, nil
	}
	if key == nil {
		resolved.MissingKey = selection.KeyID != ""
		return resolved, nil
	}

	privateKey, passphrase, err := s.KeyMaterial(ctx, key.ID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			resolved.MissingKey = true
			return resolved, nil
		}
		return ResolvedAuth{}, err
	}
	if privateKey == "" {
		resolved.MissingKey = true
		return resolved, nil
	}
	resolved.Key = &ResolvedKey{
		ID:          key.ID,
		Label:       key.Label,
		Type:        key.Type,
		PrivateKey:  privateKey,
		Passphrase:  passphrase,
		Certificate: key.Certificate,
	}
	return resolved, nil
}

// secretOrEmpty reads a credential, treating an absent value as empty. A
// locked or unreadable store stays a typed permission/internal error instead
// of silently connecting without a credential.
func (s *Service) secretOrEmpty(ctx context.Context, ref SecretRef) (string, error) {
	if s.secrets == nil {
		return "", nil
	}
	value, err := s.secrets.Get(ctx, ref)
	if err == nil {
		return value, nil
	}
	if errors.Is(err, ErrSecretNotFound) {
		return "", nil
	}
	return "", secretError(err)
}
