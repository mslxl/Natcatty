package vault

import (
	"context"
	"errors"

	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// IdentityRepository persists Identities. Passwords live in the field-secret
// store, never in these records.
type IdentityRepository interface {
	ListIdentities(ctx context.Context) ([]domainvault.Identity, error)
	GetIdentity(ctx context.Context, id model.IdentityID) (domainvault.Identity, error)
	SaveIdentity(ctx context.Context, identity domainvault.Identity) error
	DeleteIdentity(ctx context.Context, id model.IdentityID) error
}

// ListIdentities returns every Identity in display order.
func (s *Service) ListIdentities(ctx context.Context) ([]domainvault.Identity, error) {
	if s.identities == nil {
		return nil, unsupported(ErrSecretsUnavailable)
	}
	identities, err := s.identities.ListIdentities(ctx)
	if err != nil {
		return nil, internal(err)
	}
	return identities, nil
}

// GetIdentity returns one Identity.
func (s *Service) GetIdentity(ctx context.Context, id model.IdentityID) (domainvault.Identity, error) {
	if s.identities == nil {
		return domainvault.Identity{}, unsupported(ErrSecretsUnavailable)
	}
	identity, err := s.identities.GetIdentity(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return domainvault.Identity{}, ErrNotFound
	}
	if err != nil {
		return domainvault.Identity{}, internal(err)
	}
	return identity, nil
}

// CreateIdentity validates and persists one Identity. A submitted password is
// encrypted into the field-secret store.
func (s *Service) CreateIdentity(ctx context.Context, draft domainvault.IdentityDraft) (domainvault.Identity, error) {
	if s.identities == nil {
		return domainvault.Identity{}, unsupported(ErrSecretsUnavailable)
	}
	normalized, err := domainvault.NormalizeIdentityDraft(draft)
	if err != nil {
		return domainvault.Identity{}, invalid(err)
	}
	identities, err := s.identities.ListIdentities(ctx)
	if err != nil {
		return domainvault.Identity{}, internal(err)
	}

	now := s.now()
	identity := domainvault.Identity{
		ID:          s.newIdentityID(),
		Label:       normalized.Label,
		Username:    normalized.Username,
		AuthMethod:  normalized.AuthMethod,
		KeyID:       normalized.KeyID,
		OrderKey:    nextIdentityOrder(identities),
		HasPassword: normalized.Password != "" && s.SecretMode() != SecretModeSession,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if normalized.Password != "" {
		if s.secrets == nil {
			return domainvault.Identity{}, unsupported(ErrSecretsUnavailable)
		}
		if err := s.secrets.Set(ctx, IdentitySecretRef(identity.ID, FieldPassword), normalized.Password); err != nil {
			return domainvault.Identity{}, secretError(err)
		}
	}
	if err := s.identities.SaveIdentity(ctx, identity); err != nil {
		if identity.HasPassword || normalized.Password != "" {
			_ = s.secrets.Delete(ctx, IdentitySecretRef(identity.ID, FieldPassword))
		}
		return domainvault.Identity{}, internal(err)
	}
	return identity, nil
}

// UpdateIdentity replaces an Identity's editable fields. An empty Password
// keeps the stored credential; ClearPassword removes it.
func (s *Service) UpdateIdentity(ctx context.Context, id model.IdentityID, draft domainvault.IdentityDraft) (domainvault.Identity, error) {
	if s.identities == nil {
		return domainvault.Identity{}, unsupported(ErrSecretsUnavailable)
	}
	existing, err := s.GetIdentity(ctx, id)
	if err != nil {
		return domainvault.Identity{}, err
	}
	normalized, err := domainvault.NormalizeIdentityDraft(draft)
	if err != nil {
		return domainvault.Identity{}, invalid(err)
	}

	updated := existing
	updated.Label = normalized.Label
	updated.Username = normalized.Username
	updated.AuthMethod = normalized.AuthMethod
	updated.KeyID = normalized.KeyID
	updated.UpdatedAt = s.now()

	ref := IdentitySecretRef(id, FieldPassword)
	previous, previousErr := s.previousSecret(ctx, ref)
	// ADR-0010: never overwrite or delete ciphertext this session cannot read.
	// Identity label, username, and method edits remain possible.
	if (normalized.ClearPassword || normalized.Password != "") &&
		previousErr != nil && !errors.Is(previousErr, ErrSecretNotFound) {
		return domainvault.Identity{}, secretError(previousErr)
	}

	switch {
	case normalized.ClearPassword:
		if err := s.deleteSecret(ctx, ref); err != nil {
			return domainvault.Identity{}, err
		}
		updated.HasPassword = false
	case normalized.Password != "":
		if s.secrets == nil {
			return domainvault.Identity{}, unsupported(ErrSecretsUnavailable)
		}
		if err := s.secrets.Set(ctx, ref, normalized.Password); err != nil {
			return domainvault.Identity{}, secretError(err)
		}
		updated.HasPassword = s.SecretMode() != SecretModeSession
	}

	if err := s.identities.SaveIdentity(ctx, updated); err != nil {
		s.restoreSecret(ctx, ref, previous, previousErr)
		return domainvault.Identity{}, internal(err)
	}
	return updated, nil
}

// DeleteIdentity removes an Identity and its encrypted password. Host
// references are deliberately left dangling.
func (s *Service) DeleteIdentity(ctx context.Context, id model.IdentityID) error {
	if s.identities == nil {
		return unsupported(ErrSecretsUnavailable)
	}
	err := s.identities.DeleteIdentity(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return internal(err)
	}
	if s.secrets != nil {
		_ = s.secrets.Delete(ctx, IdentitySecretRef(id, FieldPassword))
	}
	return nil
}

func nextIdentityOrder(identities []domainvault.Identity) int64 {
	var max int64
	for _, identity := range identities {
		if identity.OrderKey > max {
			max = identity.OrderKey
		}
	}
	return max + orderStep
}
